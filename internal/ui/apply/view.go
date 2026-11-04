package apply

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
	revertHint = "revert only moves the destination; the next apply re-renders from the canonical source"
	applyKeys  = "m mode • e agents • p preview • a apply • ctrl+d/ctrl+u scroll"
	revertKeys = "m mode • e target • A all • t --to • p preview • v revert • ctrl+d/ctrl+u scroll"
	headLines  = 8
)

func itoa(n int) string { return strconv.Itoa(n) }

// avail is how many output lines fit under the fixed header and footer.
func (s *Screen) avail() int { return max(s.height-headLines-1, 1) }

func (s *Screen) outLines() []string {
	o := s.out[s.mode]
	if o == nil {
		return nil
	}
	var ls []string
	if o.text != "" {
		for _, l := range strings.Split(strings.TrimRight(o.text, "\n"), "\n") {
			ls = append(ls, strings.ReplaceAll(l, "\t", "  "))
		}
	}
	if o.trunc {
		ls = append(ls, styleWarn.Render("output truncated"))
	}
	return ls
}

func (s *Screen) maxScroll() int { return max(len(s.outLines())-s.avail(), 0) }

func (s *Screen) field(label, val string, focused bool) string {
	v := mask.OneLine(val)
	if focused {
		v = styleSel.Render(v + " ")
	}
	return "  " + label + " " + v
}

// View implements tea.Model. Every outside string is sanitised or masked.
func (s *Screen) View() tea.View {
	var lines []string
	apply, revert := "Apply", "Revert"
	if s.mode == modeApply {
		apply = styleSel.Render(" Apply ")
	} else {
		revert = styleSel.Render(" Revert ")
	}
	lines = append(lines, apply+" "+revert)
	if s.mode == modeApply {
		lines = append(lines, styleWarn.Render("apply writes destination files (agent config on disk)"))
		agents := s.agents
		if agents == "" && s.focus == fNone {
			agents = "(all enabled)"
		}
		lines = append(lines, s.field("agents:", agents, s.focus == fTarget))
		lines = append(lines, "")
	} else {
		lines = append(lines, styleWarn.Render("revert is destructive: it rewinds destination files to a checkpoint"))
		target := s.target
		if s.all {
			target = "all (--all)"
		} else if target == "" && s.focus != fTarget {
			target = "(none: press e, or A for all)"
		}
		lines = append(lines, s.field("target:", target, s.focus == fTarget))
		to := s.to
		if to == "" && s.focus != fTo {
			to = "(previous checkpoint)"
		}
		lines = append(lines, s.field("--to:  ", to, s.focus == fTo))
	}
	if act, err := s.build(false); err != nil {
		lines = append(lines, styleBad.Render("  "+mask.OneLine(mask.Text(err.Error()))))
	} else {
		lines = append(lines, styleBold.Render("  will run: ")+mask.OneLine(act.Display()))
	}
	st := styleDim.Render("  no preview yet: preview first")
	if s.prev[s.mode] != nil {
		st = styleOK.Render("  preview ok")
	}
	if s.msg != "" {
		st = styleWarn.Render("  " + mask.OneLine(s.msg))
	}
	lines = append(lines, st)
	if s.mode == modeRevert {
		lines = append(lines, styleDim.Render(revertHint))
	} else {
		lines = append(lines, "")
	}
	head := styleDim.Render("output")
	if o := s.out[s.mode]; o != nil {
		head = styleBold.Render(o.title)
		if o.note != "" {
			head += "  " + styleBad.Render(mask.OneLine(o.note))
		}
	}
	lines = append(lines, head)
	body := s.outLines()
	start := min(s.scroll[s.mode], s.maxScroll())
	end := min(start+s.avail(), len(body))
	lines = append(lines[:min(len(lines), headLines)], body[start:end]...)
	keys := applyKeys
	if s.mode == modeRevert {
		keys = revertKeys
	}
	lines = append(lines, styleDim.Render(keys))
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, s.width, "…")
	}
	return tea.NewView(strings.Join(lines, "\n"))
}
