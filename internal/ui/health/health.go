// Package health is the read-only health screen: the output of `agentsync
// doctor` (machine readiness) and `agentsync check` (config validity) in one
// scrollable page. Both are run directly through the Runner, never as a
// ui.DoMsg (the shell refreshes the active screen after every run, so a DoMsg
// would loop). A non-zero exit from either is a normal result ("problems
// found"), not an error; the summaries come from exit codes only.
package health

import (
	"context"
	"errors"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

// section is one command's last good result.
type section struct {
	loaded bool   // a result (good or failed) has arrived since the last reset
	ok     bool   // a good result is stored
	text   string // masked + sanitised, capped
	trunc  bool
	exit   int    // exit code of the stored result
	note   string // why the last refresh failed, display-safe
}

// Screen is the health tab.
type Screen struct {
	base  context.Context
	limit time.Duration // bound on one local read (ui.Deps.Limits().Local())
	run   runner.Runner
	reads ui.Rounds // the doctor+check round; superseded rounds are cancelled

	doctor, check section
	scroll        ui.Scroll

	width, height int
}

// resultMsg carries one finished command. text is masked already.
type resultMsg struct {
	gen        int
	kind       string // "doctor" | "check"
	ok         bool
	text       string
	trunc      bool
	exit       int
	note       string
	superseded bool
}

// New returns the health screen.
func New(d ui.Deps) *Screen {
	return &Screen{base: ui.BaseCtx(d), limit: d.Limits().Local(), run: d.Runner, width: 80, height: 20}
}

// Title implements ui.Screen.
func (s *Screen) Title() string { return "Health" }

// Init is exactly Refresh.
func (s *Screen) Init() tea.Cmd { return s.Refresh() }

// SetScope implements ui.ScopeAware: swap the runner and forget both outputs.
func (s *Screen) SetScope(r runner.Runner, _ runner.Scope, _ string) {
	s.run = r
	s.reads.Supersede()
	s.doctor, s.check, s.scroll = section{}, section{}, ui.Scroll{}
}

// CapturesKeys implements ui.KeyCapturer: the screen has no text inputs.
func (s *Screen) CapturesKeys() bool { return false }

// Refresh re-runs doctor and check concurrently through the Runner; the
// superseded round's children die now, not at their bound.
func (s *Screen) Refresh() tea.Cmd {
	gen, ctx := s.reads.Next(s.base)
	r, limit := s.run, s.limit
	return tea.Batch(
		func() tea.Msg { return readOne(ctx, gen, r, limit, "doctor", actions.Doctor().Argv) },
		func() tea.Msg { return readOne(ctx, gen, r, limit, "check", actions.Check().Argv) },
	)
}

func readOne(ctx context.Context, gen int, r runner.Runner, timeout time.Duration, kind string, argv []string) resultMsg {
	m := resultMsg{gen: gen, kind: kind}
	res, err := ui.Read(ctx, r, timeout, argv...)
	if errors.Is(err, context.Canceled) {
		m.superseded = true // cancelled by a newer round, a scope switch or quit: not a failure
		return m
	}
	if err != nil {
		m.note = ui.ReadNote(kind, res, err)
		return m
	}
	// Problems are reported on stderr as often as on stdout: show both.
	m.text, m.trunc = ui.CappedStreams(res.Stdout, res.Stderr, true)
	m.ok, m.exit = true, res.ExitCode
	return m
}

// Update implements tea.Model.
func (s *Screen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
	case resultMsg:
		if !s.reads.Current(msg.gen) || msg.superseded {
			return s, nil
		}
		sec := &s.doctor
		if msg.kind == "check" {
			sec = &s.check
		}
		sec.loaded, sec.note = true, msg.note
		if msg.ok { // a failed refresh keeps the last good output
			sec.ok, sec.text, sec.trunc, sec.exit = true, msg.text, msg.trunc, msg.exit
		}
		s.scroll.Clamp(len(s.lines()))
	case tea.KeyPressMsg:
		return s, s.key(msg.String())
	}
	return s, nil
}

func (s *Screen) key(k string) tea.Cmd {
	if s.scroll.Key(k, len(s.lines())) {
		return nil
	}
	if k == "r" {
		return s.Refresh()
	}
	return nil
}

// summary derives the one-word verdict from the exit code alone.
func (sec section) summary(name, bad string) string {
	switch {
	case !sec.loaded:
		return name + ": …"
	case !sec.ok:
		return name + ": unavailable"
	case sec.exit == 0:
		return name + ": OK"
	}
	return name + ": " + bad + " (exit " + strconv.Itoa(sec.exit) + ")"
}

// Shutdown implements ui.Shutdowner: it cancels the in-flight round.
func (s *Screen) Shutdown() { s.reads.Cancel() }
