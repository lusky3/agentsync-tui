package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/runner"
)

// ringSize is how many activity entries are kept in memory.
const ringSize = 200

// logRotateBytes is the size at which the activity-log file is rotated
// (renamed to .1, older files shifting up); logKeep is how many rotated
// files are kept. A variable so tests can rotate early.
var logRotateBytes int64 = 1 << 20

const logKeep = 3

// entry is one activity-log record. It deliberately holds ONLY the masked
// command line, the outcome (exit code or error kind) and timing: never
// stdout/stderr, error text, or unmasked argv.
type entry struct {
	Time     time.Time
	Display  string // actions.Action.Display(): already masked
	Outcome  string // "exit N" or an error kind such as "cancelled"
	Duration time.Duration
}

func (e entry) String() string {
	return fmt.Sprintf("%s  %-13s %7s  %s", e.Time.Format("15:04:05"), e.Outcome,
		e.Duration.Round(time.Millisecond), logSafe(e.Display))
}

func (e entry) fileLine() string {
	return strings.Join([]string{e.Time.UTC().Format(time.RFC3339), e.Outcome,
		e.Duration.Round(time.Millisecond).String(), logSafe(e.Display)}, "\t") + "\n"
}

// logSafe makes s safe for one terminal/log line: it sanitises and escapes
// line breaks as a literal \n (it does not fold them away, so a log line
// still shows that the text had one).
func logSafe(s string) string {
	s = mask.Sanitize(s)
	return strings.NewReplacer("\n", `\n`, "\t", " ").Replace(s)
}

// outcome reports how a run ended without including any error text.
func outcome(res runner.Result, err error) string {
	switch {
	case err == nil:
		return fmt.Sprintf("exit %d", res.ExitCode)
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, runner.ErrNotInstalled):
		return "not-installed"
	case errors.Is(err, runner.ErrShuttingDown):
		return "not-started"
	default:
		return "error"
	}
}

// activityLog is an in-memory ring plus an optional append-only file.
type activityLog struct {
	path string

	mu      sync.Mutex
	ring    []entry
	next    int
	full    bool
	fileErr error // first file error, surfaced once
}

func newActivityLog(path string) *activityLog {
	return &activityLog{path: path, ring: make([]entry, ringSize)}
}

// add records e in the ring and, if configured, appends it to the file. It
// returns a file error the first time one occurs (nil afterwards).
func (l *activityLog) add(e entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ring[l.next] = e
	l.next = (l.next + 1) % ringSize
	if l.next == 0 {
		l.full = true
	}
	if l.path == "" || l.fileErr != nil {
		return nil
	}
	if err := appendLine(l.path, e.fileLine()); err != nil {
		l.fileErr = err
		return err
	}
	return nil
}

// entries returns the ring oldest-first.
func (l *activityLog) entries() []entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.full {
		return append([]entry(nil), l.ring[:l.next]...)
	}
	return append(append([]entry(nil), l.ring[l.next:]...), l.ring[:l.next]...)
}

// appendLine appends one line to the activity-log file at path, creating
// the file and its dedicated directory as needed. The directory is the
// TUI's own ($XDG_STATE_HOME/agentsync-tui): it is never a symlink and is
// tightened to 0700 (ownedDir). The file handling is appendFile's.
func appendLine(path, line string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := ownedDir(dir); err != nil {
		return err
	}
	return appendFile(path, "activity log", line)
}

// appendFile appends one line to the log file at path (what names it in
// errors), creating it 0600 if needed. It never follows a symlink at the path (Lstat first, and
// O_NOFOLLOW where the platform has it), tightens a pre-existing file to
// 0600 before writing, and rotates the file once it has reached
// logRotateBytes (path.1 … path.logKeep, newest first). The directory is
// left exactly as it is: a --debug-log file may sit in the user's project
// directory or in /tmp.
func appendFile(path, what, line string) error {
	f, err := openLog(path, what)
	if err != nil {
		return err
	}
	fi, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	if fi.Size() >= logRotateBytes {
		_ = f.Close()
		if err := rotate(path); err != nil {
			return err
		}
		if f, err = openLog(path, what); err != nil {
			return err
		}
	}
	if _, err := f.WriteString(line); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// ownedDir refuses a symlinked log directory and tightens its mode to 0700.
func ownedDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return errors.New("activity log directory is not a directory")
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return os.Chmod(dir, 0o700)
	}
	return nil
}

// openLog opens (creating if needed) the log file for appending, refusing a
// symlink or anything but a regular file, and tightens its mode to 0600.
// what names the file in errors ("activity log", "debug log").
func openLog(path, what string) (*os.File, error) {
	if fi, err := os.Lstat(path); err == nil && (fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular()) {
		return nil, errors.New(what + " is not a regular file")
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|openNoFollow, 0o600)
	if err != nil {
		return nil, err
	}
	fi, err := f.Stat()
	if err == nil && !fi.Mode().IsRegular() {
		err = errors.New(what + " is not a regular file")
	}
	if err == nil && fi.Mode().Perm()&0o077 != 0 {
		err = f.Chmod(0o600)
	}
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}

// rotate shifts path.(logKeep-1) → path.logKeep, …, path → path.1, dropping
// the oldest.
func rotate(path string) error {
	for i := logKeep; i >= 1; i-- {
		from := path
		if i > 1 {
			from = path + "." + strconv.Itoa(i-1)
		}
		to := path + "." + strconv.Itoa(i)
		if err := os.Rename(from, to); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
