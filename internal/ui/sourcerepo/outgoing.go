package sourcerepo

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/gitrepo"
	"github.com/lusky3/agentsync-tui/internal/mask"
	"github.com/lusky3/agentsync-tui/internal/runner"
	"github.com/lusky3/agentsync-tui/internal/ui"
)

// maxOutgoing is how many paths the outgoing commits may touch. Past it the
// push is refused: an unscanned push cannot be taken back, and nobody
// reviews 2000 files on this screen.
const maxOutgoing = 2000

// whyTooMany refuses a push whose outgoing commits touch too many paths.
const whyTooMany = refused + "too many files to scan; review and push outside the TUI"

// maxChanges is how many file changes (a file in one commit, each read for
// its multi-line context) the outgoing commits may hold. Past it the push
// is refused (whyTooManyChanges): 2000 files in each of 10 commits is more
// than this screen should read before a push.
var maxChanges = 20000

// whyTooManyChanges refuses a push whose commits hold too many changes.
const whyTooManyChanges = refused + "too many changes to scan; review and push outside the TUI"

// outgoingScanBound is the deadline of the whole outgoing scan (its reads,
// one per changed file per commit, and the scan), on top of each read's
// own bound: the default network bound, ui.DefaultReadTimeout.
var outgoingScanBound = ui.DefaultReadTimeout

// whyWideCut refuses a whole-history push scan (base "") whose patch
// read the runner cut: the unread part may be anything the push
// destination lacks.
const whyWideCut = refused + "the outgoing history is too large to scan completely; push it outside the TUI"

// whyMessagesCut refuses a push whose commit-message read the runner cut,
// in a narrow scan as in a wide one (Ruling 53): the unread messages would
// be published unscanned.
const whyMessagesCut = refused + "the outgoing commit messages are too large to scan completely; push outside the TUI"

// maxCommits is how many outgoing commits the push guard reads one by one
// (CommitObjectArgv, for a NUL byte). Past it, or when the commit list
// itself was cut, the push is refused (whyTooManyCommits).
var maxCommits = 10000

// whyTooManyCommits refuses a push with more outgoing commits than
// maxCommits, or whose commit list the runner cut.
const whyTooManyCommits = refused + "too many outgoing commits to check; review and push outside the TUI"

// whyBadCommitList refuses a push whose commit list holds a line that is
// not an object id.
const whyBadCommitList = refused + "cannot read the list of outgoing commits"

// gitLog names the outgoing log reads in a refusal.
const gitLog = "git log"

// whyBadRange refuses a push whose outgoing range is not two object ids.
const whyBadRange = refused + "the outgoing range is not a pair of object ids"

// whyUnfinished refuses a push whose scan ran past outgoingScanBound.
const whyUnfinished = refused + "the outgoing scan did not finish; review and push outside the TUI"

// publicHosts are the public hosting services a push warns about: the TUI
// cannot see whether the repository there is private.
var publicHosts = map[string]bool{
	"github.com": true, "gitlab.com": true, "bitbucket.org": true,
	"codeberg.org": true, "gitea.com": true, "git.sr.ht": true,
}

// errPatchPath is a patch section whose path cannot be read back.
var errPatchPath = errors.New("cannot read a path in the outgoing patch")

