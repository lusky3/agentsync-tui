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
	listKeys = "E/enter edit in $EDITOR • j/k move • r refresh • esc close result"
	applyTip = "preview the effect with the Apply screen (p)"
)

// View implements tea.Model. Names are validated identifiers and every other
// string was masked and sanitised when it was stored; it is sanitised again
// here.
func (s *Screen) View() tea.View {
	info := infos[s.kind]
	var out []string
	switch {
	case !s.loaded:
		out = append(out, theme.Dim.Render("loading…"))
	case len(s.items) == 0:
		out = append(out, "no "+info.plural+" found — create files under "+info.dir+"/ or capture them with `import`")
	default:
		out = append(out, theme.Bold.Render(info.title+" ("+strconv.Itoa(len(s.items))+")"))
		out = append(out, s.rows()...)
	}
	if s.loadNote != "" {
		out = append(out, theme.Dim.Render(info.word+" list unavailable: "+mask.Sanitize(s.loadNote)))
	}
	if s.note != "" {
		out = append(out, theme.Warn.Render(mask.Sanitize(s.note)))
	}
	if s.check != nil {
		out = append(out, s.resultPane()...)
	}
	out = append(out, theme.Dim.Render(listKeys))
	lines := strings.Split(strings.Join(out, "\n"), "\n")
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, s.width, "…")
	}
	return tea.NewView(strings.Join(lines, "\n"))
}

// rows renders the item list, windowed around the cursor.
func (s *Screen) rows() []string {
	room := max(s.height-8, 3)
	if s.check != nil {
		room = max(room-s.paneHeight()-3, 3)
	}
	start := max(min(s.cursor-room/2, len(s.items)-room), 0)
	end := min(start+room, len(s.items))
	var out []string
	for i := start; i < end; i++ {
		it := s.items[i]
		line := it.Name
		if it.Detail != "" {
			line += "  " + theme.Dim.Render(mask.OneLine(mask.Text(it.Detail)))
		}
		if i == s.cursor {
			out = append(out, theme.Sel.Render("> "+ansi.Strip(line)))
		} else {
			out = append(out, "  "+line)
		}
	}
	return out
}

func (s *Screen) resultPane() []string {
	c := s.check
	head := "check " + c.state
	if c.state != "could not run" {
		head += " (exit " + strconv.Itoa(c.exit) + ")"
	}
	style := theme.OK
	if c.state != "passed" {
		style = theme.Bad
	}
	out := []string{"", style.Render(head)}
	end := min(c.scroll+s.paneHeight(), len(c.lines))
	for _, l := range c.lines[min(c.scroll, end):end] {
		out = append(out, "  "+mask.OneLine(l))
	}
	if c.trunc {
		out = append(out, theme.Warn.Render("  "+ui.TruncatedMarker))
	}
	if len(c.lines) > s.paneHeight() {
		out = append(out, theme.Dim.Render("  ctrl+d/ctrl+u/pgdn/pgup scroll • "+strconv.Itoa(c.scroll+1)+"/"+strconv.Itoa(len(c.lines))))
	}
	return append(out, theme.Dim.Render(applyTip))
}
