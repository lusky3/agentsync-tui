package apply

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/ui/theme"
)

const (
	revertHint = "revert only moves the destination; the next apply re-renders from the canonical source"
	applyKeys  = "m mode • e agents • p preview • a apply • ctrl+d/ctrl+u scroll"
	revertKeys = "m mode • e target • A all • t --to • p preview • v revert • ctrl+d/ctrl+u scroll"
	headLines  = 8
)

// avail is how many output lines fit under the fixed header and footer.
func (s *Screen) avail() int { return max(s.height-headLines-1, 1) }

func (s *Screen) outLines() []string {
	if o := s.out[s.mode]; o != nil {
		return o.Lines()
	}
	return nil
}

func (s *Screen) maxScroll() int { return max(len(s.outLines())-s.avail(), 0) }

func (s *Screen) field(label, val string, focused bool) string {
	v := mask.OneLine(val)
	if focused {
		v = theme.Sel.Render(v + " ")
	}
	return "  " + label + " " + v
}

// View implements tea.Model. Every outside string is sanitised or masked.
func (s *Screen) View() tea.View {
	var lines []string
	apply, revert := "Apply", "Revert"
	if s.mode == modeApply {
		apply = theme.Sel.Render(" Apply ")
	} else {
		revert = theme.Sel.Render(" Revert ")
	}
	lines = append(lines, apply+" "+revert)
	if s.mode == modeApply {
		lines = append(lines, theme.Warn.Render("apply writes destination files (agent config on disk)"))
		agents := s.agents
		if agents == "" && s.focus == fNone {
			agents = "(all enabled)"
		}
		lines = append(lines, s.field("agents:", agents, s.focus == fTarget))
		lines = append(lines, "")
	} else {
		lines = append(lines, theme.Warn.Render("revert is destructive: it rewinds destination files to a checkpoint"))
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
		lines = append(lines, theme.Bad.Render("  "+mask.OneLine(mask.Text(err.Error()))))
	} else {
		lines = append(lines, theme.Bold.Render("  will run: ")+mask.OneLine(act.Display()))
	}
	st := theme.Dim.Render("  no preview yet: preview first")
	if s.prev[s.mode] != nil {
		st = theme.OK.Render("  preview ok")
	}
	if s.msg != "" {
		st = theme.Warn.Render("  " + mask.OneLine(s.msg))
	}
	lines = append(lines, st)
	if s.mode == modeRevert {
		lines = append(lines, theme.Dim.Render(revertHint))
	} else {
		lines = append(lines, "")
	}
	head := theme.Dim.Render("output")
	if o := s.out[s.mode]; o != nil {
		head = theme.Bold.Render(o.Title)
		if o.Note != "" {
			head += "  " + theme.Bad.Render(mask.OneLine(o.Note))
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
	lines = append(lines, theme.Dim.Render(keys))
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, s.width, "…")
	}
	return tea.NewView(strings.Join(lines, "\n"))
}
