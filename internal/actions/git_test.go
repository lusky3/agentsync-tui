package actions

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

const testTop = "/home/u/.agentsync"

var testRepo = GitRepo{Top: testTop}

// testSHA is a commit id the push builder accepts.
const testSHA = "0123456789abcdef0123456789abcdef01234567"

// optionRegion is the part of a git argv that git parses as options and
// subcommand: everything before the "--" separator.
func optionRegion(argv []string) []string {
	if i := slices.Index(argv, "--"); i >= 0 {
		return argv[:i]
	}
	return argv
}

func mustAction(t *testing.T) func(Action, error) Action {
	return func(a Action, err error) Action {
		t.Helper()
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return a
	}
}

func TestGitAddArgvAndDashPaths(t *testing.T) {
	paths := []string{"-rf", "--exec=x", "skills/a/SKILL.md"}
	a := mustAction(t)(GitAdd(testRepo, paths))
	want := []string{"-C", testTop, "add", "--", "-rf", "--exec=x", "skills/a/SKILL.md"}
	if !slices.Equal(a.Argv, want) {
		t.Fatalf("argv = %q, want %q", a.Argv, want)
	}
	if a.Program != ProgramGit {
		t.Errorf("Program = %q, want %q", a.Program, ProgramGit)
	}
	if a.ReadOnly || a.Network || a.Destructive {
		t.Errorf("add flags = %+v", a)
	}
}

func TestGitCommitArgv(t *testing.T) {
	a := mustAction(t)(GitCommit(testRepo, "feat: x", []string{"agentsync.toml"}))
	want := []string{"-C", testTop, "commit", "--message=feat: x", "--", "agentsync.toml"}
	if !slices.Equal(a.Argv, want) {
		t.Fatalf("argv = %q, want %q", a.Argv, want)
	}
}

func TestGitCommitMessageIsOneElement(t *testing.T) {
	for _, msg := range []string{"--amend", "-a", "x --no-verify"} {
		t.Run(msg, func(t *testing.T) {
			a := mustAction(t)(GitCommit(testRepo, msg, []string{"agentsync.toml"}))
			checkMessageIsOneElement(t, msg, a.Argv)
		})
	}
}

// checkMessageIsOneElement checks that argv carries msg as exactly one
// "--message=" element and that no option-looking word of msg became an
// element of its own.
func checkMessageIsOneElement(t *testing.T, msg string, argv []string) {
	t.Helper()
	n := 0
	for _, el := range argv {
		if strings.HasPrefix(el, "--message=") {
			n++
			if el != "--message="+msg {
				t.Errorf("message element = %q", el)
			}
		}
		if el == "--amend" || el == "-a" || el == "--no-verify" {
			t.Errorf("message %q produced its own element %q", msg, el)
		}
	}
	if n != 1 {
		t.Errorf("message %q: %d --message= elements", msg, n)
	}
}

func TestCommitMessageRefusals(t *testing.T) {
	for name, msg := range map[string]string{
		"empty":       "",
		"blank":       "   ",
		"newline":     "a\nb",
		"cr":          "a\rb",
		"tab":         "a\tb",
		"esc":         "a\x1b[31mb",
		"bidi":        "a\u202eb",
		"zero-width":  "a\u200bb",
		"201 runes":   strings.Repeat("é", 201),
		"ghp token":   "add ghp_abcdefghijklmnop",
		"slack xoxs":  "use xoxs-12345678901234",
		"token-shape": "rotate x1f3a9c2d7e8b1f0",
		"auth header": "Authorization: Basic dXNlcjpwYXNz",
		"invalid utf": "a\xffb",
	} {
		if err := ValidCommitMessage(msg); err == nil {
			t.Errorf("%s: ValidCommitMessage(%q) accepted", name, msg)
		}
		if _, err := GitCommit(testRepo, msg, []string{"a"}); err == nil {
			t.Errorf("%s: GitCommit accepted %q", name, msg)
		}
	}
	if err := ValidCommitMessage(strings.Repeat("é", 200)); err != nil {
		t.Errorf("200 runes refused: %v", err)
	}
}

