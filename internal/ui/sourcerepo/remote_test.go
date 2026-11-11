package sourcerepo

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/gitrepo"
	"github.com/lusky3/agentsync-tui/internal/gitrepo/gittest"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/runner/runnertest"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

// newTracked makes a home whose main tracks main on a bare origin.
func newTracked(t *testing.T) (home, bare string) {
	t.Helper()
	home = newRepo(t)
	bare = gittest.Bare(t)
	gittest.Git(t, home, "remote", "add", "origin", bare)
	gittest.Git(t, home, "push", "-q", "-u", "origin", "main")
	return home, bare
}

// pushFromOther pushes one commit to bare's main from another clone.
func pushFromOther(t *testing.T, bare string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "other")
	gittest.Git(t, "", "clone", "-q", bare, dir)
	gittest.Commit(t, dir, "other", files)
	gittest.Git(t, dir, "push", "-q", "origin", "main")
}

func head(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(gittest.Git(t, dir, "rev-parse", "HEAD"))
}

// exec runs a DoMsg the way the shell does: each step through the
// fixture's runners, its RunMsg back into the screen, a failure stopping
// the chain; then the active-screen refresh.
func (f *fixture) exec(d ui.DoMsg) []ui.RunMsg {
	f.t.Helper()
	var out []ui.RunMsg
	for _, step := range append([]actions.Action{d.Action}, d.Then...) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		var r runner.Runner = f.agent
		if step.Program == actions.ProgramGit {
			r = f.git
		}
		res, err := r.Run(ctx, step.Argv...)
		cancel()
		m := ui.RunMsg{Action: step, Result: res, Err: err}
		out = append(out, m)
		_, cmd := f.s.Update(m)
		drive(f.s, cmd)
		if err != nil || res.ExitCode != 0 {
			break
		}
	}
	drive(f.s, f.s.Refresh())
	return out
}

func wantNothing(t *testing.T, out []tea.Msg) {
	t.Helper()
	if len(out) != 0 {
		t.Fatalf("emitted %v", out)
	}
}

func wantView(t *testing.T, s *Screen, wants ...string) {
	t.Helper()
	v := view(s)
	for _, w := range wants {
		if !strings.Contains(v, w) {
			t.Errorf("view lacks %q:\n%s", w, v)
		}
	}
}

func TestFetchUpdatesAheadBehind(t *testing.T) {
	home, bare := newTracked(t)
	pushFromOther(t, bare, map[string]string{skillA: "# a\n"})
	f := open(t, home)
	if strings.Contains(view(f.s), "↓1") {
		t.Fatal("behind before the fetch")
	}
	d := onlyDo(t, press(f.s, "f"))
	want := mustAct(actions.GitFetch(actions.GitRepo{Top: f.s.repo.Top}, "origin"))
	if !slices.Equal(d.Action.Argv, want.Argv) {
		t.Fatalf("argv %q, want %q", d.Action.Argv, want.Argv)
	}
	if r := f.exec(d); r[0].Result.ExitCode != 0 {
		t.Fatalf("fetch failed: %+v", r[0])
	}
	wantView(t, f.s, "↑0 ↓1", "fetched origin")
}

func TestFetchIsReadOnlyNoModal(t *testing.T) {
	home, bare := newTracked(t)
	pushFromOther(t, bare, map[string]string{skillA: "# a\n"})
	f := open(t, home)
	d := onlyDo(t, press(f.s, "f"))
	if !d.Action.ReadOnly || !d.Action.Network || d.Scope != runner.ScopeUser {
		t.Errorf("fetch action %+v scope %q", d.Action, d.Scope)
	}
	// Through the shell: it runs at once, no y pressed.
	sh := startShell(t, home, "")
	sh.keys("f")
	m := sh.wait("fetch RunMsg", func(m tea.Msg) bool {
		r, ok := m.(ui.RunMsg)
		return ok && gitCmd(r.Action.Argv) == "fetch"
	}).(ui.RunMsg)
	sh.finish()
	if m.Err != nil || m.Result.ExitCode != 0 {
		t.Fatalf("fetch: %+v", m)
	}
}

func TestPullFastForwardsAndOffersApply(t *testing.T) {
	home, bare := newTracked(t)
	pushFromOther(t, bare, map[string]string{skillA: "# a\n"})
	f := open(t, home)
	wantNothing(t, press(f.s, "A"))
	d := onlyDo(t, press(f.s, "p"))
	want := mustAct(actions.GitPull(actions.GitRepo{Top: f.s.repo.Top}))
	if !slices.Equal(d.Action.Argv, want.Argv) || d.Action.ReadOnly {
		t.Fatalf("pull action %+v", d.Action)
	}
	if r := f.exec(d); r[0].Result.ExitCode != 0 {
		t.Fatalf("pull failed: %+v", r[0])
	}
	if _, err := os.Stat(filepath.Join(home, filepath.FromSlash(skillA))); err != nil {
		t.Fatalf("not pulled: %v", err)
	}
	wantView(t, f.s, "pulled: agents see these changes only after an apply. D = Drift, A = Apply")
	if out := press(f.s, "A"); len(out) != 1 || out[0] != (ui.GotoMsg{Title: "Apply"}) {
		t.Errorf("A emitted %v", out)
	}
	if out := press(f.s, "D"); len(out) != 1 || out[0] != (ui.GotoMsg{Title: "Drift"}) {
		t.Errorf("D emitted %v", out)
	}
}

func TestPullDivergedExplainsAndChangesNothing(t *testing.T) {
	home, bare := newTracked(t)
	pushFromOther(t, bare, map[string]string{skillA: "# a\n"})
	gittest.Commit(t, home, "local", map[string]string{"skills/b/SKILL.md": "# b\n"})
	before := head(t, home)
	f := open(t, home)
	d := onlyDo(t, press(f.s, "p"))
	if r := f.exec(d); r[0].Result.ExitCode == 0 {
		t.Fatal("a diverged pull succeeded")
	}
	if head(t, home) != before {
		t.Error("HEAD moved")
	}
	top := shellQuote(f.s.repo.Top)
	wantView(t, f.s, "local and remote have diverged; nothing was changed. Resolve outside the TUI:",
		"git -C "+top+" pull --rebase", "git -C "+top+" merge @{u}", "then press r")
	if out := press(f.s, "A"); len(out) != 0 {
		t.Errorf("A after a failed pull emitted %v", out)
	}
}

