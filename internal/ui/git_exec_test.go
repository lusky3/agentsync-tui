// Git actions through the shell: program routing, the R3 bounds, confirm
// chains (R4), tab jumps and shutdown records.
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

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/runner/runnertest"
)

const testTop = "/tmp/repo"

var testRepo = actions.GitRepo{Top: testTop}

func gitDeps(agent, git *runnertest.Fake) Deps {
	d := Deps{Runner: agent, Scope: runner.ScopeUser, AgentsyncHome: "/nonexistent/agentsync"}
	if git != nil {
		d.Git = git
	}
	return d
}

// actionCalls is the agentsync fake's calls without the shell's own
// background reads (the version check and the header chip).
func actionCalls(f *runnertest.Fake) []string {
	calls, ctxs := f.Calls(), f.Ctxs()
	var out []string
	for i, c := range calls {
		if c == "version" || ctxs[i].Value(chipCtxKey{}) != nil {
			continue
		}
		out = append(out, c)
	}
	return out
}

func gitAdd(t *testing.T, paths ...string) actions.Action {
	t.Helper()
	return mustAct(actions.GitAdd(testRepo, paths))
}

func gitCommit(t *testing.T, paths ...string) actions.Action {
	t.Helper()
	return mustAct(actions.GitCommit(testRepo, "fix", paths))
}

func gitPush(t *testing.T) actions.Action {
	t.Helper()
	return mustAct(actions.GitPush(testRepo, "origin", "main", "0123456789abcdef0123456789abcdef01234567"))
}

func gitFetch(t *testing.T) actions.Action {
	t.Helper()
	return mustAct(actions.GitFetch(testRepo, "origin"))
}

// waitRunMsg blocks until a RunMsg has been handled and returns it.
func (h *harness) waitRunMsg() RunMsg {
	h.t.Helper()
	o := h.waitMsg("RunMsg", func(m tea.Msg) bool { _, ok := m.(RunMsg); return ok })
	return o.msg.(RunMsg)
}

func TestGitActionRunsOnGitRunner(t *testing.T) {
	agent, git := runnertest.New(), runnertest.New()
	h := start(t, gitDeps(agent, git), newScreen("A", "a"))
	fetch := gitFetch(t)
	fetch.Interactive = true // never a handover for git
	h.tm.Send(DoMsg{Action: fetch})
	if m := h.waitRunMsg(); m.Err != nil {
		t.Fatalf("fetch: %v", m.Err)
	}
	h.tm.Send(DoMsg{Action: gitPush(t)})
	h.pressAndWait(key("y"))
	if m := h.waitRunMsg(); m.Err != nil {
		t.Fatalf("push: %v", m.Err)
	}
	h.final()
	want := []string{"-C /tmp/repo fetch origin", "-C /tmp/repo push --no-follow-tags --no-recurse-submodules origin 0123456789abcdef0123456789abcdef01234567^{commit}:refs/heads/main"}
	if got := git.Calls(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("git calls = %q, want %q", got, want)
	}
	if got := actionCalls(agent); len(got) != 0 {
		t.Errorf("agentsync runner got %q, want no calls", got)
	}
}

func TestGitActionWithoutGitRunnerFails(t *testing.T) {
	agent := runnertest.New()
	h := start(t, gitDeps(agent, nil), newScreen("A", "a"))
	h.tm.Send(DoMsg{Action: gitFetch(t)})
	m := h.waitRunMsg()
	app := h.final()
	if m.Err == nil || m.Err.Error() != "git is not installed" {
		t.Errorf("Err = %v, want git is not installed", m.Err)
	}
	if !strings.Contains(app.status, "git is not installed") || app.statusKind != statusFail {
		t.Errorf("status = %q (%v)", app.status, app.statusKind)
	}
	if got := actionCalls(agent); len(got) != 0 {
		t.Errorf("agentsync runner got %q", got)
	}
}

