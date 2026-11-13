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
	// deadlines records, per non-version call, whether its context had one.
	deadlines []bool
	ctxs      []context.Context // per non-version call

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
	if ctx.Value(chipCtxKey{}) != nil { // the header chip's background checks
		return runner.Result{}, nil
	}
	f.mu.Lock()
	f.calls = append(f.calls, args)
	_, hasDL := ctx.Deadline()
	f.deadlines = append(f.deadlines, hasDL)
	f.ctxs = append(f.ctxs, ctx)
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

func (s *fakeScreen) View() tea.View { return tea.NewView(s.body) }

func (s *fakeScreen) Title() string { return s.title }

func (s *fakeScreen) CapturesKeys() bool { return s.capture }

func (s *fakeScreen) Refresh() tea.Cmd {
	s.refreshed <- struct{}{}
	return nil
}

// probe wraps the App and reports every processed message, so tests can wait
// for a message to have been handled instead of sleeping.
type probe struct {
	*App
	seen chan observed
}

// observed is a processed message plus a snapshot of shell state taken on
// the Update goroutine right after it was handled (race-free to read).
type observed struct {
	msg       tea.Msg
	modalOpen bool
	running   bool
}

func (p probe) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := p.App.Update(msg)
	select {
	case p.seen <- observed{msg: msg, modalOpen: p.modal != nil, running: p.running != nil}:
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

// The harness terminal size. The program delivers it to the App twice on its
// own: teatest.NewTestModel Sends it before returning, and tea.Program.Run
// sends its WithWindowSize size from a separate goroutine (`go p.Send(...)`),
// so that second copy can be processed after messages the test sends.
const (
	initW, initH    = 100, 30
	initialSizeMsgs = 2
)

func start(t *testing.T, d Deps, screens ...Screen) *harness {
	t.Helper()
	fr, _ := d.Runner.(*fakeRunner)
	p := probe{App: NewApp(d, screens...), seen: make(chan observed, 4096)}
	tm := teatest.NewTestModel(t, p, teatest.WithInitialTermSize(initW, initH))
	return &harness{t: t, tm: tm, p: p, run: fr}
}

// waitInitialSize blocks until the App has processed every startup copy of
// the initial window size, so a size the test sends afterwards is final.
// It discards other messages seen meanwhile.
func (h *harness) waitInitialSize() {
	h.t.Helper()
	for i := 0; i < initialSizeMsgs; i++ {
		h.waitMsg("initial WindowSizeMsg", func(m tea.Msg) bool {
			w, ok := m.(tea.WindowSizeMsg)
			return ok && w.Width == initW && w.Height == initH
		})
	}
}

func baseDeps(fr *fakeRunner) Deps {
	return Deps{Runner: fr, Scope: runner.ScopeUser, AgentsyncHome: "/nonexistent/agentsync"}
}

// waitMsg blocks until a processed message satisfies match.
func (h *harness) waitMsg(what string, match func(tea.Msg) bool) observed {
	h.t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case o := <-h.p.seen:
			if match(o.msg) {
				return o
			}
		case <-deadline:
			h.t.Fatalf("timed out waiting for %s", what)
			return observed{}
		}
	}
}

