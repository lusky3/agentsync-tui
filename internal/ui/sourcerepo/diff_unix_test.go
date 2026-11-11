//go:build unix

package sourcerepo

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/lusky3/agentsync-tui/internal/gitrepo/gittest"
)

// The diff of a config file is masked in the context of its post-image
// (an env table's header may lie outside every hunk). When that read
// fails after git printed the patch (the file swapped for a FIFO here),
// the diff is withheld rather than shown without the context.
func TestConfigDiffWithheldWithoutPostImage(t *testing.T) {
	home := newRepo(t)
	gittest.Commit(t, home, "env", map[string]string{tomlX: envTableTOML})
	write(t, home, tomlX, envTableTOML+"name = '"+opaqueWord+"'\n")
	f := open(t, home)
	f.s.list.Cursor = rowOf(f.s, tomlX)
	var once sync.Once
	afterContained = func() {
		once.Do(func() {
			p := filepath.Join(home, filepath.FromSlash(tomlX))
			if err := os.Remove(p); err != nil {
				t.Error(err)
			}
			if err := syscall.Mkfifo(p, 0o600); err != nil {
				t.Error(err)
			}
		})
	}
	t.Cleanup(func() { afterContained = nil })
	press(f.s, "d")
	if v := view(f.s); strings.Contains(v, opaqueWord) || !strings.Contains(v, "not shown") {
		t.Errorf("diff shown without its post-image:\n%s", v)
	}
}
