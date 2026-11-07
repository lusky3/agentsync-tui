package actions

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/lusky3/agentsync-tui/internal/mask"
)

// This file is the only place git argv is built (D1). Every argv starts
// `-C <absolute toplevel>`; the runner prepends the fixed hardening
// (--no-pager, --literal-pathspecs, ...), which Argv and Display never hold.
// Paths always follow "--", a commit message is one --message= element, and
// remote names and clone URLs are refused before an Action exists. None of
// these builders can emit --force, --no-verify, -a, --amend, a forced or
// wildcard refspec or a subcommand outside add, commit, fetch, pull and
// push (D6, D11); push's only refspec is refs/heads/<b>:refs/heads/<b>.

// Limits on what the git builders accept.
const (
	maxGitPaths     = 5000
	maxGitPathBytes = 4096
	maxCommitRunes  = 200
	maxCloneURL     = 2048
	maxBranchBytes  = 255
	// displayPaths is how many paths Display lists before "… (+N more)".
	displayPaths = 10
)

var (
	remoteNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,99}$`)
	urlHostRe    = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?$`)
	urlUserRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// GitRepo is the repository a git action runs in. Top is its absolute,
// clean toplevel (`git rev-parse --show-toplevel`); every builder refuses
// any other value.
type GitRepo struct{ Top string }

func (r GitRepo) check() error {
	if !filepath.IsAbs(r.Top) || filepath.Clean(r.Top) != r.Top || hasControl(r.Top) {
		return errors.New("invalid git toplevel: not an absolute, clean path")
	}
	return nil
}

// head is the `-C <top>` prefix plus the given subcommand words.
func (r GitRepo) head(words ...string) []string {
	return append([]string{"-C", r.Top}, words...)
}

// hasControl reports control (including \n and \t) or format characters,
// or invalid UTF-8.
func hasControl(s string) bool {
	if !utf8.ValidString(s) || mask.Sanitize(s) != s {
		return true
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return true
		}
	}
	return false
}

// ValidGitPath reports whether p may be passed to git as a path: non-empty,
// at most 4096 bytes, local (relative, no ".." escape, not the whole tree),
// valid UTF-8 and free of control and format characters. It refuses any
// path with a component equal to ".state" in any case, separated by '/' or
// '\' (the ignored state directory holds pre-apply backups with live
// secrets; D11) and any path starting with ":" (git pathspec magic:
// --literal-pathspecs only applies when the Action runs through
// runner.NewGit, so Argv must not rely on it).
// Glob characters are allowed: the runner's --literal-pathspecs makes them
// match literally.
func ValidGitPath(p string) error {
	switch {
	case p == "":
		return errors.New("invalid path: empty")
	case len(p) > maxGitPathBytes:
		return errors.New("invalid path: too long")
	case strings.IndexByte(p, 0) >= 0, hasControl(p):
		return errors.New("invalid path: control or format character, or invalid UTF-8")
	case !filepath.IsLocal(p):
		return errors.New("invalid path: not inside the repository")
	case filepath.Clean(p) == ".":
		return errors.New("invalid path: names the whole tree")
	case strings.HasPrefix(p, ":"):
		return errors.New("invalid path: pathspec magic")
	case inStateDir(p):
		return errors.New("invalid path: inside the state directory")
	}
	return nil
}

// inStateDir reports whether any component of p, split on '/' and '\'
// (git on Windows reads both as separators), is ".state" in any case
// (macOS and Windows filesystems fold case).
func inStateDir(p string) bool {
	return slices.ContainsFunc(strings.FieldsFunc(p, func(r rune) bool { return r == '/' || r == '\\' }), func(c string) bool {
		return strings.EqualFold(c, ".state")
	})
}

func checkPaths(paths []string) error {
	if len(paths) == 0 || len(paths) > maxGitPaths {
		return fmt.Errorf("invalid paths: need 1 to %d, got %d", maxGitPaths, len(paths))
	}
	for i, p := range paths {
		if err := ValidGitPath(p); err != nil {
			return fmt.Errorf("path %d: %w", i+1, err)
		}
	}
	return nil
}

// ValidCommitMessage reports whether m, trimmed, may be a commit message:
// non-empty, at most 200 runes, one line, no control or format characters,
// no credential (mask.Credential) and no token-shaped word.
func ValidCommitMessage(m string) error {
	m = strings.TrimSpace(m)
	switch {
	case m == "":
		return errors.New("invalid commit message: empty")
	case !utf8.ValidString(m):
		return errors.New("invalid commit message: invalid UTF-8")
	case utf8.RuneCountInString(m) > maxCommitRunes:
		return fmt.Errorf("invalid commit message: over %d characters", maxCommitRunes)
	case hasControl(m):
		return errors.New("invalid commit message: must be one line without control or format characters")
	}
	if kind, ok := mask.Credential(m); ok {
		return fmt.Errorf("commit message refused: it looks like it holds a credential (%s)", kind)
	}
	for _, w := range strings.FieldsFunc(m, wordSep) {
		if mask.TokenShaped(w) {
			return errors.New("commit message refused: it holds a token-shaped word")
		}
	}
	return nil
}

