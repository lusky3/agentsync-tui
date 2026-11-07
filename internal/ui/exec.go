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
// Scope is the scope the screen built the action under (its list, its
// ids); the shell refuses a scope-aware action whose Scope is no longer
// the current one, so an id read under one scope can never run under the
// other. "" means unbound (a follow-up queued by the shell itself).
//
// Then is a chain (R4): the actions that run after Action, in order, under
// the same single confirmation. Each step starts only when the one before
// it exited 0; a failure drops the rest and the status says what did not
// run. The scope check covers every step.
type DoMsg struct {
	Action actions.Action
	Then   []actions.Action
	Scope  runner.Scope
}

// DefaultReadTimeout is the default bound on network reads (Timeouts.Read).
const DefaultReadTimeout = 120 * time.Second

// GitLocalWriteBound bounds a local git write (add, commit) when
// Timeouts.Write is not set (R3): git's index and ref updates are atomic,
// so stopping one is safe, and two minutes covers a slow commit hook.
const GitLocalWriteBound = 2 * time.Minute

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
	// `plugin outdated`. Local reads, the screens' and the ReadOnly
	// actions without Network (apply/revert/import previews, check), use
	// Local, which it can lower.
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

// errNoGit is the RunMsg.Err of a git Action when Deps.Git is nil; it reads
// as runner.Program's own MissingError does.
var errNoGit error = &runner.MissingError{Name: actions.ProgramGit}

// gitLimit is the bound on a git Action (R3); git actions are always
// bounded. Network actions (fetch, pull, push) get Timeouts.Read, or
// DefaultReadTimeout when that bound is lifted; local writes (add, commit)
// get Timeouts.Write, or GitLocalWriteBound when it is unset; local reads
// get Timeouts.Local.
func gitLimit(act actions.Action, t Timeouts) time.Duration {
	switch {
	case act.Network:
		if t.Read > 0 {
			return t.Read
		}
		return DefaultReadTimeout
	case !act.ReadOnly:
		if t.Write > 0 {
			return t.Write
		}
		return GitLocalWriteBound
	}
	return t.Local()
}

// limitFor is the bound on act: gitLimit for git; for agentsync, Write for
// a write (none by default), Read for a network read and Local for a
// local one (a dry-run preview or check).
func limitFor(act actions.Action, t Timeouts) time.Duration {
	switch {
	case act.Program == actions.ProgramGit:
		return gitLimit(act, t)
	case !act.ReadOnly:
		return t.Write
	case act.Network:
		return t.Read
	}
	return t.Local()
}

// elapsedTick refreshes the status line's elapsed time once a second while
// a command runs (statusLine); it stops with the run.
type elapsedTick struct{}

func elapsedTickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return elapsedTick{} })
}

