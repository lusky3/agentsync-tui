package gittest

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const gitDirVar = "GIT_DIR"

// The environment drops inherited GIT_* variables, HOME and
// XDG_CONFIG_HOME and points git at a temp global config.
func TestEnvIsHermetic(t *testing.T) {
	Need(t)
	t.Setenv(gitDirVar, "/nonexistent/inherited")
	t.Setenv("GIT_AUTHOR_EMAIL", "leak@example.invalid")
	t.Setenv("XDG_CONFIG_HOME", "/nonexistent/xdg")
	env := Env(t)
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case gitDirVar, "GIT_AUTHOR_EMAIL":
			t.Errorf("inherited %s survived", kv)
		case "HOME", "XDG_CONFIG_HOME":
			if !strings.HasPrefix(v, os.TempDir()) {
				t.Errorf("%s not a temp dir", kv)
			}
		}
	}
	for _, want := range []string{"GIT_CONFIG_NOSYSTEM=1"} {
		if !slices.Contains(env, want) {
			t.Errorf("env lacks %s", want)
		}
	}
	if got := strings.TrimSpace(Git(t, "", "config", "--global", "--get", "user.email")); got != UserEmail {
		t.Errorf("global user.email = %q, want %q", got, UserEmail)
	}
	if again := Env(t); !slices.Equal(again, env) {
		t.Error("Env is not stable within a test")
	}
}

func TestInitCommitAndBare(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a", "repo")
	Init(t, dir)
	Commit(t, dir, "first", map[string]string{"x/y.txt": "hi\n", "z": ""})
	if got := strings.TrimSpace(Git(t, dir, "branch", "--show-current")); got != "main" {
		t.Errorf("branch = %q, want main", got)
	}
	if got := strings.TrimSpace(Git(t, dir, "log", "-1", "--format=%ae %s")); got != UserEmail+" first" {
		t.Errorf("log = %q", got)
	}
	if got := Git(t, dir, "ls-files"); got != "x/y.txt\nz\n" {
		t.Errorf("ls-files = %q", got)
	}
	bare := Bare(t)
	if got := strings.TrimSpace(Git(t, bare, "rev-parse", "--is-bare-repository")); got != "true" {
		t.Errorf("Bare is-bare = %q", got)
	}
}

// NewGit is the production Program running in the hermetic environment.
func TestNewGitUsesHermeticEnv(t *testing.T) {
	t.Setenv(gitDirVar, "/nonexistent/inherited")
	p := NewGit(t)
	if p.Name() != "git" {
		t.Fatalf("Name = %q", p.Name())
	}
	res, err := p.Run(context.Background(), "config", "--global", "--get", "user.name")
	if err != nil || res.ExitCode != 0 || strings.TrimSpace(res.Stdout) != UserName {
		t.Fatalf("config user.name = %+v, %v", res, err)
	}
}
