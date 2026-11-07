//go:build linux

package runner

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// ttyHelperEnv, when set, makes TestChildHasNoControllingTerminal act as
// the helper: a process that has a controlling terminal runs fake-git.sh
// through a Program and writes what it reported to the named file.
const ttyHelperEnv = "AGENTSYNC_TUI_TTY_HELPER_OUT"

// R1: a git child runs in its own session, with no controlling terminal,
// so ssh, a credential helper or pinentry cannot open /dev/tty and stop
// behind the TUI's alt-screen; it fails at once instead. The meaningful
// case runs the Program from a helper process that does have a
// controlling terminal (a fresh pty), since the test binary itself may not.
func TestChildHasNoControllingTerminal(t *testing.T) {
	if out := os.Getenv(ttyHelperEnv); out != "" {
		ttyHelper(t, out)
		return
	}
	t.Run("plain", func(t *testing.T) {
		p := NewGit(fakeGit(t))
		p.Env = baseEnv()
		if got := runFake(t, context.Background(), p, "-C", "/r", "status").tty; got != "none" {
			t.Fatalf("tty = %q, want none", got)
		}
	})
	t.Run("parent has a controlling terminal", func(t *testing.T) {
		master, slave := openPty(t)
		defer func() { _ = master.Close() }()
		out := filepath.Join(t.TempDir(), "helper.out")
		cmd := exec.Command(os.Args[0], "-test.run=^TestChildHasNoControllingTerminal$", "-test.count=1")
		cmd.Env = append(os.Environ(), ttyHelperEnv+"="+out)
		var logs bytes.Buffer
		cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, &logs, &logs
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
		err := cmd.Run()
		_ = slave.Close()
		if err != nil {
			t.Fatalf("helper: %v\n%s", err, logs.String())
		}
		b, err := os.ReadFile(out)
		if err != nil {
			t.Fatalf("helper wrote no report: %v\n%s", err, logs.String())
		}
		if got := strings.TrimSpace(string(b)); got != "parent=yes child=none" {
			t.Fatalf("helper report %q, want the parent on a tty and the git child on none", got)
		}
	})
}

// ttyHelper runs in the helper process: it records whether it has a
// controlling terminal itself and whether a Program child does.
func ttyHelper(t *testing.T, out string) {
	parent := "none"
	if f, err := os.Open("/dev/tty"); err == nil {
		parent = "yes"
		_ = f.Close()
	}
	p := NewGit(fakeGit(t))
	p.Env = baseEnv()
	child := runFake(t, context.Background(), p, "-C", "/r", "status").tty
	if err := os.WriteFile(out, []byte("parent="+parent+" child="+child+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// openPty opens a new pseudo-terminal pair (master, slave) without
// making either the caller's controlling terminal.
func openPty(t *testing.T) (master, slave *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Skipf("no pty: %v", err)
	}
	var unlock int32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&unlock))); e != 0 {
		t.Fatalf("unlockpt: %v", e)
	}
	var n uint32
	if _, _, e := syscall.Syscall(syscall.SYS_IOCTL, master.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n))); e != 0 {
		t.Fatalf("ptsname: %v", e)
	}
	slave, err = os.OpenFile("/dev/pts/"+strconv.Itoa(int(n)), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatal(err)
	}
	return master, slave
}
