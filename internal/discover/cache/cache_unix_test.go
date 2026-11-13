//go:build unix

package cache

import (
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestFIFOEntryIsMissWithoutBlocking(t *testing.T) {
	skipUnlessUnixUser(t)
	c, dir := openTemp(t)
	if err := syscall.Mkfifo(filepath.Join(dir, entryName("k")), 0o600); err != nil {
		t.Skip(err)
	}
	done := make(chan bool, 1)
	go func() { _, ok := c.Get("k", time.Hour); done <- ok }()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("fifo hit")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Get blocked on a FIFO")
	}
}
