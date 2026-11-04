package runner

import (
	"os"
	"os/exec"
	"sync"
	"time"
)

// live is every child Run has started and not yet reaped: the set Shutdown
// signals when the program is going away. Run keeps a child here from
// Start until Wait returns, which with a helper holding the pipes open can
// be WaitDelay after agentsync itself exited, so the TUI's exit must not
// wait for Run (Shutdown kills the group instead).
var live struct {
	mu   sync.Mutex
	cmds map[*exec.Cmd]struct{}
}

func track(cmd *exec.Cmd) {
	live.mu.Lock()
	defer live.mu.Unlock()
	if live.cmds == nil {
		live.cmds = map[*exec.Cmd]struct{}{}
	}
	live.cmds[cmd] = struct{}{}
}

func untrack(cmd *exec.Cmd) {
	live.mu.Lock()
	defer live.mu.Unlock()
	delete(live.cmds, cmd)
}

// killGrace is how long Shutdown waits for SIGKILL to take effect.
const killGrace = 2 * time.Second

// Shutdown ends every child still running when the program is going away:
// each live child's process group is sent sig (os.Interrupt for a quit,
// whose reads were already cancelled; SIGTERM or SIGHUP forwarded from the
// signal that ends the TUI), given wait to exit, then sent SIGKILL. It
// returns once no process of those groups is left (or killGrace after the
// kill), so a helper agentsync spawned that ignores SIGINT cannot outlive
// the TUI. Nothing happens when no child is live.
func Shutdown(sig os.Signal, wait time.Duration) {
	live.mu.Lock()
	cmds := make([]*exec.Cmd, 0, len(live.cmds))
	for c := range live.cmds {
		cmds = append(cmds, c)
	}
	live.mu.Unlock()
	if len(cmds) == 0 {
		return
	}
	for _, c := range cmds {
		_ = signalGroup(c, sig)
	}
	if !settle(cmds, wait) {
		for _, c := range cmds {
			_ = signalGroup(c, os.Kill)
		}
		settle(cmds, killGrace)
	}
}

// settle polls until every group is gone or wait has passed.
func settle(cmds []*exec.Cmd, wait time.Duration) bool {
	deadline := time.Now().Add(wait)
	for {
		gone := true
		for _, c := range cmds {
			if !groupGone(c) {
				gone = false
				break
			}
		}
		if gone {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(20 * time.Millisecond)
	}
}
