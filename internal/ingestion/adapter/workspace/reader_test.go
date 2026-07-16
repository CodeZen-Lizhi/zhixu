package ingestionworkspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/ingestion/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/filesystem"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const (
	testWorkspaceID     foundation.ID = "10000000-0000-4000-8000-000000000001"
	testSourceID        foundation.ID = "20000000-0000-4000-8000-000000000001"
	testSourceVersionID foundation.ID = "30000000-0000-4000-8000-000000000001"
	testArtifactID      foundation.ID = "40000000-0000-4000-8000-000000000001"
)

func TestReaderReadsVerifiedImmutableContent(t *testing.T) {
	rootPath := t.TempDir()
	content := []byte("# immutable\n")
	digest := sha256.Sum256(content)
	hash := hex.EncodeToString(digest[:])
	managedLocation := filepath.ToSlash(filepath.Join(".knowledge", "sources", hash))
	if err := os.MkdirAll(filepath.Join(rootPath, ".knowledge", "sources"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, filepath.FromSlash(managedLocation)), content, 0o600); err != nil {
		t.Fatal(err)
	}
	repository := &fakeMaterialRepository{material: sourceMaterial(rootPath, hash, int64(len(content)), managedLocation)}
	reader, err := NewReader(repository, filesystem.Scanner{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), contextKey{}, "trace")
	metadata, err := reader.GetSourceMetadata(ctx, testSourceVersionID)
	if err != nil {
		t.Fatal(err)
	}
	contentBytes, err := reader.ReadSourceContent(ctx, metadata)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.WorkspaceID != testWorkspaceID || metadata.SourceVersionID != testSourceVersionID || metadata.ContentArtifactID != testArtifactID || metadata.MediaType != "text/markdown" || metadata.ByteSize != int64(len(content)) || string(contentBytes) != string(content) {
		t.Fatalf("metadata/content = %#v / %q", metadata, contentBytes)
	}
	if repository.ctx.Value(contextKey{}) != "trace" || repository.requestedID != testSourceVersionID {
		t.Fatalf("repository context/id = %#v, %q", repository.ctx.Value(contextKey{}), repository.requestedID)
	}
}

