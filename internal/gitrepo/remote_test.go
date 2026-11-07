package gitrepo

import (
	"slices"
	"strings"
	"testing"
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
		{"head", HeadCommitArgv(r), []string{"-C", "/h/top", "rev-parse", "--verify", "HEAD^{commit}"}},
		{"upstream commit", UpstreamCommitArgv(r), []string{"-C", "/h/top", "rev-parse", "--verify", "@{u}^{commit}"}},
		{"names", OutgoingNamesArgv(r, a, b), []string{"-C", "/h/top", "log", "--format=", "--name-only", "-z",
			"--no-renames", "--diff-merges=first-parent", a + ".." + b, "--", "agentsync"}},
		{"patch", OutgoingPatchArgv(r, a, b), []string{"-C", "/h/top", "log", "-p", "--no-ext-diff", "--no-textconv",
			"--no-color", "-U0", "--no-renames", "--diff-merges=first-parent", "--src-prefix=a/", "--dst-prefix=b/",
			"--format=%H", a + ".." + b, "--", "agentsync"}},
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
	} {
		if argv != nil {
			t.Errorf("%s: built %q", name, argv)
		}
	}
}
