// Package sourcerepo is the Source repo tab: how the agentsync home is
// versioned, what changed in it under the home pathspec, a masked per-file
// diff, and a guarded commit of explicitly selected files (D2-D4, D8).
//
// Every git call here is a local read run in a background round bounded by
// Timeouts.Local (status, diff, the pre-commit scan), or an Action handed to
// the shell (add, then commit). Nothing touches the network on refresh or
// focus. Diff and file bytes reach only this screen's own diff message,
// already masked; they never reach a note, the status line or the log.
package sourcerepo

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lusky3/agentsync-tui/internal/gitrepo"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

// Title is the tab's name.
const Title = "Source repo"

// mode is what the screen shows.
type mode int

const (
	modeList mode = iota
	modeDiff
	modeCommit
)

// The reasons a key is disabled.
const (
	whyLoading   = "loading…"
	whyUnknown   = "could not inspect the repository"
	whyChezmoi   = "managed by chezmoi"
	whyNone      = "not a git repository"
	whyMissing   = "no agentsync home"
	whyNoChanges = "nothing to commit: no file is selected"
	notShown     = "content not shown: "
)

// Screen is the Source repo tab.
type Screen struct {
	base  context.Context
	limit time.Duration // ui.Deps.Limits().Local(): every read here
	run   runner.Runner // agentsync, for the pre-commit check
	git   runner.Runner
	cz    runner.Runner
	scope runner.Scope
	home  string

	reads, diffs, guards ui.Rounds

	loaded   bool
	repo     gitrepo.Repo
	status   gitrepo.Status
	loadNote string          // why the last load failed, display-safe
	sel      map[string]bool // selected file paths (File.Path)
	listed   map[string]bool // every listed File.Path
	list     ui.List

	mode    mode
	diff    diffPane
	commit  commitState
	pending *pendingCommit
	note    string // last screen message, display-safe (may hold line breaks)

	width, height int
}

// loadMsg carries one detect-and-status round.
type loadMsg struct {
	gen    int
	repo   gitrepo.Repo
	status gitrepo.Status
	note   string
}

// diffPane is the masked diff of one file.
type diffPane struct {
	title  string
	lines  []string // masked, sanitised
	trunc  bool
	note   string
	loaded bool
	scroll ui.Scroll
}

// diffMsg carries one diff read: lines are already masked.
type diffMsg struct {
	gen   int
	lines []string
	trunc bool
	note  string
}

// New returns the Source repo screen.
func New(d ui.Deps) *Screen {
	return &Screen{
		base: ui.BaseCtx(d), limit: d.Limits().Local(), run: d.Runner, git: d.Git, cz: d.Chezmoi,
		scope: d.Scope, home: ui.HomeFor(d, d.Scope), sel: map[string]bool{}, commit: newCommitState(),
		width: 80, height: 24,
	}
}

// Title implements ui.Screen.
func (s *Screen) Title() string { return Title }

// Init is exactly Refresh.
func (s *Screen) Init() tea.Cmd { return s.Refresh() }

// CapturesKeys implements ui.KeyCapturer: while the message field has focus.
func (s *Screen) CapturesKeys() bool { return s.mode == modeCommit }

// SetScope implements ui.ScopeSetter: the new home is detected afresh and
// everything loaded for the old one is dropped.
func (s *Screen) SetScope(r runner.Runner, scope runner.Scope, home string) {
	s.run, s.scope, s.home = r, scope, home
	s.reads.Supersede()
	s.diffs.Supersede()
	s.guards.Supersede()
	s.loaded, s.repo, s.status, s.loadNote = false, gitrepo.Repo{}, gitrepo.Status{}, ""
	s.sel, s.list, s.mode, s.diff = map[string]bool{}, ui.List{}, modeList, diffPane{}
	s.commit, s.pending, s.note = newCommitState(), nil, ""
}

// Shutdown implements ui.Shutdowner.
func (s *Screen) Shutdown() {
	s.reads.Cancel()
	s.diffs.Cancel()
	s.guards.Cancel()
}

// Refresh re-detects the repository and re-reads its status in the
// background, cancelling the previous round.
func (s *Screen) Refresh() tea.Cmd {
	gen, ctx := s.reads.Next(s.base)
	git, cz, home, limit := s.git, s.cz, s.home, s.limit
	return func() tea.Msg { return load(ctx, gen, git, cz, home, limit) }
}

