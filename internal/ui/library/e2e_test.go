package library

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/teatest/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

const e2eTimeout = 10 * time.Second

// probe wraps the real shell and reports every message it processed (after
// handling it), so tests wait on messages instead of sleeping.
type probe struct {
	*ui.App
	seen chan tea.Msg
}

func (p probe) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	_, cmd := p.App.Update(msg)
	select {
	case p.seen <- msg:
	default:
	}
	return p, cmd
}

// recScreen is an extra tab that records every EditOutcomeMsg the shell
// broadcasts, so tests can wait on the end of an edit deterministically.
type recScreen struct{ outs chan ui.EditOutcomeMsg }

func (r recScreen) Init() tea.Cmd    { return nil }
func (r recScreen) Refresh() tea.Cmd { return nil }
func (r recScreen) Title() string    { return "rec" }
func (r recScreen) View() tea.View   { return tea.NewView("") }
func (r recScreen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if o, ok := msg.(ui.EditOutcomeMsg); ok {
		select {
		case r.outs <- o:
		default:
		}
	}
	return r, nil
}

type e2e struct {
	rec     recScreen
	t       *testing.T
	tm      *teatest.TestModel
	p       probe
	screens map[Kind]*Screen
	log     []tea.Msg // every message drained so far
}

// startE2E runs the real ui.App (real tea.ExecProcess) over screens of kinds.
func startE2E(t *testing.T, home string, r runner.Runner, kinds ...Kind) *e2e {
	t.Helper()
	d := newDeps(home, r)
	e := &e2e{t: t, screens: map[Kind]*Screen{}, rec: recScreen{outs: make(chan ui.EditOutcomeMsg, 64)}}
	var scr []ui.Screen
	for _, k := range kinds {
		s := New(d, k)
		e.screens[k] = s
		scr = append(scr, s)
	}
	scr = append(scr, e.rec)
	e.p = probe{App: ui.NewApp(d, scr...), seen: make(chan tea.Msg, 4096)}
	e.tm = teatest.NewTestModel(t, e.p, teatest.WithInitialTermSize(120, 40))
	return e
}

func (e *e2e) wait(what string, match func(tea.Msg) bool) tea.Msg {
	e.t.Helper()
	deadline := time.After(e2eTimeout)
	for {
		select {
		case m := <-e.p.seen:
			e.log = append(e.log, m)
			if match(m) {
				return m
			}
		case <-deadline:
			e.t.Fatalf("timed out waiting for %s", what)
			return nil
		}
	}
}

func typeNamed(name string) func(tea.Msg) bool {
	return func(m tea.Msg) bool { return strings.HasSuffix(fmt.Sprintf("%T", m), name) }
}

func (e *e2e) waitLists(n int) {
	e.t.Helper()
	for i := 0; i < n; i++ {
		e.wait("listMsg", func(m tea.Msg) bool { _, ok := m.(listMsg); return ok })
	}
}

// awaitEditEnd blocks until the shell reports the end of the pending edit: a
// refusal (returns false) or the editor having exited (returns true). It must
// be called before finish so that "the editor did not run" assertions are
// made after an editor that was wrongly allowed to run has truly finished.
func (e *e2e) awaitEditEnd() (ran bool) {
	e.t.Helper()
	deadline := time.After(e2eTimeout)
	for {
		select {
		case o := <-e.rec.outs:
			switch {
			case o.Finished:
				return true
			case !o.Started:
				return false
			}
		case <-deadline:
			e.t.Fatal("timed out waiting for the edit outcome")
			return false
		}
	}
}

// finish quits the program; screens may be read safely afterwards.
func (e *e2e) finish() {
	e.t.Helper()
	_ = e.tm.Quit()
	e.tm.FinalModel(e.t, teatest.WithFinalTimeout(e2eTimeout))
	for {
		select {
		case m := <-e.p.seen:
			e.log = append(e.log, m)
		default:
			return
		}
	}
}

func (e *e2e) sawEdit() bool {
	for _, m := range e.log {
		if _, ok := m.(ui.EditFileMsg); ok {
			return true
		}
	}
	return false
}

