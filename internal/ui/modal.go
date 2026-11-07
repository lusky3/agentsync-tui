package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/runner"
)

// maxConfirmInput caps the typed-confirmation buffer.
const maxConfirmInput = 64

// modal is the confirm dialog for one pending action, or for a chain
// (act, then then's steps in order) confirmed once as a whole.
type modal struct {
	act     actions.Action
	then    []actions.Action
	verb    string // non-empty: destructive or TypedConfirm, must be typed
	input   Field
	hint    string
	scope   runner.Scope // the scope the command will run under
	project string       // Deps.ProjectDir, shown in project scope
}

func newModal(act actions.Action, scope runner.Scope, project string, then ...actions.Action) *modal {
	m := &modal{act: act, then: then, scope: scope, project: project, input: Field{Max: maxConfirmInput}}
	// The first step that must be typed sets the verb (for add-then-commit
	// with soft findings, the commit's "commit").
	for _, s := range m.steps() {
		if s.Destructive || s.TypedConfirm {
			m.verb = s.ConfirmVerb()
			if m.verb == "" {
				m.verb = "yes"
			}
			break
		}
	}
	return m
}

// steps is every action the modal confirms, in run order.
func (m *modal) steps() []actions.Action {
	return append([]actions.Action{m.act}, m.then...)
}

// cancelModal closes the dialog without running the action (or any step of
// its chain) and drops any queued follow-ups.
func (a *App) cancelModal() {
	a.setStatus(statusInfo, "not run: "+titles(a.modal.steps()))
	a.modal, a.queue = nil, nil
}

// modalKey handles a key while the modal has focus.
func (a *App) modalKey(k tea.KeyPressMsg) tea.Cmd {
	m := a.modal
	s := k.String()
	if s == "esc" {
		a.cancelModal()
		return nil
	}
	if m.verb == "" {
		// Only y runs it. Enter is refused because the forms (servers,
		// plugins, secrets) submit on enter: a double-press would open the
		// modal and confirm it before the command line could be read.
		switch s {
		case "y":
			a.modal = nil
			return a.start(m.act, m.then)
		case "enter":
			m.hint = "press y to run it"
		case "n":
			a.cancelModal()
		}
		return nil
	}
	if s == "enter" {
		if m.input.Value == m.verb {
			a.modal = nil
			return a.start(m.act, m.then)
		}
		m.hint = fmt.Sprintf("type %q exactly to confirm", m.verb)
		return nil
	}
	m.input.Key(k)
	return nil
}

// scopeLine says which tree the command operates on: the project directory
// for project scope, "user", or "n/a (user-level)" for a command that the
// CLI refuses --scope/--project on (secret, marketplace, plugin except
// upgrade, doctor, revert).
// A git action shows the repository it runs in instead.
func (m *modal) scopeLine() string {
	switch {
	case m.act.Program == actions.ProgramGit:
		top := ""
		if len(m.act.Argv) > 1 && m.act.Argv[0] == "-C" {
			top = m.act.Argv[1]
		}
		return "repo: " + mask.Sanitize(top)
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
	steps := m.steps()
	title := mask.Sanitize(m.act.Title)
	if len(steps) > 1 {
		title = joinTitles(steps, ", then ")
	}
	b.WriteString(styleBold.Render(title) + "\n" + gap)
	// One $ line per step, in run order.
	var destructive, network, interactive bool
	for _, s := range steps {
		cmd := "  $ " + mask.Sanitize(s.Display())
		if mode != renderFull {
			cmd = ansi.Truncate(cmd, w-4, "…") // w includes the border and 1-column padding each side
		}
		b.WriteString(cmd + "\n")
		destructive = destructive || s.Destructive
		network = network || s.Network
		interactive = interactive || (s.Interactive && s.Program == "")
	}
	b.WriteString("  " + styleDim.Render(m.scopeLine()) + "\n")
	for _, s := range steps {
		if s.Warning != "" {
			b.WriteString(gap + styleError.Render("! "+mask.Sanitize(s.Warning)) + "\n")
		}
	}
	// TypedConfirm asks for the verb but is not destructive: no badge.
	var badges []string
	if destructive {
		badges = append(badges, styleError.Render("[destructive]"))
	}
	if network {
		badges = append(badges, styleWarn.Render("[network]"))
	}
	if interactive {
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
		fmt.Fprintf(&b, "Type %s to confirm (esc cancels): %s_", styleError.Render(m.verb), m.input.Value)
		if m.hint != "" {
			b.WriteString("\n" + styleWarn.Render(m.hint))
		}
	}
	return styleModal.Width(w).Render(b.String())
}
