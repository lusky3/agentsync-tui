package ui

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"

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
