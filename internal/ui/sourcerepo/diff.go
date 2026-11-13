package sourcerepo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/gitrepo"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

// diffPane is the masked diff of one file.
type diffPane struct {
	title  string
	lines  []string // masked, sanitised
	trunc  bool
	note   string
	loaded bool
	scroll ui.Scroll
}

// diffMsg carries one diff read: lines are already masked.
type diffMsg struct {
	gen   int
	lines []string
	trunc bool
	note  string
}

// diffKey scrolls the diff, or leaves it on esc.
func (s *Screen) diffKey(k string) tea.Cmd {
	if s.diff.scroll.Key(k, len(s.diff.lines)) {
		return nil
	}
	if k == keyEsc {
		s.diffs.Supersede()
		s.mode, s.diff = modeList, diffPane{}
	}
	return nil
}

// diffable reports whether a file's content may be shown: never a state
// file, a sensitive one or an unusual name (a directory, a special file or
// a name git cannot be given).
func diffable(f gitrepo.File) bool {
	switch f.Class {
	case gitrepo.Owned, gitrepo.Other, gitrepo.Vault:
		return true
	}
	return false
}

// openDiff shows the diff of the file under the cursor, read in the
// background.
func (s *Screen) openDiff() tea.Cmd {
	if why := s.blocked(); why != "" {
		s.note = why
		return nil
	}
	f, ok := s.current()
	switch {
	case !ok:
		return nil
	case !diffable(f):
		s.note = notShown + mask.Sanitize(f.Note)
		return nil
	}
	gen, ctx := s.diffs.Next(s.base)
	s.mode, s.diff = modeDiff, diffPane{title: gitrepo.DisplayName(f.Path)}
	git, repo, unborn, limit := s.git, s.repo, s.status.Branch.Unborn, s.limit
	return func() tea.Msg { return readDiff(ctx, gen, git, repo, unborn, f, limit) }
}

// readDiff reads one file's diff: an untracked file's content as added
// lines, anything else through git diff against HEAD. Every line goes
// through a gitrepo.PatchMasker (MaskPatchLine with the file's post-image
// as context) and the whole is capped at ui.MaxOutput.
func readDiff(ctx context.Context, gen int, git runner.Runner, repo gitrepo.Repo, unborn bool, f gitrepo.File, limit time.Duration) diffMsg {
	if f.Kind == gitrepo.Untracked {
		b, err := bounded(ctx, limit, func(ctx context.Context) (capped, error) { return readFile(ctx, repo.Top, f.Path, ui.MaxOutput) })
		if err != nil {
			return diffMsg{gen: gen, note: notShown + errNote(err)}
		}
		if strings.IndexByte(b.data, 0) >= 0 {
			return diffMsg{gen: gen, note: "binary file: content not shown"}
		}
		return maskedDiff(gen, gitrepo.NewAddedMasker(f.HomeRel, b.data), addedPatch(b.data), b.cut)
	}
	argv := gitrepo.DiffArgv(repo, unborn, f.Path, 3)
	if argv == nil {
		return diffMsg{gen: gen, note: notShown + "git cannot be given this path"}
	}
	res, err := ui.Read(ctx, git, limit, argv...)
	if note := ui.ReadNote("git diff", res, err); note != "" {
		return diffMsg{gen: gen, note: note}
	}
	patch, cut := strings.CutSuffix(res.Stdout, runner.TruncatedMark)
	post, ok := worktreePost(ctx, repo, f, limit)
	if !ok {
		return diffMsg{gen: gen, note: whyNoPost}
	}
	return maskedDiff(gen, gitrepo.NewPatchMasker(f.HomeRel, post), patch, cut)
}

// whyNoPost withholds a config file's diff whose post-image was not read.
const whyNoPost = notShown + "the file could not be read whole, and its diff is masked in its context"

// worktreePost is the post-image of a tracked file's diff against HEAD:
// its first maxScanned bytes in the worktree, cut to whole lines, or ""
// when the diff needs none (a deletion, a symlink's target) or, for a
// file that is not a config file, when it cannot be read. ok is false for
// a config file whose post-image could not be read (replaced by a FIFO,
// timed out) or was cut: an added line's env table or secret multi-line
// body is then not known, so the diff is withheld (whyNoPost).
func worktreePost(ctx context.Context, repo gitrepo.Repo, f gitrepo.File, limit time.Duration) (post string, ok bool) {
	if f.Kind == gitrepo.Deleted {
		return "", true
	}
	b, err := bounded(ctx, limit, func(ctx context.Context) (capped, error) { return readFile(ctx, repo.Top, f.Path, maxScanned) })
	config := gitrepo.IsConfigFile(f.HomeRel)
	switch {
	case errors.Is(err, gitrepo.ErrSymlink):
		return "", true
	case err != nil:
		return "", !config
	case b.cut:
		// gitrepo.PatchMasker needs whole lines: drop the cut one.
		return b.data[:strings.LastIndexByte(b.data, '\n')+1], !config
	}
	return b.data, true
}

