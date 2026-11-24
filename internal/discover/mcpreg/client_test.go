package mcpreg_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lusky3/agentsync-tui/internal/discover/cache"
	"github.com/lusky3/agentsync-tui/internal/discover/mcpreg"
	"github.com/lusky3/agentsync-tui/internal/netx"
	"github.com/lusky3/agentsync-tui/internal/netx/netxtest"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

const (
	samplePath  = "testdata/page.json"
	contentType = "Content-Type"
	jsonType    = "application/json"
	searchParam = "search"
	cursorParam = "cursor"
	limitParam  = "limit"
	pageLimit   = "30"
	anyQuery    = "x"
	fsQuery     = "filesystem"
	searchFail  = "Search: %v"

	fsName      = "com.pulsemcp/remote-filesystem"
	weatherName = "io.example/weather"
	argsName    = "io.example/args-demo"
	oldName     = "io.example/old-server"
	sampleNext  = "io.example/old-server:0.9.0"
	cloudflare  = "<!DOCTYPE html><html><title>Attention Required! | Cloudflare</title></html>"
)

// fake is a registry stand-in reachable as mcpreg.Host. It records each
// request's query.
type fake struct {
	mu      sync.Mutex
	queries []url.Values
	paths   []string
}

func (f *fake) hits() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.queries)
}

func (f *fake) requestPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.paths)
}

func (f *fake) last(t *testing.T) url.Values {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.queries) == 0 {
		t.Fatal("no request reached the registry")
	}
	return f.queries[len(f.queries)-1]
}

// newClient starts a TLS registry fake running h and returns a Client that
// reaches it through the production allowlist, with cache c (may be nil).
func newClient(t *testing.T, c *cache.Cache, h http.HandlerFunc) (*mcpreg.Client, *fake) {
	t.Helper()
	f := &fake{}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.queries = append(f.queries, r.URL.Query())
		f.paths = append(f.paths, r.URL.Path)
		f.mu.Unlock()
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	hc := netx.New(netx.Options{
		AllowedHosts: ui.DiscoverHosts(),
		UserAgent:    "agentsync-tui/test",
		Timeout:      10 * time.Second,
		Transport:    netxtest.Transport(srv, mcpreg.Host),
	})
	return mcpreg.NewClient(hc, c), f
}

func serveBytes(body []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(contentType, jsonType)
		_, _ = w.Write(body)
	}
}

