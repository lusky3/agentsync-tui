package sourcerepo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/gitrepo/gittest"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/runner/runnertest"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

const commitMsg = "chore: update config"

func logCount(t *testing.T, dir string) int {
	t.Helper()
	return strings.Count(gittest.Git(t, dir, "log", "--format=%H"), "\n")
}

// commit presses c, types the message and enter, and returns what the
// screen emitted.
func commit(s *Screen, msg string) []tea.Msg {
	press(s, "c")
	typeText(s, msg)
	return press(s, "enter")
}

func onlyDo(t *testing.T, out []tea.Msg) ui.DoMsg {
	t.Helper()
	if len(out) != 1 {
		t.Fatalf("emitted %d messages, want one DoMsg: %v", len(out), out)
	}
	d, ok := out[0].(ui.DoMsg)
	if !ok {
		t.Fatalf("emitted %T", out[0])
	}
	return d
}

func mustAct(a actions.Action, err error) actions.Action {
	if err != nil {
		panic(err)
	}
	return a
}

// wantChain asserts the DoMsg is GitAdd(add) then GitCommit(commit).
func wantChain(t *testing.T, s *Screen, d ui.DoMsg, add, commit []string) {
	t.Helper()
	r := actions.GitRepo{Top: s.repo.Top}
	wa := mustAct(actions.GitAdd(r, add))
	wc := mustAct(actions.GitCommit(r, commitMsg, commit))
	if !slices.Equal(d.Action.Argv, wa.Argv) {
		t.Errorf("add argv\n got %q\nwant %q", d.Action.Argv, wa.Argv)
	}
	if len(d.Then) != 1 || !slices.Equal(d.Then[0].Argv, wc.Argv) {
		t.Fatalf("commit step %+v, want %q", d.Then, wc.Argv)
	}
	if d.Scope != runner.ScopeUser {
		t.Errorf("scope %q", d.Scope)
	}
}

// argvPaths is the paths after "--".
func argvPaths(argv []string) []string {
	i := slices.Index(argv, "--")
	if i < 0 {
		return nil
	}
	return argv[i+1:]
}

func TestCommitEmitsChainWithExplicitPaths(t *testing.T) {
	home := newRepo(t)
	write(t, home, tomlX, baseTOML+"args = []\n")
	write(t, home, "-rf", "not a flag\n")
	f := open(t, home)
	selectRow(t, f.s, "-rf")
	d := onlyDo(t, commit(f.s, commitMsg))
	want := []string{tomlX, "-rf"} // status order: tracked changes, then untracked
	wantChain(t, f.s, d, want, want)
	for _, argv := range [][]string{d.Action.Argv, d.Then[0].Argv} {
		dash, rf := slices.Index(argv, "--"), slices.Index(argv, "-rf")
		if dash < 0 || rf < dash {
			t.Errorf("-rf before --: %q", argv)
		}
	}
	if d.Then[0].TypedConfirm {
		t.Error("a clean commit needs no typed confirmation")
	}
	if f.s.CapturesKeys() {
		t.Error("the screen still captures keys after emitting")
	}
}

func TestCommitIncludesInHomeRenameBothSides(t *testing.T) {
	home := newRepo(t)
	gittest.Commit(t, home, "skill", map[string]string{"skills/old/SKILL.md": "# s\n"})
	gittest.Git(t, home, "mv", "skills/old/SKILL.md", "skills/new.md")
	f := open(t, home)
	d := onlyDo(t, commit(f.s, commitMsg))
	// The old path is gone from the index and the worktree: git add would
	// refuse it, git commit -- <old> records its removal.
	wantChain(t, f.s, d, []string{"skills/new.md"}, []string{"skills/new.md", "skills/old/SKILL.md"})
}

func TestCommitMessageRefusalShownInline(t *testing.T) {
	home := newRepo(t)
	write(t, home, tomlX, baseTOML+"args = []\n")
	f := open(t, home)
	out := commit(f.s, "add "+ghpToken)
	if len(out) != 0 {
		t.Fatalf("emitted %v", out)
	}
	v := view(f.s)
	if !strings.Contains(v, "reword the message") || !strings.Contains(v, "credential") || strings.Contains(v, ghpToken) {
		t.Errorf("view:\n%s", v)
	}
	if !f.s.CapturesKeys() {
		t.Error("the field lost focus")
	}
	press(f.s, "esc")
	if f.s.CapturesKeys() || f.s.mode != modeList {
		t.Error("esc did not leave the field")
	}
}

func TestCommitRefusedWhenCheckFails(t *testing.T) {
	home := newRepo(t)
	write(t, home, tomlX, baseTOML+"args = []\n")
	f := open(t, home)
	f.agent.On("check", runner.Result{ExitCode: 1, Stderr: "mcp/x.toml: bad server\n"}, nil)
	if out := commit(f.s, commitMsg); len(out) != 0 {
		t.Fatalf("emitted %v", out)
	}
	if v := view(f.s); !strings.Contains(v, "agentsync check exited 1") {
		t.Errorf("view:\n%s", v)
	}
	if got := f.agent.Calls(); !slices.Contains(got, "check") {
		t.Errorf("agentsync calls %v", got)
	}
}

