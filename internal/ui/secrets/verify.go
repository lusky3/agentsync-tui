package secrets

import (
	"context"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

// maxWarnUsers bounds how many referencing servers the remove warning names.
const maxWarnUsers = 5

// remove starts the asynchronous freshness check for removing the selected
// key; the destructive action is only built in verified.
func (s *Screen) remove() tea.Cmd {
	if s.verify.Busy() {
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
	s.vkey, s.note = key, "verifying…"
	r := s.run
	return s.verify.Start(s.base, func(ctx context.Context) any {
		return verification{key: key, fresh: readList(ctx, 0, r, ui.VerifyTimeout)}
	})
}

// verified handles a current re-read result: a failed read or a vanished key
// emits nothing.
func (s *Screen) verified(v verification) tea.Cmd {
	if s.selected() != v.key {
		s.note = ""
		return nil
	}
	if v.fresh.note != "" || v.fresh.vault != vaultOK {
		s.note = "cannot verify the vault"
		if v.fresh.note != "" {
			s.note += ": " + v.fresh.note
		}
		return nil
	}
	if !slices.Contains(v.fresh.keys, v.key) {
		s.keys = v.fresh.keys
		s.list.Clamp(len(s.keys))
		cmd := s.Refresh()
		s.note = staleNote
		return cmd
	}
	act, err := actions.SecretRemove(v.key)
	if err != nil {
		s.note = "invalid secret key"
		return nil
	}
	act.Warning = s.removeWarning(v.key)
	s.note = ""
	return ui.Do(act, s.scope)
}

func (s *Screen) removeWarning(key string) string {
	users := "no known users"
	if ids := s.uses[key]; len(ids) > 0 {
		more := ""
		if len(ids) > maxWarnUsers {
			more = " and " + strconv.Itoa(len(ids)-maxWarnUsers) + " more"
			ids = ids[:maxWarnUsers]
		}
		users = "used by " + strings.Join(ids, ", ") + more
	}
	if s.srvNote != "" {
		users += " (server files partly unreadable)"
	}
	return "Removes this secret from the vault. Servers that reference ${secret:" + display(key) + "} will fail to resolve: " + users + "."
}
