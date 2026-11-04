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
		} else if !ok && TokenShaped(pair) {
			pair = Mask // a bare token (?<secret>), as in a bare fragment
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
// name) and the URL path and query rules, and it errs toward the value side:
//
//   - a run with no separators ('_', '-', '.') of 12+ characters, with
//     letters and two or more digits, whose letters and digits alternate
//     five or more times (x1f3a9c2d7e8b1, T0A1B2C3D4E5F6, base64 with
//     digits sprinkled through) or that is a third digits or more
//     (CANARYbare1234567890abcd); a name keeps a few digits together
//     (MyServiceApiKey2024Prod, S3Bucket2024Backup);
//   - 16+ hexadecimal characters once '-' is removed (UUIDs, hex keys);
//   - a string of 20+ characters holding a digit whose letters change from
//     lower to upper case at least three times and at a quarter of the
//     letters or more (the two-letter "words" of a base64 token, AbCdEf);
//     CamelCase names have longer humps (MyServiceApiKey2024Prod).
//
// Names such as GITHUB_TOKEN, s3_bucket_2024, Slack-Bot-Token-2024,
// MyServiceApiKey2024Prod, v0.1.0 or agentsync-tui_0.1.0_linux_amd64.tar.gz
// are not token-shaped.
func TokenShaped(s string) bool {
	if len(s) < 12 {
		return false
	}
	if hexRun(strings.ReplaceAll(s, "-", "")) {
		return true
	}
	var digits, letters, humps int
	for _, w := range strings.FieldsFunc(s, func(r rune) bool { return r == '_' || r == '-' || r == '.' }) {
		var wd, wl, alternations int
		prevLower, prevDigit := false, false
		for i, r := range w {
			isDigit := unicode.IsDigit(r)
			switch {
			case isDigit:
				wd++
			case unicode.IsUpper(r):
				wl++
				if prevLower {
					humps++
				}
			case unicode.IsLetter(r):
				wl++
			}
			if i > 0 && (unicode.IsLetter(r) || isDigit) && isDigit != prevDigit {
				alternations++
			}
			prevLower, prevDigit = unicode.IsLower(r), isDigit
		}
		digits += wd
		letters += wl
		if len(w) >= 12 && wl > 0 && wd >= 2 && (alternations >= 5 || 3*wd >= len(w)) {
			return true
		}
	}
	return len(s) >= 20 && digits > 0 && humps >= 3 && humps*4 >= letters
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