func TestGitNetworkActionBoundedByRead(t *testing.T) {
	git := runnertest.New().Block()
	d := gitDeps(runnertest.New(), git)
	d.Timeouts = &Timeouts{Read: 50 * time.Millisecond}
	h := start(t, d, newScreen("A", "a"))
	h.tm.Send(DoMsg{Action: gitPush(t)})
	h.pressAndWait(key("y"))
	m := h.waitRunMsg()
	app := h.final()
	if !errors.As(m.Err, new(timeoutError)) {
		t.Fatalf("Err = %v, want a timeout", m.Err)
	}
	if !strings.Contains(app.status, "timed out after") {
		t.Errorf("status = %q", app.status)
	}
}

// gitDeadline runs a confirmed git write with the given Timeouts and
// returns how far away its context's deadline was when the run began.
func gitDeadline(t *testing.T, lim Timeouts, act actions.Action) (time.Duration, bool) {
	t.Helper()
	git := runnertest.New()
	d := gitDeps(runnertest.New(), git)
	d.Timeouts = &lim
	h := start(t, d, newScreen("A", "a"))
	before := time.Now()
	h.tm.Send(DoMsg{Action: act})
	h.pressAndWait(key("y"))
	h.waitRunMsg()
	h.final()
	ctxs := git.Ctxs()
	if len(ctxs) != 1 {
		t.Fatalf("git calls = %q", git.Calls())
	}
	dl, ok := ctxs[0].Deadline()
	return dl.Sub(before), ok
}

func TestGitLocalWriteDefaultBound(t *testing.T) {
	if GitLocalWriteBound != 2*time.Minute {
		t.Fatalf("GitLocalWriteBound = %v", GitLocalWriteBound)
	}
	left, ok := gitDeadline(t, Timeouts{Read: DefaultReadTimeout}, gitCommit(t, "a.txt"))
	if !ok {
		t.Fatal("a local git write ran without a deadline")
	}
	if left > GitLocalWriteBound+time.Second || left < GitLocalWriteBound-10*time.Second {
		t.Errorf("deadline %v away, want about %v", left, GitLocalWriteBound)
	}
}

func TestGitLocalWriteUsesWriteWhenSet(t *testing.T) {
	left, ok := gitDeadline(t, Timeouts{Read: DefaultReadTimeout, Write: 5 * time.Minute}, gitAdd(t, "a.txt"))
	if !ok || left > 5*time.Minute+time.Second || left < 5*time.Minute-10*time.Second {
		t.Errorf("deadline %v away (set %v), want about 5m", left, ok)
	}
}

// TestGitLimitTable pins R3 for every kind of git action.
func TestGitLimitTable(t *testing.T) {
	local := actions.Action{Program: actions.ProgramGit, Argv: []string{"-C", testTop, "status"}, ReadOnly: true}
	cases := []struct {
		name string
		act  actions.Action
		lim  Timeouts
		want time.Duration
	}{
		{"fetch", gitFetch(t), Timeouts{Read: time.Minute}, time.Minute},
		{"push", gitPush(t), Timeouts{Read: time.Minute, Write: time.Hour}, time.Minute},
		{"pull, read bound lifted", mustAct(actions.GitPull(testRepo)), Timeouts{}, DefaultReadTimeout},
		{"commit, no write bound", gitCommit(t, "a"), Timeouts{}, GitLocalWriteBound},
		{"add, write bound", gitAdd(t, "a"), Timeouts{Write: time.Hour}, time.Hour},
		{"local read", local, Timeouts{Read: 10 * time.Second}, 10 * time.Second},
		{"local read, default", local, DefaultTimeouts, LocalReadBound},
	}
	for _, c := range cases {
		if got := gitLimit(c.act, c.lim); got != c.want {
			t.Errorf("%s: gitLimit = %v, want %v", c.name, got, c.want)
		}
	}
}

// runSeen is a screen that records every RunMsg broadcast to it.
type runSeen struct {
	*fakeScreen
	rmu  sync.Mutex
	runs []string
}

func (s *runSeen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m, ok := msg.(RunMsg); ok {
		s.rmu.Lock()
		s.runs = append(s.runs, m.Action.Title)
		s.rmu.Unlock()
	}
	s.fakeScreen.Update(msg)
	return s, nil
}

func (s *runSeen) seen() []string {
	s.rmu.Lock()
	defer s.rmu.Unlock()
	return append([]string(nil), s.runs...)
}

