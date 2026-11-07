//go:build unix

package gitrepo

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// TestOpenRegularFileNeverBlocksOnFIFO calls the open helper directly on a
// FIFO and on a symlink to one (what a swap after Lstat would leave): it
// must return at once and refuse both.
func TestOpenRegularFileNeverBlocksOnFIFO(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(fifo, link); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{fifo, link} {
		done := make(chan error, 1)
		go func() {
			f, err := openRegularFile(p)
			if f != nil {
				_ = f.Close()
				err = errors.New("opened a non-regular file")
			}
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("%s: opened, want refused", filepath.Base(p))
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: open blocked", filepath.Base(p))
		}
	}
	regular := write(t, dir, "r", "x")
	f, err := openRegularFile(regular)
	if err != nil {
		t.Fatalf("regular file refused: %v", err)
	}
	_ = f.Close()
}
