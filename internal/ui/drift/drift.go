// Package drift is the read-only drift screen: it lists `agentsync status
// --json` grouped by agent, and for one item shows the masked diff and the
// provenance from `diff` / `explain`. It never writes anything; resolving
// drift is a separate screen.
package drift

import (
	"context"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/model"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

const (
	kindDiff    = "diff"
	kindExplain = "explain"
)

// Screen is the drift tab.
type Screen struct {
	base  context.Context
	limit time.Duration // bound on one local read (ui.Deps.Limits().Local())
	run   runner.Runner
	reads ui.Rounds // the status read; superseded rounds are cancelled

	loaded bool
	rep    model.StatusReport
	note   string // why status is unavailable, display-safe
	all    bool   // filter: all items vs needs-attention only
	list   ui.List
	// rows is the rendered list for (rep, all), built once when either
	// changes (rebuild) rather than on every View: masking 5,000 paths per
	// key press is what made the screen lag.
	rows *rendered

	detail   bool
	dreads   ui.Rounds // the diff and explain reads of the open item
	item     model.StatusItem
	diffable bool
	hunks    []model.Hunk // already masked
	diffNote string
	diffDone bool
	prov     *model.Provenance
	provNote string
	provDone bool
	dscroll  ui.Scroll

	legend     bool
	lreads     ui.Rounds // the legend read
	legendText string
	legendNote string

	width, height int
}

// statusMsg carries a parsed status result (note != "": unavailable).
type statusMsg struct {
	gen  int
	rep  model.StatusReport
	note string
}

// detailMsg carries one half of the detail pane. hunks are masked already.
type detailMsg struct {
	gen   int
	kind  string
	hunks []model.Hunk
	prov  *model.Provenance
	note  string
}

// legendMsg carries the `status --legend` text.
type legendMsg struct {
	gen  int
	text string
	note string
}

// New returns the drift screen.
func New(d ui.Deps) *Screen {
	return &Screen{base: ui.BaseCtx(d), limit: d.Limits().Local(), run: d.Runner, all: true, width: 80, height: 20}
}

// Title implements ui.Screen.
func (s *Screen) Title() string { return "Drift" }

// Init is exactly Refresh.
func (s *Screen) Init() tea.Cmd { return s.Refresh() }

// SetScope implements ui.ScopeSetter: swap the runner and forget loaded state.
func (s *Screen) SetScope(r runner.Runner, _ runner.Scope, _ string) {
	s.run = r
	s.reads.Supersede()
	s.dreads.Supersede()
	s.lreads.Supersede()
	s.loaded, s.rep, s.note, s.list = false, model.StatusReport{}, "", ui.List{}
	s.detail, s.legend = false, false
	s.rebuild()
}

// Shutdown implements ui.Shutdowner.
func (s *Screen) Shutdown() {
	s.reads.Cancel()
	s.dreads.Cancel()
	s.lreads.Cancel()
}

// CapturesKeys implements ui.KeyCapturer: the screen has no text inputs.
func (s *Screen) CapturesKeys() bool { return false }

// Refresh re-reads the status report, cancelling the previous read.
func (s *Screen) Refresh() tea.Cmd {
	gen, ctx := s.reads.Next(s.base)
	r, limit := s.run, s.limit
	return func() tea.Msg { return readStatus(ctx, gen, r, limit) }
}

func readStatus(ctx context.Context, gen int, r runner.Runner, timeout time.Duration) statusMsg {
	res, err := ui.Read(ctx, r, timeout, "status", ui.JSONFlag)
	if err != nil {
		return statusMsg{gen: gen, note: ui.ReadNote("status", res, err)}
	}
	// Exit 2 may still carry a valid report; any other non-zero exit is a
	// failure even if stdout parses (ParseStatus ignores unknown fields).
	rep, perr := model.ParseStatus(res.Stdout)
	if perr != nil || (res.ExitCode != 0 && res.ExitCode != 2) {
		if res.ExitCode == 0 {
			return statusMsg{gen: gen, note: ui.Unreadable("status", res, perr)}
		}
		return statusMsg{gen: gen, note: ui.ReadNote("status", res, nil)}
	}
	return statusMsg{gen: gen, rep: rep}
}

// Update implements tea.Model.
func (s *Screen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
	case statusMsg:
		if s.reads.Current(msg.gen) {
			s.loaded, s.note = true, msg.note
			if msg.note == "" { // a failed refresh keeps the last good report
				s.rep = msg.rep
				s.rebuild()
			}
			s.clamp()
		}
	case detailMsg:
		if s.dreads.Current(msg.gen) && s.detail {
			s.applyDetail(msg)
		}
	case legendMsg:
		if s.lreads.Current(msg.gen) && s.legend {
			s.legendText, s.legendNote = msg.text, msg.note
		}
	case tea.KeyPressMsg:
		return s, s.key(msg.String())
	}
	return s, nil
}

func (s *Screen) applyDetail(m detailMsg) {
	switch m.kind {
	case kindDiff:
		s.hunks, s.diffNote, s.diffDone = m.hunks, m.note, true
	case kindExplain:
		s.prov, s.provNote, s.provDone = m.prov, m.note, true
	}
}

