package gitrepo

import (
	"math"
	"strconv"
	"strings"
)

// PatchMasker masks the lines of one file's unified diff in order, as
// MaskPatchLine does, and also masks a config pair's value as an env or
// header value (whatever its key) when the line lies under an env,
// environment or headers [table] (Ruling 48). That table's header is
// usually outside the hunk, so it is read from the file's post-image
// (contextOf), and headers inside the hunk are followed too: an added or
// context line is under such a table when the post-image or the hunk says
// so, a removed line when the hunk says so (fail closed: the hunk's start
// takes the post-image's state). Without a post-image ("") only the hunk's
// own headers count.
type PatchMasker struct {
	homeRel        string
	env            map[int]bool
	next           int // post-image line number of the next added or context line
	oldLeft        int // removed or context lines left in the hunk
	newLeft        int // added or context lines left in the hunk
	oldEnv, newEnv bool
}

// NewPatchMasker masks a git diff of homeRel whose post-image is post.
func NewPatchMasker(homeRel, post string) *PatchMasker {
	return &PatchMasker{homeRel: homeRel, env: contextOf(post).env}
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
	return maskPatchLine(m.homeRel, line, m.step(line))
}

// step follows the hunk through one line and reports whether the line
// lies under an env, environment or headers table.
func (m *PatchMasker) step(line string) bool {
	if strings.HasPrefix(line, "@@") || m.oldLeft <= 0 && m.newLeft <= 0 {
		m.startHunk(line)
		return false
	}
	if line == "" {
		return false
	}
	body := line[1:]
	hdr, isHdr := envHeader(body)
	switch line[0] {
	case '+':
		if isHdr {
			m.newEnv = hdr
		}
		m.newLeft--
		post := m.postEnv()
		return m.newEnv || post
	case '-':
		if isHdr {
			m.oldEnv = hdr
		}
		m.oldLeft--
		return m.oldEnv
	case ' ':
		if isHdr {
			m.oldEnv, m.newEnv = hdr, hdr
		}
		m.oldLeft--
		m.newLeft--
		post := m.postEnv()
		return m.oldEnv || m.newEnv || post
	}
	return false
}

// startHunk reads a hunk header: its counts and starting line number, and
// the post-image's state there.
func (m *PatchMasker) startHunk(line string) {
	h := hunkRe.FindStringSubmatch(line)
	if h == nil {
		m.oldLeft, m.newLeft = 0, 0
		return
	}
	m.oldLeft, m.newLeft = hunkCount(h[2]), hunkCount(h[4])
	m.next, _ = strconv.Atoi(h[3])
	m.oldEnv = m.env[m.next]
	m.newEnv = m.oldEnv
}

// hunkCount is a hunk header's line count: 1 when it is left out.
func hunkCount(s string) int {
	if s == "" {
		return 1
	}
	n, _ := strconv.Atoi(strings.TrimSpace(s))
	return n
}

// postEnv reports the post-image's state of the next added or context
// line, and moves past it.
func (m *PatchMasker) postEnv() bool {
	e := m.env[m.next]
	m.next++
	return e
}