// outgoingPaths is the distinct paths of a NUL-separated name list, each
// exactly as git listed it (with an empty --format, git puts nothing
// between commits, so a leading newline belongs to the name).
func outgoingPaths(names string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range strings.Split(names, "\x00") {
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// outgoingPathRefusal refuses a push whose commits touch a .state/,
// sensitive or unusual path (a name holding a credential among them, which
// the line scan never sees): the push would publish it whatever the scan
// finds.
func outgoingPathRefusal(r gitrepo.Repo, paths []string) []string {
	for _, p := range paths {
		if why := unpushablePath(homeRel(r.Pathspec, p)); why != "" {
			return []string{
				refused + "an outgoing commit touches " + gitrepo.DisplayName(p) + " (" + why + "); a push would publish it",
				"(deleting such a file counts too: the push publishes the history that held it)",
				"to inspect the commits" + runOutside,
				"  " + inspectCommand(r.Top),
			}
		}
	}
	return nil
}

// unpushablePath is why a home-relative path may not be pushed from the
// TUI, or "" when it may: its class, or for an unusual name the reason.
func unpushablePath(rel string) string {
	class, note := gitrepo.Classify(rel, nil, false)
	switch class {
	case gitrepo.StateDir, gitrepo.Sensitive:
		return class.String() + " path"
	case gitrepo.Unusual:
		return strings.TrimSuffix(note, ", not selectable")
	}
	return ""
}

// scanOutgoing is the outgoing guard (R5 for a push): every path the
// outgoing commits touch is listed and a state or sensitive one refused,
// then every commit's patch is scanned file by file, so a credential added
// in one commit and removed in a later one is still found. The whole scan
// has one deadline, outgoingScanBound, on top of each read's own bound;
// past it the push is refused (whyUnfinished).
func scanOutgoing(ctx context.Context, in netInput, base, tip string) ([]gitrepo.Finding, []string) {
	ctx, cancel := context.WithTimeout(ctx, outgoingScanBound)
	defer cancel()
	fs, refusal := scanOutgoingRange(ctx, in, base, tip)
	if ctx.Err() != nil {
		return nil, []string{whyUnfinished}
	}
	return fs, refusal
}

// scanOutgoingRange is scanOutgoing's work, under its deadline.
func scanOutgoingRange(ctx context.Context, in netInput, base, tip string) ([]gitrepo.Finding, []string) {
	names, patchArgv := gitrepo.OutgoingNamesArgv(in.repo, base, tip), gitrepo.OutgoingPatchArgv(in.repo, base, tip)
	if names == nil || patchArgv == nil {
		return nil, []string{whyBadRange}
	}
	res, err := ui.Read(ctx, in.git, in.limit, names...)
	if note := ui.ReadNote(gitLog, res, err); note != "" {
		return nil, []string{refused + note}
	}
	list, cut := strings.CutSuffix(res.Stdout, runner.TruncatedMark)
	paths := outgoingPaths(list)
	if cut || len(paths) > maxOutgoing {
		return nil, []string{whyTooMany}
	}
	if r := outgoingPathRefusal(in.repo, paths); r != nil {
		return nil, r
	}
	res, err = ui.Read(ctx, in.git, in.limit, patchArgv...)
	if note := ui.ReadNote(gitLog, res, err); note != "" {
		return nil, []string{refused + note}
	}
	patch, cut := strings.CutSuffix(res.Stdout, runner.TruncatedMark)
	if cut && base == "" {
		return nil, []string{whyWideCut}
	}
	fs, r := scanSections(ctx, in, splitPatch(patch))
	if r != nil {
		return nil, r
	}
	if cut {
		fs = append(fs, gitrepo.Finding{Path: "outgoing commits", Kind: gitrepo.KindNotScannedPatch})
	}
	mfs, r := scanMessages(ctx, in, base, tip)
	if r != nil {
		return nil, r
	}
	return append(fs, mfs...), nil
}

// scanMessages scans the message of every outgoing commit as prose (R5):
// the push publishes the messages with the commits, and a commit made
// outside the TUI never met ValidCommitMessage. A message list the runner
// cut short refuses the push, narrow or wide (Ruling 53). Every commit
// object is then checked for a NUL byte (nulCommits), the commits taken
// from their own complete list (outgoingCommits), not from the message
// list.
func scanMessages(ctx context.Context, in netInput, base, tip string) ([]gitrepo.Finding, []string) {
	shas, r := outgoingCommits(ctx, in, base, tip)
	if r != nil {
		return nil, r
	}
	argv := gitrepo.OutgoingMessagesArgv(in.repo, base, tip)
	if argv == nil {
		return nil, []string{whyBadRange}
	}
	res, err := ui.Read(ctx, in.git, in.limit, argv...)
	if note := ui.ReadNote(gitLog, res, err); note != "" {
		return nil, []string{refused + note}
	}
	out, cut := strings.CutSuffix(res.Stdout, runner.TruncatedMark)
	if cut {
		return nil, []string{whyMessagesCut}
	}
	fs, err := bounded(ctx, in.limit, func(context.Context) ([]gitrepo.Finding, error) { return messageFindings(out), nil })
	if err != nil {
		return nil, scanRefusal(in, err)
	}
	nfs, r := nulCommits(ctx, in, shas)
	if r != nil {
		return nil, r
	}
	return append(fs, nfs...), nil
}

// outgoingCommits is every commit in the outgoing range
// (gitrepo.OutgoingCommitsArgv), read whole: a list the runner cut, or
// longer than maxCommits, refuses the push (whyTooManyCommits), as does a
// line that is not an object id.
func outgoingCommits(ctx context.Context, in netInput, base, tip string) ([]string, []string) {
	argv := gitrepo.OutgoingCommitsArgv(in.repo, base, tip)
	if argv == nil {
		return nil, []string{whyBadRange}
	}
	res, err := ui.Read(ctx, in.git, in.limit, argv...)
	if note := ui.ReadNote("git rev-list", res, err); note != "" {
		return nil, []string{refused + note}
	}
	out, cut := strings.CutSuffix(res.Stdout, runner.TruncatedMark)
	shas := strings.Fields(out)
	if cut || len(shas) > maxCommits {
		return nil, []string{whyTooManyCommits}
	}
	if slices.ContainsFunc(shas, func(s string) bool { return actions.ValidObjectID(s) != nil }) {
		return nil, []string{whyBadCommitList}
	}
	return shas, nil
}

// nulCommits reads each outgoing commit object whole (CommitObjectArgv):
// git log's %B ends a message at a NUL byte, so whatever follows one was
// not scanned, yet the push sends it. A commit holding a NUL is a hard
// finding (git itself refuses to make one; hash-object --literally or a
// fetch can bring one in). A read that fails, or that the runner cut (the
// NUL may lie past the cut), refuses the push.
func nulCommits(ctx context.Context, in netInput, shas []string) ([]gitrepo.Finding, []string) {
	var fs []gitrepo.Finding
	for _, sha := range shas {
		argv := gitrepo.CommitObjectArgv(in.repo, sha)
		if argv == nil {
			return nil, []string{whyBadRange}
		}
		res, err := ui.Read(ctx, in.git, in.limit, argv...)
		if note := ui.ReadNote("git cat-file", res, err); note != "" {
			return nil, []string{refused + note}
		}
		short := sha[:min(len(sha), 12)]
		raw, cut := strings.CutSuffix(res.Stdout, runner.TruncatedMark)
		if cut {
			return nil, []string{refused + "commit " + short + " is over 4 MiB: push it outside the TUI"}
		}
		if strings.IndexByte(raw, 0) >= 0 {
			fs = append(fs, gitrepo.Finding{Path: "commit " + short, Hard: true, Kind: gitrepo.KindNULInCommit})
		}
	}
	return fs, nil
}

// messageFindings scans each "<sha> NUL <message> NUL" record of an
// OutgoingMessagesArgv output, a message's lines numbered from 1 and
// named "commit message <short sha>".
func messageFindings(out string) []gitrepo.Finding {
	fields := strings.Split(out, "\x00")
	var fs []gitrepo.Finding
	for i := 0; i+1 < len(fields); i += 2 {
		sha := strings.TrimSpace(fields[i])
		name := "commit message " + sha[:min(len(sha), 12)]
		var lines []gitrepo.Line
		for n, l := range strings.Split(strings.TrimSuffix(fields[i+1], "\n"), "\n") {
			lines = append(lines, gitrepo.Line{N: n + 1, Text: strings.TrimSuffix(l, "\r")})
		}
		fs = append(fs, gitrepo.ScanLines(name, lines)...)
	}
	return fs
}

// scanSections scans each file section in turn: its file at its commit is
// read, the section scanned against it and the file dropped before the
// next section is read, so at most one post-image is held at a time. More
// than maxChanges sections with added lines (one read each) are refused.
func scanSections(ctx context.Context, in netInput, secs []filePatch) ([]gitrepo.Finding, []string) {
	changes := 0
	for _, sec := range secs {
		if sec.path == "" {
			return nil, scanRefusal(in, errPatchPath)
		}
		if sec.needsPost() {
			changes++
		}
	}
	if changes > maxChanges {
		return nil, []string{whyTooManyChanges}
	}
	var out []gitrepo.Finding
	for _, sec := range secs {
		fs, r := scanSection(ctx, in, sec)
		if r != nil {
			return nil, r
		}
		out = append(out, fs...)
	}
	return out, nil
}

// scanRefusal refuses a push whose patch could not be scanned.
func scanRefusal(in netInput, err error) []string {
	return []string{refused + "cannot scan the outgoing commits: " + errNote(err), "to inspect them" + runOutside,
		"  " + inspectCommand(in.repo.Top)}
}

// scanSection scans the added lines of one file section in the context of
// the file as its commit records it (readPost): the -U0 patch holds only
// the changed lines, and a line in a multi-line value is classified by
// the key that opened it, which may be unchanged. A binary section is a
// soft finding (not scanned), as is an added line past the post-image's
// 1 MiB cut (its multi-line context is not known).
func scanSection(ctx context.Context, in netInput, sec filePatch) ([]gitrepo.Finding, []string) {
	var post capped
	if sec.needsPost() {
		var r []string
		if post, r = readPost(ctx, in, sec); r != nil {
			return nil, r
		}
	}
	rel := homeRel(in.repo.Pathspec, sec.path)
	fs, err := bounded(ctx, in.limit, func(context.Context) ([]gitrepo.Finding, error) {
		var out []gitrepo.Finding
		if binaryRe.MatchString(sec.body) {
			out = append(out, notScanned(gitrepo.File{HomeRel: rel}, gitrepo.KindNotScannedBinary))
		}
		lines := gitrepo.AddedLines(sec.body)
		if post.cut && beyondCut(lines, post.data) {
			out = append(out, notScanned(gitrepo.File{HomeRel: rel}, gitrepo.KindContextNotChecked))
		}
		return append(out, gitrepo.ScanAdded(rel, lines, post.data)...), nil
	})
	if err != nil {
		return nil, scanRefusal(in, err)
	}
	return fs, nil
}

// beyondCut reports an added line past the last complete line of post, a
// post-image cut short: its multi-line context is not known.
func beyondCut(lines []gitrepo.Line, post string) bool {
	complete := strings.Count(post, "\n")
	return slices.ContainsFunc(lines, func(l gitrepo.Line) bool { return l.N > complete })
}

// gitlinkRe is a section of a submodule (gitlink) change: its added line
// is "Subproject commit <sha>", and there is no file to read.
var gitlinkRe = regexp.MustCompile(`(?m)^(?:new file mode|index \S+) 160000$`)

// needsPost reports a section whose file is read for context: one with
// added lines that is not a gitlink.
func (sec filePatch) needsPost() bool {
	return !gitlinkRe.MatchString(sec.body) && len(gitrepo.AddedLines(sec.body)) > 0
}

// readPost reads sec's file at its commit (gitrepo.BlobArgv, through the
// runner and its output cap), keeping a copy of its first maxScanned bytes
// so the read's whole output is not kept alive; cut when it was longer.
func readPost(ctx context.Context, in netInput, sec filePatch) (capped, []string) {
	argv := gitrepo.BlobArgv(in.repo, sec.commit, sec.path)
	if argv == nil {
		return capped{}, []string{refused + "cannot read " + gitrepo.DisplayName(sec.path) + " in an outgoing commit"}
	}
	res, err := ui.Read(ctx, in.git, in.limit, argv...)
	if note := ui.ReadNote("git cat-file", res, err); note != "" {
		return capped{}, []string{refused + note}
	}
	data, cut := strings.CutSuffix(res.Stdout, runner.TruncatedMark)
	return capped{data: strings.Clone(data[:min(len(data), maxScanned)]), cut: cut || len(data) > maxScanned}, nil
}

// filePatch is one "diff --git" section: its path ("" when the header
// could not be read), the commit it belongs to ("" before any %H line)
// and the lines after the header.
type filePatch struct{ path, commit, body string }

const diffHeader = "diff --git "

// splitPatch cuts a log -p output into file sections. Lines before the
// first header (a commit's %H line) belong to no section; a later commit's
// %H line ends the section before it, after its hunks. A section's commit
// is the last %H line before its header: a line that is exactly an object
// id (inside a hunk every line starts with a prefix character).
func splitPatch(patch string) []filePatch {
	var out []filePatch
	bodyStart := -1
	commit := ""
	for off := 0; off < len(patch); {
		next := len(patch)
		if i := strings.IndexByte(patch[off:], '\n'); i >= 0 {
			next = off + i + 1
		}
		line := strings.TrimRight(patch[off:next], "\r\n")
		if actions.ValidObjectID(line) == nil {
			commit = line
		}
		if rest, ok := strings.CutPrefix(line, diffHeader); ok {
			if bodyStart >= 0 {
				out[len(out)-1].body = patch[bodyStart:off]
			}
			out = append(out, filePatch{path: diffPath(rest), commit: commit})
			bodyStart = next
		}
		off = next
	}
	if bodyStart >= 0 {
		out[len(out)-1].body = patch[bodyStart:]
	}
	return out
}

// diffPath reads the path of a "diff --git a/<p> b/<p>" header (with
// --no-renames both sides name the same path), C-quoted by git when the
// path has special bytes. It returns "" when the header is anything else.
func diffPath(rest string) string {
	if strings.HasPrefix(rest, `"`) {
		q, err := strconv.QuotedPrefix(rest)
		if err != nil {
			return ""
		}
		p, err := strconv.Unquote(q)
		if err != nil {
			return ""
		}
		p, _ = strings.CutPrefix(p, "a/")
		return p
	}
	n := len(rest) - len("a/ b/")
	if n <= 0 || n%2 != 0 {
		return ""
	}
	n /= 2
	a, b := rest[:2+n], rest[2+n:]
	if !strings.HasPrefix(a, "a/") || b != " b/"+a[2:] {
		return ""
	}
	return a[2:]
}

// pushWarnings are the push modal's notices: every push URL (masked), a
// public hosting service among them, and a credential embedded in one.
func pushWarnings(urls []string) []string {
	shown := make([]string, len(urls))
	var hosts []string
	cred := false
	for i, u := range urls {
		shown[i] = mask.OneLine(mask.Text(u))
		host, userinfo := urlHost(u)
		if publicHosts[host] && !slices.Contains(hosts, host) {
			hosts = append(hosts, host)
		}
		cred = cred || userinfo
	}
	out := []string{"push URL: " + strings.Join(shown, ", ")}
	for _, h := range hosts {
		out = append(out, "pushes to "+h+": make sure the repository is private; the TUI cannot see its visibility")
	}
	if cred {
		out = append(out, "the remote URL embeds a credential")
	}
	return out
}

// urlHost is the lower-cased host of a git URL (scheme://… or the scp
// form user@host:path), "" for a local path; userinfo reports an http(s)
// URL that carries one.
func urlHost(u string) (host string, userinfo bool) {
	if strings.Contains(u, "://") {
		p, err := url.Parse(u)
		if err != nil {
			return "", false
		}
		scheme := strings.ToLower(p.Scheme)
		return normHost(p.Hostname()), p.User != nil && (scheme == "https" || scheme == "http")
	}
	left, _, ok := strings.Cut(u, ":")
	if !ok || strings.Contains(left, "/") {
		return "", false
	}
	if _, h, found := strings.Cut(left, "@"); found {
		left = h
	}
	return normHost(left), false
}

// normHost is a host name lower-cased without a trailing dot.
func normHost(h string) string { return strings.TrimSuffix(strings.ToLower(h), ".") }
