// Package agents is the agents screen: it lists every agent agentsync
// supports (`agent list --all`: registered or available) and drives add,
// enable/disable, remove and disable-and-purge through the agentsync CLI via
// ui.DoMsg, so the shell shows the confirm modal.
package agents

import (
	"context"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/model"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

const staleNote = "agent list changed — refreshed"

// Screen is the agents tab.
type Screen struct {
	base  context.Context
	limit time.Duration // bound on one local read (ui.Deps.Limits().Local())
	run   runner.Runner
	scope runner.Scope // the scope the list (and every action) belongs to
	reads ui.Rounds    // the list read; superseded rounds are cancelled

	loaded bool
	agents []model.Agent
	list   ui.List
	// loadNote explains why the last refresh failed (display-safe); the
	// last good list is kept.
	loadNote string
	note     string // last screen message, display-safe

	verify ui.Verifier // the pre-mutation re-read

	width, height int
}

// listMsg carries one `agent list --all` read.
type listMsg struct {
	gen    int
	agents []model.Agent
	note   string // non-empty: the read failed
}

// verification is what the pre-mutation re-read brings back.
type verification struct {
	key   string
	sel   model.Agent
	fresh listMsg
}

// New returns the agents screen.
func New(d ui.Deps) *Screen {
	return &Screen{base: ui.BaseCtx(d), limit: d.Limits().Local(), run: d.Runner, scope: d.Scope, width: 80, height: 20}
}

// Title implements ui.Screen.
func (s *Screen) Title() string { return "Agents" }

// Init is exactly Refresh.
func (s *Screen) Init() tea.Cmd { return s.Refresh() }

// SetScope implements ui.ScopeAware.
func (s *Screen) SetScope(r runner.Runner, sc runner.Scope, _ string) {
	s.run, s.scope = r, sc
	s.reads.Supersede()
	s.invalidate()
}

// Shutdown implements ui.Shutdowner.
func (s *Screen) Shutdown() {
	s.reads.Cancel()
	s.verify.Invalidate()
}

// invalidate drops any in-flight verification: its result will be ignored.
func (s *Screen) invalidate() {
	if s.verify.Busy() {
		s.note = ""
	}
	s.verify.Invalidate()
}

// CapturesKeys implements ui.KeyCapturer: this screen has no text input.
func (s *Screen) CapturesKeys() bool { return false }

// Refresh re-reads the agent list through the Runner, cancelling the
// previous read.
func (s *Screen) Refresh() tea.Cmd {
	s.invalidate()
	gen, ctx := s.reads.Next(s.base)
	r, limit := s.run, s.limit
	return func() tea.Msg { return readList(ctx, gen, r, limit) }
}

func readList(ctx context.Context, gen int, r runner.Runner, timeout time.Duration) listMsg {
	res, err := ui.Read(ctx, r, timeout, "agent", "list", "--all")
	if note := ui.ReadNote("agent list", res, err); note != "" {
		return listMsg{gen: gen, note: note}
	}
	list, err := model.ParseAgentList(res.Stdout)
	if err != nil {
		return listMsg{gen: gen, note: ui.Unreadable("agent list", res, err)}
	}
	return listMsg{gen: gen, agents: list}
}

// Update implements tea.Model.
func (s *Screen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
	case listMsg:
		if s.reads.Current(msg.gen) {
			s.apply(msg)
		}
	case ui.VerifiedMsg:
		if v, ok := s.verify.Accept(msg); ok {
			return s, s.verified(v.(verification))
		}
	case tea.KeyPressMsg:
		return s, s.key(msg.String())
	}
	return s, nil
}

func (s *Screen) apply(m listMsg) {
	s.loaded = true
	if m.note != "" {
		s.loadNote = m.note // keep the last good list
		return
	}
	s.loadNote, s.agents = "", m.agents
	s.list.Clamp(len(s.agents))
}

func (s *Screen) selected() (model.Agent, bool) {
	if s.list.Cursor < 0 || s.list.Cursor >= len(s.agents) {
		return model.Agent{}, false
	}
	return s.agents[s.list.Cursor], true
}

func (s *Screen) key(k string) tea.Cmd {
	if s.list.Key(k, len(s.agents)) {
		s.invalidate()
		return nil
	}
	switch k {
	case "r":
		s.note = ""
		return s.Refresh()
	case "a", "e", "x", "D":
		return s.mutate(k)
	}
	return nil
}

// mutate gates a/e/x/D on the selection, then starts an asynchronous fresh
// read of the list; the action is only built and emitted in verified, once
// the result arrives and still applies.
func (s *Screen) mutate(k string) tea.Cmd {
	if s.verify.Busy() {
		s.note = "still verifying the agent list — wait"
		return nil
	}
	sel, ok := s.selected()
	if !ok {
		return nil
	}
	if !actions.ValidID(sel.Name) {
		s.note = "invalid agent name"
		return nil
	}
	if k == "a" && sel.Registered {
		s.note = "already registered — use e to enable/disable or x to remove"
		return nil
	}
	if k != "a" && !sel.Registered {
		s.note = "not registered — press a to add it first"
		return nil
	}
	s.note = "verifying…"
	r := s.run
	return s.verify.Start(s.base, func(ctx context.Context) any {
		return verification{key: k, sel: sel, fresh: readList(ctx, 0, r, ui.VerifyTimeout)}
	})
}

// verified handles a current re-read result: a failed read or a changed or
// re-ordered agent emits nothing.
func (s *Screen) verified(v verification) tea.Cmd {
	if cur, ok := s.selected(); !ok || cur.Name != v.sel.Name {
		s.note = ""
		return nil
	}
	sel, k, fresh := v.sel, v.key, v.fresh
	if fresh.note != "" {
		s.note = "cannot verify the agent list: " + fresh.note
		return nil
	}
	i := slices.IndexFunc(fresh.agents, func(a model.Agent) bool { return a.Name == sel.Name })
	if i < 0 || fresh.agents[i] != sel {
		s.apply(fresh)
		s.note = staleNote
		return s.Refresh()
	}
	var act actions.Action
	var err error
	switch {
	case k == "a":
		act, err = actions.AgentAdd(sel.Name)
	case k == "x":
		act, err = actions.AgentRemove(sel.Name)
	case k == "D":
		act, err = actions.AgentDisablePurge(sel.Name)
	case sel.Enabled:
		act, err = actions.AgentDisable(sel.Name)
	default:
		act, err = actions.AgentEnable(sel.Name)
	}
	return ui.DoOr(act, err, &s.note, s.scope)
}
