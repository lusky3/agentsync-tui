package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// gitPreArgs is the fixed hardening every git argv starts with (D1).
var gitPreArgs = []string{"--no-pager", "-c", "core.pager=cat", "-c", "color.ui=never", "-c", "core.fsmonitor=false", "--literal-pathspecs"}

// fakeGit returns the path of testdata/fake-git.sh.
func fakeGit(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(wd, "testdata", "fake-git.sh")
}

// fakeOut is what fake-git.sh reported: its argv, the env it printed and
// whether it had a controlling terminal.
type fakeOut struct {
	args []string
	env  map[string]string
	tty  string
}

func parseFake(t *testing.T, stdout string) fakeOut {
	t.Helper()
	out := fakeOut{env: map[string]string{}}
	for _, l := range strings.Split(strings.TrimSpace(stdout), "\n") {
		switch {
		case strings.HasPrefix(l, "arg:"):
			out.args = append(out.args, strings.TrimPrefix(l, "arg:"))
		case strings.HasPrefix(l, "env:"):
			k, v, _ := strings.Cut(strings.TrimPrefix(l, "env:"), "=")
			out.env[k] = v
		case strings.HasPrefix(l, "tty="):
			out.tty = strings.TrimPrefix(l, "tty=")
		}
	}
	return out
}

// runFake runs fake-git.sh through p with args and parses what it saw.
func runFake(t *testing.T, ctx context.Context, p *Program, args ...string) fakeOut {
	t.Helper()
	res, err := p.Run(ctx, args...)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("Run: exit %d err %v stderr %q", res.ExitCode, err, res.Stderr)
	}
	return parseFake(t, res.Stdout)
}

// baseEnv is a minimal child environment with extra appended.
func baseEnv(extra ...string) []string {
	return append([]string{"PATH=" + os.Getenv("PATH")}, extra...)
}

func TestGitPreArgsPrecedeCallerArgs(t *testing.T) {
	p := NewGit(fakeGit(t))
	p.Env = baseEnv()
	got := runFake(t, context.Background(), p, "-C", "/r", "status").args
	want := append(append([]string{}, gitPreArgs...), "-C", "/r", "status")
	if !slices.Equal(got, want) {
		t.Fatalf("argv %q, want %q", got, want)
	}
	if p.Name() != "git" {
		t.Errorf("Name() = %q", p.Name())
	}
}

func TestGitEnvHardened(t *testing.T) {
	p := NewGit(fakeGit(t))
	p.Env = baseEnv("GIT_ASKPASS=/usr/bin/askpass", "GIT_TERMINAL_PROMPT=1", "LC_ALL=fr_FR.UTF-8", "GIT_PAGER=less", "GIT_EDITOR=vim", "GIT_MERGE_AUTOEDIT=yes")
	env := runFake(t, context.Background(), p, "-C", "/r", "status").env
	for k, v := range map[string]string{
		"LC_ALL": "C", "GIT_PAGER": "cat", "GIT_EDITOR": "true", "GIT_MERGE_AUTOEDIT": "no", "GIT_NO_REPLACE_OBJECTS": "1",
		"GIT_TERMINAL_PROMPT": "0", "SSH_ASKPASS_REQUIRE": "never",
	} {
		if env[k] != v {
			t.Errorf("%s = %q, want %q", k, env[k], v)
		}
	}
	if _, ok := env["GIT_ASKPASS"]; ok {
		t.Errorf("GIT_ASKPASS inherited: %q", env["GIT_ASKPASS"])
	}
}

func TestOptionalLocksOnlyForReads(t *testing.T) {
	p := NewGit(fakeGit(t))
	p.Env = baseEnv("GIT_OPTIONAL_LOCKS=1")
	if v := runFake(t, context.Background(), p, "-C", "/r", "status").env["GIT_OPTIONAL_LOCKS"]; v != "0" {
		t.Errorf("read: GIT_OPTIONAL_LOCKS = %q, want 0", v)
	}
	if v, ok := runFake(t, MarkWrite(context.Background()), p, "-C", "/r", "commit").env["GIT_OPTIONAL_LOCKS"]; ok {
		t.Errorf("write: GIT_OPTIONAL_LOCKS = %q, want absent", v)
	}
}

