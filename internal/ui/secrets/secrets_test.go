package secrets

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

type fakeRunner struct {
	mu    sync.Mutex
	calls [][]string
	res   runner.Result
	err   error
}

func (f *fakeRunner) Run(_ context.Context, args ...string) (runner.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, args)
	return f.res, f.err
}

func (f *fakeRunner) set(stdout, stderr string, exit int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.res, f.err = runner.Result{Stdout: stdout, Stderr: stderr, ExitCode: exit}, err
}

func (f *fakeRunner) sawGet() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if len(c) > 1 && c[0] == "secret" && c[1] == "get" {
			return true
		}
	}
	return false
}

func view(s *Screen) string { return ansi.Strip(s.View().Content) }

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../../model/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// home writes mcp/<id>.toml files (id -> body) under a temp AGENTSYNC_HOME.
func home(t *testing.T, servers map[string]string) string {
	t.Helper()
	h := t.TempDir()
	if err := os.MkdirAll(filepath.Join(h, "mcp"), 0o755); err != nil {
		t.Fatal(err)
	}
	for id, body := range servers {
		if err := os.WriteFile(filepath.Join(h, "mcp", id+".toml"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return h
}

// drive runs cmd and everything it produces, feeding messages back into the
// screen; DoMsgs are collected, not run.
func drive(s *Screen, cmd tea.Cmd) []tea.Msg {
	var out []tea.Msg
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch m := c().(type) {
		case nil:
		case tea.BatchMsg:
			queue = append(queue, m...)
		case ui.DoMsg:
			out = append(out, m)
		default:
			_, next := s.Update(m)
			queue = append(queue, next)
		}
	}
	return out
}

func loadedWith(t *testing.T, stdout string, servers map[string]string) (*Screen, *fakeRunner) {
	t.Helper()
	fr := &fakeRunner{}
	fr.set(stdout, "", 0, nil)
	s := New(ui.Deps{Runner: fr, Scope: runner.ScopeUser, AgentsyncHome: home(t, servers)})
	s.Update(tea.WindowSizeMsg{Width: 140, Height: 60})
	drive(s, s.Init())
	return s, fr
}

func loaded(t *testing.T) (*Screen, *fakeRunner) {
	return loadedWith(t, fixture(t, "secret-list.txt"), nil)
}

func keyMsg(k string) tea.KeyPressMsg {
	switch k {
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	}
	r := []rune(k)
	return tea.KeyPressMsg{Code: r[0], Text: k}
}

func press(s *Screen, k string) []tea.Msg {
	_, cmd := s.Update(keyMsg(k))
	return drive(s, cmd)
}

func typeStr(s *Screen, str string) {
	for _, r := range str {
		press(s, string(r))
	}
}

func onlyAction(t *testing.T, msgs []tea.Msg) actions.Action {
	t.Helper()
	if len(msgs) != 1 {
		t.Fatalf("want exactly one DoMsg, got %v", msgs)
	}
	return msgs[0].(ui.DoMsg).Action
}

func moveTo(t *testing.T, s *Screen, key string) {
	t.Helper()
	for i, k := range s.keys {
		if k == key {
			s.list.Cursor = i
			return
		}
	}
	t.Fatalf("key %q not listed", key)
}

const srvBody = `[server]
type = "stdio"
command = "run"
`

func TestListsKeysOnly(t *testing.T) {
	s, fr := loadedWith(t, "API_KEY\nGITHUB_TOKEN=CANARYx1\n", nil)
	if !reflect.DeepEqual(fr.calls[0], []string{"secret", "list"}) {
		t.Fatalf("argv = %q", fr.calls[0])
	}
	v := view(s)
	for _, want := range []string{"API_KEY", "GITHUB_TOKEN"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
	if strings.Contains(v, "CANARYx1") || strings.Contains(v, "=") {
		t.Fatalf("value leaked:\n%s", v)
	}
	if !reflect.DeepEqual(s.keys, []string{"API_KEY", "GITHUB_TOKEN"}) {
		t.Fatalf("keys = %q", s.keys)
	}
}

func TestEmptyVaultNotice(t *testing.T) {
	s, _ := loadedWith(t, fixture(t, "secret-list-empty.txt"), nil)
	if v := view(s); !strings.Contains(v, "no secrets stored — press n to add one") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestUsedByColumnFromRefs(t *testing.T) {
	srv := map[string]string{
		"alpha": "[server]\ntype = \"stdio\"\ncommand = \"run\"\n[server.env]\nT = \"${secret:API_KEY}\"\nLIT = \"hunter2CANARYx1\"\n",
		"beta":  "[server]\ntype = \"http\"\nurl = \"https://x.example/mcp\"\n[server.headers]\nAuthorization = \"Bearer ${secret:API_KEY}\"\n",
		"gamma": "[server]\ntype = \"stdio\"\ncommand = \"run\"\nargs = [\"--token=${secret:GITHUB_TOKEN}\"]\n",
		"delta": "[server]\ntype = \"http\"\nurl = \"https://x.example/${secret:API_KEY}\"\n",
		"eps":   "[server]\ntype = \"stdio\"\ncommand = \"${secret:GITHUB_TOKEN}\"\n",
	}
	s, _ := loadedWith(t, fixture(t, "secret-list.txt"), srv)
	v := view(s)
	lines := map[string]string{}
	for _, l := range strings.Split(v, "\n") {
		for _, k := range []string{"API_KEY", "GITHUB_TOKEN"} {
			if strings.Contains(l, k) && strings.Contains(l, "used by") {
				lines[k] = l
			}
		}
	}
	if !strings.Contains(lines["API_KEY"], "used by: alpha, beta, delta") {
		t.Errorf("API_KEY row = %q\n%s", lines["API_KEY"], v)
	}
	if !strings.Contains(lines["GITHUB_TOKEN"], "used by: eps, gamma") {
		t.Errorf("GITHUB_TOKEN row = %q", lines["GITHUB_TOKEN"])
	}
	if strings.Contains(v, "CANARYx1") || strings.Contains(v, "${secret") {
		t.Fatalf("literal or raw ref shown:\n%s", v)
	}
}

func TestMissingKeyWarning(t *testing.T) {
	srv := map[string]string{
		"alpha": "[server]\ntype = \"stdio\"\ncommand = \"run\"\n[server.env]\nT = \"${secret:MISSING}\"\n",
		"beta":  "[server]\ntype = \"stdio\"\ncommand = \"run\"\n[server.env]\nT = \"x-${secret:MISSING}-y\"\nK = \"${secret:API_KEY}\"\n",
	}
	s, _ := loadedWith(t, fixture(t, "secret-list.txt"), srv)
	v := view(s)
	if !strings.Contains(v, "missing: MISSING (used by alpha, beta)") {
		t.Fatalf("view:\n%s", v)
	}
	if strings.Contains(v, "missing: API_KEY") {
		t.Fatal("present key reported missing")
	}
}

func TestBackendNotConfiguredState(t *testing.T) {
	fr := &fakeRunner{}
	fr.set("", fixture(t, "secret-list-notconfigured.txt"), 1, nil)
	s := New(ui.Deps{Runner: fr, Scope: runner.ScopeUser, AgentsyncHome: home(t, nil)})
	drive(s, s.Init())
	v := view(s)
	for _, want := range []string{`set backend = "age"`, "agentsync secret --help"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
	if len(s.keys) != 0 || s.vault != vaultNotConfigured {
		t.Fatalf("state: keys %q vault %v", s.keys, s.vault)
	}
	if strings.Contains(v, "unavailable:") || strings.Contains(strings.ToLower(v), "failed") {
		t.Fatalf("shown as an error:\n%s", v)
	}
}

func TestVaultUnavailableCrossCheck(t *testing.T) {
	fr := &fakeRunner{}
	fr.set("", fixture(t, "secret-list-notconfigured.txt"), 1, nil)
	srv := map[string]string{"alpha": "[server]\ntype = \"stdio\"\ncommand = \"run\"\n[server.env]\nT = \"${secret:FOO}\"\n"}
	s := New(ui.Deps{Runner: fr, Scope: runner.ScopeUser, AgentsyncHome: home(t, srv)})
	drive(s, s.Init())
	v := view(s)
	if !strings.Contains(v, "cannot check — vault unavailable") || strings.Contains(v, "missing:") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestSourceLoadErrorIsDimNote(t *testing.T) {
	h := home(t, map[string]string{"good": srvBody})
	if err := os.WriteFile(filepath.Join(h, "mcp", "bad.toml"), []byte("[[["), 0o600); err != nil {
		t.Fatal(err)
	}
	fr := &fakeRunner{}
	fr.set("API_KEY\n", "", 0, nil)
	s := New(ui.Deps{Runner: fr, AgentsyncHome: h})
	drive(s, s.Init())
	v := view(s)
	if !strings.Contains(v, "API_KEY") || !strings.Contains(v, "servers unavailable") {
		t.Fatalf("view:\n%s", v)
	}
}

func TestSetIsInteractive(t *testing.T) {
	s, _ := loaded(t)
	press(s, "n")
	if !s.CapturesKeys() {
		t.Fatal("field not focused")
	}
	typeStr(s, "NEW_KEY")
	act := onlyAction(t, press(s, "enter"))
	if !act.Interactive || act.Destructive || !reflect.DeepEqual(act.Argv, []string{"secret", "set", "NEW_KEY"}) {
		t.Fatalf("action = %+v", act)
	}
	if strings.Contains(act.Display(), "=") {
		t.Fatalf("display = %q", act.Display())
	}
	if s.CapturesKeys() {
		t.Fatal("field still focused after submit")
	}
}

func TestSetRejectsInvalidNameInline(t *testing.T) {
	s, _ := loaded(t)
	press(s, "n")
	typeStr(s, "-rf")
	if msgs := press(s, "enter"); len(msgs) != 0 {
		t.Fatalf("invalid key emitted %v", msgs)
	}
	if !strings.Contains(view(s), "invalid key name") {
		t.Fatalf("no inline error:\n%s", view(s))
	}
	press(s, "esc")
	if s.CapturesKeys() {
		t.Fatal("esc must close the field")
	}
}

func TestUpdateSelectedKey(t *testing.T) {
	s, _ := loaded(t)
	moveTo(t, s, "GITHUB_TOKEN")
	act := onlyAction(t, press(s, "u"))
	if !act.Interactive || !reflect.DeepEqual(act.Argv, []string{"secret", "set", "GITHUB_TOKEN"}) {
		t.Fatalf("action = %+v", act)
	}
	if s.CapturesKeys() {
		t.Fatal("update must not open a text field")
	}
}

func TestEditIsInteractive(t *testing.T) {
	s, _ := loaded(t)
	act := onlyAction(t, press(s, "E"))
	if !act.Interactive || !reflect.DeepEqual(act.Argv, []string{"secret", "edit"}) {
		t.Fatalf("action = %+v", act)
	}
}

func TestRemoveIsDestructiveWithWarning(t *testing.T) {
	srv := map[string]string{
		"alpha": "[server]\ntype = \"stdio\"\ncommand = \"run\"\n[server.env]\nT = \"${secret:API_KEY}\"\n",
		"beta":  "[server]\ntype = \"stdio\"\ncommand = \"run\"\nargs = [\"${secret:API_KEY}\"]\n",
	}
	s, _ := loadedWith(t, fixture(t, "secret-list.txt"), srv)
	moveTo(t, s, "API_KEY")
	act := onlyAction(t, press(s, "x"))
	if !act.Destructive || !reflect.DeepEqual(act.Argv, []string{"secret", "remove", "API_KEY"}) || act.ConfirmVerb() != "remove" {
		t.Fatalf("action = %+v verb %q", act, act.ConfirmVerb())
	}
	for _, want := range []string{"${secret:API_KEY}", "alpha, beta"} {
		if !strings.Contains(act.Warning, want) {
			t.Errorf("warning %q lacks %q", act.Warning, want)
		}
	}
	moveTo(t, s, "GITHUB_TOKEN")
	act = onlyAction(t, press(s, "x"))
	if !strings.Contains(act.Warning, "no known users") {
		t.Fatalf("warning = %q", act.Warning)
	}
}

func TestNoRevealKey(t *testing.T) {
	var keys []tea.KeyPressMsg
	for r := rune(0x20); r < 0x7f; r++ {
		keys = append(keys, tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	for r := 'a'; r <= 'z'; r++ {
		keys = append(keys,
			tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl},
			tea.KeyPressMsg{Code: r, Mod: tea.ModAlt}, // String() is "alt+g", as a terminal sends it
			tea.KeyPressMsg{Code: r, Mod: tea.ModAlt, Text: string(r)},
			tea.KeyPressMsg{Code: r, Mod: tea.ModAlt | tea.ModShift},
			tea.KeyPressMsg{Code: r, Mod: tea.ModAlt | tea.ModShift, Text: strings.ToUpper(string(r))},
			tea.KeyPressMsg{Code: r, Mod: tea.ModCtrl | tea.ModAlt})
	}
	named := []rune{tea.KeyEnter, tea.KeyEscape, tea.KeyTab, tea.KeyUp, tea.KeyDown, tea.KeyLeft, tea.KeyRight,
		tea.KeyBackspace, tea.KeyDelete, tea.KeyHome, tea.KeyEnd, tea.KeyPgUp, tea.KeyPgDown, tea.KeySpace, tea.KeyInsert}
	for f := tea.KeyF1; f <= tea.KeyF12; f++ {
		named = append(named, f)
	}
	for _, c := range named {
		keys = append(keys, tea.KeyPressMsg{Code: c}, tea.KeyPressMsg{Code: c, Mod: tea.ModShift},
			tea.KeyPressMsg{Code: c, Mod: tea.ModCtrl}, tea.KeyPressMsg{Code: c, Mod: tea.ModAlt})
	}
	for _, typing := range []bool{false, true} {
		reached := 0
		for _, k := range keys {
			// a fresh screen per key, so every key meets the handler for
			// its mode instead of whatever an earlier key left open
			s, fr := loadedWith(t, fixture(t, "secret-list.txt"), nil)
			if typing {
				press(s, "n")
			}
			if s.CapturesKeys() != typing {
				t.Fatalf("typing=%v: wrong mode before %q", typing, k.String())
			}
			_, cmd := s.Update(k)
			reached++
			for _, m := range drive(s, cmd) {
				a := m.(ui.DoMsg).Action
				if len(a.Argv) > 1 && a.Argv[0] == "secret" && a.Argv[1] == "get" {
					t.Fatalf("typing=%v: key %q emitted secret get", typing, k.String())
				}
			}
			if fr.sawGet() {
				t.Fatalf("typing=%v: key %q made the runner run secret get", typing, k.String())
			}
		}
		if reached != len(keys) {
			t.Fatalf("typing=%v: reached %d of %d keys", typing, reached, len(keys))
		}
	}
}

func TestNoValueInputField(t *testing.T) {
	s, _ := loaded(t)
	press(s, "n")
	typeStr(s, "CANARYx1=CANARYx2")
	if v := view(s); strings.Contains(v, "CANARYx1") || strings.Contains(v, "CANARYx2") || strings.Contains(v, "=C") {
		t.Fatalf("typed text rendered after rejection:\n%s", v)
	}
	if s.input.Value != "" {
		t.Fatalf("stored %q", s.input.Value)
	}
	if !strings.Contains(view(s), "key name only") {
		t.Fatalf("no inline rejection:\n%s", view(s))
	}
	if msgs := press(s, "enter"); len(msgs) != 0 {
		t.Fatalf("emitted %v after rejected input", msgs)
	}
	// pasted assignment
	_, cmd := s.Update(tea.PasteMsg{Content: "API_KEY=CANARYx3"})
	drive(s, cmd)
	if s.input.Value != "" || strings.Contains(view(s), "CANARYx3") {
		t.Fatalf("paste stored/rendered: %q", s.input.Value)
	}
	press(s, "esc")
	press(s, "n")
	for _, bad := range []string{" ", ":", "$", "/", ";", "\"", "'", "\\"} {
		press(s, "A")
		press(s, bad)
		typeStr(s, "CANARYx4")
		if s.input.Value != "" || strings.Contains(view(s), "CANARYx4") {
			t.Fatalf("%q accepted: %q", bad, s.input.Value)
		}
		press(s, "esc")
		press(s, "n")
	}
	// after esc a fresh field takes a valid name
	typeStr(s, "OK.key-1")
	act := onlyAction(t, press(s, "enter"))
	if !reflect.DeepEqual(act.Argv, []string{"secret", "set", "OK.key-1"}) {
		t.Fatalf("argv = %q", act.Argv)
	}
}

func TestKeyCapturerWhileTyping(t *testing.T) {
	var _ ui.KeyCapturer = (*Screen)(nil)
	var _ ui.ScopeAware = (*Screen)(nil)
	s, _ := loaded(t)
	if s.CapturesKeys() {
		t.Fatal("captures at rest")
	}
	press(s, "n")
	if !s.CapturesKeys() {
		t.Fatal("not capturing while typing")
	}
	typeStr(s, "q")
	press(s, "esc")
	if s.CapturesKeys() {
		t.Fatal("captures after esc")
	}
}

func TestHostileKeyNeverActionable(t *testing.T) {
	s, fr := loaded(t)
	s.keys = []string{"-rf", "a b", "../x", "ok"}
	for i := 0; i < 3; i++ {
		s.list.Cursor = i
		for _, k := range []string{"u", "x"} {
			if msgs := press(s, k); len(msgs) != 0 {
				t.Fatalf("key %q on %q emitted %v", k, s.keys[i], msgs)
			}
		}
		if s.verify.Busy() {
			t.Fatal("verifying a hostile key")
		}
	}
	n := len(fr.calls)
	s.list.Cursor = 3
	onlyAction(t, press(s, "u"))
	if len(fr.calls) != n {
		t.Fatal("u must not run the CLI")
	}
}

func TestStaleKeyBlocksRemove(t *testing.T) {
	s, fr := loaded(t)
	moveTo(t, s, "API_KEY")
	_, cmd := s.Update(keyMsg("x"))
	if !s.verify.Busy() {
		t.Fatal("not verifying")
	}
	fr.set("GITHUB_TOKEN\n", "", 0, nil) // API_KEY removed behind our back
	if msgs := drive(s, cmd); len(msgs) != 0 {
		t.Fatalf("stale key emitted %v", msgs)
	}
	if !reflect.DeepEqual(s.keys, []string{"GITHUB_TOKEN"}) || s.verify.Busy() {
		t.Fatalf("keys %q verifying %v", s.keys, s.verify.Busy())
	}
	if !strings.Contains(view(s), "changed") {
		t.Fatalf("no stale note:\n%s", view(s))
	}
}

func TestVerifyDoesNotBlockUpdate(t *testing.T) {
	s, _ := loaded(t)
	_, cmd := s.Update(keyMsg("x"))
	if !s.verify.Busy() {
		t.Fatal("not verifying")
	}
	if _, c := s.Update(keyMsg("x")); c != nil {
		t.Fatal("second x must be ignored while verifying")
	}
	onlyAction(t, press(s, "u"))
	onlyAction(t, press(s, "E"))
	press(s, "n")
	if !s.CapturesKeys() {
		t.Fatal("n blocked by verifying")
	}
	press(s, "esc")
	if msgs := drive(s, cmd); len(msgs) != 0 {
		_ = msgs // verification may finish or be dropped; no panic is the point
	}
}

func TestVerifyDroppedWhenItemGone(t *testing.T) {
	s, fr := loaded(t)
	moveTo(t, s, "API_KEY")
	_, cmd := s.Update(keyMsg("x"))
	// a refresh result arrives that no longer has the selected key
	_, _ = s.Update(listMsg{gen: s.reads.Gen(), keys: []string{"GITHUB_TOKEN"}, vault: vaultOK})
	if s.verify.Busy() {
		t.Fatal("verifying not cleared when the item vanished")
	}
	if msgs := drive(s, cmd); len(msgs) != 0 {
		t.Fatalf("late verify emitted %v", msgs)
	}
	// and a new removal is possible
	fr.set("GITHUB_TOKEN\n", "", 0, nil)
	moveTo(t, s, "GITHUB_TOKEN")
	onlyAction(t, press(s, "x"))
}

func TestVerifyDroppedOnCursorMove(t *testing.T) {
	s, _ := loaded(t)
	_, cmd := s.Update(keyMsg("x"))
	press(s, "j")
	if s.verify.Busy() {
		t.Fatal("cursor move must cancel verification")
	}
	if msgs := drive(s, cmd); len(msgs) != 0 {
		t.Fatalf("emitted %v", msgs)
	}
}

func TestVerifyDroppedOnScopeSwitch(t *testing.T) {
	s, _ := loaded(t)
	_, cmd := s.Update(keyMsg("x"))
	s.SetScope(&fakeRunner{}, runner.ScopeProject, s.home)
	if msgs := drive(s, cmd); len(msgs) != 0 || s.verify.Busy() {
		t.Fatalf("emitted %v verifying %v", msgs, s.verify.Busy())
	}
}

func TestStatusFailureKeepsLastList(t *testing.T) {
	s, fr := loaded(t)
	fr.set("", "boom: token=CANARYx1 rejected\nsecond line", 2, nil)
	drive(s, s.Refresh())
	v := view(s)
	if !strings.Contains(v, "API_KEY") || !strings.Contains(v, "boom") {
		t.Fatalf("view:\n%s", v)
	}
	if strings.Contains(v, "CANARYx1") || strings.Contains(v, "second line") {
		t.Fatalf("leak or extra line:\n%s", v)
	}
	fr.set("", "", 0, runner.ErrNotInstalled)
	drive(s, s.Refresh())
	if v := view(s); !strings.Contains(v, "API_KEY") || !strings.Contains(v, "not installed") {
		t.Fatalf("view:\n%s", v)
	}
	fr.set("", "", 0, errors.New("spawn failed"))
	drive(s, s.Refresh())
	if !reflect.DeepEqual(s.keys, []string{"API_KEY", "GITHUB_TOKEN"}) {
		t.Fatalf("keys = %q", s.keys)
	}
}

func TestOutputStoredMasked(t *testing.T) {
	s, fr := loaded(t)
	fr.set("", "boom: token=CANARYx1 rejected", 2, nil)
	drive(s, s.Refresh())
	if strings.Contains(s.loadNote, "CANARYx1") || s.loadNote == "" {
		t.Fatalf("stored note = %q", s.loadNote)
	}
	fr.set("", "\x1b[31m"+strings.TrimSpace(fixture(t, "secret-list-notconfigured.txt"))+" token=CANARYx2", 1, nil)
	drive(s, s.Refresh())
	if strings.Contains(s.vaultMsg, "CANARYx2") || strings.Contains(s.vaultMsg, "\x1b") || s.vaultMsg == "" {
		t.Fatalf("stored message = %q", s.vaultMsg)
	}
	fr.set("GITHUB_TOKEN=CANARYx3\n", "", 0, nil)
	drive(s, s.Refresh())
	if strings.Join(s.keys, "|") != "GITHUB_TOKEN" {
		t.Fatalf("keys = %q", s.keys)
	}
}

func TestServerFieldsMaskedAndSanitised(t *testing.T) {
	srv := map[string]string{
		"alpha": "[server]\ntype = \"stdio\"\ncommand = \"run\"\n[server.env]\nT = \"${secret:A_KEY}\"\nP = \"CANARYx1\"\n",
	}
	s, _ := loadedWith(t, "A_KEY\n", srv)
	for _, l := range s.uses["A_KEY"] {
		if strings.Contains(l, "CANARYx1") {
			t.Fatal("literal stored")
		}
	}
	if v := view(s); strings.Contains(v, "CANARYx1") {
		t.Fatalf("view leaked:\n%s", v)
	}
}

func TestScopeAwareSwapsRunner(t *testing.T) {
	s, old := loaded(t)
	nr := &fakeRunner{}
	nr.set("ONLY_NEW\n", "", 0, nil)
	s.SetScope(nr, runner.ScopeProject, s.home)
	n := len(old.calls)
	drive(s, s.Refresh())
	if len(old.calls) != n || len(nr.calls) == 0 {
		t.Fatalf("old %d->%d new %d", n, len(old.calls), len(nr.calls))
	}
	if !reflect.DeepEqual(s.keys, []string{"ONLY_NEW"}) {
		t.Fatalf("keys = %q", s.keys)
	}
}

func TestInitEqualsRefresh(t *testing.T) {
	fr := &fakeRunner{}
	fr.set("API_KEY\n", "", 0, nil)
	s := New(ui.Deps{Runner: fr, AgentsyncHome: home(t, nil)})
	drive(s, s.Init())
	if len(fr.calls) != 1 || !reflect.DeepEqual(fr.calls[0], []string{"secret", "list"}) || len(s.keys) != 1 {
		t.Fatalf("calls %v keys %v", fr.calls, s.keys)
	}
	if s.Title() != "Secrets" {
		t.Fatalf("title %q", s.Title())
	}
}

func TestStaleListMessageDropped(t *testing.T) {
	s, _ := loaded(t)
	old := s.reads.Gen()
	s.Refresh()
	s.Update(listMsg{gen: old, keys: []string{"STALE"}, vault: vaultOK})
	if reflect.DeepEqual(s.keys, []string{"STALE"}) {
		t.Fatal("old generation applied")
	}
}

const tokenCanary = "ghp_CANARYCANARYCANARYCANARY0123456789"

func TestPasteRejectedInKeyField(t *testing.T) {
	s, _ := loaded(t)
	press(s, "n")
	_, cmd := s.Update(tea.PasteMsg{Content: tokenCanary})
	if msgs := drive(s, cmd); len(msgs) != 0 {
		t.Fatalf("emitted %v", msgs)
	}
	v := view(s)
	if s.input.Value != "" || strings.Contains(v, "CANARY") || !strings.Contains(v, "paste is disabled here") {
		t.Fatalf("input %q view:\n%s", s.input.Value, v)
	}
	if msgs := press(s, "enter"); len(msgs) != 0 {
		t.Fatalf("emitted %v", msgs)
	}
	// a multi-character key event is a paste in disguise
	s.Update(tea.KeyPressMsg{Code: 'g', Text: tokenCanary})
	if s.input.Value != "" || strings.Contains(view(s), "CANARY") {
		t.Fatalf("multi-char key stored %q", s.input.Value)
	}
}

func TestTokenShapedNameRefusedWhileTyping(t *testing.T) {
	s, _ := loaded(t)
	press(s, "n")
	rejected := false
	for _, r := range tokenCanary {
		press(s, string(r))
		if strings.Contains(view(s), "CANARYCANARY") {
			t.Fatalf("token rendered raw after %d chars:\n%s", len(s.input.Value), view(s))
		}
		if s.locked {
			rejected = true
			break
		}
	}
	if !rejected || s.input.Value != "" || !strings.Contains(view(s), "looks like a secret value") {
		t.Fatalf("not rejected: locked %v input %q", s.locked, s.input.Value)
	}
	if msgs := press(s, "enter"); len(msgs) != 0 {
		t.Fatalf("emitted %v", msgs)
	}
}

func TestOverlongNameRefused(t *testing.T) {
	s, _ := loaded(t)
	press(s, "n")
	typeStr(s, strings.Repeat("a", maxNameLen))
	if s.locked {
		t.Fatal("name at the cap refused")
	}
	press(s, "a")
	if !s.locked || s.input.Value != "" || !strings.Contains(view(s), "looks like a secret value") {
		t.Fatalf("over the cap kept: locked %v len %d", s.locked, len(s.input.Value))
	}
}

func TestSubmitRechecksName(t *testing.T) {
	s, _ := loaded(t)
	press(s, "n")
	s.input.Value = tokenCanary // bypass typing: the pre-build check must still refuse
	if msgs := press(s, "enter"); len(msgs) != 0 {
		t.Fatalf("emitted %v", msgs)
	}
	if strings.Contains(view(s), "CANARYCANARY") || s.input.Value != "" {
		t.Fatalf("token kept/rendered: %q", s.input.Value)
	}
}

func TestTokenShapedVaultKeyRenderedMasked(t *testing.T) {
	s, _ := loadedWith(t, "API_KEY\n"+tokenCanary+"\n", nil)
	if v := view(s); strings.Contains(v, "CANARYCANARY") || !strings.Contains(v, "API_KEY") {
		t.Fatalf("view:\n%s", v)
	}
	moveTo(t, s, tokenCanary)
	act := onlyAction(t, press(s, "x"))
	if !reflect.DeepEqual(act.Argv, []string{"secret", "remove", tokenCanary}) {
		t.Fatalf("argv = %q", act.Argv)
	}
	if strings.Contains(act.Display(), "CANARYCANARY") || strings.Contains(act.Warning, "CANARYCANARY") {
		t.Fatalf("display %q warning %q", act.Display(), act.Warning)
	}
}

func TestKeyFieldOpeningCancelsVerify(t *testing.T) {
	s, _ := loaded(t)
	_, cmd := s.Update(keyMsg("x"))
	press(s, "n")
	if s.verify.Busy() {
		t.Fatal("verification survived opening the field")
	}
	if msgs := drive(s, cmd); len(msgs) != 0 {
		t.Fatalf("late verify emitted %v while typing", msgs)
	}
}

// ---- error notes are masked (whole-branch review I-4) ----------------------

// A spawn error whose first line quotes a secret must reach neither the
// stored note nor the view.
func TestSpawnErrorNoteMasksCanary(t *testing.T) {
	s, fr := loaded(t)
	fr.set("", "", 0, errors.New("token=CANARYx1 refused\nsecond line"))
	drive(s, s.Refresh())
	if s.loadNote == "" || strings.Contains(s.loadNote, "CANARY") {
		t.Errorf("stored note %q", s.loadNote)
	}
	if v := view(s); strings.Contains(v, "CANARY") || !strings.Contains(v, "token=") {
		t.Errorf("view:\n%s", v)
	}
}

// A server file whose name cannot be a server id is reported by name; the
// note goes through mask.Text so a secret-shaped name is masked.
func TestServerLoadNoteMasksCanary(t *testing.T) {
	s, _ := loadedWith(t, fixture(t, "secret-list.txt"), map[string]string{
		"good":           "[server]\ntype = 'stdio'\ncommand = 'echo'\n",
		"token=CANARYx1": "[server]\ntype = 'stdio'\ncommand = 'echo'\n",
	})
	if s.srvNote == "" || strings.Contains(s.srvNote, "CANARY") {
		t.Errorf("stored note %q", s.srvNote)
	}
	if v := view(s); strings.Contains(v, "CANARY") || !strings.Contains(v, "servers unavailable: token=") {
		t.Errorf("view:\n%s", v)
	}
}

// ---- remove warning is bounded (whole-branch review m-5) --------------------

func TestRemoveWarningCapsUsers(t *testing.T) {
	s, _ := loaded(t)
	var ids []string
	for i := 0; i < 12; i++ {
		ids = append(ids, fmt.Sprintf("srv%02d", i))
	}
	s.uses = map[string][]string{"API_KEY": ids}
	w := s.removeWarning("API_KEY")
	for _, want := range []string{"srv00", "srv04", "and 7 more"} {
		if !strings.Contains(w, want) {
			t.Errorf("warning missing %q: %q", want, w)
		}
	}
	if strings.Contains(w, "srv05") || strings.Contains(w, "srv11") {
		t.Errorf("warning lists beyond the cap: %q", w)
	}
	s.uses["API_KEY"] = ids[:5]
	if w5 := s.removeWarning("API_KEY"); !strings.Contains(w5, "srv04") || strings.Contains(w5, "more") {
		t.Errorf("five users must be listed in full: %q", w5)
	}
}
