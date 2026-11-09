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
	// Compared as cleaned paths: TMPDIR may be spelled with a trailing or
	// doubled slash, or lie below $HOME; neither must matter.
	tmp := filepath.Clean(os.TempDir())
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case gitDirVar, "GIT_AUTHOR_EMAIL":
			t.Errorf("inherited %s survived", kv)
		case "HOME", "XDG_CONFIG_HOME":
			if rel, err := filepath.Rel(tmp, v); err != nil || !filepath.IsLocal(rel) {
				t.Errorf("%s not under the temp dir %s", kv, tmp)
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

// Discovery stops at the test's temp parent: a repository above the
// developer's TMPDIR cannot make a temp dir look versioned.
func TestEnvSetsCeiling(t *testing.T) {
	Need(t)
	dir := t.TempDir()
	want := "GIT_CEILING_DIRECTORIES=" + filepath.Dir(dir)
	if env := Env(t); !slices.Contains(env, want) {
		t.Fatalf("env lacks %s", want)
	}
}

// Commit records exactly the files it was given: a change the test staged
// before stays staged and out of the commit.
func TestCommitOnlyItsFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "w")
	Init(t, dir)
	Commit(t, dir, "base", map[string]string{"a": "1\n", "b": "1\n"})
	if err := os.WriteFile(filepath.Join(dir, "b"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	Git(t, dir, "add", "--", "b")
	Commit(t, dir, "a only", map[string]string{"a": "2\n"})
	if got := Git(t, dir, "show", "--name-only", "--format=", "HEAD"); got != "a\n" {
		t.Errorf("commit holds %q, want only a", got)
	}
	if got := Git(t, dir, "diff", "--cached", "--name-only"); got != "b\n" {
		t.Errorf("staged %q, want b still staged", got)
	}
	Commit(t, dir, "empty", nil)
}