func TestCommitRefusedOnHardFinding(t *testing.T) {
	home := newRepo(t)
	// What `mcp add --env API_KEY=ghp_…` would have written, by hand.
	write(t, home, tomlX, baseTOML+"env = { API_KEY = \""+ghpToken+"\" }\n")
	f := open(t, home)
	before := logCount(t, home)
	if out := commit(f.s, commitMsg); len(out) != 0 {
		t.Fatalf("emitted %v", out)
	}
	if logCount(t, home) != before {
		t.Error("a commit was made")
	}
	v := view(f.s)
	for _, want := range []string{"refused", "mcp/x.toml:3", "git -C " + shellQuote(f.s.repo.Top) + " commit"} {
		if !strings.Contains(v, want) {
			t.Errorf("view lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, ghpToken) {
		t.Error("the token is shown")
	}
}

func TestSoftFindingNeedsTypedCommit(t *testing.T) {
	home := newRepo(t)
	write(t, home, skillA, "# a\n\nThe id is "+opaqueWord+".\n")
	f := open(t, home)
	d := onlyDo(t, commit(f.s, commitMsg))
	wantChain(t, f.s, d, []string{skillA}, []string{skillA})
	c := d.Then[0]
	if !c.TypedConfirm || !strings.Contains(c.Warning, "1 possible secret listed on the Source repo tab; type commit to proceed") {
		t.Errorf("commit step: typed=%v warning=%q", c.TypedConfirm, c.Warning)
	}
	if c.ConfirmVerb() != "commit" {
		t.Errorf("verb %q", c.ConfirmVerb())
	}
	if v := view(f.s); !strings.Contains(v, skillA+":3") || strings.Contains(v, opaqueWord) {
		t.Errorf("findings not listed masked:\n%s", v)
	}
}

func TestTrackedBinaryChangeNeedsTypedCommit(t *testing.T) {
	home := newRepo(t)
	gittest.Commit(t, home, "bin", map[string]string{"skills/b/data.bin": "a\x00b\n"})
	write(t, home, "skills/b/data.bin", "c\x00d\n")
	f := open(t, home)
	d := onlyDo(t, commit(f.s, commitMsg))
	if !d.Then[0].TypedConfirm {
		t.Error("an unscanned binary change was not soft")
	}
	if v := view(f.s); !strings.Contains(v, "not scanned: binary") {
		t.Errorf("view:\n%s", v)
	}
}

func TestTruncatedPatchNeedsTypedCommit(t *testing.T) {
	home := newRepo(t)
	write(t, home, tomlX, baseTOML+"args = []\n")
	f := open(t, home)
	f.git.override = func(args []string) (runner.Result, bool) {
		if gitCmd(args) != "diff" {
			return runner.Result{}, false
		}
		return runner.Result{Stdout: "diff --git a/mcp/x.toml b/mcp/x.toml\n@@ -2,0 +3 @@\n+args = []\n" + runner.TruncatedMark}, true
	}
	d := onlyDo(t, commit(f.s, commitMsg))
	if !d.Then[0].TypedConfirm {
		t.Error("a truncated patch was not soft")
	}
	if v := view(f.s); !strings.Contains(v, "not scanned: patch over 4 MiB") {
		t.Errorf("view:\n%s", v)
	}
}

func TestCommitRefusedWhenIndexLockPresent(t *testing.T) {
	home := newRepo(t)
	write(t, home, tomlX, baseTOML+"args = []\n")
	f := open(t, home)
	lock := filepath.Join(home, ".git", "index.lock")
	write(t, filepath.Dir(lock), "index.lock", "")
	if out := commit(f.s, commitMsg); len(out) != 0 {
		t.Fatalf("emitted %v", out)
	}
	if v := view(f.s); !strings.Contains(v, "index.lock") {
		t.Errorf("view:\n%s", v)
	}
	if _, err := os.Lstat(lock); err != nil {
		t.Errorf("the lock is gone: %v", err)
	}
}

func TestCommitRefusedDuringMerge(t *testing.T) {
	home := newRepo(t)
	write(t, home, tomlX, baseTOML+"args = []\n")
	f := open(t, home)
	head := strings.TrimSpace(gittest.Git(t, home, "rev-parse", "HEAD"))
	write(t, filepath.Join(home, ".git"), "MERGE_HEAD", head+"\n")
	if out := commit(f.s, commitMsg); len(out) != 0 {
		t.Fatalf("emitted %v", out)
	}
	if v := view(f.s); !strings.Contains(v, "a merge is in progress") {
		t.Errorf("view:\n%s", v)
	}
}

func TestCommitRefusedDetached(t *testing.T) {
	home := newRepo(t)
	gittest.Git(t, home, "checkout", "-q", "--detach")
	write(t, home, tomlX, baseTOML+"args = []\n")
	f := open(t, home)
	if out := commit(f.s, commitMsg); len(out) != 0 {
		t.Fatalf("emitted %v", out)
	}
	if v := view(f.s); !strings.Contains(v, "HEAD is detached") {
		t.Errorf("view:\n%s", v)
	}
}

func TestCommitRefusedForBackupRepo(t *testing.T) {
	home := newRepo(t)
	gittest.Git(t, home, "config", "agentsync.managed", "true")
	write(t, home, tomlX, baseTOML+"args = []\n")
	f := open(t, home)
	for _, k := range []string{"space", "a", "c"} {
		if out := press(f.s, k); len(out) != 0 {
			t.Fatalf("%s emitted %v", k, out)
		}
	}
	if f.s.mode == modeCommit || f.s.CapturesKeys() {
		t.Error("c opened the message field")
	}
	if v := view(f.s); !strings.Contains(v, "local-only destination history") {
		t.Errorf("view:\n%s", v)
	}
}

func TestStateFileNeverInArgv(t *testing.T) {
	home := newRepo(t)
	gittest.Commit(t, home, "state", map[string]string{"skills/.state/b.json": "{}\n", skillA: "# a\n"})
	write(t, home, "skills/.state/b.json", "{\"k\":1}\n")
	write(t, home, skillA, "# a2\n")
	f := open(t, home)
	if rowOf(f.s, "skills/.state/b.json") < 0 {
		t.Fatalf("state file not listed: %v", paths(f.s))
	}
	selectRow(t, f.s, "skills/.state/b.json")
	if f.s.sel["skills/.state/b.json"] {
		t.Error("space selected the state file")
	}
	press(f.s, "a")
	if len(f.s.selectedPaths()) == 0 {
		press(f.s, "a")
	}
	d := onlyDo(t, commit(f.s, commitMsg))
	wantChain(t, f.s, d, []string{skillA}, []string{skillA})
	for _, step := range append([]actions.Action{d.Action}, d.Then...) {
		for _, a := range step.Argv {
			if strings.Contains(a, ".state") {
				t.Errorf("argv holds %q", a)
			}
		}
	}
}

func TestSelectionIsFilesNeverDirectories(t *testing.T) {
	home := newRepo(t)
	gittest.Commit(t, home, "state", map[string]string{"skills/x/.state/k": "1\n", "skills/x/SKILL.md": "# x\n"})
	write(t, home, "skills/x/.state/k", "2\n")
	write(t, home, "skills/x/SKILL.md", "# x2\n")
	write(t, home, "skills/y/SKILL.md", "# y\n")
	f := open(t, home)
	press(f.s, "a")
	if len(f.s.selectedPaths()) == 0 {
		press(f.s, "a")
	}
	d := onlyDo(t, commit(f.s, commitMsg))
	records := map[string]bool{}
	for _, fl := range f.s.status.Files {
		records[fl.Path] = true
	}
	for _, step := range append([]actions.Action{d.Action}, d.Then...) {
		for _, p := range argvPaths(step.Argv) {
			fi, err := os.Lstat(filepath.Join(home, p))
			if !records[p] || (err == nil && fi.IsDir()) || strings.HasSuffix(p, "/") {
				t.Errorf("argv path %q is not a file record", p)
			}
		}
	}
}

func TestChangedSinceListedRefreshes(t *testing.T) {
	home := newRepo(t)
	write(t, home, tomlX, baseTOML+"args = []\n")
	write(t, home, skillA, "# a\n")
	f := open(t, home)
	if err := os.Remove(filepath.Join(home, filepath.FromSlash(skillA))); err != nil {
		t.Fatal(err)
	}
	if out := commit(f.s, commitMsg); len(out) != 0 {
		t.Fatalf("emitted %v", out)
	}
	if v := view(f.s); !strings.Contains(v, "changed since listed: refreshed") {
		t.Errorf("view:\n%s", v)
	}
	if rowOf(f.s, skillA) >= 0 || f.s.mode != modeList {
		t.Errorf("not refreshed: %v mode %v", paths(f.s), f.s.mode)
	}
}

func TestHomeBelowRootPathspecOnly(t *testing.T) {
	gittest.Need(t)
	top := filepath.Join(t.TempDir(), "dots")
	gittest.Init(t, top)
	gittest.Commit(t, top, "init", map[string]string{"README.md": "r\n", "agentsync/agentsync.toml": "# a\n"})
	write(t, top, "README.md", "dirty\n")
	write(t, top, "agentsync/mcp/x.toml", baseTOML)
	f := open(t, filepath.Join(top, "agentsync"))
	if got := paths(f.s); !slices.Equal(got, []string{"agentsync/mcp/x.toml"}) {
		t.Fatalf("listed %v", got)
	}
	if v := view(f.s); strings.Contains(v, "README") {
		t.Errorf("outside file shown:\n%s", v)
	}
	press(f.s, "a")
	if len(f.s.selectedPaths()) == 0 {
		press(f.s, "a")
	}
	d := onlyDo(t, commit(f.s, commitMsg))
	wantChain(t, f.s, d, []string{"agentsync/mcp/x.toml"}, []string{"agentsync/mcp/x.toml"})
}

// --- through the real shell ---

// probe wraps the shell and reports every message it processed.
type probe struct {
	*ui.App
	seen chan tea.Msg
}

func (p probe) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := p.App.Update(msg)
	select {
	case p.seen <- msg:
	default:
	}
	return p, cmd
}

type shell struct {
	t  *testing.T
	tm *teatest.TestModel
	p  probe
	s  *Screen
}

func startShell(t *testing.T, home, logPath string) *shell {
	t.Helper()
	d := deps(home, runnertest.New(), gittest.NewGit(t), nil)
	d.LogPath = logPath
	return newShell(t, d, New(d))
}

// newShell runs the shell over d with s as its only screen, once s loaded.
func newShell(t *testing.T, d ui.Deps, s *Screen) *shell {
	t.Helper()
	sh := &shell{t: t, s: s, p: probe{App: ui.NewApp(d, s), seen: make(chan tea.Msg, 4096)}}
	sh.tm = teatest.NewTestModel(t, sh.p, teatest.WithInitialTermSize(160, 60))
	sh.wait("load", func(m tea.Msg) bool { _, ok := m.(loadMsg); return ok })
	return sh
}

func (sh *shell) wait(what string, match func(tea.Msg) bool) tea.Msg {
	sh.t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case m := <-sh.p.seen:
			if match(m) {
				return m
			}
		case <-deadline:
			sh.t.Fatalf("timed out waiting for %s", what)
			return nil
		}
	}
}

