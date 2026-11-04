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
	"sort"
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

// statusTimeout bounds one `status --json` read.
const statusTimeout = 30 * time.Second

const staleNote = "server list changed — refreshed"

// Screen is the MCP servers tab.
type Screen struct {
	d     ui.Deps
	run   runner.Runner
	scope runner.Scope
	home  string // canonical tree of the current scope
	gen   int    // refresh generation; older results are dropped

	loaded   bool
	servers  []source.Server
	dirGone  bool   // mcp/ does not exist
	loadWarn string // partial or total load failure, display-safe
	states   map[string]map[string]string
	statusOK bool
	stNote   string // why agent state is unavailable, display-safe

	cursor int
	detail bool
	form   *form
	note   string // last screen message, display-safe

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

// New returns the MCP servers screen.
func New(d ui.Deps) *Screen {
	return &Screen{d: d, run: d.Runner, scope: d.Scope, home: ui.HomeFor(d, d.Scope), width: 80, height: 20}
}

// Title implements ui.Screen.
func (s *Screen) Title() string { return "MCP servers" }

// Init is exactly Refresh (the shell may skip Init and only Refresh later).
func (s *Screen) Init() tea.Cmd { return s.Refresh() }

// SetScope implements ui.ScopeAware: swap the runner and the home and forget
// the loaded list so nothing from the old tree is shown or acted on.
func (s *Screen) SetScope(r runner.Runner, sc runner.Scope, home string) {
	s.run, s.scope, s.home = r, sc, home
	s.gen++
	s.loaded, s.servers, s.dirGone, s.loadWarn = false, nil, false, ""
	s.states, s.statusOK, s.stNote = nil, false, ""
	s.cursor, s.detail, s.form, s.note = 0, false, nil, ""
}

// CapturesKeys implements ui.KeyCapturer: true while the add form is open.
func (s *Screen) CapturesKeys() bool { return s.form != nil }

// Refresh re-reads the canonical servers and the status report concurrently.
func (s *Screen) Refresh() tea.Cmd {
	s.gen++
	gen, home, r := s.gen, s.home, s.run
	load := func() tea.Msg { return loadSource(gen, home) }
	status := func() tea.Msg { return readStatus(gen, r) }
	return tea.Batch(load, status)
}

func loadSource(gen int, home string) loadedMsg {
	srv, err := source.LoadServers(home)
	_, statErr := os.Stat(filepath.Join(home, "mcp"))
	return loadedMsg{gen: gen, servers: srv, dirGone: errors.Is(statErr, os.ErrNotExist), err: err}
}

func readStatus(gen int, r runner.Runner) statusMsg {
	if r == nil {
		return statusMsg{gen: gen, note: "no runner"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), statusTimeout)
	defer cancel()
	res, err := r.Run(ctx, "status", "--json")
	switch {
	case err != nil:
		return statusMsg{gen: gen, note: firstLine(mask.Text(err.Error()))}
	case res.ExitCode != 0:
		return statusMsg{gen: gen, note: "status exited " + itoa(res.ExitCode)}
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
			id := pointerID(it.Pointer)
			if id == "" {
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

// pointerID returns <id> for a JSON pointer "/mcpServers/<id>" (unescaped),
// or "" for any other shape.
func pointerID(p string) string {
	rest, ok := strings.CutPrefix(p, "/mcpServers/")
	if !ok || rest == "" || strings.Contains(rest, "/") {
		return ""
	}
	return strings.NewReplacer("~1", "/", "~0", "~").Replace(rest)
}

// Update implements tea.Model.
func (s *Screen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
	case loadedMsg:
		if msg.gen == s.gen {
			s.applyLoad(msg)
		}
	case statusMsg:
		if msg.gen == s.gen {
			s.statusOK, s.states, s.stNote = msg.ok, msg.states, msg.note
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
	if s.cursor >= len(s.servers) {
		s.cursor = len(s.servers) - 1
	}
	if s.cursor < 0 {
		s.cursor = 0
	}
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
		names = append(names, oneLine(mask.Text(name)))
	}
	sort.Strings(names)
	return itoa(len(names)) + " file(s) unreadable: " + strings.Join(names, ", ")
}

func (s *Screen) selected() (source.Server, bool) {
	if s.cursor < 0 || s.cursor >= len(s.servers) {
		return source.Server{}, false
	}
	return s.servers[s.cursor], true
}

func (s *Screen) listKey(k string) tea.Cmd {
	switch k {
	case "up", "k":
		if s.cursor > 0 {
			s.cursor--
		}
	case "down", "j":
		if s.cursor < len(s.servers)-1 {
			s.cursor++
		}
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

// mutate re-reads the source before toggling ("e") or removing ("x") and
// refuses if the selection no longer matches what the user saw.
func (s *Screen) mutate(k string) tea.Cmd {
	sel, ok := s.selected()
	if !ok {
		return nil
	}
	if !actions.ValidID(sel.ID) {
		s.note = "invalid server id"
		return nil
	}
	fresh := loadSource(s.gen, s.home)
	cur, found := find(fresh.servers, sel.ID)
	if !found || (k == "e" && cur.Disabled != sel.Disabled) {
		s.applyLoad(fresh)
		s.note = staleNote
		return s.Refresh()
	}
	var act actions.Action
	var err error
	switch {
	case k == "x":
		act, err = actions.McpRemove(sel.ID)
	case sel.Disabled:
		act, err = actions.McpEnable(sel.ID)
	default:
		act, err = actions.McpDisable(sel.ID)
	}
	return s.do(act, err)
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
		s.note = "error: " + firstLine(mask.Text(err.Error()))
		return nil
	}
	return func() tea.Msg { return ui.EditFileMsg{Path: p, After: []actions.Action{actions.Check()}} }
}

// do hands a built action to the shell, or shows the builder error.
func (s *Screen) do(act actions.Action, err error) tea.Cmd {
	if err != nil {
		s.note = "error: " + firstLine(mask.Text(err.Error()))
		return nil
	}
	s.note = ""
	return func() tea.Msg { return ui.DoMsg{Action: act} }
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
		return s.do(act, nil)
	}
	return nil
}

func find(list []source.Server, id string) (source.Server, bool) {
	for _, srv := range list {
		if srv.ID == id {
			return srv, true
		}
	}
	return source.Server{}, false
}
