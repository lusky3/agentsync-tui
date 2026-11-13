//go:build !unix

package cache

import "os"

func ownedByUs(os.FileInfo) bool { return true }

// enforceDirPerm is a no-op where there is no uid or mode model.
func enforceDirPerm(string, os.FileInfo) error { return nil }
