package testenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// No test here reads the real home: hermeticity is proven by the path
// assertions below, not by snapshotting ~/.agentsync.

func under(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && !strings.HasPrefix(rel, "..") && rel != "."
}

func TestEnvPathsAreUnderTempDir(t *testing.T) {
	realHome, _ := os.UserHomeDir()
	env := New(t)
	root := env.Root
	if owner := AgentsyncAncestor(root); owner != "" {
		t.Errorf("Root %q sits under %s/.agentsync", root, owner)
	}
	for name, p := range map[string]string{"Home": env.Home, "AgentsyncHome": env.AgentsyncHome} {
		if !under(root, p) {
			t.Errorf("%s %q not under test temp root %q", name, p, root)
		}
		if p == realHome || under(realHome, p) && !under(root, p) {
			t.Errorf("%s %q points into the real home", name, p)
		}
	}
	got := map[string]string{}
	for _, kv := range env.Env {
		k, v, _ := strings.Cut(kv, "=")
		got[k] = v
	}
	for _, k := range []string{"HOME", "AGENTSYNC_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		v, ok := got[k]
		if !ok || !under(root, v) {
			t.Errorf("%s=%q not overridden to a temp path", k, v)
		}
	}
	if got["HOME"] != env.Home || got["AGENTSYNC_HOME"] != env.AgentsyncHome {
		t.Errorf("Env slice disagrees with fields: %v", got)
	}
}

func TestInitDoesNotTouchRealHome(t *testing.T) {
	if _, err := exec.LookPath("agentsync"); err != nil {
		t.Skip("agentsync not on PATH")
	}
	env := New(t)
	env.Init(t)
	realHome, _ := os.UserHomeDir()
	if !under(env.Root, env.AgentsyncHome) || under(realHome, env.AgentsyncHome) && !under(env.Root, env.AgentsyncHome) {
		t.Fatalf("AgentsyncHome %q is not a temp path", env.AgentsyncHome)
	}
	entries, err := os.ReadDir(env.AgentsyncHome)
	if err != nil || len(entries) == 0 {
		t.Errorf("init created nothing under %s (err=%v)", env.AgentsyncHome, err)
	}
}

// Every AGENTSYNC_* knob the CLI honours (AGENTSYNC_TARGET_ROOT redirects
// the whole tree and outranks AGENTSYNC_HOME; the AGENTSYNC_ALLOW_* and
// AGENTSYNC_LOCK_TIMEOUT_MS switches change behaviour) and the third-party
// agent home override GROK_HOME are scrubbed from the child environment, so
// a developer's exported knobs cannot steer a "hermetic" run elsewhere.
func TestEnvScrubsCLIKnobs(t *testing.T) {
	t.Setenv("AGENTSYNC_TARGET_ROOT", "/elsewhere")
	t.Setenv("AGENTSYNC_ALLOW_SYMLINK_DEST", "1")
	t.Setenv("AGENTSYNC_LOCK_TIMEOUT_MS", "1")
	t.Setenv("GROK_HOME", "/elsewhere/grok")
	t.Setenv("NO_COLOR", "1")
	env := New(t)
	for _, kv := range env.Env {
		k, v, _ := strings.Cut(kv, "=")
		switch {
		case k == "AGENTSYNC_HOME":
			if v != env.AgentsyncHome {
				t.Errorf("AGENTSYNC_HOME=%q, want %q", v, env.AgentsyncHome)
			}
		case strings.HasPrefix(k, "AGENTSYNC_"), k == "GROK_HOME", k == "NO_COLOR":
			t.Errorf("inherited %s=%q", k, v)
		}
	}
}

// agentsync walks up from its working directory looking for a project
// tree (.agentsync/) and refuses a command that names no scope when it
// finds one. A TMPDIR below a directory holding .agentsync (a developer's
// TMPDIR under a real ~ with ~/.agentsync) must not let any agentsync child
// see that tree: the hermetic root moves elsewhere and the CLI stays on the
// fake home.
func TestTMPDIRBelowAnAgentsyncTreeIsAvoided(t *testing.T) {
	x := outerTree(t)
	work := filepath.Join(x, "work")
	t.Setenv("TMPDIR", work)
	if got := AgentsyncAncestor(work); got != x && got != mustEval(t, x) {
		t.Fatalf("AgentsyncAncestor(%q) = %q, want %q", work, got, x)
	}
	// A subtest: its t.TempDir and New's root are made after TMPDIR changed.
	t.Run("hermetic", func(t *testing.T) {
		env := New(t)
		wantOutside(t, x, env)
		if _, err := exec.LookPath("agentsync"); err != nil {
			t.Skip("agentsync not on PATH")
		}
		env.Init(t)
		// No --scope: agentsync decides from its working directory (the fake home).
		if _, se, code := env.Agentsync(t, "--no-input", "mcp", "add", "regress", "--command", "echo"); code != 0 {
			t.Fatalf("mcp add without a scope exit %d: %s", code, se)
		}
		if _, err := os.Stat(filepath.Join(env.AgentsyncHome, "mcp", "regress.toml")); err != nil {
			t.Errorf("the server was not written to the fake home: %v", err)
		}
		if entries, _ := os.ReadDir(filepath.Join(x, ".agentsync", "mcp")); len(entries) != 0 {
			t.Errorf("the outer tree was written: %v", entries)
		}
	})
}

// outerTree makes X/.agentsync/mcp and X/work in a new temp dir X.
func outerTree(t *testing.T) string {
	t.Helper()
	x, err := os.MkdirTemp("", "outer-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(x) })
	for _, d := range []string{filepath.Join(x, ".agentsync", "mcp"), filepath.Join(x, "work")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return x
}

// wantOutside asserts none of env's paths lies under x and the fake home
// has no .agentsync ancestor.
func wantOutside(t *testing.T, x string, env *Env) {
	t.Helper()
	for name, p := range map[string]string{"Root": env.Root, "Home": env.Home, "AgentsyncHome": env.AgentsyncHome} {
		if under(x, p) || under(mustEval(t, x), mustEval(t, p)) {
			t.Errorf("%s %q is under %s, which holds .agentsync", name, p, x)
		}
	}
	if owner := AgentsyncAncestor(env.Home); owner != "" {
		t.Errorf("fake home %q sits under %s/.agentsync", env.Home, owner)
	}
}

func mustEval(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return p
	}
	return r
}
