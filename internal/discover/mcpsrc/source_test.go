package mcpsrc_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/discover/mcpreg"
	"github.com/lusky3/agentsync-tui/internal/discover/mcpsrc"
	"github.com/lusky3/agentsync-tui/internal/netx"
	"github.com/lusky3/agentsync-tui/internal/netx/netxtest"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

const (
	searchFail = "Search: %v"
	anyQuery   = "x"

	goodName  = "io.example/good-server"
	ociName   = "io.example/image"
	evilName  = "io.example/evil"
	noIDName  = "io.example/---"
	nugetName = "io.example/dotnet"
	oldName   = "io.example/old"
	goneName  = "io.example/gone"

	trustMutable = "image tag is mutable (not digest-pinned)"
)

// Records for the adapter tests; entry wraps a server object and status.
const (
	goodServer = `{"name": "` + goodName + `", "description": "Good\tone", "version": "1.0.0",
		"packages": [{"registryType": "npm", "identifier": "good-server", "version": "1.0.0", "transport": {"type": "stdio"},
			"environmentVariables": [{"name": "API_KEY", "isSecret": true}]}],
		"remotes": [{"type": "sse", "url": "https://good.example/sse"}]}`
	ociServer = `{"name": "` + ociName + `", "version": "1.2",
		"packages": [{"registryType": "oci", "identifier": "ghcr.io/example/image", "version": "1.2", "transport": {"type": "stdio"}}]}`
	evilServer = `{"name": "` + evilName + `",
		"packages": [{"registryType": "npm", "identifier": "--registry=evil", "version": "1.0.0", "transport": {"type": "stdio"}}]}`
	noIDServer = `{"name": "` + noIDName + `",
		"packages": [{"registryType": "npm", "identifier": "fine", "version": "1.0.0", "transport": {"type": "stdio"}},
			{"registryType": "pypi", "identifier": "fine", "version": "1.0.0", "transport": {"type": "stdio"}}]}`
	nugetServer = `{"name": "` + nugetName + `",
		"packages": [{"registryType": "nuget", "identifier": "Dotnet.Server", "version": "1.0.0", "transport": {"type": "stdio"}}]}`
)

func entry(server, status string) string {
	if status == "" {
		return `{"server": ` + server + `}`
	}
	return `{"server": ` + server + `, "_meta": {"io.modelcontextprotocol.registry/official": {"status": "` + status + `"}}}`
}

func statusServer(name string) string {
	return `{"name": "` + name + `", "remotes": [{"type": "sse", "url": "https://x.example/sse"}]}`
}

func pageOf(next string, entries ...string) string {
	return `{"servers": [` + strings.Join(entries, ",") + `], "metadata": {"nextCursor": "` + next + `"}}`
}

// newClient serves h as the registry and returns a client reaching it
// through the production allowlist.
func newClient(t *testing.T, h http.HandlerFunc) *mcpreg.Client {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	t.Cleanup(srv.Close)
	hc := netx.New(netx.Options{
		AllowedHosts: ui.DiscoverHosts(),
		UserAgent:    "agentsync-tui/test",
		Timeout:      10 * time.Second,
		Transport:    netxtest.Transport(srv, mcpreg.Host),
	})
	return mcpreg.NewClient(hc, nil)
}

func serve(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
}

// search runs one official search over a registry serving body.
func search(t *testing.T, body string) mcpsrc.Page {
	t.Helper()
	p, err := mcpsrc.Official(newClient(t, serve(body))).Search(context.Background(), anyQuery, "")
	if err != nil {
		t.Fatalf(searchFail, err)
	}
	return p
}

func find(t *testing.T, p mcpsrc.Page, id string) mcpsrc.Entry {
	t.Helper()
	for _, e := range p.Entries {
		if e.ID == id {
			return e
		}
	}
	t.Fatalf("entry %q not in page", id)
	return mcpsrc.Entry{}
}

func TestSourceNameStable(t *testing.T) {
	if got := mcpsrc.Official(nil).Name(); got != "official" {
		t.Fatalf("Name() = %q, want official", got)
	}
}

