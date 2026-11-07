package gitrepo

import (
	"bytes"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/mask"
)

// The finding kinds the scan adds to mask's credential kinds (mask.Kind*).
const (
	// KindTokenShapedWord: a word mask.TokenShaped reads as a secret value.
	KindTokenShapedWord = "token-shaped word"
	// KindNotScannedBinary: a new file with a NUL in its first 8 KiB.
	KindNotScannedBinary = "not scanned: binary"
	// KindNotScannedLarge: a new file over 1 MiB.
	KindNotScannedLarge = "not scanned: over 1 MiB"
	// KindNotScannedPatch: a patch the runner cut short (for callers that
	// scan a truncated diff).
	KindNotScannedPatch = "not scanned: patch over 4 MiB"
	// KindLineTooLong: a line over 64 KiB, not scanned word by word.
	KindLineTooLong = "line over 64 KiB not scanned"
	// KindMultiLineSecret: a secret-named key or header whose value opens
	// a multi-line string, which the line scan cannot follow.
	KindMultiLineSecret = "multi-line secret value"
)

const (
	// ShownColumns caps Finding.Shown.
	ShownColumns = 120
	// maxScanBytes and binarySniff bound ScanNewFile.
	maxScanBytes = 1 << 20
	// maxLineBytes: a longer line is not scanned or shown word by word.
	maxLineBytes = 64 << 10
	binarySniff  = 8 << 10
)

// The empty tree of each object format: an unborn repository has no HEAD
// to diff against.
const (
	emptyTreeSHA1   = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	emptyTreeSHA256 = "6ef19b41225c5369f1c104d45d8d85efa9b057b53b14b4b9b939dd74decc5321"
)

// DiffArgv is the diff of one path against HEAD with unified context lines,
// with external diff drivers, textconv filters and colour off, so the patch
// is git's own and holds the file's bytes. When unborn (Status reports
// Branch.Unborn) there is no HEAD and the empty tree of r's object format
// stands in for it; an object format other than "sha256" is sha1, as
// Detect reports for git before 2.29. path is relative to r.Top; DiffArgv
// returns nil when actions.ValidGitPath refuses it or unified is negative.
func DiffArgv(r Repo, unborn bool, path string, unified int) []string {
	if actions.ValidGitPath(path) != nil || unified < 0 {
		return nil
	}
	base := "HEAD"
	if unborn {
		base = emptyTreeSHA1
		if r.ObjectFormat == "sha256" {
			base = emptyTreeSHA256
		}
	}
	return []string{"-C", r.Top, "diff", "--no-ext-diff", "--no-textconv", "--no-color",
		"-U" + strconv.Itoa(unified), base, "--", path}
}

// Line is one added line of a patch: N is its line number in the new file.
type Line struct {
	N    int
	Text string
}

var hunkRe = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// hunk is the parse state inside one hunk.
type hunk struct {
	next             int // line number of the next new-file line
	oldLeft, newLeft int // lines the header promised that are still to come
}

// parseHunk reads a hunk header; ok is false for anything else, a header
// with an unparseable count included.
func parseHunk(l string) (h hunk, ok bool) {
	m := hunkRe.FindStringSubmatch(l)
	if m == nil {
		return h, false
	}
	n := [4]int{0, 1, 0, 1} // an omitted count is 1
	for i, s := range m[1:] {
		if s == "" {
			continue
		}
		v, err := strconv.Atoi(s)
		if err != nil {
			return h, false
		}
		n[i] = v
	}
	return hunk{next: n[2], oldLeft: n[1], newLeft: n[3]}, true
}

// AddedLines returns the added lines of a unified diff with their new-file
// line numbers. Lines starting "+++ " or "--- " are file headers only
// outside a hunk; inside one (until the header's old and new counts are
// used up) every '+' line is an added line, so a file line "++ x" (diff
// line "+++ x") is reported. "\ No newline at end of file", omitted counts
// ("@@ -1 +1 @@") and CRLF line ends are understood.
func AddedLines(patch string) []Line {
	var out []Line
	var h hunk
	in := false
	for _, l := range strings.Split(patch, "\n") {
		l = strings.TrimSuffix(l, "\r")
		if !in {
			h, in = parseHunk(l)
			continue
		}
		if !h.step(l, &out) {
			h, in = parseHunk(l)
			continue
		}
		in = h.oldLeft > 0 || h.newLeft > 0
	}
	return out
}

