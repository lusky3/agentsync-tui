package ui

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lusky3/agentsync-tui/internal/runner"
)

func logEntry(i int) entry {
	return entry{Time: time.Unix(0, 0), Display: "agentsync check " + strings.Repeat("x", 40), Outcome: "exit 0", Duration: time.Duration(i)}
}

// A pre-existing world-readable log directory and file are tightened to
// 0700 and 0600 before anything is appended.
func TestActivityLogEnforcesModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(dir, "activity.log")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	l := newActivityLog(path)
	if err := l.add(logEntry(1)); err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v, want 0700", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v, want 0600", fi.Mode().Perm())
	}
	if b, _ := os.ReadFile(path); !strings.HasPrefix(string(b), "old\n") || !strings.Contains(string(b), "exit 0") {
		t.Errorf("content %q", b)
	}
}

// A symlink at the log path (or as the log directory) is never followed:
// the add fails and the link target stays untouched.
func TestActivityLogRefusesSymlinks(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.log")
	if err := os.WriteFile(target, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "state")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "activity.log")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	if err := newActivityLog(link).add(logEntry(1)); err == nil {
		t.Error("add through a symlinked file succeeded")
	}
	if b, _ := os.ReadFile(target); len(b) != 0 {
		t.Errorf("symlink target was written: %q", b)
	}
	// directory symlink
	realDir := filepath.Join(root, "real")
	if err := os.MkdirAll(realDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dirLink := filepath.Join(root, "linkdir")
	if err := os.Symlink(realDir, dirLink); err != nil {
		t.Fatal(err)
	}
	if err := newActivityLog(filepath.Join(dirLink, "activity.log")).add(logEntry(1)); err == nil {
		t.Error("add through a symlinked directory succeeded")
	}
	if es, _ := os.ReadDir(realDir); len(es) != 0 {
		t.Errorf("symlinked dir was written: %v", es)
	}
}

// The file is rotated at logRotateBytes: the current file is renamed to .1
// (older ones shift up), at most logKeep old files are kept, and every line
// written lands in exactly one of them.
func TestActivityLogRotates(t *testing.T) {
	old := logRotateBytes
	logRotateBytes = 300
	t.Cleanup(func() { logRotateBytes = old })
	path := filepath.Join(t.TempDir(), "state", "activity.log")
	l := newActivityLog(path)
	const n = 40
	for i := 0; i < n; i++ {
		if err := l.add(logEntry(i)); err != nil {
			t.Fatal(err)
		}
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Size() > logRotateBytes+int64(len(logEntry(0).fileLine())) {
		t.Fatalf("current log not rotated: %v size %d", err, fi.Size())
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("no .1 file: %v", err)
	}
	if _, err := os.Stat(path + "." + strconv.Itoa(logKeep+1)); err == nil {
		t.Errorf("more than %d old files kept", logKeep)
	}
	total := rotatedLines(t, path)
	if total == 0 || total > n {
		t.Errorf("%d lines across the files, want >0 and <= %d", total, n)
	}
	// the newest lines are in the current file
	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), logEntry(n-1).fileLine()) {
		t.Errorf("last entry missing from the current file: %q", b)
	}
}

// rotatedLines counts the lines in path and its kept rotations (.1 ...
// .logKeep), checking that each file that exists is private (0600).
func rotatedLines(t *testing.T, path string) int {
	t.Helper()
	total := 0
	for i := 0; i <= logKeep; i++ {
		p := path
		if i > 0 {
			p += "." + strconv.Itoa(i)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		if fi, _ := os.Stat(p); fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v", p, fi.Mode().Perm())
		}
		total += strings.Count(string(b), "\n")
	}
	return total
}

// What became of each child at shutdown is logged like any run: masked
// command line, the outcome (exited / killed / orphaned), how long it took.
func TestRecordShutdownLogsOutcomes(t *testing.T) {
	d := baseDeps(newFakeRunner())
	d.LogPath = filepath.Join(t.TempDir(), "activity.log")
	app := NewApp(d, newScreen("A", "a"))
	app.RecordShutdown([]runner.Ended{
		{Args: []string{"status", "--json"}, Outcome: "exited", Took: 120 * time.Millisecond},
		{Args: []string{"mcp", "add", "x", "--header", "Authorization: Bearer CANARYsd1"}, Write: true, Outcome: "killed", Took: 10 * time.Second},
	})
	es := app.log.entries()
	if len(es) != 2 || es[0].Outcome != "exited" || es[1].Outcome != "killed" {
		t.Fatalf("entries %+v", es)
	}
	b, err := os.ReadFile(d.LogPath)
	if err != nil || !strings.Contains(string(b), "killed") || !strings.Contains(string(b), "agentsync mcp add x") {
		t.Errorf("file %q err %v", b, err)
	}
	if strings.Contains(string(b), "CANARY") || strings.Contains(es[1].Display, "CANARY") {
		t.Errorf("unmasked argv logged: %q", b)
	}
}
