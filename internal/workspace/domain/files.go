package domain

import "context"

// ScannedFile is safe metadata observed inside a canonical Workspace root.
type ScannedFile struct {
	RelativePath string
	ByteSize     int64
	ContentHash  string
	MediaType    string
}

// FileScanner owns Workspace root canonicalization and read-only file scans.
type FileScanner interface {
	CanonicalRoot(path string) (string, error)
	Scan(ctx context.Context, rootPath string) ([]ScannedFile, error)
}
