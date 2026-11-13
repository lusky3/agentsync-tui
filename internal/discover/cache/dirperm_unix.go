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

// enforceDirPerm refuses a dir owned by another uid, which could hold
// entries planted by that user, and tightens a loose dir we own to 0700
// through root. It reports whether the dir had a group or world write bit.
func enforceDirPerm(root *os.Root, dir string, fi os.FileInfo) (bool, error) {
	if !ownedByUs(fi) {
		return false, errForeignDir(dir)
	}
	perm := fi.Mode().Perm()
	if perm&0o077 != 0 {
		if err := root.Chmod(".", dirMode); err != nil {
			return false, err
		}
	}
	return perm&0o022 != 0, nil
}