func sample(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(samplePath)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// searchSample runs one search against the sample page.
func searchSample(t *testing.T) mcpreg.Page {
	t.Helper()
	c, _ := newClient(t, nil, serveBytes(sample(t)))
	p, err := c.Search(context.Background(), fsQuery, "")
	if err != nil {
		t.Fatalf(searchFail, err)
	}
	return p
}

func find(t *testing.T, p mcpreg.Page, name string) mcpreg.Server {
	t.Helper()
	for _, s := range p.Servers {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("server %q not in page", name)
	return mcpreg.Server{}
}

func env(t *testing.T, vars []mcpreg.EnvVar, name string) mcpreg.EnvVar {
	t.Helper()
	for _, v := range vars {
		if v.Name == name {
			return v
		}
	}
	t.Fatalf("env %q not found in %+v", name, vars)
	return mcpreg.EnvVar{}
}

func TestHostIsAllowlisted(t *testing.T) {
	if !slices.Contains(ui.DiscoverHosts(), mcpreg.Host) {
		t.Fatalf("%s is not in ui.DiscoverHosts()", mcpreg.Host)
	}
}

func TestParsesRealSample(t *testing.T) {
	c, f := newClient(t, nil, serveBytes(sample(t)))
	p, err := c.Search(context.Background(), fsQuery, "")
	if err != nil {
		t.Fatalf(searchFail, err)
	}
	if got := f.requestPaths(); len(got) != 1 || got[0] != "/v0.1/servers" {
		t.Errorf("paths = %q", got)
	}
	q := f.last(t)
	if q.Get(searchParam) != fsQuery || q.Get(limitParam) != pageLimit || q.Get("version") != "latest" || q.Has(cursorParam) {
		t.Errorf("query = %v", q)
	}
	if p.Next != sampleNext {
		t.Errorf("Next = %q", p.Next)
	}
	s := find(t, p, fsName)
	if s.Version != "0.1.5" || s.Status != "active" || s.RepoURL != "https://github.com/pulsemcp/mcp-servers" {
		t.Errorf("server = %+v", s)
	}
	if len(s.Packages) != 1 {
		t.Fatalf("packages = %+v", s.Packages)
	}
	checkFSPackage(t, s.Packages[0])
}

// checkFSPackage asserts the sample's npm package for fsName.
func checkFSPackage(t *testing.T, pk mcpreg.Package) {
	t.Helper()
	if pk.RegistryType != "npm" || pk.Identifier != "remote-filesystem-mcp-server" || pk.Version != "0.1.5" ||
		pk.Transport != "stdio" || pk.RuntimeHint != "npx" {
		t.Errorf("package = %+v", pk)
	}
	if key := env(t, pk.Env, "GCS_PRIVATE_KEY"); !key.Secret || key.Required {
		t.Errorf("GCS_PRIVATE_KEY = %+v", key)
	}
	if bucket := env(t, pk.Env, "GCS_BUCKET"); !bucket.Required || bucket.Secret || bucket.Description == "" {
		t.Errorf("GCS_BUCKET = %+v", bucket)
	}
}

func TestRemoteStreamableHTTP(t *testing.T) {
	s := find(t, searchSample(t), weatherName)
	if len(s.Packages) != 0 || len(s.Remotes) != 1 {
		t.Fatalf("server = %+v", s)
	}
	r := s.Remotes[0]
	if r.Type != "streamable-http" || r.URL != "https://{region}.weather.example/mcp" {
		t.Errorf("remote = %+v", r)
	}
	if len(r.Headers) != 1 {
		t.Fatalf("headers = %+v", r.Headers)
	}
	h := r.Headers[0]
	if h.Name != "Authorization" || h.Value != "Bearer {api_key}" || !h.Required || !h.Secret {
		t.Errorf("header = %+v", h)
	}
	want := []mcpreg.EnvVar{{Name: "api_key", Description: "Your weather API key", Required: true, Secret: true}}
	if !reflect.DeepEqual(h.Variables, want) {
		t.Errorf("header variables = %+v", h.Variables)
	}
}

func TestArgumentsDefaultsAndVariablesDecoded(t *testing.T) {
	p := searchSample(t)
	pk := find(t, p, argsName).Packages[0]
	wantRuntime := []mcpreg.Argument{{Type: "named", Name: "--python", Value: "3.12"}}
	if !reflect.DeepEqual(pk.RuntimeArgs, wantRuntime) {
		t.Errorf("runtime args = %+v", pk.RuntimeArgs)
	}
	wantPkg := []mcpreg.Argument{
		{Type: "positional", Name: "root_dir", Required: true},
		{Type: "named", Name: "--mode", Default: "read-only"},
	}
	if !reflect.DeepEqual(pk.PackageArgs, wantPkg) {
		t.Errorf("package args = %+v", pk.PackageArgs)
	}
	if lvl := env(t, pk.Env, "LOG_LEVEL"); lvl.Default != "info" || lvl.Required || lvl.Value != "" {
		t.Errorf("LOG_LEVEL = %+v", lvl)
	}
	if mode := env(t, pk.Env, "MODE"); mode.Value != "strict" {
		t.Errorf("MODE = %+v, want fixed value strict", mode)
	}
	vars := find(t, p, weatherName).Remotes[0].Variables
	want := []mcpreg.EnvVar{{Name: "region", Description: "Deployment region", Default: "us", Required: true}}
	if !reflect.DeepEqual(vars, want) {
		t.Errorf("remote variables = %+v", vars)
	}
}

func TestDeprecatedStatusKept(t *testing.T) {
	p := searchSample(t)
	if len(p.Servers) != 5 || p.Skipped != 0 {
		t.Fatalf("got %d servers, %d skipped; want all 5 (deprecated ones included)", len(p.Servers), p.Skipped)
	}
	if s := find(t, p, oldName); s.Status != "deprecated" {
		t.Errorf("status = %q", s.Status)
	}
}

// hostile is every way a server string could try to drive the terminal.
const hostile = "\x1b]8;;http://x\x07evil\x1b]8;;\x07 \u202egnp.exe\x00\x1b[31m\u009b2J\r"

// hostileSkeleton is a page in which every string except input names
// (env names, variable keys: a record with a hostile one is skipped, see
// TestUncleanOrCollidingInputNamesSkipEntry) is the placeholder @H@.
const hostileSkeleton = `{
  "servers": [{
    "server": {
      "name": @H@, "description": @H@, "version": @H@, "repository": {"url": @H@},
      "packages": [{
        "registryType": @H@, "identifier": @H@, "version": @H@, "runtimeHint": @H@,
        "transport": {"type": @H@},
        "environmentVariables": [{"name": "PLAIN", "value": @H@, "description": @H@, "default": @H@}],
        "runtimeArguments": [{"type": @H@, "name": @H@, "value": @H@, "default": @H@}],
        "packageArguments": [{"type": @H@, "valueHint": @H@, "isRequired": true}]
      }],
      "remotes": [{
        "type": @H@, "url": @H@,
        "variables": {"plain": {"description": @H@, "default": @H@, "isSecret": true}},
        "headers": [{"name": @H@, "value": @H@, "variables": {"plain": {"description": @H@}}}]
      }]
    },
    "_meta": {"io.modelcontextprotocol.registry/official": {"status": @H@}}
  }],
  "metadata": {"nextCursor": @H@}
}`

func hostilePage(t *testing.T) []byte {
	t.Helper()
	q, err := json.Marshal(hostile)
	if err != nil {
		t.Fatal(err)
	}
	return []byte(strings.ReplaceAll(hostileSkeleton, "@H@", string(q)))
}

func TestSanitizesHostileText(t *testing.T) {
	c, _ := newClient(t, nil, serveBytes(hostilePage(t)))
	p, err := c.Search(context.Background(), anyQuery, "")
	if err != nil {
		t.Fatalf(searchFail, err)
	}
	if len(p.Servers) != 1 || p.Servers[0].Name != "evil gnp.exe" {
		t.Fatalf("name = %q", p.Servers[0].Name)
	}
	// No string field (names, values, defaults, the cursor) may carry a
	// control or format rune.
	all := []rune(fmtAll(p))
	for _, r := range all {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || r == '\u202e' {
			t.Fatalf("unsanitized rune %U in %q", r, string(all))
		}
	}
	if !strings.Contains(string(all), "evil") {
		t.Fatalf("visible text lost: %q", string(all))
	}
}

// fmtAll is every string in p, concatenated.
func fmtAll(p mcpreg.Page) string {
	var sb strings.Builder
	walkStrings(reflect.ValueOf(p), &sb)
	return sb.String()
}

// walkStrings appends every string reachable from v.
func walkStrings(v reflect.Value, sb *strings.Builder) {
	switch v.Kind() {
	case reflect.String:
		sb.WriteString(v.String())
	case reflect.Struct:
		for i := range v.NumField() {
			walkStrings(v.Field(i), sb)
		}
	case reflect.Slice:
		for i := range v.Len() {
			walkStrings(v.Index(i), sb)
		}
	}
}

func TestCloudflareHTMLIsErrBlocked(t *testing.T) {
	c, _ := newClient(t, nil, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(contentType, "text/html; charset=UTF-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(cloudflare))
	})
	_, err := c.Search(context.Background(), anyQuery, "")
	if !errors.Is(err, mcpreg.ErrBlocked) {
		t.Fatalf("err = %v, want ErrBlocked", err)
	}
	var se *netx.StatusError
	if !errors.As(err, &se) || se.Code != http.StatusForbidden {
		t.Errorf("err = %v, want a *netx.StatusError 403 inside", err)
	}
	if strings.Contains(err.Error(), "Cloudflare") {
		t.Errorf("error quotes the body: %v", err)
	}
}

func TestHTMLWith200IsErrBlocked(t *testing.T) {
	c, _ := newClient(t, nil, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(contentType, "text/html")
		_, _ = w.Write([]byte("\n  " + cloudflare))
	})
	_, err := c.Search(context.Background(), anyQuery, "")
	if !errors.Is(err, mcpreg.ErrBlocked) || strings.Contains(err.Error(), "Cloudflare") {
		t.Fatalf("err = %v, want ErrBlocked without the body", err)
	}
}

func TestRateLimitIsErrBlocked(t *testing.T) {
	c, _ := newClient(t, nil, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	_, err := c.Search(context.Background(), anyQuery, "")
	var se *netx.StatusError
	if !errors.Is(err, mcpreg.ErrBlocked) || !errors.As(err, &se) || se.Code != http.StatusTooManyRequests {
		t.Fatalf("err = %v, want ErrBlocked wrapping a 429", err)
	}
}

func TestOtherStatusIsStatusErrorNotBlocked(t *testing.T) {
	c, f := newClient(t, nil, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	})
	_, err := c.Search(context.Background(), anyQuery, "")
	var se *netx.StatusError
	if !errors.As(err, &se) || se.Code != http.StatusBadGateway {
		t.Fatalf("err = %v, want a *netx.StatusError 502", err)
	}
	if errors.Is(err, mcpreg.ErrBlocked) || errors.Is(err, mcpreg.ErrBadResponse) {
		t.Errorf("err = %v, misclassified", err)
	}
	if f.hits() != 1 {
		t.Errorf("%d requests, want exactly 1 (no retry)", f.hits())
	}
}

func TestTruncatedJSONIsErrBadResponse(t *testing.T) {
	full := sample(t)
	c, _ := newClient(t, nil, serveBytes(full[:len(full)/2]))
	_, err := c.Search(context.Background(), anyQuery, "")
	if !errors.Is(err, mcpreg.ErrBadResponse) {
		t.Fatalf("err = %v, want ErrBadResponse", err)
	}
}

func TestNonObjectJSONIsErrBadResponse(t *testing.T) {
	for _, body := range []string{`[]`, `"servers"`, `{"servers": {"a": 1}}`, ``, `{}`, `{"error":"x"}`} {
		c, _ := newClient(t, nil, serveBytes([]byte(body)))
		if _, err := c.Search(context.Background(), anyQuery, ""); !errors.Is(err, mcpreg.ErrBadResponse) {
			t.Errorf("body %q: err = %v, want ErrBadResponse", body, err)
		}
	}
}

func TestEmptyServerListIsAnEmptyPage(t *testing.T) {
	for _, body := range []string{`{"servers": []}`, `{"servers": null}`} {
		c, _ := newClient(t, nil, serveBytes([]byte(body)))
		p, err := c.Search(context.Background(), anyQuery, "")
		if err != nil || len(p.Servers) != 0 || p.Skipped != 0 {
			t.Errorf("body %q: page = %+v, err = %v; want an empty page", body, p, err)
		}
	}
}

// Records for the per-entry decode tests.
const (
	goodEntry     = `{"server": {"name": "io.example/good", "packages": [{"registryType": "npm", "identifier": "good", "transport": {"type": "stdio"}}]}}`
	badTransport  = `{"server": {"name": "io.example/bad1", "packages": [{"registryType": "npm", "identifier": "bad", "transport": "stdio"}]}}`
	badSecretFlag = `{"server": {"name": "io.example/bad2", "packages": [{"registryType": "npm", "identifier": "bad", "transport": {"type": "stdio"},
		"environmentVariables": [{"name": "API_KEY", "isSecret": "true"}]}]}}`
)

func pageOf(entries ...string) []byte {
	return []byte(`{"servers": [` + strings.Join(entries, ",") + `], "metadata": {}}`)
}

func TestOneMalformedEntrySkippedNotWholePage(t *testing.T) {
	c, _ := newClient(t, nil, serveBytes(pageOf(badTransport, goodEntry, badSecretFlag)))
	p, err := c.Search(context.Background(), anyQuery, "")
	if err != nil {
		t.Fatalf(searchFail, err)
	}
	if len(p.Servers) != 1 || p.Servers[0].Name != "io.example/good" || p.Skipped != 2 {
		t.Errorf("page = %+v, want only the good record and Skipped 2", p)
	}
}

func TestAllEntriesMalformedIsErrBadResponse(t *testing.T) {
	c, _ := newClient(t, nil, serveBytes(pageOf(badTransport, badSecretFlag)))
	if _, err := c.Search(context.Background(), anyQuery, ""); !errors.Is(err, mcpreg.ErrBadResponse) {
		t.Fatalf("err = %v, want ErrBadResponse", err)
	}
}

func TestUncleanOrCollidingInputNamesSkipEntry(t *testing.T) {
	cases := map[string]string{
		// "api\u0001_key" cleans to "api_key": its plain default must not
		// stand in for the secret.
		"remote variables": `{"server": {"name": "io.example/r", "remotes": [{"type": "sse", "url": "https://r.example/{api_key}",
			"variables": {"api_key": {"isSecret": true}, "api\u0001_key": {"default": "x"}}}]}}`,
		"header variables": `{"server": {"name": "io.example/h", "remotes": [{"type": "sse", "url": "https://h.example",
			"headers": [{"name": "Authorization", "value": "{t}", "variables": {"t\u202e": {}}}]}]}}`,
		"env names": `{"server": {"name": "io.example/e", "packages": [{"registryType": "npm", "identifier": "e", "transport": {"type": "stdio"},
			"environmentVariables": [{"name": "API_KEY", "isSecret": true}, {"name": "API_KEY", "default": "x"}]}]}}`,
	}
	for name, entry := range cases {
		t.Run(name, func(t *testing.T) {
			c, _ := newClient(t, nil, serveBytes(pageOf(entry, goodEntry)))
			p, err := c.Search(context.Background(), anyQuery, "")
			if err != nil || len(p.Servers) != 1 || p.Skipped != 1 {
				t.Errorf("page = %+v, err = %v; want the entry skipped", p, err)
			}
		})
	}
}

func TestUnknownRegistryTypeKept(t *testing.T) {
	pk := find(t, searchSample(t), "io.example/bundle").Packages[0]
	if pk.RegistryType != "mcpb" || pk.Transport != "stdio" {
		t.Errorf("package = %+v, want registryType mcpb kept as is", pk)
	}
}

func TestRepeatingCursorStopsPagination(t *testing.T) {
	const cur = "io.example/loop:1.0.0"
	body := []byte(`{"servers": [], "metadata": {"nextCursor": "` + cur + `"}}`)
	c, f := newClient(t, nil, serveBytes(body))
	first, err := c.Search(context.Background(), anyQuery, "")
	if err != nil || first.Next != cur {
		t.Fatalf("first page Next = %q, err = %v", first.Next, err)
	}
	again, err := c.Search(context.Background(), anyQuery, first.Next)
	if err != nil {
		t.Fatalf(searchFail, err)
	}
	if got := f.last(t).Get(cursorParam); got != cur {
		t.Errorf("cursor sent = %q", got)
	}
	if again.Next != "" {
		t.Errorf("Next = %q after the registry repeated the cursor, want \"\"", again.Next)
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

func TestCachedWithin10Minutes(t *testing.T) {
	dc := openCache(t)
	c, f := newClient(t, dc, serveBytes(sample(t)))
	ctx := context.Background()
	first, err := c.Search(ctx, fsQuery, "")
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Search(ctx, fsQuery, "")
	if err != nil {
		t.Fatal(err)
	}
	if f.hits() != 1 {
		t.Fatalf("%d requests for two identical searches, want 1", f.hits())
	}
	if !reflect.DeepEqual(first, second) {
		t.Errorf("cached page differs")
	}
	key := "mcpreg:https://" + mcpreg.Host + "/v0.1/servers?limit=30&search=" + fsQuery + "&version=latest"
	if _, ok := dc.Get(key, time.Minute); !ok {
		t.Errorf("no entry under the source-namespaced key %q", key)
	}
	if _, err := c.Search(ctx, fsQuery, sampleNext); err != nil {
		t.Fatal(err)
	}
	if f.hits() != 2 {
		t.Errorf("a different cursor was served from cache")
	}
}

func TestFailedResponseNotCached(t *testing.T) {
	good := sample(t)
	var n int
	var mu sync.Mutex
	c, f := newClient(t, openCache(t), func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		n++
		bad := n == 1
		mu.Unlock()
		if bad {
			_, _ = w.Write(good[:10])
			return
		}
		_, _ = w.Write(good)
	})
	ctx := context.Background()
	if _, err := c.Search(ctx, anyQuery, ""); !errors.Is(err, mcpreg.ErrBadResponse) {
		t.Fatalf("first: %v", err)
	}
	for range 2 {
		if _, err := c.Search(ctx, anyQuery, ""); err != nil {
			t.Fatalf(searchFail, err)
		}
	}
	if f.hits() != 2 {
		t.Errorf("%d requests, want 2 (bad not cached, good cached)", f.hits())
	}
}

func TestQueryIsURLEscaped(t *testing.T) {
	const q = "a b&limit=1#frag/é?cursor=x"
	c, f := newClient(t, nil, serveBytes(sample(t)))
	if _, err := c.Search(context.Background(), q, ""); err != nil {
		t.Fatal(err)
	}
	got := f.last(t)
	if got.Get(searchParam) != q || got.Get(limitParam) != pageLimit || got.Has(cursorParam) || len(got[limitParam]) != 1 {
		t.Errorf("query = %v", got)
	}
}

func TestEmptyQueryOmitsSearch(t *testing.T) {
	c, f := newClient(t, nil, serveBytes(sample(t)))
	if _, err := c.Search(context.Background(), "", ""); err != nil {
		t.Fatal(err)
	}
	if f.last(t).Has(searchParam) {
		t.Errorf("query = %v, want no search parameter", f.last(t))
	}
}

func TestNilHTTPIsAnErrorNotAPanic(t *testing.T) {
	_, err := mcpreg.NewClient(nil, nil).Search(context.Background(), anyQuery, "")
	if err == nil {
		t.Fatal("Search with no HTTP client succeeded")
	}
}
