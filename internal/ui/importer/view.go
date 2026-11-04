package importer

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
	styleBad  = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	styleOK   = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleSel  = lipgloss.NewStyle().Reverse(true)
)

const (
	keysTxt = "a/A agent • c/C component • n name • p preview • i import • r reload agents • ctrl+d/ctrl+u scroll"
	// headLines is the number of fixed lines above the output body.
	headLines = 8
)

func oneLine(v string) string {
	return strings.NewReplacer("\n", " ", "\t", " ").Replace(mask.Sanitize(v))
}

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
		ls = append(ls, styleWarn.Render("output truncated"))
	}
	return ls
}

func (s *Screen) maxScroll() int { return max(len(s.outLines())-s.avail(), 0) }

func (s *Screen) agentLine() string {
	switch {
	case !s.loaded:
		return "  agent:     " + styleDim.Render("loading agents…")
	case len(s.agents) == 0 && s.note == "":
		return "  agent:     " + styleWarn.Render("no registered agents (add one with: agentsync agent add <name>)")
	case len(s.agents) == 0:
		return "  agent:     " + styleBad.Render(oneLine(s.note))
	}
	line := "  agent:     " + styleBold.Render(oneLine(s.agent))
	for i, a := range s.agents {
		if a.Name == s.agent {
			line += styleDim.Render("  (" + strconv.Itoa(i+1) + "/" + strconv.Itoa(len(s.agents)) + ")")
			if !a.Enabled {
				line += styleWarn.Render("  disabled")
			}
		}
	}
	if s.note != "" {
		line += "  " + styleBad.Render(oneLine(s.note))
	}
	return line
}

// View implements tea.Model. Every outside string is sanitised or masked.
func (s *Screen) View() tea.View {
	lines := []string{
		styleBold.Render("Import") + styleDim.Render("  adopt an agent's native config into the canonical source"),
		s.agentLine(),
		"  component: " + styleBold.Render(compLabel(s.comp)),
	}
	name := oneLine(s.name)
	switch {
	case s.typing:
		name = styleSel.Render(name + " ")
	case name == "":
		name = styleDim.Render("(every item; press n to name one)")
	}
	lines = append(lines, "  name:      "+name)
	act, err := s.build(false)
	switch {
	case err == errNoAgent:
		lines = append(lines, styleDim.Render("  will run: (nothing: no agent selected)"))
	case err != nil:
		lines = append(lines, styleBad.Render("  "+oneLine(mask.Text(err.Error()))))
	default:
		l := styleBold.Render("  will run: ") + oneLine(act.Display())
		if act.Network {
			l += " " + styleWarn.Render("[network]")
		}
		lines = append(lines, l)
	}
	st := styleDim.Render("  no preview yet: " + msgPreviewFirst)
	if err == nil && s.g.allows(act, s.scope) {
		st = styleOK.Render("  preview ok: press i to import")
	}
	if s.msg != "" {
		st = styleWarn.Render("  " + oneLine(s.msg))
	}
	lines = append(lines, st, "")
	head := styleDim.Render("output")
	if s.out != nil {
		head = styleBold.Render(s.out.title)
		if s.out.note != "" {
			head += "  " + styleBad.Render(oneLine(s.out.note))
		}
	}
	lines = append(lines, head)
	body := s.outLines()
	start := min(s.scroll, s.maxScroll())
	end := min(start+s.avail(), len(body))
	lines = append(lines, body[start:end]...)
	lines = append(lines, styleDim.Render(keysTxt))
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, s.width, "…")
	}
	return tea.NewView(strings.Join(lines, "\n"))
}
