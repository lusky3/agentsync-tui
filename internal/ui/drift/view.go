package drift

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/model"
	"github.com/lusky3/agentsync-tui/internal/ui/theme"
)

const (
	listKeys   = "enter detail • f filter • g legend • r refresh • j/k move"
	detailKeys = "esc close • ctrl+d/ctrl+u scroll"
)

// classOrder lists known classes in display order; unknown ones follow sorted.
var classOrder = []string{"clean", "converged", "new", "pending", "drift", "conflict", "foreign-collision", "orphan", "orphan-drifted"}

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
	filter := "all"
	if !s.all {
		filter = "needs attention"
	}
	summary := "no drift"
	if n := len(s.rep.Drifted()); n > 0 {
		summary = theme.Bad.Render(strconv.Itoa(n) + " need attention")
	} else {
		summary = theme.OK.Render(summary)
	}
	head := []string{summary + theme.Dim.Render("  filter: "+filter), ""}
	if s.note != "" { // failed refresh: last good report stays visible
		head = append([]string{theme.Dim.Render(s.note)}, head...)
	}

	var body []string
	selLine, idx := 0, 0
	for _, a := range s.rep.Agents {
		body = append(body, theme.Bold.Render(mask.OneLine(mask.Text(a.Agent))+"  "+counts(a.Items)))
		for _, it := range s.visible(a) {
			line := "  " + itemLine(it)
			if idx == s.cursor {
				selLine = len(body)
				line = theme.Sel.Render("> " + line[2:])
			}
			body = append(body, line+"  "+theme.Dim.Render(mask.OneLine(mask.Text(it.Path))))
			idx++
		}
	}
	if idx == 0 {
		body = append(body, theme.Dim.Render("  no items match this filter"))
	}
	avail := max(s.height-len(head)-len(foot), 1)
	start := 0
	if selLine >= avail {
		start = selLine - avail + 1
	}
	end := min(start+avail, len(body))
	return append(append(head, body[start:end]...), foot...)
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
	sort.Strings(rest)
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
	case flagged[it.State]:
		return theme.Bad.Render("! "+cls) + " " + label
	case it.State == "clean" || it.State == "converged":
		return theme.OK.Render("  "+cls) + " " + label
	}
	return theme.Warn.Render("  "+cls) + " " + label
}

// pointerLabel maps /mcpServers/<id> to <id> (pointer-unescaped); any other
// pointer is shown as is.
func pointerLabel(p string) string {
	if rest, ok := strings.CutPrefix(p, "/mcpServers/"); ok && rest != "" && !strings.Contains(rest, "/") {
		p = rest
	}
	p = strings.NewReplacer("~1", "/", "~0", "~").Replace(p)
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

// detailAvail is how many content lines the detail pane shows.
func (s *Screen) detailAvail() int { return max(s.height-3, 1) }

func (s *Screen) maxScroll() int { return max(len(s.detailBody())-s.detailAvail(), 0) }

func (s *Screen) detailPane() []string {
	body := s.detailBody()
	start := min(s.scroll, s.maxScroll())
	end := min(start+s.detailAvail(), len(body))
	head := theme.Bold.Render(pointerLabel(s.item.Pointer)) + "  " + theme.Dim.Render(mask.OneLine(mask.Text(s.item.State)))
	out := append([]string{head, theme.Dim.Render(mask.OneLine(mask.Text(s.item.Path)))}, body[start:end]...)
	return append(out, theme.Dim.Render(detailKeys))
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
				add("secret:", mask.OneLine(mask.Value("", ref)))
			}
		}
	}
	return out
}
