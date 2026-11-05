package plugins

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/ui"
	"github.com/lusky3/agentsync-tui/internal/ui/theme"
)

const (
	pluginKeys = "e enable/disable • x remove • a add • u upgrade (network) • U upgrade all (network) • o check outdated (network) • enter explain • m marketplaces • r refresh • " + ui.ScrollKeys
	marketKeys = "a add • x remove • enter detail • m plugins • r refresh • " + ui.ScrollKeys
	detailKeys = "esc close • " + ui.ScrollKeys
)

// View implements tea.Model. Every outside string is sanitised or masked.
func (s *Screen) View() tea.View {
	var lines []string
	if s.detailOpen {
		lines = s.detailView()
	} else {
		lines = s.listView()
	}
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, s.width, "…")
	}
	return tea.NewView(strings.Join(lines, "\n"))
}

func (s *Screen) tabs() string {
	a, b := " Plugins ", " Marketplaces "
	if s.pane == panePlugins {
		return theme.Sel.Render(a) + theme.Dim.Render(b)
	}
	return theme.Dim.Render(a) + theme.Sel.Render(b)
}

// listView lays the active pane out as header (tabs, column titles), the
// rows windowed around the cursor, and a footer that is never cut.
func (s *Screen) listView() []string {
	header := []string{s.tabs()}
	note, loaded := s.pNote, s.pLoaded
	keys, what := pluginKeys, "plugin list"
	var cols string
	var rows []string
	if s.pane == paneMarkets {
		note, loaded = s.mNote, s.mLoaded
		keys, what = marketKeys, "marketplace list"
		cols, rows = s.marketRows()
	} else {
		cols, rows = s.pluginRows()
	}
	if !loaded {
		header = append(header, theme.Dim.Render("loading…"))
	}
	if cols != "" {
		header = append(header, cols)
	}
	var footer []string
	if note != "" {
		footer = append(footer, theme.Dim.Render(what+" unavailable: "+note))
	}
	if s.pane == panePlugins && s.outdatedRaw != "" {
		footer = append(footer, "", theme.Bold.Render("Outdated check (network)"), theme.Dim.Render(s.rawSummary()))
	}
	if s.typing >= 0 {
		label := "plugin id[@marketplace]"
		if s.typing == paneMarkets {
			label = "marketplace url or path"
		}
		footer = append(footer, "", theme.Bold.Render("add "+label+": ")+s.liveField()+"_")
		if s.inlineE != "" {
			footer = append(footer, theme.Warn.Render(s.inlineE))
		}
		footer = append(footer, theme.Dim.Render("enter add • esc cancel"))
	}
	if s.note != "" {
		footer = append(footer, theme.Warn.Render(s.note))
	}
	footer = append(footer, ui.KeyLines(keys, s.width)...)
	l, _ := s.list()
	return l.Frame(header, rows, footer, s.height)
}

func (s *Screen) rawSummary() string {
	ls := strings.Split(strings.TrimSpace(s.outdatedRaw), "\n")
	if len(ls) > 8 {
		ls = append(ls[:8], "…")
	}
	return strings.Join(ls, "\n")
}

// pluginRows returns the column header (or the empty notice) and one row
// per plugin.
func (s *Screen) pluginRows() (string, []string) {
	if len(s.plugins) == 0 {
		if s.pLoaded {
			return "no plugins installed — press a to add one (id@marketplace)", nil
		}
		return "", nil
	}
	head := theme.Bold.Render("  " + theme.Pad("PLUGIN", 28) + theme.Pad("VERSION", 12) + theme.Pad("STATE", 10) + "UPDATE")
	rows := make([]string, 0, len(s.plugins))
	for i, p := range s.plugins {
		state, upd := "disabled", ""
		if p.Enabled {
			state = "enabled"
		}
		if o, ok := s.outdated[p.ID()]; ok {
			upd = "↑ " + mask.OneLine(o.To)
		}
		line := theme.Pad(mask.OneLine(p.Name), 28) + theme.Pad(mask.OneLine(p.Version), 12) + theme.Pad(state, 10) + upd
		rows = append(rows, row(i == s.plist.Cursor, p.Enabled, line))
	}
	return head, rows
}

// marketRows returns the column header (or the empty notice) and one row
// per marketplace.
func (s *Screen) marketRows() (string, []string) {
	if len(s.markets) == 0 {
		if s.mLoaded {
			return "no marketplaces registered — press a to add a url or path", nil
		}
		return "", nil
	}
	head := theme.Bold.Render("  " + theme.Pad("MARKETPLACE", 24) + "SOURCE")
	rows := make([]string, 0, len(s.markets))
	for i, m := range s.markets {
		rows = append(rows, row(i == s.mlist.Cursor, true, theme.Pad(mask.OneLine(m.Name), 24)+m.URL))
	}
	return head, rows
}

func row(cursor, on bool, line string) string {
	switch {
	case cursor:
		return theme.Sel.Render("> " + line)
	case on:
		return "  " + theme.OK.Render(line)
	}
	return "  " + theme.Dim.Render(line)
}

func (s *Screen) detailView() []string {
	header := []string{theme.Bold.Render(mask.OneLine(s.detailFor)) + theme.Dim.Render("  ("+detailKeys+")")}
	if s.detailBusy {
		return append(header, theme.Dim.Render("loading…"))
	}
	ls := strings.Split(s.detail, "\n")
	footer := []string{""}
	out := s.dscroll.Frame(header, ls, footer, s.height)
	if s.dscroll.Offset+len(out)-len(header)-len(footer) < len(ls) {
		out[len(out)-1] = theme.Dim.Render("… more below")
	}
	return out
}
