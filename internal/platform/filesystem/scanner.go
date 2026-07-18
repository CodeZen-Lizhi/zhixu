package filesystem

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
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
		return nil, contextError(ctx)
	}
	root, err := NewRoot(rootPath)
	if err != nil {
		return nil, err
	}
	files, err := root.ScanContext(ctx, s.Options)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, contextError(ctx)
		}
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, contextError(ctx)
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

// Capture publishes an immutable managed copy after verifying the scan observation.
func (Scanner) Capture(ctx context.Context, rootPath string, file domain.ScannedFile) (domain.ContentCapture, error) {
	root, err := NewRoot(rootPath)
	if err != nil {
		return domain.ContentCapture{}, fileError(foundation.ErrorInvalidInput, "WORKSPACE_ROOT_INVALID", false, err)
	}
	location, created, err := root.Capture(ctx, file.RelativePath, file.ContentHash, file.ByteSize)
	if err != nil {
		return domain.ContentCapture{}, err
	}
	return domain.ContentCapture{ContentHash: file.ContentHash, ByteSize: file.ByteSize, ManagedLocation: location, Created: created}, nil
}

// CaptureCommitted 发布指定 Git Commit 的确切 bytes，不读取可能漂移的工作树文件。
func (s Scanner) CaptureCommitted(ctx context.Context, rootPath, relativePath string, content []byte, expectedHash string) (domain.ContentCapture, error) {
	extension := strings.ToLower(filepath.Ext(relativePath))
	if extension != ".md" && extension != ".markdown" && extension != ".txt" {
		return domain.ContentCapture{}, fileError(foundation.ErrorInvalidInput, "SOURCE_EXTENSION_UNSUPPORTED", false, errors.New("source extension is not supported by ingestion"))
	}
	maxBytes := s.Options.MaxBytes
	if maxBytes < 0 {
		return domain.ContentCapture{}, fileError(foundation.ErrorInvalidInput, "SOURCE_CAPTURE_LIMIT_INVALID", false, errors.New("capture limit must not be negative"))
	}
	if maxBytes == 0 {
		maxBytes = DefaultMaxBytes
	}
	if int64(len(content)) > maxBytes {
		return domain.ContentCapture{}, fileError(foundation.ErrorInvalidInput, "SOURCE_FILE_TOO_LARGE", false, errors.New("committed source exceeds capture limit"))
	}
	root, err := NewRoot(rootPath)
	if err != nil {
		return domain.ContentCapture{}, fileError(foundation.ErrorInvalidInput, "WORKSPACE_ROOT_INVALID", false, err)
	}
	location, created, err := root.CaptureBytes(ctx, relativePath, content, expectedHash)
	if err != nil {
		return domain.ContentCapture{}, err
	}
	return domain.ContentCapture{ContentHash: expectedHash, ByteSize: int64(len(content)), ManagedLocation: location, Created: created}, nil
}

// ReadArtifact safely re-reads immutable bytes and verifies their metadata.
func (s Scanner) ReadArtifact(ctx context.Context, rootPath string, artifact domain.ContentArtifact) ([]byte, error) {
	root, err := NewRoot(rootPath)
	if err != nil {
		return nil, fileError(foundation.ErrorInvalidInput, "WORKSPACE_ROOT_INVALID", false, err)
	}
	return root.ReadArtifactLimited(ctx, artifact.ManagedLocation, artifact.ContentHash, artifact.ByteSize, s.Options.MaxBytes)
}

var _ domain.FileScanner = Scanner{}
var _ domain.CommittedContentStore = Scanner{}
