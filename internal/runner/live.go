package runner

import (
	"context"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"
)

// writeKey marks a context as a write's (MarkWrite); Shutdown gives a write
// the full grace and a read only a short wait.
type writeKey struct{}

// MarkWrite returns ctx marked as belonging to a write (a non-ReadOnly
// action). The shell marks the context of every write it starts.
func MarkWrite(ctx context.Context) context.Context {
	return context.WithValue(ctx, writeKey{}, true)
}

// IsWrite reports whether ctx was marked by MarkWrite.
func IsWrite(ctx context.Context) bool { return ctx.Value(writeKey{}) != nil }

// child is one tracked run.
type child struct {
	cmd     *exec.Cmd
	args    []string
	write   bool
	started time.Time
}

// live is every child Run has started and not yet reaped: the set Shutdown
// signals when the program is going away. Run keeps a child here from
// Start until Wait returns, which with a helper holding the pipes open can
// be WaitDelay after agentsync itself exited, so the TUI's exit must not
// wait for Run (Shutdown kills the group instead). killed records which
// children were sent SIGKILL (by Shutdown's deadline or by KillAll), so an
// outcome says what happened rather than what was intended.
var live struct {
	mu     sync.Mutex
	cmds   map[*exec.Cmd]*child
	killed map[*child]time.Time
}

// escalated is set by KillAll during a Shutdown, which then kills at once;
// Shutdown clears it when it starts, so a KillAll with nothing live, or
// before any Shutdown, does not shorten a later one.
var escalated atomic.Bool

// markKilled notes that c's group was sent SIGKILL.
func markKilled(c *child) {
	live.mu.Lock()
	defer live.mu.Unlock()
	if live.killed == nil {
		live.killed = map[*child]time.Time{}
	}
	if _, ok := live.killed[c]; !ok {
		live.killed[c] = time.Now()
	}
}

// killedAt reports whether and when c's group was sent SIGKILL.
func killedAt(c *child) (time.Time, bool) {
	live.mu.Lock()
	defer live.mu.Unlock()
	t, ok := live.killed[c]
	return t, ok
}

func track(cmd *exec.Cmd, args []string, write bool) {
	live.mu.Lock()
	defer live.mu.Unlock()
	if live.cmds == nil {
		live.cmds = map[*exec.Cmd]*child{}
	}
	live.cmds[cmd] = &child{cmd: cmd, args: args, write: write, started: time.Now()}
}

func untrack(cmd *exec.Cmd) {
	live.mu.Lock()
	defer live.mu.Unlock()
	delete(live.cmds, cmd) // its killed mark stays until the Shutdown that reads it is done
}

// forgetKilled drops the kill marks of children a Shutdown has reported
// and of any child no longer live.
func forgetKilled(done []*child) {
	live.mu.Lock()
	defer live.mu.Unlock()
	for _, c := range done {
		delete(live.killed, c)
	}
	for c := range live.killed {
		if _, ok := live.cmds[c.cmd]; !ok {
			delete(live.killed, c)
		}
	}
}

func snapshot() []*child {
	live.mu.Lock()
	defer live.mu.Unlock()
	out := make([]*child, 0, len(live.cmds))
	for _, c := range live.cmds {
		out = append(out, c)
	}
	return out
}

// Ended is what became of one child at Shutdown: its argv (as given to
// Run, without the global flags), whether it was a write, the Outcome
// ("exited" on the signal within its wait, "killed" after it, "orphaned"
// if even SIGKILL did not take within killGrace) and how long it took.
type Ended struct {
	Args    []string
	Write   bool
	Outcome string
	Took    time.Duration
}

// killGrace is how long Shutdown waits for SIGKILL to take effect.
const killGrace = 2 * time.Second

