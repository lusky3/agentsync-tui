package servers

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/model"
	"github.com/lusky3/agentsync-tui/internal/source"
	"github.com/lusky3/agentsync-tui/internal/ui"
	"github.com/lusky3/agentsync-tui/internal/ui/theme"
)

const listKeys = "enter detail • e enable/disable • x remove • a add • E edit • r refresh • " + ui.ScrollKeys

// View implements tea.Model. Every outside string is sanitised or masked.
func (s *Screen) View() tea.View {
	var lines []string
	switch {
	case s.form != nil:
		lines = strings.Split(s.form.view(), "\n")
	case s.detail:
		lines = s.detailView()
	default:
		lines = s.listView()
	}
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, s.width, "…")
	}
	return tea.NewView(strings.Join(lines, "\n"))
}

func (s *Screen) footer() []string {
	var out []string
	if s.loaded && !s.statusOK && s.stNote != "" {
		out = append(out, theme.Dim.Render("agent state unavailable: "+s.stNote))
	}
	if s.note != "" {
		out = append(out, theme.Warn.Render(s.note))
	}
	return append(out, theme.Dim.Render(listKeys))
}

// listView lays the table out as header (warnings, column titles), the
// rows windowed around the cursor, and a footer that is never cut.
func (s *Screen) listView() []string {
	var header []string
	if !s.loaded {
		return s.list.Frame([]string{theme.Dim.Render("loading…")}, nil, s.footer(), s.height)
	}
	if s.loadWarn != "" {
		header = append(header, theme.Warn.Render(s.loadWarn))
	}
	if len(s.servers) == 0 {
		header = append(header, "no MCP servers — press a to add one")
		if s.dirGone {
			header = append(header, theme.Dim.Render("(mcp/ directory not found)"))
		}
		return s.list.Frame(header, nil, s.footer(), s.height)
	}
	head := []string{"ID", "TYPE", "TARGET", "ON", "AGENTS"}
	cells := make([][]string, len(s.servers))
	for i, srv := range s.servers {
		cells[i] = []string{mask.OneLine(srv.ID), mask.OneLine(srv.Type), target(srv), onOff(srv), allowlist(srv)}
	}
	caps := []int{28, 5, 48, 3, 20}
	widths := make([]int, len(head))
	for c := range head {
		widths[c] = ansi.StringWidth(head[c])
		for _, r := range cells {
			widths[c] = max(widths[c], min(ansi.StringWidth(r[c]), caps[c]))
		}
	}
	// The target column yields room so the state column stays visible.
	stateW := 5
	for _, srv := range s.servers {
		stateW = max(stateW, min(ansi.StringWidth(s.stateCell(srv.ID)), 48))
	}
	fixed := 2 + 2*len(head) + stateW
	for c := range head {
		if c != 2 {
			fixed += widths[c]
		}
	}
	widths[2] = max(min(widths[2], s.width-fixed), 12)
	header = append(header, theme.Bold.Render("  "+joinCells(head, widths)+"  STATE"))
	rows := make([]string, len(cells))
	for i := range cells {
		line := "  " + joinCells(cells[i], widths)
		if i == s.list.Cursor {
			line = theme.Sel.Render("> " + line[2:])
		}
		rows[i] = line + "  " + s.stateCell(s.servers[i].ID)
	}
	return s.list.Frame(header, rows, s.footer(), s.height)
}

func joinCells(cells []string, widths []int) string {
	parts := make([]string, len(cells))
	for i, c := range cells {
		c = ansi.Truncate(c, widths[i], "…")
		parts[i] = c + strings.Repeat(" ", widths[i]-ansi.StringWidth(c))
	}
	return strings.Join(parts, "  ")
}

