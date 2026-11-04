// Scope switching, the canonical home, project-scope edge cases and lazy loading.
package ui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/runner"
)

func TestScopeProjectOutsideProjectShowsEmptyState(t *testing.T) {
	fr := newFakeRunner()
	d := baseDeps(fr)
	var called bool
	d.WithScope = func(runner.Scope) (runner.Runner, func(...string) *exec.Cmd) {
		called = true
		return fr, nil
	}
	s := newScreen("A", "body-a")
	h := start(t, d, s)
	h.tm.Send(key("s"))
	app := h.final()
	v := content(app)
	if !strings.Contains(v, "no project directory: start with --project <path>") || strings.Contains(v, "body-a") {
		t.Errorf("empty state missing: %q", v)
	}
	if called || fr.nCalls() != 0 || len(s.refreshed) != 0 {
		t.Error("something ran in project scope without a project directory")
	}
}

func TestScopeSwitchRebuildsAndRefreshes(t *testing.T) {
	fr, fr2 := newFakeRunner(), newFakeRunner()
	d := baseDeps(fr)
	d.ProjectDir = "/proj"
	var got runner.Scope
	d.WithScope = func(sc runner.Scope) (runner.Runner, func(...string) *exec.Cmd) {
		got = sc
		return fr2, nil
	}
	a, b := newScreen("A", "a"), newScreen("B", "b")
	h := start(t, d, a, b)
	h.tm.Send(key("s"))
	waitRefresh(t, a) // the active screen refreshes at once
	h.tm.Send(key("tab"))
	waitRefresh(t, b) // the other one when it is next shown
	h.tm.Send(DoMsg{Action: actions.Check()})
	select {
	case <-fr2.started:
	case <-time.After(timeout):
		t.Fatal("action did not use the rebuilt runner")
	}
	fr2.respond <- fakeResp{}
	h.waitRun()
	app := h.final()
	if got != runner.ScopeProject || app.scope != runner.ScopeProject || !strings.Contains(content(app), "project") {
		t.Errorf("scope=%v got=%v", app.scope, got)
	}
}

func TestScopeSwitchUnavailable(t *testing.T) {
	h := start(t, baseDeps(newFakeRunner()), newScreen("A", "a"))
	h.tm.Send(key("s"))
	app := h.final()
	if !strings.Contains(content(app), "scope switching unavailable") || app.scope != runner.ScopeUser {
		t.Errorf("view: %q", content(app))
	}
}

// initScreen records Init calls.
type initScreen struct {
	*fakeScreen
	mu    *sync.Mutex
	inits *int
}

func (s initScreen) Init() tea.Cmd {
	s.mu.Lock()
	*s.inits++
	s.mu.Unlock()
	return nil
}

func TestStartupInProjectScopeWithoutDirRunsNothing(t *testing.T) {
	fr := newFakeRunner()
	d := Deps{Runner: fr, Scope: runner.ScopeProject}
	d.WithScope = func(runner.Scope) (runner.Runner, func(...string) *exec.Cmd) { return fr, nil }
	var mu sync.Mutex
	var inits int
	s := initScreen{fakeScreen: newScreen("A", "body-a"), mu: &mu, inits: &inits}
	h := start(t, d, s)
	h.waitVersion()     // Init has run
	h.tm.Send(key("s")) // project (no dir) -> user: a real switch
	waitRefresh(t, s.fakeScreen)
	app := h.final()
	mu.Lock()
	defer mu.Unlock()
	if inits != 0 {
		t.Errorf("screen Init ran %d times in project scope without a directory", inits)
	}
	if app.scope != runner.ScopeUser || app.noProject || !strings.Contains(content(app), "body-a") {
		t.Errorf("scope=%v noProject=%v view=%q", app.scope, app.noProject, content(app))
	}
	if fr.nCalls() != 0 {
		t.Errorf("runner called %d times", fr.nCalls())
	}
}

func TestHomeFor(t *testing.T) {
	cases := []struct {
		name  string
		d     Deps
		scope runner.Scope
		want  string
	}{
		{"user", Deps{AgentsyncHome: "/u/.agentsync", ProjectDir: "/p"}, runner.ScopeUser, "/u/.agentsync"},
		{"project", Deps{AgentsyncHome: "/u/.agentsync", ProjectDir: "/p"}, runner.ScopeProject, filepath.Join("/p", ".agentsync")},
		{"project without dir", Deps{AgentsyncHome: "/u/.agentsync"}, runner.ScopeProject, ""},
	}
	for _, c := range cases {
		if got := HomeFor(c.d, c.scope); got != c.want {
			t.Errorf("%s: HomeFor = %q, want %q", c.name, got, c.want)
		}
	}
}

