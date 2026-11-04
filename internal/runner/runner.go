// Package runner executes the agentsync CLI and reports its results.
package runner

import (
	"bytes"
	"context"
	"errors"
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
}

// NewExec returns an Exec for bin. projectDir is used only with ScopeProject.
func NewExec(bin string, scope Scope, projectDir string) *Exec {
	return &Exec{bin: bin, scope: scope, projectDir: projectDir}
}

func (e *Exec) scopeArgs() []string {
	if e.scope == ScopeProject {
		return []string{"--project", e.projectDir}
	}
	return []string{"--scope", "user"}
}

// Run executes agentsync with the scope flags followed by the global
// non-interactive flags: args --scope user --no-input --color never.
func (e *Exec) Run(ctx context.Context, args ...string) (Result, error) {
	full := append(append(append([]string{}, args...), e.scopeArgs()...), "--no-input", "--color", "never")
	cmd := exec.CommandContext(ctx, e.bin, full...)
	cmd.WaitDelay = time.Second
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
	return exec.Command(e.bin, append(append([]string{}, args...), e.scopeArgs()...)...)
}
