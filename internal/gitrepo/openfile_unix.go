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
