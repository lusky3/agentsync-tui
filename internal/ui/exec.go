package ui

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
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
// Scope is the scope the screen built the action under (its list, its
// ids); the shell refuses a scope-aware action whose Scope is no longer
// the current one, so an id read under one scope can never run under the
// other. "" means unbound (a follow-up queued by the shell itself).
type DoMsg struct {
	Action actions.Action
	Scope  runner.Scope
}

// DefaultReadTimeout is the default bound on network reads (Timeouts.Read).
const DefaultReadTimeout = 120 * time.Second

// LocalReadBound caps every local read (the lists, status, diff, explain,
// doctor and check a screen or the header chip runs): min(LocalReadBound,
// Timeouts.Read), see Timeouts.Local. A local agentsync call that takes
// longer is stuck, and a stuck read must not hold a screen for the network
// bound.
const LocalReadBound = 30 * time.Second

// Timeouts bound the commands the shell runs; zero means no bound. Past a
// bound the run is cancelled like ctrl+c (SIGINT, then SIGKILL after
// runner.InterruptGrace) and reported, and logged, as a timeout.
type Timeouts struct {
	// Read bounds network reads: ReadOnly actions with Network set (import
	// dry runs of plugins or a whole agent) and the plugins screen's
	// `plugin outdated`. Local reads use Local, which it can lower.
	Read time.Duration
	// Write bounds every non-ReadOnly action. The default is none: a write
	// such as `plugin upgrade --all` cut off partway can leave agentsync's
	// state and the destination files out of step.
	Write time.Duration
}

// DefaultTimeouts bounds network reads only.
var DefaultTimeouts = Timeouts{Read: DefaultReadTimeout}

// Local is the bound on a local read: Read when it is set and below
// LocalReadBound, else LocalReadBound. --read-timeout 0 lifts the network
// bound only.
func (t Timeouts) Local() time.Duration {
	if t.Read > 0 && t.Read < LocalReadBound {
		return t.Read
	}
	return LocalReadBound
}

// Limits returns d.Timeouts, or DefaultTimeouts when it is nil.
func (d Deps) Limits() Timeouts {
	if d.Timeouts == nil {
		return DefaultTimeouts
	}
	return *d.Timeouts
}

// timeoutError is the RunMsg.Err of a network action that hit its bound.
type timeoutError struct{ d time.Duration }

func (e timeoutError) Error() string {
	if e.d < time.Second {
		return "timed out after " + e.d.String()
	}
	return "timed out after " + strconv.Itoa(int(e.d/time.Second)) + "s"
}

// Unwrap lets outcome() log it as "timeout".
func (timeoutError) Unwrap() error { return context.DeadlineExceeded }

var errNoInteractive = errors.New("interactive handover unavailable")

// running is the action currently in flight.
type running struct {
	act    actions.Action
	start  time.Time
	cancel context.CancelFunc // nil for interactive handovers
	// armed is when ctrl+c (or q) first asked to stop this write; a second
	// ctrl+c within interruptConfirm interrupts it. Unused for reads.
	armed       time.Time
	interrupted bool // the write was sent SIGINT
}

// interruptConfirm is how long a first ctrl+c on a running write stays
// armed: a second ctrl+c inside it interrupts the write.
const interruptConfirm = 3 * time.Second

// Do returns the Cmd a screen uses to hand act to the shell: non-ReadOnly
// actions go through the confirm modal first; every action is logged and
// the active screen is refreshed afterwards.
func Do(act actions.Action, scope runner.Scope) tea.Cmd {
	return func() tea.Msg { return DoMsg{Action: act, Scope: scope} }
}

// DoOr is Do for a builder result: a builder error is written to *note as
// "error: <first line, masked>" and nothing is handed over; otherwise the
// note is cleared.
func DoOr(act actions.Action, err error, note *string, scope runner.Scope) tea.Cmd {
	if err != nil {
		*note = "error: " + mask.FirstLine(err.Error())
		return nil
	}
	*note = ""
	return Do(act, scope)
}

