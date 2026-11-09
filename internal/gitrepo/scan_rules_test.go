package gitrepo

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/lusky3/agentsync-tui/internal/mask"
)

const (
	scanJSON = "mcp/a.json"
	sri256   = "sha256-47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU="
	sri512   = "sha512-z4PhNX7vuL3xVChQ1m2AB9Yg5AULVxXcg/SpIdNs6c5H0NE8XYXysP+DGNKHfuwvY7kxvUdBeoGlODJ6+SfaPg=="
	nixPath  = "/nix/store/0c4kx6i2d4hbq3ab2xz9fr8dvrl0w0dn-gopls-0.16.1/bin/gopls"
	pseudoV  = "v0.0.0-20241015123456-abcdef123456"
	hunter   = "hunter2"
)

// basicAuth is a synthetic Basic credential, encoded at run time so the
// source holds no literal a repository secret scanner reports.
var basicAuth = "Basic " + base64.StdEncoding.EncodeToString([]byte("admin:"+hunter))

// wantNotHard asserts at most one finding, and that it is soft.
func wantNotHard(t *testing.T, path, text string) {
	t.Helper()
	if fs := scanOne(path, text); len(fs) > 1 || len(fs) == 1 && fs[0].Hard {
		t.Fatalf("%s %s: want soft or none, got %+v", path, mask.Text(text), fs)
	}
}

// An Authorization-style header written as a TOML or JSON pair is hard
// everywhere, unless its value is only references.
func TestAuthHeaderPairHard(t *testing.T) {
	probes := []string{
		"Authorization = '" + basicAuth + "'",
		"Authorization = 'MySecretPassw0rd'",
		"Proxy-Authorization = '" + hunter + "'",
		"Cookie = 'session=s%3Aabc.def'",
		"Set-Cookie = \"sid=abc\"",
		"headers = { Authorization = '" + hunter + "' }",
		"headers = { Accept = 'json', \"X-Api-Key\" = 'x' }",
		"headers = { a = 'x' }}, Authorization = '" + hunter + "' }",
		"headers = { a = '}', Authorization = '" + hunter + "'",
		"x = [{ a = 1 }]], { Authorization = '" + hunter + "' }]",
		"X-Api-Key = 'x'",
		"X-Goog-Api-Key = 'x'",
		"authorization = 'Token abc'",
		`"Authorization": "` + hunter + `",`,
	}
	for _, path := range []string{scanMCP, scanSkill, scanJSON} {
		for _, p := range probes {
			if f := wantFinding(t, path, p, true); f.Kind != mask.KindAuthHeader {
				t.Errorf("%s %q: kind %q", path, p, f.Kind)
			}
		}
		for _, p := range []string{
			"Authorization = 'Bearer ${secret:a.t}'",
			"Authorization = 'Bearer ${X}'",
			"Authorization = 'Bearer $GITHUB_TOKEN'",
			"headers = { Authorization = 'Basic ${U}:${P}' }",
			"Authorization = ''",
			`"Authorization": "Bearer ${env:T}"`,
		} {
			wantNone(t, path, p)
		}
	}
}

func TestTripleQuotedSecretHard(t *testing.T) {
	wantFinding(t, scanMCP, "API_KEY = '''"+hunter+"'''", true)
	wantFinding(t, scanMCP, `API_KEY = """`+hunter+`"""`, true)
	wantNone(t, scanMCP, "API_KEY = '''${secret:a.k}'''")
	for _, opener := range []string{`API_KEY = """`, "API_KEY = '''", `token = """first`, "Authorization = '''"} {
		if f := wantFinding(t, scanMCP, opener, true); f.Kind != KindMultiLineSecret {
			t.Errorf("%q: kind %q", opener, f.Kind)
		}
	}
	// In prose an opener after a secret-named key is soft, as a
	// secret-named assignment is.
	wantFinding(t, scanSkill, `API_KEY = """`, false)
	wantNone(t, scanMCP, `description = """`)
	wantNone(t, scanMCP, `API_KEY = """${secret:a.k}"""`)
}

func TestJSONConfigPairs(t *testing.T) {
	wantFinding(t, scanJSON, `"UPSTREAM": "`+opaqueTok+`",`, true)
	wantFinding(t, scanJSON, `  "API_KEY": "`+hunter+`"`, true)
	wantNone(t, scanJSON, `  "command": "npx",`)
	wantNone(t, scanJSON, `  "env": { "T": "${T}" },`)
	wantNotHard(t, scanJSON, `  "args": ["--id", "`+opaqueTok+`"],`)
}

