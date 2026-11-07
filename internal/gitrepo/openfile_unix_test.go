//go:build unix

package gitrepo

import (
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
