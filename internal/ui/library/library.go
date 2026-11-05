// Package library is the read-mostly browser for the canonical libraries:
// skills, subagents, slash commands, hooks, LSP servers and memory. It lists
// items (via `<kind> list`, or the memory directory) and hands the selected
// canonical file to the shell's EditFileMsg; there is deliberately no
// create or delete (agentsync has no `add` for these, creation is `import`).
package library

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/model"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

// Kind selects which library a Screen browses.
type Kind int

// The library kinds.
const (
	Skill Kind = iota
	Subagent
	Command
	Hook
	LSP
	Memory
)

const (
	memoryRoot = "AGENTS" // memory/AGENTS.md; fragments live in memory/fragments/
	staleNote  = "item changed — refreshed"
)

// kindInfo holds the per-kind words.
type kindInfo struct{ title, word, plural, dir string }

var infos = map[Kind]kindInfo{
	Skill:    {"Skills", "skill", "skills", "skills"},
	Subagent: {"Subagents", "subagent", "subagents", "subagents"},
	Command:  {"Commands", "command", "commands", "commands"},
	Hook:     {"Hooks", "hook", "hooks", "hooks"},
	LSP:      {"LSP", "lsp", "LSP servers", "lsp"},
	Memory:   {"Memory", "memory", "memory files", "memory"},
}

// checkState is how the `check` after an edit ended.
type checkState int

const (
	checkPassed checkState = iota
	checkFailed
	checkNotRun // agentsync could not run at all
)

func (c checkState) String() string {
	switch c {
	case checkPassed:
		return "passed"
	case checkFailed:
		return "failed"
	}
	return "could not run"
}

// checkResult is the last `check` outcome shown in the result pane. lines is
// masked and sanitised before it is stored.
type checkResult struct {
	state  checkState
	exit   int
	lines  []string
	trunc  bool
	scroll ui.Scroll
}

// nextID hands every Screen a process-unique identity: the shell broadcasts
// unknown messages to all screens, so a result must name its owner (see
// ui.Screen).
var nextID atomic.Uint64

// Screen is one library tab.
type Screen struct {
	id    uint64
	kind  Kind
	base  context.Context
	limit time.Duration // bound on one local read (ui.Deps.Limits().Local())
	run   runner.Runner
	home  string    // canonical tree of the current scope (memory list, edit paths)
	reads ui.Rounds // the list read; superseded rounds are cancelled

	loaded   bool
	items    []model.Item
	list     ui.List
	loadNote string // why the last refresh failed, display-safe; last good list kept
	note     string // last screen message, display-safe

	// pending is the path of the edit this screen asked the shell for, until
	// the shell reports the outcome; awaiting is set only once the shell
	// started the editor, and cleared when the outcome says it will not run
	// check or when this screen consumes the check result.
	pending  string
	awaiting bool
	check    *checkResult

	width, height int
}

// listMsg carries one list read.
type listMsg struct {
	id    uint64 // owning Screen
	gen   int
	items []model.Item
	note  string // non-empty: the read failed
}

// New returns the library screen for kind.
func New(d ui.Deps, kind Kind) *Screen {
	return &Screen{
		id: nextID.Add(1), kind: kind, base: ui.BaseCtx(d), limit: d.Limits().Local(), run: d.Runner,
		home: ui.HomeFor(d, d.Scope), width: 80, height: 24,
	}
}

// Title implements ui.Screen.
func (s *Screen) Title() string { return infos[s.kind].title }

// Init is exactly Refresh.
func (s *Screen) Init() tea.Cmd { return s.Refresh() }

// SetScope implements ui.ScopeSetter: swap the runner and the home (memory
// is listed from it and every edit path is built under it) and drop the
// items loaded from the old tree.
func (s *Screen) SetScope(r runner.Runner, _ runner.Scope, home string) {
	s.run, s.home = r, home
	s.reads.Supersede() // results of reads started under the old scope are dropped
	s.loaded, s.items, s.loadNote, s.note, s.list = false, nil, "", "", ui.List{}
	s.pending, s.awaiting, s.check = "", false, nil
}

// Shutdown implements ui.Shutdowner.
func (s *Screen) Shutdown() { s.reads.Cancel() }

// CapturesKeys implements ui.KeyCapturer: no text input here.
func (s *Screen) CapturesKeys() bool { return false }

// Refresh re-reads the list, cancelling the previous read; nothing runs
// inside Update.
func (s *Screen) Refresh() tea.Cmd {
	gen, ctx := s.reads.Next(s.base)
	id, kind, r, home, limit := s.id, s.kind, s.run, s.home, s.limit
	if kind == Memory {
		return func() tea.Msg { return readMemory(id, gen, home) }
	}
	return func() tea.Msg { return readList(ctx, id, gen, kind, r, limit) }
}

func readList(ctx context.Context, id uint64, gen int, kind Kind, r runner.Runner, timeout time.Duration) listMsg {
	word := infos[kind].word
	res, err := ui.Read(ctx, r, timeout, word, "list")
	if note := ui.ReadNote(word+" list", res, err); note != "" {
		return listMsg{id: id, gen: gen, note: note}
	}
	items, err := model.ParseList(res.Stdout)
	if err != nil {
		return listMsg{id: id, gen: gen, note: ui.Unreadable(word+" list", res, err)}
	}
	return listMsg{id: id, gen: gen, items: items}
}

