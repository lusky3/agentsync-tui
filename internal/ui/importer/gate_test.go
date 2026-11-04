package importer

import (
	"testing"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

func mustImport(t *testing.T, agent, comp string, dry bool) actions.Action {
	t.Helper()
	a, err := actions.Import(agent, comp, "", dry)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// armedGate has a successful preview of `import claude:mcp` in user scope.
func armedGate(t *testing.T) *gate {
	t.Helper()
	g := &gate{}
	dry := mustImport(t, "claude", "mcp", true)
	g.request(dry)
	if g.observe(ui.RunMsg{Action: dry}, runner.ScopeUser) != previewOK {
		t.Fatal("preview not accepted")
	}
	return g
}

func TestGateAllowsExactMatchOnly(t *testing.T) {
	g := armedGate(t)
	if !g.allows(mustImport(t, "claude", "mcp", false), runner.ScopeUser) {
		t.Fatal("exact selector and scope refused")
	}
	if g.allows(mustImport(t, "antigravity", "mcp", false), runner.ScopeUser) {
		t.Fatal("different agent, same scope allowed")
	}
	if g.allows(mustImport(t, "claude", "", false), runner.ScopeUser) {
		t.Fatal("different component, same scope allowed")
	}
	if g.allows(mustImport(t, "claude", "mcp", false), runner.ScopeProject) {
		t.Fatal("same selector, different scope allowed")
	}
	if (&gate{}).allows(mustImport(t, "claude", "mcp", false), runner.ScopeUser) {
		t.Fatal("unarmed gate allowed")
	}
}
