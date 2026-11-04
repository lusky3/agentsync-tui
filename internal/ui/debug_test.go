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
	path := filepath.Join(t.TempDir(), "agentsync-tui-debug.log")
	_ = SetDebugLog("")
	res := runner.Result{Stdout: "weird header\ntoken=CANARYd1 more\n", Stderr: "warn password=CANARYd2", ExitCode: 2}
	if got := Unreadable("status", res, errors.New("status: no agents array")); got != "status output unreadable" {
		t.Fatalf("note %q", got)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("debug log written although not configured")
	}
	if err := SetDebugLog(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = SetDebugLog("") })
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

// --debug-log names a file of the user's choosing: its directory is never
// created or chmod'ed (a 0755 project directory or a sticky /tmp stays as
// it is); the file is created 0600 and a symlink in its place is refused.
func TestDebugLogLeavesDirectoryAlone(t *testing.T) {
	for _, mode := range []os.FileMode{0o755, 0o1777} {
		dir := filepath.Join(t.TempDir(), "proj")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, mode); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "tui-debug.log")
		if err := SetDebugLog(path); err != nil {
			t.Fatalf("mode %o: %v", mode, err)
		}
		t.Cleanup(func() { _ = SetDebugLog("") })
		Unreadable("status", runner.Result{Stdout: "x"}, errors.New("e"))
		fi, err := os.Stat(dir)
		if err != nil || fi.Mode().Perm() != mode.Perm() || fi.Mode()&os.ModeSticky != mode&os.ModeSticky {
			t.Errorf("directory mode changed from %o to %v (%v)", mode, fi.Mode(), err)
		}
		fi, err = os.Stat(path)
		if err != nil || fi.Mode().Perm() != 0o600 || fi.Size() == 0 {
			t.Errorf("file: %v mode %v size %d", err, fi.Mode(), fi.Size())
		}
	}
}

// An unusable --debug-log path is an error at startup, not a silent no-op.
func TestSetDebugLogFailsLoudly(t *testing.T) {
	root := t.TempDir()
	if err := SetDebugLog(filepath.Join(root, "missing", "x.log")); err == nil {
		t.Error("missing directory accepted")
	}
	target := filepath.Join(root, "target.log")
	_ = os.WriteFile(target, nil, 0o600)
	link := filepath.Join(root, "link.log")
	if err := os.Symlink(target, link); err == nil {
		if err := SetDebugLog(link); err == nil {
			t.Error("symlinked file accepted")
		}
		if b, _ := os.ReadFile(target); len(b) != 0 {
			t.Errorf("symlink target written: %q", b)
		}
	}
	if err := SetDebugLog(root); err == nil {
		t.Error("a directory accepted as the log file")
	}
	if err := SetDebugLog(""); err != nil {
		t.Errorf("clearing: %v", err)
	}
}

// Debug samples are masked structurally as well as textually: a literal
// under env/headers/args in JSON-looking output, and a value after a
// secret-named word with only whitespace between, are masked.
func TestDebugSamplesMaskedStructurally(t *testing.T) {
	path := filepath.Join(t.TempDir(), "d.log")
	if err := SetDebugLog(path); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = SetDebugLog("") })
	Unreadable("status", runner.Result{
		Stdout: `{"env":{"X":"CANd1"},"headers":{"H":"CANd2"},"args":["--token","CANd3"],"other":"ok"}`,
		Stderr: "password CANd4 rejected\ntoken: CANd5",
	}, nil)
	b, _ := os.ReadFile(path)
	s := string(b)
	if strings.Contains(s, "CANd") {
		t.Fatalf("sample leaks: %q", s)
	}
	for _, want := range []string{`"other": "ok"`, "password ••••", "rejected"} {
		if !strings.Contains(s, want) {
			t.Errorf("sample lacks %q: %q", want, s)
		}
	}
}
