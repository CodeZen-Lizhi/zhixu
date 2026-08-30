package migration

import (
	"errors"
	"fmt"
	"io/fs"

	"ariga.io/atlas/sql/migrate"
)

// LoadAtlasDir copies an embedded Atlas migration directory (SQL files plus the
// atlas.sum integrity file) into an in-memory directory and validates the hash
// chain before any executor sees it.
func LoadAtlasDir(fsys fs.FS) (*migrate.MemDir, error) {
	if fsys == nil {
		return nil, errors.New("atlas migration filesystem is nil")
	}
	dir := &migrate.MemDir{}
	err := fs.WalkDir(fsys, ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		content, err := fs.ReadFile(fsys, path)
		if err != nil {
			return fmt.Errorf("read atlas migration %s: %w", path, err)
		}
		if err := dir.WriteFile(path, content); err != nil {
			return fmt.Errorf("load atlas migration %s: %w", path, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := migrate.Validate(dir); err != nil {
		return nil, fmt.Errorf("validate atlas migration directory: %w", err)
	}
	return dir, nil
}
