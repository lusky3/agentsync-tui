// Package runenv names the environment variables a discovery source may
// never set for a server: those that steer the runtime or the loader
// itself (npx, uvx, docker, the dynamic linker, proxies, CA bundles, git)
// rather than the server. A fixed value for one (DOCKER_HOST,
// NPM_CONFIG_REGISTRY, PIP_INDEX_URL, LD_PRELOAD, PATH) would change what
// is fetched or run, so a source refuses the install option instead.
//
// It is a leaf package (stdlib only) so both mcpreg and dockercat can use
// it without importing each other.
package runenv

import "strings"

// prefixes are matched case-insensitively at the start of a name.
var prefixes = []string{"NPM_CONFIG_", "UV_", "PIP_", "PYTHON", "DOCKER_", "LD_", "DYLD_", "SSL_CERT_", "GIT_"}

// exact are matched case-insensitively against the whole name.
var exact = map[string]bool{
	"NODE_OPTIONS": true, "NODE_PATH": true, "PATH": true, "HOME": true,
	"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true, "ALL_PROXY": true,
	"REQUESTS_CA_BUNDLE": true, "NODE_EXTRA_CA_CERTS": true, "CURL_CA_BUNDLE": true,
}

// Reserved reports whether name is a runtime or loader variable, ignoring
// case (Windows reads environment names case-insensitively, npm reads
// npm_config_* and the proxy variables are honoured in either case).
func Reserved(name string) bool {
	u := strings.ToUpper(name)
	if exact[u] {
		return true
	}
	for _, p := range prefixes {
		if strings.HasPrefix(u, p) {
			return true
		}
	}
	return false
}

// maxShown bounds the name quoted in Reason.
const maxShown = 32

// Reason is the refusal reason for an install option that sets name. name
// must already be a valid environment variable name (plain ASCII); a long
// one is shortened.
func Reason(name string) string {
	if len(name) > maxShown {
		name = name[:maxShown] + "…"
	}
	return "sets " + name + ", which changes how the runtime itself behaves"
}
