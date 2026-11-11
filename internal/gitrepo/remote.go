package gitrepo

import "github.com/lusky3/agentsync-tui/internal/actions"

// The read argv of the Source repo tab's network keys (fetch, pull, push).
// Each starts `-C <top>`; the git Program adds the fixed hardening. None
// of them writes, and only RemoteBranchArgv touches the network (the push
// guard's one network read). A function returns nil when an
// argument fails its check, so a caller can never run a half-built argv.

// UpstreamArgv reads the upstream of branch head: one line per matching
// ref, "<refname> NUL <remote name> NUL <remote ref>". The pattern may
// match more than the branch (a glob character in its name); callers keep
// only the line whose refname is exactly refs/heads/<head>.
func UpstreamArgv(r Repo, head string) []string {
	if head == "" {
		return nil
	}
	return []string{"-C", r.Top, "for-each-ref",
		"--format=%(refname)%00%(upstream:remotename)%00%(upstream:remoteref)", "refs/heads/" + head}
}

// MirrorArgv reads remote.<remote>.mirror as a boolean (exit 1 when unset).
func MirrorArgv(r Repo, remote string) []string {
	if actions.ValidRemoteName(remote) != nil {
		return nil
	}
	return []string{"-C", r.Top, "config", "--get", "--type=bool", "remote." + remote + ".mirror"}
}

// PushURLsArgv lists every URL a push to remote goes to: its pushurl
// entries when set, else its url, with pushInsteadOf applied.
func PushURLsArgv(r Repo, remote string) []string {
	if actions.ValidRemoteName(remote) != nil {
		return nil
	}
	return []string{"-C", r.Top, "remote", "get-url", "--push", "--all", remote}
}

// FetchURLArgv prints the URL a fetch from remote uses: its first url,
// with insteadOf applied. A push goes there too only when PushURLsArgv
// lists exactly this URL.
func FetchURLArgv(r Repo, remote string) []string {
	if actions.ValidRemoteName(remote) != nil {
		return nil
	}
	return []string{"-C", r.Top, "remote", "get-url", remote}
}

// RemoteBranchArgv asks remote (over the network, at its fetch URL) where
// its branch is: at most one line "<object id> TAB refs/heads/<branch>"
// among the lines ls-remote prints (a pattern also matches a longer ref
// ending in it, so callers keep only the exact ref), none when the remote
// has no such branch.
func RemoteBranchArgv(r Repo, remote, branch string) []string {
	if actions.ValidRemoteName(remote) != nil || actions.ValidBranchName(branch) != nil {
		return nil
	}
	return []string{"-C", r.Top, "ls-remote", "--refs", remote, "refs/heads/" + branch}
}

// BranchCommitArgv prints the object id of the commit branch points at,
// named in full (refs/heads/<branch>), so neither HEAD moving to another
// branch nor a tag of the same name changes what it reads.
func BranchCommitArgv(r Repo, branch string) []string {
	if actions.ValidBranchName(branch) != nil {
		return nil
	}
	return []string{"-C", r.Top, "rev-parse", "--verify", "refs/heads/" + branch + "^{commit}"}
}

// BranchUpstreamCommitArgv prints the object id of the commit branch's
// upstream (its remote-tracking ref) points at. <branch>@{upstream} always
// reads branch as a branch name (git refuses refs/heads/<b>@{upstream}).
func BranchUpstreamCommitArgv(r Repo, branch string) []string {
	if actions.ValidBranchName(branch) != nil {
		return nil
	}
	return []string{"-C", r.Top, "rev-parse", "--verify", branch + "@{upstream}^{commit}"}
}

// CommitsAfterArgv counts the commits on branch that sha does not hold
// (after a push of sha: the local commits that were not pushed).
func CommitsAfterArgv(r Repo, sha, branch string) []string {
	if actions.ValidObjectID(sha) != nil || actions.ValidBranchName(branch) != nil {
		return nil
	}
	return []string{"-C", r.Top, "rev-list", "--count", sha + "..refs/heads/" + branch, "--"}
}

