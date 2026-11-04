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
