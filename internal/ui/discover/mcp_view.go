package discover

import (
	"maps"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/discover/mcpsrc"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/ui"
	"github.com/lusky3/agentsync-tui/internal/ui/theme"
)

// Every string that came from a source (titles, descriptions, labels,
// reasons, provenance, trust lines, prompt descriptions) is rendered
// through mask.OneLine here, at render time, whatever the source did.

const (
	keysList   = "/ search • enter detail • i install • n next page • m source • " + ui.ScrollKeys
	keysDetail = "j/k choose install option • ctrl+d/ctrl+u scroll it • i install (opens the MCP servers add form) • esc back"
	keysField  = "enter search (contacts the source) • esc leave the field"

	markDeprecated  = "[deprecated] "
	markUnsupported = "unsupported: "
	indent          = "    "
)

// View implements tea.Model.
func (s *MCPScreen) View() tea.View {
	var lines []string
	if e, ok := s.shown(); ok && s.detail != "" {
		lines = s.detailView(e)
	} else {
		lines = s.listView()
	}
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, s.width, "…")
	}
	return tea.NewView(strings.Join(lines, "\n"))
}

func (s *MCPScreen) header() []string {
	src := sources[s.src]
	search := "search: " + s.query.Value
	if s.focus {
		search = theme.Sel.Render(search + "▏")
	} else if s.query.Value == "" {
		search += theme.Dim.Render("(press / to type, enter to search)")
	}
	return []string{
		theme.Bold.Render(Title) + theme.Dim.Render("  source: "+src.name+" ("+src.host+")"),
		search,
		s.status(),
	}
}

// status is the one line under the search field: a note, the source's
// error, a search in flight, why no request can be made, or a summary.
func (s *MCPScreen) status() string {
	r := s.cur()
	switch {
	case s.note != "":
		return theme.Warn.Render(s.note)
	case r.err != "":
		return theme.Bad.Render(r.err)
	case r.loading:
		return theme.Dim.Render("searching " + sources[s.src].host + "…")
	case s.blocked() != "":
		return theme.Warn.Render(s.blocked())
	case !r.searched:
		return theme.Dim.Render("nothing is requested until you press enter in the search field")
	}
	sum := strconv.Itoa(len(r.entries)) + " results for " + strconv.Quote(mask.OneLine(r.query))
	if r.next != "" {
		sum += " • n for more"
	}
	if r.skipped > 0 {
		sum += " • " + strconv.Itoa(r.skipped) + " entries could not be read"
	}
	return theme.Dim.Render(sum)
}

func (s *MCPScreen) listView() []string {
	r := s.cur()
	rows := make([]string, 0, len(r.entries))
	for i, e := range r.entries {
		row := rowText(e)
		if i == s.list.Cursor {
			row = theme.Sel.Render(row)
		}
		rows = append(rows, row)
	}
	keys := keysList
	if s.focus {
		keys = keysField
	}
	return s.list.Frame(s.header(), rows, ui.KeyLines(keys, s.width), s.height)
}

// rowText is one entry on one line: markers, title, and the first
// unsupported reason when no option can be installed, else the description.
func rowText(e mcpsrc.Entry) string {
	var b strings.Builder
	if e.Deprecated {
		b.WriteString(markDeprecated)
	}
	b.WriteString(mask.OneLine(e.Title))
	if why := unsupportedOnly(e); why != "" {
		b.WriteString("  " + markUnsupported + mask.OneLine(why))
	} else if e.Description != "" {
		b.WriteString("  " + mask.OneLine(e.Description))
	}
	return b.String()
}

// unsupportedOnly is the first option's reason when no option of e is
// installable, else "".
func unsupportedOnly(e mcpsrc.Entry) string {
	for _, in := range e.Installs {
		if in.Draft != nil {
			return ""
		}
	}
	if len(e.Installs) == 0 {
		return mcpsrc.ReasonNoInstall
	}
	return e.Installs[0].Unsupported
}

// detailView is the entry with its trust lines in the fixed head (never
// scrolled off), then every install option with its own lines; the whole
// block of the chosen option stays in view (its label first, when the
// block does not fit; the page keys then scroll through the rest of it).
func (s *MCPScreen) detailView(e mcpsrc.Entry) []string {
	head := []string{
		theme.Bold.Render(mask.OneLine(e.Title)) + theme.Dim.Render("  "+sources[s.src].name+": "+mask.OneLine(e.ID)),
		s.status(),
		theme.Bold.Render("trust"),
	}
	for _, t := range e.Trust {
		head = append(head, "  "+theme.Warn.Render(mask.OneLine(t)))
	}
	body := []string{}
	if e.Deprecated {
		body = append(body, theme.Warn.Render("deprecated: the source marks this entry deprecated or deleted"))
	}
	if e.Description != "" {
		body = append(body, mask.OneLine(e.Description))
	}
	body = append(body, "", theme.Bold.Render("install options"))
	s.opt.Clamp(len(e.Installs))
	first, last := 0, 0
	for i, in := range e.Installs {
		lines := optionLines(e, in, i == s.opt.Cursor)
		if i == s.opt.Cursor {
			first, last = len(body), len(body)+len(lines)-1
		}
		body = append(body, lines...)
	}
	foot := ui.KeyLines(keysDetail, s.width)
	// Scrolling moves the top of the view down the block, never past
	// where its last line is at the bottom.
	s.optLines = last - first + 1
	s.optScroll.Window(s.optLines, max(s.height-len(head)-len(foot), 1))
	return s.opt.FrameSpan(first+s.optScroll.Offset, last, head, body, foot, s.height)
}

// optionLines renders one install option: its label, then either the
// reason it is unsupported or its provenance, its own trust lines (those
// mcpsrc.InstallTrust adds to the entry's), the environment it sets and
// the values the form will ask for.
func optionLines(e mcpsrc.Entry, in mcpsrc.Install, chosen bool) []string {
	label := "  " + mask.OneLine(in.Label)
	if chosen {
		label = theme.Sel.Render("> " + mask.OneLine(in.Label))
	}
	out := []string{label}
	if in.Draft == nil {
		return append(out, indent+theme.Bad.Render(markUnsupported+mask.OneLine(in.Unsupported)))
	}
	out = append(out, indent+theme.Dim.Render(mask.OneLine(in.Draft.Provenance)))
	all := mcpsrc.InstallTrust(e, in)
	for _, t := range all[min(len(e.Trust), len(all)):] {
		out = append(out, indent+theme.Warn.Render(mask.OneLine(t)))
	}
	out = append(out, envLines(in.Draft.Spec.Env)...)
	for _, p := range in.Draft.Prompts {
		line := indent + "asks for " + mask.OneLine(p.Name)
		if p.Required {
			line += " (required)"
		}
		if p.Description != "" {
			line += ": " + mask.OneLine(p.Description)
		}
		out = append(out, line)
	}
	return out
}

// envLines lists the environment an option sets, sorted by name: each
// value masked (Text), or masked whole unless it is a reference (Value)
// when the name reads like a credential, on one inert row.
func envLines(env map[string]string) []string {
	var out []string
	for _, k := range slices.Sorted(maps.Keys(env)) {
		v := mask.Text(env[k])
		if mask.SensitiveKey(k) {
			v = mask.Value(env[k])
		}
		out = append(out, indent+"sets "+mask.OneLine(k)+"="+mask.OneLine(v))
	}
	return out
}
