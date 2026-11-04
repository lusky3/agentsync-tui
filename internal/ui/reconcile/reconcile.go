// Package reconcile is the drift-resolution hand-off screen. It shows the
// report of `agentsync reconcile --auto-safe` (resolve nothing, report every
// item that needs a human) and hands the terminal to agentsync's own
// interactive `reconcile` for the actual resolution.
//
// The TUI deliberately offers NO write-back/override of its own and NO bulk
// resolution: upstream agentsync #239 (override writes ${env:…}/${secret:…}
// literally into live files), #255 (bulk write-back includes foreign
// collisions) and #256 (reversed diff labels) make those unsafe to wrap. The
// only action this screen emits is actions.Reconcile(), which is Interactive
// and never carries an --auto-* flag.
//
// The report is read directly through the Runner, not via ui.DoMsg: the shell
// refreshes the active screen after every run, so a Refresh that emitted a
// DoMsg would loop forever.
package reconcile

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

// readTimeout bounds one report run.
const readTimeout = 30 * time.Second

// maxOutput caps the stored (masked) report.
const maxOutput = 64 << 10

// Screen is the reconcile tab.
type Screen struct {
	d     ui.Deps
	run   runner.Runner
	scope runner.Scope
	gen   int // refresh generation; older results are dropped

	loaded bool
	report string // masked + sanitised, capped
	trunc  bool
	exit   int    // exit code of the last good report
	note   string // why the last refresh failed, display-safe
	handed string // outcome of the last interactive handover, display-safe
	scroll int

	width, height int
}

// reportMsg carries one finished report run. text is masked already.
type reportMsg struct {
	gen   int
	ok    bool
	text  string
	trunc bool
	exit  int
	note  string
}

// New returns the reconcile screen.
func New(d ui.Deps) *Screen {
	return &Screen{d: d, run: d.Runner, scope: d.Scope, width: 80, height: 20}
}

// Title implements ui.Screen.
func (s *Screen) Title() string { return "Reconcile" }

// Init is exactly Refresh.
func (s *Screen) Init() tea.Cmd { return s.Refresh() }

// SetScope implements ui.ScopeAware: swap the runner and forget the report.
func (s *Screen) SetScope(r runner.Runner, sc runner.Scope, _ string) {
	s.run, s.scope = r, sc
	s.gen++
	s.loaded, s.report, s.trunc, s.exit, s.note, s.handed, s.scroll = false, "", false, 0, "", "", 0
}

// CapturesKeys implements ui.KeyCapturer: the screen has no text inputs.
func (s *Screen) CapturesKeys() bool { return false }

// Refresh re-runs the read-only report through the Runner (never a DoMsg).
func (s *Screen) Refresh() tea.Cmd {
	s.gen++
	gen, r, base := s.gen, s.run, ui.BaseCtx(s.d)
	return func() tea.Msg { return readReport(base, gen, r) }
}

func readReport(base context.Context, gen int, r runner.Runner) reportMsg {
	m := reportMsg{gen: gen}
	if r == nil {
		m.note = "reconcile report unavailable: no runner"
		return m
	}
	ctx, cancel := context.WithTimeout(base, readTimeout)
	defer cancel()
	res, err := r.Run(ctx, actions.ReconcileReport().Argv...)
	if err != nil {
		m.note = "reconcile report unavailable: " + firstLine(clean(err.Error()))
		return m
	}
	text := clean(res.Stdout)
	if strings.TrimSpace(text) == "" {
		// Nothing to show: a non-zero exit is a failure, exit 0 an empty report.
		if res.ExitCode != 0 {
			m.note = "reconcile report failed (exit " + strconv.Itoa(res.ExitCode) + ")"
			if l := firstLine(clean(res.Stderr)); l != "" {
				m.note += ": " + l
			}
			return m
		}
	}
	if len(text) > maxOutput {
		text, m.trunc = cutRunes(text, maxOutput), true
	}
	m.ok, m.text, m.exit = true, text, res.ExitCode
	return m
}

// Update implements tea.Model.
func (s *Screen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
		s.scroll = min(s.scroll, s.maxScroll())
	case reportMsg:
		if msg.gen != s.gen {
			return s, nil
		}
		s.loaded = true
		s.note = msg.note
		if msg.ok { // a failed refresh keeps the last good report
			s.report, s.trunc, s.exit = msg.text, msg.trunc, msg.exit
			s.scroll = min(s.scroll, s.maxScroll())
		}
	case ui.RunMsg:
		s.onRun(msg)
	case tea.KeyPressMsg:
		return s, s.key(msg.String())
	}
	return s, nil
}

// onRun notes how the interactive handover ended; the shell refreshes the
// screen afterwards, which re-runs the report.
func (s *Screen) onRun(m ui.RunMsg) {
	if !m.Action.Interactive || strings.Join(m.Action.Argv, " ") != "reconcile" {
		return
	}
	switch {
	case errors.Is(m.Err, context.Canceled):
		s.handed = "interactive reconcile cancelled"
	case m.Err != nil:
		s.handed = "interactive reconcile failed: " + firstLine(clean(m.Err.Error()))
	case m.Result.ExitCode != 0:
		s.handed = "interactive reconcile exited " + strconv.Itoa(m.Result.ExitCode)
	default:
		s.handed = "interactive reconcile finished"
	}
}

func (s *Screen) key(k string) tea.Cmd {
	page := max(s.avail()/2, 1)
	switch k {
	case "x":
		act := actions.Reconcile()
		return func() tea.Msg { return ui.DoMsg{Action: act} }
	case "r":
		return s.Refresh()
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
	return nil
}

// clean masks (before any truncation, so a cut cannot split a secret) then
// sanitises.
func clean(v string) string { return mask.Sanitize(mask.Text(v)) }

func firstLine(v string) string {
	for _, l := range strings.Split(v, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

func cutRunes(v string, n int) string {
	for n > 0 && n < len(v) && v[n]&0xC0 == 0x80 {
		n--
	}
	return v[:n]
}