// wordSep splits a commit message into the words TokenShaped judges.
func wordSep(r rune) bool {
	return unicode.IsSpace(r) || strings.ContainsRune("\"'`,;:()[]{}<>=/@#?&!|*+", r)
}

// ValidRemoteName reports whether n is a plain remote name: it starts with
// a letter or digit, holds only letters, digits and ._/-, is at most 100
// characters and contains no "..".
func ValidRemoteName(n string) error {
	if !remoteNameRe.MatchString(n) || strings.Contains(n, "..") {
		return errors.New("invalid remote name")
	}
	return nil
}

// ValidBranchName reports whether b is a branch short name GitPush may
// put in a refspec. It applies git check-ref-format's rules for
// refs/heads/<b> (no "..", "@{", "//", control characters, space or any of
// ~ ^ : ? * [ \; no component starting with '.' or ending in ".lock"; no
// leading or trailing '/'; no trailing '.'), and also refuses a leading
// '-' (an option), "@", "HEAD", '+' anywhere (a leading '+' forces a
// refspec), format characters, invalid UTF-8 and more than 255 bytes. A
// bad name is refused, never escaped.
func ValidBranchName(b string) error {
	bad := errors.New("invalid branch name")
	switch {
	case b == "", len(b) > maxBranchBytes, b == "@", b == "HEAD":
		return bad
	case strings.HasPrefix(b, "-"), strings.HasPrefix(b, "/"), strings.HasSuffix(b, "/"), strings.HasSuffix(b, "."):
		return bad
	case strings.Contains(b, ".."), strings.Contains(b, "@{"), strings.Contains(b, "//"):
		return bad
	case strings.ContainsAny(b, " ~^:?*[\\+\x7f"), hasControl(b):
		return bad
	}
	for _, c := range strings.Split(b, "/") {
		if strings.HasPrefix(c, ".") || strings.HasSuffix(c, ".lock") {
			return bad
		}
	}
	return nil
}

// ValidCloneURL reports whether u is a URL `agentsync init` may clone: at
// most 2048 bytes, and either https://host/path without userinfo,
// ssh://[user@]host[:port]/path, or the scp form [user@]host:path. It
// refuses any "<transport>::" (ext::, fd::), file: and http: URLs,
// whitespace, control and format characters, a query or fragment, and any
// component (between '/', ':' and '@') that starts with '-', so no part of
// the URL can be read as an option by git or ssh (-u, -oProxyCommand=...).
func ValidCloneURL(u string) error {
	bad := func(why string) error { return fmt.Errorf("invalid repository URL: %s", why) }
	lower := strings.ToLower(u)
	switch {
	case u == "":
		return bad("empty")
	case len(u) > maxCloneURL:
		return bad("too long")
	case hasControl(u), strings.ContainsFunc(u, unicode.IsSpace):
		return bad("whitespace, control or format character")
	case strings.Contains(u, "::"):
		return bad(`"<transport>::" URLs are not allowed`)
	case strings.HasPrefix(lower, "file:"), strings.HasPrefix(lower, "http:"):
		return bad("use https:// or ssh://")
	case strings.Contains(lower, "--upload-pack"), strings.Contains(lower, "--config"):
		return bad("contains a git option")
	case strings.ContainsAny(u, "?#"):
		return bad("a query or fragment is not allowed")
	}
	for _, c := range strings.FieldsFunc(u, func(r rune) bool { return r == '/' || r == ':' || r == '@' }) {
		if strings.HasPrefix(c, "-") {
			return bad("a part starts with '-'")
		}
	}
	switch {
	case strings.HasPrefix(u, "https://"):
		return checkSchemeURL(u, false)
	case strings.HasPrefix(u, "ssh://"):
		return checkSchemeURL(u, true)
	case strings.Contains(u, "://"):
		return bad("use https:// or ssh://")
	}
	return checkSCPURL(u)
}

// checkSchemeURL checks an https:// (no userinfo) or ssh:// (user, no
// password) URL: a plain host, an optional numeric port and a path.
func checkSchemeURL(raw string, ssh bool) error {
	p, err := url.Parse(raw)
	if err != nil {
		return errors.New("invalid repository URL: does not parse")
	}
	if p.User != nil {
		_, hasPass := p.User.Password()
		if !ssh || hasPass {
			return errors.New("invalid repository URL: credentials in the URL are not allowed")
		}
		if !urlUserRe.MatchString(p.User.Username()) {
			return errors.New("invalid repository URL: bad user name")
		}
	}
	if !urlHostRe.MatchString(p.Hostname()) {
		return errors.New("invalid repository URL: bad host")
	}
	if port := p.Port(); port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return errors.New("invalid repository URL: bad port")
		}
	} else if strings.HasSuffix(p.Host, ":") {
		return errors.New("invalid repository URL: bad port")
	}
	if len(strings.Trim(p.Path, "/")) == 0 {
		return errors.New("invalid repository URL: no repository path")
	}
	return nil
}

