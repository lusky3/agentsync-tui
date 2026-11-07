//go:build unix

package runner

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// setProcessGroup starts the child in its own process group, so a signal
// sent to the group reaches the helpers agentsync spawns (git, npm) as
// well as agentsync itself. A side effect is that the terminal's SIGHUP no
// longer reaches them directly; the TUI's own cancellation does.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// signalGroup sends sig to the child's whole process group. A group that
// is already gone reports os.ErrProcessDone.
func signalGroup(cmd *exec.Cmd, sig os.Signal) error {
	s, ok := sig.(syscall.Signal)
	if !ok || cmd.Process == nil {
		return os.ErrProcessDone
	}
	err := syscall.Kill(-cmd.Process.Pid, s)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}

// groupGone reports whether no process of the child's group is left.
func groupGone(cmd *exec.Cmd) bool {
	if cmd.Process == nil {
		return true
	}
	return errors.Is(syscall.Kill(-cmd.Process.Pid, 0), syscall.ESRCH)
}

// setSession starts the child in a new session (R1). It is then the leader
// of its own process group too, so signalGroup still reaches everything it
// spawns, and it has no controlling terminal: ssh, a credential helper or
// a hook that wants /dev/tty fails at once instead of stopping behind the
// TUI. Setpgid is not set as well: a session leader cannot setpgid (EPERM).
func setSession(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}
