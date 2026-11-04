package agents

import (
	"context"
	"errors"
	"os"
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

func (f *fakeRunner) set(stdout string, exit int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.res, f.err = runner.Result{Stdout: stdout, ExitCode: exit}, err
}

func (f *fakeRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// view renders the screen with the screen's own styling stripped, so any
// escape left over came from outside data.
func view(s *Screen) string { return ansi.Strip(s.View().Content) }

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../../model/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

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

func loaded(t *testing.T, stdout string) (*Screen, *fakeRunner) {
	t.Helper()
	fr := &fakeRunner{}
	fr.set(stdout, 0, nil)
	s := New(ui.Deps{Runner: fr, Scope: runner.ScopeUser})
	s.Update(tea.WindowSizeMsg{Width: 120, Height: 60})
	drive(s, s.Refresh())
	return s, fr
}

func press(s *Screen, k string) []tea.Msg {
	var msg tea.KeyPressMsg
	if len(k) == 1 {
		msg = tea.KeyPressMsg{Code: rune(k[0]), Text: k}
	} else {
		msg = tea.KeyPressMsg{Code: tea.KeyDown}
	}
	_, cmd := s.Update(msg)
	return drive(s, cmd)
}

// moveTo selects the agent called name.
func moveTo(t *testing.T, s *Screen, name string) {
	t.Helper()
	for i, a := range s.agents {
		if a.Name == name {
			s.cursor = i
			return
		}
	}
	t.Fatalf("agent %q not in list", name)
}

func onlyAction(t *testing.T, msgs []tea.Msg) actions.Action {
	t.Helper()
	if len(msgs) != 1 {
		t.Fatalf("want exactly one DoMsg, got %v", msgs)
	}
	return msgs[0].(ui.DoMsg).Action
}

func TestListShowsRegisteredAndAvailable(t *testing.T) {
	s, fr := loaded(t, fixture(t, "agent-list-all.txt"))
	if got := fr.calls[0]; !reflect.DeepEqual(got, []string{"agent", "list", "--all"}) {
		t.Fatalf("argv = %q", got)
	}
	v := view(s)
	for _, want := range []string{"claude", "registered", "enabled", "antigravity", "disabled", "amp", "available", "user"} {
		if !strings.Contains(v, want) {
			t.Errorf("view missing %q:\n%s", want, v)
		}
	}
	if s.agents[0].Name != "amazonq" || s.agents[1].Name != "amp" {
		t.Error("CLI order not kept")
	}
}

func TestAddOnlyOnAvailable(t *testing.T) {
	s, _ := loaded(t, fixture(t, "agent-list-all.txt"))
	moveTo(t, s, "claude")
	if msgs := press(s, "a"); len(msgs) != 0 {
		t.Fatalf("add on a registered agent emitted %v", msgs)
	}
	if !strings.Contains(view(s), "already registered") {
		t.Errorf("no reason shown:\n%s", view(s))
	}
	moveTo(t, s, "amp")
	act := onlyAction(t, press(s, "a"))
	if !reflect.DeepEqual(act.Argv, []string{"agent", "add", "amp"}) || act.Destructive {
		t.Fatalf("act = %+v", act)
	}
}

func TestToggleEnableDisable(t *testing.T) {
	s, _ := loaded(t, fixture(t, "agent-list-all.txt"))
	moveTo(t, s, "claude")
	if a := onlyAction(t, press(s, "e")); !reflect.DeepEqual(a.Argv, []string{"agent", "disable", "claude"}) {
		t.Fatalf("enabled agent: %q", a.Argv)
	}
	moveTo(t, s, "antigravity")
	if a := onlyAction(t, press(s, "e")); !reflect.DeepEqual(a.Argv, []string{"agent", "enable", "antigravity"}) {
		t.Fatalf("disabled agent: %q", a.Argv)
	}
	moveTo(t, s, "amp")
	if msgs := press(s, "e"); len(msgs) != 0 {
		t.Fatalf("toggle on available agent emitted %v", msgs)
	}
}

func TestRemoveIsDestructive(t *testing.T) {
	s, _ := loaded(t, fixture(t, "agent-list-all.txt"))
	moveTo(t, s, "claude")
	act := onlyAction(t, press(s, "x"))
	if !act.Destructive || !reflect.DeepEqual(act.Argv, []string{"agent", "remove", "claude"}) {
		t.Fatalf("act = %+v", act)
	}
	if ui.Verb(act) != "remove" {
		t.Fatalf("verb = %q", ui.Verb(act))
	}
	moveTo(t, s, "amp")
	if msgs := press(s, "x"); len(msgs) != 0 {
		t.Fatal("remove on available agent emitted")
	}
}

func TestPurgeHeaderShowsDisplayAndWarning(t *testing.T) {
	s, _ := loaded(t, fixture(t, "agent-list-all.txt"))
	moveTo(t, s, "claude")
	act := onlyAction(t, press(s, "D"))
	want, _ := actions.AgentDisablePurge("claude")
	if !reflect.DeepEqual(act, want) || ui.Verb(act) != "purge" {
		t.Fatalf("act = %+v", act)
	}
	v := view(s)
	if !strings.Contains(v, want.Display()) {
		t.Errorf("header lacks Display %q:\n%s", want.Display(), v)
	}
	flat := strings.Join(strings.Fields(v), " ") // the warning wraps
	if !strings.Contains(flat, "purge deletes the destination files agentsync owns for this agent") ||
		!strings.Contains(flat, "cannot be undone") {
		t.Errorf("warning missing:\n%s", v)
	}
	if strings.Contains(v, "revert") {
		t.Error("revert clause is unverified and must not be promised")
	}
	// Any later key clears the pending notice (the modal was dismissed).
	press(s, "j")
	if strings.Contains(view(s), "purge deletes") {
		t.Error("stale purge warning still shown")
	}
}

func TestStaleListRefreshesBeforeMutation(t *testing.T) {
	for _, key := range []string{"e", "x", "D"} {
		s, fr := loaded(t, fixture(t, "agent-list-all.txt"))
		moveTo(t, s, "claude")
		// claude was disabled behind the user's back.
		fr.set(strings.Replace(fixture(t, "agent-list-all.txt"),
			"claude       registered enabled=true", "claude       registered enabled=false", 1), 0, nil)
		if msgs := press(s, key); len(msgs) != 0 {
			t.Fatalf("%s: emitted on stale state: %v", key, msgs)
		}
		if !strings.Contains(view(s), "agent list changed — refreshed") {
			t.Errorf("%s: no stale note", key)
		}
		if s.agents[s.cursor].Enabled {
			t.Errorf("%s: list not refreshed", key)
		}
	}
	// Agent vanished entirely.
	s, fr := loaded(t, fixture(t, "agent-list-all.txt"))
	moveTo(t, s, "claude")
	fr.set(fixture(t, "agent-list-empty.txt"), 0, nil)
	if msgs := press(s, "D"); len(msgs) != 0 {
		t.Fatal("purge emitted for a vanished agent")
	}
	// Unverifiable (runner fails): no action either.
	s, fr = loaded(t, fixture(t, "agent-list-all.txt"))
	moveTo(t, s, "claude")
	fr.set("", 0, errors.New("boom"))
	if msgs := press(s, "x"); len(msgs) != 0 {
		t.Fatal("emitted though the list could not be re-read")
	}
}

func TestEmptyRegistryState(t *testing.T) {
	s, _ := loaded(t, fixture(t, "agent-list-empty.txt"))
	if !strings.Contains(view(s), "no agents registered — press a on an available agent to add it") {
		t.Errorf("view:\n%s", view(s))
	}
}

func TestStatusFailureKeepsLastList(t *testing.T) {
	s, fr := loaded(t, fixture(t, "agent-list-all.txt"))
	fr.set("", 2, nil)
	fr.res.Stderr = "boom \x1b[31mred\x1b[0m\nsecond line"
	drive(s, s.Refresh())
	v := view(s)
	if !strings.Contains(v, "claude") || !strings.Contains(v, "boom") {
		t.Errorf("last list/note lost:\n%s", v)
	}
	if strings.Contains(v, "second line") || strings.Contains(v, "\x1b") {
		t.Errorf("note must be first sanitized stderr line:\n%q", v)
	}
	fr.set("garbage that is not agents", 0, nil)
	drive(s, s.Refresh())
	if !strings.Contains(view(s), "claude") {
		t.Error("list lost on parse failure")
	}
	fr.set("", 0, runner.ErrNotInstalled)
	drive(s, s.Refresh())
	v = view(s)
	if !strings.Contains(v, "claude") || !strings.Contains(v, "not installed") {
		t.Errorf("spawn error:\n%s", v)
	}
}

func TestHostileAgentNameNeverActionable(t *testing.T) {
	hostile := "ok1 registered enabled=true scope=user\n" +
		"\x1b[31mred registered enabled=true scope=user\n" +
		"bidi\u202ename registered enabled=true scope=user\n" +
		"-rf registered enabled=true scope=user\n" +
		"../x registered enabled=true scope=user\n" +
		"a\x00b registered enabled=true scope=user\n" +
		"evil\u001b]0;pwn\x07 available\n"
	s, _ := loaded(t, hostile)
	if len(s.agents) != 1 || s.agents[0].Name != "ok1" {
		t.Fatalf("agents = %+v", s.agents)
	}
	v := view(s)
	for _, bad := range []string{"\x1b", "\u202e", "\x00", "\x07", "-rf", "../x"} {
		if strings.Contains(v, bad) {
			t.Errorf("view contains %q:\n%q", bad, v)
		}
	}
	for _, k := range []string{"a", "e", "x", "D"} {
		for range s.agents {
			if msgs := press(s, k); len(msgs) > 1 {
				t.Fatal("more than one action")
			}
		}
	}
}

func TestScopeAwareSwapsRunner(t *testing.T) {
	s, old := loaded(t, fixture(t, "agent-list-all.txt"))
	var _ ui.ScopeAware = s
	fresh := &fakeRunner{}
	fresh.set(fixture(t, "agent-list.txt"), 0, nil)
	s.SetScope(fresh, runner.ScopeProject)
	n := old.callCount()
	drive(s, s.Refresh())
	if fresh.callCount() != 1 || old.callCount() != n {
		t.Fatal("refresh did not use the new runner")
	}
}

func TestInitEqualsRefresh(t *testing.T) {
	fr := &fakeRunner{}
	fr.set(fixture(t, "agent-list.txt"), 0, nil)
	s := New(ui.Deps{Runner: fr})
	drive(s, s.Init())
	if fr.callCount() != 1 || len(s.agents) != 2 {
		t.Fatalf("calls=%d agents=%d", fr.callCount(), len(s.agents))
	}
}

func TestKeyCapturerFalse(t *testing.T) {
	var kc ui.KeyCapturer = New(ui.Deps{})
	if kc.CapturesKeys() {
		t.Error("agents screen has no text input")
	}
}

func TestNavigation(t *testing.T) {
	s, _ := loaded(t, fixture(t, "agent-list.txt"))
	press(s, "j")
	if s.cursor != 1 {
		t.Fatal("j")
	}
	press(s, "j")
	if s.cursor != 1 {
		t.Fatal("clamp")
	}
	press(s, "k")
	if s.cursor != 0 {
		t.Fatal("k")
	}
}
