package plugins

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/ui/theme"
)

const (
	pluginKeys = "e enable/disable • x remove • a add • u upgrade (network) • U upgrade all (network) • o check outdated (network) • enter explain • m marketplaces • r refresh"
	marketKeys = "a add • x remove • enter detail • m plugins • j/k move • r refresh"
)

// View implements tea.Model. Every outside string is sanitised or masked.
func (s *Screen) View() tea.View {
	var out []string
	switch {
	case s.detailOpen:
		out = s.detailView()
	default:
		out = s.listView()
	}
	lines := strings.Split(strings.Join(out, "\n"), "\n")
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

func (s *Screen) listView() []string {
	out := []string{s.tabs()}
	note, loaded, rows := s.pNote, s.pLoaded, s.pluginRows()
	keys, what := pluginKeys, "plugin list"
	if s.pane == paneMarkets {
		note, loaded, rows = s.mNote, s.mLoaded, s.marketRows()
		keys, what = marketKeys, "marketplace list"
	}
	if !loaded {
		out = append(out, theme.Dim.Render("loading…"))
	}
	out = append(out, rows...)
	if note != "" {
		out = append(out, theme.Dim.Render(what+" unavailable: "+note))
	}
	if s.pane == panePlugins && s.outdatedRaw != "" {
		out = append(out, "", theme.Bold.Render("Outdated check (network)"), theme.Dim.Render(s.rawSummary()))
	}
	if s.typing >= 0 {
		label := "plugin id[@marketplace]"
		if s.typing == paneMarkets {
			label = "marketplace url or path"
		}
		out = append(out, "", theme.Bold.Render("add "+label+": ")+s.liveField()+"_")
		if s.inlineE != "" {
			out = append(out, theme.Warn.Render(s.inlineE))
		}
		out = append(out, theme.Dim.Render("enter add • esc cancel"))
	}
	if s.note != "" {
		out = append(out, theme.Warn.Render(s.note))
	}
	return append(out, theme.Dim.Render(keys))
}

func (s *Screen) rawSummary() string {
	ls := strings.Split(strings.TrimSpace(s.outdatedRaw), "\n")
	if len(ls) > 8 {
		ls = append(ls[:8], "…")
	}
	return strings.Join(ls, "\n")
}

func (s *Screen) pluginRows() []string {
	if len(s.plugins) == 0 {
		if s.pLoaded {
			return []string{"no plugins installed — press a to add one (id@marketplace)"}
		}
		return nil
	}
	out := []string{theme.Bold.Render("  " + theme.Pad("PLUGIN", 28) + theme.Pad("VERSION", 12) + theme.Pad("STATE", 10) + "UPDATE")}
	for i, p := range s.plugins {
		state, upd := "disabled", ""
		if p.Enabled {
			state = "enabled"
		}
		if o, ok := s.outdated[p.ID()]; ok {
			upd = "↑ " + mask.OneLine(o.To)
		}
		line := theme.Pad(mask.OneLine(p.Name), 28) + theme.Pad(mask.OneLine(p.Version), 12) + theme.Pad(state, 10) + upd
		out = append(out, s.row(i == s.pCur, p.Enabled, line))
	}
	return out
}

func (s *Screen) marketRows() []string {
	if len(s.markets) == 0 {
		if s.mLoaded {
			return []string{"no marketplaces registered — press a to add a url or path"}
		}
		return nil
	}
	out := []string{theme.Bold.Render("  " + theme.Pad("MARKETPLACE", 24) + "SOURCE")}
	for i, m := range s.markets {
		out = append(out, s.row(i == s.mCur, true, theme.Pad(mask.OneLine(m.Name), 24)+m.URL))
	}
	return out
}

func (s *Screen) row(cursor, on bool, line string) string {
	switch {
	case cursor:
		return theme.Sel.Render("> " + line)
	case on:
		return "  " + theme.OK.Render(line)
	}
	return "  " + theme.Dim.Render(line)
}

func (s *Screen) detailView() []string {
	out := []string{theme.Bold.Render(mask.OneLine(s.detailFor)) + theme.Dim.Render("  (esc close • j/k scroll)")}
	switch {
	case s.detailBusy:
		return append(out, theme.Dim.Render("loading…"))
	}
	ls := strings.Split(s.detail, "\n")
	lo := min(s.scroll, max(len(ls)-1, 0))
	hi := min(lo+s.bodyLines(), len(ls))
	out = append(out, ls[lo:hi]...)
	if hi < len(ls) {
		out = append(out, theme.Dim.Render("… more below"))
	}
	return out
}
