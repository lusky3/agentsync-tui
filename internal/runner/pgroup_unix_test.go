//go:build unix

package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// alive reports whether pid still exists.
func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// Cancelling a run signals agentsync's whole process group, so a child it
// spawned (git, npm) that ignores SIGINT is killed after the grace too,
// rather than living on as an orphan.
func TestCancelSignalsProcessGroup(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "spawn.log")
	t.Setenv("TRAP_LOG", logPath)
	e := NewExec(filepath.Join(wd, "testdata", "spawn-child.sh"), ScopeUser, "")
	e.grace = 300 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, rerr := e.Run(ctx, "plugin", "upgrade", "--all"); done <- rerr }()
	var child int
	deadline := time.Now().Add(10 * time.Second)
	for child == 0 {
		b, _ := os.ReadFile(logPath)
		for _, l := range strings.Split(string(b), "\n") {
			if pid, ok := strings.CutPrefix(l, "child "); ok && strings.Contains(string(b), "ready") {
				child, _ = strconv.Atoi(pid)
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("helper never reported its child")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { _ = syscall.Kill(child, syscall.SIGKILL) })
	if !alive(child) {
		t.Fatal("precondition: child not running")
	}
	cancel()
	select {
	case err = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for alive(child) {
		if time.Now().After(deadline) {
			t.Fatalf("child %d survived the cancelled run (not in the signalled process group)", child)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// spawn starts spawn-child.sh through Exec and returns the helper's pid
// (its process group id), its child's pid and the channel Run reports on.
func spawn(t *testing.T, grace time.Duration) (self, child int, done <-chan error) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "spawn.log")
	t.Setenv("TRAP_LOG", logPath)
	e := NewExec(filepath.Join(wd, "testdata", "spawn-child.sh"), ScopeUser, "")
	e.grace = grace
	ch := make(chan error, 1)
	go func() { _, rerr := e.Run(context.Background(), "plugin", "upgrade", "--all"); ch <- rerr }()
	deadline := time.Now().Add(10 * time.Second)
	for self == 0 || child == 0 {
		b, _ := os.ReadFile(logPath)
		if strings.Contains(string(b), "ready") {
			for _, l := range strings.Split(string(b), "\n") {
				if v, ok := strings.CutPrefix(l, "child "); ok {
					child, _ = strconv.Atoi(v)
				}
				if v, ok := strings.CutPrefix(l, "self "); ok {
					self, _ = strconv.Atoi(v)
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("helper never reported its pids")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { _ = syscall.Kill(-self, syscall.SIGKILL) })
	return self, child, ch
}

// pgidGone reports whether no process of group pgid is left.
func pgidGone(pgid int) bool { return errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH) }

// When the TUI goes away (q, SIGTERM, the terminal closing) Shutdown
// forwards the signal to every live child's process group, waits, then
// kills what is left, so neither agentsync nor a helper it spawned that
// ignores SIGINT is orphaned. It returns within the wait plus a short kill
// grace, with Run's own pipe wait (WaitDelay) not holding it up.
func TestShutdownTerminatesLiveGroups(t *testing.T) {
	for _, c := range []struct {
		name string
		sig  os.Signal
	}{{"quit (SIGINT)", os.Interrupt}, {"SIGTERM", syscall.SIGTERM}, {"terminal closed (SIGHUP)", syscall.SIGHUP}} {
		t.Run(c.name, func(t *testing.T) {
			self, child, done := spawn(t, 10*time.Second)
			if pgidGone(self) || !alive(child) {
				t.Fatal("precondition: group running")
			}
			start := time.Now()
			Shutdown(c.sig, 300*time.Millisecond)
			if d := time.Since(start); d > 5*time.Second {
				t.Errorf("Shutdown took %v", d)
			}
			if !pgidGone(self) || alive(child) {
				t.Fatalf("process group %d survived %v: group gone %v, child alive %v", self, c.sig, pgidGone(self), alive(child))
			}
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("Run returned %v (a killed child is an exit code, not an error)", err)
				}
			case <-time.After(5 * time.Second):
				t.Error("Run did not return after the group was killed")
			}
		})
	}
	Shutdown(os.Interrupt, time.Second) // nothing live: returns at once
}
