package sourcerepo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/gitrepo"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

const (
	// maxMessageBytes caps the message field; ValidCommitMessage then
	// allows at most 200 runes.
	maxMessageBytes = 1024
	// scanReadLimit is how much of a new file the scan reads: one byte past
	// ScanNewFile's 1 MiB, so a larger file is reported as not scanned.
	scanReadLimit = maxScanned + 1
	// maxScanned is how much of a file ScanNewFile scans, and how much of
	// a post-image gives the added lines their context.
	maxScanned = 1 << 20
	// shownPaths is how many paths a command shown as text lists.
	shownPaths = 10
	refused    = "refused: "
	keyEsc     = "esc"
	gitC       = "git -C "
	// cannotScan starts the refusal of a file the scan could not read.
	cannotScan = refused + "cannot scan "
	// statusCmd ends a shown `git -C <top> status`.
	statusCmd = " status"
	// literalPathspecs goes before the subcommand of a command shown for
	// the user to run that names paths: like the TUI's own git runs
	// (runner.NewGit), it makes `*`, `:(glob)` and the like match only a
	// file of that name.
	literalPathspecs = " --literal-pathspecs"
)

// binaryRe is the line git prints for a binary change (binary content, or
// a -diff attribute) instead of a patch.
var binaryRe = regexp.MustCompile(`(?m)^Binary files .* differ$`)

// commitState is the commit view: the message field and what the last
// guard round found.
type commitState struct {
	field    ui.Field
	msgNote  string // why the message was refused, display-safe
	busy     bool   // a guard round is running
	refusal  []string
	findings []gitrepo.Finding // masked by the scan
}

func newCommitState() commitState { return commitState{field: ui.Field{Max: maxMessageBytes}} }

// pendingCommit is the chain handed to the shell, until its outcome.
type pendingCommit struct {
	addKey, commitKey string
	added             bool
	subject, top      string
	add               []string
}

// guardInput is what a guard round needs, copied off the screen.
type guardInput struct {
	repo     gitrepo.Repo
	run, git runner.Runner
	limit    time.Duration
	selected []gitrepo.File // the records the user selected, as listed
	msg      string
}

// guardMsg carries one finished guard round.
type guardMsg struct {
	gen      int
	msg      string
	refusal  string // display-safe; "" when every guard passed
	changed  bool   // a selected file is no longer listed; status is fresh
	status   gitrepo.Status
	kept     map[string]keptNote
	findings []gitrepo.Finding
	add      []string // paths that exist in the worktree
	commit   []string // every selected path, plus in-home rename sources
}

// openCommit focuses the message field.
func (s *Screen) openCommit() {
	if why := s.writeBlocked(); why != "" {
		s.note = why
		return
	}
	if len(s.selectedPaths()) == 0 {
		s.note = whyNoChanges
		return
	}
	s.mode, s.note = modeCommit, ""
	s.commit.msgNote, s.commit.refusal, s.commit.findings = "", nil, nil
}

// commitKey handles a key while the message field has focus.
func (s *Screen) commitKey(k tea.KeyPressMsg) tea.Cmd {
	switch k.String() {
	case keyEsc:
		s.guards.Supersede()
		s.mode, s.commit.busy, s.commit.msgNote, s.commit.refusal = modeList, false, "", nil
		return nil
	case "enter":
		return s.submit()
	}
	if s.commit.busy {
		return nil
	}
	if _, changed := s.commit.field.Key(k); changed {
		s.commit.msgNote, s.commit.refusal = "", nil
	}
	return nil
}

// submit checks the message, then starts the guard round.
func (s *Screen) submit() tea.Cmd {
	if s.commit.busy {
		return nil
	}
	msg := s.commit.field.Value
	if err := actions.ValidCommitMessage(msg); err != nil {
		s.commit.msgNote = mask.FirstLine(err.Error()) + ": reword the message"
		return nil
	}
	selected := s.selectedFiles()
	if len(selected) == 0 {
		s.commit.refusal = []string{whyNoChanges}
		return nil
	}
	s.commit.busy, s.commit.refusal, s.commit.findings = true, nil, nil
	gen, ctx := s.guards.Next(s.base)
	in := guardInput{repo: s.repo, run: s.run, git: s.git, limit: s.limit, selected: selected, msg: msg}
	return func() tea.Msg { return runGuards(ctx, gen, in) }
}

