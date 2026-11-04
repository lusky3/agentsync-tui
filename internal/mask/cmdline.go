package mask

import (
	"strings"
	"unicode"

	"github.com/lusky3/agentsync-tui/internal/shellwords"
)

// Free-form command lines (an MCP server's command and args) cannot be
// masked precisely: nothing says which words are secrets. CommandLine and
// FreeText therefore fail safe:
//   - every NAME=value / --flag=value word keeps its name and shows its value
//     only if it is a reference (Value semantics), so "--port=8080" is
//     masked too;
//   - the word after a secret-looking flag is masked (header-shaped values
//     keep their name), while generic flags such as -y or -p keep theirs;
//   - every other word goes through Text.

// shortSecretSegs are flag-name segments that mark a secret only as a whole
// segment ("--pat" yes, "--path" no).
var shortSecretSegs = map[string]bool{
	"key": true, "pat": true, "pwd": true, "auth": true, "private": true,
	"h": true, "header": true, "pass": true,
}

// longSecretParts mark a secret anywhere in a flag name.
var longSecretParts = []string{"token", "secret", "password", "passwd", "apikey", "credential", "bearer", "authorization"}

// secretFlag reports whether w is a flag whose next word is a secret. It
// covers everything mask.Args' secretFlagRe masks (any name containing key,
// auth, token, ...) plus the free-text vocabulary, with camelCase split into
// segments before matching ("githubPat" -> github, pat).
func secretFlag(w string) bool {
	if !strings.HasPrefix(w, "-") || strings.Contains(w, "=") {
		return false
	}
	if secretFlagRe.MatchString(w) {
		return true
	}
	raw := strings.TrimLeft(w, "-")
	if raw == "" {
		return false
	}
	name := strings.ToLower(raw)
	for _, p := range longSecretParts {
		if strings.Contains(name, p) {
			return true
		}
	}
	for _, seg := range flagSegments(raw) {
		if shortSecretSegs[seg] {
			return true
		}
	}
	return false
}

// flagSegments splits a flag name on '-', '_' and lower-to-upper camelCase
// boundaries, lowercased.
func flagSegments(raw string) []string {
	var segs []string
	var cur []rune
	prevLower := false
	flush := func() {
		if len(cur) > 0 {
			segs = append(segs, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	for _, r := range raw {
		switch {
		case r == '-' || r == '_':
			flush()
			prevLower = false
			continue
		case unicode.IsUpper(r) && prevLower:
			flush()
		}
		cur = append(cur, r)
		prevLower = unicode.IsLower(r) || unicode.IsDigit(r)
	}
	flush()
	return segs
}

// maxNest bounds re-scanning of quoted strings inside a command line
// (sh -c '...'); deeper nesting is masked whole.
const maxNest = 4

// maxFreeText bounds the work done on one free-form string; longer input
// (far beyond the form's 4096-byte field cap) is masked whole.
const maxFreeText = 16 << 10

// CommandLine returns a display copy of a free-form command line's words.
// A word containing whitespace (the script of sh -c '...') is re-scanned
// as a command line itself. The input is not modified.
func CommandLine(words []string) []string { return commandLine(words, 0) }

func commandLine(words []string, depth int) []string {
	out := make([]string, len(words))
	valueNext := false
	for i, w := range words {
		w = Sanitize(w)
		if valueNext && strings.HasPrefix(w, "--") {
			valueNext = false // a flag, not a value: judge it on its own
		}
		switch {
		case valueNext:
			out[i] = flagValue(w)
			valueNext = false
		case strings.ContainsAny(w, " \t\n"):
			out[i] = freeText(w, depth+1, false)
		case secretFlag(w):
			out[i] = w
			valueNext = true
		default:
			if name, val, ok := strings.Cut(w, "="); ok && name != "" {
				out[i] = Text(name) + "=" + Value(name, val)
			} else {
				out[i] = Text(w)
			}
		}
	}
	return out
}

// FreeText masks a free-form command line given as one string, for
// display of a finished value: input that does not parse (an open quote, a
// trailing backslash) is masked whole. Input that needs no masking is
// returned as typed.
func FreeText(s string) string { return freeText(s, 0, false) }

// FreeTextLive is FreeText for text still being typed: an open quote or
// trailing backslash is completed tentatively, so the words after a secret
// flag are still found while the rest stays readable.
func FreeTextLive(s string) string { return freeText(s, 0, true) }

func freeText(s string, depth int, live bool) string {
	if strings.TrimSpace(s) == "" {
		return Sanitize(s)
	}
	if depth > maxNest || len(s) > maxFreeText {
		return Mask
	}
	tails := []string{""}
	if live {
		tails = append(tails, `"`, "'", " ")
	}
	for _, tail := range tails {
		w, err := shellwords.Split(s + tail)
		if err != nil {
			continue
		}
		m := commandLine(w, depth)
		if tail == "" && strings.Join(m, "\x00") == strings.Join(w, "\x00") {
			return Text(s)
		}
		for i, x := range m {
			if strings.ContainsAny(x, " \t'\"") {
				m[i] = "'" + strings.ReplaceAll(x, "'", `'\''`) + "'"
			}
		}
		return Text(strings.Join(m, " "))
	}
	return Mask
}