func TestIntegrityExemptNeedsProperBody(t *testing.T) {
	wantNone(t, scanMCP, "integrity = '"+sri256+"'")
	wantNone(t, scanMCP, "integrity = '"+sri512+"'")
	wantNone(t, scanMCP, "image = 'x@sha256:"+hex64+"'")
	wantFinding(t, scanMCP, "UPSTREAM = 'sha256-"+opaqueTok+"'", true)
	wantFinding(t, scanMCP, "UPSTREAM = 'sha512-"+opaqueTok+opaqueTok+"'", true)
	wantFinding(t, scanMCP, "UPSTREAM = 'sha256:"+hex64+"0"+opaqueTok+"'", true)
}

func TestLongLineNotScanned(t *testing.T) {
	long := strings.Repeat("a", 2<<20) + " " + ghpTok
	start := time.Now()
	fs := ScanLines(scanMCP, []Line{{N: 3, Text: long}})
	got := MaskPatchLine(scanMCP, "+"+long)
	if d := time.Since(start); d > 200*time.Millisecond {
		t.Errorf("took %v", d)
	}
	if len(fs) != 1 || fs[0].Hard || fs[0].Kind != KindLineTooLong || fs[0].Line != 3 || fs[0].Shown != "" {
		t.Fatalf("findings %+v", fs)
	}
	if len(got) > 200 || !strings.HasPrefix(got, "+"+mask.Mask) || strings.Contains(got, "aaaa") {
		t.Fatalf("MaskPatchLine = %q", got)
	}
	if got := MaskPatchLine(scanSkill, "\x1b"+long); strings.ContainsRune(got, 0x1b) || len(got) > 200 {
		t.Fatalf("header-like: %q", got)
	}
}

// Structural keys whose values are paths, versions or hashes are soft:
// only secret-named keys and env or header values are hard.
func TestStructuralKeysSoft(t *testing.T) {
	wantNotHard(t, "lsp/l.toml", "path = '"+nixPath+"'")
	wantNotHard(t, "hooks/h.toml", "checksum = '"+hex64+"'")
	wantNotHard(t, scanMCP, "version = '"+pseudoV+"'")
	wantNotHard(t, scanMCP, "rev = '"+hex40+"'")
	wantNotHard(t, scanMCP, "sha = '"+hex40+"'")
	wantNotHard(t, scanMCP, "model = '"+opaqueTok+"'")
	wantNotHard(t, "lsp/l.toml", "root_markers = ['"+opaqueTok+"']")
	wantNotHard(t, "lsp/l.toml", "initialization_options = { x = '"+opaqueTok+"' }")
	wantNotHard(t, "hooks/h.toml", "matcher = '"+opaqueTok+"'")
	wantNotHard(t, "agentsync.toml", "author_email = '"+opaqueTok+"@users.noreply.github.com'")
	wantNotHard(t, scanMCP, "name = '"+opaqueTok+"'")
	wantNotHard(t, scanMCP, "cwd = '/w/"+opaqueTok+"'")
	// Secret-named keys stay hard whatever the table.
	wantFinding(t, scanMCP, "initialization_options = { token = 'x' }", true)
}

// A token-shaped word in an env or headers table nested under a
// structural key is an env or header value: hard, while other nested
// values under that key stay soft (Ruling 21).
func TestNestedEnvValueHard(t *testing.T) {
	wantFinding(t, "lsp/l.toml", "initialization_options = { env = { FOO = '"+opaqueTok+"' } }", true)
	wantFinding(t, "lsp/l.toml", "initialization_options = { headers = { X-Trace = '"+opaqueTok+"' } }", true)
	wantFinding(t, scanMCP, "name = { environment = { path = '"+opaqueTok+"' } }", true)
	wantNotHard(t, "lsp/l.toml", "initialization_options = { settings = { x = '"+opaqueTok+"' } }")
	// A dotted key under an env or headers table is an env or header value,
	// whatever its last part.
	wantFinding(t, scanMCP, "env.command = '"+opaqueTok+"'", true)
	wantFinding(t, scanMCP, "server.env.path = '"+opaqueTok+"'", true)
	wantFinding(t, "lsp/l.toml", "initialization_options.headers.name = '"+opaqueTok+"'", true)
	wantFinding(t, "lsp/l.toml", "initialization_options = { env.name = '"+opaqueTok+"' }", true)
	wantNotHard(t, "lsp/l.toml", "initialization_options.settings.name = '"+opaqueTok+"'")
}