// runGuards runs the pre-commit guards in their pinned order (D4): the
// status re-read, agentsync check, the git dir state, a detached HEAD, the
// repository's refusal, then the scan of every selected file.
func runGuards(ctx context.Context, gen int, in guardInput) guardMsg {
	m := guardMsg{gen: gen, msg: in.msg}
	st, note := readStatus(ctx, in.git, in.repo, in.limit)
	if note != "" {
		m.refusal = refused + note
		return m
	}
	files, ok := pick(st.Files, in.selected)
	if !ok {
		m.changed, m.status, m.kept = true, st, keptSources(in.repo, st.Files)
		return m
	}
	if m.refusal = preflight(ctx, in, st.Branch); m.refusal != "" {
		return m
	}
	if m.findings, m.refusal = scanFiles(ctx, in, st.Branch.Unborn, files); m.refusal != "" {
		return m
	}
	m.add, m.commit = commitPaths(in.repo, files)
	return m
}

// pick returns the fresh records of the selected files, or false when one
// is no longer listed, no longer selectable, or changed its kind or rename
// source (a modified file deleted since, for instance): the user would
// commit something other than what the list showed.
func pick(files, selected []gitrepo.File) ([]gitrepo.File, bool) {
	by := make(map[string]gitrepo.File, len(files))
	for _, f := range files {
		by[f.Path] = f
	}
	out := make([]gitrepo.File, 0, len(selected))
	for _, was := range selected {
		f, ok := by[was.Path]
		if !ok || !f.Selectable || f.Kind != was.Kind || f.Orig != was.Orig {
			return nil, false
		}
		out = append(out, f)
	}
	return out, true
}

// preflight is guards 2 to 5; it returns the refusal, or "".
func preflight(ctx context.Context, in guardInput, b gitrepo.Branch) string {
	res, err := ui.Read(ctx, in.run, in.limit, actions.Check().Argv...)
	if note := ui.ReadNote("agentsync check", res, err); note != "" {
		return refused + note
	}
	switch why := gitDirRefusal(in.repo.GitDir); {
	case why != "":
		return why
	case b.Detached:
		return msgDetached
	case in.repo.Refusal != "":
		return refused + mask.Sanitize(in.repo.Refusal)
	}
	return ""
}

// msgDetached refuses a commit, pull or push on a detached HEAD.
const msgDetached = refused + "HEAD is detached: check out a branch outside the TUI"

// gitDirRefusal refuses while index.lock is present or a merge, rebase,
// cherry-pick, revert or bisect is in progress; "" otherwise. The lock is
// never deleted.
func gitDirRefusal(gitDir string) string {
	gd := gitrepo.InspectGitDir(gitDir)
	switch {
	case gd.Locked:
		return refused + "index.lock is present: another git may be running. The TUI never deletes the lock; remove it yourself only if no git is running"
	case gd.InProgress != "":
		return refused + "a " + gd.InProgress + " is in progress: finish or abort it outside the TUI"
	}
	return ""
}

// scanFiles scans what each selected file would add (R5).
func scanFiles(ctx context.Context, in guardInput, unborn bool, files []gitrepo.File) ([]gitrepo.Finding, string) {
	var out []gitrepo.Finding
	for _, f := range files {
		fs, why := scanFile(ctx, in, unborn, f)
		if why != "" {
			return nil, why
		}
		out = append(out, fs...)
	}
	return out, ""
}

