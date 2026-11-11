package gitrepo

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/lusky3/agentsync-tui/internal/mask"
)

// visibleKeys are the config keys whose values MaskPatchLine shows through
// mask.Text (args through mask.Args) rather than mask.Value: they name what
// runs and where, not what it is given. The scan treats a token-shaped word
// in their values as soft.
var visibleKeys = map[string]bool{
	"type": true, "command": true, "url": true, "enabled": true, "description": true, "args": true,
	"path": true, "cwd": true, "version": true, "rev": true, "sha": true, "checksum": true,
	"root_markers": true, "initialization_options": true, "name": true, "matcher": true,
	"model": true, "author_email": true,
}

// diffHeaders start the lines of a patch that are not file content.
var diffHeaders = []string{"diff ", "index ", "--- ", "+++ ", "@@"}

// MaskPatchLine returns one line of a unified diff of the home-relative
// file homeRel, safe to display. A line over 64 KiB is shown as its first
// byte, mask.Mask and its length. Header lines (diff, index, ---, +++, @@)
// go through mask.Text whole, so a token in a file name is masked; an added
// line "++ x" inside a hunk looks like a header here and is masked the
// same way. A content line keeps its first byte ('+', '-' or ' '). In a
// config file (IsConfigFile) a TOML "key = value" or JSON "key": value
// pair whose key is not a visible one (visibleKeys) shows its value
// through mask.Value (a quoted string, each element of an array and each
// value of an inline table on its own), so references stay visible and
// literals do not; args go through mask.Args with token-shaped elements
// masked. A config line that is not a pair, a [table] header or blank (a
// multi-line string's content, an array element on its own line) shows
// every word through mask.Value, and a comment masks its token-shaped
// words. Other lines go through mask.Text. The result is sanitised and
// passes mask.Text last, so a token shape cannot survive anywhere, the
// name of a reference included.
func MaskPatchLine(homeRel, line string) string { return maskPatchLine(homeRel, line, false) }

// maskPatchLine is MaskPatchLine for a line under an env, environment or
// headers table (env): a config pair's value goes through maskStrict
// whatever its key.
func maskPatchLine(homeRel, line string, env bool) string {
	if len(line) > maxLineBytes {
		return longLine(line)
	}
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
		body = maskConfigLine(body, env)
	}
	return line[:1] + mask.Text(body)
}

// longLine is a line too long to show: its diff marker, the mask, its size.
func longLine(line string) string {
	marker := ""
	if strings.ContainsRune("+- ", rune(line[0])) {
		marker = line[:1]
	}
	return marker + mask.Mask + " (line of " + strconv.Itoa(len(line)) + " bytes not shown)"
}

// tableRe is a TOML [table] or [[array]] header line.
var tableRe = regexp.MustCompile(`^\s*\[\[?[^\[\]=]*\]\]?\s*(?:#.*)?$`)

// maskConfigLine masks one content line of a config file; a pair under an
// env, environment or headers table (env) as an env or header value.
func maskConfigLine(s string, env bool) string {
	if p, ok := parseKV(s); ok {
		if env {
			return p.strict()
		}
		return p.masked()
	}
	t := strings.TrimSpace(s)
	switch {
	case t == "" || tableRe.MatchString(t):
		return s
	case strings.HasPrefix(t, "#"):
		return maskWords(s, tokenWord)
	}
	return maskWords(s, func(w string) bool { return mask.Value(w) != w })
}

// maskWords replaces each word (wordRe) for which hide is true by mask.Mask.
func maskWords(s string, hide func(string) bool) string {
	return wordRe.ReplaceAllStringFunc(s, func(w string) string {
		if hide(w) {
			return mask.Mask
		}
		return w
	})
}

const (
	tomlBareKey   = `[A-Za-z0-9_\-]+`
	tomlQuotedKey = `"(?:[^"\\]|\\.)*"|'[^']*'`
	tomlKeyPart   = `(?:` + tomlBareKey + `|` + tomlQuotedKey + `)`
)

var (
	// kvRe is a TOML key/value pair: indent, a (dotted) key, " = ", the rest.
	kvRe = regexp.MustCompile(`^(\s*)(` + tomlKeyPart + `(?:\s*\.\s*` + tomlKeyPart + `)*)(\s*=\s*)(.*?)(\s*)$`)
	// jsonKVRe is a JSON member on one line: indent, a quoted key, ": ",
	// the value, and a trailing comma.
	jsonKVRe = regexp.MustCompile(`^(\s*)("(?:[^"\\]|\\.)*")(\s*:\s*)(.*?)(\s*,?\s*)$`)
	// keyPartRe finds the parts of a dotted key.
	keyPartRe = regexp.MustCompile(tomlKeyPart)
)

// kv is one parsed pair; indent+key+eq+value+trail is the original text.
type kv struct {
	indent, key, eq, value, trail string
}

