package app_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
	"github.com/lusky3/agentsync-tui/internal/ui/library"
	"github.com/lusky3/agentsync-tui/internal/ui/secrets"
	"github.com/lusky3/agentsync-tui/internal/ui/servers"
)

const scopeWait = 10 * time.Second

// scopeRunner answers the few reads the servers, memory and secrets screens
// make. It records every call with the scope it ran under.
type scopeRunner struct {
	scope runner.Scope
	mu    sync.Mutex
	calls [][]string
}

func (r *scopeRunner) Run(_ context.Context, args ...string) (runner.Result, error) {
	r.mu.Lock()
	r.calls = append(r.calls, append([]string{string(r.scope)}, args...))
	r.mu.Unlock()
	switch strings.Join(args, " ") {
	case "version":
		return runner.Result{Stdout: "agentsync v0.17.0 (commit abc, built today)"}, nil
	case "status --json":
		return runner.Result{Stdout: `{"agents":[]}`}, nil
	case "secret list":
		return runner.Result{Stdout: "USERKEY\nPROJKEY\n"}, nil
	}
	return runner.Result{}, nil
}

// snap wraps the App so a test can read the latest rendered frame without
// racing the program goroutine.
type snap struct {
	*ui.App
	mu   sync.Mutex
	hist []string // every frame rendered after an Update, oldest first
}

func (s *snap) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := s.App.Update(msg)
	s.mu.Lock()
	s.hist = append(s.hist, ansi.Strip(s.App.View().Content))
	s.mu.Unlock()
	return s, cmd
}

func (s *snap) frame() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.hist) == 0 {
		return ""
	}
	return s.hist[len(s.hist)-1]
}

// mark returns the current history length, for waitEver.
func (s *snap) mark() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.hist)
}

