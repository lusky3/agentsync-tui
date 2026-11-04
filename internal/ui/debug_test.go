package ui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/runner"
)

// With --debug-log set, a read whose output could not be parsed leaves a
// masked sample (name, exit code, parse error, stdout and stderr heads)
// behind, so parse drift against a newer agentsync can be diagnosed; the
// note itself is unchanged. Without it nothing is written.
func TestUnreadableWritesMaskedDebugSample(t *testing.T) {
	path := filepath.Join(t.TempDir(), "debug", "agentsync-tui-debug.log")
	SetDebugLog("")
	res := runner.Result{Stdout: "weird header\ntoken=CANARYd1 more\n", Stderr: "warn password=CANARYd2", ExitCode: 2}
	if got := Unreadable("status", res, errors.New("status: no agents array")); got != "status output unreadable" {
		t.Fatalf("note %q", got)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("debug log written although not configured")
	}
	SetDebugLog(path)
	t.Cleanup(func() { SetDebugLog("") })
	Unreadable("status", res, errors.New("status: no agents array"))
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{"status", "exit 2", "no agents array", "weird header", "token=••••", "password=••••"} {
		if !strings.Contains(s, want) {
			t.Errorf("sample lacks %q: %q", want, s)
		}
	}
	if strings.Contains(s, "CANARY") {
		t.Errorf("sample leaks: %q", s)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", fi.Mode().Perm())
	}
	// A huge stream is sampled, not copied.
	Unreadable("list", runner.Result{Stdout: strings.Repeat("x", 1<<20)}, errors.New("e"))
	if fi, _ := os.Stat(path); fi.Size() > 16<<10 {
		t.Errorf("sample too large: %d bytes", fi.Size())
	}
}

// While a command runs the status line shows how long it has been running,
// refreshed by a one-second tick that stops with the run.
func TestRunningStatusShowsElapsed(t *testing.T) {
	app := NewApp(baseDeps(newFakeRunner()), newScreen("A", "a"))
	app.Update(DoMsg{Action: actions.PluginUpgradeAll()})
	_, cmd := app.Update(key("y"))
	if cmd == nil || app.running == nil {
		t.Fatal("precondition: write running")
	}
	app.running.start = time.Now().Add(-65 * time.Second)
	_, cmd = app.Update(elapsedTick{})
	if cmd == nil {
		t.Fatal("tick not rescheduled while running")
	}
	if v := content(app); !strings.Contains(v, "(1m5s)") {
		t.Fatalf("no elapsed time in the status line: %q", app.statusLine())
	}
	app.running = nil
	_, cmd = app.Update(elapsedTick{})
	if cmd != nil || strings.Contains(content(app), "(1m") {
		t.Errorf("tick or elapsed survived the run: cmd=%v status %q", cmd != nil, app.statusLine())
	}
}
