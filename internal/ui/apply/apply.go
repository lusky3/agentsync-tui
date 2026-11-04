// Package apply is the apply/revert screen. Both flows are preview-first:
// the real command is only offered after a successful `--dry-run` for the
// same inputs, and it is emitted as a ui.DoMsg so the shell's confirm modal
// (typed verb for revert) stands between the user and the mutation. The
// screen never runs a mutating command itself.
//
// Command output is free text that can carry resolved values, so only the
// mask.Text-then-Sanitize form is ever stored or rendered.
package apply

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
	"github.com/lusky3/agentsync-tui/internal/ui/gate"
)

type mode int

const (
	modeApply mode = iota
	modeRevert
)

type field int

const (
	fNone   field = iota
	fTarget       // apply: agents list; revert: agent name
	fTo           // revert: --to checkpoint
)

const msgPreviewFirst = "preview first"

// Screen is the apply/revert tab.
type Screen struct {
	d     ui.Deps
	run   runner.Runner
	scope runner.Scope

	mode   mode
	focus  field
	agents string // apply: comma-separated, empty = all enabled
	target string // revert: one agent name
	all    bool   // revert: --all
	to     string // revert: --to checkpoint

	gates  [2]gate.Gate // the preview-then-run rule, one per mode
	out    [2]*ui.Output
	scroll [2]int
	msg    string

	width, height int
}

// New returns the apply/revert screen.
func New(d ui.Deps) *Screen {
	return &Screen{d: d, run: d.Runner, scope: d.Scope, width: 80, height: 20}
}

// Title implements ui.Screen.
func (s *Screen) Title() string { return "Apply" }

// Init is exactly Refresh.
func (s *Screen) Init() tea.Cmd { return s.Refresh() }

// Refresh is cheap and deliberately keeps a pending preview: the shell
// refreshes the screen after every run, including the dry run itself.
func (s *Screen) Refresh() tea.Cmd { return nil }

// SetScope implements ui.ScopeAware: swap the runner and forget previews.
func (s *Screen) SetScope(r runner.Runner, sc runner.Scope, _ string) {
	s.run, s.scope = r, sc
	for md := range s.gates {
		s.gates[md].Reset()
	}
	s.out, s.scroll, s.msg = [2]*ui.Output{}, [2]int{}, ""
}

// CapturesKeys implements ui.KeyCapturer: true while a text field has focus.
func (s *Screen) CapturesKeys() bool { return s.focus != fNone }

// pair returns the dry run and the real action for the current inputs.
func (s *Screen) pair() (actions.Previewed, error) {
	if s.mode == modeApply {
		var names []string
		for _, n := range strings.Split(s.agents, ",") {
			if n = strings.TrimSpace(n); n != "" {
				names = append(names, n)
			}
		}
		return actions.ApplyPair(names...)
	}
	agent := strings.TrimSpace(s.target)
	if s.all {
		agent = ""
	}
	return actions.RevertPair(agent, strings.TrimSpace(s.to), s.all)
}

// invalidate drops the mode's preview and any result still in flight.
func (s *Screen) invalidate() {
	s.gates[s.mode].Invalidate()
	s.msg = ""
}

// Update implements tea.Model.
func (s *Screen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
	case ui.RunMsg:
		s.onRun(msg)
	case ui.EditOutcomeMsg:
		s.onEdit(msg)
	case tea.KeyPressMsg:
		if s.focus != fNone {
			s.typing(msg)
			return s, nil
		}
		return s, s.key(msg.String())
	}
	return s, nil
}

func (s *Screen) typing(k tea.KeyPressMsg) {
	f := &s.agents
	switch {
	case s.mode == modeRevert && s.focus == fTarget:
		f = &s.target
	case s.focus == fTo:
		f = &s.to
	}
	switch k.String() {
	case "enter", "esc":
		s.focus = fNone
		return
	case "backspace":
		if r := []rune(*f); len(r) > 0 {
			*f = string(r[:len(r)-1])
			s.edited()
		}
		return
	case "ctrl+u":
		*f = ""
		s.edited()
		return
	}
	if k.Text != "" && k.Mod&(tea.ModCtrl|tea.ModAlt) == 0 {
		*f += mask.Sanitize(k.Text)
		s.edited()
	}
}

