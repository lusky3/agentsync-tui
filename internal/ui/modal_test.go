// The confirm modal: y/n/esc, typed verbs, badges, scope line, small heights.
package ui

import (
	"fmt"
	"github.com/charmbracelet/x/ansi"
	"os/exec"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/runner"
)

func TestPurgeActionIsDestructiveWithPurgeVerb(t *testing.T) {
	a, err := actions.AgentDisablePurge("claude")
	if err != nil || !a.Destructive {
		t.Fatalf("purge must be destructive: %+v %v", a, err)
	}
	if got := newModal(a, runner.ScopeUser, "").verb; got != "purge" {
		t.Fatalf("confirm verb = %q, want purge", got)
	}
}

func TestPurgeModalShowsWarningVerbAndBadge(t *testing.T) {
	act, err := actions.AgentDisablePurge("claude")
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(baseDeps(newFakeRunner()), newScreen("A", "body-a"))
	app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app.Update(DoMsg{Action: act})
	out := ansi.Strip(content(app))
	flat := strings.Join(strings.Fields(strings.ReplaceAll(out, "│", " ")), " ") // the warning wraps inside the box
	for _, want := range []string{"This cannot be undone", "Deletes the destination files agentsync owns", "[destructive]", "Type purge to confirm", "agent disable claude --purge"} {
		if !strings.Contains(flat, want) {
			t.Errorf("modal missing %q:\n%s", want, out)
		}
	}
}

func TestModalWarningIsSanitised(t *testing.T) {
	act := actions.Action{Title: "t", Argv: []string{"apply"}, Destructive: true,
		Warning: "bad \x1b[31mred\x1b]0;pwn\x07 \u202eevil"}
	app := NewApp(baseDeps(newFakeRunner()), newScreen("A", "body-a"))
	app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app.Update(DoMsg{Action: act})
	out := ansi.Strip(content(app))
	for _, bad := range []string{"\x1b", "\x07", "\u202e"} {
		if strings.Contains(out, bad) {
			t.Errorf("modal leaks %q:\n%q", bad, out)
		}
	}
	if !strings.Contains(out, "bad") {
		t.Error("warning not rendered")
	}
}