// parseKV reads a TOML pair or, failing that, a JSON member (mcp/ may hold
// JSON files).
func parseKV(s string) (kv, bool) {
	m := kvRe.FindStringSubmatch(s)
	if m == nil {
		m = jsonKVRe.FindStringSubmatch(s)
	}
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
	return unquoteKey(parts[len(parts)-1])
}

// unquoteKey is a key part as TOML (or JSON) reads it: a literal string's
// quotes dropped, a basic string's escapes decoded too ("e\u006ev" is
// env), so an escaped spelling names the same table as the plain one. A
// basic string with an escape strconv does not take keeps its text.
func unquoteKey(k string) string {
	if len(k) < 2 {
		return k
	}
	switch k[0] {
	case '\'':
		return k[1 : len(k)-1]
	case '"':
		if u, err := strconv.Unquote(k); err == nil {
			return u
		}
		return k[1 : len(k)-1]
	}
	return k
}

// underEnvTable reports a pair whose dotted key names an env, environment
// or headers table in any part (env.command, server.env.X): its value is
// an env or header value, whatever its last part.
func (p kv) underEnvTable() bool {
	return slices.ContainsFunc(keyPartRe.FindAllString(p.key, -1), func(k string) bool {
		return envTableKey(unquoteKey(k))
	})
}

// masked rebuilds the pair with its key through mask.Text and its value
// through maskValue; a pair under an env or headers table goes through
// maskStrict, and a visible key's value through maskVisible (args through
// maskArgs).
func (p kv) masked() string {
	key := p.lastKey()
	switch {
	case p.underEnvTable():
		return p.strict()
	case key == "args":
		return p.indent + p.key + p.eq + maskArgs(p.value) + p.trail
	case visibleKeys[key]:
		return p.indent + p.key + p.eq + maskVisible(p.value, false) + p.trail
	}
	return p.indent + mask.Text(p.key) + p.eq + maskValue(p.value) + p.trail
}

// strict rebuilds the pair as an env or header value: its key through
// mask.Text, its value through maskStrict.
func (p kv) strict() string {
	return p.indent + mask.Text(p.key) + p.eq + maskStrict(p.value) + p.trail
}

// envTableKey names a table of values handed to a program: env,
// environment, headers. Every scalar under it, at any depth and whatever
// its own key, is a value (maskStrict, and hard when token-shaped).
func envTableKey(k string) bool {
	switch strings.ToLower(k) {
	case "env", "environment", "headers":
		return true
	}
	return false
}

// maskStrict masks a value under an env or headers key: arrays and inline
// tables element by element, every pair's value through maskStrict
// whatever its key (no visible key shows a value here), and anything else
// as maskValue does.
func maskStrict(v string) string {
	t := strings.TrimSpace(v)
	lead := v[:strings.Index(v, t)]
	trail := v[len(lead)+len(t):]
	switch {
	case container(t, '[', ']'):
		return lead + "[" + mapElems(t[1:len(t)-1], maskStrict) + "]" + trail
	case container(t, '{', '}'):
		return lead + "{" + mapElems(t[1:len(t)-1], strictPair) + "}" + trail
	}
	return lead + maskTrimmed(t) + trail
}

// strictPair is maskStrict for one element of an inline table.
func strictPair(e string) string {
	p, ok := parseKV(e)
	if !ok {
		return maskStrict(e)
	}
	return p.indent + mask.Text(p.key) + p.eq + maskStrict(p.value) + p.trail
}

// maskVisible shows the value of a visible key. An inline table is shown
// pair by pair (maskVisiblePair) and an array element by element, at any
// depth; a scalar is shown as is unless mask.Credential finds a credential
// in it or, nested in a table (nested), it holds a token-shaped word, and
// then it is masked as any value. Top-level scalars keep their
// token-shaped words: a checksum, rev or store path is what these keys
// hold.
func maskVisible(v string, nested bool) string {
	t := strings.TrimSpace(v)
	lead := v[:strings.Index(v, t)]
	trail := v[len(lead)+len(t):]
	return lead + maskVisibleTrimmed(t, nested) + trail
}

// maskVisibleTrimmed is maskVisible for a value already trimmed of
// surrounding space: containers are masked element by element, a scalar
// only when it is a credential, not plain or (nested) token-shaped.
func maskVisibleTrimmed(t string, nested bool) string {
	switch {
	case container(t, '{', '}'):
		return "{" + mapElems(t[1:len(t)-1], maskVisiblePair) + "}"
	case container(t, '[', ']'):
		return "[" + mapElems(t[1:len(t)-1], func(e string) string { return maskVisible(e, nested) }) + "]"
	case !plainScalar(t), credential(t), nested && hasTokenWord(exemptRe.ReplaceAllString(t, " ")):
		return maskTrimmed(t)
	}
	return t
}

// plainScalar reports a value shown as is under a visible key: one quoted
// string, or a bare word (a number, a boolean) with no quote, bracket,
// '=', ',' or '#'. Half-edited TOML with stray braces or more than one
// value is neither, and is masked.
func plainScalar(t string) bool {
	return quotedString(t) || !strings.ContainsAny(t, "'\"=,{}[]#")
}

