package gate

import (
	"context"
	"testing"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

func importPair(t *testing.T, agent, comp string) actions.Previewed {
	t.Helper()
	p, err := actions.ImportPair(agent, comp, "")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// armed has a successful preview of `import claude:mcp` in user scope.
func armed(t *testing.T) (*Gate, actions.Previewed) {
	t.Helper()
	g := &Gate{}
	p := importPair(t, "claude", "mcp")
	g.Request(p, runner.ScopeUser)
	if g.Observe(ui.RunMsg{Action: p.Preview}) != PreviewOK {
		t.Fatal("preview not accepted")
	}
	return g, p
}

func TestAllowsExactMatchOnly(t *testing.T) {
	g, p := armed(t)
	if !g.Allows(p.Real, runner.ScopeUser) {
		t.Fatal("exact command and scope refused")
	}
	if g.Allows(importPair(t, "antigravity", "mcp").Real, runner.ScopeUser) {
		t.Fatal("different agent, same scope allowed")
	}
	if g.Allows(importPair(t, "claude", "").Real, runner.ScopeUser) {
		t.Fatal("different component, same scope allowed")
	}
	if g.Allows(p.Real, runner.ScopeProject) {
		t.Fatal("same command, different scope allowed")
	}
	if g.Allows(p.Preview, runner.ScopeUser) {
		t.Fatal("the dry run itself allowed as the real command")
	}
	if (&Gate{}).Allows(p.Real, runner.ScopeUser) {
		t.Fatal("unarmed gate allowed")
	}
}

// The scope is bound at request time: a preview requested in one scope
// never allows the real command in another, however the screen's own scope
// field changes.
func TestScopeBoundAtRequest(t *testing.T) {
	g := &Gate{}
	p := importPair(t, "claude", "mcp")
	g.Request(p, runner.ScopeProject)
	if g.Observe(ui.RunMsg{Action: p.Preview}) != PreviewOK {
		t.Fatal("preview not accepted")
	}
	if g.Allows(p.Real, runner.ScopeUser) || !g.Allows(p.Real, runner.ScopeProject) {
		t.Fatal("preview not bound to the project scope")
	}
}

func TestStaleAndForeignResults(t *testing.T) {
	g := &Gate{}
	p := importPair(t, "claude", "mcp")
	g.Request(p, runner.ScopeUser)
	g.Invalidate() // an input edit while the dry run runs
	if got := g.Observe(ui.RunMsg{Action: p.Preview}); got != NotOurs || g.Pending() {
		t.Fatalf("stale dry run: %v pending=%v", got, g.Pending())
	}
	g.Request(p, runner.ScopeUser)
	if got := g.Observe(ui.RunMsg{Action: actions.Check()}); got != NotOurs || !g.Pending() {
		t.Fatalf("foreign read-only run: %v pending=%v", got, g.Pending())
	}
	if got := g.Observe(ui.RunMsg{Action: p.Preview, Result: runner.Result{ExitCode: 1}}); got != PreviewFail || g.Allows(p.Real, runner.ScopeUser) {
		t.Fatalf("failed dry run: %v", got)
	}
}

func TestRealRunAndForeignMutationDrop(t *testing.T) {
	g, p := armed(t)
	g.Arm(p.Real)
	if got := g.Observe(ui.RunMsg{Action: p.Real, Err: context.Canceled}); got != RanReal || g.Pending() {
		t.Fatalf("cancelled real run: %v pending=%v", got, g.Pending())
	}
	g, _ = armed(t)
	other, _ := actions.McpEnable("x")
	if got := g.Observe(ui.RunMsg{Action: other}); got != Dropped || g.Pending() {
		t.Fatalf("foreign mutation: %v pending=%v", got, g.Pending())
	}
	g, _ = armed(t)
	if !g.Drop() || g.Pending() || g.Drop() {
		t.Fatal("Drop must report whether a preview was held")
	}
}
