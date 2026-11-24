package ui

import (
	"os/exec"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/runner"
)

const activeFmt = "active = %d, servers got %d"

// composeScreen records every ComposeMcpMsg it is handed.
type composeScreen struct {
	*fakeScreen
	mu  sync.Mutex
	got []ComposeMcpMsg
}

func newComposeScreen(title string) *composeScreen {
	return &composeScreen{fakeScreen: newScreen(title, title)}
}

func (s *composeScreen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m, ok := msg.(ComposeMcpMsg); ok {
		s.mu.Lock()
		s.got = append(s.got, m)
		s.mu.Unlock()
		return s, nil
	}
	s.fakeScreen.Update(msg)
	return s, nil
}

func (s *composeScreen) received() []ComposeMcpMsg {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ComposeMcpMsg(nil), s.got...)
}

func sampleCompose() ComposeMcpMsg {
	return ComposeMcpMsg{
		Spec:       actions.AddSpec{ID: "brave", Type: "stdio", Command: "docker", Args: []string{"run"}},
		Provenance: "docker catalog: brave",
		RefID:      "brave",
	}
}

// composeApp is a shell with the servers tab between two others.
func composeApp(d Deps) (*App, *composeScreen, *composeScreen, *composeScreen) {
	a, srv, c := newComposeScreen("A"), newComposeScreen(ServersTitle), newComposeScreen("C")
	return NewApp(d, a, srv, c), a, srv, c
}

// The message switches to the servers tab and reaches that screen only,
// never by broadcast.
func TestComposeActivatesServersTabAndOpensForm(t *testing.T) {
	app, a, srv, c := composeApp(baseDeps(newFakeRunner()))
	msg := sampleCompose()
	app.Update(msg)
	if app.active != 1 {
		t.Fatalf("active = %d, want the servers tab (1)", app.active)
	}
	if got := srv.received(); len(got) != 1 || got[0].Spec.ID != msg.Spec.ID || got[0].RefID != msg.RefID {
		t.Errorf("servers screen got %#v", got)
	}
	if len(a.received())+len(c.received()) != 0 {
		t.Error("the message was broadcast to other screens")
	}
	waitRefresh(t, srv.fakeScreen) // a stale tab is refreshed when shown
}

// While a modal is open or an action runs, the tab does not change and no
// form opens.
func TestComposeRefusedWhileBusy(t *testing.T) {
	app, _, srv, _ := composeApp(baseDeps(newFakeRunner()))
	app.Update(DoMsg{Action: actions.PluginUpgradeAll()})
	if app.modal == nil {
		t.Fatal("precondition: modal open")
	}
	refusedCompose(t, app, srv, "with a modal open")
	app.Update(key("y")) // the write is running; its cmd is never executed
	if app.running == nil {
		t.Fatal("precondition: write running")
	}
	refusedCompose(t, app, srv, "with an action running")
}

func refusedCompose(t *testing.T, app *App, srv *composeScreen, when string) {
	t.Helper()
	app.Update(sampleCompose())
	if app.active != 0 {
		t.Errorf("active = %d %s, want 0", app.active, when)
	}
	if n := len(srv.received()); n != 0 {
		t.Errorf("servers screen got %d message(s) %s", n, when)
	}
	if app.statusKind != statusWarn || !strings.HasPrefix(app.status, "busy:") {
		t.Errorf("status = %q (%v) %s", app.status, app.statusKind, when)
	}
}

// In project scope without a project directory the servers tab is replaced
// by a placeholder, so no form is opened behind it.
func TestComposeRefusedWithoutProject(t *testing.T) {
	fr := newFakeRunner()
	d := Deps{Runner: fr, Scope: runner.ScopeProject}
	d.WithScope = func(runner.Scope) (runner.Runner, func(...string) *exec.Cmd) { return fr, nil }
	app, _, srv, _ := composeApp(d)
	app.Update(sampleCompose())
	if app.active != 0 || len(srv.received()) != 0 {
		t.Errorf(activeFmt, app.active, len(srv.received()))
	}
	if !strings.Contains(app.status, "no project directory") {
		t.Errorf("status = %q", app.status)
	}
}

// Without a servers tab the shell warns and forwards nothing.
func TestComposeWithoutServersTabWarns(t *testing.T) {
	a := newComposeScreen("A")
	app := NewApp(baseDeps(newFakeRunner()), a)
	app.Update(sampleCompose())
	if len(a.received()) != 0 || app.statusKind != statusWarn {
		t.Errorf("got %d, status %q", len(a.received()), app.status)
	}
}

// refusedOnTab checks that m was refused with a warning naming want: no
// tab switch, nothing forwarded.
func refusedOnTab(t *testing.T, m ComposeMcpMsg, want string) {
	t.Helper()
	app, _, srv, _ := composeApp(baseDeps(newFakeRunner()))
	app.Update(m)
	if app.active != 0 || len(srv.received()) != 0 {
		t.Errorf(activeFmt, app.active, len(srv.received()))
	}
	if app.statusKind != statusWarn || !strings.Contains(app.status, want) {
		t.Errorf("status = %q (%v)", app.status, app.statusKind)
	}
}

// A prompt that asks for a secret value would store it as plain text:
// discovery carries secrets as references only, so the entry is refused.
func TestSecretPromptRefused(t *testing.T) {
	m := sampleCompose()
	m.Prompts = []FieldPrompt{{Name: "REGION"}, {Name: "PASSWORD", Secret: true}}
	refusedOnTab(t, m, "asks for a secret value")
}

// References built for another id would be submitted in that server's
// vault namespace if the user pressed ctrl+s straight away.
func TestMismatchedRefIDRefused(t *testing.T) {
	m := sampleCompose()
	m.RefID = "other"
	refusedOnTab(t, m, "another server id")
}

// An open form on the servers tab (it captures keys) is never replaced.
func TestComposeRefusedWhileServersFormOpen(t *testing.T) {
	app, _, srv, _ := composeApp(baseDeps(newFakeRunner()))
	srv.capture = true
	app.Update(sampleCompose())
	if app.active != 0 || len(srv.received()) != 0 {
		t.Errorf(activeFmt, app.active, len(srv.received()))
	}
	if !strings.Contains(app.status, "finish or close the open form first") {
		t.Errorf("status = %q", app.status)
	}
}
