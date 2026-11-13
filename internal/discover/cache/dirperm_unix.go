//go:build unix

package cache

import (
	"os"
	"syscall"
)

// ownedByUs reports whether fi is owned by the current uid.
func ownedByUs(fi os.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Geteuid()
}

// enforceDirPerm tightens a loose dir we own to 0700 and refuses one owned
// by another uid, which could hold entries planted by that user.
func enforceDirPerm(dir string, fi os.FileInfo) error {
	if !ownedByUs(fi) {
		return errForeignDir(dir)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return os.Chmod(dir, dirMode)
	}
	return nil
}