func TestChainRunsSecondOnlyAfterSuccess(t *testing.T) {
	git := runnertest.New()
	h := start(t, gitDeps(runnertest.New(), git), newScreen("A", "a"))
	add, commit := gitAdd(t, "a.txt"), gitCommit(t, "a.txt")
	h.tm.Send(DoChain(runner.ScopeUser, add, commit)())
	h.pressAndWait(key("y"))
	first := h.waitRunMsg()
	second := h.waitRunMsg()
	app := h.final()
	if first.Action.Key() != add.Key() || second.Action.Key() != commit.Key() {
		t.Errorf("ran %q then %q", first.Action.Title, second.Action.Title)
	}
	want := []string{"-C /tmp/repo add -- a.txt", "-C /tmp/repo commit --message=fix -- a.txt"}
	if got := git.Calls(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("git calls = %q", got)
	}
	if app.status != "done: Commit 1 file" {
		t.Errorf("status = %q", app.status)
	}
}

// R4: the second step starts only after the first has returned. The add
// is held in flight; the commit must not reach the runner until it is
// released.
func TestChainSecondStepWaitsForFirst(t *testing.T) {
	add, commit := gitAdd(t, "a.txt"), gitCommit(t, "a.txt")
	addKey := strings.Join(add.Argv, " ")
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	git := runnertest.New()
	git.Guard = func(argv string) {
		if argv == addKey {
			close(started)
			<-release
		}
	}
	h := start(t, gitDeps(runnertest.New(), git), newScreen("A", "a"))
	h.tm.Send(DoChain(runner.ScopeUser, add, commit)())
	h.pressAndWait(key("y"))
	select {
	case <-started:
	case <-time.After(timeout):
		t.Fatal("the add never started")
	}
	time.Sleep(50 * time.Millisecond) // room for a wrongly early commit to start
	if got := git.Calls(); len(got) != 0 {
		t.Fatalf("while the add was in flight the runner saw %q; the commit started early", got)
	}
	unblock()
	if first, second := h.waitRunMsg(), h.waitRunMsg(); first.Action.Key() != add.Key() || second.Action.Key() != commit.Key() {
		t.Errorf("ran %q then %q", first.Action.Title, second.Action.Title)
	}
	h.final()
	want := []string{addKey, strings.Join(commit.Argv, " ")}
	if got := git.Calls(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("git calls = %q, want %q", got, want)
	}
}

func TestChainStopsOnFailureAndSaysWhatDidNotRun(t *testing.T) {
	add := gitAdd(t, "a.txt")
	git := runnertest.New().On(strings.Join(add.Argv, " "), runner.Result{ExitCode: 1, Stderr: "fatal: pathspec"}, nil)
	h := start(t, gitDeps(runnertest.New(), git), newScreen("A", "a"))
	h.tm.Send(DoChain(runner.ScopeUser, add, gitCommit(t, "a.txt"), gitPush(t))())
	h.pressAndWait(key("y"))
	o := h.waitMsg("RunMsg", func(m tea.Msg) bool { _, ok := m.(RunMsg); return ok })
	if o.running || o.modalOpen {
		t.Errorf("after the failure: running=%v modal=%v, want idle", o.running, o.modalOpen)
	}
	app := h.final()
	if got := git.Calls(); len(got) != 1 {
		t.Errorf("git calls = %q, want only the add", got)
	}
	if !strings.HasPrefix(app.status, "failed (exit 1): fatal: pathspec") ||
		!strings.HasSuffix(app.status, "; not run: Commit 1 file, Push") {
		t.Errorf("status = %q", app.status)
	}
	if n := len(app.log.entries()); n != 1 {
		t.Errorf("log entries = %d, want 1", n)
	}
}

func TestChainLogsEachStep(t *testing.T) {
	git := runnertest.New()
	s := &runSeen{fakeScreen: newScreen("A", "a")}
	h := start(t, gitDeps(runnertest.New(), git), s)
	add, commit := gitAdd(t, "a.txt"), gitCommit(t, "a.txt")
	h.tm.Send(DoChain(runner.ScopeUser, add, commit)())
	h.pressAndWait(key("y"))
	h.waitRunMsg()
	h.waitRunMsg()
	app := h.final()
	es := app.log.entries()
	if len(es) != 2 || es[0].Display != add.Display() || es[1].Display != commit.Display() {
		t.Fatalf("log = %+v", es)
	}
	for _, e := range es {
		if !strings.HasPrefix(e.Display, "git -C /tmp/repo ") || e.Outcome != "exit 0" {
			t.Errorf("entry %+v", e)
		}
	}
	if got := s.seen(); strings.Join(got, "|") != "Stage 1 file|Commit 1 file" {
		t.Errorf("broadcast RunMsgs = %q", got)
	}
}