// step consumes one line inside a hunk, appending it to out when added. It
// reports false for a line that cannot be part of the hunk.
func (h *hunk) step(l string, out *[]Line) bool {
	switch {
	case strings.HasPrefix(l, "+") && h.newLeft > 0:
		*out = append(*out, Line{N: h.next, Text: l[1:]})
		h.next++
		h.newLeft--
	case strings.HasPrefix(l, "-") && h.oldLeft > 0:
		h.oldLeft--
	case strings.HasPrefix(l, `\`):
		// "\ No newline at end of file" belongs to the line before it.
	case (l == "" || l[0] == ' ') && h.oldLeft > 0 && h.newLeft > 0:
		// Context; an empty line is a context line whose space was trimmed.
		h.next++
		h.oldLeft--
		h.newLeft--
	default:
		return false
	}
	return true
}

// Finding is one scan result. Path and Shown are masked and sanitised:
// Shown is the line through mask.Text with every token-shaped word masked
// too, cut to ShownColumns. Hard findings refuse the commit; soft ones need
// a typed confirmation (R5). Line is 0 for a file that was not scanned.
type Finding struct {
	Path  string
	Line  int
	Shown string
	Hard  bool
	Kind  string
}

// ScanLines scans added lines of the home-relative file homeRel (R5).
// Everywhere, a known token shape, an Authorization-style header value
// (as "Name: value", or as a TOML or JSON pair named Authorization,
// Proxy-Authorization, Cookie, Set-Cookie or *-Api-Key, inline tables
// included), URL userinfo, a PEM private key or an age identity is hard.
// In a config file (IsConfigFile) a secret-named key assigned a literal is
// hard (TOML triple quotes included), as is a secret-named key or header
// whose value opens a multi-line string (fail closed: its lines cannot be
// told apart), and a token-shaped word that is an env or header value
// (the value of a key outside visibleKeys) or a URL query value; another
// token-shaped word is soft, and an exact OCI digest or integrity value, a
// 40-hex commit pinned with '@' and a UUID are not findings. Elsewhere a
// secret-named assignment (or multi-line opener) and a token-shaped word
// are soft. References (${NAME}, ${env:NAME}, ${secret:NAME}, and in
// header values an upper-case $NAME) are never findings, unless their name
// is a token shape. A line over 64 KiB is not scanned: it gives one soft
// finding with no Shown. A line gives at most one finding, hard when any
// of its findings is.
func ScanLines(homeRel string, lines []Line) []Finding {
	config := IsConfigFile(homeRel)
	shownPath := mask.OneLine(mask.Text(homeRel))
	var out []Finding
	for _, l := range lines {
		if len(l.Text) > maxLineBytes {
			out = append(out, Finding{Path: shownPath, Line: l.N, Kind: KindLineTooLong})
			continue
		}
		kind, hard, found := classify(l.Text, config)
		if found {
			out = append(out, Finding{Path: shownPath, Line: l.N, Shown: shown(l.Text), Hard: hard, Kind: kind})
		}
	}
	return out
}

// ScanNewFile scans a whole new (untracked) file. A binary file (a NUL in
// its first 8 KiB) or one over 1 MiB is not scanned and gives one soft
// finding saying so. CRLF line ends are understood.
func ScanNewFile(homeRel string, content []byte) []Finding {
	notScanned := func(kind string) []Finding {
		return []Finding{{Path: mask.OneLine(mask.Text(homeRel)), Kind: kind}}
	}
	if bytes.IndexByte(content[:min(len(content), binarySniff)], 0) >= 0 {
		return notScanned(KindNotScannedBinary)
	}
	if len(content) > maxScanBytes {
		return notScanned(KindNotScannedLarge)
	}
	split := strings.Split(string(content), "\n")
	lines := make([]Line, 0, len(split))
	for i, l := range split {
		lines = append(lines, Line{N: i + 1, Text: strings.TrimSuffix(l, "\r")})
	}
	return ScanLines(homeRel, lines)
}

// IsConfigFile reports whether the home-relative path is one of agentsync's
// TOML config files: agentsync.toml, anything under mcp/, hooks/*.toml or
// lsp/*.toml. The match ignores case, so on a case-folding file system
// MCP/x is treated as config too.
func IsConfigFile(homeRel string) bool {
	p := strings.ToLower(path.Clean(strings.ReplaceAll(homeRel, `\`, "/")))
	if p == "agentsync.toml" || strings.HasPrefix(p, "mcp/") {
		return true
	}
	dir, file := path.Split(p)
	return (dir == "hooks/" || dir == "lsp/") && strings.HasSuffix(file, ".toml")
}

// classify decides one line's finding.
func classify(text string, config bool) (kind string, hard, found bool) {
	pairs := allPairs(text)
	credKind, cred := mask.Credential(tripleQuotes.Replace(text))
	opener, authOpener := multiLineOpener(pairs)
	switch {
	case cred && credKind != mask.KindSecretAssignment:
		return credKind, true, true
	case slices.ContainsFunc(pairs, authLiteral):
		return mask.KindAuthHeader, true, true
	case opener:
		return KindMultiLineSecret, config || authOpener, true
	case cred:
		return credKind, config, true
	case !config:
		return KindTokenShapedWord, false, hasTokenWord(text)
	}
	text = exemptRe.ReplaceAllString(mask.Sanitize(text), " ")
	if hardTokenWord(text) {
		return KindTokenShapedWord, true, true
	}
	return KindTokenShapedWord, false, hasTokenWord(text)
}

// tripleQuotes folds TOML's multi-line string quotes into ordinary ones, so
// mask.Credential reads API_KEY = ”'x”' as API_KEY = 'x'.
var tripleQuotes = strings.NewReplacer(`"""`, `"`, "'''", "'")

// allPairs is the line's key/value pair and every pair inside its value
// at any depth. The value is cut at each bracket and comma outside quoted
// strings (looseFragments) rather than parsed, so half-edited TOML with a
// stray or missing brace still has each of its pairs checked.
func allPairs(text string) []kv {
	p, ok := parseKV(text)
	if !ok {
		return nil
	}
	out := []kv{p}
	if !strings.ContainsAny(p.value, "{[") {
		return out
	}
	for _, f := range looseFragments(p.value) {
		if q, ok := parseKV(f); ok {
			out = append(out, q)
		}
	}
	return out
}

// looseFragments cuts s at every bracket, brace and comma outside quoted
// strings.
func looseFragments(s string) []string {
	var out []string
	start := 0
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
		case strings.IndexByte("{}[],", c) >= 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// authKeyRe names an Authorization-style header: Authorization,
// Proxy-Authorization, Cookie, Set-Cookie, X-Api-Key and any *-Api-Key.
var authKeyRe = regexp.MustCompile(`^(?:(?:proxy-)?authorization|(?:set-)?cookie|(?:[a-z0-9]+-)*api-key)$`)

func authKey(p kv) bool { return authKeyRe.MatchString(strings.ToLower(p.lastKey())) }

// authLiteral reports a header pair (TOML or JSON form, which mask's
// "Name: value" rules do not read) whose value, after an optional scheme
// word, is not only references: it is checked as the header line
// "Authorization: <value>" would be.
func authLiteral(p kv) bool {
	if !authKey(p) {
		return false
	}
	v := unquote(p.value)
	if v == "" {
		return false
	}
	_, found := mask.Credential("Authorization: " + v)
	return found
}

// unquote strips a TOML or JSON value's quotes (triple quotes included)
// and a JSON trailing comma.
func unquote(v string) string {
	v = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(v), ","))
	for _, q := range []string{`"""`, "'''", `"`, "'"} {
		if strings.HasPrefix(v, q) {
			return strings.TrimSuffix(strings.TrimPrefix(v, q), q)
		}
	}
	return v
}

// multiLineOpener reports a pair whose key is secret-named or a header and
// whose value opens a multi-line string it does not close on this line: the
// value's lines cannot be told apart from others, so the opener is the
// finding (fail closed). auth is true when the key is a header, which is
// hard everywhere.
func multiLineOpener(pairs []kv) (found, auth bool) {
	for _, p := range pairs {
		if !opensMultiLine(p.value) {
			continue
		}
		if authKey(p) {
			return true, true
		}
		if _, ok := mask.Credential(p.lastKey() + " = x"); ok {
			found = true
		}
	}
	return found, false
}

func opensMultiLine(v string) bool {
	v = strings.TrimSpace(v)
	for _, q := range []string{`"""`, "'''"} {
		if strings.HasPrefix(v, q) && strings.Count(v, q) == 1 {
			return true
		}
	}
	return false
}

var (
	// wordRe splits a line into words: a ${reference} is one word.
	wordRe = regexp.MustCompile("\\$\\{[^}\\s]*\\}|[^\\s\"'`=,;&?#|*\\[\\](){}<>]+")
	// exemptRe: what is never a finding in a config file: an OCI digest or
	// an integrity value of the exact length (sha256:/384:/512: hex, or
	// SRI sha256-/384-/512- base64), a 40-hex commit pinned with '@'
	// (go install x@<sha>, git+https://...@<sha>), a UUID. Each must end
	// where its body ends, so a secret cannot hide behind a digest prefix.
	exemptRe = regexp.MustCompile(`(?i)(?:\bsha256:[0-9a-f]{64}|\bsha384:[0-9a-f]{96}|\bsha512:[0-9a-f]{128}` +
		`|\bsha256-[a-z0-9+/]{43}=|\bsha384-[a-z0-9+/]{64}|\bsha512-[a-z0-9+/]{86}==` +
		`|@[0-9a-f]{40}|\b[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12})(?:[^a-z0-9+/=]|$)`)
	// urlRe finds a URL, for its query values.
	urlRe = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.\-]*://[^\s"'<>]+`)
)