func TestCommitMessageIsTrimmed(t *testing.T) {
	a := mustAction(t)(GitCommit(testRepo, "  feat: x  ", []string{"a"}))
	if !slices.Contains(a.Argv, "--message=feat: x") {
		t.Errorf("argv = %q", a.Argv)
	}
}

func TestGitPathRefusals(t *testing.T) {
	for _, p := range []string{"../x", "/abs", "a/../../b", "a\nb", "a\tb", "\xff", "a\u202eb", "a\u200bb", "", "a\x00b", ".", "a/..", strings.Repeat("a", 4097),
		".state", ".state/backup/x", ".agentsync/.state/x", "a/.state", "./.state/x", ":(glob)x", ":/x", ":!x", ":"} {
		if err := ValidGitPath(p); err == nil {
			t.Errorf("ValidGitPath(%q) accepted", p)
		}
		if _, err := GitAdd(testRepo, []string{"ok", p}); err == nil {
			t.Errorf("GitAdd accepted %q", p)
		}
		if _, err := GitCommit(testRepo, "m", []string{p}); err == nil {
			t.Errorf("GitCommit accepted %q", p)
		}
	}
	if _, err := GitAdd(testRepo, nil); err == nil {
		t.Error("GitAdd accepted no paths")
	}
	if _, err := GitCommit(testRepo, "m", nil); err == nil {
		t.Error("GitCommit accepted no paths")
	}
	many := make([]string, 5001)
	for i := range many {
		many[i] = fmt.Sprintf("f%d", i)
	}
	if _, err := GitAdd(testRepo, many); err == nil {
		t.Error("GitAdd accepted 5001 paths")
	}
	if _, err := GitAdd(testRepo, many[:5000]); err != nil {
		t.Errorf("GitAdd refused 5000 paths: %v", err)
	}
	if err := ValidGitPath(strings.Repeat("a", 4096)); err != nil {
		t.Errorf("4096-byte path refused: %v", err)
	}
}

func TestGlobCharsAllowedInPaths(t *testing.T) {
	for _, p := range []string{"a*b?[c]", " leading space", `quote"and\back`, "skills/ünï/SKILL.md"} {
		if err := ValidGitPath(p); err != nil {
			t.Errorf("ValidGitPath(%q) = %v", p, err)
		}
	}
	a := mustAction(t)(GitAdd(testRepo, []string{"a*b?[c]"}))
	if a.Argv[len(a.Argv)-1] != "a*b?[c]" {
		t.Errorf("argv = %q", a.Argv)
	}
}

func TestBadTopRefused(t *testing.T) {
	for _, top := range []string{"", "rel/dir", "/a/../b", "/a/", "/a//b"} {
		r := GitRepo{Top: top}
		checks := map[string]error{}
		_, checks["add"] = GitAdd(r, []string{"a"})
		_, checks["commit"] = GitCommit(r, "m", []string{"a"})
		_, checks["fetch"] = GitFetch(r, "origin")
		_, checks["pull"] = GitPull(r)
		_, checks["push"] = GitPush(r, "origin", "main", testSHA)
		for name, err := range checks {
			if err == nil {
				t.Errorf("%s accepted top %q", name, top)
			}
		}
	}
}

func TestGitFetch(t *testing.T) {
	a := mustAction(t)(GitFetch(testRepo, "origin"))
	if want := []string{"-C", testTop, "fetch", "origin"}; !slices.Equal(a.Argv, want) {
		t.Fatalf("argv = %q, want %q", a.Argv, want)
	}
	if !a.ReadOnly || !a.Network {
		t.Errorf("fetch flags: ReadOnly=%v Network=%v", a.ReadOnly, a.Network)
	}
	for _, ok := range []string{"origin", "up.stream", "team/fork", "a_b-c"} {
		if err := ValidRemoteName(ok); err != nil {
			t.Errorf("ValidRemoteName(%q) = %v", ok, err)
		}
	}
}

