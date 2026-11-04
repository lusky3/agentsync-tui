package agents

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/lusky3/agentsync-tui/internal/mask"
)

var (
	styleBold = lipgloss.NewStyle().Bold(true)
	styleDim  = lipgloss.NewStyle().Faint(true)
	styleWarn = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styleOK   = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleSel  = lipgloss.NewStyle().Reverse(true)
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
		out = append(out, styleDim.Render("loading…"))
	default:
		out = append(out, s.rows()...)
	}
	if s.loadNote != "" {
		out = append(out, styleDim.Render("agent list unavailable: "+s.loadNote))
	}
	if s.note != "" {
		out = append(out, styleWarn.Render(s.note))
	}
	out = append(out, styleDim.Render(listKeys))
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
	out = append(out, styleBold.Render("  "+pad("AGENT", 20)+pad("STATUS", 12)+pad("STATE", 10)+"SCOPE"))
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
		line := pad(mask.OneLine(a.Name), 20) + pad(status, 12) + pad(state, 10) + scope
		switch {
		case i == s.cursor:
			line = styleSel.Render("> " + line)
		case a.Registered && a.Enabled:
			line = "  " + styleOK.Render(line)
		case !a.Registered:
			line = "  " + styleDim.Render(line)
		default:
			line = "  " + line
		}
		out = append(out, line)
	}
	return out
}

func pad(s string, n int) string {
	s = ansi.Truncate(s, n-1, "…")
	return s + strings.Repeat(" ", max(n-ansi.StringWidth(s), 0))
}

func itoa(n int) string { return strconv.Itoa(n) }
