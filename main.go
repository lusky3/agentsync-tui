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
	"sync"
	"syscall"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/app"
	"github.com/lusky3/agentsync-tui/internal/discover/cache"
	"github.com/lusky3/agentsync-tui/internal/netx"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

// progName names the binary in flag errors, the version line and messages,
// and is the directory of its state under $XDG_STATE_HOME.
const progName = "agentsync-tui"

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

// main runs the TUI and exits with run's status.
func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// program is what run needs of a tea.Program.
type program interface {
	Run() (tea.Model, error)
	Send(tea.Msg)
}

// hooks are the process-level pieces run drives, injectable by tests: the
// program, and the runner's shutdown of the children.
type hooks struct {
	newProgram func(*ui.App) program
	shutdown   func(sig os.Signal, readWait, writeWait time.Duration) []runner.Ended
	killAll    func()
	notify     func(chan<- os.Signal, ...os.Signal) // os/signal.Notify
	stop       func(chan<- os.Signal)               // os/signal.Stop
}

var defaultHooks = hooks{
	newProgram: func(a *ui.App) program { return tea.NewProgram(a, tea.WithFilter(a.Filter)) },
	shutdown:   runner.Shutdown,
	killAll:    runner.KillAll,
	notify:     signal.Notify,
	stop:       signal.Stop,
}

// run returns the exit code; every path returns through here so the
// terminal is always restored by tea.Program.Run before the process exits.
func run(args []string, stdout, stderr io.Writer) int {
	return runWith(args, stdout, stderr, defaultHooks)
}

// runWith is run with the process hooks supplied (tests replace them).
func runWith(args []string, stdout, stderr io.Writer, h hooks) int {
	o, exit, ok := parseOptions(args, stderr)
	if !ok {
		return exit
	}
	if o.showVersion {
		_, _ = fmt.Fprintln(stdout, progName+" "+resolveVersion(version, debug.ReadBuildInfo))
		return 0
	}

	scope, projectDir, err := resolveScope(o.scope, o.project)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, progName+":", err)
		return 2
	}

	// Every background read a screen starts derives from ctx; the shell
	// cancels it on quit so no agentsync child outlives the TUI.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	deps := buildDeps(ctx, cancel, o, scope, projectDir)
	a := ui.NewApp(deps, app.Screens(deps)...)
	if err := runProgram(a, h); err != nil {
		_, _ = fmt.Fprintln(stderr, progName+":", err)
		return 1
	}
	return 0
}

// buildDeps wires the screens' dependencies. Git is always the hardened
// git Program (runner.NewGit: its pre-args carry --literal-pathspecs, so no
// other runner may stand in for it); a missing git binary is reported by
// the Source repo tab. Chezmoi is wired only when chezmoi is on PATH, at
// the path found there.
func buildDeps(ctx context.Context, cancel context.CancelFunc, o options, scope runner.Scope, projectDir string) ui.Deps {
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
		Timeouts:      &ui.Timeouts{Read: o.readTimeout, Write: o.writeTimeout},
		WithScope:     withScope,
		Git:           runner.NewGit("git"),
	}
	if path, err := exec.LookPath("chezmoi"); err == nil {
		deps.Chezmoi = runner.NewChezmoi(path)
	}
	deps.Discover = buildDiscover(o)
	// The initial runner is the same pair a scope switch would build.
	deps.Runner, deps.Interactive = withScope(scope)
	return deps
}

// buildDiscover wires discovery without touching the network or the disk:
// the client only dials on an explicit search or install, and the cache
// and staging directories are created on first use. --offline leaves it
// with no client, cache or staging directory. When the user cache
// directory is unknown, discovery runs uncached and cannot stage installs.
func buildDiscover(o options) ui.Discover {
	if o.offline {
		return ui.Discover{Offline: true}
	}
	d := ui.Discover{HTTP: netx.New(discoverNetx(o))}
	if dir, err := cache.DefaultDir(); err == nil {
		d.Cache = ui.NewLazyCache(dir)
		d.Stage = filepath.Join(dir, "stage")
	}
	return d
}

// discoverNetx are the discovery client's options: the four allowed hosts,
// an honest User-Agent, and --read-timeout, which keeps the 2m default at 0
// (a discovery request is always bounded, like network git).
func discoverNetx(o options) netx.Options {
	timeout := o.readTimeout
	if timeout <= 0 {
		timeout = ui.DefaultReadTimeout
	}
	return netx.Options{
		AllowedHosts: ui.DiscoverHosts(),
		UserAgent:    netx.DefaultUserAgent(resolveVersion(version, debug.ReadBuildInfo)),
		Timeout:      timeout,
	}
}

// options are the command-line settings runWith acts on.
type options struct {
	showVersion, offline      bool
	scope, project            string
	readTimeout, writeTimeout time.Duration
}

