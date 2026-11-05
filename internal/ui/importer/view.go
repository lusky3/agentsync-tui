package importer

import (
	"errors"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/ui"
	"github.com/lusky3/agentsync-tui/internal/ui/theme"
)

const keysTxt = "a/A agent • c/C component • n name • p preview • i import • r reload agents • " + ui.ScrollKeys

func (s *Screen) outLines() []string {
	if s.out == nil {
		return nil
	}
	return s.out.Lines()
}

// agentLabel starts the agent selector line; the value follows it.
const agentLabel = "  agent:     "

func (s *Screen) agentLine() string {
	switch {
	case !s.loaded:
		return agentLabel + theme.Dim.Render("loading agents…")
	case len(s.agents) == 0 && s.note == "":
		return agentLabel + theme.Warn.Render("no registered agents (add one with: agentsync agent add <name>)")
	case len(s.agents) == 0:
		return agentLabel + theme.Bad.Render(mask.OneLine(s.note))
	}
	line := agentLabel + theme.Bold.Render(mask.OneLine(s.agent))
	for i, a := range s.agents {
		if a.Name == s.agent {
			line += theme.Dim.Render("  (" + strconv.Itoa(i+1) + "/" + strconv.Itoa(len(s.agents)) + ")")
			if !a.Enabled {
				line += theme.Warn.Render("  disabled")
			}
		}
	}
	if s.note != "" {
		line += "  " + theme.Bad.Render(mask.OneLine(s.note))
	}
	return line
}

// header is every fixed line above the output: the form, the command, the
// status and the output title. The body gets whatever height is left.
func (s *Screen) header() []string {
	lines := []string{
		theme.Bold.Render("Import") + theme.Dim.Render("  adopt an agent's native config into the canonical source"),
		s.agentLine(),
		"  component: " + theme.Bold.Render(compLabel(s.comp)),
	}
	name := mask.OneLine(s.name.Value)
	switch {
	case s.typing:
		name = theme.Sel.Render(name + " ")
	case name == "":
		name = theme.Dim.Render("(every item; press n to name one)")
	}
	lines = append(lines, "  name:      "+name)
	p, err := s.pair()
	switch {
	case errors.Is(err, errNoAgent):
		lines = append(lines, theme.Dim.Render("  will run: (nothing: no agent selected)"))
	case err != nil:
		lines = append(lines, theme.Bad.Render("  "+mask.OneLine(mask.Text(err.Error()))))
	default:
		l := theme.Bold.Render("  will run: ") + mask.OneLine(p.Real.Display())
		if p.Real.Network {
			l += " " + theme.Warn.Render("[network]")
		}
		lines = append(lines, l)
	}
	st := theme.Dim.Render("  no preview yet: " + msgPreviewFirst)
	if err == nil && s.g.Allows(p.Real, s.scope) {
		st = theme.OK.Render("  preview ok: press i to import")
	}
	if s.msg != "" {
		st = theme.Warn.Render("  " + mask.OneLine(s.msg))
	}
	lines = append(lines, st, "")
	head := theme.Dim.Render("output")
	if s.out != nil {
		head = theme.Bold.Render(s.out.Title)
		if s.out.Note != "" {
			head += "  " + theme.Bad.Render(mask.OneLine(s.out.Note))
		}
	}
	return append(lines, head)
}

// View implements tea.Model. Every outside string is sanitised or masked.
func (s *Screen) View() tea.View {
	lines := s.scroll.Frame(s.header(), s.outLines(), ui.KeyLines(keysTxt, s.width), s.height)
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, s.width, "…")
	}
	return tea.NewView(strings.Join(lines, "\n"))
}
