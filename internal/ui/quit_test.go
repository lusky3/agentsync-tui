// Quitting and ctrl+c while idle, while a read runs and while a write runs.
package ui

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/runner"
)

func TestQuitKey(t *testing.T) {
	h := start(t, baseDeps(newFakeRunner()), newScreen("A", "body-a"))
	h.tm.Send(key("q"))
	h.tm.WaitFinished(t, teatest.WithFinalTimeout(timeout))
}

func TestCtrlCCancelsRunningAction(t *testing.T) {
	fr := newFakeRunner()
	h := start(t, baseDeps(fr), newScreen("A", "a"))
	h.tm.Send(DoMsg{Action: actions.Check()})
	h.waitStarted()
	h.tm.Send(key("ctrl+c"))
	h.waitRun() // the program is still alive: ctrl+c did not quit
	app := h.final()
	if !strings.Contains(content(app), "cancelled") {
		t.Errorf("view: %q", content(app))
	}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if len(fr.ctxErrs) != 1 || !errors.Is(fr.ctxErrs[0], context.Canceled) {
		t.Errorf("ctx errs %v", fr.ctxErrs)
	}
}

func TestCtrlCQuitsWhenIdle(t *testing.T) {
	h := start(t, baseDeps(newFakeRunner()), newScreen("A", "a"))
	h.tm.Send(key("ctrl+c"))
	h.tm.WaitFinished(t, teatest.WithFinalTimeout(timeout))
}

func TestQuitWhileRunningCancelsAndLogsFirst(t *testing.T) {
	fr := newFakeRunner()
	d := baseDeps(fr)
	d.LogPath = filepath.Join(t.TempDir(), "activity.log")
	h := start(t, d, newScreen("A", "a"))
	h.tm.Send(DoMsg{Action: actions.Check()})
	h.waitStarted()
	h.tm.Send(key("q"))
	h.tm.WaitFinished(t, teatest.WithFinalTimeout(timeout))
	app := h.tm.FinalModel(t).(probe).App
	es := app.log.entries()
	if len(es) != 1 || es[0].Outcome != "cancelled" {
		t.Fatalf("log entries %+v, want one cancelled entry", es)
	}
	fr.mu.Lock()
	if len(fr.ctxErrs) != 1 || !errors.Is(fr.ctxErrs[0], context.Canceled) {
		t.Errorf("run context not cancelled: %v", fr.ctxErrs)
	}
	fr.mu.Unlock()
	b, err := os.ReadFile(d.LogPath)
	if err != nil || !bytes.Contains(b, []byte("cancelled")) {
		t.Errorf("log file %q err %v", b, err)
	}
}

