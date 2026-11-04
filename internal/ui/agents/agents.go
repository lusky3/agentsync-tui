// Package agents is the agents screen: it lists every agent agentsync
// supports (`agent list --all`: registered or available) and drives add,
// enable/disable, remove and disable-and-purge through the agentsync CLI via
// ui.DoMsg, so the shell shows the confirm modal.
package agents

import (
	"context"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/model"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

// listTimeout bounds one `agent list --all` refresh; verifyTimeout bounds the
// pre-mutation re-read, which the user is waiting on.
const (
	listTimeout   = 30 * time.Second
	verifyTimeout = 5 * time.Second
)

const staleNote = "agent list changed — refreshed"

// Screen is the agents tab.
type Screen struct {
	d     ui.Deps
	run   runner.Runner
	scope runner.Scope
	gen   int // refresh generation; older results are dropped

	loaded bool
	agents []model.Agent
	cursor int
	// loadNote explains why the last refresh failed (display-safe); the
	// last good list is kept.
	loadNote string
	note     string // last screen message, display-safe
	// verifying is set while a pre-mutation re-read is in flight; vreq
	verifying bool
	vreq      int

	width, height int
}

// listMsg carries one `agent list --all` read.
type listMsg struct {
	gen    int
	agents []model.Agent
	note   string // non-empty: the read failed
}

// verifyMsg carries the fresh list read for one pending mutation.
type verifyMsg struct {
	req   int
	key   string
	sel   model.Agent
	fresh listMsg
}

// New returns the agents screen.
func New(d ui.Deps) *Screen {
	return &Screen{d: d, run: d.Runner, scope: d.Scope, width: 80, height: 20}
}

// Title implements ui.Screen.
func (s *Screen) Title() string { return "Agents" }

// Init is exactly Refresh.
func (s *Screen) Init() tea.Cmd { return s.Refresh() }

// SetScope implements ui.ScopeAware.
func (s *Screen) SetScope(r runner.Runner, sc runner.Scope, _ string) {
	s.run, s.scope = r, sc
	s.invalidate()
}

// invalidate drops any in-flight verification: its result will be ignored.
func (s *Screen) invalidate() {
	if s.verifying {
		s.verifying, s.note = false, ""
	}
	s.vreq++
}

// CapturesKeys implements ui.KeyCapturer: this screen has no text input.
func (s *Screen) CapturesKeys() bool { return false }

// Refresh re-reads the agent list through the Runner.
func (s *Screen) Refresh() tea.Cmd {
	s.gen++
	s.invalidate()
	gen, r := s.gen, s.run
	return func() tea.Msg { return readList(gen, r, listTimeout) }
}

func readList(gen int, r runner.Runner, timeout time.Duration) listMsg {
	if r == nil {
		return listMsg{gen: gen, note: "no runner"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	res, err := r.Run(ctx, "agent", "list", "--all")
	if err != nil {
		return listMsg{gen: gen, note: oneLine(mask.Text(err.Error()))}
	}
	if res.ExitCode != 0 {
		n := "agent list exited " + itoa(res.ExitCode)
		if l := firstLine(mask.Text(res.Stderr)); l != "" {
			n += ": " + l
		}
		return listMsg{gen: gen, note: n}
	}
	list, err := model.ParseAgentList(res.Stdout)
	if err != nil {
		return listMsg{gen: gen, note: "agent list output unreadable"}
	}
	return listMsg{gen: gen, agents: list}
}

// Update implements tea.Model.
func (s *Screen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
	case listMsg:
		if msg.gen == s.gen {
			s.apply(msg)
		}
	case verifyMsg:
		return s, s.verified(msg)
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
	s.cursor = max(min(s.cursor, len(s.agents)-1), 0)
}

func (s *Screen) selected() (model.Agent, bool) {
	if s.cursor < 0 || s.cursor >= len(s.agents) {
		return model.Agent{}, false
	}
	return s.agents[s.cursor], true
}

func (s *Screen) key(k string) tea.Cmd {
	switch k {
	case "up", "k":
		s.cursor = max(s.cursor-1, 0)
		s.invalidate()
	case "down", "j":
		s.cursor = max(min(s.cursor+1, len(s.agents)-1), 0)
		s.invalidate()
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
	if s.verifying {
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
	s.vreq++
	s.verifying, s.note = true, "verifying…"
	req, r := s.vreq, s.run
	return func() tea.Msg {
		return verifyMsg{req: req, key: k, sel: sel, fresh: readList(0, r, verifyTimeout)}
	}
}

// verified handles a re-read result: stale requests are dropped; a failed
// read or a changed agent emits nothing.
func (s *Screen) verified(m verifyMsg) tea.Cmd {
	cur, ok := s.selected()
	if !s.verifying || m.req != s.vreq || !ok || cur.Name != m.sel.Name {
		return nil
	}
	s.verifying = false
	sel, k, fresh := m.sel, m.key, m.fresh
	if fresh.note != "" {
		s.note = "cannot verify the agent list: " + fresh.note
		return nil
	}
	now, found := find(fresh.agents, sel.Name)
	if !found || now.Registered != sel.Registered || now.Enabled != sel.Enabled || now.Scope != sel.Scope {
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
	if err != nil {
		s.note = "error: " + firstLine(mask.Text(err.Error()))
		return nil
	}
	s.note = ""
	return func() tea.Msg { return ui.DoMsg{Action: act} }
}

func find(list []model.Agent, name string) (model.Agent, bool) {
	for _, a := range list {
		if a.Name == name {
			return a, true
		}
	}
	return model.Agent{}, false
}

// oneLine makes s safe for a single terminal line.
func oneLine(s string) string {
	s = mask.Sanitize(s)
	return strings.NewReplacer("\n", " ", "\t", " ").Replace(s)
}

// firstLine returns the first non-blank line of s, trimmed and sanitised.
func firstLine(s string) string {
	for _, l := range strings.Split(mask.Sanitize(s), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return oneLine(l)
		}
	}
	return ""
}
