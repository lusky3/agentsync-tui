package main

import (
	"bytes"
	"runtime/debug"
	"strings"
	"testing"
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
