package importer

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

const agentsTwo = "antigravity enabled=false scope=user\nclaude     enabled=true scope=user\n"

// fakeRunner answers by joined argv; unknown commands get def.
type fakeRunner struct {
	mu    sync.Mutex
	calls []string
	by    map[string]runner.Result
	def   runner.Result
}

func (f *fakeRunner) Run(_ context.Context, args ...string) (runner.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k := strings.Join(args, " ")
	if strings.Contains(k, "plugin") && !strings.Contains(k, "--dry-run") {
		panic("a real plugin import reached the runner: " + k)
	}
	f.calls = append(f.calls, k)
	if r, ok := f.by[k]; ok {
		return r, nil
	}
	return f.def, nil
}

func (f *fakeRunner) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// imports lists the import commands that ran (agent list excluded).
func (f *fakeRunner) imports() []string {
	var out []string
	for _, c := range f.got() {
		if strings.HasPrefix(c, "import") {
			out = append(out, c)
		}
	}
	return out
}

func newRunner(agents, preview string) *fakeRunner {
	return &fakeRunner{
		by:  map[string]runner.Result{"agent list": {Stdout: agents}},
		def: runner.Result{Stdout: preview},
	}
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
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
	h := &shell{t: t, s: s, r: r}
	h.send(tea.WindowSizeMsg{Width: 140, Height: 40})
	h.pump(s.Init())
	return h
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
	case nil:
	default:
		h.send(m)
	}
}

func (h *shell) run(a actions.Action) {
	res, err := h.r.Run(context.Background(), a.Argv...)
	h.send(ui.RunMsg{Action: a, Result: res, Err: err})
}

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
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
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

func (h *shell) typeName(v string) {
	h.press("n")
	h.press("ctrl+u")
	for _, r := range v {
		h.press(string(r))
	}
	h.press("enter")
}

func (h *shell) view() string { return ansi.Strip(h.s.View().Content) }

// claudeShell selects agent claude (the second registered agent).
func claudeShell(t *testing.T, preview string) *shell {
	t.Helper()
	h := newShell(t, newRunner(agentsTwo, preview))
	h.press("a")
	if h.s.agentName() != "claude" {
		t.Fatalf("agent = %q", h.s.agentName())
	}
	return h
}

// setComponent cycles forward to component c ("all" for the empty one).
func (h *shell) setComponent(c string) {
	h.t.Helper()
	for i := 0; i < len(components); i++ {
		if compLabel(h.s.component()) == c {
			return
		}
		h.press("c")
	}
	h.t.Fatalf("component %q not reachable", c)
}

func asked1(t *testing.T, h *shell, want string) actions.Action {
	t.Helper()
	if len(h.asked) != 1 || strings.Join(h.asked[0].Argv, " ") != want || h.asked[0].ReadOnly {
		t.Fatalf("asked = %+v, want %q", h.asked, want)
	}
	return h.asked[0]
}

func TestImportSelectorGrammar(t *testing.T) {
	cases := []struct{ comp, name, sel string }{
		{"mcp", "", "claude:mcp"},
		{"all", "", "claude"},
		{"mcp", "github", "claude:mcp:github"},
	}
	for _, c := range cases {
		h := claudeShell(t, "plan")
		h.setComponent(c.comp)
		if c.name != "" {
			h.typeName(c.name)
		}
		h.press("p")
		if got := h.r.imports(); len(got) != 1 || got[0] != "import "+c.sel+" --dry-run" {
			t.Fatalf("%+v: preview calls = %q", c, got)
		}
		h.press("i")
		asked1(t, h, "import "+c.sel)
	}
	// rejected inline, nothing emitted
	bad := []struct{ comp, name, msg string }{
		{"mcp", "bad;name", "invalid name"},
		{"mcp", "--all", "invalid name"},
		{"all", "github", "requires a component"},
	}
	for _, c := range bad {
		h := claudeShell(t, "plan")
		h.setComponent(c.comp)
		h.typeName(c.name)
		h.press("p", "i")
		if len(h.r.imports()) != 0 || len(h.asked) != 0 {
			t.Fatalf("%+v emitted: %q %+v", c, h.r.imports(), h.asked)
		}
		if !strings.Contains(h.view(), c.msg) {
			t.Fatalf("%+v: no inline error:\n%s", c, h.view())
		}
	}
	h := claudeShell(t, "plan")
	h.s.comp = "bogus" // not reachable by cycling; the builder still rejects it
	h.press("p", "i")
	if len(h.r.imports()) != 0 || len(h.asked) != 0 || !strings.Contains(h.view(), "invalid component") {
		t.Fatalf("bogus component: %q %+v\n%s", h.r.imports(), h.asked, h.view())
	}
}

