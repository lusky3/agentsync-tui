package ui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/lusky3/agentsync-tui/internal/ui/theme"
)

// The scroll keys every list and every long view share (ScrollKeys is the
// footer hint; the help screen and README list them too).
const ScrollKeys = "j/k move • ctrl+d/ctrl+u page (space in long views) • home/end"

// defaultPage is the page size before a list has been rendered.
const defaultPage = 10

// List is the cursor and viewport of a list screen. The cursor is always
// visible: Window moves the viewport by the least amount that keeps it in
// view, and Frame lays header, rows and footer out so the footer is never
// cut. The zero value is a list at its top.
type List struct {
	Cursor int
	offset int // first visible row
	page   int // rows the last Frame/Window showed; the paging step
}

// Key applies a navigation key to a list of n rows and reports whether it
// was one: up/k and down/j move one row, pgup/ctrl+u and pgdown/ctrl+d a
// page, home and end jump.
func (l *List) Key(k string, n int) bool {
	page := l.page
	if page <= 0 {
		page = defaultPage
	}
	switch k {
	case "up", "k":
		l.Cursor--
	case "down", "j":
		l.Cursor++
	case "pgup", "ctrl+u":
		l.Cursor -= page
	case "pgdown", "ctrl+d":
		l.Cursor += page
	case "home":
		l.Cursor = 0
	case "end":
		l.Cursor = n - 1
	default:
		return false
	}
	l.Clamp(n)
	return true
}

// Clamp keeps the cursor inside a list of n rows (0 when it is empty).
func (l *List) Clamp(n int) { l.Cursor = max(min(l.Cursor, n-1), 0) }

// Window returns the rows [start, end) to show when room rows fit, keeping
// the cursor visible and moving the viewport as little as possible.
func (l *List) Window(n, room int) (start, end int) {
	return l.WindowAt(l.Cursor, n, room)
}

// WindowAt is Window for a list whose cursor sits on row anchor (a screen
// that interleaves group headers with its rows maps the cursor to a row).
func (l *List) WindowAt(anchor, n, room int) (start, end int) {
	room = max(room, 1)
	l.page = room
	if n <= room {
		l.offset = 0
		return 0, n
	}
	anchor = max(min(anchor, n-1), 0)
	if anchor < l.offset {
		l.offset = anchor
	} else if anchor >= l.offset+room {
		l.offset = anchor - room + 1
	}
	l.offset = max(min(l.offset, n-room), 0)
	return l.offset, l.offset + room
}

// Frame lays a list screen out in height lines: header, then the window of
// rows around the cursor, then footer. The footer always fits: rows yield
// to it (at least one row stays when there are rows at all).
func (l *List) Frame(header, rows, footer []string, height int) []string {
	return frame(header, rows, footer, height, func(n, room int) (int, int) { return l.Window(n, room) })
}

// FrameAt is Frame with the cursor mapped to row anchor (see WindowAt).
func (l *List) FrameAt(anchor int, header, rows, footer []string, height int) []string {
	return frame(header, rows, footer, height, func(n, room int) (int, int) { return l.WindowAt(anchor, n, room) })
}

// Scroll is the viewport of a long text (command output, a diff): the same
// keys as List, moving the offset instead of a cursor.
type Scroll struct {
	Offset int
	page   int
}

// Key applies a scroll key to a text of n lines and reports whether it was
// one: up/k and down/j one line, pgup/ctrl+u and pgdown/ctrl+d (or space) a
// page, home and end jump. The offset is clamped by the next Window.
func (s *Scroll) Key(k string, n int) bool {
	page := s.page
	if page <= 0 {
		page = defaultPage
	}
	switch k {
	case "up", "k":
		s.Offset--
	case "down", "j":
		s.Offset++
	case "pgup", "ctrl+u":
		s.Offset -= page
	case "pgdown", "ctrl+d", "space":
		s.Offset += page
	case "home":
		s.Offset = 0
	case "end":
		s.Offset = n
	default:
		return false
	}
	s.Offset = max(min(s.Offset, max(n-page, 0)), 0)
	return true
}

// Window returns the lines [start, end) to show when room lines fit,
// clamping the offset so the text never scrolls past its end.
func (s *Scroll) Window(n, room int) (start, end int) {
	room = max(room, 1)
	s.page = room
	s.Offset = max(min(s.Offset, n-room), 0)
	return s.Offset, min(s.Offset+room, n)
}

// Frame lays a long view out in height lines: header, the visible window
// of body, footer; the footer always fits.
func (s *Scroll) Frame(header, body, footer []string, height int) []string {
	return frame(header, body, footer, height, s.Window)
}

// MaxOffset is the largest offset that still fills the last window shown.
func (s *Scroll) MaxOffset(n int) int { return max(n-max(s.page, 1), 0) }

// Clamp keeps the offset within a text of n lines (the next Window does the
// same; Clamp is for state that is inspected before a render).
func (s *Scroll) Clamp(n int) { s.Offset = max(min(s.Offset, s.MaxOffset(n)), 0) }

func frame(header, rows, footer []string, height int, window func(n, room int) (int, int)) []string {
	room := height - len(header) - len(footer)
	if len(rows) > 0 {
		room = max(room, 1)
	}
	out := make([]string, 0, len(header)+max(room, 0)+len(footer))
	out = append(out, header...)
	if room > 0 {
		start, end := window(len(rows), room)
		out = append(out, rows[start:end]...)
	}
	return append(out, footer...)
}

// KeyLines renders a footer key hint (items joined by " • ") in dim style,
// wrapped at the separators so every line fits width and no item, the
// scroll keys last of all, is cut off by the shell's line truncation. A
// width too small for one item still yields that item on its own line.
func KeyLines(hint string, width int) []string {
	const sep = " • "
	var lines []string
	cur := ""
	for _, item := range strings.Split(hint, sep) {
		switch {
		case cur == "":
			cur = item
		case lipgloss.Width(cur+sep+item) <= width:
			cur += sep + item
		default:
			lines = append(lines, cur)
			cur = item
		}
	}
	lines = append(lines, cur)
	for i, l := range lines {
		lines[i] = theme.Dim.Render(l)
	}
	return lines
}
