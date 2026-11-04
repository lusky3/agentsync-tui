package ui

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/runner"
)

// chipFake answers by space-joined argv. "version" is always instant. When
// block is set, "check" and "status --exit-code" wait for gate (or ctx).
type chipFake struct {
	mu        sync.Mutex
	calls     []string
	deadlines []bool
	resp      map[string]chipResp
	block     bool
	gate      chan struct{}
	started   chan string
}

type chipResp struct {
	res runner.Result
	err error
}

func newChipFake() *chipFake {
	return &chipFake{resp: map[string]chipResp{}, gate: make(chan struct{}), started: make(chan string, 64)}
}

func (f *chipFake) set(argv string, exit int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resp[argv] = chipResp{res: runner.Result{ExitCode: exit}, err: err}
}

func (f *chipFake) Run(ctx context.Context, args ...string) (runner.Result, error) {
	k := strings.Join(args, " ")
	if k == "version" {
		return runner.Result{Stdout: "agentsync v0.17.0 (commit abc, built today)"}, nil
	}
	f.mu.Lock()
	f.calls = append(f.calls, k)
	_, dl := ctx.Deadline()
	f.deadlines = append(f.deadlines, dl)
	r, block := f.resp[k], f.block
	f.mu.Unlock()
	f.started <- k
	if block && (k == "check" || k == "status --exit-code") {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return runner.Result{}, ctx.Err()
		}
	}
	return r.res, r.err
}

func (f *chipFake) count(argv string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c == argv {
			n++
		}
	}
	return n
}

// exec runs cmd (batches concurrently, like the runtime) and returns every
// message produced.
func execCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	m := cmd()
	b, ok := m.(tea.BatchMsg)
	if !ok {
		if m == nil {
			return nil
		}
		return []tea.Msg{m}
	}
	var mu sync.Mutex
	var out []tea.Msg
	var wg sync.WaitGroup
	for _, c := range b {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := execCmd(c)
			mu.Lock()
			out = append(out, r...)
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

// settle feeds cmd's messages back through Update until nothing is left.
func settle(a *App, cmd tea.Cmd) {
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		for _, m := range execCmd(c) {
			_, next := a.Update(m)
			queue = append(queue, next)
		}
	}
}

func chipApp(fr runner.Runner, screens ...Screen) *App {
	if len(screens) == 0 {
		screens = []Screen{newScreen("A", "a")}
	}
	return NewApp(Deps{Runner: fr, Scope: runner.ScopeUser, AgentsyncHome: "/nonexistent/agentsync"}, screens...)
}

func header(a *App) string { return strings.Split(a.View().Content, "\n")[0] }

func TestChipPendingThenOk(t *testing.T) {
	fr := newChipFake()
	a := chipApp(fr)
	cmd := a.Init()
	if h := header(a); !strings.Contains(h, "…") {
		t.Fatalf("pending chip missing before any result: %q", h)
	}
	settle(a, cmd)
	if h := header(a); !strings.Contains(h, "✓ ok") || strings.Contains(h, "…") {
		t.Fatalf("header = %q", h)
	}
	if fr.count("check") != 1 || fr.count("status --exit-code") != 1 {
		t.Fatalf("calls = %v", fr.calls)
	}
}

func TestStartupChecksDoNotBlockFirstRender(t *testing.T) {
	fr := newChipFake()
	fr.block = true
	a := chipApp(fr)
	initDone := make(chan tea.Cmd, 1)
	go func() { initDone <- a.Init() }() // an inline run would hang here
	var cmd tea.Cmd
	select {
	case cmd = <-initDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Init blocked on the chip checks")
	}
	done := make(chan struct{})
	go func() { defer close(done); settle(a, cmd) }()
	select {
	case <-fr.started: // the chip check is genuinely in flight and blocked
	case <-time.After(timeout):
		t.Fatal("chip check never started")
	}
	rendered := make(chan string, 1)
	go func() { rendered <- header(a) }()
	select {
	case h := <-rendered:
		if !strings.Contains(h, "…") {
			t.Errorf("header while pending = %q", h)
		}
	case <-time.After(timeout):
		t.Fatal("first render blocked on the chip checks")
	}
	close(fr.gate)
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatal("startup never settled")
	}
}

func TestChipCheckFailureWinsOverDrift(t *testing.T) {
	fr := newChipFake()
	fr.set("check", 1, nil)
	fr.set("status --exit-code", 2, nil)
	a := chipApp(fr)
	settle(a, a.Init())
	if h := header(a); !strings.Contains(h, "✗ check failed") || strings.Contains(h, "drift") {
		t.Fatalf("header = %q", h)
	}
}

func TestChipDrift(t *testing.T) {
	fr := newChipFake()
	fr.set("status --exit-code", 2, nil)
	a := chipApp(fr)
	settle(a, a.Init())
	if h := header(a); !strings.Contains(h, "⚠ drift") {
		t.Fatalf("header = %q", h)
	}
}

