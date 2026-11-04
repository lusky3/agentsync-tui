package library

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/ui"
	"github.com/lusky3/agentsync-tui/internal/ui/theme"
)

const (
	listKeys = "E/enter edit in $EDITOR • r refresh • esc close result • " + ui.ScrollKeys
	applyTip = "preview the effect with the Apply screen (p)"
)

// View implements tea.Model. Names are validated identifiers and every other
// string was masked and sanitised when it was stored; it is sanitised again
// here. The item rows are windowed around the cursor; the result pane and
// the key hints form the footer, which is never cut.
func (s *Screen) View() tea.View {
	info := infos[s.kind]
	var header, rows []string
	switch {
	case !s.loaded:
		header = append(header, theme.Dim.Render("loading…"))
	case len(s.items) == 0:
		header = append(header, "no "+info.plural+" found — create files under "+info.dir+"/ or capture them with `import`")
	default:
		header = append(header, theme.Bold.Render(info.title+" ("+strconv.Itoa(len(s.items))+")"))
		rows = s.rows()
	}
	var footer []string
	if s.loadNote != "" {
		footer = append(footer, theme.Dim.Render(info.word+" list unavailable: "+mask.Sanitize(s.loadNote)))
	}
	if s.note != "" {
		footer = append(footer, theme.Warn.Render(mask.Sanitize(s.note)))
	}
	if s.check != nil {
		footer = append(footer, s.resultPane()...)
	}
	footer = append(footer, theme.Dim.Render(listKeys))
	lines := s.list.Frame(header, rows, footer, s.height)
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, s.width, "…")
	}
	return tea.NewView(strings.Join(lines, "\n"))
}

// rows renders every item; View windows them around the cursor.
func (s *Screen) rows() []string {
	out := make([]string, 0, len(s.items))
	for i, it := range s.items {
		line := it.Name
		if it.Detail != "" {
			line += "  " + theme.Dim.Render(mask.OneLine(mask.Text(it.Detail)))
		}
		if i == s.list.Cursor {
			out = append(out, theme.Sel.Render("> "+ansi.Strip(line)))
		} else {
			out = append(out, "  "+line)
		}
	}
	return out
}

func (s *Screen) resultPane() []string {
	c := s.check
	head := "check " + c.state.String()
	if c.state != checkNotRun {
		head += " (exit " + strconv.Itoa(c.exit) + ")"
	}
	style := theme.OK
	if c.state != checkPassed {
		style = theme.Bad
	}
	out := []string{"", style.Render(head)}
	start, end := c.scroll.Window(len(c.lines), s.paneHeight())
	for _, l := range c.lines[start:end] {
		out = append(out, "  "+mask.OneLine(l))
	}
	if c.trunc {
		out = append(out, theme.Warn.Render("  "+ui.TruncatedMarker))
	}
	if len(c.lines) > s.paneHeight() {
		out = append(out, theme.Dim.Render("  "+ui.ScrollKeys+" • "+strconv.Itoa(start+1)+"/"+strconv.Itoa(len(c.lines))))
	}
	return append(out, theme.Dim.Render(applyTip))
}
