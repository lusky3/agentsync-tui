package model

import (
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/lusky3/agentsync-tui/internal/ident"
)

// Item is one entry of a library listing (skill, subagent, command, hook or
// LSP server). Name is a validated identifier; Detail is the rest of the line
// with control characters already excluded (callers still mask it).
type Item struct {
	Name   string
	Detail string
}

// ParseList parses `agentsync skill|subagent|command|hook|lsp list` output.
//
// Formats seen with agentsync (dev build, 2026-11): one item name per line,
// optionally followed by detail tokens (none seen yet):
//
//	greet
//	plan-it
//	(no skills; author one by creating skills/<name>/SKILL.md ...)   (empty list)
//
// Blank lines and parenthesised notes are skipped. A line holding a control
// character, escape sequence, bidi/zero-width rune or invalid UTF-8, or whose
// first token is not a valid identifier (leading '-', '/', '..', too long),
// is dropped. Duplicate names keep the first. Non-blank input from which
// nothing parses and which holds no parenthesised note is an error.
func ParseList(stdout string) ([]Item, error) {
	var out []Item
	seen := map[string]bool{}
	recognised, nonBlank := false, false
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		nonBlank = true
		if hostileLine(line) {
			continue
		}
		f := strings.Fields(line)
		if strings.HasPrefix(f[0], "(") {
			recognised = true
			continue
		}
		if !ident.Valid(f[0]) {
			continue
		}
		recognised = true
		if seen[f[0]] {
			continue
		}
		seen[f[0]] = true
		out = append(out, Item{Name: f[0], Detail: strings.Join(f[1:], " ")})
	}
	if nonBlank && !recognised {
		return nil, errors.New("list: no items in output")
	}
	return out, nil
}

// hostileLine reports whether s holds anything unsafe to print: controls
// (tab excepted), escapes, bidi/zero-width format runes, invalid UTF-8.
func hostileLine(s string) bool {
	if !utf8.ValidString(s) {
		return true
	}
	for _, r := range s {
		switch {
		case r == '\t':
		case r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f:
			return true
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069,
			r >= 0x200b && r <= 0x200f, r == 0xfeff:
			return true
		}
	}
	return false
}
