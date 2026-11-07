//go:build !unix

package gitrepo

import (
	"errors"
	"os"
)

// openRegularFile opens name for reading and refuses anything that is not
// a regular file. Lstat and the SameFile check in openRegular guard the
// symlink case on systems without O_NOFOLLOW.
func openRegularFile(name string) (*os.File, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	return regularOnly(f)
}

// openDir opens name for listing once Stat calls it a directory, so a FIFO
// is never opened.
func openDir(name string) (*os.File, error) {
	fi, err := os.Stat(name)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, errors.New("not a directory")
	}
	return os.Open(name)
}
