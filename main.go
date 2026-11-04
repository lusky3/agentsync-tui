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
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"syscall"

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
		"bound on network reads (import previews, plugin outdated); 0 = none. Local reads (lists, status, diff, explain, doctor, check) are bounded at min(30s, this)")
	debugLog := fs.String("debug-log", "",
		"append a masked sample (4 KiB per stream, parse error, exit code) of every read whose output could not be parsed to this file, for bug reports; off by default")
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
	if *debugLog != "" {
		abs, err := filepath.Abs(*debugLog)
		if err == nil {
			err = ui.SetDebugLog(abs)
		}
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "agentsync-tui: --debug-log:", err)
			return 2
		}
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
		AgentsyncHome: agentsyncHome(os.Getenv),
		Scope:         scope,
		ProjectDir:    projectDir,
		LogPath:       logPath(),
		Timeouts:      &ui.Timeouts{Read: *readTimeout, Write: *writeTimeout},
		WithScope:     withScope,
	}
	// The initial runner is the same pair a scope switch would build.
	deps.Runner, deps.Interactive = withScope(scope)
	a := ui.NewApp(deps, app.Screens(deps)...)
	// Filter logs a run still in flight as abandoned when Bubble Tea ends the
	// program on SIGTERM (QuitMsg) or an off-TTY SIGINT (InterruptMsg).
	// SIGHUP (terminal closed) would otherwise end the process at once with
	// no record, so it is turned into the same QuitMsg.
	p := tea.NewProgram(a, tea.WithFilter(a.Filter))
	hup, done := make(chan os.Signal, 1), make(chan struct{})
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		select {
		case <-hup:
			p.Send(tea.QuitMsg{})
		case <-done:
		}
	}()
	// Run restores the terminal (alt screen, raw mode) before returning,
	// including after a recovered panic.
	_, err = p.Run()
	signal.Stop(hup)
	close(done)
	if err != nil {
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

// agentsyncHome is the user-scope canonical tree, resolved exactly as the
// CLI's paths.AgentsyncHome (v0.17.0) does, so the TUI reads and edits the
// tree the commands it runs operate on:
//  1. $AGENTSYNC_TARGET_ROOT/.agentsync: a redirected run is a sandbox and
//     the user-facing override below is ignored (the CLI's test knob);
//  2. $AGENTSYNC_HOME;
//  3. $HOME/.agentsync.
//
// The project tree is <project>/.agentsync whatever the environment, as it
// is for the CLI's --project.
func agentsyncHome(getenv func(string) string) string {
	if root := getenv("AGENTSYNC_TARGET_ROOT"); root != "" {
		return filepath.Join(root, ".agentsync")
	}
	if h := getenv("AGENTSYNC_HOME"); h != "" {
		return h
	}
	if home := getenv("HOME"); home != "" {
		return filepath.Join(home, ".agentsync")
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
