package main

import (
	"bytes"
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
	}
}

func TestRunBadFlagExitsTwo(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--nope"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}

func TestRunBadScopeExitsTwo(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"--scope", "galaxy"}, &out, &errb); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}
