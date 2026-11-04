package reconcile

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/ui"
	"github.com/lusky3/agentsync-tui/internal/ui/theme"
)

const (
	banner1 = "No in-TUI write-back/override and no bulk W/O/S or auto resolution (upstream agentsync #239, #255, #256)."
	banner2 = "Press x to resolve item by item in agentsync's own interactive reconcile."
	keysTxt = "x resolve interactively • r refresh • " + ui.ScrollKeys
)

// skippedPrefix starts each line of the --auto-safe report that needs a human.
const skippedPrefix = "skipped (needs manual review):"

func (s *Screen) lines() []string { return ui.TextLines(s.report, s.trunc) }

// summary is the one-line digest of the last good report.
func (s *Screen) summary() string {
	if !s.loaded {
		return theme.Dim.Render("loading reconcile report…")
	}
	n := 0
	for _, l := range strings.Split(s.report, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), skippedPrefix) {
			n++
		}
	}
	var out string
	switch {
	case n > 0:
		out = theme.Warn.Render(strconv.Itoa(n) + " item(s) need a human: press x to resolve them interactively")
	case strings.Contains(s.report, "nothing to reconcile"):
		out = theme.OK.Render("nothing to reconcile")
	case strings.TrimSpace(s.report) == "":
		out = theme.Dim.Render("no report")
	default:
		out = theme.Dim.Render("report loaded")
	}
	if s.exit != 0 {
		out += theme.Warn.Render(" (exit " + strconv.Itoa(s.exit) + ")")
	}
	return out
}

// header is every fixed line above the report; the body gets what is left.
func (s *Screen) header() []string {
	status := ""
	switch {
	case s.note != "":
		status = theme.Dim.Render(mask.OneLine(s.note))
	case s.handed != "":
		status = theme.Dim.Render(mask.OneLine(s.handed))
	}
	return []string{
		theme.Bold.Render("Reconcile") + theme.Dim.Render("  report: agentsync reconcile --auto-safe (resolves nothing)"),
		theme.Warn.Render(banner1),
		theme.Dim.Render(banner2),
		s.summary(),
		status,
	}
}

// View implements tea.Model. The report is stored masked and sanitised.
func (s *Screen) View() tea.View {
	lines := s.scroll.Frame(s.header(), s.lines(), ui.KeyLines(keysTxt, s.width), s.height)
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, s.width, "…")
	}
	return tea.NewView(strings.Join(lines, "\n"))
}