// running is the action currently in flight.
type running struct {
	act actions.Action
	// then is the rest of a confirmed chain (DoMsg.Then): finish starts
	// then[0] only when act exited 0.
	then   []actions.Action
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

// DoChain is Do for a chain (R4): first and then run in order under one
// confirmation, each only after the one before it succeeded.
func DoChain(scope runner.Scope, first actions.Action, then ...actions.Action) tea.Cmd {
	rest := append([]actions.Action(nil), then...)
	return func() tea.Msg { return DoMsg{Action: first, Then: rest, Scope: scope} }
}

// titles is the sanitised titles of acts, comma-separated.
func titles(acts []actions.Action) string {
	return joinTitles(acts, ", ")
}

// joinTitles joins the sanitised titles of acts with sep.
func joinTitles(acts []actions.Action, sep string) string {
	ts := make([]string, len(acts))
	for i, a := range acts {
		ts[i] = mask.Sanitize(a.Title)
	}
	return strings.Join(ts, sep)
}

// scopeBound reports whether act depends on the scope it was built under:
// every git action (its repository is the scope's home) and every
// scope-aware agentsync command.
func scopeBound(act actions.Action) bool {
	return act.Program != "" || !runner.ScopeUnaware(act.Argv)
}

// request handles a single-action DoMsg inside Update.
func (a *App) request(act actions.Action, scope runner.Scope) tea.Cmd {
	return a.requestChain(act, nil, scope)
}

// requestChain handles a DoMsg inside Update: act, then each of then once
// the one before it succeeded, all under one confirmation (none when every
// step is ReadOnly). The scope check covers every step.
func (a *App) requestChain(act actions.Action, then []actions.Action, scope runner.Scope) tea.Cmd {
	chain := append([]actions.Action{act}, then...)
	bound, readOnly := false, true
	for _, c := range chain {
		bound = bound || scopeBound(c)
		readOnly = readOnly && c.ReadOnly
	}
	switch {
	case a.running != nil || a.modal != nil:
		a.setStatus(statusFail, "busy: another action is in progress")
		return nil
	case a.noProject:
		a.setStatus(statusFail, noProjectText)
		return nil
	case scope != "" && scope != a.scope && bound:
		// Built under the other scope (an s between the key and its Cmd):
		// its ids came from that scope's list, so it must not run here.
		a.setStatus(statusFail, "not run: "+titles(chain)+": scope changed since it was built ("+sanitizeScope(scope)+" → "+sanitizeScope(a.scope)+")")
		return nil
	case readOnly:
		return a.start(act, then)
	}
	a.modal = newModal(act, a.scope, a.d.ProjectDir, then...)
	return nil
}

// start launches a confirmed action; then is the rest of its chain.
func (a *App) start(act actions.Action, then []actions.Action) tea.Cmd {
	a.running = &running{act: act, then: then, start: time.Now()}
	a.setStatus(statusInfo, "running: "+mask.Sanitize(act.Title)+"…")
	r := a.run
	switch act.Program {
	case "":
	case actions.ProgramGit:
		if a.d.Git == nil {
			return func() tea.Msg { return RunMsg{Action: act, Err: errNoGit} }
		}
		r = a.d.Git
	default:
		err := errors.New("unsupported program " + mask.Sanitize(act.Program))
		return func() tea.Msg { return RunMsg{Action: act, Err: err} }
	}
	if act.Interactive && act.Program == "" { // git is never handed the terminal (R1)
		if a.interactive == nil {
			return func() tea.Msg { return RunMsg{Action: act, Err: errNoInteractive} }
		}
		return tea.ExecProcess(a.interactive(act.Argv...), func(err error) tea.Msg {
			res, err := exitResult(err)
			return RunMsg{Action: act, Result: res, Err: err}
		})
	}
	limit := limitFor(act, a.d.Limits())
	// Actions derive from context.Background, not Deps.Ctx: quitting must
	// not cut a write off (quitAfterWrite waits for it); the running
	// action is cancelled explicitly (cancelRunning, interrupt) instead.
	// A write is marked for the runner, whose shutdown gives it the full
	// grace where a read gets a short wait.
	base := context.Background()
	if !act.ReadOnly {
		base = runner.MarkWrite(base)
	}
	var ctx context.Context
	var cancel context.CancelFunc
	if limit > 0 {
		ctx, cancel = context.WithTimeout(base, limit)
	} else {
		ctx, cancel = context.WithCancel(base)
	}
	a.running.cancel = cancel
	return tea.Batch(func() tea.Msg {
		res, err := r.Run(ctx, act.Argv...)
		if err != nil && limit > 0 && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = timeoutError{limit}
		}
		return RunMsg{Action: act, Result: res, Err: err}
	}, elapsedTickCmd())
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
// screen; after a write the other screens are marked stale. When the run
// was a successful step of a confirmed chain, the next step starts instead.
func (a *App) finish(m RunMsg) tea.Cmd {
	dur, then := a.endRun()
	a.record(m.Action, runOutcome(m), dur)
	if a.quitting {
		// q was pressed while this run was in flight: it is now cancelled
		// and logged, so exit. The rest of a confirmed chain is not run;
		// the log says so (R4: a staged-but-uncommitted index is explained).
		a.recordNotRun(then)
		return tea.Quit
	}
	ok := m.succeeded()
	a.setStatus(runStatus(m))
	if len(then) > 0 && !ok {
		a.status += "; not run: " + titles(then)
	}
	cmds := a.broadcast(m)
	if !m.Action.ReadOnly {
		a.markOthersStale()
	}
	if len(then) > 0 && ok {
		// The chain was confirmed as a whole: its next step runs now,
		// without a modal. Health and the active screen are refreshed once
		// the chain has ended.
		return tea.Batch(append(cmds, a.start(then[0], then[1:]))...)
	}
	return tea.Batch(append(cmds, a.afterRun(m.Action)...)...)
}

// endRun clears the in-flight run, releasing its context, and returns how
// long it ran and the rest of its chain (zero and nil when none was
// running).
func (a *App) endRun() (time.Duration, []actions.Action) {
	r := a.running
	if r == nil {
		return 0, nil
	}
	if r.cancel != nil {
		r.cancel()
	}
	a.running = nil
	return time.Since(r.start), r.then
}

// succeeded reports whether the run exited 0 without an error.
func (m RunMsg) succeeded() bool {
	return m.Err == nil && m.Result.ExitCode == 0
}

// runOutcome is the activity-log outcome of a finished run. A cancelled
// write was sent SIGINT (interrupted), not just dropped.
func runOutcome(m RunMsg) string {
	oc := outcome(m.Result, m.Err)
	if oc == "cancelled" && !m.Action.ReadOnly {
		return "interrupted"
	}
	return oc
}

// runStatus is the status line for a finished run: the masked title, or
// for a non-zero exit the first stderr line when there is one.
func runStatus(m RunMsg) (statusKind, string) {
	title := mask.Sanitize(m.Action.Title)
	switch {
	case errors.Is(m.Err, context.Canceled) && !m.Action.ReadOnly:
		return statusWarn, "interrupted: " + title
	case errors.Is(m.Err, context.Canceled):
		return statusWarn, "cancelled: " + title
	case errors.As(m.Err, new(timeoutError)):
		return statusFail, "failed: " + title + ": " + m.Err.Error()
	case m.Err != nil:
		return statusFail, "error: " + title + ": " + mask.FirstLine(m.Err.Error())
	case m.Result.ExitCode != 0:
		detail := mask.FirstLine(m.Result.Stderr)
		if detail == "" {
			detail = title
		}
		return statusFail, "failed (exit " + strconv.Itoa(m.Result.ExitCode) + "): " + detail
	}
	return statusOK, "done: " + title
}

// afterRun is the follow-up of a run that ended without starting a chain
// step, in order: the health chip after a write (apply/revert/import may
// change health), the next queued action and the active screen's refresh.
func (a *App) afterRun(act actions.Action) []tea.Cmd {
	var cmds []tea.Cmd
	if !act.ReadOnly {
		cmds = append(cmds, a.chipCmd())
	}
	return append(cmds, a.next(), a.refreshActive())
}

// recordNotRun logs each chain step dropped by a quit as not run.
func (a *App) recordNotRun(then []actions.Action) {
	for _, s := range then {
		a.record(s, "not run", 0)
	}
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
		a.recordNotRun(r.then)
		a.running = nil
	}
	return tea.Quit
}