func TestPushRoundTripToBare(t *testing.T) {
	home, bare := newTracked(t)
	gittest.Commit(t, home, "local", map[string]string{skillA: "# a\n"})
	f := open(t, home)
	d := onlyDo(t, press(f.s, "P"))
	if d.Action.ReadOnly || d.Action.TypedConfirm {
		t.Errorf("push action %+v", d.Action)
	}
	if r := f.exec(d); r[0].Result.ExitCode != 0 {
		t.Fatalf("push failed: %+v", r[0])
	}
	if got := strings.TrimSpace(gittest.Git(t, bare, "rev-parse", "main")); got != head(t, home) {
		t.Errorf("bare main %s, local %s", got, head(t, home))
	}
	wantView(t, f.s, "pushed "+head(t, home)[:7]+" to origin/main", "↑0 ↓0")
	if strings.Contains(f.s.note, "not pushed") {
		t.Errorf("note %q", f.s.note)
	}
}

func TestPushNeverForce(t *testing.T) {
	home, _ := newTracked(t)
	// A config that would force every branch with a plain `git push`.
	gittest.Git(t, home, "config", "remote.origin.push", "+refs/heads/*:refs/heads/*")
	gittest.Git(t, home, "config", "push.followTags", "true")
	gittest.Commit(t, home, "local", map[string]string{skillA: "# a\n"})
	f := open(t, home)
	d := onlyDo(t, press(f.s, "P"))
	want := mustAct(actions.GitPush(actions.GitRepo{Top: f.s.repo.Top}, "origin", "main", head(t, home)))
	if !slices.Equal(d.Action.Argv, want.Argv) || len(d.Then) != 0 {
		t.Fatalf("argv %q, want %q", d.Action.Argv, want.Argv)
	}
	for _, a := range d.Action.Argv {
		if strings.HasPrefix(a, "--force") || strings.HasPrefix(a, "+") || strings.Contains(a, "*") {
			t.Errorf("argv holds %q", a)
		}
	}
}

func TestPushRefusedMirrorRemote(t *testing.T) {
	home, _ := newTracked(t)
	gittest.Git(t, home, "config", "remote.origin.mirror", "true")
	gittest.Commit(t, home, "local", map[string]string{skillA: "# a\n"})
	f := open(t, home)
	wantNothing(t, press(f.s, "P"))
	wantView(t, f.s, "the remote is configured as a mirror; push outside the TUI")
}

func TestPushRefusedWhenUpstreamNameDiffers(t *testing.T) {
	home := newRepo(t)
	bare := gittest.Bare(t)
	gittest.Git(t, home, "remote", "add", "origin", bare)
	gittest.Git(t, home, "push", "-q", "origin", "main:master")
	gittest.Git(t, home, "branch", "-q", "--set-upstream-to=origin/master")
	gittest.Commit(t, home, "local", map[string]string{skillA: "# a\n"})
	f := open(t, home)
	wantNothing(t, press(f.s, "P"))
	wantView(t, f.s, "refs/heads/master", "push outside the TUI")
	if strings.Contains(gittest.Git(t, bare, "branch", "--list"), "main") {
		t.Error("a main branch was created on the remote")
	}
}

func TestPushRefusedNoUpstreamShowsGuidance(t *testing.T) {
	home := newRepo(t)
	f := open(t, home)
	top := shellQuote(f.s.repo.Top)
	for _, k := range []string{"P", "p", "f"} {
		wantNothing(t, press(f.s, k))
		wantView(t, f.s, "No upstream. Create a PRIVATE repository, then run outside the TUI:",
			"  git -C "+top+" remote add origin <url>",
			"  git -C "+top+" push -u origin main",
			"With the GitHub CLI: gh repo create <name> --private --source "+top+" --push")
	}
}

func TestLocalBranchUpstreamRefusedWithReason(t *testing.T) {
	home := newRepo(t)
	gittest.Git(t, home, "branch", "-q", "base")
	gittest.Git(t, home, "branch", "-q", "--set-upstream-to=base")
	gittest.Commit(t, home, "local", map[string]string{skillA: "# a\n"})
	f := open(t, home)
	for _, k := range []string{"f", "p", "P"} {
		wantNothing(t, press(f.s, k))
		wantView(t, f.s, "the upstream is a local branch; fetch, pull and push it outside the TUI")
		if strings.Contains(view(f.s), "invalid remote name") {
			t.Errorf("%s: generic remote-name error shown", k)
		}
	}
}

func TestPushRefusedWhenBehind(t *testing.T) {
	home, bare := newTracked(t)
	pushFromOther(t, bare, map[string]string{skillA: "# a\n"})
	gittest.Git(t, home, "fetch", "-q", "origin")
	gittest.Commit(t, home, "local", map[string]string{"skills/b/SKILL.md": "# b\n"})
	f := open(t, home)
	wantNothing(t, press(f.s, "P"))
	wantView(t, f.s, "behind 1: pull first")
}

func TestPushRefusedOutgoingHardFinding(t *testing.T) {
	home, _ := newTracked(t)
	// A commit made outside the TUI, with what `mcp add --env` would write.
	gittest.Commit(t, home, "token", map[string]string{tomlX: baseTOML + "env = { API_KEY = \"" + ghpToken + "\" }\n"})
	f := open(t, home)
	wantNothing(t, press(f.s, "P"))
	wantView(t, f.s, "refused: 1 hard finding", "mcp/x.toml:3", "git -C "+shellQuote(f.s.repo.Top)+" log -p @{u}..HEAD")
	if strings.Contains(view(f.s), ghpToken) {
		t.Error("the token is shown")
	}
}

func TestPushRefusedTokenAddedThenRemoved(t *testing.T) {
	home, _ := newTracked(t)
	gittest.Commit(t, home, "add", map[string]string{tomlX: baseTOML + "env = { API_KEY = \"" + ghpToken + "\" }\n"})
	gittest.Commit(t, home, "remove", map[string]string{tomlX: baseTOML})
	if d := gittest.Git(t, home, "diff", "@{u}", "HEAD"); d != "" {
		t.Fatalf("net diff not clean: %q", d)
	}
	f := open(t, home)
	wantNothing(t, press(f.s, "P"))
	wantView(t, f.s, "refused: 1 hard finding", "mcp/x.toml:3")
}

func TestPushRefusedTooManyFiles(t *testing.T) {
	home, _ := newTracked(t)
	files := map[string]string{}
	for i := range 2001 {
		files[fmt.Sprintf("skills/many/f%04d.md", i)] = "x\n"
	}
	gittest.Commit(t, home, "many", files)
	f := open(t, home)
	wantNothing(t, press(f.s, "P"))
	wantView(t, f.s, "too many files to scan; review and push outside the TUI")
}