// R2: an inherited repository-selecting variable (the TUI started from a
// git hook) must not redirect the child to another repository.
func TestRepoSelectingEnvScrubbed(t *testing.T) {
	scrubbed := []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_COMMON_DIR", "GIT_NAMESPACE", "GIT_CEILING_DIRECTORIES",
		"GIT_GLOB_PATHSPECS", "GIT_NOGLOB_PATHSPECS", "GIT_ICASE_PATHSPECS", "GIT_CONFIG_PARAMETERS", "GIT_CONFIG_COUNT"}
	t.Setenv("GIT_DIR", "/elsewhere")
	t.Setenv("GIT_WORK_TREE", "/x")
	t.Setenv("GIT_INDEX_FILE", "/y")
	t.Setenv("GIT_CONFIG_PARAMETERS", "'core.hooksPath'='/evil'")
	for _, k := range scrubbed[3:] {
		if k != "GIT_CONFIG_PARAMETERS" {
			t.Setenv(k, "1")
		}
	}
	p := NewGit(fakeGit(t)) // nil Env: the process environment
	env := runFake(t, context.Background(), p, "-C", "/r", "status").env
	for _, k := range scrubbed {
		if v, ok := env[k]; ok {
			t.Errorf("%s reached the child: %q", k, v)
		}
	}
}

// hermeticGit returns an environment in which real git reads no user or
// system configuration, or skips the test when git is not installed.
func hermeticGit(t *testing.T) (gitBin string, env []string) {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	home := t.TempDir()
	cfg := filepath.Join(home, "gitconfig")
	if err := os.WriteFile(cfg, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return gitBin, []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_CONFIG_GLOBAL=" + cfg, "GIT_CONFIG_NOSYSTEM=1"}
}

// gitIn runs real git with env in dir, failing the test on error.
func gitIn(t *testing.T, gitBin string, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command(gitBin, args...)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %q: %v\n%s", args, err, out)
	}
}

// R1: ssh BatchMode is added for network commands only, and never over an
// ssh command the user chose (env or the repository's core.sshCommand).
func TestBatchModeAddedOnlyWhenNoUserSSH(t *testing.T) {
	const batch = "ssh -o BatchMode=yes"
	for _, c := range []struct {
		name  string
		extra []string
		args  []string
		want  string // GIT_SSH_COMMAND the child sees ("" = absent)
	}{
		{"none set", nil, []string{"-C", "/r", "fetch", "origin"}, batch},
		{"pull", nil, []string{"-C", "/r", "pull", "--ff-only"}, batch},
		{"push after -c", nil, []string{"-C", "/r", "-c", "push.default=simple", "push"}, batch},
		{"GIT_SSH_COMMAND set", []string{"GIT_SSH_COMMAND=ssh -i /k"}, []string{"-C", "/r", "fetch"}, "ssh -i /k"},
		{"GIT_SSH set", []string{"GIT_SSH=/usr/bin/myssh"}, []string{"-C", "/r", "push"}, ""},
		{"probe unreadable", []string{"FAKE_CONFIG_EXIT=128"}, []string{"-C", "/r", "fetch"}, ""},
		{"probe says set", []string{"FAKE_CONFIG_EXIT=0"}, []string{"-C", "/r", "fetch"}, ""},
		{"status", nil, []string{"-C", "/r", "status"}, ""},
		{"push as a path", nil, []string{"-C", "/r", "add", "--", "push"}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := NewGit(fakeGit(t))
			p.Env = baseEnv(c.extra...)
			if got := runFake(t, context.Background(), p, c.args...).env["GIT_SSH_COMMAND"]; got != c.want {
				t.Errorf("GIT_SSH_COMMAND = %q, want %q", got, c.want)
			}
		})
	}
	t.Run("core.sshCommand in the repo (real git)", func(t *testing.T) {
		gitBin, env := hermeticGit(t)
		repo := t.TempDir()
		gitIn(t, gitBin, env, "init", "-q", repo)
		p := NewGit(fakeGit(t))
		p.Env = append(env, "FAKE_REAL_GIT="+gitBin)
		if got, ok := runFake(t, context.Background(), p, "-C", repo, "fetch").env["GIT_SSH_COMMAND"]; !ok || got != batch {
			t.Fatalf("precondition (unset in a real repo): GIT_SSH_COMMAND = %q, want %q", got, batch)
		}
		gitIn(t, gitBin, env, "-C", repo, "config", "core.sshCommand", "ssh -i /mine")
		if got, ok := runFake(t, context.Background(), p, "-C", repo, "fetch").env["GIT_SSH_COMMAND"]; ok {
			t.Errorf("GIT_SSH_COMMAND = %q added over the repo's core.sshCommand", got)
		}
	})
	t.Run("core.sshCommand from the caller's -c (real git)", func(t *testing.T) {
		gitBin, env := hermeticGit(t)
		repo := t.TempDir()
		gitIn(t, gitBin, env, "init", "-q", repo)
		p := NewGit(fakeGit(t))
		p.Env = append(env, "FAKE_REAL_GIT="+gitBin)
		args := []string{"-C", repo, "-c", "core.sshCommand=ssh -i /caller", "fetch", "origin"}
		if got, ok := runFake(t, context.Background(), p, args...).env["GIT_SSH_COMMAND"]; ok {
			t.Errorf("GIT_SSH_COMMAND = %q added over the caller's -c core.sshCommand", got)
		}
	})
}

