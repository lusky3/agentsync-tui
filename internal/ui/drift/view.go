package drift

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/model"
	"github.com/lusky3/agentsync-tui/internal/ui"
	"github.com/lusky3/agentsync-tui/internal/ui/theme"
)

const (
	listKeys   = "enter detail • f filter • g legend • r refresh • " + ui.ScrollKeys
	detailKeys = "esc close • " + ui.ScrollKeys
)

// classOrder lists known classes in display order; unknown ones follow sorted.
var classOrder = []string{"clean", "converged", "new", "pending", "drift", "conflict", "foreign-collision", "orphan", "orphan-drifted"}

// rendered is the list for one (report, filter): every string masked and
// styled once. Only the cursor row changes between renders.
type rendered struct {
	items   []model.StatusItem // visible items in display order
	rows    []string           // group headers and item rows, unselected form
	itemRow []int              // rows index of each item
	inner   []string           // itemLine of each item, for the selected form
	path    []string           // the dim masked path of each item
	summary string             // "N need attention" or "no drift"
}

// rebuild renders the report once (masking every path, agent and class) so
// View only windows and marks the cursor.
func (s *Screen) rebuild() {
	r := &rendered{}
	if n := len(s.rep.Drifted()); n > 0 {
		r.summary = theme.Bad.Render(strconv.Itoa(n) + " need attention")
	} else {
		r.summary = theme.OK.Render("no drift")
	}
	for _, a := range s.rep.Agents {
		r.rows = append(r.rows, theme.Bold.Render(mask.OneLine(mask.Text(a.Agent))+"  "+counts(a.Items)))
		for _, it := range s.visible(a) {
			inner, path := itemLine(it), theme.Dim.Render(mask.OneLine(mask.Text(it.Path)))
			r.items = append(r.items, it)
			r.itemRow = append(r.itemRow, len(r.rows))
			r.inner = append(r.inner, inner)
			r.path = append(r.path, path)
			r.rows = append(r.rows, "  "+inner+"  "+path)
		}
	}
	if len(r.items) == 0 {
		r.rows = append(r.rows, theme.Dim.Render("  no items match this filter"))
	}
	s.rows = r
}

// View implements tea.Model. Every outside string is sanitised or masked.
func (s *Screen) View() tea.View {
	var lines []string
	switch {
	case s.legend:
		lines = s.legendLines()
	case s.detail:
		lines = s.detailPane()
	default:
		lines = s.listLines()
	}
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, s.width, "…")
	}
	return tea.NewView(strings.Join(lines, "\n"))
}

func (s *Screen) listLines() []string {
	if !s.loaded {
		return []string{theme.Dim.Render("loading…"), theme.Dim.Render(listKeys)}
	}
	foot := []string{theme.Dim.Render(listKeys)}
	if s.note != "" && len(s.rep.Agents) == 0 {
		return append([]string{theme.Dim.Render(s.note)}, foot...)
	}
	if len(s.rep.Agents) == 0 {
		return append([]string{"no agents registered"}, foot...)
	}
	r := s.rows
	if r == nil {
		s.rebuild()
		r = s.rows
	}
	filter := "all"
	if !s.all {
		filter = "needs attention"
	}
	head := []string{r.summary + theme.Dim.Render("  filter: "+filter), ""}
	if s.note != "" { // failed refresh: last good report stays visible
		head = append([]string{theme.Dim.Render(s.note)}, head...)
	}
	anchor := 0
	if s.list.Cursor >= 0 && s.list.Cursor < len(r.itemRow) {
		anchor = r.itemRow[s.list.Cursor]
	}
	start, end := s.list.WindowAt(anchor, len(r.rows), s.height-len(head)-len(foot))
	out := make([]string, 0, len(head)+end-start+len(foot))
	out = append(out, head...)
	out = append(out, r.rows[start:end]...)
	if anchor >= start && anchor < end && len(r.items) > 0 {
		c := s.list.Cursor
		out[len(head)+anchor-start] = theme.Sel.Render("> "+r.inner[c]) + "  " + r.path[c]
	}
	return append(out, foot...)
}

// counts renders "12 clean · 3 drift" over every item of an agent.
func counts(items []model.StatusItem) string {
	n := map[string]int{}
	for _, it := range items {
		n[it.State]++
	}
	var order []string
	seen := map[string]bool{}
	for _, c := range classOrder {
		if n[c] > 0 {
			order, seen[c] = append(order, c), true
		}
	}
	var rest []string
	for c := range n {
		if !seen[c] {
			rest = append(rest, c)
		}
	}
	slices.Sort(rest)
	parts := make([]string, 0, len(n))
	for _, c := range append(order, rest...) {
		parts = append(parts, strconv.Itoa(n[c])+" "+mask.OneLine(mask.Text(c)))
	}
	if len(parts) == 0 {
		return "no items"
	}
	return strings.Join(parts, " · ")
}