// Filter is the tea.WithFilter hook main installs. Bubble Tea turns SIGTERM
// into a tea.QuitMsg and SIGINT off a TTY into a tea.InterruptMsg, and ends
// the program on either without the model seeing it; main also sends a
// QuitMsg on SIGHUP. A run still in flight is logged here as "abandoned"
// (a write keeps running on its own, which is the safe choice for a write;
// a read or editor is simply left), the rest of its confirmed chain as "not
// run", and the background reads are cancelled,
// so the next start has a record of it. The shell's own quits (q, ctrl+c
// when idle, the quit after a logged run) have already logged and cleared
// the run, so they pass through unchanged.
func (a *App) Filter(_ tea.Model, msg tea.Msg) tea.Msg {
	switch msg.(type) {
	case tea.QuitMsg, tea.InterruptMsg:
		if r := a.running; r != nil {
			a.record(r.act, "abandoned", time.Since(r.start))
			a.recordNotRun(r.then)
			a.running = nil
		}
		a.shutdown()
	}
	return msg
}

// RecordShutdown logs what became of each child the runner ended when the
// program went away (runner.Shutdown): the masked command line, the
// outcome (exited, killed, orphaned) and how long it took. main calls it
// after tea.Program.Run has returned, so the entries reach the file only.
func (a *App) RecordShutdown(ended []runner.Ended) {
	for _, e := range ended {
		a.recordDisplay(actions.Action{Program: e.Program, Argv: e.Args}.Display(), e.Outcome, e.Took)
	}
}
