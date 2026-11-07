//go:build unix

package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// programTrapRun is trapRun for a Program: it runs testdata/trap-int.sh as
// git with the given grace, cancels once its SIGINT trap is armed, and
// returns what the script logged, how long Run took after cancel and Run's
// error.
func programTrapRun(t *testing.T, mode string, grace time.Duration) (log string, took time.Duration, err error) {
	t.Helper()
	wd, _ := os.Getwd()
	logPath := filepath.Join(t.TempDir(), "trap.log")
	p := NewGit(filepath.Join(wd, "testdata", "trap-int.sh"))
	p.Env = append(os.Environ(), "TRAP_LOG="+logPath, "TRAP_MODE="+mode)
	p.grace = grace
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, rerr := p.Run(ctx, "-C", "/r", "status"); done <- rerr }()
	waitLog(t, logPath, "ready")
	start := time.Now()
	cancel()
	select {
	case err = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	took = time.Since(start)
	b, _ := os.ReadFile(logPath)
	return string(b), took, err
}

// waitLog waits (10 s at most) for logPath to contain s.
func waitLog(t *testing.T, logPath, s string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if b, _ := os.ReadFile(logPath); strings.Contains(string(b), s) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("helper never logged %q", s)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// startSpawnChild runs spawn-child.sh as git (its own session) and returns
// the helper's pid (its process group id), its child's pid and Run's channel.
func startSpawnChild(t *testing.T, ctx context.Context, grace time.Duration) (self, child int, done <-chan error) {
	t.Helper()
	wd, _ := os.Getwd()
	logPath := filepath.Join(t.TempDir(), "spawn.log")
	p := NewGit(filepath.Join(wd, "testdata", "spawn-child.sh"))
	p.Env = append(os.Environ(), "TRAP_LOG="+logPath)
	p.grace = grace
	ch := make(chan error, 1)
	go func() { _, rerr := p.Run(ctx, "-C", "/r", "status"); ch <- rerr }()
	waitLog(t, logPath, "ready")
	b, _ := os.ReadFile(logPath)
	self, child = pidsFrom(string(b))
	t.Cleanup(func() { _ = syscall.Kill(-self, syscall.SIGKILL) })
	return self, child, ch
}

// Cancelling a Program run behaves like an Exec run: SIGINT first, SIGKILL
// to the whole group after the grace, so a helper git spawned (ssh, a
// credential helper, a hook) that ignores SIGINT does not outlive it.
func TestProgramCancelInterruptsThenKillsGroup(t *testing.T) {
	t.Run("SIGINT first", testProgramSIGINTFirst)
	t.Run("killed after grace", testProgramKilledAfterGrace)
	t.Run("group signalled", testProgramGroupSignalled)
}

// testProgramSIGINTFirst: a child that exits on SIGINT ends the run well
// before any kill.
func testProgramSIGINTFirst(t *testing.T) {
	log, took, err := programTrapRun(t, "exit", 5*time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if !strings.Contains(log, "INT") {
		t.Fatalf("child never saw SIGINT; log %q", log)
	}
	if took > 3*time.Second {
		t.Errorf("Run took %v after cancel; the child exited on SIGINT", took)
	}
}

// testProgramKilledAfterGrace: a child that ignores SIGINT is killed once
// the grace has passed, and not much later.
func testProgramKilledAfterGrace(t *testing.T) {
	const grace = 300 * time.Millisecond
	log, took, err := programTrapRun(t, "ignore", grace)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if !strings.Contains(log, "INT") {
		t.Fatalf("killed without a SIGINT first; log %q", log)
	}
	if took < grace || took > 5*time.Second {
		t.Errorf("killed after %v, want the %v grace and not much more", took, grace)
	}
}

// testProgramGroupSignalled: the helper's own child, in its process group,
// does not survive the cancelled run.
func testProgramGroupSignalled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, child, done := startSpawnChild(t, ctx, 300*time.Millisecond)
	if !alive(child) {
		t.Fatal("precondition: child not running")
	}
	cancel()
	if err := awaitRun(t, done); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	awaitGone(t, child)
}

// awaitRun waits (20 s at most) for a cancelled run to return its error.
func awaitRun(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	return nil
}

// awaitGone waits (5 s at most) for child to have exited.
func awaitGone(t *testing.T, child int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for alive(child) {
		if time.Now().After(deadline) {
			t.Fatalf("child %d survived the cancelled run", child)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Shutdown reports a live Program child under its program's name, so the
// activity log can say "git …" rather than "agentsync …".
func TestEndedCarriesProgram(t *testing.T) {
	self, child, _ := startSpawnChild(t, context.Background(), 10*time.Second)
	ended := Shutdown(os.Interrupt, 300*time.Millisecond, 300*time.Millisecond)
	if len(ended) != 1 || ended[0].Program != "git" || ended[0].Args[len(ended[0].Args)-1] != "status" {
		t.Fatalf("outcomes %+v, want one git status", ended)
	}
	if !pgidGone(self) || alive(child) {
		t.Error("group survived Shutdown")
	}
}
