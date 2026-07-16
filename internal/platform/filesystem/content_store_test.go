package filesystem

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestRootCapturePublishesImmutableArtifactAndExcludesManagedStore(t *testing.T) {
	rootPath := newGitWorkspace(t)
	content := []byte("# immutable\n")
	if err := os.WriteFile(filepath.Join(rootPath, "note.md"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := NewRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	files, err := root.Scan(ScanOptions{})
	if err != nil || len(files) != 1 {
		t.Fatalf("Scan() = %#v, %v", files, err)
	}
	location, created, err := root.Capture(context.Background(), files[0].RelativePath, files[0].SHA256, files[0].Size)
	if err != nil || !created {
		t.Fatalf("Capture() = %q, %v, %v", location, created, err)
	}
	if location != ".knowledge/sources/"+files[0].SHA256 {
		t.Fatalf("managed location = %q", location)
	}
	got, err := root.ReadArtifact(context.Background(), location, files[0].SHA256, files[0].Size)
	if err != nil || string(got) != string(content) {
		t.Fatalf("ReadArtifact() = %q, %v", got, err)
	}
	rescanned, err := root.Scan(ScanOptions{})
	if err != nil || len(rescanned) != 1 || rescanned[0].RelativePath != "note.md" {
		t.Fatalf("managed store leaked into scan: %#v, %v", rescanned, err)
	}
	exclude, err := os.ReadFile(filepath.Join(rootPath, ".git", "info", "exclude"))
	if err != nil || !strings.Contains(string(exclude), "/.knowledge/") {
		t.Fatalf("git exclude = %q, %v", exclude, err)
	}
}

func TestRootCaptureRejectsSourceChangedAfterScan(t *testing.T) {
	rootPath := newGitWorkspace(t)
	path := filepath.Join(rootPath, "note.md")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := NewRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	files, err := root.Scan(ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("after!"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, err = root.Capture(context.Background(), files[0].RelativePath, files[0].SHA256, files[0].Size)
	requireFilesystemError(t, err, foundation.ErrorVersionConflict, "SOURCE_VERSION_CONTENT_CONFLICT")
}

func TestRootCaptureConcurrentSameHashIsCreateOnly(t *testing.T) {
	rootPath := newGitWorkspace(t)
	if err := os.WriteFile(filepath.Join(rootPath, "note.md"), []byte("same"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := NewRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	files, err := root.Scan(ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var created atomic.Int32
	var wait sync.WaitGroup
	errorsFound := make(chan error, 8)
	for range 8 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, wasCreated, captureErr := root.Capture(context.Background(), files[0].RelativePath, files[0].SHA256, files[0].Size)
			if captureErr != nil {
				errorsFound <- captureErr
				return
			}
			if wasCreated {
				created.Add(1)
			}
		}()
	}
	wait.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Fatal(err)
	}
	if created.Load() != 1 {
		t.Fatalf("created count = %d, want 1", created.Load())
	}
}

func TestRootCaptureHonorsCancelledContext(t *testing.T) {
	rootPath := newGitWorkspace(t)
	if err := os.WriteFile(filepath.Join(rootPath, "note.md"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := NewRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = root.Capture(ctx, "note.md", strings.Repeat("a", 64), 7)
	requireFilesystemError(t, err, foundation.ErrorNonRetryableFailure, "OPERATION_CANCELLED")
}

func TestRootReadArtifactRejectsManagedDirectorySymlink(t *testing.T) {
	rootPath := newGitWorkspace(t)
	content := []byte("content")
	if err := os.WriteFile(filepath.Join(rootPath, "note.md"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := NewRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	files, err := root.Scan(ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	location, _, err := root.Capture(context.Background(), "note.md", files[0].SHA256, files[0].Size)
	if err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.RemoveAll(filepath.Join(rootPath, ".knowledge")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(rootPath, ".knowledge")); err != nil {
		t.Fatal(err)
	}
	_, err = root.ReadArtifact(context.Background(), location, files[0].SHA256, files[0].Size)
	requireFilesystemError(t, err, foundation.ErrorConsistencyViolation, "CONTENT_ARTIFACT_INVALID")
}

func TestRootReadArtifactLimitedRejectsBeforeLoadingOversizedArtifact(t *testing.T) {
	rootPath := newGitWorkspace(t)
	content := []byte("oversized")
	if err := os.WriteFile(filepath.Join(rootPath, "note.md"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := NewRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	files, err := root.Scan(ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	location, _, err := root.Capture(context.Background(), "note.md", files[0].SHA256, files[0].Size)
	if err != nil {
		t.Fatal(err)
	}
	_, err = root.ReadArtifactLimited(context.Background(), location, files[0].SHA256, files[0].Size, 4)
	requireFilesystemError(t, err, foundation.ErrorInvalidInput, "SOURCE_FILE_TOO_LARGE")
}

func TestRootCaptureRejectsUntrustedGitdirMarker(t *testing.T) {
	rootPath := t.TempDir()
	outsideGit := t.TempDir()
	if err := os.WriteFile(filepath.Join(rootPath, ".git"), []byte("gitdir: "+outsideGit+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, "note.md"), []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := NewRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	files, err := root.Scan(ScanOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = root.Capture(context.Background(), "note.md", files[0].SHA256, files[0].Size)
	requireFilesystemError(t, err, foundation.ErrorDependencyUnavailable, "CONTENT_ARTIFACT_GIT_EXCLUDE_FAILED")
}

func TestHashReaderWithContextHonorsCancellation(t *testing.T) {
	reader := &blockingHashReader{firstRead: make(chan struct{}), unblock: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, _, err := hashReaderWithContext(ctx, reader)
		result <- err
	}()
	<-reader.firstRead
	cancel()
	close(reader.unblock)
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("hash error = %v, want context canceled", err)
	}
}

type blockingHashReader struct {
	firstRead chan struct{}
	unblock   chan struct{}
	readOnce  bool
}

func (r *blockingHashReader) Read(buffer []byte) (int, error) {
	if !r.readOnce {
		r.readOnce = true
		buffer[0] = 'x'
		close(r.firstRead)
		return 1, nil
	}
	<-r.unblock
	buffer[0] = 'y'
	return 1, nil
}

func newGitWorkspace(t *testing.T) string {
	t.Helper()
	rootPath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(rootPath, ".git", "info"), 0o700); err != nil {
		t.Fatal(err)
	}
	return rootPath
}

func requireFilesystemError(t *testing.T, err error, kind foundation.ErrorKind, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != kind || classified.Code != code {
		t.Fatalf("error = %#v, want kind=%q code=%q", err, kind, code)
	}
}
