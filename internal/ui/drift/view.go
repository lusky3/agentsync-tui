package drift

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/model"
)

var (
	styleBold = lipgloss.NewStyle().Bold(true)
	styleDim  = lipgloss.NewStyle().Faint(true)
	styleWarn = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styleBad  = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	styleOK   = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleSel  = lipgloss.NewStyle().Reverse(true)
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
		return []string{styleDim.Render("loading…"), styleDim.Render(listKeys)}
	}
	foot := []string{styleDim.Render(listKeys)}
	if s.note != "" {
		return append([]string{styleDim.Render(s.note)}, foot...)
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
		summary = styleBad.Render(itoa(n) + " need attention")
	} else {
		summary = styleOK.Render(summary)
	}
	head := []string{summary + styleDim.Render("  filter: "+filter), ""}

	var body []string
	selLine, idx := 0, 0
	for _, a := range s.rep.Agents {
		body = append(body, styleBold.Render(oneLine(mask.Text(a.Agent))+"  "+counts(a.Items)))
		for _, it := range s.visible(a) {
			line := "  " + itemLine(it)
			if idx == s.cursor {
				selLine = len(body)
				line = styleSel.Render("> " + line[2:])
			}
			body = append(body, line+"  "+styleDim.Render(oneLine(mask.Text(it.Path))))
			idx++
		}
	}
	if idx == 0 {
		body = append(body, styleDim.Render("  no items match this filter"))
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
		parts = append(parts, itoa(n[c])+" "+oneLine(mask.Text(c)))
	}
	if len(parts) == 0 {
		return "no items"
	}
	return strings.Join(parts, " · ")
}

// itemLine renders the class mark, class and label of one item.
func itemLine(it model.StatusItem) string {
	cls := oneLine(mask.Text(it.State))
	label := pointerLabel(it.Pointer)
	switch {
	case flagged[it.State]:
		return styleBad.Render("! "+cls) + " " + label
	case it.State == "clean" || it.State == "converged":
		return styleOK.Render("  "+cls) + " " + label
	}
	return styleWarn.Render("  "+cls) + " " + label
}

// pointerLabel maps /mcpServers/<id> to <id> (pointer-unescaped); any other
// pointer is shown as is.
func pointerLabel(p string) string {
	if rest, ok := strings.CutPrefix(p, "/mcpServers/"); ok && rest != "" && !strings.Contains(rest, "/") {
		p = rest
	}
	p = strings.NewReplacer("~1", "/", "~0", "~").Replace(p)
	return oneLine(mask.Text(p))
}

func (s *Screen) legendLines() []string {
	out := []string{styleBold.Render("Status classes"), ""}
	switch {
	case s.legendNote != "":
		out = append(out, styleDim.Render(s.legendNote))
	case s.legendText == "":
		out = append(out, styleDim.Render("loading…"))
	default:
		for _, l := range strings.Split(strings.TrimRight(mask.Sanitize(s.legendText), "\n"), "\n") {
			out = append(out, strings.ReplaceAll(l, "\t", "  "))
		}
	}
	return append(out, "", styleDim.Render("esc close"))
}

// detailAvail is how many content lines the detail pane shows.
func (s *Screen) detailAvail() int { return max(s.height-3, 1) }

func (s *Screen) maxScroll() int { return max(len(s.detailBody())-s.detailAvail(), 0) }

func (s *Screen) detailPane() []string {
	body := s.detailBody()
	start := min(s.scroll, s.maxScroll())
	end := min(start+s.detailAvail(), len(body))
	head := styleBold.Render(pointerLabel(s.item.Pointer)) + "  " + styleDim.Render(oneLine(mask.Text(s.item.State)))
	out := append([]string{head, styleDim.Render(oneLine(mask.Text(s.item.Path)))}, body[start:end]...)
	return append(out, styleDim.Render(detailKeys))
}

// detailBody renders provenance then the masked hunks, one string per line.
func (s *Screen) detailBody() []string {
	if !s.diffable {
		return []string{"", styleWarn.Render("path not diffable")}
	}
	out := []string{""}
	switch {
	case !s.provDone:
		out = append(out, styleDim.Render("loading provenance…"))
	case s.provNote != "":
		out = append(out, styleDim.Render(s.provNote))
	default:
		out = append(out, provLines(*s.prov)...)
	}
	out = append(out, "")
	switch {
	case !s.diffDone:
		out = append(out, styleDim.Render("loading diff…"))
	case s.diffNote != "":
		out = append(out, styleDim.Render(s.diffNote))
	case len(s.hunks) == 0:
		out = append(out, styleDim.Render("no differing hunk for this item"))
	default:
		for _, h := range s.hunks {
			out = append(out, styleBold.Render("hunk "+oneLine(h.Pointer)))
			out = append(out, side("source (canonical)", h.Source)...)
			out = append(out, side("dest (on disk)", h.Dest)...)
		}
	}
	return out
}

// side renders one already-masked hunk side as an indented block.
func side(title, content string) []string {
	out := []string{styleDim.Render("  " + title)}
	for _, l := range strings.Split(strings.TrimRight(mask.Sanitize(content), "\n"), "\n") {
		out = append(out, "    "+strings.ReplaceAll(l, "\t", "  "))
	}
	return out
}

func provLines(p model.Provenance) []string {
	t := func(v string) string { return oneLine(mask.Text(v)) }
	out := []string{styleBold.Render("provenance")}
	add := func(k, v string) { out = append(out, fmt.Sprintf("  %-10s %s", k, v)) }
	if p.Scope != "" {
		add("scope:", t(p.Scope))
	}
	if p.Unmanaged {
		add("managed:", "no (unmanaged key)")
	}
	if len(p.Owners) == 0 {
		return append(out, styleDim.Render("  no owners reported"))
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
				add("secret:", oneLine(mask.Value("", ref)))
			}
		}
	}
	return out
}

// oneLine makes v safe for a single terminal line.
func oneLine(v string) string {
	v = mask.Sanitize(v)
	return strings.NewReplacer("\n", " ", "\t", " ").Replace(v)
}

// firstLine returns the first non-blank line of v, trimmed and sanitised.
func firstLine(v string) string {
	for _, l := range strings.Split(mask.Sanitize(v), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return oneLine(l)
		}
	}
	return ""
}

func itoa(n int) string { return strconv.Itoa(n) }
