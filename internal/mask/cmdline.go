package mask

import (
	"strings"

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

// secretFlag reports whether w is a flag whose next word is a secret.
func secretFlag(w string) bool {
	if !strings.HasPrefix(w, "-") || strings.Contains(w, "=") {
		return false
	}
	name := strings.ToLower(strings.TrimLeft(w, "-"))
	if name == "" {
		return false
	}
	for _, p := range longSecretParts {
		if strings.Contains(name, p) {
			return true
		}
	}
	for _, seg := range strings.FieldsFunc(name, func(r rune) bool { return r == '-' || r == '_' }) {
		if shortSecretSegs[seg] {
			return true
		}
	}
	return false
}

// CommandLine returns a display copy of a free-form command line's words.
// The input is not modified.
func CommandLine(words []string) []string {
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
		case secretFlag(w):
			out[i] = w
			valueNext = true
		default:
			if name, val, ok := strings.Cut(w, "="); ok && name != "" && !strings.ContainsAny(name, " \t") {
				out[i] = Text(name) + "=" + Value(name, val)
			} else {
				out[i] = Text(w)
			}
		}
	}
	return out
}

// FreeText masks a free-form command line typed as one string. Input with
// an open quote or a trailing backslash is completed tentatively so the
// words after a secret flag are still found; if nothing parses the whole
// string is masked. Input that needs no masking is returned as typed.
func FreeText(s string) string {
	for _, tail := range []string{"", `"`, "'", " "} {
		w, err := shellwords.Split(s + tail)
		if err != nil {
			continue
		}
		m := CommandLine(w)
		if tail == "" && strings.Join(m, "\x00") == strings.Join(w, "\x00") {
			return Text(s)
		}
		return strings.Join(m, " ")
	}
	if strings.TrimSpace(s) == "" {
		return Sanitize(s)
	}
	return Mask
}
