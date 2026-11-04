package secrets

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/ui"
	"github.com/lusky3/agentsync-tui/internal/ui/theme"
)

const (
	listKeys    = "n new • u update selected • E edit all in $EDITOR • x remove • r refresh • " + ui.ScrollKeys
	fieldKeys   = "enter set (agentsync prompts for the value) • esc cancel"
	emptyNotice = "no secrets stored — press n to add one"
)

// View implements tea.Model. Every outside string is sanitised or masked;
// secret values are never held, so none can be shown. The rows are
// windowed around the cursor so the footer is always visible.
func (s *Screen) View() tea.View {
	var header, rows []string
	switch {
	case !s.loaded:
		header = append(header, theme.Dim.Render("loading…"))
	case s.vault == vaultNotConfigured:
		header = append(header, s.notConfigured()...)
	case len(s.keys) == 0 && s.vault == vaultOK:
		header = append(header, emptyNotice)
	default:
		header, rows = s.table()
	}
	var footer []string
	if s.loaded {
		footer = append(footer, s.crossCheck()...)
	}
	if s.loadNote != "" {
		footer = append(footer, theme.Dim.Render("secret list unavailable: "+s.loadNote))
	}
	if s.srvNote != "" {
		footer = append(footer, theme.Dim.Render("servers unavailable: "+s.srvNote))
	}
	if s.note != "" {
		footer = append(footer, theme.Warn.Render(s.note))
	}
	if s.typing {
		footer = append(footer, theme.Bold.Render("key name: ")+display(s.input.Value)+"▏")
		if s.fieldErr != "" {
			footer = append(footer, theme.Warn.Render(s.fieldErr))
		}
		footer = append(footer, theme.Dim.Render(fieldKeys))
	} else {
		footer = append(footer, theme.Dim.Render(listKeys))
	}
	lines := s.list.Frame(header, rows, footer, s.height)
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

// table returns the column header and one row per key.
func (s *Screen) table() (header, rows []string) {
	header = []string{theme.Bold.Render("  " + theme.Pad("KEY", 30) + "USED BY")}
	for i, k := range s.keys {
		users := "(none)"
		if ids := s.uses[k]; len(ids) > 0 {
			users = strings.Join(ids, ", ")
		}
		line := theme.Pad(display(k), 30) + "used by: " + users
		if i == s.list.Cursor {
			rows = append(rows, theme.Sel.Render("> "+line))
		} else {
			rows = append(rows, "  "+line)
		}
	}
	return header, rows
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
	var missing []string
	for k := range s.uses {
		if !slices.Contains(s.keys, k) {
			missing = append(missing, k)
		}
	}
	slices.Sort(missing)
	var out []string
	for _, k := range missing {
		out = append(out, theme.Warn.Render("missing: "+display(k)+" (used by "+strings.Join(s.uses[k], ", ")+")"))
	}
	return out
}

// display is how a key name is rendered: masked, then sanitised, so a token
// that reached the vault elsewhere never shows raw.
func display(k string) string { return mask.Sanitize(mask.Text(k)) }
