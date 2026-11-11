// Package sourcerepo is the Source repo tab: how the agentsync home is
// versioned, what changed in it under the home pathspec, a masked per-file
// diff, a guarded commit of explicitly selected files (D2-D4, D8), fetch,
// fast-forward pull and a guarded push (D5-D7, R7), and the bootstrap of a
// missing home from a URL (D10).
//
// Every git call here is a local read run in a background round bounded by
// Timeouts.Local (status, upstream, diff, the pre-commit and pre-push scans),
// the push guard's one network read (ls-remote of the branch, bounded as a
// network action), or an Action handed to the shell (add, then commit;
// fetch, pull, push; agentsync init). Only the keys f, p, P and the bootstrap's enter emit an
// action that touches the network; nothing does on refresh or focus. Diff and file bytes reach only this screen's own diff message,
// already masked; they never reach a note, the status line or the log.
package sourcerepo

import (
	"context"
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
	modeURL
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
	base     context.Context
	limit    time.Duration // ui.Deps.Limits().Local(): every local read here
	netLimit time.Duration // the network bound: the push guard's ls-remote
	run      runner.Runner // agentsync, for the pre-commit check
	git      runner.Runner
	cz       runner.Runner
	scope    runner.Scope
	home     string

	reads, diffs, guards, nets ui.Rounds

	loaded   bool
	repo     gitrepo.Repo
	status   gitrepo.Status
	remote   remoteInfo          // the branch's upstream, read with status
	kept     map[string]keptNote // rename path -> why its source stays out of a commit
	loadNote string              // why the last load failed, display-safe
	sel      map[string]bool     // selected file paths (File.Path)
	list     ui.List

	mode    mode
	diff    diffPane
	commit  commitState
	pending *pendingCommit
	netState
	note string // last screen message, display-safe (may hold line breaks)

	width, height int
}

// loadMsg carries one detect-and-status round.
type loadMsg struct {
	gen  int
	repo gitrepo.Repo
	snap snapshot
	note string
}

// snapshot is what one status read learns: the status, the branch's
// upstream and why each in-home rename source stays out of a commit.
type snapshot struct {
	status gitrepo.Status
	remote remoteInfo
	kept   map[string]keptNote
}

// New returns the Source repo screen.
func New(d ui.Deps) *Screen {
	return &Screen{
		base: ui.BaseCtx(d), limit: d.Limits().Local(), netLimit: networkBound(d.Limits()), run: d.Runner, git: d.Git, cz: d.Chezmoi,
		scope: d.Scope, home: ui.HomeFor(d, d.Scope), sel: map[string]bool{}, commit: newCommitState(),
		netState: newNetState(), width: 80, height: 24,
	}
}

// networkBound is the bound on a network read, as on the network actions:
// Timeouts.Read, or ui.DefaultReadTimeout when that bound is lifted (R3:
// git is always bounded).
func networkBound(t ui.Timeouts) time.Duration {
	if t.Read > 0 {
		return t.Read
	}
	return ui.DefaultReadTimeout
}

// Title implements ui.Screen.
func (s *Screen) Title() string { return Title }

// Init is exactly Refresh.
func (s *Screen) Init() tea.Cmd { return s.Refresh() }

// CapturesKeys implements ui.KeyCapturer: while the message or URL field
// has focus.
func (s *Screen) CapturesKeys() bool { return s.mode == modeCommit || s.mode == modeURL }

// SetScope implements ui.ScopeSetter: the new home is detected afresh and
// everything loaded for the old one is dropped.
func (s *Screen) SetScope(r runner.Runner, scope runner.Scope, home string) {
	s.run, s.scope, s.home = r, scope, home
	s.reads.Supersede()
	s.diffs.Supersede()
	s.guards.Supersede()
	s.nets.Supersede()
	s.loaded, s.repo, s.status, s.loadNote = false, gitrepo.Repo{}, gitrepo.Status{}, ""
	s.remote, s.kept, s.netState = remoteInfo{}, nil, newNetState()
	s.sel, s.list, s.mode, s.diff = map[string]bool{}, ui.List{}, modeList, diffPane{}
	s.commit, s.pending, s.note = newCommitState(), nil, ""
}

// Shutdown implements ui.Shutdowner.
func (s *Screen) Shutdown() {
	s.reads.Cancel()
	s.diffs.Cancel()
	s.guards.Cancel()
	s.nets.Cancel()
}

// Refresh re-detects the repository and re-reads its status in the
// background, cancelling the previous round.
func (s *Screen) Refresh() tea.Cmd {
	gen, ctx := s.reads.Next(s.base)
	git, cz, home, limit := s.git, s.cz, s.home, s.limit
	return func() tea.Msg { return load(ctx, gen, git, cz, home, limit) }
}

// load is one refresh round: Detect, then, for a git repository with a
// toplevel, its status and upstream.
func load(ctx context.Context, gen int, git, cz runner.Runner, home string, limit time.Duration) loadMsg {
	repo, err := gitrepo.Detect(ctx, git, cz, home, limit)
	m := loadMsg{gen: gen, repo: repo}
	if err != nil {
		m.note = mask.FirstLine(err.Error())
		return m
	}
	if repo.Manager == gitrepo.Git && repo.Top != "" {
		m.snap, m.note = readSnapshot(ctx, git, repo, limit)
	}
	return m
}