func scanFile(ctx context.Context, in guardInput, unborn bool, f gitrepo.File) ([]gitrepo.Finding, string) {
	switch f.Kind {
	case gitrepo.Deleted:
		return nil, ""
	case gitrepo.Untracked:
		return scanUntracked(ctx, in, f)
	}
	return scanTracked(ctx, in, unborn, f)
}

// scanUntracked scans a new file whole (a symlink: its target).
func scanUntracked(ctx context.Context, in guardInput, f gitrepo.File) ([]gitrepo.Finding, string) {
	b, err := bounded(ctx, in.limit, func() (capped, error) { return readNew(in.repo.Top, f.Path) })
	if err != nil {
		return nil, cannotScan + gitrepo.DisplayName(f.Path) + ": " + errNote(err)
	}
	return gitrepo.ScanNewFile(f.HomeRel, []byte(b.data)), ""
}

// readNew is what committing rel would record: a symlink's target, or the
// regular file's first scanReadLimit bytes.
func readNew(top, rel string) (capped, error) {
	full := filepath.Join(top, filepath.FromSlash(rel))
	if fi, err := os.Lstat(full); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(full)
		return capped{data: target}, err
	}
	return readFile(top, rel, scanReadLimit)
}

// scanTracked scans the added lines of a tracked file's diff against HEAD,
// in the context of the worktree file they come from (postImage). A binary
// change or a patch the runner cut is soft: it was not scanned.
func scanTracked(ctx context.Context, in guardInput, unborn bool, f gitrepo.File) ([]gitrepo.Finding, string) {
	argv := gitrepo.DiffArgv(in.repo, unborn, f.Path, 0)
	if argv == nil {
		return nil, cannotScan + gitrepo.DisplayName(f.Path)
	}
	res, err := ui.Read(ctx, in.git, in.limit, argv...)
	if note := ui.ReadNote("git diff", res, err); note != "" {
		return nil, refused + note
	}
	patch, cut := strings.CutSuffix(res.Stdout, runner.TruncatedMark)
	var out []gitrepo.Finding
	if cut {
		out = append(out, notScanned(f, gitrepo.KindNotScannedPatch))
	}
	if binaryRe.MatchString(patch) {
		out = append(out, notScanned(f, gitrepo.KindNotScannedBinary))
	}
	lines := gitrepo.AddedLines(patch)
	if len(lines) == 0 {
		return out, ""
	}
	post, err := bounded(ctx, in.limit, func() (capped, error) { return postImage(in.repo.Top, f.Path) })
	if err != nil {
		return nil, cannotScan + gitrepo.DisplayName(f.Path) + ": " + errNote(err)
	}
	if post.cut && beyondCut(lines, post.data) {
		out = append(out, notScanned(f, gitrepo.KindContextNotChecked))
	}
	return append(out, gitrepo.ScanAdded(f.HomeRel, lines, post.data)...), ""
}

// postImage is the worktree file rel the diff's added lines come from, its
// first maxScanned bytes (cut past them: an added line past the cut is
// still scanned, but soft, as its multi-line context is not known). A symlink's diff is its target,
// which has no multi-line strings: it gives no context.
func postImage(top, rel string) (capped, error) {
	c, err := readFile(top, rel, maxScanned)
	if errors.Is(err, gitrepo.ErrSymlink) {
		return capped{}, nil
	}
	return c, err
}

func notScanned(f gitrepo.File, kind string) gitrepo.Finding {
	return gitrepo.Finding{Path: mask.OneLine(mask.Text(f.HomeRel)), Kind: kind}
}

// commitPaths are the explicit file paths of the chain, never a directory
// (a directory pathspec would sweep in a tracked .state/ descendant). add
// holds the paths present in the worktree: git add refuses a path gone
// from both the worktree and the index (a staged deletion, a rename's
// source), and commit -- <path> records those itself. commit holds every
// selected path and, for a rename inside the home, its source, so the
// source's removal is committed too; a source outside the home never is.
func commitPaths(repo gitrepo.Repo, files []gitrepo.File) (add, commit []string) {
	for _, f := range files {
		commit = append(commit, f.Path)
		if _, err := os.Lstat(filepath.Join(repo.Top, filepath.FromSlash(f.Path))); err == nil {
			add = append(add, f.Path)
		}
		if f.Kind == gitrepo.Renamed && commitSource(repo, f.Orig) {
			commit = append(commit, f.Orig)
		}
	}
	return add, commit
}

