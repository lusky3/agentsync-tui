package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// trapRun starts testdata/trap-int.sh through Exec with the given grace,
// cancels its context once the script's SIGINT trap is armed, and returns
// what the script logged, how long Run took after cancel and Run's error.
func trapRun(t *testing.T, mode string, grace time.Duration) (log string, took time.Duration, err error) {
	t.Helper()
	wd, werr := os.Getwd()
	if werr != nil {
		t.Fatal(werr)
	}
	logPath := filepath.Join(t.TempDir(), "trap.log")
	t.Setenv("TRAP_LOG", logPath)
	t.Setenv("TRAP_MODE", mode)
	e := NewExec(filepath.Join(wd, "testdata", "trap-int.sh"), ScopeUser, "")
	e.grace = grace
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, rerr := e.Run(ctx, "plugin", "upgrade", "--all"); done <- rerr }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		b, _ := os.ReadFile(logPath)
		if strings.Contains(string(b), "ready") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper script never armed its trap")
		}
		time.Sleep(10 * time.Millisecond)
	}
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

func TestNewExecDefaultGrace(t *testing.T) {
	if InterruptGrace != 10*time.Second || NewExec("x", ScopeUser, "").grace != InterruptGrace {
		t.Fatalf("InterruptGrace = %v, NewExec grace = %v", InterruptGrace, NewExec("x", ScopeUser, "").grace)
	}
}

// Cancelling a run sends SIGINT first, so a CLI that handles it can finish
// or roll back and exit on its own, well inside the grace period.
func TestCancelSendsSIGINTFirst(t *testing.T) {
	log, took, err := trapRun(t, "exit", 5*time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if !strings.Contains(log, "INT") {
		t.Fatalf("child never saw SIGINT (killed outright?); log %q", log)
	}
	if took > 3*time.Second {
		t.Errorf("Run took %v after cancel: the child exited on SIGINT, it should not wait out the grace", took)
	}
}

// A child that ignores SIGINT is killed once the grace period has passed,
// and it saw the SIGINT before that.
func TestCancelKillsAfterGrace(t *testing.T) {
	const grace = 300 * time.Millisecond
	log, took, err := trapRun(t, "ignore", grace)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if !strings.Contains(log, "INT") {
		t.Fatalf("child was killed without a SIGINT first; log %q", log)
	}
	if took < grace {
		t.Errorf("killed after %v, before the %v grace", took, grace)
	}
	if took > 5*time.Second {
		t.Errorf("Run took %v: SIGKILL fallback did not fire", took)
	}
}