// load is one refresh round: Detect, then, for a git repository with a
// toplevel, its status.
func load(ctx context.Context, gen int, git, cz runner.Runner, home string, limit time.Duration) loadMsg {
	repo, err := gitrepo.Detect(ctx, git, cz, home, limit)
	m := loadMsg{gen: gen, repo: repo}
	if err != nil {
		m.note = mask.FirstLine(err.Error())
		return m
	}
	if repo.Manager == gitrepo.Git && repo.Top != "" {
		m.status, m.note = readStatus(ctx, git, repo, limit)
	}
	return m
}

// readStatus runs the status read and parses it, both bounded by limit
// (ParseStatus reads the changed files, so it runs here, off the UI
// goroutine). note is display-safe and empty on success.
func readStatus(ctx context.Context, git runner.Runner, repo gitrepo.Repo, limit time.Duration) (gitrepo.Status, string) {
	res, err := ui.Read(ctx, git, limit, gitrepo.StatusArgv(repo)...)
	if note := ui.ReadNote("git status", res, err); note != "" {
		return gitrepo.Status{}, note
	}
	if strings.HasSuffix(res.Stdout, runner.TruncatedMark) {
		return gitrepo.Status{}, "git status: too many changes to list"
	}
	st, err := bounded(ctx, limit, func() (gitrepo.Status, error) {
		return gitrepo.ParseStatus([]byte(res.Stdout), repo)
	})
	if err != nil {
		return gitrepo.Status{}, mask.FirstLine(err.Error())
	}
	return st, ""
}

// bounded runs fn on its own goroutine and gives up when ctx ends or limit
// passes; a late result is dropped.
func bounded[T any](ctx context.Context, limit time.Duration, fn func() (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	type result struct {
		v   T
		err error
	}
	ch := make(chan result, 1)
	go func() {
		v, err := fn()
		ch <- result{v, err}
	}()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	}
}

// Update implements tea.Model.
func (s *Screen) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		s.width, s.height = msg.Width, msg.Height
	case loadMsg:
		if s.reads.Current(msg.gen) {
			s.apply(msg)
		}
	case diffMsg:
		if s.diffs.Current(msg.gen) && s.mode == modeDiff {
			s.diff.lines, s.diff.trunc, s.diff.note, s.diff.loaded = msg.lines, msg.trunc, msg.note, true
		}
	case guardMsg:
		if s.guards.Current(msg.gen) {
			return s, s.onGuard(msg)
		}
	case ui.RunMsg:
		s.onRun(msg)
	case tea.KeyPressMsg:
		return s, s.keyPress(msg)
	}
	return s, nil
}

// apply takes a finished load.
func (s *Screen) apply(m loadMsg) {
	s.loaded, s.repo, s.loadNote = true, m.repo, m.note
	if m.repo.Manager != gitrepo.Git || m.repo.Top == "" {
		s.applyStatus(gitrepo.Status{})
		return
	}
	if m.note == "" {
		s.applyStatus(m.status)
	}
}

// applyStatus replaces the listed files. A file listed before keeps its
// selection; a new one starts at its default.
func (s *Screen) applyStatus(st gitrepo.Status) {
	known := map[string]bool{}
	for _, f := range s.status.Files {
		known[f.Path] = true
	}
	sel := map[string]bool{}
	for _, f := range st.Files {
		switch {
		case !f.Selectable:
		case known[f.Path]:
			sel[f.Path] = s.sel[f.Path]
		default:
			sel[f.Path] = f.Default
		}
	}
	listed := make(map[string]bool, len(st.Files))
	for _, f := range st.Files {
		listed[f.Path] = true
	}
	s.status, s.sel, s.listed = st, sel, listed
	s.list.Clamp(len(st.Files))
}

// blocked is why the read keys (diff) are disabled, "" when they are not.
func (s *Screen) blocked() string {
	switch {
	case !s.loaded:
		return whyLoading
	case s.repo.Manager == gitrepo.Unknown:
		return whyUnknown
	case s.repo.Manager == gitrepo.Chezmoi:
		return whyChezmoi
	case s.repo.Manager == gitrepo.None:
		return whyNone
	case s.repo.Manager == gitrepo.Missing:
		return whyMissing
	}
	return ""
}

