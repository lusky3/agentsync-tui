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
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/model"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

const (
	listTimeout    = 30 * time.Second
	verifyTimeout  = 5 * time.Second
	explainTimeout = 15 * time.Second
	// maxDetail caps stored `plugin explain` / outdated text.
	maxDetail = 64 << 10

	staleNote = "list changed — refreshed"
)

const (
	panePlugins = iota
	paneMarkets
)

// Screen is the plugins tab.
type Screen struct {
	d     ui.Deps
	run   runner.Runner
	scope runner.Scope
	gen   int // refresh generation; older list results are dropped

	pane             int
	plugins          []model.Plugin
	markets          []model.Marketplace // URL already masked
	pLoaded, mLoaded bool
	pNote, mNote     string // why the last read failed (display-safe)
	pCur, mCur       int
	note             string // last screen message, display-safe

	// outdated is the on-demand `plugin outdated` result (network).
	outdated    map[string]model.Outdated
	outdatedRaw string // masked, capped text of the last check ("" = never)
	oChecking   bool
	oreq        int

	// detail overlay (explain output or marketplace summary).
	detailOpen bool
	detail     string
	detailFor  string
	detailBusy bool
	dreq       int
	scroll     int

	// text field for add.
	typing  int // -1 none; else the pane being added to
	buf     string
	inlineE string

	// pre-mutation staleness check.
	verifying bool
	vreq      int

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

type verifyMsg struct {
	req  int
	key  string
	pane int
	p    model.Plugin
	m    model.Marketplace
	pf   pluginsMsg
	mf   marketsMsg
}

type outdatedMsg struct {
	req     int
	list    []model.Outdated
	raw     string
	note    string
	parsed  bool
	checked bool
}

type explainMsg struct {
	req  int
	name string
	text string
	note string
}

// New returns the plugins screen.
func New(d ui.Deps) *Screen {
	return &Screen{d: d, run: d.Runner, scope: d.Scope, typing: -1, width: 80, height: 24}
}

func (s *Screen) Title() string { return "Plugins" }

// Init is exactly Refresh.
func (s *Screen) Init() tea.Cmd { return s.Refresh() }

// CapturesKeys implements ui.KeyCapturer: true while the add field is open.
func (s *Screen) CapturesKeys() bool { return s.typing >= 0 }

// SetScope implements ui.ScopeAware.
func (s *Screen) SetScope(r runner.Runner, sc runner.Scope, _ string) {
	s.run, s.scope = r, sc
	s.outdated, s.outdatedRaw, s.oChecking = nil, "", false
	s.oreq++
	s.closeDetail()
	s.invalidate()
}

func (s *Screen) invalidate() {
	if s.verifying {
		s.verifying, s.note = false, ""
	}
	s.vreq++
}

func (s *Screen) closeDetail() {
	s.detailOpen, s.detailBusy, s.detail, s.scroll = false, false, "", 0
	s.dreq++
}

// Refresh re-reads both lists concurrently. It never runs `plugin outdated`.
func (s *Screen) Refresh() tea.Cmd {
	s.gen++
	s.invalidate()
	gen, r, base := s.gen, s.run, ui.BaseCtx(s.d)
	return tea.Batch(
		func() tea.Msg { return readPlugins(base, gen, r, listTimeout) },
		func() tea.Msg { return readMarkets(base, gen, r, listTimeout) },
	)
}

// runRead runs one read-only command and returns stdout, or a display-safe
// failure note.
func runRead(base context.Context, r runner.Runner, timeout time.Duration, args ...string) (string, string) {
	if r == nil {
		return "", "no runner"
	}
	ctx, cancel := context.WithCancel(base)
	if timeout > 0 {
		cancel()
		ctx, cancel = context.WithTimeout(base, timeout)
	}
	defer cancel()
	name := strings.Join(args[:min(len(args), 2)], " ")
	res, err := r.Run(ctx, args...)
	if err != nil {
		return "", name + ": " + mask.OneLine(mask.Text(err.Error()))
	}
	if res.ExitCode != 0 {
		n := name + " exited " + strconv.Itoa(res.ExitCode)
		if l := firstLine(res.Stderr); l != "" {
			n += ": " + l
		}
		return "", n
	}
	return res.Stdout, ""
}

func readPlugins(base context.Context, gen int, r runner.Runner, timeout time.Duration) pluginsMsg {
	out, note := runRead(base, r, timeout, "plugin", "list")
	if note != "" {
		return pluginsMsg{gen: gen, note: note}
	}
	list, err := model.ParsePluginList(out)
	if err != nil {
		return pluginsMsg{gen: gen, note: "plugin list output unreadable"}
	}
	return pluginsMsg{gen: gen, plugins: list}
}

func readMarkets(base context.Context, gen int, r runner.Runner, timeout time.Duration) marketsMsg {
	out, note := runRead(base, r, timeout, "marketplace", "list")
	if note != "" {
		return marketsMsg{gen: gen, note: note}
	}
	list, err := model.ParseMarketplaces(out)
	if err != nil {
		return marketsMsg{gen: gen, note: "marketplace list output unreadable"}
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
		if msg.gen == s.gen {
			s.applyPlugins(msg)
		}
	case marketsMsg:
		if msg.gen == s.gen {
			s.applyMarkets(msg)
		}
	case verifyMsg:
		return s, s.verified(msg)
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
	s.pCur = max(min(s.pCur, len(s.plugins)-1), 0)
}

func (s *Screen) applyMarkets(m marketsMsg) {
	s.mLoaded = true
	if m.note != "" {
		s.mNote = m.note
		return
	}
	s.mNote, s.markets = "", m.markets
	s.mCur = max(min(s.mCur, len(s.markets)-1), 0)
}

func (s *Screen) selPlugin() (model.Plugin, bool) {
	if s.pCur < 0 || s.pCur >= len(s.plugins) {
		return model.Plugin{}, false
	}
	return s.plugins[s.pCur], true
}

func (s *Screen) selMarket() (model.Marketplace, bool) {
	if s.mCur < 0 || s.mCur >= len(s.markets) {
		return model.Marketplace{}, false
	}
	return s.markets[s.mCur], true
}

func (s *Screen) key(k string) tea.Cmd {
	if s.detailOpen {
		return s.detailKey(k)
	}
	switch k {
	case "m":
		s.pane = 1 - s.pane
		s.note = ""
		s.invalidate()
	case "up", "k":
		s.move(-1)
	case "down", "j":
		s.move(1)
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
			s.typing, s.buf, s.inlineE, s.note = s.pane, "", "", ""
		}
	case "U":
		if s.pane == panePlugins && s.guard() {
			return s.emit(actions.PluginUpgradeAll())
		}
	case "e", "x", "u":
		return s.mutate(k)
	}
	return nil
}

func (s *Screen) move(d int) {
	if s.pane == panePlugins {
		s.pCur = max(min(s.pCur+d, len(s.plugins)-1), 0)
	} else {
		s.mCur = max(min(s.mCur+d, len(s.markets)-1), 0)
	}
	s.invalidate()
}

// firstLine returns the first non-blank line of s, masked and sanitised.
func firstLine(s string) string {
	for _, l := range strings.Split(mask.Text(s), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return mask.OneLine(l)
		}
	}
	return ""
}

// capText masks then caps stored command output (never the other way
// round: cutting first could leave a prefix of a secret).
func capText(s string) string {
	s = mask.Sanitize(mask.Text(s))
	if len(s) > maxDetail {
		s = strings.ToValidUTF8(s[:maxDetail], "") + "\n… (truncated)"
	}
	return s
}
