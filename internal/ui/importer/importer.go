// Package importer is the import screen: pick a registered agent, a
// component and optionally one item name, preview `agentsync import
// <selector> --dry-run`, then run the real import. The real command is only
// offered after a successful preview of the same selector in the same scope
// (see gate), and it is emitted as a ui.DoMsg so the shell's confirm modal
// stands between the user and the mutation. The screen never runs an import
// itself; its only direct read is `agent list`.
//
// Command output is free text that can carry resolved values, so only the
// mask.Text-then-Sanitize form is ever stored or rendered.
package importer

import (
	"context"
	"errors"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/model"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

// readTimeout bounds the agent list read.
const readTimeout = 30 * time.Second

const msgPreviewFirst = "preview first"

// components in cycle order; "" is "all" (the agent's full native config).
var components = []string{"", "mcp", "skill", "subagent", "command", "hook", "lsp", "memory", "plugin"}

func compLabel(c string) string {
	if c == "" {
		return "all"
	}
	return c
}

// agentsMsg carries a parsed `agent list` (ok=false: unavailable, see note).
type agentsMsg struct {
	gen    int
	ok     bool
	agents []model.Agent
	note   string
}

// Screen is the import tab.
type Screen struct {
	d     ui.Deps
	run   runner.Runner
	scope runner.Scope

	agen   int // agent list generation; older results are dropped
	loaded bool
	agents []model.Agent // registered agents only
	note   string        // why the agent list is unavailable, display-safe

	agent  string // selected agent name
	comp   string // selected component ("" = all)
	name   string // optional item name
	typing bool   // the name field has focus

	g      gate
	out    *ui.Output
	scroll int
	msg    string

	width, height int
}

// New returns the import screen.
func New(d ui.Deps) *Screen {
	return &Screen{d: d, run: d.Runner, scope: d.Scope, width: 80, height: 20}
}

// Title implements ui.Screen.
func (s *Screen) Title() string { return "Import" }

// Init is exactly Refresh.
func (s *Screen) Init() tea.Cmd { return s.Refresh() }

// Refresh reloads the agent list. It deliberately keeps the preview: the
// shell refreshes the screen after every run, including the dry run itself.
func (s *Screen) Refresh() tea.Cmd {
	s.agen++
	gen, r, base := s.agen, s.run, ui.BaseCtx(s.d)
	return func() tea.Msg { return readAgents(base, gen, r) }
}

func readAgents(base context.Context, gen int, r runner.Runner) agentsMsg {
	m := agentsMsg{gen: gen}
	if r == nil {
		m.note = "agent list unavailable: no runner"
		return m
	}
	ctx, cancel := context.WithTimeout(base, readTimeout)
	defer cancel()
	res, err := r.Run(ctx, "agent", "list")
	if err != nil {
		m.note = "agent list unavailable: " + mask.FirstLine(err.Error())
		return m
	}
	list, perr := model.ParseAgentList(res.Stdout)
	if perr != nil || res.ExitCode != 0 {
		m.note = "agent list failed"
		if res.ExitCode != 0 {
			m.note += " (exit " + strconv.Itoa(res.ExitCode) + ")"
		}
		if l := mask.FirstLine(res.Stderr); l != "" {
			m.note += ": " + l
		}
		return m
	}
	for _, a := range list {
		if a.Registered {
			m.agents = append(m.agents, a)
		}
	}
	m.ok = true
	return m
}

// SetScope implements ui.ScopeAware: swap the runner and forget the agent
// list, the preview and any output.
func (s *Screen) SetScope(r runner.Runner, sc runner.Scope, _ string) {
	s.run, s.scope = r, sc
	s.agen++
	s.loaded, s.agents, s.note = false, nil, ""
	s.g.invalidate()
	s.g.live = ""
	s.out, s.scroll, s.msg = nil, 0, ""
}

// CapturesKeys implements ui.KeyCapturer: true while the name field has focus.
func (s *Screen) CapturesKeys() bool { return s.typing }

func (s *Screen) agentName() string { return s.agent }
func (s *Screen) component() string { return s.comp }

// build returns the real (non-dry) or dry-run action for the current inputs.
func (s *Screen) build(dry bool) (actions.Action, error) {
	if s.agent == "" {
		return actions.Action{}, errNoAgent
	}
	return actions.Import(s.agent, s.comp, s.name, dry)
}

var errNoAgent = errors.New("no agent selected")

// Update implements tea.Model.
func (s *Screen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
		s.scroll = min(s.scroll, s.maxScroll())
	case agentsMsg:
		if msg.gen == s.agen {
			s.onAgents(msg)
		}
	case ui.RunMsg:
		s.onRun(msg)
	case ui.EditOutcomeMsg:
		s.onEdit(msg)
	case tea.KeyPressMsg:
		if s.typing {
			s.typed(msg)
			return s, nil
		}
		return s, s.key(msg.String())
	}
	return s, nil
}