// pressAndWait sends k and returns the state right after it was handled.
func (h *harness) pressAndWait(k tea.KeyPressMsg) observed {
	h.t.Helper()
	h.tm.Send(k)
	return h.waitMsg("key "+k.String(), func(m tea.Msg) bool {
		got, ok := m.(tea.KeyPressMsg)
		return ok && got.String() == k.String()
	})
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

func TestNoScreensEmptyState(t *testing.T) {
	h := start(t, baseDeps(newFakeRunner()))
	if !strings.Contains(content(h.final()), "No screens registered") {
		t.Error("empty state missing")
	}
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
	// Without this, the program's own late initial size can land after the
	// 40x15 below and leave the App at 100x30.
	h.waitInitialSize()
	h.tm.Send(tea.WindowSizeMsg{Width: 40, Height: 15})
	// probe reports a message only after App.Update (which resizes the
	// screens synchronously) has handled it.
	h.waitMsg("resize", func(m tea.Msg) bool { w, ok := m.(tea.WindowSizeMsg); return ok && w.Width == 40 && w.Height == 15 })
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
		_ = l.add(entry{Display: "agentsync check"})
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
	if !strings.Contains(content(app), "reach the rest") {
		t.Errorf("help does not say tab reaches screens past 9: %q", content(app))
	}
}

func (f *fakeRunner) ctxErr(i int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ctxs[i].Err()
}

// startWrite confirms and starts `plugin upgrade --all`, a network write.
func startWrite(t *testing.T, h *harness) {
	t.Helper()
	act := actions.PluginUpgradeAll()
	if act.ReadOnly {
		t.Fatal("precondition: a write")
	}
	h.tm.Send(DoMsg{Action: act})
	h.pressAndWait(key("y"))
	h.waitStarted()
}

// Without a project the placeholder replaces the screen, so a screen that
// captures keys does not get them: the global keys work, and s switches
// back to user scope.
func TestNoProjectGlobalKeysBypassCapturingScreen(t *testing.T) {
	fr := newFakeRunner()
	d := Deps{Runner: fr, Scope: runner.ScopeProject}
	d.WithScope = func(runner.Scope) (runner.Runner, func(...string) *exec.Cmd) { return fr, nil }
	a := newScreen("A", "body-a")
	a.capture = true
	app := NewApp(d, a)
	if !app.noProject {
		t.Fatal("precondition: project scope without a directory")
	}
	app.Update(key("s"))
	if app.scope != runner.ScopeUser || app.noProject {
		t.Errorf("s did not switch to user scope: scope=%v noProject=%v", app.scope, app.noProject)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.keys) != 0 {
		t.Errorf("hidden screen got keys %q", a.keys)
	}
}

// An open confirm dialog takes keys before a screen that captures them:
// esc cancels the dialog and the screen sees nothing.
func TestModalTakesKeysBeforeCapturingScreen(t *testing.T) {
	a := newScreen("A", "body-a")
	a.capture = true
	app := NewApp(baseDeps(newFakeRunner()), a)
	app.Update(DoMsg{Action: mustAct(actions.McpRemove("x"))})
	if app.modal == nil {
		t.Fatal("precondition: confirm dialog open")
	}
	app.Update(key("esc"))
	if app.modal != nil {
		t.Error("esc did not cancel the dialog")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.keys) != 0 {
		t.Errorf("screen got keys %q while the dialog was open", a.keys)
	}
}

// A LazyCache creates nothing until Open, then opens the directory once and
// hands every caller the same cache; a nil one means no cache.
func TestDiscoverLazyCacheOpensOnFirstUse(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agentsync-tui")
	l := NewLazyCache(dir)
	t.Cleanup(func() { _ = l.Close() })
	if _, err := os.Lstat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("NewLazyCache created %s (err %v)", dir, err)
	}
	c1, err := l.Open()
	if err != nil || c1 == nil {
		t.Fatalf("Open() = %v, %v", c1, err)
	}
	c2, err := l.Open()
	if err != nil || c2 != c1 {
		t.Errorf("second Open() = %p, %v, want the same cache %p", c2, err, c1)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("cache dir after Open: %v, %v", fi, err)
	}
	if c, err := (*LazyCache)(nil).Open(); c != nil || err != nil {
		t.Errorf("nil LazyCache Open() = %v, %v, want nil, nil", c, err)
	}
}

// A refused cache directory (a symlink) is refused on every call: a retry
// re-checks it rather than trusting an earlier attempt.
func TestDiscoverLazyCacheRefusesSymlinkEachTime(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "link")
	if err := os.Symlink(base, dir); err != nil {
		t.Skip(err)
	}
	l := NewLazyCache(dir)
	for range 2 {
		if c, err := l.Open(); c != nil || err == nil {
			t.Fatalf("Open() of a symlinked dir = %v, %v, want an error", c, err)
		}
	}
}

// A failed open is not sticky: once the cause is gone the next Open works.
func TestDiscoverLazyCacheRetriesFailedOpen(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "parent")
	if err := os.WriteFile(parent, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	l := NewLazyCache(filepath.Join(parent, "agentsync-tui"))
	t.Cleanup(func() { _ = l.Close() })
	if c, err := l.Open(); c != nil || err == nil {
		t.Fatalf("Open() under a regular file = %v, %v, want an error", c, err)
	}
	if err := os.Remove(parent); err != nil {
		t.Fatal(err)
	}
	if c, err := l.Open(); c == nil || err != nil {
		t.Fatalf("Open() after the cause is gone = %v, %v, want a cache", c, err)
	}
}

// Close releases an opened cache and creates nothing when it was never
// opened; afterwards Open refuses, so a late caller cannot reopen it.
func TestDiscoverLazyCacheClose(t *testing.T) {
	if err := (*LazyCache)(nil).Close(); err != nil {
		t.Errorf("nil Close() = %v", err)
	}
	unused := filepath.Join(t.TempDir(), "unused")
	if err := NewLazyCache(unused).Close(); err != nil {
		t.Errorf("Close() before Open = %v", err)
	}
	if _, err := os.Lstat(unused); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Close created %s (err %v)", unused, err)
	}
	l := NewLazyCache(filepath.Join(t.TempDir(), "c"))
	c, err := l.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if err := c.Put("k", []byte("v")); err == nil {
		t.Error("Put after Close succeeded, want the closed cache to refuse")
	}
	if c, err := l.Open(); c != nil || err == nil {
		t.Errorf("Open() after Close = %v, %v, want an error", c, err)
	}
}

// App.Close closes the discovery cache (main calls it once Run returns).
func TestAppCloseClosesDiscoveryCache(t *testing.T) {
	d := baseDeps(newFakeRunner())
	d.Discover.Cache = NewLazyCache(filepath.Join(t.TempDir(), "c"))
	c, err := d.Discover.Cache.Open()
	if err != nil {
		t.Fatal(err)
	}
	if err := NewApp(d).Close(); err != nil {
		t.Fatalf("App.Close() = %v", err)
	}
	if err := c.Put("k", []byte("v")); err == nil {
		t.Error("cache still open after App.Close")
	}
	if err := NewApp(baseDeps(newFakeRunner())).Close(); err != nil {
		t.Errorf("App.Close() without discovery = %v", err)
	}
}