// edited runs after any input change.
func (s *Screen) edited() {
	if s.mode == modeRevert && s.focus == fTarget {
		s.all = false
	}
	s.invalidate()
}

func (s *Screen) key(k string) tea.Cmd {
	page := max(s.avail()/2, 1)
	switch k {
	case "m", "[", "]":
		s.mode = 1 - s.mode
		s.msg = ""
	case "e":
		s.focus = fTarget
	case "t":
		if s.mode == modeRevert {
			s.focus = fTo
		}
	case "A":
		if s.mode == modeRevert {
			s.all = !s.all
			s.invalidate()
		}
	case "p":
		return s.preview()
	case "a":
		if s.mode == modeApply {
			return s.commit()
		}
	case "v":
		if s.mode == modeRevert {
			return s.commit()
		}
	case "ctrl+d", "pgdown":
		s.scroll[s.mode] += page
	case "ctrl+u", "pgup":
		s.scroll[s.mode] = max(s.scroll[s.mode]-page, 0)
	}
	s.scroll[s.mode] = min(s.scroll[s.mode], s.maxScroll())
	return nil
}

// preview emits the dry run for the current inputs.
func (s *Screen) preview() tea.Cmd {
	p, err := s.pair()
	if err != nil {
		s.msg = ""
		return nil // the builder error is rendered inline
	}
	s.gates[s.mode].Request(p, s.scope)
	s.msg = "running preview…"
	return ui.Do(p.Preview)
}

// commit emits the real command, only behind a successful matching preview
// in the current scope.
func (s *Screen) commit() tea.Cmd {
	p, err := s.pair()
	if err != nil {
		return nil
	}
	if !s.gates[s.mode].Allows(p.Real, s.scope) {
		s.msg = msgPreviewFirst
		return nil
	}
	s.gates[s.mode].Arm(p.Real)
	s.msg = "waiting for confirmation"
	return ui.Do(p.Real)
}

// onRun feeds a finished action to both gates. One of them may own it (a
// dry run or a real run of ours); any mutation drops the other mode's
// preview too, because the disk may have changed under it.
func (s *Screen) onRun(m ui.RunMsg) {
	had := s.gates[modeApply].Pending() || s.gates[modeRevert].Pending()
	owner, outcome := mode(-1), gate.NotOurs
	for md := range s.gates {
		if o := s.gates[md].Observe(m); o != gate.NotOurs && o != gate.Dropped {
			owner, outcome = mode(md), o
		}
	}
	switch outcome {
	case gate.PreviewOK:
		s.store(owner, m)
		s.msg = "preview ok"
	case gate.PreviewFail:
		s.store(owner, m)
		s.msg = msgPreviewFirst
	case gate.RanReal:
		s.store(owner, m)
		// Any completed real run (success, failure, cancel) may have changed
		// the disk: no preview describes it any more.
		if m.Err == nil && m.Result.ExitCode == 0 {
			s.msg = "done; preview again before the next run"
		} else {
			s.msg = "run did not succeed; preview again"
		}
	default:
		// A mutation issued elsewhere (another tab) may change what an apply
		// or revert would do; read-only runs are harmless.
		if !m.Action.ReadOnly && had {
			s.msg = "preview invalidated by another action — preview again"
		}
	}
}

// onEdit drops both previews when an $EDITOR edit starts or finishes (on any
// screen, whatever its result): the canonical files a preview described may
// have changed, and an edit never produces a RunMsg. A refused edit changed
// nothing and keeps them.
func (s *Screen) onEdit(m ui.EditOutcomeMsg) {
	if !m.Started && !m.Finished {
		return
	}
	a, r := s.gates[modeApply].Drop(), s.gates[modeRevert].Drop()
	if a || r {
		s.msg = "preview invalidated by an edit — preview again"
	}
}

// store keeps only the masked, capped form of the run's output (ui.OutputOf).
func (s *Screen) store(md mode, m ui.RunMsg) {
	o := ui.OutputOf(m)
	s.out[md], s.scroll[md] = &o, 0
}
