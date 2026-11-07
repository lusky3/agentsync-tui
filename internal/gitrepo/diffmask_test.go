package gitrepo

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/lusky3/agentsync-tui/internal/mask"
)

func TestMaskPatchLineConfigValues(t *testing.T) {
	same := []string{
		"+command = 'npx'",
		"+T = '${env:T}'",
		"-TOKEN = \"${secret:gh.token}\"",
		" type = 'stdio'",
		"+enabled = true",
		"+description = 'GitHub tools'",
		"+[github.env]",
		"+name = 'notsecret'",
		"+",
		" ",
	}
	for _, l := range same {
		if got := MaskPatchLine(scanMCP, l); got != l {
			t.Errorf("%q -> %q, want unchanged", l, got)
		}
	}
	masked := map[string]string{
		"+X-Api-Key = 'literal'":                         "literal",
		"-UPSTREAM_ID = \"" + opaqueTok + "\"":           opaqueTok,
		" env = { A = 'plain', B = '${B}' }":             "plain",
		"+args = ['--token', 'abc123']":                  "abc123",
		"+url = 'https://h.example/x?token=s3cr3tval'":   "s3cr3tval",
		"+url = 'https://u:pw0rd@h.example/x'":           "pw0rd",
		"+headers.Cookie = 'sid=abcdef'":                 "abcdef",
		"+'quoted key' = 'value'":                        "value",
		"+API_KEY = 'abc' # trailing comment":            "abc",
		"+PASSWORD =  'abc'":                             "abc",
		"+env.GH = '" + ghpTok + "'":                     ghpTok,
		"+X = '${" + ghpTok + "}'":                       ghpTok,
		"+" + ghpTok + " = 1":                            ghpTok,
		"+args = [\"run\", \"" + opaqueTok + "\"]":       opaqueTok,
		"+inline = { url = 'https://h/x?k=v', T = 'q' }": "'q'",
	}
	for l, raw := range masked {
		got := MaskPatchLine(scanMCP, l)
		if strings.Contains(got, raw) || !strings.Contains(got, mask.Mask) || got[0] != l[0] {
			t.Errorf("%q -> %q", l, got)
		}
	}
	// The key and the reference stay readable next to a masked literal.
	if got := MaskPatchLine(scanMCP, " env = { A = 'plain', B = '${B}' }"); !strings.Contains(got, "B = '${B}'") || !strings.Contains(got, "A = ") {
		t.Errorf("inline table: %q", got)
	}
	// Outside the config files a line is only scrubbed with mask.Text.
	if got := MaskPatchLine(scanSkill, "+name = 'notsecret'"); got != "+name = 'notsecret'" {
		t.Errorf("prose: %q", got)
	}
	if got := MaskPatchLine(scanSkill, "+API_KEY = 'abc'"); strings.Contains(got, "abc") {
		t.Errorf("prose secret: %q", got)
	}
}

func TestMaskPatchLineStripsEscapes(t *testing.T) {
	for _, path := range []string{scanSkill, scanMCP} {
		for _, l := range []string{
			"+\x1b]0;title\x07hello\x1b[31m red\x1b[0m",
			"-a = '\x1b[2Jx'",
			"diff --git a/\x1b[31mx b/x",
			"+ok\r",
			"\\ No newline at end of file\x1b[0m",
		} {
			got := MaskPatchLine(path, l)
			if strings.ContainsAny(got, "\x1b\x07\r") || got == "" {
				t.Errorf("%s %q -> %q", path, l, got)
			}
		}
	}
	if got := MaskPatchLine(scanSkill, "+\x1b]0;title\x07hello"); got != "+hello" {
		t.Errorf("got %q", got)
	}
}

func TestMaskPatchLineHeaderTokens(t *testing.T) {
	for _, path := range []string{scanSkill, scanMCP, "skills/" + ghpTok + "/x"} {
		for _, l := range []string{
			diffHeadTok + "skills/" + ghpTok + "/x b/skills/" + ghpTok + "/x",
			"--- a/skills/" + ghpTok + "/x",
			"+++ b/skills/" + ghpTok + "/x",
			"+++ " + ghpTok,
			"@@ -1 +1 @@ func " + ghpTok,
			"index 1..2 100644",
			"rename from skills/" + ghpTok,
		} {
			if got := MaskPatchLine(path, l); strings.Contains(got, ghpTok) {
				t.Errorf("%s %q -> %q", path, l, got)
			}
		}
	}
}

