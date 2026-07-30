//go:build darwin || linux

package localfs

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"reflect"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

var errPathBindingChanged = errors.New("directory binding changed")

type secureFilesystemHook struct {
	afterWorkspaceLstat func()
	afterChildLstat     func(string)
	afterFileLstat      func(string)
	afterManagedVerify  func()
	afterPreparedVerify func()
}

// secureRoot keeps the workspace directory open. Every descendant is reached
// through an already-open parent descriptor, never by re-resolving a path.
type secureRoot struct {
	fd       int
	rootPath string
	info     fs.FileInfo
	hook     *secureFilesystemHook
}

type secureDir struct {
	fd   int
	info fs.FileInfo
	hook *secureFilesystemHook
}

type managedDirectories struct {
	root      *secureRoot
	knowledge *secureDir
	exports   *secureDir
	staging   *secureDir
}

func openSecureRoot(rootPath string, expected fs.FileInfo, hook *secureFilesystemHook) (*secureRoot, error) {
	fd, err := unix.Open(rootPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	info, err := statFD(fd)
	if err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	if !sameFileInfo(expected, info) {
		_ = unix.Close(fd)
		return nil, errPathBindingChanged
	}
	return &secureRoot{fd: fd, rootPath: rootPath, info: info, hook: hook}, nil
}

func (root *secureRoot) Close() error {
	if root == nil || root.fd < 0 {
		return nil
	}
	err := unix.Close(root.fd)
	root.fd = -1
	return err
}

func (root *secureRoot) assertBinding() error {
	if root == nil || root.fd < 0 {
		return os.ErrClosed
	}
	info, err := os.Lstat(root.rootPath)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !sameFileInfo(root.info, info) {
		return errPathBindingChanged
	}
	return nil
}

func (root *secureRoot) Lstat(relative string) (fs.FileInfo, error) {
	parent, base, err := root.openParent(relative, false)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	return lstatAt(parent.fd, base)
}

func (root *secureRoot) OpenFile(relative string, flags int, mode fs.FileMode) (*os.File, error) {
	parent, base, err := root.openParent(relative, false)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	fd, err := unix.Openat(parent.fd, base, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), base), nil
}

func (root *secureRoot) Remove(relative string) error {
	parent, base, err := root.openParent(relative, false)
	if err != nil {
		return err
	}
	defer parent.Close()
	return unix.Unlinkat(parent.fd, base, 0)
}

func (root *secureRoot) Link(oldRelative, newRelative string) error {
	oldParent, oldBase, err := root.openParent(oldRelative, false)
	if err != nil {
		return err
	}
	defer oldParent.Close()
	newParent, newBase, err := root.openParent(newRelative, false)
	if err != nil {
		return err
	}
	defer newParent.Close()
	return unix.Linkat(oldParent.fd, oldBase, newParent.fd, newBase, 0)
}

func (root *secureRoot) Mkdir(relative string, mode fs.FileMode) error {
	parent, base, err := root.openParent(relative, false)
	if err != nil {
		return err
	}
	defer parent.Close()
	return unix.Mkdirat(parent.fd, base, uint32(mode.Perm()))
}

func (root *secureRoot) ReadDir(relative string) ([]os.DirEntry, error) {
	directory, err := root.openDirectory(relative, false)
	if err != nil {
		return nil, err
	}
	defer directory.Close()
	duplicate, err := unix.Dup(directory.fd)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(duplicate), relative)
	entries, readErr := file.ReadDir(-1)
	closeErr := file.Close()
	return entries, errors.Join(readErr, closeErr)
}

func (root *secureRoot) SyncDirectory(relative string) error {
	directory, err := root.openDirectory(relative, false)
	if err != nil {
		return err
	}
	defer directory.Close()
	return unix.Fsync(directory.fd)
}

func (root *secureRoot) openDirectory(relative string, create bool) (*secureDir, error) {
	parts, err := securePathParts(relative)
	if err != nil {
		return nil, err
	}
	current, err := duplicateDir(root.fd, root.info, root.hook)
	if err != nil {
		return nil, err
	}
	for _, part := range parts {
		next, openErr := openChildDirectory(current, part, create)
		_ = current.Close()
		if openErr != nil {
			return nil, openErr
		}
		current = next
	}
	return current, nil
}

func (root *secureRoot) openParent(relative string, create bool) (*secureDir, string, error) {
	parts, err := securePathParts(relative)
	if err != nil || len(parts) == 0 {
		return nil, "", os.ErrInvalid
	}
	parentPath := strings.Join(parts[:len(parts)-1], "/")
	parent, err := root.openDirectory(parentPath, create)
	return parent, parts[len(parts)-1], err
}

func (root *secureRoot) assertDirectoryBinding(relative string, expected fs.FileInfo) error {
	info, err := root.Lstat(relative)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !sameFileInfo(expected, info) {
		return errPathBindingChanged
	}
	return nil
}

