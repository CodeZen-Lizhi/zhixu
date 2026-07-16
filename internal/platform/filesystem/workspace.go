// Package filesystem provides the local Workspace filesystem boundary.
package filesystem

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Root is a canonical, existing Workspace directory.
type Root struct {
	path string
}

// File is the safe metadata and content fingerprint returned by a scan.
type File struct {
	RelativePath string
	Size         int64
	SHA256       string
	MediaType    string
}

// ScanOptions bounds the scanner and defines supported file extensions.
type ScanOptions struct {
	MaxBytes          int64
	AllowedExtensions map[string]struct{}
}

// NewRoot canonicalizes an existing directory and rejects non-directories.
func NewRoot(path string) (Root, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return Root{}, errors.New("workspace root is required")
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return Root{}, fmt.Errorf("resolve workspace root: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return Root{}, fmt.Errorf("resolve workspace root symlinks: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return Root{}, fmt.Errorf("stat workspace root: %w", err)
	}
	if !info.IsDir() {
		return Root{}, errors.New("workspace root is not a directory")
	}
	return Root{path: filepath.Clean(canonical)}, nil
}

// Path returns the canonical root for internal adapters and diagnostics.
func (r Root) Path() string { return r.path }

// Resolve returns an existing path safely contained by the Workspace root.
func (r Root) Resolve(relative string) (string, error) {
	if r.path == "" {
		return "", errors.New("workspace root is not initialized")
	}
	if strings.TrimSpace(relative) == "" {
		return "", errors.New("relative path is required")
	}
	if filepath.IsAbs(relative) {
		return "", errors.New("absolute paths are not allowed")
	}
	joined := filepath.Join(r.path, filepath.Clean(relative))
	canonical, err := filepath.EvalSymlinks(joined)
	if err != nil {
		return "", fmt.Errorf("resolve workspace path: %w", err)
	}
	if !within(r.path, canonical) {
		return "", errors.New("path escapes workspace root")
	}
	return canonical, nil
}

// Scan discovers supported regular files without following directory symlinks.
func (r Root) Scan(options ScanOptions) ([]File, error) {
	if r.path == "" {
		return nil, errors.New("workspace root is not initialized")
	}
	if options.MaxBytes < 0 {
		return nil, errors.New("max bytes must not be negative")
	}
	allowed := options.AllowedExtensions
	if len(allowed) == 0 {
		allowed = DefaultExtensions()
	}
	files := make([]File, 0)
	err := filepath.WalkDir(r.path, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == r.path {
			return nil
		}
		relative, err := filepath.Rel(r.path, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == "tmp" || entry.Name() == ".tmp" {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			resolved, resolveErr := r.Resolve(relative)
			if resolveErr != nil {
				return nil
			}
			info, statErr := os.Stat(resolved)
			if statErr != nil || !info.Mode().IsRegular() {
				return nil
			}
			path = resolved
		}
		ext := strings.ToLower(filepath.Ext(relative))
		if _, ok := allowed[ext]; !ok {
			return nil
		}
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if options.MaxBytes > 0 && info.Size() > options.MaxBytes {
			return fmt.Errorf("file %s exceeds max size", relative)
		}
		digest, err := hashFile(path)
		if err != nil {
			return fmt.Errorf("hash %s: %w", relative, err)
		}
		files = append(files, File{
			RelativePath: filepath.ToSlash(relative),
			Size:         info.Size(),
			SHA256:       digest,
			MediaType:    mediaType(ext),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan workspace: %w", err)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].RelativePath < files[j].RelativePath })
	return files, nil
}

// DefaultExtensions returns the formats accepted by the initial scanner.
func DefaultExtensions() map[string]struct{} {
	return map[string]struct{}{".md": {}, ".markdown": {}, ".txt": {}, ".pdf": {}, ".html": {}, ".htm": {}}
}

func within(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func mediaType(extension string) string {
	switch extension {
	case ".md", ".markdown":
		return "text/markdown"
	case ".txt":
		return "text/plain"
	case ".html", ".htm":
		return "text/html"
	case ".pdf":
		return "application/pdf"
	}
	if value := mime.TypeByExtension(extension); value != "" {
		return value
	}
	return "application/octet-stream"
}
