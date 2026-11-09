//go:build unix

package gitrepo

import (
	"os"
	"syscall"
)

// openRegularFile opens name for reading without following a symlink
// (O_NOFOLLOW) and without blocking on a FIFO (O_NONBLOCK), then refuses
// anything fstat does not call a regular file. O_NONBLOCK has no effect on
// a regular file's reads.
func openRegularFile(name string) (*os.File, error) {
	f, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return regularOnly(f)
}

// openDir opens name for listing without blocking on a FIFO (O_NONBLOCK),
// refusing anything that is not a directory (O_DIRECTORY).
func openDir(name string) (*os.File, error) {
	return os.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_DIRECTORY, 0)
}

// openRegularRootFile opens name inside root for reading and refuses
// anything fstat does not call a regular file. It does not refuse a
// symlink: os.Root.OpenFile adds O_NOFOLLOW itself but still follows a
// final-component link whose target stays inside root. openChecked's Lstat
// and SameFile check refuse a symlinked file; use it through openRegularIn.
func openRegularRootFile(root *os.Root, name string) (*os.File, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return regularOnly(f)
}
