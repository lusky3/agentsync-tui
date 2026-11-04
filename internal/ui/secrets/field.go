package secrets

import (
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

// typingKey handles a key while the key-name field has focus. The field is
// a ui.Field whose Accept is the key charset; this screen adds the lock: the
// first refused rune wipes the buffer and ignores everything after it.
func (s *Screen) typingKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		s.typing, s.locked, s.fieldErr = false, false, ""
		s.input.Clear()
		return nil
	case "enter":
		if s.locked {
			return nil
		}
		if s.input.Value == "" {
			s.fieldErr = "enter a key name"
			return nil
		}
		if !plausibleName(s.input.Value) {
			s.lock(valueNote)
			return nil
		}
		act, err := actions.SecretSet(s.input.Value)
		if err != nil {
			s.fieldErr = "invalid key name"
			return nil
		}
		s.typing, s.fieldErr = false, ""
		s.input.Clear()
		return ui.Do(act)
	}
	if s.locked {
		return nil
	}
	switch k.String() {
	case "backspace", "ctrl+u":
		s.input.Key(k)
		return nil
	}
	if k.Text == "" || k.Mod&(tea.ModCtrl|tea.ModAlt) != 0 {
		return nil
	}
	if utf8.RuneCountInString(k.Text) > 1 {
		s.rejectPaste() // a multi-character "key" is a paste in disguise
		return nil
	}
	changed, refused := s.input.Insert(k.Text)
	if refused {
		s.lock(rejectNote) // outside the key charset: wipe and refuse the rest
		return nil
	}
	if changed {
		s.insert()
	}
	return nil
}

// insert checks the field after an edit: a refused rune (outside the key
// charset) or a value-shaped name wipes the buffer and locks the field so
// nothing typed after it (for example the value half of KEY=VALUE) is kept.
func (s *Screen) insert() {
	s.fieldErr = ""
	if !plausibleName(s.input.Value) {
		s.lock(valueNote)
	}
}

// lock wipes the field and refuses further input until esc.
func (s *Screen) lock(why string) {
	s.input.Clear()
	s.locked, s.fieldErr = true, why
}

// rejectPaste refuses pasted text: key names are short and typed, and a
// paste is how a secret value ends up in a name field.
func (s *Screen) rejectPaste() { s.fieldErr = pasteNote }

// maxNameLen is the longest key name the field accepts. Real key names are
// short identifiers; anything longer is far more likely a pasted token, and
// the cap is well under the 128 the CLI would take.
const maxNameLen = 64

// plausibleName reports whether s can be a key name rather than a secret
// value: within the length cap and unchanged by mask.Text (token shapes such
// as ghp_…, sk-…, AKIA… are rewritten by it).
func plausibleName(s string) bool {
	return len(s) <= maxNameLen && mask.Text(s) == s && !mask.TokenShaped(s)
}

func keyRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-'
}
