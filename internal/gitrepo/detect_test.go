package gitrepo

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/lusky3/agentsync-tui/internal/gitrepo/gittest"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/runner/runnertest"
)

const (
	bound      = 30 * time.Second
	tomlName   = "agentsync.toml"
	homeName   = ".agentsync"
	sourcePath = "source-path"
	revParse   = "rev-parse --show-toplevel --absolute-git-dir"
	backupDir  = ".claude"
	dotsDir    = "dots"
	subDir     = "agentsync"
	initMsg    = "init"
	dotGit     = ".git"
)

// resolved resolves p's symlinks (t.TempDir may itself sit behind one).
func resolved(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// newHome makes a repository at dir holding an agentsync.toml.
func newHome(t *testing.T, dir string) {
	t.Helper()
	gittest.Init(t, dir)
	gittest.Commit(t, dir, initMsg, map[string]string{tomlName: "agents = []\n"})
}

func detect(t *testing.T, home string) Repo {
	t.Helper()
	r, err := Detect(context.Background(), gittest.NewGit(t), nil, home, bound)
	if err != nil {
		t.Fatalf("Detect(%s): %v", home, err)
	}
	return r
}

// wantGit checks a usable git detection: manager, top, pathspec, layout.
func wantGit(t *testing.T, r Repo, top, pathspec string, layout Layout) {
	t.Helper()
	if r.Manager != Git || r.Refusal != "" {
		t.Fatalf("Manager = %v, Refusal = %q; want Git, no refusal", r.Manager, r.Refusal)
	}
	if r.Top != resolved(t, top) || r.Pathspec != pathspec || r.Layout != layout {
		t.Fatalf("Top %q Pathspec %q Layout %v; want %q %q %v", r.Top, r.Pathspec, r.Layout, resolved(t, top), pathspec, layout)
	}
	if !filepath.IsAbs(r.GitDir) || filepath.Base(r.GitDir) != dotGit {
		t.Errorf("GitDir = %q", r.GitDir)
	}
	if r.ObjectFormat != "sha1" {
		t.Errorf("ObjectFormat = %q, want sha1", r.ObjectFormat)
	}
}

func TestMissingHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "nope")
	if r := detect(t, home); r.Manager != Missing || r.Home != home {
		t.Fatalf("got %+v, want Missing", r)
	}
	link := filepath.Join(t.TempDir(), homeName)
	if err := os.Symlink(home, link); err != nil {
		t.Fatal(err)
	}
	if r := detect(t, link); r.Manager != Missing {
		t.Fatalf("dangling symlink: got %v, want Missing", r.Manager)
	}
}

func TestEmptyHomeIsMissing(t *testing.T) {
	if r := detect(t, t.TempDir()); r.Manager != Missing {
		t.Fatalf("got %v, want Missing", r.Manager)
	}
}

func TestHomeIsAFileIsAnError(t *testing.T) {
	f := filepath.Join(t.TempDir(), homeName)
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Detect(context.Background(), gittest.NewGit(t), nil, f, bound); err == nil {
		t.Fatal("a file as the home was accepted")
	}
}

func TestNoRepoIsNone(t *testing.T) {
	home := t.TempDir()
	// The production Program scrubs GIT_CEILING_DIRECTORIES (R2), so a
	// repository above TMPDIR would be found: skip rather than fail.
	probe := exec.Command("git", "-C", home, "rev-parse", "--git-dir")
	probe.Env = append(gittest.Env(t), "GIT_CEILING_DIRECTORIES=")
	if probe.Run() == nil {
		t.Skip("the temp dir is inside a git repository")
	}
	if err := os.WriteFile(filepath.Join(home, tomlName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r := detect(t, home)
	if r.Manager != None || r.Refusal != "" || r.ResolvedHome != resolved(t, home) {
		t.Fatalf("got %+v, want None", r)
	}
}

func TestRepoAtHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), homeName)
	newHome(t, home)
	r := detect(t, home)
	wantGit(t, r, home, ".", AtRoot)
	if r.Home != home || r.ResolvedHome != resolved(t, home) {
		t.Errorf("Home %q ResolvedHome %q", r.Home, r.ResolvedHome)
	}
}

func TestHomeBelowDotfilesRoot(t *testing.T) {
	dots := filepath.Join(t.TempDir(), "dotfiles")
	gittest.Init(t, dots)
	gittest.Commit(t, dots, dotsDir, map[string]string{".zshrc": "", homeName + "/" + tomlName: ""})
	wantGit(t, detect(t, filepath.Join(dots, homeName)), dots, homeName, BelowRoot)
}

