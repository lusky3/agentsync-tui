//go:build !unix

package ui

// openNoFollow: no O_NOFOLLOW on this platform; the Lstat check in openLog
// is the only symlink guard.
const openNoFollow = 0
