package sourcerepo

import (
	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/gitrepo"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
	"github.com/lusky3/agentsync-tui/internal/ui/theme"
)

// The bootstrap (D10): with no home in user scope, i takes a URL and
// emits `agentsync init <url>`.
const (
	whyUserOnly   = "init from a URL is user scope only"
	whyHomeExists = "init from a URL needs a missing or empty home"
	urlKeys       = "enter clone • esc back • ctrl+u clear"
	// maxURLBytes caps the URL field; ValidCloneURL allows 2048 bytes.
	maxURLBytes = 2048
)

// initBlocked is why i is disabled, "" when it is not: only a missing or
// empty home in user scope can be bootstrapped, never when the detection
// failed or the home is managed in any way.
func (s *Screen) initBlocked() string {
	switch why := s.blocked(); why {
	case whyMissing:
	case "":
		return whyHomeExists
	default:
		return why
	}
	switch {
	case s.repo.Manager != gitrepo.Missing:
		return whyHomeExists
	case s.scope == runner.ScopeProject:
		return whyUserOnly
	}
	return ""
}

// openURL focuses the URL field.
func (s *Screen) openURL() {
	if why := s.initBlocked(); why != "" {
		s.note = why
		return
	}
	s.mode, s.note, s.urlNote = modeURL, "", ""
}

// urlKey handles a key while the URL field has focus.
func (s *Screen) urlKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case keyEsc:
		s.mode, s.urlNote = modeList, ""
		return nil
	case "enter":
		return s.submitURL()
	}
	if _, changed := s.url.Key(k); changed {
		s.urlNote = ""
	}
	return nil
}

// submitURL validates the URL and emits the init: a refused URL is noted
// and nothing is emitted.
func (s *Screen) submitURL() tea.Cmd {
	if why := s.initBlocked(); why != "" {
		s.mode, s.note = modeList, why
		return nil
	}
	act, err := actions.InitFromURL(s.url.Value)
	if err != nil {
		s.urlNote = mask.FirstLine(err.Error())
		return nil
	}
	s.mode = modeList
	return s.emitNet(act, nil, pendingNet{op: opInit})
}

// initOutcome is the note after the init; a clone is refreshed at once,
// so Detect sees the new repository.
func (s *Screen) initOutcome(m ui.RunMsg, ok bool) tea.Cmd {
	if !ok {
		s.note = "init failed (" + outcomeOf(m) + ")"
		return nil
	}
	s.note = "cloned the agentsync home: run agentsync check on the Health tab to verify it"
	s.url.Clear()
	return s.Refresh()
}

// missingHeader is the bootstrap view's heading.
func (s *Screen) missingHeader(home string) []string {
	out := []string{"No agentsync home at " + home + "."}
	if s.scope == runner.ScopeProject {
		return append(out, theme.Dim.Render(whyUserOnly))
	}
	return append(out, "To start from an existing home, press i and give its repository URL (agentsync init <url>; https or ssh).",
		theme.Dim.Render("Keep that repository PRIVATE: the home names your servers and agents."))
}

func (s *Screen) urlView() []string {
	out := append(s.header(), "",
		theme.Bold.Render("Clone an agentsync home"),
		"URL (https:// or ssh://): "+mask.Text(s.url.Value)+"▏")
	if s.urlNote != "" {
		out = append(out, theme.Bad.Render(mask.Sanitize(s.urlNote)))
	}
	return append(out, ui.KeyLines(urlKeys, s.width)...)
}