func (e *e2e) sendKey(k string) { e.tm.Send(key(k)) }

// fakeEditor appends a line to its last argument and drops a marker file.
func fakeEditor(t *testing.T) (script, marker string) {
	t.Helper()
	marker = filepath.Join(t.TempDir(), "editor-ran")
	script = filepath.Join(t.TempDir(), "fake-editor")
	body := "#!/bin/sh\ntouch '" + marker + "'\nfor last; do :; done\nprintf 'pwned\\n' >> \"$last\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return script, marker
}

func setEditor(t *testing.T, ed string) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", ed)
}

func scrView(s *Screen) string { return ansi.Strip(s.View().Content) }

func fileIs(t *testing.T, p, want string) {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil || string(b) != want {
		t.Errorf("%s = %q (%v), want %q", p, b, err, want)
	}
}

// escapeHome builds a home whose skills/evil is a directory symlink to an
// outside directory holding SKILL.md.
func escapeHome(t *testing.T) (home, outsideFile string) {
	t.Helper()
	home, outside := t.TempDir(), t.TempDir()
	outsideFile = filepath.Join(outside, "SKILL.md")
	if err := os.WriteFile(outsideFile, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "skills"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, "skills", "evil")); err != nil {
		t.Fatal(err)
	}
	return home, outsideFile
}

func foreignCheck() ui.RunMsg {
	return ui.RunMsg{Action: actions.Check(), Result: runner.Result{ExitCode: 1, Stderr: "commands/deploy.md: broken"}}
}

// TestSymlinkEscapeRefusedEndToEnd: the screen emits the edit (lstat of a
// path behind a directory symlink sees a regular file); only the shell's
// resolveInside stands between it and the editor. The editor is real, so a
// shell that skips the check would create the marker and modify the file.
func TestSymlinkEscapeRefusedEndToEnd(t *testing.T) {
	home, outsideFile := escapeHome(t)
	ed, marker := fakeEditor(t)
	setEditor(t, ed)
	e := startE2E(t, home, stdoutRunner("evil\n"), Skill)
	e.waitLists(1)
	e.sendKey("E")
	e.wait("EditFileMsg", func(m tea.Msg) bool { _, ok := m.(ui.EditFileMsg); return ok })
	e.awaitEditEnd()
	e.finish()
	if _, err := os.Stat(marker); err == nil {
		t.Error("editor ran on a symlink escape")
	}
	fileIs(t, outsideFile, "outside\n")
	v := ansi.Strip(e.p.View().Content)
	if !strings.Contains(v, "refused") || strings.Contains(v, filepath.Dir(outsideFile)) {
		t.Errorf("view:\n%s", v)
	}
}

// File-level symlink: the screen's lstat refuses first and nothing reaches
// the shell.
func TestFileSymlinkRefusedByScreen(t *testing.T) {
	home, outside := t.TempDir(), filepath.Join(t.TempDir(), "o.md")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "skills", "evil"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(home, "skills", "evil", "SKILL.md")); err != nil {
		t.Fatal(err)
	}
	ed, marker := fakeEditor(t)
	setEditor(t, ed)
	e := startE2E(t, home, stdoutRunner("evil\n"), Skill)
	e.waitLists(1)
	e.sendKey("E")
	// Deterministic end: the stale-item refresh (listMsg) is what a correct
	// screen does next; an EditFileMsg means the screen let it through, so
	// wait for that edit to end before asserting.
	switch e.wait("refresh or edit", func(m tea.Msg) bool {
		_, isList := m.(listMsg)
		_, isEdit := m.(ui.EditFileMsg)
		return isList || isEdit
	}).(type) {
	case ui.EditFileMsg:
		e.awaitEditEnd()
	}
	e.finish()
	if e.sawEdit() {
		t.Error("an EditFileMsg reached the shell for a symlinked file")
	}
	if got := e.screens[Skill].note; got != staleNote {
		t.Errorf("note %q", got)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Error("editor ran")
	}
	fileIs(t, outside, "outside\n")
}

