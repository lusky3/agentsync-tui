package servers

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/model"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

// noteAdded starts every after-add note.
const noteAdded = "added "

// vaultRefRe finds ${secret:KEY} references anywhere in a value (the vault
// key rule, as the Secrets tab reads them).
var vaultRefRe = regexp.MustCompile(`\$\{secret:([A-Za-z0-9][A-Za-z0-9._-]*)\}`)

// pendingAdd is an add submitted from the form whose outcome is awaited:
// once its RunMsg reports success, the vault is listed and the note names
// the referenced secrets it does not hold.
type pendingAdd struct {
	argv []string
	id   string
	keys []string // sorted, unique vault keys the server references
}

// vaultMsg carries the `secret list` read behind the after-add note.
type vaultMsg struct {
	gen  int
	add  pendingAdd
	have []string
	note string // why the vault could not be listed, display-safe
}

// secretKeys is every vault key s references, sorted and unique.
func secretKeys(s actions.AddSpec) []string {
	vals := []string{s.Command, s.URL}
	vals = append(vals, s.Args...)
	vals = append(vals, s.Headers...)
	for _, v := range s.Env {
		vals = append(vals, v)
	}
	var keys []string
	for _, v := range vals {
		for _, m := range vaultRefRe.FindAllStringSubmatch(v, -1) {
			keys = append(keys, m[1])
		}
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

// expectAdd remembers a submitted add that references vault secrets.
func (s *Screen) expectAdd(act actions.Action, spec actions.AddSpec) {
	s.added = nil
	if keys := secretKeys(spec); len(keys) > 0 {
		s.added = &pendingAdd{argv: act.Argv, id: spec.ID, keys: keys}
	}
}

// addOutcome handles a finished run: after the awaited add succeeded it
// lists the vault in the background (never inside Update).
func (s *Screen) addOutcome(m ui.RunMsg) tea.Cmd {
	p := s.added
	if p == nil || !slices.Equal(m.Action.Argv, p.argv) {
		return nil
	}
	s.added = nil
	if m.Err != nil || m.Result.ExitCode != 0 {
		return nil
	}
	gen, ctx := s.vault.Next(s.base)
	r, limit := s.run, s.limit
	return func() tea.Msg { return readVault(ctx, gen, r, limit, *p) }
}

// addDropped forgets the awaited add when it will not run.
func (s *Screen) addDropped(m ui.NotRunMsg) {
	if s.added != nil && slices.ContainsFunc(m.Actions, func(a actions.Action) bool {
		return slices.Equal(a.Argv, s.added.argv)
	}) {
		s.added = nil
	}
}

func readVault(ctx context.Context, gen int, r runner.Runner, timeout time.Duration, p pendingAdd) vaultMsg {
	m := vaultMsg{gen: gen, add: p}
	res, err := ui.Read(ctx, r, timeout, "secret", "list")
	switch {
	case err == nil && res.ExitCode != 0 && (model.IsBackendNotConfigured(res.Stderr) || model.IsBackendNotConfigured(res.Stdout)):
		m.note = "no vault backend is configured"
	default:
		m.note = ui.ReadNote("secret list", res, err)
	}
	if m.note != "" {
		return m
	}
	if m.have, err = model.ParseSecretKeys(res.Stdout); err != nil {
		m.note = ui.Unreadable("secret list", res, err)
	}
	return m
}

// afterAddNote says which referenced secrets the vault lacks and points to
// the Secrets tab, where the user sets them; the TUI never sets a value
// here. When the vault could not be listed it says so and names every
// reference, rather than claiming none is missing.
func afterAddNote(m vaultMsg) string {
	id := noteAdded + mask.OneLine(m.add.id)
	if m.note != "" {
		return id + "; could not check the vault (" + m.note + "): make sure these are set in the Secrets tab: " +
			joinKeys(m.add.keys)
	}
	var missing []string
	for _, k := range m.add.keys {
		if !slices.Contains(m.have, k) {
			missing = append(missing, k)
		}
	}
	if len(missing) == 0 {
		return id + "; every secret it references is set"
	}
	return id + "; set these secrets in the Secrets tab: " + joinKeys(missing)
}

func joinKeys(keys []string) string {
	out := make([]string, len(keys))
	for i, k := range keys {
		out[i] = mask.OneLine(k)
	}
	return strings.Join(out, ", ")
}
