package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
)

// ServersTitle is the title of the MCP servers tab, the screen that opens
// the add form for a ComposeMcpMsg.
const ServersTitle = "MCP servers"

// ComposeMcpMsg asks the shell to open the MCP servers tab's add form
// prefilled from a discovered entry (a discovery screen emits it on
// install). Nothing runs until the user submits the form and confirms the
// resulting `mcp add` in the usual modal.
//
// Spec is the draft install spec; its secrets are only ${secret:...}
// references, never values. RefID is the server id the draft's generated
// references were built with (${secret:<RefID>.<name>}), so the form can
// move them to a new id when the user changes it. Provenance, Trust lines
// and prompt descriptions are network text that may hold \n or \t: they
// are rendered with mask.OneLine.
type ComposeMcpMsg struct {
	Spec       actions.AddSpec
	Prompts    []FieldPrompt
	Provenance string
	Trust      []string
	RefID      string
}

// FieldPrompt is a value the user must still supply before the add can be
// submitted: an environment variable named Name. Default prefills it;
// a Required one may not be left empty. Secret asks for the value to be
// masked as it is typed (a prompt whose name reads like a credential is
// masked too); discovery never sets it, as secrets are references only.
type FieldPrompt struct {
	Name                 string
	Secret, Required     bool
	Default, Description string
}

// FormOpenNote is why a ComposeMcpMsg is refused while the servers tab
// already has an add form open: that form is never replaced.
const FormOpenNote = "finish or close the open form first"

// Refusal is why m must not open a form, or "" when it may: a prompt that
// asks for a secret value (it would be stored as plain text; discovery
// carries secrets as references only), or references generated for
// another id than the spec's (they would land in that server's vault
// namespace if the form were submitted unchanged).
func (m ComposeMcpMsg) Refusal() string {
	if m.RefID != m.Spec.ID {
		return "its secret references name another server id"
	}
	for _, p := range m.Prompts {
		if p.Secret {
			return "it asks for a secret value, which discovery never carries"
		}
	}
	return ""
}

// compose handles a ComposeMcpMsg as a GotoMsg for the servers tab (same
// lookup and busy rule) and then hands the message to that screen alone:
// it is never broadcast, so no other screen can act on it. A message
// refused (composeRefusal) changes no tab.
func (a *App) compose(m ComposeMcpMsg) tea.Cmd {
	if why := a.composeRefusal(m); why != "" {
		a.setStatus(statusWarn, why)
		return nil
	}
	i, cmd, ok := a.jump(ServersTitle)
	if !ok {
		return nil
	}
	return tea.Batch(cmd, a.updateScreen(i, m))
}

// composeRefusal is the status for a ComposeMcpMsg that must not reach
// the servers tab, or "": no project directory, m.Refusal, or a form
// already open there (the screen captures keys only while it has one).
func (a *App) composeRefusal(m ComposeMcpMsg) string {
	if a.noProject {
		return noProjectText
	}
	if why := m.Refusal(); why != "" {
		return "discovered entry not opened: " + why
	}
	for _, s := range a.screens {
		if s.Title() != ServersTitle {
			continue
		}
		if kc, ok := s.(KeyCapturer); ok && kc.CapturesKeys() {
			return ServersTitle + ": " + FormOpenNote
		}
		break
	}
	return ""
}
