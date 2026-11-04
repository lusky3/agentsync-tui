package secrets

import (
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/ui/theme"
)

const (
	listKeys    = "n new • u update selected • E edit all in $EDITOR • x remove • j/k move • r refresh"
	fieldKeys   = "enter set (agentsync prompts for the value) • esc cancel"
	emptyNotice = "no secrets stored — press n to add one"
)

// View implements tea.Model. Every outside string is sanitised or masked;
// secret values are never held, so none can be shown.
func (s *Screen) View() tea.View {
	var out []string
	switch {
	case !s.loaded:
		out = append(out, theme.Dim.Render("loading…"))
	case s.vault == vaultNotConfigured:
		out = append(out, s.notConfigured()...)
	case len(s.keys) == 0 && s.vault == vaultOK:
		out = append(out, emptyNotice)
	default:
		out = append(out, s.rows()...)
	}
	if s.loaded {
		out = append(out, s.crossCheck()...)
	}
	if s.loadNote != "" {
		out = append(out, theme.Dim.Render("secret list unavailable: "+s.loadNote))
	}
	if s.srvNote != "" {
		out = append(out, theme.Dim.Render("servers unavailable: "+s.srvNote))
	}
	if s.note != "" {
		out = append(out, theme.Warn.Render(s.note))
	}
	if s.typing {
		out = append(out, theme.Bold.Render("key name: ")+display(s.input)+"▏")
		if s.fieldErr != "" {
			out = append(out, theme.Warn.Render(s.fieldErr))
		}
		out = append(out, theme.Dim.Render(fieldKeys))
	} else {
		out = append(out, theme.Dim.Render(listKeys))
	}
	lines := strings.Split(strings.Join(out, "\n"), "\n")
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, s.width, "…")
	}
	return tea.NewView(strings.Join(lines, "\n"))
}

func (s *Screen) notConfigured() []string {
	out := []string{theme.Warn.Render("The secrets backend is not configured.")}
	if s.vaultMsg != "" {
		out = append(out, theme.Dim.Render(s.vaultMsg))
	}
	return append(out,
		"Configure the [secrets] table in agentsync.toml, then press r.",
		"see `agentsync secret --help`")
}

func (s *Screen) rows() []string {
	out := []string{theme.Bold.Render("  " + theme.Pad("KEY", 30) + "USED BY")}
	for i, k := range s.keys {
		users := "(none)"
		if ids := s.uses[k]; len(ids) > 0 {
			users = strings.Join(ids, ", ")
		}
		line := theme.Pad(display(k), 30) + "used by: " + users
		if i == s.cursor {
			out = append(out, theme.Sel.Render("> "+line))
		} else {
			out = append(out, "  "+line)
		}
	}
	return out
}

// crossCheck lists referenced keys that the vault does not hold, or says the
// check cannot run when the vault could not be read.
func (s *Screen) crossCheck() []string {
	if len(s.uses) == 0 {
		return nil
	}
	if s.vault != vaultOK {
		return []string{theme.Warn.Render("cannot check — vault unavailable")}
	}
	have := map[string]bool{}
	for _, k := range s.keys {
		have[k] = true
	}
	var missing []string
	for k := range s.uses {
		if !have[k] {
			missing = append(missing, k)
		}
	}
	sort.Strings(missing)
	var out []string
	for _, k := range missing {
		out = append(out, theme.Warn.Render("missing: "+display(k)+" (used by "+strings.Join(s.uses[k], ", ")+")"))
	}
	return out
}

// display is how a key name is rendered: masked, then sanitised, so a token
// that reached the vault elsewhere never shows raw.
func display(k string) string { return mask.Sanitize(mask.Text(k)) }