func TestQuitWhileRunningFallsBackWhenRunHangs(t *testing.T) {
	app := NewApp(baseDeps(newFakeRunner()), newScreen("A", "a"))
	app.Update(DoMsg{Action: actions.Check()}) // starts; the cmd is never executed
	_, cmd := app.Update(key("q"))
	if cmd == nil || app.running == nil {
		t.Fatal("q while running should wait for the cancelled run")
	}
	_, cmd = app.Update(quitDeadlineMsg{})
	es := app.log.entries()
	if len(es) != 1 || es[0].Outcome != "cancelled" {
		t.Fatalf("log entries %+v", es)
	}
	if cmd == nil {
		t.Fatal("fallback did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("fallback cmd is not tea.Quit")
	}
}

// ---- modal height (whole-branch review I-2) --------------------------------

// A single ctrl+c must not stop a running write; a second one interrupts it.
func TestCtrlCOnWriteNeedsConfirmation(t *testing.T) {
	fr := newFakeRunner()
	h := start(t, baseDeps(fr), newScreen("A", "a"))
	startWrite(t, h)
	if o := h.pressAndWait(key("ctrl+c")); !o.running {
		t.Fatal("first ctrl+c ended the write")
	}
	if err := fr.ctxErr(0); err != nil {
		t.Fatalf("first ctrl+c cancelled the write: %v", err)
	}
	h.pressAndWait(key("ctrl+c"))
	if err := fr.ctxErr(0); !errors.Is(err, context.Canceled) {
		t.Fatalf("second ctrl+c did not interrupt the write: %v", err)
	}
	h.waitRun() // still alive: interrupting a write does not quit
	app := h.final()
	if v := content(app); !strings.Contains(v, "interrupted: ") {
		t.Errorf("view: %q", v)
	}
	if es := app.log.entries(); len(es) != 1 || es[0].Outcome != "interrupted" {
		t.Errorf("log %+v", es)
	}
}

// The confirmation expires: a ctrl+c long after the first one re-arms
// instead of interrupting. Reads are still cancelled by a single ctrl+c
// (TestCtrlCCancelsRunningAction).
func TestCtrlCOnWriteConfirmationExpires(t *testing.T) {
	app := NewApp(baseDeps(newFakeRunner()), newScreen("A", "a"))
	app.Update(DoMsg{Action: actions.PluginUpgradeAll()})
	app.Update(key("y")) // starts; the run cmd is never executed
	if app.running == nil {
		t.Fatal("precondition: write running")
	}
	app.Update(key("ctrl+c"))
	if app.running.interrupted || !strings.Contains(content(app), msgWriteRunning) {
		t.Fatalf("first ctrl+c: interrupted=%v view %q", app.running.interrupted, content(app))
	}
	app.running.armed = time.Now().Add(-interruptConfirm - time.Second)
	app.Update(key("ctrl+c"))
	if app.running.interrupted {
		t.Fatal("an expired confirmation interrupted the write")
	}
	app.Update(key("ctrl+c"))
	if !app.running.interrupted || !strings.Contains(content(app), "interrupting: ") {
		t.Fatalf("a fresh second ctrl+c did not interrupt: view %q", content(app))
	}
}

// q during a write waits for it to finish, then logs it and exits.
func TestQuitDuringWriteWaitsForIt(t *testing.T) {
	fr := newFakeRunner()
	d := baseDeps(fr)
	d.LogPath = filepath.Join(t.TempDir(), "activity.log")
	h := start(t, d, newScreen("A", "a"))
	startWrite(t, h)
	if o := h.pressAndWait(key("q")); !o.running {
		t.Fatal("q ended the write")
	}
	if err := fr.ctxErr(0); err != nil {
		t.Fatalf("q cancelled the write: %v", err)
	}
	fr.respond <- fakeResp{}
	h.tm.WaitFinished(t, teatest.WithFinalTimeout(timeout))
	app := h.tm.FinalModel(t).(probe).App
	if es := app.log.entries(); len(es) != 1 || es[0].Outcome != "exit 0" {
		t.Fatalf("log %+v, want the finished write", es)
	}
	if b, err := os.ReadFile(d.LogPath); err != nil || !bytes.Contains(b, []byte("exit 0")) {
		t.Errorf("log file %q err %v", b, err)
	}
}

// After q, one ctrl+c interrupts the write; the exit waits for it to report
// and logs it.
func TestQuitDuringWriteThenCtrlCInterrupts(t *testing.T) {
	fr := newFakeRunner()
	d := baseDeps(fr)
	d.LogPath = filepath.Join(t.TempDir(), "activity.log")
	h := start(t, d, newScreen("A", "a"))
	startWrite(t, h)
	h.pressAndWait(key("q"))
	h.tm.Send(key("ctrl+c"))
	h.tm.WaitFinished(t, teatest.WithFinalTimeout(timeout))
	app := h.tm.FinalModel(t).(probe).App
	if err := fr.ctxErr(0); !errors.Is(err, context.Canceled) {
		t.Errorf("write not interrupted: %v", err)
	}
	if es := app.log.entries(); len(es) != 1 || es[0].Outcome != "interrupted" {
		t.Fatalf("log %+v", es)
	}
	if b, err := os.ReadFile(d.LogPath); err != nil || !bytes.Contains(b, []byte("interrupted")) {
		t.Errorf("log file %q err %v", b, err)
	}
}

// q during a write schedules no deadline (the write is waited for); after an
// interrupt the exit is bounded by quitGrace, which outlasts the runner's
// SIGINT-to-SIGKILL grace.
func TestQuitDuringWriteFallsBackAfterInterrupt(t *testing.T) {
	if quitGrace <= runner.InterruptGrace {
		t.Fatalf("quitGrace %v must exceed runner.InterruptGrace %v", quitGrace, runner.InterruptGrace)
	}
	app := NewApp(baseDeps(newFakeRunner()), newScreen("A", "a"))
	app.Update(DoMsg{Action: actions.PluginUpgradeAll()})
	app.Update(key("y"))
	if _, cmd := app.Update(key("q")); cmd != nil || app.running == nil {
		t.Fatal("q during a write must neither stop it nor schedule a quit deadline")
	}
	_, cmd := app.Update(key("ctrl+c"))
	if cmd == nil || !app.running.interrupted {
		t.Fatal("ctrl+c after q must interrupt and schedule the quit deadline")
	}
	_, cmd = app.Update(quitDeadlineMsg{})
	if es := app.log.entries(); len(es) != 1 || es[0].Outcome != "interrupted" {
		t.Fatalf("log entries %+v", es)
	}
	if cmd == nil {
		t.Fatal("fallback did not quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("fallback cmd is not tea.Quit")
	}
}

// ---- external quit (SIGTERM, SIGHUP, InterruptMsg) --------------------------

// Bubble Tea turns SIGTERM into a QuitMsg (and SIGINT off a TTY into an
// InterruptMsg) that the model never sees; main installs Filter so a run in
// flight is logged as abandoned, and the reads are cancelled, before the
// program exits.
func TestExternalQuitLogsAbandonedRun(t *testing.T) {
	for _, m := range []tea.Msg{tea.QuitMsg{}, tea.InterruptMsg{}} {
		d := baseDeps(newFakeRunner())
		d.LogPath = filepath.Join(t.TempDir(), "activity.log")
		app := NewApp(d, newScreen("A", "a"))
		app.Update(DoMsg{Action: actions.PluginUpgradeAll()})
		app.Update(key("y")) // the write is running; its cmd is never executed
		if app.running == nil {
			t.Fatal("precondition: write running")
		}
		if got := app.Filter(app, m); got != m {
			t.Errorf("Filter changed %T into %T", m, got)
		}
		if es := app.log.entries(); len(es) != 1 || es[0].Outcome != "abandoned" {
			t.Fatalf("%T: log %+v, want one abandoned entry", m, es)
		}
		if b, err := os.ReadFile(d.LogPath); err != nil || !bytes.Contains(b, []byte("abandoned")) {
			t.Errorf("%T: log file %q err %v", m, b, err)
		}
		if app.root.Err() == nil {
			t.Errorf("%T: background reads not cancelled", m)
		}
	}
	// Idle: nothing to log, the message passes through.
	app := NewApp(baseDeps(newFakeRunner()), newScreen("A", "a"))
	app.Filter(app, tea.QuitMsg{})
	if es := app.log.entries(); len(es) != 0 {
		t.Errorf("idle quit logged %+v", es)
	}
}

// q after a double ctrl+c (the write is already interrupted) must not offer
// ctrl+c again and must bound the exit with the quit deadline.
func TestQuitAfterInterruptedWriteSchedulesDeadline(t *testing.T) {
	app := NewApp(baseDeps(newFakeRunner()), newScreen("A", "a"))
	app.Update(DoMsg{Action: actions.PluginUpgradeAll()})
	app.Update(key("y"))
	app.Update(key("ctrl+c"))
	app.Update(key("ctrl+c"))
	if app.running == nil || !app.running.interrupted {
		t.Fatal("precondition: write interrupted")
	}
	_, cmd := app.Update(key("q"))
	if cmd == nil {
		t.Fatal("q after an interrupt scheduled no quit deadline")
	}
	if v := content(app); strings.Contains(v, "press ctrl+c") || !strings.Contains(v, "interrupted") {
		t.Errorf("status still offers ctrl+c: %q", app.status)
	}
	_, cmd = app.Update(quitDeadlineMsg{})
	if es := app.log.entries(); len(es) != 1 || es[0].Outcome != "interrupted" {
		t.Fatalf("log entries %+v", es)
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("fallback cmd is not tea.Quit")
	}
}
