//go:build unix

package sourcerepo

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lusky3/agentsync-tui/internal/gitrepo/gittest"
)

// TestSSHPushCannotPrompt runs a push to an ssh:// URL with an ssh on PATH
// that records how git started it, then fails the way a refused key does.
// The real ssh the push would run gets BatchMode=yes (so it fails instead
// of asking for a passphrase or a host key), SSH_ASKPASS_REQUIRE=never and
// no controlling terminal to prompt on; the push then fails at once and
// the note says so. TestNoTTYPromptOnSSH covers a real ssh reaching a
// server.
func TestSSHPushCannotPrompt(t *testing.T) {
	bin, out := t.TempDir(), t.TempDir()
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > " + shellQuote(filepath.Join(out, "argv")) + "\n" +
		"printf '%s\\n' \"${SSH_ASKPASS_REQUIRE-unset}\" > " + shellQuote(filepath.Join(out, "askpass")) + "\n" +
		"if (: </dev/tty) 2>/dev/null; then echo yes; else echo no; fi > " + shellQuote(filepath.Join(out, "tty")) + "\n" +
		"echo 'git@127.0.0.1: Permission denied (publickey).' >&2\n" +
		"exit 255\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GIT_SSH_COMMAND", "")
	t.Setenv("GIT_SSH", "")

	home, _ := newTracked(t)
	gittest.Git(t, home, "config", "remote.origin.pushurl", "ssh://git@127.0.0.1:1/r.git")
	gittest.Commit(t, home, "local", map[string]string{skillA: "# a\n"})
	f := open(t, home)
	d := onlyDo(t, press(f.s, "P"))
	start := time.Now()
	r := f.exec(d)[0]
	if r.Err != nil || r.Result.ExitCode == 0 {
		t.Fatalf("push over ssh: %+v", r)
	}
	if took := time.Since(start); took > 20*time.Second {
		t.Errorf("took %v", took)
	}
	if !strings.Contains(f.s.note, "push failed") {
		t.Errorf("note %q", f.s.note)
	}
	read := func(name string) string {
		b, err := os.ReadFile(filepath.Join(out, name))
		if err != nil {
			t.Fatalf("the fake ssh did not run: %v", err)
		}
		return strings.TrimSpace(string(b))
	}
	argv := strings.Split(read("argv"), "\n")
	if i := slices.Index(argv, "-o"); i < 0 || i+1 >= len(argv) || argv[i+1] != "BatchMode=yes" {
		t.Errorf("ssh argv %q lacks -o BatchMode=yes", argv)
	}
	if got := read("askpass"); got != "never" {
		t.Errorf("SSH_ASKPASS_REQUIRE = %q, want never", got)
	}
	if got := read("tty"); got != "no" {
		t.Errorf("ssh could open /dev/tty (%q): it could prompt", got)
	}
}
