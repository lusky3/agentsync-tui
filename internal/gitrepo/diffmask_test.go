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
		"+name = 'notsecret'":                            "notsecret",
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
	})
}
