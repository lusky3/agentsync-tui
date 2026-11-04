package agents

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/ui/theme"
)

const (
	listKeys    = "a add • e enable/disable • x remove • D disable+purge (deletes owned files) • j/k move • r refresh"
	emptyNotice = "no agents registered — press a on an available agent to add it"
)

// View implements tea.Model. Every outside string is sanitised or masked.
func (s *Screen) View() tea.View {
	var out []string
	switch {
	case !s.loaded:
		out = append(out, theme.Dim.Render("loading…"))
	default:
		out = append(out, s.rows()...)
	}
	if s.loadNote != "" {
		out = append(out, theme.Dim.Render("agent list unavailable: "+s.loadNote))
	}
	if s.note != "" {
		out = append(out, theme.Warn.Render(s.note))
	}
	out = append(out, theme.Dim.Render(listKeys))
	lines := strings.Split(strings.Join(out, "\n"), "\n")
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, s.width, "…")
	}
	return tea.NewView(strings.Join(lines, "\n"))
}

func (s *Screen) rows() []string {
	var out []string
	registered := 0
	for _, a := range s.agents {
		if a.Registered {
			registered++
		}
	}
	if registered == 0 {
		out = append(out, emptyNotice)
	}
	if len(s.agents) == 0 {
		return out
	}
	out = append(out, theme.Bold.Render("  "+theme.Pad("AGENT", 20)+theme.Pad("STATUS", 12)+theme.Pad("STATE", 10)+"SCOPE"))
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
		case i == s.cursor:
			line = theme.Sel.Render("> " + line)
		case a.Registered && a.Enabled:
			line = "  " + theme.OK.Render(line)
		case !a.Registered:
			line = "  " + theme.Dim.Render(line)
		default:
			line = "  " + line
		}
		out = append(out, line)
	}
	return out
}
