package sourcerepo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/gitrepo"
	"github.com/lusky3/agentsync-tui/internal/gitrepo/gittest"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/runner/runnertest"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

const (
	tomlX      = "mcp/x.toml"
	skillA     = "skills/a/SKILL.md"
	ghpToken   = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"
	opaqueWord = "Zx9Kq2Lm7Pw4Rt8Vn3Bc6Hj5"
	baseTOML   = "[servers.x]\ncommand = \"npx\"\n"
)

// recGit records every git argv and answers through the real hardened
// git Program, unless override answers first.
type recGit struct {
	inner    runner.Runner
	mu       sync.Mutex
	calls    [][]string
	override func(args []string) (runner.Result, bool)
}

func (r *recGit) Run(ctx context.Context, args ...string) (runner.Result, error) {
	r.mu.Lock()
	r.calls = append(r.calls, append([]string(nil), args...))
	over := r.override
	r.mu.Unlock()
	if over != nil {
		if res, ok := over(args); ok {
			return res, nil
		}
	}
	return r.inner.Run(ctx, args...)
}

func (r *recGit) all() [][]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

// gitCmd is the git subcommand of argv (after -C <dir>).
func gitCmd(argv []string) string {
	if len(argv) > 2 && argv[0] == "-C" {
		return argv[2]
	}
	if len(argv) > 0 {
		return argv[0]
	}
	return ""
}

type fixture struct {
	t     *testing.T
	home  string
	s     *Screen
	git   *recGit
	agent *runnertest.Fake
}

func write(t *testing.T, dir, rel, body string) {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newRepo makes a home that is a git repository with one commit.
func newRepo(t *testing.T) string {
	t.Helper()
	gittest.Need(t)
	home := filepath.Join(t.TempDir(), "home")
	gittest.Init(t, home)
	gittest.Commit(t, home, "init", map[string]string{"agentsync.toml": "# config\n", tomlX: baseTOML})
	return home
}

func deps(home string, agent, git, chezmoi runner.Runner) ui.Deps {
	return ui.Deps{Runner: agent, Git: git, Chezmoi: chezmoi, Scope: runner.ScopeUser, AgentsyncHome: home}
}

// wideView is the fixture's width: no line of a test view is cut, however
// long the temp paths are.
const wideView = 1000

// open builds the screen over home and loads it.
func open(t *testing.T, home string) *fixture {
	t.Helper()
	f := &fixture{t: t, home: home, git: &recGit{inner: gittest.NewGit(t)}, agent: runnertest.New()}
	f.s = New(deps(home, f.agent, f.git, nil))
	// Wide enough that a long temp path (TMPDIR anywhere) is never cut.
	f.s.Update(tea.WindowSizeMsg{Width: wideView, Height: 80})
	drive(f.s, f.s.Init())
	if !f.s.loaded {
		t.Fatal("screen did not load")
	}
	return f
}

// drive runs cmd (through batches), feeds every screen message back into s
// and returns the shell-bound messages (DoMsg, GotoMsg).
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
		case ui.DoMsg, ui.GotoMsg:
			out = append(out, m)
		default:
			_, next := s.Update(m)
			queue = append(queue, next)
		}
	}
	return out
}

var namedKeys = map[string]rune{
	"esc": tea.KeyEscape, "enter": tea.KeyEnter, "up": tea.KeyUp, "down": tea.KeyDown,
	"backspace": tea.KeyBackspace, "home": tea.KeyHome, "end": tea.KeyEnd, "tab": tea.KeyTab,
}

