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

// HeadCommitArgv prints the object id of the commit HEAD names.
func HeadCommitArgv(r Repo) []string {
	return []string{"-C", r.Top, "rev-parse", "--verify", "HEAD^{commit}"}
}

// UpstreamCommitArgv prints the object id of the commit the current
// branch's upstream (its remote-tracking ref) names.
func UpstreamCommitArgv(r Repo) []string {
	return []string{"-C", r.Top, "rev-parse", "--verify", "@{u}^{commit}"}
}

// OutgoingNamesArgv lists, NUL-separated, every path any commit in
// base..tip touches under the home pathspec, not only the net diff;
// merges against their first parent, renames split into a deletion and an
// addition so both names are listed. base and tip must be object ids.
func OutgoingNamesArgv(r Repo, base, tip string) []string {
	if actions.ValidObjectID(base) != nil || actions.ValidObjectID(tip) != nil {
		return nil
	}
	return []string{"-C", r.Top, "log", "--format=", "--name-only", "-z", "--no-renames",
		"--diff-merges=first-parent", base + ".." + tip, "--", r.Pathspec}
}

// OutgoingPatchArgv is every commit's own patch in base..tip with no
// context: git's own bytes (no external diff, no textconv, no colour),
// fixed a/ b/ prefixes and no rename sections, so each file section's
// header names one path twice. Merges diff against their first parent,
// never a combined diff (AddedLines does not parse one). base and tip
// must be object ids.
func OutgoingPatchArgv(r Repo, base, tip string) []string {
	if actions.ValidObjectID(base) != nil || actions.ValidObjectID(tip) != nil {
		return nil
	}
	return []string{"-C", r.Top, "log", "-p", "--no-ext-diff", "--no-textconv", "--no-color", "-U0",
		"--no-renames", "--diff-merges=first-parent", "--src-prefix=a/", "--dst-prefix=b/",
		"--format=%H", base + ".." + tip, "--", r.Pathspec}
}
