package sourcerepo

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"

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

// outgoingNamesArgv lists every path any outgoing commit (base..tip)
// touches, not only the net diff; merges against their first parent.
// Renames are split into a deletion and an addition, so both names are
// listed.
func outgoingNamesArgv(r gitrepo.Repo, base, tip string) []string {
	return []string{"-C", r.Top, "log", "--format=", "--name-only", "-z", "--no-renames",
		"--diff-merges=first-parent", base + ".." + tip, "--", r.Pathspec}
}

// outgoingPatchArgv is every outgoing commit's own patch with no context,
// git's own bytes (no external diff, no textconv, no colour), fixed a/ b/
// prefixes and no rename sections, so each file section's header names
// one path twice. Merges diff against their first parent: never a
// combined diff, which AddedLines does not parse.
func outgoingPatchArgv(r gitrepo.Repo, base, tip string) []string {
	return []string{"-C", r.Top, "log", "-p", "--no-ext-diff", "--no-textconv", "--no-color", "-U0",
		"--no-renames", "--diff-merges=first-parent", "--src-prefix=a/", "--dst-prefix=b/",
		"--format=%H", base + ".." + tip, "--", r.Pathspec}
}

// scanOutgoing is the outgoing guard (R5 for a push): every path the
// outgoing commits touch is listed and a state or sensitive one refused,
// then every commit's patch is scanned file by file, so a credential added
// in one commit and removed in a later one is still found.
func scanOutgoing(ctx context.Context, in netInput, base, tip string) ([]gitrepo.Finding, []string) {
	res, err := ui.Read(ctx, in.git, in.limit, outgoingNamesArgv(in.repo, base, tip)...)
	if note := ui.ReadNote("git log", res, err); note != "" {
		return nil, []string{refused + note}
	}
	names, cut := strings.CutSuffix(res.Stdout, runner.TruncatedMark)
	paths := outgoingPaths(names)
	if cut || len(paths) > maxOutgoing {
		return nil, []string{whyTooMany}
	}
	if r := outgoingPathRefusal(in.repo, paths); r != nil {
		return nil, r
	}
	res, err = ui.Read(ctx, in.git, in.limit, outgoingPatchArgv(in.repo, base, tip)...)
	if note := ui.ReadNote("git log", res, err); note != "" {
		return nil, []string{refused + note}
	}
	patch, cut := strings.CutSuffix(res.Stdout, runner.TruncatedMark)
	fs, err := bounded(ctx, in.limit, func() ([]gitrepo.Finding, error) { return scanPatch(patch, in.repo.Pathspec) })
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

// outgoingPathRefusal refuses a push whose commits touch a .state/ or
// sensitive path: the push would publish it whatever the scan finds.
func outgoingPathRefusal(r gitrepo.Repo, paths []string) []string {
	for _, p := range paths {
		class, _ := gitrepo.Classify(homeRel(r.Pathspec, p), nil, false)
		if class == gitrepo.StateDir || class == gitrepo.Sensitive {
			return []string{
				refused + "an outgoing commit touches " + gitrepo.DisplayName(p) + " (" + class.String() + " path); a push would publish it",
				"to inspect the commits" + runOutside,
				"  " + inspectCommand(r.Top),
			}
		}
	}
	return nil
}

// scanPatch scans the added lines of each file section of patch. A binary
// section is a soft finding: it was not scanned.
func scanPatch(patch, pathspec string) ([]gitrepo.Finding, error) {
	var out []gitrepo.Finding
	for _, sec := range splitPatch(patch) {
		if sec.path == "" {
			return nil, errPatchPath
		}
		rel := homeRel(pathspec, sec.path)
		if binaryRe.MatchString(sec.body) {
			out = append(out, notScanned(gitrepo.File{HomeRel: rel}, gitrepo.KindNotScannedBinary))
		}
		out = append(out, gitrepo.ScanLines(rel, gitrepo.AddedLines(sec.body))...)
	}
	return out, nil
}

// filePatch is one "diff --git" section: its path ("" when the header
// could not be read) and the lines after the header.
type filePatch struct{ path, body string }

const diffHeader = "diff --git "

// splitPatch cuts a log -p output into file sections. Lines before the
// first header (a commit's %H line) belong to no section; a later commit's
// %H line ends the section before it, after its hunks.
func splitPatch(patch string) []filePatch {
	var out []filePatch
	bodyStart := -1
	for off := 0; off < len(patch); {
		next := len(patch)
		if i := strings.IndexByte(patch[off:], '\n'); i >= 0 {
			next = off + i + 1
		}
		if rest, ok := strings.CutPrefix(patch[off:next], diffHeader); ok {
			if bodyStart >= 0 {
				out[len(out)-1].body = patch[bodyStart:off]
			}
			out = append(out, filePatch{path: diffPath(strings.TrimRight(rest, "\r\n"))})
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