func TestFetchRemoteRefusals(t *testing.T) {
	for _, r := range []string{"--receive-pack=x", "-x", "a b", "../r", "a..b", "", ".hidden", strings.Repeat("a", 101), "https://h/r", "a\nb"} {
		if err := ValidRemoteName(r); err == nil {
			t.Errorf("ValidRemoteName(%q) accepted", r)
		}
		if _, err := GitFetch(testRepo, r); err == nil {
			t.Errorf("GitFetch accepted %q", r)
		}
	}
}

func TestPullAndPushArgvPinned(t *testing.T) {
	pull := mustAction(t)(GitPull(testRepo))
	if want := []string{"-C", testTop, "pull", "--ff-only", "--no-rebase", "--no-autostash"}; !slices.Equal(pull.Argv, want) {
		t.Errorf("pull argv = %q, want %q", pull.Argv, want)
	}
	if !pull.Network || pull.ReadOnly {
		t.Errorf("pull flags = %+v", pull)
	}
	push := mustAction(t)(GitPush(testRepo, "origin", "main", testSHA))
	want := []string{"-C", testTop, "push", "--no-follow-tags", "--no-recurse-submodules", "origin", testSHA + ":refs/heads/main"}
	if !slices.Equal(push.Argv, want) {
		t.Errorf("push argv = %q, want %q", push.Argv, want)
	}
	if !push.Network || push.ReadOnly {
		t.Errorf("push flags = %+v", push)
	}
}

func TestCloneURLTable(t *testing.T) {
	for _, u := range []string{
		"https://github.com/o/r.git",
		"ssh://git@github.com/o/r.git",
		"ssh://git@example.com:2222/o/r.git",
		"git@github.com:o/r.git",
		"host:r.git",
	} {
		if err := ValidCloneURL(u); err != nil {
			t.Errorf("ValidCloneURL(%q) = %v", u, err)
		}
	}
	for _, u := range []string{
		"ext::sh -c x",
		"ext::sh",
		"fd::0",
		"file:///x",
		"FILE:///x",
		"http://h/r",
		"-uX",
		"https://tok@github.com/o/r",
		"https://u:p@h/r",
		"ssh://u:p@h/r",
		"ssh://-oProxyCommand=x/r",
		"git@host:-x",
		"git@host:/abs",
		"a b",
		"x --upload-pack=y",
		"x--upload-pack=y",
		"https://h/r--config=x",
		"",
		"https://h",
		"https://h/",
		"git+ssh://h/r",
		"a/b:c",
		"host:",
		"https://h/r\x00",
		"https://h/r\x1b",
		"https://h/" + strings.Repeat("a", 2048),
	} {
		if err := ValidCloneURL(u); err == nil {
			t.Errorf("ValidCloneURL(%q) accepted", u)
		}
		if _, err := InitFromURL(u); err == nil {
			t.Errorf("InitFromURL accepted %q", u)
		}
	}
}

func TestInitFromURL(t *testing.T) {
	a := mustAction(t)(InitFromURL("git@github.com:o/r.git"))
	if want := []string{"init", "git@github.com:o/r.git"}; !slices.Equal(a.Argv, want) {
		t.Errorf("argv = %q", a.Argv)
	}
	if a.Program != "" || !a.Network || a.Title != "Clone agentsync home" {
		t.Errorf("init action = %+v", a)
	}
	if !strings.HasPrefix(a.Display(), "agentsync init ") {
		t.Errorf("Display = %q", a.Display())
	}
}

