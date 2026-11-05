package health

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/runner"
)

// ctxFake blocks every call until its context ends.
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

func (f *ctxFake) Run(ctx context.Context, _ ...string) (runner.Result, error) {
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

func awaitN(t *testing.T, ch <-chan struct{}, n int, what string) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s (%d/%d)", what, i, n)
		}
	}
}

// async runs cmd's batch members concurrently and delivers their messages.
func async(cmd tea.Cmd) <-chan []tea.Msg {
	out := make(chan []tea.Msg, 1)
	go func() {
		var msgs []tea.Msg
		var mu sync.Mutex
		var wg sync.WaitGroup
		m := cmd()
		if b, ok := m.(tea.BatchMsg); ok {
			for _, c := range b {
				wg.Add(1)
				go func() {
					defer wg.Done()
					r := <-async(c)
					mu.Lock()
					msgs = append(msgs, r...)
					mu.Unlock()
				}()
			}
			wg.Wait()
		} else if m != nil {
			msgs = append(msgs, m)
		}
		out <- msgs
	}()
	return out
}

func TestSupersededRefreshesAreCancelled(t *testing.T) {
	fr := newCtxFake()
	s := newScreen(fr)
	t.Cleanup(s.Shutdown)
	async(s.Refresh())
	awaitN(t, fr.started, 2, "the first round")
	for i := 0; i < 20; i++ {
		_, cmd := s.Update(key("r"))
		async(cmd)
		awaitN(t, fr.returned, 2, "the superseded round to be cancelled")
		awaitN(t, fr.started, 2, "the new round")
	}
	if p := fr.peak(); p > 4 {
		t.Fatalf("peak concurrent calls = %d, want <= 4 (one round of two)", p)
	}
	ctxs := fr.ctxList()
	for i, c := range ctxs[:len(ctxs)-2] {
		if c.Err() == nil {
			t.Errorf("superseded call %d: ctx not done", i)
		}
	}
}

func TestCancelledRoundShowsNoNote(t *testing.T) {
	fr := newCtxFake()
	s := newScreen(fr)
	res := async(s.Refresh())
	awaitN(t, fr.started, 2, "the round")
	s.Shutdown()
	awaitN(t, fr.returned, 2, "cancellation")
	deliver(s, <-res)
	if v := view(s); s.doctor.note != "" || s.check.note != "" || strings.Contains(v, "unavailable") {
		t.Fatalf("a cancelled round surfaced as a failure: %q", v)
	}
}

func TestSetScopeCancelsInFlightRound(t *testing.T) {
	fr := newCtxFake()
	s := newScreen(fr)
	async(s.Refresh())
	awaitN(t, fr.started, 2, "the round")
	s.SetScope(newFake(), runner.ScopeProject, "")
	awaitN(t, fr.returned, 2, "SetScope to cancel the round")
	for _, c := range fr.ctxList() {
		if c.Err() == nil {
			t.Error("ctx not cancelled by SetScope")
		}
	}
}