// readSnapshot reads the status, then the upstream of its branch, and
// works out which rename sources a commit leaves out. note is display-safe
// and empty on success.
func readSnapshot(ctx context.Context, git runner.Runner, repo gitrepo.Repo, limit time.Duration) (snapshot, string) {
	st, note := readStatus(ctx, git, repo, limit)
	if note != "" {
		return snapshot{}, note
	}
	sn := snapshot{status: st, kept: keptSources(repo, st.Files)}
	sn.remote, note = readRemote(ctx, git, repo, st.Branch, limit)
	return sn, note
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
	st, err := bounded(ctx, limit, func(ctx context.Context) (gitrepo.Status, error) {
		return gitrepo.ParseStatusContext(ctx, []byte(res.Stdout), repo)
	})
	if err != nil {
		return gitrepo.Status{}, mask.FirstLine(err.Error())
	}
	return st, ""
}

// maxWorkers bounds the bounded workers alive at once. The cap is
// process-global: workerSlots is shared by every Screen and every kind of
// bounded call (status parse, file reads, scans). A worker abandoned on
// time keeps its slot until it returns, so refreshes over a slow file
// cannot pile up readers; when every slot is held, a new bounded call waits
// for one within its own limit and then fails as a timeout. The workers
// honour their context between reads (ctxReader), but a worker stuck in an
// uninterruptible read (a hung network filesystem) keeps its slot until
// that read returns or the process exits; once all four are stuck, every
// bounded call fails closed and the scans refuse with "cannot scan".
const maxWorkers = 4

var workerSlots = make(chan struct{}, maxWorkers)

// bounded runs fn on its own goroutine and gives up when ctx ends or limit
// passes; a late result is dropped. fn gets a context that ends when
// bounded returns, so a worker that honours it stops instead of running on.
// fn runs in one of the process-global maxWorkers slots.
func bounded[T any](ctx context.Context, limit time.Duration, fn func(context.Context) (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	var zero T
	select {
	case workerSlots <- struct{}{}:
	case <-ctx.Done():
		return zero, ctx.Err()
	}
	type result struct {
		v   T
		err error
	}
	ch := make(chan result, 1)
	go func() {
		defer func() { <-workerSlots }()
		v, err := fn(ctx)
		ch <- result{v, err}
	}()
	select {
	case r := <-ch:
		return r.v, r.err
	case <-ctx.Done():
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
	case netMsg:
		if s.nets.Current(msg.gen) {
			return s, s.onNet(msg)
		}
	case unpushedMsg:
		if s.nets.Current(msg.gen) {
			s.onUnpushed(msg)
		}
	case ui.RunMsg:
		s.onRun(msg)
		return s, s.onNetRun(msg)
	case ui.NotRunMsg:
		s.onNotRun(msg)
	case tea.KeyPressMsg:
		return s, s.keyPress(msg)
	}
	return s, nil
}

// apply takes a finished load. A load of another repository (the home's
// symlink moved) first drops the old one's files, selection and upstream,
// so they never stand under the new heading when its own read failed, and
// the work started on the old one (dropRepoWork).
func (s *Screen) apply(m loadMsg) {
	if s.loaded && !sameRepo(s.repo, m.repo) {
		s.dropRepoWork()
		s.applySnapshot(snapshot{})
	}
	s.loaded, s.repo, s.loadNote = true, m.repo, m.note
	if m.repo.Manager != gitrepo.Git || m.repo.Top == "" {
		s.applySnapshot(snapshot{})
		return
	}
	if m.note == "" {
		s.applySnapshot(m.snap)
	}
}

// dropRepoWork supersedes the diff, commit guard and pull or push guard
// rounds started on the old repository and returns to the list: a guard
// result read there must never emit a commit, pull or push here, where its
// files were neither selected nor scanned (and a pull refused below the
// root may not be refused). The commit message is kept. An action already
// handed to the shell names its repository in its own argv.
func (s *Screen) dropRepoWork() {
	s.diffs.Supersede()
	s.guards.Supersede()
	s.cancelNet()
	s.commit.busy, s.commit.refusal, s.commit.findings, s.pushFindings = false, nil, nil, nil
	if s.mode == modeCommit {
		s.note = "the repository changed: select the files and commit again"
	}
	if s.mode == modeDiff || s.mode == modeCommit {
		s.mode, s.diff = modeList, diffPane{}
	}
}

// sameRepo reports whether a and b are the same repository and home.
func sameRepo(a, b gitrepo.Repo) bool {
	return a.ResolvedHome == b.ResolvedHome && a.Top == b.Top && a.GitDir == b.GitDir && a.Pathspec == b.Pathspec
}

// applySnapshot takes a fresh status with what was read alongside it.
func (s *Screen) applySnapshot(sn snapshot) {
	s.remote, s.kept = sn.remote, sn.kept
	s.applyStatus(sn.status)
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
	s.status, s.sel = st, sel
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
	case modeURL:
		return s.urlKey(k)
	}
	return s.listKey(k.String())
}

// listKey handles a key in the file list.
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
		s.cancelNet()
	default:
		return s.remoteKey(k)
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

// toggle flips the selection of the file under the cursor, or says why
// it cannot be selected.
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

// selectedFiles is the selected records, in list order.
func (s *Screen) selectedFiles() []gitrepo.File {
	var out []gitrepo.File
	for _, f := range s.status.Files {
		if f.Selectable && s.sel[f.Path] {
			out = append(out, f)
		}
	}
	return out
}

// selectedPaths is the selected File.Path values, in list order.
func (s *Screen) selectedPaths() []string {
	var out []string
	for _, f := range s.selectedFiles() {
		out = append(out, f.Path)
	}
	return out
}
