//go:build unix

package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// spawnKind is spawn for a child started under a read or a write context,
// with its own log so two helpers can run side by side.
func spawnKind(t *testing.T, write bool) (self, child int, done <-chan error) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "spawn.log")
	e := NewExec(filepath.Join(wd, "testdata", "spawn-child.sh"), ScopeUser, "")
	e.Env = append(os.Environ(), "TRAP_LOG="+logPath)
	ctx := context.Background()
	if write {
		ctx = MarkWrite(ctx)
	}
	ch := make(chan error, 1)
	go func() { _, rerr := e.Run(ctx, "plugin", "upgrade", "--all"); ch <- rerr }()
	deadline := time.Now().Add(10 * time.Second)
	for self == 0 || child == 0 {
		b, _ := os.ReadFile(logPath)
		if strings.Contains(string(b), "ready") {
			self, child = pidsFrom(string(b))
		}
		if time.Now().After(deadline) {
			t.Fatal("helper never reported its pids")
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Cleanup(func() { _ = syscall.Kill(-self, syscall.SIGKILL) })
	return self, child, ch
}

// A write context is visible to the runner, and a plain one is a read.
func TestMarkWrite(t *testing.T) {
	if IsWrite(context.Background()) || !IsWrite(MarkWrite(context.Background())) {
		t.Fatal("MarkWrite/IsWrite do not agree")
	}
}

// Shutdown reports what became of every child: a read that ignores the
// signal is killed after the short read wait, a write after the write
// grace; a child that exits on the signal is reported as exited. Each
// outcome names the child's argv and whether it was a write.
func TestShutdownOutcomesAndKinds(t *testing.T) {
	rself, rchild, _ := spawnKind(t, false)
	wself, wchild, _ := spawnKind(t, true)
	startExitingOnInt(t, "status", "--json")
	start := time.Now()
	ended := Shutdown(os.Interrupt, 300*time.Millisecond, 1500*time.Millisecond)
	took := time.Since(start)
	if took < 1500*time.Millisecond || took > 6*time.Second {
		t.Errorf("Shutdown took %v, want the write grace (1.5 s) and not much more", took)
	}
	if !pgidGone(rself) || !pgidGone(wself) || alive(rchild) || alive(wchild) {
		t.Fatal("a group survived")
	}
	read, write, exited := byKind(ended)
	if read == nil || write == nil || exited == nil {
		t.Fatalf("outcomes %+v", ended)
	}
	if read.Outcome != "killed" || read.Took < 300*time.Millisecond || read.Took > time.Second {
		t.Errorf("read: %+v, want killed after the short wait", *read)
	}
	if write.Outcome != "killed" || write.Took < 1500*time.Millisecond || write.Args[0] != "plugin" {
		t.Errorf("write: %+v, want killed after the write grace", *write)
	}
	if exited.Outcome != "exited" || exited.Took > time.Second || exited.Write {
		t.Errorf("exiting child: %+v, want exited quickly", *exited)
	}
}

// startExitingOnInt runs trap-int.sh with args, in the mode that exits on
// SIGINT, and returns once its trap is armed.
func startExitingOnInt(t *testing.T, args ...string) {
	t.Helper()
	wd, _ := os.Getwd()
	logPath := filepath.Join(t.TempDir(), "trap.log")
	e := NewExec(filepath.Join(wd, "testdata", "trap-int.sh"), ScopeUser, "")
	e.Env = append(os.Environ(), "TRAP_LOG="+logPath, "TRAP_MODE=exit")
	go func() { _, _ = e.Run(context.Background(), args...) }()
	for {
		if b, _ := os.ReadFile(logPath); strings.Contains(string(b), "ready") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// byKind picks out of ended the "status" run (the one that exits on the
// signal), the write and the other read; nil for any that is missing.
func byKind(ended []Ended) (read, write, exited *Ended) {
	for i := range ended {
		switch {
		case ended[i].Args[0] == "status":
			exited = &ended[i]
		case ended[i].Write:
			write = &ended[i]
		default:
			read = &ended[i]
		}
	}
	return read, write, exited
}

// KillAll during a Shutdown (a second ctrl+c or SIGTERM while the TUI is
// already going away) skips the remaining grace: every group is killed at
// once and Shutdown returns with the outcomes, bounded.
func TestKillAllEscalatesShutdown(t *testing.T) {
	self, child, _ := spawnKind(t, true)
	done := make(chan []Ended, 1)
	start := time.Now()
	go func() { done <- Shutdown(syscall.SIGTERM, 10*time.Second, 10*time.Second) }()
	time.Sleep(200 * time.Millisecond)
	KillAll()
	select {
	case ended := <-done:
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("Shutdown did not return promptly after KillAll: %v", d)
		}
		if len(ended) != 1 || ended[0].Outcome != "killed" {
			t.Errorf("outcomes %+v", ended)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Shutdown waited out its grace despite KillAll")
	}
	if !pgidGone(self) || alive(child) {
		t.Error("group survived KillAll")
	}
}

// Escalation is scoped to one Shutdown: a KillAll with nothing live (or
// before a Shutdown) does not make the next Shutdown kill at once, so a
// child that exits on the signal is reported as exited.
func TestKillAllDoesNotEscalateALaterShutdown(t *testing.T) {
	KillAll()
	wd, _ := os.Getwd()
	logPath := filepath.Join(t.TempDir(), "trap.log")
	e := NewExec(filepath.Join(wd, "testdata", "trap-int.sh"), ScopeUser, "")
	e.Env = append(os.Environ(), "TRAP_LOG="+logPath, "TRAP_MODE=exit")
	go func() { _, _ = e.Run(context.Background(), "status", "--json") }()
	for {
		if b, _ := os.ReadFile(logPath); strings.Contains(string(b), "ready") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	ended := Shutdown(os.Interrupt, 2*time.Second, 2*time.Second)
	if len(ended) != 1 || ended[0].Outcome != "exited" {
		t.Fatalf("outcomes %+v, want the child exited on SIGINT", ended)
	}
}

// Under escalation a child that had already exited on the signal is still
// "exited"; only what KillAll (or the deadline) actually killed is "killed".
func TestEscalationLabelsByWhatHappened(t *testing.T) {
	self, child, _ := spawnKind(t, true) // ignores everything but SIGKILL
	wd, _ := os.Getwd()
	logPath := filepath.Join(t.TempDir(), "trap.log")
	e := NewExec(filepath.Join(wd, "testdata", "trap-int.sh"), ScopeUser, "")
	e.Env = append(os.Environ(), "TRAP_LOG="+logPath, "TRAP_MODE=exit")
	go func() { _, _ = e.Run(context.Background(), "check") }()
	for {
		if b, _ := os.ReadFile(logPath); strings.Contains(string(b), "ready") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	done := make(chan []Ended, 1)
	go func() { done <- Shutdown(os.Interrupt, 10*time.Second, 10*time.Second) }()
	time.Sleep(300 * time.Millisecond) // trap-int has exited on SIGINT by now
	KillAll()
	ended := <-done
	got := map[string]string{}
	for _, e := range ended {
		got[e.Args[0]] = e.Outcome
	}
	if got["check"] != "exited" || got["plugin"] != "killed" {
		t.Fatalf("outcomes %v, want check exited and plugin killed", got)
	}
	if !pgidGone(self) || alive(child) {
		t.Error("group survived")
	}
}

// A group that even SIGKILL does not end within killGrace is reported as
// orphaned, after the grace and not before. The stand-in is a group leader
// that is never reaped while Shutdown runs: killed, it stays a zombie, so
// its group never reads as gone (Linux keeps a zombie signalable).
func TestShutdownReportsOrphanedGroup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("relies on Linux zombie semantics")
	}
	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	track(cmd, []string{"mcp", "add"}, true)
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); untrack(cmd) })
	ended := Shutdown(os.Interrupt, 100*time.Millisecond, 100*time.Millisecond)
	if len(ended) != 1 || ended[0].Outcome != "orphaned" || !ended[0].Write || ended[0].Args[0] != "mcp" {
		t.Fatalf("outcomes %+v, want the write orphaned", ended)
	}
	if ended[0].Took < killGrace {
		t.Errorf("orphaned after %v, before the kill grace (%v)", ended[0].Took, killGrace)
	}
}

