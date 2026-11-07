// Package gittest is the hermetic git environment every test that runs the
// real git binary shares: a temp HOME and XDG_CONFIG_HOME, a temp
// GIT_CONFIG_GLOBAL file holding a test identity, GIT_CONFIG_NOSYSTEM=1, and
// every other inherited GIT_* variable scrubbed, so neither the developer's
// git configuration nor a GIT_DIR inherited from a hook can reach the
// repositories a test builds. Tests skip when git is not on PATH.
package gittest

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/lusky3/agentsync-tui/internal/runner"
)

// Identity is the author and committer every gittest commit uses.
const (
	UserName  = "gittest"
	UserEmail = "gittest@example.invalid"
)

// envs caches one hermetic environment per test, so every helper call in a
// test shares the same HOME and global config.
var envs sync.Map // testing.TB -> []string

// Need skips t when git is not on PATH.
func Need(t testing.TB) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
}

// Env returns t's hermetic git environment: os.Environ() without HOME,
// XDG_CONFIG_HOME and any GIT_* variable, plus a temp HOME, a temp
// XDG_CONFIG_HOME, GIT_CONFIG_GLOBAL naming a temp file with the test
// identity (and init.defaultBranch=main), GIT_CONFIG_NOSYSTEM=1 and
// GIT_CEILING_DIRECTORIES at the parent of the test's temp dirs.
func Env(t testing.TB) []string {
	t.Helper()
	if v, ok := envs.Load(t); ok {
		return v.([]string)
	}
	root := t.TempDir()
	home := filepath.Join(root, "home")
	xdg := filepath.Join(root, "xdg")
	for _, d := range []string{home, xdg} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	global := filepath.Join(root, "gitconfig")
	cfg := "[user]\n\tname = " + UserName + "\n\temail = " + UserEmail +
		"\n[init]\n\tdefaultBranch = main\n[commit]\n\tgpgSign = false\n"
	if err := os.WriteFile(global, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if k == "HOME" || k == "XDG_CONFIG_HOME" || strings.HasPrefix(k, "GIT_") {
			continue
		}
		env = append(env, kv)
	}
	// Discovery stops at the parent of the test's temp dirs, so a repository
	// above the developer's TMPDIR is never found by a gittest.Git call. (The
	// production Program scrubs this variable, R2.)
	env = append(env, "HOME="+home, "XDG_CONFIG_HOME="+xdg,
		"GIT_CONFIG_GLOBAL="+global, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_CEILING_DIRECTORIES="+filepath.Dir(root))
	envs.Store(t, env)
	t.Cleanup(func() { envs.Delete(t) })
	return env
}

// Git runs git with args (preceded by -C dir when dir is not empty) in t's
// hermetic environment and returns its stdout. A non-zero exit fails t.
func Git(t testing.TB, dir string, args ...string) string {
	t.Helper()
	Need(t)
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	cmd := exec.Command("git", args...)
	cmd.Env = Env(t)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String()
}

// Init creates dir (and its parents) and makes it a git repository whose
// branch is main.
func Init(t testing.TB, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	Git(t, dir, "init", "-q")
}

// Bare creates a bare repository in a new temp dir (a test remote) and
// returns its path.
func Bare(t testing.TB) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "remote.git")
	Git(t, "", "init", "-q", "--bare", dir)
	return dir
}

// Commit writes files (slash-separated paths relative to dir, parents
// created) and commits exactly those paths with msg as the test identity.
func Commit(t testing.TB, dir, msg string, files map[string]string) {
	t.Helper()
	paths := make([]string, 0, len(files))
	for p, body := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
	}
	sort.Strings(paths)
	args := []string{"commit", "-q", "--allow-empty", "--message=" + msg}
	if len(paths) > 0 {
		Git(t, dir, append([]string{"add", "--"}, paths...)...)
		// commit -- <paths> (--only): another staged change stays staged.
		args = append(append(args, "--"), paths...)
	}
	Git(t, dir, args...)
}

// NewGit returns the production git Program (runner.NewGit, with its
// hardening) whose base environment is t's hermetic one.
func NewGit(t testing.TB) *runner.Program {
	t.Helper()
	Need(t)
	p := runner.NewGit("git")
	p.Env = Env(t)
	return p
}