// waitEver waits until some frame rendered since mark satisfies ok (for
// transient status lines that a later message replaces).
func waitEver(t *testing.T, s *snap, mark int, what string, ok func(string) bool) {
	t.Helper()
	deadline := time.Now().Add(scopeWait)
	for {
		s.mu.Lock()
		frames := append([]string(nil), s.hist[mark:]...)
		s.mu.Unlock()
		for _, f := range frames {
			if ok(f) {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; last frame:\n%s", what, s.frame())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func waitFrame(t *testing.T, s *snap, what string, ok func(string) bool) string {
	t.Helper()
	deadline := time.Now().Add(scopeWait)
	for {
		if f := s.frame(); ok(f) {
			return f
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s; last frame:\n%s", what, s.frame())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// squash drops every space and box border from s.
func squash(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r == '│' {
			return -1
		}
		return r
	}, s)
}

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// twoHomes builds a user home with usersrv (using ${secret:USERKEY}) and a
// project dir whose .agentsync holds projsrv (using ${secret:PROJKEY}), each
// with one memory fragment.
func twoHomes(t *testing.T) (userHome, projectDir string) {
	t.Helper()
	userHome = filepath.Join(t.TempDir(), "home", ".agentsync")
	projectDir = filepath.Join(t.TempDir(), "proj")
	writeFile(t, filepath.Join(userHome, "mcp", "usersrv.toml"), "[server]\ntype = 'stdio'\ncommand = 'echo'\n\n[server.env]\nK = '${secret:USERKEY}'\n")
	writeFile(t, filepath.Join(projectDir, ".agentsync", "mcp", "projsrv.toml"), "[server]\ntype = 'stdio'\ncommand = 'echo'\n\n[server.env]\nK = '${secret:PROJKEY}'\n")
	writeFile(t, filepath.Join(userHome, "memory", "fragments", "userfrag.md"), "# u\n")
	writeFile(t, filepath.Join(projectDir, ".agentsync", "memory", "fragments", "projfrag.md"), "# p\n")
	return userHome, projectDir
}

// Switching to project scope must make every file-reading screen read
// <project>/.agentsync: the servers list, the servers staleness re-check
// before a mutation, the memory list and the secrets cross-check. The
// confirm modal names the scope the command will run under.
func TestProjectScopeReadsProjectHome(t *testing.T) {
	userHome, projectDir := twoHomes(t)
	ur, pr := &scopeRunner{scope: runner.ScopeUser}, &scopeRunner{scope: runner.ScopeProject}
	d := ui.Deps{
		Runner:        ur,
		AgentsyncHome: userHome,
		Scope:         runner.ScopeUser,
		ProjectDir:    projectDir,
		WithScope: func(sc runner.Scope) (runner.Runner, func(...string) *exec.Cmd) {
			if sc == runner.ScopeProject {
				return pr, nil
			}
			return ur, nil
		},
	}
	m := &snap{App: ui.NewApp(d, servers.New(d), library.New(d, library.Memory), secrets.New(d))}
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 30))
	t.Cleanup(func() { _ = tm.Quit(); tm.WaitFinished(t, teatest.WithFinalTimeout(scopeWait)) })

	waitFrame(t, m, "user servers", func(f string) bool { return strings.Contains(f, "usersrv") })

	tm.Send(tea.KeyPressMsg{Code: 's', Text: "s"})
	f := waitFrame(t, m, "project servers", func(f string) bool {
		return strings.Contains(f, "scope: project") && strings.Contains(f, "projsrv") && !strings.Contains(f, "usersrv")
	})
	if strings.Contains(f, "usersrv") {
		t.Errorf("project scope still lists the user tree:\n%s", f)
	}

	// The staleness re-check inside mutate() must find projsrv in the
	// project home; reading the user home would report "list changed".
	tm.Send(tea.KeyPressMsg{Code: 'e', Text: "e"})
	f = waitFrame(t, m, "disable modal", func(f string) bool { return strings.Contains(f, "Run it?") })
	// The modal wraps a long project path anywhere, so compare without
	// spaces and borders: the test must not depend on TMPDIR's length.
	flat := squash(f)
	for _, want := range []string{"mcp disable projsrv", "scope: project — " + projectDir} {
		if !strings.Contains(flat, squash(want)) {
			t.Errorf("modal missing %q:\n%s", want, f)
		}
	}
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEscape})
	waitFrame(t, m, "modal closed", func(f string) bool { return !strings.Contains(f, "Run it?") })

	tm.Send(tea.KeyPressMsg{Code: tea.KeyTab})
	waitFrame(t, m, "project memory", func(f string) bool {
		return strings.Contains(f, "projfrag") && !strings.Contains(f, "userfrag")
	})

	tm.Send(tea.KeyPressMsg{Code: tea.KeyTab})
	waitFrame(t, m, "project secrets cross-check", func(f string) bool {
		return strings.Contains(f, "PROJKEY") && strings.Contains(f, "used by: projsrv") && !strings.Contains(f, "usersrv")
	})

	// Back to user scope: the user tree again, and the modal says so.
	tm.Send(tea.KeyPressMsg{Code: tea.KeyTab}) // servers
	tm.Send(tea.KeyPressMsg{Code: 's', Text: "s"})
	waitFrame(t, m, "user servers again", func(f string) bool {
		return strings.Contains(f, "scope: user") && strings.Contains(f, "usersrv") && !strings.Contains(f, "projsrv")
	})
	tm.Send(tea.KeyPressMsg{Code: 'e', Text: "e"})
	f = waitFrame(t, m, "user disable modal", func(f string) bool { return strings.Contains(f, "Run it?") })
	flat = squash(f)
	if !strings.Contains(flat, squash("mcp disable usersrv")) || !strings.Contains(flat, squash("scope: user")) || strings.Contains(flat, squash(projectDir)) {
		t.Errorf("user-scope modal:\n%s", f)
	}
	tm.Send(tea.KeyPressMsg{Code: tea.KeyEscape})

	pr.mu.Lock()
	defer pr.mu.Unlock()
	if len(pr.calls) == 0 {
		t.Error("project runner never used")
	}
}