func TestPushRefusedOutgoingStatePath(t *testing.T) {
	home, _ := newTracked(t)
	gittest.Commit(t, home, "state", map[string]string{"skills/.state/b.json": "{}\n"})
	f := open(t, home)
	wantNothing(t, press(f.s, "P"))
	wantView(t, f.s, "refused", "skills/.state/b.json")
}

func TestPushSoftFindingNeedsTypedPush(t *testing.T) {
	home, _ := newTracked(t)
	gittest.Commit(t, home, "soft", map[string]string{skillA: "id " + opaqueWord + "\n"})
	f := open(t, home)
	d := onlyDo(t, press(f.s, "P"))
	if !d.Action.TypedConfirm || d.Action.ConfirmVerb() != "push" ||
		!strings.Contains(d.Action.Warning, "1 possible secret") {
		t.Errorf("push action typed=%v warning=%q", d.Action.TypedConfirm, d.Action.Warning)
	}
	wantView(t, f.s, skillA+":1")
}

func TestPushWarnsPublicHost(t *testing.T) {
	home, _ := newTracked(t)
	gittest.Git(t, home, "config", "remote.origin.pushurl", "https://github.com/o/r.git")
	gittest.Commit(t, home, "local", map[string]string{skillA: "# a\n"})
	f := open(t, home)
	d := onlyDo(t, press(f.s, "P"))
	w := d.Action.Warning
	for _, want := range []string{"https://github.com/o/r.git",
		"pushes to github.com: make sure the repository is private; the TUI cannot see its visibility",
		"scanned against origin/main as last fetched; fetch (f) first if the remote may have been rewritten", whyWide} {
		if !strings.Contains(w, want) {
			t.Errorf("warning lacks %q: %q", want, w)
		}
	}
}

func TestPushModalShowsEveryPushURLMasked(t *testing.T) {
	home, bare := newTracked(t)
	cred := "https://user:" + ghpToken + "@example.com/o/r.git"
	gittest.Git(t, home, "config", "--add", "remote.origin.pushurl", bare)
	gittest.Git(t, home, "config", "--add", "remote.origin.pushurl", cred)
	gittest.Commit(t, home, "local", map[string]string{skillA: "# a\n"})
	f := open(t, home)
	d := onlyDo(t, press(f.s, "P"))
	app := ui.NewApp(deps(home, runnertest.New(), f.git, nil), New(deps(home, runnertest.New(), f.git, nil)))
	app.Update(tea.WindowSizeMsg{Width: 400, Height: 60})
	app.Update(d)
	v := ansi.Strip(app.View().Content)
	// The modal wraps long paths anywhere: compare without spaces and borders.
	flat := strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r == '│' {
			return -1
		}
		return r
	}, v)
	for _, want := range []string{bare, "example.com/o/r.git", "theremoteURLembedsacredential"} {
		if !strings.Contains(flat, strings.ReplaceAll(want, " ", "")) {
			t.Errorf("modal lacks %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, ghpToken) || strings.Contains(d.Action.Warning, ghpToken) {
		t.Errorf("modal shows the credential:\n%s", v)
	}
}

func TestPushStderrTokenMasked(t *testing.T) {
	home, _ := newTracked(t)
	hook := filepath.Join(home, ".git", "hooks", "pre-push")
	write(t, filepath.Dir(hook), "pre-push",
		"#!/bin/sh\necho \"rejected https://user:"+ghpToken+"@example.com/r.git token="+ghpToken+"\" >&2\nexit 1\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.Commit(t, home, "local", map[string]string{skillA: "# a\n"})
	sh := startShell(t, home, "")
	sh.keys("P")
	sh.wait("DoMsg", func(m tea.Msg) bool { _, ok := m.(ui.DoMsg); return ok })
	sh.keys("y")
	r := sh.wait("push RunMsg", func(m tea.Msg) bool {
		r, ok := m.(ui.RunMsg)
		return ok && gitCmd(r.Action.Argv) == "push"
	}).(ui.RunMsg)
	if r.Result.ExitCode == 0 {
		t.Fatal("the hook did not reject the push")
	}
	sh.finish()
	v := ansi.Strip(sh.p.App.View().Content)
	if strings.Contains(v, ghpToken) || strings.Contains(sh.s.note, "ghp_") {
		t.Errorf("token shown:\n%s\nnote %q", v, sh.s.note)
	}
	if !strings.Contains(v, "failed (exit 1)") || !strings.Contains(sh.s.note, "push failed") {
		t.Errorf("view:\n%s\nnote %q", v, sh.s.note)
	}
}

func TestPushRejectedNonFastForwardNote(t *testing.T) {
	home, _ := newTracked(t)
	gittest.Commit(t, home, "local", map[string]string{skillA: "# a\n"})
	f := open(t, home)
	d := onlyDo(t, press(f.s, "P"))
	f.s.Update(ui.RunMsg{Action: d.Action, Result: runner.Result{ExitCode: 1,
		Stderr: " ! [rejected]        main -> main (fetch first)\nerror: failed to push some refs\n"}})
	if !strings.Contains(f.s.note, "remote has new commits: fetch (f) and pull (p) first") {
		t.Errorf("note %q", f.s.note)
	}
}

func TestPullPushRefusedBelowRoot(t *testing.T) {
	gittest.Need(t)
	top := filepath.Join(t.TempDir(), "dots")
	gittest.Init(t, top)
	gittest.Commit(t, top, "init", map[string]string{"README.md": "r\n", "agentsync/agentsync.toml": "# a\n"})
	bare := gittest.Bare(t)
	gittest.Git(t, top, "remote", "add", "origin", bare)
	gittest.Git(t, top, "push", "-q", "-u", "origin", "main")
	gittest.Commit(t, top, "local", map[string]string{"agentsync/skills/a/SKILL.md": "# a\n"})
	f := open(t, filepath.Join(top, "agentsync"))
	rt := shellQuote(f.s.repo.Top)
	for _, k := range []string{"p", "P"} {
		wantNothing(t, press(f.s, k))
		wantView(t, f.s, "the repository at "+rt+" holds more than the agentsync home; pull and push it outside the TUI",
			"git -C "+rt+" pull --ff-only", "git -C "+rt+" push")
	}
	// Fetch stays allowed: it changes no file.
	d := onlyDo(t, press(f.s, "f"))
	if gitCmd(d.Action.Argv) != "fetch" {
		t.Errorf("f emitted %+v", d.Action)
	}
}

func TestPullRefusedDuringMergeAndLock(t *testing.T) {
	home, bare := newTracked(t)
	pushFromOther(t, bare, map[string]string{skillA: "# a\n"})
	f := open(t, home)
	lock := filepath.Join(home, ".git", "index.lock")
	write(t, filepath.Dir(lock), "index.lock", "")
	wantNothing(t, press(f.s, "p"))
	wantView(t, f.s, "index.lock")
	if _, err := os.Lstat(lock); err != nil {
		t.Errorf("the lock is gone: %v", err)
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(home, ".git"), "MERGE_HEAD", head(t, home)+"\n")
	wantNothing(t, press(f.s, "P"))
	wantView(t, f.s, "a merge is in progress")
}

func TestNetworkKeysDisabledForBackupRepo(t *testing.T) {
	home, _ := newTracked(t)
	gittest.Git(t, home, "config", "agentsync.managed", "true")
	f := open(t, home)
	n := len(f.git.all())
	for _, k := range []string{"f", "p", "P", "i"} {
		wantNothing(t, press(f.s, k))
	}
	if len(f.git.all()) != n {
		t.Errorf("keys ran git: %v", f.git.all()[n:])
	}
	wantView(t, f.s, "local-only destination history")
}

// --- hanging and unreachable remotes, through the real shell ---

// listener serves 127.0.0.1 and hands every connection to serve.
func listener(t *testing.T, serve func(net.Conn)) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			serve(c)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return ln.Addr().String()
}

// startShellBounded is startShell with the shell's Timeouts.Read set to
// bound; the screen's own local reads keep the default bound.
func startShellBounded(t *testing.T, home string, bound time.Duration) *shell {
	t.Helper()
	git := gittest.NewGit(t)
	sd := deps(home, runnertest.New(), git, nil)
	ad := sd
	ad.Timeouts = &ui.Timeouts{Read: bound}
	s := New(sd)
	return newShell(t, ad, s)
}

// waitRefresh waits for the refresh the shell starts after a run, so no
// read of the screen's is still in flight when the test checks for
// children left running.
func (sh *shell) waitRefresh() {
	sh.t.Helper()
	sh.wait("refresh", func(m tea.Msg) bool { _, ok := m.(loadMsg); return ok })
}

func noLiveChild(t *testing.T) {
	t.Helper()
	if live := runner.Live(); len(live) != 0 {
		t.Errorf("children left running: %q", live)
	}
}

func TestHangingRemoteTimesOut(t *testing.T) {
	home, _ := newTracked(t)
	addr := listener(t, func(net.Conn) {}) // accepts, never answers
	gittest.Git(t, home, "config", "remote.origin.pushurl", "http://"+addr+"/r.git")
	gittest.Commit(t, home, "local", map[string]string{skillA: "# a\n"})
	sh := startShellBounded(t, home, 200*time.Millisecond)
	sh.keys("P")
	sh.wait("DoMsg", func(m tea.Msg) bool { _, ok := m.(ui.DoMsg); return ok })
	sh.keys("y")
	r := sh.wait("push RunMsg", func(m tea.Msg) bool {
		r, ok := m.(ui.RunMsg)
		return ok && gitCmd(r.Action.Argv) == "push"
	}).(ui.RunMsg)
	sh.waitRefresh()
	sh.finish()
	if r.Err == nil || !strings.Contains(r.Err.Error(), "timed out") {
		t.Fatalf("push RunMsg %+v", r)
	}
	v := ansi.Strip(sh.p.App.View().Content)
	if !strings.Contains(v, "timed out") || !strings.Contains(sh.s.note, "timed out") {
		t.Errorf("view:\n%s\nnote %q", v, sh.s.note)
	}
	noLiveChild(t)
}

func TestPullStoppedSaysWorktreeMayBePartlyUpdated(t *testing.T) {
	home, _ := newTracked(t)
	addr := listener(t, func(net.Conn) {})
	gittest.Git(t, home, "config", "remote.origin.url", "http://"+addr+"/r.git")
	sh := startShellBounded(t, home, 200*time.Millisecond)
	sh.keys("p")
	sh.wait("DoMsg", func(m tea.Msg) bool { _, ok := m.(ui.DoMsg); return ok })
	sh.keys("y")
	sh.wait("pull RunMsg", func(m tea.Msg) bool {
		r, ok := m.(ui.RunMsg)
		return ok && gitCmd(r.Action.Argv) == "pull"
	})
	sh.waitRefresh()
	sh.finish()
	note := sh.s.note
	for _, want := range []string{"the pull was stopped; the worktree may be partly updated. Nothing was rolled back",
		"git -C " + shellQuote(sh.s.repo.Top) + " status"} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q: %q", want, note)
		}
	}
	if strings.Contains(note, "nothing was changed") {
		t.Errorf("note claims nothing changed: %q", note)
	}
	noLiveChild(t)
}