// commitSource reports whether a rename's source may join the commit
// paths. `git commit -- <path>` records the worktree's current content at
// the path, so a source that exists again (recreated after the move, as a
// file or a directory) would be committed unselected and unscanned: it is
// left out, its staged removal stays staged, and the rename's new side
// commits on its own. A source outside the home, one git cannot be given,
// or one whose name is a state, sensitive or unusual path is never
// included either (keptReason says which).
func commitSource(repo gitrepo.Repo, orig string) bool {
	return inHome(repo.Pathspec, orig) && keptReason(repo, orig).text == ""
}

// homeRel is the toplevel-relative p relative to the home.
func homeRel(pathspec, p string) string {
	if pathspec == "." {
		return p
	}
	return strings.TrimPrefix(p, pathspec+"/")
}

// inHome reports whether the toplevel-relative p lies below the home.
func inHome(pathspec, p string) bool {
	return p != "" && (pathspec == "." || strings.HasPrefix(p, pathspec+"/"))
}

// onGuard takes a finished guard round: a refusal is shown, a changed
// list is refreshed, and a clean or soft round emits the chain.
func (s *Screen) onGuard(m guardMsg) tea.Cmd {
	s.commit.busy = false
	switch {
	case m.changed:
		s.kept = m.kept
		s.applyStatus(m.status)
		s.mode, s.note = modeList, "changed since listed: refreshed"
		return nil
	case m.refusal != "":
		s.commit.refusal = []string{m.refusal}
		return nil
	}
	return s.emit(m)
}

// emit builds the add and commit Actions and hands them to the shell as
// one chain; hard findings refuse instead, soft ones make the commit
// typed.
func (s *Screen) emit(m guardMsg) tea.Cmd {
	repo := actions.GitRepo{Top: s.repo.Top}
	commit, err := actions.GitCommit(repo, m.msg, m.commit)
	var add actions.Action
	if err == nil && len(m.add) > 0 {
		add, err = actions.GitAdd(repo, m.add)
	}
	if err != nil {
		s.commit.refusal = []string{refused + mask.FirstLine(err.Error())}
		return nil
	}
	s.commit.findings = m.findings
	steps := []actions.Action{commit}
	if len(m.add) > 0 {
		steps = []actions.Action{add, commit}
	}
	c := countFindings(m.findings)
	if c.hard > 0 {
		s.commit.refusal = hardRefusal(c.hard, steps)
		return nil
	}
	if len(m.findings) > 0 {
		steps[len(steps)-1].TypedConfirm = true
		steps[len(steps)-1].Warning = c.soft() + " listed on the Source repo tab; type commit to proceed"
	}
	s.pending = &pendingCommit{
		addKey: add.Key(), commitKey: commit.Key(), subject: mask.Sanitize(strings.TrimSpace(m.msg)),
		top: s.repo.Top, add: m.add,
	}
	if len(m.add) == 0 {
		s.pending.addKey = ""
	}
	s.mode = modeList
	return ui.DoChain(s.scope, steps[0], steps[1:]...)
}

// counts splits findings: hard ones, soft possible secrets, and files
// that were not scanned at all (binary, over 1 MiB, a cut patch).
type counts struct{ hard, secrets, unscanned int }

// unscannedKinds are the findings that report a file the scan skipped.
var unscannedKinds = map[string]bool{
	gitrepo.KindNotScannedBinary: true, gitrepo.KindNotScannedLarge: true, gitrepo.KindNotScannedPatch: true,
}

func countFindings(fs []gitrepo.Finding) counts {
	var c counts
	for _, f := range fs {
		switch {
		case f.Hard:
			c.hard++
		case unscannedKinds[f.Kind]:
			c.unscanned++
		default:
			c.secrets++
		}
	}
	return c
}

