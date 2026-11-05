// Package app assembles the application's screens in tab order.
package app

import (
	"github.com/lusky3/agentsync-tui/internal/ui"
	"github.com/lusky3/agentsync-tui/internal/ui/agents"
	"github.com/lusky3/agentsync-tui/internal/ui/apply"
	"github.com/lusky3/agentsync-tui/internal/ui/drift"
	"github.com/lusky3/agentsync-tui/internal/ui/health"
	"github.com/lusky3/agentsync-tui/internal/ui/importer"
	"github.com/lusky3/agentsync-tui/internal/ui/library"
	"github.com/lusky3/agentsync-tui/internal/ui/plugins"
	"github.com/lusky3/agentsync-tui/internal/ui/reconcile"
	"github.com/lusky3/agentsync-tui/internal/ui/secrets"
	"github.com/lusky3/agentsync-tui/internal/ui/servers"
)

// Screens returns every screen in tab order (15 tabs). Digits 1-9 jump to the
// first nine; tab and shift+tab reach the rest.
func Screens(d ui.Deps) []ui.Screen {
	return []ui.Screen{
		servers.New(d),
		drift.New(d),
		apply.New(d),
		reconcile.New(d),
		importer.New(d),
		agents.New(d),
		plugins.New(d),
		secrets.New(d),
		library.New(d, library.Skill),
		library.New(d, library.Subagent),
		library.New(d, library.Command),
		library.New(d, library.Hook),
		library.New(d, library.LSP),
		library.New(d, library.Memory),
		health.New(d),
	}
}
