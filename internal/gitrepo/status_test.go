package gitrepo

import (
	"bytes"
	"context"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/lusky3/agentsync-tui/internal/gitrepo/gittest"
)

// update regenerates testdata/porcelain-*.bin from real git
// (go test ./internal/gitrepo/ -run TestRegenerate -update). Normal runs
// only read the committed fixtures.
var update = flag.Bool("update", false, "regenerate testdata/porcelain-*.bin from real git")

const (
	skillPath    = "skills/s/SKILL.md"
	memoryPath   = "memory/c.md"
	notesPath    = "skills/notes.md"
	homePS       = "home"
	ageMarker    = "AGE-SECRET-KEY-1QQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQ"
	vaultHeader  = "age-encryption.org/v1\n-> X25519 abc\n"
	zeroOID      = "0000000000000000000000000000000000000000"
	someOID      = "587be6b4c3f93f93c489c0111bba5596147a26cb"
	headerOID    = "# branch.oid " + someOID
	headerMain   = "# branch.head main"
	fmtFiles     = "Files = %+v"
	zwName       = "a\u200bb"
	gitCheckout  = "checkout"
	fxAB         = "ab"
	plainAB      = "ab" // the plain name zero-width variants must not render as
	fxKinds      = "kinds"
	fxUnborn     = "unborn"
	fxDetached   = "detached"
	fxHostile    = "hostile"
	fxMaintainer = "maintainer"
	homeSkillA   = "home/skills/a.md"
	originName   = "origin"
	otherBranch  = "other"
	setupSh      = "setup.sh"
	skillsDir    = "skills"
	addedPath    = "skills/a.md"
	modPath      = "skills/m.md"
	okPath       = "skills/ok.md"
	renNew       = "skills/r-new.md"
	renOld       = "skills/r-old.md"
	vaultPath    = "skills/v.age"
	spacePath    = "skills/with space.md"
	mcpPath      = "mcp/x.toml"
)

// fixturePath is testdata/porcelain-<name>.bin.
func fixturePath(name string) string {
	return filepath.Join("testdata", "porcelain-"+name+".bin")
}

// fixture reads testdata/porcelain-<name>.bin.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(fixturePath(name))
	if err != nil {
		t.Fatalf("reading fixture %s (regenerate with -run TestRegenerate -update): %v", name, err)
	}
	return b
}

// recs joins records into porcelain -z output (each NUL-terminated).
func recs(r ...string) []byte {
	return []byte(strings.Join(r, "\x00") + "\x00")
}

// ordinary is a `1 ` record for path with status xy.
func ordinary(xy, path string) string {
	return "1 " + xy + " N... 100644 100644 100644 " + someOID + " " + someOID + " " + path
}

// renamed is a `2 ` record moving orig to path (orig is the next record).
func renamed(path, orig string) string {
	return "2 R. N... 100644 100644 100644 " + someOID + " " + someOID + " R100 " + path + "\x00" + orig
}

func untracked(path string) string { return "? " + path }

func parse(t *testing.T, out []byte, r Repo) Status {
	t.Helper()
	s, err := ParseStatus(out, r)
	if err != nil {
		t.Fatalf("ParseStatus: %v", err)
	}
	return s
}

// atRoot is a Repo whose home is its toplevel, an empty temp dir (fixture
// paths do not exist there, so they classify by name).
func atRoot(t *testing.T) Repo {
	t.Helper()
	return Repo{Manager: Git, Top: t.TempDir(), Pathspec: "."}
}

// byPath indexes s.Files by Path.
func byPath(s Status) map[string]File {
	m := make(map[string]File, len(s.Files))
	for _, f := range s.Files {
		m[f.Path] = f
	}
	return m
}

func get(t *testing.T, s Status, path string) File {
	t.Helper()
	f, ok := byPath(s)[path]
	if !ok {
		t.Fatalf("no file %q in %+v", path, s.Files)
	}
	return f
}

// write creates rel (slash-separated) under dir with body.
func write(t *testing.T, dir, rel, body string) string {
	t.Helper()
	full := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return full
}

