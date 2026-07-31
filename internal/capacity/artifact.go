package capacity

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// PrepareArtifactDirectory 创建权限为 0700 的容量产物目录。
func PrepareArtifactDirectory(path string) error {
	if path == "" {
		return errors.New("capacity artifact directory is required")
	}
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("resolve capacity artifact directory: %w", err)
	}
	if filepath.Dir(absolute) == absolute {
		return errors.New("capacity artifact directory cannot be a filesystem root")
	}
	if workingDirectory, workingErr := os.Getwd(); workingErr == nil && absolute == workingDirectory {
		return errors.New("capacity artifact directory cannot be the working directory")
	}
	if homeDirectory, homeErr := os.UserHomeDir(); homeErr == nil && absolute == homeDirectory {
		return errors.New("capacity artifact directory cannot be the user home directory")
	}
	info, statErr := os.Lstat(absolute)
	if statErr == nil {
		return validateExistingArtifactDirectory(info)
	}
	if !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect capacity artifact directory: %w", statErr)
	}
	if err := createPrivateArtifactDirectory(absolute); err != nil {
		return err
	}
	return nil
}

func validateExistingArtifactDirectory(info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("capacity artifact directory cannot be a symbolic link")
	}
	if !info.IsDir() {
		return errors.New("capacity artifact directory must be a directory")
	}
	if info.Mode().Perm() != 0o700 {
		return fmt.Errorf("existing capacity artifact directory must already have mode 0700, got %#o", info.Mode().Perm())
	}
	return nil
}

func createPrivateArtifactDirectory(path string) error {
	parent := filepath.Dir(path)
	if parentInfo, err := os.Stat(parent); errors.Is(err, os.ErrNotExist) {
		if err := createPrivateArtifactDirectory(parent); err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("inspect capacity artifact parent directory: %w", err)
	} else if !parentInfo.IsDir() {
		return errors.New("capacity artifact parent must be a directory")
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			info, statErr := os.Lstat(path)
			if statErr != nil {
				return fmt.Errorf("inspect concurrently created capacity artifact directory: %w", statErr)
			}
			return validateExistingArtifactDirectory(info)
		}
		return fmt.Errorf("create capacity artifact directory: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("restrict newly created capacity artifact directory: %w", err)
	}
	return nil
}

// WriteAtomicArtifact 以 0600 临时文件写入并原子替换容量产物。
func WriteAtomicArtifact(path string, write func(io.Writer) error) error {
	if path == "" || write == nil {
		return errors.New("capacity artifact path and writer are required")
	}
	directory := filepath.Dir(path)
	if err := PrepareArtifactDirectory(directory); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".capacity-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := write(temporary); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// WriteJSONArtifact 将值以稳定缩进 JSON 写入 0600 容量产物。
func WriteJSONArtifact(path string, value any) error {
	return WriteAtomicArtifact(path, func(writer io.Writer) error {
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	})
}
