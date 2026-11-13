package runner

import (
	"context"
	"os"
	"os/exec"
	"slices"
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
	program string // "" for agentsync, else the Program's name
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
	// starting is read-held by runCore from just before Start until the
	// child is tracked. The child runs (and can be seen running) before
	// Start returns, so without it a Shutdown or KillAll that takes its
	// snapshot in between would miss a child that is already running and
	// leave it behind. Shutdown and KillAll hold it for their snapshot.
	starting sync.RWMutex
	// closed is set once Shutdown or KillAll has begun and stays set for
	// the rest of the process: runCore then starts no new child.
	closed atomic.Bool
	// killedAll is set by KillAll before its snapshot and stays set. A
	// child tracked after that snapshot (it was between Start and track,
	// which KillAll never waits for) is killed as it is tracked: either
	// the snapshot holds it or its tracking sees the flag (startTracked).
	killedAll atomic.Bool
}

// escalated is set by KillAll during a Shutdown, which then kills at once;
// Shutdown clears it when it starts, so a KillAll with nothing live, or
// before any Shutdown, does not shorten a later one.
var escalated atomic.Bool

// killGroup sends SIGKILL to c's group and, if it was sent, marks c as
// killed. Both happen under live.mu, so a Shutdown that sees the group gone
// and then asks killedAt cannot fall between the kill and its mark and
// report a killed child as exited. A group already gone exited on its own.
func killGroup(c *child) {
	live.mu.Lock()
	defer live.mu.Unlock()
	if signalGroup(c.cmd, os.Kill) != nil {
		return
	}
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

// track records cmd, an agentsync child, as live.
func track(cmd *exec.Cmd, args []string, write bool) { trackAs(cmd, args, write, "") }

// trackAs records cmd as live under program ("" for agentsync) and
// returns its record.
func trackAs(cmd *exec.Cmd, args []string, write bool, program string) *child {
	live.mu.Lock()
	defer live.mu.Unlock()
	if live.cmds == nil {
		live.cmds = map[*exec.Cmd]*child{}
	}
	c := &child{cmd: cmd, args: args, write: write, program: program, started: time.Now()}
	live.cmds[cmd] = c
	return c
}

// untrack removes cmd from the live registry once it has ended.
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

// startedSnapshot is snapshot once no Run is between Start and track:
// every child started before it is in it. With the registry closed no new
// start begins, so this waits only for those in progress, and only until a
// KillAll escalates: exec can hang (a dead mount), and the escape must not.
func startedSnapshot() []*child {
	for !live.starting.TryLock() {
		if escalated.Load() {
			return snapshot()
		}
		time.Sleep(time.Millisecond)
	}
	defer live.starting.Unlock()
	return snapshot()
}

// Live reports the argv of every child still running, as copies the
// caller may change. Tests use it to check that nothing was left behind
// without ending the registry, as a Shutdown would.
func Live() [][]string {
	var out [][]string
	for _, c := range snapshot() {
		out = append(out, slices.Clone(c.args))
	}
	return out
}

// snapshot copies the live children under the registry's lock.
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
// Run, without the global flags or a Program's pre-args), the Program that
// ran it ("" for agentsync, else "git" or "chezmoi"), whether it was a
// write, the Outcome
// ("exited" on the signal within its wait, "killed" after it, "orphaned"
// if even SIGKILL did not take within killGrace) and how long it took.
type Ended struct {
	Args    []string
	Program string
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
	escalated.Store(false)  // escalation is scoped to this call
	live.closed.Store(true) // nothing new starts from here on
	forgetKilled(nil)       // marks of children already reaped are stale
	children := startedSnapshot()
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

// newEnding starts a Shutdown's record of children: each gets a deadline
// of start plus readWait, or writeWait for a write.
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
	killGroup(c)
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

// record notes how child c ended and how long after the start.
func (r *ending) record(c *child, outcome string, now time.Time) {
	r.out = append(r.out, Ended{Args: c.args, Program: c.program, Write: c.write, Outcome: outcome, Took: now.Sub(r.start)})
}

// KillAll sends SIGKILL to every live child's process group at once and
// makes a Shutdown in progress stop waiting: the second ctrl+c or SIGTERM
// while the TUI is already going away. Nothing new starts after it. It is
// best effort and never waits for a start in progress, which may hang; a
// child whose start was in progress is killed when it is tracked
// (killedAll).
func KillAll() {
	escalated.Store(true)
	live.closed.Store(true)
	live.killedAll.Store(true)
	for _, c := range snapshot() {
		killGroup(c)
	}
}
