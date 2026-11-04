// Command agentsync-tui is a Bubble Tea TUI that drives the agentsync CLI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/app"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

// version is overridden at build time via -ldflags "-X main.version=...".
var version = "dev"

// resolveVersion returns v unless it is the "dev" default, in which case it
// asks build for the module version (set by `go install ...@vX.Y.Z`).
func resolveVersion(v string, build func() (*debug.BuildInfo, bool)) string {
	if v != "dev" {
		return v
	}
	if bi, ok := build(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return v
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run returns the exit code; every path returns through here so the
// terminal is always restored by tea.Program.Run before the process exits.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("agentsync-tui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "print the version and exit")
	scopeFlag := fs.String("scope", "", "agentsync scope: user or project (default user; --project implies project)")
	project := fs.String("project", "", "project directory (implies --scope project)")
	readTimeout := fs.Duration("read-timeout", ui.DefaultReadTimeout,
		"bound on every background read (lists, status, import previews, plugin outdated); 0 = none")
	writeTimeout := fs.Duration("write-timeout", 0,
		"bound on every write (apply, import, plugin upgrade, ...); 0 = none. A write cut off partway can leave agentsync's state and the destination files out of step")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *readTimeout < 0 || *writeTimeout < 0 {
		_, _ = fmt.Fprintln(stderr, "agentsync-tui: --read-timeout and --write-timeout must not be negative")
		return 2
	}
	if *showVersion {
		_, _ = fmt.Fprintln(stdout, "agentsync-tui "+resolveVersion(version, debug.ReadBuildInfo))
		return 0
	}

	scope, projectDir, err := resolveScope(*scopeFlag, *project)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "agentsync-tui:", err)
		return 2
	}

	// Every background read a screen starts derives from ctx; the shell
	// cancels it on quit so no agentsync child outlives the TUI.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	withScope := func(s runner.Scope) (runner.Runner, func(args ...string) *exec.Cmd) {
		e := runner.NewExec("agentsync", s, projectDir)
		return e, e.Interactive
	}
	deps := ui.Deps{
		Ctx:           ctx,
		Cancel:        cancel,
		AgentsyncHome: agentsyncHome(),
		Scope:         scope,
		ProjectDir:    projectDir,
		LogPath:       logPath(),
		Timeouts:      &ui.Timeouts{Read: *readTimeout, Write: *writeTimeout},
		WithScope:     withScope,
	}
	// The initial runner is the same pair a scope switch would build.
	deps.Runner, deps.Interactive = withScope(scope)
	// Run restores the terminal (alt screen, raw mode) before returning,
	// including after a recovered panic.
	if _, err := tea.NewProgram(ui.NewApp(deps, app.Screens(deps)...)).Run(); err != nil {
		_, _ = fmt.Fprintln(stderr, "agentsync-tui:", err)
		return 1
	}
	return 0
}

// resolveScope applies the flag rules: --project implies project scope unless
// --scope says otherwise; the project path must be an existing directory.
func resolveScope(scopeFlag, project string) (runner.Scope, string, error) {
	var dir string
	if project != "" {
		abs, err := filepath.Abs(project)
		if err != nil {
			return "", "", err
		}
		if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
			return "", "", fmt.Errorf("--project %q is not a directory", project)
		}
		dir = abs
	}
	switch scopeFlag {
	case "":
		if dir != "" {
			return runner.ScopeProject, dir, nil
		}
		return runner.ScopeUser, dir, nil
	case string(runner.ScopeUser), string(runner.ScopeProject):
		return runner.Scope(scopeFlag), dir, nil
	}
	return "", "", fmt.Errorf("--scope must be user or project, got %q", scopeFlag)
}

func agentsyncHome() string {
	if h := os.Getenv("AGENTSYNC_HOME"); h != "" {
		return h
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".agentsync")
	}
	return ""
}

// logPath is $XDG_STATE_HOME/agentsync-tui/activity.log, falling back to
// ~/.local/state; empty (file log disabled) when neither is known.
func logPath() string {
	if s := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(s) {
		return filepath.Join(s, "agentsync-tui", "activity.log")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", "agentsync-tui", "activity.log")
	}
	return ""
}
