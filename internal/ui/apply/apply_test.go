package apply

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

// fakeRunner answers by joined argv; unknown commands get def.
type fakeRunner struct {
	mu    sync.Mutex
	calls []string
	by    map[string]runner.Result
	def   runner.Result
	err   error
}

func (f *fakeRunner) Run(_ context.Context, args ...string) (runner.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := strings.Join(args, " ")
	f.calls = append(f.calls, k)
	if r, ok := f.by[k]; ok {
		return r, f.err
	}
	return f.def, f.err
}

// shell stands in for ui.App: ReadOnly actions run at once, everything else
// is held as "asked" (the confirm modal) until confirm is called.
type shell struct {
	t     *testing.T
	s     *Screen
	r     *fakeRunner
	asked []actions.Action
}

func newShell(t *testing.T, r *fakeRunner) *shell {
	t.Helper()
	s := New(ui.Deps{Runner: r, Scope: runner.ScopeUser})
	s.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	return &shell{t: t, s: s, r: r}
}

func (h *shell) send(msg tea.Msg) {
	_, cmd := h.s.Update(msg)
	h.pump(cmd)
}

func (h *shell) pump(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch m := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range m {
			h.pump(c)
		}
	case ui.DoMsg:
		if m.Action.ReadOnly {
			h.run(m.Action)
		} else {
			h.asked = append(h.asked, m.Action)
		}
	}
}

func (h *shell) run(a actions.Action) {
	res, err := h.r.Run(context.Background(), a.Argv...)
	h.send(ui.RunMsg{Action: a, Result: res, Err: err})
}

// confirm runs the oldest held action and clears the queue.
func (h *shell) confirm() {
	h.t.Helper()
	if len(h.asked) == 0 {
		h.t.Fatal("nothing awaiting confirmation")
	}
	a := h.asked[0]
	h.asked = nil
	h.run(a)
}

func k(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "ctrl+d":
		return tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}
	case "ctrl+u":
		return tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

func (h *shell) press(keys ...string) {
	for _, s := range keys {
		h.send(k(s))
	}
}

// typeText focuses the field with focusKey, types v and leaves the field.
func (h *shell) typeText(focusKey, v string) {
	h.press(focusKey)
	for _, r := range v {
		h.press(string(r))
	}
	h.press("enter")
}

func (h *shell) view() string { return ansi.Strip(h.s.View().Content) }

func (h *shell) calls() []string {
	h.r.mu.Lock()
	defer h.r.mu.Unlock()
	return append([]string(nil), h.r.calls...)
}