func TestNoTTYPromptOnSSH(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("ssh is not installed")
	}
	home, _ := newTracked(t)
	// An SSH server's banner, so ssh goes on to key exchange; then the
	// server reads the client's banner and hangs up.
	addr := listener(t, func(c net.Conn) {
		_, _ = c.Write([]byte("SSH-2.0-test\r\n"))
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, _ = c.Read(make([]byte, 4096))
		_ = c.Close()
	})
	_, port, _ := net.SplitHostPort(addr)
	gittest.Git(t, home, "config", "remote.origin.pushurl", "ssh://git@127.0.0.1:"+port+"/r.git")
	gittest.Commit(t, home, "local", map[string]string{skillA: "# a\n"})
	f := open(t, home)
	d := onlyDo(t, press(f.s, "P"))
	start := time.Now()
	r := f.exec(d)[0]
	if r.Err != nil || r.Result.ExitCode == 0 {
		t.Fatalf("push over ssh: %+v", r)
	}
	if time.Since(start) > 20*time.Second {
		t.Errorf("took %v", time.Since(start))
	}
	if !strings.Contains(f.s.note, "push failed") || strings.Contains(f.s.note, "stopped") {
		t.Errorf("note %q", f.s.note)
	}
	if !strings.Contains(r.Result.Stderr, "Connection closed") && !strings.Contains(r.Result.Stderr, "Host key verification failed") {
		t.Errorf("ssh did not reach the server: %q", r.Result.Stderr)
	}
}

func TestPushScansMergeCommits(t *testing.T) {
	home, _ := newTracked(t)
	gittest.Git(t, home, "checkout", "-q", "-b", "side")
	gittest.Commit(t, home, "side", map[string]string{tomlX: baseTOML + "env = { API_KEY = \"" + ghpToken + "\" }\n"})
	gittest.Git(t, home, "checkout", "-q", "main")
	gittest.Commit(t, home, "main", map[string]string{skillA: "# a\n"})
	gittest.Git(t, home, "merge", "-q", "--no-ff", "--no-edit", "side")
	f := open(t, home)
	wantNothing(t, press(f.s, "P"))
	wantView(t, f.s, "refused: 2 hard findings", "mcp/x.toml:3")
}

