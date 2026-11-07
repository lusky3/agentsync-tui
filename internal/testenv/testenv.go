// Package testenv provides a hermetic environment for tests that drive the
// real agentsync binary: HOME, AGENTSYNC_HOME and XDG_* all point into a
// temp root no ancestor of which holds a .agentsync entry (see Root), and
// every knob the CLI reads from the environment that could
// steer it elsewhere (AGENTSYNC_* by prefix, GROK_HOME, NO_COLOR) is
// scrubbed, so nothing touches the developer's real ~/.agentsync.
package testenv

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Env is a hermetic home for running agentsync.
type Env struct {
	Root          string   // the temp root everything below lives in
	Home          string   // fake $HOME
	AgentsyncHome string   // fake $AGENTSYNC_HOME (the canonical source tree)
	Env           []string // os.Environ() with the hermetic overrides applied
}

// New creates a hermetic environment rooted in a new Root.
func New(t *testing.T) *Env {
	t.Helper()
	root := Root(t)
	e := &Env{
		Root:          root,
		Home:          filepath.Join(root, "home"),
		AgentsyncHome: filepath.Join(root, "home", ".agentsync"),
	}
	over := map[string]string{
		"HOME":            e.Home,
		"AGENTSYNC_HOME":  e.AgentsyncHome,
		"XDG_CONFIG_HOME": filepath.Join(root, "xdg", "config"),
		"XDG_STATE_HOME":  filepath.Join(root, "xdg", "state"),
		"XDG_CACHE_HOME":  filepath.Join(root, "xdg", "cache"),
	}
	for k, v := range over {
		if k == "AGENTSYNC_HOME" {
			continue // must not exist before init
		}
		if err := os.MkdirAll(v, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if _, ok := over[k]; !ok && !scrubbed(k) {
			e.Env = append(e.Env, kv)
		}
	}
	for k, v := range over {
		e.Env = append(e.Env, k+"="+v)
	}
	return e
}

// fallbackTemp is where Root goes when os.TempDir() lies below a
// .agentsync tree.
const fallbackTemp = "/tmp"

// Root returns a new temp directory, removed when t ends, such that neither
// it nor any ancestor holds a .agentsync entry. agentsync walks up from its
// working directory looking for a project tree and, finding one, refuses a
// command that names no scope (or, with one, may read it). A developer's
// TMPDIR below the real home would otherwise put the real ~/.agentsync on
// that walk. The directory is made under os.TempDir() when that is clear,
// else under /tmp; when neither is, t fails.
func Root(t testing.TB) string {
	t.Helper()
	base := os.TempDir()
	if AgentsyncAncestor(base) != "" {
		base = fallbackTemp
		if owner := AgentsyncAncestor(base); owner != "" {
			t.Fatalf("testenv: both TMPDIR and %s lie below %s/.agentsync; set TMPDIR elsewhere", fallbackTemp, owner)
		}
	}
	dir, err := os.MkdirTemp(base, "agentsync-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeAll(dir) })
	return dir
}

// removeAll deletes dir, first making every directory below it writable
// (as t.TempDir's cleanup does) so a read-only tree is removed too.
func removeAll(dir string) {
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o700)
		}
		return nil
	})
	_ = os.RemoveAll(dir)
}

// AgentsyncAncestor returns the nearest of dir and its ancestors that holds
// a .agentsync entry (of any type), checking both the path as given and
// with its symlinks resolved; "" when there is none.
func AgentsyncAncestor(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return ""
	}
	paths := []string{abs}
	if r, err := filepath.EvalSymlinks(abs); err == nil && r != abs {
		paths = append(paths, r)
	}
	for _, p := range paths {
		if owner := agentsyncAbove(p); owner != "" {
			return owner
		}
	}
	return ""
}

func agentsyncAbove(dir string) string {
	for {
		if _, err := os.Lstat(filepath.Join(dir, ".agentsync")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// scrubbed reports whether an inherited variable must not reach the child.
// AGENTSYNC_* covers every CLI knob (v0.17.0 internal/paths, internal/cli):
// AGENTSYNC_TARGET_ROOT redirects the whole tree and outranks
// AGENTSYNC_HOME, the AGENTSYNC_ALLOW_* switches and AGENTSYNC_LOCK_TIMEOUT_MS
// change behaviour. GROK_HOME is a third-party agent home override the CLI
// honours; NO_COLOR flips its colour decision. EDITOR is left alone: no
// test drives `secret edit` through this environment.
func scrubbed(k string) bool {
	return strings.HasPrefix(k, "AGENTSYNC_") || k == "GROK_HOME" || k == "NO_COLOR"
}

// Agentsync runs the real agentsync binary with the hermetic environment and
// returns its output and exit code. The test is skipped if the binary is not
// on PATH.
func (e *Env) Agentsync(t *testing.T, args ...string) (stdout, stderr string, exit int) {
	t.Helper()
	bin, err := exec.LookPath("agentsync")
	if err != nil {
		t.Skip("agentsync not on PATH")
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = e.Home
	cmd.Env = e.Env
	var o, s bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &s
	err = cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		exit = ee.ExitCode()
	default:
		t.Fatalf("running agentsync: %v", err)
	}
	return o.String(), s.String(), exit
}

// Init scaffolds the hermetic source tree and fails the test on error.
//
// Discovered with agentsync v0.17.0: `agentsync init` (no args) scaffolds
// ~/.agentsync with the standard layout and a stub agentsync.toml. The
// destination must not exist or be empty, so re-running on a populated home
// is refused. The global --no-input flag makes it fail instead of prompting,
// which is what we pass; the hermetic HOME is the only thing standing between
// it and the real ~/.agentsync.
func (e *Env) Init(t *testing.T) {
	t.Helper()
	out, errOut, code := e.Agentsync(t, "--no-input", "init")
	if code != 0 {
		t.Fatalf("agentsync init exit %d\nstdout: %s\nstderr: %s", code, out, errOut)
	}
}
