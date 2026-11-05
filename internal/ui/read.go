package ui

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/runner"
)

// VerifyTimeout bounds a pre-mutation re-read (Verifier), which the user is
// waiting on: a stale answer is worse than a refused key.
const VerifyTimeout = 5 * time.Second

// JSONFlag asks a read for agentsync's machine-readable output (status,
// diff, explain).
const JSONFlag = "--json"

// ErrNoRunner is the Read error when the screen has no runner.
var ErrNoRunner = errors.New("no runner")

// Read runs one read-only command through r, bounded by ctx and timeout
// (0 = none; screens pass Deps.Limits().Read). err is ErrNoRunner or the
// runner's error (spawn failure, cancellation, the bound); it is never set
// for a non-zero exit, which callers judge themselves (status exits 2 with a
// valid report; doctor's non-zero exit is a finding).
func Read(ctx context.Context, r runner.Runner, timeout time.Duration, argv ...string) (runner.Result, error) {
	if r == nil {
		return runner.Result{}, ErrNoRunner
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return r.Run(ctx, argv...)
}

// ReadNote describes a failed Read for the status line, display-safe: "" when
// err is nil and the exit code is 0, "<name> unavailable: <reason>" for an
// error and "<name> exited N[: <first stderr line>]" for a non-zero exit.
func ReadNote(name string, res runner.Result, err error) string {
	switch {
	case errors.Is(err, runner.ErrNotInstalled):
		return name + " unavailable: agentsync is not installed"
	case err != nil:
		return name + " unavailable: " + mask.FirstLine(err.Error())
	case res.ExitCode != 0:
		n := name + " exited " + strconv.Itoa(res.ExitCode)
		if l := mask.FirstLine(res.Stderr); l != "" {
			n += ": " + l
		}
		return n
	}
	return ""
}

// Rounds owns a screen's "latest wins" background reads: Next starts a round
// and cancels the one before it (its children die now, not at their bound),
// Current tells a result from a superseded round, Cancel stops the current
// one (quit) and Supersede drops it without starting another (scope switch).
// The zero value is ready.
type Rounds struct {
	gen    int
	cancel context.CancelFunc
}

// Next cancels the current round and returns the generation and context of
// the next one, derived from base.
func (r *Rounds) Next(base context.Context) (int, context.Context) {
	r.Supersede()
	ctx, cancel := context.WithCancel(base)
	r.cancel = cancel
	return r.gen, ctx
}

// Current reports whether gen is the current round.
func (r *Rounds) Current(gen int) bool { return gen == r.gen }

// Gen is the current generation (for tests that forge a stale result).
func (r *Rounds) Gen() int { return r.gen }

// Cancel stops the current round's children. Its results, if any still
// arrive, keep the current generation.
func (r *Rounds) Cancel() {
	if r.cancel != nil {
		r.cancel()
		r.cancel = nil
	}
}

// Supersede cancels the current round and makes its results stale.
func (r *Rounds) Supersede() {
	r.Cancel()
	r.gen++
}

// verifierIDs hands every Verifier a process-unique owner, because the
// shell broadcasts a VerifiedMsg to every screen.
var verifierIDs atomic.Uint64

// Verifier is the asynchronous pre-mutation check the list screens share:
// before a mutation is built, the list is re-read and the action is only
// emitted if the selected item is unchanged. Verifier owns the busy flag,
// the request counter and the cancellation of a superseded read; the
// screen keeps the domain comparison.
type Verifier struct {
	id     uint64
	rounds Rounds
	busy   bool
}

// VerifiedMsg carries one finished verification read back to its owner.
type VerifiedMsg struct {
	owner uint64
	req   int
	Val   any
}

// Busy reports whether a verification read is in flight.
func (v *Verifier) Busy() bool { return v.busy }

// Invalidate drops any in-flight read: its result will be ignored. Screens
// call it on cursor moves, scope switches and refreshes.
func (v *Verifier) Invalidate() {
	v.busy = false
	v.rounds.Supersede()
}

// Start begins a verification read: read runs on its own goroutine with a
// context derived from base and its return value comes back in a
// VerifiedMsg that Accept recognises.
func (v *Verifier) Start(base context.Context, read func(ctx context.Context) any) tea.Cmd {
	if v.id == 0 {
		v.id = verifierIDs.Add(1)
	}
	v.busy = true
	req, ctx := v.rounds.Next(base)
	id := v.id
	return func() tea.Msg { return VerifiedMsg{owner: id, req: req, Val: read(ctx)} }
}

// Accept returns the value of a current verification read and ends the busy
// state; a foreign or stale message yields false and changes nothing.
func (v *Verifier) Accept(m VerifiedMsg) (any, bool) {
	if !v.busy || m.owner != v.id || !v.rounds.Current(m.req) {
		return nil, false
	}
	v.busy = false
	return m.Val, true
}
