package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lusky3/agentsync-tui/internal/testenv"
)

func fakeBin(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(wd, "testdata", "fake-agentsync.sh")
}

func TestRunAppendsGlobalFlags(t *testing.T) {
	res, err := NewExec(fakeBin(t), ScopeUser, "").Run(context.Background(), "status")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(res.Stdout); got != "status --scope user --no-input --color never" {
		t.Fatalf("argv = %q", got)
	}
}

func TestRunProjectScope(t *testing.T) {
	res, err := NewExec(fakeBin(t), ScopeProject, "/tmp/proj").Run(context.Background(), "status")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimSpace(res.Stdout)
	if !strings.Contains(got, "--project /tmp/proj") || !strings.HasSuffix(got, "--no-input --color never") {
		t.Fatalf("argv = %q", got)
	}
}

func TestRunSeparatesStreams(t *testing.T) {
	res, err := NewExec(fakeBin(t), ScopeUser, "").Run(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.Stdout, "fake-stderr-marker") || !strings.Contains(res.Stderr, "fake-stderr-marker") {
		t.Fatalf("stdout=%q stderr=%q", res.Stdout, res.Stderr)
	}
}

func TestNonZeroExitIsResultNotError(t *testing.T) {
	t.Setenv("FAKE_EXIT", "2")
	res, err := NewExec(fakeBin(t), ScopeUser, "").Run(context.Background(), "x")
	if err != nil || res.ExitCode != 2 {
		t.Fatalf("exit=%d err=%v", res.ExitCode, err)
	}
}

func TestContextCancelKillsProcess(t *testing.T) {
	t.Setenv("FAKE_SLEEP", "30")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := NewExec(fakeBin(t), ScopeUser, "").Run(ctx, "x")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("took %v", d)
	}
}

func TestMissingBinary(t *testing.T) {
	_, err := NewExec("does-not-exist", ScopeUser, "").Run(context.Background(), "x")
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("err = %v", err)
	}
}

func TestInteractiveOmitsNoInput(t *testing.T) {
	args := NewExec("agentsync", ScopeProject, "/p").Interactive("init").Args
	got := strings.Join(args, " ")
	if strings.Contains(got, "--no-input") || !strings.HasSuffix(got, "init --project /p") {
		t.Fatalf("args = %q", got)
	}
}

func versionRunner(t *testing.T, fixture string) Runner {
	t.Helper()
	wd, _ := os.Getwd()
	t.Setenv("FAKE_VERSION_FILE", filepath.Join(wd, "testdata", fixture))
	return NewExec(fakeBin(t), ScopeUser, "")
}

func TestCheckVersion(t *testing.T) {
	for _, c := range []struct {
		fixture, v string
		ok         bool
	}{
		{"version-v0.17.0.txt", "v0.17.0", true},
		{"version-v0.18.2.txt", "v0.18.2", false},
		{"version.txt", "dev", false}, // real dev build output
	} {
		v, ok, err := CheckVersion(context.Background(), versionRunner(t, c.fixture))
		if err != nil || v != c.v || ok != c.ok {
			t.Errorf("%s: v=%q ok=%v err=%v", c.fixture, v, ok, err)
		}
	}
}