func (sh *shell) keys(ks ...string) {
	for _, k := range ks {
		sh.tm.Send(key(k))
	}
}

// commitThrough commits the default selection through the modal and
// returns the commit step's RunMsg.
func (sh *shell) commitThrough() ui.RunMsg {
	sh.t.Helper()
	sh.keys("c")
	sh.tm.Type(commitMsg)
	sh.keys("enter")
	sh.wait("guard", func(m tea.Msg) bool { _, ok := m.(guardMsg); return ok })
	sh.wait("DoMsg", func(m tea.Msg) bool { _, ok := m.(ui.DoMsg); return ok })
	sh.keys("y")
	m := sh.wait("commit RunMsg", func(m tea.Msg) bool {
		r, ok := m.(ui.RunMsg)
		return ok && gitCmd(r.Action.Argv) == "commit"
	})
	return m.(ui.RunMsg)
}

func (sh *shell) finish() {
	sh.t.Helper()
	_ = sh.tm.Quit()
	sh.tm.FinalModel(sh.t, teatest.WithFinalTimeout(20*time.Second))
}

func TestActivityLogHasNoDiffContent(t *testing.T) {
	home := newRepo(t)
	const content = "CONTENT_LINE_never_logged"
	write(t, home, tomlX, baseTOML+"# "+content+"\n")
	logPath := filepath.Join(t.TempDir(), "activity.log")
	sh := startShell(t, home, logPath)
	if r := sh.commitThrough(); r.Err != nil || r.Result.ExitCode != 0 {
		t.Fatalf("commit failed: %+v", r)
	}
	sh.finish()
	if !strings.Contains(sh.s.note, "committed: "+commitMsg) {
		t.Errorf("note %q", sh.s.note)
	}
	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	log := string(b)
	if !strings.Contains(log, "commit '--message="+commitMsg+"' -- mcp/x.toml") {
		t.Errorf("log lacks the masked commit line:\n%s", log)
	}
	if strings.Contains(log, content) || strings.Contains(log, "[servers.x]") {
		t.Errorf("log holds file content:\n%s", log)
	}
	if logCount(t, home) != 2 {
		t.Error("nothing was committed")
	}
}

