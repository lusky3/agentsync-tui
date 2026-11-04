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

// maxField caps the add field (bytes).
const maxField = 2048

// typingKey handles a key while the add field has focus.
func (s *Screen) typingKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		s.typing, s.buf, s.inlineE = -1, "", ""
	case "ctrl+u":
		s.buf, s.inlineE = "", ""
	case "backspace":
		if s.buf != "" {
			_, n := utf8.DecodeLastRuneInString(s.buf)
			s.buf = s.buf[:len(s.buf)-n]
		}
		s.inlineE = ""
	case "enter":
		return s.submitAdd()
	default:
		if k.Text != "" {
			s.insert(k.Text)
		}
	}
	return nil
}

func (s *Screen) insert(t string) {
	t = strings.NewReplacer("\n", "", "\t", "", "\r", "").Replace(mask.Sanitize(t))
	if len(s.buf)+len(t) <= maxField {
		s.buf += t
		s.inlineE = ""
	}
}

// submitAdd builds the add action; the builder is the validator. A bad value
// stays in the field with an inline error and nothing is emitted.
func (s *Screen) submitAdd() tea.Cmd {
	var act actions.Action
	var err error
	val := s.buf
	if s.typing == panePlugins {
		act, err = actions.PluginAdd(strings.TrimSpace(val))
	} else {
		act, err = actions.MarketplaceAdd(strings.TrimSpace(val))
	}
	if err != nil {
		s.inlineE = mask.FirstLine(err.Error())
		return nil
	}
	s.typing, s.buf, s.inlineE, s.note = -1, "", "", ""
	return func() tea.Msg { return ui.DoMsg{Action: act} }
}

// liveField renders the buffer for display. Credentials in a URL's
// authority cannot be told from a host until '@' or '/' is typed, so the
// authority is bulleted while it is still open; everything is masked.
func (s *Screen) liveField() string {
	v := s.buf
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
// Runner, bounded by the network read timeout (ui.Deps.Limits().Read).
func (s *Screen) checkOutdated() tea.Cmd {
	if s.oChecking {
		s.note = "already checking for updates"
		return nil
	}
	s.oreq++
	s.oChecking, s.note = true, "checking for updates (network)…"
	req, r, base, limit := s.oreq, s.run, ui.BaseCtx(s.d), s.d.Limits().Read
	return func() tea.Msg {
		out, note := runRead(base, r, limit, "plugin", "outdated")
		if note != "" {
			return outdatedMsg{req: req, note: note}
		}
		list, err := model.ParseOutdated(out)
		m := outdatedMsg{req: req, raw: capText(out), checked: true}
		if err == nil {
			m.list, m.parsed = list, true
		}
		return m
	}
}

func (s *Screen) applyOutdated(m outdatedMsg) {
	if m.req != s.oreq {
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
	s.dreq++
	s.scroll, s.detailOpen = 0, true
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
	req, r, name, base := s.dreq, s.run, p.Name, ui.BaseCtx(s.d)
	return func() tea.Msg {
		out, note := runRead(base, r, explainTimeout, "plugin", "explain", name)
		return explainMsg{req: req, name: name, text: capText(out), note: note}
	}
}

func (s *Screen) applyExplain(m explainMsg) {
	if m.req != s.dreq || !s.detailOpen {
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
	case "down", "j":
		s.scroll = min(s.scroll+1, s.maxScroll())
	case "up", "k":
		s.scroll = max(s.scroll-1, 0)
	case "pgdown", " ":
		s.scroll = min(s.scroll+s.bodyLines(), s.maxScroll())
	case "pgup":
		s.scroll = max(s.scroll-s.bodyLines(), 0)
	}
	return nil
}

func (s *Screen) bodyLines() int { return max(s.height-3, 3) }

func (s *Screen) maxScroll() int {
	return max(len(strings.Split(s.detail, "\n"))-s.bodyLines(), 0)
}
