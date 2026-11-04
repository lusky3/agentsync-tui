// Package drift is the read-only drift screen: it lists `agentsync status
// --json` grouped by agent, and for one item shows the masked diff and the
// provenance from `diff` / `explain`. It never writes anything; resolving
// drift is a separate screen.
package drift

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/model"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

// readTimeout bounds one read-only CLI call.
const readTimeout = 30 * time.Second

const (
	kindDiff    = "diff"
	kindExplain = "explain"
)

// Screen is the drift tab.
type Screen struct {
	d     ui.Deps
	run   runner.Runner
	scope runner.Scope
	gen   int // refresh generation; older results are dropped

	loaded bool
	rep    model.StatusReport
	note   string // why status is unavailable, display-safe
	all    bool   // filter: all items vs needs-attention only
	cursor int    // index into items()

	detail   bool
	seq      int // detail generation; bumped on every open/close
	item     model.StatusItem
	diffable bool
	hunks    []model.Hunk // already masked
	diffNote string
	diffDone bool
	prov     *model.Provenance
	provNote string
	provDone bool
	scroll   int

	lseq       int // legend generation; bumped on every open/close/scope switch
	legend     bool
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
	seq   int
	kind  string
	hunks []model.Hunk
	prov  *model.Provenance
	note  string
}

// legendMsg carries the `status --legend` text.
type legendMsg struct {
	seq  int
	text string
	note string
}

// New returns the drift screen.
func New(d ui.Deps) *Screen {
	return &Screen{d: d, run: d.Runner, scope: d.Scope, all: true, width: 80, height: 20}
}

// Title implements ui.Screen.
func (s *Screen) Title() string { return "Drift" }

// Init is exactly Refresh.
func (s *Screen) Init() tea.Cmd { return s.Refresh() }

// SetScope implements ui.ScopeAware: swap the runner and forget loaded state.
func (s *Screen) SetScope(r runner.Runner, sc runner.Scope, _ string) {
	s.run, s.scope = r, sc
	s.gen++
	s.seq++
	s.lseq++
	s.loaded, s.rep, s.note, s.cursor = false, model.StatusReport{}, "", 0
	s.detail, s.legend = false, false
}

// CapturesKeys implements ui.KeyCapturer: the screen has no text inputs.
func (s *Screen) CapturesKeys() bool { return false }

// Refresh re-reads the status report.
func (s *Screen) Refresh() tea.Cmd {
	s.gen++
	gen, r, base := s.gen, s.run, ui.BaseCtx(s.d)
	return func() tea.Msg { return readStatus(base, gen, r) }
}

func readStatus(base context.Context, gen int, r runner.Runner) statusMsg {
	res, err := call(base, r, "status", "--json")
	if err != nil {
		return statusMsg{gen: gen, note: "status failed: " + mask.FirstLine(mask.Text(err.Error()))}
	}
	// Exit 2 may still carry a valid report; any other non-zero exit is a
	// failure even if stdout parses (ParseStatus ignores unknown fields).
	rep, perr := model.ParseStatus(res.Stdout)
	if perr != nil || (res.ExitCode != 0 && res.ExitCode != 2) {
		msg := "status failed"
		if res.ExitCode != 0 {
			msg += " (exit " + strconv.Itoa(res.ExitCode) + ")"
		}
		if l := mask.FirstLine(mask.Text(res.Stderr)); l != "" {
			msg += ": " + l
		}
		return statusMsg{gen: gen, note: msg}
	}
	return statusMsg{gen: gen, rep: rep}
}

func call(base context.Context, r runner.Runner, args ...string) (runner.Result, error) {
	if r == nil {
		return runner.Result{}, errors.New("no runner")
	}
	ctx, cancel := context.WithTimeout(base, readTimeout)
	defer cancel()
	return r.Run(ctx, args...)
}

// Update implements tea.Model.
func (s *Screen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
	case statusMsg:
		if msg.gen == s.gen {
			s.loaded, s.note = true, msg.note
			if msg.note == "" { // a failed refresh keeps the last good report
				s.rep = msg.rep
			}
			s.clamp()
		}
	case detailMsg:
		if msg.seq == s.seq && s.detail {
			s.applyDetail(msg)
		}
	case legendMsg:
		if msg.seq == s.lseq && s.legend {
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
			s.lseq++
		}
	case s.detail:
		s.detailKey(k)
	default:
		return s.listKey(k)
	}
	return nil
}

