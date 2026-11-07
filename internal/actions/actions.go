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
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/lusky3/agentsync-tui/internal/ident"
	"github.com/lusky3/agentsync-tui/internal/mask"
)

// ProgramGit is the Program of an Action that runs git (D1). Its Argv
// starts at `-C <toplevel>`: the runner adds the fixed hardening pre-args.
const ProgramGit = "git"

// Action is one agentsync (or, with Program set, git) invocation plus the
// metadata the UI needs to decide how to confirm and run it.
type Action struct {
	// Program is the binary the Action runs: "" for agentsync, ProgramGit
	// for git.
	Program     string
	Title       string
	Argv        []string
	Destructive bool
	Interactive bool
	Network     bool
	// ReadOnly actions change nothing; the UI skips the confirm modal for
	// them but still logs them.
	ReadOnly bool
	// Warning is an optional consequence notice the confirm modal shows
	// prominently under the command. It is sanitised at render time; the
	// zero value means none.
	Warning string
	// TypedConfirm asks the modal for the typed ConfirmVerb, as for a
	// destructive action, but without the destructive badge.
	TypedConfirm bool

	// display, when set by a builder that knows its argv's structure, is a
	// pre-masked copy of Argv that Display renders instead of running the
	// generic mask.Args heuristics over Argv.
	display []string
	// more, when positive, is the number of trailing argv elements display
	// leaves out; Display ends with "… (+more more)".
	more int
}

// ValidID reports whether s is a safe server/agent/plugin identifier. The
// rule itself lives in ident (a leaf package) so model and source share it.
func ValidID(s string) bool { return ident.Valid(s) }

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// ConfirmVerb is the word a user must type to confirm a destructive action:
// "purge" for a purge (so it cannot share the word of a plain disable), the
// subcommand for grouped commands (mcp/agent/plugin/marketplace/secret),
// otherwise the command itself; "" for an empty Argv. For git it is the
// subcommand after the leading `-C <dir>` and `-c k=v` pairs.
func (a Action) ConfirmVerb() string {
	if a.Program == ProgramGit {
		return gitSubcommand(a.Argv)
	}
	if len(a.Argv) == 0 {
		return ""
	}
	if slices.Contains(a.Argv, "--purge") {
		return "purge"
	}
	switch a.Argv[0] {
	case "mcp", "agent", "plugin", "marketplace", "secret":
		if len(a.Argv) > 1 {
			return a.Argv[1]
		}
	}
	return a.Argv[0]
}

// Key is the argv joined with NUL: the identity of an action for matching a
// RunMsg back to the request that caused it.
func (a Action) Key() string { return strings.Join(a.Argv, "\x00") }

// Previewed is a mutation with its dry run: Preview is ReadOnly and carries
// --dry-run, Real is the same command without it. Builders return the pair
// so the preview gate never has to edit argv.
type Previewed struct{ Preview, Real Action }

// Display returns the masked, POSIX shell-quoted command line shown to the
// user. It is never used for execution.
func (a Action) Display() string {
	prog := a.Program
	if prog == "" {
		prog = "agentsync"
	}
	parts := []string{prog}
	shown := mask.Args(a.Argv)
	if a.display != nil {
		shown = make([]string, len(a.display))
		for i, s := range a.display {
			shown[i] = mask.Text(s)
		}
	}
	for _, s := range shown {
		if shellSafe.MatchString(s) {
			parts = append(parts, s)
		} else {
			parts = append(parts, "'"+strings.ReplaceAll(s, "'", `'\''`)+"'")
		}
	}
	if a.more > 0 {
		parts = append(parts, fmt.Sprintf("… (+%d more)", a.more))
	}
	return strings.Join(parts, " ")
}

// gitSubcommand returns the first argv element after the leading `-C <dir>`
// and `-c k=v` pairs, or "" when there is none.
func gitSubcommand(argv []string) string {
	i := 0
	for i+1 < len(argv) && (argv[i] == "-C" || argv[i] == "-c") {
		i += 2
	}
	if i < len(argv) {
		return argv[i]
	}
	return ""
}
