//go:build !unix

package runner

import (
	"os"
	"os/exec"
)

// setProcessGroup: no process groups here; signals reach agentsync only.
func setProcessGroup(*exec.Cmd) {}

// signalGroup signals the child itself (os.Kill when sig is not
// deliverable, as on Windows).
func signalGroup(cmd *exec.Cmd, sig os.Signal) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	if err := cmd.Process.Signal(sig); err != nil && sig != os.Kill {
		return cmd.Process.Kill()
	} else if err != nil {
		return err
	}
	return nil
}

// groupGone: without process groups only the child itself is known; it is
// gone once Run has reaped it (untrack), which Shutdown observes through
// the live set, so a tracked child counts as present.
func groupGone(cmd *exec.Cmd) bool {
	live.mu.Lock()
	defer live.mu.Unlock()
	_, present := live.cmds[cmd]
	return !present
}
