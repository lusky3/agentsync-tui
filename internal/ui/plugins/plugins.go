// Package plugins is the plugins and marketplaces screen: two panes, each a
// list read through the Runner (`plugin list`, `marketplace list`; neither
// has --json) that drives enable/disable/remove/add/upgrade and marketplace
// add/remove through the agentsync CLI via ui.DoMsg, so the shell shows the
// confirm modal. `plugin outdated` and `plugin explain` are read-only
// private-message reads; outdated needs the network and therefore only ever
// runs on demand (key o), never from Init or Refresh.
package plugins

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/model"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

const staleNote = "list changed — refreshed"

const (
	panePlugins = iota
	paneMarkets
)

// maxField caps the add field (bytes).
const maxField = 2048

// Screen is the plugins tab.
type Screen struct {
	base     context.Context
	limit    time.Duration // bound on one local read (ui.Deps.Limits().Local())
	netLimit time.Duration // bound on `plugin outdated` (ui.Deps.Limits().Read)
	scope    runner.Scope  // the scope the lists (and `plugin upgrade`) belong to
	run      runner.Runner
	reads    ui.Rounds // the two list reads; superseded rounds are cancelled

	pane             int
	plugins          []model.Plugin
	markets          []model.Marketplace // URL already masked
	pLoaded, mLoaded bool
	pNote, mNote     string // why the last read failed (display-safe)
	plist, mlist     ui.List
	note             string // last screen message, display-safe

	// outdated is the on-demand `plugin outdated` result (network).
	outdated    map[string]model.Outdated
	outdatedRaw string // masked, capped text of the last check ("" = never)
	oChecking   bool
	oreads      ui.Rounds

	// detail overlay (explain output or marketplace summary).
	detailOpen bool
	detail     string
	detailFor  string
	detailBusy bool
	dreads     ui.Rounds
	dscroll    ui.Scroll

	// text field for add.
	typing  int // -1 none; else the pane being added to
	buf     ui.Field
	inlineE string

	verify ui.Verifier // the pre-mutation staleness check

	width, height int
}

type pluginsMsg struct {
	gen     int
	plugins []model.Plugin
	note    string
}

type marketsMsg struct {
	gen     int
	markets []model.Marketplace
	note    string
}

// verification is what the pre-mutation re-read brings back.
type verification struct {
	key  string
	pane int
	p    model.Plugin
	m    model.Marketplace
	pf   pluginsMsg
	mf   marketsMsg
}

type outdatedMsg struct {
	gen     int
	list    []model.Outdated
	raw     string
	note    string
	parsed  bool
	checked bool
}

type explainMsg struct {
	gen  int
	name string
	text string
	note string
}

// New returns the plugins screen.
func New(d ui.Deps) *Screen {
	return &Screen{
		base: ui.BaseCtx(d), limit: d.Limits().Local(), netLimit: d.Limits().Read, run: d.Runner, scope: d.Scope,
		typing: -1, buf: ui.Field{Max: maxField}, width: 80, height: 24,
	}
}

func (s *Screen) Title() string { return "Plugins" }

// Init is exactly Refresh.
func (s *Screen) Init() tea.Cmd { return s.Refresh() }

// CapturesKeys implements ui.KeyCapturer: true while the add field is open.
func (s *Screen) CapturesKeys() bool { return s.typing >= 0 }

// SetScope implements ui.ScopeSetter.
func (s *Screen) SetScope(r runner.Runner, sc runner.Scope, _ string) {
	s.run, s.scope = r, sc
	s.reads.Supersede()
	s.outdated, s.outdatedRaw, s.oChecking = nil, "", false
	s.oreads.Supersede()
	s.closeDetail()
	s.invalidate()
}

// Shutdown implements ui.Shutdowner.
func (s *Screen) Shutdown() {
	s.reads.Cancel()
	s.oreads.Cancel()
	s.dreads.Cancel()
	s.verify.Invalidate()
}

func (s *Screen) invalidate() {
	if s.verify.Busy() {
		s.note = ""
	}
	s.verify.Invalidate()
}

func (s *Screen) closeDetail() {
	s.detailOpen, s.detailBusy, s.detail = false, false, ""
	s.dscroll = ui.Scroll{}
	s.dreads.Supersede()
}

// Refresh re-reads both lists concurrently, cancelling the previous reads.
// It never runs `plugin outdated`.
func (s *Screen) Refresh() tea.Cmd {
	s.invalidate()
	gen, ctx := s.reads.Next(s.base)
	r, limit := s.run, s.limit
	return tea.Batch(
		func() tea.Msg { return readPlugins(ctx, gen, r, limit) },
		func() tea.Msg { return readMarkets(ctx, gen, r, limit) },
	)
}

