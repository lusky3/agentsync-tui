package sourcerepo

import (
	"context"
	"errors"
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

// netOp is the network action a key started.
type netOp int

const (
	opFetch netOp = iota
	opPull
	opPush
	opInit
)

// The fixed texts of the network keys.
const (
	whyLocalUpstream = "the upstream is a local branch; fetch, pull and push it outside the TUI"
	whyMirror        = "the remote is configured as a mirror; push outside the TUI"
	checkingNet      = "checking: status, upstream, outgoing commits, secret scan…"
	pulledNote       = "pulled: agents see these changes only after an apply. D = Drift, A = Apply"
	rejectedNote     = "remote has new commits: fetch (f) and pull (p) first"
	runOutside       = ", run outside the TUI:"
	outgoingRange    = "@{u}..HEAD"
	headsPrefix      = "refs/heads/"
)

// netState is the network keys' part of the screen.
type netState struct {
	netBusy      bool              // a pull or push guard round is running
	net          *pendingNet       // the network action handed to the shell
	pulled       bool              // the last pull succeeded: D and A jump
	pushFindings []gitrepo.Finding // the last pre-push scan's, masked
	url          ui.Field          // the bootstrap URL
	urlNote      string            // why the URL was refused, display-safe
}

func newNetState() netState {
	return netState{url: ui.Field{Max: maxURLBytes}}
}

// pendingNet is a network action handed to the shell, until its RunMsg.
type pendingNet struct {
	op                netOp
	key               string
	top, remote, head string
}

// remoteInfo is the current branch's upstream as for-each-ref reports it,
// read with every status: name is %(upstream:remotename) ("." when the
// upstream is a local branch), ref is %(upstream:remoteref).
type remoteInfo struct{ name, ref string }

// readRemote reads the upstream of b's branch; the zero remoteInfo when it
// has none. note is display-safe and empty on success.
func readRemote(ctx context.Context, git runner.Runner, top string, b gitrepo.Branch, limit time.Duration) (remoteInfo, string) {
	if b.Upstream == "" || b.Head == "" || b.Detached {
		return remoteInfo{}, ""
	}
	want := headsPrefix + b.Head
	res, err := ui.Read(ctx, git, limit, "-C", top, "for-each-ref",
		"--format=%(refname)%00%(upstream:remotename)%00%(upstream:remoteref)", want)
	if note := ui.ReadNote("git for-each-ref", res, err); note != "" {
		return remoteInfo{}, note
	}
	for _, l := range strings.Split(res.Stdout, "\n") {
		// The pattern may match more than the branch (a glob character in
		// its name); only the exact ref counts.
		if f := strings.Split(l, "\x00"); len(f) == 3 && f[0] == want {
			return remoteInfo{name: f[1], ref: f[2]}, ""
		}
	}
	return remoteInfo{}, ""
}

// remoteKey handles the network and bootstrap keys of the list.
func (s *Screen) remoteKey(k string) tea.Cmd {
	switch k {
	case "f":
		return s.fetch()
	case "p":
		return s.startNet(opPull)
	case "P":
		return s.startNet(opPush)
	case "i":
		s.openURL()
	case "D", "A":
		return s.jump(k)
	}
	return nil
}

// jump offers Drift and Apply after a successful pull (D5).
func (s *Screen) jump(k string) tea.Cmd {
	if !s.pulled {
		return nil
	}
	title := "Drift"
	if k == "A" {
		title = "Apply"
	}
	return func() tea.Msg { return ui.GotoMsg{Title: title} }
}

// netRefusal is why op cannot start from what the last refresh read, or
// nil. Pull and push are refused below the repository's root (R7).
func (s *Screen) netRefusal(op netOp) []string {
	if why := s.writeBlocked(); why != "" {
		return []string{why}
	}
	if op != opFetch && s.repo.Layout == gitrepo.BelowRoot {
		return belowRoot(s.repo.Top)
	}
	return upstreamRefusal(s.repo.Top, s.status.Branch, s.remote)
}

// upstreamRefusal is why the branch cannot be fetched, pulled or pushed
// through its upstream, or nil.
func upstreamRefusal(top string, b gitrepo.Branch, r remoteInfo) []string {
	switch {
	case b.Detached:
		return []string{msgDetached}
	case b.Upstream == "" || r.name == "":
		return noUpstream(top, b.Head)
	case r.name == ".":
		return []string{refused + whyLocalUpstream}
	case actions.ValidRemoteName(r.name) != nil:
		return []string{refused + "the upstream's remote name is not one the TUI passes to git; fetch, pull and push outside the TUI"}
	}
	return nil
}

// noUpstream is the guidance for a branch without an upstream (D6): the
// TUI never creates remotes or repositories.
func noUpstream(top, branch string) []string {
	t, b := shellQuote(top), "<branch>"
	if branch != "" {
		b = shellQuote(branch)
	}
	return []string{
		"No upstream. Create a PRIVATE repository, then run outside the TUI:",
		"  " + gitC + t + " remote add origin <url>",
		"  " + gitC + t + " push -u origin " + b,
		"With the GitHub CLI: gh repo create <name> --private --source " + t + " --push",
	}
}

// belowRoot explains why pull and push are refused when the repository
// holds more than the home (R7).
func belowRoot(top string) []string {
	t := shellQuote(top)
	return []string{
		"the repository at " + t + " holds more than the agentsync home; pull and push it outside the TUI:",
		"  " + gitC + t + " pull --ff-only",
		"  " + gitC + t + " push",
	}
}

// fetch emits `git fetch <remote>` for the upstream read at the last
// refresh (D7): ReadOnly, so the shell runs it without a modal.
func (s *Screen) fetch() tea.Cmd {
	if r := s.netRefusal(opFetch); r != nil {
		s.note = strings.Join(r, "\n")
		return nil
	}
	act, err := actions.GitFetch(actions.GitRepo{Top: s.repo.Top}, s.remote.name)
	return s.emitNet(act, err, pendingNet{op: opFetch, remote: s.remote.name, head: s.status.Branch.Head})
}

// emitNet hands act to the shell and remembers it until its RunMsg.
func (s *Screen) emitNet(act actions.Action, err error, p pendingNet) tea.Cmd {
	if err != nil {
		s.note = refused + mask.FirstLine(err.Error())
		return nil
	}
	p.key, p.top = act.Key(), s.repo.Top
	s.net, s.note, s.pulled = &p, "", false
	return ui.Do(act, s.scope)
}

// netInput is what a pull or push guard round needs, copied off the screen.
type netInput struct {
	repo  gitrepo.Repo
	git   runner.Runner
	limit time.Duration
	op    netOp
}

// netMsg carries one finished pull or push guard round.
type netMsg struct {
	gen          int
	op           netOp
	refusal      []string // display-safe lines; nil when every guard passed
	push         pushPlan
	remote, head string
}

// pushPlan is what the push guard found: the commit it scanned (the push
// sends exactly that commit, Ruling 32), the push URLs (unmasked) and the
// scan's findings.
type pushPlan struct {
	sha      string
	urls     []string
	findings []gitrepo.Finding
}

// startNet checks what the last refresh read, then starts the guard round
// of a pull or push in the background.
func (s *Screen) startNet(op netOp) tea.Cmd {
	if s.netBusy {
		return nil
	}
	if r := s.netRefusal(op); r != nil {
		s.note = strings.Join(r, "\n")
		return nil
	}
	s.netBusy, s.note, s.pushFindings, s.commit.findings = true, "", nil, nil
	gen, ctx := s.nets.Next(s.base)
	in := netInput{repo: s.repo, git: s.git, limit: s.limit, op: op}
	return func() tea.Msg { return runNet(ctx, gen, in) }
}

// cancelNet drops a running guard round.
func (s *Screen) cancelNet() {
	s.nets.Supersede()
	s.netBusy = false
}

// runNet re-reads the status and upstream, then checks the git dir and,
// for a push, everything the push would publish.
func runNet(ctx context.Context, gen int, in netInput) netMsg {
	m := netMsg{gen: gen, op: in.op}
	sn, note := readSnapshot(ctx, in.git, in.repo, in.limit)
	if note != "" {
		m.refusal = []string{refused + note}
		return m
	}
	b := sn.status.Branch
	if m.refusal = upstreamRefusal(in.repo.Top, b, sn.remote); m.refusal != nil {
		return m
	}
	if why := gitDirRefusal(in.repo.GitDir); why != "" {
		m.refusal = []string{why}
		return m
	}
	m.remote, m.head = sn.remote.name, b.Head
	if in.op == opPush {
		m.push, m.refusal = pushGuard(ctx, in, b, sn.remote)
	}
	return m
}

// pushGuard is the pre-push guard: ahead of a fetched upstream and not
// behind it, the upstream is the same-named branch on a remote that is
// not a mirror, and the outgoing commits pass the scan. HEAD and the
// upstream are pinned to object ids first, so the scan and the push cover
// the same commits.
func pushGuard(ctx context.Context, in netInput, b gitrepo.Branch, r remoteInfo) (pushPlan, []string) {
	if why := pushBranchRefusal(b, r); why != "" {
		return pushPlan{}, []string{why}
	}
	if why := mirrorRefusal(ctx, in, r.name); why != "" {
		return pushPlan{}, []string{why}
	}
	urls, why := pushURLs(ctx, in, r.name)
	if why != "" {
		return pushPlan{}, []string{why}
	}
	base, tip, why := pinRange(ctx, in)
	if why != "" {
		return pushPlan{}, []string{why}
	}
	fs, refusal := scanOutgoing(ctx, in, base, tip)
	return pushPlan{sha: tip, urls: urls, findings: fs}, refusal
}

// pushBranchRefusal is why the branch's state forbids a push, or "".
func pushBranchRefusal(b gitrepo.Branch, r remoteInfo) string {
	switch {
	case !b.ABKnown:
		return refused + "the upstream has not been fetched: fetch (f) first"
	case b.Behind > 0:
		return refused + "behind " + strconv.Itoa(b.Behind) + ": pull first"
	case b.Ahead == 0:
		return "nothing to push: the branch is not ahead of its upstream"
	case r.ref != headsPrefix+b.Head:
		return refused + "the upstream is " + mask.Sanitize(r.ref) + " on " + mask.Sanitize(r.name) + ", not " +
			headsPrefix + mask.Sanitize(b.Head) + "; a push from the TUI would create a new remote branch: push outside the TUI"
	}
	return ""
}

// pinRange reads the object ids of the upstream and HEAD: the outgoing
// commits are base..tip, and the push sends tip.
func pinRange(ctx context.Context, in netInput) (base, tip, why string) {
	if base, why = revParse(ctx, in, upstreamCommit); why != "" {
		return "", "", why
	}
	tip, why = revParse(ctx, in, headCommit)
	return base, tip, why
}

const (
	headCommit     = "HEAD^{commit}"
	upstreamCommit = "@{u}^{commit}"
)

// revParse reads the object id rev names; why is a refusal.
func revParse(ctx context.Context, in netInput, rev string) (string, string) {
	res, err := ui.Read(ctx, in.git, in.limit, "-C", in.repo.Top, "rev-parse", "--verify", rev)
	if note := ui.ReadNote("git rev-parse", res, err); note != "" {
		return "", refused + note
	}
	id := strings.TrimSpace(res.Stdout)
	if actions.ValidObjectID(id) != nil {
		return "", refused + "git rev-parse did not print an object id"
	}
	return id, ""
}

// mirrorRefusal refuses a remote configured as a mirror: git would refuse
// the explicit refspec with a confusing error (D6).
func mirrorRefusal(ctx context.Context, in netInput, remote string) string {
	res, err := ui.Read(ctx, in.git, in.limit, "-C", in.repo.Top, "config", "--get", "--type=bool", "remote."+remote+".mirror")
	if err == nil && res.ExitCode == 1 { // not set
		return ""
	}
	if note := ui.ReadNote("git config", res, err); note != "" {
		return refused + note
	}
	if strings.TrimSpace(res.Stdout) == "true" {
		return refused + whyMirror
	}
	return ""
}

// pushURLs is every URL a push to remote goes to: pushurl entries when
// set, else url, with pushInsteadOf applied.
func pushURLs(ctx context.Context, in netInput, remote string) ([]string, string) {
	res, err := ui.Read(ctx, in.git, in.limit, "-C", in.repo.Top, "remote", "get-url", "--push", "--all", remote)
	if note := ui.ReadNote("git remote get-url", res, err); note != "" {
		return nil, refused + note
	}
	var urls []string
	for _, l := range strings.Split(res.Stdout, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			urls = append(urls, l)
		}
	}
	if len(urls) == 0 {
		return nil, refused + "the remote has no push URL"
	}
	return urls, ""
}