func TestCommitAfterAddFailureShowsStagedNote(t *testing.T) {
	home := newRepo(t)
	write(t, home, tomlX, baseTOML+"args = []\n")
	write(t, filepath.Join(home, ".git", "hooks"), "pre-commit", "#!/bin/sh\nexit 1\n")
	if err := os.Chmod(filepath.Join(home, ".git", "hooks", "pre-commit"), 0o755); err != nil {
		t.Fatal(err)
	}
	sh := startShell(t, home, "")
	r := sh.commitThrough()
	if r.Result.ExitCode == 0 {
		t.Fatal("the hook did not reject the commit")
	}
	sh.finish()
	note := sh.s.note
	for _, want := range []string{"staged", "git -C " + shellQuote(sh.s.repo.Top) + " --literal-pathspecs restore --staged -- mcp/x.toml"} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q: %q", want, note)
		}
	}
	if staged := gittest.Git(t, home, "diff", "--cached", "--name-only"); staged != tomlX+"\n" {
		t.Errorf("staged %q", staged)
	}
}

func TestCommitOutcomeMatchedOnCommitKey(t *testing.T) {
	home := newRepo(t)
	write(t, home, tomlX, baseTOML+"args = []\n")
	f := open(t, home)
	d := onlyDo(t, commit(f.s, commitMsg))
	f.s.Update(ui.RunMsg{Action: d.Action})
	if strings.Contains(f.s.note, "committed") {
		t.Fatalf("the add's RunMsg was taken for the commit: %q", f.s.note)
	}
	f.s.Update(ui.RunMsg{Action: d.Then[0], Result: runner.Result{ExitCode: 1, Stderr: "hook said " + ghpToken}})
	if !strings.Contains(f.s.note, "restore --staged") || strings.Contains(f.s.note, "ghp_") {
		t.Errorf("note %q", f.s.note)
	}
	// An add that failed stops the chain: nothing is staged.
	d = onlyDo(t, commit(f.s, commitMsg))
	f.s.Update(ui.RunMsg{Action: d.Action, Result: runner.Result{ExitCode: 128}})
	if strings.Contains(f.s.note, "restore") || !strings.Contains(f.s.note, "nothing was committed") {
		t.Errorf("note after add failure %q", f.s.note)
	}
}

func TestRecreatedSensitiveRenameSourceNeverCommitted(t *testing.T) {
	home := newRepo(t)
	gittest.Commit(t, home, "env", map[string]string{"skills/x/.env": "TOKEN=${TOKEN}\n"})
	gittest.Git(t, home, "mv", "skills/x/.env", "skills/x/env.example")
	write(t, home, "skills/x/.env", "TOKEN="+ghpToken+"\n")
	f := open(t, home)
	if i := rowOf(f.s, "skills/x/.env"); i < 0 || f.s.status.Files[i].Selectable {
		t.Fatalf("recreated .env must be listed and not selectable: %v", paths(f.s))
	}
	if v := view(f.s); !strings.Contains(v, "old path exists again; its removal is not part of this commit") {
		t.Errorf("row note missing:\n%s", v)
	}
	d := onlyDo(t, commit(f.s, commitMsg))
	wantChain(t, f.s, d, []string{"skills/x/env.example"}, []string{"skills/x/env.example"})

	// Through the shell and real git: HEAD never holds the recreated file.
	sh := startShell(t, home, "")
	if r := sh.commitThrough(); r.Err != nil || r.Result.ExitCode != 0 {
		t.Fatalf("commit failed: %+v", r)
	}
	sh.finish()
	tree := gittest.Git(t, home, "ls-tree", "-r", "--name-only", "HEAD")
	if !strings.Contains(tree, "skills/x/env.example") {
		t.Errorf("the rename's new side was not committed:\n%s", tree)
	}
	// The old path's staged removal stays staged: HEAD keeps the old
	// content, never the recreated file.
	if got := gittest.Git(t, home, "show", "HEAD:skills/x/.env"); got != "TOKEN=${TOKEN}\n" {
		t.Errorf("HEAD:skills/x/.env = %q", got)
	}
	if out := gittest.Git(t, home, "log", "-p", "-1"); strings.Contains(out, "ghp_") {
		t.Error("HEAD holds the secret")
	}
}

func TestRecreatedStateRenameSourceNeverInArgv(t *testing.T) {
	home := newRepo(t)
	gittest.Commit(t, home, "state", map[string]string{"skills/.state/a.json": "{}\n"})
	gittest.Git(t, home, "mv", "skills/.state/a.json", "skills/a.json")
	write(t, home, "skills/.state/a.json", "{\"k\":1}\n")
	f := open(t, home)
	selectRow(t, f.s, "skills/a.json")
	if !f.s.sel["skills/a.json"] {
		selectRow(t, f.s, "skills/a.json")
	}
	d := onlyDo(t, commit(f.s, commitMsg))
	wantChain(t, f.s, d, []string{"skills/a.json"}, []string{"skills/a.json"})
}