func TestGitDisplayStartsWithGitAndIsMasked(t *testing.T) {
	if _, err := GitCommit(testRepo, "rotate ghp_abcdefghijklmnop", []string{"a"}); err == nil {
		t.Error("a token-shaped commit message built an action")
	}
	p := "skills/ghp_x/SKILL.md"
	for _, a := range []Action{
		mustAction(t)(GitAdd(testRepo, []string{p})),
		mustAction(t)(GitCommit(testRepo, "docs: x", []string{p})),
	} {
		d := a.Display()
		if !strings.HasPrefix(d, "git -C "+testTop+" ") {
			t.Errorf("Display = %q", d)
		}
		if strings.Contains(d, "ghp_x") || strings.Contains(d, "--no-pager") {
			t.Errorf("Display leaked: %q", d)
		}
		if a.Argv[len(a.Argv)-1] != p {
			t.Errorf("argv path changed: %q", a.Argv)
		}
	}
	for _, a := range []Action{mustAction(t)(GitPull(testRepo)), mustAction(t)(GitPush(testRepo, "origin", "main", testSHA)), mustAction(t)(GitFetch(testRepo, "origin"))} {
		if !strings.HasPrefix(a.Display(), "git -C "+testTop+" ") {
			t.Errorf("Display = %q", a.Display())
		}
	}
}

func TestDisplayAbbreviatesManyPaths(t *testing.T) {
	paths := make([]string, 15)
	for i := range paths {
		paths[i] = fmt.Sprintf("memory/f%02d.md", i)
	}
	for _, a := range []Action{
		mustAction(t)(GitAdd(testRepo, paths)),
		mustAction(t)(GitCommit(testRepo, "m", paths)),
	} {
		d := a.Display()
		if !strings.Contains(d, "memory/f09.md") || strings.Contains(d, "memory/f10.md") {
			t.Errorf("Display does not list exactly 10 paths: %q", d)
		}
		if !strings.HasSuffix(d, " … (+5 more)") {
			t.Errorf("Display lacks the remainder: %q", d)
		}
		if got := a.Argv[len(a.Argv)-15:]; !slices.Equal(got, paths) {
			t.Errorf("argv lost paths: %q", a.Argv)
		}
	}
	a := mustAction(t)(GitAdd(testRepo, paths[:10]))
	if strings.Contains(a.Display(), "more") {
		t.Errorf("10 paths abbreviated: %q", a.Display())
	}
}

func TestConfirmVerbForGit(t *testing.T) {
	cases := []struct {
		a    Action
		want string
	}{
		{mustAction(t)(GitCommit(testRepo, "m", []string{"a"})), "commit"},
		{mustAction(t)(GitPush(testRepo, "origin", "main", testSHA)), "push"},
		{mustAction(t)(GitPull(testRepo)), "pull"},
		{mustAction(t)(GitAdd(testRepo, []string{"a"})), "add"},
		{mustAction(t)(GitFetch(testRepo, "origin")), "fetch"},
		{mustAction(t)(InitFromURL("host:r.git")), "init"},
	}
	for _, c := range cases {
		if got := c.a.ConfirmVerb(); got != c.want {
			t.Errorf("ConfirmVerb(%q) = %q, want %q", c.a.Argv, got, c.want)
		}
	}
}

// forbidden are the options no git builder may ever emit (D11).
var forbidden = []string{
	"--force", "-f", "--force-with-lease", "--no-verify", "-n", "-a", "--all", "-A",
	"--amend", "--mirror", "--tags", "--delete", "-d", "--upload-pack", "--receive-pack", "--exec",
}

