package runenv_test

import (
	"strings"
	"testing"

	"github.com/lusky3/agentsync-tui/internal/discover/runenv"
)

func TestReserved(t *testing.T) {
	for _, n := range []string{
		"NPM_CONFIG_REGISTRY", "npm_config_registry", "Npm_Config_X", "NODE_OPTIONS", "node_path",
		"UV_INDEX_URL", "PIP_INDEX_URL", "pip_extra_index_url", "PYTHONPATH", "PYTHONSTARTUP", "python_x",
		"DOCKER_HOST", "docker_config", "PATH", "Path", "HOME", "LD_PRELOAD", "LD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES",
		"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy", "NO_PROXY", "no_proxy", "ALL_PROXY", "all_proxy",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "REQUESTS_CA_BUNDLE", "NODE_EXTRA_CA_CERTS", "CURL_CA_BUNDLE",
		"GIT_SSH_COMMAND", "GIT_CONFIG_GLOBAL",
	} {
		if !runenv.Reserved(n) {
			t.Errorf("Reserved(%q) = false, want true", n)
		}
	}
	for _, n := range []string{
		"API_KEY", "BRAVE_API_KEY", "MODE", "LOG_LEVEL", "NODE_ENV", "PATHS", "HOMEPAGE", "MY_PATH", "GITHUB_TOKEN",
		"DOCKERHUB_USER", "LDAP_URL", "PIPELINE", "UVX", "NPM_TOKEN", "SSL_MODE", "", "X",
	} {
		if runenv.Reserved(n) {
			t.Errorf("Reserved(%q) = true, want false", n)
		}
	}
}

func TestReason(t *testing.T) {
	if got, want := runenv.Reason("DOCKER_HOST"), "sets DOCKER_HOST, which changes how the runtime itself behaves"; got != want {
		t.Errorf("Reason = %q, want %q", got, want)
	}
}

func TestReasonShortensLongNames(t *testing.T) {
	long := "PIP_" + strings.Repeat("X", 100)
	if got := runenv.Reason(long); strings.Contains(got, long) || !strings.Contains(got, "…") {
		t.Errorf("Reason = %q, want the name shortened", got)
	}
}
