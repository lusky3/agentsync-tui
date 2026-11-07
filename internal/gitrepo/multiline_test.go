package gitrepo

import (
	"strings"
	"testing"

	"github.com/lusky3/agentsync-tui/internal/mask"
)

// greptileTOML is a TOML file whose secret-named key holds a multi-line
// literal string: with -U0 an edit of its body sends only the body lines.
const greptileTOML = "[servers.x]\ncommand = \"npx\"\n[servers.x.env]\nAPI_KEY = '''\nhunter2\n'''\n"

func TestScanAddedBodyOfSecretMultiLineValue(t *testing.T) {
	added := []Line{{N: 5, Text: "hunter2"}}
	if fs := ScanLines(scanMCP, added); len(fs) != 0 {
		t.Fatalf("precondition: the line alone is a finding: %+v", fs)
	}
	for _, c := range []struct {
		path string
		hard bool
	}{{scanMCP, true}, {scanSkill, false}} {
		fs := ScanAdded(c.path, added, greptileTOML)
		if len(fs) != 1 || fs[0].Hard != c.hard || fs[0].Line != 5 || fs[0].Kind != mask.KindSecretAssignment {
			t.Fatalf("%s: findings %+v, want one hard=%v at line 5", c.path, fs, c.hard)
		}
		if strings.Contains(fs[0].Shown, "hunter2") {
			t.Errorf("%s: the body is shown: %q", c.path, fs[0].Shown)
		}
	}
}

func TestScanAddedMultiLineBodyCases(t *testing.T) {
	cases := []struct {
		name, post string
		line       Line
		want       int // findings; -1 hard, 1 soft, 0 none (config path)
	}{
		{"closing line", "API_KEY = '''\nhunter2'''\n", Line{2, "hunter2'''"}, -1},
		{"basic string", "[env]\nTOKEN = \"\"\"\n  hunter2\n\"\"\"\n", Line{3, "  hunter2"}, -1},
		{"reference body", "API_KEY = '''\n${TOKEN}\n'''\n", Line{2, "${TOKEN}"}, 0},
		{"blank body line", "API_KEY = '''\n\nx\n'''\n", Line{2, ""}, 0},
		{"non-secret key", "description = '''\nhunter2\n'''\n", Line{2, "hunter2"}, 0},
		{"after the string", "API_KEY = '''\nx\n'''\nname = 1\nhunter2\n", Line{5, "hunter2"}, 0},
		{"auth header", "[headers]\nAuthorization = '''\nBearer abc123def\n'''\n", Line{3, "Bearer abc123def"}, -1},
		{"auth header reference", "[headers]\nAuthorization = '''\nBearer ${TOKEN}\n'''\n", Line{3, "Bearer ${TOKEN}"}, 0},
		{"inline table opener", "env = { API_KEY = '''\nhunter2\n''' }\n", Line{2, "hunter2"}, -1},
		{"closed on one line", "API_KEY = '''x'''\nhunter2\n", Line{2, "hunter2"}, 0},
		{"crlf", "API_KEY = '''\r\nhunter2\r\n'''\r\n", Line{2, "hunter2"}, -1},
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
}

// An unchanged secret body is not an added line: it is not reported.
func TestScanAddedUnchangedBodyNotReported(t *testing.T) {
	post := "command = \"npx2\"\n" + strings.TrimPrefix(greptileTOML, "[servers.x]\ncommand = \"npx\"\n")
	if fs := ScanAdded(scanMCP, []Line{{N: 1, Text: "command = \"npx2\""}}, post); len(fs) != 0 {
		t.Errorf("findings %+v", fs)
	}
}
