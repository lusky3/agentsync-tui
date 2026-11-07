package gitrepo

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/lusky3/agentsync-tui/internal/actions"
	"github.com/lusky3/agentsync-tui/internal/mask"
)

// StatusArgv is the status read for r: porcelain v2, NUL-separated, with
// branch headers, every untracked file (never a collapsed directory) and
// submodules ignored, limited to the home pathspec. The git Program adds
// --literal-pathspecs, so a home named with glob characters matches only
// itself.
func StatusArgv(r Repo) []string {
	return []string{"-C", r.Top, "status", "--porcelain=v2", "-z", "--branch",
		"--untracked-files=all", "--ignore-submodules=all", "--", r.Pathspec}
}

// Branch is what the `# branch.*` headers say. Head is empty when Detached.
// ABKnown is false when there is no upstream (no branch.ab header).
type Branch struct {
	Head             string
	Detached, Unborn bool
	Upstream         string
	Ahead, Behind    int
	ABKnown          bool
}

// Kind is what changed in a file.
type Kind int

// The kinds of change.
const (
	Modified Kind = iota
	Added
	Deleted
	Renamed
	Untracked
	Conflict
	TypeChanged
)

var kindNames = [...]string{"modified", "added", "deleted", "renamed", "untracked", "conflict", "type changed"}

func (k Kind) String() string {
	if k < 0 || int(k) >= len(kindNames) {
		return "unknown"
	}
	return kindNames[k]
}

// Class is how a file relates to agentsync, which decides whether it may
// be selected for a commit and whether it is selected by default.
type Class int

const (
	// Owned: under the agentsync-owned set (D3); selected by default.
	Owned Class = iota
	// Other: anything else in the home; selectable, not by default.
	Other
	// StateDir: under a .state directory (any case); never selectable.
	StateDir
	// Sensitive: a private key, an age identity or an env file (R6);
	// never selectable.
	Sensitive
	// Vault: age ciphertext; selectable, not by default (R6).
	Vault
	// Unusual: a name with control or format characters, invalid UTF-8, a
	// credential in it, one git cannot be given, or a directory or special
	// file; never selectable.
	Unusual
)

var classNames = [...]string{"owned", "other", "state", "sensitive", "vault", "unusual"}

func (c Class) String() string {
	if c < 0 || int(c) >= len(classNames) {
		return "unknown"
	}
	return classNames[c]
}

// File is one status record under the home. Path is git's exact path
// relative to the toplevel (the argv form); HomeRel is it relative to the
// home. Orig is a rename's old path (relative to the toplevel), which may
// lie outside the home and is never part of a commit.
type File struct {
	Path, Orig, HomeRel string
	Kind                Kind
	Class               Class
	Selectable, Default bool
	Note                string
}

// Status is a parsed `git status`. Dropped counts records whose path was
// not under the home pathspec or not local to the repository.
type Status struct {
	Branch  Branch
	Files   []File
	Dropped int
}

// Note explains Dropped, or is empty when nothing was dropped.
func (s Status) Note() string {
	switch s.Dropped {
	case 0:
		return ""
	case 1:
		return "1 path outside the home was skipped"
	}
	return fmt.Sprintf("%d paths outside the home were skipped", s.Dropped)
}

// The notes a File can carry.
const (
	noteOther            = "not an agentsync file"
	noteState            = "agentsync state directory: never committed"
	noteSensitive        = "looks like a secret (private key, age identity or env file): never committed"
	noteSensitiveDeleted = "looks like a secret (private key, age identity or env file): removing it must be committed outside the TUI"
	noteUnreadable       = "could not be read; not selectable"
	noteVault            = "encrypted vault: commit only if you mean to share the ciphertext"
	noteUnusual          = "unusual name, not selectable"
	noteCredName         = "the name looks like a credential, not selectable"
	noteBadPath          = "git cannot be given this path safely, not selectable"
	noteSpecial          = "not a regular file, not selectable"
	noteSymlink          = "symlink: classified by its name only"
	noteConflict         = "unresolved conflict: resolve it outside the TUI"
	noteOrigOutside      = "the old path is outside the home and is not part of this commit"
)

// The suffixes DisplayName adds.
const (
	suffixUnusual = " (unusual name)"
	suffixCred    = " (name looks like a credential)"
	hiddenName    = "(name hidden)"
)

// headSize is how much of a file Classify sees; ageReadBuf is the read
// size of the key-material scan.
const (
	headSize   = 4 << 10
	ageReadBuf = 32 << 10
)

