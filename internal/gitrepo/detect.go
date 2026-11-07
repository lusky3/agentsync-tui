// Package gitrepo is the Source repo tab's model of the agentsync home's
// version control: how the home is versioned (Detect), whether its git dir
// is locked or mid-operation (InspectGitDir), and, in later files, status
// parsing and the diff and scan reads. Every git call here is a read: it
// runs through a runner.Runner with a context that is not MarkWrite'd, so
// the git Program sets GIT_OPTIONAL_LOCKS=0 and nothing refreshes the
// index, and each call is bounded by the caller's timeout.
package gitrepo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/runner"
)

// Manager is what versions the home.
type Manager int

const (
	// Missing: the home does not exist or is an empty directory (the
	// bootstrap case).
	Missing Manager = iota
	// None: the home exists and is in no git repository.
	None
	// Git: the home is inside a git work tree (possibly refused, see
	// Repo.Refusal).
	Git
	// Chezmoi: the home is chezmoi's source or a chezmoi target; the tab
	// leaves it to chezmoi (D8).
	Chezmoi
	// Unknown: Detect failed (its error says why). Nothing may be done:
	// the Repo carries only Home, ResolvedHome and RefusalUnknown.
	Unknown
)

// Layout is where the home sits in its repository.
type Layout int

const (
	// AtRoot: the home is the repository's toplevel.
	AtRoot Layout = iota
	// BelowRoot: the toplevel is above the home (a dotfiles repo, or a
	// project with .agentsync/ inside); git is limited to the pathspec and
	// push and pull are refused (R7).
	BelowRoot
)

// DefaultBackupAuthor is the email agentsync commits its destination
// backup checkpoints as when agentsync.toml sets none.
const DefaultBackupAuthor = "agentsync@localhost"

// The refusals Detect can set besides git's own dubious-ownership line.
const (
	RefusalLineBreak = "repository path has a line break"
	RefusalControl   = "repository path has a control character"
	RefusalUnknown   = "the repository could not be inspected"
	RefusalOutside   = "the home is outside the repository"
	RefusalBackup    = "this is agentsync's local-only destination history; it must never be committed or pushed"
)

// Repo is the result of Detect. Pathspec is "." or the slash-separated
// home path relative to Top. A non-empty Refusal disables every git write.
type Repo struct {
	Manager Manager
	Layout  Layout
	// Home is the path Detect was given; ResolvedHome is it with every
	// symlink resolved.
	Home, ResolvedHome string
	// Top is the resolved toplevel and GitDir git's absolute git dir.
	Top, GitDir string
	Pathspec    string
	// ChezmoiSource is `chezmoi source-path` (Manager Chezmoi only).
	ChezmoiSource string
	// ObjectFormat is "sha1" or "sha256".
	ObjectFormat string
	Refusal      string
}

// errNoRunner is returned when Detect is given no git runner.
var errNoRunner = errors.New("no git runner")

// Detect reports how home is versioned, in the pinned order: a missing or
// empty home; chezmoi (when chezmoi is not nil); the git work tree around
// the resolved home, refused when its paths have a line break, when the
// home lies outside it, when it is agentsync's destination-backup history
// or when git reports dubious ownership. Each runner call is bounded by
// timeout. The error is for what Detect could not find out (git missing,
// an unexpected git failure, a bound reached); a refusal is not an error.
//
// On error the Repo is Manager Unknown with RefusalUnknown and only Home
// and ResolvedHome set, never a half-filled repository a caller could take
// for a writable one or a Missing home it would offer to clone over.
func Detect(ctx context.Context, git, chezmoi runner.Runner, home string, timeout time.Duration) (Repo, error) {
	r, err := detectHome(ctx, git, chezmoi, home, timeout)
	if err != nil {
		return Repo{Manager: Unknown, Home: home, ResolvedHome: r.ResolvedHome, Refusal: RefusalUnknown}, err
	}
	return r, nil
}

// detectHome is Detect without the error shape.
func detectHome(ctx context.Context, git, chezmoi runner.Runner, home string, timeout time.Duration) (Repo, error) {
	r := Repo{Home: home}
	resolvedHome, missing, err := resolveHome(home)
	if err != nil || missing {
		return r, err
	}
	r.ResolvedHome = resolvedHome
	src, managed, err := chezmoiManages(ctx, chezmoi, resolvedHome, timeout)
	if err != nil {
		return r, err
	}
	if managed {
		r.Manager, r.ChezmoiSource = Chezmoi, src
		return r, nil
	}
	return detectGit(ctx, git, r, timeout)
}

