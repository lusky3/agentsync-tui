package testenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// realSnapshot lists entry names under the real ~/.agentsync, or nil+false
// when it does not exist.
func realSnapshot(t *testing.T) ([]string, bool) {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(home, ".agentsync"))
	if os.IsNotExist(err) {
		return nil, false
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names, true
}

func sameSnapshot(a []string, aok bool, b []string, bok bool) bool {
	return aok == bok && strings.Join(a, "\x00") == strings.Join(b, "\x00")
}

func under(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && !strings.HasPrefix(rel, "..") && rel != "."
}

func TestEnvPathsAreUnderTempDir(t *testing.T) {
	before, beforeOK := realSnapshot(t)
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
	after, afterOK := realSnapshot(t)
	if !sameSnapshot(before, beforeOK, after, afterOK) {
		t.Errorf("real ~/.agentsync changed: %v -> %v", before, after)
	}
}

func TestInitDoesNotTouchRealHome(t *testing.T) {
	if _, err := exec.LookPath("agentsync"); err != nil {
		t.Skip("agentsync not on PATH")
	}
	before, beforeOK := realSnapshot(t)
	env := New(t)
	env.Init(t)
	after, afterOK := realSnapshot(t)
	if !sameSnapshot(before, beforeOK, after, afterOK) {
		t.Errorf("real ~/.agentsync changed: %v -> %v", before, after)
	}
	entries, err := os.ReadDir(env.AgentsyncHome)
	if err != nil || len(entries) == 0 {
		t.Errorf("init created nothing under %s (err=%v)", env.AgentsyncHome, err)
	}
}
