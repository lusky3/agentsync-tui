package servers

import (
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

const (
	keyRef   = "${secret:x.k}"
	formOpen = "finish or close the open form first"
	notOpen  = "cannot prefill the add form"
	yKeyRef  = "${secret:y.k}"
	yAuth    = "Authorization: Bearer ${secret:y.api_key}"
	keyFlag  = "--key"
)

// argsCompose is a stdio draft for server x whose args hold generated refs.
func argsCompose(args ...string) ui.ComposeMcpMsg {
	return ui.ComposeMcpMsg{
		Spec:  actions.AddSpec{ID: "x", Type: stdio, Command: "npx", Args: args},
		RefID: "x",
	}
}

// renameFromArgs moves focus from the args field back to the id field and replaces
// the id with typed text.
func renameFromArgs(s *Screen, id string) {
	press(s, keyBackTab, keyBackTab, keyBackTab, keyBackspace)
	typeText(s, id)
}

func argsOf(t *testing.T, s *Screen) []string {
	t.Helper()
	return submitSpec(t, s).Args
}

// The user appends a copy of a generated reference: only the generated
// (first) occurrence follows the id.
func TestRenameRewritesOnlyGeneratedOccurrence(t *testing.T) {
	s := open(t, argsCompose(keyFlag, keyRef))
	press(s, "tab", "tab", "tab")
	typeText(s, " "+keyRef)
	renameFromArgs(s, "y")
	if got, want := argsOf(t, s), []string{keyFlag, yKeyRef, keyRef}; !slices.Equal(got, want) {
		t.Errorf("args %q, want %q", got, want)
	}
}

// Of two generated references the user deletes the last and types it back:
// the retyped one is the user's and stays.
func TestRetypedGeneratedRefIsUsers(t *testing.T) {
	s := open(t, argsCompose(keyRef, keyRef))
	press(s, "tab", "tab", "tab", keyBackspace)
	typeText(s, "}")
	renameFromArgs(s, "y")
	if got, want := argsOf(t, s), []string{yKeyRef, keyRef}; !slices.Equal(got, want) {
		t.Errorf("args %q, want %q", got, want)
	}
}

// A pasted id moves the references at once.
func TestPastedIDMovesRefs(t *testing.T) {
	s := open(t, remoteCompose())
	press(s, keyBackspace)
	s.Update(tea.PasteMsg{Content: "y"})
	if got := submitSpec(t, s).Headers; got[1] != "X-Team: ${secret:y.team_id}" {
		t.Errorf("headers %q", got)
	}
}

// An id another server took after the form opened is refused for the
// rewrite: the taken set is re-read before every key.
func TestIDTakenSinceOpenNotFollowed(t *testing.T) {
	s := open(t, remoteCompose())
	writeServer(t, s.home, "z", offTOML)
	drive(s, s.Refresh())
	press(s, keyBackspace)
	typeText(s, "z")
	if got := submitSpec(t, s).Headers; got[1] != teamHeader {
		t.Errorf("rewrote to a taken id: %q", got)
	}
}

// Re-entering an entry in place makes it the user's, as drop-and-retype
// does: a rename leaves its reference alone.
func TestReenteredEntryIsUsers(t *testing.T) {
	s := open(t, remoteCompose())
	press(s, "tab", "tab", "tab", "tab", "tab")
	typeText(s, "X-Team: shared ${secret:x.team_id}")
	press(s, "enter", keyBackTab, keyBackTab, keyBackTab, keyBackTab, keyBackTab, keyBackspace)
	typeText(s, "y")
	want := []string{yAuth, "X-Team: shared ${secret:x.team_id}"}
	if got := submitSpec(t, s).Headers; !slices.Equal(got, want) {
		t.Errorf("headers %q, want %q", got, want)
	}
}

// pendingBrave opens the brave draft, submits it and returns the action.
func pendingBrave(t *testing.T, id string) (*Screen, actions.Action) {
	t.Helper()
	m := braveCompose()
	m.Spec.ID, m.RefID = id, id
	m.Spec.Env = map[string]string{braveEnv: "${secret:" + id + ".brave_api_key}"}
	s := open(t, m)
	s.run = &scripted{}
	return s, onlyDo(t, press(s, keySave))
}

func vaultReads(s *Screen) int { return s.run.(*scripted).ran(secretList) }

// upd hands msg to s and returns its command.
func upd(s *Screen, msg tea.Msg) tea.Cmd {
	_, cmd := s.Update(msg)
	return cmd
}

// runCmd executes cmd once and returns its message (nil for none).
func runCmd(cmd tea.Cmd) tea.Msg {
	if cmd == nil {
		return nil
	}
	return cmd()
}

func TestVaultResultAfterScopeSwitchIgnored(t *testing.T) {
	s, act := pendingBrave(t, "brave")
	msg := runCmd(upd(s, ui.RunMsg{Action: act}))
	if msg == nil {
		t.Fatal("precondition: no vault read started")
	}
	s.SetScope(s.run, runner.ScopeProject, s.home)
	s.Update(msg)
	if s.note != "" {
		t.Errorf("note after scope switch: %q", s.note)
	}
}

func TestAddPendingDroppedOnScopeSwitch(t *testing.T) {
	s, act := pendingBrave(t, "brave")
	s.SetScope(s.run, runner.ScopeProject, s.home)
	if runCmd(upd(s, ui.RunMsg{Action: act})) != nil || vaultReads(s) != 0 {
		t.Error("a read started for an add from before the scope switch")
	}
}

func TestOlderVaultResultIgnored(t *testing.T) {
	s, first := pendingBrave(t, "first")
	msg1 := runCmd(upd(s, ui.RunMsg{Action: first}))
	s.Update(braveCompose())
	second := onlyDo(t, press(s, keySave))
	msg2 := runCmd(upd(s, ui.RunMsg{Action: second}))
	if msg1 == nil || msg2 == nil {
		t.Fatal("precondition: two vault reads")
	}
	s.Update(msg2)
	s.Update(msg1)
	if !strings.Contains(s.note, "added brave") || strings.Contains(s.note, "first") {
		t.Errorf("note %q", s.note)
	}
}

func TestOtherRunStartsNoVaultRead(t *testing.T) {
	s, _ := pendingBrave(t, "brave")
	other := mustAct(actions.McpRemove("brave"))
	if runCmd(upd(s, ui.RunMsg{Action: other})) != nil || vaultReads(s) != 0 {
		t.Error("an unrelated run started a vault read")
	}
}

func TestNotRunAddStartsNoVaultRead(t *testing.T) {
	s, act := pendingBrave(t, "brave")
	s.Update(ui.NotRunMsg{Actions: []actions.Action{act}})
	if runCmd(upd(s, ui.RunMsg{Action: act})) != nil || vaultReads(s) != 0 {
		t.Error("a cancelled add started a vault read")
	}
}

// A message that does not survive the form unchanged is refused: the form
// would submit something else than was shown for review.
func TestUnrepresentableDraftRefused(t *testing.T) {
	long := strings.Repeat("\xff", maxField+1)
	cases := map[string]func(*ui.ComposeMcpMsg){
		"newline in arg":     func(m *ui.ComposeMcpMsg) { m.Spec.Args = []string{"--x\n--evil"} },
		"control in arg":     func(m *ui.ComposeMcpMsg) { m.Spec.Args = []string{"a\x1bb"} },
		"bidi in arg":        func(m *ui.ComposeMcpMsg) { m.Spec.Args = []string{"a\u202eb"} },
		"duplicate header":   func(m *ui.ComposeMcpMsg) { m.Spec.Headers = []string{"A: 1", "A: 2"} },
		"invalid utf-8 long": func(m *ui.ComposeMcpMsg) { m.Spec.Command = long },
		"prompt default":     func(m *ui.ComposeMcpMsg) { m.Prompts = []ui.FieldPrompt{{Name: "R", Default: "a\nb"}} },
	}
	for name, mut := range cases {
		m := braveCompose()
		mut(&m)
		s := newLoaded(t, fixtureHome(t), okRunner())
		s.Update(m)
		if s.form != nil || !strings.Contains(s.note, notOpen) {
			t.Errorf("%s: form %v note %q", name, s.form != nil, s.note)
		}
	}
}

// An open form is never replaced by a discovered entry.
func TestComposeRefusedWhileFormOpen(t *testing.T) {
	app, scr := shell(t, okRunner())
	appDrive(app, func() tea.Msg { return braveCompose() })
	appType(app, "-edited")
	appDrive(app, func() tea.Msg { return remoteCompose() })
	if got := strings.TrimSpace(scr.form.vals[fID].Value); got != "brave-edited" {
		t.Errorf("open form replaced: id %q", got)
	}
	if v := appView(app); !strings.Contains(v, formOpen) {
		t.Errorf("no refusal shown:\n%s", v)
	}
	// The screen refuses too when handed the message directly.
	scr.Update(remoteCompose())
	if got := scr.form.vals[fID].Value; got != "brave-edited" {
		t.Errorf("open form replaced on the screen: id %q", got)
	}
}

// A prompt asking for a secret value, or references built for another id,
// never reach a form, even handed to the screen directly.
func TestScreenRefusesSecretPromptAndMismatchedRefs(t *testing.T) {
	secret := braveCompose()
	secret.Prompts = []ui.FieldPrompt{{Name: "PASSWORD", Secret: true}}
	other := remoteCompose()
	other.RefID = "z"
	for name, m := range map[string]ui.ComposeMcpMsg{"secret prompt": secret, "ref id": other} {
		s := newLoaded(t, fixtureHome(t), okRunner())
		s.Update(m)
		if s.form != nil || !strings.Contains(s.note, notOpen) {
			t.Errorf("%s: form %v note %q", name, s.form != nil, s.note)
		}
	}
}