func TestConfirmModalCancelDoesNotRun(t *testing.T) {
	fr := newFakeRunner()
	h := start(t, baseDeps(fr), newScreen("A", "a"))
	act := mustAct(actions.McpEnable("x"))
	h.tm.Send(Do(act, runner.ScopeUser)())
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
	// mustNotRun asserts that the enter just processed left the modal open
	// and started nothing.
	mustNotRun := func(label string, o observed) {
		t.Helper()
		if !o.modalOpen || o.running {
			t.Fatalf("%s: modalOpen=%v running=%v, want modal open and nothing running", label, o.modalOpen, o.running)
		}
		if n := fr.nCalls(); n != 0 {
			t.Fatalf("%s: runner called %d times", label, n)
		}
	}
	clear := func(n int) {
		for i := 0; i < n; i++ {
			h.pressAndWait(key("backspace"))
		}
	}
	mustNotRun("y", h.pressAndWait(key("y")))
	clear(1)
	mustNotRun("enter on empty buffer", h.pressAndWait(key("enter")))
	h.pressAndWait(key("y"))
	mustNotRun("y then enter", h.pressAndWait(key("enter")))
	clear(1)
	for _, wrong := range []string{"REMOVE", " remove", "removex", "remov"} {
		for _, r := range wrong {
			h.pressAndWait(key(string(r)))
		}
		mustNotRun(fmt.Sprintf("wrong verb %q", wrong), h.pressAndWait(key("enter")))
		clear(len(wrong))
	}
	for _, r := range "remove" {
		h.pressAndWait(key(string(r)))
	}
	if o := h.pressAndWait(key("enter")); o.modalOpen || !o.running {
		t.Fatalf("typed verb did not start the run: modalOpen=%v running=%v", o.modalOpen, o.running)
	}
	if got := h.waitStarted(); strings.Join(got, " ") != "mcp remove x" {
		t.Errorf("ran %q", got)
	}
	if n := fr.nCalls(); n != 1 {
		t.Errorf("runner called %d times, want exactly 1", n)
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

// The confirm prompt and badges must stay visible however short the body
// is: a modal taller than the body is clipped from the middle, never from
// the bottom.
func TestModalPromptVisibleAtSmallHeights(t *testing.T) {
	act, err := actions.AgentDisablePurge("claude")
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []int{10, 12, 24} {
		app := NewApp(baseDeps(newFakeRunner()), newScreen("A", "body-a"))
		app.Update(tea.WindowSizeMsg{Width: 100, Height: h})
		app.Update(DoMsg{Action: act})
		out := ansi.Strip(content(app))
		if n := strings.Count(out, "\n") + 1; n > h {
			t.Errorf("height %d: view has %d lines", h, n)
		}
		flat := strings.Join(strings.Fields(strings.ReplaceAll(out, "│", " ")), " ")
		for _, want := range []string{"Type purge to confirm", "[destructive]", "Disable agent"} {
			if !strings.Contains(flat, want) {
				t.Errorf("height %d: modal missing %q:\n%s", h, want, out)
			}
		}
		if h >= 12 && !strings.Contains(flat, "This cannot be undone") {
			t.Errorf("height %d: warning missing:\n%s", h, out)
		}
		if h == 10 && !strings.Contains(out, "│ …") {
			t.Errorf("height %d: no clip marker inside the box:\n%s", h, out)
		}
		// Typing still works against the visible prompt.
		for _, r := range "purge" {
			app.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
		}
		if !strings.Contains(ansi.Strip(content(app)), "purge_") {
			t.Errorf("height %d: typed verb not shown:\n%s", h, ansi.Strip(content(app)))
		}
	}
}

// A long Display line is width-truncated rather than wrapped when the body
// is short, so it cannot push the badges and prompt below the fold.
func TestModalLongDisplayKeepsPrompt(t *testing.T) {
	argv := []string{"mcp", "add", "x", "--command", "npx"}
	for i := 0; i < 40; i++ {
		argv = append(argv, "--arg", fmt.Sprintf("value-number-%02d", i))
	}
	act := actions.Action{Title: "Add server x", Argv: argv, Network: true}
	app := NewApp(baseDeps(newFakeRunner()), newScreen("A", "body-a"))
	app.Update(tea.WindowSizeMsg{Width: 100, Height: 12})
	app.Update(DoMsg{Action: act})
	out := ansi.Strip(content(app))
	if n := strings.Count(out, "\n") + 1; n > 12 {
		t.Errorf("view has %d lines", n)
	}
	flat := strings.Join(strings.Fields(strings.ReplaceAll(out, "│", " ")), " ")
	for _, want := range []string{"Run it? y = yes", "[network]", "agentsync mcp add x --command npx --arg value-number-00", "…"} {
		if !strings.Contains(flat, want) {
			t.Errorf("modal missing %q:\n%s", want, out)
		}
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "$ agentsync") && !strings.HasSuffix(strings.TrimRight(l, " │"), "…") {
			t.Errorf("command line not truncated with an ellipsis: %q", l)
		}
	}
	if strings.Contains(flat, "value-number-39") {
		t.Errorf("command wrapped instead of truncated:\n%s", out)
	}
	// At a normal height the whole command is still readable (wrapped).
	app.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	flat = strings.Join(strings.Fields(strings.ReplaceAll(ansi.Strip(content(app)), "│", " ")), " ")
	if !strings.Contains(flat, "value-number-39") || !strings.Contains(flat, "Run it? y = yes") {
		t.Errorf("tall view lost the command or prompt:\n%s", ansi.Strip(content(app)))
	}
}

// ---- project scope home (whole-branch review C-1) ---------------------------

// The confirm modal names the tree the command will run against.
func TestModalShowsScopeLine(t *testing.T) {
	disable, err := actions.McpDisable("x")
	if err != nil {
		t.Fatal(err)
	}
	secretRemove := actions.Action{Title: "Remove secret K", Argv: []string{"secret", "remove", "K"}, Destructive: true}
	cases := []struct {
		name    string
		scope   runner.Scope
		project string
		act     actions.Action
		want    string
		absent  string
	}{
		{"user", runner.ScopeUser, "/proj", disable, "scope: user", "/proj"},
		{"project", runner.ScopeProject, "/proj", disable, "scope: project — /proj", "scope: user"},
		{"project dir sanitised", runner.ScopeProject, "/pr\x1b[31moj", disable, "scope: project — /proj", "\x1b"},
		{"scope-unaware in project scope", runner.ScopeProject, "/proj", secretRemove, "scope: n/a (user-level)", "/proj"},
		{"scope-unaware in user scope", runner.ScopeUser, "", secretRemove, "scope: n/a (user-level)", "scope: user"},
	}
	for _, c := range cases {
		m := newModal(c.act, c.scope, c.project)
		out := ansi.Strip(m.view(100, 30))
		flat := strings.Join(strings.Fields(strings.ReplaceAll(out, "│", " ")), " ")
		if !strings.Contains(flat, c.want) {
			t.Errorf("%s: modal missing %q:\n%s", c.name, c.want, out)
		}
		if strings.Contains(flat, c.absent) {
			t.Errorf("%s: modal must not show %q:\n%s", c.name, c.absent, out)
		}
	}
	// Through the shell: the modal is built with the App's current scope.
	fr := newFakeRunner()
	d := baseDeps(fr)
	d.ProjectDir = "/proj"
	d.WithScope = func(runner.Scope) (runner.Runner, func(...string) *exec.Cmd) { return fr, nil }
	app := NewApp(d, newScreen("A", "a"))
	app.Update(key("s"))
	app.Update(DoMsg{Action: disable})
	if out := ansi.Strip(content(app)); !strings.Contains(out, "scope: project — /proj") {
		t.Errorf("shell modal missing the project scope line:\n%s", out)
	}
}

// TestNonDestructiveConfirmNeedsY: enter must not confirm a non-destructive
// action. The add forms submit on enter, so a double-press would otherwise
// open the modal and run `mcp add` / `plugin add` before it could be read.
func TestNonDestructiveConfirmNeedsY(t *testing.T) {
	fr := newFakeRunner()
	h := start(t, baseDeps(fr), newScreen("A", "a"))
	act := mustAct(actions.McpEnable("x"))
	if act.Destructive || act.ReadOnly {
		t.Fatal("precondition: a confirmable, non-destructive action")
	}
	h.tm.Send(DoMsg{Action: act})
	for i := 0; i < 2; i++ {
		if o := h.pressAndWait(key("enter")); !o.modalOpen || o.running {
			t.Fatalf("enter #%d: modalOpen=%v running=%v, want the modal still open", i+1, o.modalOpen, o.running)
		}
	}
	if n := fr.nCalls(); n != 0 {
		t.Fatalf("enter ran the action: %d runner calls", n)
	}
	if o := h.pressAndWait(key("y")); o.modalOpen || !o.running {
		t.Fatalf("y: modalOpen=%v running=%v", o.modalOpen, o.running)
	}
	h.waitStarted()
	fr.respond <- fakeResp{}
	h.waitRun()
	h.final()
}

func TestNonDestructiveModalSaysPressY(t *testing.T) {
	app := NewApp(baseDeps(newFakeRunner()), newScreen("A", "a"))
	app.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	app.Update(DoMsg{Action: mustAct(actions.McpEnable("x"))})
	if v := ansi.Strip(content(app)); !strings.Contains(v, "Run it? y = yes, n/esc = no") || strings.Contains(v, "enter") {
		t.Errorf("prompt should offer y only:\n%s", v)
	}
	if _, cmd := app.Update(key("enter")); cmd != nil || app.modal == nil {
		t.Fatal("enter confirmed the modal")
	}
	if v := ansi.Strip(content(app)); !strings.Contains(v, "press y to run it") {
		t.Errorf("no hint after enter:\n%s", v)
	}
}

// ---- interrupting a running write (production audit H1) -------------------