// itemLine renders the class mark, class and label of one item.
func itemLine(it model.StatusItem) string {
	cls := mask.OneLine(mask.Text(it.State))
	label := pointerLabel(it.Pointer)
	switch {
	case model.NeedsAttention(it.State):
		return theme.Bad.Render("! "+cls) + " " + label
	case model.IsSettled(it.State):
		return theme.OK.Render("  "+cls) + " " + label
	}
	return theme.Warn.Render("  "+cls) + " " + label
}

// pointerLabel maps /mcpServers/<id> to <id> (pointer-unescaped); any other
// pointer is shown as is, unescaped.
func pointerLabel(p string) string {
	if id, ok := model.MCPServerID(p); ok {
		p = id
	} else {
		p = model.UnescapePointer(p)
	}
	return mask.OneLine(mask.Text(p))
}

func (s *Screen) legendLines() []string {
	out := []string{theme.Bold.Render("Status classes"), ""}
	switch {
	case s.legendNote != "":
		out = append(out, theme.Dim.Render(s.legendNote))
	case s.legendText == "":
		out = append(out, theme.Dim.Render("loading…"))
	default:
		for _, l := range strings.Split(strings.TrimRight(mask.Sanitize(s.legendText), "\n"), "\n") {
			out = append(out, strings.ReplaceAll(l, "\t", "  "))
		}
	}
	return append(out, "", theme.Dim.Render("esc close"))
}

func (s *Screen) detailPane() []string {
	head := []string{
		theme.Bold.Render(pointerLabel(s.item.Pointer)) + "  " + theme.Dim.Render(mask.OneLine(mask.Text(s.item.State))),
		theme.Dim.Render(mask.OneLine(mask.Text(s.item.Path))),
	}
	return s.dscroll.Frame(head, s.detailBody(), []string{theme.Dim.Render(detailKeys)}, s.height)
}

// detailBody renders provenance then the masked hunks, one string per line.
func (s *Screen) detailBody() []string {
	if !s.diffable {
		return []string{"", theme.Warn.Render("path not diffable")}
	}
	out := []string{""}
	switch {
	case !s.provDone:
		out = append(out, theme.Dim.Render("loading provenance…"))
	case s.provNote != "":
		out = append(out, theme.Dim.Render(s.provNote))
	default:
		out = append(out, provLines(*s.prov)...)
	}
	out = append(out, "")
	switch {
	case !s.diffDone:
		out = append(out, theme.Dim.Render("loading diff…"))
	case s.diffNote != "":
		out = append(out, theme.Dim.Render(s.diffNote))
	case len(s.hunks) == 0:
		out = append(out, theme.Dim.Render("no differing hunk for this item"))
	default:
		for _, h := range s.hunks {
			out = append(out, theme.Bold.Render("hunk "+mask.OneLine(h.Pointer)))
			out = append(out, side("source (canonical)", h.Source)...)
			out = append(out, side("dest (on disk)", h.Dest)...)
		}
	}
	return out
}

// side renders one already-masked hunk side as an indented block.
func side(title, content string) []string {
	out := []string{theme.Dim.Render("  " + title)}
	for _, l := range strings.Split(strings.TrimRight(mask.Sanitize(content), "\n"), "\n") {
		out = append(out, "    "+strings.ReplaceAll(l, "\t", "  "))
	}
	return out
}

func provLines(p model.Provenance) []string {
	t := func(v string) string { return mask.OneLine(mask.Text(v)) }
	out := []string{theme.Bold.Render("provenance")}
	add := func(k, v string) { out = append(out, fmt.Sprintf("  %-10s %s", k, v)) }
	if p.Scope != "" {
		add("scope:", t(p.Scope))
	}
	if p.Unmanaged {
		add("managed:", "no (unmanaged key)")
	}
	if len(p.Owners) == 0 {
		return append(out, theme.Dim.Render("  no owners reported"))
	}
	for _, o := range p.Owners {
		for _, it := range o.Items {
			add("owner:", t(o.Agent))
			add("source:", t(it.Source.File))
			add("component:", t(it.Component))
			add("name:", t(it.Name))
			add("ownership:", t(it.Ownership))
			add("drift:", t(it.Drift))
			for _, ref := range it.Secrets {
				add("secret:", mask.OneLine(mask.Value(ref)))
			}
		}
	}
	return out
}
