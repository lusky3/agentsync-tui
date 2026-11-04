package ui

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/runner"
)

const (
	canary  = "CANARYc4n4ry0123456789abcdef"
	timeout = 10 * time.Second
)

// ---- fakes ----------------------------------------------------------------

type fakeResp struct {
	res runner.Result
	err error
}

// fakeRunner answers "version" immediately and blocks every other call until
// the test feeds a response (or the context is cancelled).
type fakeRunner struct {
	version    string
	versionErr error

	mu      sync.Mutex
	calls   [][]string
	ctxErrs []error

	started chan []string
	respond chan fakeResp
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{
		version: "agentsync v0.17.0 (commit abc, built today)",
		started: make(chan []string, 16),
		respond: make(chan fakeResp, 16),
	}
}

func (f *fakeRunner) Run(ctx context.Context, args ...string) (runner.Result, error) {
	if len(args) == 1 && args[0] == "version" {
		return runner.Result{Stdout: f.version}, f.versionErr
	}
	f.mu.Lock()
	f.calls = append(f.calls, args)
	f.mu.Unlock()
	f.started <- args
	select {
	case r := <-f.respond:
		return r.res, r.err
	case <-ctx.Done():
		f.mu.Lock()
		f.ctxErrs = append(f.ctxErrs, ctx.Err())
		f.mu.Unlock()
		return runner.Result{}, ctx.Err()
	}
}

func (f *fakeRunner) nCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type fakeScreen struct {
	title     string
	body      string
	capture   bool
	refreshed chan struct{}

	mu     sync.Mutex
	keys   []string
	width  int
	height int
}

func newScreen(title, body string) *fakeScreen {
	return &fakeScreen{title: title, body: body, refreshed: make(chan struct{}, 32)}
}

func (s *fakeScreen) Init() tea.Cmd { return nil }
func (s *fakeScreen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
	case tea.KeyPressMsg:
		s.keys = append(s.keys, msg.String())
	}
	return s, nil
}
func (s *fakeScreen) View() tea.View     { return tea.NewView(s.body) }
func (s *fakeScreen) Title() string      { return s.title }
func (s *fakeScreen) CapturesKeys() bool { return s.capture }
func (s *fakeScreen) Refresh() tea.Cmd {
	s.refreshed <- struct{}{}
	return nil
}

// probe wraps the App and reports every processed message, so tests can wait
// for a message to have been handled instead of sleeping.
type probe struct {
	*App
	seen chan tea.Msg
}

func (p probe) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := p.App.Update(msg)
	select {
	case p.seen <- msg:
	default:
	}
	return p, cmd
}

type harness struct {
	t   *testing.T
	tm  *teatest.TestModel
	p   probe
	run *fakeRunner
}

func start(t *testing.T, d Deps, screens ...Screen) *harness {
	t.Helper()
	fr, _ := d.Runner.(*fakeRunner)
	p := probe{App: NewApp(d, screens...), seen: make(chan tea.Msg, 4096)}
	tm := teatest.NewTestModel(t, p, teatest.WithInitialTermSize(100, 30))
	return &harness{t: t, tm: tm, p: p, run: fr}
}

func baseDeps(fr *fakeRunner) Deps {
	return Deps{Runner: fr, Scope: runner.ScopeUser, AgentsyncHome: "/nonexistent/agentsync"}
}