var (
	ageSecret = []byte("AGE-SECRET-KEY-")
	ageVault  = []byte("age-encryption.org/v1")
	// privateKeyRe is a PEM or OpenSSH private-key header (as mask's
	// KindPrivateKey reads one), its label bounded so that a header split
	// across two reads is still found (markerCarry).
	privateKeyRe = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]{0,40}PRIVATE KEY-----`)
)

// markerCarry is how many bytes of one read the key-material scan keeps
// for the next: more than the longest marker privateKeyRe or ageSecret
// matches.
const markerCarry = 80

// keyMaterial reports whether b holds an age identity or a private-key
// header.
func keyMaterial(b []byte) bool {
	return bytes.Contains(b, ageSecret) || privateKeyRe.Match(b)
}

// HoldsKeyMaterial reports whether s holds an age identity or a PEM or
// OpenSSH private-key header: a diff that does is not shown, whichever of
// its lines (an added or a removed one) carries the key.
func HoldsKeyMaterial(s string) bool { return keyMaterial([]byte(s)) }

// ownedFiles and ownedDirs are the agentsync-owned set (D3), relative to
// the home.
var (
	ownedFiles = map[string]bool{"agentsync.toml": true, ".gitignore": true}
	ownedDirs  = map[string]bool{
		"mcp": true, "memory": true, "skills": true, "subagents": true, "commands": true,
		"hooks": true, "lsp": true, "plugins": true, "marketplaces": true,
	}
)

// The parse errors; no message ever holds record bytes.
var (
	errRecord     = errors.New("unexpected record")
	errNoOrig     = errors.New("rename without its old path")
	errAB         = errors.New("malformed branch.ab header")
	errChanged    = errors.New("the file changed while it was opened")
	errNotRegular = ErrNotRegular
)

// The errors ReadRegular returns for what it refuses to read.
var (
	ErrSymlink    = errors.New("a symlink is not followed")
	ErrNotRegular = errors.New("not a regular file")
)

// ParseStatus parses StatusArgv's output for r and classifies every file,
// reading at most the first 4 KiB of each worktree file (never through a
// symlink) and scanning it whole for an age identity. A record outside the
// pinned grammar is an error whose message never echoes the record.
func ParseStatus(out []byte, r Repo) (Status, error) {
	p := parser{r: r, top: resolvePath(r.Top)}
	recs := strings.Split(string(out), "\x00")
	if n := len(recs); recs[n-1] == "" {
		recs = recs[:n-1]
	}
	for i := 0; i < len(recs); i++ {
		extra, err := p.record(recs, i)
		if err != nil {
			return Status{}, fmt.Errorf("git status: record %d: %w", i+1, err)
		}
		i += extra
	}
	return p.s, nil
}

type parser struct {
	r   Repo
	top string
	s   Status
}

// record parses recs[i] and returns how many following records it used
// (1 for a rename's old path).
func (p *parser) record(recs []string, i int) (int, error) {
	rec := recs[i]
	if len(rec) < 3 || rec[1] != ' ' {
		return 0, errRecord
	}
	body := rec[2:]
	switch rec[0] {
	case '#':
		return 0, p.s.Branch.header(body)
	case '1':
		return 0, p.changed(rec)
	case '2':
		if i+1 >= len(recs) || recs[i+1] == "" {
			return 0, errNoOrig
		}
		return 1, p.renamed(rec, recs[i+1])
	case 'u':
		return 0, p.unmerged(rec)
	case '?':
		p.add(File{Path: body, Kind: Untracked})
		return 0, nil
	case '!':
		return 0, nil
	}
	return 0, errRecord
}

// header applies one `# ` header; unknown headers are ignored.
func (b *Branch) header(body string) error {
	key, val, _ := strings.Cut(body, " ")
	switch key {
	case "branch.oid":
		b.Unborn = val == "(initial)"
	case "branch.head":
		b.Detached = val == "(detached)"
		if !b.Detached {
			b.Head = val
		}
	case "branch.upstream":
		b.Upstream = val
	case "branch.ab":
		return b.ab(val)
	}
	return nil
}

