package model

import (
	"errors"
	"regexp"
	"strings"
)

// secretKeyRe is the key charset (the same rule as actions.ValidID).
var secretKeyRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// IsSecretKey reports whether s is a safe secret key name.
func IsSecretKey(s string) bool { return secretKeyRe.MatchString(s) }

// IsBackendNotConfigured reports whether text is the message `secret list`
// prints (exit 1, stderr) when [secrets] has no backend.
func IsBackendNotConfigured(text string) bool {
	return strings.Contains(text, `set backend = "age"`)
}

// headerWords are first tokens of header/notice lines, never keys.
var headerWords = map[string]bool{
	"key": true, "keys": true, "name": true, "secret": true, "secrets": true,
	"warn": true, "error": true, "info": true,
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
// Only the first token of each line is considered, cut at the first '=' or
// ':' so that a line shaped like KEY=VALUE or KEY: VALUE can never yield
// anything but the key; trailing columns are ignored. Tokens that are not
// valid key names (control characters, ANSI, bidi, a leading '-', '../'),
// header words, parenthesised notes and blank lines are dropped. Non-blank
// input that yields no key and holds no note is an error.
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
		tok := strings.Fields(line)[0]
		if i := strings.IndexAny(tok, "=:"); i >= 0 {
			tok = tok[:i]
		}
		if headerWords[strings.ToLower(tok)] || !secretKeyRe.MatchString(tok) {
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
