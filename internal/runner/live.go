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
// wait for Run (Shutdown kills the group instead).
var live struct {
	mu   sync.Mutex
	cmds map[*exec.Cmd]*child
}

// escalated is set by KillAll: a Shutdown in progress kills at once.
var escalated atomic.Bool

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
	delete(live.cmds, cmd)
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
	children := snapshot()
	if len(children) == 0 {
		escalated.Store(false)
		return nil
	}
	for _, c := range children {
		_ = signalGroup(c.cmd, sig)
	}
	start := time.Now()
	deadline := make(map[*child]time.Time, len(children))
	for _, c := range children {
		wait := readWait
		if c.write {
			wait = writeWait
		}
		deadline[c] = start.Add(wait)
	}
	killed := map[*child]time.Time{}
	gone := map[*child]bool{}
	var out []Ended
	for {
		now := time.Now()
		left, allKilled := 0, true
		var lastKill time.Time
		for _, c := range children {
			if gone[c] {
				continue
			}
			if groupGone(c.cmd) {
				gone[c] = true
				oc := "exited"
				if _, k := killed[c]; k || escalated.Load() { // KillAll kills directly
					oc = "killed"
				}
				out = append(out, Ended{Args: c.args, Write: c.write, Outcome: oc, Took: now.Sub(start)})
				continue
			}
			left++
			if _, k := killed[c]; !k && (escalated.Load() || !now.Before(deadline[c])) {
				_ = signalGroup(c.cmd, os.Kill)
				killed[c] = now
			}
			k, ok := killed[c]
			allKilled = allKilled && ok
			if k.After(lastKill) {
				lastKill = k
			}
		}
		if left == 0 {
			break
		}
		if allKilled && now.After(lastKill.Add(killGrace)) {
			for _, c := range children {
				if !gone[c] {
					out = append(out, Ended{Args: c.args, Write: c.write, Outcome: "orphaned", Took: now.Sub(start)})
				}
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	escalated.Store(false)
	return out
}

// KillAll sends SIGKILL to every live child's process group at once and
// makes a Shutdown in progress stop waiting: the second ctrl+c or SIGTERM
// while the TUI is already going away.
func KillAll() {
	escalated.Store(true)
	for _, c := range snapshot() {
		_ = signalGroup(c.cmd, os.Kill)
	}
}
