package importer

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/ui/theme"
)

const (
	keysTxt = "a/A agent • c/C component • n name • p preview • i import • r reload agents • ctrl+d/ctrl+u scroll"
	// headLines is the number of fixed lines above the output body.
	headLines = 8
)

// avail is how many output lines fit under the header and above the footer.
func (s *Screen) avail() int { return max(s.height-headLines-1, 1) }

func (s *Screen) outLines() []string {
	if s.out == nil {
		return nil
	}
	var ls []string
	if s.out.text != "" {
		for _, l := range strings.Split(strings.TrimRight(s.out.text, "\n"), "\n") {
			ls = append(ls, strings.ReplaceAll(l, "\t", "  "))
		}
	}
	if s.out.trunc {
		ls = append(ls, theme.Warn.Render("output truncated"))
	}
	return ls
}

func (s *Screen) maxScroll() int { return max(len(s.outLines())-s.avail(), 0) }

func (s *Screen) agentLine() string {
	switch {
	case !s.loaded:
		return "  agent:     " + theme.Dim.Render("loading agents…")
	case len(s.agents) == 0 && s.note == "":
		return "  agent:     " + theme.Warn.Render("no registered agents (add one with: agentsync agent add <name>)")
	case len(s.agents) == 0:
		return "  agent:     " + theme.Bad.Render(mask.OneLine(s.note))
	}
	line := "  agent:     " + theme.Bold.Render(mask.OneLine(s.agent))
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

// View implements tea.Model. Every outside string is sanitised or masked.
func (s *Screen) View() tea.View {
	lines := []string{
		theme.Bold.Render("Import") + theme.Dim.Render("  adopt an agent's native config into the canonical source"),
		s.agentLine(),
		"  component: " + theme.Bold.Render(compLabel(s.comp)),
	}
	name := mask.OneLine(s.name)
	switch {
	case s.typing:
		name = theme.Sel.Render(name + " ")
	case name == "":
		name = theme.Dim.Render("(every item; press n to name one)")
	}
	lines = append(lines, "  name:      "+name)
	act, err := s.build(false)
	switch {
	case err == errNoAgent:
		lines = append(lines, theme.Dim.Render("  will run: (nothing: no agent selected)"))
	case err != nil:
		lines = append(lines, theme.Bad.Render("  "+mask.OneLine(mask.Text(err.Error()))))
	default:
		l := theme.Bold.Render("  will run: ") + mask.OneLine(act.Display())
		if act.Network {
			l += " " + theme.Warn.Render("[network]")
		}
		lines = append(lines, l)
	}
	st := theme.Dim.Render("  no preview yet: " + msgPreviewFirst)
	if err == nil && s.g.allows(act, s.scope) {
		st = theme.OK.Render("  preview ok: press i to import")
	}
	if s.msg != "" {
		st = theme.Warn.Render("  " + mask.OneLine(s.msg))
	}
	lines = append(lines, st, "")
	head := theme.Dim.Render("output")
	if s.out != nil {
		head = theme.Bold.Render(s.out.title)
		if s.out.note != "" {
			head += "  " + theme.Bad.Render(mask.OneLine(s.out.note))
		}
	}
	lines = append(lines, head)
	body := s.outLines()
	start := min(s.scroll, s.maxScroll())
	end := min(start+s.avail(), len(body))
	lines = append(lines, body[start:end]...)
	lines = append(lines, theme.Dim.Render(keysTxt))
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, s.width, "…")
	}
	return tea.NewView(strings.Join(lines, "\n"))
}