// A screen whose lstat is bypassed (or any other sender) still cannot make
// the shell edit a file symlinked out of the home.
func TestShellRefusesFileSymlinkWhenScreenBypassed(t *testing.T) {
	home, outside := t.TempDir(), filepath.Join(t.TempDir(), "o.md")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "skills", "evil", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	ed, marker := fakeEditor(t)
	setEditor(t, ed)
	e := startE2E(t, home, stdoutRunner("evil\n"), Skill)
	e.waitLists(1)
	e.tm.Send(ui.EditFileMsg{Path: link, After: []actions.Action{actions.Check()}})
	e.wait("EditFileMsg", func(m tea.Msg) bool { _, ok := m.(ui.EditFileMsg); return ok })
	e.awaitEditEnd()
	e.finish()
	if _, err := os.Stat(marker); err == nil {
		t.Error("editor ran on a symlinked file")
	}
	fileIs(t, outside, "outside\n")
	if v := ansi.Strip(e.p.View().Content); !strings.Contains(v, "refused") {
		t.Errorf("view:\n%s", v)
	}
}

// A directory is not a regular file: the shell must refuse it even when the
// sender skipped the screen's lstat.
func TestShellRefusesDirectoryWhenScreenBypassed(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, "skills", "evil", "SKILL.md")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	ed, marker := fakeEditor(t)
	setEditor(t, ed)
	e := startE2E(t, home, stdoutRunner("evil\n"), Skill)
	e.waitLists(1)
	e.tm.Send(ui.EditFileMsg{Path: dir, After: []actions.Action{actions.Check()}})
	e.wait("EditFileMsg", func(m tea.Msg) bool { _, ok := m.(ui.EditFileMsg); return ok })
	e.awaitEditEnd()
	e.finish()
	if _, err := os.Stat(marker); err == nil {
		t.Error("editor ran on a directory")
	}
	if v := ansi.Strip(e.p.View().Content); !strings.Contains(v, "refused") {
		t.Errorf("view:\n%s", v)
	}
}

// Two library screens in one App: an outcome for the Skills path arms only
// Skills, and the following check result is adopted only there.
func TestOutcomeArmsOnlyTheOwningScreen(t *testing.T) {
	home := t.TempDir()
	writeItem(t, home, Skill, "alpha")
	writeItem(t, home, Command, "alpha")
	d := newDeps(home, stdoutRunner("alpha\n"))
	sk, cm := New(d, Skill), New(d, Command)
	for _, s := range []*Screen{sk, cm} {
		drive(s, s.Init())
	}
	skEdit := onlyEdit(t, press(sk, "E"))
	onlyEdit(t, press(cm, "E")) // both screens have a pending edit
	app := ui.NewApp(d, sk, cm)
	app.Update(ui.EditOutcomeMsg{Path: skEdit.Path, Started: true})
	app.Update(ui.RunMsg{Action: actions.Check(), Result: runner.Result{Stdout: "schema valid"}})
	if sk.check == nil || !strings.Contains(scrView(sk), "check passed") {
		t.Errorf("owning screen missed its check: %v", sk.check)
	}
	if cm.check != nil || cm.awaiting {
		t.Errorf("other screen adopted a foreign check: %v awaiting=%v", cm.check, cm.awaiting)
	}
}

func TestForeignCheckIgnoredAfterRefusedEdit(t *testing.T) {
	home, _ := escapeHome(t)
	ed, _ := fakeEditor(t)
	setEditor(t, ed)
	e := startE2E(t, home, stdoutRunner("evil\n"), Skill)
	e.waitLists(1)
	e.sendKey("E")
	e.wait("EditFileMsg", func(m tea.Msg) bool { _, ok := m.(ui.EditFileMsg); return ok })
	e.tm.Send(foreignCheck())
	e.wait("RunMsg", func(m tea.Msg) bool { _, ok := m.(ui.RunMsg); return ok })
	e.finish()
	s := e.screens[Skill]
	if s.check != nil || s.awaiting || s.pending != "" {
		t.Errorf("state after refusal: check=%v awaiting=%v pending=%q", s.check, s.awaiting, s.pending)
	}
	if strings.Contains(scrView(s), "check failed") {
		t.Errorf("foreign check shown:\n%s", scrView(s))
	}
}