// Toggling scope moves App.home to the project tree, so an edit of a
// user-home path is refused in project scope (and vice versa), even though
// the screens never changed.
func TestToggleScopeMovesHomeForEdits(t *testing.T) {
	userHome, userFile := editHome(t, "usersrv.toml")
	projectDir := t.TempDir()
	projFile := filepath.Join(projectDir, ".agentsync", "mcp", "projsrv.toml")
	if err := os.MkdirAll(filepath.Dir(projFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(projFile, []byte("[server]\ntype = 'stdio'\ncommand = 'echo'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "true")
	fr := newFakeRunner()
	d := baseDeps(fr)
	d.AgentsyncHome, d.ProjectDir = userHome, projectDir
	d.WithScope = func(runner.Scope) (runner.Runner, func(...string) *exec.Cmd) { return fr, nil }
	app := NewApp(d, newScreen("A", "a"))
	if app.home != userHome {
		t.Fatalf("initial home %q", app.home)
	}
	app.Update(key("s"))
	if want := filepath.Join(projectDir, ".agentsync"); app.home != want {
		t.Fatalf("home after toggle %q, want %q", app.home, want)
	}
	if _, cmd := app.Update(EditFileMsg{Path: userFile}); cmd != nil || !strings.Contains(app.status, "edit refused: usersrv.toml: outside the agentsync home") {
		t.Errorf("project scope accepted a user-home path: cmd=%v status=%q", cmd != nil, app.status)
	}
	if _, cmd := app.Update(EditFileMsg{Path: projFile}); cmd == nil || !strings.Contains(app.status, "editing projsrv.toml") {
		t.Errorf("project scope refused the project path: status=%q", app.status)
	}
	app.running = nil // the editor is not actually awaited here
	app.Update(key("s"))
	if app.home != userHome {
		t.Fatalf("home after toggling back %q", app.home)
	}
	if _, cmd := app.Update(EditFileMsg{Path: projFile}); cmd != nil || !strings.Contains(app.status, "edit refused: projsrv.toml: outside the agentsync home") {
		t.Errorf("user scope accepted a project path: cmd=%v status=%q", cmd != nil, app.status)
	}
}

// Startup reads only for the active screen; a screen loads when it is first
// shown, and a write elsewhere makes the screens not on show stale, so they
// refresh when next shown rather than keep old data.
func TestScreensLoadLazily(t *testing.T) {
	fr := newFakeRunner()
	var mu sync.Mutex
	var inits [3]int
	var screens []Screen
	var fakes []*fakeScreen
	for i := range inits {
		f := newScreen(fmt.Sprintf("S%d", i), fmt.Sprintf("body-%d", i))
		fakes = append(fakes, f)
		screens = append(screens, initScreen{fakeScreen: f, mu: &mu, inits: &inits[i]})
	}
	h := start(t, baseDeps(fr), screens...)
	h.waitVersion()
	mu.Lock()
	if inits != [3]int{1, 0, 0} {
		t.Fatalf("inits at startup = %v, want only the active screen", inits)
	}
	mu.Unlock()
	select {
	case <-fakes[1].refreshed:
		t.Fatal("an inactive screen was refreshed at startup")
	default:
	}
	h.tm.Send(key("tab"))
	waitRefresh(t, fakes[1]) // first shown: loads now
	h.tm.Send(key("1"))
	h.tm.Send(key("tab"))
	select {
	case <-fakes[1].refreshed:
		t.Fatal("a loaded screen was refreshed again without a reason")
	case <-time.After(100 * time.Millisecond):
	}
	// A write on screen 2 (the current one) leaves screens 1 and 3 stale.
	h.tm.Send(RunMsg{Action: actions.Action{Title: "w", Argv: []string{"mcp", "enable", "x"}}})
	waitRefresh(t, fakes[1]) // the active screen refreshes after every run
	h.tm.Send(key("1"))
	waitRefresh(t, fakes[0])
	h.tm.Send(key("3"))
	waitRefresh(t, fakes[2])
	mu.Lock()
	defer mu.Unlock()
	if inits != [3]int{1, 0, 0} {
		t.Errorf("Init ran again: %v", inits)
	}
}
