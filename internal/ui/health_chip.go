package ui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/runner"
)

// ChipTimeout bounds one round of the header chip's background checks.
const ChipTimeout = 30 * time.Second

// chipTimeout is the live value; tests shorten it.
var chipTimeout = ChipTimeout

// chipState is what the header chip shows. The zero value is pending.
type chipState int

const (
	chipPending chipState = iota
	chipOK
	chipFailed
	chipDrift
	chipUnavailable
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

// IsChipCheck reports whether ctx belongs to the header chip's background
// checks, so a test double can leave them out of the calls it records.
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
	a.chipGen++
	gen, r := a.chipGen, a.run
	if r == nil || a.noProject {
		a.chip = chipUnavailable
		return nil
	}
	a.chip = chipPending
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.WithValue(context.Background(), chipCtxKey{}, true), chipTimeout)
		defer cancel()
		return chipMsg{gen: gen, state: chipDerive(ctx, r)}
	}
}

// chipDerive runs `check` then `status --exit-code`. A failing check takes
// precedence, so status is not consulted then.
func chipDerive(ctx context.Context, r runner.Runner) chipState {
	res, err := r.Run(ctx, "check")
	switch {
	case err != nil:
		return chipUnavailable
	case res.ExitCode != 0:
		return chipFailed
	}
	res, err = r.Run(ctx, "status", "--exit-code")
	switch {
	case err != nil:
		return chipUnavailable
	case res.ExitCode == 2:
		return chipDrift
	case res.ExitCode == 0:
		return chipOK
	}
	return chipUnavailable
}

// headerLine renders the tabs, the chip and the scope on one line. When it
// does not fit, the tabs give way first, then the scope, and the chip last.
func (a *App) headerLine(tabs string) string {
	right := a.chip.render() + "  " + styleDim.Render("scope: "+sanitizeScope(a.scope))
	if a.width > 0 {
		avail := max(a.width-ansi.StringWidth(right)-2, 0)
		if ansi.StringWidth(tabs) > avail {
			tabs = ansi.Truncate(tabs, avail, "")
		}
	}
	return tabs + "  " + right
}
