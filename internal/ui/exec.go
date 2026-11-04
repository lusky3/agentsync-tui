package ui

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/runner"
)

// RunMsg reports a finished action. A non-zero Result.ExitCode is a failed
// command, not an Err; Err is set only for spawn failure or cancellation.
type RunMsg struct {
	Action actions.Action
	Result runner.Result
	Err    error
}

// DoMsg asks the shell to run an action (confirming it first unless it is
// ReadOnly). Screens that do not hold the *App can return it from a Cmd.
type DoMsg struct{ Action actions.Action }

var errNoInteractive = errors.New("interactive handover unavailable")

// running is the action currently in flight.
type running struct {
	act    actions.Action
	start  time.Time
	cancel context.CancelFunc // nil for interactive handovers
}

// Do returns a Cmd that runs act through the shell: non-ReadOnly actions go
// through the confirm modal first; every action is logged and the active
// screen is refreshed afterwards.
func (a *App) Do(act actions.Action) tea.Cmd {
	return func() tea.Msg { return DoMsg{Action: act} }
}

// request handles a DoMsg inside Update.
func (a *App) request(act actions.Action) tea.Cmd {
	switch {
	case a.running != nil || a.modal != nil:
		a.setStatus(statusFail, "busy: another action is in progress")
		return nil
	case a.noProject:
		a.setStatus(statusFail, noProjectText)
		return nil
	case act.ReadOnly:
		return a.start(act)
	}
	a.modal = newModal(act)
	return nil
}

// start launches a confirmed action.
func (a *App) start(act actions.Action) tea.Cmd {
	a.running = &running{act: act, start: time.Now()}
	a.setStatus(statusInfo, "running: "+mask.Sanitize(act.Title)+"…")
	if act.Interactive {
		if a.interactive == nil {
			return func() tea.Msg { return RunMsg{Action: act, Err: errNoInteractive} }
		}
		return tea.ExecProcess(a.interactive(act.Argv...), func(err error) tea.Msg {
			var res runner.Result
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				res.ExitCode, err = ee.ExitCode(), nil
			}
			return RunMsg{Action: act, Result: res, Err: err}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.running.cancel = cancel
	r := a.run
	return func() tea.Msg {
		res, err := r.Run(ctx, act.Argv...)
		return RunMsg{Action: act, Result: res, Err: err}
	}
}

// cancelRunning cancels the in-flight action, if it can be cancelled.
func (a *App) cancelRunning() {
	if a.running != nil && a.running.cancel != nil {
		a.running.cancel()
		a.setStatus(statusWarn, "cancelling…")
	}
}

// finish logs a RunMsg, sets the status line and refreshes the active screen.
func (a *App) finish(m RunMsg) tea.Cmd {
	var dur time.Duration
	if a.running != nil {
		dur = time.Since(a.running.start)
		if a.running.cancel != nil {
			a.running.cancel()
		}
		a.running = nil
	}
	if err := a.log.add(entry{Time: time.Now(), Display: m.Action.Display(), Outcome: outcome(m.Result, m.Err), Duration: dur}); err != nil {
		a.logNote = "activity log file unavailable"
	}
	title := mask.Sanitize(m.Action.Title)
	switch {
	case errors.Is(m.Err, context.Canceled):
		a.setStatus(statusWarn, "cancelled: "+title)
	case m.Err != nil:
		a.setStatus(statusFail, "error: "+title+": "+firstLine(mask.Text(m.Err.Error())))
	case m.Result.ExitCode != 0:
		msg := "failed (exit " + strconv.Itoa(m.Result.ExitCode) + "): "
		if l := firstLine(mask.Text(m.Result.Stderr)); l != "" {
			msg += l
		} else {
			msg += title
		}
		a.setStatus(statusFail, msg)
	default:
		a.setStatus(statusOK, "done: "+title)
	}
	cmds := a.broadcast(m)
	if s := a.current(); s != nil && !a.noProject {
		cmds = append(cmds, s.Refresh())
	}
	return tea.Batch(cmds...)
}

// firstLine returns the first non-blank line of s, trimmed.
func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}