func (s *Screen) key(k string) tea.Cmd {
	switch {
	case s.legend:
		if k == "esc" || k == "g" || k == "q" {
			s.legend = false
			s.lreads.Supersede()
		}
	case s.detail:
		s.detailKey(k)
	default:
		return s.listKey(k)
	}
	return nil
}

func (s *Screen) listKey(k string) tea.Cmd {
	if s.list.Key(k, len(s.items())) {
		return nil
	}
	switch k {
	case "f":
		s.all = !s.all
		s.rebuild()
		s.clamp()
	case "r":
		return s.Refresh()
	case "g":
		return s.openLegend()
	case "enter":
		return s.openDetail()
	}
	return nil
}

func (s *Screen) detailKey(k string) {
	switch k {
	case "esc", "enter", "q":
		s.detail = false
		s.dreads.Supersede()
	default:
		s.dscroll.Key(k, len(s.detailBody()))
	}
}

// items returns the visible items in display order.
func (s *Screen) items() []model.StatusItem {
	if s.rows == nil {
		s.rebuild()
	}
	return s.rows.items
}

func (s *Screen) visible(a model.AgentStatus) []model.StatusItem {
	if s.all {
		return a.Items
	}
	var out []model.StatusItem
	for _, it := range a.Items {
		if model.NeedsAttention(it.State) {
			out = append(out, it)
		}
	}
	return out
}

func (s *Screen) selected() (model.StatusItem, bool) {
	its := s.items()
	if s.list.Cursor < 0 || s.list.Cursor >= len(its) {
		return model.StatusItem{}, false
	}
	return its[s.list.Cursor], true
}

func (s *Screen) clamp() { s.list.Clamp(len(s.items())) }

func (s *Screen) openLegend() tea.Cmd {
	s.legend, s.legendText, s.legendNote = true, "", ""
	gen, ctx := s.lreads.Next(s.base)
	r, limit := s.run, s.limit
	return func() tea.Msg {
		res, err := ui.Read(ctx, r, limit, "status", "--legend")
		if err != nil {
			return legendMsg{gen: gen, note: "legend unavailable: " + mask.FirstLine(err.Error())}
		}
		if strings.TrimSpace(res.Stdout) == "" {
			return legendMsg{gen: gen, note: "legend unavailable: " + mask.FirstLine(res.Stderr)}
		}
		return legendMsg{gen: gen, text: mask.Text(res.Stdout)}
	}
}

// safeArg reports whether v may be passed to the CLI as (part of) one argv
// element: non-empty and free of control characters and format runes.
func safeArg(v string) bool {
	if v == "" || mask.Sanitize(v) != v || strings.ContainsAny(v, "\n\t") {
		return false
	}
	return strings.IndexFunc(v, unicode.IsControl) < 0
}

// diffable: only absolute, clean paths and clean pointers reach the CLI, so
// neither can be read as a flag or smuggle control bytes.
func diffable(it model.StatusItem) bool {
	return strings.HasPrefix(it.Path, "/") && !strings.Contains(it.Path, "#") && safeArg(it.Path) && safeArg(it.Pointer)
}

func (s *Screen) openDetail() tea.Cmd {
	it, ok := s.selected()
	if !ok {
		return nil
	}
	s.detail, s.item, s.dscroll = true, it, ui.Scroll{}
	s.hunks, s.diffNote, s.diffDone = nil, "", false
	s.prov, s.provNote, s.provDone = nil, "", false
	s.diffable = diffable(it)
	gen, ctx := s.dreads.Next(s.base)
	if !s.diffable {
		return nil
	}
	r, limit := s.run, s.limit
	diff := func() tea.Msg { return loadDiff(ctx, gen, r, limit, it) }
	explain := func() tea.Msg { return loadExplain(ctx, gen, r, limit, it) }
	return tea.Batch(diff, explain)
}

func loadDiff(ctx context.Context, gen int, r runner.Runner, timeout time.Duration, it model.StatusItem) detailMsg {
	m := detailMsg{gen: gen, kind: kindDiff}
	res, err := ui.Read(ctx, r, timeout, "diff", it.Path, ui.JSONFlag)
	if err != nil {
		m.note = "diff unavailable: " + mask.FirstLine(err.Error())
		return m
	}
	hunks, perr := model.ParseDiff(res.Stdout)
	if perr != nil {
		m.note = "diff unavailable: " + mask.FirstLine(res.Stderr)
		return m
	}
	for _, h := range hunks {
		if h.Pointer == it.Pointer && h.Path == it.Path {
			m.hunks = append(m.hunks, mask.Hunk(h))
		}
	}
	return m
}

func loadExplain(ctx context.Context, gen int, r runner.Runner, timeout time.Duration, it model.StatusItem) detailMsg {
	m := detailMsg{gen: gen, kind: kindExplain}
	res, err := ui.Read(ctx, r, timeout, "explain", it.Path+"#"+it.Pointer, ui.JSONFlag)
	if err != nil {
		m.note = "provenance unavailable: " + mask.FirstLine(err.Error())
		return m
	}
	p, perr := model.ParseExplain(res.Stdout)
	if perr != nil {
		m.note = "provenance unavailable: " + mask.FirstLine(res.Stderr)
		return m
	}
	m.prov = &p
	return m
}