func key(s string) tea.KeyPressMsg {
	if c, ok := namedKeys[s]; ok {
		return tea.KeyPressMsg{Code: c}
	}
	if s == "space" {
		return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

func press(s *Screen, keys ...string) []tea.Msg {
	var out []tea.Msg
	for _, k := range keys {
		_, cmd := s.Update(key(k))
		out = append(out, drive(s, cmd)...)
	}
	return out
}

// typeText types text one rune at a time.
func typeText(s *Screen, text string) {
	for _, r := range text {
		k := string(r)
		if r == ' ' {
			k = "space"
		}
		press(s, k)
	}
}

func view(s *Screen) string { return ansi.Strip(s.View().Content) }

// rowOf is the index of the listed file with path p, or -1.
func rowOf(s *Screen, p string) int {
	return slices.IndexFunc(s.status.Files, func(f gitrepo.File) bool { return f.Path == p })
}

// selectRow moves the cursor to p and toggles it.
func selectRow(t *testing.T, s *Screen, p string) {
	t.Helper()
	i := rowOf(s, p)
	if i < 0 {
		t.Fatalf("%q not listed", p)
	}
	s.list.Cursor = i
	press(s, "space")
}

func paths(s *Screen) []string {
	var out []string
	for _, f := range s.status.Files {
		out = append(out, f.Path)
	}
	return out
}

func TestListsChangedFilesWithDefaults(t *testing.T) {
	home := newRepo(t)
	write(t, home, tomlX, baseTOML+"args = [\"-y\"]\n")
	write(t, home, skillA, "# a\n")
	write(t, home, "notes.txt", "hello\n")
	f := open(t, home)
	if got, want := paths(f.s), []string{tomlX, "notes.txt", skillA}; !slices.Equal(got, want) {
		t.Fatalf("listed %v, want %v", got, want)
	}
	if !f.s.sel[tomlX] || !f.s.sel[skillA] || f.s.sel["notes.txt"] {
		t.Errorf("default selection %v", f.s.sel)
	}
	v := view(f.s)
	for _, want := range []string{"git", "repo: " + resolved(t, home), "home: .", "main", "no upstream",
		"modified", "untracked", "owned", "other", "not an agentsync file"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	selectRow(t, f.s, "notes.txt")
	if !f.s.sel["notes.txt"] {
		t.Errorf("space did not select: %v", f.s.sel)
	}
	press(f.s, "a")
	if len(f.s.selectedPaths()) != 0 {
		t.Errorf("a with all selected must clear: %v", f.s.sel)
	}
	press(f.s, "a")
	if len(f.s.selectedPaths()) != 3 {
		t.Errorf("a must select every selectable file: %v", f.s.sel)
	}
}

func resolved(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestNoNetworkOnRefreshOrFocus(t *testing.T) {
	home := newRepo(t)
	write(t, home, tomlX, baseTOML+"args = []\n")
	f := open(t, home)
	drive(f.s, f.s.Refresh())
	press(f.s, "r", "d", "esc", "j", "k")
	for _, c := range f.git.all() {
		switch gitCmd(c) {
		case "fetch", "pull", "push", "ls-remote", "remote", "clone":
			t.Errorf("network-capable git call on refresh/focus: %v", c)
		}
	}
	if len(f.git.all()) == 0 {
		t.Fatal("no git calls recorded")
	}
}

func TestDiffViewMasked(t *testing.T) {
	home := newRepo(t)
	write(t, home, tomlX, baseTOML+"env = { API_KEY = \""+ghpToken+"\" }\n# \x1b]0;evil\x07 title\n")
	f := open(t, home)
	f.s.list.Cursor = rowOf(f.s, tomlX)
	press(f.s, "enter")
	if f.s.mode != modeDiff {
		t.Fatalf("mode %v, want diff", f.s.mode)
	}
	v := view(f.s)
	if !strings.Contains(v, "••••") || strings.Contains(v, ghpToken) || strings.Contains(v, "ghp_") {
		t.Errorf("diff not masked:\n%s", v)
	}
	if strings.ContainsRune(v, '\x1b') || strings.ContainsRune(strings.Join(f.s.diff.lines, "\n"), '\x1b') {
		t.Error("diff holds an ESC")
	}
	if !strings.Contains(v, "command") {
		t.Errorf("diff lacks context:\n%s", v)
	}
	press(f.s, "esc")
	if f.s.mode != modeList {
		t.Errorf("esc: mode %v", f.s.mode)
	}
	// An untracked file is shown as added lines, masked too.
	write(t, home, "mcp/y.toml", "[servers.y]\nenv = { TOKEN = \""+ghpToken+"\" }\n")
	drive(f.s, f.s.Refresh())
	f.s.list.Cursor = rowOf(f.s, "mcp/y.toml")
	press(f.s, "d")
	v = view(f.s)
	if !strings.Contains(v, "+[servers.y]") || strings.Contains(v, ghpToken) {
		t.Errorf("untracked diff:\n%s", v)
	}
}

func TestUntrackedDiffRefusedThroughSymlink(t *testing.T) {
	home := newRepo(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	write(t, filepath.Dir(outside), "secret.txt", "OUTSIDE CONTENT\n")
	if err := os.MkdirAll(filepath.Join(home, "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, "skills", "link.md")); err != nil {
		t.Fatal(err)
	}
	f := open(t, home)
	f.s.list.Cursor = rowOf(f.s, "skills/link.md")
	press(f.s, "d")
	if v := view(f.s); strings.Contains(v, "OUTSIDE CONTENT") || !strings.Contains(v, "symlink is not followed") {
		t.Errorf("symlink followed:\n%s", v)
	}
}

func TestUnusualNameShownQuotedNotSelectable(t *testing.T) {
	home := newRepo(t)
	odd := "skills/a\nb.md"
	write(t, home, odd, "x\n")
	f := open(t, home)
	if rowOf(f.s, odd) < 0 {
		t.Fatalf("not listed: %q", paths(f.s))
	}
	selectRow(t, f.s, odd)
	if f.s.sel[odd] {
		t.Error("unusual name selected")
	}
	v := view(f.s)
	if !strings.Contains(v, `"skills/a\nb.md" (unusual name)`) || !strings.Contains(v, "not selectable") {
		t.Errorf("view:\n%s", v)
	}
	press(f.s, "a")
	if f.s.sel[odd] {
		t.Error("a selected the unusual name")
	}
}

func TestChezmoiNoticeAndNoGitKeys(t *testing.T) {
	gittest.Need(t)
	home := filepath.Join(t.TempDir(), "home")
	write(t, home, "agentsync.toml", "# x\n")
	cz := runnertest.New().On("source-path", runner.Result{Stdout: home + "\n"}, nil)
	git := &recGit{inner: gittest.NewGit(t)}
	s := New(deps(home, runnertest.New(), git, cz))
	s.Update(tea.WindowSizeMsg{Width: 160, Height: 60})
	drive(s, s.Init())
	v := view(s)
	for _, want := range []string{"chezmoi re-add", "chezmoi git -- status", "chezmoi git -- add -- <path>",
		"chezmoi git -- commit -m <msg>", "chezmoi git -- push", "chezmoi update"} {
		if !strings.Contains(v, want) {
			t.Errorf("notice lacks %q:\n%s", want, v)
		}
	}
	before := len(git.all())
	for _, k := range []string{"space", "a", "c", "d", "enter", "f", "p", "P", "i"} {
		if out := press(s, k); len(out) != 0 {
			t.Errorf("%s emitted %v", k, out)
		}
		if !strings.Contains(view(s), "managed by chezmoi") {
			t.Errorf("%s: no 'managed by chezmoi'", k)
		}
	}
	if s.mode != modeList || len(git.all()) != before {
		t.Errorf("git keys ran: mode %v calls %v", s.mode, git.all()[before:])
	}
}

func TestNotARepoShowsCommandsOnly(t *testing.T) {
	gittest.Need(t)
	home := filepath.Join(t.TempDir(), "home")
	write(t, home, "agentsync.toml", "# x\n")
	git := &recGit{inner: gittest.NewGit(t)}
	s := New(deps(home, runnertest.New(), git, nil))
	s.Update(tea.WindowSizeMsg{Width: 160, Height: 60})
	drive(s, s.Init())
	if s.repo.Manager != gitrepo.None {
		t.Skipf("the temp dir is inside a git repository (manager %v)", s.repo.Manager)
	}
	v := view(s)
	for _, want := range []string{"not a git repository", "git -C " + home + " init", "git -C " + home + " remote add origin <url>"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	for _, k := range []string{"space", "a", "c", "d"} {
		if out := press(s, k); len(out) != 0 {
			t.Errorf("%s emitted %v", k, out)
		}
	}
	for _, c := range git.all() {
		if cmd := gitCmd(c); cmd == "init" || cmd == "remote" {
			t.Errorf("ran %v", c)
		}
	}
}

func TestUnknownManagerDisablesKeys(t *testing.T) {
	gittest.Need(t)
	home := filepath.Join(t.TempDir(), "home")
	write(t, home, "agentsync.toml", "# x\n")
	git := runnertest.New().Fail(fmt.Errorf("boom"))
	s := New(deps(home, runnertest.New(), git, nil))
	s.Update(tea.WindowSizeMsg{Width: 160, Height: 60})
	drive(s, s.Init())
	if s.repo.Manager != gitrepo.Unknown {
		t.Fatalf("manager %v", s.repo.Manager)
	}
	v := view(s)
	if !strings.Contains(v, "could not inspect the repository") || !strings.Contains(v, gitrepo.RefusalUnknown) {
		t.Errorf("view:\n%s", v)
	}
	n := len(git.Calls())
	for _, k := range []string{"space", "a", "c", "d", "enter", "f", "p", "P", "i"} {
		if out := press(s, k); len(out) != 0 || s.mode != modeList {
			t.Errorf("%s: emitted %v mode %v", k, out, s.mode)
		}
	}
	if len(git.Calls()) != n {
		t.Errorf("keys ran git: %v", git.Calls()[n:])
	}
	press(s, "r")
	if len(git.Calls()) == n {
		t.Error("r must re-detect")
	}
}

func TestScopeSwitchRedetects(t *testing.T) {
	user := newRepo(t)
	write(t, user, tomlX, baseTOML+"args = []\n")
	proj := filepath.Join(t.TempDir(), "proj")
	gittest.Init(t, proj)
	gittest.Commit(t, proj, "init", map[string]string{"README.md": "r\n", ".agentsync/agentsync.toml": "# p\n"})
	write(t, proj, ".agentsync/skills/p/SKILL.md", "# p\n")
	f := open(t, user)
	if f.s.repo.Pathspec != "." || rowOf(f.s, tomlX) < 0 {
		t.Fatalf("user scope: %+v %v", f.s.repo, paths(f.s))
	}
	agent := runnertest.New()
	f.s.SetScope(agent, runner.ScopeProject, filepath.Join(proj, ".agentsync"))
	if f.s.loaded || len(f.s.status.Files) != 0 {
		t.Error("SetScope must drop what was loaded")
	}
	drive(f.s, f.s.Refresh())
	if f.s.repo.Pathspec != ".agentsync" || f.s.repo.Layout != gitrepo.BelowRoot {
		t.Fatalf("project scope: %+v", f.s.repo)
	}
	if got := paths(f.s); !slices.Equal(got, []string{".agentsync/skills/p/SKILL.md"}) {
		t.Errorf("project files %v", got)
	}
	if v := view(f.s); !strings.Contains(v, "home: .agentsync") || !strings.Contains(v, "repo: "+resolved(t, proj)) {
		t.Errorf("view:\n%s", v)
	}
}

func TestStaleResultIgnored(t *testing.T) {
	home := newRepo(t)
	write(t, home, tomlX, baseTOML+"args = []\n")
	gittest.Need(t)
	s := New(deps(home, runnertest.New(), &recGit{inner: gittest.NewGit(t)}, nil))
	first := s.Refresh()
	second := s.Refresh()
	old := first()
	s.Update(old)
	if s.loaded {
		t.Fatal("a superseded round's result was applied")
	}
	s.Update(second())
	if !s.loaded || rowOf(s, tomlX) < 0 {
		t.Fatal("the current round was not applied")
	}
	// A stale diff is dropped too.
	s.list.Cursor = rowOf(s, tomlX)
	_, d1 := s.Update(key("d"))
	press(s, "esc")
	_, d2 := s.Update(key("d"))
	s.Update(d1())
	if s.diff.loaded {
		t.Error("stale diff applied")
	}
	s.Update(d2())
	if !s.diff.loaded {
		t.Error("current diff not applied")
	}
	// A foreign message changes nothing.
	s.Update(ui.RunMsg{})
	s.Update(tea.WindowSizeMsg{Width: 10, Height: 5})
}

func TestStateFileWarningRow(t *testing.T) {
	home := newRepo(t)
	write(t, home, "skills/.state/b.json", "{}\n")
	f := open(t, home)
	if v := view(f.s); !strings.Contains(v, ".state/ is not ignored: add /.state/ to .gitignore") {
		t.Errorf("view:\n%s", v)
	}
}