// onAgents applies a current agent list, keeping the selection by name. A
// failed reload keeps the last good list.
func (s *Screen) onAgents(m agentsMsg) {
	s.loaded, s.note = true, m.note
	if !m.ok {
		return
	}
	s.agents = m.agents
	for _, a := range s.agents {
		if a.Name == s.agent {
			return
		}
	}
	// The selection is gone: pick the first agent. The selector changed, so
	// the preview no longer describes the real command: drop it (the gate's
	// argv comparison would refuse it too; this is defence in depth).
	prev := s.agent
	s.agent = ""
	if len(s.agents) > 0 {
		s.agent = s.agents[0].Name
	}
	if s.agent != prev {
		s.edited()
	}
}

func (s *Screen) typed(k tea.KeyPressMsg) {
	switch k.String() {
	case "enter", "esc":
		s.typing = false
		return
	case "backspace":
		if r := []rune(s.name); len(r) > 0 {
			s.name = string(r[:len(r)-1])
			s.edited()
		}
		return
	case "ctrl+u":
		if s.name != "" {
			s.name = ""
			s.edited()
		}
		return
	}
	if k.Text != "" && k.Mod&(tea.ModCtrl|tea.ModAlt) == 0 {
		s.name += mask.Sanitize(k.Text)
		s.edited()
	}
}

// edited runs after any input change or cycle.
func (s *Screen) edited() {
	s.g.invalidate()
	s.msg = ""
}

func (s *Screen) key(k string) tea.Cmd {
	page := max(s.avail()/2, 1)
	switch k {
	case "a":
		s.cycleAgent(1)
	case "A":
		s.cycleAgent(-1)
	case "c":
		s.cycleComp(1)
	case "C":
		s.cycleComp(-1)
	case "n":
		s.typing = true
	case "p":
		return s.preview()
	case "i":
		return s.commit()
	case "r":
		return s.Refresh()
	case "ctrl+d", "pgdown":
		s.scroll += page
	case "ctrl+u", "pgup":
		s.scroll = max(s.scroll-page, 0)
	}
	s.scroll = min(s.scroll, s.maxScroll())
	return nil
}

func (s *Screen) cycleAgent(d int) {
	n := len(s.agents)
	if n == 0 {
		return
	}
	i := 0
	for j, a := range s.agents {
		if a.Name == s.agent {
			i = j
		}
	}
	s.agent = s.agents[((i+d)%n+n)%n].Name
	s.edited()
}

func (s *Screen) cycleComp(d int) {
	i := 0
	for j, c := range components {
		if c == s.comp {
			i = j
		}
	}
	n := len(components)
	s.comp = components[((i+d)%n+n)%n]
	s.edited()
}

func doCmd(a actions.Action) tea.Cmd { return func() tea.Msg { return ui.DoMsg{Action: a} } }

// preview emits the dry run for the current inputs.
func (s *Screen) preview() tea.Cmd {
	act, err := s.build(true)
	if err != nil {
		s.msg = ""
		return nil // the builder error is rendered inline
	}
	s.g.request(act)
	s.msg = "running preview…"
	return doCmd(act)
}

// commit emits the real import, only behind a successful matching preview.
func (s *Screen) commit() tea.Cmd {
	act, err := s.build(false)
	if err != nil {
		return nil
	}
	if !s.g.allows(act, s.scope) {
		s.msg = msgPreviewFirst
		return nil
	}
	s.g.arm(act)
	s.msg = "waiting for confirmation"
	return doCmd(act)
}

// onRun applies a finished action matched to this screen by argv.
func (s *Screen) onRun(m ui.RunMsg) {
	switch s.g.observe(m, s.scope) {
	case previewOK:
		s.store(m)
		s.msg = "preview ok: press i to import"
	case previewFail:
		s.store(m)
		s.msg = "preview failed; fix the selector and preview first"
	case ranReal:
		s.store(m)
		if m.Err == nil && m.Result.ExitCode == 0 {
			s.msg = "import done; preview first before another import"
		} else {
			s.msg = "import did not succeed; preview first to retry"
		}
	case dropped:
		s.msg = ""
	}
}

// onEdit drops the preview when an $EDITOR edit starts or finishes (on any
// screen, whatever its result): the canonical tree an import preview
// described may have changed, and an edit never produces a RunMsg. A
// refused edit changed nothing and keeps it.
func (s *Screen) onEdit(m ui.EditOutcomeMsg) {
	if !m.Started && !m.Finished {
		return
	}
	if s.g.editInvalidate() {
		s.msg = "preview invalidated by an edit — preview again"
	}
}

// store keeps only the masked, capped form of the run's output (ui.OutputOf).
func (s *Screen) store(m ui.RunMsg) {
	o := ui.OutputOf(m)
	s.out, s.scroll = &o, 0
}
