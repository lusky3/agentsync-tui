// Package runner executes the agentsync CLI and reports its results.
package runner

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"time"
)

// Scope selects which agentsync tree a command operates on.
type Scope string

const (
	ScopeUser    Scope = "user"
	ScopeProject Scope = "project"
)

// ErrNotInstalled is returned when the agentsync binary cannot be found.
var ErrNotInstalled = errors.New("agentsync is not installed (see https://agentsync.cc for install instructions)")

// Result is the outcome of a completed CLI invocation.
type Result struct {
	Stdout, Stderr string
	ExitCode       int
}

// Runner runs agentsync. The error is non-nil only for spawn failure or
// context cancellation, never for a non-zero exit (see Result.ExitCode).
type Runner interface {
	Run(ctx context.Context, args ...string) (Result, error)
}

// Exec is a Runner backed by the real binary.
type Exec struct {
	bin        string
	scope      Scope
	projectDir string

	// Env, when non-nil, is the child process environment (default: the
	// current process environment). Tests use it for a hermetic home.
	Env []string

	grace time.Duration // SIGINT-to-SIGKILL delay on cancel (InterruptGrace)
}

// InterruptGrace is how long a cancelled run may take to exit after SIGINT
// before it is killed.
const InterruptGrace = 10 * time.Second

// NewExec returns an Exec for bin. projectDir is used only with ScopeProject.
func NewExec(bin string, scope Scope, projectDir string) *Exec {
	return &Exec{bin: bin, scope: scope, projectDir: projectDir, grace: InterruptGrace}
}

// ScopeUnaware reports whether the command in args rejects --scope/--project.
// agentsync's root pre-run refuses those flags on commands marked scope-unaware.
// This mirrors markScopeUnaware/markGroupScopeUnaware at v0.17.0-dev: version,
// doctor, revert, the secret and marketplace groups, the plugin group except
// `plugin upgrade`, and cobra's help and completion. It can drift on upgrade;
// re-verify in agentsync's internal/cli/scope_flags.go and the
// markScopeUnaware/markGroupScopeUnaware/markScopeAware call sites. Exec
// omits the scope flags for these; the shell's confirm modal uses it to say
// "scope: n/a (user-level)".
func ScopeUnaware(args []string) bool {
	if len(args) == 0 {
		return false
	}
	switch args[0] {
	case "version", "doctor", "revert", "secret", "marketplace", "help", "completion":
		return true
	case "plugin":
		return len(args) < 2 || args[1] != "upgrade"
	}
	return false
}

func (e *Exec) scopeArgs(args []string) []string {
	if ScopeUnaware(args) {
		return nil
	}
	if e.scope == ScopeProject {
		return []string{"--project", e.projectDir}
	}
	return []string{"--scope", "user"}
}

// Run executes agentsync with the scope flags followed by the global
// non-interactive flags: args --scope user --no-input --color never.
func (e *Exec) Run(ctx context.Context, args ...string) (Result, error) {
	full := append(append(append([]string{}, args...), e.scopeArgs(args)...), "--no-input", "--color", "never")
	cmd := exec.CommandContext(ctx, e.bin, full...)
	// On cancel, interrupt rather than kill: a write such as `plugin upgrade
	// --all` then gets the chance to finish or roll back. Only if it is
	// still running grace later is it killed (WaitDelay). agentsync v0.17.0
	// installs no SIGINT handler outside `secret edit`, so today it exits at
	// once on SIGINT, much as on SIGKILL: its single-file writes are atomic
	// and its flock dies with it, but a multi-file run can still stop
	// halfway. Where SIGINT cannot be delivered, kill at once.
	cmd.Cancel = func() error {
		if err := cmd.Process.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = e.grace
	if cmd.WaitDelay <= 0 {
		cmd.WaitDelay = InterruptGrace // a zero Exec must still kill eventually
	}
	cmd.Env = e.Env
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	res := Result{Stdout: out.String(), Stderr: errb.String()}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.ExitCode = ee.ExitCode()
	case errors.Is(err, exec.ErrNotFound):
		return res, ErrNotInstalled
	default:
		return res, err
	}
	return res, nil
}

// Interactive builds a command for tea.ExecProcess. It omits --no-input so
// the CLI may prompt, and omits --color (the user's terminal decides); only
// the scope flags are appended.
func (e *Exec) Interactive(args ...string) *exec.Cmd {
	cmd := exec.Command(e.bin, append(append([]string{}, args...), e.scopeArgs(args)...)...)
	cmd.Env = e.Env
	return cmd
}
