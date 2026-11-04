// Package runner executes the agentsync CLI and reports its results.
package runner

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"sync/atomic"
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

// MaxStream caps what Run keeps of each of stdout and stderr, in bytes; a
// stream that was cut ends with TruncatedMark. The screens store far less
// (ui.MaxOutput) after masking, so this bounds the masking cost, not what
// is shown.
const MaxStream = 4 << 20

// TruncatedMark ends a stream that MaxStream cut.
const TruncatedMark = "\n[agentsync-tui: output truncated at 4 MiB]"

// waitDelay is the effective SIGINT-to-SIGKILL grace: a zero or negative
// grace (a zero Exec) falls back to InterruptGrace so a child that ignores
// SIGINT is still killed.
func (e *Exec) waitDelay() time.Duration {
	if e.grace <= 0 {
		return InterruptGrace
	}
	return e.grace
}

// capWriter keeps the first n bytes written and notes whether more came.
type capWriter struct {
	buf       bytes.Buffer
	n         int
	truncated bool
}

func (w *capWriter) Write(p []byte) (int, error) {
	n := len(p)
	if room := w.n - w.buf.Len(); n > room {
		w.truncated = true
		p = p[:max(room, 0)]
	}
	w.buf.Write(p)
	return n, nil // the excess is consumed, never reported short
}

// String returns what was kept, with TruncatedMark when anything was cut.
func (w *capWriter) String() string {
	if w.truncated {
		return w.buf.String() + TruncatedMark
	}
	return w.buf.String()
}

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
	setProcessGroup(cmd) // signals reach the helpers agentsync spawns too
	grace := e.waitDelay()
	// On cancel, interrupt rather than kill: a write such as `plugin upgrade
	// --all` then gets the chance to finish or roll back. Only if it is
	// still running grace later is it killed: the whole group by the timer,
	// agentsync itself and its pipes by WaitDelay. agentsync v0.17.0
	// installs no SIGINT handler outside `secret edit`, so today it exits at
	// once on SIGINT, much as on SIGKILL: its single-file writes are atomic
	// and its flock dies with it, but a multi-file run can still stop
	// halfway. Where SIGINT cannot be delivered, kill at once.
	var killTimer atomic.Pointer[time.Timer]
	cmd.Cancel = func() error {
		if err := signalGroup(cmd, os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return signalGroup(cmd, os.Kill)
		}
		killTimer.Store(time.AfterFunc(grace, func() { _ = signalGroup(cmd, os.Kill) }))
		return nil
	}
	cmd.WaitDelay = grace
	cmd.Env = e.Env
	out, errb := &capWriter{n: MaxStream}, &capWriter{n: MaxStream}
	cmd.Stdout, cmd.Stderr = out, errb
	err := cmd.Run()
	if t := killTimer.Load(); t != nil {
		t.Stop()
	}
	res := Result{Stdout: out.String(), Stderr: errb.String()}
	if ctx.Err() != nil {
		_ = signalGroup(cmd, os.Kill) // nothing of a cancelled run survives it
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
