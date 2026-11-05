// Package importer is the import screen: pick a registered agent, a
// component and optionally one item name, preview `agentsync import
// <selector> --dry-run`, then run the real import. The real command is only
// offered after a successful preview of the same selector in the same scope
// (see gate.Gate), and it is emitted as a ui.DoMsg so the shell's confirm
// modal stands between the user and the mutation. The screen never runs an
// import itself; its only direct read is `agent list`.
//
// Command output is free text that can carry resolved values, so only the
// mask.Text-then-Sanitize form is ever stored or rendered.
package importer

import (
	"context"
	"errors"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/model"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
	"github.com/lusky3/agentsync-tui/internal/ui/gate"
)

const msgPreviewFirst = "preview first"

// maxName caps the item-name field (bytes); identifiers are far shorter.
const maxName = 256

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
	base  context.Context
	limit time.Duration // bound on one local read (ui.Deps.Limits().Local())
	run   runner.Runner
	scope runner.Scope // the preview is bound to it (gate.Gate)
	reads ui.Rounds    // the agent list read; superseded rounds are cancelled

	loaded bool
	agents []model.Agent // registered agents only
	note   string        // why the agent list is unavailable, display-safe

	agent  string   // selected agent name
	comp   string   // selected component ("" = all)
	name   ui.Field // optional item name
	typing bool     // the name field has focus

	g      gate.Gate
	out    *ui.Output
	scroll ui.Scroll
	msg    string

	width, height int
}

// New returns the import screen.
func New(d ui.Deps) *Screen {
	return &Screen{
		base: ui.BaseCtx(d), limit: d.Limits().Local(), run: d.Runner, scope: d.Scope,
		name: ui.Field{Max: maxName}, width: 80, height: 20,
	}
}

// Title implements ui.Screen.
func (s *Screen) Title() string { return "Import" }

// Init is exactly Refresh.
func (s *Screen) Init() tea.Cmd { return s.Refresh() }

// Refresh reloads the agent list, cancelling the previous read. It
// deliberately keeps the preview: the shell refreshes the screen after every
// run, including the dry run itself.
func (s *Screen) Refresh() tea.Cmd {
	gen, ctx := s.reads.Next(s.base)
	r, limit := s.run, s.limit
	return func() tea.Msg { return readAgents(ctx, gen, r, limit) }
}

func readAgents(ctx context.Context, gen int, r runner.Runner, timeout time.Duration) agentsMsg {
	m := agentsMsg{gen: gen}
	res, err := ui.Read(ctx, r, timeout, "agent", "list")
	if m.note = ui.ReadNote("agent list", res, err); m.note != "" {
		return m
	}
	list, perr := model.ParseAgentList(res.Stdout)
	if perr != nil {
		m.note = ui.Unreadable("agent list", res, perr)
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

// SetScope implements ui.ScopeSetter: swap the runner and forget the agent
// list, the preview and any output.
func (s *Screen) SetScope(r runner.Runner, sc runner.Scope, _ string) {
	s.run, s.scope = r, sc
	s.reads.Supersede()
	s.loaded, s.agents, s.note = false, nil, ""
	s.g.Reset()
	s.out, s.scroll, s.msg = nil, ui.Scroll{}, ""
}

// Shutdown implements ui.Shutdowner.
func (s *Screen) Shutdown() { s.reads.Cancel() }

// CapturesKeys implements ui.KeyCapturer: true while the name field has focus.
func (s *Screen) CapturesKeys() bool { return s.typing }

// pair returns the dry run and the real import for the current inputs.
func (s *Screen) pair() (actions.Previewed, error) {
	if s.agent == "" {
		return actions.Previewed{}, errNoAgent
	}
	return actions.ImportPair(s.agent, s.comp, s.name.Value)
}

var errNoAgent = errors.New("no agent selected")

// Update implements tea.Model.
func (s *Screen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
	case agentsMsg:
		if s.reads.Current(msg.gen) {
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
	if slices.ContainsFunc(s.agents, func(a model.Agent) bool { return a.Name == s.agent }) {
		return
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
	}
	if _, changed := s.name.Key(k); changed {
		s.edited()
	}
}

// edited runs after any input change or cycle.
func (s *Screen) edited() {
	s.g.Invalidate()
	s.msg = ""
}

func (s *Screen) key(k string) tea.Cmd {
	if s.scroll.Key(k, len(s.outLines())) {
		return nil
	}
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
	}
	return nil
}

func (s *Screen) cycleAgent(d int) {
	n := len(s.agents)
	if n == 0 {
		return
	}
	i := max(slices.IndexFunc(s.agents, func(a model.Agent) bool { return a.Name == s.agent }), 0)
	s.agent = s.agents[((i+d)%n+n)%n].Name
	s.edited()
}

func (s *Screen) cycleComp(d int) {
	i := max(slices.Index(components, s.comp), 0)
	n := len(components)
	s.comp = components[((i+d)%n+n)%n]
	s.edited()
}

// preview emits the dry run for the current inputs.
func (s *Screen) preview() tea.Cmd {
	p, err := s.pair()
	if err != nil {
		s.msg = ""
		return nil // the builder error is rendered inline
	}
	s.g.Request(p, s.scope)
	s.msg = "running preview…"
	return ui.Do(p.Preview, s.scope)
}

// commit emits the real import, only behind a successful matching preview.
func (s *Screen) commit() tea.Cmd {
	p, err := s.pair()
	if err != nil {
		return nil
	}
	if !s.g.Allows(p.Real, s.scope) {
		s.msg = msgPreviewFirst
		return nil
	}
	s.g.Arm(p.Real)
	s.msg = "waiting for confirmation"
	return ui.Do(p.Real, s.scope)
}

// onRun applies a finished action matched to this screen by argv.
func (s *Screen) onRun(m ui.RunMsg) {
	switch s.g.Observe(m) {
	case gate.PreviewOK:
		s.store(m)
		s.msg = "preview ok: press i to import"
	case gate.PreviewFail:
		s.store(m)
		s.msg = "preview failed; fix the selector and preview first"
	case gate.RanReal:
		s.store(m)
		if m.Err == nil && m.Result.ExitCode == 0 {
			s.msg = "import done; preview first before another import"
		} else {
			s.msg = "import did not succeed; preview first to retry"
		}
	case gate.Dropped:
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
	if s.g.Drop() {
		s.msg = "preview invalidated by an edit — preview again"
	}
}

// store keeps only the masked, capped form of the run's output (ui.OutputOf).
func (s *Screen) store(m ui.RunMsg) {
	o := ui.OutputOf(m)
	s.out, s.scroll = &o, ui.Scroll{}
}
