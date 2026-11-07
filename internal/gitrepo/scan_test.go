package gitrepo

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lusky3/agentsync-tui/internal/gitrepo/gittest"
	"github.com/lusky3/agentsync-tui/internal/mask"
)

const (
	scanSkill   = "skills/x/SKILL.md"
	scanMCP     = "mcp/a.toml"
	scanBrave   = "mcp/brave.toml"
	ghpTok      = "ghp_abcdefghijklmnopqrst"
	hex64       = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	hex40       = "587be6b4c3f93f93c489c0111bba5596147a26cb"
	someUUID    = "123e4567-e89b-12d3-a456-426614174000"
	opaqueTok   = "Zx9Kq2Lm7Pw4Rt8Vn3Bc6Hj5"
	diffHeadTok = "diff --git a/"
	emptySHA1   = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	emptySHA256 = "6ef19b41225c5369f1c104d45d8d85efa9b057b53b14b4b9b939dd74decc5321"
)

func scanOne(path, text string) []Finding {
	return ScanLines(path, []Line{{N: 7, Text: text}})
}

// wantFinding asserts exactly one finding of the given hardness.
func wantFinding(t *testing.T, path, text string, hard bool) Finding {
	t.Helper()
	fs := scanOne(path, text)
	if len(fs) != 1 {
		t.Fatalf("%s: %d findings, want 1 (hard=%v)", mask.Text(text), len(fs), hard)
	}
	if fs[0].Hard != hard || fs[0].Line != 7 || fs[0].Path != path {
		t.Fatalf("%s: got %+v, want hard=%v", mask.Text(text), fs[0], hard)
	}
	return fs[0]
}

func wantNone(t *testing.T, path, text string) {
	t.Helper()
	if fs := scanOne(path, text); len(fs) != 0 {
		t.Fatalf("%s: want no finding, got %+v", path, fs)
	}
}

func TestOpaqueTokIsTokenShaped(t *testing.T) {
	if !mask.TokenShaped(opaqueTok) || !mask.TokenShaped(hex64) || !mask.TokenShaped(someUUID) {
		t.Fatal("test fixtures must be TokenShaped")
	}
}

