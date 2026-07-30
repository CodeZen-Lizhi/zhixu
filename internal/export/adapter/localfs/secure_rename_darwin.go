//go:build darwin

package localfs

import "golang.org/x/sys/unix"

func renameAtNoReplace(fromFD int, from string, toFD int, to string) error {
	return unix.RenameatxNp(fromFD, from, toFD, to, unix.RENAME_EXCL)
}
