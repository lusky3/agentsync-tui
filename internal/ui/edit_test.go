package ui

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
)

// editHome builds a temp AgentsyncHome with mcp/<name> holding content.
func editHome(t *testing.T, name string) (home, file string) {
	t.Helper()
	home = t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "mcp"), 0o700); err != nil {
		t.Fatal(err)
	}
	file = filepath.Join(home, "mcp", name)
	if err := os.WriteFile(file, []byte("[server]\ntype = 'stdio'\ncommand = 'echo'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return home, file
}

// fakeEditor writes a script that appends "edited" to its last argument.
func fakeEditor(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fake-editor")
	script := "#!/bin/sh\nfor last; do :; done\nprintf 'edited\\n' >> \"$last\"\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func isEditDone(m tea.Msg) bool { _, ok := m.(editDoneMsg); return ok }

func TestEditFileRefusesPathOutsideHome(t *testing.T) {
	home, inside := editHome(t, "srv.toml")
	outDir := t.TempDir() // sibling of home under the test's temp root
	outside := filepath.Join(outDir, "evil.toml")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "mcp", "link.toml")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"outside":          outside,
		"symlink-out":      link,
		"dotdot-traversal": home + "/mcp/../../" + filepath.Base(outDir) + "/evil.toml",
		"relative":         "mcp/srv.toml",
		"dash-relative":    "-rf",
		"directory":        filepath.Join(home, "mcp"),
		"home-itself":      home,
		"missing":          filepath.Join(home, "mcp", "nope.toml"),
		"empty":            "",
	}
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", fakeEditor(t))
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			fr := newFakeRunner()
			d := baseDeps(fr)
			d.AgentsyncHome = home
			app := NewApp(d)
			_, cmd := app.Update(EditFileMsg{Path: p, After: []actions.Action{actions.Check()}})
			if cmd != nil {
				t.Fatalf("editor would run for %q", p)
			}
			if app.running != nil || !strings.Contains(app.status, "refused") {
				t.Errorf("status %q running %v", app.status, app.running)
			}
			if strings.Contains(app.status, outDir) {
				t.Errorf("status leaks the full path: %q", app.status)
			}
		})
	}
	b, _ := os.ReadFile(outside)
	if string(b) != "x" {
		t.Errorf("outside file modified: %q", b)
	}
	_ = inside
}

func TestEditFileRunsEditorThenAfterActions(t *testing.T) {
	// A file name starting with '-' must reach the editor as an absolute
	// path, never as something that looks like a flag.
	home, file := editHome(t, "-evil.toml")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", fakeEditor(t)+" -w")
	fr := newFakeRunner()
	d := baseDeps(fr)
	d.AgentsyncHome = home
	s := newScreen("A", "a")
	h := start(t, d, s)
	h.tm.Send(EditFileMsg{Path: file, After: []actions.Action{actions.Check()}})
	h.waitMsg("editDoneMsg", isEditDone)
	if got := h.waitStarted(); strings.Join(got, " ") != "check" {
		t.Fatalf("after action argv %q", got)
	}
	fr.respond <- fakeResp{}
	h.waitRun()
	waitRefresh(t, s)
	app := h.final()
	b, err := os.ReadFile(file)
	if err != nil || !strings.HasSuffix(string(b), "edited\n") {
		t.Errorf("editor did not edit the file: %q %v", b, err)
	}
	if !strings.Contains(content(app), "done: Check configuration") {
		t.Errorf("view: %q", content(app))
	}
}

func TestEditorEnvPrecedenceAndFallback(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}
	tests := []struct {
		name    string
		env     map[string]string
		want    []string
		wantErr bool
	}{
		{"visual wins", map[string]string{"VISUAL": "nvim -f", "EDITOR": "nano"}, []string{"nvim", "-f"}, false},
		{"editor next", map[string]string{"EDITOR": "code -w"}, []string{"code", "-w"}, false},
		{"blank visual skipped", map[string]string{"VISUAL": "  ", "EDITOR": "nano"}, []string{"nano"}, false},
		{"quoted path", map[string]string{"EDITOR": `"/opt/my editor/ed" --wait`}, []string{"/opt/my editor/ed", "--wait"}, false},
		{"fallback vi", map[string]string{}, []string{"vi"}, false},
		{"unterminated quote", map[string]string{"EDITOR": `"vim`}, nil, true},
	}
	for _, tt := range tests {
		got, err := editorArgv(env(tt.env))
		if tt.wantErr {
			if err == nil {
				t.Errorf("%s: want error, got %q", tt.name, got)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: got %q, %v; want %q", tt.name, got, err, tt.want)
		}
	}
}

func TestEditFileLogsEntry(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		home, file := editHome(t, "srv.toml")
		t.Setenv("VISUAL", "")
		t.Setenv("EDITOR", "true")
		fr := newFakeRunner()
		d := baseDeps(fr)
		d.AgentsyncHome = home
		d.LogPath = filepath.Join(t.TempDir(), "activity.log")
		h := start(t, d, newScreen("A", "a"))
		h.tm.Send(EditFileMsg{Path: file})
		h.waitMsg("editDoneMsg", isEditDone)
		app := h.final()
		es := app.log.entries()
		if len(es) != 1 || es[0].Display != "edit srv.toml" || es[0].Outcome != "exit 0" {
			t.Fatalf("entries %+v", es)
		}
		b, _ := os.ReadFile(d.LogPath)
		if !strings.Contains(string(b), "edit srv.toml") || strings.Contains(string(b), home) {
			t.Errorf("log file %q", b)
		}
	})
	t.Run("editor failure skips after actions", func(t *testing.T) {
		home, file := editHome(t, "srv.toml")
		t.Setenv("VISUAL", "false")
		fr := newFakeRunner()
		d := baseDeps(fr)
		d.AgentsyncHome = home
		h := start(t, d, newScreen("A", "a"))
		h.tm.Send(EditFileMsg{Path: file, After: []actions.Action{actions.Check()}})
		h.waitMsg("editDoneMsg", isEditDone)
		app := h.final()
		es := app.log.entries()
		if len(es) != 1 || es[0].Outcome != "exit 1" {
			t.Fatalf("entries %+v", es)
		}
		if fr.nCalls() != 0 {
			t.Errorf("after actions ran after a failed edit: %d calls", fr.nCalls())
		}
	})
}