func TestRenameSourceOfSensitiveNameNotCommitted(t *testing.T) {
	home := newRepo(t)
	gittest.Commit(t, home, "pem", map[string]string{"skills/k.pem": "x\n"})
	gittest.Git(t, home, "mv", "skills/k.pem", "skills/k.txt")
	f := open(t, home)
	selectRow(t, f.s, "skills/k.txt")
	if !f.s.sel["skills/k.txt"] {
		selectRow(t, f.s, "skills/k.txt")
	}
	d := onlyDo(t, commit(f.s, commitMsg))
	for _, p := range argvPaths(d.Then[0].Argv) {
		if p == "skills/k.pem" {
			t.Errorf("a sensitive-named rename source reached argv: %q", d.Then[0].Argv)
		}
	}
}

func TestChangedKindSinceListedRefreshes(t *testing.T) {
	home := newRepo(t)
	write(t, home, tomlX, baseTOML+"args = []\n")
	f := open(t, home)
	// Still listed, but now a deletion: what would be committed changed.
	if err := os.Remove(filepath.Join(home, filepath.FromSlash(tomlX))); err != nil {
		t.Fatal(err)
	}
	if out := commit(f.s, commitMsg); len(out) != 0 {
		t.Fatalf("emitted %v", out)
	}
	if !strings.Contains(view(f.s), "changed since listed: refreshed") || f.s.mode != modeList {
		t.Errorf("view:\n%s", view(f.s))
	}
	if i := rowOf(f.s, tomlX); i < 0 || f.s.status.Files[i].Kind.String() != "deleted" {
		t.Errorf("not refreshed: %+v", f.s.status.Files)
	}
}

func TestFindingWordingSeparatesKinds(t *testing.T) {
	home := newRepo(t)
	gittest.Commit(t, home, "bin", map[string]string{"skills/b/data.bin": "a\x00b\n"})
	write(t, home, "skills/b/data.bin", "c\x00d\n")
	f := open(t, home)
	d := onlyDo(t, commit(f.s, commitMsg))
	w := d.Then[0].Warning
	if !strings.Contains(w, "1 file not scanned") || strings.Contains(w, "possible secret") {
		t.Errorf("binary-only warning %q", w)
	}
	write(t, home, skillA, "id "+opaqueWord+"\n")
	drive(f.s, f.s.Refresh())
	press(f.s, "a")
	if len(f.s.selectedPaths()) < 2 {
		press(f.s, "a")
	}
	d = onlyDo(t, commit(f.s, ""))
	if w := d.Then[0].Warning; !strings.Contains(w, "1 possible secret and 1 file not scanned") {
		t.Errorf("mixed warning %q", w)
	}
}

func TestHardRefusalNamesHardFindings(t *testing.T) {
	home := newRepo(t)
	write(t, home, tomlX, baseTOML+"env = { API_KEY = \""+ghpToken+"\" }\n")
	write(t, home, skillA, "id "+opaqueWord+"\n")
	f := open(t, home)
	if out := commit(f.s, commitMsg); len(out) != 0 {
		t.Fatalf("emitted %v", out)
	}
	v := view(f.s)
	if !strings.Contains(v, "refused: 1 hard finding") || strings.Contains(v, "refused: 2") {
		t.Errorf("view:\n%s", v)
	}
}

func TestStaleGuardIgnoredAfterEscAndScopeSwitch(t *testing.T) {
	home := newRepo(t)
	write(t, home, tomlX, baseTOML+"args = []\n")
	f := open(t, home)
	press(f.s, "c")
	typeText(f.s, commitMsg)
	_, guard := f.s.Update(key("enter"))
	press(f.s, "esc")
	if _, next := f.s.Update(guard()); next != nil || f.s.pending != nil || f.s.commit.findings != nil {
		t.Error("a guard result after esc was applied")
	}
	press(f.s, "c")
	_, guard = f.s.Update(key("enter"))
	f.s.SetScope(f.agent, runner.ScopeProject, t.TempDir())
	if _, next := f.s.Update(guard()); next != nil || f.s.pending != nil {
		t.Error("a guard result after a scope switch was applied")
	}
	load := f.s.Refresh()
	f.s.SetScope(f.agent, runner.ScopeUser, home)
	f.s.Update(load())
	if f.s.loaded {
		t.Error("an old-scope load was applied")
	}
}

func TestRenameSourceNowDirectoryNoteShown(t *testing.T) {
	home := newRepo(t)
	gittest.Commit(t, home, "skill", map[string]string{"skills/old.md": "# s\n"})
	gittest.Git(t, home, "mv", "skills/old.md", "skills/new.md")
	write(t, home, "skills/old.md/x.md", "# x\n")
	f := open(t, home)
	wantView(t, f.s, "old path is now a directory; its removal stays staged",
		"resolve outside the TUI", "git -C "+shellQuote(f.s.repo.Top)+" status")
	if strings.Contains(view(f.s), "commit -- skills/old.md") {
		t.Errorf("suggests committing a path that now matches a directory:\n%s", view(f.s))
	}
	selectRow(t, f.s, "skills/old.md/x.md")
	if f.s.sel["skills/old.md/x.md"] {
		selectRow(t, f.s, "skills/old.md/x.md")
	}
	d := onlyDo(t, commit(f.s, commitMsg))
	wantChain(t, f.s, d, []string{"skills/new.md"}, []string{"skills/new.md"})
}

func TestRenameSourceSensitiveNameNoteShown(t *testing.T) {
	home := newRepo(t)
	gittest.Commit(t, home, "pem", map[string]string{"skills/k.pem": "x\n"})
	gittest.Git(t, home, "mv", "skills/k.pem", "skills/k.txt")
	f := open(t, home)
	wantView(t, f.s, "old path is a sensitive path; its removal stays staged",
		"git -C "+shellQuote(f.s.repo.Top)+" --literal-pathspecs commit -- skills/k.pem")
}

