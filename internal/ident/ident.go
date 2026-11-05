// Package ident is the one definition of a safe identifier (server, agent,
// plugin, marketplace, secret key, library item): it is a leaf package so
// actions, model and source can all share it (actions.ValidID is its public
// face; model cannot import actions because of the actions→mask→model chain).
package ident

import "regexp"

// MaxLen is the longest identifier accepted.
const MaxLen = 128

// re is the identifier shape: a letter or digit, then letters, digits, '.',
// '_' or '-'. A leading '-' (a flag), '/' or '..' (a path) can never match.
var re = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Valid reports whether s is a safe identifier of at most MaxLen bytes.
func Valid(s string) bool {
	return len(s) <= MaxLen && re.MatchString(s)
}