// writeBlocked is why the selection and commit keys are disabled.
func (s *Screen) writeBlocked() string {
	if why := s.blocked(); why != "" {
		return why
	}
	if s.repo.Refusal != "" {
		return refused + mask.Sanitize(s.repo.Refusal)
	}
	return ""
}

// keyPress routes a key by mode.
func (s *Screen) keyPress(k tea.KeyPressMsg) tea.Cmd {
	switch s.mode {
	case modeDiff:
		return s.diffKey(k.String())
	case modeCommit:
		return s.commitKey(k)
	}
	return s.listKey(k.String())
}

func (s *Screen) diffKey(k string) tea.Cmd {
	if s.diff.scroll.Key(k, len(s.diff.lines)) {
		return nil
	}
	if k == keyEsc {
		s.diffs.Supersede()
		s.mode, s.diff = modeList, diffPane{}
	}
	return nil
}

func (s *Screen) listKey(k string) tea.Cmd {
	if s.list.Key(k, len(s.status.Files)) {
		return nil
	}
	switch k {
	case "r":
		s.note = ""
		return s.Refresh()
	case "space":
		s.toggle()
	case "a":
		s.toggleAll()
	case "enter", "d":
		return s.openDiff()
	case "c":
		s.openCommit()
	case keyEsc:
		s.note = ""
	}
	return nil
}

// current is the file under the cursor.
func (s *Screen) current() (gitrepo.File, bool) {
	if s.list.Cursor < 0 || s.list.Cursor >= len(s.status.Files) {
		return gitrepo.File{}, false
	}
	return s.status.Files[s.list.Cursor], true
}

func (s *Screen) toggle() {
	if why := s.writeBlocked(); why != "" {
		s.note = why
		return
	}
	f, ok := s.current()
	switch {
	case !ok:
	case !f.Selectable:
		s.note = gitrepo.DisplayName(f.Path) + ": " + mask.Sanitize(f.Note)
	default:
		s.sel[f.Path] = !s.sel[f.Path]
	}
}

// toggleAll selects every selectable file, or clears the selection when
// all of them already are.
func (s *Screen) toggleAll() {
	if why := s.writeBlocked(); why != "" {
		s.note = why
		return
	}
	all := true
	for _, f := range s.status.Files {
		all = all && (!f.Selectable || s.sel[f.Path])
	}
	for _, f := range s.status.Files {
		if f.Selectable {
			s.sel[f.Path] = !all
		}
	}
}

// selectedPaths is the selected File.Path values, in list order.
func (s *Screen) selectedPaths() []string {
	var out []string
	for _, f := range s.status.Files {
		if f.Selectable && s.sel[f.Path] {
			out = append(out, f.Path)
		}
	}
	return out
}

// diffable reports whether a file's content may be shown: never a state
// file, a sensitive one or an unusual name (a directory, a special file or
// a name git cannot be given).
func diffable(f gitrepo.File) bool {
	switch f.Class {
	case gitrepo.Owned, gitrepo.Other, gitrepo.Vault:
		return true
	}
	return false
}

// openDiff shows the diff of the file under the cursor, read in the
// background.
func (s *Screen) openDiff() tea.Cmd {
	if why := s.blocked(); why != "" {
		s.note = why
		return nil
	}
	f, ok := s.current()
	switch {
	case !ok:
		return nil
	case !diffable(f):
		s.note = notShown + mask.Sanitize(f.Note)
		return nil
	}
	gen, ctx := s.diffs.Next(s.base)
	s.mode, s.diff = modeDiff, diffPane{title: gitrepo.DisplayName(f.Path)}
	git, repo, unborn, limit := s.git, s.repo, s.status.Branch.Unborn, s.limit
	return func() tea.Msg { return readDiff(ctx, gen, git, repo, unborn, f, limit) }
}

