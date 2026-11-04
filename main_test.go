package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

func TestRunVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--version"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d, want 0 (stderr %q)", code, errb.String())
	}
	if got := out.String(); got != "agentsync-tui "+version+"\n" {
		t.Fatalf("stdout %q", got)
	}
}

func TestRunHelpExitsZero(t *testing.T) {
	for _, a := range []string{"-h", "--help"} {
		var out, errb bytes.Buffer
		if code := run([]string{a}, &out, &errb); code != 0 {
			t.Fatalf("%s: exit %d, want 0", a, code)
		}
		if !strings.Contains(errb.String(), "-version") {
			t.Fatalf("%s: usage not printed: %q", a, errb.String())
		}
		for _, want := range []string{"-read-timeout", "(default 2m0s)", "-write-timeout", "0 = none", "-debug-log"} {
			if !strings.Contains(errb.String(), want) {
				t.Errorf("%s: usage lacks %q: %q", a, want, errb.String())
			}
		}
	}
}

func TestRunBadFlagExitsTwo(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--nope"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}

func TestRunNegativeTimeoutExitsTwo(t *testing.T) {
	for _, args := range [][]string{{"--read-timeout", "-1s"}, {"--write-timeout", "-5m"}} {
		var out, errb bytes.Buffer
		if code := run(args, &out, &errb); code != 2 {
			t.Errorf("%q: exit %d, want 2", args, code)
		}
		if !strings.Contains(errb.String(), "must not be negative") {
			t.Errorf("%q: stderr %q", args, errb.String())
		}
	}
}

func TestRunBadScopeExitsTwo(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--scope", "galaxy"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}

func TestResolveVersion(t *testing.T) {
	info := func(v string) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{Version: v}}, true
		}
	}
	none := func() (*debug.BuildInfo, bool) { return nil, false }
	for _, tc := range []struct {
		name, ldflags string
		build         func() (*debug.BuildInfo, bool)
		want          string
	}{
		{"ldflags wins", "1.2.3", info("v9.9.9"), "1.2.3"},
		{"go install version", "dev", info("v0.1.0"), "v0.1.0"},
		{"devel build", "dev", info("(devel)"), "dev"},
		{"empty", "dev", info(""), "dev"},
		{"no build info", "dev", none, "dev"},
	} {
		if got := resolveVersion(tc.ldflags, tc.build); got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The user-scope canonical home must resolve exactly as the CLI's
// paths.AgentsyncHome does: $AGENTSYNC_TARGET_ROOT/.agentsync first (a
// redirected run is a sandbox, and the user-facing override is ignored),
// then $AGENTSYNC_HOME, then $HOME/.agentsync. Otherwise the TUI reads and
// edits one tree while every command it runs operates on another.
func TestAgentsyncHomeMatchesCLI(t *testing.T) {
	cases := []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{"AGENTSYNC_TARGET_ROOT": "/sandbox", "AGENTSYNC_HOME": "/explicit", "HOME": "/home/u"}, "/sandbox/.agentsync"},
		{map[string]string{"AGENTSYNC_HOME": "/explicit", "HOME": "/home/u"}, "/explicit"},
		{map[string]string{"HOME": "/home/u"}, "/home/u/.agentsync"},
		{map[string]string{"AGENTSYNC_TARGET_ROOT": "", "AGENTSYNC_HOME": "", "HOME": "/home/u"}, "/home/u/.agentsync"},
	}
	for _, c := range cases {
		getenv := func(k string) string { return c.env[k] }
		if got := agentsyncHome(getenv); got != c.want {
			t.Errorf("env %v: got %q, want %q", c.env, got, c.want)
		}
	}
}

// An unusable --debug-log path fails at startup with a message, never
// silently records nothing.
func TestRunBadDebugLogExitsTwo(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--debug-log", filepath.Join(t.TempDir(), "missing", "x.log"), "--version"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d, want 2; stderr %q", code, errb.String())
	}
	if !strings.Contains(errb.String(), "debug-log") {
		t.Errorf("stderr %q", errb.String())
	}
}

// fakeProgram stands in for tea.Program: Run returns at once with the
// configured error.
type fakeProgram struct {
	err  error
	sent []tea.Msg
}

func (f *fakeProgram) Run() (tea.Model, error) { return nil, f.err }
func (f *fakeProgram) Send(m tea.Msg)          { f.sent = append(f.sent, m) }

// hermetic points every path the TUI would touch at temp directories.
func hermetic(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("AGENTSYNC_HOME", filepath.Join(root, ".agentsync"))
	t.Setenv("AGENTSYNC_TARGET_ROOT", "")
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
}

// Every exit of the program, clean or with an error (a recovered panic
// comes back as Run's error), ends the children: the shutdown runs with a
// quit's signal and waits, and its outcomes are logged.
func TestRunShutsDownChildrenOnEveryExit(t *testing.T) {
	for _, runErr := range []error{nil, errors.New("boom")} {
		hermetic(t)
		var got []shutdownCall
		src := &fakeSignals{}
		h := hooks{
			notify:     src.notify,
			stop:       src.stop,
			newProgram: func(*ui.App) program { return &fakeProgram{err: runErr} },
			shutdown: func(sig os.Signal, readWait, writeWait time.Duration) []runner.Ended {
				got = append(got, shutdownCall{sig, readWait, writeWait})
				return []runner.Ended{{Args: []string{"status", "--json"}, Outcome: "killed", Took: time.Second}}
			},
			killAll: func() { t.Error("killAll without a second signal") },
		}
		var out, errb bytes.Buffer
		code := runWith([]string{}, &out, &errb, h)
		if want := map[bool]int{false: 0, true: 1}[runErr != nil]; code != want {
			t.Errorf("err %v: exit %d, want %d (%s)", runErr, code, want, errb.String())
		}
		if len(got) != 1 || got[0].sig != os.Interrupt || got[0].readWait != readShutdownWait || got[0].writeWait != runner.InterruptGrace {
			t.Fatalf("err %v: shutdown calls %+v", runErr, got)
		}
		b, err := os.ReadFile(filepath.Join(os.Getenv("XDG_STATE_HOME"), "agentsync-tui", "activity.log"))
		if err != nil || !strings.Contains(string(b), "killed") || !strings.Contains(string(b), "agentsync status --json") {
			t.Errorf("err %v: shutdown outcome not logged: %q (%v)", runErr, b, err)
		}
	}
}

