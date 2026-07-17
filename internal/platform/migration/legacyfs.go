// Package migration contains the project and River schema migration runtime.
package migration

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const legacyMigrationMaxVersion = 10

var dollarQuotePattern = regexp.MustCompile(`\$[A-Za-z_][A-Za-z0-9_]*\$|\$\$`)

// NewLegacyAnnotationFS returns a read-only filesystem that adds Goose statement
// boundaries in memory for legacy migrations 00001 through 00010. Repository SQL
// files and migrations starting at 00011 are never changed by this compatibility layer.
func NewLegacyAnnotationFS(base fs.FS) (fs.FS, error) {
	if base == nil {
		return nil, errors.New("migration filesystem is nil")
	}
	return legacyAnnotationFS{base: base}, nil
}

type legacyAnnotationFS struct {
	base fs.FS
}

func (f legacyAnnotationFS) Open(name string) (fs.File, error) {
	file, err := f.base.Open(name)
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if info.IsDir() || !isLegacyMigration(name) {
		return file, nil
	}
	content, err := io.ReadAll(file)
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	annotated := annotateLegacyDirections(content)
	return &memoryFile{
		Reader: bytes.NewReader(annotated),
		info:   memoryFileInfo{FileInfo: info, size: int64(len(annotated))},
	}, nil
}

func isLegacyMigration(name string) bool {
	base := path.Base(name)
	prefix, _, ok := strings.Cut(base, "_")
	if !ok || len(prefix) != 5 || !strings.HasSuffix(strings.ToLower(base), ".sql") {
		return false
	}
	version, err := strconv.Atoi(prefix)
	return err == nil && version >= 1 && version <= legacyMigrationMaxVersion
}

func annotateLegacyDirections(content []byte) []byte {
	lines := strings.SplitAfter(string(content), "\n")
	var output strings.Builder
	output.Grow(len(content) + 128)
	for index := 0; index < len(lines); {
		trimmed := strings.TrimSpace(lines[index])
		if trimmed != "-- +goose Up" && trimmed != "-- +goose Down" {
			output.WriteString(lines[index])
			index++
			continue
		}
		next := index + 1
		for next < len(lines) {
			candidate := strings.TrimSpace(lines[next])
			if candidate == "-- +goose Up" || candidate == "-- +goose Down" {
				break
			}
			next++
		}
		body := strings.Join(lines[index+1:next], "")
		output.WriteString(lines[index])
		if dollarQuotePattern.MatchString(body) && !strings.Contains(body, "-- +goose StatementBegin") {
			if !strings.HasSuffix(lines[index], "\n") {
				output.WriteByte('\n')
			}
			output.WriteString("-- +goose StatementBegin\n")
			output.WriteString(body)
			if body != "" && !strings.HasSuffix(body, "\n") {
				output.WriteByte('\n')
			}
			output.WriteString("-- +goose StatementEnd\n")
		} else {
			output.WriteString(body)
		}
		index = next
	}
	return []byte(output.String())
}

type memoryFile struct {
	*bytes.Reader
	info fs.FileInfo
}

func (f *memoryFile) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f *memoryFile) Close() error               { return nil }

type memoryFileInfo struct {
	fs.FileInfo
	size int64
}

func (i memoryFileInfo) Size() int64        { return i.size }
func (i memoryFileInfo) ModTime() time.Time { return i.FileInfo.ModTime() }
