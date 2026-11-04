package secrets

import (
	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/ui"
	"strings"
)

// remove starts the asynchronous freshness check for removing the selected
// key; the destructive action is only built in verified.
func (s *Screen) remove() tea.Cmd {
	if s.verifying {
		s.note = "still verifying the vault — wait"
		return nil
	}
	key := s.selected()
	if key == "" {
		return nil
	}
	if !actions.ValidID(key) {
		s.note = "invalid secret key"
		return nil
	}
	s.vreq++
	s.verifying, s.vkey, s.note = true, key, "verifying…"
	req, r := s.vreq, s.run
	return func() tea.Msg {
		return verifyMsg{req: req, key: key, fresh: readList(0, r, verifyTimeout)}
	}
}

// verified handles a re-read result: stale requests are dropped; a failed
// read or a vanished key emits nothing.
func (s *Screen) verified(m verifyMsg) tea.Cmd {
	if !s.verifying || m.req != s.vreq {
		return nil
	}
	if s.selected() != m.key {
		s.verifying, s.note = false, ""
		return nil
	}
	s.verifying = false
	if m.fresh.note != "" || m.fresh.vault != vaultOK {
		s.note = "cannot verify the vault"
		if m.fresh.note != "" {
			s.note += ": " + m.fresh.note
		}
		return nil
	}
	if !contains(m.fresh.keys, m.key) {
		s.keys = m.fresh.keys
		s.cursor = max(min(s.cursor, len(s.keys)-1), 0)
		cmd := s.Refresh()
		s.note = staleNote
		return cmd
	}
	act, err := actions.SecretRemove(m.key)
	if err != nil {
		s.note = "invalid secret key"
		return nil
	}
	act.Warning = s.removeWarning(m.key)
	s.note = ""
	return func() tea.Msg { return ui.DoMsg{Action: act} }
}

func (s *Screen) removeWarning(key string) string {
	users := "no known users"
	if ids := s.uses[key]; len(ids) > 0 {
		users = "used by " + strings.Join(ids, ", ")
	}
	if s.srvNote != "" {
		users += " (server files partly unreadable)"
	}
	return "Removes this secret from the vault. Servers that reference ${secret:" + display(key) + "} will fail to resolve: " + users + "."
}