// tokenWord reports whether a word is token-shaped and not a reference.
// Trailing prose punctuation is not part of the word.
func tokenWord(w string) bool {
	w = strings.TrimRight(w, ".:!")
	return !mask.IsRef(w) && mask.TokenShaped(w)
}

func hasTokenWord(s string) bool {
	for _, w := range wordRe.FindAllString(s, -1) {
		if tokenWord(w) {
			return true
		}
	}
	return false
}

// softKey reports a config key whose value is not an env or header value
// (the keys MaskPatchLine shows, args among them): a token-shaped word
// there is soft.
func softKey(k string) bool { return visibleKeys[k] }

// hardTokenWord reports a config line's hard token-shaped word: in the
// value of a key that is not a softKey (an env or header value), or a URL query
// value anywhere on the line.
func hardTokenWord(text string) bool {
	if kv, ok := parseKV(text); ok && !softKey(kv.lastKey()) && hasTokenWord(kv.value) {
		return true
	}
	for _, u := range urlRe.FindAllString(text, -1) {
		_, q, ok := strings.Cut(u, "?")
		if !ok {
			continue
		}
		q, _, _ = strings.Cut(q, "#")
		for _, pair := range strings.FieldsFunc(q, func(r rune) bool { return r == '&' || r == ';' }) {
			if _, v, ok := strings.Cut(pair, "="); ok && hasTokenWord(v) {
				return true
			}
		}
	}
	return false
}

// shown is a finding's display line: mask.Text, then every token-shaped
// word masked, on one row, cut to ShownColumns.
func shown(text string) string {
	s := wordRe.ReplaceAllStringFunc(mask.Text(text), func(w string) string {
		if tokenWord(w) {
			return mask.Mask
		}
		return w
	})
	return ansi.Truncate(mask.OneLine(s), ShownColumns, "")
}