// A home laid out like the maintainer's real one (synthetic: no real file
// is read): the agentsync-owned files are selected by default, the other
// tracked-worthy files (kiro-agents-src/, steering/, setup.sh) are listed
// unselected, ignored .state/ is not listed, and `${VAR}` / `Bearer ${VAR}`
// references are not findings, so the default commit needs no typed word.
func TestMaintainerLayoutDefaultSelection(t *testing.T) {
	gittest.Need(t)
	home := filepath.Join(t.TempDir(), "agent-config")
	gittest.Init(t, home)
	gittest.Commit(t, home, "init", map[string]string{".gitignore": "/.state/\n", "README.md": "# agent config\n"})
	files := map[string]string{
		"agentsync.toml":                        "[agents]\nenabled = ['claude', 'kiro']\n",
		"mcp/brave.toml":                        "[server]\ntype = 'stdio'\ncommand = 'npx'\nargs = ['-y', '@brave/brave-search-mcp-server']\n\n[server.env]\nBRAVE_API_KEY = '${BRAVE_API_KEY}'\n",
		"mcp/github.toml":                       "[server]\ntype = 'http'\nurl = 'https://api.githubcopilot.com/mcp/'\n\n[server.headers]\nAuthorization = 'Bearer ${GITHUB_TOKEN}'\n",
		"skills/review/SKILL.md":                "---\nname: review\n---\n# Review\nRead the diff first.\n",
		"kiro-agents-src/agents/ka-leader.json": "{\"headers\": {\"Authorization\": \"Bearer $KA_TOKEN\"}}\n",
		"steering/product.md":                   "# Product\n",
		"setup.sh":                              "#!/bin/sh\nagentsync apply\n",
		".state/backup.json":                    "{\"note\": \"ignored\"}\n",
	}
	for p, body := range files {
		write(t, home, p, body)
	}
	write(t, home, "README.md", "# agent config\n\nUpdated.\n")
	f := open(t, home)
	listed := paths(f.s)
	for _, p := range []string{"agentsync.toml", "mcp/brave.toml", "mcp/github.toml", "skills/review/SKILL.md"} {
		if !slices.Contains(listed, p) || !f.s.sel[p] {
			t.Errorf("%s not selected by default (listed %v, selection %v)", p, listed, f.s.sel)
		}
	}
	for _, p := range []string{"kiro-agents-src/agents/ka-leader.json", "steering/product.md", "setup.sh", "README.md"} {
		if !slices.Contains(listed, p) || f.s.sel[p] {
			t.Errorf("%s should be listed unselected (listed %v, selection %v)", p, listed, f.s.sel)
		}
	}
	for _, p := range listed {
		if strings.HasPrefix(p, ".state") {
			t.Errorf("ignored %s listed", p)
		}
	}
	d := onlyDo(t, commit(f.s, commitMsg))
	want := []string{"agentsync.toml", "mcp/brave.toml", "mcp/github.toml", "skills/review/SKILL.md"}
	if got := argvPaths(d.Then[0].Argv); !slices.Equal(got, want) {
		t.Errorf("commit paths %q, want %q", got, want)
	}
	if d.Then[0].TypedConfirm || len(f.s.commit.findings) != 0 {
		t.Errorf("references reported as findings: %+v", f.s.commit.findings)
	}
}

// Every refusal line is sanitised, not only the first: a later line can
// carry a path or git's own words.
func TestCommitRefusalLinesSanitised(t *testing.T) {
	f := open(t, newRepo(t))
	f.s.mode = modeCommit
	f.s.commit.refusal = []string{"refused \x1b]0;a\x07", "  git -C \x1b]0;evil\x07 log\x1b[2J", "\x1b[31mred\r"}
	raw := strings.Join(f.s.commitView(), "\n")
	for _, bad := range []string{"\x07", "\x1b]", "\x1b[2J", "\x1b[31m", "\r"} {
		if strings.Contains(raw, bad) {
			t.Errorf("commit view holds %q: %q", bad, raw)
		}
	}
}

// Greptile's example: only the body of an existing multi-line value under
// a secret-named key changes, so the -U0 diff holds only the body line.
// It is scanned as that key's value: hard in a config file.
func TestCommitRefusedMultiLineSecretBodyEdit(t *testing.T) {
	home := newRepo(t)
	gittest.Commit(t, home, "ref", map[string]string{tomlX: baseTOML + "[servers.x.env]\nAPI_KEY = '''\n${TOKEN}\n'''\n"})
	write(t, home, tomlX, baseTOML+"[servers.x.env]\nAPI_KEY = '''\nhunter2\n'''\n")
	f := open(t, home)
	before := logCount(t, home)
	if out := commit(f.s, commitMsg); len(out) != 0 {
		t.Fatalf("emitted %v", out)
	}
	if logCount(t, home) != before {
		t.Error("a commit was made")
	}
	wantView(t, f.s, "refused", "mcp/x.toml:5")
	if strings.Contains(view(f.s), "hunter2") {
		t.Error("the secret is shown")
	}
}

// Ruling 43: an edit of a multi-line env value's body is hard whatever the
// key; the -U0 patch holds only the body, so the worktree post-image gives
// the context. The diff pane keeps the value masked.
func TestCommitRefusedEnvMultiLineBodyEdit(t *testing.T) {
	home := newRepo(t)
	gittest.Commit(t, home, "ref", map[string]string{tomlX: baseTOML + "[servers.x.env]\nFOO = '''\n${TOKEN}\n'''\n"})
	write(t, home, tomlX, baseTOML+"[servers.x.env]\nFOO = '''\n"+opaqueWord+"\n'''\n")
	f := open(t, home)
	before := logCount(t, home)
	if out := commit(f.s, commitMsg); len(out) != 0 {
		t.Fatalf("emitted %v", out)
	}
	if logCount(t, home) != before {
		t.Error("a commit was made")
	}
	wantView(t, f.s, "refused", "mcp/x.toml:5")
	if strings.Contains(view(f.s), opaqueWord) {
		t.Error("the value is shown")
	}
	f = open(t, home)
	f.s.list.Cursor = rowOf(f.s, tomlX)
	press(f.s, "enter")
	if f.s.mode != modeDiff {
		t.Fatalf("mode %v, want diff", f.s.mode)
	}
	if d := strings.Join(f.s.diff.lines, "\n"); strings.Contains(d, opaqueWord) || !strings.Contains(d, "FOO") {
		t.Errorf("diff pane:\n%s", d)
	}
}