func TestChainOneConfirm(t *testing.T) {
	git := runnertest.New()
	h := start(t, gitDeps(runnertest.New(), git), newScreen("A", "a"))
	h.tm.Send(DoChain(runner.ScopeUser, gitAdd(t, "a.txt"), gitCommit(t, "a.txt"))())
	if o := h.waitMsg("DoMsg", func(m tea.Msg) bool { _, ok := m.(DoMsg); return ok }); !o.modalOpen {
		t.Fatal("the chain did not open a modal")
	}
	if o := h.pressAndWait(key("y")); o.modalOpen {
		t.Fatal("modal still open after y")
	}
	runs := 0
	for runs < 2 {
		o := h.waitMsg("RunMsg", func(tea.Msg) bool { return true })
		if o.modalOpen {
			t.Fatalf("a second modal opened (after %T)", o.msg)
		}
		if _, ok := o.msg.(RunMsg); ok {
			runs++
		}
	}
	h.final()
	if n := len(git.Calls()); n != 2 {
		t.Errorf("git calls = %d, want 2", n)
	}
}

func TestChainRefusedAfterScopeSwitch(t *testing.T) {
	agent, git := runnertest.New(), runnertest.New()
	d := gitDeps(agent, git)
	d.ProjectDir = "/tmp/proj"
	d.WithScope = func(runner.Scope) (runner.Runner, func(...string) *exec.Cmd) { return agent, nil }
	doctor := actions.Action{Title: "Doctor", Argv: []string{"doctor"}} // scope-unaware on its own
	chains := map[string][]actions.Action{
		"git":                      {gitAdd(t, "a.txt"), gitCommit(t, "a.txt")},
		"scope-unaware first step": {doctor, gitCommit(t, "a.txt")},
	}
	for name, chain := range chains {
		t.Run(name, func(t *testing.T) {
			app := NewApp(d, newScreen("A", "a"))
			app.Update(key("s"))
			if app.scope != runner.ScopeProject {
				t.Fatalf("scope = %q", app.scope)
			}
			_, cmd := app.Update(DoChain(runner.ScopeUser, chain[0], chain[1:]...)())
			if cmd != nil || app.modal != nil || app.running != nil {
				t.Fatal("the chain was not refused")
			}
			if !strings.Contains(app.status, "scope changed since it was built") {
				t.Errorf("status = %q", app.status)
			}
		})
	}
	if n := len(git.Calls()) + len(actionCalls(agent)); n != 0 {
		t.Errorf("%d commands ran", n)
	}
}

func TestGotoSelectsByTitleAndRefreshesStale(t *testing.T) {
	a, drift, dup := newScreen("A", "a"), newScreen("Drift", "d"), newScreen("Drift", "x")
	app := NewApp(baseDeps(newFakeRunner()), a, drift, dup)
	app.Update(GotoMsg{Title: "Drift"})
	if app.active != 1 {
		t.Fatalf("active = %d, want the first Drift (1)", app.active)
	}
	waitRefresh(t, drift)
	app.Update(GotoMsg{Title: "A"})
	app.Update(GotoMsg{Title: "Drift"})
	if app.active != 1 {
		t.Fatalf("active = %d", app.active)
	}
	select {
	case <-drift.refreshed:
		t.Error("a fresh screen was refreshed again")
	default:
	}
	if len(dup.refreshed) != 0 {
		t.Error("the second screen with the title was refreshed")
	}
}

func TestGotoUnknownTitleWarns(t *testing.T) {
	app := NewApp(baseDeps(newFakeRunner()), newScreen("A", "a"), newScreen("B", "b"))
	app.Update(GotoMsg{Title: "Nope\x1b[31m"})
	if app.active != 0 {
		t.Errorf("active = %d", app.active)
	}
	if app.statusKind != statusWarn || !strings.Contains(app.status, "Nope") || strings.Contains(app.status, "\x1b") {
		t.Errorf("status = %q (%v)", app.status, app.statusKind)
	}
}