// onNet takes a finished guard round: a refusal is shown, a clean round
// emits the pull or push.
func (s *Screen) onNet(m netMsg) tea.Cmd {
	s.netBusy, s.pushFindings = false, m.push.findings
	if len(m.refusal) > 0 {
		s.note = strings.Join(m.refusal, "\n")
		return nil
	}
	p := pendingNet{op: m.op, remote: m.remote, head: m.head}
	if m.op == opPull {
		act, err := actions.GitPull(actions.GitRepo{Top: s.repo.Top})
		return s.emitNet(act, err, p)
	}
	act, err := actions.GitPush(actions.GitRepo{Top: s.repo.Top}, m.remote, m.head, m.push.sha)
	if err != nil {
		return s.emitNet(act, err, p)
	}
	c := countFindings(m.push.findings)
	if c.hard > 0 {
		s.note = strings.Join(pushHardRefusal(c.hard, s.repo.Top), "\n")
		return nil
	}
	warn := pushWarnings(m.push.urls)
	if len(m.push.findings) > 0 {
		act.TypedConfirm = true
		warn = append(warn, c.soft()+" in the outgoing commits, listed on the Source repo tab; type push to proceed")
	}
	act.Warning = strings.Join(warn, "; ")
	return s.emitNet(act, nil, p)
}