func TestProjectScopeSubdir(t *testing.T) {
	proj := filepath.Join(t.TempDir(), "proj")
	gittest.Init(t, proj)
	gittest.Commit(t, proj, "proj", map[string]string{"main.go": "package main\n", homeName + "/" + tomlName: "", "sub/" + homeName + "/" + tomlName: ""})
	wantGit(t, detect(t, filepath.Join(proj, homeName)), proj, homeName, BelowRoot)
	nested := filepath.Join(proj, "sub", homeName)
	wantGit(t, detect(t, nested), proj, "sub/"+homeName, BelowRoot)
}

// The maintainer layout: ~/.agentsync is a symlink to ~/git/agent-config,
// a repository of its own. Temp dirs only.
func TestSymlinkedHomeResolved(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "git", "agent-config")
	newHome(t, target)
	link := filepath.Join(base, homeName)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	r := detect(t, link)
	wantGit(t, r, target, ".", AtRoot)
	if r.Home != link || r.ResolvedHome != resolved(t, target) {
		t.Errorf("Home %q ResolvedHome %q", r.Home, r.ResolvedHome)
	}
}

func TestSymlinkedHomeIntoOtherRepoSubdir(t *testing.T) {
	base := t.TempDir()
	dots := filepath.Join(base, dotsDir)
	gittest.Init(t, dots)
	gittest.Commit(t, dots, dotsDir, map[string]string{subDir + "/" + tomlName: ""})
	link := filepath.Join(base, homeName)
	if err := os.Symlink(filepath.Join(dots, subDir), link); err != nil {
		t.Fatal(err)
	}
	wantGit(t, detect(t, link), dots, subDir, BelowRoot)
}

// A gitfile in the home naming a repository whose core.worktree is another
// directory puts the toplevel outside the home: refused, never a pathspec
// with "..".
func TestHomeOutsideWorktreeRefused(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	gittest.Init(t, repo)
	other := filepath.Join(base, "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, repo, "config", "core.worktree", other)
	home := filepath.Join(base, homeName)
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, dotGit), []byte("gitdir: "+filepath.Join(repo, dotGit)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := detect(t, home)
	if r.Manager != Git || r.Refusal != RefusalOutside || r.Pathspec != "" {
		t.Fatalf("got Manager %v Refusal %q Pathspec %q; want the outside refusal", r.Manager, r.Refusal, r.Pathspec)
	}
}

// A real directory whose name holds a line break makes rev-parse print more
// than two lines: refused rather than misparsed.
func TestRepoPathWithLineBreakRefused(t *testing.T) {
	home := filepath.Join(t.TempDir(), "a\nb")
	newHome(t, home)
	r := detect(t, home)
	if r.Manager != Git || r.Refusal != RefusalLineBreak || r.Top != "" {
		t.Fatalf("got Manager %v Refusal %q Top %q", r.Manager, r.Refusal, r.Top)
	}
}

// commitAs commits a file in dir as email.
func commitAs(t *testing.T, dir, email, file string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, file), []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, dir, "add", "--", file)
	gittest.Git(t, dir, "-c", "user.name=agentsync", "-c", "user.email="+email, "commit", "-q", "--message=checkpoint")
}

// wantBackup checks that home is refused as agentsync's backup history.
func wantBackup(t *testing.T, home string) {
	t.Helper()
	if r := detect(t, home); r.Manager != Git || r.Refusal != RefusalBackup {
		t.Fatalf("Manager %v Refusal %q, want Git with the backup refusal", r.Manager, r.Refusal)
	}
}

func TestBackupAuthorRepoRefused(t *testing.T) {
	home := filepath.Join(t.TempDir(), backupDir)
	gittest.Init(t, home)
	commitAs(t, home, DefaultBackupAuthor, "settings.json")
	wantBackup(t, home)
}

func TestBackupRepoRefusedAfterManualCommit(t *testing.T) {
	home := filepath.Join(t.TempDir(), backupDir)
	gittest.Init(t, home)
	commitAs(t, home, DefaultBackupAuthor, "settings.json")
	commitAs(t, home, "me@example.invalid", "notes.md")
	wantBackup(t, home)
}