func TestAddedLinesNumbers(t *testing.T) {
	patch := diffHeadTok + "x b/x\nindex 1..2 100644\n--- a/x\n+++ b/x\n" +
		"@@ -1,2 +1,3 @@\n ctx\n+one\n ctx2\n" +
		"@@ -10 +11,2 @@\n-old\n+two\n+three\n"
	got := AddedLines(patch)
	want := []Line{{2, "one"}, {11, "two"}, {12, "three"}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestAddedLinesNoNewlineAndCRLF(t *testing.T) {
	patch := "--- a/x\r\n+++ b/x\r\n@@ -1 +1 @@\r\n-a\r\n\\ No newline at end of file\r\n+b\r\n\\ No newline at end of file\r\n" +
		"@@ -0,0 +5 @@\n+c\n"
	got := AddedLines(patch)
	want := []Line{{1, "b"}, {5, "c"}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

// Several files in one patch, a new file and a deletion.
func TestAddedLinesSeveralFiles(t *testing.T) {
	patch := diffHeadTok + "a b/a\nnew file mode 100644\n--- /dev/null\n+++ b/a\n@@ -0,0 +1,2 @@\n+a1\n+a2\n" +
		diffHeadTok + "b b/b\ndeleted file mode 100644\n--- a/b\n+++ /dev/null\n@@ -1 +0,0 @@\n-gone\n" +
		diffHeadTok + "c b/c\n--- a/c\n+++ b/c\n@@ -3,0 +4 @@\n+c4\n"
	got := AddedLines(patch)
	want := []Line{{1, "a1"}, {2, "a2"}, {4, "c4"}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestAddedLinesMalformedHeaderIgnored(t *testing.T) {
	for _, p := range []string{"@@ -1 +x @@\n+a\n", "@@ -1 +99999999999999999999 @@\n+a\n", "@@\n+a\n", "+++ b/x\n+a\n"} {
		if got := AddedLines(p); len(got) != 0 {
			t.Errorf("%q: got %+v", p, got)
		}
	}
}

func TestAddedLineStartingPlusPlusInsideHunk(t *testing.T) {
	patch := diffHeadTok + "x b/x\n--- a/x\n+++ b/x\n@@ -1,0 +1,2 @@\n+++ " + ghpTok + "\n+--- y\n"
	got := AddedLines(patch)
	want := []Line{{1, "++ " + ghpTok}, {2, "--- y"}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	fs := ScanLines(scanSkill, got)
	if len(fs) != 1 || !fs[0].Hard || fs[0].Line != 1 {
		t.Fatalf("findings %+v", fs)
	}
}

func TestHardEverywhereKinds(t *testing.T) {
	cases := map[string]string{
		"use " + ghpTok:                                mask.KindTokenShape,
		"github_pat_11ABCDEFG0123456789_abcd":          mask.KindTokenShape,
		"gg_pat_abcdefghijklmnop":                      mask.KindTokenShape,
		"key sk-abcdefghijklmnop":                      mask.KindTokenShape,
		"slack xoxb-1234-abcdef":                       mask.KindTokenShape,
		"aws AKIAABCDEFGHIJKLMNOP":                     mask.KindTokenShape,
		"jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig": mask.KindTokenShape,
		"Authorization: Bearer literalvalue":           mask.KindAuthHeader,
		"x-api-key: literalvalue":                      mask.KindAuthHeader,
		"Cookie: session=abc":                          mask.KindAuthHeader,
		"clone https://user:pw@example.com/r.git":      mask.KindURLUserinfo,
		"-----BEGIN OPENSSH PRIVATE KEY-----":          mask.KindPrivateKey,
		"AGE-SECRET-KEY-1QQQQQQQQQQQQQQQQQQQQQQQQQQQ":  mask.KindAgeIdentity,
	}
	for text, kind := range cases {
		if f := wantFinding(t, scanSkill, text, true); f.Kind != kind {
			t.Errorf("%s: kind %q, want %q", mask.Text(text), f.Kind, kind)
		}
	}
}

func TestConfigLiteralSecretHard(t *testing.T) {
	wantFinding(t, scanMCP, "API_KEY = 'abc123def456ghi789'", true)
	wantNone(t, scanMCP, "API_KEY = '${secret:a.api_key}'")
	wantFinding(t, "agentsync.toml", `password = "hunter2"`, true)
	wantFinding(t, "hooks/h.toml", `token = "abc"`, true)
	wantFinding(t, "lsp/l.toml", `client_secret = "abc"`, true)
}

func TestBearerRefNotAFinding(t *testing.T) {
	for _, l := range []string{
		"Authorization = 'Bearer ${GITHUB_TOKEN}'",
		"Authorization = 'Bearer ${secret:gh.token}'",
		"headers = { Authorization = \"Bearer ${env:T}\" }",
		"GITHUB_TOKEN = '${GITHUB_TOKEN}'",
	} {
		wantNone(t, scanMCP, l)
		wantNone(t, scanSkill, l)
	}
	wantNone(t, scanSkill, "Authorization: Bearer ${API_KEY}")
}

func TestProseTokenShapedIsSoft(t *testing.T) {
	f := wantFinding(t, scanSkill, "The checksum is "+hex64+".", false)
	if f.Kind != KindTokenShapedWord {
		t.Errorf("kind %q", f.Kind)
	}
	wantFinding(t, "memory/m.md", "id `"+opaqueTok+"`", false)
	wantNone(t, scanSkill, "Use GITHUB_TOKEN and MyServiceApiKey2024Prod in v0.1.0.")
}

func TestProsePasswordWordIsSoft(t *testing.T) {
	f := wantFinding(t, scanSkill, "password: hunter2", false)
	if f.Kind != mask.KindSecretAssignment {
		t.Errorf("kind %q", f.Kind)
	}
	wantNone(t, scanSkill, "Ask the user for their password.")
}

func TestUUIDUnderSecretKeyIsHard(t *testing.T) {
	wantFinding(t, scanMCP, "API_KEY = '"+someUUID+"'", true)
}

func TestUUIDElsewhereNotAFinding(t *testing.T) {
	wantNone(t, scanMCP, "project_id = '"+someUUID+"'")
	wantNone(t, scanMCP, "args = ['--project', '"+someUUID+"']")
}

func TestImageDigestInArgsNotAFinding(t *testing.T) {
	wantNone(t, scanBrave, "args = ['run','-i','--rm','mcp/brave-search@sha256:"+hex64+"']")
	wantNone(t, scanBrave, "  'docker.io/mcp/brave-search@sha256:"+hex64+"',")
	wantNone(t, scanBrave, "integrity = '"+sri512+"'")
}

func TestCommitShaInArgsNotAFinding(t *testing.T) {
	wantNone(t, scanMCP, "args = ['install', 'golang.org/x/tools/gopls@"+hex40+"']")
	wantNone(t, scanMCP, "args = ['--from', 'git+https://github.com/o/r@"+hex40+"', 'srv']")
}

func TestTokenShapedInArgsIsSoft(t *testing.T) {
	wantFinding(t, scanMCP, "args = ['--id', '"+opaqueTok+"']", false)
	wantFinding(t, scanMCP, "  '"+opaqueTok+"',", false)
	wantFinding(t, scanMCP, "url = 'https://h.example/"+opaqueTok+"/sse'", false)
}

func TestTokenShapedEnvValueIsHard(t *testing.T) {
	// The brief's shape: a secret-named key under [server.env].
	wantFinding(t, scanMCP, "TOKEN = '"+opaqueTok+"'", true)
	// A key that is not secret-named: hard because it is an env value.
	wantFinding(t, scanMCP, "UPSTREAM_ID = '"+opaqueTok+"'", true)
	wantFinding(t, scanMCP, "env = { UPSTREAM_ID = '"+opaqueTok+"' }", true)
	wantFinding(t, scanMCP, "X-Upstream = \""+opaqueTok+"\"", true)
	// A URL query value is hard too, whatever the key.
	wantFinding(t, scanMCP, "url = 'https://h.example/sse?code="+opaqueTok+"'", true)
	// References stay allowed.
	wantNone(t, scanMCP, "UPSTREAM_ID = '${UPSTREAM_ID}'")
}

func TestBinaryAndHugeNewFilesSoft(t *testing.T) {
	bin := append([]byte("ghp_"), 0, 1, 2)
	fs := ScanNewFile(scanSkill, bin)
	if len(fs) != 1 || fs[0].Hard || fs[0].Kind != KindNotScannedBinary || fs[0].Path != scanSkill {
		t.Fatalf("binary: %+v", fs)
	}
	huge := bytes.Repeat([]byte("a"), 1<<20+1)
	fs = ScanNewFile(scanSkill, huge)
	if len(fs) != 1 || fs[0].Hard || fs[0].Kind != KindNotScannedLarge {
		t.Fatalf("huge: %+v", fs)
	}
	// A NUL past the first 8 KiB does not make it binary.
	late := append(bytes.Repeat([]byte("a\n"), 5000), 0)
	if fs := ScanNewFile(scanSkill, late); len(fs) != 0 {
		t.Fatalf("late NUL: %+v", fs)
	}
}

func TestScanNewFileLines(t *testing.T) {
	content := []byte("first\r\nsecond " + ghpTok + "\r\nthird\nAPI_KEY = 'lit'\n")
	fs := ScanNewFile(scanMCP, content)
	if len(fs) != 2 || fs[0].Line != 2 || !fs[0].Hard || fs[1].Line != 4 || !fs[1].Hard {
		t.Fatalf("got %+v", fs)
	}
}

func TestFindingShownIsMasked(t *testing.T) {
	// One case per classify branch, then the header and triple-quoted
	// forms in both file kinds: Shown never holds the raw value.
	cases := []struct{ path, text, raw string }{
		{scanSkill, "token " + ghpTok, ghpTok},                              // hard credential: token shape
		{scanSkill, "see https://u:hunter2abc@example.com/x", "hunter2abc"}, // hard credential: URL userinfo
		{scanMCP, `API_KEY = """hunter2abc`, "hunter2abc"},                  // multi-line opener
		{scanSkill, "password: hunter2xyz", "hunter2xyz"},                   // secret assignment (soft in prose)
		{scanMCP, "API_KEY = 'abc123def456ghi789'", "abc123def456ghi789"},   // secret assignment (hard in config)
		{scanSkill, "the id is " + opaqueTok, opaqueTok},                    // prose token-shaped word
		{scanMCP, "UPSTREAM_ID = '" + opaqueTok + "'", opaqueTok},           // config: hard token-shaped word
		{scanMCP, "model = '" + opaqueTok + "'", opaqueTok},                 // config: soft token-shaped word
		{scanSkill, "\x1b[31mtoken " + ghpTok + "\x1b[0m", "\x1b"},
	}
	for _, path := range []string{scanMCP, scanSkill} {
		for _, text := range []string{ // header pairs and triple quotes
			`Cookie = "session=hunter2abc"`,
			`Authorization = "Basic hunter2abc"`,
			`Proxy-Authorization = "Token hunter2abc"`,
			`Set-Cookie = "id=hunter2abc"`,
			`API_KEY = """hunter2abc"""`,
		} {
			cases = append(cases, struct{ path, text, raw string }{path, text, "hunter2abc"})
		}
	}
	for _, c := range cases {
		fs := scanOne(c.path, c.text)
		if len(fs) != 1 {
			t.Fatalf("%q: %d findings", mask.Text(c.text), len(fs))
		}
		if strings.Contains(fs[0].Shown, c.raw) {
			t.Errorf("%s: Shown %q contains the raw value", c.path, fs[0].Shown)
		}
	}
	long := strings.Repeat("word ", 60) + ghpTok
	fs := scanOne(scanSkill, long)
	if len(fs) != 1 || len([]rune(fs[0].Shown)) > 120 || strings.Contains(fs[0].Shown, "ghp_") {
		t.Fatalf("long: %+v", fs)
	}
	// A credential in the file name is masked in Path too.
	if fs := scanOne("skills/"+ghpTok+"/x.md", "token "+ghpTok); len(fs) != 1 || strings.Contains(fs[0].Path, ghpTok) {
		t.Fatalf("path: %+v", fs)
	}
}

func TestIsConfigFile(t *testing.T) {
	for p, want := range map[string]bool{
		"agentsync.toml": true, "mcp/a.toml": true, "mcp/x/y.json": true, "hooks/h.toml": true,
		"lsp/l.toml": true, "hooks/x/h.toml": false, "hooks/h.sh": false, scanSkill: false,
		"memory/agentsync.toml": false, "MCP/a.toml": true,
	} {
		if got := IsConfigFile(p); got != want {
			t.Errorf("IsConfigFile(%q) = %v", p, got)
		}
	}
}

func TestDiffArgv(t *testing.T) {
	r := Repo{Top: "/h", ObjectFormat: "sha1"}
	want := []string{"-C", "/h", "diff", "--no-ext-diff", "--no-textconv", "--no-color", "-U3", "HEAD", "--", "skills/a.md"}
	if got := DiffArgv(r, false, "skills/a.md", 3); !slices.Equal(got, want) {
		t.Fatalf("got %q", got)
	}
	want[6], want[7] = "-U0", emptySHA1
	if got := DiffArgv(r, true, "skills/a.md", 0); !slices.Equal(got, want) {
		t.Fatalf("unborn: got %q", got)
	}
	r.ObjectFormat = "sha256"
	if got := DiffArgv(r, true, "skills/a.md", 0); got[7] != emptySHA256 {
		t.Fatalf("sha256: got %q", got)
	}
	for _, bad := range []string{"", "../x", "a\nb", ":(glob)x", "/abs"} {
		if got := DiffArgv(r, false, bad, 3); got != nil {
			t.Errorf("%q: got %q, want nil", bad, got)
		}
	}
	if got := DiffArgv(r, false, "x", -1); got != nil {
		t.Errorf("negative context: %q", got)
	}
}

// The empty-tree ids are what git itself computes, and diffing an unborn
// repository against them shows the staged file as added.
func TestDiffArgvUnbornRealGit(t *testing.T) {
	gittest.Need(t)
	for _, format := range []string{"sha1", "sha256"} {
		dir := t.TempDir()
		gittest.Git(t, dir, "init", "-q", "--object-format="+format)
		if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("hello\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		gittest.Git(t, dir, "add", "a.md")
		top := strings.TrimSpace(gittest.Git(t, dir, "rev-parse", "--show-toplevel"))
		argv := DiffArgv(Repo{Top: top, ObjectFormat: format}, true, "a.md", 0)
		out := gittest.Git(t, "", argv...)
		if got := AddedLines(out); !slices.Equal(got, []Line{{1, "hello"}}) {
			t.Fatalf("%s: added %+v from %q", format, got, out)
		}
	}
}
