//go:build !unix

package gitrepo

import "os"

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