func readPlugins(ctx context.Context, gen int, r runner.Runner, timeout time.Duration) pluginsMsg {
	res, err := ui.Read(ctx, r, timeout, "plugin", "list")
	if note := ui.ReadNote("plugin list", res, err); note != "" {
		return pluginsMsg{gen: gen, note: note}
	}
	list, err := model.ParsePluginList(res.Stdout)
	if err != nil {
		return pluginsMsg{gen: gen, note: ui.Unreadable("plugin list", res, err)}
	}
	return pluginsMsg{gen: gen, plugins: list}
}

func readMarkets(ctx context.Context, gen int, r runner.Runner, timeout time.Duration) marketsMsg {
	res, err := ui.Read(ctx, r, timeout, "marketplace", "list")
	if note := ui.ReadNote("marketplace list", res, err); note != "" {
		return marketsMsg{gen: gen, note: note}
	}
	list, err := model.ParseMarketplaces(res.Stdout)
	if err != nil {
		return marketsMsg{gen: gen, note: ui.Unreadable("marketplace list", res, err)}
	}
	for i := range list {
		list[i].URL = mask.OneLine(mask.Text(list[i].URL)) // masked before it is stored
	}
	return marketsMsg{gen: gen, markets: list}
}

// Update implements tea.Model.
func (s *Screen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
	case pluginsMsg:
		if s.reads.Current(msg.gen) {
			s.applyPlugins(msg)
		}
	case marketsMsg:
		if s.reads.Current(msg.gen) {
			s.applyMarkets(msg)
		}
	case ui.VerifiedMsg:
		if v, ok := s.verify.Accept(msg); ok {
			return s, s.verified(v.(verification))
		}
	case outdatedMsg:
		s.applyOutdated(msg)
	case explainMsg:
		s.applyExplain(msg)
	case ui.RunMsg:
		if len(msg.Action.Argv) > 0 && (msg.Action.Argv[0] == "plugin" || msg.Action.Argv[0] == "marketplace") {
			s.outdated, s.outdatedRaw = nil, "" // a mutation makes the last check stale
			s.closeDetail()
		}
	case tea.PasteMsg:
		if s.typing >= 0 {
			s.insert(msg.Content)
		}
	case tea.KeyPressMsg:
		if s.typing >= 0 {
			return s, s.typingKey(msg)
		}
		return s, s.key(msg.String())
	}
	return s, nil
}

func (s *Screen) applyPlugins(m pluginsMsg) {
	s.pLoaded = true
	if m.note != "" {
		s.pNote = m.note // keep the last good list
		return
	}
	s.pNote, s.plugins = "", m.plugins
	s.plist.Clamp(len(s.plugins))
}

func (s *Screen) applyMarkets(m marketsMsg) {
	s.mLoaded = true
	if m.note != "" {
		s.mNote = m.note
		return
	}
	s.mNote, s.markets = "", m.markets
	s.mlist.Clamp(len(s.markets))
}

func (s *Screen) selPlugin() (model.Plugin, bool) {
	if s.plist.Cursor < 0 || s.plist.Cursor >= len(s.plugins) {
		return model.Plugin{}, false
	}
	return s.plugins[s.plist.Cursor], true
}

func (s *Screen) selMarket() (model.Marketplace, bool) {
	if s.mlist.Cursor < 0 || s.mlist.Cursor >= len(s.markets) {
		return model.Marketplace{}, false
	}
	return s.markets[s.mlist.Cursor], true
}

// list is the cursor of the active pane and its row count.
func (s *Screen) list() (*ui.List, int) {
	if s.pane == panePlugins {
		return &s.plist, len(s.plugins)
	}
	return &s.mlist, len(s.markets)
}

func (s *Screen) key(k string) tea.Cmd {
	if s.detailOpen {
		return s.detailKey(k)
	}
	if l, n := s.list(); l.Key(k, n) {
		s.invalidate()
		return nil
	}
	switch k {
	case "m":
		s.pane = 1 - s.pane
		s.note = ""
		s.invalidate()
	case "r":
		s.note = ""
		return s.Refresh()
	case "enter":
		return s.openDetail()
	case "o":
		if s.pane == panePlugins {
			return s.checkOutdated()
		}
	case "a":
		if s.guard() {
			s.typing, s.inlineE, s.note = s.pane, "", ""
			s.buf.Clear()
		}
	case "U":
		if s.pane == panePlugins && s.guard() {
			return s.emit(actions.PluginUpgradeAll(), nil)
		}
	case "e", "x", "u":
		return s.mutate(k)
	}
	return nil
}

// capText masks then caps stored command output (mask.Capped: never the
// other way round, cutting first could leave a prefix of a secret), with
// the truncation marker as its last line.
func capText(s string) string {
	t, trunc := mask.Capped(s, ui.MaxOutput)
	if trunc {
		t += "\n… " + ui.TruncatedMarker
	}
	return t
}
