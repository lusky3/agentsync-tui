// Package servers is the MCP servers screen: it lists the canonical
// definitions in <home>/mcp/*.toml (read-only; the user tree in user scope,
// <project>/.agentsync in project scope, see ui.HomeFor) with each agent's
// render state from `agentsync status --json`, and drives every mutation
// through the agentsync CLI via ui.DoMsg / ui.EditFileMsg.
package servers

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/model"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/source"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

const staleNote = "server list changed — refreshed"

// Screen is the MCP servers tab.
type Screen struct {
	base  context.Context
	limit time.Duration // bound on one local read (ui.Deps.Limits().Local())
	run   runner.Runner
	scope runner.Scope // the scope the list (and every action) belongs to
	home  string       // canonical tree of the current scope
	reads ui.Rounds    // the list and status reads; superseded rounds are cancelled

	loaded   bool
	servers  []source.Server
	dirGone  bool   // mcp/ does not exist
	loadWarn string // partial or total load failure, display-safe
	states   map[string]map[string]string
	statusOK bool
	stNote   string // why agent state is unavailable, display-safe

	list   ui.List
	detail bool
	form   *form
	note   string // last screen message, display-safe

	verify ui.Verifier // the pre-mutation re-read of the source tree

	width, height int
}

// loadedMsg carries a LoadServers result.
type loadedMsg struct {
	gen     int
	servers []source.Server
	dirGone bool
	err     error
}

// statusMsg carries a parsed `status --json` result (ok=false: unavailable).
type statusMsg struct {
	gen    int
	ok     bool
	states map[string]map[string]string
	note   string
}

// verification is what the pre-mutation re-read brings back.
type verification struct {
	key   string
	sel   source.Server
	fresh loadedMsg
}

// New returns the MCP servers screen.
func New(d ui.Deps) *Screen {
	return &Screen{base: ui.BaseCtx(d), limit: d.Limits().Local(), run: d.Runner, scope: d.Scope, home: ui.HomeFor(d, d.Scope), width: 80, height: 20}
}

// Title implements ui.Screen.
func (s *Screen) Title() string { return "MCP servers" }

// Init is exactly Refresh (the shell may skip Init and only Refresh later).
func (s *Screen) Init() tea.Cmd { return s.Refresh() }

// SetScope implements ui.ScopeAware: swap the runner and the home and forget
// the loaded list so nothing from the old tree is shown or acted on.
func (s *Screen) SetScope(r runner.Runner, sc runner.Scope, home string) {
	s.run, s.scope, s.home = r, sc, home
	s.reads.Supersede()
	s.verify.Invalidate()
	s.loaded, s.servers, s.dirGone, s.loadWarn = false, nil, false, ""
	s.states, s.statusOK, s.stNote = nil, false, ""
	s.list, s.detail, s.form, s.note = ui.List{}, false, nil, ""
}

// Shutdown implements ui.Shutdowner.
func (s *Screen) Shutdown() {
	s.reads.Cancel()
	s.verify.Invalidate()
}

// CapturesKeys implements ui.KeyCapturer: true while the add form is open.
func (s *Screen) CapturesKeys() bool { return s.form != nil }

// Refresh re-reads the canonical servers and the status report concurrently,
// cancelling the previous status read.
func (s *Screen) Refresh() tea.Cmd {
	gen, ctx := s.reads.Next(s.base)
	home, r, limit := s.home, s.run, s.limit
	load := func() tea.Msg { return loadSource(gen, home) }
	status := func() tea.Msg { return readStatus(ctx, gen, r, limit) }
	return tea.Batch(load, status)
}

func loadSource(gen int, home string) loadedMsg {
	srv, err := source.LoadServers(home)
	_, statErr := os.Stat(filepath.Join(home, "mcp"))
	return loadedMsg{gen: gen, servers: srv, dirGone: errors.Is(statErr, os.ErrNotExist), err: err}
}

func readStatus(ctx context.Context, gen int, r runner.Runner, timeout time.Duration) statusMsg {
	res, err := ui.Read(ctx, r, timeout, "status", "--json")
	if note := ui.ReadNote("status", res, err); note != "" {
		return statusMsg{gen: gen, note: note}
	}
	rep, err := model.ParseStatus(res.Stdout)
	if err != nil {
		return statusMsg{gen: gen, note: "status output unreadable"}
	}
	return statusMsg{gen: gen, ok: true, states: statesByServer(rep)}
}

// statesByServer maps server ID -> agent -> class, from items whose pointer
// is exactly /mcpServers/<id>.
func statesByServer(rep model.StatusReport) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, a := range rep.Agents {
		for _, it := range a.Items {
			id, ok := model.MCPServerID(it.Pointer)
			if !ok {
				continue
			}
			if out[id] == nil {
				out[id] = map[string]string{}
			}
			out[id][a.Agent] = it.State
		}
	}
	return out
}

