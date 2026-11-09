//go:build unix

package gitrepo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// TestReadRegularRefusesFIFOAndSymlinks: a FIFO, a symlink to one and a
// symlink to a regular file are refused at once; a regular file is read
// up to max bytes.
func TestReadRegularRefusesFIFOAndSymlinks(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	regular := write(t, dir, "r", "0123456789")
	for name, target := range map[string]string{"fifo-link": fifo, "file-link": regular} {
		if err := os.Symlink(target, filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, p := range []string{fifo, filepath.Join(dir, "fifo-link"), filepath.Join(dir, "file-link")} {
		readRefusedPromptly(t, p)
	}
	if _, err := ReadRegular(filepath.Join(dir, "fifo-link"), 1); !errors.Is(err, ErrSymlink) {
		t.Errorf("symlink error %v, want ErrSymlink", err)
	}
	if _, err := ReadRegular(fifo, 1); !errors.Is(err, ErrNotRegular) {
		t.Errorf("fifo error %v, want ErrNotRegular", err)
	}
	if _, err := ReadRegular(filepath.Join(dir, "missing"), 1); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing error %v, want ErrNotExist", err)
	}
	b, err := ReadRegular(regular, 4)
	if err != nil || string(b) != "0123" {
		t.Errorf("capped read = %q, %v", b, err)
	}
	if b, err := ReadRegular(regular, 100); err != nil || string(b) != "0123456789" {
		t.Errorf("full read = %q, %v", b, err)
	}
}

// readRefusedPromptly asserts ReadRegular refuses p within five seconds.
func readRefusedPromptly(t *testing.T, p string) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		_, err := ReadRegular(p, 100)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Errorf("%s: read, want refused", filepath.Base(p))
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: read blocked", filepath.Base(p))
	}
}

// BackupAuthor never blocks on agentsync.toml as a FIFO or a symlink to
// one; both, like an oversize file, give the default author.
func TestBackupAuthorBoundedRead(t *testing.T) {
	cases := map[string]func(home string) error{
		"fifo": func(home string) error { return syscall.Mkfifo(filepath.Join(home, tomlName), 0o600) },
		"symlink to fifo": func(home string) error {
			fifo := filepath.Join(home, "fifo")
			if err := syscall.Mkfifo(fifo, 0o600); err != nil {
				return err
			}
			return os.Symlink(fifo, filepath.Join(home, tomlName))
		},
		"oversize": func(home string) error {
			body := "[destination_directory_git_backup]\nauthor_email = \"big@example.invalid\"\n" +
				"# " + strings.Repeat("x", maxConfigBytes) + "\n"
			return os.WriteFile(filepath.Join(home, tomlName), []byte(body), 0o600)
		},
	}
	for name, mk := range cases {
		home := t.TempDir()
		if err := mk(home); err != nil {
			t.Skipf("%s: %v", name, err)
		}
		done := make(chan string, 1)
		go func() { done <- BackupAuthor(home) }()
		select {
		case got := <-done:
			if got != DefaultBackupAuthor {
				t.Errorf("%s: %q, want the default", name, got)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: BackupAuthor blocked", name)
		}
	}
}

// A home that is a FIFO (or a symlink to one) is refused at once: opening
// it to list entries would wait for a writer forever, outside any bound.
func TestResolveHomeNeverBlocksOnFIFO(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "home")
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
			_, _, err := resolveHome(p)
			done <- err
		}()
		select {
		case err := <-done:
			if err == nil {
				t.Errorf("%s: a FIFO home was accepted", filepath.Base(p))
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: resolveHome blocked on a FIFO", filepath.Base(p))
		}
	}
}

// TestReadRegularInStaysInsideTop: a parent directory that is a symlink
// leaving top (what a swap after a containment check leaves) fails the
// read, where ReadRegular on the joined path would read the outside file.
// A parent symlink inside top is followed; the file itself never is; a
// FIFO is refused at once.
func TestReadRegularInStaysInsideTop(t *testing.T) {
	top, outside := t.TempDir(), t.TempDir()
	write(t, outside, "x.md", "OUTSIDE")
	if err := os.Symlink(outside, filepath.Join(top, "skills")); err != nil {
		t.Fatal(err)
	}
	if b, err := ReadRegular(filepath.Join(top, "skills", "x.md"), 100); err != nil || string(b) != "OUTSIDE" {
		t.Fatalf("control: ReadRegular through the parent link = %q, %v", b, err)
	}
	if b, err := ReadRegularIn(top, "skills/x.md", 100); err == nil || strings.Contains(string(b), "OUTSIDE") {
		t.Errorf("ReadRegularIn read outside top: %q, %v", b, err)
	}
	write(t, top, "real/y.md", "INSIDE")
	if err := os.Symlink("real", filepath.Join(top, "alias")); err != nil {
		t.Fatal(err)
	}
	if b, err := ReadRegularIn(top, "alias/y.md", 100); err != nil || string(b) != "INSIDE" {
		t.Errorf("parent link inside top = %q, %v", b, err)
	}
	if err := os.Symlink("y.md", filepath.Join(top, "real", "z.md")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRegularIn(top, "real/z.md", 100); !errors.Is(err, ErrSymlink) {
		t.Errorf("file link error %v, want ErrSymlink", err)
	}
	if _, err := ReadRegularIn(top, "real/missing", 100); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing error %v, want ErrNotExist", err)
	}
	if err := syscall.Mkfifo(filepath.Join(top, "real", "fifo"), 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := ReadRegularIn(top, "real/fifo", 100)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNotRegular) {
			t.Errorf("fifo error %v, want ErrNotRegular", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ReadRegularIn blocked on a FIFO")
	}
}

// TestRootOpenFollowsInRootLinkOpenCheckedRefuses pins where the "never a
// symlinked file" guarantee lives: openRegularRootFile alone follows a
// final-component link whose target is inside the root (os.Root does), and
// openRegularIn (openChecked's Lstat and SameFile) refuses it.
func TestRootOpenFollowsInRootLinkOpenCheckedRefuses(t *testing.T) {
	top := t.TempDir()
	write(t, top, "real.md", "INSIDE")
	if err := os.Symlink("real.md", filepath.Join(top, "link.md")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(top)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	f, err := openRegularRootFile(root, "link.md")
	if err != nil {
		t.Fatalf("openRegularRootFile did not follow the in-root link (%v): os.Root semantics changed, revisit these comments", err)
	}
	_ = f.Close()
	f, kind, err := openRegularIn(top, "link.md")
	if f != nil {
		_ = f.Close()
	}
	if f != nil || kind != fileLink || err != nil {
		t.Errorf("openRegularIn(link) = file %v, kind %v, err %v; want no file, fileLink", f != nil, kind, err)
	}
}

// TestReadRegularInContextStops: an ended context stops the read with its
// error; a live one reads the file.
func TestReadRegularInContextStops(t *testing.T) {
	top := t.TempDir()
	write(t, top, "a.md", strings.Repeat("x", 1<<16))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ReadRegularInContext(ctx, top, "a.md", 1<<20); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled read error %v, want context.Canceled", err)
	}
	if b, err := ReadRegularInContext(context.Background(), top, "a.md", 1<<20); err != nil || len(b) != 1<<16 {
		t.Errorf("live read = %d bytes, %v", len(b), err)
	}
}
