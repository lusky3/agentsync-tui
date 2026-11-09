package e2e

import (
	"encoding/pem"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lusky3/agentsync-tui/internal/gitrepo/gittest"
	"github.com/lusky3/agentsync-tui/internal/testenv"
)

// httpsGitServer serves the bare repositories under root over HTTPS with
// git http-backend (smart HTTP, read-only), and returns the server's base
// URL and a CA file that trusts it (for GIT_SSL_CAINFO). Everything stays
// on the loopback interface.
func httpsGitServer(t *testing.T, root string) (base, caFile string) {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	srv := httptest.NewTLSServer(&cgi.Handler{
		Path: gitBin,
		Args: []string{"http-backend"},
		Env:  []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"},
	})
	t.Cleanup(srv.Close)
	caFile = filepath.Join(t.TempDir(), "ca.pem")
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(caFile, ca, 0o600); err != nil {
		t.Fatal(err)
	}
	return srv.URL, caFile
}

// setEnv replaces (or adds) k in env.Env.
func setEnv(env *testenv.Env, k, v string) {
	out := env.Env[:0]
	for _, kv := range env.Env {
		if name, _, _ := strings.Cut(kv, "="); name != k {
			out = append(out, kv)
		}
	}
	env.Env = append(out, k+"="+v)
}

// TestSourceRepoBootstrapClonesIntoConfiguredHome drives the bootstrap
// (i, a URL, the confirm) with $AGENTSYNC_HOME set to a directory that is
// not $HOME/.agentsync, against a real agentsync and a source repository
// served over HTTPS on the loopback interface. The clone lands in the
// configured home with the source's history and files, the default home
// stays absent, and the tab then shows the cloned repository.
func TestSourceRepoBootstrapClonesIntoConfiguredHome(t *testing.T) {
	src := newSourceRepoEnv(t)
	src.Init(t)
	gittest.Git(t, src.AgentsyncHome, "init", "-q", "-b", "main")
	gittest.Git(t, src.AgentsyncHome, "add", "-A")
	gittest.Git(t, src.AgentsyncHome, "commit", "-q", "--message=source")
	served := t.TempDir()
	gittest.Git(t, "", "clone", "-q", "--bare", src.AgentsyncHome, filepath.Join(served, "agent-config.git"))
	base, caFile := httpsGitServer(t, served)
	srcHead := strings.TrimSpace(gittest.Git(t, src.AgentsyncHome, "rev-parse", "HEAD"))
	srcConfig, err := os.ReadFile(filepath.Join(src.AgentsyncHome, "agentsync.toml"))
	if err != nil {
		t.Fatal(err)
	}

	env := newSourceRepoEnv(t)
	env.AgentsyncHome = filepath.Join(env.Root, "elsewhere", "agent-config")
	setEnv(env, "AGENTSYNC_HOME", env.AgentsyncHome)
	setEnv(env, "GIT_SSL_CAINFO", caFile)
	setEnv(env, "SSL_CERT_FILE", caFile)
	setEnv(env, "GIT_CONFIG_NOSYSTEM", "1")
	setEnv(env, "GIT_CONFIG_GLOBAL", os.DevNull)

	h := openTab(t, env, "the bootstrap prompt", func(f string) bool { return strings.Contains(f, "No agentsync home at") })
	h.key("i")
	h.wait("the URL field", func(f string) bool { return strings.Contains(f, "URL") })
	h.key(base + "/agent-config.git")
	h.key("enter")
	h.wait("the clone modal", func(f string) bool { return strings.Contains(f, "Run it?") && strings.Contains(f, "init") })
	h.key("y")
	h.wait("the cloned repository", func(f string) bool { return strings.Contains(f, "repo: ") })

	if got := strings.TrimSpace(gittest.Git(t, env.AgentsyncHome, "rev-parse", "HEAD")); got != srcHead {
		t.Errorf("cloned HEAD %s, want the source's %s", got, srcHead)
	}
	if b, err := os.ReadFile(filepath.Join(env.AgentsyncHome, "agentsync.toml")); err != nil || string(b) != string(srcConfig) {
		t.Errorf("cloned agentsync.toml = %q, %v; want the source's", b, err)
	}
	if _, err := os.Lstat(filepath.Join(env.Home, ".agentsync")); !os.IsNotExist(err) {
		t.Errorf("the default home was created (lstat: %v)", err)
	}
}
