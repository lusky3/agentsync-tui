package e2e

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/lusky3/agentsync-tui/internal/app"
	"github.com/lusky3/agentsync-tui/internal/gitrepo/gittest"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/testenv"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

// frames wraps the app and keeps the latest rendered frame, so a test
// waits on whole frames rather than on the renderer's partial redraws.
type frames struct {
	*ui.App
	mu   sync.Mutex
	last string
}

func (f *frames) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := f.App.Update(msg)
	v := ansi.Strip(f.App.View().Content)
	f.mu.Lock()
	f.last = v
	f.mu.Unlock()
	return f, cmd
}

func (f *frames) frame() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last
}

// harness is one TUI over a hermetic agentsync home that is a git
// repository, driven by the real agentsync and git binaries.
type harness struct {
	t   *testing.T
	env *testenv.Env
	tm  *teatest.TestModel
	f   *frames
	log string
}

// newSourceRepoEnv skips without agentsync or git, then makes the hermetic
// home and moves every child's working directory into it, so no agentsync
// child can walk up to a real .agentsync tree.
func newSourceRepoEnv(t *testing.T) *testenv.Env {
	t.Helper()
	if _, err := exec.LookPath("agentsync"); err != nil {
		t.Skip("agentsync not on PATH")
	}
	gittest.Need(t)
	env := testenv.New(t)
	env.Chdir(t)
	return env
}

// trackedHome initialises dir as a repository whose agentsync.toml and
// .gitignore are committed and pushed to a new bare origin, and returns
// the bare repository.
func trackedHome(t *testing.T, dir string) string {
	t.Helper()
	gittest.Git(t, dir, "init", "-q", "-b", "main")
	gittest.Git(t, dir, "add", "--", "agentsync.toml", ".gitignore")
	gittest.Git(t, dir, "commit", "-q", "--message=init")
	bare := gittest.Bare(t)
	gittest.Git(t, dir, "remote", "add", "origin", bare)
	gittest.Git(t, dir, "push", "-q", "-u", "origin", "main")
	return bare
}

// open starts the TUI and moves to the last tab (Source repo).
func open(t *testing.T, env *testenv.Env) *harness {
	t.Helper()
	return openTab(t, env, "the Source repo tab", func(f string) bool { return strings.Contains(f, "repo: ") })
}