// The maintainer's mcp files use shell-style ${VAR} references and
// Bearer ${VAR} headers; these synthetic lines have the same shape.
func TestMaskPatchLineMaintainerSample(t *testing.T) {
	sample := []string{
		"+[github]",
		"+command = 'npx'",
		"+[github.env]",
		"+GITHUB_PERSONAL_ACCESS_TOKEN = '${GITHUB_TOKEN}'",
		"+[context7]",
		"+url = 'https://mcp.example.com/mcp'",
		"+[context7.headers]",
		"+Authorization = 'Bearer ${CONTEXT7_API_KEY}'",
		"+CONTEXT7_API_KEY = \"${CONTEXT7_API_KEY}\"",
		" BRAVE_API_KEY = '${BRAVE_API_KEY}'",
		"-env = { SLACK_BOT_TOKEN = '${SLACK_BOT_TOKEN}', SLACK_TEAM_ID = '${SLACK_TEAM_ID}' }",
		"+headers = { Authorization = 'Bearer ${GITHUB_TOKEN}' }",
	}
	for _, l := range sample {
		if got := MaskPatchLine("mcp/servers.toml", l); got != l {
			t.Errorf("%q -> %q, want unchanged", l, got)
		}
		if fs := scanOne("mcp/servers.toml", l[1:]); len(fs) != 0 {
			t.Errorf("%q: finding %+v", l, fs)
		}
	}
}

func FuzzMaskPatchLine(f *testing.F) {
	f.Add(scanMCP, "+X = '", uint(6))
	f.Add(scanMCP, "+env = { A = '${B}' }", uint(14))
	f.Add(scanSkill, "+++ b/x", uint(4))
	f.Add(scanSkill, diffHeadTok+"x b/x", uint(13))
	f.Add(scanMCP, "+Authorization = 'Bearer '", uint(25))
	f.Add(scanMCP, "+args = ['\x1b[31m', '']", uint(11))
	f.Add("agentsync.toml", " K = \"${secret:", uint(15))
	f.Add(scanMCP, "+name = { a = 'x' }}, Authorization = '"+hunter+"' }", uint(0))
	// A header name glued to a preceding character (a fuzz crash).
	f.Add("mCp/0", " name={0Authorization = '"+hunter+"'}", uint(65))
	f.Fuzz(func(t *testing.T, path, line string, at uint) {
		pos := int(at % uint(len(line)+1))
		planted := line[:pos] + ghpTok + line[pos:]
		for _, in := range []string{line, planted} {
			got := MaskPatchLine(path, in)
			if strings.ContainsRune(got, 0x1b) || !utf8.ValidString(got) {
				t.Fatalf("MaskPatchLine(%q, %q) = %q", path, in, got)
			}
		}
		if got := MaskPatchLine(path, planted); strings.Contains(got, ghpTok) {
			t.Fatalf("token survived: MaskPatchLine(%q, %q) = %q", path, planted, got)
		}
		// The seeded header literal never survives a line that holds it as
		// an Authorization value, balanced or not.
		if strings.Contains(line, "Authorization = '"+hunter+"'") && IsConfigFile(path) && len(line) > 0 && strings.ContainsRune("+- ", rune(line[0])) {
			if got := MaskPatchLine(path, line); strings.Contains(got, hunter) {
				t.Fatalf("seeded secret survived: MaskPatchLine(%q, %q) = %q", path, line, got)
			}
		}
	})
}

