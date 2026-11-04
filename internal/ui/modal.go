package ui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/runner"
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
	act     actions.Action
	verb    string // non-empty: destructive, must be typed
	input   string
	hint    string
	scope   runner.Scope // the scope the command will run under
	project string       // Deps.ProjectDir, shown in project scope
}

func newModal(act actions.Action, scope runner.Scope, project string) *modal {
	m := &modal{act: act, scope: scope, project: project}
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
		// Only y runs it. Enter is refused because the forms (servers,
		// plugins, secrets) submit on enter: a double-press would open the
		// modal and confirm it before the command line could be read.
		switch s {
		case "y":
			a.modal = nil
			return a.start(m.act)
		case "enter":
			m.hint = "press y to run it"
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

// scopeLine says which tree the command operates on: the project directory
// for project scope, "user", or "n/a (user-level)" for a command that the
// CLI refuses --scope/--project on (secret, marketplace, plugin except
// upgrade, doctor, revert).
func (m *modal) scopeLine() string {
	switch {
	case runner.ScopeUnaware(m.act.Argv):
		return "scope: n/a (user-level)"
	case m.scope == runner.ScopeProject:
		return "scope: project — " + mask.Sanitize(m.project)
	}
	return "scope: user"
}

// view renders the modal box within width columns and height lines. Only
// the masked Display() is ever shown. A box taller than height is
// re-rendered with the command line truncated instead of wrapped, then
// without the blank spacer lines, and if still too tall it is clipped from
// the middle (clipMiddle) so the warning, badges and prompt at the bottom
// stay visible.
func (m *modal) view(width, height int) string {
	w := width - 4
	if w > 76 {
		w = 76
	}
	if w < 10 {
		w = 10
	}
	var out string
	for _, mode := range []renderMode{renderFull, renderTruncated, renderCompact} {
		out = m.render(w, mode)
		if height <= 0 || strings.Count(out, "\n")+1 <= height {
			return out
		}
	}
	return clipMiddle(out, height)
}

// renderMode is how much the modal gives up to fit its height.
type renderMode int

const (
	renderFull      renderMode = iota // wrap the command line, blank spacers
	renderTruncated                   // command line cut to one line
	renderCompact                     // truncated and no blank spacer lines
)

// clipMiddle keeps the first two lines of a rendered box (top border and
// title), a dim "…" line, and the last lines, so the bottom is never lost.
func clipMiddle(box string, height int) string {
	lines := strings.Split(box, "\n")
	if len(lines) <= height {
		return box
	}
	if height < 4 {
		return strings.Join(lines[len(lines)-height:], "\n")
	}
	border := lipgloss.RoundedBorder()
	total := lipgloss.Width(lines[0])
	inner := total - lipgloss.Width(border.Left) - lipgloss.Width(border.Right)
	marker := border.Left + lipgloss.NewStyle().Width(inner).Render(" "+styleDim.Render("…")) + border.Right
	keep := append([]string{lines[0], lines[1], marker}, lines[len(lines)-(height-3):]...)
	return strings.Join(keep, "\n")
}

// render builds the box at inner width w in the given mode.
func (m *modal) render(w int, mode renderMode) string {
	var b strings.Builder
	gap := "\n"
	if mode == renderCompact {
		gap = ""
	}
	b.WriteString(styleBold.Render(mask.Sanitize(m.act.Title)) + "\n" + gap)
	cmd := "  $ " + mask.Sanitize(m.act.Display())
	if mode != renderFull {
		cmd = ansi.Truncate(cmd, w-4, "…") // w includes the border and 1-column padding each side
	}
	b.WriteString(cmd + "\n")
	b.WriteString("  " + styleDim.Render(m.scopeLine()) + "\n")
	if m.act.Warning != "" {
		b.WriteString(gap + styleError.Render("! "+mask.Sanitize(m.act.Warning)) + "\n")
	}
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
		b.WriteString(gap + strings.Join(badges, " ") + "\n")
	}
	b.WriteString(gap)
	if m.verb == "" {
		b.WriteString("Run it? y = yes, n/esc = no")
		if m.hint != "" {
			b.WriteString("\n" + styleWarn.Render(m.hint))
		}
	} else {
		fmt.Fprintf(&b, "Type %s to confirm (esc cancels): %s_", styleError.Render(m.verb), m.input)
		if m.hint != "" {
			b.WriteString("\n" + styleWarn.Render(m.hint))
		}
	}
	return styleModal.Width(w).Render(b.String())
}
