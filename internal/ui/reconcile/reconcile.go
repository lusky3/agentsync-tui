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
// DoMsg would loop forever. `reconcile --auto-safe` takes agentsync's global
// write lock, so the shell only refreshes this screen while it is the active
// tab (lazy screens), never at startup or on every scope switch.
package reconcile

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

// Screen is the reconcile tab.
type Screen struct {
	base  context.Context
	limit time.Duration // bound on one read (ui.Deps.Limits().Read)
	run   runner.Runner
	reads ui.Rounds // the report read; superseded rounds are cancelled

	loaded bool
	report string // masked + sanitised, capped
	trunc  bool
	exit   int    // exit code of the last good report
	note   string // why the last refresh failed, display-safe
	handed string // outcome of the last interactive handover, display-safe
	scroll ui.Scroll

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
	return &Screen{base: ui.BaseCtx(d), limit: d.Limits().Read, run: d.Runner, width: 80, height: 20}
}

// Title implements ui.Screen.
func (s *Screen) Title() string { return "Reconcile" }

// Init is exactly Refresh.
func (s *Screen) Init() tea.Cmd { return s.Refresh() }

// SetScope implements ui.ScopeAware: swap the runner and forget the report.
func (s *Screen) SetScope(r runner.Runner, _ runner.Scope, _ string) {
	s.run = r
	s.reads.Supersede()
	s.loaded, s.report, s.trunc, s.exit, s.note, s.handed, s.scroll = false, "", false, 0, "", "", ui.Scroll{}
}

// Shutdown implements ui.Shutdowner.
func (s *Screen) Shutdown() { s.reads.Cancel() }

// CapturesKeys implements ui.KeyCapturer: the screen has no text inputs.
func (s *Screen) CapturesKeys() bool { return false }

// Refresh re-runs the read-only report through the Runner (never a DoMsg),
// cancelling the previous run.
func (s *Screen) Refresh() tea.Cmd {
	gen, ctx := s.reads.Next(s.base)
	r, limit := s.run, s.limit
	return func() tea.Msg { return readReport(ctx, gen, r, limit) }
}

func readReport(ctx context.Context, gen int, r runner.Runner, timeout time.Duration) reportMsg {
	m := reportMsg{gen: gen}
	res, err := ui.Read(ctx, r, timeout, actions.ReconcileReport().Argv...)
	if err != nil {
		m.note = ui.ReadNote("reconcile report", res, err)
		return m
	}
	text, trunc := mask.Capped(res.Stdout, ui.MaxOutput)
	if strings.TrimSpace(text) == "" && res.ExitCode != 0 {
		// Nothing to show: a non-zero exit is a failure, exit 0 an empty report.
		m.note = ui.ReadNote("reconcile report", res, nil)
		return m
	}
	m.ok, m.text, m.trunc, m.exit = true, text, trunc, res.ExitCode
	return m
}

// Update implements tea.Model.
func (s *Screen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
	case reportMsg:
		if !s.reads.Current(msg.gen) {
			return s, nil
		}
		s.loaded = true
		s.note = msg.note
		if msg.ok { // a failed refresh keeps the last good report
			s.report, s.trunc, s.exit = msg.text, msg.trunc, msg.exit
			s.scroll.Clamp(len(s.lines()))
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
	if !m.Action.Interactive || !slices.Equal(m.Action.Argv, actions.Reconcile().Argv) {
		return
	}
	switch {
	case errors.Is(m.Err, context.Canceled):
		s.handed = "interactive reconcile cancelled"
	case m.Err != nil:
		s.handed = "interactive reconcile failed: " + mask.FirstLine(m.Err.Error())
	case m.Result.ExitCode != 0:
		s.handed = "interactive reconcile exited " + strconv.Itoa(m.Result.ExitCode)
	default:
		s.handed = "interactive reconcile finished"
	}
}

func (s *Screen) key(k string) tea.Cmd {
	if s.scroll.Key(k, len(s.lines())) {
		return nil
	}
	switch k {
	case "x":
		return ui.Do(actions.Reconcile())
	case "r":
		return s.Refresh()
	}
	return nil
}