type shutdownCall struct {
	sig                 os.Signal
	readWait, writeWait time.Duration
}

// A signal that arrives while the children are being ended (a second
// ctrl+c or SIGTERM, the terminal in cooked mode again) is caught and
// escalates to an immediate kill of every group; the exit is then bounded
// and nothing survives. The signal that ended the TUI is forwarded.
func TestEndChildrenEscalatesOnSecondSignal(t *testing.T) {
	released := make(chan struct{})
	killed := 0
	h := hooks{
		shutdown: func(sig os.Signal, readWait, writeWait time.Duration) []runner.Ended {
			if sig != syscall.SIGTERM {
				t.Errorf("forwarded %v, want SIGTERM", sig)
			}
			select {
			case <-released: // KillAll let the groups go
			case <-time.After(writeWait):
				t.Error("shutdown waited out the grace: no escalation")
			}
			return []runner.Ended{{Args: []string{"apply"}, Write: true, Outcome: "killed"}}
		},
		killAll: func() { killed++; close(released) },
	}
	sigs := make(chan os.Signal, 2)
	done := make(chan []runner.Ended, 1)
	go func() { done <- endChildren(syscall.SIGTERM, sigs, h) }()
	time.Sleep(50 * time.Millisecond)
	sigs <- syscall.SIGTERM
	select {
	case ended := <-done:
		if killed != 1 || len(ended) != 1 || ended[0].Outcome != "killed" {
			t.Errorf("killAll %d, ended %+v", killed, ended)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("endChildren did not return after the second signal")
	}
	// No signal: the forwarded one is a quit's SIGINT and killAll is never called.
	h.killAll = func() { t.Error("killAll on a plain exit") }
	h.shutdown = func(sig os.Signal, r, w time.Duration) []runner.Ended {
		if sig != os.Interrupt || r != readShutdownWait || w != runner.InterruptGrace {
			t.Errorf("quit: %v %v %v", sig, r, w)
		}
		return nil
	}
	endChildren(nil, sigs, h)
}

// fakeSignals stands in for os/signal: deliver hands a signal to the
// registered channel, or counts it as uncaught (Go's default handler would
// have ended the process) when it is not registered or already stopped.
type fakeSignals struct {
	ch         chan<- os.Signal
	registered map[os.Signal]bool
	stopped    bool
	uncaught   []os.Signal
}

func (f *fakeSignals) notify(ch chan<- os.Signal, sigs ...os.Signal) {
	f.ch = ch
	if f.registered == nil {
		f.registered = map[os.Signal]bool{}
	}
	for _, s := range sigs {
		f.registered[s] = true
	}
}

func (f *fakeSignals) stop(chan<- os.Signal) { f.stopped = true }

func (f *fakeSignals) deliver(s os.Signal) {
	if f.stopped || !f.registered[s] {
		f.uncaught = append(f.uncaught, s)
		return
	}
	f.ch <- s
}

// SIGINT, SIGTERM and SIGHUP are registered before the program runs and
// stay registered until the children are ended: a ctrl+c delivered during
// the shutdown wait (the terminal is in cooked mode again) is consumed and
// escalates to KillAll instead of reaching Go's default handler.
func TestSignalsCaughtUntilChildrenEnded(t *testing.T) {
	hermetic(t)
	src := &fakeSignals{}
	released := make(chan struct{})
	killed, stoppedBeforeShutdownEnded := 0, false
	var atRun map[os.Signal]bool
	h := hooks{
		notify: src.notify,
		stop:   src.stop,
		newProgram: func(*ui.App) program {
			atRun = map[os.Signal]bool{}
			for s := range src.registered {
				atRun[s] = true
			}
			return &fakeProgram{}
		},
		shutdown: func(os.Signal, time.Duration, time.Duration) []runner.Ended {
			src.deliver(os.Interrupt) // ctrl+c during the wait
			select {
			case <-released:
			case <-time.After(3 * time.Second):
				t.Error("the SIGINT during the wait did not escalate")
			}
			stoppedBeforeShutdownEnded = src.stopped
			return nil
		},
		killAll: func() { killed++; close(released) },
	}
	var out, errb bytes.Buffer
	if code := runWith(nil, &out, &errb, h); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	for _, s := range []os.Signal{os.Interrupt, syscall.SIGTERM, syscall.SIGHUP} {
		if !atRun[s] {
			t.Errorf("%v not registered before the program ran: %v", s, atRun)
		}
	}
	if len(src.uncaught) != 0 {
		t.Errorf("signals reached the default handler: %v", src.uncaught)
	}
	if killed != 1 || stoppedBeforeShutdownEnded || !src.stopped {
		t.Errorf("killAll %d, stopped before the shutdown ended %v, stopped at exit %v", killed, stoppedBeforeShutdownEnded, src.stopped)
	}
}