func TestMissingBinaryIsMissingError(t *testing.T) {
	for _, bin := range []string{"does-not-exist-git", filepath.Join(t.TempDir(), "git")} {
		_, err := NewGit(bin).Run(context.Background(), "-C", "/r", "status")
		var me *MissingError
		if !errors.As(err, &me) || err.Error() != "git is not installed" {
			t.Errorf("%s: err = %v, want MissingError \"git is not installed\"", bin, err)
		}
	}
	_, err := NewChezmoi("does-not-exist-chezmoi").Run(context.Background(), "source-path")
	if err == nil || err.Error() != "chezmoi is not installed" {
		t.Errorf("chezmoi: err = %v", err)
	}
}

func TestChezmoiEnvAndArgs(t *testing.T) {
	p := NewChezmoi(fakeGit(t))
	p.Env = baseEnv("GIT_ASKPASS=/x", "NO_COLOR=0", "LC_ALL=de_DE")
	got := runFake(t, context.Background(), p, "source-path")
	if !slices.Equal(got.args, []string{"source-path"}) || p.Name() != "chezmoi" {
		t.Errorf("argv %q name %q", got.args, p.Name())
	}
	for k, v := range map[string]string{"LC_ALL": "C", "NO_COLOR": "1", "GIT_TERMINAL_PROMPT": "0", "SSH_ASKPASS_REQUIRE": "never"} {
		if got.env[k] != v {
			t.Errorf("%s = %q, want %q", k, got.env[k], v)
		}
	}
	if _, ok := got.env["GIT_ASKPASS"]; ok {
		t.Error("GIT_ASKPASS inherited")
	}
}

func TestOutputCappedLikeExec(t *testing.T) {
	script := filepath.Join(t.TempDir(), "flood.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nhead -c "+strconv.Itoa(MaxStream+1<<20)+" /dev/zero | tr '\\0' x\nhead -c 10 /dev/zero | tr '\\0' e >&2\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	res, err := NewGit(script).Run(context.Background(), "-C", "/r", "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(res.Stdout, TruncatedMark) || len(res.Stdout) > MaxStream+len(TruncatedMark) {
		t.Errorf("stdout len %d", len(res.Stdout))
	}
	if res.Stderr != "eeeeeeeeee" {
		t.Errorf("stderr %q", res.Stderr)
	}
}

func TestProgramNonZeroExitIsResult(t *testing.T) {
	p := NewGit(fakeGit(t))
	p.Env = baseEnv("FAKE_CONFIG_EXIT=3")
	res, err := p.Run(context.Background(), "-C", "/r", "config", "--get", "x")
	if err != nil || res.ExitCode != 3 {
		t.Fatalf("exit %d err %v", res.ExitCode, err)
	}
}