func TestImportPreviewBeforeRun(t *testing.T) {
	h := claudeShell(t, fixture(t, "claude-mcp-dry.stdout"))
	h.setComponent("mcp")
	h.press("i")
	if len(h.asked) != 0 || len(h.r.imports()) != 0 {
		t.Fatalf("i before p emitted: %+v %q", h.asked, h.r.imports())
	}
	if !strings.Contains(h.view(), "preview first") {
		t.Fatalf("no hint:\n%s", h.view())
	}
	h.press("p")
	if !strings.Contains(h.view(), "would import mcp/fsdemo.toml") {
		t.Fatalf("preview not shown:\n%s", h.view())
	}
	h.press("i")
	asked1(t, h, "import claude:mcp")
	h.confirm()
	if got := h.r.imports(); strings.Join(got, "|") != "import claude:mcp --dry-run|import claude:mcp" {
		t.Fatalf("imports = %q", got)
	}
}

func TestImportFailedPreviewDoesNotArm(t *testing.T) {
	r := newRunner(agentsTwo, "")
	r.by["import claude:mcp:nosuch --dry-run"] = runner.Result{
		ExitCode: 1, Stderr: fixture(t, "claude-mcp-missing-dry.stderr"),
	}
	h := newShell(t, r)
	h.press("a")
	h.setComponent("mcp")
	h.typeName("nosuch")
	h.press("p", "i")
	if len(h.asked) != 0 {
		t.Fatal("failed preview armed the import")
	}
	if v := h.view(); !strings.Contains(v, `mcp server "nosuch" not found`) || !strings.Contains(v, "exit 1") {
		t.Fatalf("failure not shown:\n%s", v)
	}
}

func TestImportGateInvalidations(t *testing.T) {
	steps := map[string]func(h *shell){
		"edit name":       func(h *shell) { h.typeName("x"); h.typeName("") },
		"cycle component": func(h *shell) { h.press("c", "C") },
		"cycle agent":     func(h *shell) { h.press("a", "A") },
		"set scope":       func(h *shell) { h.s.SetScope(h.r, runner.ScopeUser, ""); h.pump(h.s.Refresh()) },
		"own run success": func(h *shell) { h.press("i"); h.confirm() },
		"own run failure": func(h *shell) {
			h.press("i")
			a := h.asked[0]
			h.asked = nil
			h.send(ui.RunMsg{Action: a, Result: runner.Result{ExitCode: 1, Stderr: "boom"}})
		},
		"own run cancel": func(h *shell) {
			h.press("i")
			a := h.asked[0]
			h.asked = nil
			h.send(ui.RunMsg{Action: a, Err: context.Canceled})
		},
		"foreign mutation": func(h *shell) {
			a, _ := actions.McpDisable("github")
			h.send(ui.RunMsg{Action: a})
		},
		"edit started": func(h *shell) { h.send(ui.EditOutcomeMsg{Path: "/x/skill.md", Started: true}) },
		"edit finished": func(h *shell) {
			h.send(ui.EditOutcomeMsg{Path: "/x/skill.md", Finished: true})
		},
		"failed edit finished": func(h *shell) {
			h.send(ui.EditOutcomeMsg{Path: "/x/skill.md", Finished: true, Err: "editor failed (exit 1)"})
		},
	}
	for name, step := range steps {
		t.Run(name, func(t *testing.T) {
			h := claudeShell(t, "plan")
			h.setComponent("mcp")
			h.press("p")
			step(h)
			h.asked = nil
			h.press("i")
			if len(h.asked) != 0 {
				t.Fatalf("preview survived %s: %+v", name, h.asked)
			}
			if !strings.Contains(h.view(), "preview first") {
				t.Fatalf("no hint after %s:\n%s", name, h.view())
			}
			// not stuck: a fresh preview re-arms
			h.press("p", "i")
			if len(h.asked) != 1 {
				t.Fatalf("stuck after %s", name)
			}
		})
	}
}

