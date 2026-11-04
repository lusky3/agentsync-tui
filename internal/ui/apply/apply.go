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
)

// maxOutput caps the stored (masked) output of one run.
const maxOutput = 64 << 10

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

// preview is a successful dry run, bound to the argv of the real command.
type preview struct{ key string }

// output is one run's masked result.
type output struct {
	title string
	text  string // masked + sanitised, capped
	trunc bool
	note  string // masked error / cancellation note
}

// pending is an in-flight request, matched back by argv and seq.
type pending struct {
	argv string
	seq  int
}

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

	seq    int // bumped by every invalidation; older results are dropped
	prev   [2]*preview
	req    [2]*pending // in-flight preview per mode
	live   [2]string   // argv of the real command last handed to the shell
	out    [2]*output
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
func (s *Screen) SetScope(r runner.Runner, sc runner.Scope) {
	s.run, s.scope = r, sc
	s.seq++
	s.prev, s.req, s.live, s.out, s.scroll = [2]*preview{}, [2]*pending{}, [2]string{}, [2]*output{}, [2]int{}
	s.msg = ""
}

// CapturesKeys implements ui.KeyCapturer: true while a text field has focus.
func (s *Screen) CapturesKeys() bool { return s.focus != fNone }

// build returns the real (non-dry) or dry-run action for the current inputs.
func (s *Screen) build(dry bool) (actions.Action, error) {
	if s.mode == modeApply {
		var names []string
		for _, n := range strings.Split(s.agents, ",") {
			if n = strings.TrimSpace(n); n != "" {
				names = append(names, n)
			}
		}
		return actions.Apply(dry, names...)
	}
	agent := strings.TrimSpace(s.target)
	if s.all {
		agent = ""
	}
	return actions.Revert(agent, strings.TrimSpace(s.to), s.all, dry)
}

func argvKey(a actions.Action) string { return strings.Join(a.Argv, "\x00") }

// invalidate drops the mode's preview and any result still in flight.
func (s *Screen) invalidate() {
	s.seq++
	s.prev[s.mode], s.req[s.mode] = nil, nil
	s.msg = ""
}

// Update implements tea.Model.
func (s *Screen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
	case ui.RunMsg:
		s.onRun(msg)
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

func doCmd(a actions.Action) tea.Cmd { return func() tea.Msg { return ui.DoMsg{Action: a} } }

// preview emits the dry run for the current inputs.
func (s *Screen) preview() tea.Cmd {
	act, err := s.build(true)
	if err != nil {
		s.msg = ""
		return nil // the builder error is rendered inline
	}
	s.seq++
	s.prev[s.mode] = nil
	s.req[s.mode] = &pending{argv: argvKey(act), seq: s.seq}
	s.msg = "running preview…"
	return doCmd(act)
}

// commit emits the real command, only behind a successful matching preview.
func (s *Screen) commit() tea.Cmd {
	act, err := s.build(false)
	pv := s.prev[s.mode]
	if err != nil || pv == nil || pv.key != argvKey(act) {
		if err == nil {
			s.msg = msgPreviewFirst
		}
		return nil
	}
	s.live[s.mode] = argvKey(act)
	s.msg = "waiting for confirmation"
	return doCmd(act)
}

// onRun matches a finished action to this screen's requests by argv.
func (s *Screen) onRun(m ui.RunMsg) {
	if len(m.Action.Argv) == 0 {
		return
	}
	key := argvKey(m.Action)
	for md := modeApply; md <= modeRevert; md++ {
		if m.Action.ReadOnly {
			if r := s.req[md]; r != nil && r.argv == key && r.seq == s.seq {
				s.req[md] = nil
				s.store(md, m)
				ok := m.Err == nil && m.Result.ExitCode == 0
				s.prev[md] = nil
				if ok {
					s.prev[md] = &preview{key: realKey(key)}
					s.msg = "preview ok"
				} else {
					s.msg = msgPreviewFirst
				}
				return
			}
		} else if s.live[md] == key {
			s.live[md] = ""
			s.store(md, m)
			// Any completed real run (success, failure, cancel) may have
			// changed the disk: no preview describes it any more.
			s.dropPreviews()
			if m.Err == nil && m.Result.ExitCode == 0 {
				s.msg = "done; preview again before the next run"
			} else {
				s.msg = "run did not succeed; preview again"
			}
			return
		}
	}
	if !m.Action.ReadOnly {
		// A mutation issued elsewhere (another tab) may change what an apply
		// or revert would do; read-only runs are harmless.
		s.dropPreviews()
	}
}

// dropPreviews invalidates both modes' previews and in-flight requests.
func (s *Screen) dropPreviews() {
	s.seq++
	s.prev, s.req = [2]*preview{}, [2]*pending{}
}

// realKey maps a dry-run argv key to the matching real command's key.
func realKey(dry string) string {
	parts := strings.Split(dry, "\x00")
	out := parts[:0]
	for _, p := range parts {
		if p != "--dry-run" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\x00")
}

// store keeps only the masked form of the run's output.
func (s *Screen) store(md mode, m ui.RunMsg) {
	o := &output{title: mask.Sanitize(m.Action.Title)}
	var b strings.Builder
	b.WriteString(clean(m.Result.Stdout))
	if m.Err != nil || m.Result.ExitCode != 0 {
		if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
			b.WriteString("\n")
		}
		b.WriteString(clean(m.Result.Stderr))
	}
	if m.Err != nil {
		o.note = oneLine(clean(m.Err.Error()))
	} else if m.Result.ExitCode != 0 {
		o.note = "exit " + itoa(m.Result.ExitCode)
	}
	o.text = b.String()
	if len(o.text) > maxOutput {
		o.text, o.trunc = cutRunes(o.text, maxOutput), true
	}
	s.out[md], s.scroll[md] = o, 0
}

// clean masks (before any truncation, so a cut cannot split a secret) then
// sanitises.
func clean(v string) string { return mask.Sanitize(mask.Text(v)) }

func cutRunes(v string, n int) string {
	for n > 0 && n < len(v) && v[n]&0xC0 == 0x80 {
		n--
	}
	return v[:n]
}