// stateCell renders "agent:class" per agent, flagging classes that differ.
func (s *Screen) stateCell(id string) string {
	if !s.statusOK {
		return theme.Dim.Render("?")
	}
	m := s.states[id]
	if len(m) == 0 {
		return theme.Dim.Render("—")
	}
	agents := make([]string, 0, len(m))
	for a := range m {
		agents = append(agents, a)
	}
	slices.Sort(agents)
	cells := make([]string, len(agents))
	for i, a := range agents {
		cls := m[a]
		txt := mask.OneLine(a) + ":" + mask.OneLine(cls)
		switch {
		case model.NeedsAttention(cls):
			cells[i] = theme.Bad.Render(txt + "!")
		case model.IsSettled(cls):
			cells[i] = theme.OK.Render(txt)
		default:
			cells[i] = theme.Warn.Render(txt)
		}
	}
	return strings.Join(cells, " ")
}

// target summarises where a server runs: masked command line or URL.
func target(srv source.Server) string {
	if srv.Type == "stdio" || srv.URL == "" {
		parts := append([]string{mask.FreeText(srv.Command)}, mask.CommandLine(srv.Args)...)
		return mask.OneLine(mask.Text(strings.Join(parts, " ")))
	}
	return mask.OneLine(mask.Text(srv.URL))
}

func onOff(srv source.Server) string {
	if srv.Disabled {
		return "off"
	}
	return "on"
}

// allowlist renders Extra["agents"] (e.g. "*" or "claude,kiro").
func allowlist(srv source.Server) string {
	list, ok := srv.Extra["agents"].([]any)
	if !ok {
		return "-"
	}
	names := make([]string, 0, len(list))
	for _, v := range list {
		names = append(names, mask.OneLine(mask.Text(fmt.Sprint(v))))
	}
	if len(names) == 0 {
		return "-"
	}
	return strings.Join(names, ",")
}

func (s *Screen) detailView() []string {
	srv, ok := s.selected()
	if !ok {
		return s.listView()
	}
	state := "enabled"
	if srv.Disabled {
		state = "disabled"
	}
	out := []string{theme.Bold.Render(mask.OneLine(srv.ID)) + "  " + theme.Dim.Render(state), ""}
	add := func(k, v string) { out = append(out, fmt.Sprintf("  %-9s %s", k, v)) }
	add("type:", mask.OneLine(srv.Type))
	if srv.Command != "" {
		add("command:", mask.OneLine(mask.FreeText(srv.Command)))
	}
	if len(srv.Args) > 0 {
		add("args:", "")
		for _, a := range mask.CommandLine(srv.Args) {
			out = append(out, "    "+mask.OneLine(a))
		}
	}
	if srv.URL != "" {
		add("url:", mask.OneLine(mask.Text(srv.URL)))
	}
	out = appendKV(out, "env:", srv.Env)
	out = appendKV(out, "headers:", srv.Headers)
	add("agents:", allowlist(srv))
	out = append(out, extraLines(srv.Extra)...)
	if s.statusOK {
		add("state:", s.stateCell(srv.ID))
	}
	out = append(out, "", theme.Dim.Render("esc/enter back • e enable/disable • x remove • E edit"))
	if s.note != "" {
		out = append(out, theme.Warn.Render(s.note))
	}
	return out
}

// appendKV renders a map as sorted "KEY = <masked value>" lines.
func appendKV(out []string, title string, m map[string]string) []string {
	if len(m) == 0 {
		return out
	}
	out = append(out, "  "+title)
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		out = append(out, "    "+mask.OneLine(mask.Text(k))+" = "+mask.OneLine(mask.Value(m[k])))
	}
	return out
}

// extraLines renders unknown [server] keys. Scalars are masked; tables and
// arrays are only summarised, never dumped.
func extraLines(extra map[string]any) []string {
	keys := make([]string, 0, len(extra))
	for k := range extra {
		if k != "agents" {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	slices.Sort(keys)
	out := []string{"  other:"}
	for _, k := range keys {
		var v string
		switch x := extra[k].(type) {
		case []any:
			v = "[" + strconv.Itoa(len(x)) + " items]"
		case map[string]any:
			v = "{" + strconv.Itoa(len(x)) + " keys}"
		default:
			v = fmt.Sprint(x)
			if mask.SensitiveKey(k) {
				v = mask.Value(v)
			}
			v = mask.Text(v)
		}
		out = append(out, "    "+mask.OneLine(mask.Text(k))+" = "+mask.OneLine(v))
	}
	return out
}