func (directory *secureDir) Close() error {
	if directory == nil || directory.fd < 0 {
		return nil
	}
	err := unix.Close(directory.fd)
	directory.fd = -1
	return err
}

func (directory *secureDir) Lstat(name string) (fs.FileInfo, error) {
	if !validSecureName(name) {
		return nil, os.ErrInvalid
	}
	return lstatAt(directory.fd, name)
}

func (directory *secureDir) OpenFile(name string, flags int, mode fs.FileMode) (*os.File, error) {
	if !validSecureName(name) {
		return nil, os.ErrInvalid
	}
	fd, err := unix.Openat(directory.fd, name, flags|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func (directory *secureDir) Remove(name string) error {
	if directory == nil || directory.fd < 0 || !validSecureName(name) {
		return os.ErrInvalid
	}
	return unix.Unlinkat(directory.fd, name, 0)
}

func (directory *secureDir) RenameNoReplace(name, targetName string) error {
	if directory == nil || directory.fd < 0 || !validSecureName(name) || !validSecureName(targetName) {
		return os.ErrInvalid
	}
	return renameAtNoReplace(directory.fd, name, directory.fd, targetName)
}

func (directory *secureDir) Link(name string, target *secureDir, targetName string) error {
	if directory == nil || directory.fd < 0 || target == nil || target.fd < 0 ||
		!validSecureName(name) || !validSecureName(targetName) {
		return os.ErrInvalid
	}
	return unix.Linkat(directory.fd, name, target.fd, targetName, 0)
}

func (directory *secureDir) Sync() error {
	if directory == nil || directory.fd < 0 {
		return os.ErrInvalid
	}
	return unix.Fsync(directory.fd)
}

func (directory *secureDir) ReadDir() ([]os.DirEntry, error) {
	duplicate, err := unix.Openat(directory.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(duplicate), "directory")
	entries, readErr := file.ReadDir(-1)
	closeErr := file.Close()
	return entries, errors.Join(readErr, closeErr)
}

func (directory *secureDir) ForEachDirEntry(batchSize int, visit func(os.DirEntry) error) error {
	if directory == nil || directory.fd < 0 || batchSize < 1 || visit == nil {
		return os.ErrInvalid
	}
	duplicate, err := unix.Openat(directory.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(duplicate), "directory")
	defer file.Close()
	for {
		entries, readErr := file.ReadDir(batchSize)
		for _, entry := range entries {
			if err := visit(entry); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func (directory *secureDir) OpenDirectory(name string) (*secureDir, error) {
	return openChildDirectory(directory, name, false)
}

func (directory *secureDir) OpenDirectoryCreate(name string) (*secureDir, error) {
	return openChildDirectory(directory, name, true)
}

func openManagedDirectories(root *secureRoot, create, includeStaging bool) (*managedDirectories, error) {
	knowledge, err := root.openDirectory(".knowledge", create)
	if err != nil {
		return nil, err
	}
	managed := &managedDirectories{root: root, knowledge: knowledge}
	var exports *secureDir
	if create {
		exports, err = knowledge.OpenDirectoryCreate("exports")
	} else {
		exports, err = knowledge.OpenDirectory("exports")
	}
	if err != nil {
		_ = managed.Close()
		return nil, err
	}
	if exports.info.Mode().Perm()&0o077 != 0 {
		_ = exports.Close()
		_ = managed.Close()
		return nil, errPathBindingChanged
	}
	managed.exports = exports
	if !includeStaging {
		return managed, nil
	}
	var staging *secureDir
	if create {
		staging, err = exports.OpenDirectoryCreate(".staging")
	} else {
		staging, err = exports.OpenDirectory(".staging")
	}
	if err != nil {
		_ = managed.Close()
		return nil, err
	}
	if staging.info.Mode().Perm()&0o077 != 0 {
		_ = staging.Close()
		_ = managed.Close()
		return nil, errPathBindingChanged
	}
	managed.staging = staging
	return managed, nil
}

func (managed *managedDirectories) Close() error {
	if managed == nil {
		return nil
	}
	return errors.Join(
		managed.staging.Close(),
		managed.exports.Close(),
		managed.knowledge.Close(),
	)
}

func (managed *managedDirectories) VerifyBindings() error {
	if managed == nil || managed.root == nil || managed.knowledge == nil || managed.exports == nil {
		return os.ErrInvalid
	}
	if err := managed.root.assertBinding(); err != nil {
		return err
	}
	if err := managed.root.assertDirectoryBinding(".knowledge", managed.knowledge.info); err != nil {
		return err
	}
	if err := managed.root.assertDirectoryBinding(exportDirectory, managed.exports.info); err != nil {
		return err
	}
	if managed.staging != nil {
		if err := managed.root.assertDirectoryBinding(stagingDirectory, managed.staging.info); err != nil {
			return err
		}
	}
	return nil
}

func securePathParts(relative string) ([]string, error) {
	if relative == "" {
		return nil, nil
	}
	if path.Clean(relative) != relative || strings.Contains(relative, "\\") || path.IsAbs(relative) {
		return nil, os.ErrInvalid
	}
	parts := strings.Split(relative, "/")
	for _, part := range parts {
		if !validSecureName(part) {
			return nil, os.ErrInvalid
		}
	}
	return parts, nil
}

func validSecureName(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, "/\\\x00")
}

func duplicateDir(fd int, expected fs.FileInfo, hook *secureFilesystemHook) (*secureDir, error) {
	duplicate, err := unix.Dup(fd)
	if err != nil {
		return nil, err
	}
	info, err := statFD(duplicate)
	if err != nil || !info.IsDir() || !sameFileInfo(expected, info) {
		_ = unix.Close(duplicate)
		if err != nil {
			return nil, err
		}
		return nil, errPathBindingChanged
	}
	return &secureDir{fd: duplicate, info: info, hook: hook}, nil
}

func openChildDirectory(parent *secureDir, name string, create bool) (*secureDir, error) {
	if parent == nil || parent.fd < 0 || !validSecureName(name) {
		return nil, os.ErrInvalid
	}
	before, err := lstatAt(parent.fd, name)
	if errors.Is(err, os.ErrNotExist) && create {
		if mkdirErr := unix.Mkdirat(parent.fd, name, 0o700); mkdirErr != nil && !errors.Is(mkdirErr, unix.EEXIST) {
			return nil, mkdirErr
		}
		before, err = lstatAt(parent.fd, name)
	}
	if err != nil {
		return nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
		return nil, errPathBindingChanged
	}
	if parent.hook != nil && parent.hook.afterChildLstat != nil {
		parent.hook.afterChildLstat(name)
	}
	fd, err := unix.Openat(parent.fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	after, err := statFD(fd)
	if err != nil || !sameFileInfo(before, after) {
		_ = unix.Close(fd)
		if err != nil {
			return nil, err
		}
		return nil, errPathBindingChanged
	}
	return &secureDir{fd: fd, info: after, hook: parent.hook}, nil
}

func lstatAt(fd int, name string) (fs.FileInfo, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(fd, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	return unixFileInfo{name: name, stat: stat}, nil
}

// statFD deliberately uses /proc-free descriptor metadata. os.File.Stat does
// not resolve the descriptor's original path and therefore preserves the FD
// binding established by openat.
func statFD(fd int) (fs.FileInfo, error) {
	duplicate, err := unix.Dup(fd)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(duplicate), "descriptor")
	info, statErr := file.Stat()
	closeErr := file.Close()
	return info, errors.Join(statErr, closeErr)
}

func sameFileInfo(left, right fs.FileInfo) bool {
	if left == nil || right == nil {
		return false
	}
	leftDev, leftIno, leftOK := fileIdentity(left)
	rightDev, rightIno, rightOK := fileIdentity(right)
	return leftOK && rightOK && leftDev == rightDev && leftIno == rightIno
}

type unixFileInfo struct {
	name string
	stat unix.Stat_t
}

func (info unixFileInfo) Name() string       { return info.name }
func (info unixFileInfo) Size() int64        { return info.stat.Size }
func (info unixFileInfo) Mode() fs.FileMode  { return unixMode(uint64(info.stat.Mode)) }
func (info unixFileInfo) ModTime() time.Time { return statModTime(info.stat) }
func (info unixFileInfo) IsDir() bool        { return info.Mode().IsDir() }
func (info unixFileInfo) Sys() any           { return &info.stat }

func unixMode(mode uint64) fs.FileMode {
	result := fs.FileMode(mode & 0o777)
	switch mode & uint64(unix.S_IFMT) {
	case uint64(unix.S_IFDIR):
		result |= fs.ModeDir
	case uint64(unix.S_IFLNK):
		result |= fs.ModeSymlink
	case uint64(unix.S_IFCHR):
		result |= fs.ModeCharDevice
	case uint64(unix.S_IFBLK):
		result |= fs.ModeDevice
	case uint64(unix.S_IFIFO):
		result |= fs.ModeNamedPipe
	case uint64(unix.S_IFSOCK):
		result |= fs.ModeSocket
	}
	return result
}

func fileIdentity(info fs.FileInfo) (uint64, uint64, bool) {
	value := reflect.ValueOf(info.Sys())
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return 0, 0, false
		}
		value = value.Elem()
	}
	dev := value.FieldByName("Dev")
	ino := value.FieldByName("Ino")
	if !dev.IsValid() || !ino.IsValid() {
		return 0, 0, false
	}
	devValue, devOK := reflectUnsigned(dev)
	inoValue, inoOK := reflectUnsigned(ino)
	return devValue, inoValue, devOK && inoOK
}

func reflectUnsigned(value reflect.Value) (uint64, bool) {
	switch value.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return value.Uint(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if value.Int() < 0 {
			return 0, false
		}
		return uint64(value.Int()), true
	default:
		return 0, false
	}
}
