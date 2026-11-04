package secrets

import (
	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
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
		if k.Mod&(tea.ModCtrl|tea.ModAlt) == 0 {
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
		if len(s.input) < 128 {
			s.input += string(r)
			s.fieldErr = ""
		}
	}
}

func keyRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '_' || r == '-'
}