func TestChipUnavailableOnSpawnErrorAndOtherExit(t *testing.T) {
	cases := []struct {
		name  string
		check chipResp
		stat  chipResp
	}{
		{"check not installed", chipResp{err: runner.ErrNotInstalled}, chipResp{}},
		{"check spawn error", chipResp{err: errors.New("boom")}, chipResp{}},
		{"status spawn error", chipResp{}, chipResp{err: errors.New("boom")}},
		{"status exit 1", chipResp{}, chipResp{res: runner.Result{ExitCode: 1}}},
		{"status exit 3", chipResp{}, chipResp{res: runner.Result{ExitCode: 3}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fr := newChipFake()
			fr.resp["check"], fr.resp["status --exit-code"] = c.check, c.stat
			a := chipApp(fr)
			settle(a, a.Init())
			if h := header(a); !strings.Contains(h, "? unavailable") {
				t.Fatalf("header = %q", h)
			}
		})
	}
}

func TestChipRerunsAfterNonReadOnlyRun(t *testing.T) {
	fr := newChipFake()
	fr.set("status --exit-code", 2, nil)
	a := chipApp(fr)
	settle(a, a.Init())
	if !strings.Contains(header(a), "⚠ drift") {
		t.Fatal("precondition: drift")
	}
	fr.set("status --exit-code", 0, nil) // the apply fixed it
	act := mustAct(actions.Apply(false))
	_, cmd := a.Update(RunMsg{Action: act})
	settle(a, cmd)
	if fr.count("check") != 2 || !strings.Contains(header(a), "✓ ok") {
		t.Fatalf("check calls = %d, header = %q", fr.count("check"), header(a))
	}
}

func TestChipNotRerunByReadOnlyRun(t *testing.T) {
	fr := newChipFake()
	a := chipApp(fr)
	settle(a, a.Init())
	if fr.count("check") != 1 || fr.count("status --exit-code") != 1 {
		t.Fatalf("the chip's own checks re-triggered it: %v", fr.calls)
	}
	// A read-only run (the shell also refreshes the active screen here).
	_, cmd := a.Update(RunMsg{Action: actions.Check()})
	settle(a, cmd)
	_, cmd = a.Update(RunMsg{Action: actions.Doctor()})
	settle(a, cmd)
	if fr.count("check") != 1 || fr.count("status --exit-code") != 1 {
		t.Fatalf("read-only run re-triggered the chip: %v", fr.calls)
	}
}

func TestChipRerunsOnScopeSwitch(t *testing.T) {
	fr, fr2 := newChipFake(), newChipFake()
	fr2.set("status --exit-code", 2, nil)
	d := Deps{Runner: fr, Scope: runner.ScopeUser, ProjectDir: "/proj", AgentsyncHome: "/nonexistent/agentsync"}
	d.WithScope = func(runner.Scope) (runner.Runner, func(...string) *exec.Cmd) { return fr2, nil }
	a := NewApp(d, newScreen("A", "a"))
	settle(a, a.Init())
	if !strings.Contains(header(a), "✓ ok") {
		t.Fatal("precondition: ok")
	}
	_, cmd := a.Update(key("s"))
	if h := header(a); !strings.Contains(h, "…") {
		t.Fatalf("chip should be pending right after the switch: %q", h)
	}
	settle(a, cmd)
	if fr2.count("check") != 1 || fr.count("check") != 1 {
		t.Fatalf("old=%v new=%v", fr.calls, fr2.calls)
	}
	if !strings.Contains(header(a), "⚠ drift") {
		t.Fatalf("header = %q", header(a))
	}
}

func TestChipRerunsOnEditFinished(t *testing.T) {
	fr := newChipFake()
	a := chipApp(fr)
	settle(a, a.Init())
	for _, m := range []EditOutcomeMsg{{Path: "/x", Err: "refused"}, {Path: "/x", Started: true}} {
		_, cmd := a.Update(m)
		settle(a, cmd)
	}
	if fr.count("check") != 1 {
		t.Fatalf("refused/started edit re-ran the chip: %v", fr.calls)
	}
	_, cmd := a.Update(EditOutcomeMsg{Path: "/x", Finished: true})
	settle(a, cmd)
	if fr.count("check") != 2 || fr.count("status --exit-code") != 2 {
		t.Fatalf("calls = %v", fr.calls)
	}
}

func TestChipStaleResultDropped(t *testing.T) {
	fr := newChipFake()
	fr.set("status --exit-code", 2, nil)
	a := chipApp(fr)
	stale := execCmd(a.Init()) // generation 1 resolved to drift, not yet delivered
	fr.set("status --exit-code", 0, nil)
	_, cmd := a.Update(RunMsg{Action: mustAct(actions.Apply(false))})
	fresh := execCmd(cmd) // generation 2 resolved to ok
	for _, m := range fresh {
		a.Update(m)
	}
	for _, m := range stale {
		a.Update(m)
	}
	if h := header(a); !strings.Contains(h, "✓ ok") || strings.Contains(h, "drift") {
		t.Fatalf("a stale result overwrote the chip: %q", h)
	}
}