// A config line that is not a pair (a multi-line string's content, an
// array element on its own line) masks every literal word; a comment masks
// its token-shaped words.
func TestMaskPatchLineMultiLineValue(t *testing.T) {
	masked := map[string]string{
		"+" + hunter:                            hunter,
		"+  'run',":                             "run",
		"-  \"" + opaqueTok + "\",":             opaqueTok,
		"+# id " + opaqueTok:                    opaqueTok,
		" password hunter3 \"\"\"":              "hunter3",
		"+    \"API_KEY\": \"" + hunter + "\",": hunter,
	}
	for _, path := range []string{scanMCP, scanJSON} {
		for l, raw := range masked {
			if got := MaskPatchLine(path, l); strings.Contains(got, raw) || !strings.Contains(got, mask.Mask) || got[0] != l[0] {
				t.Errorf("%s %q -> %q", path, l, got)
			}
		}
		for _, l := range []string{"+# a plain comment", "+[table.sub]", "+[[arr]]", "+}", "+  ],", "+  '${X}',", "+", `+"""`, `+  "command": "npx",`, `+  "env": { "T": "${T}" },`} {
			if got := MaskPatchLine(path, l); got != l {
				t.Errorf("%s %q -> %q, want unchanged", path, l, got)
			}
		}
	}
	if got := MaskPatchLine(scanJSON, `+    "API_KEY": "`+hunter+`",`); got != `+    "API_KEY": "`+mask.Mask+`",` {
		t.Errorf("json pair: %q", got)
	}
}

// args go through mask.Args (only secret-bearing and token-shaped elements
// are masked, an exempt digest stays) and structural keys are shown.
func TestMaskPatchLineArgsReadable(t *testing.T) {
	for _, l := range []string{
		"+args = ['run','-i','--rm','mcp/brave-search@sha256:" + hex64 + "']",
		"+args = ['-e', 'GH=${GH}', '-y', '@scope/server']",
		`+  "args": ["run", "-i"],`,
		"+args = [ 'a' , 8080, true ]",
		"+path = '" + nixPath + "'",
		"+checksum = '" + hex64 + "'",
		"+version = '" + pseudoV + "'",
		"+rev = '" + hex40 + "'",
	} {
		if got := MaskPatchLine(scanMCP, l); got != l {
			t.Errorf("%q -> %q, want unchanged", l, got)
		}
	}
	for l, raw := range map[string]string{
		"+args = ['--id', '" + opaqueTok + "']": opaqueTok,
		"+args = ['--token', 'abc123']":         "abc123",
		"+args = ['--api-key=lit3ral']":         "lit3ral",
	} {
		got := MaskPatchLine(scanMCP, l)
		if strings.Contains(got, raw) || !strings.Contains(got, "args = ['--") {
			t.Errorf("%q -> %q", l, got)
		}
	}
}

// A visible key's inline table or array is masked pair by pair: only a
// plain value the scan would not flag is shown as is.
func TestMaskPatchLineVisibleKeyTables(t *testing.T) {
	for l, raw := range map[string]string{
		"+initialization_options = { headers = { Authorization = '" + hunter + "' } }": hunter,
		"+name = { Authorization = '" + hunter + "' }":                                 hunter,
		"+initialization_options = { secret_value = '" + opaqueTok + "' }":             opaqueTok,
		"+initialization_options = { x = '" + opaqueTok + "' }":                        opaqueTok,
		"+description = { token = '" + ghpTok + "' }":                                  ghpTok,
		"+description = { token = 'plainlit' }":                                        "plainlit",
		"+root_markers = [{ Authorization = '" + hunter + "' }]":                       hunter,
		"+model = '-----BEGIN RSA PRIVATE KEY-----'":                                   "BEGIN",
	} {
		got := MaskPatchLine(scanMCP, l)
		if strings.Contains(got, raw) || !strings.Contains(got, mask.Mask) {
			t.Errorf("%q -> %q", l, got)
		}
	}
	for _, l := range []string{
		"+name = 'my-server'",
		"+initialization_options = { foo = 'bar' }",
		"+initialization_options = { settings = { gopls = { staticcheck = true } } }",
		"+root_markers = ['go.mod', '.git']",
		"+Authorization = 'Bearer $GH_TOKEN'",
		"+headers = { Authorization = 'Bearer `${GH_TOKEN}`' }",
	} {
		if got := MaskPatchLine(scanMCP, l); got != l {
			t.Errorf("%q -> %q, want unchanged", l, got)
		}
	}
}

