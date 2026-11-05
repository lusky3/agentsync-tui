package mask

import (
	"strings"
	"unicode/utf8"
)

// Sanitize makes s safe to print to a terminal. It removes C0/C1 control
// characters (except \n and \t), DEL, whole ANSI CSI/OSC/DCS-style escape
// sequences, and bidi/zero-width format runes. \r\n becomes \n and a lone \r
// is dropped. Invalid UTF-8 becomes U+FFFD.
func Sanitize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b.WriteRune(utf8.RuneError)
			i++
			continue
		}
		i += size
		switch {
		case r == 0x1b:
			i = skipEscape(s, i)
		case r == 0x9b: // 8-bit CSI
			i = skipCSI(s, i)
		case r == 0x90 || r == 0x98 || r == 0x9d || r == 0x9e || r == 0x9f: // DCS SOS OSC PM APC
			i = skipString(s, i)
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			// dropped (includes \r)
		case isFormat(r):
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isFormat(r rune) bool {
	return (r >= 0x202a && r <= 0x202e) || (r >= 0x2066 && r <= 0x2069) ||
		(r >= 0x200b && r <= 0x200f) || r == 0xfeff
}

// skipEscape is called just after an ESC and returns the index after the
// whole sequence.
func skipEscape(s string, i int) int {
	if i >= len(s) {
		return i
	}
	switch s[i] {
	case '[':
		return skipCSI(s, i+1)
	case ']', 'P', 'X', '^', '_':
		return skipString(s, i+1)
	}
	// Two-byte (or nF) escape: ESC, intermediates 0x20-0x2f, final byte.
	for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
		i++
	}
	if i < len(s) && s[i] >= 0x30 && s[i] <= 0x7e {
		i++
	}
	return i
}

// skipCSI consumes parameter bytes, intermediates and the final byte.
func skipCSI(s string, i int) int {
	for i < len(s) && s[i] >= 0x30 && s[i] <= 0x3f {
		i++
	}
	for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
		i++
	}
	if i < len(s) && s[i] >= 0x40 && s[i] <= 0x7e {
		i++
	}
	return i
}

// skipString consumes a string-type sequence (OSC etc.) up to and including
// its terminator (BEL, ESC \, or U+009C). An unterminated one swallows the
// rest of the input.
func skipString(s string, i int) int {
	for i < len(s) {
		switch {
		case s[i] == 0x07:
			return i + 1
		case s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\':
			return i + 2
		case strings.HasPrefix(s[i:], "\u009c"):
			return i + 2
		}
		i++
	}
	return i
}
