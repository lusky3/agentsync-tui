package app_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
	"github.com/lusky3/agentsync-tui/internal/ui/plugins"
	"github.com/lusky3/agentsync-tui/internal/ui/servers"
)

// blockingRunner answers version at once and blocks every other call until
// its context is done, recording each call's context.
type blockingRunner struct {
	mu   sync.Mutex
	ctxs map[string]context.Context
}

func (r *blockingRunner) Run(ctx context.Context, args ...string) (runner.Result, error) {
	key := strings.Join(args, " ")
	if key == "version" {
		return runner.Result{Stdout: "agentsync v0.17.0 (commit abc, built today)"}, nil
	}
	r.mu.Lock()
	if r.ctxs == nil {
		r.ctxs = map[string]context.Context{}
	}
	r.ctxs[key] = ctx
	r.mu.Unlock()
	<-ctx.Done()
	return runner.Result{}, ctx.Err()
}

func (r *blockingRunner) ctxOf(key string) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ctxs[key]
}

func waitCtx(t *testing.T, r *blockingRunner, key string) context.Context {
	t.Helper()
	deadline := time.Now().Add(scopeWait)
	for {
		if c := r.ctxOf(key); c != nil {
			return c
		}
		if time.Now().After(deadline) {
			t.Fatalf("%q was never run", key)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Quitting must cancel every in-flight screen read (the runner kills the
// child on cancel), including the 120 s `plugin outdated` check.
func TestQuitCancelsInFlightReads(t *testing.T) {
	r := &blockingRunner{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d := ui.Deps{Runner: r, Scope: runner.ScopeUser, AgentsyncHome: t.TempDir(), Ctx: ctx, Cancel: cancel}
	app := ui.NewApp(d, servers.New(d), plugins.New(d))
	tm := teatest.NewTestModel(t, app, teatest.WithInitialTermSize(120, 30))

	status := waitCtx(t, r, "status --json")
	tm.Send(tea.KeyPressMsg{Code: tea.KeyTab})
	tm.Send(tea.KeyPressMsg{Code: 'o', Text: "o"})
	outdated := waitCtx(t, r, "plugin outdated")

	tm.Send(tea.KeyPressMsg{Code: 'q', Text: "q"})
	tm.WaitFinished(t, teatest.WithFinalTimeout(scopeWait))
	for name, c := range map[string]context.Context{"status --json": status, "plugin outdated": outdated} {
		select {
		case <-c.Done():
		case <-time.After(3 * time.Second):
			t.Errorf("%s: read context not cancelled by quit", name)
		}
	}
}
