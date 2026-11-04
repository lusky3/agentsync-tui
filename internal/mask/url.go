package mask

import (
	"regexp"
	"strings"
	"unicode"
)

// urlRe finds a URL in free text. Closers that belong to the surrounding
// prose (a trailing '.', ',' or ')') are given back by maskURL.
var urlRe = regexp.MustCompile(`(?i)\b[a-z][a-z0-9+.\-]*://[^\s"'<>]+`)

// maskURL returns a URL with its credential-bearing parts masked: the
// userinfo, every query value (keys stay), every fragment value, and any
// path segment that is TokenShaped. The host, ordinary path segments and
// query keys stay, so the user can still tell which server a command names;
// a value that is only references stays visible.
func maskURL(u string) string {
	keep := trimClosers(u)
	tail := u[len(keep):]
	i := strings.Index(keep, "://")
	if i < 0 {
		return u
	}
	scheme, rest := keep[:i+3], keep[i+3:]
	authEnd := strings.IndexAny(rest, "/?#")
	if authEnd < 0 {
		authEnd = len(rest)
	}
	authority, rest := rest[:authEnd], rest[authEnd:]
	if at := strings.LastIndex(authority, "@"); at >= 0 && !onlyRefs(authority[:at]) {
		authority = Mask + authority[at:]
	}
	var b strings.Builder
	b.WriteString(scheme)
	b.WriteString(authority)
	// path
	end := strings.IndexAny(rest, "?#")
	if end < 0 {
		end = len(rest)
	}
	path, rest := rest[:end], rest[end:]
	segs := strings.Split(path, "/")
	for j, seg := range segs {
		if TokenShaped(seg) {
			segs[j] = Mask
		}
	}
	b.WriteString(strings.Join(segs, "/"))
	// query
	if strings.HasPrefix(rest, "?") {
		end = strings.Index(rest, "#")
		if end < 0 {
			end = len(rest)
		}
		b.WriteString("?" + maskPairs(rest[1:end]))
		rest = rest[end:]
	}
	// fragment
	if strings.HasPrefix(rest, "#") {
		frag := rest[1:]
		switch {
		case strings.Contains(frag, "="):
			frag = maskPairs(frag)
		case TokenShaped(frag):
			frag = Mask
		}
		b.WriteString("#" + frag)
	}
	return b.String() + tail
}

// trimClosers drops trailing punctuation that belongs to the surrounding
// prose: '.', ',', ';', ':' always, and a closing bracket only when the URL
// holds more closers than openers of its kind (so the '}' of a trailing
// ${ref} stays).
func trimClosers(u string) string {
	pairs := map[byte]byte{')': '(', ']': '[', '}': '{', '>': '<'}
	for len(u) > 0 {
		c := u[len(u)-1]
		switch {
		case strings.IndexByte(".,;:", c) >= 0:
		case pairs[c] != 0 && strings.Count(u, string(c)) > strings.Count(u, string(pairs[c])):
		default:
			return u
		}
		u = u[:len(u)-1]
	}
	return u
}

// maskPairs masks the value of every key=value pair in a query or fragment
// string ('&' or ';' separated); keys, bare words and reference-only values
// stay.
func maskPairs(q string) string {
	var out strings.Builder
	for i := 0; i < len(q); {
		j := strings.IndexAny(q[i:], "&;")
		if j < 0 {
			j = len(q)
		} else {
			j += i
		}
		pair := q[i:j]
		if k, v, ok := strings.Cut(pair, "="); ok && v != "" && !onlyRefs(v) {
			pair = k + "=" + Mask
		}
		out.WriteString(pair)
		if j < len(q) {
			out.WriteByte(q[j])
		}
		i = j + 1
	}
	return out.String()
}

// TokenShaped reports whether s reads like a secret value rather than a
// name or an ordinary path segment. It is a documented heuristic, shared by
// the secrets screen's key field (a value typed there would become a key
// name) and the URL path rule, and it errs toward the value side:
//
//   - a run with no separators ('_', '-', '.') of 16+ characters mixing
//     letters and two or more digits, or of 12+ characters with four or
//     more digits (hex keys, opaque ids, base64 tokens);
//   - 16+ hexadecimal characters once '-' is removed (UUIDs, hex keys);
//   - a string of 20+ characters holding a digit, upper- and lower-case
//     letters and a lower-to-upper transition inside a word (camelCase
//     "words" of a base64-style token), which no SNAKE_CASE, kebab-case or
//     dotted name has.
//
// Names such as GITHUB_TOKEN, s3_bucket_2024, Slack-Bot-Token-2024, v0.1.0
// or agentsync-tui_0.1.0_linux_amd64.tar.gz are not token-shaped.
func TokenShaped(s string) bool {
	if len(s) < 12 {
		return false
	}
	if hexRun(strings.ReplaceAll(s, "-", "")) {
		return true
	}
	var digits, upper, lower int
	camel := false
	for _, w := range strings.FieldsFunc(s, func(r rune) bool { return r == '_' || r == '-' || r == '.' }) {
		var wd, wl int
		prevLower := false
		for _, r := range w {
			switch {
			case unicode.IsDigit(r):
				wd++
			case unicode.IsUpper(r):
				wl++
				upper++
				if prevLower {
					camel = true
				}
			case unicode.IsLetter(r):
				wl++
				lower++
			}
			prevLower = unicode.IsLower(r)
		}
		digits += wd
		if wl > 0 && (len(w) >= 16 && wd >= 2 || len(w) >= 12 && wd >= 4) {
			return true
		}
	}
	return len(s) >= 20 && digits > 0 && upper > 0 && lower > 0 && camel
}

// hexRun reports whether s is 16+ hexadecimal characters with at least one
// digit.
func hexRun(s string) bool {
	if len(s) < 16 {
		return false
	}
	digit := false
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return digit
}