func okRunner(stdout string) *fakeRunner {
	return &fakeRunner{def: runner.Result{Stdout: stdout}}
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func wantCalls(t *testing.T, h *shell, want ...string) {
	t.Helper()
	got := h.calls()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

// ---- apply ------------------------------------------------------------------

func TestApplyAlwaysPreviewsFirst(t *testing.T) {
	h := newShell(t, okRunner(fixture(t, "apply-dry.stdout")))
	h.press("a") // no preview yet
	if len(h.asked) != 0 || len(h.calls()) != 0 {
		t.Fatalf("a before p emitted something: asked=%v calls=%v", h.asked, h.calls())
	}
	if !strings.Contains(h.view(), "preview first") {
		t.Fatalf("no hint:\n%s", h.view())
	}
	h.press("p")
	wantCalls(t, h, "apply --dry-run")
	if !strings.Contains(h.view(), "mcp_config.json") {
		t.Fatalf("plan not shown:\n%s", h.view())
	}
	if len(h.asked) != 0 {
		t.Fatal("preview must not queue the real apply")
	}
	h.press("a")
	if len(h.asked) != 1 || h.asked[0].ReadOnly || strings.Join(h.asked[0].Argv, " ") != "apply" {
		t.Fatalf("asked = %+v", h.asked)
	}
	wantCalls(t, h, "apply --dry-run") // real apply waits for the confirm
	h.confirm()
	wantCalls(t, h, "apply --dry-run", "apply")
}

func TestApplyAgentSubset(t *testing.T) {
	h := newShell(t, okRunner("plan"))
	h.typeText("e", "antigravity, claude")
	h.press("p")
	wantCalls(t, h, "apply --dry-run --agents antigravity,claude")
	h.press("a")
	if len(h.asked) != 1 || strings.Join(h.asked[0].Argv, " ") != "apply --agents antigravity,claude" {
		t.Fatalf("asked = %+v", h.asked)
	}
}

func TestApplyAgentsValidation(t *testing.T) {
	for _, bad := range []string{"a;b", "--all", "x y", "ok,$(id)", "../x"} {
		h := newShell(t, okRunner("plan"))
		h.typeText("e", bad)
		h.press("p", "a")
		if len(h.calls()) != 0 || len(h.asked) != 0 {
			t.Fatalf("%q: emitted calls=%v asked=%v", bad, h.calls(), h.asked)
		}
		if !strings.Contains(h.view(), "invalid agent") {
			t.Fatalf("%q: no inline error:\n%s", bad, h.view())
		}
	}
}

func TestEditingAgentsInvalidatesPreview(t *testing.T) {
	h := newShell(t, okRunner("plan"))
	h.press("p")
	h.typeText("e", "claude")
	h.press("a")
	if len(h.asked) != 0 || !strings.Contains(h.view(), "preview first") {
		t.Fatalf("edit did not invalidate: asked=%v\n%s", h.asked, h.view())
	}
}

func TestPreviewSurvivesRefresh(t *testing.T) {
	h := newShell(t, okRunner("plan"))
	h.press("p")
	h.pump(h.s.Refresh())
	h.press("a")
	if len(h.asked) != 1 {
		t.Fatal("Refresh cleared the preview")
	}
}

func TestSuccessfulApplyClearsPreview(t *testing.T) {
	h := newShell(t, okRunner("wrote"))
	h.press("p", "a")
	h.confirm()
	h.press("a")
	if len(h.asked) != 0 || !strings.Contains(h.view(), "preview first") {
		t.Fatalf("preview survived a successful apply: %v", h.asked)
	}
	if !strings.Contains(h.view(), "wrote") {
		t.Fatalf("apply output not shown:\n%s", h.view())
	}
}

func TestScopeSwitchInvalidatesPreview(t *testing.T) {
	h := newShell(t, okRunner("plan"))
	h.press("p")
	h.s.SetScope(okRunner("x"), runner.ScopeProject, "")
	h.press("a")
	if len(h.asked) != 0 {
		t.Fatal("scope switch kept the preview")
	}
}

func TestScopeAwareSwapsRunner(t *testing.T) {
	h := newShell(t, okRunner("x"))
	var _ ui.ScopeAware = h.s
	nr := okRunner("y")
	h.s.SetScope(nr, runner.ScopeProject, "")
	if h.s.run != runner.Runner(nr) || h.s.scope != runner.ScopeProject {
		t.Fatal("runner/scope not swapped")
	}
}

func TestStalePreviewResultDropped(t *testing.T) {
	h := newShell(t, okRunner("plan"))
	_, cmd := h.s.Update(k("p"))
	do := cmd().(ui.DoMsg)
	h.typeText("e", "claude") // inputs change while the preview is in flight
	h.send(ui.RunMsg{Action: do.Action, Result: runner.Result{Stdout: "STALE"}})
	h.press("a")
	if len(h.asked) != 0 || strings.Contains(h.view(), "STALE") {
		t.Fatalf("stale result accepted: %v\n%s", h.asked, h.view())
	}
	// scope switch drops it too
	h = newShell(t, okRunner("plan"))
	_, cmd = h.s.Update(k("p"))
	do = cmd().(ui.DoMsg)
	h.s.SetScope(okRunner("z"), runner.ScopeProject, "")
	h.send(ui.RunMsg{Action: do.Action, Result: runner.Result{Stdout: "STALE"}})
	h.press("a")
	if len(h.asked) != 0 {
		t.Fatal("result after scope switch accepted")
	}
}

func TestForeignRunMsgIgnored(t *testing.T) {
	h := newShell(t, okRunner("plan"))
	h.send(ui.RunMsg{Action: actions.Action{Title: "other", Argv: []string{"status", "--json"}, ReadOnly: true}, Result: runner.Result{Stdout: "OTHER"}})
	if strings.Contains(h.view(), "OTHER") {
		t.Fatal("foreign result shown")
	}
}

func TestApplyFailureShowsMaskedStderr(t *testing.T) {
	h := newShell(t, &fakeRunner{def: runner.Result{ExitCode: 1, Stderr: "boom token=CANARYx1\n"}})
	h.press("p")
	v := h.view()
	if strings.Contains(v, "CANARYx1") || !strings.Contains(v, "boom") || !strings.Contains(v, "exit 1") {
		t.Fatalf("failure view:\n%s", v)
	}
	h.press("a")
	if len(h.asked) != 0 || !strings.Contains(h.view(), "preview first") {
		t.Fatal("a enabled after a failed preview")
	}
}

func TestCancelledOrFailedApplyShownMaskedAndNotStuck(t *testing.T) {
	h := newShell(t, okRunner("plan"))
	h.press("p", "a")
	a := h.asked[0]
	h.asked = nil
	h.send(ui.RunMsg{Action: a, Result: runner.Result{Stdout: "partial (token=CANARYx1)\n"}, Err: context.Canceled})
	v := h.view()
	if strings.Contains(v, "CANARYx1") || !strings.Contains(v, "partial") || !strings.Contains(v, "canceled") {
		t.Fatalf("cancelled view:\n%s", v)
	}
	// the preview no longer describes the disk: a emits nothing until p
	h.press("a")
	if len(h.asked) != 0 || !strings.Contains(h.view(), "preview first") {
		t.Fatalf("a emitted after a cancelled apply: %v", h.asked)
	}
	// not stuck: a new preview and apply cycle works
	h.press("p", "a")
	if len(h.asked) != 1 {
		t.Fatal("screen stuck after cancel")
	}
	h.asked = nil
	h.send(ui.RunMsg{Action: a, Err: errors.New("spawn failed secret=CANARYx1")})
	if strings.Contains(h.view(), "CANARYx1") {
		t.Fatalf("error leaked:\n%s", h.view())
	}
}

// ---- output handling --------------------------------------------------------

func TestOutputStoredMasked(t *testing.T) {
	raw := "wrote /x/.claude.json (token=CANARYx1)\n\x1b[31mred\x1b[0m\nAuthorization: Bearer abcdefghijklmnopqrstuvwxyz0123456789\n"
	h := newShell(t, okRunner(raw))
	h.press("p")
	o := h.s.out[modeApply]
	if o == nil {
		t.Fatal("no output stored")
	}
	for _, bad := range []string{"CANARYx1", "\x1b", "abcdefghijklmnopqrstuvwxyz0123456789"} {
		if strings.Contains(o.Text, bad) || strings.Contains(h.view(), bad) {
			t.Fatalf("%q survived (stored %q)", bad, o.Text)
		}
	}
	if !strings.Contains(o.Text, "wrote /x/.claude.json") || !strings.Contains(o.Text, "red") {
		t.Fatalf("lost content: %q", o.Text)
	}
}

func TestOutputCappedAt64KiB(t *testing.T) {
	h := newShell(t, okRunner(strings.Repeat("line of plan output\n", 10000)))
	h.press("p")
	o := h.s.out[modeApply]
	if len(o.Text) > 64<<10 || !o.Truncated {
		t.Fatalf("len=%d trunc=%v", len(o.Text), o.Truncated)
	}
	h.press("ctrl+d", "ctrl+d", "ctrl+d", "ctrl+d", "ctrl+d", "ctrl+d", "ctrl+d", "ctrl+d")
	for i := 0; i < 2000; i++ {
		h.press("ctrl+d")
	}
	if !strings.Contains(h.view(), "output truncated") {
		t.Fatalf("no truncation note at end:\n%s", h.view())
	}
	h.press("ctrl+u")
	if h.s.scroll[modeApply] >= h.s.maxScroll() {
		t.Fatal("ctrl+u did not scroll up")
	}
}

func TestOutputTruncatedWidthAware(t *testing.T) {
	h := newShell(t, okRunner(strings.Repeat("x", 500)+"\n"))
	h.s.Update(tea.WindowSizeMsg{Width: 40, Height: 20})
	h.press("p")
	for _, l := range strings.Split(h.view(), "\n") {
		if w := ansi.StringWidth(l); w > 40 {
			t.Fatalf("line width %d > 40: %q", w, l)
		}
	}
}

// ---- plumbing ---------------------------------------------------------------

func TestInitEqualsRefresh(t *testing.T) {
	s := New(ui.Deps{Runner: okRunner("x")})
	if s.Init() != nil || s.Refresh() != nil {
		t.Fatal("Init/Refresh must be the same cheap no-op")
	}
}

func TestKeyCapturerWhileTyping(t *testing.T) {
	h := newShell(t, okRunner("x"))
	var kc ui.KeyCapturer = h.s
	if kc.CapturesKeys() {
		t.Fatal("captures while idle")
	}
	h.press("e")
	if !kc.CapturesKeys() {
		t.Fatal("not capturing while typing")
	}
	h.press("p") // typed, not a preview
	if len(h.calls()) != 0 || h.s.agents != "p" {
		t.Fatalf("typing leaked to commands: %q calls=%v", h.s.agents, h.calls())
	}
	h.press("esc")
	if kc.CapturesKeys() {
		t.Fatal("still capturing after esc")
	}
}

func TestShellKeysNotUsed(t *testing.T) {
	// the shell owns tab, shift+tab, 1-9, s, l, ?, q: none may do anything here
	h := newShell(t, okRunner("x"))
	h.press("s", "l", "?", "q", "1", "9")
	if h.s.mode != modeApply || h.s.focus != fNone || len(h.calls()) != 0 {
		t.Fatal("a shell key changed screen state")
	}
}

// ---- revert -----------------------------------------------------------------

func TestRevertPreviewThenDestructiveAction(t *testing.T) {
	h := newShell(t, okRunner("would revert"))
	h.press("m")
	h.typeText("e", "claude")
	if !strings.Contains(h.view(), "agentsync revert claude") {
		t.Fatalf("header lacks Display():\n%s", h.view())
	}
	h.press("v")
	if len(h.asked) != 0 || len(h.calls()) != 0 {
		t.Fatal("v before p emitted")
	}
	h.press("p")
	wantCalls(t, h, "revert claude --dry-run")
	h.press("v")
	if len(h.asked) != 1 || !h.asked[0].Destructive || h.asked[0].ReadOnly || strings.Join(h.asked[0].Argv, " ") != "revert claude" {
		t.Fatalf("asked = %+v", h.asked)
	}
	if got := h.asked[0].Display(); !strings.Contains(h.view(), got) {
		t.Fatalf("header does not show %q:\n%s", got, h.view())
	}
}

func TestRevertToCheckpointValidatesRef(t *testing.T) {
	for _, ok := range []string{"HEAD", "HEAD~2", "abcdef1", strings.Repeat("a", 40)} {
		h := newShell(t, okRunner("x"))
		h.press("m")
		h.typeText("e", "claude")
		h.typeText("t", ok)
		h.press("p")
		if want := "revert claude --to " + ok + " --dry-run"; len(h.calls()) != 1 || h.calls()[0] != want {
			t.Fatalf("%q: calls=%v", ok, h.calls())
		}
	}
	for _, bad := range []string{"; rm -rf", "--all", "a b", "-x", "abc", strings.Repeat("a", 41), "HEAD~", "$(id)"} {
		h := newShell(t, okRunner("x"))
		h.press("m")
		h.typeText("e", "claude")
		h.typeText("t", bad)
		h.press("p", "v")
		if len(h.calls()) != 0 || len(h.asked) != 0 {
			t.Fatalf("%q: emitted %v %v", bad, h.calls(), h.asked)
		}
		if !strings.Contains(h.view(), "invalid revision") {
			t.Fatalf("%q: no inline error:\n%s", bad, h.view())
		}
	}
}

func TestRevertAgentOrAllExclusive(t *testing.T) {
	h := newShell(t, okRunner("x"))
	h.press("m", "p") // neither agent nor all
	if len(h.calls()) != 0 || !strings.Contains(h.view(), "exactly one") {
		t.Fatalf("calls=%v\n%s", h.calls(), h.view())
	}
	h.typeText("e", "claude")
	h.press("A", "p") // all wins; the typed agent is not passed
	wantCalls(t, h, "revert --all --dry-run")
	h.press("A")
	if strings.Contains(h.view(), "--all") && strings.Contains(h.view(), "will run: agentsync revert --all") {
		t.Fatal("A did not toggle off")
	}
	h.typeText("e", "x") // typing a target clears all
	if h.s.all {
		t.Fatal("typing a target left --all on")
	}
}

func TestRevertNoticeShown(t *testing.T) {
	notice := "revert recorded a new commit; the next 'agentsync apply' re-renders from the canonical source\n"
	r := &fakeRunner{by: map[string]runner.Result{"revert claude": {Stdout: notice}}, def: runner.Result{Stdout: "dry"}}
	h := newShell(t, r)
	h.press("m")
	h.typeText("e", "claude")
	h.press("p", "v")
	h.confirm()
	v := h.view()
	if !strings.Contains(v, "re-renders from the canonical source") || !strings.Contains(v, revertHint) {
		t.Fatalf("notice/hint missing:\n%s", v)
	}
	h.press("v")
	if len(h.asked) != 0 {
		t.Fatal("preview survived a successful revert")
	}
}

func TestRealFixtures(t *testing.T) {
	// real agentsync output captured from a hermetic home
	h := newShell(t, &fakeRunner{by: map[string]runner.Result{
		"revert claude --dry-run": {ExitCode: 1, Stderr: fixture(t, "revert-agent-dry.stderr")},
		"revert --all --dry-run":  {Stdout: fixture(t, "revert-all-dry.stdout")},
	}})
	h.press("m")
	h.typeText("e", "claude")
	h.press("p")
	if !strings.Contains(h.view(), "not an agentsync-managed git backup") {
		t.Fatalf("revert error not shown:\n%s", h.view())
	}
	h.press("A", "p")
	if !strings.Contains(h.view(), "no agentsync-managed destination dirs") {
		t.Fatalf("revert --all output not shown:\n%s", h.view())
	}
}

func TestFailedRealRunClearsPreview(t *testing.T) {
	for name, res := range map[string]ui.RunMsg{
		"exit":   {Result: runner.Result{ExitCode: 1, Stderr: "partial write"}},
		"cancel": {Err: context.Canceled},
		"ok":     {Result: runner.Result{Stdout: "done"}},
	} {
		h := newShell(t, okRunner("plan"))
		h.press("p", "a")
		res.Action = h.asked[0]
		h.asked = nil
		h.send(res)
		h.press("a")
		if len(h.asked) != 0 {
			t.Fatalf("%s: apply preview survived the run", name)
		}
	}
	for name, res := range map[string]ui.RunMsg{
		"exit":   {Result: runner.Result{ExitCode: 1}},
		"cancel": {Err: context.Canceled},
	} {
		h := newShell(t, okRunner("would revert"))
		h.press("m")
		h.typeText("e", "claude")
		h.press("p", "v")
		res.Action = h.asked[0]
		h.asked = nil
		h.send(res)
		h.press("v")
		if len(h.asked) != 0 {
			t.Fatalf("%s: revert preview survived the run", name)
		}
	}
}

func TestForeignMutationInvalidatesPreviews(t *testing.T) {
	foreign := actions.Action{Title: "Add server", Argv: []string{"mcp", "add", "x"}}
	for _, res := range []runner.Result{{}, {ExitCode: 1}} {
		h := newShell(t, okRunner("plan"))
		h.press("p")
		h.press("m")
		h.typeText("e", "claude")
		h.press("p") // revert preview too
		h.send(ui.RunMsg{Action: foreign, Result: res})
		h.press("v")
		h.press("m", "a")
		if len(h.asked) != 0 {
			t.Fatalf("preview survived a foreign mutation: %v", h.asked)
		}
	}
	h := newShell(t, okRunner("plan"))
	h.press("p")
	h.send(ui.RunMsg{Action: foreign, Err: context.Canceled})
	h.press("a")
	if len(h.asked) != 0 {
		t.Fatal("preview survived a cancelled foreign mutation")
	}
}

// An $EDITOR edit (any screen) may change the canonical files a preview
// described; it never produces a RunMsg, so the screen watches
// EditOutcomeMsg. Both the start and the end of an edit, whatever its
// result, drop both modes' previews. A refused edit keeps them.
func TestEditInvalidatesPreviews(t *testing.T) {
	for name, m := range map[string]ui.EditOutcomeMsg{
		"started":       {Path: "/x/skill.md", Started: true},
		"finished":      {Path: "/x/skill.md", Finished: true},
		"failed finish": {Path: "/x/skill.md", Finished: true, Err: "editor failed (exit 1)"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newShell(t, okRunner("plan"))
			h.press("p")
			h.press("m")
			h.typeText("e", "claude")
			h.press("p")
			h.send(m)
			h.press("v")
			h.press("m", "a")
			if len(h.asked) != 0 {
				t.Fatalf("preview survived an edit: %v", h.asked)
			}
			if !strings.Contains(h.view(), "preview") {
				t.Errorf("no hint:\n%s", h.view())
			}
			h.press("p", "a")
			if len(h.asked) != 1 {
				t.Fatal("stuck: a fresh preview did not re-arm")
			}
		})
	}
	h := newShell(t, okRunner("plan"))
	h.press("p")
	h.send(ui.EditOutcomeMsg{Path: "/x/skill.md", Err: "edit refused: not a regular file"})
	h.press("a")
	if len(h.asked) != 1 {
		t.Fatal("a refused edit invalidated the preview")
	}
}

func TestForeignReadOnlyRunKeepsPreview(t *testing.T) {
	h := newShell(t, okRunner("plan"))
	h.press("p")
	h.send(ui.RunMsg{Action: actions.Action{Title: "status", Argv: []string{"status", "--json"}, ReadOnly: true}})
	h.press("a")
	if len(h.asked) != 1 {
		t.Fatal("a foreign read-only run invalidated the preview")
	}
}

// A mutation from another screen drops an in-flight preview; the status must
// not keep saying "running preview…" for a preview that will be ignored.
func TestForeignMutationClearsPreviewMessage(t *testing.T) {
	h := newShell(t, okRunner("x"))
	h.s.agents = "claude"
	h.s.preview() // request recorded, DoMsg not run: the preview is in flight
	if h.s.msg != "running preview…" {
		t.Fatalf("precondition: msg %q", h.s.msg)
	}
	foreign := actions.Action{Title: "Disable server", Argv: []string{"mcp", "disable", "x"}}
	h.s.Update(ui.RunMsg{Action: foreign})
	if h.s.msg == "running preview…" || !strings.Contains(h.s.msg, "preview") {
		t.Errorf("msg after a foreign mutation: %q", h.s.msg)
	}
	if h.s.gates[h.s.mode].Pending() {
		t.Error("preview request still pending")
	}
}

// The preview is bound to the scope it was requested in (gate.Gate), not
// only to the argv: even if the screen's scope changed without SetScope
// dropping the preview, the real command is refused in the other scope.
func TestPreviewBoundToScope(t *testing.T) {
	h := newShell(t, okRunner("x"))
	h.s.agents = "claude"
	h.press("p")
	if !strings.Contains(h.s.msg, "preview ok") {
		t.Fatalf("precondition: %q", h.s.msg)
	}
	h.s.scope = runner.ScopeProject // defence in depth: no SetScope reset
	h.press("a")
	if len(h.asked) != 0 || h.s.msg != msgPreviewFirst {
		t.Fatalf("real apply offered in another scope: asked=%v msg=%q", h.asked, h.s.msg)
	}
	if strings.Contains(h.view(), "preview ok") {
		t.Error("view still claims the preview is good in the other scope")
	}
	h.s.scope = runner.ScopeUser
	h.press("a")
	if len(h.asked) != 1 {
		t.Fatalf("real apply not offered back in the preview's scope: %v", h.asked)
	}
}
