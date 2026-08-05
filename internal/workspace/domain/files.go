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

// ManagedContentStage binds verified bytes to a controlled staging locator and
// the stable content-addressed location that may be persisted before publish.
type ManagedContentStage struct {
	ContentHash     string
	ByteSize        int64
	ManagedLocation string
	StagingLocation string
}

// ManagedContentStore stages already-bounded bytes and only publishes them
// after the caller confirms the corresponding database facts committed.
type ManagedContentStore interface {
	StageManaged(ctx context.Context, rootPath, sourceRef string, content []byte, expectedHash string) (ManagedContentStage, error)
	PublishManaged(ctx context.Context, rootPath string, stage ManagedContentStage) (ContentCapture, error)
	DiscardManaged(ctx context.Context, rootPath string, stage ManagedContentStage) error
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