// ab parses `+A -B`.
func (b *Branch) ab(val string) error {
	a, c, ok := strings.Cut(val, " ")
	if !ok || !strings.HasPrefix(a, "+") || !strings.HasPrefix(c, "-") {
		return errAB
	}
	ahead, err1 := strconv.Atoi(a[1:])
	behind, err2 := strconv.Atoi(c[1:])
	if err1 != nil || err2 != nil || ahead < 0 || behind < 0 {
		return errAB
	}
	b.Ahead, b.Behind, b.ABKnown = ahead, behind, true
	return nil
}

// fields splits rec into n space-separated fields (the record type
// first) and the path (the rest, spaces kept). The second field is XY and
// has two characters; no field and no path may be empty.
func fields(rec string, n int) ([]string, string, error) {
	parts := strings.SplitN(rec, " ", n+1)
	if len(parts) != n+1 || len(parts[1]) != 2 {
		return nil, "", errRecord
	}
	for _, f := range parts {
		if f == "" {
			return nil, "", errRecord
		}
	}
	return parts[:n], parts[n], nil
}

// changed parses a `1 ` record: 8 fields, then the path.
func (p *parser) changed(rec string) error {
	f, name, err := fields(rec, 8)
	if err != nil {
		return err
	}
	p.add(File{Path: name, Kind: ordinaryKind(f[1])})
	return nil
}

// renamed parses a `2 ` record (9 fields, then the path) whose old path
// is orig, the next record. A copy (C score) is listed as Added.
func (p *parser) renamed(rec, orig string) error {
	f, name, err := fields(rec, 9)
	if err != nil {
		return err
	}
	var kind Kind
	switch f[8][0] {
	case 'R':
		kind = Renamed
	case 'C':
		kind = Added
	default:
		return errRecord
	}
	p.add(File{Path: name, Orig: orig, Kind: kind})
	return nil
}

// unmerged parses a `u ` record: 10 fields, then the path.
func (p *parser) unmerged(rec string) error {
	_, name, err := fields(rec, 10)
	if err != nil {
		return err
	}
	p.add(File{Path: name, Kind: Conflict})
	return nil
}

// ordinaryKind maps a `1 ` record's XY to a Kind.
func ordinaryKind(xy string) Kind {
	switch {
	case xy[0] == 'A' && xy[1] != 'D':
		return Added
	case strings.ContainsRune(xy, 'D'):
		return Deleted
	case strings.ContainsRune(xy, 'T'):
		return TypeChanged
	}
	return Modified
}

// local reports whether p is a path inside the repository that is not
// the whole tree and has no empty, "." or ".." component (git never
// prints one; "home/../x" would otherwise pass as below the home). A
// trailing slash (an untracked nested repository) is allowed.
func local(p string) bool {
	if !filepath.IsLocal(p) {
		return false
	}
	for _, c := range strings.Split(strings.TrimSuffix(p, "/"), "/") {
		if c == "" || c == "." || c == ".." {
			return false
		}
	}
	return true
}

// homeRelative is p relative to the home, when p lies strictly below it.
func homeRelative(p, pathspec string) (string, bool) {
	if pathspec == "." {
		return p, p != ""
	}
	rel, ok := strings.CutPrefix(p, pathspec+"/")
	return rel, ok && rel != ""
}

// validPath is the git builders' path check; a path failing it is never
// selectable.
func validPath(p string) error { return actions.ValidGitPath(p) }

// add classifies f and appends it, or counts it in Dropped when its path
// (or a rename's old path) is not local or not under the home.
func (p *parser) add(f File) {
	rel, ok := homeRelative(f.Path, p.r.Pathspec)
	if !ok || !local(f.Path) || (f.Orig != "" && !local(f.Orig)) {
		p.s.Dropped++
		return
	}
	f.HomeRel = rel
	c := inspect(p.top, f.Path)
	f.Class, f.Note = Classify(rel, c.head, c.age)
	f.Selectable = selectable(f.Class)
	p.restrict(&f, c)
	f.Default = f.Selectable && f.Class == Owned && !c.link
	p.s.Files = append(p.s.Files, f)
}

// restrict applies what Classify cannot see: the full path's validity,
// the file's type, a conflict and a rename's old path.
func (p *parser) restrict(f *File, c content) {
	if f.Selectable {
		if note := pathIssue(f.Path, c.special); note != "" {
			f.Class, f.Note, f.Selectable = Unusual, note, false
		}
	}
	switch {
	case c.unreadable && f.Class == Sensitive:
		f.Note = noteUnreadable
	case f.Kind == Deleted && f.Class == Sensitive:
		// What is refused is committing its removal.
		f.Note = noteSensitiveDeleted
	}
	if c.link {
		f.Note = joinNote(f.Note, noteSymlink)
	}
	if f.Kind == Conflict {
		f.Selectable = false
		f.Note = joinNote(f.Note, noteConflict)
	}
	if f.Kind == Renamed {
		if _, in := homeRelative(f.Orig, p.r.Pathspec); !in {
			f.Note = joinNote(f.Note, noteOrigOutside)
		}
	}
}

