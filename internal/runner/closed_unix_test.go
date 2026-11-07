//go:build unix

package runner

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// reopen undoes the end of the registry that a Shutdown or KillAll makes
// for the rest of the process, so the next test can start children.
func reopen() { live.closed.Store(false) }

// trapExec is trap-int.sh in the mode that exits on SIGINT, logging to log.
func trapExec(t *testing.T, log string) *Exec {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	e := NewExec(filepath.Join(wd, "testdata", "trap-int.sh"), ScopeUser, "")
	e.Env = append(os.Environ(), "TRAP_LOG="+log, "TRAP_MODE=exit")
	return e
}

// A Shutdown that comes while a child is between its Start and its
// tracking still reports and signals it: the child is already running, so
// missing it would leave it behind. The hook calls Shutdown in that gap and
// gives it time to finish there, which it does only if it took its
// snapshot without waiting for the start.
func TestShutdownWaitsForAStartingChild(t *testing.T) {
	t.Cleanup(reopen)
	t.Cleanup(func() { afterStartHook = nil })
	ended := make(chan []Ended, 1)
	afterStartHook = func() {
		go func() { ended <- Shutdown(os.Interrupt, 10*time.Second, 10*time.Second) }()
		select {
		case got := <-ended: // Shutdown is over with the child not yet tracked
			ended <- got
		case <-time.After(300 * time.Millisecond):
		}
	}
	ran := make(chan struct{})
	go func() {
		defer close(ran)
		_, _ = trapExec(t, filepath.Join(t.TempDir(), "trap.log")).Run(context.Background(), "gap")
	}()
	var got []Ended
	select {
	case got = <-ended:
	case <-time.After(15 * time.Second):
		t.Fatal("Shutdown did not return")
	}
	// exited, not killed, with a 10 s wait: the child was sent the signal
	if len(got) != 1 || got[0].Args[0] != "gap" || got[0].Outcome != "exited" {
		t.Fatalf("outcomes %+v, want the starting child reported as exited on the signal", got)
	}
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the child was signalled")
	}
}

// Once Shutdown or KillAll has begun, a run starts nothing: it returns
// ErrShuttingDown, and the child (which would write its log at once) never
// runs.
func TestRunAfterShutdownStartsNothing(t *testing.T) {
	for _, c := range []struct {
		name string
		end  func()
	}{
		{"Shutdown", func() { Shutdown(os.Interrupt, time.Second, time.Second) }},
		{"KillAll", KillAll},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Cleanup(reopen)
			c.end()
			marker := filepath.Join(t.TempDir(), "trap.log")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if _, err := trapExec(t, marker).Run(ctx, "status"); !errors.Is(err, ErrShuttingDown) {
				t.Errorf("Run after %s: err %v, want ErrShuttingDown", c.name, err)
			}
			if _, err := os.Stat(marker); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("a child ran after %s (stat %v)", c.name, err)
			}
		})
	}
}

// KillAll, the escape from a Shutdown, does not wait for a start in
// progress (exec hung on a dead mount), and the Shutdown it escalates stops
// waiting for it too.
func TestKillAllDoesNotWaitForAStart(t *testing.T) {
	t.Cleanup(reopen)
	live.starting.RLock() // a run stuck in exec
	var release sync.Once
	t.Cleanup(func() { release.Do(live.starting.RUnlock) })
	done := make(chan []Ended, 1)
	go func() { done <- Shutdown(os.Interrupt, 10*time.Second, 10*time.Second) }()
	deadline := time.Now().Add(2 * time.Second)
	for !live.closed.Load() { // Shutdown has begun
		if time.Now().After(deadline) {
			t.Fatal("Shutdown never ended the registry")
		}
		time.Sleep(time.Millisecond)
	}
	killed := make(chan struct{})
	go func() { KillAll(); close(killed) }()
	select {
	case <-killed:
	case <-time.After(2 * time.Second):
		t.Fatal("KillAll waited for the start")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown waited for the start despite KillAll")
	}
	release.Do(live.starting.RUnlock)
}
