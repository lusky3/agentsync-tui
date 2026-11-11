package sourcerepo

import (
	"fmt"
	"math"
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
	// commitScrollKeys scroll the refusal and findings in the commit view;
	// j/k and space type into the message there.
	commitScrollKeys = "↑/↓ pgup/pgdown scroll"
	// maxFindings is how many findings the pane lists.
	maxFindings = 20
	stateWarn   = ".state/ is not ignored: add .state/ (no leading slash: it matches at any depth, skills/.state/ too) to .gitignore"
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

// listView is the file list under the repository heading, with the
// findings, notes and keys below it.
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
	var rest []string
	if s.netBusy {
		rest = append(rest, theme.Dim.Render(checkingNet))
	}
	rest = append(rest, s.noteLines()...)
	rest = append(rest, ui.KeyLines(keys, s.width)...)
	room := math.MaxInt
	if s.height > 0 {
		// One row of the list stays (frame keeps it); the findings get
		// what the header, that row and the rest of the footer leave.
		room = s.height - len(header) - 1 - len(rest)
	}
	footer := append(s.findingLines(room), rest...)
	return s.list.Frame(header, rows, footer, s.height)
}

// header describes the repository, or what stands in for one.
func (s *Screen) header() []string {
	if !s.loaded {
		return []string{theme.Dim.Render(whyLoading)}
	}
	home := quotePath(s.repo.Home)
	switch s.repo.Manager {
	case gitrepo.Unknown:
		return []string{
			theme.Bad.Render(whyUnknown + ": " + mask.Sanitize(s.loadNote)),
			theme.Bad.Render(mask.Sanitize(s.repo.Refusal)),
			theme.Dim.Render("every key but r is disabled; press r to try again"),
		}
	case gitrepo.Chezmoi:
		out := []string{
			theme.Bold.Render("Managed by chezmoi") + "  source: " + mask.Text(s.repo.ChezmoiSource),
			"The TUI leaves this home to chezmoi. Run these outside the TUI:",
		}
		for _, c := range chezmoiCommands {
			out = append(out, "  "+c)
		}
		return out
	case gitrepo.None:
		return withPathNote([]string{
			theme.Bold.Render(whyNone) + ": " + home,
			"To version the home, run these outside the TUI (it runs neither):",
			"  " + gitC + home + " init",
			"  " + gitC + home + " remote add origin <url>",
		}, s.repo.Home)
	case gitrepo.Missing:
		return s.missingHeader(home)
	}
	return s.gitHeader()
}

// gitHeader is the repository, home, branch and upstream lines, and any
// refusal.
func (s *Screen) gitHeader() []string {
	r, b := s.repo, s.status.Branch
	out := []string{
		theme.Bold.Render("git") + "  repo: " + mask.Text(r.Top) + "  home: " + mask.Text(r.Pathspec),
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

// branchText describes the branch: its name (masked), detached or unborn.
func branchText(b gitrepo.Branch) string {
	head := mask.Text(b.Head)
	switch {
	case b.Detached:
		return "detached HEAD"
	case b.Unborn:
		return head + " (no commits yet)"
	}
	return head
}

// upstreamText describes the upstream and how far ahead and behind it is.
func upstreamText(b gitrepo.Branch) string {
	if b.Upstream == "" {
		return "no upstream"
	}
	ab := "not fetched"
	if b.ABKnown {
		ab = "↑" + strconv.Itoa(b.Ahead) + " ↓" + strconv.Itoa(b.Behind)
	}
	return ab + "  upstream: " + mask.Text(b.Upstream)
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
		for _, l := range keptLines(s.repo.Top, f, s.kept[f.Path]) {
			out = append(out, theme.Dim.Render(l))
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

// row is one file's line: its selection box, kind, path (and a rename's
// old path), class and note.
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
// pre-push scan's when it found any, else the pre-commit scan's. At most
// maxFindings are listed, fewer when the list would not fit in room lines
// (the heading and a "… N more" line included).
func (s *Screen) findingLines(room int) []string {
	fs := s.commit.findings
	if len(s.pushFindings) > 0 {
		fs = s.pushFindings
	}
	if len(fs) == 0 {
		return nil
	}
	n := min(len(fs), maxFindings)
	if n < len(fs) || 1+n > room {
		n = min(n, max(room-2, 0))
	}
	out := []string{theme.Bold.Render(countFindings(fs).summary() + " (file:line, masked):")}
	for _, f := range fs[:n] {
		out = append(out, "  "+findingLine(f))
	}
	if more := len(fs) - n; more > 0 {
		out = append(out, theme.Dim.Render("  … "+strconv.Itoa(more)+" more"))
	}
	return out
}

// findingLine is one finding: its file:line, severity, kind and masked
// text.
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

// diffView is the diff pane: its title, the masked patch and its keys.
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

// commitView is the message field over a scrolling body (commitBody) and
// the key guidance, which always fits.
func (s *Screen) commitView() []string {
	c := &s.commit
	header := append(s.header(), "",
		theme.Bold.Render("Commit "+strconv.Itoa(len(s.selectedPaths()))+" selected file(s)"),
		"message: "+mask.Text(c.field.Value)+"▏")
	body := s.commitBody()
	keys := commitKeys
	if len(body) > 0 {
		keys += " • " + commitScrollKeys
	}
	return c.scroll.Frame(header, body, ui.KeyLines(keys, s.width), s.height)
}

// commitBody is what the commit view shows below the message: the
// message's refusal, the guard round's progress, its refusal and the
// findings.
func (s *Screen) commitBody() []string {
	c := &s.commit
	var out []string
	if c.msgNote != "" {
		out = append(out, theme.Bad.Render(mask.Sanitize(c.msgNote)))
	}
	if c.busy {
		out = append(out, theme.Dim.Render("checking: status, agentsync check, git state, secret scan…"))
	}
	for i, l := range c.refusal {
		l = mask.Sanitize(l)
		if i == 0 {
			l = theme.Bad.Render(l)
		}
		out = append(out, l)
	}
	return append(out, s.findingLines(math.MaxInt)...)
}

// joinNote joins two notes with "; ", either of which may be empty.
func joinNote(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}
