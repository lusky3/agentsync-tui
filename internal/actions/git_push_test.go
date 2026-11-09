package actions

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Branch names git itself accepts that ValidBranchName must accept too.
var goodBranches = []string{"main", "master", "feat/source-repo", "release-1.2", "a_b", "x.y", "v1", "üñí", "user/a.b/c"}

// Branch names ValidBranchName refuses: git check-ref-format rules for
// refs/heads/<name>, plus a leading '-' (an option), "@" and "HEAD".
var badBranches = []string{
	"", "-x", "--force", "@", "HEAD", "a..b", "a@{u}", "@{-1}", "a b", "a\tb", "a\nb", "a\x7fb",
	"a~1", "a^", "a:b", ":main", "a?", "a*", "a[b", `a\b`, "a/", "/a", "a//b", "a.", "a.lock",
	"a.lock/b", "a/.b", ".a", "+main", "a\u202eb", "\xff", strings.Repeat("a", 256),
}

func TestValidBranchName(t *testing.T) {
	for _, b := range goodBranches {
		if err := ValidBranchName(b); err != nil {
			t.Errorf("ValidBranchName(%q) = %v", b, err)
		}
	}
	for _, b := range badBranches {
		if err := ValidBranchName(b); err == nil {
			t.Errorf("ValidBranchName(%q) accepted", b)
		}
		if a, err := GitPush(testRepo, "origin", b, testSHA); err == nil {
			t.Errorf("GitPush accepted branch %q: %q", b, a.Argv)
		}
	}
}

func TestGitPushRefusesBadRemote(t *testing.T) {
	for _, r := range []string{"", "-x", "--receive-pack=x", "a b", "../r", "+origin", "https://h/r"} {
		if a, err := GitPush(testRepo, r, "main", testSHA); err == nil {
			t.Errorf("GitPush accepted remote %q: %q", r, a.Argv)
		}
	}
}

// Every name ValidBranchName accepts is a branch name real git accepts.
func TestValidBranchNamesAgreeWithGit(t *testing.T) {
	g := newHermeticGit(t)
	for _, b := range goodBranches {
		if out, err := g.run("", "check-ref-format", "refs/heads/"+b); err != nil {
			t.Errorf("git check-ref-format refuses %q: %v %s", b, err, out)
		}
	}
}

// hermeticGit runs real git with no user or system configuration.
type hermeticGit struct {
	t   *testing.T
	bin string
	env []string
}

// newHermeticGit returns a hermetic git, or skips the test when git is
// not installed.
func newHermeticGit(t *testing.T) hermeticGit {
	t.Helper()
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	home := t.TempDir()
	cfg := filepath.Join(home, "gitconfig")
	if err := os.WriteFile(cfg, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "GIT_CONFIG_GLOBAL=" + cfg, "GIT_CONFIG_NOSYSTEM=1", "LC_ALL=C"}
	return hermeticGit{t: t, bin: bin, env: env}
}