// resolveHome returns home with its symlinks resolved, or missing when it
// does not exist (a dangling symlink included) or is an empty directory.
// A home that is not a directory is an error.
func resolveHome(home string) (resolvedHome string, missing bool, err error) {
	if _, err := os.Lstat(home); errors.Is(err, os.ErrNotExist) {
		return "", true, nil
	}
	abs, err := filepath.Abs(home)
	if err != nil {
		return "", false, err
	}
	resolvedHome, err = filepath.EvalSymlinks(abs)
	if errors.Is(err, os.ErrNotExist) {
		return "", true, nil
	} else if err != nil {
		return "", false, err
	}
	empty, err := emptyDir(resolvedHome)
	if err != nil {
		return "", false, err
	}
	return resolvedHome, empty, nil
}

// emptyDir reports whether dir is a directory with no entries.
func emptyDir(dir string) (bool, error) {
	f, err := os.Open(dir)
	if err != nil {
		return false, err
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Readdirnames(1); errors.Is(err, io.EOF) {
		return true, nil
	} else if err != nil {
		return false, fmt.Errorf("the home %s is not a readable directory: %w", dir, err)
	}
	return false, nil
}

// chezmoiManages reports whether chezmoi owns home: home lies inside
// chezmoi's source directory, or `chezmoi source-path <home>` exits 0 (home
// is a chezmoi target). A chezmoi that is absent, not configured or fails
// to start falls through to git; only a bound or cancellation is an error.
func chezmoiManages(ctx context.Context, cz runner.Runner, home string, timeout time.Duration) (src string, managed bool, err error) {
	if cz == nil {
		return "", false, nil
	}
	res, err := read(ctx, cz, timeout, "source-path")
	if err != nil {
		return "", false, ctxErr(err)
	}
	if res.ExitCode == 0 {
		src = strings.TrimRight(res.Stdout, "\r\n")
	}
	if src != "" && within(resolvePath(src), home) {
		return src, true, nil
	}
	res, err = read(ctx, cz, timeout, "source-path", home)
	if err != nil {
		return "", false, ctxErr(err)
	}
	return src, res.ExitCode == 0, nil
}

// ctxErr keeps err only when it is a context's end; any other runner error
// (a binary that is not installed) is dropped.
func ctxErr(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// resolvePath is p with its symlinks resolved, or p cleaned when that
// fails.
func resolvePath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// relWithin returns p relative to dir and whether p is dir or below it.
func relWithin(dir, p string) (string, bool) {
	rel, err := filepath.Rel(dir, p)
	if err != nil {
		return "", false
	}
	return rel, rel == "." || filepath.IsLocal(rel)
}

// within reports whether p is dir or lies below it.
func within(dir, p string) bool {
	_, ok := relWithin(dir, p)
	return ok
}

// detectGit fills r from the git work tree around r.ResolvedHome (steps
// 4 to 7 of the pinned order). A toplevel, git dir or pathspec holding a
// control or format character (a \r, or a \n below the toplevel, which
// rev-parse's two lines cannot show) or invalid UTF-8 is refused and not
// handed on.
func detectGit(ctx context.Context, git runner.Runner, r Repo, timeout time.Duration) (Repo, error) {
	res, err := readIn(ctx, git, timeout, r.ResolvedHome, "rev-parse", "--show-toplevel", "--absolute-git-dir")
	if err != nil {
		return r, fmt.Errorf("git rev-parse: %w", err)
	}
	if res.ExitCode != 0 {
		return revParseFailed(r, res)
	}
	r.Manager = Git
	lines := strings.Split(strings.TrimSuffix(res.Stdout, "\n"), "\n")
	if len(lines) != 2 {
		r.Refusal = RefusalLineBreak
		return r, nil
	}
	top, err := filepath.EvalSymlinks(lines[0])
	if err != nil {
		return r, fmt.Errorf("resolving the git toplevel: %w", err)
	}
	rel, ok := relWithin(top, r.ResolvedHome)
	if !ok {
		r.Refusal = RefusalOutside
		return r, nil
	}
	if actions.HasControl(top) || actions.HasControl(lines[1]) || actions.HasControl(rel) {
		r.Refusal = RefusalControl
		return r, nil
	}
	r.Top, r.GitDir, r.Pathspec = top, lines[1], filepath.ToSlash(rel)
	if rel != "." {
		r.Layout = BelowRoot
	}
	if r.ObjectFormat, err = objectFormat(ctx, git, top, timeout); err != nil {
		return r, err
	}
	backup, err := isBackupRepo(ctx, git, top, BackupAuthor(r.ResolvedHome), timeout)
	if backup {
		r.Refusal = RefusalBackup
	}
	return r, err
}

// revParseFailed classifies a failed rev-parse: dubious ownership is a
// refusal carrying git's first line (the TUI never adds safe.directory),
// "not a git repository" is None, anything else an error.
func revParseFailed(r Repo, res runner.Result) (Repo, error) {
	switch {
	case strings.Contains(res.Stderr, "dubious ownership"):
		r.Manager, r.Refusal = Git, mask.FirstLine(res.Stderr)
		return r, nil
	case strings.Contains(res.Stderr, "not a git repository"):
		r.Manager = None
		return r, nil
	}
	return r, fmt.Errorf("git rev-parse exited %d: %s", res.ExitCode, mask.FirstLine(res.Stderr))
}

// objectFormat is the repository's hash ("sha1" or "sha256"). Git older
// than 2.29 rejects --show-object-format and has no sha256 repositories, so
// a non-zero exit means sha1.
func objectFormat(ctx context.Context, git runner.Runner, top string, timeout time.Duration) (string, error) {
	res, err := readIn(ctx, git, timeout, top, "rev-parse", "--show-object-format")
	if err != nil {
		return "", fmt.Errorf("git rev-parse --show-object-format: %w", err)
	}
	if f := strings.TrimSpace(res.Stdout); res.ExitCode == 0 && f != "" {
		return f, nil
	}
	return "sha1", nil
}

// isBackupRepo reports whether top is one of agentsync's destination-backup
// repositories: its local config carries agentsync's marker
// ([agentsync] managed = true, set when agentsync creates one), or a commit
// reachable from HEAD is authored by author (any commit, not only the
// latest, so a backup repo the user has committed to by hand still counts).
// An unborn HEAD has no commits and is not a backup.
func isBackupRepo(ctx context.Context, git runner.Runner, top, author string, timeout time.Duration) (bool, error) {
	res, err := readIn(ctx, git, timeout, top, "config", "--local", "--type=bool", "--get", "agentsync.managed")
	if err != nil {
		return false, fmt.Errorf("git config: %w", err)
	}
	if res.ExitCode == 0 && strings.TrimSpace(res.Stdout) == "true" {
		return true, nil
	}
	res, err = readIn(ctx, git, timeout, top, "log", "-1", "--fixed-strings", "--author=<"+author+">", "--format=%H")
	if err != nil {
		return false, fmt.Errorf("git log: %w", err)
	}
	switch {
	case res.ExitCode == 0:
		return strings.TrimSpace(res.Stdout) != "", nil
	case strings.Contains(res.Stderr, "does not have any commits yet"):
		return false, nil
	}
	return false, fmt.Errorf("git log exited %d: %s", res.ExitCode, mask.FirstLine(res.Stderr))
}

// read runs argv on r bounded by timeout (none when timeout <= 0), as
// ui.Read does; ctx is passed on unmarked, so a git read runs with
// GIT_OPTIONAL_LOCKS=0.
func read(ctx context.Context, r runner.Runner, timeout time.Duration, argv ...string) (runner.Result, error) {
	if r == nil {
		return runner.Result{}, errNoRunner
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	return r.Run(ctx, argv...)
}

// readIn is read of `git -C dir args...`.
func readIn(ctx context.Context, git runner.Runner, timeout time.Duration, dir string, args ...string) (runner.Result, error) {
	return read(ctx, git, timeout, append([]string{"-C", dir}, args...)...)
}

// GitDirState is what InspectGitDir found: a held index.lock and the
// operation in progress ("" when none).
type GitDirState struct {
	Locked     bool
	InProgress string
}

// inProgress maps git dir entries to the operation each one marks, in the
// order they are checked.
var inProgress = []struct{ entry, op string }{
	{"MERGE_HEAD", "merge"},
	{"rebase-merge", "rebase"},
	{"rebase-apply", "rebase"},
	{"CHERRY_PICK_HEAD", "cherry-pick"},
	{"REVERT_HEAD", "revert"},
	{"BISECT_LOG", "bisect"},
}

// InspectGitDir reports whether gitDir holds index.lock and which of a
// merge, rebase, cherry-pick, revert or bisect is in progress. It only
// Lstats (a dangling symlink counts) and never removes anything: a lock may
// be the user's own running git.
func InspectGitDir(gitDir string) GitDirState {
	s := GitDirState{Locked: exists(filepath.Join(gitDir, "index.lock"))}
	for _, m := range inProgress {
		if exists(filepath.Join(gitDir, m.entry)) {
			s.InProgress = m.op
			break
		}
	}
	return s
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// BackupAuthor is the email agentsync's destination backups commit as:
// [destination_directory_git_backup].author_email in <home>/agentsync.toml,
// or DefaultBackupAuthor when that is unset, empty or unreadable.
func BackupAuthor(home string) string {
	b, err := os.ReadFile(filepath.Join(home, "agentsync.toml"))
	if err != nil {
		return DefaultBackupAuthor
	}
	var cfg struct {
		Backup struct {
			AuthorEmail string `toml:"author_email"`
		} `toml:"destination_directory_git_backup"`
	}
	if toml.Unmarshal(b, &cfg) != nil || cfg.Backup.AuthorEmail == "" {
		return DefaultBackupAuthor
	}
	return cfg.Backup.AuthorEmail
}