// The same body edit under a key that is not secret-named is no finding.
func TestCommitMultiLineBodyEditUnderPlainKey(t *testing.T) {
	home := newRepo(t)
	gittest.Commit(t, home, "doc", map[string]string{tomlX: baseTOML + "description = '''\nold\n'''\n"})
	write(t, home, tomlX, baseTOML+"description = '''\nhunter2\n'''\n")
	f := open(t, home)
	d := onlyDo(t, commit(f.s, commitMsg))
	if d.Then[0].TypedConfirm {
		t.Errorf("a plain body edit needs a typed commit: %q", d.Then[0].Warning)
	}
}

// An edit of a tracked file over 1 MiB is scanned in full; the multi-line
// context is cut at 1 MiB, so only an added line past the cut is soft.
func TestCommitLargeTrackedFileContextNote(t *testing.T) {
	big := strings.Repeat("plain line\n", (1<<20)/11+1000)
	for _, c := range []struct {
		name, body string
		typed      bool
	}{
		{"edit near the start", "edited\n" + big[len("plain line\n"):], false},
		{"edit past the cut", big + "appended\n", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := newRepo(t)
			gittest.Commit(t, home, "big", map[string]string{"skills/b/big.md": big})
			write(t, home, "skills/b/big.md", c.body)
			f := open(t, home)
			d := onlyDo(t, commit(f.s, commitMsg))
			if d.Then[0].TypedConfirm != c.typed {
				t.Errorf("typed=%v, want %v (warning %q)", d.Then[0].TypedConfirm, c.typed, d.Then[0].Warning)
			}
		})
	}
}

// A commit step that was stopped (timed out or interrupted) may already
// have been recorded: git updates HEAD before a post-commit hook runs. The
// note says so and asks for a check instead of claiming a failure.
func TestStoppedCommitOutcomeIsUncertain(t *testing.T) {
	home := newRepo(t)
	write(t, home, tomlX, baseTOML+"args = []\n")
	f := open(t, home)
	d := onlyDo(t, commit(f.s, commitMsg))
	f.s.Update(ui.RunMsg{Action: d.Action})
	f.s.Update(ui.RunMsg{Action: d.Then[0], Err: errors.New("timed out after 2m0s")})
	note := f.s.note
	for _, want := range []string{"may have been recorded", "git -C " + shellQuote(f.s.repo.Top) + " log -1", "status"} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q: %q", want, note)
		}
	}
	if strings.Contains(note, "commit failed") || strings.Contains(note, "nothing was committed") {
		t.Errorf("note claims a failure: %q", note)
	}
}

// envTableTOML is a config file whose [servers.x.env] header is more than
// three lines above its last pairs, so neither a -U0 nor a -U3 hunk of an
// edit there holds the header.
const envTableTOML = baseTOML + "[servers.x.env]\nA = '1'\nB = '2'\nC = '3'\nD = '4'\n"

// Ruling 48: under an env or headers table every value is an env or header
// value, whatever its key: a visible key's credential or token-shaped value
// is hard, its context read from the post-image.
func TestCommitRefusedVisibleKeyInEnvTable(t *testing.T) {
	for _, c := range []struct{ name, line, secret string }{
		{"credential", "command = '" + ghpToken + "'", ghpToken},
		{"token-shaped", "name = '" + opaqueWord + "'", opaqueWord},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := newRepo(t)
			gittest.Commit(t, home, "env", map[string]string{tomlX: envTableTOML})
			write(t, home, tomlX, envTableTOML+c.line+"\n")
			f := open(t, home)
			before := logCount(t, home)
			if out := commit(f.s, commitMsg); len(out) != 0 {
				t.Fatalf("emitted %v", out)
			}
			if logCount(t, home) != before {
				t.Error("a commit was made")
			}
			wantView(t, f.s, "refused", "mcp/x.toml:8")
			if strings.Contains(view(f.s), c.secret) {
				t.Error("the value is shown")
			}
		})
	}
}

// The same for a new file: it is its own context.
func TestCommitRefusedVisibleKeyInEnvTableNewFile(t *testing.T) {
	home := newRepo(t)
	write(t, home, tomlX, envTableTOML+"name = '"+opaqueWord+"'\n")
	f := open(t, home)
	if out := commit(f.s, commitMsg); len(out) != 0 {
		t.Fatalf("emitted %v", out)
	}
	wantView(t, f.s, "refused", "mcp/x.toml:8")
}

// Outside an env table a visible key's token-shaped value stays soft
// (Rulings 20, 21): a typed commit, not a refusal.
func TestCommitVisibleKeyOutsideEnvTableSoft(t *testing.T) {
	home := newRepo(t)
	body := baseTOML + "[servers.x.env]\nA = '1'\n[servers.y]\ncommand = 'npx'\n"
	gittest.Commit(t, home, "env", map[string]string{tomlX: body})
	write(t, home, tomlX, body+"name = '"+opaqueWord+"'\n")
	f := open(t, home)
	d := onlyDo(t, commit(f.s, commitMsg))
	if !d.Then[0].TypedConfirm {
		t.Errorf("want a typed commit: %q", d.Then[0].Warning)
	}
}

// manyLines is n lines "K<i> = <value>" for a TOML env table.
func manyLines(n int, value string) string {
	var b strings.Builder
	for i := range n {
		b.WriteString("K" + strconv.Itoa(i) + " = \"" + value + "\"\n")
	}
	return b.String()
}