// A refused edit ran no editor, so it changed nothing.
func TestImportRefusedEditKeepsPreview(t *testing.T) {
	h := claudeShell(t, "plan")
	h.setComponent("mcp")
	h.press("p")
	h.send(ui.EditOutcomeMsg{Path: "/x/skill.md", Err: "edit refused: outside the agentsync home"})
	h.press("i")
	asked1(t, h, "import claude:mcp")
}

func TestImportForeignReadOnlyKeepsPreview(t *testing.T) {
	h := claudeShell(t, "plan")
	h.setComponent("mcp")
	h.press("p")
	h.send(ui.RunMsg{Action: actions.Check(), Result: runner.Result{Stdout: "OTHER"}})
	h.send(ui.RunMsg{Action: actions.ReconcileReport(), Result: runner.Result{Stdout: "OTHER"}})
	h.press("i")
	asked1(t, h, "import claude:mcp")
	if strings.Contains(h.view(), "OTHER") {
		t.Fatal("foreign output shown")
	}
}

func TestImportPreviewSurvivesRefresh(t *testing.T) {
	h := claudeShell(t, "plan")
	h.setComponent("mcp")
	h.press("p")
	h.pump(h.s.Refresh())
	h.press("i")
	asked1(t, h, "import claude:mcp")
}

func TestImportStalePreviewResultDropped(t *testing.T) {
	h := claudeShell(t, "plan")
	h.setComponent("mcp")
	_, cmd := h.s.Update(k("p"))
	do := cmd().(ui.DoMsg)
	h.typeName("github") // inputs change while the preview is in flight
	h.typeName("")       // and change back: still a different request
	h.send(ui.RunMsg{Action: do.Action, Result: runner.Result{Stdout: "STALE"}})
	h.press("i")
	if len(h.asked) != 0 || strings.Contains(h.view(), "STALE") {
		t.Fatalf("stale result accepted: %+v\n%s", h.asked, h.view())
	}
	// a scope switch drops it too
	h = claudeShell(t, "plan")
	h.setComponent("mcp")
	_, cmd = h.s.Update(k("p"))
	do = cmd().(ui.DoMsg)
	h.s.SetScope(h.r, runner.ScopeProject, "")
	h.send(ui.RunMsg{Action: do.Action, Result: runner.Result{Stdout: "STALE"}})
	h.press("i")
	if len(h.asked) != 0 || strings.Contains(h.view(), "STALE") {
		t.Fatal("result from before the scope switch accepted")
	}
}

func TestImportNetworkFlagForPluginAndAll(t *testing.T) {
	for comp, net := range map[string]bool{"all": true, "plugin": true, "mcp": false, "skill": false} {
		h := claudeShell(t, "plan")
		h.setComponent(comp)
		if got := strings.Contains(h.view(), "[network]"); got != net {
			t.Fatalf("%s: network badge shown=%v\n%s", comp, got, h.view())
		}
		h.press("p", "i")
		if len(h.asked) != 1 || h.asked[0].Network != net {
			t.Fatalf("%s: asked = %+v", comp, h.asked)
		}
		// the fake runner panics if a real plugin import reaches it; never confirm here
	}
}

const canaryOut = "  → would import mcp/x.toml password: CANARYx1\ntoken=CANARYx1\n" +
	"Authorization: Bearer CANARYx1\n\x1b[31mred\x1b[0m \u202eevil\n"