// A `merge -s ours` of a side branch that added (then deleted) a token
// is TREESAME to main, but the push publishes the side history: it is
// scanned, so the push is refused.
func TestPushRefusedOursMergeOfSecretSideBranch(t *testing.T) {
	home, _ := newTracked(t)
	gittest.Git(t, home, "checkout", "-q", "-b", "side")
	gittest.Commit(t, home, "side", map[string]string{tomlX: baseTOML + "env = { API_KEY = \"" + ghpToken + "\" }\n"})
	gittest.Commit(t, home, "side drop", map[string]string{tomlX: baseTOML})
	gittest.Git(t, home, "checkout", "-q", "main")
	gittest.Commit(t, home, "main", map[string]string{skillA: "# a\n"})
	gittest.Git(t, home, "merge", "-q", "-s", "ours", "--no-edit", "side")
	f := open(t, home)
	wantNothing(t, press(f.s, "P"))
	wantView(t, f.s, "refused: 1 hard finding", "mcp/x.toml:3")
}

// Greptile's example on the push side: an outgoing commit edits only the
// body of a multi-line value under a secret-named key (and a later one
// puts the reference back). The body is scanned in the commit's file.
func TestPushRefusedMultiLineSecretBodyEdit(t *testing.T) {
	home, _ := newTracked(t)
	ref := baseTOML + "[servers.x.env]\nAPI_KEY = '''\n${TOKEN}\n'''\n"
	gittest.Commit(t, home, "ref", map[string]string{tomlX: ref})
	gittest.Git(t, home, "push", "-q", "origin", "main")
	gittest.Commit(t, home, "literal", map[string]string{tomlX: strings.Replace(ref, "${TOKEN}", "hunter2", 1)})
	gittest.Commit(t, home, "back", map[string]string{tomlX: ref})
	f := open(t, home)
	wantNothing(t, press(f.s, "P"))
	wantView(t, f.s, "refused: 1 hard finding", "mcp/x.toml:5")
	if strings.Contains(view(f.s), "hunter2") {
		t.Error("the secret is shown")
	}
}

// Ruling 43: the same for a push: the body of a multi-line env value is
// classified by the blob's context, so it is hard whatever the key.
func TestPushRefusedEnvMultiLineBodyEdit(t *testing.T) {
	home, _ := newTracked(t)
	ref := baseTOML + "env.command = \"\"\"\n${TOKEN}\n\"\"\"\n"
	gittest.Commit(t, home, "ref", map[string]string{tomlX: ref})
	gittest.Git(t, home, "push", "-q", "origin", "main")
	gittest.Commit(t, home, "literal", map[string]string{tomlX: strings.Replace(ref, "${TOKEN}", opaqueWord, 1)})
	f := open(t, home)
	wantNothing(t, press(f.s, "P"))
	wantView(t, f.s, "refused: 1 hard finding", "mcp/x.toml:4")
	if strings.Contains(view(f.s), opaqueWord) {
		t.Error("the value is shown")
	}
}

// The same body edit under a key that is not secret-named needs no typed
// push.
func TestPushMultiLineBodyEditUnderPlainKey(t *testing.T) {
	home, _ := newTracked(t)
	gittest.Commit(t, home, "doc", map[string]string{tomlX: baseTOML + "description = '''\nold\n'''\n"})
	gittest.Git(t, home, "push", "-q", "origin", "main")
	gittest.Commit(t, home, "edit", map[string]string{tomlX: baseTOML + "description = '''\nhunter2\n'''\n"})
	f := open(t, home)
	d := onlyDo(t, press(f.s, "P"))
	if d.Action.TypedConfirm {
		t.Errorf("a plain body edit under a plain key asked for a typed push: %q", d.Action.Warning)
	}
}

// Ruling 32: a commit made on the branch after the guard scanned (while
// the confirm modal is open) never reaches the remote.
func TestPushSendsOnlyTheScannedCommit(t *testing.T) {
	home, bare := newTracked(t)
	gittest.Commit(t, home, "local", map[string]string{skillA: "# a\n"})
	scanned := head(t, home)
	f := open(t, home)
	d := onlyDo(t, press(f.s, "P"))
	gittest.Commit(t, home, "late", map[string]string{tomlX: baseTOML + "env = { API_KEY = \"" + ghpToken + "\" }\n"})
	late := head(t, home)
	if r := f.exec(d); r[0].Result.ExitCode != 0 {
		t.Fatalf("push failed: %+v", r[0])
	}
	if got := strings.TrimSpace(gittest.Git(t, bare, "rev-parse", "main")); got != scanned {
		t.Errorf("remote main %s, want the scanned %s", got, scanned)
	}
	if strings.Contains(gittest.Git(t, bare, "rev-list", "--all"), late) {
		t.Error("the unscanned commit reached the remote")
	}
	wantView(t, f.s, "↑1 ↓0", "pushed "+scanned[:7]+" to origin/main; 1 newer local commit(s) were not pushed (made after the scan)"+
		" — press P again to scan and push them")
}

func TestPendingPullBlocksOtherNetworkKeys(t *testing.T) {
	home, bare := newTracked(t)
	pushFromOther(t, bare, map[string]string{skillA: "# a\n"})
	f := open(t, home)
	d := onlyDo(t, press(f.s, "p"))
	for _, k := range []string{"f", "p", "P"} {
		wantNothing(t, press(f.s, k))
		wantView(t, f.s, "busy: the pull is still pending")
	}
	if r := f.exec(d); r[0].Result.ExitCode != 0 {
		t.Fatalf("pull failed: %+v", r[0])
	}
	wantView(t, f.s, pulledNote)
	if out := press(f.s, "A"); len(out) != 1 || out[0] != (ui.GotoMsg{Title: "Apply"}) {
		t.Errorf("A emitted %v", out)
	}
}

func TestStoppedPullNoteSurvivesFetchKey(t *testing.T) {
	home, _ := newTracked(t)
	f := open(t, home)
	d := onlyDo(t, press(f.s, "p"))
	wantNothing(t, press(f.s, "f"))
	f.s.Update(ui.RunMsg{Action: d.Action, Err: context.DeadlineExceeded})
	if !strings.Contains(f.s.note, "the pull was stopped") {
		t.Errorf("note %q", f.s.note)
	}
}