func (s *Screen) listKey(k string) tea.Cmd {
	n := len(s.items())
	switch k {
	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
		}
	case "down", "j":
		if s.cursor < n-1 {
			s.cursor++
		}
	case "f":
		s.all = !s.all
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
	page := max(s.detailAvail()/2, 1)
	switch k {
	case "esc", "enter", "q":
		s.detail = false
		s.seq++
	case "ctrl+d", "pgdown":
		s.scroll += page
	case "ctrl+u", "pgup":
		s.scroll = max(s.scroll-page, 0)
	case "down", "j":
		s.scroll++
	case "up", "k":
		s.scroll = max(s.scroll-1, 0)
	}
	s.scroll = min(s.scroll, s.maxScroll())
}

// flagged classes need attention: the destination differs from canonical.
var flagged = map[string]bool{"drift": true, "conflict": true, "foreign-collision": true, "orphan-drifted": true}

// items returns the visible items in display order.
func (s *Screen) items() []model.StatusItem {
	var out []model.StatusItem
	for _, a := range s.rep.Agents {
		out = append(out, s.visible(a)...)
	}
	return out
}

func (s *Screen) visible(a model.AgentStatus) []model.StatusItem {
	if s.all {
		return a.Items
	}
	var out []model.StatusItem
	for _, it := range a.Items {
		if flagged[it.State] {
			out = append(out, it)
		}
	}
	return out
}

func (s *Screen) selected() (model.StatusItem, bool) {
	its := s.items()
	if s.cursor < 0 || s.cursor >= len(its) {
		return model.StatusItem{}, false
	}
	return its[s.cursor], true
}

func (s *Screen) clamp() {
	s.cursor = max(min(s.cursor, len(s.items())-1), 0)
}

func (s *Screen) openLegend() tea.Cmd {
	s.lseq++
	s.legend, s.legendText, s.legendNote = true, "", ""
	seq, r, base := s.lseq, s.run, ui.BaseCtx(s.d)
	return func() tea.Msg {
		res, err := call(base, r, "status", "--legend")
		if err != nil {
			return legendMsg{seq: seq, note: "legend unavailable: " + mask.FirstLine(mask.Text(err.Error()))}
		}
		if strings.TrimSpace(res.Stdout) == "" {
			return legendMsg{seq: seq, note: "legend unavailable: " + mask.FirstLine(mask.Text(res.Stderr))}
		}
		return legendMsg{seq: seq, text: mask.Text(res.Stdout)}
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
	s.seq++
	s.detail, s.item, s.scroll = true, it, 0
	s.hunks, s.diffNote, s.diffDone = nil, "", false
	s.prov, s.provNote, s.provDone = nil, "", false
	s.diffable = diffable(it)
	if !s.diffable {
		return nil
	}
	seq, r, base := s.seq, s.run, ui.BaseCtx(s.d)
	diff := func() tea.Msg { return loadDiff(base, seq, r, it) }
	explain := func() tea.Msg { return loadExplain(base, seq, r, it) }
	return tea.Batch(diff, explain)
}

func loadDiff(base context.Context, seq int, r runner.Runner, it model.StatusItem) detailMsg {
	m := detailMsg{seq: seq, kind: kindDiff}
	res, err := call(base, r, "diff", it.Path, "--json")
	if err != nil {
		m.note = "diff unavailable: " + mask.FirstLine(mask.Text(err.Error()))
		return m
	}
	hunks, perr := model.ParseDiff(res.Stdout)
	if perr != nil {
		m.note = "diff unavailable: " + mask.FirstLine(mask.Text(res.Stderr))
		return m
	}
	for _, h := range hunks {
		if h.Pointer == it.Pointer && h.Path == it.Path {
			m.hunks = append(m.hunks, mask.Hunk(h))
		}
	}
	return m
}

func loadExplain(base context.Context, seq int, r runner.Runner, it model.StatusItem) detailMsg {
	m := detailMsg{seq: seq, kind: kindExplain}
	res, err := call(base, r, "explain", it.Path+"#"+it.Pointer, "--json")
	if err != nil {
		m.note = "provenance unavailable: " + mask.FirstLine(mask.Text(err.Error()))
		return m
	}
	p, perr := model.ParseExplain(res.Stdout)
	if perr != nil {
		m.note = "provenance unavailable: " + mask.FirstLine(mask.Text(res.Stderr))
		return m
	}
	m.prov = &p
	return m
}
