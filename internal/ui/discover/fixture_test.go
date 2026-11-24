package discover

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/discover/dockercat"
	"github.com/lusky3/agentsync-tui/internal/discover/mcpreg"
	"github.com/lusky3/agentsync-tui/internal/netx"
	"github.com/lusky3/agentsync-tui/internal/netx/netxtest"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

const (
	registryPath = "/v0.1/servers"
	catalogPath  = "/mcp/catalog/v3/catalog.yaml"

	braveQuery   = "brave"
	braveRegName = "io.example/brave"
	oldRegName   = "io.example/old"
	evilRegName  = "io.example/evil"
	ociRegName   = "io.example/image"
	braveKey     = "brave"

	keyEnter = "enter"
	keyEsc   = "esc"
)

// Registry records (wire form) for the screen tests.
const (
	braveServer = `{"name": "` + braveRegName + `", "description": "Brave search\tfrom the registry", "version": "1.0.0",
		"packages": [
			{"registryType": "npm", "identifier": "brave-mcp", "version": "1.0.0", "transport": {"type": "stdio"},
				"environmentVariables": [
					{"name": "BRAVE_API_KEY", "isSecret": true, "isRequired": true},
					{"name": "REGION", "isRequired": true, "default": "us", "description": "Search\nregion"}]},
			{"registryType": "pypi", "identifier": "brave-mcp", "version": "1.0.0", "transport": {"type": "stdio"}}],
		"remotes": [{"type": "sse", "url": "https://brave.example/sse"}]}`
	oldServer = `{"name": "` + oldRegName + `", "description": "An old one",
		"remotes": [{"type": "sse", "url": "https://old.example/sse"}]}`
	evilServer = `{"name": "` + evilRegName + `", "description": "evil",
		"packages": [{"registryType": "npm", "identifier": "--registry=evil", "version": "1.0.0", "transport": {"type": "stdio"}}]}`
	ociServer = `{"name": "` + ociRegName + `", "version": "1.2",
		"packages": [{"registryType": "oci", "identifier": "ghcr.io/example/image", "version": "1.2", "transport": {"type": "stdio"}}]}`
)

// The catalog served for the docker source: one installable server and one
// whose image is not digest-pinned.
const catalogYAML = `version: 3
name: docker-mcp
registry:
  brave:
    description: Brave Search from the Docker catalog.
    title: Brave Search
    type: server
    image: mcp/brave-search@sha256:a1bb216b2bbb7eb30b25960e6840aea87490f642a086e0c0ef4aae2ee7ce56f4
    secrets:
      - name: brave.api_key
        env: BRAVE_API_KEY
  loose:
    description: An image without a digest.
    title: Loose
    type: server
    image: mcp/loose:latest
`

func regEntry(server, status string) string {
	if status == "" {
		return `{"server": ` + server + `}`
	}
	return `{"server": ` + server + `, "_meta": {"io.modelcontextprotocol.registry/official": {"status": "` + status + `"}}}`
}

func regPage(next string, entries ...string) string {
	return `{"servers": [` + strings.Join(entries, ",") + `], "metadata": {"nextCursor": "` + next + `"}}`
}

// fake is the registry and the catalog behind one TLS server. registry
// answers a registry request (status, body); catalog is the catalog body.
// Every request is recorded; hold, when set, makes a request wait until
// its context ends.
type fake struct {
	mu       sync.Mutex
	reqs     []*url.URL
	registry func(q url.Values) (int, string)
	catalog  string
	hold     bool
	held     chan struct{} // closed when a held request's context ends
	srv      *httptest.Server
}

func newFake(t *testing.T) *fake {
	t.Helper()
	f := &fake{
		catalog: catalogYAML,
		held:    make(chan struct{}),
		registry: func(url.Values) (int, string) {
			return http.StatusOK, regPage("", regEntry(braveServer, ""), regEntry(oldServer, "deprecated"))
		},
	}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.reqs = append(f.reqs, r.URL)
	hold, reg, cat := f.hold, f.registry, f.catalog
	f.mu.Unlock()
	if hold {
		<-r.Context().Done()
		close(f.held)
		return
	}
	switch r.URL.Path {
	case registryPath:
		code, body := reg(r.URL.Query())
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	case catalogPath:
		_, _ = w.Write([]byte(cat))
	default:
		http.NotFound(w, r)
	}
}

func (f *fake) requests() []*url.URL {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*url.URL(nil), f.reqs...)
}

// client reaches f through the production allowlist.
func (f *fake) client() *netx.Client {
	return netx.New(netx.Options{
		AllowedHosts: ui.DiscoverHosts(),
		UserAgent:    "agentsync-tui/test",
		Timeout:      10 * time.Second,
		Transport:    netxtest.Transport(f.srv, mcpreg.Host, dockercat.Host),
	})
}

func (f *fake) deps() ui.Deps { return ui.Deps{Discover: ui.Discover{HTTP: f.client()}} }

// newScreen is a sized screen over d.
func newScreen(d ui.Deps) *MCPScreen {
	s := NewMCP(d)
	s.Update(tea.WindowSizeMsg{Width: 300, Height: 60})
	return s
}

// drive runs cmd (through batches), feeds the screen's own messages back
// and returns every other message (what the shell would get).
func drive(s *MCPScreen, cmd tea.Cmd) []tea.Msg {
	var out []tea.Msg
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch m := c().(type) {
		case nil:
		case tea.BatchMsg:
			queue = append(queue, m...)
		case pageMsg:
			_, next := s.Update(m)
			queue = append(queue, next)
		default:
			out = append(out, m)
		}
	}
	return out
}

func keyMsg(k string) tea.KeyPressMsg {
	switch k {
	case keyEnter:
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case keyEsc:
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	}
	r := []rune(k)[0]
	return tea.KeyPressMsg{Code: r, Text: k}
}

// press sends keys and drives what they start; it returns the messages
// for the shell.
func press(s *MCPScreen, keys ...string) []tea.Msg {
	var out []tea.Msg
	for _, k := range keys {
		_, cmd := s.Update(keyMsg(k))
		out = append(out, drive(s, cmd)...)
	}
	return out
}

func typeText(s *MCPScreen, text string) {
	for _, r := range text {
		s.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

// search types q into the field and runs it.
func search(s *MCPScreen, q string) []tea.Msg {
	press(s, "/")
	s.query.Clear()
	typeText(s, q)
	return press(s, keyEnter)
}

func view(s *MCPScreen) string { return ansi.Strip(s.View().Content) }