// pushHardRefusal explains hard findings in the outgoing commits.
func pushHardRefusal(n int, top string) []string {
	return []string{
		refused + plural(n, "hard finding") + " (likely secrets) in the outgoing commits, listed below; a push would publish them in history",
		"to inspect them" + runOutside,
		"  " + inspectCommand(top),
	}
}

func inspectCommand(top string) string {
	return gitC + shellQuote(top) + " log -p " + outgoingRange
}

// onNetRun takes the outcome of this screen's network action. The note
// never holds the run's output, only its exit code or error kind and what
// git's stderr says about it.
func (s *Screen) onNetRun(m ui.RunMsg) tea.Cmd {
	p := s.net
	if p == nil || m.Action.Key() != p.key {
		return nil
	}
	s.net = nil
	ok := m.Err == nil && m.Result.ExitCode == 0
	switch p.op {
	case opFetch:
		s.note = fetchOutcome(p, m, ok)
	case opPull:
		s.note, s.pulled = pullOutcome(p, m, ok), ok
	case opPush:
		s.note = pushOutcome(p, m, ok)
	case opInit:
		return s.initOutcome(m, ok)
	}
	return nil
}

// stopped reports a run cut off by its bound or interrupted.
func stopped(m ui.RunMsg) bool {
	return errors.Is(m.Err, context.DeadlineExceeded) || errors.Is(m.Err, context.Canceled)
}

