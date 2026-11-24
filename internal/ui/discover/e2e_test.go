package discover

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/runner/runnertest"
	"github.com/lusky3/agentsync-tui/internal/ui"
	"github.com/lusky3/agentsync-tui/internal/ui/servers"
)

const e2eTimeout = 10 * time.Second

// probe wraps the real shell and reports every message it handled, so the
// test waits on messages instead of sleeping.
type probe struct {
	*ui.App
	seen chan tea.Msg
}

func (p probe) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := p.App.Update(msg)
	select {
	case p.seen <- msg:
	default:
	}
	return p, cmd
}

func awaitMsg(t *testing.T, seen <-chan tea.Msg, what string, match func(tea.Msg) bool) {
	t.Helper()
	deadline := time.After(e2eTimeout)
	for {
		select {
		case m := <-seen:
			if match(m) {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

// Search the (fake) registry through the real shell, open the entry, install
// it: the MCP servers tab becomes active with the add form prefilled from the
// entry. No agentsync runs (the runner is scripted) and no real host is
// reached (netxtest maps the registry host to the local fake).
func TestSearchDetailInstallOpensPrefilledServersForm(t *testing.T) {
	f := newFake(t)
	d := f.deps()
	d.Runner = runnertest.New()
	d.AgentsyncHome = t.TempDir()
	d.Scope = runner.ScopeUser
	srv := servers.New(d)
	mcp := NewMCP(d)
	p := probe{App: ui.NewApp(d, srv, mcp), seen: make(chan tea.Msg, 4096)}
	tm := teatest.NewTestModel(t, p, teatest.WithInitialTermSize(220, 50))

	tm.Send(keyMsg("2"))
	tm.Send(keyMsg("/"))
	for _, r := range braveQuery {
		tm.Send(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	tm.Send(keyMsg(keyEnter))
	awaitMsg(t, p.seen, "the search result", func(m tea.Msg) bool { _, ok := m.(pageMsg); return ok })
	tm.Send(keyMsg(keyEnter)) // detail of the first row
	tm.Send(keyMsg("i"))      // its first installable option (npm)
	awaitMsg(t, p.seen, "the compose message", func(m tea.Msg) bool { _, ok := m.(ui.ComposeMcpMsg); return ok })

	banner := "from official registry: " + braveRegName + "@1.0.0: the command below runs code on your machine"
	teatest.WaitFor(t, tm.Output(), func(b []byte) bool {
		return strings.Contains(ansi.Strip(string(b)), banner)
	}, teatest.WithDuration(e2eTimeout))

	_ = tm.Quit()
	tm.FinalModel(t, teatest.WithFinalTimeout(e2eTimeout))
	if !srv.CapturesKeys() {
		t.Error("the servers tab has no form open")
	}
	v := ansi.Strip(srv.View().Content)
	for _, want := range []string{banner, "REGION", "brave-mcp@1.0.0", "${secret:brave.brave_api_key}", "unreviewed"} {
		if !strings.Contains(v, want) {
			t.Errorf("form lacks %q:\n%s", want, v)
		}
	}
	if got := len(f.requests()); got != 1 {
		t.Errorf(wantRequests, got, 1)
	}
}
