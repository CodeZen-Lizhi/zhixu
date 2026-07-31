package hostcontroller

import (
	"errors"
	"os"
	"syscall"
)

// ValidateStateDirectory rejects symlinks, unexpected owners, and permissive state directories.
func ValidateStateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("controller state directory is unavailable")
	}
	if info.Mode().Perm() != 0o700 {
		return errors.New("controller state directory permissions are invalid")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("controller state directory owner is invalid")
	}
	return nil
}
