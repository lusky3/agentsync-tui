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

// Removed lines fail closed on their table: the pre-image's table is
// followed across hunks (the unchanged lines between hunks read from the
// post-image), a header-like line inside a multi-line string is no header,
// and when the pre-image's table is not known a removed value is masked.
func TestPatchMaskerRemovedLinesFailClosed(t *testing.T) {
	tok := opaqueTok
	fill := "A = '1'\nB = '2'\nC = '3'\nD = '4'\nE = '5'\nF = '6'\nG = '7'\nH = '8'\n"
	cases := []struct {
		name, post, patch, hidden string
	}{
		// (a) the env header became a plain one in the first hunk; the
		// second hunk removes a value that was under the env table.
		{"header changed in an earlier hunk", "[x]\n" + fill + "Z = '9'\n",
			"@@ -1 +1 @@\n-[x.env]\n+[x]\n@@ -10,2 +10 @@\n Z = '9'\n-name = '" + tok + "'", "-name = '" + tok + "'"},
		// (b) a body line that looks like a header does not end the env table.
		{"header-like body line", "[x.env]\ndescription = '''\n[ok]\n'''\n" + fill,
			"@@ -1,5 +1,4 @@\n [x.env]\n description = '''\n [ok]\n '''\n-name = '" + tok + "'", "-name = '" + tok + "'"},
		// The same body line in the unchanged gap between hunks.
		{"header-like body line between hunks", "[x.env]\ndescription = '''\n[ok]\n'''\n" + fill,
			"@@ -1,0 +2,0 @@\n@@ -13 +12,0 @@\n-name = '" + tok + "'", "-name = '" + tok + "'"},
		// No post-image and the hunk starts mid-file: the table is not known.
		{"unknown table", "", "@@ -40 +39,0 @@\n-name = '" + tok + "'", "-name = '" + tok + "'"},
		// A post-image cut before the hunk: the table is not known either.
		{"post-image too short", "[x]\nA = '1'\n", "@@ -40 +39,0 @@\n-name = '" + tok + "'", "-name = '" + tok + "'"},
	}
	for _, c := range cases {
		got := maskAll(NewPatchMasker(scanMCP, c.post), c.patch)
		if strings.Contains(got, c.hidden) {
			t.Errorf("%s: shown:\n%s", c.name, got)
		}
	}
	// Known plain tables still show a visible key's value (Rulings 20, 21).
	for _, c := range []struct{ name, post, patch string }{
		{"plain table before the hunk", "[x]\n" + fill, "@@ -10 +9,0 @@\n-name = '" + tok + "'"},
		{"start of the file", "", "@@ -1,2 +0,0 @@\n-[x]\n-name = '" + tok + "'"},
		{"env header replaced earlier, then a plain one in the gap", "[x]\nA = '1'\n[y]\n" + fill,
			"@@ -1 +1 @@\n-[x.env]\n+[x]\n@@ -12 +11,0 @@\n-name = '" + tok + "'"},
	} {
		if got := maskAll(NewPatchMasker(scanMCP, c.post), c.patch); !strings.Contains(got, "-name = '"+tok+"'") {
			t.Errorf("%s: masked:\n%s", c.name, got)
		}
	}
}

// Every line of a multi-line string opened by a secret-named, header or
// env key is the secret's: shown in the diff pane it is masked whole,
// whatever it looks like (a visible key's pair included), on the added
// side from the post-image and on the removed side as the pre-image's
// state is followed. A plain multi-line string stays readable.
func TestPatchMaskerHidesSecretMultiLineBodies(t *testing.T) {
	secret := "[s]\npassword = '''\nname = '" + hunter + "'\n" + hunter + "\n'''\nname = 'shown'\n"
	got := maskAll(NewAddedMasker(scanMCP, secret), addedLines(secret))
	if strings.Contains(got, hunter) || !strings.Contains(got, "+name = 'shown'") {
		t.Errorf("added body:\n%s", got)
	}
	removed := "@@ -1,6 +1,2 @@\n [s]\n-password = '''\n-name = '" + hunter + "'\n-" + hunter + "\n-'''\n name = 'shown'"
	got = maskAll(NewPatchMasker(scanMCP, "[s]\nname = 'shown'\n"), removed)
	if strings.Contains(got, hunter) || !strings.Contains(got, " name = 'shown'") {
		t.Errorf("removed body:\n%s", got)
	}
	// The body continues past a hunk boundary: its opener is in the gap.
	gap := "@@ -4 +4 @@\n-name = '" + hunter + "'\n+name = '" + hunter + "x'"
	post := "[s]\nAuthorization = \"\"\"\nBearer\nname = '" + hunter + "x'\n\"\"\"\n"
	if got = maskAll(NewPatchMasker(scanMCP, post), gap); strings.Contains(got, hunter) {
		t.Errorf("body across the gap:\n%s", got)
	}
	plain := "[s]\ndescription = '''\nname = 'visible'\n'''\n"
	if got = maskAll(NewAddedMasker(scanMCP, plain), addedLines(plain)); !strings.Contains(got, "+name = 'visible'") {
		t.Errorf("plain body masked:\n%s", got)
	}
}

// addedLines shows content as an untracked file's added lines.
func addedLines(content string) string {
	return "+" + strings.ReplaceAll(strings.TrimSuffix(content, "\n"), "\n", "\n+")
}