// openTab is open that waits on ready, described as what, once on the
// Source repo tab.
func openTab(t *testing.T, env *testenv.Env, what string, ready func(string) bool) *harness {
	t.Helper()
	ex := runner.NewExec("agentsync", runner.ScopeUser, "")
	ex.Env = env.Env
	git := runner.NewGit("git")
	git.Env = gittest.Env(t)
	h := &harness{t: t, env: env, log: filepath.Join(env.Home, "activity.log")}
	deps := ui.Deps{
		Runner:        ex,
		Interactive:   ex.Interactive,
		Git:           git,
		AgentsyncHome: env.AgentsyncHome,
		Scope:         runner.ScopeUser,
		LogPath:       h.log,
	}
	h.f = &frames{App: ui.NewApp(deps, app.Screens(deps)...)}
	h.tm = teatest.NewTestModel(t, h.f, teatest.WithInitialTermSize(200, 50))
	t.Cleanup(func() {
		h.tm.Send(tea.KeyPressMsg{Code: tea.KeyEscape}) // leave the message field, if open
		h.tm.Type("q")
		h.tm.WaitFinished(t, teatest.WithFinalTimeout(waitFor))
	})
	h.wait("first tab", func(string) bool { return true })
	h.tm.Send(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	h.wait(what, ready)
	return h
}

// wait polls the latest frame until ok holds and returns it.
func (h *harness) wait(what string, ok func(string) bool) string {
	h.t.Helper()
	deadline := time.Now().Add(waitFor)
	for {
		if f := h.f.frame(); f != "" && ok(f) {
			return f
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s; last frame:\n%s", what, h.f.frame())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (h *harness) key(s string) {
	if s == "enter" {
		h.tm.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
		return
	}
	h.tm.Type(s)
}

// squash drops every space and box-drawing border, so a path the modal
// wrapped over two lines still matches.
func squash(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || r == '│' {
			return -1
		}
		return r
	}, s)
}

// commitAll types msg into the commit field and waits for the chain's
// confirm modal, whose frame it returns.
func (h *harness) commitAll(msg string) string {
	h.t.Helper()
	h.key("c")
	h.wait("the message field", func(f string) bool { return strings.Contains(f, "message") })
	h.key(msg)
	h.key("enter")
	return h.wait("the commit modal", func(f string) bool {
		return strings.Contains(f, "Run it?") && strings.Count(f, "git -C") >= 2
	})
}

func TestSourceRepoCommitPushCloneRoundTrip(t *testing.T) {
	env := newSourceRepoEnv(t)
	env.Init(t)
	bare := trackedHome(t, env.AgentsyncHome)
	if _, se, code := env.Agentsync(t, "--no-input", "mcp", "add", "e2e-srv", "--command", "echo"); code != 0 {
		t.Fatalf("mcp add exit %d: %s", code, se)
	}

	h := open(t, env)
	h.wait("the new server file", func(f string) bool { return strings.Contains(f, "mcp/e2e-srv.toml") })

	modal := h.commitAll("feat: add e2e-srv")
	if n := strings.Count(modal, "$ git -C"); n != 2 {
		t.Errorf("modal shows %d git command lines, want add and commit:\n%s", n, modal)
	}
	h.key("y")
	h.wait("the commit", func(f string) bool { return strings.Contains(f, "committed: feat: add e2e-srv") })

	h.key("P")
	h.wait("the push modal", func(f string) bool { return strings.Contains(f, "Run it?") && strings.Contains(f, "push") })
	h.key("y")
	h.wait("the push", func(f string) bool { return strings.Contains(f, "pushed ") })

	other := filepath.Join(t.TempDir(), "other")
	gittest.Git(t, "", "clone", "-q", bare, other)
	b, err := os.ReadFile(filepath.Join(other, "mcp", "e2e-srv.toml"))
	if err != nil || !strings.Contains(string(b), "command = 'echo'") {
		t.Fatalf("clone lacks the server: %v\n%s", err, b)
	}
	if _, err := os.Stat(filepath.Join(other, ".state")); !os.IsNotExist(err) {
		t.Errorf(".state reached the remote (stat: %v)", err)
	}

	lb, err := os.ReadFile(h.log)
	if err != nil {
		t.Fatal(err)
	}
	log := string(lb)
	for _, want := range []string{" add -- mcp/e2e-srv.toml", " commit '--message=feat: add e2e-srv' -- mcp/e2e-srv.toml", " push --no-follow-tags --no-recurse-submodules origin "} {
		if !strings.Contains(log, want) {
			t.Errorf("activity log lacks %q:\n%s", want, log)
		}
	}
	if n := strings.Count(log, "git -C "); n < 2 {
		t.Errorf("activity log has %d git -C entries, want the add and the commit at least:\n%s", n, log)
	}
	for _, leak := range []string{"command = ", "[server]", "agents = "} {
		if strings.Contains(log, leak) {
			t.Errorf("activity log holds TOML content %q:\n%s", leak, log)
		}
	}
}

func TestSourceRepoRefusesLiteralTokenEndToEnd(t *testing.T) {
	env := newSourceRepoEnv(t)
	env.Init(t)
	trackedHome(t, env.AgentsyncHome)
	token := "ghp_" + strings.Repeat("A1b2C3d4E5", 4)[:36]
	if _, se, code := env.Agentsync(t, "--no-input", "mcp", "add", "leak", "--command", "echo", "--env", "API_KEY="+token); code != 0 {
		t.Fatalf("mcp add exit %d: %s", code, se)
	}
	before := strings.TrimSpace(gittest.Git(t, env.AgentsyncHome, "rev-list", "--count", "HEAD"))

	h := open(t, env)
	h.wait("the leaking file", func(f string) bool { return strings.Contains(f, "mcp/leak.toml") })
	h.key("c")
	h.wait("the message field", func(f string) bool { return strings.Contains(f, "message") })
	h.key("feat: add leak")
	h.key("enter")
	f := h.wait("the refusal", func(f string) bool { return strings.Contains(f, "hard finding") })
	if !strings.Contains(f, "mcp/leak.toml:") || !strings.Contains(f, "••••") {
		t.Errorf("refusal lacks the masked file:line finding:\n%s", f)
	}
	if strings.Contains(f, token) {
		t.Errorf("refusal shows the token:\n%s", f)
	}
	if strings.Contains(f, "Run it?") {
		t.Errorf("a commit was offered:\n%s", f)
	}
	if after := strings.TrimSpace(gittest.Git(t, env.AgentsyncHome, "rev-list", "--count", "HEAD")); after != before {
		t.Errorf("commits %s -> %s: the refused commit ran", before, after)
	}
}

func TestSourceRepoSymlinkedHome(t *testing.T) {
	env := newSourceRepoEnv(t)
	env.Init(t)
	repo := filepath.Join(env.Root, "dotfiles", "agentsync")
	if err := os.MkdirAll(filepath.Dir(repo), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(env.AgentsyncHome, repo); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(repo, env.AgentsyncHome); err != nil {
		t.Fatal(err)
	}
	trackedHome(t, repo)
	if _, se, code := env.Agentsync(t, "--no-input", "mcp", "add", "linked", "--command", "echo"); code != 0 {
		t.Fatalf("mcp add exit %d: %s", code, se)
	}
	resolved, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}

	h := open(t, env)
	h.wait("the new server file", func(f string) bool { return strings.Contains(f, "mcp/linked.toml") })
	modal := h.commitAll("feat: add linked")
	if !strings.Contains(squash(modal), squash("git -C "+resolved+" add")) {
		t.Errorf("modal does not run git in the resolved home %s:\n%s", resolved, modal)
	}
	h.key("y")
	h.wait("the commit", func(f string) bool { return strings.Contains(f, "committed: feat: add linked") })
	if got := gittest.Git(t, repo, "log", "-1", "--format=%s"); strings.TrimSpace(got) != "feat: add linked" {
		t.Errorf("HEAD subject = %q", got)
	}
}