// addedPatch shows content as the added lines of a new file.
func addedPatch(content string) string {
	content = strings.TrimSuffix(content, "\n")
	if content == "" {
		return ""
	}
	return "+" + strings.ReplaceAll(content, "\n", "\n+")
}

// whyKeyMaterial is the note for a diff that holds key material.
const whyKeyMaterial = "the diff holds a private key or an age identity: content not shown"

// maskedDiff masks every line of patch through pm and caps the result at
// ui.MaxOutput.
// A patch holding key material (a private-key header or an age identity,
// on an added or a removed line) is not shown at all: the lines of a key
// body are not credential shapes MaskPatchLine would mask.
func maskedDiff(gen int, pm *gitrepo.PatchMasker, patch string, cut bool) diffMsg {
	m := diffMsg{gen: gen, trunc: cut}
	switch {
	case patch == "":
		m.note = "no changes to show"
		return m
	case gitrepo.HoldsKeyMaterial(patch):
		return diffMsg{gen: gen, note: whyKeyMaterial}
	}
	size := 0
	for _, l := range strings.Split(strings.TrimSuffix(patch, "\n"), "\n") {
		l = strings.ReplaceAll(pm.Line(strings.TrimSuffix(l, "\r")), "\t", "  ")
		if size += len(l) + 1; size > ui.MaxOutput {
			m.trunc = true
			break
		}
		m.lines = append(m.lines, l)
	}
	return m
}

// capped is a file read cut at a size: data and whether more followed.
type capped struct {
	data string
	cut  bool
}

// errOutside refuses a file whose parent directory resolves outside the
// toplevel.
var errOutside = errors.New("the path leaves the repository through a symlink")

// afterContained, when set (tests only), runs between readFile's
// containment check and its read.
var afterContained func()

// readFile reads at most limit bytes of the file rel under top through
// gitrepo.ReadRegularIn (no symlink, no FIFO, regular files only, every
// component resolved inside top), once its parent directory is known to
// resolve inside top. The read is bound to top, so a parent swapped for a
// symlink that leaves top after the check fails the read instead of
// reading outside the repository. It stops reading when ctx ends.
func readFile(ctx context.Context, top, rel string, limit int) (capped, error) {
	if err := parentInside(top, rel); err != nil {
		return capped{}, err
	}
	if afterContained != nil {
		afterContained()
	}
	b, err := gitrepo.ReadRegularInContext(ctx, top, rel, int64(limit)+1)
	if err != nil {
		if errors.Is(parentInside(top, rel), errOutside) {
			return capped{}, errOutside
		}
		return capped{}, err
	}
	if len(b) > limit {
		return capped{data: string(b[:limit]), cut: true}, nil
	}
	return capped{data: string(b)}, nil
}

// parentInside is nil when rel's parent directory under top resolves
// inside top, errOutside when it resolves elsewhere, and the resolution's
// error when it cannot be resolved.
func parentInside(top, rel string) error {
	dir, err := filepath.EvalSymlinks(filepath.Dir(filepath.Join(top, filepath.FromSlash(rel))))
	if err != nil {
		return err
	}
	if r, err := filepath.Rel(top, dir); err != nil || (r != "." && !filepath.IsLocal(r)) {
		return errOutside
	}
	return nil
}

// errNote is a display-safe reason for a failed read: never the path's
// bytes beyond what mask.FirstLine keeps.
func errNote(err error) string {
	switch {
	case errors.Is(err, gitrepo.ErrSymlink), errors.Is(err, errOutside):
		return gitrepo.ErrSymlink.Error()
	case errors.Is(err, gitrepo.ErrNotRegular):
		return gitrepo.ErrNotRegular.Error()
	case errors.Is(err, os.ErrNotExist):
		return "the file is gone"
	case errors.Is(err, context.DeadlineExceeded):
		return "the read timed out"
	}
	return "the file could not be read"
}
