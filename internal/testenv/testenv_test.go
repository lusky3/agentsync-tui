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
	tmp := t.TempDir()
	// New's own TempDir is a sibling inside the same per-test root.
	env := New(t)
	root := filepath.Dir(tmp)
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
	if !under(filepath.Dir(t.TempDir()), env.AgentsyncHome) || under(realHome, env.AgentsyncHome) && !under(filepath.Dir(t.TempDir()), env.AgentsyncHome) {
		t.Fatalf("AgentsyncHome %q is not a temp path", env.AgentsyncHome)
	}
	entries, err := os.ReadDir(env.AgentsyncHome)
	if err != nil || len(entries) == 0 {
		t.Errorf("init created nothing under %s (err=%v)", env.AgentsyncHome, err)
	}
}
