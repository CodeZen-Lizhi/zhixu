package filesystem

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

// Scanner adapts the safe local Root implementation to the Workspace port.
type Scanner struct {
	Options ScanOptions
}

// CanonicalRoot validates and resolves one existing Workspace directory.
func (Scanner) CanonicalRoot(path string) (string, error) {
	root, err := NewRoot(path)
	if err != nil {
		return "", err
	}
	return root.Path(), nil
}

// Scan reads supported file metadata without modifying Workspace contents.
func (s Scanner) Scan(ctx context.Context, rootPath string) ([]domain.ScannedFile, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := NewRoot(rootPath)
	if err != nil {
		return nil, err
	}
	files, err := root.Scan(s.Options)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := make([]domain.ScannedFile, len(files))
	for index, file := range files {
		result[index] = domain.ScannedFile{
			RelativePath: file.RelativePath,
			ByteSize:     file.Size,
			ContentHash:  file.SHA256,
			MediaType:    file.MediaType,
		}
	}
	return result, nil
}

var _ domain.FileScanner = Scanner{}
