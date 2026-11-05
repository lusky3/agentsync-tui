// Package testenv provides a hermetic environment for tests that drive the
// real agentsync binary: HOME, AGENTSYNC_HOME and XDG_* all point at
// t.TempDir(), and every knob the CLI reads from the environment that could
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
	Home          string   // fake $HOME
	AgentsyncHome string   // fake $AGENTSYNC_HOME (the canonical source tree)
	Env           []string // os.Environ() with the hermetic overrides applied
}

// New creates a hermetic environment rooted in t.TempDir().
func New(t *testing.T) *Env {
	t.Helper()
	root := t.TempDir()
	e := &Env{
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
