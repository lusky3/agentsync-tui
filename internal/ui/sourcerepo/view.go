package sourcerepo

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lusky3/agentsync-tui/internal/gitrepo"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/ui"
	"github.com/lusky3/agentsync-tui/internal/ui/theme"
)

const (
	listKeys    = "space select • a select all • enter/d diff • c commit • f fetch • p pull • P push • r refresh • " + ui.ScrollKeys
	pulledKeys  = "D drift • A apply • "
	refreshKeys = "r refresh"
	initKeys    = "i clone from a URL • r refresh"
	diffKeys    = "esc back • " + ui.ScrollKeys
	commitKeys  = "enter check and commit • esc back • ctrl+u clear"
	// maxFindings is how many findings the pane lists.
	maxFindings = 20
	stateWarn   = ".state/ is not ignored: add /.state/ to .gitignore"
	sourceBack  = "old path exists again; its removal is not part of this commit"
)

// chezmoiCommands are the commands a chezmoi-managed home is versioned
// with (D8); the TUI runs none of them.
var chezmoiCommands = []string{
	"chezmoi re-add",
	"chezmoi git -- status",
	"chezmoi git -- add -- <path>",
	"chezmoi git -- commit -m <msg>",
	"chezmoi git -- push",
	"chezmoi update",
}

// View implements tea.Model. Paths are shown only through DisplayName;
// diff lines and findings were masked when they were stored and are
// sanitised again here.
func (s *Screen) View() tea.View {
	var lines []string
	switch s.mode {
	case modeDiff:
		lines = s.diffView()
	case modeCommit:
		lines = s.commitView()
	case modeURL:
		lines = s.urlView()
	default:
		lines = s.listView()
	}
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, s.width, "…")
	}
	return tea.NewView(strings.Join(lines, "\n"))
}

// isGit reports whether the home is a git work tree with a toplevel.
func (s *Screen) isGit() bool {
	return s.loaded && s.repo.Manager == gitrepo.Git && s.repo.Top != ""
}

func (s *Screen) listView() []string {
	header := s.header()
	var rows []string
	keys := refreshKeys
	switch {
	case s.isGit():
		header = append(header, s.warnings()...)
		rows, keys = s.rows(), listKeys
		if s.pulled {
			keys = pulledKeys + keys
		}
	case s.initBlocked() == "":
		keys = initKeys
	}
	footer := s.findingLines()
	if s.netBusy {
		footer = append(footer, theme.Dim.Render(checkingNet))
	}
	footer = append(footer, s.noteLines()...)
	footer = append(footer, ui.KeyLines(keys, s.width)...)
	return s.list.Frame(header, rows, footer, s.height)
}

// header describes the repository, or what stands in for one.
func (s *Screen) header() []string {
	if !s.loaded {
		return []string{theme.Dim.Render(whyLoading)}
	}
	home := shellQuote(s.repo.Home)
	switch s.repo.Manager {
	case gitrepo.Unknown:
		return []string{
			theme.Bad.Render(whyUnknown + ": " + mask.Sanitize(s.loadNote)),
			theme.Bad.Render(mask.Sanitize(s.repo.Refusal)),
			theme.Dim.Render("every key but r is disabled; press r to try again"),
		}
	case gitrepo.Chezmoi:
		out := []string{
			theme.Bold.Render("Managed by chezmoi") + "  source: " + mask.Sanitize(s.repo.ChezmoiSource),
			"The TUI leaves this home to chezmoi. Run these outside the TUI:",
		}
		for _, c := range chezmoiCommands {
			out = append(out, "  "+c)
		}
		return out
	case gitrepo.None:
		return []string{
			theme.Bold.Render(whyNone) + ": " + home,
			"To version the home, run these outside the TUI (it runs neither):",
			"  " + gitC + home + " init",
			"  " + gitC + home + " remote add origin <url>",
		}
	case gitrepo.Missing:
		return s.missingHeader(home)
	}
	return s.gitHeader()
}

func (s *Screen) gitHeader() []string {
	r, b := s.repo, s.status.Branch
	out := []string{
		theme.Bold.Render("git") + "  repo: " + mask.Sanitize(r.Top) + "  home: " + mask.Sanitize(r.Pathspec),
		"branch: " + branchText(b) + "  " + upstreamText(b),
	}
	if r.Refusal != "" {
		out = append(out, theme.Bad.Render(refused+mask.Sanitize(r.Refusal)+" (every write key is disabled)"))
	}
	if s.loadNote != "" {
		out = append(out, theme.Warn.Render(mask.Sanitize(s.loadNote)))
	}
	return out
}

func branchText(b gitrepo.Branch) string {
	head := mask.Sanitize(b.Head)
	switch {
	case b.Detached:
		return "detached HEAD"
	case b.Unborn:
		return head + " (no commits yet)"
	}
	return head
}

func upstreamText(b gitrepo.Branch) string {
	if b.Upstream == "" {
		return "no upstream"
	}
	ab := "not fetched"
	if b.ABKnown {
		ab = "↑" + strconv.Itoa(b.Ahead) + " ↓" + strconv.Itoa(b.Behind)
	}
	return ab + "  upstream: " + mask.Sanitize(b.Upstream)
}

