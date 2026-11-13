//go:build unix

package source

import (
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A FIFO named like a server file must be reported, not opened: opening it
// would block LoadServers (and the UI, which calls it inside Update) forever.
func TestLoadServersSkipsFIFO(t *testing.T) {
	h := home(t, map[string]string{"stdio.toml": "good.toml"})
	if err := syscall.Mkfifo(filepath.Join(h, "mcp", "x.toml"), 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	type out struct {
		s   []Server
		err error
	}
	ch := make(chan out, 1)
	go func() {
		s, err := LoadServers(h)
		ch <- out{s, err}
	}()
	select {
	case o := <-ch:
		if len(o.s) != 1 || o.s[0].ID != "good" {
			t.Fatalf("servers: %+v", o.s)
		}
		if o.err == nil || !strings.Contains(o.err.Error(), "x.toml: not a regular file") {
			t.Fatalf("want the FIFO reported, got %v", o.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("LoadServers hung on the FIFO")
	}
}