// run runs git with args in dir ("" = the test's directory).
func (g hermeticGit) run(dir string, args ...string) (string, error) {
	cmd := exec.Command(g.bin, args...)
	cmd.Dir, cmd.Env = dir, g.env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// must runs git and fails the test on error, returning trimmed output.
func (g hermeticGit) must(dir string, args ...string) string {
	g.t.Helper()
	out, err := g.run(dir, args...)
	if err != nil {
		g.t.Fatalf("git %q: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(out)
}

// commit writes name with content in dir and commits it.
func (g hermeticGit) commit(dir, name, content string) {
	g.t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		g.t.Fatal(err)
	}
	g.must(dir, "add", "--", name)
	g.must(dir, "commit", "-q", "-m", name+" "+content)
}

// pushFixture is a work repo on main with a bare origin that holds main
// and a branch "other" that the work repo has since rewritten (so a
// forced push of it would replace the remote's history).
type pushFixture struct {
	g            hermeticGit
	work, remote string
}

func newPushFixture(t *testing.T) pushFixture {
	g := newHermeticGit(t)
	dir := t.TempDir()
	f := pushFixture{g: g, work: filepath.Join(dir, "work"), remote: filepath.Join(dir, "remote.git")}
	g.must("", "init", "-q", "--bare", "-b", "main", f.remote)
	g.must("", "init", "-q", "-b", "main", f.work)
	g.must(f.work, "config", "user.name", "t")
	g.must(f.work, "config", "user.email", "t@example.invalid")
	g.must(f.work, "config", "commit.gpgSign", "false")
	g.commit(f.work, "f", "1")
	g.must(f.work, "remote", "add", "origin", f.remote)
	g.must(f.work, "push", "-q", "origin", "main")
	g.must(f.work, "checkout", "-q", "-b", "other")
	g.commit(f.work, "o", "1")
	g.must(f.work, "push", "-q", "origin", "other")
	g.must(f.work, "reset", "-q", "--hard", "main")
	g.commit(f.work, "o", "2") // other has diverged from origin/other
	g.must(f.work, "checkout", "-q", "main")
	return f
}

// remoteRef is the remote's value of ref ("" when absent).
func (f pushFixture) remoteRef(ref string) string {
	out, err := f.g.run("", "--git-dir="+f.remote, "rev-parse", "--verify", "-q", ref)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// push runs GitPush's argv with real git and returns its output and error.
func (f pushFixture) push(t *testing.T) (string, error) {
	t.Helper()
	top, err := filepath.EvalSymlinks(f.work)
	if err != nil {
		t.Fatal(err)
	}
	sha := f.g.must(f.work, "rev-parse", "--verify", "main^{commit}")
	a := mustAction(t)(GitPush(GitRepo{Top: top}, "origin", "main", sha))
	return f.g.run("", a.Argv...)
}

// D6 with Ruling 9: a remote.<name>.push refspec that forces every branch,
// and push.followTags, cannot widen GitPush: only main moves, by fast
// forward, and no other branch or tag is touched.
func TestGitPushIgnoresConfiguredRefspecsAndTags(t *testing.T) {
	f := newPushFixture(t)
	f.g.must(f.work, "config", "remote.origin.push", "+refs/heads/*:refs/heads/*")
	f.g.must(f.work, "config", "push.followTags", "true")
	f.g.must(f.work, "tag", "-a", "v1", "-m", "v1")
	f.g.commit(f.work, "f", "2")
	otherBefore := f.remoteRef("refs/heads/other")
	if out, err := f.push(t); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	if got, want := f.remoteRef("refs/heads/main"), f.g.must(f.work, "rev-parse", "main"); got != want {
		t.Errorf("remote main = %q, want %q", got, want)
	}
	if got := f.remoteRef("refs/heads/other"); got != otherBefore {
		t.Errorf("remote other moved from %s to %s: a configured forced refspec widened the push", otherBefore, got)
	}
	if got := f.remoteRef("refs/tags/v1"); got != "" {
		t.Errorf("tag v1 pushed (%s) despite --no-follow-tags", got)
	}
}

// A non-fast-forward is rejected, never forced, even when the configured
// refspec would force it.
func TestGitPushNonFastForwardRejected(t *testing.T) {
	f := newPushFixture(t)
	f.g.must(f.work, "config", "remote.origin.push", "+refs/heads/*:refs/heads/*")
	f.g.must(f.work, "commit", "-q", "--amend", "-m", "rewritten")
	before := f.remoteRef("refs/heads/main")
	out, err := f.push(t)
	if err == nil {
		t.Fatalf("a non-fast-forward push succeeded:\n%s", out)
	}
	if !strings.Contains(out, "non-fast-forward") {
		t.Errorf("output does not report a non-fast-forward:\n%s", out)
	}
	if got := f.remoteRef("refs/heads/main"); got != before {
		t.Errorf("remote main moved from %s to %s", before, got)
	}
}

// remote.<name>.mirror=true cannot turn GitPush into a mirror push: git
// refuses the explicit refspec and changes nothing. (The error names
// --mirror, which the user never typed, so the Source repo tab refuses a
// mirror remote up front; plan Task 9.)
func TestGitPushToMirrorRemoteChangesNothing(t *testing.T) {
	f := newPushFixture(t)
	f.g.must(f.work, "config", "remote.origin.mirror", "true")
	f.g.commit(f.work, "f", "2")
	main, other := f.remoteRef("refs/heads/main"), f.remoteRef("refs/heads/other")
	out, err := f.push(t)
	if err == nil {
		t.Fatalf("push to a mirror remote succeeded:\n%s", out)
	}
	if f.remoteRef("refs/heads/main") != main || f.remoteRef("refs/heads/other") != other {
		t.Errorf("a mirror remote's refs changed:\n%s", out)
	}
}

// push.recurseSubmodules=on-demand would make a push also push a
// submodule's commits to the submodule's own remote, and =check would gate
// the push on other repositories. --no-recurse-submodules keeps GitPush to
// the one branch of the one repository: the superproject's main moves and
// the submodule's remote is untouched.
func TestGitPushNeverRecursesIntoSubmodules(t *testing.T) {
	for _, mode := range []string{"on-demand", "check"} {
		t.Run(mode, func(t *testing.T) {
			g := newHermeticGit(t)
			g.must("", "config", "--global", "protocol.file.allow", "always") // local submodule URL
			g.must("", "config", "--global", "user.name", "t")
			g.must("", "config", "--global", "user.email", "t@example.invalid")
			dir := t.TempDir()
			subRemote, superRemote := filepath.Join(dir, "sub.git"), filepath.Join(dir, "super.git")
			work := filepath.Join(dir, "work")
			g.must("", "init", "-q", "--bare", "-b", "main", subRemote)
			g.must("", "init", "-q", "--bare", "-b", "main", superRemote)
			seed := filepath.Join(dir, "seed")
			g.must("", "init", "-q", "-b", "main", seed)
			g.commit(seed, "s", "1")
			g.must(seed, "push", "-q", subRemote, "main")
			g.must("", "init", "-q", "-b", "main", work)
			g.must(work, "submodule", "add", "-q", subRemote, "sub")
			g.must(work, "commit", "-q", "-m", "add sub")
			g.must(work, "remote", "add", "origin", superRemote)
			g.must(work, "push", "-q", "origin", "main")
			g.commit(filepath.Join(work, "sub"), "s", "2") // unpushed submodule commit
			g.must(work, "add", "sub")
			g.must(work, "commit", "-q", "-m", "bump sub")
			g.must(work, "config", "push.recurseSubmodules", mode)
			subBefore := g.must("", "--git-dir="+subRemote, "rev-parse", "main")
			top, err := filepath.EvalSymlinks(work)
			if err != nil {
				t.Fatal(err)
			}
			a := mustAction(t)(GitPush(GitRepo{Top: top}, "origin", "main", g.must(work, "rev-parse", "main")))
			if out, err := g.run("", a.Argv...); err != nil {
				t.Fatalf("push: %v\n%s", err, out)
			}
			if got := g.must("", "--git-dir="+subRemote, "rev-parse", "main"); got != subBefore {
				t.Errorf("the submodule's remote moved from %s to %s", subBefore, got)
			}
			if got, want := g.must("", "--git-dir="+superRemote, "rev-parse", "main"), g.must(work, "rev-parse", "main"); got != want {
				t.Errorf("superproject remote main = %s, want %s", got, want)
			}
		})
	}
}

func TestValidObjectID(t *testing.T) {
	for _, s := range []string{testSHA, strings.Repeat("f", 40), strings.Repeat("0", 64)} {
		if err := ValidObjectID(s); err != nil {
			t.Errorf("ValidObjectID(%q) = %v", s, err)
		}
	}
	for _, s := range []string{"", "HEAD", "main", strings.ToUpper(testSHA), testSHA[:39], testSHA + "0",
		strings.Repeat("a", 63), strings.Repeat("a", 65), "+" + testSHA[1:], "-" + testSHA[1:], testSHA[:39] + "g",
		"HEAD^{commit}", "@{u}", testSHA[:20] + ":" + testSHA[:19]} {
		if err := ValidObjectID(s); err == nil {
			t.Errorf("ValidObjectID(%q) accepted", s)
		}
		if a, err := GitPush(testRepo, "origin", "main", s); err == nil {
			t.Errorf("GitPush accepted object id %q: %q", s, a.Argv)
		}
	}
}

// The push sends exactly the commit it was given, whatever the branch
// points at by the time it runs: a commit made after the guard scanned
// never reaches the remote.
func TestGitPushSendsTheGivenCommitOnly(t *testing.T) {
	f := newPushFixture(t)
	f.g.commit(f.work, "f", "2")
	scanned := f.g.must(f.work, "rev-parse", "main")
	top, err := filepath.EvalSymlinks(f.work)
	if err != nil {
		t.Fatal(err)
	}
	a := mustAction(t)(GitPush(GitRepo{Top: top}, "origin", "main", scanned))
	f.g.commit(f.work, "f", "3") // made after the action was built
	if out, err := f.g.run("", a.Argv...); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	if got := f.remoteRef("refs/heads/main"); got != scanned {
		t.Errorf("remote main = %s, want the scanned %s", got, scanned)
	}
	// git updates the remote-tracking ref through the fetch refspec.
	if got := f.g.must(f.work, "rev-parse", "origin/main"); got != scanned {
		t.Errorf("origin/main = %s, want %s", got, scanned)
	}
}

// A ref whose name is the scanned commit's object id (refs/heads/<sha>,
// which git can be made to create) wins over the object id when git
// resolves a bare 40-hex push source; GitPush peels the source
// (<sha>^{commit}) so it is always the object itself.
func TestGitPushSourceIsTheObjectNotARefNamedLikeIt(t *testing.T) {
	f := newPushFixture(t)
	f.g.commit(f.work, "f", "2")
	scanned := f.g.must(f.work, "rev-parse", "main")
	f.g.commit(f.work, "f", "3")
	later := f.g.must(f.work, "rev-parse", "main")
	f.g.must(f.work, "update-ref", "refs/heads/"+scanned, later)
	top, err := filepath.EvalSymlinks(f.work)
	if err != nil {
		t.Fatal(err)
	}
	a := mustAction(t)(GitPush(GitRepo{Top: top}, "origin", "main", scanned))
	if out, err := f.g.run("", a.Argv...); err != nil {
		t.Fatalf("push: %v\n%s", err, out)
	}
	if got := f.remoteRef("refs/heads/main"); got != scanned {
		t.Errorf("remote main = %s, want the scanned %s (not %s)", got, scanned, later)
	}
	if got := f.g.must(f.work, "rev-parse", "refs/remotes/origin/main"); got != scanned {
		t.Errorf("origin/main = %s, want %s", got, scanned)
	}
}
