package gitrepo

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lusky3/agentsync-tui/internal/gitrepo/gittest"
)

func TestSourceRepoReadArgvPinned(t *testing.T) {
	r := Repo{Top: "/h/top", Pathspec: "agentsync"}
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 64)
	cases := []struct {
		name string
		got  []string
		want []string
	}{
		{"upstream", UpstreamArgv(r, "main"), []string{"-C", "/h/top", "for-each-ref",
			"--format=%(refname)%00%(upstream:remotename)%00%(upstream:remoteref)", "refs/heads/main"}},
		{"mirror", MirrorArgv(r, "origin"), []string{"-C", "/h/top", "config", "--get", "--type=bool", "remote.origin.mirror"}},
		{"push urls", PushURLsArgv(r, "origin"), []string{"-C", "/h/top", "remote", "get-url", "--push", "--all", "origin"}},
		{"branch commit", BranchCommitArgv(r, "main"), []string{"-C", "/h/top", "rev-parse", "--verify", "refs/heads/main^{commit}"}},
		{"upstream commit", BranchUpstreamCommitArgv(r, "main"), []string{"-C", "/h/top", "rev-parse", "--verify", "main@{upstream}^{commit}"}},
		{"commits after", CommitsAfterArgv(r, a, "main"), []string{"-C", "/h/top", "rev-list", "--count", a + "..refs/heads/main", "--"}},
		{"names", OutgoingNamesArgv(r, a, b), []string{"-C", "/h/top", "log", "--format=", "--name-only", "-z",
			"--no-renames", "--full-history", "--diff-merges=first-parent", a + ".." + b, "--", "agentsync"}},
		{"patch", OutgoingPatchArgv(r, a, b), []string{"-C", "/h/top", "log", "-p", "--no-ext-diff", "--no-textconv",
			"--no-color", "-U0", "--no-renames", "--full-history", "--diff-merges=first-parent", "--src-prefix=a/", "--dst-prefix=b/",
			"--format=%H", a + ".." + b, "--", "agentsync"}},
		{"blob", BlobArgv(r, a, "mcp/x.toml"), []string{"-C", "/h/top", "cat-file", "blob", a + ":mcp/x.toml"}},
	}
	for _, c := range cases {
		if !slices.Equal(c.got, c.want) {
			t.Errorf("%s argv\n got %q\nwant %q", c.name, c.got, c.want)
		}
	}
}

func TestSourceRepoReadArgvRefusesBadArguments(t *testing.T) {
	r := Repo{Top: "/h/top", Pathspec: "."}
	sha := strings.Repeat("a", 40)
	for name, argv := range map[string][]string{
		"upstream no head":   UpstreamArgv(r, ""),
		"mirror option":      MirrorArgv(r, "--x"),
		"mirror local":       MirrorArgv(r, "."),
		"push urls option":   PushURLsArgv(r, "-x"),
		"names bad base":     OutgoingNamesArgv(r, "@{u}", sha),
		"names bad tip":      OutgoingNamesArgv(r, sha, "HEAD"),
		"patch bad base":     OutgoingPatchArgv(r, "--all", sha),
		"patch short tip":    OutgoingPatchArgv(r, sha, sha[:39]),
		"patch upper tip":    OutgoingPatchArgv(r, sha, strings.ToUpper(sha)),
		"names glued option": OutgoingNamesArgv(r, sha+"..x", sha),
		"branch commit bad":  BranchCommitArgv(r, "-x"),
		"branch commit HEAD": BranchCommitArgv(r, "HEAD"),
		"upstream bad":       BranchUpstreamCommitArgv(r, "a@{u}"),
		"upstream empty":     BranchUpstreamCommitArgv(r, ""),
		"after bad sha":      CommitsAfterArgv(r, "HEAD", "main"),
		"after bad branch":   CommitsAfterArgv(r, sha, "a..b"),
		"blob bad sha":       BlobArgv(r, "HEAD", "x"),
		"blob bad path":      BlobArgv(r, sha, "../x"),
		"blob empty path":    BlobArgv(r, sha, ""),
	} {
		if argv != nil {
			t.Errorf("%s: built %q", name, argv)
		}
	}
}

// A `merge -s ours` is TREESAME to its first parent, so git's history
// simplification may drop the side branch from a path-limited log; the
// push publishes it all the same. Both outgoing reads list every commit
// in base..tip: the side commit that added the token, and the one that
// deleted it again.
func TestOutgoingReadsListOursMergedSideCommits(t *testing.T) {
	top := filepath.Join(t.TempDir(), "w")
	gittest.Init(t, top)
	gittest.Commit(t, top, "base", map[string]string{"readme": "a\n"})
	base := strings.TrimSpace(gittest.Git(t, top, "rev-parse", "HEAD"))
	gittest.Git(t, top, "checkout", "-q", "-b", "side")
	gittest.Commit(t, top, "side add", map[string]string{"mcp/x.toml": "token = \"ghp_" + strings.Repeat("a", 36) + "\"\n"})
	sideAdd := strings.TrimSpace(gittest.Git(t, top, "rev-parse", "HEAD"))
	gittest.Git(t, top, "rm", "-q", "mcp/x.toml")
	gittest.Git(t, top, "commit", "-q", "-m", "side del")
	gittest.Git(t, top, "checkout", "-q", "main")
	gittest.Commit(t, top, "main", map[string]string{"readme": "a\nb\n"})
	gittest.Git(t, top, "merge", "-q", "-s", "ours", "--no-edit", "side")
	tip := strings.TrimSpace(gittest.Git(t, top, "rev-parse", "HEAD"))
	r := Repo{Top: top, Pathspec: "."}
	git := gittest.NewGit(t)
	names, err := git.Run(context.Background(), OutgoingNamesArgv(r, base, tip)...)
	if err != nil || names.ExitCode != 0 {
		t.Fatalf("names: %+v %v", names, err)
	}
	if !slices.Contains(strings.Split(names.Stdout, "\x00"), "mcp/x.toml") {
		t.Errorf("names read lacks the side branch's mcp/x.toml: %q", names.Stdout)
	}
	patch, err := git.Run(context.Background(), OutgoingPatchArgv(r, base, tip)...)
	if err != nil || patch.ExitCode != 0 {
		t.Fatalf("patch: %+v %v", patch, err)
	}
	if !strings.Contains(patch.Stdout, sideAdd+"\n") || !strings.Contains(patch.Stdout, "+token = ") {
		t.Errorf("patch read lacks the side commit %s:\n%s", sideAdd, patch.Stdout)
	}
}