func TestChipFixedStringsOnly(t *testing.T) {
	fixed := []string{"…", "✓ ok", "✗ check failed", "⚠ drift", "? unavailable"}
	for i := 0; i < 4; i++ {
		fr := newChipFake()
		leak := runner.Result{Stdout: canary + " stdout", Stderr: "password: " + canary + " stderr"}
		switch i {
		case 0:
			leak.ExitCode = 1
			fr.resp["check"] = chipResp{res: leak}
		case 1:
			leak.ExitCode = 2
			fr.resp["status --exit-code"] = chipResp{res: leak}
		case 2:
			fr.resp["check"] = chipResp{err: errors.New("token=" + canary)}
		case 3:
			leak.ExitCode = 7
			fr.resp["status --exit-code"] = chipResp{res: leak}
		}
		a := chipApp(fr)
		settle(a, a.Init())
		h := header(a)
		if strings.Contains(h, canary) || strings.Contains(a.View().Content, canary) {
			t.Fatalf("case %d: command output reached the view: %q", i, h)
		}
		ok := false
		for _, f := range fixed {
			ok = ok || strings.Contains(h, f)
		}
		if !ok {
			t.Fatalf("case %d: header has no fixed chip string: %q", i, h)
		}
	}
}

func TestChipNarrowWidth(t *testing.T) {
	for _, w := range []int{0, 20} {
		fr := newChipFake()
		a := chipApp(fr, newScreen("Servers", "s"), newScreen("Drift", "d"), newScreen("Health", "h"))
		a.Update(tea.WindowSizeMsg{Width: w, Height: 10})
		settle(a, a.Init())
		for _, l := range strings.Split(a.View().Content, "\n") {
			if ansi.StringWidth(l) > w {
				t.Fatalf("width %d: line %q is %d wide", w, l, ansi.StringWidth(l))
			}
		}
		if w == 20 && !strings.Contains(header(a), "✓ ok") {
			t.Errorf("chip lost at width 20: %q", header(a))
		}
	}
}

// The chip's checks are local reads: bounded at min(LocalReadBound,
// --read-timeout) like every other local read.
func TestChipChecksBoundedByTimeout(t *testing.T) {
	fr := newChipFake()
	fr.block = true // never released: only the context can end the call
	a := NewApp(Deps{Runner: fr, Scope: runner.ScopeUser, AgentsyncHome: "/nonexistent/agentsync",
		Timeouts: &Timeouts{Read: 20 * time.Millisecond}}, newScreen("A", "a"))
	done := make(chan struct{})
	go func() { defer close(done); settle(a, a.Init()) }()
	select {
	case <-done:
	case <-time.After(timeout):
		t.Fatal("a hung check was not bounded by the timeout")
	}
	if !strings.Contains(header(a), "? unavailable") {
		t.Fatalf("header = %q", header(a))
	}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if len(fr.deadlines) == 0 || !fr.deadlines[0] {
		t.Fatalf("chip check ran without a deadline: %v", fr.deadlines)
	}
}

func TestQuitNotDelayedByChipChecks(t *testing.T) {
	fr := newChipFake()
	fr.block = true // never released
	t.Cleanup(func() { close(fr.gate) })
	tm := teatest.NewTestModel(t, chipApp(fr), teatest.WithInitialTermSize(100, 30))
	select {
	case <-fr.started:
	case <-time.After(timeout):
		t.Fatal("chip check never started")
	}
	tm.Send(key("q"))
	tm.WaitFinished(t, teatest.WithFinalTimeout(3*time.Second))
}

func TestChipUnavailableWithoutProjectOrRunner(t *testing.T) {
	fr := newChipFake()
	a := NewApp(Deps{Runner: fr, Scope: runner.ScopeProject}, newScreen("A", "a"))
	settle(a, a.Init())
	if h := header(a); !strings.Contains(h, "? unavailable") || len(fr.calls) != 0 {
		t.Fatalf("no project dir: header %q calls %v", h, fr.calls)
	}
	b := NewApp(Deps{Scope: runner.ScopeUser}, newScreen("A", "a"))
	settle(b, b.Init())
	if h := header(b); !strings.Contains(h, "? unavailable") {
		t.Fatalf("no runner: header %q", h)
	}
	// user -> project without a directory drops a pending result.
	fr2 := newChipFake()
	d := Deps{Runner: fr2, Scope: runner.ScopeUser, WithScope: func(runner.Scope) (runner.Runner, func(...string) *exec.Cmd) { return fr2, nil }}
	c := NewApp(d, newScreen("A", "a"))
	stale := execCmd(c.Init())
	c.Update(key("s"))
	for _, m := range stale {
		c.Update(m)
	}
	if h := header(c); !strings.Contains(h, "? unavailable") {
		t.Fatalf("project without dir: header %q", h)
	}
}
