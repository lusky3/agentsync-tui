package ui

import (
	"strconv"
	"strings"

	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/ui/theme"
)

// MaxOutput caps the stored (masked) output of one command, in bytes.
const MaxOutput = 64 << 10

// TruncatedMarker is the line shown after capped output.
const TruncatedMarker = "output truncated"

// Output is one run's stored result. Text is masked (mask.Text), sanitised
// and capped at MaxOutput by mask.Capped, in that order, so nothing raw is
// ever held and a cut cannot leave the prefix of a secret behind.
type Output struct {
	Title     string // sanitised action title
	Text      string
	Truncated bool
	Note      string // masked error or "exit N"; "" on success
}

// OutputOf stores a finished run: stdout, plus stderr when the run failed
// (an error or a non-zero exit).
func OutputOf(m RunMsg) Output {
	o := Output{Title: mask.Sanitize(m.Action.Title)}
	failed := m.Err != nil || m.Result.ExitCode != 0
	o.Text, o.Truncated = CappedStreams(m.Result.Stdout, m.Result.Stderr, failed)
	switch {
	case m.Err != nil:
		o.Note = mask.FirstLine(m.Err.Error())
	case m.Result.ExitCode != 0:
		o.Note = "exit " + strconv.Itoa(m.Result.ExitCode)
	}
	return o
}

// CappedStreams joins stdout and (when withStderr) stderr, masks the whole
// and caps it at MaxOutput.
func CappedStreams(stdout, stderr string, withStderr bool) (string, bool) {
	text := stdout
	if withStderr && strings.TrimSpace(stderr) != "" {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += stderr
	}
	return mask.Capped(text, MaxOutput)
}

// Lines renders stored output one string per line, tabs widened, with the
// truncation marker last when the text was capped.
func (o Output) Lines() []string { return TextLines(o.Text, o.Truncated) }

// TextLines splits stored (already masked) text into display lines: tabs
// become two spaces and a trailing newline adds no empty line; truncated
// appends TruncatedMarker.
func TextLines(text string, truncated bool) []string {
	var ls []string
	if text != "" {
		for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
			ls = append(ls, strings.ReplaceAll(l, "\t", "  "))
		}
	}
	if truncated {
		ls = append(ls, theme.Warn.Render(TruncatedMarker))
	}
	return ls
}