// pathIssue is why the full path may not be selected ("" when it may).
func pathIssue(p string, special bool) string {
	switch {
	case special:
		return noteSpecial
	case validPath(p) != nil:
		return noteBadPath
	}
	if _, found := mask.Credential(p); found {
		return noteCredName
	}
	return ""
}

func joinNote(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

func selectable(c Class) bool {
	return c == Owned || c == Other || c == Vault
}

// Classify classifies a file by its home-relative path, its first bytes
// (head, at most 4 KiB) and whether its whole content holds key material
// (an age identity or a private-key header), in the pinned order: state dir, sensitive, vault, unusual
// name, owned, other. It returns the class and its note.
func Classify(homeRel string, head []byte, ageIdentity bool) (Class, string) {
	switch {
	case actions.InStateDir(homeRel):
		return StateDir, noteState
	case ageIdentity || keyMaterial(head) || sensitiveName(homeRel):
		return Sensitive, noteSensitive
	case bytes.HasPrefix(head, ageVault):
		return Vault, noteVault
	case actions.HasControl(homeRel):
		return Unusual, noteUnusual
	}
	if _, found := mask.Credential(homeRel); found {
		return Unusual, noteCredName
	}
	if owned(homeRel) {
		return Owned, ""
	}
	return Other, noteOther
}

// sensitiveName: basename key.txt, *.pem, id_* except *.pub, .env or
// .env.*, in any case.
func sensitiveName(rel string) bool {
	b := strings.ToLower(path.Base(rel))
	switch {
	case b == "key.txt", b == ".env", strings.HasSuffix(b, ".pem"), strings.HasPrefix(b, ".env."):
		return true
	}
	return strings.HasPrefix(b, "id_") && !strings.HasSuffix(b, ".pub")
}

// owned reports whether rel is in the agentsync-owned set (D3).
func owned(rel string) bool {
	first, rest, nested := strings.Cut(rel, "/")
	if !nested {
		return ownedFiles[rel]
	}
	return ownedDirs[first] && rest != ""
}

// DisplayName is how a path is shown: itself when it is printable valid
// UTF-8, else quoted with " (unusual name)", so two names never render
// alike (a plain name that ends in a DisplayName suffix is quoted too). A
// name holding a credential is shown masked with " (name looks like a
// credential)". Argv keeps the original path.
func DisplayName(p string) string {
	shown := p
	if actions.HasControl(p) || strings.HasSuffix(p, suffixUnusual) || strings.HasSuffix(p, suffixCred) {
		shown = strconv.QuoteToASCII(p) + suffixUnusual
	}
	if _, found := mask.Credential(p); found {
		shown = maskedName(shown) + suffixCred
	}
	return shown
}

// maskedName is mask.Text(s), or a placeholder when Text leaves a
// credential Credential still finds (an age identity or a PEM header).
func maskedName(s string) string {
	m := mask.Text(s)
	if _, still := mask.Credential(m); still {
		return hiddenName
	}
	return m
}

// content is what inspect read of a worktree file.
type content struct {
	head []byte
	// age is set when the file holds key material (an age identity or a
	// private-key header) or could not be read (fail closed); unreadable
	// tells the two apart for the note.
	age, unreadable bool
	link, special   bool
}

// unreadable is a file inspect could not read: Sensitive, never selectable.
var unreadable = content{age: true, unreadable: true}

// inspect reads rel under top: its first 4 KiB and a key-material scan
// (age identity, private-key header) of the whole file. A symlink (or a path whose parent resolves outside
// top) is not read; a directory or special file is not read; a missing
// file has no content; a failed read counts as an age identity and is
// marked unreadable.
func inspect(top, rel string) content {
	if strings.HasSuffix(rel, "/") {
		return content{special: true}
	}
	full := filepath.Join(top, filepath.FromSlash(rel))
	if !parentWithin(top, full) {
		return content{link: true}
	}
	f, kind, err := openRegular(full)
	switch {
	case err != nil:
		return unreadable
	case kind == fileLink:
		return content{link: true}
	case kind == fileSpecial:
		return content{special: true}
	case f == nil:
		return content{}
	}
	defer func() { _ = f.Close() }()
	head, err := io.ReadAll(io.LimitReader(f, headSize))
	if err != nil {
		return unreadable
	}
	age, err := scanKeys(io.MultiReader(bytes.NewReader(head), f))
	if err != nil {
		return unreadable
	}
	return content{head: head, age: age}
}

// parentWithin reports whether full's parent directory, resolved, lies
// within top. An unresolvable parent (a deleted directory) has nothing to
// read and counts as within.
func parentWithin(top, full string) bool {
	dir, err := filepath.EvalSymlinks(filepath.Dir(full))
	if err != nil {
		return true
	}
	return within(top, dir)
}

type fileKind int

const (
	fileMissing fileKind = iota
	fileRegular
	fileLink
	fileSpecial
)

// openRegular opens p only when os.Lstat says it is a regular file, the
// open itself refuses a symlink or non-regular file swapped in after the
// Lstat (openRegularFile), and the opened file is the one Lstat saw. A
// missing path, a symlink or a special file returns a nil file and no
// error.
func openRegular(p string) (*os.File, fileKind, error) {
	fi, err := os.Lstat(p)
	switch {
	case errors.Is(err, os.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
		return nil, fileMissing, nil
	case err != nil:
		return nil, fileRegular, err
	case fi.Mode()&os.ModeSymlink != 0:
		return nil, fileLink, nil
	case !fi.Mode().IsRegular():
		return nil, fileSpecial, nil
	}
	f, err := openRegularFile(p)
	if err != nil {
		return nil, fileRegular, err
	}
	if st, err := f.Stat(); err != nil || !os.SameFile(fi, st) {
		_ = f.Close()
		return nil, fileRegular, errChanged
	}
	return f, fileRegular, nil
}

// regularOnly returns f when fstat calls it a regular file, else closes
// it and refuses.
func regularOnly(f *os.File) (*os.File, error) {
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		_ = f.Close()
		return nil, errNotRegular
	}
	return f, nil
}

// ReadRegular reads at most limit bytes of the regular file name, never
// through a symlink and never blocking on a FIFO: name is Lstat'ed, opened
// with O_NOFOLLOW|O_NONBLOCK where the system has them, and the opened file
// must be the regular file Lstat saw. A symlink is ErrSymlink, a directory
// or special file (or one swapped in after the Lstat) ErrNotRegular, and a
// missing file wraps os.ErrNotExist. Callers that need to know whether the
// file was longer pass limit+1.
func ReadRegular(name string, limit int64) ([]byte, error) {
	f, kind, err := openRegular(name)
	switch {
	case errors.Is(err, errChanged), errors.Is(err, syscall.ELOOP):
		return nil, fmt.Errorf("%w: %w", ErrNotRegular, err)
	case err != nil:
		return nil, err
	case kind == fileLink:
		return nil, ErrSymlink
	case kind == fileSpecial:
		return nil, ErrNotRegular
	case f == nil:
		return nil, &os.PathError{Op: "read", Path: name, Err: os.ErrNotExist}
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, max(limit, 0)))
}

// HasKeyMaterial reports whether the file named name holds an age identity
// ("AGE-SECRET-KEY-") or a private-key header anywhere, streaming the whole file (no size cap) and
// matching across read boundaries. It never follows a symlink: a symlink,
// a special file or a missing path is false. A read error counts as found
// and is returned.
func HasKeyMaterial(name string) (bool, error) {
	f, _, err := openRegular(name)
	if err != nil {
		return true, err
	}
	if f == nil {
		return false, nil
	}
	defer func() { _ = f.Close() }()
	found, err := scanKeys(f)
	if err != nil {
		return true, err
	}
	return found, nil
}

// scanKeys streams r in ageReadBuf reads, carrying the last markerCarry
// bytes over so a marker split across reads is found.
func scanKeys(r io.Reader) (bool, error) {
	buf := make([]byte, markerCarry+ageReadBuf)
	keep := 0
	for {
		n, err := r.Read(buf[keep : keep+ageReadBuf])
		total := keep + n
		if keyMaterial(buf[:total]) {
			return true, nil
		}
		if errors.Is(err, io.EOF) {
			return false, nil
		} else if err != nil {
			return false, err
		}
		keep = min(total, markerCarry)
		copy(buf, buf[total-keep:total])
	}
}
