package plugins

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var (
	styleBold = lipgloss.NewStyle().Bold(true)
	styleDim  = lipgloss.NewStyle().Faint(true)
	styleWarn = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	styleOK   = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	styleSel  = lipgloss.NewStyle().Reverse(true)
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
		return styleSel.Render(a) + styleDim.Render(b)
	}
	return styleDim.Render(a) + styleSel.Render(b)
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
		out = append(out, styleDim.Render("loading…"))
	}
	out = append(out, rows...)
	if note != "" {
		out = append(out, styleDim.Render(what+" unavailable: "+note))
	}
	if s.pane == panePlugins && s.outdatedRaw != "" {
		out = append(out, "", styleBold.Render("Outdated check (network)"), styleDim.Render(s.rawSummary()))
	}
	if s.typing >= 0 {
		label := "plugin id[@marketplace]"
		if s.typing == paneMarkets {
			label = "marketplace url or path"
		}
		out = append(out, "", styleBold.Render("add "+label+": ")+s.liveField()+"_")
		if s.inlineE != "" {
			out = append(out, styleWarn.Render(s.inlineE))
		}
		out = append(out, styleDim.Render("enter add • esc cancel"))
	}
	if s.note != "" {
		out = append(out, styleWarn.Render(s.note))
	}
	return append(out, styleDim.Render(keys))
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
	out := []string{styleBold.Render("  " + pad("PLUGIN", 28) + pad("VERSION", 12) + pad("STATE", 10) + "UPDATE")}
	for i, p := range s.plugins {
		state, upd := "disabled", ""
		if p.Enabled {
			state = "enabled"
		}
		if o, ok := s.outdated[strings.SplitN(p.Name, "@", 2)[0]]; ok {
			upd = "↑ " + oneLine(o.To)
		}
		line := pad(oneLine(p.Name), 28) + pad(oneLine(p.Version), 12) + pad(state, 10) + upd
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
	out := []string{styleBold.Render("  " + pad("MARKETPLACE", 24) + "SOURCE")}
	for i, m := range s.markets {
		out = append(out, s.row(i == s.mCur, true, pad(oneLine(m.Name), 24)+m.URL))
	}
	return out
}

func (s *Screen) row(cursor, on bool, line string) string {
	switch {
	case cursor:
		return styleSel.Render("> " + line)
	case on:
		return "  " + styleOK.Render(line)
	}
	return "  " + styleDim.Render(line)
}

func (s *Screen) detailView() []string {
	out := []string{styleBold.Render(oneLine(s.detailFor)) + styleDim.Render("  (esc close • j/k scroll)")}
	switch {
	case s.detailBusy:
		return append(out, styleDim.Render("loading…"))
	}
	ls := strings.Split(s.detail, "\n")
	lo := min(s.scroll, max(len(ls)-1, 0))
	hi := min(lo+s.bodyLines(), len(ls))
	out = append(out, ls[lo:hi]...)
	if hi < len(ls) {
		out = append(out, styleDim.Render("… more below"))
	}
	return out
}

func pad(s string, n int) string {
	s = ansi.Truncate(s, n-1, "…")
	return s + strings.Repeat(" ", max(n-ansi.StringWidth(s), 0))
}
