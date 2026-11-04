package ui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/mask"
)

// maxConfirmInput caps the typed-confirmation buffer.
const maxConfirmInput = 64

// Verb is the word a user must type to confirm a destructive action: the
// subcommand for grouped commands (mcp/agent/plugin/marketplace/secret),
// otherwise the command itself.
func Verb(act actions.Action) string {
	if len(act.Argv) == 0 {
		return ""
	}
	for _, a := range act.Argv {
		if a == "--purge" {
			return "purge" // a purge must not share the word of a plain disable
		}
	}
	switch act.Argv[0] {
	case "mcp", "agent", "plugin", "marketplace", "secret":
		if len(act.Argv) > 1 {
			return act.Argv[1]
		}
	}
	return act.Argv[0]
}

// modal is the confirm dialog for one pending action.
type modal struct {
	act   actions.Action
	verb  string // non-empty: destructive, must be typed
	input string
	hint  string
}

func newModal(act actions.Action) *modal {
	m := &modal{act: act}
	if act.Destructive {
		m.verb = Verb(act)
		if m.verb == "" {
			m.verb = "yes"
		}
	}
	return m
}

// modalKey handles a key while the modal has focus.
func (a *App) modalKey(k tea.KeyPressMsg) tea.Cmd {
	m := a.modal
	s := k.String()
	if s == "esc" {
		a.modal, a.queue = nil, nil
		a.setStatus(statusInfo, "not run: "+mask.Sanitize(m.act.Title))
		return nil
	}
	if m.verb == "" {
		switch s {
		case "y", "enter":
			a.modal = nil
			return a.start(m.act)
		case "n":
			a.modal, a.queue = nil, nil
			a.setStatus(statusInfo, "not run: "+mask.Sanitize(m.act.Title))
		}
		return nil
	}
	switch s {
	case "enter":
		if m.input == m.verb {
			a.modal = nil
			return a.start(m.act)
		}
		m.hint = fmt.Sprintf("type %q exactly to confirm", m.verb)
	case "backspace":
		if m.input != "" {
			_, n := utf8.DecodeLastRuneInString(m.input)
			m.input = m.input[:len(m.input)-n]
		}
	default:
		if k.Text != "" && len(m.input)+len(k.Text) <= maxConfirmInput {
			m.input += mask.Sanitize(k.Text)
		}
	}
	return nil
}

// view renders the modal box. Only the masked Display() is ever shown.
func (m *modal) view(width int) string {
	var b strings.Builder
	b.WriteString(styleBold.Render(mask.Sanitize(m.act.Title)) + "\n\n")
	b.WriteString("  $ " + mask.Sanitize(m.act.Display()) + "\n")
	var badges []string
	if m.act.Destructive {
		badges = append(badges, styleError.Render("[destructive]"))
	}
	if m.act.Network {
		badges = append(badges, styleWarn.Render("[network]"))
	}
	if m.act.Interactive {
		badges = append(badges, styleInfo.Render("[interactive]"))
	}
	if len(badges) > 0 {
		b.WriteString("\n" + strings.Join(badges, " ") + "\n")
	}
	b.WriteString("\n")
	if m.verb == "" {
		b.WriteString("Run it? y/enter = yes, n/esc = no")
	} else {
		b.WriteString(fmt.Sprintf("Type %s to confirm (esc cancels): %s_", styleError.Render(m.verb), m.input))
		if m.hint != "" {
			b.WriteString("\n" + styleWarn.Render(m.hint))
		}
	}
	w := width - 4
	if w > 76 {
		w = 76
	}
	if w < 10 {
		w = 10
	}
	return styleModal.Width(w).Render(b.String())
}