func TestCheckVersionWarnsOnOtherMinor(t *testing.T) {
	_, ok, err := CheckVersion(context.Background(), versionRunner(t, "version-v0.18.2.txt"))
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestCheckVersionPatchDifferenceSupported(t *testing.T) {
	if !sameMinor("v0.17.3", TestedVersion) {
		t.Fatal("patch difference should be supported")
	}
}

func hasScopeFlag(argv string) bool {
	return strings.Contains(argv, "--scope") || strings.Contains(argv, "--project")
}

var scopeCases = []struct {
	args      []string
	wantScope bool
}{
	{[]string{"version"}, false},
	{[]string{"doctor"}, false},
	{[]string{"revert"}, false},
	{[]string{"secret", "list"}, false},
	{[]string{"secret", "set"}, false},
	{[]string{"marketplace", "list"}, false},
	{[]string{"marketplace", "add"}, false},
	{[]string{"plugin", "list"}, false},
	{[]string{"plugin", "add"}, false},
	{[]string{"plugin", "enable"}, false},
	{[]string{"plugin"}, false},
	{[]string{"help"}, false},
	{[]string{"completion", "bash"}, false},
	{[]string{"plugin", "upgrade"}, true},
	{[]string{"plugin", "upgrade", "--all"}, true},
	{[]string{"status"}, true},
	{[]string{"mcp", "list"}, true},
	{[]string{"mcp", "add", "doctor"}, true},
	{[]string{"agent", "list"}, true},
	{[]string{"agent", "add", "version"}, true},
	{[]string{"apply"}, true},
	{[]string{"check"}, true},
	{[]string{"diff"}, true},
	{[]string{"import"}, true},
	{[]string{"reconcile"}, true},
	{[]string{"skill", "list"}, true},
}

func TestRunOmitsScopeForMachineCommands(t *testing.T) {
	for _, c := range scopeCases {
		for _, s := range []Scope{ScopeUser, ScopeProject} {
			res, err := NewExec(fakeBin(t), s, "/tmp/proj").Run(context.Background(), c.args...)
			if err != nil {
				t.Fatal(err)
			}
			got := strings.TrimSpace(res.Stdout)
			if hasScopeFlag(got) != c.wantScope || !strings.HasSuffix(got, "--no-input --color never") {
				t.Errorf("%v/%s: argv = %q", c.args, s, got)
			}
		}
	}
}

func TestInteractiveOmitsScopeForMachineCommands(t *testing.T) {
	for _, c := range scopeCases {
		for _, s := range []Scope{ScopeUser, ScopeProject} {
			got := strings.Join(NewExec("agentsync", s, "/p").Interactive(c.args...).Args, " ")
			if hasScopeFlag(got) != c.wantScope {
				t.Errorf("%v/%s: args = %q", c.args, s, got)
			}
		}
	}
}

func TestCheckVersionAgainstRealBinary(t *testing.T) {
	env := testenv.New(t)
	bin, err := exec.LookPath("agentsync")
	if err != nil {
		t.Skip("agentsync not on PATH")
	}
	env.Init(t)
	t.Chdir(env.Home) // the children's working directory: never below a real .agentsync
	r := NewExec(bin, ScopeUser, "")
	r.Env = env.Env
	// Read-only commands only. Non-zero exit is tolerated only when it is
	// not a scope rejection.
	for _, args := range [][]string{
		{"version"}, {"doctor"}, {"secret", "list"}, {"marketplace", "list"},
		{"plugin", "list"}, {"completion", "bash"}, {"help"},
	} {
		res, err := r.Run(context.Background(), args...)
		if err != nil {
			t.Errorf("%v: %v", args, err)
			continue
		}
		if strings.Contains(res.Stderr+res.Stdout, "does not take --scope") ||
			strings.Contains(res.Stderr+res.Stdout, "does not take --project") {
			t.Errorf("%v: scope rejected: %q", args, res.Stderr)
		}
		if res.ExitCode != 0 {
			t.Logf("%v: exit %d (not a scope error): %s", args, res.ExitCode, res.Stderr)
		}
	}
	if _, _, err := CheckVersion(context.Background(), r); err != nil {
		t.Fatal(err)
	}
}

// A zero or negative grace falls back to InterruptGrace, so a zero Exec
// still kills a child that ignores SIGINT.
func TestWaitDelayFallsBackToInterruptGrace(t *testing.T) {
	for _, c := range []struct {
		grace, want time.Duration
	}{
		{0, InterruptGrace},
		{-1, InterruptGrace},
		{5 * time.Second, 5 * time.Second},
	} {
		e := Exec{grace: c.grace}
		if got := e.waitDelay(); got != c.want {
			t.Errorf("grace %v: waitDelay %v, want %v", c.grace, got, c.want)
		}
	}
}

// Each stream is capped at MaxStream bytes and ends with TruncatedMark when
// it was cut, so masking cost is bounded whatever the command prints.
func TestOutputCappedPerStream(t *testing.T) {
	script := filepath.Join(t.TempDir(), "flood.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nhead -c "+strconv.Itoa(MaxStream+1<<20)+" /dev/zero | tr '\\0' x\nhead -c 10 /dev/zero | tr '\\0' e >&2\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	res, err := NewExec(script, ScopeUser, "").Run(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(res.Stdout, TruncatedMark) || len(res.Stdout) > MaxStream+len(TruncatedMark) {
		t.Errorf("stdout len %d, suffix %q", len(res.Stdout), res.Stdout[max(len(res.Stdout)-40, 0):])
	}
	if res.Stderr != "eeeeeeeeee" {
		t.Errorf("stderr %q: a stream under the cap must not be marked", res.Stderr)
	}
}

// A background child must never stop on a /dev/tty prompt: git's terminal
// prompt and ssh's askpass are disabled in the child environment, and an
// inherited GIT_ASKPASS is dropped. Interactive handovers keep the user's
// environment.
func TestRunChildEnvDisablesGitPrompts(t *testing.T) {
	script := filepath.Join(t.TempDir(), "env.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho \"GTP=$GIT_TERMINAL_PROMPT SAR=$SSH_ASKPASS_REQUIRE GA=$GIT_ASKPASS\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_ASKPASS", "/usr/bin/askpass")
	t.Setenv("GIT_TERMINAL_PROMPT", "1")
	res, err := NewExec(script, ScopeUser, "").Run(context.Background(), "x")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(res.Stdout) != "GTP=0 SAR=never GA=" {
		t.Errorf("child env: %q", res.Stdout)
	}
	e := NewExec(script, ScopeUser, "")
	e.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_ASKPASS=/x"}
	if res, _ := e.Run(context.Background(), "x"); strings.TrimSpace(res.Stdout) != "GTP=0 SAR=never GA=" {
		t.Errorf("explicit Env not scrubbed: %q", res.Stdout)
	}
	cmd := e.Interactive("x")
	if strings.Join(cmd.Env, " ") != strings.Join(e.Env, " ") {
		t.Errorf("Interactive env changed: %q", cmd.Env)
	}
}