// A repository carrying agentsync's own marker ([agentsync] managed = true
// in its local config) is refused whoever authored its commits.
func TestBackupMarkerRepoRefused(t *testing.T) {
	home := filepath.Join(t.TempDir(), backupDir)
	newHome(t, home)
	gittest.Git(t, home, "config", "--local", "agentsync.managed", "true")
	wantBackup(t, home)
}

func TestCustomBackupAuthorFromToml(t *testing.T) {
	home := filepath.Join(t.TempDir(), homeName)
	gittest.Init(t, home)
	toml := "[destination_directory_git_backup]\nmode = \"on\"\nauthor_email = \"backup@example.invalid\"\n"
	if err := os.WriteFile(filepath.Join(home, tomlName), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := BackupAuthor(home); got != "backup@example.invalid" {
		t.Fatalf("BackupAuthor = %q", got)
	}
	commitAs(t, home, "backup@example.invalid", "x")
	wantBackup(t, home)
}

func TestBackupAuthorDefaults(t *testing.T) {
	dir := t.TempDir()
	if got := BackupAuthor(dir); got != DefaultBackupAuthor {
		t.Errorf("no toml: %q", got)
	}
	for _, body := range []string{"agents = [\n", "[destination_directory_git_backup]\nmode = \"on\"\n", "[destination_directory_git_backup]\nauthor_email = \"\"\n"} {
		if err := os.WriteFile(filepath.Join(dir, tomlName), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := BackupAuthor(dir); got != DefaultBackupAuthor {
			t.Errorf("%q: %q", body, got)
		}
	}
}

// An unborn repository (no commit yet) is a usable git home.
func TestUnbornRepoIsGit(t *testing.T) {
	home := filepath.Join(t.TempDir(), homeName)
	gittest.Init(t, home)
	wantGit(t, detect(t, home), home, ".", AtRoot)
}

func TestObjectFormatSHA256(t *testing.T) {
	home := filepath.Join(t.TempDir(), homeName)
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, home, initMsg, "-q", "--object-format=sha256")
	gittest.Commit(t, home, initMsg, map[string]string{tomlName: ""})
	if r := detect(t, home); r.ObjectFormat != "sha256" || r.Refusal != "" {
		t.Fatalf("ObjectFormat = %q Refusal %q", r.ObjectFormat, r.Refusal)
	}
}

// fakeHome is a non-empty temp home and its resolved path, for detections
// run against scripted runners.
func fakeHome(t *testing.T) (home, res string) {
	t.Helper()
	home = filepath.Join(t.TempDir(), homeName)
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, tomlName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return home, resolved(t, home)
}

// fakeTop scripts a successful rev-parse for res as its own toplevel.
func fakeTop(f *runnertest.Fake, res string) *runnertest.Fake {
	return f.On("-C "+res+" "+revParse, runner.Result{Stdout: res + "\n" + res + "/" + dotGit + "\n"}, nil)
}

// Old git rejects --show-object-format: the format is sha1.
func TestObjectFormatFallsBackToSHA1(t *testing.T) {
	home, res := fakeHome(t)
	f := fakeTop(runnertest.New().Default(runner.Result{ExitCode: 129, Stderr: "unknown option"}, nil), res)
	f.On("-C "+res+" log -1 --fixed-strings --author=<"+DefaultBackupAuthor+"> --format=%H", runner.Result{}, nil)
	f.On("-C "+res+" config --local --type=bool --get agentsync.managed", runner.Result{ExitCode: 1}, nil)
	r, err := Detect(context.Background(), f, nil, home, bound)
	if err != nil || r.ObjectFormat != "sha1" || r.Refusal != "" {
		t.Fatalf("got %+v, %v", r, err)
	}
}

func TestDubiousOwnershipRefused(t *testing.T) {
	home, res := fakeHome(t)
	stderr := "fatal: detected dubious ownership in repository at '" + res + "'\nTo add an exception for this directory, call:\n\n\tgit config --global --add safe.directory " + res + "\n"
	f := runnertest.New().On("-C "+res+" "+revParse, runner.Result{ExitCode: 128, Stderr: stderr}, nil)
	r, err := Detect(context.Background(), f, nil, home, bound)
	if err != nil {
		t.Fatal(err)
	}
	if want := "fatal: detected dubious ownership in repository at '" + res + "'"; r.Manager != Git || r.Refusal != want {
		t.Fatalf("Manager %v Refusal %q; want Git, %q", r.Manager, r.Refusal, want)
	}
	if calls := f.Calls(); len(calls) != 1 {
		t.Errorf("ran %q after the dubious-ownership failure", calls)
	}
}

func TestRevParseFailureIsAnError(t *testing.T) {
	home, res := fakeHome(t)
	f := runnertest.New().On("-C "+res+" "+revParse, runner.Result{ExitCode: 128, Stderr: "fatal: something else\n"}, nil)
	if _, err := Detect(context.Background(), f, nil, home, bound); err == nil {
		t.Fatal("an unexpected rev-parse failure was not reported")
	}
}

func TestGitMissingIsAnError(t *testing.T) {
	home, _ := fakeHome(t)
	_, err := Detect(context.Background(), runner.NewGit(filepath.Join(t.TempDir(), "no-git")), nil, home, bound)
	var miss *runner.MissingError
	if !errors.As(err, &miss) {
		t.Fatalf("err = %v, want *runner.MissingError", err)
	}
}

// noGit is a git runner that fails the test when called.
func noGit(t *testing.T) *runnertest.Fake {
	f := runnertest.New()
	f.Guard = func(argv string) { t.Errorf("git ran for a chezmoi home: %s", argv) }
	return f
}

func TestChezmoiSourceContainsHome(t *testing.T) {
	src := t.TempDir()
	home := filepath.Join(src, subDir)
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, tomlName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cz := runnertest.New().On(sourcePath, runner.Result{Stdout: src + "\n"}, nil)
	r, err := Detect(context.Background(), noGit(t), cz, home, bound)
	if err != nil || r.Manager != Chezmoi || r.ChezmoiSource != src {
		t.Fatalf("got %+v, %v; want Chezmoi with source %q", r, err, src)
	}
}

func TestChezmoiManagedTarget(t *testing.T) {
	home, res := fakeHome(t)
	src := t.TempDir()
	cz := runnertest.New().
		On(sourcePath, runner.Result{Stdout: src + "\n"}, nil).
		On(sourcePath+" "+res, runner.Result{Stdout: src + "/dot_agentsync\n"}, nil)
	r, err := Detect(context.Background(), noGit(t), cz, home, bound)
	if err != nil || r.Manager != Chezmoi || r.ChezmoiSource != src {
		t.Fatalf("got %+v, %v; want Chezmoi", r, err)
	}
}

func TestChezmoiAbsentFallsThroughToGit(t *testing.T) {
	home := filepath.Join(t.TempDir(), homeName)
	newHome(t, home)
	absent := runner.NewChezmoi(filepath.Join(t.TempDir(), "no-chezmoi"))
	unmanaged := runnertest.New().
		On(sourcePath, runner.Result{Stdout: t.TempDir() + "\n"}, nil).
		Default(runner.Result{ExitCode: 1, Stderr: "not managed"}, nil)
	for name, cz := range map[string]runner.Runner{"nil": nil, "not installed": absent, "unmanaged": unmanaged} {
		r, err := Detect(context.Background(), gittest.NewGit(t), cz, home, bound)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		wantGit(t, r, home, ".", AtRoot)
	}
}

func TestInspectLockAndInProgress(t *testing.T) {
	cases := []struct {
		marker string
		dir    bool
		want   GitDirState
	}{
		{"", false, GitDirState{}},
		{"index.lock", false, GitDirState{Locked: true}},
		{"MERGE_HEAD", false, GitDirState{InProgress: "merge"}},
		{"rebase-merge", true, GitDirState{InProgress: "rebase"}},
		{"rebase-apply", true, GitDirState{InProgress: "rebase"}},
		{"CHERRY_PICK_HEAD", false, GitDirState{InProgress: "cherry-pick"}},
		{"REVERT_HEAD", false, GitDirState{InProgress: "revert"}},
		{"BISECT_LOG", false, GitDirState{InProgress: "bisect"}},
		// A cherry-pick or revert of several commits whose stopped pick
		// was committed by hand: CHERRY_PICK_HEAD is gone, the sequence
		// is still in progress.
		{"sequencer", true, GitDirState{InProgress: "cherry-pick or revert sequence"}},
	}
	for _, c := range cases {
		gd := t.TempDir()
		p := filepath.Join(gd, c.marker)
		switch {
		case c.marker == "":
		case c.dir:
			if err := os.Mkdir(p, 0o755); err != nil {
				t.Fatal(err)
			}
		default:
			if err := os.WriteFile(p, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if got := InspectGitDir(gd); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.marker, got, c.want)
		}
		if _, err := os.Lstat(p); c.marker != "" && err != nil {
			t.Errorf("%s was removed: %v", c.marker, err)
		}
	}
}

// A dangling index.lock symlink still counts (Lstat, not Stat), and is left
// in place.
func TestInspectDanglingLockSymlink(t *testing.T) {
	gd := t.TempDir()
	lock := filepath.Join(gd, "index.lock")
	if err := os.Symlink(filepath.Join(gd, "nowhere"), lock); err != nil {
		t.Fatal(err)
	}
	if !InspectGitDir(gd).Locked {
		t.Fatal("dangling index.lock not reported")
	}
	if _, err := os.Lstat(lock); err != nil {
		t.Fatal("index.lock removed")
	}
}

// recorder wraps a runner and records whether each call's context was a
// write's.
type recorder struct {
	runner.Runner
	writes []bool
}

func (r *recorder) Run(ctx context.Context, args ...string) (runner.Result, error) {
	r.writes = append(r.writes, runner.IsWrite(ctx))
	return r.Runner.Run(ctx, args...)
}

func TestDetectDoesNotWriteToRepo(t *testing.T) {
	home := filepath.Join(t.TempDir(), homeName)
	newHome(t, home)
	index := filepath.Join(home, dotGit, "index")
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(index, old, old); err != nil {
		t.Fatal(err)
	}
	// A stale stat in the index: a refreshing read (status) would rewrite it.
	if err := os.Chtimes(filepath.Join(home, tomlName), time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{Runner: gittest.NewGit(t)}
	if _, err := Detect(context.Background(), rec, nil, home, bound); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(index)
	if err != nil {
		t.Fatal(err)
	}
	if !st.ModTime().Equal(old) {
		t.Errorf("index mtime changed: %v -> %v", old, st.ModTime())
	}
	if _, err := os.Lstat(index + ".lock"); err == nil {
		t.Error("index.lock left behind")
	}
	if len(rec.writes) == 0 {
		t.Fatal("no git call recorded")
	}
	for i, w := range rec.writes {
		if w {
			t.Errorf("call %d ran with a write context (GIT_OPTIONAL_LOCKS would not be set)", i)
		}
	}
}

func TestDetectBounded(t *testing.T) {
	home, _ := fakeHome(t)
	for name, run := range map[string]func() (Repo, error){
		"git": func() (Repo, error) {
			return Detect(context.Background(), runnertest.New().Block(), nil, home, 50*time.Millisecond)
		},
		"chezmoi": func() (Repo, error) {
			return Detect(context.Background(), noGit(t), runnertest.New().Block(), home, 50*time.Millisecond)
		},
	} {
		start := time.Now()
		r, err := run()
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("%s: err = %v, want DeadlineExceeded", name, err)
		}
		wantUnknown(t, r, home)
		if took := time.Since(start); took > 5*time.Second {
			t.Errorf("%s: took %v", name, took)
		}
	}
}

// wantUnknown checks the shape of a failed detection: nothing usable, and
// nothing a caller could mistake for a writable repository or a missing
// home (which would offer the bootstrap).
func wantUnknown(t *testing.T, r Repo, home string) {
	t.Helper()
	if r.Manager != Unknown || r.Refusal != RefusalUnknown || r.Home != home {
		t.Errorf("Manager %v Refusal %q Home %q; want Unknown with %q", r.Manager, r.Refusal, r.Home, RefusalUnknown)
	}
	if r.Top != "" || r.GitDir != "" || r.Pathspec != "" || r.ChezmoiSource != "" {
		t.Errorf("partial repo leaked: %+v", r)
	}
}

// A failure after rev-parse succeeded (a later read bounded out, or git log
// failing) never returns the half-filled repository.
func TestDetectErrorAfterRevParseIsNotWritable(t *testing.T) {
	home, res := fakeHome(t)
	blockObjectFormat := fakeTop(runnertest.New().Block(), res)
	failLog := fakeTop(runnertest.New(), res).
		On("-C "+res+" rev-parse --show-object-format", runner.Result{Stdout: "sha1\n"}, nil).
		On("-C "+res+" config --local --type=bool --get agentsync.managed", runner.Result{ExitCode: 1}, nil).
		On("-C "+res+" log -1 --fixed-strings --author=<"+DefaultBackupAuthor+"> --format=%H", runner.Result{ExitCode: 128, Stderr: "fatal: bad object HEAD\n"}, nil)
	for name, git := range map[string]*runnertest.Fake{"object format blocks": blockObjectFormat, "log fails": failLog} {
		r, err := Detect(context.Background(), git, nil, home, 50*time.Millisecond)
		if err == nil {
			t.Fatalf("%s: no error", name)
		}
		wantUnknown(t, r, home)
	}
}

// A home that is not a directory is an error with the Unknown shape, never
// Missing (which would offer to clone over it).
func TestHomeErrorIsUnknown(t *testing.T) {
	f := filepath.Join(t.TempDir(), homeName)
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := Detect(context.Background(), gittest.NewGit(t), nil, f, bound)
	if err == nil {
		t.Fatal("no error")
	}
	wantUnknown(t, r, f)
}

// wantControlRefused checks the control-character refusal: no path of the
// repository is handed on.
func wantControlRefused(t *testing.T, home string) {
	t.Helper()
	r := detect(t, home)
	if r.Manager != Git || r.Refusal != RefusalControl {
		t.Fatalf("Manager %v Refusal %q; want Git with %q", r.Manager, r.Refusal, RefusalControl)
	}
	if r.Top != "" || r.GitDir != "" || r.Pathspec != "" {
		t.Fatalf("unsafe paths handed on: Top %q GitDir %q Pathspec %q", r.Top, r.GitDir, r.Pathspec)
	}
}

// A line break below the toplevel reaches only the pathspec (rev-parse
// still prints two lines).
func TestControlCharBelowTopRefused(t *testing.T) {
	dots := filepath.Join(t.TempDir(), dotsDir)
	gittest.Init(t, dots)
	home := filepath.Join(dots, "a\nb", homeName)
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, tomlName), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	wantControlRefused(t, home)
}