// request handles a DoMsg inside Update.
func (a *App) request(act actions.Action, scope runner.Scope) tea.Cmd {
	switch {
	case a.running != nil || a.modal != nil:
		a.setStatus(statusFail, "busy: another action is in progress")
		return nil
	case a.noProject:
		a.setStatus(statusFail, noProjectText)
		return nil
	case scope != "" && scope != a.scope && !runner.ScopeUnaware(act.Argv):
		// Built under the other scope (an s between the key and its Cmd):
		// its ids came from that scope's list, so it must not run here.
		a.setStatus(statusFail, "not run: "+mask.Sanitize(act.Title)+": scope changed since it was built ("+sanitizeScope(scope)+" → "+sanitizeScope(a.scope)+")")
		return nil
	case act.ReadOnly:
		return a.start(act)
	}
	a.modal = newModal(act, a.scope, a.d.ProjectDir)
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
			res, err := exitResult(err)
			return RunMsg{Action: act, Result: res, Err: err}
		})
	}
	lim, limit := a.d.Limits(), time.Duration(0)
	switch {
	case !act.ReadOnly:
		limit = lim.Write
	case act.Network:
		limit = lim.Read
	}
	// Actions derive from context.Background, not Deps.Ctx: quitting must
	// not cut a write off (quitAfterWrite waits for it); the running
	// action is cancelled explicitly (cancelRunning, interrupt) instead.
	var ctx context.Context
	var cancel context.CancelFunc
	if limit > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), limit)
	} else {
		ctx, cancel = context.WithCancel(context.Background())
	}
	a.running.cancel = cancel
	r := a.run
	return func() tea.Msg {
		res, err := r.Run(ctx, act.Argv...)
		if err != nil && limit > 0 && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = timeoutError{limit}
		}
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

// msgWriteRunning asks for a second ctrl+c before a write is interrupted.
const msgWriteRunning = "a write is running: press ctrl+c again to interrupt it, or wait"

// interrupt handles ctrl+c while an action runs. A read is cancelled at
// once. A write is interrupted (SIGINT, then SIGKILL after
// runner.InterruptGrace) only by a second ctrl+c within interruptConfirm,
// or by ctrl+c after q: stopping it halfway can leave agentsync's state
// and the destination files out of step.
func (a *App) interrupt() tea.Cmd {
	r := a.running
	switch {
	case r == nil || r.cancel == nil || r.interrupted:
		return nil
	case r.act.ReadOnly:
		a.cancelRunning()
		return nil
	case !a.quitting && (r.armed.IsZero() || time.Since(r.armed) > interruptConfirm):
		r.armed = time.Now()
		a.setStatus(statusWarn, msgWriteRunning)
		return nil
	}
	r.interrupted = true
	r.cancel()
	if a.quitting {
		a.setStatus(statusWarn, "interrupting before quit: "+mask.Sanitize(r.act.Title)+"…")
		return a.quitTick()
	}
	a.setStatus(statusWarn, "interrupting: "+mask.Sanitize(r.act.Title)+"…")
	return nil
}

// exitResult turns the error of a finished process into a Result: a
// non-zero exit is a Result.ExitCode, not an error.
func exitResult(err error) (runner.Result, error) {
	var res runner.Result
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return runner.Result{ExitCode: ee.ExitCode()}, nil
	}
	return res, err
}

// finish logs a RunMsg, sets the status line and refreshes the active
// screen; after a write the other screens are marked stale.
func (a *App) finish(m RunMsg) tea.Cmd {
	var dur time.Duration
	if a.running != nil {
		dur = time.Since(a.running.start)
		if a.running.cancel != nil {
			a.running.cancel()
		}
		a.running = nil
	}
	oc := outcome(m.Result, m.Err)
	if oc == "cancelled" && !m.Action.ReadOnly {
		oc = "interrupted" // a write was sent SIGINT (interrupt), not just dropped
	}
	a.record(m.Action, oc, dur)
	if a.quitting {
		// q was pressed while this run was in flight: it is now cancelled
		// and logged, so exit.
		return tea.Quit
	}
	title := mask.Sanitize(m.Action.Title)
	switch {
	case errors.Is(m.Err, context.Canceled) && !m.Action.ReadOnly:
		a.setStatus(statusWarn, "interrupted: "+title)
	case errors.Is(m.Err, context.Canceled):
		a.setStatus(statusWarn, "cancelled: "+title)
	case errors.As(m.Err, new(timeoutError)):
		a.setStatus(statusFail, "failed: "+title+": "+m.Err.Error())
	case m.Err != nil:
		a.setStatus(statusFail, "error: "+title+": "+mask.FirstLine(m.Err.Error()))
	case m.Result.ExitCode != 0:
		msg := "failed (exit " + strconv.Itoa(m.Result.ExitCode) + "): "
		if l := mask.FirstLine(m.Result.Stderr); l != "" {
			msg += l
		} else {
			msg += title
		}
		a.setStatus(statusFail, msg)
	default:
		a.setStatus(statusOK, "done: "+title)
	}
	cmds := a.broadcast(m)
	if !m.Action.ReadOnly {
		cmds = append(cmds, a.chipCmd()) // apply/revert/import may change health
		a.markOthersStale()
	}
	cmds = append(cmds, a.next(), a.refreshActive())
	return tea.Batch(cmds...)
}