// Shutdown ends every child still running when the program is going away:
// each live child's process group is sent sig (os.Interrupt for a quit,
// whose reads were already cancelled; SIGTERM or SIGHUP forwarded from the
// signal that ends the TUI), given its wait to exit (readWait for a read,
// writeWait for a write, see MarkWrite), then sent SIGKILL. It returns the
// outcome of every child once no process of those groups is left (or
// killGrace after the kill), so a helper agentsync spawned that ignores
// SIGINT cannot outlive the TUI. A KillAll meanwhile ends the waits at
// once. Nothing happens when no child is live.
func Shutdown(sig os.Signal, readWait, writeWait time.Duration) []Ended {
	escalated.Store(false) // escalation is scoped to this call
	forgetKilled(nil)      // marks of children already reaped are stale
	children := snapshot()
	if len(children) == 0 {
		return nil
	}
	for _, c := range children {
		_ = signalGroup(c.cmd, sig)
	}
	r := newEnding(children, time.Now(), readWait, writeWait)
	for !r.poll(time.Now()) {
		time.Sleep(20 * time.Millisecond)
	}
	escalated.Store(false)
	forgetKilled(children)
	return r.out
}

// ending is one Shutdown's progress: the children it signalled, when each
// one's wait runs out, which are gone, and the outcomes recorded so far.
type ending struct {
	children []*child
	start    time.Time
	deadline map[*child]time.Time
	gone     map[*child]bool
	out      []Ended
}

func newEnding(children []*child, start time.Time, readWait, writeWait time.Duration) *ending {
	deadline := make(map[*child]time.Time, len(children))
	for _, c := range children {
		wait := readWait
		if c.write {
			wait = writeWait
		}
		deadline[c] = start.Add(wait)
	}
	return &ending{children: children, start: start, deadline: deadline, gone: map[*child]bool{}}
}

// poll checks every child not yet gone once, at now: a group that has gone
// is recorded as exited or killed; one still present is sent SIGKILL when
// its wait has run out or a KillAll escalated. It reports whether the
// Shutdown is over: every group gone, or every one left killed more than
// killGrace ago (those are recorded as orphaned).
func (r *ending) poll(now time.Time) bool {
	left, allKilled := 0, true
	var lastKill time.Time
	for _, c := range r.children {
		if r.gone[c] {
			continue
		}
		if groupGone(c.cmd) {
			r.reap(c, now)
			continue
		}
		left++
		r.killIfDue(c, now)
		k, ok := killedAt(c)
		allKilled = allKilled && ok
		if k.After(lastKill) {
			lastKill = k
		}
	}
	if left == 0 {
		return true
	}
	if allKilled && now.After(lastKill.Add(killGrace)) {
		r.orphanRest(now)
		return true
	}
	return false
}

// reap records c, whose group is gone, as killed if it was sent SIGKILL and
// as exited otherwise.
func (r *ending) reap(c *child, now time.Time) {
	r.gone[c] = true
	oc := "exited"
	if _, k := killedAt(c); k {
		oc = "killed"
	}
	r.record(c, oc, now)
}

// killIfDue sends SIGKILL to c's group, once, when its wait has run out or
// a KillAll escalated this Shutdown.
func (r *ending) killIfDue(c *child, now time.Time) {
	if _, killed := killedAt(c); killed {
		return
	}
	if !escalated.Load() && now.Before(r.deadline[c]) {
		return
	}
	if signalGroup(c.cmd, os.Kill) == nil {
		markKilled(c)
	}
}

// orphanRest records every child not gone as orphaned: even SIGKILL did not
// end its group within killGrace.
func (r *ending) orphanRest(now time.Time) {
	for _, c := range r.children {
		if !r.gone[c] {
			r.record(c, "orphaned", now)
		}
	}
}

func (r *ending) record(c *child, outcome string, now time.Time) {
	r.out = append(r.out, Ended{Args: c.args, Write: c.write, Outcome: outcome, Took: now.Sub(r.start)})
}

// KillAll sends SIGKILL to every live child's process group at once and
// makes a Shutdown in progress stop waiting: the second ctrl+c or SIGTERM
// while the TUI is already going away.
func KillAll() {
	escalated.Store(true)
	for _, c := range snapshot() {
		if signalGroup(c.cmd, os.Kill) == nil { // a group already gone exited on its own
			markKilled(c)
		}
	}
}
