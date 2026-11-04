package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/runner"
)

// ringSize is how many activity entries are kept in memory.
const ringSize = 200

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
		e.Duration.Round(time.Millisecond), oneLine(e.Display))
}

func (e entry) fileLine() string {
	return strings.Join([]string{e.Time.UTC().Format(time.RFC3339), e.Outcome,
		e.Duration.Round(time.Millisecond).String(), oneLine(e.Display)}, "\t") + "\n"
}

// oneLine makes s safe for one terminal/log line.
func oneLine(s string) string {
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

func appendLine(path, line string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(line); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
