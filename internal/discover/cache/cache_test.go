package cache

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
)

func skipUnlessUnixUser(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a non-root unix user")
	}
}

func openTemp(t *testing.T) (*Cache, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "c")
	c, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return c, dir
}

func TestPutGetRoundTrip(t *testing.T) {
	c, _ := openTemp(t)
	want := []byte("hello\x00world")
	if err := c.Put("k", want); err != nil {
		t.Fatal(err)
	}
	got, ok := c.Get("k", time.Hour)
	if !ok || !bytes.Equal(got, want) {
		t.Fatalf("got %q, %v", got, ok)
	}
	if _, ok := c.Get("other", time.Hour); ok {
		t.Fatal("unknown key hit")
	}
	if err := c.Put("k", []byte("v2")); err != nil {
		t.Fatal(err)
	}
	if got, _ := c.Get("k", time.Hour); string(got) != "v2" {
		t.Fatalf("overwrite: %q", got)
	}
}

func TestExpiredEntryIsMiss(t *testing.T) {
	c, _ := openTemp(t)
	now := time.Now()
	c.now = func() time.Time { return now }
	if err := c.Put("k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	c.now = func() time.Time { return now.Add(2 * time.Minute) }
	if _, ok := c.Get("k", time.Minute); ok {
		t.Fatal("expired entry hit")
	}
	if _, ok := c.Get("k", time.Hour); !ok {
		t.Fatal("fresh entry missed")
	}
}

func TestFilesAre0600DirIs0700(t *testing.T) {
	skipUnlessUnixUser(t)
	c, dir := openTemp(t)
	if err := c.Put("k", []byte("v")); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir %v %v", fi, err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Fatalf("want 1 entry (no temp left), got %d", len(ents))
	}
	fi, _ := ents[0].Info()
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %v", fi.Mode())
	}
}

func TestSymlinkedCacheDirRefused(t *testing.T) {
	skipUnlessUnixUser(t)
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(link); err == nil {
		t.Fatal("symlinked dir accepted")
	}
}

func TestSymlinkedEntryNotFollowed(t *testing.T) {
	skipUnlessUnixUser(t)
	c, dir := openTemp(t)
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("precious"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(dir, entryName("k"))); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get("k", time.Hour); ok {
		t.Fatal("Get followed symlink")
	}
	if err := c.Put("k", []byte("new")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(victim); string(b) != "precious" {
		t.Fatalf("victim overwritten: %q", b)
	}
	if got, ok := c.Get("k", time.Hour); !ok || string(got) != "new" {
		t.Fatalf("got %q %v", got, ok)
	}
}

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

func TestOversizeEntryRefused(t *testing.T) {
	c, dir := openTemp(t)
	if err := c.Put("k", make([]byte, maxEntry+1)); err == nil {
		t.Fatal("oversize Put accepted")
	}
	if err := c.Put("ok", make([]byte, maxEntry)); err != nil {
		t.Fatalf("max-size Put: %v", err)
	}
	// An oversize file planted on disk is a miss, not an OOM.
	big := append([]byte(header+" 0 "), bytes.Repeat([]byte("x"), maxEntry+headerMax)...)
	if err := os.WriteFile(filepath.Join(dir, entryName("big")), big, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get("big", time.Hour); ok {
		t.Fatal("oversize entry hit")
	}
}

func TestCorruptEntryIsMiss(t *testing.T) {
	c, dir := openTemp(t)
	for name, content := range map[string]string{
		"empty": "", "garbage": "not a cache entry", "nonewline": header + " 1 abc",
	} {
		if err := os.WriteFile(filepath.Join(dir, entryName(name)), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok := c.Get(name, time.Hour); ok {
			t.Fatalf("%s hit", name)
		}
	}
	if err := c.Put("k", []byte("value")); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, entryName("k"))
	b, _ := os.ReadFile(p)
	b[len(b)-1] ^= 0xff
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get("k", time.Hour); ok {
		t.Fatal("tampered body hit")
	}
}

func TestDefaultDir(t *testing.T) {
	d, err := DefaultDir()
	if err != nil {
		t.Skip(err)
	}
	if filepath.Base(d) != "agentsync-tui" {
		t.Fatal(d)
	}
}
