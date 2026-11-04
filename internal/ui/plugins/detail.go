package plugins

import (
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/model"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

// typingKey handles a key while the add field has focus.
func (s *Screen) typingKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		s.typing, s.inlineE = -1, ""
		s.buf.Clear()
	case "enter":
		return s.submitAdd()
	default:
		if _, changed := s.buf.Key(k); changed {
			s.inlineE = ""
		}
	}
	return nil
}

func (s *Screen) insert(t string) {
	if changed, _ := s.buf.Insert(t); changed {
		s.inlineE = ""
	}
}

// submitAdd builds the add action; the builder is the validator. A bad value
// stays in the field with an inline error and nothing is emitted.
func (s *Screen) submitAdd() tea.Cmd {
	var act actions.Action
	var err error
	val := strings.TrimSpace(s.buf.Value)
	if s.typing == panePlugins {
		act, err = actions.PluginAdd(val)
	} else {
		act, err = actions.MarketplaceAdd(val)
	}
	if err != nil {
		s.inlineE = mask.FirstLine(err.Error())
		return nil
	}
	s.typing, s.inlineE, s.note = -1, "", ""
	s.buf.Clear()
	return ui.Do(act)
}

// liveField renders the buffer for display. Credentials in a URL's
// authority cannot be told from a host until '@' or '/' is typed, so the
// authority is bulleted while it is still open; everything is masked.
func (s *Screen) liveField() string {
	v := s.buf.Value
	if s.typing == paneMarkets {
		if i := strings.Index(v, "://"); i >= 0 {
			rest := v[i+3:]
			if !strings.ContainsAny(rest, "/@") {
				v = v[:i+3] + strings.Repeat("•", utf8.RuneCountInString(rest))
			}
		}
	}
	return mask.OneLine(mask.Text(v))
}

// checkOutdated runs `plugin outdated` (network) on demand through the
// Runner, bounded by the read timeout (ui.Deps.Limits().Read).
func (s *Screen) checkOutdated() tea.Cmd {
	if s.oChecking {
		s.note = "already checking for updates"
		return nil
	}
	s.oChecking, s.note = true, "checking for updates (network)…"
	gen, ctx := s.oreads.Next(s.base)
	r, limit := s.run, s.netLimit
	return func() tea.Msg {
		res, err := ui.Read(ctx, r, limit, "plugin", "outdated")
		if note := ui.ReadNote("plugin outdated", res, err); note != "" {
			return outdatedMsg{gen: gen, note: note}
		}
		list, err := model.ParseOutdated(res.Stdout)
		m := outdatedMsg{gen: gen, raw: capText(res.Stdout), checked: true}
		if err == nil {
			m.list, m.parsed = list, true
		}
		return m
	}
}

func (s *Screen) applyOutdated(m outdatedMsg) {
	if !s.oreads.Current(m.gen) {
		return
	}
	s.oChecking = false
	if m.note != "" {
		s.note = "outdated check failed: " + m.note
		return
	}
	s.outdated, s.outdatedRaw, s.note = map[string]model.Outdated{}, m.raw, ""
	for _, o := range m.list {
		s.outdated[o.ID()] = o
	}
	if !m.parsed {
		s.note = "outdated output not understood — raw text shown"
	}
}

// openDetail opens the overlay for the selected item: `plugin explain` for
// a plugin (read-only, asynchronous), a local summary for a marketplace.
func (s *Screen) openDetail() tea.Cmd {
	s.dscroll, s.detailOpen = ui.Scroll{}, true
	if s.pane == paneMarkets {
		m, ok := s.selMarket()
		if !ok {
			s.detailOpen = false
			return nil
		}
		s.detailFor = m.Name
		s.detail = "marketplace " + m.Name + "\nsource: " + m.URL + "\nsha: " + m.SHA
		return nil
	}
	p, ok := s.selPlugin()
	if !ok || !actions.ValidID(p.ID()) {
		s.detailOpen = false
		return nil
	}
	s.detailFor, s.detail, s.detailBusy = p.Name, "", true
	gen, ctx := s.dreads.Next(s.base)
	r, name, limit := s.run, p.Name, s.limit
	return func() tea.Msg {
		res, err := ui.Read(ctx, r, limit, "plugin", "explain", name)
		return explainMsg{gen: gen, name: name, text: capText(res.Stdout), note: ui.ReadNote("plugin explain", res, err)}
	}
}

func (s *Screen) applyExplain(m explainMsg) {
	if !s.dreads.Current(m.gen) || !s.detailOpen {
		return
	}
	s.detailBusy = false
	if m.note != "" {
		s.detail = "explain unavailable: " + m.note
		return
	}
	s.detail = m.text
}

func (s *Screen) detailKey(k string) tea.Cmd {
	switch k {
	case "esc", "enter", "q":
		s.closeDetail()
	default:
		s.dscroll.Key(k, len(strings.Split(s.detail, "\n")))
	}
	return nil
}