func TestNotRunReleasesPending(t *testing.T) {
	home, _ := newTracked(t)
	gittest.Commit(t, home, "local", map[string]string{skillA: "# a\n"})
	f := open(t, home)
	d := onlyDo(t, press(f.s, "P"))
	f.s.Update(ui.NotRunMsg{Actions: []actions.Action{{Program: actions.ProgramGit, Argv: []string{"-C", "/x", "fetch", "o"}}}})
	wantNothing(t, press(f.s, "f")) // another action's NotRunMsg releases nothing
	f.s.Update(ui.NotRunMsg{Actions: []actions.Action{d.Action}})
	if out := press(f.s, "f"); len(out) != 1 {
		t.Errorf("f after the push was not run emitted %v", out)
	}
}

func TestCancelledModalReleasesPending(t *testing.T) {
	home, _ := newTracked(t)
	gittest.Commit(t, home, "local", map[string]string{skillA: "# a\n"})
	sh := startShell(t, home, "")
	sh.keys("P")
	sh.wait("DoMsg", func(m tea.Msg) bool { _, ok := m.(ui.DoMsg); return ok })
	sh.keys("esc", "f")
	m := sh.wait("fetch RunMsg", func(m tea.Msg) bool {
		r, ok := m.(ui.RunMsg)
		return ok && gitCmd(r.Action.Argv) == "fetch"
	}).(ui.RunMsg)
	sh.finish()
	if m.Err != nil || m.Result.ExitCode != 0 {
		t.Fatalf("fetch: %+v", m)
	}
}

func TestPullBlockedByLocalChanges(t *testing.T) {
	home, bare := newTracked(t)
	pushFromOther(t, bare, map[string]string{tomlX: baseTOML + "args = [\"remote\"]\n"})
	write(t, home, tomlX, baseTOML+"args = [\"local\"]\n")
	before := head(t, home)
	f := open(t, home)
	d := onlyDo(t, press(f.s, "p"))
	r := f.exec(d)[0]
	if r.Result.ExitCode == 0 || !strings.Contains(r.Result.Stderr, "would be overwritten") {
		t.Fatalf("pull: %+v", r)
	}
	if head(t, home) != before {
		t.Error("HEAD moved")
	}
	wantView(t, f.s, "local changes block the pull; commit or stash them outside the TUI, then press r")
	if strings.Contains(f.s.note, "Your local changes") || strings.Contains(f.s.note, "x.toml") {
		t.Errorf("stderr reached the note: %q", f.s.note)
	}
}

func TestPushRefusedDeletingSensitivePath(t *testing.T) {
	home, _ := newTracked(t)
	gittest.Commit(t, home, "pem", map[string]string{"skills/k.pem": "x\n"})
	gittest.Git(t, home, "push", "-q", "origin", "main")
	gittest.Git(t, home, "rm", "-q", "skills/k.pem")
	gittest.Git(t, home, "commit", "-q", "-m", "drop pem")
	f := open(t, home)
	wantNothing(t, press(f.s, "P"))
	wantView(t, f.s, "skills/k.pem", "deleting such a file counts too")
}

// The guard round of a pull or push is running: f, p, P and i wait, so
// the network action that comes out of the round never replaces another.
func TestGuardRoundBlocksNetworkKeys(t *testing.T) {
	for _, first := range []string{"P", "p"} {
		t.Run(first, func(t *testing.T) {
			home, _ := newTracked(t)
			gittest.Commit(t, home, "local", map[string]string{skillA: "# a\n"})
			f := open(t, home)
			_, guard := f.s.Update(key(first))
			if guard == nil {
				t.Fatal("no guard round")
			}
			for _, k := range []string{"f", "p", "P", "i"} {
				wantNothing(t, press(f.s, k))
				wantView(t, f.s, "busy: the "+map[string]string{"P": "push", "p": "pull"}[first]+" is being checked")
			}
			d := onlyDo(t, drive(f.s, guard))
			// The shell refuses it (another action ran meanwhile): the
			// pending state is released, and a fetch now runs and reports.
			f.s.Update(ui.NotRunMsg{Actions: []actions.Action{d.Action}})
			df := onlyDo(t, press(f.s, "f"))
			if r := f.exec(df); r[0].Result.ExitCode != 0 {
				t.Fatalf("fetch: %+v", r[0])
			}
			wantView(t, f.s, "fetched origin")
		})
	}
}

// A checkout of another branch between the guard's status read and its
// pin never sends that branch's commits: the guard pins refs/heads/<b>
// of the branch it read, so the push is main's tip.
func TestPushPinsTheScannedBranch(t *testing.T) {
	home, bare := newTracked(t)
	gittest.Commit(t, home, "local", map[string]string{skillA: "# a\n"})
	mainTip := head(t, home)
	gittest.Git(t, home, "checkout", "-q", "-b", "feat")
	gittest.Commit(t, home, "feat", map[string]string{"skills/f/SKILL.md": "# f\n"})
	featTip := head(t, home)
	gittest.Git(t, home, "checkout", "-q", "main")
	f := open(t, home)
	var once sync.Once
	f.git.override = func(args []string) (runner.Result, bool) {
		if gitCmd(args) == "rev-parse" {
			once.Do(func() { gittest.Git(t, home, "checkout", "-q", "feat") })
		}
		return runner.Result{}, false
	}
	d := onlyDo(t, press(f.s, "P"))
	f.git.override = nil
	want := mustAct(actions.GitPush(actions.GitRepo{Top: f.s.repo.Top}, "origin", "main", mainTip))
	if !slices.Equal(d.Action.Argv, want.Argv) {
		t.Fatalf("argv %q, want %q", d.Action.Argv, want.Argv)
	}
	if r := f.exec(d); r[0].Result.ExitCode != 0 {
		t.Fatalf("push: %+v", r[0])
	}
	if got := strings.TrimSpace(gittest.Git(t, bare, "rev-parse", "main")); got != mainTip {
		t.Errorf("remote main %s, want main's tip %s", got, mainTip)
	}
	if strings.Contains(gittest.Git(t, bare, "rev-list", "--all"), featTip) {
		t.Error("feat's commit reached the remote")
	}
}

// A path whose name holds a credential, committed outside the TUI, is
// refused by name: the line scan never sees file names.
func TestPushRefusedCredentialNamedPath(t *testing.T) {
	home, _ := newTracked(t)
	gittest.Commit(t, home, "named", map[string]string{"skills/" + ghpToken + "/x.md": "# x\n"})
	f := open(t, home)
	wantNothing(t, press(f.s, "P"))
	wantView(t, f.s, "refused", "an outgoing commit touches", "the name looks like a credential", "a push would publish it")
	if strings.Contains(view(f.s), ghpToken) {
		t.Error("the token in the path is shown")
	}
}