// checkSCPURL checks the scp form [user@]host:path, where host holds no '/'
// and path does not start with '-' or '/'.
func checkSCPURL(u string) error {
	left, path, ok := strings.Cut(u, ":")
	if !ok {
		return errors.New("invalid repository URL: use https://, ssh:// or user@host:path")
	}
	host := left
	if user, h, hasUser := strings.Cut(left, "@"); hasUser {
		if !urlUserRe.MatchString(user) {
			return errors.New("invalid repository URL: bad user name")
		}
		host = h
	}
	if !urlHostRe.MatchString(host) {
		return errors.New("invalid repository URL: bad host")
	}
	if path == "" || strings.HasPrefix(path, "-") || strings.HasPrefix(path, "/") {
		return errors.New("invalid repository URL: bad repository path")
	}
	return nil
}

// withPaths appends "--" and paths to head, and returns the Action with a
// display copy that lists at most displayPaths of them.
func withPaths(title string, head, paths []string) Action {
	argv := append(append(head, "--"), paths...)
	shown := min(len(paths), displayPaths)
	disp := append(append([]string(nil), head...), "--")
	disp = append(disp, paths[:shown]...)
	return Action{Program: ProgramGit, Title: title, Argv: argv, display: disp, more: len(paths) - shown}
}

func plural(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + one + "s"
}

// GitAdd builds `-C <top> add -- <paths>`.
func GitAdd(r GitRepo, paths []string) (Action, error) {
	if err := r.check(); err != nil {
		return Action{}, err
	}
	if err := checkPaths(paths); err != nil {
		return Action{}, err
	}
	return withPaths("Stage "+plural(len(paths), "file"), r.head("add"), paths), nil
}

// GitCommit builds `-C <top> commit --message=<msg> -- <paths>`, with msg
// trimmed. Committing named paths never sweeps in other staged changes.
func GitCommit(r GitRepo, msg string, paths []string) (Action, error) {
	if err := r.check(); err != nil {
		return Action{}, err
	}
	if err := ValidCommitMessage(msg); err != nil {
		return Action{}, err
	}
	if err := checkPaths(paths); err != nil {
		return Action{}, err
	}
	head := r.head("commit", "--message="+strings.TrimSpace(msg))
	return withPaths("Commit "+plural(len(paths), "file"), head, paths), nil
}

// gitAction builds a path-free git Action whose display is its argv.
func gitAction(title string, argv []string) Action {
	return Action{Program: ProgramGit, Title: title, Argv: argv, display: append([]string(nil), argv...)}
}

// GitFetch builds `-C <top> fetch <remote>`: ReadOnly (no confirm, still
// logged) and Network.
func GitFetch(r GitRepo, remote string) (Action, error) {
	if err := r.check(); err != nil {
		return Action{}, err
	}
	if err := ValidRemoteName(remote); err != nil {
		return Action{}, err
	}
	a := gitAction("Fetch "+remote, r.head("fetch", remote))
	a.ReadOnly, a.Network = true, true
	return a, nil
}

// GitPull builds `-C <top> pull --ff-only --no-rebase --no-autostash` (D5).
func GitPull(r GitRepo) (Action, error) {
	if err := r.check(); err != nil {
		return Action{}, err
	}
	a := gitAction("Pull (fast-forward only)", r.head("pull", "--ff-only", "--no-rebase", "--no-autostash"))
	a.Network = true
	return a, nil
}

// GitPush builds `-C <top> push --no-follow-tags <remote>
// refs/heads/<branch>:refs/heads/<branch>` (D6): one explicit, non-forced,
// single-branch refspec. An explicit refspec overrides the repository's
// remote.<remote>.push refspecs (which take precedence over push.default
// and can force every branch) and push.default itself; --no-follow-tags
// overrides push.followTags. A non-fast-forward is rejected by git, never
// forced. remote.<remote>.mirror makes git refuse the push outright.
func GitPush(r GitRepo, remote, branch string) (Action, error) {
	if err := r.check(); err != nil {
		return Action{}, err
	}
	if err := ValidRemoteName(remote); err != nil {
		return Action{}, err
	}
	if err := ValidBranchName(branch); err != nil {
		return Action{}, err
	}
	ref := "refs/heads/" + branch
	a := gitAction("Push", r.head("push", "--no-follow-tags", remote, ref+":"+ref))
	a.Network = true
	return a, nil
}

// InitFromURL builds the agentsync bootstrap `init <url>` (D10) after
// ValidCloneURL.
func InitFromURL(u string) (Action, error) {
	if err := ValidCloneURL(u); err != nil {
		return Action{}, err
	}
	argv := []string{"init", u}
	return Action{Title: "Clone agentsync home", Argv: argv, Network: true, display: append([]string(nil), argv...)}, nil
}
