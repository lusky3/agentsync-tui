package ui

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lusky3/agentsync-tui/internal/runner"
)

// readFake blocks until its context is done and records every context.
type readFake struct {
	mu   sync.Mutex
	ctxs []context.Context
	res  runner.Result
	err  error
}

func (f *readFake) Run(ctx context.Context, _ ...string) (runner.Result, error) {
	f.mu.Lock()
	f.ctxs = append(f.ctxs, ctx)
	f.mu.Unlock()
	if f.err != nil || f.res.ExitCode != 0 || f.res.Stdout != "" {
		return f.res, f.err
	}
	<-ctx.Done()
	return runner.Result{}, ctx.Err()
}

func TestReadNoRunnerAndBound(t *testing.T) {
	if _, err := Read(context.Background(), nil, time.Second, "x"); !errors.Is(err, ErrNoRunner) {
		t.Fatalf("nil runner: %v", err)
	}
	f := &readFake{}
	start := time.Now()
	_, err := Read(context.Background(), f, 20*time.Millisecond, "x")
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatalf("bound not applied: %v after %v", err, time.Since(start))
	}
	// A zero timeout means no bound: only the context ends the read.
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if _, err := Read(ctx, f, 0, "x"); !errors.Is(err, context.Canceled) {
		t.Fatalf("unbounded read: %v", err)
	}
}

func TestReadNote(t *testing.T) {
	if n := ReadNote("agent list", runner.Result{}, nil); n != "" {
		t.Errorf("success: %q", n)
	}
	n := ReadNote("agent list", runner.Result{ExitCode: 2, Stderr: "\nboom token=CANARYrn0123456789abcdefghijk\nmore"}, nil)
	if !strings.HasPrefix(n, "agent list exited 2: boom token=") || strings.Contains(n, "CANARY") {
		t.Errorf("exit note: %q", n)
	}
	if n := ReadNote("x", runner.Result{ExitCode: 1}, nil); n != "x exited 1" {
		t.Errorf("exit without stderr: %q", n)
	}
	if n := ReadNote("x", runner.Result{}, errors.New("ghp_CANARYrn2 spawn")); strings.Contains(n, "CANARY") || !strings.HasPrefix(n, "x unavailable: ") {
		t.Errorf("error note: %q", n)
	}
	if n := ReadNote("x", runner.Result{}, runner.ErrNotInstalled); n != "x unavailable: agentsync is not installed" {
		t.Errorf("not installed: %q", n)
	}
}

func TestRoundsLatestWins(t *testing.T) {
	var r Rounds
	g1, c1 := r.Next(context.Background())
	g2, c2 := r.Next(context.Background())
	if c1.Err() == nil {
		t.Fatal("Next did not cancel the previous round")
	}
	if r.Current(g1) || !r.Current(g2) || c2.Err() != nil {
		t.Fatalf("generations: g1=%d g2=%d current=%v", g1, g2, r.gen)
	}
	r.Supersede()
	if c2.Err() == nil || r.Current(g2) {
		t.Fatal("Supersede must cancel and make the round stale")
	}
	g3, c3 := r.Next(context.Background())
	r.Cancel()
	if c3.Err() == nil || !r.Current(g3) {
		t.Fatal("Cancel must stop the children but keep the generation")
	}
}

func TestVerifierAcceptsOnlyItsCurrentRead(t *testing.T) {
	var a, b Verifier
	cmdA := a.Start(context.Background(), func(context.Context) any { return "a" })
	cmdB := b.Start(context.Background(), func(context.Context) any { return "b" })
	if !a.Busy() || !b.Busy() {
		t.Fatal("Start must set busy")
	}
	mb := cmdB().(VerifiedMsg)
	if _, ok := a.Accept(mb); ok {
		t.Fatal("a accepted b's result")
	}
	ma := cmdA().(VerifiedMsg)
	if v, ok := a.Accept(ma); !ok || v != "a" || a.Busy() {
		t.Fatalf("a did not accept its own result: %v %v busy=%v", v, ok, a.Busy())
	}
	if _, ok := a.Accept(ma); ok {
		t.Fatal("a accepted the same result twice")
	}
	// A read superseded by Invalidate is dropped and its context cancelled.
	var seen context.Context
	cmd := b.Start(context.Background(), func(ctx context.Context) any { seen = ctx; return "late" })
	b.Invalidate()
	if _, ok := b.Accept(cmd().(VerifiedMsg)); ok || b.Busy() || seen.Err() == nil {
		t.Fatal("stale verification accepted or not cancelled")
	}
	if _, ok := b.Accept(mb); ok {
		t.Fatal("old result accepted after Invalidate")
	}
}
