package retrievalworkspace

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
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/filesystem"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

const (
	testWorkspaceID     foundation.ID = "94000000-0000-4000-8000-000000000001"
	testSourceID        foundation.ID = "94000000-0000-4000-8000-000000000002"
	testSourceVersionID foundation.ID = "94000000-0000-4000-8000-000000000003"
	testArtifactID      foundation.ID = "94000000-0000-4000-8000-000000000004"
)

func TestReaderReadsManagedArtifactInsteadOfRelativeSourcePath(t *testing.T) {
	rootPath := t.TempDir()
	content := []byte("immutable artifact")
	hash := contentHash(content)
	managedLocation := ".knowledge/sources/" + hash
	if err := os.MkdirAll(filepath.Join(rootPath, ".knowledge", "sources"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rootPath, "notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, filepath.FromSlash(managedLocation)), content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "notes", "current.md"), []byte("mutable worktree drift"), 0o600); err != nil {
		t.Fatal(err)
	}
	material := validMaterial(rootPath, hash, int64(len(content)), managedLocation)
	material.SourceVersion.OriginalContentLocation = "notes/current.md"
	reader, err := NewReader(&fakeMaterialRepository{material: material}, filesystem.Scanner{})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := reader.ReadEvidenceArtifact(context.Background(), testWorkspaceID, testSourceVersionID)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.WorkspaceID != testWorkspaceID || artifact.SourceVersionID != testSourceVersionID ||
		artifact.ContentArtifactID != testArtifactID || artifact.ContentHash != hash || artifact.ByteSize != int64(len(content)) ||
		string(artifact.Bytes) != string(content) {
		t.Fatalf("artifact = %#v", artifact)
	}
}

func TestReaderPassesOnlyManagedArtifactToFileScanner(t *testing.T) {
	content := []byte("managed")
	hash := contentHash(content)
	material := validMaterial("/canonical/workspace", hash, int64(len(content)), ".knowledge/sources/"+hash)
	material.SourceVersion.OriginalContentLocation = "notes/do-not-read.md"
	files := &fakeFileScanner{content: content}
	reader, err := NewReader(&fakeMaterialRepository{material: material}, files)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), contextKey{}, "trace")
	artifact, err := reader.ReadEvidenceArtifact(ctx, testWorkspaceID, testSourceVersionID)
	if err != nil {
		t.Fatal(err)
	}
	if string(artifact.Bytes) != string(content) || files.ctx.Value(contextKey{}) != "trace" ||
		files.rootPath != material.WorkspaceRootPath || files.artifact != material.ContentArtifact ||
		files.artifact.ManagedLocation == material.SourceVersion.OriginalContentLocation {
		t.Fatalf("filesystem call = root %q artifact %#v", files.rootPath, files.artifact)
	}
}

func TestReaderReturnsNotFoundForCrossWorkspaceWithoutReadingFile(t *testing.T) {
	content := []byte("managed")
	hash := contentHash(content)
	material := validMaterial("/canonical/workspace", hash, int64(len(content)), ".knowledge/sources/"+hash)
	material.WorkspaceID = "94000000-0000-4000-8000-000000000099"
	material.ContentArtifact.WorkspaceID = material.WorkspaceID
	files := &fakeFileScanner{content: content}
	reader, err := NewReader(&fakeMaterialRepository{material: material}, files)
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.ReadEvidenceArtifact(context.Background(), testWorkspaceID, testSourceVersionID)
	requireClassifiedError(t, err, foundation.ErrorNotFound, evidenceReferenceNotFoundCode)
	if files.readCalled {
		t.Fatal("filesystem read executed for cross-workspace reference")
	}
}

