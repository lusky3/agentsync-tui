package reconcile

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

type fakeRunner struct {
	mu    sync.Mutex
	calls []string
	res   runner.Result
	err   error
}

func (f *fakeRunner) Run(_ context.Context, args ...string) (runner.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, strings.Join(args, " "))
	return f.res, f.err
}

func (f *fakeRunner) set(res runner.Result, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.res, f.err = res, err
}

func (f *fakeRunner) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func okRunner(stdout string) *fakeRunner { return &fakeRunner{res: runner.Result{Stdout: stdout}} }

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// harness drives the screen like the shell: Cmds are executed and their
// messages fed back; DoMsgs are collected, never run.
type harness struct {
	t   *testing.T
	s   *Screen
	dos []ui.DoMsg
}

func newHarness(t *testing.T, r runner.Runner) *harness {
	t.Helper()
	s := New(ui.Deps{Runner: r, Scope: runner.ScopeUser})
	s.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	return &harness{t: t, s: s}
}

// pump runs cmd to completion, feeding screen messages back.
func (h *harness) pump(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch m := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range m {
			h.pump(c)
		}
	case ui.DoMsg:
		h.dos = append(h.dos, m)
	case nil:
	default:
		_, next := h.s.Update(m)
		h.pump(next)
	}
}

func (h *harness) refresh()     { h.pump(h.s.Refresh()) }
func (h *harness) view() string { return ansi.Strip(h.s.View().Content) }

