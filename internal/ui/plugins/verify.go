package plugins

import (
	"context"
	"slices"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/model"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

// guard refuses mutating keys while a pre-mutation check is in flight.
func (s *Screen) guard() bool {
	if s.verify.Busy() {
		s.note = "still verifying the list — wait"
		return false
	}
	return true
}

// emit hands a built action to the shell, or shows the builder error.
func (s *Screen) emit(act actions.Action, err error) tea.Cmd {
	return ui.DoOr(act, err, &s.note, s.scope)
}

// mutate gates e/x/u on the selection, then starts an asynchronous fresh
// read of the pane's list; the action is only built and emitted in verified,
// once the result arrives and the selected item is unchanged.
func (s *Screen) mutate(k string) tea.Cmd {
	if !s.guard() {
		return nil
	}
	v := verification{key: k, pane: s.pane}
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
		if !actions.ValidID(p.ID()) {
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
	s.note = "verifying…"
	r := s.run
	return s.verify.Start(s.base, func(ctx context.Context) any {
		if v.pane == panePlugins {
			v.pf = readPlugins(ctx, 0, r, ui.VerifyTimeout)
		} else {
			v.mf = readMarkets(ctx, 0, r, ui.VerifyTimeout)
		}
		return v
	})
}

// verified handles a current re-read result, whatever it then decides.
func (s *Screen) verified(v verification) tea.Cmd {
	if v.pane != s.pane {
		s.note = ""
		return nil
	}
	if v.pane == panePlugins {
		return s.verifiedPlugin(v)
	}
	return s.verifiedMarket(v)
}

func (s *Screen) verifiedPlugin(v verification) tea.Cmd {
	if cur, ok := s.selPlugin(); !ok || cur.Name != v.p.Name {
		s.note = ""
		return nil
	}
	if v.pf.note != "" {
		s.note = "cannot verify the plugin list: " + v.pf.note
		return nil
	}
	i := slices.IndexFunc(v.pf.plugins, func(p model.Plugin) bool { return p.Name == v.p.Name })
	if i < 0 || v.pf.plugins[i] != v.p {
		s.applyPlugins(v.pf)
		s.note = staleNote
		return s.Refresh()
	}
	switch v.key {
	case "x":
		a, err := actions.PluginRemove(v.p.Name)
		a.Warning = "Removes the plugin and its cached files. This cannot be undone."
		return s.emit(a, err)
	case "u":
		return s.emit(actions.PluginUpgrade(v.p.Name))
	case "e":
		if v.p.Enabled {
			return s.emit(actions.PluginDisable(v.p.Name))
		}
		return s.emit(actions.PluginEnable(v.p.Name))
	}
	return nil
}

func (s *Screen) verifiedMarket(v verification) tea.Cmd {
	if cur, ok := s.selMarket(); !ok || cur.Name != v.m.Name {
		s.note = ""
		return nil
	}
	if v.mf.note != "" {
		s.note = "cannot verify the marketplace list: " + v.mf.note
		return nil
	}
	i := slices.IndexFunc(v.mf.markets, func(m model.Marketplace) bool { return m.Name == v.m.Name })
	if i < 0 || v.mf.markets[i] != v.m {
		s.applyMarkets(v.mf)
		s.note = staleNote
		return s.Refresh()
	}
	a, err := actions.MarketplaceRemove(v.m.Name)
	a.Warning = "Removes the marketplace and its cached files. This cannot be undone."
	return s.emit(a, err)
}
