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

// Screen is one library tab.
type Screen struct {
	d     ui.Deps
	kind  Kind
	run   runner.Runner
	scope runner.Scope
	gen   int // refresh generation; older results are dropped

	loaded   bool
	items    []model.Item
	cursor   int
	loadNote string // why the last refresh failed, display-safe; last good list kept
	note     string // last screen message, display-safe

	awaiting bool // this screen asked the shell to edit; a check result may follow
	check    *checkResult

	width, height int
}

// listMsg carries one list read.
type listMsg struct {
	gen   int
	items []model.Item
	note  string // non-empty: the read failed
}

// New returns the library screen for kind.
func New(d ui.Deps, kind Kind) *Screen {
	return &Screen{d: d, kind: kind, run: d.Runner, scope: d.Scope, width: 80, height: 24}
}

// Title implements ui.Screen.
func (s *Screen) Title() string { return infos[s.kind].title }

// Init is exactly Refresh.
func (s *Screen) Init() tea.Cmd { return s.Refresh() }

// SetScope implements ui.ScopeAware.
func (s *Screen) SetScope(r runner.Runner, sc runner.Scope) {
	s.run, s.scope = r, sc
	s.gen++ // results of reads started under the old scope are dropped
}

// CapturesKeys implements ui.KeyCapturer: no text input here.
func (s *Screen) CapturesKeys() bool { return false }

// Refresh re-reads the list; nothing runs inside Update.
func (s *Screen) Refresh() tea.Cmd {
	s.gen++
	gen, kind, r, home := s.gen, s.kind, s.run, s.d.AgentsyncHome
	if kind == Memory {
		return func() tea.Msg { return readMemory(gen, home) }
	}
	return func() tea.Msg { return readList(gen, kind, r) }
}

func readList(gen int, kind Kind, r runner.Runner) listMsg {
	if r == nil {
		return listMsg{gen: gen, note: "no runner"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
	defer cancel()
	word := infos[kind].word
	res, err := r.Run(ctx, word, "list")
	switch {
	case errors.Is(err, runner.ErrNotInstalled):
		return listMsg{gen: gen, note: "agentsync is not installed"}
	case err != nil:
		return listMsg{gen: gen, note: oneLine(mask.Text(err.Error()))}
	case res.ExitCode != 0:
		n := word + " list exited " + strconv.Itoa(res.ExitCode)
		if l := firstLine(mask.Text(res.Stderr)); l != "" {
			n += ": " + l
		}
		return listMsg{gen: gen, note: n}
	}
	items, err := model.ParseList(res.Stdout)
	if err != nil {
		return listMsg{gen: gen, note: word + " list output unreadable"}
	}
	for i := range items {
		items[i].Detail = oneLine(mask.Text(items[i].Detail))
	}
	return listMsg{gen: gen, items: items}
}

// readMemory lists memory/AGENTS.md and memory/fragments/*.md directly:
// regular files only (no symlinks, directories or odd names), sorted.
func readMemory(gen int, home string) listMsg {
	var items []model.Item
	if fi, err := os.Lstat(filepath.Join(home, "memory", "AGENTS.md")); err == nil && fi.Mode().IsRegular() {
		items = append(items, model.Item{Name: memoryRoot})
	}
	entries, err := os.ReadDir(filepath.Join(home, "memory", "fragments"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return listMsg{gen: gen, note: "cannot read memory/fragments"}
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
	return listMsg{gen: gen, items: append(items, frags...)}
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
		s.check, s.awaiting = nil, false
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
	p, err := PathFor(s.d.AgentsyncHome, s.kind, it.Name)
	if err != nil {
		s.note = "error: " + oneLine(mask.Text(err.Error()))
		return nil
	}
	if fi, err := os.Lstat(p); err != nil || !fi.Mode().IsRegular() {
		s.note = staleNote
		return s.Refresh()
	}
	s.note, s.check, s.awaiting = "", nil, true
	return func() tea.Msg { return ui.EditFileMsg{Path: p, After: []actions.Action{actions.Check()}} }
}

// onRun records the `check` the shell ran after this screen's edit.
func (s *Screen) onRun(m ui.RunMsg) {
	if !s.awaiting || len(m.Action.Argv) != 1 || m.Action.Argv[0] != "check" {
		return
	}
	s.awaiting = false
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
// maxStored bytes; nothing raw is kept.
func storedLines(first, second string) []string {
	const rawCap = 4 * maxStored
	in := func(x string) string {
		if len(x) > rawCap {
			x = x[:rawCap]
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
