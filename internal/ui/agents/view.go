package agents

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/ui"
	"github.com/lusky3/agentsync-tui/internal/ui/theme"
)

const (
	listKeys    = "a add • e enable/disable • x remove • D disable+purge (deletes owned files) • r refresh • " + ui.ScrollKeys
	emptyNotice = "no agents registered — press a on an available agent to add it"
)

// View implements tea.Model. Every outside string is sanitised or masked.
// The rows are windowed around the cursor so the footer is always visible.
func (s *Screen) View() tea.View {
	var header, rows []string
	switch {
	case !s.loaded:
		header = append(header, theme.Dim.Render("loading…"))
	default:
		header, rows = s.table()
	}
	var footer []string
	if s.loadNote != "" {
		footer = append(footer, theme.Dim.Render("agent list unavailable: "+s.loadNote))
	}
	if s.note != "" {
		footer = append(footer, theme.Warn.Render(s.note))
	}
	footer = append(footer, ui.KeyLines(listKeys, s.width)...)
	lines := s.list.Frame(header, rows, footer, s.height)
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, s.width, "…")
	}
	return tea.NewView(strings.Join(lines, "\n"))
}

// table returns the header lines (empty notice, column titles) and one row
// per agent.
func (s *Screen) table() (header, rows []string) {
	registered := 0
	for _, a := range s.agents {
		if a.Registered {
			registered++
		}
	}
	if registered == 0 {
		header = append(header, emptyNotice)
	}
	if len(s.agents) == 0 {
		return header, nil
	}
	header = append(header, theme.Bold.Render("  "+theme.Pad("AGENT", 20)+theme.Pad("STATUS", 12)+theme.Pad("STATE", 10)+"SCOPE"))
	for i, a := range s.agents {
		status, state, scope := "available", "-", "-"
		if a.Registered {
			status, state = "registered", "disabled"
			if a.Enabled {
				state = "enabled"
			}
		}
		if a.Scope != "" {
			scope = mask.OneLine(a.Scope)
		}
		line := theme.Pad(mask.OneLine(a.Name), 20) + theme.Pad(status, 12) + theme.Pad(state, 10) + scope
		switch {
		case i == s.list.Cursor:
			line = theme.Sel.Render("> " + line)
		case a.Registered && a.Enabled:
			line = "  " + theme.OK.Render(line)
		case !a.Registered:
			line = "  " + theme.Dim.Render(line)
		default:
			line = "  " + line
		}
		rows = append(rows, line)
	}
	return header, rows
}
