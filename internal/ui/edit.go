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
// regular file inside Deps.AgentsyncHome; anything else is refused. The edit
// itself is logged as "edit <basename>".
type EditFileMsg struct {
	Path  string
	After []actions.Action
}

// editDoneMsg reports that the editor exited.
type editDoneMsg struct {
	base  string // display-safe base name
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
	base := oneLine(mask.Text(filepath.Base(m.Path)))
	switch {
	case a.running != nil || a.modal != nil:
		a.setStatus(statusFail, "busy: another action is in progress")
		return nil
	case a.noProject:
		a.setStatus(statusFail, noProjectText)
		return nil
	}
	path, err := resolveInside(a.d.AgentsyncHome, m.Path)
	if err != nil {
		a.setStatus(statusFail, "edit refused: "+base+": "+err.Error())
		return nil
	}
	argv, err := editorArgv(os.Getenv)
	if err != nil {
		a.setStatus(statusFail, "edit refused: "+oneLine(mask.Text(err.Error())))
		return nil
	}
	start := time.Now()
	a.running = &running{act: actions.Action{Title: "Edit " + base}, start: start}
	a.setStatus(statusInfo, "editing "+base+"…")
	// path is absolute (starts with '/'), so it can never read as a flag.
	cmd := exec.Command(argv[0], append(argv[1:], path)...)
	after := append([]actions.Action(nil), m.After...)
	return tea.ExecProcess(cmd, func(err error) tea.Msg {
		var res runner.Result
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			res.ExitCode, err = ee.ExitCode(), nil
		}
		return editDoneMsg{base: base, start: start, after: after, res: res, err: err}
	})
}

// editDone logs the edit, runs the After actions if the editor succeeded and
// refreshes the active screen.
func (a *App) editDone(m editDoneMsg) tea.Cmd {
	a.running = nil
	a.recordDisplay("edit "+m.base, outcome(m.res, m.err), time.Since(m.start))
	if a.quitting {
		return tea.Quit
	}
	var cmds []tea.Cmd
	switch {
	case m.err != nil:
		a.setStatus(statusFail, "editor error: "+m.base+": "+firstLine(mask.Text(m.err.Error())))
	case m.res.ExitCode != 0:
		a.setStatus(statusFail, "editor failed (exit "+strconv.Itoa(m.res.ExitCode)+"): "+m.base)
	default:
		a.setStatus(statusOK, "edited "+m.base)
		a.queue = append(a.queue, m.after...)
		cmds = append(cmds, a.next())
	}
	if s := a.current(); s != nil && !a.noProject {
		cmds = append(cmds, s.Refresh())
	}
	return tea.Batch(cmds...)
}

// next requests the first queued follow-up action, if any.
func (a *App) next() tea.Cmd {
	if len(a.queue) == 0 {
		return nil
	}
	act := a.queue[0]
	a.queue = a.queue[1:]
	return a.request(act)
}
