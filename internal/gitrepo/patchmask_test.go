package gitrepo

import (
	"strings"
	"testing"
)

// envTOML has an env table whose header is more than three lines above
// its last pair, and a plain table after it.
const envTOML = "[servers.x]\ncommand = 'npx'\n[servers.x.env]\nA = '1'\nB = '2'\nC = '3'\nD = '4'\nname = '" + opaqueTok + "'\n[servers.y]\nrev = '" + opaqueTok + "'\n"

// Ruling 48: a line under an env or headers [table] is an env or header
// value whatever its key; its header is read from the post-image.
func TestScanAddedEnvTableContext(t *testing.T) {
	cases := []struct {
		name, post string
		line       Line
		want       int // -1 hard, 1 soft, 0 none
	}{
		{"visible key under env", envTOML, Line{8, "name = '" + opaqueTok + "'"}, -1},
		{"credential under env", envTOML, Line{8, "command = '" + ghpTok + "'"}, -1},
		{"visible key after the env table", envTOML, Line{10, "rev = '" + opaqueTok + "'"}, 1},
		{"no post-image", "", Line{8, "name = '" + opaqueTok + "'"}, 1},
		{"headers array", "[[s.headers]]\nversion = '" + opaqueTok + "'\n", Line{2, "version = '" + opaqueTok + "'"}, -1},
		{"digest under env", "[x.env]\nchecksum = 'sha256:" + strings.Repeat("a", 64) + "'\n", Line{2, "checksum = 'sha256:" + strings.Repeat("a", 64) + "'"}, 0},
		{"reference under env", "[x.env]\ncommand = '${CMD}'\n", Line{2, "command = '${CMD}'"}, 0},
		{"header inside a multi-line string", "description = '''\n[x.env]\n'''\nname = '" + opaqueTok + "'\n", Line{4, "name = '" + opaqueTok + "'"}, 1},
	}
	for _, c := range cases {
		fs := ScanAdded(scanMCP, []Line{c.line}, c.post)
		got := len(fs)
		if got == 1 && fs[0].Hard {
			got = -1
		}
		if got != c.want {
			t.Errorf("%s: findings %+v, want %d", c.name, fs, c.want)
		}
	}
	if fs := ScanNewFile(scanMCP, []byte(envTOML)); len(fs) != 2 || !fs[0].Hard || fs[0].Line != 8 || fs[1].Hard {
		t.Errorf("new file: findings %+v, want hard at 8 and soft at 10", fs)
	}
}

func maskAll(m *PatchMasker, patch string) string {
	var out []string
	for _, l := range strings.Split(patch, "\n") {
		out = append(out, m.Line(l))
	}
	return strings.Join(out, "\n")
}

func TestPatchMaskerEnvTable(t *testing.T) {
	tok := opaqueTok
	// An untracked file is its own post-image.
	got := maskAll(NewAddedMasker(scanMCP, envTOML), "+"+strings.ReplaceAll(strings.TrimSuffix(envTOML, "\n"), "\n", "\n+"))
	if !strings.Contains(got, "+name = '••••'") || !strings.Contains(got, "+rev = '"+tok+"'") || !strings.Contains(got, "+command = 'npx'") {
		t.Errorf("added:\n%s", got)
	}
	// A hunk without its header: the post-image gives the context.
	hunk := "diff --git a/mcp/a.toml b/mcp/a.toml\n--- a/mcp/a.toml\n+++ b/mcp/a.toml\n@@ -5,4 +5,5 @@\n B = '2'\n C = '3'\n D = '4'\n+name = '" + tok + "'\n [servers.y]\n rev = '" + tok + "'"
	got = maskAll(NewPatchMasker(scanMCP, envTOML), hunk)
	if !strings.Contains(got, "+name = '••••'") || !strings.Contains(got, " rev = '"+tok+"'") {
		t.Errorf("hunk:\n%s", got)
	}
	// Without a post-image only the hunk's own headers count: a removed
	// env table is masked, and the value after a plain header is shown.
	removed := "@@ -1,4 +0,0 @@\n-[servers.x.env]\n-name = '" + tok + "'\n-[servers.y]\n-rev = '" + tok + "'"
	got = maskAll(NewPatchMasker(scanMCP, ""), removed)
	if !strings.Contains(got, "-name = '••••'") || !strings.Contains(got, "-rev = '"+tok+"'") {
		t.Errorf("removed:\n%s", got)
	}
	// Two hunks: the second takes the post-image's state at its start.
	two := "@@ -2,0 +2,1 @@\n+version = '" + tok + "'\n@@ -7,0 +8,1 @@\n+name = '" + tok + "'"
	post := "[servers.x]\nversion = '" + tok + "'\n[servers.x.env]\nA = '1'\nB = '2'\nC = '3'\nD = '4'\nname = '" + tok + "'\n"
	got = maskAll(NewPatchMasker(scanMCP, post), two)
	if !strings.Contains(got, "+version = '"+tok+"'") || !strings.Contains(got, "+name = '••••'") {
		t.Errorf("two hunks:\n%s", got)
	}
	// A plain MaskPatchLine (no context) keeps today's behaviour.
	if l := "+name = '" + tok + "'"; MaskPatchLine(scanMCP, l) != l {
		t.Errorf("MaskPatchLine changed %q", l)
	}
}