func TestGitBuildersNeverEmitForbidden(t *testing.T) {
	hostile := []string{
		"--force", "-f", "--force-with-lease", "--no-verify", "-n", "-a", "--all", "-A", "--amend",
		"--mirror", "--tags", "--delete", "-d", "--upload-pack=x", "--receive-pack=x", "--exec=x",
		"-rf", "--output=/tmp/x", "origin", "x --no-verify", "a b", "ext::sh -c x", "-uX",
		// Refspec shapes a remote or branch argument must never smuggle in:
		// a forced update, a wildcard, a deletion and a src:dst pair.
		"+main", "+refs/heads/*:refs/heads/*", "refs/heads/*", ":main", "main:other", "@{u}", "main..x",
	}
	// Paths no path-taking builder may accept: the ignored .state/ (pre-apply
	// backups holding live secrets) at any depth, and git pathspec magic.
	refusedPaths := []string{".state/backup/x", ".agentsync/.state/x", ":(glob)x", ":(top)a", ":!x"}
	t.Run("refused paths", func(t *testing.T) {
		for _, p := range refusedPaths {
			checkPathRefused(t, p)
		}
	})
	hostile = append(hostile, refusedPaths...)
	var built []Action
	for _, h := range hostile {
		built = append(built, buildHostile(h)...)
	}
	built = append(built, mustAction(t)(GitPull(testRepo)), mustAction(t)(GitPush(testRepo, "origin", "main", testSHA)))
	if len(built) < 50 {
		t.Fatalf("only %d hostile actions built; the table is not exercising the builders", len(built))
	}
	for _, a := range built {
		if a.Program == "" {
			checkAgentsyncInit(t, a)
			continue
		}
		checkGitArgvSafe(t, a)
	}
}

// checkPathRefused checks that every path-taking git builder refuses p,
// alone and among accepted paths.
func checkPathRefused(t *testing.T, p string) {
	t.Helper()
	for name, b := range map[string]func() (Action, error){
		"GitAdd":       func() (Action, error) { return GitAdd(testRepo, []string{p}) },
		"GitAdd mixed": func() (Action, error) { return GitAdd(testRepo, []string{"ok", p}) },
		"GitCommit":    func() (Action, error) { return GitCommit(testRepo, "m", []string{p}) },
	} {
		if a, err := b(); err == nil {
			t.Errorf("%s accepted %q: %q", name, p, a.Argv)
		}
	}
}

// buildHostile feeds h to every builder in every argument position it
// takes and returns the actions the builders accepted.
func buildHostile(h string) []Action {
	var built []Action
	for _, b := range []func() (Action, error){
		func() (Action, error) { return GitAdd(testRepo, []string{h}) },
		func() (Action, error) { return GitAdd(testRepo, []string{"ok", h, "x"}) },
		func() (Action, error) { return GitCommit(testRepo, h, []string{"a"}) },
		func() (Action, error) { return GitCommit(testRepo, "m", []string{h}) },
		func() (Action, error) { return GitCommit(testRepo, h, []string{h}) },
		func() (Action, error) { return GitFetch(testRepo, h) },
		func() (Action, error) { return GitPull(GitRepo{Top: h}) },
		func() (Action, error) { return GitPush(GitRepo{Top: h}, "origin", "main", testSHA) },
		func() (Action, error) { return GitPush(testRepo, h, "main", testSHA) },
		func() (Action, error) { return GitPush(testRepo, "origin", h, testSHA) },
		func() (Action, error) { return GitPush(testRepo, "origin", "main", h) },
		func() (Action, error) { return GitAdd(GitRepo{Top: h}, []string{"a"}) },
		func() (Action, error) { return InitFromURL(h) },
	} {
		if a, err := b(); err == nil {
			built = append(built, a)
		}
	}
	return built
}

// checkAgentsyncInit checks an agentsync action a hostile value built: it
// must be "init <url>" with a url that cannot be read as an option.
func checkAgentsyncInit(t *testing.T, a Action) {
	t.Helper()
	if len(a.Argv) != 2 || a.Argv[0] != "init" || strings.HasPrefix(a.Argv[1], "-") {
		t.Errorf("agentsync builder emitted %q", a.Argv)
	}
}