// A replacement object (git replace) hides a committed token from git
// log, but a push sends the original commit: the guard reads outgoing
// commits as stored (GIT_NO_REPLACE_OBJECTS), so the push is refused.
func TestPushScansOriginalNotReplacementObjects(t *testing.T) {
	home, _ := newTracked(t)
	gittest.Commit(t, home, "token", map[string]string{tomlX: baseTOML + "env = { API_KEY = \"" + ghpToken + "\" }\n"})
	orig := head(t, home)
	parent := strings.TrimSpace(gittest.Git(t, home, "rev-parse", "HEAD^"))
	tree := strings.TrimSpace(gittest.Git(t, home, "rev-parse", "HEAD^^{tree}"))
	clean := strings.TrimSpace(gittest.Git(t, home, "commit-tree", tree, "-p", parent, "-m", "clean"))
	gittest.Git(t, home, "replace", orig, clean)
	f := open(t, home)
	wantNothing(t, press(f.s, "P"))
	wantView(t, f.s, "refused: 1 hard finding", "mcp/x.toml:3")
}

// A push publishes the outgoing commits' messages too: a token in one
// (committed outside the TUI, which refuses such a message) is a hard
// finding, an opaque word a soft one.
func TestPushScansOutgoingCommitMessages(t *testing.T) {
	home, _ := newTracked(t)
	gittest.Commit(t, home, "first", map[string]string{skillA: "# a\n"})
	gittest.Commit(t, home, "rotate\n\nold key "+ghpToken+"\n", map[string]string{skillA: "# b\n"})
	f := open(t, home)
	wantNothing(t, press(f.s, "P"))
	wantView(t, f.s, "refused: 1 hard finding", "commit message")
	if strings.Contains(view(f.s), ghpToken) {
		t.Errorf("view shows the token:\n%s", view(f.s))
	}
}

func TestPushSoftFindingInCommitMessageNeedsTypedPush(t *testing.T) {
	home, _ := newTracked(t)
	gittest.Commit(t, home, "id "+opaqueWord, map[string]string{skillA: "# a\n"})
	f := open(t, home)
	d := onlyDo(t, press(f.s, "P"))
	if !d.Action.TypedConfirm || !strings.Contains(d.Action.Warning, "1 possible secret") {
		t.Errorf("push action typed=%v warning=%q", d.Action.TypedConfirm, d.Action.Warning)
	}
}

// An outgoing path that starts with a newline keeps it: the unusual-name
// refusal sees the bytes git listed, not a trimmed, ordinary-looking name.
func TestPushRefusesOutgoingPathWithLeadingNewline(t *testing.T) {
	home, _ := newTracked(t)
	gittest.Commit(t, home, "odd", map[string]string{"\nnotes.md": ""}) // no added line: no post-image read
	f := open(t, home)
	wantNothing(t, press(f.s, "P"))
	wantView(t, f.s, "refused: an outgoing commit touches", "unusual name")
}

// A push URL other than the fetch URL may lack more than the fetched
// upstream does: here it holds only the first commit, the upstream also a
// commit with a token, and pushing the next commit there would publish
// that token too. The guard then scans the branch's whole history.
func TestPushToOtherURLScansWholeHistory(t *testing.T) {
	home, bare := newTracked(t)
	mirror := filepath.Join(t.TempDir(), "mirror.git")
	gittest.Git(t, "", "clone", "-q", "--bare", bare, mirror)
	pushFromOther(t, bare, map[string]string{tomlX: baseTOML + "env = { API_KEY = \"" + ghpToken + "\" }\n"})
	gittest.Git(t, home, "pull", "-q", "--ff-only")
	gittest.Commit(t, home, "local", map[string]string{skillA: "# a\n"})
	gittest.Git(t, home, "config", "remote.origin.pushurl", mirror)
	f := open(t, home)
	wantNothing(t, press(f.s, "P"))
	wantView(t, f.s, "refused: 1 hard finding", "mcp/x.toml:3")
}

// With the push URL the fetch URL, the scan stays base..tip: the same
// history pushed to its own remote passes.
func TestPushToFetchURLScansOutgoingOnly(t *testing.T) {
	home, bare := newTracked(t)
	pushFromOther(t, bare, map[string]string{tomlX: baseTOML + "env = { API_KEY = \"" + ghpToken + "\" }\n"})
	gittest.Git(t, home, "pull", "-q", "--ff-only")
	gittest.Commit(t, home, "local", map[string]string{skillA: "# a\n"})
	f := open(t, home)
	d := onlyDo(t, press(f.s, "P"))
	if d.Action.TypedConfirm {
		t.Errorf("push needs typing: %q", d.Action.Warning)
	}
}

// log.showRoot=false (user or repository config) makes git log leave out
// the root commit's diff: a whole-history scan must still read it, or a
// token the root added (and a later commit deleted) goes out unscanned.
func TestWideScanReadsRootCommitWhatever(t *testing.T) {
	gittest.Need(t)
	home := filepath.Join(t.TempDir(), "home")
	gittest.Init(t, home)
	gittest.Commit(t, home, "root", map[string]string{"agentsync.toml": "# config\n",
		tomlX: baseTOML + "env = { API_KEY = \"" + ghpToken + "\" }\n"})
	gittest.Git(t, home, "rm", "-q", "--", tomlX)
	gittest.Git(t, home, "commit", "-q", "--message=drop")
	bare, mirror := gittest.Bare(t), filepath.Join(t.TempDir(), "mirror.git")
	gittest.Git(t, home, "remote", "add", "origin", bare)
	gittest.Git(t, home, "push", "-q", "-u", "origin", "main")
	gittest.Git(t, "", "init", "-q", "--bare", mirror)
	gittest.Git(t, home, "config", "remote.origin.pushurl", mirror)
	gittest.Git(t, home, "config", "log.showRoot", "false")
	gittest.Commit(t, home, "local", map[string]string{skillA: "# a\n"})
	f := open(t, home)
	wantNothing(t, press(f.s, "P"))
	wantView(t, f.s, "refused: 1 hard finding", "mcp/x.toml:3")
	for _, c := range f.git.all() {
		if (slices.Contains(c, "--name-only") || slices.Contains(c, "-p")) && !slices.Contains(c, "--root") {
			t.Errorf("outgoing log read without --root: %q", c)
		}
	}
}

