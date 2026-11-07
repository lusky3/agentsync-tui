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

// publicHosts are the public hosting services a push warns about: the TUI
// cannot see whether the repository there is private.
var publicHosts = map[string]bool{
	"github.com": true, "gitlab.com": true, "bitbucket.org": true,
	"codeberg.org": true, "gitea.com": true, "git.sr.ht": true,
}

// errPatchPath is a patch section whose path cannot be read back.
var errPatchPath = errors.New("cannot read a path in the outgoing patch")

// scanOutgoing is the outgoing guard (R5 for a push): every path the
// outgoing commits touch is listed and a state or sensitive one refused,
// then every commit's patch is scanned file by file, so a credential added
// in one commit and removed in a later one is still found.
func scanOutgoing(ctx context.Context, in netInput, base, tip string) ([]gitrepo.Finding, []string) {
	names, patchArgv := gitrepo.OutgoingNamesArgv(in.repo, base, tip), gitrepo.OutgoingPatchArgv(in.repo, base, tip)
	if names == nil || patchArgv == nil {
		return nil, []string{refused + "the outgoing range is not a pair of object ids"}
	}
	res, err := ui.Read(ctx, in.git, in.limit, names...)
	if note := ui.ReadNote("git log", res, err); note != "" {
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
	if note := ui.ReadNote("git log", res, err); note != "" {
		return nil, []string{refused + note}
	}
	patch, cut := strings.CutSuffix(res.Stdout, runner.TruncatedMark)
	secs := splitPatch(patch)
	if r := readPostImages(ctx, in, secs); r != nil {
		return nil, r
	}
	fs, err := bounded(ctx, in.limit, func() ([]gitrepo.Finding, error) { return scanPatch(secs, in.repo.Pathspec) })
	if err != nil {
		return nil, []string{refused + "cannot scan the outgoing commits: " + errNote(err), "to inspect them" + runOutside,
			"  " + inspectCommand(in.repo.Top)}
	}
	if cut {
		fs = append(fs, gitrepo.Finding{Path: "outgoing commits", Kind: gitrepo.KindNotScannedPatch})
	}
	return fs, nil
}

// outgoingPaths is the distinct paths of a NUL-separated name list.
func outgoingPaths(names string) []string {
	seen := map[string]bool{}
	var out []string
	for _, p := range strings.Split(names, "\x00") {
		p = strings.TrimLeft(p, "\n")
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

// scanPatch scans the added lines of each file section, in the context of
// the file as its commit records it (readPostImages). A binary section,
// or one whose file was cut, is a soft finding: it was not scanned (whole).
func scanPatch(secs []filePatch, pathspec string) ([]gitrepo.Finding, error) {
	var out []gitrepo.Finding
	for _, sec := range secs {
		if sec.path == "" {
			return nil, errPatchPath
		}
		rel := homeRel(pathspec, sec.path)
		if binaryRe.MatchString(sec.body) {
			out = append(out, notScanned(gitrepo.File{HomeRel: rel}, gitrepo.KindNotScannedBinary))
		}
		if sec.postCut {
			out = append(out, notScanned(gitrepo.File{HomeRel: rel}, gitrepo.KindNotScannedLarge))
		}
		out = append(out, gitrepo.ScanAdded(rel, gitrepo.AddedLines(sec.body), sec.post)...)
	}
	return out, nil
}

// gitlinkRe is a section of a submodule (gitlink) change: its added line
// is "Subproject commit <sha>", and there is no file to read.
var gitlinkRe = regexp.MustCompile(`(?m)^(?:new file mode|index \S+) 160000$`)

// readPostImages reads, for each section with added lines, the file as the
// section's commit records it (gitrepo.BlobArgv, the runner's output cap),
// keeping its first maxScanned bytes: the -U0 patch holds only the changed
// lines, and a line in a multi-line value is classified by the key that
// opened it, which may be unchanged. It returns a refusal, or nil.
func readPostImages(ctx context.Context, in netInput, secs []filePatch) []string {
	n := 0
	for i := range secs {
		sec := &secs[i]
		if sec.path == "" || gitlinkRe.MatchString(sec.body) || len(gitrepo.AddedLines(sec.body)) == 0 {
			continue
		}
		if n++; n > maxOutgoing {
			return []string{whyTooMany}
		}
		if r := sec.readPost(ctx, in); r != nil {
			return r
		}
	}
	return nil
}

// readPost reads sec's file at its commit into sec.post; a refusal or nil.
func (sec *filePatch) readPost(ctx context.Context, in netInput) []string {
	argv := gitrepo.BlobArgv(in.repo, sec.commit, sec.path)
	if argv == nil {
		return []string{refused + "cannot read " + gitrepo.DisplayName(sec.path) + " in an outgoing commit"}
	}
	res, err := ui.Read(ctx, in.git, in.limit, argv...)
	if note := ui.ReadNote("git cat-file", res, err); note != "" {
		return []string{refused + note}
	}
	data, cut := strings.CutSuffix(res.Stdout, runner.TruncatedMark)
	sec.post, sec.postCut = data[:min(len(data), maxScanned)], cut || len(data) > maxScanned
	return nil
}

// filePatch is one "diff --git" section: its path ("" when the header
// could not be read), the commit it belongs to ("" before any %H line),
// the lines after the header, and its file as that commit records it
// (readPostImages; postCut when that was cut).
type filePatch struct {
	path, commit, body string
	post               string
	postCut            bool
}

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

func normHost(h string) string { return strings.TrimSuffix(strings.ToLower(h), ".") }
