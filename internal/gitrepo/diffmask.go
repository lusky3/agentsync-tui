package gitrepo

import (
	"regexp"
	"strings"

	"github.com/lusky3/agentsync-tui/internal/mask"
)

// visibleKeys are the config keys whose values MaskPatchLine shows through
// mask.Text rather than mask.Value: they name what runs, not what it is
// given.
var visibleKeys = map[string]bool{"type": true, "command": true, "url": true, "enabled": true, "description": true}

// diffHeaders start the lines of a patch that are not file content.
var diffHeaders = []string{"diff ", "index ", "--- ", "+++ ", "@@"}

// MaskPatchLine returns one line of a unified diff of the home-relative
// file homeRel, safe to display. Header lines (diff, index, ---, +++, @@)
// go through mask.Text whole, so a token in a file name is masked; an added
// line "++ x" inside a hunk looks like a header here and is masked the
// same way. A content line keeps its first byte ('+', '-' or ' '). In a
// config file (IsConfigFile) a TOML "key = value" line whose key is not
// type, command, url, enabled or description shows its value through
// mask.Value (a quoted string, each element of an array and each value of
// an inline table on its own), so references stay visible and literals do
// not. Everything else goes through mask.Text. The result is sanitised and
// passes mask.Text last, so a token shape cannot survive anywhere, the name
// of a reference included.
func MaskPatchLine(homeRel, line string) string {
	line = mask.Sanitize(line)
	for _, h := range diffHeaders {
		if strings.HasPrefix(line, h) {
			return mask.Text(line)
		}
	}
	if line == "" || !strings.ContainsRune("+- ", rune(line[0])) {
		return mask.Text(line)
	}
	body := line[1:]
	if IsConfigFile(homeRel) {
		body = maskTOMLLine(body)
	}
	return line[:1] + mask.Text(body)
}

// maskTOMLLine masks the value of a "key = value" line.
func maskTOMLLine(s string) string {
	kv, ok := parseKV(s)
	if !ok {
		return s
	}
	return kv.masked()
}

const (
	tomlBareKey   = `[A-Za-z0-9_\-]+`
	tomlQuotedKey = `"(?:[^"\\]|\\.)*"|'[^']*'`
	tomlKeyPart   = `(?:` + tomlBareKey + `|` + tomlQuotedKey + `)`
)

// kvRe is a TOML key/value pair: indent, a (dotted) key, " = ", the rest.
var kvRe = regexp.MustCompile(`^(\s*)(` + tomlKeyPart + `(?:\s*\.\s*` + tomlKeyPart + `)*)(\s*=\s*)(.*?)(\s*)$`)

// keyPartRe finds the parts of a dotted key.
var keyPartRe = regexp.MustCompile(tomlKeyPart)

// kv is one parsed pair; indent+key+eq+value+trail is the original text.
type kv struct {
	indent, key, eq, value, trail string
}

func parseKV(s string) (kv, bool) {
	m := kvRe.FindStringSubmatch(s)
	if m == nil {
		return kv{}, false
	}
	return kv{indent: m[1], key: m[2], eq: m[3], value: m[4], trail: m[5]}, true
}

// maskPair masks one element of an inline table: a pair as a line, and
// anything else (not valid TOML) as a value.
func maskPair(s string) string {
	if p, ok := parseKV(s); ok {
		return p.masked()
	}
	return maskValue(s)
}

// lastKey is the last part of the dotted key, unquoted.
func (p kv) lastKey() string {
	parts := keyPartRe.FindAllString(p.key, -1)
	last := parts[len(parts)-1]
	if len(last) >= 2 && (last[0] == '"' || last[0] == '\'') {
		return last[1 : len(last)-1]
	}
	return last
}

// masked rebuilds the pair with its key through mask.Text and its value
// through maskValue, unless the key is a visible one.
func (p kv) masked() string {
	if visibleKeys[p.lastKey()] {
		return p.indent + p.key + p.eq + p.value + p.trail
	}
	return p.indent + mask.Text(p.key) + p.eq + maskValue(p.value) + p.trail
}

// maskValue masks a TOML value: a one-line quoted string keeps its quotes
// around mask.Value of its content, an array or inline table is masked
// element by element (an inline table's pairs as lines), and anything else
// (a number, a value followed by a comment, a multi-line opener) is
// mask.Value of the whole.
func maskValue(v string) string {
	t := strings.TrimSpace(v)
	lead := v[:strings.Index(v, t)]
	trail := v[len(lead)+len(t):]
	return lead + maskTrimmed(t) + trail
}

func maskTrimmed(t string) string {
	switch {
	case len(t) >= 2 && (t[0] == '\'' || t[0] == '"') && t[len(t)-1] == t[0] && closesAtEnd(t):
		return t[:1] + mask.Value(t[1:len(t)-1]) + t[:1]
	case len(t) >= 2 && t[0] == '[' && t[len(t)-1] == ']':
		return "[" + mapElems(t[1:len(t)-1], maskValue) + "]"
	case len(t) >= 2 && t[0] == '{' && t[len(t)-1] == '}':
		return "{" + mapElems(t[1:len(t)-1], maskPair) + "}"
	}
	return mask.Value(t)
}

// closesAtEnd reports whether the quoted string t (starting and ending with
// its quote) has no other unescaped closing quote: 'a' # 'b' is not one
// string.
func closesAtEnd(t string) bool {
	q := t[0]
	for i := 1; i < len(t)-1; i++ {
		switch {
		case q == '"' && t[i] == '\\':
			i++
		case t[i] == q:
			return false
		}
	}
	return true
}

// mapElems applies f to each comma-separated element of s (an array's or
// inline table's inside), splitting only outside quotes and nested
// brackets, and joins them back with the same commas. Unbalanced input is
// one element.
func mapElems(s string, f func(string) string) string {
	var out strings.Builder
	start, depth := 0, 0
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case quote != 0:
			quote = quoteState(quote, c)
			if c == '\\' && quote == '"' {
				i++
			}
		case c == '\'' || c == '"':
			quote = c
		case c == '[' || c == '{':
			depth++
		case c == ']' || c == '}':
			depth--
		case c == ',' && depth == 0:
			out.WriteString(f(s[start:i]) + ",")
			start = i + 1
		}
	}
	out.WriteString(f(s[start:]))
	return out.String()
}

// quoteState is the open quote after byte c inside a string opened by q.
func quoteState(q, c byte) byte {
	if c == q {
		return 0
	}
	return q
}