func key(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	case "pgup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "ctrl+d":
		return tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
	case "ctrl+u":
		return tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

func (h *harness) press(keys ...string) {
	for _, k := range keys {
		_, cmd := h.s.Update(key(k))
		h.pump(cmd)
	}
}

func TestReconcileShowsAutoSafeReport(t *testing.T) {
	r := okRunner(fixture(t, "auto-safe-drift.stdout"))
	h := newHarness(t, r)
	h.refresh()
	if got := r.got(); len(got) != 1 || got[0] != "reconcile --auto-safe" {
		t.Fatalf("calls = %q", got)
	}
	v := h.view()
	for _, want := range []string{
		"skipped (needs manual review): /tmp/hermetic/home/.claude.json#/mcpServers/fsdemo (drift)",
		"1 item(s) need a human",
	} {
		if !strings.Contains(v, want) {
			t.Fatalf("missing %q:\n%s", want, v)
		}
	}
	r.set(runner.Result{Stdout: fixture(t, "auto-safe-clean.stdout")}, nil)
	h.refresh()
	if v := h.view(); !strings.Contains(v, "nothing to reconcile") || strings.Contains(v, "fsdemo") {
		t.Fatalf("clean report:\n%s", v)
	}
	// a non-zero exit that still carries a report is a normal report
	r.set(runner.Result{Stdout: fixture(t, "auto-safe-drift.stdout"), ExitCode: 3}, nil)
	h.refresh()
	if v := h.view(); !strings.Contains(v, "fsdemo") || !strings.Contains(v, "exit 3") {
		t.Fatalf("non-zero report:\n%s", v)
	}
}

func TestReconcileHandsOffInteractively(t *testing.T) {
	h := newHarness(t, okRunner(fixture(t, "auto-safe-drift.stdout")))
	h.refresh()
	h.press("x")
	if len(h.dos) != 1 {
		t.Fatalf("dos = %+v", h.dos)
	}
	a := h.dos[0].Action
	if !a.Interactive || a.ReadOnly || strings.Join(a.Argv, " ") != "reconcile" {
		t.Fatalf("action = %+v", a)
	}
	for _, arg := range a.Argv {
		if strings.HasPrefix(arg, "--auto") {
			t.Fatalf("handover carries %q", arg)
		}
	}
	// after the handover the shell refreshes: the report re-runs
	r := okRunner(fixture(t, "auto-safe-clean.stdout"))
	h.s.SetScope(r, runner.ScopeUser)
	h.s.Update(ui.RunMsg{Action: a})
	h.refresh()
	if !strings.Contains(h.view(), "nothing to reconcile") {
		t.Fatalf("report not re-run:\n%s", h.view())
	}
}

// everyKey is every key a user could plausibly press.
func everyKey() []string {
	var ks []string
	for c := '!'; c <= '~'; c++ {
		ks = append(ks, string(c))
	}
	return append(ks, "enter", "esc", "pgdown", "pgup", "ctrl+d", "ctrl+u")
}

func TestNoBulkResolveKeys(t *testing.T) {
	for _, k := range everyKey() {
		r := okRunner(fixture(t, "auto-safe-drift.stdout"))
		h := newHarness(t, r)
		h.refresh()
		before := len(r.got())
		h.press(k)
		for _, d := range h.dos {
			for _, arg := range d.Action.Argv {
				if arg == "--auto-writeback" || arg == "--auto-override" {
					t.Fatalf("key %q emitted %v", k, d.Action.Argv)
				}
			}
		}
		for _, c := range r.got() {
			if strings.Contains(c, "--auto-writeback") || strings.Contains(c, "--auto-override") {
				t.Fatalf("key %q ran %q", k, c)
			}
		}
		switch k {
		case "W", "O", "S", "w", "o":
			if len(h.dos) != 0 || len(r.got()) != before {
				t.Fatalf("key %q did something: dos=%v calls=%v", k, h.dos, r.got())
			}
		case "x":
			if len(h.dos) != 1 || !h.dos[0].Action.Interactive {
				t.Fatalf("x: %+v", h.dos)
			}
		default:
			if len(h.dos) != 0 {
				t.Fatalf("key %q emitted %+v", k, h.dos)
			}
		}
	}
	src, err := os.ReadFile("reconcile.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`"--auto-writeback"`, `"--auto-override"`} {
		if strings.Contains(string(src), bad) {
			t.Fatalf("source builds %s", bad)
		}
	}
}

func TestRefreshDoesNotEmitDoMsg(t *testing.T) {
	r := okRunner(fixture(t, "auto-safe-drift.stdout"))
	h := newHarness(t, r)
	h.refresh()
	h.pump(h.s.Init())
	if len(h.dos) != 0 {
		t.Fatalf("Refresh/Init emitted DoMsg: %+v", h.dos)
	}
	if len(r.got()) != 2 {
		t.Fatalf("report did not run through the runner: %q", r.got())
	}
}

func TestReconcileFailedRefreshKeepsLastReport(t *testing.T) {
	r := okRunner(fixture(t, "auto-safe-drift.stdout"))
	h := newHarness(t, r)
	h.refresh()
	r.set(runner.Result{ExitCode: 1, Stderr: "✗ ERROR  boom token=CANARYx1\n"}, nil)
	h.refresh()
	v := h.view()
	if !strings.Contains(v, "fsdemo") || !strings.Contains(v, "boom") || strings.Contains(v, "CANARYx1") {
		t.Fatalf("failed refresh:\n%s", v)
	}
	r.set(runner.Result{}, runner.ErrNotInstalled)
	h.refresh()
	v = h.view()
	if !strings.Contains(v, "fsdemo") || !strings.Contains(v, "not installed") {
		t.Fatalf("spawn failure:\n%s", v)
	}
	// with no previous report the note is shown on its own
	h = newHarness(t, &fakeRunner{err: errors.New("spawn failed")})
	h.refresh()
	if v := h.view(); !strings.Contains(v, "spawn failed") {
		t.Fatalf("no note:\n%s", v)
	}
}

func TestReconcileBannerShown(t *testing.T) {
	h := newHarness(t, okRunner(""))
	v := h.view()
	for _, want := range []string{"#239", "#255", "#256", "W/O/S", "x"} {
		if !strings.Contains(v, want) {
			t.Fatalf("banner missing %q:\n%s", want, v)
		}
	}
}

const canaryReport = "skipped (needs manual review): /x#/a (drift) password: CANARYx1\n" +
	"token=CANARYx1\nAuthorization: Bearer CANARYx1\n\x1b[31mred\x1b[0m \u202eevil\n"

func TestReconcileOutputStoredMasked(t *testing.T) {
	h := newHarness(t, okRunner(canaryReport))
	h.refresh()
	for _, bad := range []string{"CANARYx1", "\x1b", "\u202e"} {
		if strings.Contains(h.s.report, bad) {
			t.Fatalf("stored report holds %q: %q", bad, h.s.report)
		}
		if strings.Contains(h.s.View().Content, bad) && bad != "\x1b" {
			t.Fatalf("view holds %q", bad)
		}
		if strings.Contains(h.view(), bad) {
			t.Fatalf("view holds %q", bad)
		}
	}
	if !strings.Contains(h.s.report, "password: ••••") || !strings.Contains(h.s.report, "red") {
		t.Fatalf("stored = %q", h.s.report)
	}
	// capped
	h = newHarness(t, okRunner(strings.Repeat("skipped (needs manual review): /x#/a (drift)\n", 5000)))
	h.refresh()
	if len(h.s.report) > 64<<10 || !h.s.trunc {
		t.Fatalf("len=%d trunc=%v", len(h.s.report), h.s.trunc)
	}
	for i := 0; i < 3000; i++ {
		h.press("pgdown")
	}
	if !strings.Contains(h.view(), "output truncated") {
		t.Fatalf("no truncation note at end")
	}
	at := h.s.scroll
	h.press("ctrl+u")
	if h.s.scroll >= at {
		t.Fatal("ctrl+u did not scroll up")
	}
	h.press("ctrl+d")
	if h.s.scroll != at {
		t.Fatal("ctrl+d did not scroll down")
	}
	h.press("pgup")
	if h.s.scroll >= at {
		t.Fatal("pgup did not scroll up")
	}
}

func TestReconcileScopeAwareSwapsRunner(t *testing.T) {
	r1, r2 := okRunner("a"), okRunner("nothing to reconcile\n")
	h := newHarness(t, r1)
	var _ ui.ScopeAware = h.s
	var _ ui.KeyCapturer = h.s
	var _ ui.Screen = h.s
	h.s.SetScope(r2, runner.ScopeProject)
	h.refresh()
	if len(r1.got()) != 0 || len(r2.got()) != 1 || h.s.scope != runner.ScopeProject {
		t.Fatalf("r1=%q r2=%q", r1.got(), r2.got())
	}
	if h.s.CapturesKeys() {
		t.Fatal("reconcile screen has no text input")
	}
}

func TestReconcileInitEqualsRefresh(t *testing.T) {
	r := okRunner(fixture(t, "auto-safe-drift.stdout"))
	h := newHarness(t, r)
	h.pump(h.s.Init())
	vi := h.view()
	h2 := newHarness(t, okRunner(fixture(t, "auto-safe-drift.stdout")))
	h2.refresh()
	if vi != h2.view() || strings.Join(r.got(), "|") != "reconcile --auto-safe" {
		t.Fatalf("Init differs from Refresh:\n%s\n---\n%s", vi, h2.view())
	}
}

func TestReconcileStaleResultDropped(t *testing.T) {
	r := okRunner("skipped (needs manual review): /old#/a (drift)\n")
	h := newHarness(t, r)
	oldMsg := h.s.Refresh()() // older generation, finished but not yet delivered
	r.set(runner.Result{Stdout: "nothing to reconcile\n"}, nil)
	h.refresh()
	h.s.Update(oldMsg) // the older generation reports last
	if v := h.view(); strings.Contains(v, "/old") || !strings.Contains(v, "nothing to reconcile") {
		t.Fatalf("stale newer-refresh result applied:\n%s", v)
	}
	// a scope switch drops an in-flight result too
	r = okRunner("skipped (needs manual review): /userscope#/a (drift)\n")
	h = newHarness(t, r)
	oldMsg = h.s.Refresh()()
	h.s.SetScope(okRunner(""), runner.ScopeProject)
	h.s.Update(oldMsg)
	if strings.Contains(h.view(), "/userscope") {
		t.Fatalf("result from the previous scope applied:\n%s", h.view())
	}
}

// the shell's RunMsg for unrelated actions must not disturb the report
func TestReconcileIgnoresForeignRunMsg(t *testing.T) {
	h := newHarness(t, okRunner(fixture(t, "auto-safe-drift.stdout")))
	h.refresh()
	h.s.Update(ui.RunMsg{Action: actions.Check(), Result: runner.Result{Stdout: "OTHER"}})
	if strings.Contains(h.view(), "OTHER") || !strings.Contains(h.view(), "fsdemo") {
		t.Fatalf("foreign RunMsg changed the view:\n%s", h.view())
	}
}