// container reports whether t is one array or inline table: it opens with
// opener, closes with closer, and its brackets and quotes balance (topScan).
// Unbalanced, half-edited TOML is never walked as a table, so a stray
// closer cannot end it early and leave a later pair unmasked.
func container(t string, opener, closer byte) bool {
	return len(t) >= 2 && t[0] == opener && t[len(t)-1] == closer && topScan(t, nil)
}

// credential reports what mask.Credential finds in s, TOML triple quotes
// read as ordinary ones.
func credential(s string) bool {
	_, found := mask.Credential(tripleQuotes.Replace(s))
	return found
}

// maskVisiblePair masks one pair of an inline table under a visible key:
// a pair the scan would flag (a credential, a header, a token-shaped
// value) is masked as maskPair would, any other is shown, its tables and
// arrays recursively.
func maskVisiblePair(e string) string {
	p, ok := parseKV(e)
	switch {
	case !ok:
		return maskVisible(e, true)
	case p.underEnvTable(), p.lastKey() == "args":
		return p.masked()
	case authKey(p), credential(e):
		return p.indent + mask.Text(p.key) + p.eq + maskValue(p.value) + p.trail
	}
	return p.indent + mask.Text(p.key) + p.eq + maskVisible(p.value, true) + p.trail
}

// maskArgs masks a one-line args array as a command line (mask.Args, so a
// flag's secret value is masked and references stay), and masks any
// element holding a token-shaped word outside the exempt digests and
// commits. A value that is not a whole array is masked as any value; a
// lone "[" (a multi-line array's opener) stays.
func maskArgs(v string) string {
	t := strings.TrimSpace(v)
	if t == "[" {
		return v
	}
	if !container(t, '[', ']') {
		return maskValue(v)
	}
	elems := splitElems(t[1 : len(t)-1])
	contents := make([]string, len(elems))
	for i, e := range elems {
		_, contents[i], _, _ = splitQuoted(e)
	}
	masked := mask.Args(contents)
	for i, e := range elems {
		lead, content, q, trail := splitQuoted(e)
		if hasTokenWord(exemptRe.ReplaceAllString(content, " ")) {
			masked[i] = mask.Mask
		}
		elems[i] = lead + q + masked[i] + q + trail
	}
	lead := v[:strings.Index(v, t)]
	return lead + "[" + strings.Join(elems, ",") + "]" + v[len(lead)+len(t):]
}

// splitQuoted splits an array element into its surrounding whitespace, its
// content and its quote (empty for a bare value).
func splitQuoted(e string) (lead, content, quote, trail string) {
	t := strings.TrimSpace(e)
	lead = e[:strings.Index(e, t)]
	trail = e[len(lead)+len(t):]
	if quotedString(t) {
		return lead, t[1 : len(t)-1], t[:1], trail
	}
	return lead, t, "", trail
}

// quotedString reports a one-line quoted string, its quotes at both ends.
func quotedString(t string) bool {
	return len(t) >= 2 && (t[0] == '\'' || t[0] == '"') && t[len(t)-1] == t[0] && closesAtEnd(t)
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

// maskTrimmed masks a trimmed value through mask.Value: a quoted string's
// contents, every element of an array or inline table, else the scalar.
func maskTrimmed(t string) string {
	switch {
	case quotedString(t):
		return t[:1] + mask.Value(t[1:len(t)-1]) + t[:1]
	case container(t, '[', ']'):
		return "[" + mapElems(t[1:len(t)-1], maskValue) + "]"
	case container(t, '{', '}'):
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

// mapElems applies f to each element of s (splitElems) and joins them back
// with the same commas.
func mapElems(s string, f func(string) string) string {
	elems := splitElems(s)
	for i, e := range elems {
		elems[i] = f(e)
	}
	return strings.Join(elems, ",")
}

// splitElems splits the inside of an array or inline table at its
// top-level commas: outside quotes and nested brackets. Joining the result
// with "," gives s back. Callers pass the inside of a container, so the
// brackets balance.
func splitElems(s string) []string {
	var out []string
	start := 0
	topScan(s, func(i int) {
		out = append(out, s[start:i])
		start = i + 1
	})
	return append(out, s[start:])
}

// topScan walks s outside quoted strings, calling comma (when not nil) at
// each comma outside every bracket. ok is false when a closer has no
// opener, or a bracket or quote is left open.
func topScan(s string, comma func(i int)) (ok bool) {
	depth := 0
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
			if depth < 0 {
				return false
			}
		case c == ',' && depth == 0 && comma != nil:
			comma(i)
		}
	}
	return depth == 0 && quote == 0
}

// quoteState is the open quote after byte c inside a string opened by q.
func quoteState(q, c byte) byte {
	if c == q {
		return 0
	}
	return q
}