// warnings are the lines above the file rows.
func (s *Screen) warnings() []string {
	var out []string
	for _, f := range s.status.Files {
		if f.Class == gitrepo.StateDir {
			out = append(out, theme.Warn.Render(stateWarn))
			break
		}
	}
	if n := s.status.Note(); n != "" {
		out = append(out, theme.Dim.Render(n))
	}
	for _, f := range s.status.Files {
		if s.kept[f.Path].cmd {
			out = append(out, theme.Dim.Render("to commit the staged removal of "+gitrepo.DisplayName(f.Orig)+
				", run outside the TUI: "+removalCommand(s.repo.Top, f.Orig)))
		}
	}
	if len(s.status.Files) == 0 && s.loadNote == "" {
		out = append(out, theme.Dim.Render("no changes under the home"))
	}
	return out
}

// rows renders every file; View windows them around the cursor.
func (s *Screen) rows() []string {
	out := make([]string, 0, len(s.status.Files))
	for i, f := range s.status.Files {
		line := s.row(f)
		if i == s.list.Cursor {
			out = append(out, theme.Sel.Render("> "+ansi.Strip(line)))
		} else {
			out = append(out, "  "+line)
		}
	}
	return out
}

func (s *Screen) row(f gitrepo.File) string {
	box := " - "
	if f.Selectable {
		box = "[ ]"
		if s.sel[f.Path] {
			box = "[x]"
		}
	}
	name := gitrepo.DisplayName(f.Path)
	if f.Orig != "" {
		name += " ← " + gitrepo.DisplayName(f.Orig)
	}
	line := fmt.Sprintf("%s %-12s %s  [%s]", box, f.Kind, name, f.Class)
	note := f.Note
	if k, ok := s.kept[f.Path]; ok {
		note = joinNote(note, k.text)
	}
	if note != "" {
		line += "  " + theme.Dim.Render(mask.Sanitize(note))
	}
	return line
}

// noteLines renders the screen note, one line per line it holds.
func (s *Screen) noteLines() []string {
	if s.note == "" {
		return nil
	}
	var out []string
	for _, l := range strings.Split(s.note, "\n") {
		out = append(out, theme.Warn.Render(mask.Sanitize(l)))
	}
	return out
}

// findingLines lists the last scan's findings (masked by the scan): the
// pre-push scan's when it found any, else the pre-commit scan's.
func (s *Screen) findingLines() []string {
	fs := s.commit.findings
	if len(s.pushFindings) > 0 {
		fs = s.pushFindings
	}
	if len(fs) == 0 {
		return nil
	}
	out := []string{theme.Bold.Render(countFindings(fs).summary() + " (file:line, masked):")}
	for _, f := range fs[:min(len(fs), maxFindings)] {
		out = append(out, "  "+findingLine(f))
	}
	if n := len(fs) - maxFindings; n > 0 {
		out = append(out, theme.Dim.Render("  … "+strconv.Itoa(n)+" more"))
	}
	return out
}

func findingLine(f gitrepo.Finding) string {
	loc := f.Path
	if f.Line > 0 {
		loc += ":" + strconv.Itoa(f.Line)
	}
	sev := theme.Warn.Render("soft")
	if f.Hard {
		sev = theme.Bad.Render("HARD")
	}
	l := mask.Sanitize(loc) + "  " + sev + " " + mask.Sanitize(f.Kind)
	if f.Shown != "" {
		l += "  " + mask.Sanitize(f.Shown)
	}
	return l
}

func (s *Screen) diffView() []string {
	d := &s.diff
	header := []string{theme.Bold.Render("diff: " + d.title)}
	var body []string
	switch {
	case !d.loaded:
		body = []string{theme.Dim.Render(whyLoading)}
	case d.note != "":
		body = []string{theme.Warn.Render(mask.Sanitize(d.note))}
	default:
		for _, l := range d.lines {
			body = append(body, mask.Sanitize(l))
		}
		if d.trunc {
			body = append(body, theme.Warn.Render(ui.TruncatedMarker))
		}
	}
	return d.scroll.Frame(header, body, ui.KeyLines(diffKeys, s.width), s.height)
}

func (s *Screen) commitView() []string {
	c := &s.commit
	out := append(s.header(), "",
		theme.Bold.Render("Commit "+strconv.Itoa(len(s.selectedPaths()))+" selected file(s)"),
		"message: "+mask.Text(c.field.Value)+"▏")
	if c.msgNote != "" {
		out = append(out, theme.Bad.Render(mask.Sanitize(c.msgNote)))
	}
	if c.busy {
		out = append(out, theme.Dim.Render("checking: status, agentsync check, git state, secret scan…"))
	}
	for i, l := range c.refusal {
		if i == 0 {
			l = theme.Bad.Render(mask.Sanitize(l))
		}
		out = append(out, l)
	}
	out = append(out, s.findingLines()...)
	return append(out, ui.KeyLines(commitKeys, s.width)...)
}

func joinNote(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}
