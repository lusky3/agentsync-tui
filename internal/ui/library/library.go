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
	"sort"
	"strconv"
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
	memoryRoot  = "AGENTS" // memory/AGENTS.md; fragments live in memory/fragments/
	listTimeout = 30 * time.Second
	maxStored   = 64 << 10 // cap on stored check output (bytes, after masking)
	staleNote   = "item changed — refreshed"
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

// checkResult is the last `check` outcome shown in the result pane. lines is
// masked and sanitised before it is stored.
type checkResult struct {
	state  string // "passed", "failed" or "could not run"
	exit   int
	lines  []string
	scroll int
}

// nextID hands every Screen a process-unique identity: the shell broadcasts
// unknown messages to all screens, so a result must name its owner.
var nextID atomic.Uint64

// Screen is one library tab.
type Screen struct {
	id    uint64
	d     ui.Deps
	kind  Kind
	run   runner.Runner
	scope runner.Scope
	home  string // canonical tree of the current scope (memory list, edit paths)
	gen   int    // refresh generation; older results are dropped

	loaded   bool
	items    []model.Item
	cursor   int
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
	return &Screen{id: nextID.Add(1), d: d, kind: kind, run: d.Runner, scope: d.Scope, home: ui.HomeFor(d, d.Scope), width: 80, height: 24}
}

// Title implements ui.Screen.
func (s *Screen) Title() string { return infos[s.kind].title }

// Init is exactly Refresh.
func (s *Screen) Init() tea.Cmd { return s.Refresh() }

// SetScope implements ui.ScopeAware: swap the runner and the home (memory
// is listed from it and every edit path is built under it) and drop the
// items loaded from the old tree.
func (s *Screen) SetScope(r runner.Runner, sc runner.Scope, home string) {
	s.run, s.scope, s.home = r, sc, home
	s.gen++ // results of reads started under the old scope are dropped
	s.loaded, s.items, s.loadNote, s.note, s.cursor = false, nil, "", "", 0
	s.pending, s.awaiting, s.check = "", false, nil
}

// CapturesKeys implements ui.KeyCapturer: no text input here.
func (s *Screen) CapturesKeys() bool { return false }

// Refresh re-reads the list; nothing runs inside Update.
func (s *Screen) Refresh() tea.Cmd {
	s.gen++
	id, gen, kind, r, home, base := s.id, s.gen, s.kind, s.run, s.home, ui.BaseCtx(s.d)
	if kind == Memory {
		return func() tea.Msg { return readMemory(id, gen, home) }
	}
	return func() tea.Msg { return readList(base, id, gen, kind, r) }
}

func readList(base context.Context, id uint64, gen int, kind Kind, r runner.Runner) listMsg {
	if r == nil {
		return listMsg{id: id, gen: gen, note: "no runner"}
	}
	ctx, cancel := context.WithTimeout(base, listTimeout)
	defer cancel()
	word := infos[kind].word
	res, err := r.Run(ctx, word, "list")
	switch {
	case errors.Is(err, runner.ErrNotInstalled):
		return listMsg{id: id, gen: gen, note: "agentsync is not installed"}
	case err != nil:
		return listMsg{id: id, gen: gen, note: oneLine(mask.Text(err.Error()))}
	case res.ExitCode != 0:
		n := word + " list exited " + strconv.Itoa(res.ExitCode)
		if l := firstLine(mask.Text(res.Stderr)); l != "" {
			n += ": " + l
		}
		return listMsg{id: id, gen: gen, note: n}
	}
	items, err := model.ParseList(res.Stdout)
	if err != nil {
		return listMsg{id: id, gen: gen, note: word + " list output unreadable"}
	}
	for i := range items {
		items[i].Detail = oneLine(mask.Text(items[i].Detail))
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
	sort.Slice(frags, func(i, j int) bool { return frags[i].Name < frags[j].Name })
	return listMsg{id: id, gen: gen, items: append(items, frags...)}
}

// Update implements tea.Model.
func (s *Screen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
	case listMsg:
		if msg.id == s.id && msg.gen == s.gen {
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
	s.cursor = max(min(s.cursor, len(s.items)-1), 0)
}

func (s *Screen) selected() (model.Item, bool) {
	if s.cursor < 0 || s.cursor >= len(s.items) {
		return model.Item{}, false
	}
	return s.items[s.cursor], true
}

func (s *Screen) keyPress(k string) tea.Cmd {
	switch k {
	case "up", "k":
		s.cursor = max(s.cursor-1, 0)
	case "down", "j":
		s.cursor = max(min(s.cursor+1, len(s.items)-1), 0)
	case "r":
		s.note = ""
		return s.Refresh()
	case "E", "enter":
		return s.edit()
	case "esc":
		s.check, s.pending, s.awaiting = nil, "", false
	case "ctrl+d":
		s.scrollBy(s.paneHeight() / 2)
	case "ctrl+u":
		s.scrollBy(-s.paneHeight() / 2)
	case "pgdown", "pgdn":
		s.scrollBy(s.paneHeight())
	case "pgup":
		s.scrollBy(-s.paneHeight())
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
		s.note = "error: " + oneLine(mask.Text(err.Error()))
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
	if !s.awaiting || len(m.Action.Argv) != 1 || m.Action.Argv[0] != "check" {
		return
	}
	s.awaiting = false // consumed
	r := &checkResult{exit: m.Result.ExitCode}
	switch {
	case errors.Is(m.Err, runner.ErrNotInstalled):
		r.state, r.lines = "could not run", []string{"agentsync is not installed"}
	case m.Err != nil:
		r.state, r.lines = "could not run", []string{firstLine(mask.Text(m.Err.Error()))}
	case m.Result.ExitCode != 0:
		r.state, r.lines = "failed", storedLines(m.Result.Stderr, m.Result.Stdout)
	default:
		r.state, r.lines = "passed", storedLines(m.Result.Stdout, m.Result.Stderr)
	}
	s.check = r
}

// storedLines masks and sanitises the streams, then caps the result at
// maxStored bytes; nothing raw is kept. The raw input is cut at rawCap
// first, on a line boundary: a secret never straddles a line, so a cut
// inside one cannot leave a half-masked value behind.
func storedLines(first, second string) []string {
	const rawCap = 4 * maxStored
	in := func(x string) string {
		if len(x) > rawCap {
			x = x[:rawCap]
			if i := strings.LastIndexByte(x, '\n'); i >= 0 {
				x = x[:i]
			}
		}
		return strings.TrimSpace(mask.Text(x))
	}
	text := strings.TrimSpace(in(first) + "\n" + in(second))
	if len(text) > maxStored {
		text = strings.ToValidUTF8(text[:maxStored], "")
	}
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

func (s *Screen) scrollBy(n int) {
	if s.check == nil {
		return
	}
	limit := max(len(s.check.lines)-s.paneHeight(), 0)
	s.check.scroll = max(min(s.check.scroll+n, limit), 0)
}

// paneHeight is the number of output lines the result pane shows.
func (s *Screen) paneHeight() int { return max(s.height/3, 5) }

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
