package ui

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/mask"
)

// header renders the top line: the tab strip, the health chip and the scope.
func (a *App) header() string {
	labels := make([]string, len(a.screens))
	for i, s := range a.screens {
		labels[i] = " " + strconv.Itoa(i+1) + " " + mask.Sanitize(s.Title()) + " "
	}
	return a.headerLine(labels, a.active)
}

// headerLine renders the tab strip, the chip and the scope on one line.
// Priority when space is short: chip, then scope, then tabs. The strip is a
// window of whole labels that always contains the active tab, with ‹ / › for
// hidden tabs; if the active label alone does not fit it becomes "n/N Title"
// (ellipsised) so the active tab is never invisible.
func (a *App) headerLine(labels []string, active int) string {
	chip := a.chip.render()
	scope := styleDim.Render("scope: " + sanitizeScope(a.scope))
	right := chip + "  " + scope
	if a.width <= 0 {
		return tabStrip(labels, active, 0) + "  " + right
	}
	avail := a.width - ansi.StringWidth(right) - 2
	if avail < minTabCols(len(labels), active) { // drop the scope before the active tab
		right = chip
		avail = a.width - ansi.StringWidth(right) - 2
	}
	return tabStrip(labels, active, max(avail, 0)) + "  " + right
}

// minTabCols is the room worth keeping for the tab strip: "n/N " plus a few
// characters of the title.
func minTabCols(n, active int) int {
	return len(strconv.Itoa(active+1)) + 1 + len(strconv.Itoa(n)) + 1 + 4
}

// tabWindow chooses the labels [lo, hi] to show when the whole strip (widths
// summing to more than avail) does not fit: it starts from the active label
// and grows right, then left, while the window plus its ‹ / › markers fits.
// ok is false when not even the active label fits on its own; avail <= 0
// means unlimited.
func tabWindow(widths []int, active, avail int) (lo, hi int, ok bool) {
	n := len(widths)
	if n == 0 {
		return 0, -1, false
	}
	active = min(max(active, 0), n-1)
	if avail <= 0 || windowCost(widths, 0, n-1) <= avail {
		return 0, n - 1, true
	}
	if windowCost(widths, active, active) > avail {
		return active, active, false
	}
	lo, hi = active, active
	for grew := true; grew; {
		grew = false
		if hi < n-1 && windowCost(widths, lo, hi+1) <= avail {
			hi++
			grew = true
		}
		if lo > 0 && windowCost(widths, lo-1, hi) <= avail {
			lo--
			grew = true
		}
	}
	return lo, hi, true
}

// windowCost is the width of labels [lo, hi] plus one column for each of
// the ‹ / › markers the window needs (hidden labels to its left or right).
// The whole strip, [0, n-1], needs no marker and costs the plain sum.
func windowCost(widths []int, lo, hi int) int {
	c := 0
	for _, w := range widths[lo : hi+1] {
		c += w
	}
	if lo > 0 {
		c++
	}
	if hi < len(widths)-1 {
		c++
	}
	return c
}

// tabStrip renders the labels that tabWindow keeps, or the "n/N Title"
// fallback when the active label alone does not fit.
func tabStrip(labels []string, active, avail int) string {
	n := len(labels)
	if n == 0 {
		return ""
	}
	active = min(max(active, 0), n-1)
	widths := make([]int, n)
	for i, l := range labels {
		widths[i] = ansi.StringWidth(l)
	}
	lo, hi, ok := tabWindow(widths, active, avail)
	if !ok {
		t := strings.TrimSpace(labels[active])
		num, title, _ := strings.Cut(t, " ")
		short := num + "/" + strconv.Itoa(n) + " " + title
		return styleTabActive.Render(ansi.Truncate(short, avail, "…"))
	}
	var b strings.Builder
	if lo > 0 {
		b.WriteString(styleDim.Render("‹"))
	}
	for i := lo; i <= hi; i++ {
		if i == active {
			b.WriteString(styleTabActive.Render(labels[i]))
		} else {
			b.WriteString(styleTab.Render(labels[i]))
		}
	}
	if hi < n-1 {
		b.WriteString(styleDim.Render("›"))
	}
	return b.String()
}
