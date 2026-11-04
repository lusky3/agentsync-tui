package app_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
	"github.com/lusky3/agentsync-tui/internal/ui/apply"
	"github.com/lusky3/agentsync-tui/internal/ui/importer"
)

// okRunner answers every call at once: an agent list with claude, and exit 0
// with empty output for everything else. It records each call.
type okRunner struct {
	mu    sync.Mutex
	calls []string
}

func (r *okRunner) Run(_ context.Context, args ...string) (runner.Result, error) {
	k := strings.Join(args, " ")
	r.mu.Lock()
	r.calls = append(r.calls, k)
	r.mu.Unlock()
	switch k {
	case "version":
		return runner.Result{Stdout: "agentsync v0.17.0 (commit abc, built today)"}, nil
	case "agent list":
		return runner.Result{Stdout: "claude     enabled=true scope=user\n"}, nil
	}
	return runner.Result{Stdout: "ok\n"}, nil
}

// seenProbe reports every message the real shell processed.
type seenProbe struct {
	*ui.App
	seen chan tea.Msg
}

func (p seenProbe) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := p.App.Update(msg)
	select {
	case p.seen <- msg:
	default:
	}
	return p, cmd
}

func waitSeen(t *testing.T, seen chan tea.Msg, what string, match func(tea.Msg) bool) {
	t.Helper()
	deadline := time.After(scopeWait)
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

func isRun(argv string) func(tea.Msg) bool {
	return func(m tea.Msg) bool {
		r, ok := m.(ui.RunMsg)
		return ok && strings.Join(r.Action.Argv, " ") == argv
	}
}

func isKey(s string) func(tea.Msg) bool {
	return func(m tea.Msg) bool { k, ok := m.(tea.KeyPressMsg); return ok && k.String() == s }
}

func press(tm *teatest.TestModel, s string) {
	tm.Send(tea.KeyPressMsg{Code: []rune(s)[0], Text: s})
}

// An $EDITOR edit can change the canonical files a preview described, so a
// preview taken before the edit must not let the real apply or import run
// afterwards: both screens must ask for a new preview.
func TestEditInvalidatesApplyAndImportPreviews(t *testing.T) {
	home := t.TempDir()
	file := filepath.Join(home, "skill.md")
	if err := os.WriteFile(file, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", "true") // exits 0 without touching the file
	r := &okRunner{}
	d := ui.Deps{Runner: r, Scope: runner.ScopeUser, AgentsyncHome: home}
	ap, im := apply.New(d), importer.New(d)
	p := seenProbe{App: ui.NewApp(d, ap, im), seen: make(chan tea.Msg, 4096)}
	tm := teatest.NewTestModel(t, p, teatest.WithInitialTermSize(140, 40))
	// The importer loads its agent list through its own read (agentsMsg).
	waitSeen(t, p.seen, "agent list", func(m tea.Msg) bool { return fmt.Sprintf("%T", m) == "importer.agentsMsg" })

	// Preview on both screens.
	press(tm, "p")
	waitSeen(t, p.seen, "apply preview", isRun("apply --dry-run"))
	press(tm, "2")
	press(tm, "p")
	waitSeen(t, p.seen, "import preview", func(m tea.Msg) bool {
		r, ok := m.(ui.RunMsg)
		return ok && r.Action.ReadOnly && len(r.Action.Argv) > 0 && r.Action.Argv[0] == "import"
	})

	// Edit a canonical file.
	tm.Send(ui.EditFileMsg{Path: file})
	// The shell broadcasts EditOutcomeMsg to the screens itself (not through
	// Update), so wait for the editor's exit message instead.
	waitSeen(t, p.seen, "edit finished", func(m tea.Msg) bool { return fmt.Sprintf("%T", m) == "ui.editDoneMsg" })

	// The real import, then the real apply, must now be refused. Had either
	// been allowed, the shell's confirm modal would be open and would
	// swallow the keys that follow it.
	press(tm, "i")
	press(tm, "1")
	press(tm, "a")
	waitSeen(t, p.seen, "a", isKey("a"))
	_ = tm.Quit()
	final := tm.FinalModel(t, teatest.WithFinalTimeout(scopeWait)).(seenProbe).App
	if v := ansi.Strip(final.View().Content); strings.Contains(v, "Run it?") {
		t.Errorf("a real run was offered behind a pre-edit preview:\n%s", v)
	}
	for name, scr := range map[string]ui.Screen{"apply": ap, "import": im} {
		if v := ansi.Strip(scr.View().Content); !strings.Contains(v, "preview first") {
			t.Errorf("%s screen did not ask for a new preview:\n%s", name, v)
		}
	}
}
