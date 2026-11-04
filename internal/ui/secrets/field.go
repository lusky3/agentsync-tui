package secrets

import (
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

// typingKey handles a key while the key-name field has focus.
func (s *Screen) typingKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case "esc":
		s.typing, s.input, s.locked, s.fieldErr = false, "", false, ""
	case "enter":
		if s.locked {
			return nil
		}
		if s.input == "" {
			s.fieldErr = "enter a key name"
			return nil
		}
		if !plausibleName(s.input) {
			s.input, s.locked, s.fieldErr = "", true, valueNote
			return nil
		}
		act, err := actions.SecretSet(s.input)
		if err != nil {
			s.fieldErr = "invalid key name"
			return nil
		}
		s.typing, s.input, s.fieldErr = false, "", ""
		return func() tea.Msg { return ui.DoMsg{Action: act} }
	case "backspace":
		if !s.locked && s.input != "" {
			s.input = s.input[:len(s.input)-1]
		}
	default:
		switch {
		case k.Mod&(tea.ModCtrl|tea.ModAlt) != 0:
		case utf8.RuneCountInString(k.Text) > 1:
			s.rejectPaste() // a multi-character "key" is a paste in disguise
		default:
			s.insert(k.Text)
		}
	}
	return nil
}

// insert adds typed text one rune at a time. The first character outside the
// key charset wipes the buffer and locks the field: nothing typed after it
// (for example the value half of KEY=VALUE) is kept.
func (s *Screen) insert(text string) {
	for _, r := range text {
		if s.locked {
			return
		}
		if !keyRune(r) {
			s.input, s.locked, s.fieldErr = "", true, rejectNote
			return
		}
		s.input += string(r)
		s.fieldErr = ""
		if !plausibleName(s.input) {
			s.input, s.locked, s.fieldErr = "", true, valueNote
			return
		}
	}
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
	return len(s) <= maxNameLen && mask.Text(s) == s
}

func keyRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-'
}
