package dockercat_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lusky3/agentsync-tui/internal/discover/cache"
	"github.com/lusky3/agentsync-tui/internal/discover/dockercat"
	"github.com/lusky3/agentsync-tui/internal/discover/mcpsrc"
	"github.com/lusky3/agentsync-tui/internal/netx"
	"github.com/lusky3/agentsync-tui/internal/netx/netxtest"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

const (
	catalogPath = "testdata/catalog.yaml"
	searchFail  = "Search: %v"
	errWant     = "err = %v, want %v"
	keyBrave    = "brave"
	keyContext7 = "context7"
	keyStripe   = "stripe"
	keyTime     = "time"
	keyFS       = "filesystem"
	keyCurl     = "curl"
	keyLinear   = "linear"
	cacheKey    = "dockercat:" + dockercat.CatalogURL
	keyCommand  = "command"
	keyOK       = "ok"
)

// fake counts requests to the catalog stand-in.
type fake struct {
	mu   sync.Mutex
	n    int
	path string
}

func (f *fake) hits() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

// newClient starts a TLS stand-in for the catalog host running h and
// returns a Client reaching it through the production allowlist.
func newClient(t *testing.T, c *cache.Cache, h http.HandlerFunc) (*dockercat.Client, *fake) {
	t.Helper()
	f := &fake{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.n++
		f.path = r.URL.Path
		f.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	hc := netx.New(netx.Options{
		AllowedHosts: ui.DiscoverHosts(),
		UserAgent:    "agentsync-tui/test",
		Timeout:      10 * time.Second,
		Transport:    netxtest.Transport(srv, dockercat.Host),
	})
	return dockercat.NewClient(hc, c), f
}

func serveBytes(body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-yaml")
		_, _ = w.Write(body)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// searchBody runs one search for query over a catalog body.
func searchBody(t *testing.T, body []byte, query string) mcpsrc.Page {
	t.Helper()
	c, _ := newClient(t, nil, serveBytes(body))
	p, err := c.Search(context.Background(), query, "")
	if err != nil {
		t.Fatalf(searchFail, err)
	}
	return p
}

func searchSample(t *testing.T) mcpsrc.Page {
	t.Helper()
	return searchBody(t, readFile(t, catalogPath), "")
}

func find(t *testing.T, p mcpsrc.Page, key string) mcpsrc.Entry {
	t.Helper()
	for _, e := range p.Entries {
		if e.ID == key {
			return e
		}
	}
	t.Fatalf("no entry %q in %d entries", key, len(p.Entries))
	return mcpsrc.Entry{}
}

func keys(p mcpsrc.Page) []string {
	var out []string
	for _, e := range p.Entries {
		out = append(out, e.ID)
	}
	return out
}

func TestParsesTrimmedRealCatalog(t *testing.T) {
	c, f := newClient(t, nil, serveBytes(readFile(t, catalogPath)))
	p, err := c.Search(context.Background(), "", "")
	if err != nil {
		t.Fatalf(searchFail, err)
	}
	if f.path != "/mcp/catalog/v3/catalog.yaml" {
		t.Errorf("requested %q", f.path)
	}
	want := []string{keyBrave, keyContext7, keyCurl, keyFS, keyLinear, keyStripe, keyTime}
	if got := keys(p); !slices.Equal(got, want) {
		t.Fatalf("keys = %q, want %q", got, want)
	}
	if p.Next != "" || p.Skipped != 0 {
		t.Errorf("Next = %q, Skipped = %d", p.Next, p.Skipped)
	}
	installable := map[string]bool{keyBrave: true, keyContext7: true, keyStripe: true, keyTime: true}
	for _, e := range p.Entries {
		if e.Source != dockercat.Name || e.Deprecated || len(e.Installs) != 1 {
			t.Errorf("%s: source %q, deprecated %v, %d installs", e.ID, e.Source, e.Deprecated, len(e.Installs))
			continue
		}
		in := e.Installs[0]
		if (in.Draft != nil) != installable[e.ID] || (in.Draft == nil) == (in.Unsupported == "") {
			t.Errorf("%s: draft %v, unsupported %q", e.ID, in.Draft != nil, in.Unsupported)
		}
	}
	if got := find(t, p, keyBrave).Title; got != "Brave Search" {
		t.Errorf("title = %q", got)
	}
}

func TestSourceNameStable(t *testing.T) {
	c := dockercat.NewClient(nil, nil)
	var s mcpsrc.Source = c
	if s.Name() != "docker" {
		t.Errorf("Name = %q", s.Name())
	}
}

// generated is a catalog of n remote entries keyed e000, e001, ...; every
// third one mentions "Needle" in its description.
func generated(n int) []byte {
	var b bytes.Buffer
	b.WriteString("registry:\n")
	for i := n - 1; i >= 0; i-- { // reverse order: Search must sort
		desc := "plain"
		if i%3 == 0 {
			desc = "has a Needle"
		}
		fmt.Fprintf(&b, "  e%03d:\n    type: remote\n    description: %s\n    remote:\n      transport_type: sse\n      url: https://e%d.example.com/sse\n", i, desc, i)
	}
	return b.Bytes()
}

func TestSearchFiltersSortsAndPages(t *testing.T) {
	c, _ := newClient(t, nil, serveBytes(generated(65)))
	ctx := context.Background()
	var all []string
	cursor := ""
	for range 3 {
		p, err := c.Search(ctx, "", cursor)
		if err != nil {
			t.Fatalf(searchFail, err)
		}
		all = append(all, keys(p)...)
		cursor = p.Next
	}
	if cursor != "" || len(all) != 65 || !slices.IsSorted(all) || all[0] != "e000" {
		t.Fatalf("paged %d keys (sorted %v), last cursor %q", len(all), slices.IsSorted(all), cursor)
	}
	p, err := c.Search(ctx, "nEEDLE", "")
	if err != nil {
		t.Fatalf(searchFail, err)
	}
	if len(p.Entries) != 22 || p.Next != "" {
		t.Errorf("query matched %d entries, Next %q; want 22, none", len(p.Entries), p.Next)
	}
	if p, _ := c.Search(ctx, "E064", ""); len(p.Entries) != 1 {
		t.Errorf("key search matched %d", len(p.Entries))
	}
	for _, bad := range []string{"abc", "-1", "030", "0", "999"} {
		if _, err := c.Search(ctx, "", bad); !errors.Is(err, dockercat.ErrBadCursor) {
			t.Errorf("cursor %q: "+errWant, bad, err, dockercat.ErrBadCursor)
		}
	}
}

func openCache(t *testing.T) *cache.Cache {
	t.Helper()
	c, err := cache.Open(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestCachedForOneHour(t *testing.T) {
	dc := openCache(t)
	c, f := newClient(t, dc, serveBytes(readFile(t, catalogPath)))
	ctx := context.Background()
	first, err := c.Search(ctx, "", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Search(ctx, "stri", "")
	if err != nil {
		t.Fatal(err)
	}
	if f.hits() != 1 {
		t.Fatalf("%d downloads for two searches, want 1", f.hits())
	}
	if len(second.Entries) != 1 || !reflect.DeepEqual(second.Entries[0], find(t, first, keyStripe)) {
		t.Errorf("cached search differs")
	}
	if _, ok := dc.Get(cacheKey, 59*time.Minute); !ok {
		t.Error("no entry under the source-namespaced key")
	}
}

func TestBadCatalogNotCached(t *testing.T) {
	good := readFile(t, catalogPath)
	var mu sync.Mutex
	n := 0
	c, f := newClient(t, openCache(t), func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		n++
		first := n == 1
		mu.Unlock()
		if first {
			_, _ = w.Write([]byte("registry: [unclosed"))
			return
		}
		_, _ = w.Write(good)
	})
	ctx := context.Background()
	if _, err := c.Search(ctx, "", ""); !errors.Is(err, dockercat.ErrBadResponse) {
		t.Fatalf(errWant, err, dockercat.ErrBadResponse)
	}
	for range 2 {
		if _, err := c.Search(ctx, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if f.hits() != 2 {
		t.Errorf("%d downloads, want 2 (bad one not cached, good one cached)", f.hits())
	}
}

func TestNilCacheGoesToNetwork(t *testing.T) {
	c, f := newClient(t, nil, serveBytes(readFile(t, catalogPath)))
	for range 2 {
		if _, err := c.Search(context.Background(), "", ""); err != nil {
			t.Fatal(err)
		}
	}
	if f.hits() != 2 {
		t.Errorf("%d downloads with no cache, want 2", f.hits())
	}
}

func TestOfflineClientErrs(t *testing.T) {
	if _, err := dockercat.NewClient(nil, nil).Search(context.Background(), "", ""); err == nil {
		t.Fatal("a client without HTTP answered with no cache")
	}
}

const hostileYAML = `registry:
  evil:
    type: remote
    title: "Evil\e]8;;http://x\aClick\e]8;;\a\u202Egnp.exe\0"
    description: "line\e[31m red\u200B"
    remote:
      transport_type: sse
      url: https://evil.example.com/sse
  names:
    type: server
    image: mcp/x@sha256:` + digest64 + `
    secrets:
      - name: x
        env: "API\u200B_KEY"
  image:
    type: server
    image: "mcp/x\e[0m@sha256:` + digest64 + `"
  command:
    type: server
    image: mcp/x@sha256:` + digest64 + `
    command: ["--tools=all\u200B"]
  "esc\e]8;;http://x\akey":
    type: poci
  "bidi\u202Ekey":
    type: poci
  "nul\0key":
    type: poci
`

const digest64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestSanitizesHostileText(t *testing.T) {
	p := searchBody(t, []byte(hostileYAML), "")
	e := find(t, p, "evil")
	for _, s := range []string{e.Title, e.Description} {
		if strings.ContainsAny(s, "\x1b\x07\x00\u202e\u200b") {
			t.Errorf("unsanitized text %q", s)
		}
	}
	if !strings.Contains(e.Title, "Click") {
		t.Errorf("title lost its text: %q", e.Title)
	}
	for _, key := range []string{"names", "image", keyCommand} {
		in := find(t, p, key).Installs[0]
		if in.Draft != nil || in.Unsupported != dockercat.ReasonNames {
			t.Errorf("%s changed by sanitizing: %+v", key, in)
		}
	}
	if got := keys(p); !slices.Equal(got, []string{keyCommand, "evil", "image", "names"}) || p.Skipped != 3 {
		t.Errorf("keys %q, skipped %d; want the three hostile keys skipped", got, p.Skipped)
	}
}

func TestDuplicateKeyKeepsFirst(t *testing.T) {
	body := "registry:\n  dup:\n    type: poci\n    title: first\n  dup:\n    type: poci\n    title: second\n"
	p := searchBody(t, []byte(body), "")
	if len(p.Entries) != 1 || p.Entries[0].Title != "first" || p.Skipped != 1 {
		t.Errorf("entries %+v, skipped %d; want the first dup and one skipped", p.Entries, p.Skipped)
	}
}

// timed fails t when f takes over a second (timeScale seconds under the
// race detector).
func timed(t *testing.T, f func()) {
	t.Helper()
	start := time.Now()
	f()
	if d := time.Since(start); d > timeScale*time.Second {
		t.Errorf("took %v", d)
	}
}

func TestWideMappingInEntrySkippedQuickly(t *testing.T) {
	var b strings.Builder
	b.WriteString("registry:\n  ok:\n    type: poci\n  wide:\n    type: remote\n    remote:\n      headers:\n")
	for i := range 10000 {
		fmt.Fprintf(&b, "        h%d: v\n", i)
	}
	timed(t, func() {
		p := searchBody(t, []byte(b.String()), "")
		if got := keys(p); !slices.Equal(got, []string{keyOK}) || p.Skipped != 1 {
			t.Errorf("keys %q, skipped %d; want the wide entry skipped", got, p.Skipped)
		}
	})
}

func TestHugeRegistryRefusedQuickly(t *testing.T) {
	var b strings.Builder
	b.WriteString("registry:\n")
	for i := range 50000 { // under 2 MiB, ten times the entry cap
		fmt.Fprintf(&b, "  k%d: {type: poci}\n", i)
	}
	c, _ := newClient(t, nil, serveBytes([]byte(b.String())))
	timed(t, func() {
		if _, err := c.Search(context.Background(), "", ""); !errors.Is(err, dockercat.ErrBadResponse) {
			t.Errorf(errWant, err, dockercat.ErrBadResponse)
		}
	})
}

func TestCanceledContextStopsCachedDecode(t *testing.T) {
	dc := openCache(t)
	if err := dc.Put(cacheKey, readFile(t, catalogPath)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := dockercat.NewClient(nil, dc).Search(ctx, "", ""); !errors.Is(err, context.Canceled) {
		t.Errorf(errWant, err, context.Canceled)
	}
}

func TestOversizeCachedCatalogNotServed(t *testing.T) {
	dc := openCache(t)
	big := append(readFile(t, catalogPath), '#')
	big = append(big, bytes.Repeat([]byte("x"), int(dockercat.MaxBody))...)
	if err := dc.Put(cacheKey, big); err != nil {
		t.Fatal(err)
	}
	if p, err := dockercat.NewClient(nil, dc).Search(context.Background(), "", ""); err == nil {
		t.Errorf("an oversize cached catalog was served: %d entries", len(p.Entries))
	}
}

func TestAliasBombRejectedQuickly(t *testing.T) {
	start := time.Now()
	c, _ := newClient(t, nil, serveBytes(readFile(t, "testdata/alias_bomb.yaml")))
	_, err := c.Search(context.Background(), "", "")
	if !errors.Is(err, dockercat.ErrBadResponse) {
		t.Fatalf(errWant, err, dockercat.ErrBadResponse)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Errorf("alias bomb took %v", d)
	}
}

func TestOversizeCatalogRefused(t *testing.T) {
	big := append([]byte("registry: {}\n#"), bytes.Repeat([]byte("x"), int(dockercat.MaxBody))...)
	c, _ := newClient(t, openCache(t), serveBytes(big))
	if _, err := c.Search(context.Background(), "", ""); !errors.Is(err, netx.ErrTooLarge) {
		t.Fatalf(errWant, err, netx.ErrTooLarge)
	}
}

func TestHTMLInsteadOfYAMLIsErrBlocked(t *testing.T) {
	c, _ := newClient(t, nil, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("\n<!DOCTYPE html><title>Just a moment...</title>"))
	})
	if _, err := c.Search(context.Background(), "", ""); !errors.Is(err, dockercat.ErrBlocked) {
		t.Fatalf(errWant, err, dockercat.ErrBlocked)
	}
}

func TestRefusalStatusIsErrBlocked(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests} {
		c, _ := newClient(t, nil, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(code) })
		_, err := c.Search(context.Background(), "", "")
		var se *netx.StatusError
		if !errors.Is(err, dockercat.ErrBlocked) || !errors.As(err, &se) || se.Code != code {
			t.Errorf("%d: err = %v", code, err)
		}
	}
	c, _ := newClient(t, nil, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) })
	if _, err := c.Search(context.Background(), "", ""); errors.Is(err, dockercat.ErrBlocked) || !errors.Is(err, netx.ErrStatus) {
		t.Errorf("502: err = %v", err)
	}
}

func TestBadEnvelopeIsErrBadResponse(t *testing.T) {
	for name, body := range map[string]string{
		"not yaml":      "registry: [unclosed",
		"no registry":   "version: 3\n",
		"list registry": "registry: [a, b]\n",
		"scalar doc":    "just text\n",
		"all bad":       "registry:\n  a: 1\n  b:\n    disableNetwork: maybe\n",
		"empty":         "",
	} {
		c, _ := newClient(t, nil, serveBytes([]byte(body)))
		if _, err := c.Search(context.Background(), "", ""); !errors.Is(err, dockercat.ErrBadResponse) {
			t.Errorf("%s: "+errWant, name, err, dockercat.ErrBadResponse)
		}
	}
}

func TestNullOrEmptyRegistryIsEmptyPage(t *testing.T) {
	for _, body := range []string{"registry:\n", "registry: {}\n"} {
		p := searchBody(t, []byte(body), "")
		if len(p.Entries) != 0 || p.Skipped != 0 {
			t.Errorf("%q: %+v", body, p)
		}
	}
}

func TestOneBadEntryDoesNotBlankCatalog(t *testing.T) {
	body := append(readFile(t, catalogPath), []byte(`  zz-bad:
    type: server
    disableNetwork: "maybe"
  zz-list: [1, 2]
  zz-alias:
    type: server
    title: *nothing
`)...)
	// *nothing is an undefined alias, a YAML error for the whole document;
	// drop it and use a defined one instead.
	body = bytes.Replace(body, []byte("*nothing"), []byte("*x"), 1)
	body = bytes.Replace(body, []byte("displayName: Docker MCP Catalog"), []byte("displayName: &x Docker MCP Catalog"), 1)
	p := searchBody(t, body, "")
	if len(p.Entries) != 7 || p.Skipped != 3 {
		t.Fatalf("%d entries, %d skipped; want 7, 3", len(p.Entries), p.Skipped)
	}
	if p2 := searchBody(t, body, keyBrave); p2.Skipped != 3 {
		t.Errorf("Skipped = %d on a filtered first page", p2.Skipped)
	}
}

func TestSkippedOnlyOnFirstPage(t *testing.T) {
	body := append(generated(40), []byte("  zz: [1]\n")...)
	c, _ := newClient(t, nil, serveBytes(body))
	first, err := c.Search(context.Background(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Search(context.Background(), "", first.Next)
	if err != nil {
		t.Fatal(err)
	}
	if first.Skipped != 1 || second.Skipped != 0 || len(second.Entries) != 10 {
		t.Errorf("skipped %d then %d, second page %d entries", first.Skipped, second.Skipped, len(second.Entries))
	}
}

// entryWith is a one-entry catalog whose remote entry ends with extra.
func entryWith(extra string) []byte {
	return []byte("registry:\n  ok:\n    type: poci\n  e:\n    type: remote\n" + extra)
}

func TestEntryBudget(t *testing.T) {
	var pairs128, pairs129, nodes strings.Builder
	for i := range 129 {
		if i < 128 {
			fmt.Fprintf(&pairs128, "      h%d: v\n", i)
		}
		fmt.Fprintf(&pairs129, "      h%d: v\n", i)
	}
	for i := range 10000 {
		fmt.Fprintf(&nodes, "      - t%d\n", i)
	}
	for name, tc := range map[string]struct {
		extra string
		kept  bool
	}{
		"128 pairs":    {"    remote:\n      transport_type: sse\n    headers2:\n" + pairs128.String(), true},
		"129 pairs":    {"    headers2:\n" + pairs129.String(), false},
		"10000 nodes":  {"    tools:\n" + nodes.String(), false},
		"small entry":  {"    tools:\n      - a\n", true},
		"alias inside": {"    title: &t x\n    description: *t\n", false},
	} {
		p := searchBody(t, entryWith(tc.extra), "")
		if kept := len(p.Entries) == 2; kept != tc.kept || p.Skipped != boolInt(!tc.kept) {
			t.Errorf("%s: kept %v (skipped %d), want %v", name, kept, p.Skipped, tc.kept)
		}
	}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func TestDuplicateKeyEntriesSkippedBeforeDecode(t *testing.T) {
	var dup strings.Builder
	for range 128 {
		dup.WriteString("a: x, ")
	}
	var b strings.Builder
	b.WriteString("registry:\n  ok:\n    type: poci\n")
	for i := range 400 {
		fmt.Fprintf(&b, "  d%d:\n    type: server\n    env: [{%s}, {%s}]\n", i, dup.String(), dup.String())
	}
	p := searchBody(t, []byte(b.String()), "")
	if got := keys(p); !slices.Equal(got, []string{keyOK}) || p.Skipped != 400 {
		t.Errorf("keys %q, skipped %d; want 400 skipped", got, p.Skipped)
	}
}

func TestMergeKeyEntriesSkipped(t *testing.T) {
	for name, extra := range map[string]string{
		"merge key":  "    <<: {title: merged}\n",
		"merge list": "    remote: {<<: [{url: 'https://a.example.com'}]}\n",
		"merge tag":  "    !!merge m: {title: merged}\n",
	} {
		p := searchBody(t, entryWith(extra), "")
		if got := keys(p); !slices.Equal(got, []string{keyOK}) || p.Skipped != 1 {
			t.Errorf("%s: keys %q, skipped %d; want the merge entry skipped", name, got, p.Skipped)
		}
	}
}

func TestCatalogCapIsTwoMiB(t *testing.T) {
	if dockercat.MaxBody != 2<<20 {
		t.Errorf("MaxBody = %d, want 2 MiB", dockercat.MaxBody)
	}
}
