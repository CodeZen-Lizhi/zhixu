// Package localfs provides the managed local filesystem Artifact export adapter.
package localfs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"

	artifactapp "github.com/CodeZen-Lizhi/zhixu/internal/artifact/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/artifact/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/filesystem"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const (
	exportDirectory       = ".knowledge/exports/artifacts"
	privateDirectoryMode  = 0o700
	privateExportFileMode = 0o600

	errorCodeRequestInvalid = "ARTIFACT_MARKDOWN_EXPORT_INVALID"
	errorCodePathUnsafe     = "ARTIFACT_MARKDOWN_EXPORT_PATH_UNSAFE"
	errorCodeIOFailed       = "ARTIFACT_MARKDOWN_EXPORT_IO_FAILED"
	errorCodeResultInvalid  = "ARTIFACT_MARKDOWN_EXPORT_RESULT_INVALID"
)

// WorkspaceReader resolves the managed root for a single Workspace.
type WorkspaceReader interface {
	GetWorkspaceByID(context.Context, foundation.ID) (workspacedomain.Workspace, error)
}

// Exporter writes verified Artifact snapshots into their Workspace managed root.
type Exporter struct{ workspaces WorkspaceReader }

var _ artifactapp.MarkdownExporter = (*Exporter)(nil)

// NewExporter creates a Markdown exporter backed by the Workspace read port.
func NewExporter(workspaces WorkspaceReader) (*Exporter, error) {
	if nilDependency(workspaces) {
		return nil, unavailable(errors.New("artifact workspace reader is unavailable"))
	}
	return &Exporter{workspaces: workspaces}, nil
}

// Export writes the frozen Artifact revision through a private, atomic managed path.
func (exporter *Exporter) Export(ctx context.Context, snapshot artifactapp.ExportSnapshot) (artifactapp.ManagedExport, error) {
	if err := validateSnapshot(ctx, snapshot); err != nil {
		return artifactapp.ManagedExport{}, err
	}
	root, err := exporter.openWorkspaceRoot(ctx, snapshot.State.Artifact.WorkspaceID)
	if err != nil {
		return artifactapp.ManagedExport{}, err
	}
	defer root.Close()

	relativePath := managedPath(snapshot.State.Artifact.ID, snapshot.ExportID)
	if err := ensureExportDirectories(root, snapshot.State.Artifact.ID); err != nil {
		return artifactapp.ManagedExport{}, err
	}
	if err := rejectUnsafeTarget(root, relativePath); err != nil {
		return artifactapp.ManagedExport{}, err
	}

	payload := renderMarkdown(snapshot)
	digest := sha256.Sum256(payload)
	expectedHash := hex.EncodeToString(digest[:])
	temporaryPath, err := reserveTemporaryPath(root, relativePath)
	if err != nil {
		return artifactapp.ManagedExport{}, err
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = root.Remove(temporaryPath)
		}
	}()

	file, err := root.OpenFile(temporaryPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, privateExportFileMode)
	if err != nil {
		return artifactapp.ManagedExport{}, ioFailure(err)
	}
	writeErr := writeAndSync(file, payload)
	if writeErr == nil {
		writeErr = file.Chmod(privateExportFileMode)
	}
	closeErr := file.Close()
	if writeErr != nil {
		return artifactapp.ManagedExport{}, ioFailure(writeErr)
	}
	if closeErr != nil {
		return artifactapp.ManagedExport{}, ioFailure(closeErr)
	}

	// A second check rejects a target that was swapped to a symlink while writing.
	if err := rejectUnsafeTarget(root, relativePath); err != nil {
		return artifactapp.ManagedExport{}, err
	}
	if err := root.Rename(temporaryPath, relativePath); err != nil {
		return artifactapp.ManagedExport{}, ioFailure(err)
	}
	removeTemporary = false
	if err := syncDirectory(root, path.Dir(relativePath)); err != nil {
		return artifactapp.ManagedExport{}, err
	}
	if err := verifyExport(root, relativePath, expectedHash, int64(len(payload))); err != nil {
		return artifactapp.ManagedExport{}, err
	}
	return artifactapp.ManagedExport{OutputPath: relativePath, OutputHash: expectedHash, OutputSize: int64(len(payload))}, nil
}

