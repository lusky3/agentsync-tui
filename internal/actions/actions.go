// Package actions builds validated argv for every agentsync mutation.
//
// All functions are pure: they perform no I/O and never execute anything.
// An Action's Argv holds only the subcommand and its own flags (for example
// ["mcp","enable","x"]); the runner adds the binary name and the global
// --no-input/--color flags.
//
// There is deliberately NO "secret get" builder. Secret values must never be
// read back into the TUI, so the only secret actions are Set and Edit (which
// hand the terminal to agentsync's own prompt) and Remove.
package actions

import (
	"regexp"
	"strings"

	"github.com/lusky3/agentsync-tui/internal/mask"
)

// Action is one agentsync invocation plus the metadata the UI needs to
// decide how to confirm and run it.
type Action struct {
	Title       string
	Argv        []string
	Destructive bool
	Interactive bool
	Network     bool
	// ReadOnly actions change nothing; the UI skips the confirm modal for
	// them but still logs them.
	ReadOnly bool
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// ValidID reports whether s is a safe server/agent/plugin identifier.
func ValidID(s string) bool {
	return len(s) <= 128 && idRe.MatchString(s)
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// Display returns the masked, POSIX shell-quoted command line shown to the
// user. It is never used for execution.
func (a Action) Display() string {
	parts := []string{"agentsync"}
	for _, s := range mask.Args(a.Argv) {
		if shellSafe.MatchString(s) {
			parts = append(parts, s)
		} else {
			parts = append(parts, "'"+strings.ReplaceAll(s, "'", `'\''`)+"'")
		}
	}
	return strings.Join(parts, " ")
}