// Update implements tea.Model.
func (s *Screen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
	case loadedMsg:
		if s.reads.Current(msg.gen) {
			s.applyLoad(msg)
		}
	case statusMsg:
		if s.reads.Current(msg.gen) {
			s.statusOK, s.states, s.stNote = msg.ok, msg.states, msg.note
		}
	case ui.VerifiedMsg:
		if v, ok := s.verify.Accept(msg); ok {
			return s, s.verified(v.(verification))
		}
	case tea.PasteMsg:
		if s.form != nil {
			s.form.paste(msg.Content)
		}
	case tea.KeyPressMsg:
		if s.form != nil {
			return s, s.formKey(msg)
		}
		return s, s.listKey(msg.String())
	}
	return s, nil
}

func (s *Screen) applyLoad(m loadedMsg) {
	s.loaded, s.servers, s.dirGone = true, m.servers, m.dirGone
	s.loadWarn = loadWarning(m.err)
	s.list.Clamp(len(s.servers))
	if len(s.servers) == 0 {
		s.detail = false
	}
}

// loadWarning describes a LoadServers error. A joined (partial) error names
// the bad files by base name; anything else is a whole-directory failure.
func loadWarning(err error) string {
	if err == nil {
		return ""
	}
	j, ok := err.(interface{ Unwrap() []error })
	if !ok {
		return "cannot read the mcp/ directory"
	}
	var names []string
	for _, e := range j.Unwrap() {
		name, _, _ := strings.Cut(e.Error(), ": ")
		names = append(names, mask.OneLine(mask.Text(name)))
	}
	slices.Sort(names)
	return strconv.Itoa(len(names)) + " file(s) unreadable: " + strings.Join(names, ", ")
}

func (s *Screen) selected() (source.Server, bool) {
	if s.list.Cursor < 0 || s.list.Cursor >= len(s.servers) {
		return source.Server{}, false
	}
	return s.servers[s.list.Cursor], true
}

func (s *Screen) listKey(k string) tea.Cmd {
	if s.list.Key(k, len(s.servers)) {
		s.verify.Invalidate()
		return nil
	}
	switch k {
	case "enter":
		if _, ok := s.selected(); ok {
			s.detail = !s.detail
		}
	case "esc":
		s.detail = false
	case "r":
		s.note = ""
		return s.Refresh()
	case "a":
		s.form = newForm(s.ids())
		s.detail = false
	case "e", "x":
		return s.mutate(k)
	case "E":
		return s.edit()
	}
	return nil
}

func (s *Screen) ids() map[string]bool {
	m := make(map[string]bool, len(s.servers))
	for _, srv := range s.servers {
		m[srv.ID] = true
	}
	return m
}

// mutate re-reads the source tree on its own goroutine before toggling
// ("e") or removing ("x"); the action is only built in verified, and refused
// if the selection no longer matches what the user saw. Nothing touches the
// disk inside Update.
func (s *Screen) mutate(k string) tea.Cmd {
	if s.verify.Busy() {
		s.note = "still verifying the server list — wait"
		return nil
	}
	sel, ok := s.selected()
	if !ok {
		return nil
	}
	if !actions.ValidID(sel.ID) {
		s.note = "invalid server id"
		return nil
	}
	s.note = "verifying…"
	home := s.home
	return s.verify.Start(s.base, func(context.Context) any {
		return verification{key: k, sel: sel, fresh: loadSource(0, home)}
	})
}

// verified handles a current re-read result.
func (s *Screen) verified(v verification) tea.Cmd {
	if cur, ok := s.selected(); !ok || cur.ID != v.sel.ID {
		s.note = ""
		return nil
	}
	i := slices.IndexFunc(v.fresh.servers, func(srv source.Server) bool { return srv.ID == v.sel.ID })
	if i < 0 || (v.key == "e" && v.fresh.servers[i].Disabled != v.sel.Disabled) {
		s.applyLoad(v.fresh)
		s.note = staleNote
		return s.Refresh()
	}
	var act actions.Action
	var err error
	switch {
	case v.key == "x":
		act, err = actions.McpRemove(v.sel.ID)
	case v.sel.Disabled:
		act, err = actions.McpEnable(v.sel.ID)
	default:
		act, err = actions.McpDisable(v.sel.ID)
	}
	return ui.DoOr(act, err, &s.note, s.scope)
}

func (s *Screen) edit() tea.Cmd {
	sel, ok := s.selected()
	if !ok {
		return nil
	}
	if !actions.ValidID(sel.ID) {
		s.note = "invalid server id"
		return nil
	}
	p, err := source.ServerPath(s.home, sel.ID)
	if err != nil {
		s.note = "error: " + mask.FirstLine(err.Error())
		return nil
	}
	return func() tea.Msg { return ui.EditFileMsg{Path: p, After: []actions.Action{actions.Check()}} }
}

func (s *Screen) formKey(k tea.KeyPressMsg) tea.Cmd {
	switch s.form.update(k) {
	case formCancel:
		s.form = nil
	case formSubmit:
		s.form.existing = s.ids() // the list may have refreshed since the form opened
		act, err := s.form.submit()
		if err != nil {
			return nil // shown inline by the form
		}
		s.form = nil
		return ui.DoOr(act, nil, &s.note, s.scope)
	}
	return nil
}
