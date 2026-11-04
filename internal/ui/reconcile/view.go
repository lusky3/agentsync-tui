package reconcile

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
)

const (
	banner1 = "No in-TUI write-back/override and no bulk W/O/S or auto resolution (upstream agentsync #239, #255, #256)."
	banner2 = "Press x to resolve item by item in agentsync's own interactive reconcile."
	keysTxt = "x resolve interactively • r refresh • ctrl+d/ctrl+u scroll"
	// headLines is the number of fixed lines above the report.
	headLines = 5
)

// skippedPrefix starts each line of the --auto-safe report that needs a human.
const skippedPrefix = "skipped (needs manual review):"

func oneLine(v string) string {
	return strings.NewReplacer("\n", " ", "\t", " ").Replace(mask.Sanitize(v))
}

// avail is how many report lines fit under the header and above the footer.
func (s *Screen) avail() int { return max(s.height-headLines-1, 1) }

func (s *Screen) lines() []string {
	var ls []string
	if s.report != "" {
		for _, l := range strings.Split(strings.TrimRight(s.report, "\n"), "\n") {
			ls = append(ls, strings.ReplaceAll(l, "\t", "  "))
		}
	}
	if s.trunc {
		ls = append(ls, styleWarn.Render("output truncated"))
	}
	return ls
}

func (s *Screen) maxScroll() int { return max(len(s.lines())-s.avail(), 0) }

// summary is the one-line digest of the last good report.
func (s *Screen) summary() string {
	if !s.loaded {
		return styleDim.Render("loading reconcile report…")
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
		out = styleWarn.Render(strconv.Itoa(n) + " item(s) need a human: press x to resolve them interactively")
	case strings.Contains(s.report, "nothing to reconcile"):
		out = styleOK.Render("nothing to reconcile")
	case strings.TrimSpace(s.report) == "":
		out = styleDim.Render("no report")
	default:
		out = styleDim.Render("report loaded")
	}
	if s.exit != 0 {
		out += styleWarn.Render(" (exit " + strconv.Itoa(s.exit) + ")")
	}
	return out
}

// View implements tea.Model. The report is stored masked and sanitised.
func (s *Screen) View() tea.View {
	lines := []string{
		styleBold.Render("Reconcile") + styleDim.Render("  report: agentsync reconcile --auto-safe (resolves nothing)"),
		styleWarn.Render(banner1),
		styleDim.Render(banner2),
		s.summary(),
	}
	status := ""
	switch {
	case s.note != "":
		status = styleDim.Render(oneLine(s.note))
	case s.handed != "":
		status = styleDim.Render(oneLine(s.handed))
	}
	lines = append(lines, status)
	body := s.lines()
	start := min(s.scroll, s.maxScroll())
	end := min(start+s.avail(), len(body))
	lines = append(lines, body[start:end]...)
	lines = append(lines, styleDim.Render(keysTxt))
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, s.width, "…")
	}
	return tea.NewView(strings.Join(lines, "\n"))
}