// parseOptions parses and checks the flags and opens the --debug-log file.
// When ok is false the program ends with exit: 0 after -h, 2 for a bad
// flag or value (already reported on stderr).
func parseOptions(args []string, stderr io.Writer) (o options, exit int, ok bool) {
	fs := flag.NewFlagSet(progName, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&o.showVersion, "version", false, "print the version and exit")
	fs.StringVar(&o.scope, "scope", "", "agentsync scope: user or project (default user; --project implies project)")
	fs.StringVar(&o.project, "project", "", "project directory (implies --scope project)")
	fs.DurationVar(&o.readTimeout, "read-timeout", ui.DefaultReadTimeout,
		"bound on network reads (import previews, plugin outdated, git fetch/pull/push, discovery requests); 0 = none, except network git (fetch, pull, push) and discovery requests, which keep the 2m default. Local reads (lists, status, diff, explain, doctor, check) are bounded at min(30s, this)")
	fs.BoolVar(&o.offline, "offline", false,
		"disable discovery: no requests to the MCP Registry, the Docker MCP Catalog, skills.sh or GitHub archives (the discovery screens say so)")
	debugLog := fs.String("debug-log", "",
		"append a masked sample (4 KiB per stream, parse error, exit code) of every read whose output could not be parsed to this file, for bug reports; off by default")
	fs.DurationVar(&o.writeTimeout, "write-timeout", 0,
		"bound on every write (apply, import, plugin upgrade, git add and commit, ...); 0 = none, except git add and commit, which keep a 2m bound. An agentsync write cut off partway can leave agentsync's state and the destination files out of step")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return o, 0, false
		}
		return o, 2, false
	}
	if o.readTimeout < 0 || o.writeTimeout < 0 {
		_, _ = fmt.Fprintln(stderr, progName+": --read-timeout and --write-timeout must not be negative")
		return o, 2, false
	}
	if *debugLog != "" {
		abs, err := filepath.Abs(*debugLog)
		if err == nil {
			err = ui.SetDebugLog(abs)
		}
		if err != nil {
			_, _ = fmt.Fprintln(stderr, progName+": --debug-log:", err)
			return o, 2, false
		}
	}
	return o, 0, true
}

// runProgram runs the TUI until it ends, then ends the agentsync children
// still running (endChildren) and logs their outcomes. It returns Run's
// error.
//
// Filter logs a run still in flight as abandoned when Bubble Tea ends the
// program on SIGTERM (QuitMsg) or an off-TTY SIGINT (InterruptMsg).
// SIGHUP (terminal closed) would otherwise end the process at once with
// no record, so it is turned into the same QuitMsg. The signal that ends
// the TUI is remembered and, once Run has returned, forwarded to every
// agentsync child still running (runner.Shutdown): a read was already
// cancelled with SIGINT and gets a short wait, a write abandoned to
// SIGTERM or SIGHUP gets the interrupt grace, then the group is killed,
// so nothing is orphaned.
// SIGINT, SIGTERM and SIGHUP are caught from before the program runs
// until the children are ended (h.stop below): a ctrl+c once the
// terminal is back in cooked mode is a real SIGINT and must escalate
// the shutdown, not end the process through Go's default handler. While
// the TUI runs in raw mode a ctrl+c is a key press that Bubble Tea
// handles; a real SIGINT (kill -INT) is also seen by Bubble Tea's own
// handler, which ends the program (InterruptMsg), so here it is only
// noted as the signal to forward.
func runProgram(a *ui.App, h hooks) error {
	sigs, done := make(chan os.Signal, 4), make(chan struct{})
	h.notify(sigs, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	p := h.newProgram(a)
	var exit lastSignal
	go func() {
		for {
			select {
			case s := <-sigs:
				exit.note(s)
				if s == syscall.SIGHUP {
					p.Send(tea.QuitMsg{})
				}
			case <-done:
				return
			}
		}
	}()
	// Run restores the terminal (alt screen, raw mode) before returning,
	// including after a recovered panic.
	_, err := p.Run()
	// The forwarding goroutine stops; endChildren drains the channel, which
	// stays registered until the children are gone.
	close(done)
	select { // a signal delivered as Run was returning
	case s := <-sigs:
		exit.note(s)
	default:
	}
	a.RecordShutdown(endChildren(exit.get(), sigs, h))
	h.stop(sigs)
	return err
}

// lastSignal is the most recent signal noted while the program ran, shared
// by the signal-watching goroutine and runProgram.
type lastSignal struct {
	mu  sync.Mutex
	sig os.Signal
}

// note records s as the last signal.
func (l *lastSignal) note(s os.Signal) {
	l.mu.Lock()
	l.sig = s
	l.mu.Unlock()
}

// get is the last signal noted, or nil.
func (l *lastSignal) get() os.Signal {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.sig
}

// endChildren ends the agentsync children once the program has returned,
// forwarding sig (the signal that ended the TUI; nil is a quit, whose reads
// were already cancelled with SIGINT) with the short read wait and the
// write grace (runner.Shutdown). A further signal arriving on sigs
// meanwhile, a second ctrl+c or SIGTERM, escalates: every group is killed
// at once (killAll) and the shutdown returns with the outcomes. It never
// returns with a child alive.
func endChildren(sig os.Signal, sigs <-chan os.Signal, h hooks) []runner.Ended {
	if sig == nil {
		sig = os.Interrupt
	}
	done := make(chan []runner.Ended, 1)
	go func() { done <- h.shutdown(sig, readShutdownWait, runner.InterruptGrace) }()
	for {
		select {
		case ended := <-done:
			return ended
		case <-sigs:
			h.killAll()
		}
	}
}

// readShutdownWait is how long a quit waits for the cancelled reads (already
// sent SIGINT) before their groups are killed.
const readShutdownWait = 2 * time.Second

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
		return filepath.Join(root, ui.HomeDirName)
	}
	if h := getenv("AGENTSYNC_HOME"); h != "" {
		return h
	}
	if home := getenv("HOME"); home != "" {
		return filepath.Join(home, ui.HomeDirName)
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ui.HomeDirName)
	}
	return ""
}

// logPath is $XDG_STATE_HOME/agentsync-tui/activity.log, falling back to
// ~/.local/state; empty (file log disabled) when neither is known.
func logPath() string {
	if s := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(s) {
		return filepath.Join(s, progName, "activity.log")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", progName, "activity.log")
	}
	return ""
}
