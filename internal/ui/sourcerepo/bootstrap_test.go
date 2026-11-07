package sourcerepo

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/gitrepo"
	"github.com/lusky3/agentsync-tui/internal/gitrepo/gittest"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/runner/runnertest"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

const goodURL = "git@github.com:o/agent-config.git"

// openMissing builds the screen over a home that does not exist.
func openMissing(t *testing.T, scope runner.Scope) (*Screen, *runnertest.Fake, string) {
	t.Helper()
	gittest.Need(t)
	root := t.TempDir()
	home := filepath.Join(root, ".agentsync")
	agent := runnertest.New()
	d := deps(home, agent, &recGit{inner: gittest.NewGit(t)}, nil)
	d.Scope, d.ProjectDir = scope, root
	s := New(d)
	s.Update(tea.WindowSizeMsg{Width: 160, Height: 60})
	drive(s, s.Init())
	if s.repo.Manager != gitrepo.Missing {
		t.Fatalf("manager %v", s.repo.Manager)
	}
	return s, agent, home
}

func TestBootstrapShownWhenHomeMissing(t *testing.T) {
	s, _, home := openMissing(t, runner.ScopeUser)
	wantView(t, s, "No agentsync home at "+home+".", "i clone")
	press(s, "i")
	if !s.CapturesKeys() || s.mode != modeURL {
		t.Fatalf("i did not focus the URL field: mode %v", s.mode)
	}
	typeText(s, "https://example.com/o/r.git")
	wantView(t, s, "https://example.com/o/r.git")
	press(s, "esc")
	if s.CapturesKeys() || s.mode != modeList {
		t.Error("esc did not leave the field")
	}
}

func TestBootstrapRejectsBadURLs(t *testing.T) {
	s, agent, _ := openMissing(t, runner.ScopeUser)
	for _, u := range []string{
		"ext::sh -c x", "fd::0", "file:///x", "http://h/r", "-uX", "https://tok@github.com/o/r",
		"https://u:p@h/r", "ssh://-oProxyCommand=x/r", "git@host:-x", "a b", "x --upload-pack=y",
		"", "https://h", "git+ssh://h/r", "https://h/r\x1b",
	} {
		press(s, "i")
		s.url.Value = u
		if out := press(s, "enter"); len(out) != 0 {
			t.Errorf("%q emitted %v", u, out)
		}
		if s.mode != modeURL || s.urlNote == "" {
			t.Errorf("%q: mode %v note %q", u, s.mode, s.urlNote)
		}
		if strings.Contains(view(s), "\x1b") {
			t.Errorf("%q: ESC in view", u)
		}
		press(s, "esc")
	}
	if len(agent.Calls()) != 0 {
		t.Errorf("agentsync ran: %v", agent.Calls())
	}
}

func TestBootstrapEmitsInitAction(t *testing.T) {
	s, _, _ := openMissing(t, runner.ScopeUser)
	press(s, "i")
	typeText(s, goodURL)
	d := onlyDo(t, press(s, "enter"))
	want := mustAct(actions.InitFromURL(goodURL))
	if !slices.Equal(d.Action.Argv, want.Argv) || d.Action.Program != "" || !d.Action.Network {
		t.Fatalf("action %+v", d.Action)
	}
	if d.Scope != runner.ScopeUser || s.mode != modeList {
		t.Errorf("scope %q mode %v", d.Scope, s.mode)
	}
	_, cmd := s.Update(ui.RunMsg{Action: d.Action})
	if cmd == nil {
		t.Error("no refresh after a successful init")
	}
	if !strings.Contains(s.note, "agentsync check") || !strings.Contains(s.note, "Health") {
		t.Errorf("note %q", s.note)
	}
}

func TestBootstrapDisabledInProjectScope(t *testing.T) {
	s, _, _ := openMissing(t, runner.ScopeProject)
	wantNothing(t, press(s, "i"))
	if s.mode != modeList || s.CapturesKeys() {
		t.Errorf("mode %v", s.mode)
	}
	wantView(t, s, "init from a URL is user scope only")
}

func TestBootstrapDisabledWhenUnknown(t *testing.T) {
	gittest.Need(t)
	home := filepath.Join(t.TempDir(), "home")
	write(t, home, "agentsync.toml", "# x\n")
	s := New(deps(home, runnertest.New(), runnertest.New().Fail(fmt.Errorf("boom")), nil))
	s.Update(tea.WindowSizeMsg{Width: 160, Height: 60})
	drive(s, s.Init())
	if s.repo.Manager != gitrepo.Unknown {
		t.Fatalf("manager %v", s.repo.Manager)
	}
	press(s, "i")
	if s.mode != modeList || s.CapturesKeys() {
		t.Fatal("i opened the URL field under Unknown")
	}
	typeText(s, goodURL)
	wantNothing(t, press(s, "enter"))
	wantView(t, s, whyUnknown)
}

func TestBootstrapDisabledWhenHomeExists(t *testing.T) {
	f := open(t, newRepo(t))
	press(f.s, "i")
	if f.s.mode != modeList {
		t.Error("i opened the URL field over an existing home")
	}
}
