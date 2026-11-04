package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestRunOmitsScopeForMachineCommands(t *testing.T) {
	for _, c := range []struct {
		cmd       string
		scope     Scope
		wantScope bool
	}{
		{"version", ScopeUser, false},
		{"version", ScopeProject, false},
		{"doctor", ScopeUser, false},
		{"doctor", ScopeProject, false},
		{"status", ScopeUser, true},
		{"mcp", ScopeProject, true},
	} {
		res, err := NewExec(fakeBin(t), c.scope, "/tmp/proj").Run(context.Background(), c.cmd)
		if err != nil {
			t.Fatal(err)
		}
		got := strings.TrimSpace(res.Stdout)
		if hasScopeFlag(got) != c.wantScope || !strings.HasSuffix(got, "--no-input --color never") {
			t.Errorf("%s/%s: argv = %q", c.cmd, c.scope, got)
		}
	}
}

func TestInteractiveOmitsScopeForMachineCommands(t *testing.T) {
	for _, cmd := range []string{"version", "doctor"} {
		for _, s := range []Scope{ScopeUser, ScopeProject} {
			got := strings.Join(NewExec("agentsync", s, "/p").Interactive(cmd).Args, " ")
			if hasScopeFlag(got) {
				t.Errorf("%s/%s: args = %q", cmd, s, got)
			}
		}
	}
	if got := strings.Join(NewExec("agentsync", ScopeUser, "").Interactive("init").Args, " "); !hasScopeFlag(got) {
		t.Errorf("init should keep scope: %q", got)
	}
}

func TestCheckVersionAgainstRealBinary(t *testing.T) {
	bin, err := exec.LookPath("agentsync")
	if err != nil {
		t.Skip("agentsync not on PATH")
	}
	r := NewExec(bin, ScopeUser, "")
	res, err := r.Run(context.Background(), "version")
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("exit=%d err=%v stderr=%q", res.ExitCode, err, res.Stderr)
	}
	if _, _, err := CheckVersion(context.Background(), r); err != nil {
		t.Fatal(err)
	}
}
