// Package argtok is the one rule for a single argv token that a discovery
// source places after a package or image reference (a registry package
// argument, a Docker catalog entry's fixed command). Such a token reaches
// the server's own argv through `agentsync mcp add --args`, so it may be a
// flag, but it must never be able to change argument boundaries or mean
// something to a shell if one is ever involved downstream.
//
// It is a leaf package (stdlib only) so both mcpreg and dockercat can use
// it without importing each other.
package argtok

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxLen is the longest token accepted, in bytes.
const MaxLen = 1024

// forbidden are the shell metacharacters a token may never hold. Commas are
// allowed: actions.McpAdd escapes them.
const forbidden = "`;&|<>$\\()'\"*?[]{}!#~"

var (
	// flagRe is a bare flag: -f or --flag, the name [A-Za-z0-9][A-Za-z0-9_-]*.
	flagRe = regexp.MustCompile(`^--?[A-Za-z0-9][A-Za-z0-9_-]*$`)
	// flagValueRe is --flag=value with a non-empty value.
	flagValueRe = regexp.MustCompile(`^--[A-Za-z0-9][A-Za-z0-9_-]*=.+$`)
)

// Valid reports whether s is a safe argv token: non-empty, at most MaxLen
// bytes, valid UTF-8, no whitespace, control or other non-printing
// character (NUL, bidi and zero-width included), none of the shell
// metacharacters `;&|<>$\()'"*?[]{}!#~, and either a flag (-f, --flag,
// --flag=value) or a plain token that does not start with '-'.
func Valid(s string) bool {
	if s == "" || len(s) > MaxLen || !utf8.ValidString(s) || strings.ContainsAny(s, forbidden) {
		return false
	}
	for _, r := range s {
		if r == ' ' || !unicode.IsPrint(r) {
			return false
		}
	}
	if !strings.HasPrefix(s, "-") {
		return true
	}
	return flagRe.MatchString(s) || flagValueRe.MatchString(s)
}

// Flag reports whether s is a bare flag (-f or --flag, no =value), such as
// the name of a named argument whose value follows as the next token.
func Flag(s string) bool {
	return flagRe.MatchString(s)
}