// soft names the soft findings: "N possible secrets and M files not
// scanned", either part left out when zero.
func (c counts) soft() string {
	var parts []string
	if c.secrets > 0 {
		parts = append(parts, plural(c.secrets, "possible secret"))
	}
	if c.unscanned > 0 {
		parts = append(parts, plural(c.unscanned, "file")+" not scanned")
	}
	return strings.Join(parts, " and ")
}

// summary names every kind of finding, for the findings pane.
func (c counts) summary() string {
	parts := []string{}
	if c.hard > 0 {
		parts = append(parts, plural(c.hard, "hard finding"))
	}
	if s := c.soft(); s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, ", ")
}

func plural(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + one + "s"
}

// hardRefusal explains a hard finding and shows the commands that commit
// a known false positive outside the TUI.
func hardRefusal(n int, steps []actions.Action) []string {
	out := []string{
		refused + plural(n, "hard finding") + " (likely secrets) in the selected files, listed below; a commit would publish them",
		"for a known false positive, commit outside the TUI:",
	}
	for _, a := range steps {
		out = append(out, "  "+a.Display())
	}
	return out
}

// onRun follows this screen's own chain: the add's RunMsg is broadcast
// first, so the outcome is the commit's (matched on its Key).
func (s *Screen) onRun(m ui.RunMsg) {
	p := s.pending
	if p == nil {
		return
	}
	ok := m.Err == nil && m.Result.ExitCode == 0
	switch key := m.Action.Key(); {
	case key == p.commitKey:
		s.pending, s.note = nil, commitOutcome(p, m, ok)
		if ok {
			s.commit = newCommitState()
		}
	case p.addKey != "" && key == p.addKey:
		p.added = ok
		if !ok {
			s.pending, s.note = nil, "staging failed ("+outcomeOf(m)+"): nothing was committed"
		}
	}
}

// commitOutcome is the note after the commit step. It never holds the
// run's output: only its exit code or error kind. A commit that was
// stopped (m.Err other than git missing: timed out, interrupted) may
// already be recorded, as git
// updates HEAD before the post-commit hook runs: the note asks for a
// check rather than claiming a failure or offering the unstage command.
func commitOutcome(p *pendingCommit, m ui.RunMsg, ok bool) string {
	switch {
	case ok:
		return "committed: " + p.subject
	case m.Err != nil && !errors.As(m.Err, new(*runner.MissingError)):
		t := shellQuote(p.top)
		return "the commit was stopped (" + outcomeOf(m) + "); it may have been recorded. Check before retrying or unstaging" +
			runOutside + "\n  " + gitC + t + " log -1\n  " + gitC + t + statusCmd
	case p.added:
		return "commit failed (" + outcomeOf(m) + "); the selected files are now staged. " +
			"To unstage them, run outside the TUI:\n  " + restoreCommand(p.top, p.add)
	}
	return "commit failed (" + outcomeOf(m) + "): nothing was committed"
}

func outcomeOf(m ui.RunMsg) string {
	if m.Err != nil {
		return mask.FirstLine(m.Err.Error())
	}
	return "exit " + strconv.Itoa(m.Result.ExitCode)
}

// restoreCommand is the unstage command, as text for the user (R4): the
// TUI never runs it (D11). Its paths are taken literally.
func restoreCommand(top string, paths []string) string {
	words := []string{"git", "-C", shellQuote(top), "--literal-pathspecs", "restore", "--staged", "--"}
	for _, p := range paths[:min(len(paths), shownPaths)] {
		words = append(words, shellQuote(mask.Text(p)))
	}
	if n := len(paths) - shownPaths; n > 0 {
		words = append(words, "… (+"+strconv.Itoa(n)+" more)")
	}
	return strings.Join(words, " ")
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func shellQuote(s string) string {
	s = mask.Sanitize(s)
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