func TestReaderRejectsCorruptContentArtifact(t *testing.T) {
	rootPath := t.TempDir()
	expected := []byte("expected")
	digest := sha256.Sum256(expected)
	hash := hex.EncodeToString(digest[:])
	managedLocation := filepath.ToSlash(filepath.Join(".knowledge", "sources", hash))
	if err := os.MkdirAll(filepath.Join(rootPath, ".knowledge", "sources"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, filepath.FromSlash(managedLocation)), []byte("corrupt!"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := NewReader(&fakeMaterialRepository{material: sourceMaterial(rootPath, hash, int64(len(expected)), managedLocation)}, filesystem.Scanner{})
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := reader.GetSourceMetadata(context.Background(), testSourceVersionID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.ReadSourceContent(context.Background(), metadata)
	requireErrorCode(t, err, "CONTENT_ARTIFACT_CONTENT_CONFLICT")
}

func TestReaderPassesContextAndArtifactReferenceToFilesystem(t *testing.T) {
	material := sourceMaterial(t.TempDir(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 1, ".knowledge/sources/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	files := &fakeFileScanner{content: []byte("a")}
	reader, err := NewReader(&fakeMaterialRepository{material: material}, files)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), contextKey{}, "filesystem-trace")
	metadata, err := reader.GetSourceMetadata(ctx, testSourceVersionID)
	if err != nil {
		t.Fatal(err)
	}
	contentBytes, err := reader.ReadSourceContent(ctx, metadata)
	if err != nil {
		t.Fatal(err)
	}
	if string(contentBytes) != "a" || files.ctx.Value(contextKey{}) != "filesystem-trace" || files.rootPath != material.WorkspaceRootPath || files.artifact.ID != testArtifactID {
		t.Fatalf("filesystem call = content %q, context %#v, root %q, artifact %#v", contentBytes, files.ctx.Value(contextKey{}), files.rootPath, files.artifact)
	}
}

func TestReaderRejectsCrossScopeBeforeFilesystemRead(t *testing.T) {
	material := sourceMaterial(t.TempDir(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 1, ".knowledge/sources/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	material.ContentArtifact.WorkspaceID = "10000000-0000-4000-8000-000000000099"
	files := &fakeFileScanner{}
	reader, err := NewReader(&fakeMaterialRepository{material: material}, files)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.GetSourceMetadata(context.Background(), testSourceVersionID)
	requireErrorCode(t, err, "INGESTION_SOURCE_SCOPE_INVALID")
	if files.readCalled {
		t.Fatal("filesystem read executed for cross-scope material")
	}
}

func TestReaderRejectsMetadataChangeBeforeFilesystemRead(t *testing.T) {
	first := sourceMaterial(t.TempDir(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", 1, ".knowledge/sources/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	second := first
	second.SourceVersion.ByteSize = 2
	second.ContentArtifact.ByteSize = 2
	files := &fakeFileScanner{}
	reader, err := NewReader(&fakeMaterialRepository{materials: []workspacedomain.SourceMaterial{first, second}}, files)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := reader.GetSourceMetadata(context.Background(), testSourceVersionID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.ReadSourceContent(context.Background(), metadata)
	requireErrorCode(t, err, "INGESTION_SOURCE_METADATA_CONFLICT")
	if files.readCalled {
		t.Fatal("filesystem read executed after source metadata changed")
	}
}

func TestReaderExposesOversizedArtifactMetadataWithoutFilesystemRead(t *testing.T) {
	material := sourceMaterial(t.TempDir(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", application.DefaultContentMaxBytes+1, ".knowledge/sources/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	files := &fakeFileScanner{}
	reader, err := NewReader(&fakeMaterialRepository{material: material}, files)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := reader.GetSourceMetadata(context.Background(), testSourceVersionID)
	if err != nil || metadata.ByteSize != application.DefaultContentMaxBytes+1 {
		t.Fatalf("metadata = %#v, err=%v", metadata, err)
	}
	if files.readCalled {
		t.Fatal("filesystem read executed for oversized material")
	}
}

func TestReaderPreservesRepositoryErrors(t *testing.T) {
	expected := foundation.NewError(foundation.ErrorConsistencyViolation, "SOURCE_VERSION_ARTIFACT_MISSING", false, errors.New("legacy row"))
	reader, err := NewReader(&fakeMaterialRepository{err: expected}, &fakeFileScanner{})
	if err != nil {
		t.Fatal(err)
	}
	_, actual := reader.GetSourceMetadata(context.Background(), testSourceVersionID)
	if !errors.Is(actual, expected) {
		t.Fatalf("ReadSourceContent() error = %v", actual)
	}
}

func sourceMaterial(rootPath, hash string, size int64, location string) workspacedomain.SourceMaterial {
	return workspacedomain.SourceMaterial{
		WorkspaceID: testWorkspaceID, WorkspaceRootPath: rootPath, SourceID: testSourceID,
		SourceVersion: workspacedomain.SourceVersion{
			ID: testSourceVersionID, SourceID: testSourceID, ContentArtifactID: testArtifactID,
			ContentHash: hash, ByteSize: size, MediaType: "text/markdown",
		},
		ContentArtifact: workspacedomain.ContentArtifact{
			ID: testArtifactID, WorkspaceID: testWorkspaceID, ContentHash: hash, ByteSize: size,
			ManagedLocation: location, CreatedAt: time.Unix(1, 0),
		},
	}
}

type contextKey struct{}

type fakeMaterialRepository struct {
	material    workspacedomain.SourceMaterial
	materials   []workspacedomain.SourceMaterial
	err         error
	ctx         context.Context
	requestedID foundation.ID
	calls       int
}

func (f *fakeMaterialRepository) GetSourceMaterial(ctx context.Context, id foundation.ID) (workspacedomain.SourceMaterial, error) {
	f.ctx, f.requestedID = ctx, id
	if f.calls < len(f.materials) {
		material := f.materials[f.calls]
		f.calls++
		return material, f.err
	}
	f.calls++
	return f.material, f.err
}

type fakeFileScanner struct {
	readCalled bool
	ctx        context.Context
	rootPath   string
	artifact   workspacedomain.ContentArtifact
	content    []byte
}

func (*fakeFileScanner) CanonicalRoot(string) (string, error) { return "", nil }
func (*fakeFileScanner) Scan(context.Context, string) ([]workspacedomain.ScannedFile, error) {
	return nil, nil
}
func (*fakeFileScanner) Capture(context.Context, string, workspacedomain.ScannedFile) (workspacedomain.ContentCapture, error) {
	return workspacedomain.ContentCapture{}, nil
}
func (f *fakeFileScanner) ReadArtifact(ctx context.Context, rootPath string, artifact workspacedomain.ContentArtifact) ([]byte, error) {
	f.readCalled = true
	f.ctx, f.rootPath, f.artifact = ctx, rootPath, artifact
	return f.content, nil
}

func requireErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != code {
		t.Fatalf("error = %#v, want code %q", err, code)
	}
}
