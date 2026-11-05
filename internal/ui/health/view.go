package health

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/ui"
	"github.com/lusky3/agentsync-tui/internal/ui/theme"
)

const keysTxt = "r refresh • " + ui.ScrollKeys

func sectionLines(title string, sec section) []string {
	ls := []string{theme.Bold.Render(title)}
	switch {
	case !sec.loaded:
		ls = append(ls, theme.Dim.Render("loading…"))
	case sec.ok:
		ls = append(ls, ui.TextLines(sec.text, sec.trunc)...)
	}
	return ls
}

func (s *Screen) lines() []string {
	ls := sectionLines("doctor — machine readiness", s.doctor)
	ls = append(ls, "")
	return append(ls, sectionLines("check — config validity", s.check)...)
}

// maxScroll is the largest offset that still fills the body.
func (s *Screen) maxScroll() int { return s.scroll.MaxOffset(len(s.lines())) }

func verdict(sec section, text string) string {
	switch {
	case sec.loaded && sec.ok && sec.exit == 0:
		return theme.OK.Render(text)
	case sec.loaded && sec.ok:
		return theme.Warn.Render(text)
	}
	return theme.Dim.Render(text)
}

// header is every fixed line above the output: title, verdicts, status.
func (s *Screen) header() []string {
	status := ""
	for _, n := range []string{s.doctor.note, s.check.note} {
		if n != "" {
			status += mask.OneLine(n) + "  "
		}
	}
	return []string{
		theme.Bold.Render("Health") + theme.Dim.Render("  doctor + check (read-only)"),
		verdict(s.doctor, s.doctor.summary("doctor", "problems found")) + "  " +
			verdict(s.check, s.check.summary("check", "failed")),
		theme.Dim.Render(strings.TrimSpace(status)),
	}
}

// View implements tea.Model. Both outputs are stored masked and sanitised.
func (s *Screen) View() tea.View {
	lines := s.scroll.Frame(s.header(), s.lines(), ui.KeyLines(keysTxt, s.width), s.height)
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, s.width, "…")
	}
	return tea.NewView(strings.Join(lines, "\n"))
}
