package library

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/model"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

const canary = "CANARYx1"

// ---- fakes and helpers ------------------------------------------------------

type fakeRunner struct {
	mu    sync.Mutex
	calls []string
	fn    func(args ...string) (runner.Result, error)
}

func (f *fakeRunner) Run(ctx context.Context, args ...string) (runner.Result, error) {
	if ui.IsChipCheck(ctx) { // the shell header chip's background checks are not the screen's calls
		return runner.Result{}, nil
	}
	f.mu.Lock()
	f.calls = append(f.calls, strings.Join(args, " "))
	fn := f.fn
	f.mu.Unlock()
	return fn(args...)
}

func (f *fakeRunner) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func stdoutRunner(out string) *fakeRunner {
	return &fakeRunner{fn: func(...string) (runner.Result, error) { return runner.Result{Stdout: out}, nil }}
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var kindWord = map[Kind]string{Skill: "skill", Subagent: "subagent", Command: "command", Hook: "hook", LSP: "lsp"}

// writeItem creates the canonical regular file for (kind, name) under home.
func writeItem(t *testing.T, home string, kind Kind, name string) string {
	t.Helper()
	p, err := PathFor(home, kind, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func newDeps(home string, r runner.Runner) ui.Deps {
	return ui.Deps{Runner: r, AgentsyncHome: home, Scope: runner.ScopeUser}
}

// drive runs cmd (through batches), feeds results into s and returns the
// shell-bound messages (EditFileMsg / DoMsg).
func drive(s *Screen, cmd tea.Cmd) []tea.Msg {
	var out []tea.Msg
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch m := c().(type) {
		case nil:
		case tea.BatchMsg:
			queue = append(queue, m...)
		case ui.DoMsg, ui.EditFileMsg:
			out = append(out, m)
		default:
			_, next := s.Update(m)
			queue = append(queue, next)
		}
	}
	return out
}

var namedKeys = map[string]rune{
	"esc": tea.KeyEscape, "enter": tea.KeyEnter, "up": tea.KeyUp, "down": tea.KeyDown, "left": tea.KeyLeft,
	"right": tea.KeyRight, "tab": tea.KeyTab, "backspace": tea.KeyBackspace, "delete": tea.KeyDelete,
	"insert": tea.KeyInsert, "home": tea.KeyHome, "end": tea.KeyEnd, "pgdown": tea.KeyPgDown, "pgup": tea.KeyPgUp,
}

// key builds the key press named like KeyPressMsg.String(): optional
// ctrl+/alt+/shift+ prefixes, then a named key, f1..f12 or one rune.
func key(s string) tea.KeyPressMsg {
	var mod tea.KeyMod
	rest := s
	for {
		switch {
		case strings.HasPrefix(rest, "ctrl+") && len(rest) > 5:
			mod, rest = mod|tea.ModCtrl, rest[5:]
		case strings.HasPrefix(rest, "alt+") && len(rest) > 4:
			mod, rest = mod|tea.ModAlt, rest[4:]
		case strings.HasPrefix(rest, "shift+") && len(rest) > 6:
			mod, rest = mod|tea.ModShift, rest[6:]
		default:
			goto done
		}
	}
done:
	if c, ok := namedKeys[rest]; ok {
		return tea.KeyPressMsg{Code: c, Mod: mod}
	}
	var n int
	if _, err := fmt.Sscanf(rest, "f%d", &n); err == nil && n >= 1 && n <= 12 && rest == fmt.Sprintf("f%d", n) {
		return tea.KeyPressMsg{Code: tea.KeyF1 + rune(n-1), Mod: mod}
	}
	if rest == "space" {
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " ", Mod: mod}
	}
	r := []rune(rest)[0]
	if mod != 0 {
		return tea.KeyPressMsg{Code: r, Mod: mod}
	}
	return tea.KeyPressMsg{Code: r, Text: rest}
}

func press(s *Screen, keys ...string) []tea.Msg {
	var out []tea.Msg
	for _, k := range keys {
		_, cmd := s.Update(key(k))
		out = append(out, drive(s, cmd)...)
	}
	return out
}

func view(s *Screen) string { return ansi.Strip(s.View().Content) }

// loaded builds a screen of the kind with files for every listed name, and
// loads it from the runner.
func loaded(t *testing.T, kind Kind, r runner.Runner, itemNames ...string) (*Screen, string) {
	t.Helper()
	home := t.TempDir()
	for _, n := range itemNames {
		writeItem(t, home, kind, n)
	}
	s := New(newDeps(home, r), kind)
	drive(s, s.Init())
	return s, home
}

func names(s *Screen) []string {
	out := []string{}
	for _, it := range s.items {
		out = append(out, it.Name)
	}
	return out
}

func onlyEdit(t *testing.T, msgs []tea.Msg) ui.EditFileMsg {
	t.Helper()
	if len(msgs) != 1 {
		t.Fatalf("want one message, got %#v", msgs)
	}
	e, ok := msgs[0].(ui.EditFileMsg)
	if !ok {
		t.Fatalf("want EditFileMsg, got %#v", msgs[0])
	}
	return e
}

func noCanary(t *testing.T, where, s string) {
	t.Helper()
	if strings.Contains(s, "CANARY") {
		t.Errorf("%s leaks the canary:\n%s", where, s)
	}
}

// ---- listing ----------------------------------------------------------------

func TestListsItemsPerKind(t *testing.T) {
	want := map[Kind][]string{
		Skill: {"greet", "plan-it"}, Subagent: {"reviewer"}, Command: {"deploy", "test-all"},
		Hook: {"PreToolUse"}, LSP: {"gopls", "rust-analyzer"},
	}
	titles := map[Kind]string{Skill: "Skills", Subagent: "Subagents", Command: "Commands", Hook: "Hooks", LSP: "LSP", Memory: "Memory"}
	for kind, w := range want {
		t.Run(kindWord[kind], func(t *testing.T) {
			fr := stdoutRunner(fixture(t, kindWord[kind]+"-list.txt"))
			s, _ := loaded(t, kind, fr)
			if !reflect.DeepEqual(names(s), w) {
				t.Errorf("items %v, want %v", names(s), w)
			}
			if got := fr.callList(); len(got) != 1 || got[0] != kindWord[kind]+" list" {
				t.Errorf("calls %q", got)
			}
			v := view(s)
			for _, n := range w {
				if !strings.Contains(v, n) {
					t.Errorf("view lacks %q:\n%s", n, v)
				}
			}
			if s.Title() != titles[kind] {
				t.Errorf("title %q", s.Title())
			}
		})
	}
	if New(ui.Deps{}, Memory).Title() != "Memory" {
		t.Error("memory title")
	}
}

func TestMemoryListsFilesDirectly(t *testing.T) {
	home := t.TempDir()
	frag := filepath.Join(home, "memory", "fragments")
	if err := os.MkdirAll(frag, 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(p string) {
		if err := os.WriteFile(p, []byte("m\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(home, "memory", "AGENTS.md"))
	write(filepath.Join(frag, "b.md"))
	write(filepath.Join(frag, "a.md"))
	write(filepath.Join(frag, "notes.txt"))
	write(filepath.Join(frag, "bad name.md"))
	write(filepath.Join(frag, "-x.md"))
	write(filepath.Join(frag, ".hid.md"))
	write(filepath.Join(frag, "AGENTS.md")) // would alias the root file
	_ = os.Mkdir(filepath.Join(frag, "dir.md"), 0o700)
	if err := os.Symlink(filepath.Join(frag, "a.md"), filepath.Join(frag, "link.md")); err != nil {
		t.Fatal(err)
	}
	fr := stdoutRunner("should not be used")
	s := New(newDeps(home, fr), Memory)
	drive(s, s.Init())
	if !reflect.DeepEqual(names(s), []string{"AGENTS", "a", "b"}) {
		t.Errorf("items %v", names(s))
	}
	if len(fr.callList()) != 0 {
		t.Errorf("memory must not call the CLI: %q", fr.callList())
	}
	// A symlinked AGENTS.md at the root is skipped too.
	_ = os.Remove(filepath.Join(home, "memory", "AGENTS.md"))
	if err := os.Symlink(filepath.Join(frag, "a.md"), filepath.Join(home, "memory", "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
	drive(s, s.Refresh())
	if !reflect.DeepEqual(names(s), []string{"a", "b"}) {
		t.Errorf("items after symlinked root %v", names(s))
	}
}

func TestEmptyStatePerKind(t *testing.T) {
	want := map[Kind]string{
		Skill:    "no skills found — create files under skills/ or capture them with `import`",
		Subagent: "no subagents found — create files under subagents/ or capture them with `import`",
		Command:  "no commands found — create files under commands/ or capture them with `import`",
		Hook:     "no hooks found — create files under hooks/ or capture them with `import`",
		LSP:      "no LSP servers found — create files under lsp/ or capture them with `import`",
		Memory:   "no memory files found — create files under memory/ or capture them with `import`",
	}
	for kind, msg := range want {
		t.Run(msg[:12], func(t *testing.T) {
			var r runner.Runner
			if kind != Memory {
				r = stdoutRunner(fixture(t, kindWord[kind]+"-list-empty.txt"))
			}
			s, _ := loaded(t, kind, r)
			if v := strings.Join(strings.Fields(view(s)), " "); !strings.Contains(v, msg) {
				t.Errorf("view:\n%s", view(s))
			}
			if got := press(s, "E", "enter", "j", "k"); len(got) != 0 {
				t.Errorf("empty list emitted %#v", got)
			}
		})
	}
}

func TestListFailureKeepsLastList(t *testing.T) {
	fr := stdoutRunner("alpha\nbeta\n")
	s, _ := loaded(t, Skill, fr, "alpha", "beta")
	fail := func(res runner.Result, err error) {
		fr.mu.Lock()
		fr.fn = func(...string) (runner.Result, error) { return res, err }
		fr.mu.Unlock()
		drive(s, s.Refresh())
		if !reflect.DeepEqual(names(s), []string{"alpha", "beta"}) {
			t.Errorf("list lost: %v", names(s))
		}
		noCanary(t, "view", view(s))
	}
	fail(runner.Result{ExitCode: 1, Stderr: "password=" + canary + "\nsecond-line"}, nil)
	if v := view(s); !strings.Contains(v, "exited 1") || strings.Contains(v, "second-line") {
		t.Errorf("failure note:\n%s", v)
	}
	fail(runner.Result{}, errors.New("spawn: token="+canary))
	if v := view(s); !strings.Contains(v, "unavailable") {
		t.Errorf("spawn note:\n%s", v)
	}
	fail(runner.Result{}, runner.ErrNotInstalled)
	if v := strings.Join(strings.Fields(view(s)), " "); !strings.Contains(v, "not installed") {
		t.Errorf("not-installed note:\n%s", v)
	}
	fail(runner.Result{Stdout: "✗ ERROR  nonsense " + canary + "\n"}, nil)
	if v := view(s); !strings.Contains(v, "unreadable") {
		t.Errorf("unreadable note:\n%s", v)
	}
	// recovery clears the note
	fr.mu.Lock()
	fr.fn = func(...string) (runner.Result, error) { return runner.Result{Stdout: "gamma\n"}, nil }
	fr.mu.Unlock()
	drive(s, s.Refresh())
	if !reflect.DeepEqual(names(s), []string{"gamma"}) || strings.Contains(view(s), "unavailable") {
		t.Errorf("recovery: %v\n%s", names(s), view(s))
	}
}

func TestOutputStoredMasked(t *testing.T) {
	fr := stdoutRunner("alpha\nbeta\n")
	s, _ := loaded(t, Skill, fr, "alpha", "beta")
	noCanary(t, "view", view(s))
	startEdit(t, s)
	s.Update(ui.RunMsg{Action: actions.Check(), Result: runner.Result{Stdout: "password=" + canary + "\nline2", Stderr: "token=" + canary}})
	if s.check == nil {
		t.Fatal("no check result stored")
	}
	noCanary(t, "stored check", strings.Join(s.check.lines, "\n"))
	noCanary(t, "view", view(s))
}

// ---- editing ----------------------------------------------------------------

func TestEditEmitsEditFileMsgWithCheck(t *testing.T) {
	for _, k := range []string{"E", "enter"} {
		s, home := loaded(t, Skill, stdoutRunner("alpha\nbeta\n"), "alpha", "beta")
		press(s, "j")
		e := onlyEdit(t, press(s, k))
		want, _ := PathFor(home, Skill, "beta")
		if e.Path != want {
			t.Errorf("%s: path %q want %q", k, e.Path, want)
		}
		if !reflect.DeepEqual(e.After, []actions.Action{actions.Check()}) {
			t.Errorf("%s: after %#v", k, e.After)
		}
	}
	// every kind maps to its canonical file
	for _, kind := range []Kind{Subagent, Command, Hook, LSP} {
		s, home := loaded(t, kind, stdoutRunner("one\n"), "one")
		e := onlyEdit(t, press(s, "E"))
		want, _ := PathFor(home, kind, "one")
		if e.Path != want {
			t.Errorf("kind %v path %q", kind, e.Path)
		}
	}
}

func TestStaleItemBlocksEdit(t *testing.T) {
	check := func(t *testing.T, mutate func(p string)) {
		fr := stdoutRunner("alpha\n")
		s, home := loaded(t, Skill, fr, "alpha")
		p, _ := PathFor(home, Skill, "alpha")
		mutate(p)
		before := len(fr.callList())
		if got := press(s, "E"); len(got) != 0 {
			t.Fatalf("stale item emitted %#v", got)
		}
		if len(fr.callList()) <= before {
			t.Error("no refresh after stale item")
		}
		if v := view(s); !strings.Contains(v, "item changed — refreshed") {
			t.Errorf("view:\n%s", v)
		}
	}
	t.Run("removed", func(t *testing.T) { check(t, func(p string) { _ = os.Remove(p) }) })
	t.Run("replaced by symlink", func(t *testing.T) {
		check(t, func(p string) {
			out := filepath.Join(t.TempDir(), "o.md")
			_ = os.WriteFile(out, []byte("o"), 0o600)
			_ = os.Remove(p)
			if err := os.Symlink(out, p); err != nil {
				t.Fatal(err)
			}
		})
	})
	t.Run("replaced by directory", func(t *testing.T) {
		check(t, func(p string) { _ = os.Remove(p); _ = os.Mkdir(p, 0o700) })
	})
}

func TestHostileNameNeverActionable(t *testing.T) {
	out := "good\n-rf\n../x\nevil\x1b[31m\nbidi\u202ex\na/b\nbell\x07\n"
	s, home := loaded(t, Skill, stdoutRunner(out), "good")
	if !reflect.DeepEqual(names(s), []string{"good"}) {
		t.Fatalf("items %v", names(s))
	}
	if got := onlyEdit(t, press(s, "E")); !strings.HasSuffix(got.Path, filepath.Join("skills", "good", "SKILL.md")) {
		t.Errorf("path %q", got.Path)
	}
	// White box: even if a hostile item reached the list, nothing is emitted.
	for _, bad := range []string{"-rf", "../x", "a\x1b[0mb", "a/b", ""} {
		s.items = append(s.items[:1], itemOf(bad))
		s.list.Cursor = 1
		if got := press(s, "E", "enter"); len(got) != 0 {
			t.Errorf("hostile %q emitted %#v", bad, got)
		}
	}
	_ = home
}

// ---- check result -----------------------------------------------------------

// startEdit presses E and tells the screen the shell started the editor.
func startEdit(t *testing.T, s *Screen) {
	t.Helper()
	e := onlyEdit(t, press(s, "E"))
	s.Update(ui.EditOutcomeMsg{Path: e.Path, Started: true})
}

func afterEdit(t *testing.T) *Screen {
	t.Helper()
	s, _ := loaded(t, Skill, stdoutRunner("alpha\n"), "alpha")
	startEdit(t, s)
	return s
}

func TestCheckResultShownMasked(t *testing.T) {
	s := afterEdit(t)
	s.Update(ui.RunMsg{Action: actions.Check(), Result: runner.Result{
		Stdout: "password=" + canary + "\nschema valid; all references resolve\n"}})
	v := view(s)
	noCanary(t, "view", v)
	for _, w := range []string{"check passed", "exit 0", "schema valid", "preview the effect with the Apply screen (p)"} {
		if !strings.Contains(v, w) {
			t.Errorf("view lacks %q:\n%s", w, v)
		}
	}
}

func TestCheckFailureShown(t *testing.T) {
	s := afterEdit(t)
	s.Update(ui.RunMsg{Action: actions.Check(), Result: runner.Result{ExitCode: 1,
		Stderr: "token=" + canary + "\n✗ ERROR  check: parse lsp/bad.toml: boom\n"}})
	v := view(s)
	noCanary(t, "view", v)
	noCanary(t, "stored", strings.Join(s.check.lines, "\n"))
	for _, w := range []string{"check failed", "exit 1", "boom"} {
		if !strings.Contains(v, w) {
			t.Errorf("view lacks %q:\n%s", w, v)
		}
	}
	// spawn error path
	s = afterEdit(t)
	s.Update(ui.RunMsg{Action: actions.Check(), Err: errors.New("exec: password=" + canary + "\nx")})
	v = view(s)
	noCanary(t, "spawn error view", v)
	if !strings.Contains(v, "check could not run") {
		t.Errorf("view:\n%s", v)
	}
	noCanary(t, "stored", strings.Join(s.check.lines, "\n"))
	s = afterEdit(t)
	s.Update(ui.RunMsg{Action: actions.Check(), Err: runner.ErrNotInstalled})
	if v := strings.Join(strings.Fields(view(s)), " "); !strings.Contains(v, "not installed") {
		t.Errorf("view:\n%s", v)
	}
}

func TestCheckResultOnlyAfterOwnEditAndOnlyCheck(t *testing.T) {
	s, _ := loaded(t, Skill, stdoutRunner("alpha\n"), "alpha")
	s.Update(ui.RunMsg{Action: actions.Check(), Result: runner.Result{Stdout: "x"}})
	if s.check != nil {
		t.Error("check result shown without an edit from this screen")
	}
	startEdit(t, s)
	s.Update(ui.RunMsg{Action: actions.Doctor(), Result: runner.Result{Stdout: "other"}})
	if s.check != nil {
		t.Error("non-check RunMsg shown")
	}
	s.Update(ui.RunMsg{Action: actions.Check(), Result: runner.Result{Stdout: "ok"}})
	if s.check == nil {
		t.Fatal("check result missing")
	}
	press(s, "esc")
	if s.check != nil {
		t.Error("esc must close the result pane")
	}
}

func TestCheckResultCappedAndScrollable(t *testing.T) {
	s := afterEdit(t)
	var b strings.Builder
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&b, "line %03d %s\n", i, strings.Repeat("x", 600))
	}
	s.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	s.Update(ui.RunMsg{Action: actions.Check(), Result: runner.Result{ExitCode: 1, Stdout: b.String()}})
	if n := len(strings.Join(s.check.lines, "\n")); n > ui.MaxOutput {
		t.Errorf("stored %d bytes > cap", n)
	}
	if !strings.Contains(view(s), "line 000") {
		t.Fatalf("first line missing:\n%s", view(s))
	}
	for _, k := range []string{"ctrl+d", "pgdown"} {
		before := s.check.scroll.Offset
		press(s, k)
		if s.check.scroll.Offset <= before {
			t.Errorf("%s did not scroll", k)
		}
	}
	for _, k := range []string{"ctrl+u", "pgup"} {
		before := s.check.scroll.Offset
		press(s, k)
		if s.check.scroll.Offset >= before {
			t.Errorf("%s did not scroll back", k)
		}
	}
}

// ---- keys -------------------------------------------------------------------

func allKeys() []string {
	keys := []string{"esc", "enter", "up", "down", "left", "right", "tab", "backspace", "delete", "insert", "home", "end",
		"pgdown", "pgup", "space", "shift+tab", "shift+delete", "shift+enter", "shift+up", "shift+down", "ctrl+enter"}
	for r := '!'; r <= '~'; r++ {
		keys = append(keys, string(r))
	}
	for r := 'a'; r <= 'z'; r++ {
		keys = append(keys, "ctrl+"+string(r), "alt+"+string(r), "alt+shift+"+string(r), "ctrl+alt+"+string(r))
	}
	for r := 'A'; r <= 'Z'; r++ {
		keys = append(keys, "alt+"+string(r))
	}
	for r := '0'; r <= '9'; r++ {
		keys = append(keys, "alt+"+string(r))
	}
	for n := 1; n <= 12; n++ {
		keys = append(keys, fmt.Sprintf("f%d", n), fmt.Sprintf("shift+f%d", n), fmt.Sprintf("alt+f%d", n), fmt.Sprintf("ctrl+f%d", n))
	}
	keys = append(keys, "alt+enter", "alt+backspace", "alt+delete", "alt+insert", "alt+up", "alt+down", "alt+left",
		"alt+right", "alt+tab", "alt+esc", "alt+home", "alt+end", "alt+pgup", "alt+pgdown", "alt+space", "ctrl+delete")
	return keys
}

func TestNoCreateDeleteKeys(t *testing.T) {
	keys := allKeys()
	reached, edits := 0, map[string]bool{}
	for _, k := range keys {
		// The constructed press must really be the key it is named after.
		if got := key(k).String(); got != k {
			t.Fatalf("key(%q).String() = %q: the harness does not send what it names", k, got)
		}
		s, _ := loaded(t, Skill, stdoutRunner("alpha\nbeta\n"), "alpha", "beta") // fresh per key
		if len(s.items) != 2 {
			t.Fatalf("setup: %v", names(s))
		}
		reached++
		for _, m := range press(s, k) {
			switch m.(type) {
			case ui.EditFileMsg:
				edits[k] = true
			default:
				t.Errorf("key %q emitted %#v", k, m)
			}
		}
	}
	if reached != len(keys) || reached < 300 {
		t.Errorf("reached %d of %d keys", reached, len(keys))
	}
	if len(edits) != 2 || !edits["E"] || !edits["enter"] {
		t.Errorf("edit emitted for %v, want exactly E and enter", edits)
	}
	// And a full burst on one screen stays free of DoMsg.
	s, _ := loaded(t, Skill, stdoutRunner("alpha\n"), "alpha")
	for _, m := range press(s, keys...) {
		if _, ok := m.(ui.DoMsg); ok {
			t.Errorf("burst emitted %#v", m)
		}
	}
}

// ---- shell integration ------------------------------------------------------

func TestScopeAwareSwapsRunner(t *testing.T) {
	r1 := stdoutRunner("one\n")
	s, _ := loaded(t, Skill, r1, "one", "two")
	var sa ui.ScopeAware = s
	r2 := stdoutRunner("two\n")
	sa.SetScope(r2, runner.ScopeProject, s.home)
	drive(s, s.Refresh())
	if len(r2.callList()) != 1 || len(r1.callList()) != 1 {
		t.Errorf("r1 %q r2 %q", r1.callList(), r2.callList())
	}
	if !reflect.DeepEqual(names(s), []string{"two"}) {
		t.Errorf("items %v", names(s))
	}
}

func TestInitEqualsRefresh(t *testing.T) {
	run := func(useInit bool) ([]string, []string) {
		fr := stdoutRunner("a\nb\n")
		s := New(newDeps(t.TempDir(), fr), Skill)
		cmd := s.Refresh
		if useInit {
			cmd = s.Init
		}
		drive(s, cmd())
		return fr.callList(), names(s)
	}
	c1, n1 := run(true)
	c2, n2 := run(false)
	if !reflect.DeepEqual(c1, c2) || !reflect.DeepEqual(n1, n2) || len(c1) != 1 {
		t.Errorf("init %q %v vs refresh %q %v", c1, n1, c2, n2)
	}
}

func TestStaleListResultsDropped(t *testing.T) {
	s := New(newDeps(t.TempDir(), stdoutRunner("a\n")), Skill)
	c1 := s.Refresh()
	c2 := s.Refresh()
	drive(s, c2)
	drive(s, c1) // older generation arrives late
	if !reflect.DeepEqual(names(s), []string{"a"}) {
		t.Errorf("items %v", names(s))
	}
	s.reads.Supersede()
	s.Update(listMsg{id: s.id, gen: s.reads.Gen() - 1, items: nil})
	if len(s.items) != 1 {
		t.Error("stale listMsg applied")
	}
}

func TestKeyCapturerFalse(t *testing.T) {
	var kc ui.KeyCapturer = New(ui.Deps{}, Skill)
	if kc.CapturesKeys() {
		t.Error("library screens have no text input")
	}
	var _ ui.Screen = New(ui.Deps{}, Skill)
}

func itemOf(n string) model.Item { return model.Item{Name: n} }

// storedLines masks before it caps: a secret that straddles the cap must
// not survive as a bare prefix, and the cut falls on a line boundary.
func TestStoredLinesMasksBeforeCap(t *testing.T) {
	filler := strings.Repeat("x", 99) + "\n" // 100 bytes per line
	var b strings.Builder
	for b.Len() < ui.MaxOutput-20 {
		b.WriteString(filler)
	}
	// The secret starts 20 bytes before the cap, so a raw cut would keep
	// "CANARYx1secr" with no key to mask it by.
	b.WriteString(`"api_key": "CANARYx1secret_value_that_runs_past_the_cap"` + "\n")
	lines, trunc := storedLines(b.String(), "")
	if !trunc {
		t.Fatal("input over the cap not reported as truncated")
	}
	if n := len(strings.Join(lines, "\n")); n > ui.MaxOutput {
		t.Errorf("stored %d bytes > cap", n)
	}
	for _, l := range lines {
		if strings.Contains(l, "CANARY") || strings.Contains(l, "secr") {
			t.Fatalf("leaked across the cap: %q", l)
		}
		if len(l) != 99 && l != `"api_key": "`+"••••"+`"` {
			t.Errorf("cut inside a line: %q", l)
		}
	}
	if got, tr := storedLines("ok\n", ""); len(got) != 1 || got[0] != "ok" || tr {
		t.Errorf("small input changed: %q %v", got, tr)
	}
}