// outcomeScreen records every EditOutcomeMsg it is sent.
type outcomeScreen struct {
	*fakeScreen
	mu   sync.Mutex
	outs []EditOutcomeMsg
}

func (s *outcomeScreen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if o, ok := msg.(EditOutcomeMsg); ok {
		s.mu.Lock()
		s.outs = append(s.outs, o)
		s.mu.Unlock()
	}
	s.fakeScreen.Update(msg)
	return s, nil
}

func (s *outcomeScreen) got() []EditOutcomeMsg {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]EditOutcomeMsg(nil), s.outs...)
}

func TestEditOutcomeBroadcastRefused(t *testing.T) {
	home, _ := editHome(t, "srv.toml")
	outside := filepath.Join(t.TempDir(), "evil.toml")
	if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "mcp", "link.toml")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", fakeEditor(t))
	for name, p := range map[string]string{"symlink": link, "relative": "mcp/srv.toml", "ctrl": "mcp/\x1b[31m\x07evil"} {
		t.Run(name, func(t *testing.T) {
			d := baseDeps(newFakeRunner())
			d.AgentsyncHome = home
			rs := &outcomeScreen{fakeScreen: newScreen("A", "a")}
			app := NewApp(d, rs)
			_, cmd := app.Update(EditFileMsg{Path: p})
			_ = cmd
			outs := rs.got()
			if len(outs) != 1 || outs[0].Path != p || outs[0].Started || outs[0].Finished {
				t.Fatalf("outcomes %+v", outs)
			}
			if !strings.Contains(outs[0].Err, "refused") || strings.ContainsAny(outs[0].Err, "\x1b\x07") || strings.Contains(outs[0].Err, outside) {
				t.Errorf("err %q", outs[0].Err)
			}
		})
	}
	t.Run("busy", func(t *testing.T) {
		d := baseDeps(newFakeRunner())
		d.AgentsyncHome = home
		rs := &outcomeScreen{fakeScreen: newScreen("A", "a")}
		app := NewApp(d, rs)
		app.running = &running{}
		app.Update(EditFileMsg{Path: link})
		if outs := rs.got(); len(outs) != 1 || outs[0].Started || !strings.Contains(outs[0].Err, "busy") {
			t.Errorf("outcomes %+v", outs)
		}
	})
}

func TestEditOutcomeBroadcastStartedFinishedFailed(t *testing.T) {
	for _, tc := range []struct {
		name, editor string
		wantErr      string
		wantCalls    int
	}{
		{"exit 0", "true", "", 1},
		{"exit nonzero", "false", "editor failed (exit 1)", 0},
		{"spawn error", "/nonexistent/editor-\x1b[31m", "editor error", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home, file := editHome(t, "srv.toml")
			t.Setenv("VISUAL", tc.editor)
			fr := newFakeRunner()
			d := baseDeps(fr)
			d.AgentsyncHome = home
			rs := &outcomeScreen{fakeScreen: newScreen("A", "a")}
			h := start(t, d, rs)
			h.tm.Send(EditFileMsg{Path: file, After: []actions.Action{actions.Check()}})
			h.waitMsg("editDoneMsg", isEditDone)
			if tc.wantCalls > 0 {
				h.waitStarted()
				fr.respond <- fakeResp{}
				h.waitRun()
			}
			h.final()
			outs := rs.got()
			if len(outs) != 2 || !outs[0].Started || outs[0].Finished || outs[0].Path != file ||
				!outs[1].Finished || outs[1].Path != file {
				t.Fatalf("outcomes %+v", outs)
			}
			if tc.wantErr == "" && outs[1].Err != "" || !strings.HasPrefix(outs[1].Err, tc.wantErr) {
				t.Errorf("finish err %q want prefix %q", outs[1].Err, tc.wantErr)
			}
			if strings.ContainsAny(outs[1].Err, "\x1b\x07") {
				t.Errorf("unsanitised %q", outs[1].Err)
			}
			if fr.nCalls() != tc.wantCalls {
				t.Errorf("calls %d want %d", fr.nCalls(), tc.wantCalls)
			}
		})
	}
}

// A finished edit may have changed what every other screen shows: they are
// marked stale (and refresh when next shown) while the active one refreshes
// now.
func TestEditDoneMarksOtherScreensStale(t *testing.T) {
	a, b := newScreen("A", "a"), newScreen("B", "b")
	app := NewApp(baseDeps(newFakeRunner()), a, b)
	app.stale[0], app.stale[1] = false, false
	app.running = &running{act: actions.Action{Title: "Edit x"}, start: time.Now()}
	app.Update(editDoneMsg{base: "x", path: "/p/x", start: time.Now()})
	if app.stale[0] || !app.stale[1] {
		t.Fatalf("stale after an edit = %v, want [false true]", app.stale)
	}
	waitRefresh(t, a) // the active screen refreshed at once
	app.Update(key("tab"))
	waitRefresh(t, b) // the stale one when shown
	if app.stale[1] {
		t.Error("B still stale after being shown")
	}
}