func TestOfficialAdapterMapsEntries(t *testing.T) {
	p := search(t, pageOf("next-1", entry(goodServer, "active"), entry(ociServer, "")))
	if len(p.Entries) != 2 || p.Next != "next-1" || p.Skipped != 0 {
		t.Fatalf("page = %+v, want 2 entries, Next next-1", p)
	}
	e := find(t, p, goodName)
	if e.Source != "official" || e.Title != goodName || e.Description != "Good\tone" || e.Deprecated {
		t.Errorf("entry = %+v", e)
	}
	if len(e.Installs) != 2 {
		t.Fatalf("installs = %+v, want npm and sse", e.Installs)
	}
	for _, in := range e.Installs {
		checkInstallable(t, in)
	}
	npm := e.Installs[0].Draft.Spec
	if npm.Command != "npx" || !slices.Equal(npm.Args, []string{"-y", "good-server@1.0.0"}) {
		t.Errorf("npm spec = %+v", npm)
	}
	if npm.Env["API_KEY"] != "${secret:good-server.api_key}" {
		t.Errorf("env = %v, want a reference namespaced by the suggested id", npm.Env)
	}
	if !strings.HasPrefix(e.Installs[0].Label, "npm ") || !strings.HasPrefix(e.Installs[1].Label, "sse ") {
		t.Errorf("labels = %q, %q", e.Installs[0].Label, e.Installs[1].Label)
	}
}

// checkInstallable asserts in is a draft for the suggested id that the
// add command accepts.
func checkInstallable(t *testing.T, in mcpsrc.Install) {
	t.Helper()
	if in.Draft == nil || in.Unsupported != "" {
		t.Fatalf("install %q: draft %v, unsupported %q", in.Label, in.Draft, in.Unsupported)
	}
	if in.Draft.Spec.ID != "good-server" {
		t.Errorf("spec id = %q, want the suggested id", in.Draft.Spec.ID)
	}
	if _, err := actions.McpAdd(in.Draft.Spec); err != nil {
		t.Errorf("McpAdd(%q): %v", in.Label, err)
	}
}

func TestUnsafeIdentifierBecomesUnsupportedNotDropped(t *testing.T) {
	e := find(t, search(t, pageOf("", entry(evilServer, ""))), evilName)
	if len(e.Installs) != 1 {
		t.Fatalf("installs = %+v, want the refused npm option listed", e.Installs)
	}
	in := e.Installs[0]
	if in.Draft != nil || in.Unsupported != mcpreg.ErrUnsafeIdentifier.Error() {
		t.Errorf("install = %+v, want no draft and the refusal reason", in)
	}
	if strings.Contains(in.Unsupported, "--registry") {
		t.Errorf("reason %q quotes the raw identifier", in.Unsupported)
	}
}

func TestDeprecatedCarriedThrough(t *testing.T) {
	p := search(t, pageOf("",
		entry(statusServer(oldName), "deprecated"),
		entry(statusServer(goneName), "deleted"),
		entry(statusServer("io.example/live"), "active"),
		entry(statusServer("io.example/unknown"), ""),
	))
	want := map[string]bool{oldName: true, goneName: true, "io.example/live": false, "io.example/unknown": false}
	for id, dep := range want {
		if got := find(t, p, id).Deprecated; got != dep {
			t.Errorf("%s: Deprecated = %v, want %v", id, got, dep)
		}
	}
}

func TestTrustLinesAlwaysPresentForOfficial(t *testing.T) {
	p := search(t, pageOf("", entry(goodServer, ""), entry(evilServer, ""), entry(nugetServer, ""), entry(ociServer, "")))
	want := []string{
		"unreviewed: the registry does not scan entries",
		"namespace ownership is verified at publish time",
	}
	for _, e := range p.Entries {
		if !slices.Equal(e.Trust, want) {
			t.Errorf("%s: Trust = %q, want %q", e.ID, e.Trust, want)
		}
	}
	// Mutating one entry's lines must not reach another's.
	p.Entries[0].Trust[0] = "changed"
	if p.Entries[1].Trust[0] != want[0] || mcpsrc.OfficialTrust()[0] != want[0] {
		t.Error("entries share one Trust slice")
	}
}

