package ui

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/runner"
)

// ctxFake blocks every non-version call until its context ends, recording
// each context and the concurrent / peak number of calls in flight.
type ctxFake struct {
	mu       sync.Mutex
	cur, max int
	ctxs     []context.Context
	started  chan struct{}
	returned chan struct{}
}

func newCtxFake() *ctxFake {
	return &ctxFake{started: make(chan struct{}, 256), returned: make(chan struct{}, 256)}
}

func (f *ctxFake) Run(ctx context.Context, args ...string) (runner.Result, error) {
	if len(args) == 1 && args[0] == "version" {
		return runner.Result{}, nil
	}
	f.mu.Lock()
	f.cur++
	f.max = max(f.max, f.cur)
	f.ctxs = append(f.ctxs, ctx)
	f.mu.Unlock()
	f.started <- struct{}{}
	<-ctx.Done()
	f.mu.Lock()
	f.cur--
	f.mu.Unlock()
	f.returned <- struct{}{}
	return runner.Result{}, ctx.Err()
}

func (f *ctxFake) peak() int { f.mu.Lock(); defer f.mu.Unlock(); return f.max }

func (f *ctxFake) ctxList() []context.Context {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]context.Context(nil), f.ctxs...)
}

func await(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// async executes cmd on its own goroutine and delivers its messages.
func async(cmd tea.Cmd) <-chan []tea.Msg {
	out := make(chan []tea.Msg, 1)
	go func() { out <- execCmd(cmd) }()
	return out
}

func TestChipSupersededRoundsAreCancelled(t *testing.T) {
	fr := newCtxFake()
	d := Deps{Runner: fr, Scope: runner.ScopeUser, ProjectDir: "/proj", AgentsyncHome: "/nonexistent/agentsync"}
	d.WithScope = func(runner.Scope) (runner.Runner, func(...string) *exec.Cmd) { return fr, nil }
	a := NewApp(d, newScreen("A", "a"))
	t.Cleanup(a.shutdown)
	async(a.Init())
	await(t, fr.started, "the startup chip check")
	for i := 0; i < 20; i++ {
		_, cmd := a.Update(key("s"))
		async(cmd)
		// The superseded round must end (be cancelled) before the new one is
		// waited for: a leak would time out here.
		await(t, fr.returned, "the superseded chip check to be cancelled")
		await(t, fr.started, "the new chip check")
	}
	if p := fr.peak(); p > 2 {
		t.Fatalf("peak concurrent chip checks = %d, want <= 2", p)
	}
	ctxs := fr.ctxList()
	for i, c := range ctxs[:len(ctxs)-1] {
		if c.Err() == nil {
			t.Errorf("superseded round %d: ctx not done", i)
		}
	}
	if !IsChipCheck(ctxs[0]) {
		t.Error("derived contexts lost the chip tag")
	}
}

func TestChipQuitCancelsInFlightCheckAndShowsNoFailure(t *testing.T) {
	fr := newCtxFake()
	a := chipApp(fr)
	res := async(a.Init())
	await(t, fr.started, "the chip check")
	_, cmd := a.Update(key("q"))
	if cmd == nil {
		t.Fatal("q returned no quit command")
	}
	await(t, fr.returned, "quit to cancel the in-flight check")
	if fr.ctxList()[0].Err() == nil {
		t.Fatal("ctx not cancelled by quit")
	}
	var msgs []tea.Msg
	select {
	case msgs = <-res:
	case <-time.After(5 * time.Second):
		t.Fatal("round never reported")
	}
	for _, m := range msgs {
		a.Update(m)
	}
	if h := header(a); strings.Contains(h, "unavailable") || strings.Contains(h, "failed") {
		t.Fatalf("a cancelled round surfaced as a failure: %q", h)
	}
}

func TestChipCancelledRoundNeverFlipsChip(t *testing.T) {
	fr := newCtxFake()
	a := chipApp(fr)
	t.Cleanup(a.shutdown)
	res := async(a.Init())
	await(t, fr.started, "the chip check")
	// Cancel without superseding the generation: only the root goes.
	a.rootCancel()
	await(t, fr.returned, "cancellation")
	for _, m := range <-res {
		a.Update(m)
	}
	if h := header(a); !strings.Contains(h, "…") || a.chip != chipPending {
		t.Fatalf("header = %q chip = %v, want the chip still pending", h, a.chip)
	}
}