// waitMsg blocks until a processed message satisfies match.
func (h *harness) waitMsg(what string, match func(tea.Msg) bool) {
	h.t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case m := <-h.p.seen:
			if match(m) {
				return
			}
		case <-deadline:
			h.t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func (h *harness) waitRun() {
	h.waitMsg("RunMsg", func(m tea.Msg) bool { _, ok := m.(RunMsg); return ok })
}
func (h *harness) waitVersion() {
	h.waitMsg("versionMsg", func(m tea.Msg) bool { _, ok := m.(versionMsg); return ok })
}

func (h *harness) waitStarted() []string {
	h.t.Helper()
	select {
	case a := <-h.run.started:
		return a
	case <-time.After(timeout):
		h.t.Fatal("runner was never called")
		return nil
	}
}

func waitRefresh(t *testing.T, s *fakeScreen) {
	t.Helper()
	select {
	case <-s.refreshed:
	case <-time.After(timeout):
		t.Fatal("screen was never refreshed")
	}
}

// final quits the program and returns the final App.
func (h *harness) final() *App {
	h.t.Helper()
	_ = h.tm.Quit()
	return h.tm.FinalModel(h.t, teatest.WithFinalTimeout(timeout)).(probe).App
}

func content(a *App) string { return a.View().Content }

func key(s string) tea.KeyPressMsg {
	switch s {
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "shift+tab":
		return tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "ctrl+c":
		return tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

func mustAct(a actions.Action, err error) actions.Action {
	if err != nil {
		panic(err)
	}
	return a
}

// ---- tests ----------------------------------------------------------------

func TestVerb(t *testing.T) {
	cases := []struct {
		argv []string
		want string
	}{
		{[]string{"mcp", "remove", "x"}, "remove"},
		{[]string{"agent", "remove", "claude"}, "remove"},
		{[]string{"plugin", "remove", "p"}, "remove"},
		{[]string{"marketplace", "remove", "m"}, "remove"},
		{[]string{"secret", "remove", "K"}, "remove"},
		{[]string{"revert", "--all"}, "revert"},
		{[]string{"apply"}, "apply"},
		{[]string{"mcp"}, "mcp"},
		{nil, ""},
	}
	for _, c := range cases {
		if got := Verb(actions.Action{Argv: c.argv}); got != c.want {
			t.Errorf("Verb(%q) = %q, want %q", c.argv, got, c.want)
		}
	}
}

func TestNoScreensEmptyState(t *testing.T) {
	h := start(t, baseDeps(newFakeRunner()))
	if !strings.Contains(content(h.final()), "No screens registered") {
		t.Error("empty state missing")
	}
}

func TestQuitKey(t *testing.T) {
	h := start(t, baseDeps(newFakeRunner()), newScreen("A", "body-a"))
	h.tm.Send(key("q"))
	h.tm.WaitFinished(t, teatest.WithFinalTimeout(timeout))
}

func TestTabSwitching(t *testing.T) {
	a, b, c := newScreen("Alpha", "body-alpha"), newScreen("Beta", "body-beta"), newScreen("Gamma", "body-gamma")
	h := start(t, baseDeps(newFakeRunner()), a, b, c)
	h.tm.Send(key("tab"))
	h.tm.Send(key("tab"))
	h.tm.Send(key("tab")) // wraps to Alpha
	h.tm.Send(key("shift+tab"))
	h.tm.Send(key("2"))
	h.tm.Send(key("9")) // out of range: ignored
	app := h.final()
	v := content(app)
	if app.active != 1 || !strings.Contains(v, "body-beta") || strings.Contains(v, "body-alpha") {
		t.Errorf("active=%d view=%q", app.active, v)
	}
	for _, title := range []string{"Alpha", "Beta", "Gamma"} {
		if !strings.Contains(v, title) {
			t.Errorf("tab %q missing", title)
		}
	}
}

func TestResizeReflows(t *testing.T) {
	long := strings.Repeat("wide ", 60)
	s := newScreen("A", long+"\n"+strings.Repeat("line\n", 80))
	h := start(t, baseDeps(newFakeRunner()), s)
	h.tm.Send(tea.WindowSizeMsg{Width: 40, Height: 15})
	h.waitMsg("resize", func(m tea.Msg) bool { w, ok := m.(tea.WindowSizeMsg); return ok && w.Width == 40 })
	app := h.final()
	lines := strings.Split(content(app), "\n")
	if len(lines) > 15 {
		t.Errorf("view has %d lines, want <= 15", len(lines))
	}
	for _, l := range lines {
		if w := lipgloss.Width(l); w > 40 {
			t.Errorf("line width %d > 40: %q", w, l)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.width != 40 || s.height <= 0 || s.height >= 15 {
		t.Errorf("screen got size %dx%d", s.width, s.height)
	}
}

func TestConfirmModalCancelDoesNotRun(t *testing.T) {
	fr := newFakeRunner()
	h := start(t, baseDeps(fr), newScreen("A", "a"))
	act := mustAct(actions.McpEnable("x"))
	h.tm.Send(h.p.Do(act)())
	h.tm.Send(key("esc"))
	app := h.final()
	if n := fr.nCalls(); n != 0 {
		t.Errorf("runner called %d times after esc", n)
	}
	if app.modal != nil {
		t.Error("modal still open")
	}
}

func TestConfirmModalRunsOnYes(t *testing.T) {
	fr := newFakeRunner()
	s := newScreen("A", "a")
	h := start(t, baseDeps(fr), s)
	act := mustAct(actions.McpEnable("x"))
	h.tm.Send(DoMsg{Action: act})
	h.tm.Send(key("y"))
	if got := h.waitStarted(); strings.Join(got, " ") != "mcp enable x" {
		t.Errorf("ran %q", got)
	}
	fr.respond <- fakeResp{res: runner.Result{ExitCode: 0}}
	h.waitRun()
	waitRefresh(t, s)
	app := h.final()
	if len(app.log.entries()) != 1 {
		t.Errorf("log entries = %d", len(app.log.entries()))
	}
}

func TestConfirmModalShowsMaskedDisplayAndBadges(t *testing.T) {
	app := NewApp(baseDeps(newFakeRunner()), newScreen("A", "a"))
	act := actions.Action{Title: "Add server", Argv: []string{"mcp", "add", "x", "--header", "Authorization: Bearer " + canary}, Network: true, Interactive: true}
	app.Update(DoMsg{Action: act})
	v := content(app)
	if strings.Contains(v, canary) {
		t.Fatal("modal leaked the canary")
	}
	for _, want := range []string{"Add server", "agentsync mcp add x", "network", "interactive"} {
		if !strings.Contains(v, want) {
			t.Errorf("modal missing %q in %q", want, v)
		}
	}
}

func TestDestructiveNeedsTypedConfirm(t *testing.T) {
	fr := newFakeRunner()
	h := start(t, baseDeps(fr), newScreen("A", "a"))
	act := mustAct(actions.McpRemove("x"))
	if !act.Destructive {
		t.Fatal("McpRemove should be destructive")
	}
	h.tm.Send(DoMsg{Action: act})
	h.tm.Send(key("y"))
	h.tm.Send(key("enter"))
	h.tm.Send(key("backspace"))
	h.tm.Type("remove")
	h.tm.Send(key("enter"))
	if got := h.waitStarted(); strings.Join(got, " ") != "mcp remove x" {
		t.Errorf("ran %q", got)
	}
	if n := fr.nCalls(); n != 1 {
		t.Errorf("runner called %d times, want 1 (y/enter must not confirm)", n)
	}
	fr.respond <- fakeResp{}
	h.waitRun()
	h.final()
}

func TestReadOnlyActionSkipsModal(t *testing.T) {
	fr := newFakeRunner()
	h := start(t, baseDeps(fr), newScreen("A", "a"))
	h.tm.Send(DoMsg{Action: actions.Check()})
	if got := h.waitStarted(); got[0] != "check" {
		t.Errorf("ran %q", got)
	}
	fr.respond <- fakeResp{}
	h.waitRun()
	app := h.final()
	if app.modal != nil {
		t.Error("modal opened for read-only action")
	}
	if len(app.log.entries()) != 1 {
		t.Error("read-only action was not logged")
	}
}

func TestActivityLogMasksArgv(t *testing.T) {
	fr := newFakeRunner()
	d := baseDeps(fr)
	d.LogPath = filepath.Join(t.TempDir(), "state", "agentsync-tui", "activity.log")
	h := start(t, d, newScreen("A", "a"))
	act := actions.Action{Title: "Add", Argv: []string{"mcp", "add", "x", "--header", "Authorization: Bearer " + canary}}
	h.tm.Send(DoMsg{Action: act})
	h.tm.Send(key("y"))
	h.waitStarted()
	fr.respond <- fakeResp{res: runner.Result{Stdout: "out " + canary, Stderr: "STDERR-TEXT", ExitCode: 3}}
	h.waitRun()
	h.tm.Send(key("l"))
	app := h.final()
	v := content(app)
	if strings.Contains(v, canary) {
		t.Error("View leaked canary")
	}
	if !strings.Contains(v, "agentsync mcp add x") {
		t.Errorf("log pane missing entry: %q", v)
	}
	for _, e := range app.log.entries() {
		if strings.Contains(e.String(), canary) {
			t.Error("ring leaked canary")
		}
	}
	b, err := os.ReadFile(d.LogPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(b, []byte(canary)) || bytes.Contains(b, []byte("STDERR-TEXT")) || bytes.Contains(b, []byte("out ")) {
		t.Errorf("log file leaked: %q", b)
	}
	if !bytes.Contains(b, []byte("exit 3")) {
		t.Errorf("log file missing exit code: %q", b)
	}
	if fi, _ := os.Stat(d.LogPath); fi.Mode().Perm() != 0o600 {
		t.Errorf("log perms %v", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Dir(d.LogPath)); fi.Mode().Perm() != 0o700 {
		t.Errorf("log dir perms %v", fi.Mode().Perm())
	}
}

func TestActivityRingIsBounded(t *testing.T) {
	l := newActivityLog("")
	for i := 0; i < 250; i++ {
		l.add(entry{Display: "agentsync check"})
	}
	if n := len(l.entries()); n != 200 {
		t.Errorf("ring holds %d, want 200", n)
	}
}

func TestOutputIsSanitisedAndMasked(t *testing.T) {
	fr := newFakeRunner()
	h := start(t, baseDeps(fr), newScreen("A", "a"))
	h.tm.Send(DoMsg{Action: actions.Check()})
	h.waitStarted()
	fr.respond <- fakeResp{res: runner.Result{Stderr: "\x1b[2Jfatal: Authorization: Bearer " + canary + "\nsecond line", ExitCode: 1}}
	h.waitRun()
	v := content(h.final())
	if strings.Contains(v, "\x1b[2J") || strings.Contains(v, canary) {
		t.Errorf("unsafe output in view: %q", v)
	}
	if !strings.Contains(v, "fatal: Authorization: Bearer") || !strings.Contains(v, "exit 1") {
		t.Errorf("failure banner missing: %q", v)
	}
}

func TestErrorIsMasked(t *testing.T) {
	fr := newFakeRunner()
	h := start(t, baseDeps(fr), newScreen("A", "a"))
	h.tm.Send(DoMsg{Action: actions.Check()})
	h.waitStarted()
	fr.respond <- fakeResp{err: errors.New("spawn: \x1b]0;evil\x07Bearer " + canary)}
	h.waitRun()
	v := content(h.final())
	if strings.Contains(v, canary) || strings.Contains(v, "evil") {
		t.Errorf("unsafe error in view: %q", v)
	}
}

func TestMissingAgentsyncShowsBanner(t *testing.T) {
	fr := newFakeRunner()
	fr.versionErr = runner.ErrNotInstalled
	h := start(t, baseDeps(fr), newScreen("A", "a"))
	h.waitVersion()
	v := content(h.final())
	if !strings.Contains(v, "agentsync not found on PATH; install: mise use -g go:github.com/spxrogers/agentsync/cmd/agentsync") {
		t.Errorf("install banner missing: %q", v)
	}
}

func TestUnsupportedVersionShowsWarningNotError(t *testing.T) {
	fr := newFakeRunner()
	fr.version = "agentsync v0.18.2 (commit abc, built today)"
	h := start(t, baseDeps(fr), newScreen("A", "body-a"))
	h.waitVersion()
	v := content(h.final())
	if !strings.Contains(v, "agentsync v0.18.2 (tested with v0.17.0)") {
		t.Errorf("warning missing: %q", v)
	}
	if strings.Contains(v, "not found") || !strings.Contains(v, "body-a") {
		t.Errorf("unsupported version treated as fatal: %q", v)
	}
}

func TestDevBuildShowsInfoNotWarning(t *testing.T) {
	fr := newFakeRunner()
	fr.version = "agentsync dev (commit none, built unknown)"
	h := start(t, baseDeps(fr), newScreen("A", "a"))
	h.waitVersion()
	v := content(h.final())
	if !strings.Contains(v, "agentsync dev build (version unknown)") {
		t.Errorf("dev note missing: %q", v)
	}
	if strings.Contains(v, "tested with") {
		t.Errorf("dev build shown as warning: %q", v)
	}
}

func TestVersionCheckOtherErrorIsDimNote(t *testing.T) {
	fr := newFakeRunner()
	fr.versionErr = errors.New("boom Bearer " + canary)
	h := start(t, baseDeps(fr), newScreen("A", "body-a"))
	h.waitVersion()
	v := content(h.final())
	if strings.Contains(v, canary) || !strings.Contains(v, "body-a") || !strings.Contains(v, "version check failed") {
		t.Errorf("view: %q", v)
	}
}

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
	waitRefresh(t, a)
	waitRefresh(t, b)
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

func TestInteractiveHandoffRestoresTerminal(t *testing.T) {
	fr := newFakeRunner()
	d := baseDeps(fr)
	var gotArgs []string
	d.Interactive = func(args ...string) *exec.Cmd {
		gotArgs = args
		return exec.Command("true")
	}
	s := newScreen("A", "a")
	h := start(t, d, s)
	act := mustAct(actions.SecretSet("GG_TOKEN"))
	if !act.Interactive {
		t.Fatal("SecretSet should be interactive")
	}
	h.tm.Send(DoMsg{Action: act})
	h.tm.Send(key("y"))
	h.waitRun()
	waitRefresh(t, s)
	altOn := []byte("\x1b[?1049h")
	teatest.WaitFor(t, h.tm.Output(), func(b []byte) bool { return bytes.Count(b, altOn) >= 2 },
		teatest.WithDuration(timeout))
	app := h.final()
	if strings.Join(gotArgs, " ") != strings.Join(act.Argv, " ") || fr.nCalls() != 0 {
		t.Errorf("interactive args %q, runner calls %d", gotArgs, fr.nCalls())
	}
	if !app.View().AltScreen {
		t.Error("view does not request the alt screen")
	}
}

func TestCtrlCCancelsRunningAction(t *testing.T) {
	fr := newFakeRunner()
	h := start(t, baseDeps(fr), newScreen("A", "a"))
	h.tm.Send(DoMsg{Action: actions.Check()})
	h.waitStarted()
	h.tm.Send(key("ctrl+c"))
	h.waitRun() // the program is still alive: ctrl+c did not quit
	app := h.final()
	if !strings.Contains(content(app), "cancelled") {
		t.Errorf("view: %q", content(app))
	}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if len(fr.ctxErrs) != 1 || !errors.Is(fr.ctxErrs[0], context.Canceled) {
		t.Errorf("ctx errs %v", fr.ctxErrs)
	}
}

func TestCtrlCQuitsWhenIdle(t *testing.T) {
	h := start(t, baseDeps(newFakeRunner()), newScreen("A", "a"))
	h.tm.Send(key("ctrl+c"))
	h.tm.WaitFinished(t, teatest.WithFinalTimeout(timeout))
}

func TestKeyCapturerSuppressesGlobalKeys(t *testing.T) {
	a := newScreen("A", "a")
	a.capture = true
	h := start(t, baseDeps(newFakeRunner()), a, newScreen("B", "b"))
	for _, k := range []string{"q", "tab", "s", "l", "?", "2"} {
		h.tm.Send(key(k))
	}
	app := h.final()
	a.mu.Lock()
	defer a.mu.Unlock()
	if strings.Join(a.keys, ",") != "q,tab,s,l,?,2" {
		t.Errorf("screen got keys %q", a.keys)
	}
	if app.active != 0 || app.showLog || app.showHelp {
		t.Errorf("global keys leaked: active=%d log=%v help=%v", app.active, app.showLog, app.showHelp)
	}
}

func TestHelpToggle(t *testing.T) {
	app := NewApp(baseDeps(newFakeRunner()), newScreen("A", "a"))
	app.Update(key("?"))
	if !strings.Contains(content(app), "toggle scope") {
		t.Errorf("help missing: %q", content(app))
	}
}
