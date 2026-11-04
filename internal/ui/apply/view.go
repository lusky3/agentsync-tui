package apply

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/ui"
	"github.com/lusky3/agentsync-tui/internal/ui/theme"
)

const (
	revertHint = "revert only moves the destination; the next apply re-renders from the canonical source"
	applyKeys  = "m mode • e agents • p preview • a apply • " + ui.ScrollKeys
	revertKeys = "m mode • e target • A all • t --to • p preview • v revert • " + ui.ScrollKeys
)

func (s *Screen) outLines() []string {
	if o := s.out[s.mode]; o != nil {
		return o.Lines()
	}
	return nil
}

// maxScroll is the largest offset that still fills the body.
func (s *Screen) maxScroll() int { return s.scroll[s.mode].MaxOffset(len(s.outLines())) }

func (s *Screen) fieldLine(label, val string, focused bool) string {
	v := mask.OneLine(val)
	if focused {
		v = theme.Sel.Render(v + " ")
	}
	return "  " + label + " " + v
}

// header is every fixed line above the output: the mode tabs, the form,
// the command, the status and the output title. The body gets the rest.
func (s *Screen) header() []string {
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
		agents := s.agents.Value
		if agents == "" && s.focus == focusNone {
			agents = "(all enabled)"
		}
		lines = append(lines, s.fieldLine("agents:", agents, s.focus == focusTarget))
		lines = append(lines, "")
	} else {
		lines = append(lines, theme.Warn.Render("revert is destructive: it rewinds destination files to a checkpoint"))
		target := s.target.Value
		if s.all {
			target = "all (--all)"
		} else if target == "" && s.focus != focusTarget {
			target = "(none: press e, or A for all)"
		}
		lines = append(lines, s.fieldLine("target:", target, s.focus == focusTarget))
		to := s.to.Value
		if to == "" && s.focus != focusTo {
			to = "(previous checkpoint)"
		}
		lines = append(lines, s.fieldLine("--to:  ", to, s.focus == focusTo))
	}
	p, err := s.pair()
	if err != nil {
		lines = append(lines, theme.Bad.Render("  "+mask.OneLine(mask.Text(err.Error()))))
	} else {
		lines = append(lines, theme.Bold.Render("  will run: ")+mask.OneLine(p.Real.Display()))
	}
	st := theme.Dim.Render("  no preview yet: preview first")
	if err == nil && s.gates[s.mode].Allows(p.Real, s.scope) {
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
	return append(lines, head)
}

// View implements tea.Model. Every outside string is sanitised or masked.
func (s *Screen) View() tea.View {
	keys := applyKeys
	if s.mode == modeRevert {
		keys = revertKeys
	}
	lines := s.scroll[s.mode].Frame(s.header(), s.outLines(), []string{theme.Dim.Render(keys)}, s.height)
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, s.width, "…")
	}
	return tea.NewView(strings.Join(lines, "\n"))
}