func TestForeignCheckIgnoredAfterEditorFailure(t *testing.T) {
	home := t.TempDir()
	writeItem(t, home, Skill, "alpha")
	setEditor(t, "false")
	fr := stdoutRunner("alpha\n")
	e := startE2E(t, home, fr, Skill)
	e.waitLists(1)
	e.sendKey("E")
	e.wait("editDoneMsg", typeNamed("editDoneMsg"))
	e.tm.Send(foreignCheck())
	e.wait("RunMsg", func(m tea.Msg) bool { _, ok := m.(ui.RunMsg); return ok })
	e.finish()
	for _, c := range fr.callList() {
		if c == "check" {
			t.Error("After ran after a failed editor")
		}
	}
	s := e.screens[Skill]
	if s.check != nil || s.awaiting || s.pending != "" {
		t.Errorf("state: check=%v awaiting=%v pending=%q", s.check, s.awaiting, s.pending)
	}
	if strings.Contains(scrView(s), "check failed") {
		t.Errorf("foreign check shown:\n%s", scrView(s))
	}
}

func TestOwnCheckShownAfterSuccessfulEdit(t *testing.T) {
	home := t.TempDir()
	p := writeItem(t, home, Skill, "alpha")
	ed, marker := fakeEditor(t)
	setEditor(t, ed)
	fr := &fakeRunner{fn: func(a ...string) (runner.Result, error) {
		if len(a) == 1 && a[0] == "check" {
			return runner.Result{Stdout: "schema valid; all references resolve\n"}, nil
		}
		return runner.Result{Stdout: "alpha\n"}, nil
	}}
	e := startE2E(t, home, fr, Skill)
	e.waitLists(1)
	e.sendKey("E")
	e.wait("editDoneMsg", typeNamed("editDoneMsg"))
	e.wait("check RunMsg", func(m tea.Msg) bool {
		r, ok := m.(ui.RunMsg)
		return ok && reflect.DeepEqual(r.Action.Argv, []string{"check"})
	})
	e.finish()
	if _, err := os.Stat(marker); err != nil {
		t.Error("editor did not run")
	}
	fileIs(t, p, "x\npwned\n")
	s := e.screens[Skill]
	if s.check == nil || s.check.state != checkPassed || !strings.Contains(scrView(s), "check passed") {
		t.Errorf("own check missing: %v\n%s", s.check, scrView(s))
	}
	if s.awaiting {
		t.Error("awaiting not cleared after consuming the result")
	}
}

