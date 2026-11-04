package model

import (
	"errors"
	"strings"

	"github.com/lusky3/agentsync-tui/internal/ident"
)

// IsSecretKey reports whether s is a safe secret key name (the shared
// identifier rule, ident.Valid).
func IsSecretKey(s string) bool { return ident.Valid(s) }

// IsBackendNotConfigured reports whether text is the message `secret list`
// prints (exit 1, stderr) when [secrets] has no backend.
func IsBackendNotConfigured(text string) bool {
	return strings.Contains(text, `set backend = "age"`)
}

// ParseSecretKeys parses `agentsync secret list` (plain text; one KEY per
// line, in the CLI's order; never values).
//
// Formats seen with agentsync (dev build, 2026-11), hermetic home:
//
//	API_KEY                                                  (configured vault)
//	GITHUB_TOKEN
//	(vault is empty; add one with `agentsync secret set <key>`)   (empty vault)
//
// A fresh home exits 1 with `set backend = "age" in agentsync.toml
// [secrets]` on stderr (see IsBackendNotConfigured), not on stdout.
//
// The CLI prints exactly one key per line, so a line with more than one
// whitespace-separated field is prose (a header row, "No secrets stored")
// and never yields a key. A single token is cut at the first '=' or ':' so
// that a line shaped like KEY=VALUE or KEY:VALUE can never yield anything
// but the key. Tokens that are not valid key names (control characters,
// ANSI, bidi, a leading '-', '../'), a bare "Heading:" line, parenthesised
// notes and blank lines are dropped. Non-blank input that yields no key and
// holds no note is an error, so unreadable output is reported rather than
// shown as an empty or junk list.
func ParseSecretKeys(stdout string) ([]string, error) {
	var keys []string
	recognised, nonBlank := false, false
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		nonBlank = true
		if strings.HasPrefix(line, "(") {
			recognised = true
			continue
		}
		f := strings.Fields(line)
		if len(f) != 1 || strings.HasSuffix(f[0], ":") {
			continue // prose, or a "Keys:" style heading with nothing after it
		}
		tok := f[0]
		if i := strings.IndexAny(tok, "=:"); i >= 0 {
			tok = tok[:i]
		}
		if !ident.Valid(tok) {
			continue
		}
		keys = append(keys, tok)
		recognised = true
	}
	if nonBlank && !recognised {
		return nil, errors.New("secret list: no keys in output")
	}
	return keys, nil
}