// E edits only files inside the current scope's home: in project scope a
// user-home path is refused and a project-home path is accepted, and the
// reverse after toggling back.
func TestProjectScopeEditStaysInsideScopeHome(t *testing.T) {
	userHome, projectDir := twoHomes(t)
	projHome := filepath.Join(projectDir, ".agentsync")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "true")
	ur, pr := &scopeRunner{scope: runner.ScopeUser}, &scopeRunner{scope: runner.ScopeProject}
	d := ui.Deps{
		Runner:        ur,
		AgentsyncHome: userHome,
		Scope:         runner.ScopeUser,
		ProjectDir:    projectDir,
		WithScope: func(sc runner.Scope) (runner.Runner, func(...string) *exec.Cmd) {
			if sc == runner.ScopeProject {
				return pr, nil
			}
			return ur, nil
		},
	}
	m := &snap{App: ui.NewApp(d, servers.New(d))}
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 30))
	t.Cleanup(func() { _ = tm.Quit(); tm.WaitFinished(t, teatest.WithFinalTimeout(scopeWait)) })
	row := func(id string) string { return "> " + id + "  stdio" }
	waitFrame(t, m, "user servers", func(f string) bool { return strings.Contains(f, row("usersrv")) })

	tm.Send(tea.KeyPressMsg{Code: 's', Text: "s"})
	waitFrame(t, m, "project servers", func(f string) bool {
		return strings.Contains(f, row("projsrv")) && !strings.Contains(f, "usersrv")
	})

	// The screen's own E key resolves the selected server in the project
	// home and the shell accepts it ("editing …" is shown, the editor exits 0,
	// then `check` runs and replaces the status).
	mk := m.mark()
	tm.Send(tea.KeyPressMsg{Code: 'E', Text: "E"})
	waitEver(t, m, mk, "E edits projsrv", func(f string) bool { return strings.Contains(f, "editing projsrv.toml…") })
	waitFrame(t, m, "check after E", func(f string) bool { return strings.Contains(f, "done: Check configuration") })

	tm.Send(ui.EditFileMsg{Path: filepath.Join(userHome, "mcp", "usersrv.toml")})
	waitFrame(t, m, "user path refused in project scope", func(f string) bool {
		return strings.Contains(f, "edit refused: usersrv.toml: outside the agentsync home")
	})

	tm.Send(tea.KeyPressMsg{Code: 's', Text: "s"})
	waitFrame(t, m, "user servers again", func(f string) bool {
		return strings.Contains(f, row("usersrv")) && !strings.Contains(f, row("projsrv"))
	})
	tm.Send(ui.EditFileMsg{Path: filepath.Join(projHome, "mcp", "projsrv.toml")})
	waitFrame(t, m, "project path refused in user scope", func(f string) bool {
		return strings.Contains(f, "edit refused: projsrv.toml: outside the agentsync home")
	})
	mk = m.mark()
	tm.Send(tea.KeyPressMsg{Code: 'E', Text: "E"})
	waitEver(t, m, mk, "E edits usersrv", func(f string) bool { return strings.Contains(f, "editing usersrv.toml…") })
	waitFrame(t, m, "check after E", func(f string) bool { return strings.Contains(f, "done: Check configuration") })
}

// A project directory with no .agentsync tree yet is an empty project, not
// an error: every file-reading screen shows its normal empty state.
func TestProjectScopeWithoutProjectTreeShowsEmptyStates(t *testing.T) {
	userHome, _ := twoHomes(t)
	projectDir := t.TempDir() // no .agentsync inside
	ur, pr := &scopeRunner{scope: runner.ScopeUser}, &scopeRunner{scope: runner.ScopeProject}
	d := ui.Deps{
		Runner:        ur,
		AgentsyncHome: userHome,
		Scope:         runner.ScopeUser,
		ProjectDir:    projectDir,
		WithScope: func(sc runner.Scope) (runner.Runner, func(...string) *exec.Cmd) {
			if sc == runner.ScopeProject {
				return pr, nil
			}
			return ur, nil
		},
	}
	m := &snap{App: ui.NewApp(d, servers.New(d), library.New(d, library.Memory), secrets.New(d))}
	tm := teatest.NewTestModel(t, m, teatest.WithInitialTermSize(120, 30))
	t.Cleanup(func() { _ = tm.Quit(); tm.WaitFinished(t, teatest.WithFinalTimeout(scopeWait)) })
	waitFrame(t, m, "user servers", func(f string) bool { return strings.Contains(f, "usersrv") })

	tm.Send(tea.KeyPressMsg{Code: 's', Text: "s"})
	f := waitFrame(t, m, "empty project servers", func(f string) bool {
		return strings.Contains(f, "no MCP servers") && !strings.Contains(f, "usersrv")
	})
	if strings.Contains(strings.ToLower(f), "error") {
		t.Errorf("servers shows an error for a missing project tree:\n%s", f)
	}
	tm.Send(tea.KeyPressMsg{Code: tea.KeyTab})
	waitFrame(t, m, "empty project memory", func(f string) bool {
		return strings.Contains(f, "2 Memory") && !strings.Contains(f, "userfrag") && !strings.Contains(f, "cannot read")
	})
	tm.Send(tea.KeyPressMsg{Code: tea.KeyTab})
	f = waitFrame(t, m, "project secrets without uses", func(f string) bool {
		return strings.Contains(f, "USERKEY") && strings.Contains(f, "used by: (none)") && !strings.Contains(f, "usersrv")
	})
	if strings.Contains(strings.ToLower(f), "error") {
		t.Errorf("secrets shows an error for a missing project tree:\n%s", f)
	}
}