func TestImportOutputStoredMasked(t *testing.T) {
	h := claudeShell(t, canaryOut)
	h.setComponent("mcp")
	h.press("p")
	o := h.s.out
	if o == nil {
		t.Fatal("no output stored")
	}
	for _, bad := range []string{"CANARYx1", "\x1b", "\u202e"} {
		if strings.Contains(o.Text, bad) || strings.Contains(h.view(), bad) {
			t.Fatalf("%q survived (stored %q)", bad, o.Text)
		}
	}
	if !strings.Contains(o.Text, "would import mcp/x.toml") || !strings.Contains(o.Text, "password: ••••") {
		t.Fatalf("stored = %q", o.Text)
	}
	// errors and stderr are masked as well
	h.press("i")
	a := h.asked[0]
	h.asked = nil
	h.send(ui.RunMsg{Action: a, Result: runner.Result{ExitCode: 2, Stderr: "fail token=CANARYx1"}, Err: errors.New("x secret=CANARYx1")})
	if strings.Contains(h.s.out.Text+h.s.out.Note, "CANARYx1") || strings.Contains(h.view(), "CANARYx1") {
		t.Fatalf("error leaked: %+v", h.s.out)
	}
	// capped at 64 KiB
	h = claudeShell(t, strings.Repeat("  → would import mcp/x.toml\n", 5000))
	h.setComponent("mcp")
	h.press("p")
	if len(h.s.out.Text) > 64<<10 || !h.s.out.Truncated {
		t.Fatalf("len=%d trunc=%v", len(h.s.out.Text), h.s.out.Truncated)
	}
	for i := 0; i < 3000; i++ {
		h.press("pgdown")
	}
	if !strings.Contains(h.view(), "output truncated") {
		t.Fatal("no truncation note")
	}
}

func TestImportNoRegisteredAgentsEmptyState(t *testing.T) {
	h := newShell(t, newRunner(fixture(t, "../../../model/testdata/agent-list-empty.txt"), "plan"))
	if !strings.Contains(h.view(), "no registered agents") {
		t.Fatalf("no empty state:\n%s", h.view())
	}
	h.press("a", "c", "p", "i")
	if len(h.r.imports()) != 0 || len(h.asked) != 0 {
		t.Fatalf("emitted with no agents: %q %+v", h.r.imports(), h.asked)
	}
	// an agent list failure is a note, not a crash
	r := newRunner("", "plan")
	r.by["agent list"] = runner.Result{ExitCode: 1, Stderr: "✗ ERROR  no agentsync config token=CANARYx1"}
	h = newShell(t, r)
	if v := h.view(); !strings.Contains(v, "no agentsync config") || strings.Contains(v, "CANARYx1") {
		t.Fatalf("failure note:\n%s", v)
	}
}

func TestImportPendingDisplayShownInHeader(t *testing.T) {
	h := claudeShell(t, "plan")
	h.setComponent("mcp")
	h.typeName("github")
	lines := strings.Split(h.view(), "\n")
	head := strings.Join(lines[:min(len(lines), headLines)], "\n")
	if !strings.Contains(head, "will run: agentsync import claude:mcp:github") {
		t.Fatalf("header:\n%s", head)
	}
	if strings.Contains(head, "--dry-run") {
		t.Fatalf("header shows the dry run, not the real action:\n%s", head)
	}
}

func TestImportScopeAwareSwapsRunner(t *testing.T) {
	h := claudeShell(t, "plan")
	var _ ui.ScopeAware = h.s
	var _ ui.Screen = h.s
	r2 := newRunner("codex enabled=true scope=project\n", "plan")
	h.s.SetScope(r2, runner.ScopeProject, "")
	h.r = r2
	h.pump(h.s.Refresh())
	if got := r2.got(); len(got) != 1 || got[0] != "agent list" || h.s.scope != runner.ScopeProject {
		t.Fatalf("r2 calls = %q", got)
	}
	if h.s.agentName() != "codex" {
		t.Fatalf("agent = %q", h.s.agentName())
	}
}