// readMemory lists memory/AGENTS.md and memory/fragments/*.md directly:
// regular files only (no symlinks, directories or odd names), sorted.
func readMemory(id uint64, gen int, home string) listMsg {
	var items []model.Item
	if fi, err := os.Lstat(filepath.Join(home, "memory", "AGENTS.md")); err == nil && fi.Mode().IsRegular() {
		items = append(items, model.Item{Name: memoryRoot})
	}
	entries, err := os.ReadDir(filepath.Join(home, "memory", "fragments"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return listMsg{id: id, gen: gen, note: "cannot read memory/fragments"}
	}
	var frags []model.Item
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".md")
		if !ok || !e.Type().IsRegular() || !actions.ValidID(name) || name == memoryRoot {
			continue
		}
		frags = append(frags, model.Item{Name: name})
	}
	slices.SortFunc(frags, func(a, b model.Item) int { return strings.Compare(a.Name, b.Name) })
	return listMsg{id: id, gen: gen, items: append(items, frags...)}
}

// Update implements tea.Model.
func (s *Screen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
	case listMsg:
		if msg.id == s.id && s.reads.Current(msg.gen) {
			s.apply(msg)
		}
	case ui.EditOutcomeMsg:
		s.onOutcome(msg)
	case ui.RunMsg:
		s.onRun(msg)
	case tea.KeyPressMsg:
		return s, s.keyPress(msg.String())
	}
	return s, nil
}

func (s *Screen) apply(m listMsg) {
	s.loaded = true
	if m.note != "" {
		s.loadNote = m.note // keep the last good list
		return
	}
	s.loadNote, s.items = "", m.items
	s.list.Clamp(len(s.items))
}

func (s *Screen) selected() (model.Item, bool) {
	if s.list.Cursor < 0 || s.list.Cursor >= len(s.items) {
		return model.Item{}, false
	}
	return s.items[s.list.Cursor], true
}

// keyPress routes the scroll keys to the result pane while one is open and
// to the item list otherwise.
func (s *Screen) keyPress(k string) tea.Cmd {
	if s.check != nil && s.check.scroll.Key(k, len(s.check.lines)) {
		return nil
	}
	if s.check == nil && s.list.Key(k, len(s.items)) {
		return nil
	}
	switch k {
	case "r":
		s.note = ""
		return s.Refresh()
	case "E", "enter":
		return s.edit()
	case "esc":
		s.check, s.pending, s.awaiting = nil, "", false
	}
	return nil
}

// edit hands the selected canonical file to the shell. The shell re-validates
// the path (symlinks included) and runs `check` once the editor exits 0.
func (s *Screen) edit() tea.Cmd {
	it, ok := s.selected()
	if !ok {
		return nil
	}
	p, err := PathFor(s.home, s.kind, it.Name)
	if err != nil {
		s.note = "error: " + mask.FirstLine(err.Error())
		return nil
	}
	if fi, err := os.Lstat(p); err != nil || !fi.Mode().IsRegular() {
		s.note = staleNote
		return s.Refresh()
	}
	s.note, s.check, s.pending, s.awaiting = "", nil, p, false
	return func() tea.Msg { return ui.EditFileMsg{Path: p, After: []actions.Action{actions.Check()}} }
}

// onOutcome tracks what became of this screen's own edit; outcomes for any
// other path (another screen's edit) are ignored.
func (s *Screen) onOutcome(m ui.EditOutcomeMsg) {
	if s.pending == "" || m.Path != s.pending {
		return
	}
	switch {
	case m.Finished:
		s.pending = ""
		if m.Err != "" { // editor failed: the shell skips the check
			s.awaiting = false
		}
	case m.Started:
		s.awaiting = true
	default: // refused
		s.pending, s.awaiting = "", false
	}
}

// onRun records the `check` the shell ran after this screen's edit.
func (s *Screen) onRun(m ui.RunMsg) {
	if !s.awaiting || !slices.Equal(m.Action.Argv, actions.Check().Argv) {
		return
	}
	s.awaiting = false // consumed
	r := &checkResult{exit: m.Result.ExitCode}
	switch {
	case errors.Is(m.Err, runner.ErrNotInstalled):
		r.state, r.lines = checkNotRun, []string{"agentsync is not installed"}
	case m.Err != nil:
		r.state, r.lines = checkNotRun, []string{mask.FirstLine(m.Err.Error())}
	case m.Result.ExitCode != 0:
		r.state = checkFailed
		r.lines, r.trunc = storedLines(m.Result.Stderr, m.Result.Stdout)
	default:
		r.state = checkPassed
		r.lines, r.trunc = storedLines(m.Result.Stdout, m.Result.Stderr)
	}
	s.check = r
}

// storedLines masks and sanitises both streams, then caps the result at
// ui.MaxOutput (ui.CappedStreams: mask first, so a cut cannot leave the
// prefix of a secret behind); nothing raw is kept.
func storedLines(first, second string) ([]string, bool) {
	text, trunc := ui.CappedStreams(strings.TrimSpace(first), strings.TrimSpace(second), true)
	if text = strings.TrimSpace(text); text == "" {
		return nil, trunc
	}
	return strings.Split(text, "\n"), trunc
}

// paneHeight is the number of output lines the result pane shows.
func (s *Screen) paneHeight() int { return max(s.height/3, 5) }
