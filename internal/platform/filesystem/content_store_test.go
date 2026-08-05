package filesystem

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
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

func TestRootCaptureBytesPublishesCommitContentWithoutReadingWorktree(t *testing.T) {
	rootPath := newGitWorkspace(t)
	if err := os.WriteFile(filepath.Join(rootPath, "note.md"), []byte("drifted worktree"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := NewRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("committed bytes")
	hash := testContentHash(content)
	location, created, err := root.CaptureBytes(context.Background(), "note.md", content, hash)
	if err != nil || !created {
		t.Fatalf("CaptureBytes()=%q %t %v", location, created, err)
	}
	replayedLocation, replayCreated, err := root.CaptureBytes(context.Background(), "note.md", content, hash)
	if err != nil || replayCreated || replayedLocation != location {
		t.Fatalf("replay=%q %t %v", replayedLocation, replayCreated, err)
	}
	got, err := root.ReadArtifact(context.Background(), location, hash, int64(len(content)))
	if err != nil || string(got) != string(content) {
		t.Fatalf("artifact=%q err=%v", got, err)
	}
}

func TestRootManagedStageDoesNotPublishBeforeConfirmation(t *testing.T) {
	rootPath := newGitWorkspace(t)
	root, err := NewRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("staged capture")
	hash := testContentHash(content)
	stage, err := root.StageManagedBytes(context.Background(), "captures/one/input", content, hash)
	if err != nil {
		t.Fatal(err)
	}
	if stage.ManagedLocation != ".knowledge/sources/"+hash || stage.StagingLocation == "" {
		t.Fatalf("stage=%#v", stage)
	}
	if _, err := os.Lstat(filepath.Join(rootPath, filepath.FromSlash(stage.ManagedLocation))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("final artifact published before confirmation: %v", err)
	}
	info, err := os.Lstat(filepath.Join(rootPath, filepath.FromSlash(stage.StagingLocation)))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		t.Fatalf("staging info=%#v err=%v", info, err)
	}
}

func TestEnsureManagedStagingDirectoriesSyncEveryCreatedParent(t *testing.T) {
	rootPath := newGitWorkspace(t)
	workspaceRoot, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer workspaceRoot.Close()

	var synced []string
	err = ensureManagedStagingHashDirectoryRootWithSync(workspaceRoot, strings.Repeat("a", 64), func(_ *os.Root, path string) error {
		synced = append(synced, path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".", ".knowledge", managedSourceDirectory, managedSourceStagingDirectory}
	if !slices.Equal(synced, want) {
		t.Fatalf("synced parents=%q want=%q", synced, want)
	}
}

func TestRootManagedStageRejectsSymlinkedStagingDirectory(t *testing.T) {
	rootPath := newGitWorkspace(t)
	if err := os.MkdirAll(filepath.Join(rootPath, managedSourceDirectory), 0o700); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(rootPath, managedSourceStagingDirectory)); err != nil {
		t.Fatal(err)
	}
	root, err := NewRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("private staged content")
	_, err = root.StageManagedBytes(context.Background(), "captures/symlink/input", content, testContentHash(content))
	requireFilesystemError(t, err, foundation.ErrorPermissionDenied, "CONTENT_ARTIFACT_STAGE_UNSAFE")
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatalf("outside staging target changed: entries=%v err=%v", entries, err)
	}
}

func TestRootManagedStageRejectsSymlinkedStageFile(t *testing.T) {
	rootPath := newGitWorkspace(t)
	root, err := NewRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("expected staged content")
	hash := testContentHash(content)
	stageLocation := managedStageLocation("captures/symlink-file/input", hash)
	if err := os.MkdirAll(filepath.Join(rootPath, filepath.FromSlash(filepath.Dir(stageLocation))), 0o700); err != nil {
		t.Fatal(err)
	}
	outsidePath := filepath.Join(t.TempDir(), "outside.stage")
	if err := os.WriteFile(outsidePath, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsidePath, filepath.Join(rootPath, filepath.FromSlash(stageLocation))); err != nil {
		t.Fatal(err)
	}
	_, err = root.StageManagedBytes(context.Background(), "captures/symlink-file/input", content, hash)
	requireFilesystemError(t, err, foundation.ErrorConsistencyViolation, "CONTENT_ARTIFACT_STAGE_INVALID")
}

func TestRootManagedStageConcurrentSameHashPublishesExactlyOnce(t *testing.T) {
	rootPath := newGitWorkspace(t)
	root, err := NewRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("shared immutable bytes")
	hash := testContentHash(content)
	stages := make([]domain.ManagedContentStage, 8)
	for index := range stages {
		stages[index], err = root.StageManagedBytes(context.Background(), fmt.Sprintf("captures/%d/input", index), content, hash)
		if err != nil {
			t.Fatal(err)
		}
	}
	var created atomic.Int32
	errorsFound := make(chan error, len(stages))
	var wait sync.WaitGroup
	for _, stage := range stages {
		stage := stage
		wait.Add(1)
		go func() {
			defer wait.Done()
			published, publishErr := root.PublishManagedStage(context.Background(), stage)
			if publishErr != nil {
				errorsFound <- publishErr
				return
			}
			if published.Created {
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
		t.Fatalf("created=%d want=1", created.Load())
	}
	got, err := root.ReadArtifact(context.Background(), stages[0].ManagedLocation, hash, int64(len(content)))
	if err != nil || string(got) != string(content) {
		t.Fatalf("artifact=%q err=%v", got, err)
	}
	for _, stage := range stages {
		if _, err := os.Lstat(filepath.Join(rootPath, filepath.FromSlash(stage.StagingLocation))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("staging remains after publish: %s: %v", stage.StagingLocation, err)
		}
	}
}

func TestRootReadArtifactSelfHealsConfirmedDeterministicStage(t *testing.T) {
	rootPath := newGitWorkspace(t)
	root, err := NewRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("recover after database commit")
	hash := testContentHash(content)
	stage, err := root.StageManagedBytes(context.Background(), "captures/recovery/input", content, hash)
	if err != nil {
		t.Fatal(err)
	}
	got, err := root.ReadArtifact(context.Background(), stage.ManagedLocation, hash, int64(len(content)))
	if err != nil || string(got) != string(content) {
		t.Fatalf("ReadArtifact()=%q err=%v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(rootPath, filepath.FromSlash(stage.StagingLocation))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("self-healed staging remains: %v", err)
	}
}

func TestRootDiscardManagedStageNeverDeletesPublishedSameHash(t *testing.T) {
	rootPath := newGitWorkspace(t)
	root, err := NewRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("same hash independent commands")
	hash := testContentHash(content)
	committed, err := root.StageManagedBytes(context.Background(), "captures/committed/input", content, hash)
	if err != nil {
		t.Fatal(err)
	}
	unreferenced, err := root.StageManagedBytes(context.Background(), "captures/rolled-back/input", content, hash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := root.PublishManagedStage(context.Background(), committed); err != nil {
		t.Fatal(err)
	}
	if err := root.DiscardManagedStage(context.Background(), unreferenced); err != nil {
		t.Fatal(err)
	}
	got, err := root.ReadArtifact(context.Background(), committed.ManagedLocation, hash, int64(len(content)))
	if err != nil || string(got) != string(content) {
		t.Fatalf("published artifact changed by discard: %q %v", got, err)
	}
}

func TestRootPublishManagedStageRejectsStagingSymlink(t *testing.T) {
	rootPath := newGitWorkspace(t)
	root, err := NewRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("trusted stage")
	hash := testContentHash(content)
	stage, err := root.StageManagedBytes(context.Background(), "captures/symlink/input", content, hash)
	if err != nil {
		t.Fatal(err)
	}
	stagePath := filepath.Join(rootPath, filepath.FromSlash(stage.StagingLocation))
	if err := os.Remove(stagePath); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, stagePath); err != nil {
		t.Fatal(err)
	}
	_, err = root.PublishManagedStage(context.Background(), stage)
	requireFilesystemError(t, err, foundation.ErrorConsistencyViolation, "CONTENT_ARTIFACT_STAGE_INVALID")
	if _, err := os.Lstat(filepath.Join(rootPath, filepath.FromSlash(stage.ManagedLocation))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe stage published final artifact: %v", err)
	}
}

func TestRootPublishManagedStageRejectsOversizedStagingFileBeforeHashing(t *testing.T) {
	rootPath := newGitWorkspace(t)
	root, err := NewRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("trusted stage")
	hash := testContentHash(content)
	stage, err := root.StageManagedBytes(context.Background(), "captures/oversized/input", content, hash)
	if err != nil {
		t.Fatal(err)
	}
	stagePath := filepath.Join(rootPath, filepath.FromSlash(stage.StagingLocation))
	if err := os.WriteFile(stagePath, bytes.Repeat([]byte("x"), 1024*1024), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = root.PublishManagedStage(context.Background(), stage)
	requireFilesystemError(t, err, foundation.ErrorConsistencyViolation, "CONTENT_ARTIFACT_CONTENT_CONFLICT")
	if _, err := os.Lstat(filepath.Join(rootPath, filepath.FromSlash(stage.ManagedLocation))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oversized stage published final artifact: %v", err)
	}
}

func TestScannerCaptureCommittedSupportsIngestionExtensionsAndRejectsInvalidContent(t *testing.T) {
	rootPath := newGitWorkspace(t)
	scanner := Scanner{Options: ScanOptions{MaxBytes: 4}}
	content := []byte("hello")
	for _, path := range []string{"notes/a.md", "notes/a.markdown", "notes/a.txt", "notes/a.html", "notes/a.htm", "notes/a.pdf"} {
		if _, err := (Scanner{Options: ScanOptions{MaxBytes: 16}}).CaptureCommitted(context.Background(), rootPath, path, content, testContentHash(content)); err != nil {
			t.Fatalf("supported committed path %q rejected: %v", path, err)
		}
	}
	if _, err := scanner.CaptureCommitted(context.Background(), rootPath, "notes/a.bin", content, testContentHash(content)); err == nil {
		t.Fatal("unsupported committed extension accepted")
	}
	if _, err := scanner.CaptureCommitted(context.Background(), rootPath, "notes/a.md", content, testContentHash(content)); err == nil {
		t.Fatal("oversized committed content accepted")
	}
	if _, err := scanner.CaptureCommitted(context.Background(), rootPath, "notes/../a.md", []byte("ok"), testContentHash([]byte("ok"))); err == nil {
		t.Fatal("noncanonical committed path accepted")
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

func testContentHash(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
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

func TestRootEnsureGitExcludePatternsAppendsMissingPatternsOnce(t *testing.T) {
	rootPath := newGitWorkspace(t)
	excludePath := filepath.Join(rootPath, ".git", "info", "exclude")
	if err := os.WriteFile(excludePath, []byte("# local rules\n/existing/"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := NewRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	patterns := []string{"/.knowledge/", "**/.zhixu-writeback-*", "/.knowledge/"}
	if err := root.EnsureGitExcludePatterns(patterns...); err != nil {
		t.Fatal(err)
	}
	if err := root.EnsureGitExcludePatterns(patterns...); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(content)
	if !strings.HasPrefix(got, "# local rules\n/existing/\n") {
		t.Fatalf("existing git exclude content changed: %q", got)
	}
	for _, pattern := range patterns[:2] {
		if count := strings.Count(got, pattern+"\n"); count != 1 {
			t.Fatalf("pattern %q count = %d in %q", pattern, count, got)
		}
	}
}

func TestRootEnsureGitExcludePatternsRejectsUnsafeInputWithoutMutation(t *testing.T) {
	rootPath := newGitWorkspace(t)
	excludePath := filepath.Join(rootPath, ".git", "info", "exclude")
	original := []byte("/existing/\n")
	if err := os.WriteFile(excludePath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := NewRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, pattern := range []string{"", "   ", "bad\npattern", "bad\rpattern", "bad\x00pattern"} {
		err := root.EnsureGitExcludePatterns(pattern)
		requireFilesystemError(t, err, foundation.ErrorInvalidInput, "GIT_EXCLUDE_PATTERN_INVALID")
	}
	content, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != string(original) {
		t.Fatalf("git exclude mutated after invalid input: %q", content)
	}
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