func TestOutcomeStateMachine(t *testing.T) {
	mk := func() (*Screen, string) {
		s, home := loaded(t, Skill, stdoutRunner("alpha\n"), "alpha")
		e := onlyEdit(t, press(s, "E"))
		_ = home
		return s, e.Path
	}
	chk := ui.RunMsg{Action: actions.Check(), Result: runner.Result{Stdout: "ok"}}
	t.Run("edit alone does not arm", func(t *testing.T) {
		s, _ := mk()
		s.Update(chk)
		if s.check != nil {
			t.Error("armed before the shell started the editor")
		}
	})
	t.Run("other path ignored", func(t *testing.T) {
		s, _ := mk()
		s.Update(ui.EditOutcomeMsg{Path: "/elsewhere", Started: true})
		s.Update(chk)
		if s.check != nil {
			t.Error("foreign outcome armed the screen")
		}
	})
	t.Run("started then check", func(t *testing.T) {
		s, p := mk()
		s.Update(ui.EditOutcomeMsg{Path: p, Started: true})
		s.Update(ui.EditOutcomeMsg{Path: p, Finished: true})
		s.Update(chk)
		if s.check == nil {
			t.Error("own check dropped")
		}
		s.check = nil
		s.Update(chk)
		if s.check != nil {
			t.Error("second check adopted after the first was consumed")
		}
	})
	t.Run("finished with error disarms", func(t *testing.T) {
		s, p := mk()
		s.Update(ui.EditOutcomeMsg{Path: p, Started: true})
		s.Update(ui.EditOutcomeMsg{Path: p, Finished: true, Err: "editor failed (exit 1)"})
		s.Update(chk)
		if s.check != nil {
			t.Error("armed after a failed editor")
		}
	})
	t.Run("refused disarms", func(t *testing.T) {
		s, p := mk()
		s.Update(ui.EditOutcomeMsg{Path: p, Err: "edit refused"})
		s.Update(ui.EditOutcomeMsg{Path: p, Started: true}) // late/duplicate: pending is gone
		s.Update(chk)
		if s.check != nil {
			t.Error("armed after a refusal")
		}
	})
	t.Run("SetScope disarms", func(t *testing.T) {
		s, p := mk()
		s.Update(ui.EditOutcomeMsg{Path: p, Started: true})
		s.SetScope(stdoutRunner("alpha\n"), runner.ScopeProject, s.home)
		s.Update(chk)
		if s.check != nil || s.awaiting || s.pending != "" {
			t.Errorf("SetScope left state: %v %v %q", s.check, s.awaiting, s.pending)
		}
	})
}

// TestEachScreenKeepsItsOwnList: with every kind registered in one real App,
// Every screen refreshes when it is first shown, with its own generation
// counter; each must still show only its own list, not the broadcast
// results of the others.
func TestEachScreenKeepsItsOwnList(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "memory", "fragments"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"memory/AGENTS.md", "memory/fragments/zz.md"} {
		if err := os.WriteFile(filepath.Join(home, f), []byte("m"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	lists := map[string]string{
		"skill list": "s-one\ns-two\n", "subagent list": "a-one\n", "command list": "c-one\nc-two\nc-three\n",
		"hook list": "PreToolUse\n", "lsp list": "gopls\n",
	}
	fr := &fakeRunner{fn: func(a ...string) (runner.Result, error) {
		return runner.Result{Stdout: lists[strings.Join(a, " ")]}, nil
	}}
	kinds := []Kind{Skill, Subagent, Command, Hook, LSP, Memory}
	e := startE2E(t, home, fr, kinds...)
	for range kinds[1:] {
		e.sendKey("tab") // show each screen once so it loads
	}
	e.waitLists(len(kinds))
	e.finish()
	want := map[Kind][]string{
		Skill: {"s-one", "s-two"}, Subagent: {"a-one"}, Command: {"c-one", "c-two", "c-three"},
		Hook: {"PreToolUse"}, LSP: {"gopls"}, Memory: {"AGENTS", "zz"},
	}
	for _, k := range kinds {
		if got := names(e.screens[k]); !reflect.DeepEqual(got, want[k]) {
			t.Errorf("%s shows %v, want %v", e.screens[k].Title(), got, want[k])
		}
	}
}

// The maintainer's real home is ~/.agentsync -> ~/git/agent-config: E must
// still open the editor when AgentsyncHome is a symlink.
func TestLibraryEditUnderSymlinkedHome(t *testing.T) {
	tmp := t.TempDir()
	real, link := filepath.Join(tmp, "real"), filepath.Join(tmp, "link")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	p := writeItem(t, link, Skill, "alpha")
	ed, marker := fakeEditor(t)
	setEditor(t, ed)
	e := startE2E(t, link, stdoutRunner("alpha\n"), Skill)
	e.waitLists(1)
	e.sendKey("E")
	e.wait("editDoneMsg", typeNamed("editDoneMsg"))
	e.finish()
	if _, err := os.Stat(marker); err != nil {
		t.Error("editor did not run under a symlinked home")
	}
	fileIs(t, p, "x\npwned\n")
	if v := scrView(e.screens[Skill]); strings.Contains(v, "refused") {
		t.Errorf("view:\n%s", v)
	}
}