// A tab jump is refused while a modal is open or an action runs.
func TestGotoRefusedWhileBusy(t *testing.T) {
	app := NewApp(baseDeps(newFakeRunner()), newScreen("A", "a"), newScreen("B", "b"))
	app.Update(DoMsg{Action: actions.PluginUpgradeAll()})
	if app.modal == nil {
		t.Fatal("precondition: modal open")
	}
	app.Update(GotoMsg{Title: "B"})
	if app.active != 0 {
		t.Errorf("active = %d with a modal open, want 0", app.active)
	}
	if app.statusKind != statusWarn || !strings.HasPrefix(app.status, "busy:") {
		t.Errorf("status = %q (%v)", app.status, app.statusKind)
	}
	app.Update(key("y")) // the write is running; its cmd is never executed
	if app.running == nil {
		t.Fatal("precondition: write running")
	}
	app.Update(GotoMsg{Title: "B"})
	if app.active != 0 {
		t.Errorf("active = %d with an action running, want 0", app.active)
	}
}

func TestRecordShutdownShowsGitProgram(t *testing.T) {
	app := NewApp(baseDeps(newFakeRunner()), newScreen("A", "a"))
	app.RecordShutdown([]runner.Ended{
		{Program: "git", Args: []string{"-C", testTop, "push"}, Outcome: "killed"},
		{Args: []string{"apply"}, Outcome: "exited"},
	})
	es := app.log.entries()
	if len(es) != 2 || es[0].Display != "git -C /tmp/repo push" || es[1].Display != "agentsync apply" {
		t.Errorf("log = %+v", es)
	}
}

func TestRunMsgStderrWithTokenURLMaskedInStatus(t *testing.T) {
	app := NewApp(gitDeps(runnertest.New(), runnertest.New()), newScreen("A", "a"))
	app.Update(RunMsg{Action: gitPush(t), Result: runner.Result{ExitCode: 128,
		Stderr: "fatal: unable to access 'https://u:ghp_x@github.com/o/r/'"}})
	if !strings.HasPrefix(app.status, "failed (exit 128): fatal: unable to access") {
		t.Errorf("status = %q", app.status)
	}
	for _, leak := range []string{"ghp_x", "u:"} {
		if strings.Contains(app.status, leak) || strings.Contains(app.statusLine(), leak) {
			t.Errorf("status leaks %q: %q", leak, app.status)
		}
	}
}

// runStatus words every way a run can end; a failed exit with no stderr
// names the action instead.
func TestRunStatus(t *testing.T) {
	read := actions.Action{Title: "Doctor\x1b[31m", Argv: []string{"doctor"}, ReadOnly: true}
	write := gitPush(t)
	for _, c := range []struct {
		name string
		m    RunMsg
		kind statusKind
		want string
	}{
		{"done", RunMsg{Action: write}, statusOK, "done: Push"},
		{"interrupted write", RunMsg{Action: write, Err: context.Canceled}, statusWarn, "interrupted: Push"},
		{"cancelled read", RunMsg{Action: read, Err: context.Canceled}, statusWarn, "cancelled: Doctor"},
		{"timeout", RunMsg{Action: read, Err: timeoutError{2 * time.Second}}, statusFail, "failed: Doctor: " + timeoutError{2 * time.Second}.Error()},
		{"error", RunMsg{Action: write, Err: errors.New("boom\nmore")}, statusFail, "error: Push: boom"},
		{"exit with stderr", RunMsg{Action: write, Result: runner.Result{ExitCode: 2, Stderr: "fatal: x\nmore"}}, statusFail, "failed (exit 2): fatal: x"},
		{"exit without stderr", RunMsg{Action: write, Result: runner.Result{ExitCode: 1}}, statusFail, "failed (exit 1): Push"},
	} {
		if kind, got := runStatus(c.m); kind != c.kind || got != c.want {
			t.Errorf("%s: runStatus = %v %q, want %v %q", c.name, kind, got, c.kind, c.want)
		}
	}
}
