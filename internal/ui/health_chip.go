package ui

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/runner"
)

// chipTimeout bounds one round of the header chip's background checks. It is
// a variable only so tests can shorten it.
var chipTimeout = 30 * time.Second

// chipState is what the header chip shows. The zero value is pending.
type chipState int

const (
	chipPending chipState = iota
	chipOK
	chipFailed
	chipDrift
	chipUnavailable
	chipSuperseded // a cancelled round: never stored or shown
)

// text is the whole chip: a fixed string per state. Nothing from command
// output is ever rendered in the header.
func (c chipState) text() string {
	switch c {
	case chipOK:
		return "✓ ok"
	case chipFailed:
		return "✗ check failed"
	case chipDrift:
		return "⚠ drift"
	case chipUnavailable:
		return "? unavailable"
	}
	return "…"
}

func (c chipState) render() string {
	switch c {
	case chipOK:
		return styleOK.Render(c.text())
	case chipFailed:
		return styleError.Render(c.text())
	case chipDrift:
		return styleWarn.Render(c.text())
	}
	return styleDim.Render(c.text())
}

// chipCtxKey marks the context of the chip's background checks, so a test
// double can tell them from the actions a test drives.
type chipCtxKey struct{}

// IsChipCheck is a test seam: it reports whether ctx belongs to the header
// chip's background checks, so a test double in another package (it is
// exported only for internal/ui/library's tests) can leave them out of the
// calls it records. Production code never calls it.
func IsChipCheck(ctx context.Context) bool { return ctx.Value(chipCtxKey{}) != nil }

// chipMsg carries one finished round of chip checks.
type chipMsg struct {
	gen   int
	state chipState
}

// chipCmd starts a new round of checks in the background (latest wins: the
// generation is bumped and the chip goes pending). The checks go straight
// through the Runner — not DoMsg, no modal, no activity entry, and no RunMsg,
// so they can never re-trigger themselves.
func (a *App) chipCmd() tea.Cmd {
	if a.chipCancel != nil {
		a.chipCancel() // the superseded round's children die now, not after 30 s
		a.chipCancel = nil
	}
	a.chipGen++
	gen, r := a.chipGen, a.run
	if r == nil || a.noProject {
		a.chip = chipUnavailable
		return nil
	}
	a.chip = chipPending
	round, cancelRound := context.WithCancel(a.root)
	a.chipCancel = cancelRound
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.WithValue(round, chipCtxKey{}, true), chipTimeout)
		defer cancel()
		return chipMsg{gen: gen, state: chipDerive(ctx, r)}
	}
}

// chipDerive runs `check` then `status --exit-code`. A failing check takes
// precedence, so status is not consulted then.
func chipDerive(ctx context.Context, r runner.Runner) chipState {
	res, err := r.Run(ctx, "check")
	switch {
	case errors.Is(err, context.Canceled):
		return chipSuperseded
	case err != nil:
		return chipUnavailable
	case res.ExitCode != 0:
		return chipFailed
	}
	res, err = r.Run(ctx, "status", "--exit-code")
	switch {
	case errors.Is(err, context.Canceled):
		return chipSuperseded
	case err != nil:
		return chipUnavailable
	case res.ExitCode == 2:
		return chipDrift
	case res.ExitCode == 0:
		return chipOK
	}
	return chipUnavailable
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
		return a.tabStrip(labels, active, 0) + "  " + right
	}
	avail := a.width - ansi.StringWidth(right) - 2
	if avail < minTabCols(len(labels), active) { // drop the scope before the active tab
		right = chip
		avail = a.width - ansi.StringWidth(right) - 2
	}
	return a.tabStrip(labels, active, max(avail, 0)) + "  " + right
}

// minTabCols is the room worth keeping for the tab strip: "n/N " plus a few
// characters of the title.
func minTabCols(n, active int) int {
	return len(strconv.Itoa(active+1)) + 1 + len(strconv.Itoa(n)) + 1 + 4
}

func (a *App) tabStrip(labels []string, active, avail int) string {
	n := len(labels)
	if n == 0 {
		return ""
	}
	active = min(max(active, 0), n-1)
	render := func(i int) string {
		if i == active {
			return styleTabActive.Render(labels[i])
		}
		return styleTab.Render(labels[i])
	}
	w := func(i int) int { return ansi.StringWidth(labels[i]) }
	total := 0
	for i := range labels {
		total += w(i)
	}
	lo, hi := 0, n-1
	if avail > 0 && total > avail {
		cost := func(lo, hi int) int {
			c := 0
			for i := lo; i <= hi; i++ {
				c += w(i)
			}
			if lo > 0 {
				c++
			}
			if hi < n-1 {
				c++
			}
			return c
		}
		if cost(active, active) > avail {
			t := strings.TrimSpace(labels[active])
			num, title, _ := strings.Cut(t, " ")
			short := num + "/" + strconv.Itoa(n) + " " + title
			return styleTabActive.Render(ansi.Truncate(short, avail, "…"))
		}
		lo, hi = active, active
		for grew := true; grew; {
			grew = false
			if hi < n-1 && cost(lo, hi+1) <= avail {
				hi++
				grew = true
			}
			if lo > 0 && cost(lo-1, hi) <= avail {
				lo--
				grew = true
			}
		}
	}
	var b strings.Builder
	if lo > 0 {
		b.WriteString(styleDim.Render("‹"))
	}
	for i := lo; i <= hi; i++ {
		b.WriteString(render(i))
	}
	if hi < n-1 {
		b.WriteString(styleDim.Render("›"))
	}
	return b.String()
}
