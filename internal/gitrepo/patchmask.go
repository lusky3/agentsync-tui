package gitrepo

import (
	"math"
	"strconv"
	"strings"

	"github.com/lusky3/agentsync-tui/internal/mask"
)

// PatchMasker masks the lines of one file's unified diff in order, as
// MaskPatchLine does, and also masks a config pair's value as an env or
// header value (whatever its key) when the line lies under an env,
// environment or headers [table] (Ruling 48). That table's header is
// usually outside the hunk, so it is read from the file's post-image.
//
// An added line is under such a table when the post-image says so
// (contextOf) or the hunk's own headers do. A removed line is judged by
// the pre-image's table, followed in order (tableState): the unchanged
// lines before and between hunks are the post-image's, so they are read
// from it, and the hunk's removed and context lines follow; a header-like
// line inside a multi-line string is no header. When that is not known
// (no post-image, or one that ends before the hunk), removed and context
// lines are masked as env values (fail closed). A context line is masked
// when either side puts it under such a table.
//
// A line in the body of a multi-line string opened by a secret-named,
// header or env key (its closing line included) is the secret's and is
// masked whole, whatever it looks like: on the added side as the
// post-image says (contextOf), on the removed side as the pre-image's
// state is followed, each side on its own; a removed or context line
// whose pre-image state is not known counts as one (fail closed).
type PatchMasker struct {
	homeRel   string
	env       map[int]bool
	bodies    map[int]mlOpener // post-image lines in a secret multi-line body
	post      []string         // the post-image's lines; nil when unknown
	havePost  bool
	next      int // post-image line number of the next added or context line
	oldLeft   int // removed or context lines left in the hunk
	newLeft   int // added or context lines left in the hunk
	newEnv    bool
	old       tableState // the pre-image's table at the next removed or context line
	oldKnown  bool
	postGapAt int // the post-image line the next gap starts at
}

// tableState follows TOML lines in order: whether the current [table] is
// an env, environment or headers table, the closing quotes of the
// multi-line string open, if any (no header inside it), and whether that
// string is a secret's (opened by a secret-named, header or env key).
type tableState struct {
	env    bool
	delim  string
	secret bool
}

// inSecretBody reports whether the next line lies in a secret's
// multi-line string body.
func (s tableState) inSecretBody() bool { return s.delim != "" && s.secret }

// feed moves the state past one line.
func (s *tableState) feed(l string) {
	if s.delim != "" {
		if delimIndex(l, s.delim) >= 0 {
			s.delim = ""
		}
		return
	}
	if e, ok := envHeader(l); ok {
		s.env = e
		return
	}
	if o := openerOf(l, s.env); o != nil {
		s.delim, s.secret = o.delim, o.auth || o.secret || o.env
	}
}

// NewPatchMasker masks a git diff of homeRel whose post-image is post
// ("" when unknown; a post-image cut short must end at a whole line).
func NewPatchMasker(homeRel, post string) *PatchMasker {
	c := contextOf(post)
	m := &PatchMasker{homeRel: homeRel, env: c.env, bodies: c.bodies, postGapAt: 1}
	if post != "" {
		m.havePost = true
		m.post = strings.Split(strings.TrimSuffix(post, "\n"), "\n")
	}
	return m
}

// NewAddedMasker masks content shown as added lines with no hunk header
// (an untracked file): content is its own post-image.
func NewAddedMasker(homeRel, content string) *PatchMasker {
	m := NewPatchMasker(homeRel, content)
	m.next, m.newLeft = 1, math.MaxInt
	return m
}

// Line masks the next line of the diff.
func (m *PatchMasker) Line(line string) string {
	at := m.step(line)
	if at.body {
		return maskedBodyLine(line)
	}
	return maskPatchLine(m.homeRel, line, at.env)
}

// maskedBodyLine is a diff line in a secret's multi-line body: its marker
// and indent, then the mask.
func maskedBodyLine(line string) string {
	rest := line[1:]
	return line[:1] + rest[:len(rest)-len(strings.TrimLeft(rest, " \t"))] + mask.Mask
}

// lineAt is where a diff line lies: under an env, environment or headers
// table (env), in a secret's multi-line string body (body).
type lineAt struct{ env, body bool }

// or is where a context line lies: either side's answer counts.
func (a lineAt) or(b lineAt) lineAt { return lineAt{env: a.env || b.env, body: a.body || b.body} }

// step follows the hunk through one line and reports where it lies.
func (m *PatchMasker) step(line string) lineAt {
	if strings.HasPrefix(line, "@@") || m.oldLeft <= 0 && m.newLeft <= 0 {
		m.startHunk(line)
		return lineAt{}
	}
	if line == "" {
		return lineAt{}
	}
	switch line[0] {
	case '+':
		return m.added(line[1:])
	case '-':
		m.oldLeft--
		return m.oldLine(line[1:])
	case ' ':
		m.oldLeft--
		old := m.oldLine(line[1:])
		return m.added(line[1:]).or(old)
	}
	return lineAt{}
}

// added follows an added (or context) line on the post-image's side.
func (m *PatchMasker) added(body string) lineAt {
	if e, ok := envHeader(body); ok {
		m.newEnv = e
	}
	m.newLeft--
	post := m.env[m.next]
	_, inBody := m.bodies[m.next]
	m.next++
	m.postGapAt = m.next
	return lineAt{env: m.newEnv || post, body: inBody}
}

// oldLine follows a removed (or context) line on the pre-image's side: it
// is under an env table, or in a secret's body, when the state before it
// says so, or when the state is not known.
func (m *PatchMasker) oldLine(body string) lineAt {
	at := lineAt{env: m.old.env || !m.oldKnown, body: m.old.inSecretBody() || !m.oldKnown}
	m.old.feed(body)
	return at
}

// startHunk reads a hunk header: its counts and starting line numbers,
// the post-image's state for added lines, and the pre-image's state for
// removed ones (gapState).
func (m *PatchMasker) startHunk(line string) {
	h := hunkRe.FindStringSubmatch(line)
	if h == nil {
		m.oldLeft, m.newLeft = 0, 0
		return
	}
	oldStart, _ := strconv.Atoi(h[1])
	m.oldLeft, m.newLeft = hunkCount(h[2]), hunkCount(h[4])
	m.next, _ = strconv.Atoi(h[3])
	m.newEnv = m.env[m.next]
	before := oldStart // pre-image lines before the hunk
	if m.oldLeft > 0 {
		before--
	}
	gapEnd := m.next // post-image lines before the hunk
	if m.newLeft > 0 {
		gapEnd--
	}
	m.gapState(before, gapEnd)
}

// gapState brings the pre-image's state to the hunk: no lines before it
// is the empty state; otherwise the unchanged lines since the last hunk
// (the post-image's lines postGapAt..gapEnd) are fed to it, and without
// those lines the state is not known.
func (m *PatchMasker) gapState(before, gapEnd int) {
	switch {
	case before <= 0:
		m.old, m.oldKnown = tableState{}, true
	case !m.havePost || gapEnd > len(m.post) || m.postGapAt < 1:
		m.oldKnown = false
	default:
		for i := m.postGapAt; i <= gapEnd; i++ {
			m.old.feed(m.post[i-1])
		}
		if m.postGapAt == 1 {
			m.oldKnown = true
		}
	}
	m.postGapAt = gapEnd + 1
}

// hunkCount is a hunk header's line count: 1 when it is left out.
func hunkCount(s string) int {
	if s == "" {
		return 1
	}
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}