// gitMayFail runs git in t's hermetic env, ignoring a non-zero exit (a
// merge that stops on a conflict).
func gitMayFail(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = pinnedEnv(t)
	_ = cmd.Run()
}

// pinnedGit is gittest.Git with pinnedEnv's fixed dates.
func pinnedGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = pinnedEnv(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// pinnedCommit is gittest.Commit with pinnedEnv's fixed dates.
func pinnedCommit(t *testing.T, dir, msg string, files map[string]string) {
	t.Helper()
	paths := make([]string, 0, len(files))
	for p, body := range files {
		write(t, dir, p, body)
		paths = append(paths, p)
	}
	slices.Sort(paths)
	if len(paths) > 0 {
		pinnedGit(t, dir, append([]string{"add", "--"}, paths...)...)
	}
	pinnedGit(t, dir, "commit", "-q", "--allow-empty", "--message="+msg)
}

// status runs StatusArgv for r through the production git Program.
func status(t *testing.T, r Repo) []byte {
	t.Helper()
	res, err := gittest.NewGit(t).Run(context.Background(), StatusArgv(r)...)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("git status: %v (exit %d) %s", err, res.ExitCode, res.Stderr)
	}
	return []byte(res.Stdout)
}

func TestRegenerate(t *testing.T) {
	if !*update {
		t.Skip("run with -update to regenerate the porcelain fixtures")
	}
	gittest.Need(t)
	gens := map[string]func(t *testing.T) Repo{
		fxAB: genAheadBehind, fxKinds: genKinds, fxUnborn: genUnborn,
		fxDetached: genDetached, fxMaintainer: genMaintainer, fxHostile: genHostile,
	}
	for name, gen := range gens {
		out := status(t, gen(t))
		if err := os.WriteFile(fixturePath(name), out, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func repoAt(t *testing.T) (string, Repo) {
	t.Helper()
	dir := resolved(t, t.TempDir())
	gittest.Init(t, dir)
	return dir, Repo{Manager: Git, Top: dir, Pathspec: "."}
}

// genAheadBehind: upstream origin/main, two local commits ahead, one
// remote commit behind, plus a modified file.
func genAheadBehind(t *testing.T) Repo {
	dir, r := repoAt(t)
	remote := gittest.Bare(t)
	pinnedCommit(t, dir, initMsg, map[string]string{skillPath: "v1\n"})
	pinnedGit(t, dir, "remote", "add", originName, remote)
	pinnedGit(t, dir, "push", "-q", "-u", originName, "main")
	pinnedCommit(t, dir, "remote side", map[string]string{"skills/r.md": "r\n"})
	pinnedGit(t, dir, "push", "-q", originName, "main")
	pinnedGit(t, dir, "reset", "-q", "--hard", "HEAD~1")
	pinnedCommit(t, dir, "local 1", map[string]string{"skills/l1.md": "1\n"})
	pinnedCommit(t, dir, "local 2", map[string]string{"skills/l2.md": "2\n"})
	pinnedGit(t, dir, "fetch", "-q", originName)
	write(t, dir, skillPath, "v2\n")
	return r
}

// genKinds: modified, added, deleted, renamed, type-changed, untracked and
// a merge conflict.
func genKinds(t *testing.T) Repo {
	dir, r := repoAt(t)
	pinnedCommit(t, dir, initMsg, map[string]string{
		modPath: "m\n", "skills/d.md": "d\n", renOld: "rename me please\n",
		"skills/t.md": "t\n", memoryPath: "base\n",
	})
	pinnedGit(t, dir, "branch", otherBranch)
	pinnedCommit(t, dir, "ours", map[string]string{memoryPath: "ours\n"})
	pinnedGit(t, dir, gitCheckout, "-q", otherBranch)
	pinnedCommit(t, dir, "theirs", map[string]string{memoryPath: "theirs\n"})
	pinnedGit(t, dir, gitCheckout, "-q", "main")
	gitMayFail(t, dir, "merge", "-q", otherBranch)
	write(t, dir, modPath, "m2\n")
	if err := os.Remove(filepath.Join(dir, skillsDir, "d.md")); err != nil {
		t.Fatal(err)
	}
	pinnedGit(t, dir, "mv", renOld, renNew)
	tm := filepath.Join(dir, skillsDir, "t.md")
	if err := os.Remove(tm); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("m.md", tm); err != nil {
		t.Fatal(err)
	}
	write(t, dir, addedPath, "a\n")
	pinnedGit(t, dir, "add", "--", addedPath)
	write(t, dir, "skills/u.md", "u\n")
	return r
}

func genUnborn(t *testing.T) Repo {
	dir, r := repoAt(t)
	write(t, dir, tomlName, "agents = []\n")
	return r
}

func genDetached(t *testing.T) Repo {
	dir, r := repoAt(t)
	pinnedCommit(t, dir, initMsg, map[string]string{skillPath: "1\n"})
	pinnedCommit(t, dir, "second", map[string]string{skillPath: "2\n"})
	pinnedGit(t, dir, gitCheckout, "-q", "--detach", "HEAD~1")
	write(t, dir, skillPath, "3\n")
	return r
}

func genMaintainer(t *testing.T) Repo {
	dir, r := repoAt(t)
	pinnedCommit(t, dir, initMsg, map[string]string{tomlName: "agents = []\n"})
	write(t, dir, mcpPath, "[x]\n")
	write(t, dir, "kiro-agents-src/a", "a\n")
	write(t, dir, setupSh, "#!/bin/sh\n")
	return r
}

// hostileNames are the exact names genHostile creates.
var hostileNames = []string{
	"skills/a b.md", "skills/new\nline.md", "skills/tab\there.md",
	`skills/q"uote.md`, `skills/back\slash.md`, " skills/lead.md",
}

func genHostile(t *testing.T) Repo {
	dir, r := repoAt(t)
	pinnedCommit(t, dir, initMsg, map[string]string{spacePath: "1\n"})
	write(t, dir, spacePath, "2\n")
	for _, n := range hostileNames {
		write(t, dir, n, "x\n")
	}
	return r
}

func TestStatusArgv(t *testing.T) {
	got := strings.Join(StatusArgv(Repo{Top: "/r", Pathspec: "h"}), " ")
	want := "-C /r status --porcelain=v2 -z --branch --untracked-files=all --ignore-submodules=all -- h"
	if got != want {
		t.Fatalf("StatusArgv = %q, want %q", got, want)
	}
}

func TestParseHeaders(t *testing.T) {
	s := parse(t, fixture(t, fxAB), atRoot(t))
	b := s.Branch
	if b.Head != "main" || b.Upstream != "origin/main" || !b.ABKnown || b.Ahead != 2 || b.Behind != 1 || b.Detached || b.Unborn {
		t.Fatalf("Branch = %+v", b)
	}
	if f := get(t, s, skillPath); f.Kind != Modified || f.Class != Owned || !f.Default {
		t.Fatalf("%s = %+v", skillPath, f)
	}
	s = parse(t, recs(headerOID, headerMain), atRoot(t))
	if s.Branch.ABKnown || s.Branch.Upstream != "" {
		t.Fatalf("no upstream: Branch = %+v", s.Branch)
	}
}

func TestParseUnbornAndDetached(t *testing.T) {
	s := parse(t, fixture(t, fxUnborn), atRoot(t))
	if !s.Branch.Unborn || s.Branch.Head != "main" || s.Branch.Detached {
		t.Fatalf("unborn Branch = %+v", s.Branch)
	}
	if f := get(t, s, tomlName); f.Kind != Untracked || !f.Default {
		t.Fatalf("%s = %+v", tomlName, f)
	}
	s = parse(t, fixture(t, fxDetached), atRoot(t))
	if !s.Branch.Detached || s.Branch.Head != "" || s.Branch.Unborn {
		t.Fatalf("detached Branch = %+v", s.Branch)
	}
}

func TestParseOrdinaryRenamedUnmergedUntracked(t *testing.T) {
	s := parse(t, fixture(t, fxKinds), atRoot(t))
	want := map[string]Kind{
		modPath: Modified, "skills/d.md": Deleted, renNew: Renamed,
		"skills/t.md": TypeChanged, addedPath: Added, "skills/u.md": Untracked, memoryPath: Conflict,
	}
	if len(s.Files) != len(want) {
		t.Fatalf("Files = %+v, want %d", s.Files, len(want))
	}
	for p, k := range want {
		if f := get(t, s, p); f.Kind != k {
			t.Errorf("%s: Kind = %v, want %v", p, f.Kind, k)
		}
	}
	if f := get(t, s, renNew); f.Orig != renOld || !f.Default {
		t.Errorf("rename = %+v", f)
	}
	if f := get(t, s, memoryPath); f.Selectable || f.Default || f.Note == "" {
		t.Errorf("conflict = %+v, want not selectable with a note", f)
	}
}

func TestPathWithSpacesNewlineTabQuoteBackslashKeptExact(t *testing.T) {
	s := parse(t, fixture(t, fxHostile), atRoot(t))
	m := byPath(s)
	for _, n := range append([]string{spacePath}, hostileNames...) {
		if _, ok := m[n]; !ok {
			t.Errorf("missing exact path %q", n)
		}
	}
	if len(m) != len(hostileNames)+1 {
		t.Errorf(fmtFiles, s.Files)
	}
	for _, n := range []string{"skills/new\nline.md", "skills/tab\there.md"} {
		if f := m[n]; f.Class != Unusual || f.Selectable {
			t.Errorf("%q = %+v, want Unusual, not selectable", n, f)
		}
	}
	for _, n := range []string{"skills/a b.md", `skills/q"uote.md`, `skills/back\slash.md`, spacePath} {
		if f := m[n]; f.Class != Owned || !f.Selectable {
			t.Errorf("%q = %+v, want Owned, selectable", n, f)
		}
	}
	if f := m[" skills/lead.md"]; f.Class != Other || f.Default {
		t.Errorf("leading space = %+v, want Other, not default", f)
	}
}

func TestInvalidUTF8AndBidiAreUnusual(t *testing.T) {
	names := []string{"skills/\xff.md", "skills/a\u202eb.md", "skills/a\u2066b.md", "skills/a\u200db.md"}
	out := []string{headerOID, headerMain}
	for _, n := range names {
		out = append(out, untracked(n))
	}
	s := parse(t, recs(out...), atRoot(t))
	for _, n := range names {
		f := get(t, s, n)
		if f.Class != Unusual || f.Selectable || f.Default {
			t.Errorf("%q = %+v, want Unusual, not selectable", n, f)
		}
		if d := DisplayName(n); !strings.HasSuffix(d, " (unusual name)") || d == n {
			t.Errorf("DisplayName(%q) = %q", n, d)
		}
	}
}

func TestDisplayNamesNeverCollide(t *testing.T) {
	pairs := [][2]string{
		{zwName, plainAB},
		{"a\u202eb", plainAB},
		{"a\xffb", "a\ufffdb"},
		{"a\nb", "a b"},
		// A plain name spelling another name's quoted rendering.
		{zwName, strconv.QuoteToASCII(zwName) + " (unusual name)"},
	}
	for _, p := range pairs {
		if DisplayName(p[0]) == DisplayName(p[1]) {
			t.Errorf("DisplayName(%q) == DisplayName(%q) == %q", p[0], p[1], DisplayName(p[0]))
		}
	}
	if d := DisplayName("skills/plain.md"); d != "skills/plain.md" {
		t.Errorf("plain name rendered %q", d)
	}
}

func TestTokenInFileNameMaskedAndNotSelectable(t *testing.T) {
	token := "ghp_" + strings.Repeat("A1b2C", 4)
	name := "skills/" + token + "/SKILL.md"
	s := parse(t, recs(headerOID, headerMain, untracked(name)), atRoot(t))
	f := get(t, s, name)
	if f.Class != Unusual || f.Selectable || f.Default {
		t.Fatalf("token name = %+v, want Unusual, not selectable", f)
	}
	row := DisplayName(f.Path) + " " + f.Note
	if strings.Contains(row, token) || strings.Contains(row, "ghp_") {
		t.Fatalf("row %q shows the token", row)
	}
	if !strings.Contains(row, "(name looks like a credential)") {
		t.Fatalf("row %q lacks the credential marker", row)
	}
	// An unusual name with a token is masked too.
	if d := DisplayName("skills/\u200b" + token); strings.Contains(d, token) {
		t.Fatalf("quoted DisplayName %q shows the token", d)
	}
	if d := DisplayName("keys/" + ageMarker); strings.Contains(d, "AGE-SECRET-KEY") {
		t.Fatalf("DisplayName %q shows an age identity", d)
	}
}

func TestGlobNamedFileMatchedLiterally(t *testing.T) {
	gittest.Need(t)
	dir, _ := repoAt(t)
	gittest.Commit(t, dir, initMsg, map[string]string{"a*/x": "1\n", "ab/x": "1\n"})
	write(t, dir, "a*/x", "2\n")
	write(t, dir, "ab/x", "2\n")
	r := Repo{Manager: Git, Layout: BelowRoot, Top: dir, Pathspec: "a*"}
	s := parse(t, status(t, r), r)
	if len(s.Files) != 1 || s.Files[0].Path != "a*/x" || s.Files[0].HomeRel != "x" {
		t.Fatalf("Files = %+v, want only a*/x", s.Files)
	}
}

func TestOutsidePathspecDropped(t *testing.T) {
	r := Repo{Manager: Git, Layout: BelowRoot, Top: t.TempDir(), Pathspec: homePS}
	out := recs(headerOID, headerMain,
		ordinary(".M", homeSkillA), ordinary(".M", "other/x"), untracked("homework/x"),
		untracked("../x"), untracked("/etc/passwd"), untracked("home"), untracked("home/../x"), untracked("home/./x"), untracked("home//x"))
	s := parse(t, out, r)
	if len(s.Files) != 1 || s.Files[0].Path != homeSkillA || s.Files[0].HomeRel != addedPath {
		t.Fatalf(fmtFiles, s.Files)
	}
	if s.Dropped != 8 || !strings.Contains(s.Note(), "8") {
		t.Fatalf("Dropped = %d, Note = %q", s.Dropped, s.Note())
	}
	if parse(t, recs(headerOID), r).Note() != "" {
		t.Fatal("Note set with nothing dropped")
	}
}

func TestMalformedRecordErrorHidesBytes(t *testing.T) {
	secret := "ghp_SECRETSECRETSECRET"
	bad := [][]byte{
		recs("x " + secret),
		recs("1 .M " + secret),
		recs("2 R. N... 100644 100644 100644 a b R100 " + secret),
		recs("u UU N... 1 2 3 4 a b " + secret),
		recs(headerMain, "", untracked(secret)),
		recs("# branch.ab +x -" + secret),
		recs("? "),
		recs("1 .M N... 100644 100644 100644 a b "),
	}
	for i, out := range bad {
		_, err := ParseStatus(out, atRoot(t))
		if err == nil {
			t.Errorf("case %d: no error", i)
			continue
		}
		if strings.Contains(err.Error(), "SECRET") {
			t.Errorf("case %d: error %q echoes bytes", i, err)
		}
	}
}

func TestClassifyTable(t *testing.T) {
	cases := []struct {
		rel  string
		head string
		age  bool
		want Class
	}{
		{".state/x", "", false, StateDir},
		{".STATE/x", "", false, StateDir},
		{"skills/.State/y", "", false, StateDir},
		{notesPath, "x " + ageMarker, false, Sensitive},
		{notesPath, "", true, Sensitive},
		{notesPath, "key:\n" + pemHeader + "\nQUFBQQ==\n", false, Sensitive},
		{"key.txt", "", false, Sensitive},
		{"skills/KEY.TXT", "", false, Sensitive},
		{"skills/server.pem", "", false, Sensitive},
		{"skills/id_ed25519", "", false, Sensitive},
		{".env", "", false, Sensitive},
		{"skills/.env.local", "", false, Sensitive},
		{"skills/id_ed25519.pub", "", false, Owned},
		{"id_ed25519.pub", "", false, Other},
		{"secrets/vault.age", vaultHeader, false, Vault},
		{vaultPath, vaultHeader, false, Vault},
		{"skills/a\nb", "", false, Unusual},
		{"skills/ghp_" + strings.Repeat("x", 20), "", false, Unusual},
		{tomlName, "", false, Owned},
		{".gitignore", "", false, Owned},
		{mcpPath, "", false, Owned},
		{"memory/m.md", "", false, Owned},
		{"subagents/a.md", "", false, Owned},
		{"commands/c.md", "", false, Owned},
		{"hooks/h.toml", "", false, Owned},
		{"lsp/l.toml", "", false, Owned},
		{"plugins/p", "", false, Owned},
		{"marketplaces/m", "", false, Owned},
		{setupSh, "", false, Other},
		{skillsDir, "", false, Other},
		{"mcpx/y", "", false, Other},
		{"sub/agentsync.toml", "", false, Other},
	}
	for _, c := range cases {
		got, note := Classify(c.rel, []byte(c.head), c.age)
		if got != c.want {
			t.Errorf("Classify(%q) = %v (%q), want %v", c.rel, got, note, c.want)
		}
		if got == Other && note != "not an agentsync file" {
			t.Errorf("Classify(%q) note = %q", c.rel, note)
		}
	}
}

func TestAgeIdentityByContentWhateverName(t *testing.T) {
	r := atRoot(t)
	write(t, r.Top, notesPath, "notes\n"+ageMarker+"\n")
	write(t, r.Top, okPath, "fine\n")
	s := parse(t, recs(headerOID, headerMain, untracked(notesPath), untracked(okPath)), r)
	if f := get(t, s, notesPath); f.Class != Sensitive || f.Selectable || f.Default {
		t.Fatalf("%s = %+v, want Sensitive", notesPath, f)
	}
	if f := get(t, s, okPath); f.Class != Owned || !f.Default {
		t.Fatalf("ok.md = %+v", f)
	}
}

func TestAgeIdentityPastFirst4KiB(t *testing.T) {
	dir := t.TempDir()
	at5000 := write(t, dir, "a", strings.Repeat("x", 5000)+ageMarker)
	// 2 MiB, the marker straddling a read-buffer boundary past the 1 MiB cap.
	off := 48*ageReadBuf - 7
	big := bytes.Repeat([]byte("y"), 2<<20)
	copy(big[off:], ageMarker)
	bigPath := filepath.Join(dir, "big")
	if err := os.WriteFile(bigPath, big, 0o644); err != nil {
		t.Fatal(err)
	}
	clean := write(t, dir, "clean", strings.Repeat("z", 3*ageReadBuf))
	for _, c := range []struct {
		path string
		want bool
	}{{at5000, true}, {bigPath, true}, {clean, false}} {
		got, err := HasKeyMaterial(c.path)
		if err != nil || got != c.want {
			t.Errorf("HasKeyMaterial(%s) = %v, %v; want %v", filepath.Base(c.path), got, err, c.want)
		}
	}
	// Through ParseStatus, a file named like a skill.
	r := atRoot(t)
	write(t, r.Top, notesPath, strings.Repeat("x", 5000)+ageMarker)
	if f := get(t, parse(t, recs(headerOID, untracked(notesPath)), r), notesPath); f.Class != Sensitive {
		t.Fatalf("marker past 4 KiB: %+v", f)
	}
}

// pemHeader is a PEM private-key header (no key follows it here).
const pemHeader = "-----BEGIN OPENSSH PRIVATE KEY-----"

// A private key is sensitive by content, whatever the file's name, also
// past the first 4 KiB and across a read-buffer boundary, so its diff is
// never shown and it is never selectable.
func TestPrivateKeyByContentWhateverName(t *testing.T) {
	dir := t.TempDir()
	off := 3*ageReadBuf - 9
	big := bytes.Repeat([]byte("y"), 4*ageReadBuf)
	copy(big[off:], "-----BEGIN RSA PRIVATE KEY-----")
	bigPath := filepath.Join(dir, "big")
	if err := os.WriteFile(bigPath, big, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := HasKeyMaterial(bigPath); !got || err != nil {
		t.Errorf("HasKeyMaterial(big) = %v, %v", got, err)
	}
	r := atRoot(t)
	write(t, r.Top, notesPath, strings.Repeat("x", 5000)+"\n"+pemHeader+"\n")
	write(t, r.Top, okPath, "a PRIVATE KEY is not a header\n")
	s := parse(t, recs(headerOID, untracked(notesPath), untracked(okPath)), r)
	if f := get(t, s, notesPath); f.Class != Sensitive || f.Selectable {
		t.Fatalf("%s = %+v, want Sensitive", notesPath, f)
	}
	if f := get(t, s, okPath); f.Class != Owned {
		t.Fatalf("ok.md = %+v", f)
	}
}

func TestSymlinkClassifiedByNameNeverDefault(t *testing.T) {
	r := atRoot(t)
	target := write(t, t.TempDir(), "secret", ageMarker)
	link := filepath.Join(r.Top, skillsDir, "l.md")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if got, err := HasKeyMaterial(link); got || err != nil {
		t.Fatalf("HasKeyMaterial followed a symlink: %v, %v", got, err)
	}
	f := get(t, parse(t, recs(headerOID, untracked("skills/l.md")), r), "skills/l.md")
	if f.Class != Owned || f.Default {
		t.Fatalf("symlink = %+v, want Owned by name, not default", f)
	}
}

func TestDirectoryEntryNotSelectable(t *testing.T) {
	r := atRoot(t)
	if err := os.MkdirAll(filepath.Join(r.Top, skillsDir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := get(t, parse(t, recs(headerOID, untracked("skills/nested/")), r), "skills/nested/")
	if f.Selectable || f.Default {
		t.Fatalf("directory = %+v, want not selectable", f)
	}
}

func TestVaultUnselectedByDefault(t *testing.T) {
	r := atRoot(t)
	write(t, r.Top, vaultPath, vaultHeader)
	f := get(t, parse(t, recs(headerOID, untracked(vaultPath)), r), vaultPath)
	if f.Class != Vault || !f.Selectable || f.Default || !strings.Contains(f.Note, "encrypted vault") {
		t.Fatalf("vault = %+v, want Vault, selectable, not default, noted", f)
	}
}

func TestStateDirNeverSelectable(t *testing.T) {
	r := atRoot(t)
	names := []string{".state/x", ".STATE/x", "skills/.State/y", ".state/backup/agentsync.toml"}
	out := []string{headerOID}
	for _, n := range names {
		out = append(out, untracked(n))
	}
	s := parse(t, recs(out...), r)
	for _, n := range names {
		if f := get(t, s, n); f.Class != StateDir || f.Selectable || f.Default {
			t.Errorf("%s = %+v, want StateDir, not selectable", n, f)
		}
	}
	// BelowRoot: the state dir is matched below the pathspec too.
	b := Repo{Manager: Git, Layout: BelowRoot, Top: t.TempDir(), Pathspec: homePS}
	f := get(t, parse(t, recs(headerOID, untracked("home/.State/x")), b), "home/.State/x")
	if f.Class != StateDir || f.Selectable {
		t.Fatalf("below root: %+v", f)
	}
}

func TestRenameAcrossHomeBoundaryNoted(t *testing.T) {
	r := Repo{Manager: Git, Layout: BelowRoot, Top: t.TempDir(), Pathspec: homePS}
	out := recs(headerOID, headerMain, renamed("home/skills/moved.md", "outside.txt"), renamed("home/skills/b.md", homeSkillA))
	s := parse(t, out, r)
	f := get(t, s, "home/skills/moved.md")
	if f.Kind != Renamed || f.Orig != "outside.txt" || !f.Selectable ||
		!strings.Contains(f.Note, "the old path is outside the home and is not part of this commit") {
		t.Fatalf("cross-boundary rename = %+v", f)
	}
	if g := get(t, s, "home/skills/b.md"); g.Orig != homeSkillA || strings.Contains(g.Note, "outside") {
		t.Fatalf("in-home rename = %+v", g)
	}
	if len(s.Files) != 2 {
		t.Fatalf("Files = %+v (the Orig record must not be a file)", s.Files)
	}
}

func TestMaintainerLayoutDefaults(t *testing.T) {
	s := parse(t, fixture(t, fxMaintainer), atRoot(t))
	want := map[string]bool{mcpPath: true, "kiro-agents-src/a": false, setupSh: false}
	if len(s.Files) != len(want) {
		t.Fatalf(fmtFiles, s.Files)
	}
	for p, d := range want {
		f := get(t, s, p)
		if f.Default != d || !f.Selectable {
			t.Errorf("%s = %+v, want Default %v, selectable", p, f, d)
		}
	}
	if f := get(t, s, setupSh); f.Note != "not an agentsync file" {
		t.Errorf("setup.sh note = %q", f.Note)
	}
}

func FuzzParseStatus(f *testing.F) {
	for _, n := range []string{fxAB, fxKinds, fxUnborn, fxDetached, fxMaintainer, fxHostile} {
		if b, err := os.ReadFile(fixturePath(n)); err == nil {
			f.Add(b, false)
		}
	}
	f.Add(recs(renamed("home/a", "../b"), untracked("home/.state/x")), true)
	f.Add(recs("u UU N... 100644 100644 100644 100644 a b c home/x"), true)
	top := f.TempDir()
	f.Fuzz(func(t *testing.T, out []byte, below bool) {
		r := Repo{Manager: Git, Top: top, Pathspec: "."}
		if below {
			r.Layout, r.Pathspec = BelowRoot, homePS
		}
		if s, err := ParseStatus(out, r); err == nil {
			checkFuzzed(t, s, r)
		}
	})
}

// underPathspec reports whether p lies strictly below the home pathspec.
func underPathspec(p, pathspec string) bool {
	_, ok := homeRelative(p, pathspec)
	return ok
}

// checkFuzzed asserts FuzzParseStatus's invariants: every path is local
// and below the home, a selectable path passes ValidGitPath, and only a
// selectable Owned file is a default.
func checkFuzzed(t *testing.T, s Status, r Repo) {
	t.Helper()
	for _, file := range s.Files {
		if !local(file.Path) || !local(file.HomeRel) || !underPathspec(file.Path, r.Pathspec) {
			t.Fatalf("path %q escapes", file.Path)
		}
		if file.Selectable && validPath(file.Path) != nil {
			t.Fatalf("selectable path %q fails ValidGitPath", file.Path)
		}
		if file.Default && (!file.Selectable || file.Class != Owned) {
			t.Fatalf("default %+v", file)
		}
	}
}

func TestUnreadableFileFailsClosedWithItsOwnNote(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a non-root unix user (mode 000 must deny the read)")
	}
	r := atRoot(t)
	p := write(t, r.Top, skillPath, "fine\n")
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
	f := get(t, parse(t, recs(headerOID, untracked(skillPath)), r), skillPath)
	if f.Class != Sensitive || f.Selectable || f.Default {
		t.Fatalf("unreadable = %+v, want Sensitive, not selectable", f)
	}
	if f.Note != noteUnreadable || strings.Contains(f.Note, "secret") {
		t.Fatalf("unreadable note = %q, want %q", f.Note, noteUnreadable)
	}
}

// pinnedEnv is t's hermetic git env with fixed author and committer dates,
// so TestRegenerate writes the same commit ids every run.
func pinnedEnv(t *testing.T) []string {
	t.Helper()
	const date = "2026-01-01T00:00:00Z"
	return append(slices.Clone(gittest.Env(t)), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
}

// A deleted sensitive file's note says what is refused: committing its
// removal, not the file itself.
func TestDeletedSensitiveNoteIsKindAware(t *testing.T) {
	s := parse(t, recs(headerOID, ordinary(".D", ".env"), ordinary(".M", "key.txt")), atRoot(t))
	del, mod := get(t, s, ".env"), get(t, s, "key.txt")
	if del.Class != Sensitive || del.Selectable || del.Default {
		t.Fatalf("deleted = %+v, want Sensitive, not selectable", del)
	}
	if del.Note != noteSensitiveDeleted || strings.Contains(del.Note, "never committed") {
		t.Fatalf("deleted note = %q, want %q", del.Note, noteSensitiveDeleted)
	}
	if mod.Note != noteSensitive {
		t.Fatalf("modified note = %q, want %q", mod.Note, noteSensitive)
	}
}