// cutRead makes the fixture's git read for argv holding marker come back
// marked cut (runner.TruncatedMark after its output), as a read past the
// runner's cap does.
func cutRead(f *fixture, marker string) {
	f.git.mu.Lock()
	defer f.git.mu.Unlock()
	f.git.override = func(args []string) (runner.Result, bool) {
		if !slices.Contains(args, marker) {
			return runner.Result{}, false
		}
		res, err := f.git.inner.Run(context.Background(), args...)
		if err != nil {
			return runner.Result{}, false
		}
		res.Stdout += runner.TruncatedMark
		return res, true
	}
}

// A whole-history scan (push URL other than the fetch URL) that was cut
// is refused outright: what it did not read may be anything the
// destination lacks. A cut narrow scan stays a soft finding.
func TestWideScanCutIsRefused(t *testing.T) {
	for _, marker := range []string{"-p", "--format=%H%x00%B%x00"} {
		home, _ := newTracked(t)
		gittest.Commit(t, home, "local", map[string]string{skillA: "# a\n" + strings.Repeat("line\n", 50)})
		mirror := filepath.Join(t.TempDir(), "mirror.git")
		gittest.Git(t, "", "init", "-q", "--bare", mirror)
		gittest.Git(t, home, "config", "remote.origin.pushurl", mirror)
		f := open(t, home)
		cutRead(f, marker)
		wantNothing(t, press(f.s, "P"))
		wantView(t, f.s, whyWideCut)
	}
	home, _ := newTracked(t)
	gittest.Commit(t, home, "local", map[string]string{skillA: "# a\n" + strings.Repeat("line\n", 50)})
	f := open(t, home)
	cutRead(f, "-p")
	d := onlyDo(t, press(f.s, "P"))
	if !d.Action.TypedConfirm {
		t.Errorf("a cut narrow scan did not ask for a typed push: %q", d.Action.Warning)
	}
}

// Ruling 48 for a push: the blob gives the [servers.x.env] context.
func TestPushRefusedVisibleKeyInEnvTable(t *testing.T) {
	home, _ := newTracked(t)
	gittest.Commit(t, home, "env", map[string]string{tomlX: envTableTOML})
	gittest.Git(t, home, "push", "-q", "origin", "main")
	gittest.Commit(t, home, "name", map[string]string{tomlX: envTableTOML + "name = '" + opaqueWord + "'\n"})
	f := open(t, home)
	wantNothing(t, press(f.s, "P"))
	wantView(t, f.s, "refused: 1 hard finding", "mcp/x.toml:8")
	if strings.Contains(view(f.s), opaqueWord) {
		t.Error("the value is shown")
	}
}

// The fetched upstream tells what the destination holds only while it is
// still the destination's: after `remote set-url` to a new, empty
// repository the old remote-tracking ref stays, but the push would
// publish the whole history there. The guard asks the remote where its
// branch is; without one there, the whole history is scanned.
func TestPushToEmptiedRemoteScansWholeHistory(t *testing.T) {
	home, bare := newTracked(t)
	pushFromOther(t, bare, map[string]string{tomlX: baseTOML + "env = { API_KEY = \"" + ghpToken + "\" }\n"})
	gittest.Git(t, home, "pull", "-q", "--ff-only")
	gittest.Commit(t, home, "local", map[string]string{skillA: "# a\n"})
	gittest.Git(t, home, "remote", "set-url", "origin", gittest.Bare(t))
	f := open(t, home)
	wantNothing(t, press(f.s, "P"))
	wantView(t, f.s, "refused: 1 hard finding", "mcp/x.toml:3", "has no main")
}

// A remote whose branch is not where the remote-tracking ref says (moved
// on since the last fetch, or another repository behind the same name)
// is refused until a fetch: the scan's base is not known to be there.
func TestPushRefusedWhenRemoteBranchMovedSinceFetch(t *testing.T) {
	home, bare := newTracked(t)
	gittest.Commit(t, home, "local", map[string]string{skillA: "# a\n"})
	pushFromOther(t, bare, map[string]string{"skills/b/SKILL.md": "# b\n"})
	f := open(t, home)
	wantNothing(t, press(f.s, "P"))
	wantView(t, f.s, "is not where it was last fetched", "fetch (f) first")
}

// git log's %B ends a message at a NUL byte, so a token after one is never
// scanned, yet a push sends the whole commit object. git refuses to make
// such a commit; one written with hash-object --literally (or fetched from
// elsewhere) is refused as a hard finding.
func TestPushRefusedNULInCommitMessage(t *testing.T) {
	home, _ := newTracked(t)
	tree := strings.TrimSpace(gittest.Git(t, home, "rev-parse", "HEAD^{tree}"))
	obj := "tree " + tree + "\nparent " + head(t, home) + "\nauthor t <t@example.invalid> 1 +0000\n" +
		"committer t <t@example.invalid> 1 +0000\n\nsubject\n\x00" + ghpToken + "\n"
	p := filepath.Join(t.TempDir(), "commit")
	if err := os.WriteFile(p, []byte(obj), 0o600); err != nil {
		t.Fatal(err)
	}
	c := strings.TrimSpace(gittest.Git(t, home, "hash-object", "--literally", "-t", "commit", "-w", p))
	gittest.Git(t, home, "update-ref", "refs/heads/main", c)
	f := open(t, home)
	wantNothing(t, press(f.s, "P"))
	wantView(t, f.s, "refused: 1 hard finding", gitrepo.KindNULInCommit)
	if strings.Contains(view(f.s), ghpToken) {
		t.Error("the token after the NUL is shown")
	}
}

// A commit object over the runner's 4 MiB cut (a huge extra header made
// with hash-object --literally) hides a NUL past the cut from the
// cat-file read, while git log shows a harmless subject: the push is
// refused rather than passed unchecked. A normal commit is unaffected.
func TestPushRefusedCommitObjectReadCut(t *testing.T) {
	home, _ := newTracked(t)
	tree := strings.TrimSpace(gittest.Git(t, home, "rev-parse", "HEAD^{tree}"))
	obj := "tree " + tree + "\nparent " + head(t, home) + "\nauthor t <t@example.invalid> 1 +0000\n" +
		"committer t <t@example.invalid> 1 +0000\nx-pad " + strings.Repeat("a", 5<<20) + "\n\nsubject\n\x00" + ghpToken + "\n"
	p := filepath.Join(t.TempDir(), "commit")
	if err := os.WriteFile(p, []byte(obj), 0o600); err != nil {
		t.Fatal(err)
	}
	c := strings.TrimSpace(gittest.Git(t, home, "hash-object", "--literally", "-t", "commit", "-w", p))
	gittest.Git(t, home, "update-ref", "refs/heads/main", c)
	f := open(t, home)
	wantNothing(t, press(f.s, "P"))
	wantView(t, f.s, "is over 4 MiB", "push it outside the TUI")
}
