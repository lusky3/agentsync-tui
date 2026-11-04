package ui

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/runner"
)

// debugSample is how much of each stream a debug record keeps, after
// masking.
const debugSample = 4 << 10

// debugLog is the --debug-log file: "" (the default) records nothing.
var debugLog struct {
	mu   sync.Mutex
	path string
}

// SetDebugLog names the file Unreadable appends to (--debug-log); "" turns
// the samples off. The file gets the activity log's handling: 0600, no
// symlinks, rotation.
func SetDebugLog(path string) {
	debugLog.mu.Lock()
	defer debugLog.mu.Unlock()
	debugLog.path = path
}

// Unreadable is the status note for a read whose output could not be
// parsed ("<name> output unreadable"): agentsync is pre-1.0 and its text
// output is not a stable interface, so parse drift is the likely field
// failure, and the note alone cannot be diagnosed. When --debug-log is set
// a record is appended: the time, name, exit code, the parse error, and
// the first 4 KiB of stdout and of stderr, masked (mask.Capped) with line
// breaks escaped. Raw output is never written anywhere.
func Unreadable(name string, res runner.Result, err error) string {
	note := name + " output unreadable"
	debugLog.mu.Lock()
	defer debugLog.mu.Unlock()
	if debugLog.path == "" {
		return note
	}
	reason := ""
	if err != nil {
		reason = mask.FirstLine(err.Error())
	}
	rec := []string{
		time.Now().UTC().Format(time.RFC3339), logSafe(name), "exit " + strconv.Itoa(res.ExitCode), reason,
		"stdout: " + debugHead(res.Stdout), "stderr: " + debugHead(res.Stderr),
	}
	_ = appendLine(debugLog.path, strings.Join(rec, "\t")+"\n") // best effort; the note is the user-facing part
	return note
}

// debugHead is the masked, one-line head of a stream. The raw stream is
// cut to a window of 16 samples first, so masking a 4 MiB stream does not
// stall the read; the window is then masked before the 4 KiB cut, so a
// secret straddling that cut leaves no prefix (anything cut at the window
// edge lies far beyond the sample and is dropped whole).
func debugHead(s string) string {
	trunc := false
	if len(s) > 16*debugSample {
		s, trunc = s[:16*debugSample], true
	}
	head, t := mask.Capped(s, debugSample)
	head = logSafe(head)
	if trunc || t {
		head += " [truncated]"
	}
	return head
}