// Half-edited TOML with unbalanced braces or brackets is never shown as a
// table: the whole value is masked.
func TestMaskPatchLineUnbalancedTables(t *testing.T) {
	for _, l := range []string{
		"+name = { a = 'x' }}, Authorization = '" + hunter + "' }",
		"+name = { a = 'x' , Authorization = '" + hunter + "'",
		"+name = ] Authorization = '" + hunter + "'",
		"+name = [{ a = 'x' }]], { Authorization = '" + hunter + "' }]",
		"+env = { a = 'x' }}, B = '" + hunter + "' }",
		"+args = ['a'], '" + hunter + "']",
		"+args = ['a', '" + hunter + "]",
		"+initialization_options = { a = '}', b = '" + hunter + "' ",
	} {
		if got := MaskPatchLine(scanMCP, l); strings.Contains(got, hunter) {
			t.Errorf("%q -> %q", l, got)
		}
	}
	for _, l := range []string{
		"+initialization_options = { a = '}{', b = ']' }",
		"+root_markers = ['[', ']']",
		"+args = ['run', '{x}']",
	} {
		if got := MaskPatchLine(scanMCP, l); got != l {
			t.Errorf("%q -> %q, want unchanged", l, got)
		}
	}
}

// Header pairs and triple-quoted secrets are masked in a prose file's
// diff as in a config file's, and references stay visible.
func TestMaskPatchLineHeaderPairsInProse(t *testing.T) {
	for _, path := range []string{scanSkill, scanMCP} {
		for _, l := range []string{
			`+Cookie = "session=hunter2abc"`,
			`+Authorization = "Basic hunter2abc"`,
			`+Proxy-Authorization = "Token hunter2abc"`,
			`+Set-Cookie = "id=hunter2abc"`,
			`+API_KEY = """hunter2abc"""`,
			`+API_KEY = '''hunter2abc'''`,
			`+API_KEY = """hunter2abc`,
			`+"X-Custom-Api-Key" = "hunter2abc"`,
			` name={0Authorization = 'hunter2abc'}`,
			`+x_Cookie = "id=hunter2abc"`,
		} {
			if got := MaskPatchLine(path, l); strings.Contains(got, "hunter2abc") {
				t.Errorf("%s %q -> %q", path, l, got)
			}
		}
		for _, l := range []string{
			`+Authorization = "Bearer ${secret:x}"`,
			`+Authorization = "Bearer ${X}"`,
			`+Authorization = "Bearer $GH_TOKEN"`,
		} {
			if got := MaskPatchLine(path, l); got != l {
				t.Errorf("%s %q -> %q, want unchanged", path, l, got)
			}
		}
	}
}

// An env or headers table holds values, whatever their keys are called:
// a nested key that is visible elsewhere (command, url, name) does not
// show its value there (CodeRabbit, PR #4).
func TestMaskPatchLineEnvTableHidesVisibleKeys(t *testing.T) {
	for _, l := range []string{
		"+env = { command = '" + hunter + "' }",
		"+headers = { url = '" + hunter + "' }",
		"+environment = { name = '" + hunter + "', path = '" + hunter + "' }",
		"+x = { env = { command = '" + hunter + "' } }",
		"+initialization_options = { env = { command = '" + hunter + "' } }",
		"+initialization_options = { headers = { args = ['" + hunter + "'] } }",
		"+env.command = '" + hunter + "'",
		"+server.env.path = '" + hunter + "'",
		"+\"headers\".url = '" + hunter + "'",
		"+env.args = ['" + hunter + "']",
		"+initialization_options = { env.command = '" + hunter + "' }",
		"+initialization_options = { env.args = ['" + hunter + "'] }",
	} {
		if got := MaskPatchLine(scanMCP, l); strings.Contains(got, hunter) {
			t.Errorf("%q -> %q", l, got)
		}
	}
	for _, l := range []string{
		"+env = { command = '${env:CMD}' }",
		"+initialization_options = { env = { HOME = '$HOME' } }",
		"+env.command = '${env:CMD}'",
	} {
		if got := MaskPatchLine(scanMCP, l); got != l {
			t.Errorf("%q -> %q, want unchanged", l, got)
		}
	}
}