// A carriage return in the toplevel keeps rev-parse at two lines.
func TestControlCharInTopRefused(t *testing.T) {
	home := filepath.Join(t.TempDir(), "a\rb")
	newHome(t, home)
	wantControlRefused(t, home)
}

// A stow-style agentsync.toml, a symlink to a regular file inside the
// resolved home, is read through the link.
func TestBackupAuthorThroughSymlink(t *testing.T) {
	home := t.TempDir()
	body := "[destination_directory_git_backup]\nauthor_email = \"stow@example.invalid\"\n"
	if err := os.MkdirAll(filepath.Join(home, "stow"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "stow", tomlName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("stow", tomlName), filepath.Join(home, tomlName)); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if got := BackupAuthor(home); got != "stow@example.invalid" {
		t.Errorf("BackupAuthor = %q", got)
	}
	// A link leaving the home is not followed.
	out := filepath.Join(t.TempDir(), tomlName)
	if err := os.WriteFile(out, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	if err := os.Symlink(out, filepath.Join(other, tomlName)); err != nil {
		t.Fatal(err)
	}
	if got := BackupAuthor(other); got != DefaultBackupAuthor {
		t.Errorf("outside link: %q", got)
	}
}

// With real git: after the first pick of a two-commit cherry-pick stops on
// a conflict and is resolved by a plain commit, git still has the rest of
// the sequence to pick, and so does InspectGitDir.
func TestInspectSequencerAfterManualCommit(t *testing.T) {
	dir := t.TempDir()
	gittest.Init(t, dir)
	gittest.Commit(t, dir, "a", map[string]string{"f": "a\n"})
	gittest.Git(t, dir, "checkout", "-q", "-b", "side")
	gittest.Commit(t, dir, "s1", map[string]string{"f": "s1\n"})
	gittest.Commit(t, dir, "s2", map[string]string{"g": "s2\n"})
	gittest.Git(t, dir, "checkout", "-q", "main")
	gittest.Commit(t, dir, "m", map[string]string{"f": "m\n"})
	pick := exec.Command("git", "-C", dir, "cherry-pick", "side~1", "side")
	pick.Env = gittest.Env(t)
	if out, err := pick.CombinedOutput(); err == nil {
		t.Fatalf("cherry-pick did not stop:\n%s", out)
	}
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("resolved\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, dir, "add", "--", "f")
	gittest.Git(t, dir, "commit", "-q", "--message=resolved")
	gd := filepath.Join(dir, ".git")
	if _, err := os.Lstat(filepath.Join(gd, "CHERRY_PICK_HEAD")); err == nil {
		t.Fatal("CHERRY_PICK_HEAD still there")
	}
	if InspectGitDir(gd).InProgress == "" {
		t.Fatal("sequence in progress not reported")
	}
}
