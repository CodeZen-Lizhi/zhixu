package filesystem

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"strings"
)

// MissingLocalSource 在同一有效根目录下核验缺失。它绝不
// 跟随符号链接，也不将根目录缺失或被替换视为来源已删除。
func (Scanner) MissingLocalSource(ctx context.Context, rootPath, relative string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !fs.ValidPath(relative) || relative == "." || strings.ContainsAny(relative, "\\:\x00") {
		return false, errors.New("invalid local source path")
	}
	for _, part := range strings.Split(relative, "/") {
		if part == ".git" || part == ".knowledge" || part == "tmp" || part == ".tmp" {
			return false, errors.New("excluded local source path")
		}
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return false, err
	}
	defer root.Close()
	identity, err := root.Stat(".")
	if err != nil {
		return false, err
	}
	checkRoot := func() error {
		live, err := os.Stat(rootPath)
		if err != nil {
			return err
		}
		if !os.SameFile(identity, live) {
			return errors.New("workspace root identity changed")
		}
		return ctx.Err()
	}
	if err := checkRoot(); err != nil {
		return false, err
	}
	parts := strings.Split(relative, "/")
	for index := range parts {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		info, err := root.Lstat(path.Join(parts[:index+1]...))
		if errors.Is(err, fs.ErrNotExist) {
			if err := checkRoot(); err != nil {
				return false, err
			}
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return false, errors.New("local source path contains a symlink")
		}
		if index < len(parts)-1 && !info.IsDir() {
			return false, errors.New("local source parent is not a directory")
		}
		if index == len(parts)-1 && !info.Mode().IsRegular() {
			return false, errors.New("local source is not a regular file")
		}
	}
	return false, checkRoot()
}
