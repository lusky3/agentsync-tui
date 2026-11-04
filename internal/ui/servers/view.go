package servers

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/source"
)

var (
	styleBold = lipgloss.NewStyle().Bold(true)
	styleDim  = lipgloss.NewStyle().Faint(true)
	styleWarn = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styleBad  = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	styleOK   = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleSel  = lipgloss.NewStyle().Reverse(true)
)

// flagged classes need attention: the destination differs from canonical.
var flagged = map[string]bool{"drift": true, "conflict": true, "foreign-collision": true, "orphan-drifted": true}

// sensitiveKey marks Extra keys whose scalar values are masked outright.
var sensitiveKey = regexp.MustCompile(`(?i)token|secret|pass|key|auth|credential|bearer|cookie|session`)

const listKeys = "enter detail • e enable/disable • x remove • a add • E edit • r refresh"

// View implements tea.Model. Every outside string is sanitised or masked.
func (s *Screen) View() tea.View {
	var body string
	switch {
	case s.form != nil:
		body = s.form.view()
	case s.detail:
		body = s.detailView()
	default:
		body = s.listView()
	}
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, s.width, "…")
	}
	return tea.NewView(strings.Join(lines, "\n"))
}

func (s *Screen) footer() []string {
	var out []string
	if s.loaded && !s.statusOK && s.stNote != "" {
		out = append(out, styleDim.Render("agent state unavailable: "+s.stNote))
	}
	if s.note != "" {
		out = append(out, styleWarn.Render(s.note))
	}
	return append(out, styleDim.Render(listKeys))
}

func (s *Screen) listView() string {
	var out []string
	if !s.loaded {
		out = append(out, styleDim.Render("loading…"))
		return strings.Join(append(out, s.footer()...), "\n")
	}
	if s.loadWarn != "" {
		out = append(out, styleWarn.Render(s.loadWarn))
	}
	if len(s.servers) == 0 {
		out = append(out, "no MCP servers — press a to add one")
		if s.dirGone {
			out = append(out, styleDim.Render("(mcp/ directory not found)"))
		}
		return strings.Join(append(out, s.footer()...), "\n")
	}
	head := []string{"ID", "TYPE", "TARGET", "ON", "AGENTS"}
	rows := make([][]string, len(s.servers))
	for i, srv := range s.servers {
		rows[i] = []string{mask.OneLine(srv.ID), mask.OneLine(srv.Type), target(srv), onOff(srv), allowlist(srv)}
	}
	caps := []int{28, 5, 48, 3, 20}
	widths := make([]int, len(head))
	for c := range head {
		widths[c] = ansi.StringWidth(head[c])
		for _, r := range rows {
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
	out = append(out, styleBold.Render("  "+joinCells(head, widths)+"  STATE"))
	foot := s.footer()
	avail := max(s.height-len(out)-len(foot), 1)
	start := 0
	if s.cursor >= avail {
		start = s.cursor - avail + 1
	}
	for i := start; i < len(rows) && i < start+avail; i++ {
		line := "  " + joinCells(rows[i], widths)
		if i == s.cursor {
			line = styleSel.Render("> " + line[2:])
		}
		out = append(out, line+"  "+s.stateCell(s.servers[i].ID))
	}
	return strings.Join(append(out, foot...), "\n")
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
		return styleDim.Render("?")
	}
	m := s.states[id]
	if len(m) == 0 {
		return styleDim.Render("—")
	}
	agents := make([]string, 0, len(m))
	for a := range m {
		agents = append(agents, a)
	}
	sort.Strings(agents)
	cells := make([]string, len(agents))
	for i, a := range agents {
		cls := m[a]
		txt := mask.OneLine(a) + ":" + mask.OneLine(cls)
		switch {
		case flagged[cls]:
			cells[i] = styleBad.Render(txt + "!")
		case cls == "clean" || cls == "converged":
			cells[i] = styleOK.Render(txt)
		default:
			cells[i] = styleWarn.Render(txt)
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

func (s *Screen) detailView() string {
	srv, ok := s.selected()
	if !ok {
		return s.listView()
	}
	state := "enabled"
	if srv.Disabled {
		state = "disabled"
	}
	out := []string{styleBold.Render(mask.OneLine(srv.ID)) + "  " + styleDim.Render(state), ""}
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
	out = append(out, "", styleDim.Render("esc/enter back • e enable/disable • x remove • E edit"))
	if s.note != "" {
		out = append(out, styleWarn.Render(s.note))
	}
	return strings.Join(out, "\n")
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
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, "    "+mask.OneLine(mask.Text(k))+" = "+mask.OneLine(mask.Value(k, m[k])))
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
	sort.Strings(keys)
	out := []string{"  other:"}
	for _, k := range keys {
		var v string
		switch x := extra[k].(type) {
		case []any:
			v = "[" + itoa(len(x)) + " items]"
		case map[string]any:
			v = "{" + itoa(len(x)) + " keys}"
		default:
			v = fmt.Sprint(x)
			if sensitiveKey.MatchString(k) {
				v = mask.Value(k, v)
			}
			v = mask.Text(v)
		}
		out = append(out, "    "+mask.OneLine(mask.Text(k))+" = "+mask.OneLine(v))
	}
	return out
}

func itoa(n int) string { return strconv.Itoa(n) }
