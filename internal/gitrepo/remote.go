package gitrepo

import "github.com/lusky3/agentsync-tui/internal/actions"

// The read argv of the Source repo tab's network keys (fetch, pull, push).
// Each starts `-C <top>`; the git Program adds the fixed hardening. None
// of them writes or touches the network. A function returns nil when an
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

// OutgoingNamesArgv lists, NUL-separated, every path any commit in
// base..tip touches under the home pathspec, not only the net diff;
// merges against their first parent, renames split into a deletion and an
// addition so both names are listed. --full-history keeps every commit of
// the range: without it a path-limited log may simplify away the side of
// a merge that is TREESAME to its first parent (`merge -s ours`), whose
// commits a push still publishes. base and tip must be object ids.
func OutgoingNamesArgv(r Repo, base, tip string) []string {
	if actions.ValidObjectID(base) != nil || actions.ValidObjectID(tip) != nil {
		return nil
	}
	return []string{"-C", r.Top, "log", "--format=", "--name-only", "-z", "--no-renames",
		"--full-history", "--diff-merges=first-parent", base + ".." + tip, "--", r.Pathspec}
}

// OutgoingPatchArgv is every commit's own patch in base..tip with no
// context: git's own bytes (no external diff, no textconv, no colour),
// fixed a/ b/ prefixes and no rename sections, so each file section's
// header names one path twice. Merges diff against their first parent,
// never a combined diff (AddedLines does not parse one); --full-history
// as in OutgoingNamesArgv. base and tip must be object ids.
func OutgoingPatchArgv(r Repo, base, tip string) []string {
	if actions.ValidObjectID(base) != nil || actions.ValidObjectID(tip) != nil {
		return nil
	}
	return []string{"-C", r.Top, "log", "-p", "--no-ext-diff", "--no-textconv", "--no-color", "-U0",
		"--no-renames", "--full-history", "--diff-merges=first-parent", "--src-prefix=a/", "--dst-prefix=b/",
		"--format=%H", base + ".." + tip, "--", r.Pathspec}
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