func fetchOutcome(p *pendingNet, m ui.RunMsg, ok bool) string {
	switch {
	case ok:
		return "fetched " + mask.Sanitize(p.remote)
	case stopped(m):
		return "fetch stopped (" + outcomeOf(m) + "); press r to see what arrived"
	}
	return "fetch failed (" + outcomeOf(m) + ")"
}

// pullOutcome is the note after a pull (D5, R3).
func pullOutcome(p *pendingNet, m ui.RunMsg, ok bool) string {
	t := shellQuote(p.top)
	switch {
	case ok:
		return pulledNote
	case stopped(m):
		return "the pull was stopped; the worktree may be partly updated. Nothing was rolled back (" + outcomeOf(m) +
			"). To inspect" + runOutside + "\n  " + gitC + t + " status"
	case strings.Contains(m.Result.Stderr, "Not possible to fast-forward") || strings.Contains(m.Result.Stderr, "diverging"):
		return "local and remote have diverged; nothing was changed. Resolve outside the TUI:\n  " +
			gitC + t + " pull --rebase\n  or " + gitC + t + " merge @{u}\nthen press r"
	}
	return "pull failed (" + outcomeOf(m) + ")"
}

func pushOutcome(p *pendingNet, m ui.RunMsg, ok bool) string {
	switch {
	case ok:
		return "pushed " + mask.Sanitize(p.head) + " to " + mask.Sanitize(p.remote)
	case stopped(m):
		return "push stopped (" + outcomeOf(m) + "): the remote may or may not have the commits; fetch (f) to see"
	case strings.Contains(m.Result.Stderr, "non-fast-forward") || strings.Contains(m.Result.Stderr, "fetch first"):
		return rejectedNote
	}
	return "push failed (" + outcomeOf(m) + ")"
}