// record appends one activity-log entry.
func (a *App) record(act actions.Action, outcome string, dur time.Duration) {
	a.recordDisplay(act.Display(), outcome, dur)
}

// recordDisplay appends an entry whose display text is already masked.
func (a *App) recordDisplay(display, outcome string, dur time.Duration) {
	if err := a.log.add(entry{Time: time.Now(), Display: display, Outcome: outcome, Duration: dur}); err != nil {
		a.logNote = "activity log file unavailable"
	}
}

// quitGrace bounds how long quitting waits for a cancelled or interrupted
// run to report back. runner.Exec kills the child InterruptGrace after
// SIGINT, so this is a fallback for a runner that ignores its context.
const quitGrace = runner.InterruptGrace + 3*time.Second

// quitDeadlineMsg fires when a cancelled run has not reported back within
// quitGrace after a quit request.
type quitDeadlineMsg struct{}

// quitWhenCancelled handles q while a read is running: the read is
// cancelled and the program exits once its RunMsg has been logged (see
// finish), or after quitGrace, logging it as cancelled, if it never reports.
func (a *App) quitWhenCancelled() tea.Cmd {
	a.quitting = true
	a.shutdown()
	a.cancelRunning()
	a.setStatus(statusWarn, "cancelling before quit…")
	return a.quitTick()
}

// quitAfterWrite handles q while a write is running. The write is not
// stopped: the program exits once it has finished and been logged (see
// finish). A ctrl+c meanwhile interrupts it (interrupt), and then the exit
// waits at most quitGrace. If the write was already interrupted, the exit
// is bounded by quitGrace from now.
func (a *App) quitAfterWrite() tea.Cmd {
	a.quitting = true
	a.shutdown()
	if a.running.interrupted {
		a.setStatus(statusWarn, "an interrupted write is still stopping: quitting when it has")
		return a.quitTick()
	}
	a.running.armed = time.Now()
	a.setStatus(statusWarn, "a write is running: quitting when it finishes; press ctrl+c to interrupt it, or wait")
	return nil
}

// quitTick schedules the quitGrace fallback.
func (a *App) quitTick() tea.Cmd {
	return tea.Tick(quitGrace, func(time.Time) tea.Msg { return quitDeadlineMsg{} })
}

// quitDeadline is the quitGrace fallback.
func (a *App) quitDeadline() tea.Cmd {
	if r := a.running; r != nil {
		oc := "cancelled"
		if r.interrupted {
			oc = "interrupted"
		}
		a.record(r.act, oc, time.Since(r.start))
		a.running = nil
	}
	return tea.Quit
}

// Filter is the tea.WithFilter hook main installs. Bubble Tea turns SIGTERM
// into a tea.QuitMsg and SIGINT off a TTY into a tea.InterruptMsg, and ends
// the program on either without the model seeing it; main also sends a
// QuitMsg on SIGHUP. A run still in flight is logged here as "abandoned"
// (a write keeps running on its own, which is the safe choice for a write;
// a read or editor is simply left) and the background reads are cancelled,
// so the next start has a record of it. The shell's own quits (q, ctrl+c
// when idle, the quit after a logged run) have already logged and cleared
// the run, so they pass through unchanged.
func (a *App) Filter(_ tea.Model, msg tea.Msg) tea.Msg {
	switch msg.(type) {
	case tea.QuitMsg, tea.InterruptMsg:
		if r := a.running; r != nil {
			a.record(r.act, "abandoned", time.Since(r.start))
			a.running = nil
		}
		a.shutdown()
	}
	return msg
}
