package ui

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/shellwords"
)

// EditFileMsg asks the shell to open Path in the user's editor ($VISUAL,
// then $EDITOR, then vi) and, once the editor exits 0, to run each After
// action through the normal path (confirm modal unless ReadOnly, logging,
// refresh). Path must be absolute and resolve, symlinks included, to a
// regular file inside the current scope's canonical home (HomeFor: the user
// tree in user scope, <project>/.agentsync in project scope); anything else
// is refused. The edit itself is logged as "edit <basename>".
type EditFileMsg struct {
	Path  string
	After []actions.Action
}

// EditOutcomeMsg is broadcast to every screen for each EditFileMsg outcome,
// so a screen that asked for an edit learns what became of it. Path is the
// Path of the originating EditFileMsg. Exactly one shape is sent per stage:
//
//   - refused:   Started=false, Finished=false, Err = short sanitised reason
//   - started:   Started=true,  Finished=false (the editor is about to run)
//   - finished:  Finished=true; Err == "" means the editor exited 0 and the
//     After actions were queued; otherwise Err is a short sanitised reason
//     (non-zero exit or spawn error) and After is skipped.
type EditOutcomeMsg struct {
	Path     string
	Started  bool
	Finished bool
	Err      string
}

// editOutcome broadcasts m (reason sanitised) to every screen.
func (a *App) editOutcome(m EditOutcomeMsg) tea.Cmd {
	m.Err = logSafe(mask.Text(m.Err))
	return tea.Batch(a.broadcast(m)...)
}

// refuseEdit reports a refused edit on the status line and to the screens.
func (a *App) refuseEdit(path, reason string) tea.Cmd {
	a.setStatus(statusFail, reason)
	return a.editOutcome(EditOutcomeMsg{Path: path, Err: reason})
}

// editDoneMsg reports that the editor exited.
type editDoneMsg struct {
	base  string // display-safe base name
	path  string // the EditFileMsg path, echoed in EditOutcomeMsg
	start time.Time
	after []actions.Action
	res   runner.Result
	err   error
}

// editorArgv returns the editor command line: $VISUAL, else $EDITOR, else
// vi. Values are split with shellwords so "code -w" works.
func editorArgv(getenv func(string) string) ([]string, error) {
	for _, k := range []string{"VISUAL", "EDITOR"} {
		v := getenv(k)
		if strings.TrimSpace(v) == "" {
			continue
		}
		w, err := shellwords.Split(v)
		if err != nil {
			return nil, fmt.Errorf("$%s: %v", k, err)
		}
		if len(w) > 0 {
			return w, nil
		}
	}
	return []string{"vi"}, nil
}

// resolveInside returns the symlink-free absolute form of p if it is a
// regular file strictly inside home. Errors never include either path.
func resolveInside(home, p string) (string, error) {
	if home == "" {
		return "", errors.New("no agentsync home")
	}
	if !filepath.IsAbs(p) {
		return "", errors.New("path is not absolute")
	}
	h, err := filepath.EvalSymlinks(home)
	if err == nil {
		h, err = filepath.Abs(h)
	}
	if err != nil {
		return "", errors.New("agentsync home cannot be resolved")
	}
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", errors.New("file cannot be resolved")
	}
	rel, err := filepath.Rel(h, r)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errors.New("outside the agentsync home")
	}
	fi, err := os.Stat(r)
	if err != nil || !fi.Mode().IsRegular() {
		return "", errors.New("not a regular file")
	}
	return r, nil
}

// editFile handles an EditFileMsg inside Update.
func (a *App) editFile(m EditFileMsg) tea.Cmd {
	base := logSafe(mask.Text(filepath.Base(m.Path)))
	switch {
	case a.running != nil || a.modal != nil:
		return a.refuseEdit(m.Path, "busy: another action is in progress")
	case a.noProject:
		return a.refuseEdit(m.Path, noProjectText)
	}
	path, err := resolveInside(a.home, m.Path)
	if err != nil {
		a.setStatus(statusFail, "edit refused: "+base+": "+err.Error())
		return a.editOutcome(EditOutcomeMsg{Path: m.Path, Err: "edit refused: " + err.Error()})
	}
	argv, err := editorArgv(os.Getenv)
	if err != nil {
		return a.refuseEdit(m.Path, "edit refused: "+logSafe(mask.Text(err.Error())))
	}
	start := time.Now()
	a.running = &running{act: actions.Action{Title: "Edit " + base}, start: start}
	a.setStatus(statusInfo, "editing "+base+"…")
	// path is absolute (starts with '/'), so it can never read as a flag.
	cmd := exec.Command(argv[0], append(argv[1:], path)...)
	after := append([]actions.Action(nil), m.After...)
	started := a.editOutcome(EditOutcomeMsg{Path: m.Path, Started: true})
	return tea.Batch(started, tea.ExecProcess(cmd, func(err error) tea.Msg {
		res, err := exitResult(err)
		return editDoneMsg{base: base, path: m.Path, start: start, after: after, res: res, err: err}
	}))
}

// editDone logs the edit, runs the After actions if the editor succeeded and
// refreshes the active screen; the other screens are marked stale, since
// the edit may have changed what they show.
func (a *App) editDone(m editDoneMsg) tea.Cmd {
	a.running = nil
	a.recordDisplay("edit "+m.base, outcome(m.res, m.err), time.Since(m.start))
	if a.quitting {
		return tea.Quit
	}
	var cmds []tea.Cmd
	fin := EditOutcomeMsg{Path: m.path, Finished: true}
	switch {
	case m.err != nil:
		a.setStatus(statusFail, "editor error: "+m.base+": "+mask.FirstLine(m.err.Error()))
		fin.Err = "editor error: " + mask.FirstLine(m.err.Error())
	case m.res.ExitCode != 0:
		a.setStatus(statusFail, "editor failed (exit "+strconv.Itoa(m.res.ExitCode)+"): "+m.base)
		fin.Err = "editor failed (exit " + strconv.Itoa(m.res.ExitCode) + ")"
	default:
		a.setStatus(statusOK, "edited "+m.base)
		a.queue = append(a.queue, m.after...)
		cmds = append(cmds, a.next())
	}
	// Broadcast before the queued After action is requested so a screen
	// has adopted the outcome by the time its RunMsg arrives.
	cmds = append([]tea.Cmd{a.editOutcome(fin)}, cmds...)
	a.markOthersStale()
	return tea.Batch(append(cmds, a.refreshActive())...)
}

// next requests the first queued follow-up action, if any.
func (a *App) next() tea.Cmd {
	if len(a.queue) == 0 {
		return nil
	}
	act := a.queue[0]
	a.queue = a.queue[1:]
	return a.request(act, "") // a follow-up of the shell's own, not bound to a scope
}