func (exporter *Exporter) openWorkspaceRoot(ctx context.Context, workspaceID foundation.ID) (*os.Root, error) {
	if exporter == nil || nilDependency(exporter.workspaces) {
		return nil, unavailable(errors.New("artifact markdown exporter is unavailable"))
	}
	workspace, err := exporter.workspaces.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	rootPath := strings.TrimSpace(workspace.RootPath)
	if workspace.ID != workspaceID || rootPath == "" || !filepath.IsAbs(rootPath) || filepath.Clean(rootPath) != rootPath {
		return nil, unsafe(errors.New("workspace root binding is invalid"))
	}
	info, err := os.Lstat(rootPath)
	if err != nil {
		return nil, ioFailure(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, unsafe(errors.New("workspace root is not a regular directory"))
	}
	canonical, err := filesystem.NewRoot(rootPath)
	if err != nil {
		return nil, ioFailure(err)
	}
	if canonical.Path() != rootPath {
		return nil, unsafe(errors.New("workspace root is not canonical"))
	}
	root, err := os.OpenRoot(canonical.Path())
	if err != nil {
		return nil, ioFailure(err)
	}
	return root, nil
}

func validateSnapshot(ctx context.Context, snapshot artifactapp.ExportSnapshot) error {
	if ctx == nil {
		return invalid(errors.New("artifact markdown export context is nil"))
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !validID(snapshot.ExportID) || snapshot.State.Artifact.Status != domain.StatusExported {
		return invalid(errors.New("artifact markdown export snapshot is invalid"))
	}
	if err := artifactapp.ValidateState(snapshot.State); err != nil {
		return invalid(fmt.Errorf("artifact markdown export state: %w", err))
	}
	if !completeCoverage(snapshot.State.Artifact, snapshot.State.Revision) {
		return invalid(errors.New("artifact markdown export revision is incomplete"))
	}
	return nil
}

func completeCoverage(artifact domain.Artifact, revision domain.Revision) bool {
	if len(revision.Outline) == 0 || len(revision.Sections) != len(revision.Outline) || len(artifact.SourceCoverage) != len(revision.Outline) {
		return false
	}
	sections := make(map[string]domain.Section, len(revision.Sections))
	for _, section := range revision.Sections {
		sections[section.Key] = section
	}
	coverage := make(map[string]domain.Coverage, len(artifact.SourceCoverage))
	for _, item := range artifact.SourceCoverage {
		coverage[item.SectionKey] = item
	}
	for _, outline := range revision.Outline {
		section, sectionFound := sections[outline.Key]
		item, coverageFound := coverage[outline.Key]
		if !sectionFound || !coverageFound || !reflect.DeepEqual(section.Coverage, item) {
			return false
		}
	}
	return true
}

func managedPath(artifactID, exportID foundation.ID) string {
	return fmt.Sprintf("%s/%s/%s.md", exportDirectory, artifactID, exportID)
}

func ensureExportDirectories(root *os.Root, artifactID foundation.ID) error {
	for _, directory := range []string{
		".knowledge",
		".knowledge/exports",
		exportDirectory,
		path.Join(exportDirectory, string(artifactID)),
	} {
		info, err := root.Lstat(directory)
		if errors.Is(err, os.ErrNotExist) {
			if err := root.Mkdir(directory, privateDirectoryMode); err != nil && !errors.Is(err, os.ErrExist) {
				return ioFailure(err)
			}
			info, err = root.Lstat(directory)
		}
		if err != nil {
			return ioFailure(err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return unsafe(errors.New("artifact export directory is a symlink or non-directory"))
		}
		if directory != ".knowledge" && info.Mode().Perm()&0o077 != 0 {
			return unsafe(errors.New("artifact export directory permissions are too broad"))
		}
	}
	return nil
}

func rejectUnsafeTarget(root *os.Root, relativePath string) error {
	info, err := root.Lstat(relativePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return ioFailure(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return unsafe(errors.New("artifact export target is not a regular file"))
	}
	return nil
}

func reserveTemporaryPath(root *os.Root, target string) (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		var random [16]byte
		if _, err := io.ReadFull(rand.Reader, random[:]); err != nil {
			return "", unavailable(err)
		}
		candidate := fmt.Sprintf("%s.%s.tmp", target, hex.EncodeToString(random[:]))
		if _, err := root.Lstat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate, nil
		} else if err != nil {
			return "", ioFailure(err)
		}
	}
	return "", unavailable(errors.New("could not reserve artifact export temporary path"))
}

func writeAndSync(file *os.File, payload []byte) error {
	for len(payload) > 0 {
		written, err := file.Write(payload)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		payload = payload[written:]
	}
	return file.Sync()
}

func syncDirectory(root *os.Root, directory string) error {
	opened, err := root.Open(directory)
	if err != nil {
		return ioFailure(err)
	}
	defer opened.Close()
	if err := opened.Sync(); err != nil {
		return ioFailure(err)
	}
	return nil
}

func verifyExport(root *os.Root, relativePath, expectedHash string, expectedSize int64) error {
	info, err := root.Lstat(relativePath)
	if err != nil {
		return ioFailure(err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != privateExportFileMode || info.Size() != expectedSize {
		return inconsistent(errors.New("artifact export metadata does not match its binding"))
	}
	file, err := root.OpenFile(relativePath, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return ioFailure(err)
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, expectedSize+1))
	if err != nil {
		return ioFailure(err)
	}
	if int64(len(payload)) != expectedSize {
		return inconsistent(errors.New("artifact export size changed while reading"))
	}
	digest := sha256.Sum256(payload)
	if hex.EncodeToString(digest[:]) != expectedHash {
		return inconsistent(errors.New("artifact export hash does not match its binding"))
	}
	return nil
}

func renderMarkdown(snapshot artifactapp.ExportSnapshot) []byte {
	artifact := snapshot.State.Artifact
	revision := snapshot.State.Revision
	sections := make(map[string]domain.Section, len(revision.Sections))
	for _, section := range revision.Sections {
		sections[section.Key] = section
	}

	var builder strings.Builder
	builder.WriteString("# ")
	builder.WriteString(artifact.Title)
	builder.WriteString("\n\n")
	writeMetadata(&builder, "Artifact ID", string(artifact.ID))
	writeMetadata(&builder, "Workspace ID", string(artifact.WorkspaceID))
	writeMetadata(&builder, "Artifact Type", artifact.Type)
	writeMetadata(&builder, "Artifact Status", string(artifact.Status))
	writeMetadata(&builder, "Artifact Version", fmt.Sprintf("%d", artifact.Version))
	writeMetadata(&builder, "Revision ID", string(revision.ID))
	writeMetadata(&builder, "Revision Number", fmt.Sprintf("%d", revision.RevisionNo))
	writeMetadata(&builder, "Revision SHA-256", revision.ContentHash)
	writeMetadata(&builder, "Export ID", string(snapshot.ExportID))
	writeMetadata(&builder, "Revision Created At", revision.CreatedAt.UTC().Format(time.RFC3339Nano))
	builder.WriteString("- Formal Knowledge: `false`\n\n")
	builder.WriteString("## Scope\n\n")
	writeBlock(&builder, artifact.ScopeDefinition)

	for _, outline := range revision.Outline {
		section := sections[outline.Key]
		builder.WriteString("## ")
		builder.WriteString(outline.Title)
		builder.WriteString("\n\n")
		writeBlock(&builder, section.Content)
		builder.WriteString("### Coverage\n\n")
		writeMetadata(&builder, "Status", string(section.Coverage.Status))
		if len(section.Coverage.Gaps) > 0 {
			builder.WriteString("\n### Knowledge Gaps\n\n")
			for _, gap := range section.Coverage.Gaps {
				builder.WriteString("- `")
				builder.WriteString(gap.Code)
				builder.WriteString("`: ")
				builder.WriteString(gap.Description)
				builder.WriteByte('\n')
			}
		}
		if len(section.Citations) > 0 {
			builder.WriteString("\n### Citations\n\n")
			for _, citation := range section.Citations {
				builder.WriteString("- Source Version: `")
				builder.WriteString(string(citation.SourceVersionID))
				builder.WriteString("`; Source Span: `")
				builder.WriteString(string(citation.SourceSpanID))
				builder.WriteString("`; SHA-256: `")
				builder.WriteString(citation.VerifiedContentHash)
				builder.WriteString("`\n")
				writeQuotedBlock(&builder, citation.Excerpt)
			}
		}
		builder.WriteByte('\n')
	}
	return []byte(builder.String())
}

func writeMetadata(builder *strings.Builder, key, value string) {
	builder.WriteString("- ")
	builder.WriteString(key)
	builder.WriteString(": `")
	builder.WriteString(value)
	builder.WriteString("`\n")
}

func writeBlock(builder *strings.Builder, value string) {
	if value == "" {
		return
	}
	builder.WriteString(value)
	if !strings.HasSuffix(value, "\n") {
		builder.WriteByte('\n')
	}
	builder.WriteByte('\n')
}

func writeQuotedBlock(builder *strings.Builder, value string) {
	for _, line := range strings.Split(value, "\n") {
		builder.WriteString("  > ")
		builder.WriteString(line)
		builder.WriteByte('\n')
	}
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func nilDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	return reflected.Kind() == reflect.Pointer && reflected.IsNil()
}

func invalid(cause error) error {
	return foundation.NewError(foundation.ErrorInvalidInput, errorCodeRequestInvalid, false, cause)
}

func unsafe(cause error) error {
	return foundation.NewError(foundation.ErrorPermissionDenied, errorCodePathUnsafe, false, cause)
}

func unavailable(cause error) error {
	return foundation.NewError(foundation.ErrorDependencyUnavailable, errorCodeIOFailed, true, cause)
}

func ioFailure(cause error) error {
	return foundation.NewError(foundation.ErrorRetryableFailure, errorCodeIOFailed, true, cause)
}

func inconsistent(cause error) error {
	return foundation.NewError(foundation.ErrorConsistencyViolation, errorCodeResultInvalid, false, cause)
}
