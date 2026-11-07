package ui

import (
	"slices"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

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

func TestCancelledModalBroadcastsNotRun(t *testing.T) {
	for _, k := range []string{"esc", "n"} {
		s := &notRunScreen{fakeScreen: newScreen("A", "a")}
		app := NewApp(gitDeps(runnertest.New(), runnertest.New()), s)
		push := gitPush(t)
		app.Update(DoMsg{Action: push})
		if app.modal == nil {
			t.Fatal("no modal")
		}
		_, cmd := app.Update(key(k))
		drain(cmd)
		if got := s.notRun(); len(got) != 1 || !slices.Equal(got[0], []string{push.Key()}) {
			t.Errorf("%s: NotRunMsg %q", k, got)
		}
	}
}

func TestRefusedRequestBroadcastsNotRun(t *testing.T) {
	s := &notRunScreen{fakeScreen: newScreen("A", "a")}
	app := NewApp(gitDeps(runnertest.New(), runnertest.New()), s)
	app.Update(DoMsg{Action: gitPush(t)})
	fetch := gitFetch(t)
	_, cmd := app.Update(DoMsg{Action: fetch}) // busy: the modal is open
	drain(cmd)
	if got := s.notRun(); len(got) != 1 || !slices.Equal(got[0], []string{fetch.Key()}) {
		t.Errorf("NotRunMsg %q", got)
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
