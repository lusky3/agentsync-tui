package ui

import (
	"slices"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/runner/runnertest"
)

// notRunScreen records the NotRunMsg the shell broadcasts.
type notRunScreen struct {
	*fakeScreen
	mu  sync.Mutex
	got [][]string // the Keys of each NotRunMsg's actions
}

func (s *notRunScreen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m, ok := msg.(NotRunMsg); ok {
		var keys []string
		for _, a := range m.Actions {
			keys = append(keys, a.Key())
		}
		s.mu.Lock()
		s.got = append(s.got, keys)
		s.mu.Unlock()
	}
	_, cmd := s.fakeScreen.Update(msg)
	return s, cmd
}

func (s *notRunScreen) notRun() [][]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.got)
}

// addCommit is a two-step chain (add, then commit) whose keys a NotRunMsg
// must carry in order after the head action.
func addCommit(t *testing.T) []actions.Action {
	t.Helper()
	paths := []string{"mcp/x.toml", "skills/a.md"}
	return []actions.Action{
		mustAct(actions.GitAdd(testRepo, paths)),
		mustAct(actions.GitCommit(testRepo, "msg", paths)),
	}
}

func keysOf(head actions.Action, then []actions.Action) []string {
	out := []string{head.Key()}
	for _, a := range then {
		out = append(out, a.Key())
	}
	return out
}

func TestCancelledModalBroadcastsNotRun(t *testing.T) {
	for _, k := range []string{"esc", "n"} {
		s := &notRunScreen{fakeScreen: newScreen("A", "a")}
		app := NewApp(gitDeps(runnertest.New(), runnertest.New()), s)
		push, then := gitPush(t), addCommit(t)
		app.Update(DoMsg{Action: push, Then: then})
		if app.modal == nil {
			t.Fatal("no modal")
		}
		_, cmd := app.Update(key(k))
		drain(cmd)
		if got := s.notRun(); len(got) != 1 || !slices.Equal(got[0], keysOf(push, then)) {
			t.Errorf("%s: NotRunMsg %q, want %q", k, got, keysOf(push, then))
		}
	}
}

func TestRefusedRequestBroadcastsNotRun(t *testing.T) {
	s := &notRunScreen{fakeScreen: newScreen("A", "a")}
	app := NewApp(gitDeps(runnertest.New(), runnertest.New()), s)
	app.Update(DoMsg{Action: gitPush(t)})
	fetch, then := gitFetch(t), addCommit(t)
	_, cmd := app.Update(DoMsg{Action: fetch, Then: then}) // busy: the modal is open
	drain(cmd)
	if got := s.notRun(); len(got) != 1 || !slices.Equal(got[0], keysOf(fetch, then)) {
		t.Errorf("NotRunMsg %q, want %q", got, keysOf(fetch, then))
	}
}

// drain runs cmd and its batches, discarding their messages.
func drain(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if b, ok := cmd().(tea.BatchMsg); ok {
		for _, c := range b {
			drain(c)
		}
	}
}
