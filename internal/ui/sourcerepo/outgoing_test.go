package sourcerepo

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lusky3/agentsync-tui/internal/gitrepo"
	"github.com/lusky3/agentsync-tui/internal/gitrepo/gittest"
	"github.com/lusky3/agentsync-tui/internal/runner"
)

// outgoingFixture is a tracked home with commits outgoing files each, and
// a netInput whose cat-file reads go to catFile.
func outgoingFixture(t *testing.T, commits, files int, catFile func() runner.Result) (netInput, string, string) {
	t.Helper()
	home, _ := newTracked(t)
	base := head(t, home)
	for c := range commits {
		m := map[string]string{}
		for i := range files {
			m[fmt.Sprintf("skills/m/f%03d.md", i)] = fmt.Sprintf("x %d\n", c)
		}
		gittest.Commit(t, home, fmt.Sprintf("c%d", c), m)
	}
	f := open(t, home)
	g := &recGit{inner: gittest.NewGit(t), override: func(args []string) (runner.Result, bool) {
		if gitCmd(args) == "cat-file" {
			return catFile(), true
		}
		return runner.Result{}, false
	}}
	return netInput{repo: f.s.repo, git: g, limit: 30 * time.Second, op: opPush}, base, head(t, home)
}

func heapNow() uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// Each section's post-image is read, scanned and dropped before the next
// is read: with 64 sections of 4 MiB post-images the heap never holds more
// than a few of them (all of them would be 256 MiB, their 1 MiB prefixes
// 64 MiB).
func TestOutgoingPostImagesStreamed(t *testing.T) {
	const big = 4 << 20
	var mu sync.Mutex
	var peak uint64
	in, base, tip := outgoingFixture(t, 1, 64, func() runner.Result {
		h := heapNow()
		mu.Lock()
		peak = max(peak, h)
		mu.Unlock()
		return runner.Result{Stdout: strings.Repeat("a\n", big/2)}
	})
	before := heapNow()
	fs, refusal := scanOutgoing(context.Background(), in, base, tip)
	if refusal != nil {
		t.Fatalf("refused: %q", refusal)
	}
	for _, f := range fs {
		if f.Kind == gitrepo.KindContextNotChecked {
			t.Errorf("context note for a line inside the first 1 MiB: %+v", f)
		}
	}
	if grew := int64(peak) - int64(before); grew > 24<<20 {
		t.Errorf("heap grew %d MiB while reading post-images", grew>>20)
	}
}

// The whole outgoing scan has one deadline on top of each read's.
func TestOutgoingScanOverallBound(t *testing.T) {
	old := outgoingScanBound
	outgoingScanBound = 300 * time.Millisecond
	t.Cleanup(func() { outgoingScanBound = old })
	in, base, tip := outgoingFixture(t, 1, 3, func() runner.Result {
		time.Sleep(200 * time.Millisecond)
		return runner.Result{Stdout: "x\n"}
	})
	_, refusal := scanOutgoing(context.Background(), in, base, tip)
	if len(refusal) == 0 || refusal[0] != whyUnfinished {
		t.Errorf("refusal %q, want %q", refusal, whyUnfinished)
	}
}

// 2000 is a cap on distinct paths, not on file×commit changes: 100 files
// changed in each of 25 commits (2500 changes) are scanned.
func TestOutgoingManyChangesFewPaths(t *testing.T) {
	in, base, tip := outgoingFixture(t, 25, 100, func() runner.Result { return runner.Result{Stdout: "x\n"} })
	if _, refusal := scanOutgoing(context.Background(), in, base, tip); refusal != nil {
		t.Fatalf("refused: %q", refusal)
	}
}

// Past maxChanges file sections the push is refused with its own message.
func TestOutgoingTooManyChanges(t *testing.T) {
	old := maxChanges
	maxChanges = 10
	t.Cleanup(func() { maxChanges = old })
	in, base, tip := outgoingFixture(t, 3, 4, func() runner.Result { return runner.Result{Stdout: "x\n"} })
	_, refusal := scanOutgoing(context.Background(), in, base, tip)
	if len(refusal) == 0 || refusal[0] != whyTooManyChanges {
		t.Errorf("refusal %q, want %q", refusal, whyTooManyChanges)
	}
}

// A post-image cut at 1 MiB gives the soft note only when an added line
// lies past the cut, where its multi-line context is unknown.
func TestBeyondCut(t *testing.T) {
	post := "a\nb\nc" // two complete lines, the third cut
	if beyondCut([]gitrepo.Line{{N: 2, Text: "b"}}, post) {
		t.Error("line 2 is inside the cut post-image")
	}
	if !beyondCut([]gitrepo.Line{{N: 1, Text: "a"}, {N: 3, Text: "c"}}, post) {
		t.Error("line 3 is past the last complete line")
	}
}