func TestImportInitEqualsRefresh(t *testing.T) {
	r := newRunner(agentsTwo, "plan")
	s := New(ui.Deps{Runner: r, Scope: runner.ScopeUser})
	m1 := s.Init()()
	m2 := s.Refresh()()
	a1, ok1 := m1.(agentsMsg)
	a2, ok2 := m2.(agentsMsg)
	if !ok1 || !ok2 || len(a1.agents) != len(a2.agents) || a1.note != a2.note {
		t.Fatalf("Init %+v != Refresh %+v", m1, m2)
	}
	if strings.Join(r.got(), "|") != "agent list|agent list" {
		t.Fatalf("calls = %q", r.got())
	}
	// and the older generation is dropped
	s.Update(m1)
	if s.loaded {
		t.Fatal("stale agent list applied")
	}
	s.Update(m2)
	if !s.loaded || s.agentName() != "antigravity" {
		t.Fatalf("current agent list not applied: %q", s.agentName())
	}
}

func TestImportKeyCapturerWhileTyping(t *testing.T) {
	h := claudeShell(t, "plan")
	var kc ui.KeyCapturer = h.s
	if kc.CapturesKeys() {
		t.Fatal("captures while idle")
	}
	h.setComponent("mcp")
	h.press("n")
	if !kc.CapturesKeys() {
		t.Fatal("not capturing while typing")
	}
	h.press("p", "i", "a", "c", "s", "q")
	if len(h.r.imports()) != 0 || len(h.asked) != 0 || h.s.name != "piacsq" {
		t.Fatalf("typing leaked: name=%q imports=%q", h.s.name, h.r.imports())
	}
	h.press("backspace")
	if h.s.name != "piacs" {
		t.Fatalf("backspace: %q", h.s.name)
	}
	h.press("esc")
	if kc.CapturesKeys() {
		t.Fatal("still capturing after esc")
	}
	if h.s.agentName() != "claude" || compLabel(h.s.component()) != "mcp" {
		t.Fatal("typed keys changed the selection")
	}
}

// reloadAgents makes the next `agent list` return list and delivers it.
func (h *shell) reloadAgents(list string) {
	h.r.mu.Lock()
	h.r.by["agent list"] = runner.Result{Stdout: list}
	h.r.mu.Unlock()
	h.pump(h.s.Refresh())
}

func TestImportAgentReloadDroppingSelectionNeedsPreview(t *testing.T) {
	h := claudeShell(t, "plan")
	h.setComponent("mcp")
	h.press("p")
	h.reloadAgents("antigravity enabled=true scope=user\n") // claude is gone
	if h.s.agentName() != "antigravity" {
		t.Fatalf("selection = %q", h.s.agentName())
	}
	h.press("i")
	if len(h.asked) != 0 {
		t.Fatalf("import of another agent without its own preview: %+v", h.asked)
	}
	if !strings.Contains(h.view(), "preview first") {
		t.Fatalf("no hint:\n%s", h.view())
	}
}

func TestImportAgentReloadKeepingSelectionKeepsPreview(t *testing.T) {
	h := claudeShell(t, "plan")
	h.setComponent("mcp")
	h.press("p")
	h.reloadAgents(agentsTwo + "codex enabled=true scope=user\n")
	if h.s.agentName() != "claude" {
		t.Fatalf("selection = %q", h.s.agentName())
	}
	h.press("i")
	asked1(t, h, "import claude:mcp")
}

// Defence in depth: a reload that changes the selection invalidates the gate
// itself, so the argv comparison in allows is not the only guard.
func TestImportAgentReloadChangingSelectionInvalidatesGate(t *testing.T) {
	h := claudeShell(t, "plan")
	h.setComponent("mcp")
	h.press("p")
	if !h.s.g.Pending() {
		t.Fatal("preview did not arm the gate")
	}
	h.reloadAgents("antigravity enabled=true scope=user\n")
	if h.s.g.Pending() {
		t.Fatal("gate not invalidated by the selection change")
	}
	// a harmless reload does not
	h = claudeShell(t, "plan")
	h.setComponent("mcp")
	h.press("p")
	h.reloadAgents(agentsTwo)
	if !h.s.g.Pending() {
		t.Fatal("harmless reload invalidated the gate")
	}
}