func TestReaderFailsClosedForDamagedMaterialAndBytes(t *testing.T) {
	content := []byte("managed")
	hash := contentHash(content)
	tests := []struct {
		name     string
		mutate   func(*workspacedomain.SourceMaterial)
		returned []byte
		read     bool
	}{
		{name: "artifact binding", mutate: func(material *workspacedomain.SourceMaterial) {
			material.ContentArtifact.ID = "94000000-0000-4000-8000-000000000099"
		}},
		{name: "metadata binding", mutate: func(material *workspacedomain.SourceMaterial) {
			material.ContentArtifact.ByteSize++
		}},
		{name: "managed locator", mutate: func(material *workspacedomain.SourceMaterial) {
			material.ContentArtifact.ManagedLocation = "notes/current.md"
		}},
		{name: "oversized metadata", mutate: func(material *workspacedomain.SourceMaterial) {
			material.SourceVersion.ByteSize = workspacedomain.MaxCommittedSourceBytes + 1
			material.ContentArtifact.ByteSize = workspacedomain.MaxCommittedSourceBytes + 1
		}},
		{name: "corrupt bytes", returned: []byte("corrupt"), read: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			material := validMaterial("/canonical/workspace", hash, int64(len(content)), ".knowledge/sources/"+hash)
			if test.mutate != nil {
				test.mutate(&material)
			}
			returned := test.returned
			if returned == nil {
				returned = content
			}
			files := &fakeFileScanner{content: returned}
			reader, err := NewReader(&fakeMaterialRepository{material: material}, files)
			if err != nil {
				t.Fatal(err)
			}
			_, err = reader.ReadEvidenceArtifact(context.Background(), testWorkspaceID, testSourceVersionID)
			requireClassifiedError(t, err, foundation.ErrorConsistencyViolation, evidenceArtifactInvalidCode)
			if files.readCalled != test.read {
				t.Fatalf("filesystem read=%t, want %t", files.readCalled, test.read)
			}
		})
	}
}

func TestReaderPreservesRepositoryAndFileErrors(t *testing.T) {
	repositoryErr := foundation.NewError(foundation.ErrorDependencyUnavailable, "MATERIAL_QUERY_FAILED", true, errors.New("database unavailable"))
	reader, err := NewReader(&fakeMaterialRepository{err: repositoryErr}, &fakeFileScanner{})
	if err != nil {
		t.Fatal(err)
	}
	if _, actual := reader.ReadEvidenceArtifact(context.Background(), testWorkspaceID, testSourceVersionID); !errors.Is(actual, repositoryErr) {
		t.Fatalf("repository error = %v", actual)
	}

	content := []byte("managed")
	hash := contentHash(content)
	fileErr := foundation.NewError(foundation.ErrorConsistencyViolation, "CONTENT_ARTIFACT_INVALID", false, errors.New("unsafe locator"))
	reader, err = NewReader(
		&fakeMaterialRepository{material: validMaterial("/canonical/workspace", hash, int64(len(content)), ".knowledge/sources/"+hash)},
		&fakeFileScanner{err: fileErr},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, actual := reader.ReadEvidenceArtifact(context.Background(), testWorkspaceID, testSourceVersionID); !errors.Is(actual, fileErr) {
		t.Fatalf("file error = %v", actual)
	}
}

func TestReaderRejectsMissingDependenciesAndInvalidIDs(t *testing.T) {
	if _, err := NewReader(nil, &fakeFileScanner{}); err == nil {
		t.Fatal("nil repository accepted")
	}
	reader, err := NewReader(&fakeMaterialRepository{}, &fakeFileScanner{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = reader.ReadEvidenceArtifact(context.Background(), "invalid", testSourceVersionID)
	requireClassifiedError(t, err, foundation.ErrorInvalidInput, "RETRIEVAL_EVIDENCE_REFERENCE_INVALID")
}

func validMaterial(rootPath, hash string, size int64, managedLocation string) workspacedomain.SourceMaterial {
	return workspacedomain.SourceMaterial{
		WorkspaceID: testWorkspaceID, WorkspaceRootPath: rootPath, SourceID: testSourceID,
		SourceVersion: workspacedomain.SourceVersion{
			ID: testSourceVersionID, SourceID: testSourceID, ContentArtifactID: testArtifactID,
			ContentHash: hash, ByteSize: size, MediaType: "text/markdown",
		},
		ContentArtifact: workspacedomain.ContentArtifact{
			ID: testArtifactID, WorkspaceID: testWorkspaceID, ContentHash: hash, ByteSize: size,
			ManagedLocation: managedLocation, CreatedAt: time.Unix(1, 0),
		},
	}
}

type contextKey struct{}

type fakeMaterialRepository struct {
	material workspacedomain.SourceMaterial
	err      error
}

func (f *fakeMaterialRepository) GetSourceMaterial(context.Context, foundation.ID) (workspacedomain.SourceMaterial, error) {
	return f.material, f.err
}

type fakeFileScanner struct {
	readCalled bool
	ctx        context.Context
	rootPath   string
	artifact   workspacedomain.ContentArtifact
	content    []byte
	err        error
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
	return f.content, f.err
}

func contentHash(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func requireClassifiedError(t *testing.T, err error, kind foundation.ErrorKind, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != kind || classified.Code != code {
		t.Fatalf("error = %#v, want kind=%q code=%q", err, kind, code)
	}
}