// TestCommitFindingsScrollKeepKeys: on a 24-row terminal, 25 hard findings
// do not push the key guidance off the screen; up/down and pgup/pgdown
// scroll the refusal and findings, and the last finding line is reachable.
func TestCommitFindingsScrollKeepKeys(t *testing.T) {
	home := newRepo(t)
	write(t, home, tomlX, baseTOML+"[env]\n"+manyLines(25, ghpToken))
	f := open(t, home)
	f.s.Update(tea.WindowSizeMsg{Width: wideView, Height: 24})
	if out := commit(f.s, commitMsg); len(out) != 0 {
		t.Fatalf("emitted %v", out)
	}
	lines := strings.Split(view(f.s), "\n")
	if len(lines) > 24 {
		t.Errorf("view is %d lines on a 24-row terminal", len(lines))
	}
	if last := lines[len(lines)-1]; !strings.Contains(strings.Join(lines[max(len(lines)-2, 0):], " "), "enter check and commit") || !strings.Contains(last, "scroll") {
		t.Errorf("key guidance not at the bottom:\n%s", strings.Join(lines, "\n"))
	}
	if v := view(f.s); strings.Contains(v, "5 more") {
		t.Fatalf("the end of the list shows before scrolling:\n%s", v)
	}
	press(f.s, "pgdown", "pgdown", "pgdown")
	if v := view(f.s); !strings.Contains(v, "5 more") || strings.Contains(v, ghpToken) {
		t.Errorf("pgdown did not reach the end of the findings:\n%s", v)
	}
	press(f.s, "pgup", "pgup", "pgup", "up")
	if v := view(f.s); !strings.Contains(v, "refused") {
		t.Errorf("pgup did not return to the refusal:\n%s", v)
	}
	if f.s.commit.field.Value != commitMsg {
		t.Errorf("scroll keys edited the message: %q", f.s.commit.field.Value)
	}
}

// TestListFindingsFitTheScreen: soft findings listed under the files after
// a typed commit is offered are cut to fit, so the keys stay visible.
func TestListFindingsFitTheScreen(t *testing.T) {
	home := newRepo(t)
	var b strings.Builder
	b.WriteString("# a\n")
	for i := range 25 {
		b.WriteString("id " + strconv.Itoa(i) + " is " + opaqueWord + strconv.Itoa(i) + "\n")
	}
	write(t, home, skillA, b.String())
	f := open(t, home)
	f.s.Update(tea.WindowSizeMsg{Width: wideView, Height: 24})
	onlyDo(t, commit(f.s, commitMsg))
	lines := strings.Split(view(f.s), "\n")
	if len(lines) > 24 || !strings.Contains(lines[len(lines)-1], "home/end") {
		t.Errorf("list view (%d lines) lost its keys:\n%s", len(lines), strings.Join(lines, "\n"))
	}
	if v := view(f.s); !strings.Contains(v, "more") {
		t.Errorf("no count of the findings cut:\n%s", v)
	}
}

// A repository path holding a credential shape is masked in every command
// the screen shows for running outside the TUI, as in its heading, with a
// line saying the masked part must be put back first; a plain path is
// shown as is, with no such line.
func TestGuidanceMasksRepositoryPath(t *testing.T) {
	top := "/srv/" + ghpToken + "/home"
	p := &pendingCommit{top: top, add: []string{skillA}, added: true}
	stopped := ui.RunMsg{Err: context.DeadlineExceeded}
	failed := ui.RunMsg{Result: runner.Result{ExitCode: 1}}
	texts := map[string]string{
		"stopped commit": commitOutcome(p, stopped, false),
		"staged commit":  commitOutcome(p, failed, false),
		"no upstream":    strings.Join(noUpstream(top, "main"), "\n"),
		"below root":     strings.Join(belowRoot(top), "\n"),
		"stopped pull":   pullOutcome(&pendingNet{top: top}, stopped, false),
		"push refusal":   strings.Join(pushHardRefusal(1, top, ""), "\n"),
	}
	for name, txt := range texts {
		if strings.Contains(txt, ghpToken) || !strings.Contains(txt, "/srv/") || !strings.Contains(txt, whyMaskedPath) {
			t.Errorf("%s:\n%s", name, txt)
		}
	}
	if txt := commitOutcome(&pendingCommit{top: "/srv/home"}, stopped, false); strings.Contains(txt, whyMaskedPath) ||
		!strings.Contains(txt, "git -C /srv/home log -1") {
		t.Errorf("plain path:\n%s", txt)
	}
}

// git add stores what a clean filter (or a working-tree-encoding) makes
// of the worktree file, not the file itself: a filter that expands a
// reference into a credential would commit it although the file holds
// only the reference. A new file with such an attribute is scanned as git
// will store it (a tracked one already is: git diff applies the filter).
func TestCommitScansCleanFilteredContent(t *testing.T) {
	home := newRepo(t)
	gittest.Commit(t, home, "attrs", map[string]string{".gitattributes": "mcp/*.toml filter=expand\n"})
	gittest.Git(t, home, "config", "filter.expand.clean", "sed s/REF/"+ghpToken+"/")
	write(t, home, "mcp/new.toml", "[x]\nk = 'REF'\n")
	write(t, home, tomlX, baseTOML+"k = 'REF'\n")
	for _, p := range []string{"mcp/new.toml", tomlX} {
		f := open(t, home)
		for _, q := range f.s.selectedPaths() {
			if q != p {
				selectRow(t, f.s, q)
			}
		}
		if got := f.s.selectedPaths(); len(got) != 1 || got[0] != p {
			t.Fatalf("selected %v, want only %s", got, p)
		}
		if out := commit(f.s, commitMsg); len(out) != 0 {
			t.Fatalf("%s: emitted %v", p, out)
		}
		wantView(t, f.s, "refused: 1 hard finding", p)
		if strings.Contains(view(f.s), ghpToken) {
			t.Errorf("%s: token shown", p)
		}
	}
}
