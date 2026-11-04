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
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

// readTimeout bounds one read-only CLI call.
const readTimeout = 30 * time.Second

// maxOutput caps each stored (masked) section.
const maxOutput = 64 << 10

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
	d     ui.Deps
	run   runner.Runner
	scope runner.Scope
	gen   int // refresh generation; older results are dropped

	doctor, check section
	cancel        context.CancelFunc // the current round; superseded rounds are cancelled
	scroll        int

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
	return &Screen{d: d, run: d.Runner, scope: d.Scope, width: 80, height: 20}
}

// Title implements ui.Screen.
func (s *Screen) Title() string { return "Health" }

// Init is exactly Refresh.
func (s *Screen) Init() tea.Cmd { return s.Refresh() }

// SetScope implements ui.ScopeAware: swap the runner and forget both outputs.
func (s *Screen) SetScope(r runner.Runner, sc runner.Scope, _ string) {
	s.run, s.scope = r, sc
	s.gen++
	s.Shutdown()
	s.doctor, s.check, s.scroll = section{}, section{}, 0
}

// CapturesKeys implements ui.KeyCapturer: the screen has no text inputs.
func (s *Screen) CapturesKeys() bool { return false }

// Refresh re-runs doctor and check concurrently through the Runner.
func (s *Screen) Refresh() tea.Cmd {
	s.gen++
	s.Shutdown() // the superseded round's children die now, not after readTimeout
	gen, r := s.gen, s.run
	ctx, cancel := context.WithCancel(ui.BaseCtx(s.d))
	s.cancel = cancel
	return tea.Batch(
		func() tea.Msg { return readOne(ctx, gen, r, "doctor", actions.Doctor().Argv) },
		func() tea.Msg { return readOne(ctx, gen, r, "check", actions.Check().Argv) },
	)
}

func readOne(parent context.Context, gen int, r runner.Runner, kind string, argv []string) resultMsg {
	m := resultMsg{gen: gen, kind: kind}
	if r == nil {
		m.note = kind + " unavailable: no runner"
		return m
	}
	ctx, cancel := context.WithTimeout(parent, readTimeout)
	defer cancel()
	res, err := r.Run(ctx, argv...)
	if errors.Is(err, context.Canceled) {
		m.superseded = true // cancelled by a newer round, a scope switch or quit: not a failure
		return m
	}
	if err != nil {
		m.note = kind + " unavailable: " + firstLine(clean(err.Error()))
		return m
	}
	// Problems are reported on stderr as often as on stdout: show both.
	text := clean(res.Stdout)
	if e := clean(res.Stderr); strings.TrimSpace(e) != "" {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += e
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
	case resultMsg:
		if msg.gen != s.gen || msg.superseded {
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
		s.scroll = min(s.scroll, s.maxScroll())
	case tea.KeyPressMsg:
		return s, s.key(msg.String())
	}
	return s, nil
}

func (s *Screen) key(k string) tea.Cmd {
	page := max(s.avail()/2, 1)
	switch k {
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

// Shutdown implements ui.Shutdowner: it cancels the in-flight round (also
// used by Refresh and SetScope to supersede it).
func (s *Screen) Shutdown() {
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
}
