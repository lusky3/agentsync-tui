//go:build unix

package ui

import "syscall"

// openNoFollow makes OpenFile fail on a symlink at the log path instead of
// following it (the Lstat check before it is advisory; this closes the gap).
const openNoFollow = syscall.O_NOFOLLOW