// checkGitArgvSafe checks a git action a hostile value built: it starts
// -C <top>, runs an allowed subcommand and has no forbidden option before
// the "--" separator.
func checkGitArgvSafe(t *testing.T, a Action) {
	t.Helper()
	allowed := []string{"add", "commit", "fetch", "pull", "push"}
	if len(a.Argv) < 3 || a.Argv[0] != "-C" || a.Argv[1] != testTop {
		t.Errorf("git argv does not start -C <top>: %q", a.Argv)
	}
	if v := a.ConfirmVerb(); !slices.Contains(allowed, v) {
		t.Errorf("subcommand %q outside %q: %q", v, allowed, a.Argv)
	}
	for _, el := range optionRegion(a.Argv) {
		checkOptionElement(t, el, a.Argv)
	}
}

// checkOptionElement checks one element of a git argv's option region: no
// forced refspec and no forbidden option. A leading '+' forces a refspec;
// one anywhere in a refspec is refused too, so no forced update can be
// built (D6). The commit message is one --message= element, never a
// refspec or an option.
func checkOptionElement(t *testing.T, el string, argv []string) {
	t.Helper()
	isMessage := strings.HasPrefix(el, "--message=")
	if strings.HasPrefix(el, "+") || !isMessage && strings.Contains(el, "refs/") && strings.Contains(el, "+") {
		t.Errorf("forced refspec %q in %q", el, argv)
	}
	for _, f := range forbidden {
		if el == f || strings.HasPrefix(el, f) && !isMessage {
			t.Errorf("forbidden %q in option region of %q", f, argv)
		}
	}
}

func TestAgentsyncDisplayUnchanged(t *testing.T) {
	a := mustAction(t)(McpEnable("srv"))
	if a.Program != "" {
		t.Errorf("Program = %q", a.Program)
	}
	if got := a.Display(); got != "agentsync mcp enable srv" {
		t.Errorf("Display = %q", got)
	}
	if got := a.ConfirmVerb(); got != "enable" {
		t.Errorf("ConfirmVerb = %q", got)
	}
}

// The .state check holds where git on Windows reads '\' as a separator
// and where the filesystem folds case (macOS, Windows), so no spelling of
// the state directory reaches git.
func TestStatePathRefusedAnyCaseOrSeparator(t *testing.T) {
	for _, p := range []string{`.state\backup\config`, `skills\.state\x`, ".STATE/backup/x", "a/.State/x", `.State\x`} {
		if err := ValidGitPath(p); err == nil {
			t.Errorf("ValidGitPath(%q) accepted", p)
		}
		checkPathRefused(t, p)
	}
	for _, p := range []string{"state/x", ".stateful/x", "a.state/x", `x\y`} {
		if err := ValidGitPath(p); err != nil {
			t.Errorf("ValidGitPath(%q) = %v", p, err)
		}
	}
}

// A plain ssh user name is not a secret: the clone URL's display keeps it
// in both the ssh:// and the scp form, and an https URL is unchanged.
func TestInitFromURLDisplayKeepsPlainUser(t *testing.T) {
	for u, want := range map[string]string{
		"ssh://git@github.com/o/r.git":   "agentsync init ssh://git@github.com/o/r.git",
		"ssh://deploy.bot@h:2222/o/r":    "agentsync init ssh://deploy.bot@h:2222/o/r",
		"git@github.com:o/r.git":         "agentsync init git@github.com:o/r.git",
		"https://github.com/o/r.git":     "agentsync init https://github.com/o/r.git",
		"ssh://git@h/o/ghp_abcdefghijkl": "agentsync init 'ssh://git@h/o/••••'",
	} {
		if got := mustAction(t)(InitFromURL(u)).Display(); got != want {
			t.Errorf("Display(%q) = %q, want %q", u, got, want)
		}
	}
	// A user name that is a token shape stays masked, whether or not
	// mask.Text knows its shape.
	for _, user := range []string{"ghp_abcdefghijkl", "AbCdEf0123456789XyZ", "glpat-abcdefghij0123456789"} {
		if got := mustAction(t)(InitFromURL("ssh://" + user + "@h/o/r")).Display(); strings.Contains(got, user) {
			t.Errorf("token-shaped user shown: %q", got)
		}
	}
}
