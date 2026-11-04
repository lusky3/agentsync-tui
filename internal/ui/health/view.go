package health

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/ui/theme"
)

const (
	headLines = 3 // title, summary, status
	keysTxt   = "r refresh • ctrl+d/ctrl+u pgdn/pgup scroll"
)

func (s *Screen) avail() int { return max(s.height-headLines-1, 1) }

func sectionLines(title string, sec section) []string {
	ls := []string{theme.Bold.Render(title)}
	switch {
	case !sec.loaded:
		ls = append(ls, theme.Dim.Render("loading…"))
	case sec.ok:
		if t := strings.TrimRight(sec.text, "\n"); t != "" {
			for _, l := range strings.Split(t, "\n") {
				ls = append(ls, strings.ReplaceAll(l, "\t", "  "))
			}
		}
		if sec.trunc {
			ls = append(ls, theme.Warn.Render("output truncated"))
		}
	}
	return ls
}

func (s *Screen) lines() []string {
	ls := sectionLines("doctor — machine readiness", s.doctor)
	ls = append(ls, "")
	return append(ls, sectionLines("check — config validity", s.check)...)
}

func (s *Screen) maxScroll() int { return max(len(s.lines())-s.avail(), 0) }

func verdict(sec section, text string) string {
	switch {
	case sec.loaded && sec.ok && sec.exit == 0:
		return theme.OK.Render(text)
	case sec.loaded && sec.ok:
		return theme.Warn.Render(text)
	}
	return theme.Dim.Render(text)
}

func oneLine(v string) string { return strings.Join(strings.Fields(v), " ") }

// View implements tea.Model. Both outputs are stored masked and sanitised.
func (s *Screen) View() tea.View {
	status := ""
	for _, n := range []string{s.doctor.note, s.check.note} {
		if n != "" {
			status += oneLine(n) + "  "
		}
	}
	lines := []string{
		theme.Bold.Render("Health") + theme.Dim.Render("  doctor + check (read-only)"),
		verdict(s.doctor, s.doctor.summary("doctor", "problems found")) + "  " +
			verdict(s.check, s.check.summary("check", "failed")),
		theme.Dim.Render(strings.TrimSpace(status)),
	}
	body := s.lines()
	start := min(s.scroll, s.maxScroll())
	end := min(start+s.avail(), len(body))
	lines = append(lines, body[start:end]...)
	lines = append(lines, theme.Dim.Render(keysTxt))
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, s.width, "…")
	}
	return tea.NewView(strings.Join(lines, "\n"))
}