// outgoingRange is the revision range of the Outgoing*Argv reads:
// base..tip, or, when base is "", tip's whole history (a push destination
// whose state is not known may lack any of it). ok is false when tip, or
// a base that is not "", is not an object id.
func outgoingRange(base, tip string) (string, bool) {
	switch {
	case actions.ValidObjectID(tip) != nil:
		return "", false
	case base == "":
		return tip, true
	case actions.ValidObjectID(base) != nil:
		return "", false
	}
	return base + ".." + tip, true
}

// OutgoingNamesArgv lists, NUL-separated, every path any commit in
// base..tip touches under the home pathspec, not only the net diff;
// merges against their first parent, renames split into a deletion and an
// addition so both names are listed. --root lists a root commit's files
// whatever log.showRoot says (a whole-history range, or an unrelated
// history's second root, holds one). --full-history keeps every commit of
// the range: without it a path-limited log may simplify away the side of
// a merge that is TREESAME to its first parent (`merge -s ours`), whose
// commits a push still publishes. base and tip are as outgoingRange
// takes them.
func OutgoingNamesArgv(r Repo, base, tip string) []string {
	rng, ok := outgoingRange(base, tip)
	if !ok {
		return nil
	}
	return []string{"-C", r.Top, "log", "--format=", "--name-only", "-z", "--no-renames",
		"--root", "--full-history", "--diff-merges=first-parent", rng, "--", r.Pathspec}
}

// OutgoingPatchArgv is every commit's own patch in base..tip with no
// context: git's own bytes (no external diff, no textconv, no colour),
// fixed a/ b/ prefixes and no rename sections, so each file section's
// header names one path twice. Merges diff against their first parent,
// never a combined diff (AddedLines does not parse one); --root and
// --full-history as in OutgoingNamesArgv; base and tip as outgoingRange takes them.
func OutgoingPatchArgv(r Repo, base, tip string) []string {
	rng, ok := outgoingRange(base, tip)
	if !ok {
		return nil
	}
	argv := append([]string{"-C", r.Top, "log", "-p"}, plainPatch...)
	return append(argv, "-U0", "--no-renames", "--root", "--full-history", "--diff-merges=first-parent",
		"--src-prefix=a/", "--dst-prefix=b/", "--format=%H", rng, "--", r.Pathspec)
}

// OutgoingMessagesArgv prints the raw message of every commit in
// base..tip as "<sha> NUL <message> NUL", each record ended by a newline.
// It has no pathspec: a push publishes every commit of the range, and
// its message, whatever the commit touches. base and tip are as
// outgoingRange takes them.
func OutgoingMessagesArgv(r Repo, base, tip string) []string {
	rng, ok := outgoingRange(base, tip)
	if !ok {
		return nil
	}
	return []string{"-C", r.Top, "log", "--format=%H%x00%B%x00", rng, "--"}
}

// OutgoingCommitsArgv lists, one object id per line, every commit in
// base..tip (or, base "", tip's whole history): the same commits as
// OutgoingMessagesArgv, read on their own so the list stays complete
// whatever the messages' size. Like every git read it runs with
// GIT_NO_REPLACE_OBJECTS=1 (the git Program's environment), so it lists
// the commits a push sends, not their replacements. base and tip are as
// outgoingRange takes them.
func OutgoingCommitsArgv(r Repo, base, tip string) []string {
	rng, ok := outgoingRange(base, tip)
	if !ok {
		return nil
	}
	return []string{"-C", r.Top, "rev-list", rng, "--"}
}

// CommitObjectArgv prints the raw commit object sha, as a push sends it:
// its headers and its whole message, a NUL byte included (git log's %B
// stops at one).
func CommitObjectArgv(r Repo, sha string) []string {
	if actions.ValidObjectID(sha) != nil {
		return nil
	}
	return []string{"-C", r.Top, "cat-file", "commit", sha}
}

// BlobArgv prints the content of path (relative to the toplevel) as
// commit sha records it: the raw blob, no textconv or filter, so a push
// scan reads an outgoing commit's file the way the push publishes it.
// sha must be an object id and path a valid git path.
func BlobArgv(r Repo, sha, path string) []string {
	if actions.ValidObjectID(sha) != nil || actions.ValidGitPath(path) != nil {
		return nil
	}
	return []string{"-C", r.Top, "cat-file", "blob", sha + ":" + path}
}