// readDiff reads one file's diff: an untracked file's content as added
// lines, anything else through git diff against HEAD. Every line goes
// through MaskPatchLine and the whole is capped at ui.MaxOutput.
func readDiff(ctx context.Context, gen int, git runner.Runner, repo gitrepo.Repo, unborn bool, f gitrepo.File, limit time.Duration) diffMsg {
	if f.Kind == gitrepo.Untracked {
		b, err := bounded(ctx, limit, func() (capped, error) { return readFile(repo.Top, f.Path, ui.MaxOutput) })
		if err != nil {
			return diffMsg{gen: gen, note: notShown + errNote(err)}
		}
		if strings.IndexByte(b.data, 0) >= 0 {
			return diffMsg{gen: gen, note: "binary file: content not shown"}
		}
		return maskedDiff(gen, f.HomeRel, addedPatch(b.data), b.cut)
	}
	argv := gitrepo.DiffArgv(repo, unborn, f.Path, 3)
	if argv == nil {
		return diffMsg{gen: gen, note: notShown + "git cannot be given this path"}
	}
	res, err := ui.Read(ctx, git, limit, argv...)
	if note := ui.ReadNote("git diff", res, err); note != "" {
		return diffMsg{gen: gen, note: note}
	}
	patch, cut := strings.CutSuffix(res.Stdout, runner.TruncatedMark)
	return maskedDiff(gen, f.HomeRel, patch, cut)
}

// addedPatch shows content as the added lines of a new file.
func addedPatch(content string) string {
	content = strings.TrimSuffix(content, "\n")
	if content == "" {
		return ""
	}
	return "+" + strings.ReplaceAll(content, "\n", "\n+")
}

// maskedDiff masks every line of patch and caps the result at ui.MaxOutput.
func maskedDiff(gen int, homeRel, patch string, cut bool) diffMsg {
	m := diffMsg{gen: gen, trunc: cut}
	if patch == "" {
		m.note = "no changes to show"
		return m
	}
	size := 0
	for _, l := range strings.Split(strings.TrimSuffix(patch, "\n"), "\n") {
		l = strings.ReplaceAll(gitrepo.MaskPatchLine(homeRel, strings.TrimSuffix(l, "\r")), "\t", "  ")
		if size += len(l) + 1; size > ui.MaxOutput {
			m.trunc = true
			break
		}
		m.lines = append(m.lines, l)
	}
	return m
}

// capped is a file read cut at a size: data and whether more followed.
type capped struct {
	data string
	cut  bool
}

// errSymlink and errNotRegular refuse a read through a symlink or of
// anything that is not a regular file.
var (
	errSymlink    = errors.New("a symlink is not followed")
	errNotRegular = errors.New("not a regular file")
)

// readFile reads at most limit bytes of the regular file rel under top,
// never through a symlink: rel itself is Lstat'ed, its parent must resolve
// inside top, and the opened file must be the one Lstat saw.
func readFile(top, rel string, limit int) (capped, error) {
	full := filepath.Join(top, filepath.FromSlash(rel))
	dir, err := filepath.EvalSymlinks(filepath.Dir(full))
	if err != nil {
		return capped{}, err
	}
	if r, err := filepath.Rel(top, dir); err != nil || (r != "." && !filepath.IsLocal(r)) {
		return capped{}, errSymlink
	}
	fi, err := os.Lstat(full)
	switch {
	case err != nil:
		return capped{}, err
	case fi.Mode()&os.ModeSymlink != 0:
		return capped{}, errSymlink
	case !fi.Mode().IsRegular():
		return capped{}, errNotRegular
	}
	fh, err := os.Open(full)
	if err != nil {
		return capped{}, err
	}
	defer func() { _ = fh.Close() }()
	if st, err := fh.Stat(); err != nil || !os.SameFile(fi, st) {
		return capped{}, errNotRegular
	}
	b, err := io.ReadAll(io.LimitReader(fh, int64(limit)+1))
	if err != nil {
		return capped{}, err
	}
	if len(b) > limit {
		return capped{data: string(b[:limit]), cut: true}, nil
	}
	return capped{data: string(b)}, nil
}

// errNote is a display-safe reason for a failed read: never the path's
// bytes beyond what mask.FirstLine keeps.
func errNote(err error) string {
	switch {
	case errors.Is(err, errSymlink):
		return errSymlink.Error()
	case errors.Is(err, errNotRegular):
		return errNotRegular.Error()
	case errors.Is(err, os.ErrNotExist):
		return "the file is gone"
	case errors.Is(err, context.DeadlineExceeded):
		return "the read timed out"
	}
	return "the file could not be read"
}
