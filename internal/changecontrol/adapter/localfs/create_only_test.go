package localfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

func TestReaderEnsureTargetAbsentUsesVersionedProof(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	reader, err := NewReader(fakeWorkspaces{workspace: workspacedomain.Workspace{ID: "workspace", RootPath: root}})
	if err != nil {
		t.Fatal(err)
	}
	token, err := domain.ComputeAbsenceToken("workspace", "notes/new.md")
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.EnsureTargetAbsent(context.Background(), "workspace", "notes/new.md", token); err != nil {
		t.Fatalf("EnsureTargetAbsent() = %v", err)
	}
	if err := reader.EnsureTargetAbsent(context.Background(), "workspace", "notes/new.md", domain.ComputeWritebackResultHash(nil)); err == nil {
		t.Fatal("EnsureTargetAbsent() accepted an empty-file content hash")
	}
	if err := os.WriteFile(filepath.Join(root, "notes", "new.md"), []byte("occupied\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = reader.EnsureTargetAbsent(context.Background(), "workspace", "notes/new.md", token)
	requireWritebackError(t, err, foundation.ErrorVersionConflict, "CREATE_ONLY_TARGET_EXISTS")
}

func TestWriterCreateOnlyNeverReplacesConcurrentOccupant(t *testing.T) {
	root := newWritebackWorkspace(t)
	targetPath := "notes/new.md"
	content := []byte("# new\n")
	lock, err := newTestWriter(t, root).AcquireCreateOnlyTarget(context.Background(), "workspace", targetPath)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	prepared, err := lock.Prepare(context.Background(), createOnlyPrepareCommand(t, targetPath, content))
	if err != nil {
		t.Fatal(err)
	}
	if prepared.BackupRef != "" || prepared.BackupLockToken != "" {
		t.Fatalf("CREATE_ONLY prepared a backup: %+v", prepared)
	}
	absoluteTarget := filepath.Join(root, filepath.FromSlash(targetPath))
	if err := os.WriteFile(absoluteTarget, []byte("concurrent owner\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = lock.CommitCAS(context.Background(), prepared)
	requireWritebackError(t, err, foundation.ErrorVersionConflict, "CREATE_ONLY_TARGET_EXISTS")
	if got := string(readFile(t, absoluteTarget)); got != "concurrent owner\n" {
		t.Fatalf("concurrent target was overwritten: %q", got)
	}
}

func TestWriterCreateOnlyRecoversLinkResponseLossAndCompensatesExactly(t *testing.T) {
	root := newWritebackWorkspace(t)
	targetPath := "notes/new.md"
	content := []byte("# new\n\nbody\n")
	writer := newFaultWriter(t, root, func(operations *writerOperations) {
		operations.link = func(root *os.Root, oldPath, newPath string) error {
			if err := root.Link(oldPath, newPath); err != nil {
				return err
			}
			return errors.New("link response lost")
		}
	})
	lock, err := writer.AcquireCreateOnlyTarget(context.Background(), "workspace", targetPath)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := lock.Prepare(context.Background(), createOnlyPrepareCommand(t, targetPath, content))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lock.CommitCAS(context.Background(), prepared); !errors.Is(err, domain.ErrWritebackManualRecoveryRequired) {
		t.Fatalf("CommitCAS() error = %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}

	recovered, recoveredPrepared, recoveredApplied, err := newTestWriter(t, root).ResumeCreateOnlyTarget(
		context.Background(), "workspace", targetPath, domain.ResumeWrite{Prepared: prepared},
	)
	if err != nil || recoveredApplied == nil || recoveredPrepared != prepared {
		t.Fatalf("ResumeCreateOnlyTarget() prepared=%+v applied=%+v err=%v", recoveredPrepared, recoveredApplied, err)
	}
	applied := *recoveredApplied
	result, err := recovered.RestoreCAS(context.Background(), applied)
	if err != nil || !result.Restored {
		t.Fatalf("RestoreCAS() = %+v, %v", result, err)
	}
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(targetPath))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("compensated target still exists: %v", err)
	}
	if err := recovered.Cleanup(context.Background(), applied); err != nil {
		t.Fatal(err)
	}
	if err := recovered.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(prepared.TemporaryRef))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("controlled temp still exists: %v", err)
	}
}

func TestWriterCreateOnlyCleanupKeepsPublishedTarget(t *testing.T) {
	root := newWritebackWorkspace(t)
	targetPath := "notes/new.md"
	content := []byte("# published\n")
	lock, err := newTestWriter(t, root).AcquireCreateOnlyTarget(context.Background(), "workspace", targetPath)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	prepared, err := lock.Prepare(context.Background(), createOnlyPrepareCommand(t, targetPath, content))
	if err != nil {
		t.Fatal(err)
	}
	applied, err := lock.CommitCAS(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Cleanup(context.Background(), applied); err != nil {
		t.Fatal(err)
	}
	if got := string(readFile(t, filepath.Join(root, filepath.FromSlash(targetPath)))); got != string(content) {
		t.Fatalf("published target changed during cleanup: %q", got)
	}
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(prepared.TemporaryRef))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("controlled temp still exists: %v", err)
	}
}

func createOnlyPrepareCommand(t *testing.T, targetPath string, content []byte) domain.PrepareWrite {
	t.Helper()
	token, err := domain.ComputeAbsenceToken("workspace", targetPath)
	if err != nil {
		t.Fatal(err)
	}
	changeHash, err := domain.ComputeChangeHashForTarget("workspace", targetPath, domain.TargetModeCreateOnly, token, string(content))
	if err != nil {
		t.Fatal(err)
	}
	return domain.PrepareWrite{
		ExecutionID: "execution", WorkspaceID: "workspace", TargetMode: domain.TargetModeCreateOnly,
		ExpectedBaseHash: token, ApprovedChangeHash: changeHash, Content: append([]byte(nil), content...),
	}
}
