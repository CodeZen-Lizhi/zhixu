package domain

import (
	"context"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// MaxCommittedSourceBytes 是 committed Source capture 与真实 Ingestion 共用的 v1 大小上限。
const MaxCommittedSourceBytes int64 = 10 * 1024 * 1024

// ScannedFile is safe metadata observed inside a canonical Workspace root.
type ScannedFile struct {
	RelativePath           string
	ByteSize               int64
	ContentHash            string
	MediaType              string
	SourceID               foundation.ID
	SourceVersionID        foundation.ID
	ContentArtifactID      foundation.ID
	ContentArtifactCreated bool
}

// ContentCapture reports the immutable managed copy created or reused for a scan observation.
type ContentCapture struct {
	ContentHash     string
	ByteSize        int64
	ManagedLocation string
	Created         bool
}

// CommittedContentStore 将已验证的 Git Commit bytes create-only 发布到不可变 Artifact Store。
type CommittedContentStore interface {
	CaptureCommitted(ctx context.Context, rootPath, relativePath string, content []byte, expectedHash string) (ContentCapture, error)
}

// FileScanner owns Workspace root canonicalization and read-only file scans.
type FileScanner interface {
	CanonicalRoot(path string) (string, error)
	Scan(ctx context.Context, rootPath string) ([]ScannedFile, error)
	Capture(ctx context.Context, rootPath string, file ScannedFile) (ContentCapture, error)
	ReadArtifact(ctx context.Context, rootPath string, artifact ContentArtifact) ([]byte, error)
}