// A KillAll also ends, at once, a group whose leader has already been
// reaped: agentsync exited on the signal and Run returned after WaitDelay,
// so KillAll's own sweep no longer sees the child, but a helper that ignores
// the signal still holds the group. Shutdown itself must kill it rather
// than wait out the rest of the grace.
func TestKillAllEscalatesGroupOfReapedLeader(t *testing.T) {
	self, child, done := spawn(t, 100*time.Millisecond) // short WaitDelay
	ended := make(chan []Ended, 1)
	start := time.Now()
	go func() { ended <- Shutdown(os.Interrupt, 10*time.Second, 10*time.Second) }()
	select {
	case err := <-done: // the leader exited on SIGINT and Run returned (untracked)
		if err != nil {
			t.Errorf("Run returned %v (an exit is a result, not an error)", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after the leader exited")
	}
	if pgidGone(self) || !alive(child) {
		t.Fatal("precondition: the helper outlives its reaped leader")
	}
	KillAll()
	select {
	case got := <-ended:
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("Shutdown took %v after KillAll", d)
		}
		if len(got) != 1 || got[0].Outcome != "killed" {
			t.Errorf("outcomes %+v, want the group killed", got)
		}
	case <-time.After(9 * time.Second):
		t.Fatal("Shutdown waited out its grace despite KillAll")
	}
	if !pgidGone(self) || alive(child) {
		t.Error("group survived KillAll")
	}
}
