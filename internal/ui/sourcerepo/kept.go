package sourcerepo

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/gitrepo"
	"github.com/lusky3/agentsync-tui/internal/mask"
)

// keptNote is why a rename's in-home source stays out of a commit, for
// its row, and which command (shown as text, never run) the line above
// the rows suggests for its staged removal.
type keptNote struct {
	text string
	cmd  keptCmd
}

// keptCmd is the command a kept source's note suggests.
type keptCmd int

// The zero keptCmd suggests no command.
const (
	_ keptCmd = iota
	// cmdCommit: the old path is gone and only its name keeps it out;
	// `git commit -- <old>` commits exactly its removal.
	cmdCommit
	// cmdStatus: the old path is now a directory, so `-- <old>` would
	// also match the files under it; the user resolves it outside the TUI.
	cmdStatus
)

// The row notes of a rename source a commit leaves out (Ruling 29).
const (
	keptDir     = "old path is now a directory; its removal stays staged: resolve outside the TUI (command above)"
	keptName    = "old path is a %s path; its removal stays staged (command above)"
	keptUnknown = "old path cannot be inspected; its removal is not part of this commit"
)

// keptSources maps each listed rename whose in-home source a commit leaves
// out to the reason. It runs in a background round (it stats the worktree).
func keptSources(repo gitrepo.Repo, files []gitrepo.File) map[string]keptNote {
	out := map[string]keptNote{}
	for _, f := range files {
		if f.Kind != gitrepo.Renamed || !inHome(repo.Pathspec, f.Orig) {
			continue
		}
		if k := keptReason(repo, f.Orig); k.text != "" {
			out[f.Path] = k
		}
	}
	return out
}

// keptReason is why the in-home rename source orig stays out of a commit,
// or the zero keptNote when it joins it.
func keptReason(repo gitrepo.Repo, orig string) keptNote {
	fi, err := os.Lstat(filepath.Join(repo.Top, filepath.FromSlash(orig)))
	switch {
	case err == nil && fi.IsDir():
		return keptNote{text: keptDir, cmd: cmdStatus}
	case err == nil:
		return keptNote{text: sourceBack}
	case !errors.Is(err, fs.ErrNotExist):
		return keptNote{text: keptUnknown}
	}
	if class := sourceClass(repo, orig); class != "" {
		return keptNote{text: fmt.Sprintf(keptName, class), cmd: cmdCommit}
	}
	return keptNote{}
}

// sourceClass names the class of a rename source whose name keeps it out
// of a commit (state, sensitive, unusual), or "".
func sourceClass(repo gitrepo.Repo, orig string) string {
	switch class, _ := gitrepo.Classify(homeRel(repo.Pathspec, orig), nil, false); {
	case class == gitrepo.StateDir, class == gitrepo.Sensitive, class == gitrepo.Unusual:
		return class.String()
	case actions.ValidGitPath(orig) != nil:
		return gitrepo.Unusual.String()
	}
	return ""
}

// keptLines are the lines above the rows for a kept source whose note
// suggests a command: what to do, then the command on its own line. The
// command is text for the user: the TUI never runs it.
func keptLines(top string, f gitrepo.File, k keptNote) []string {
	name := gitrepo.DisplayName(f.Orig)
	switch k.cmd {
	case cmdCommit:
		old := name
		if !actions.HasControl(f.Orig) {
			old = shellQuote(mask.Text(f.Orig))
		}
		return withPathNote([]string{"to commit the staged removal of " + name + runOutside,
			"  " + gitC + quotePath(top) + literalPathspecs + " commit -- " + old}, top, f.Orig)
	case cmdStatus:
		return withPathNote([]string{"the staged removal of " + name + " now shares its path with a directory; review it" + runOutside,
			"  " + gitC + quotePath(top) + statusCmd}, top)
	}
	return nil
}
