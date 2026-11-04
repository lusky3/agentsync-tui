package plugins

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/model"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

// guard refuses mutating keys while a pre-mutation check is in flight.
func (s *Screen) guard() bool {
	if s.verifying {
		s.note = "still verifying the list — wait"
		return false
	}
	return true
}

func (s *Screen) emit(act actions.Action, err error) tea.Cmd {
	if err != nil {
		s.note = "error: " + firstLine(mask.Text(err.Error()))
		return nil
	}
	s.note = ""
	return func() tea.Msg { return ui.DoMsg{Action: act} }
}

// mutate gates e/x/u on the selection, then starts an asynchronous fresh
// read of the pane's list; the action is only built and emitted in verified,
// once the result arrives and the selected item is unchanged.
func (s *Screen) mutate(k string) tea.Cmd {
	if !s.guard() {
		return nil
	}
	v := verifyMsg{req: s.vreq + 1, key: k, pane: s.pane}
	switch {
	case s.pane == panePlugins && k != "e" && k != "x" && k != "u":
		return nil
	case s.pane == paneMarkets && k != "x":
		return nil
	case s.pane == panePlugins:
		p, ok := s.selPlugin()
		if !ok {
			return nil
		}
		if !actions.ValidID(strings.SplitN(p.Name, "@", 2)[0]) {
			s.note = "invalid plugin name"
			return nil
		}
		v.p = p
	default:
		m, ok := s.selMarket()
		if !ok {
			return nil
		}
		v.m = m
	}
	s.vreq++
	s.verifying, s.note = true, "verifying…"
	r, base := s.run, ui.BaseCtx(s.d)
	return func() tea.Msg {
		if v.pane == panePlugins {
			v.pf = readPlugins(base, 0, r, verifyTimeout)
		} else {
			v.mf = readMarkets(base, 0, r, verifyTimeout)
		}
		return v
	}
}

// verified handles a re-read result. A stale request is dropped; a matching
// request always ends the verifying state, whatever it then decides.
func (s *Screen) verified(m verifyMsg) tea.Cmd {
	if !s.verifying || m.req != s.vreq {
		return nil
	}
	s.verifying = false
	if m.pane != s.pane {
		s.note = ""
		return nil
	}
	if m.pane == panePlugins {
		return s.verifiedPlugin(m)
	}
	return s.verifiedMarket(m)
}

func (s *Screen) verifiedPlugin(m verifyMsg) tea.Cmd {
	if cur, ok := s.selPlugin(); !ok || cur.Name != m.p.Name {
		s.note = ""
		return nil
	}
	if m.pf.note != "" {
		s.note = "cannot verify the plugin list: " + m.pf.note
		return nil
	}
	now, found := model.Plugin{}, false
	for _, p := range m.pf.plugins {
		if p.Name == m.p.Name {
			now, found = p, true
			break
		}
	}
	if !found || now != m.p {
		s.applyPlugins(m.pf)
		s.note = staleNote
		return s.Refresh()
	}
	switch m.key {
	case "x":
		a, err := actions.PluginRemove(m.p.Name)
		a.Warning = "Removes the plugin and its cached files. This cannot be undone."
		return s.emit(a, err)
	case "u":
		return s.emit(actions.PluginUpgrade(m.p.Name))
	case "e":
		if m.p.Enabled {
			return s.emit(actions.PluginDisable(m.p.Name))
		}
		return s.emit(actions.PluginEnable(m.p.Name))
	}
	return nil
}

func (s *Screen) verifiedMarket(m verifyMsg) tea.Cmd {
	if cur, ok := s.selMarket(); !ok || cur.Name != m.m.Name {
		s.note = ""
		return nil
	}
	if m.mf.note != "" {
		s.note = "cannot verify the marketplace list: " + m.mf.note
		return nil
	}
	now, found := model.Marketplace{}, false
	for _, x := range m.mf.markets {
		if x.Name == m.m.Name {
			now, found = x, true
			break
		}
	}
	if !found || now != m.m {
		s.applyMarkets(m.mf)
		s.note = staleNote
		return s.Refresh()
	}
	a, err := actions.MarketplaceRemove(m.m.Name)
	a.Warning = "Removes the marketplace and its cached files. This cannot be undone."
	return s.emit(a, err)
}