func TestPerInstallTrustStaysOnTheDraft(t *testing.T) {
	e := find(t, search(t, pageOf("", entry(ociServer, ""))), ociName)
	if slices.Contains(e.Trust, trustMutable) {
		t.Errorf("entry Trust %q holds a per-install line", e.Trust)
	}
	in := e.Installs[0]
	if in.Draft == nil || !slices.Contains(in.Draft.Trust, trustMutable) {
		t.Fatalf("install = %+v, want the OCI draft with the mutable-tag line", in)
	}
	got := mcpsrc.InstallTrust(e, in)
	want := append(mcpsrc.OfficialTrust(), trustMutable)
	if !slices.Equal(got, want) {
		t.Errorf("InstallTrust = %q, want %q", got, want)
	}
	got[0] = "changed"
	if e.Trust[0] != mcpsrc.TrustUnreviewed {
		t.Error("InstallTrust aliases the entry's lines")
	}
	if u := mcpsrc.InstallTrust(e, mcpsrc.Install{Unsupported: "x"}); !slices.Equal(u, mcpsrc.OfficialTrust()) {
		t.Errorf("unsupported install trust = %q", u)
	}
}

func TestNoUsableIDMakesEveryInstallUnsupported(t *testing.T) {
	e := find(t, search(t, pageOf("", entry(noIDServer, ""))), noIDName)
	if len(e.Installs) != 2 {
		t.Fatalf("installs = %+v, want both packages listed", e.Installs)
	}
	for _, in := range e.Installs {
		if in.Draft != nil || in.Unsupported != mcpsrc.ReasonNoID {
			t.Errorf("install = %+v, want unsupported with %q", in, mcpsrc.ReasonNoID)
		}
	}
}

func TestEntryWithoutKnownOptionListedWithReason(t *testing.T) {
	e := find(t, search(t, pageOf("", entry(nugetServer, ""))), nugetName)
	if len(e.Installs) != 1 || e.Installs[0].Draft != nil || e.Installs[0].Unsupported != mcpsrc.ReasonNoInstall {
		t.Fatalf("installs = %+v, want one unsupported option with %q", e.Installs, mcpsrc.ReasonNoInstall)
	}
}

func TestSkippedCarriedThrough(t *testing.T) {
	bad := `{"server": {"name": "io.example/bad", "packages": [{"registryType": "npm", "identifier": "bad", "transport": "stdio"}]}}`
	p := search(t, pageOf("", bad, entry(goodServer, ""), bad))
	if len(p.Entries) != 1 || p.Skipped != 2 {
		t.Fatalf("page = %+v, want one entry and Skipped 2", p)
	}
}

func TestEmptyPageHasNoEntries(t *testing.T) {
	p := search(t, `{"servers": null}`)
	if len(p.Entries) != 0 || p.Next != "" || p.Skipped != 0 {
		t.Fatalf("page = %+v, want empty", p)
	}
}

func TestClientErrorsPassedThroughUnchanged(t *testing.T) {
	c := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	p, err := mcpsrc.Official(c).Search(context.Background(), anyQuery, "")
	var se *netx.StatusError
	if !errors.Is(err, mcpreg.ErrBlocked) || !errors.As(err, &se) || se.Code != http.StatusTooManyRequests {
		t.Fatalf("err = %v, want ErrBlocked wrapping a 429", err)
	}
	if len(p.Entries) != 0 {
		t.Errorf("page = %+v on error", p)
	}
}

func TestOfflineClientErrorPassedThrough(t *testing.T) {
	_, err := mcpsrc.Official(mcpreg.NewClient(nil, nil)).Search(context.Background(), anyQuery, "")
	if err == nil {
		t.Fatal("a client without HTTP answered a search with no cache")
	}
}

func TestNilClientIsErrNoClient(t *testing.T) {
	if _, err := mcpsrc.Official(nil).Search(context.Background(), anyQuery, ""); !errors.Is(err, mcpsrc.ErrNoClient) {
		t.Fatalf("err = %v, want ErrNoClient", err)
	}
}
