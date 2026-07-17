package localfs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workspacedomain "github.com/CodeZen-Lizhi/zhixu/internal/workspace/domain"
)

func TestWriterPrepareCommitRestoreCleanupLifecycle(t *testing.T) {
	workspaceRoot := newWritebackWorkspace(t)
	targetPath := "notes/a.md"
	absoluteTarget := filepath.Join(workspaceRoot, filepath.FromSlash(targetPath))
	baseContent := []byte("# base\n")
	resultContent := []byte("# updated\n\n| a | b |\n| - | - |\n| 1 | 2 |\n")
	if err := os.WriteFile(absoluteTarget, baseContent, 0o640); err != nil {
		t.Fatal(err)
	}
	originalInfo, err := os.Stat(absoluteTarget)
	if err != nil {
		t.Fatal(err)
	}

	writer := newTestWriter(t, workspaceRoot)
	lock, err := writer.AcquireTarget(context.Background(), "workspace", targetPath)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	command := prepareCommand(targetPath, baseContent, resultContent)
	prepared, err := lock.Prepare(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.ResultHash != hashBytes(resultContent) || prepared.ByteSize != int64(len(resultContent)) || prepared.Mode != 0o640 {
		t.Fatalf("prepared = %+v", prepared)
	}
	temporaryInfo, err := os.Lstat(filepath.Join(workspaceRoot, filepath.FromSlash(prepared.TemporaryRef)))
	if err != nil || !temporaryInfo.Mode().IsRegular() || temporaryInfo.Mode().Perm() != 0o640 {
		t.Fatalf("temporary info = %#v, %v", temporaryInfo, err)
	}
	reservedBackup := filepath.Join(workspaceRoot, filepath.FromSlash(prepared.BackupRef))
	reservedInfo, err := os.Lstat(reservedBackup)
	if err != nil || !reservedInfo.Mode().IsRegular() || reservedInfo.Mode().Perm() != 0o640 || reservedInfo.Size() != int64(len(baseContent)) {
		t.Fatalf("reserved backup info = %#v, %v", reservedInfo, err)
	}
	if got := readFile(t, reservedBackup); string(got) != string(baseContent) {
		t.Fatalf("prepared backup = %q", got)
	}

	applied, err := lock.CommitCAS(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, absoluteTarget); string(got) != string(resultContent) {
		t.Fatalf("target after commit = %q", got)
	}
	backupPath := filepath.Join(workspaceRoot, filepath.FromSlash(applied.BackupRef))
	if got := readFile(t, backupPath); string(got) != string(baseContent) {
		t.Fatalf("backup = %q", got)
	}
	backupInfo, err := os.Stat(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	resultInfo, err := os.Stat(absoluteTarget)
	if err != nil {
		t.Fatal(err)
	}
	if sameInode(backupInfo, resultInfo) || sameInode(originalInfo, resultInfo) {
		t.Fatal("backup or result reused the replaced target inode")
	}
	if resultInfo.Mode().Perm() != 0o640 || backupInfo.Mode().Perm() != 0o640 {
		t.Fatalf("modes result=%o backup=%o", resultInfo.Mode().Perm(), backupInfo.Mode().Perm())
	}
	exclude := string(readFile(t, filepath.Join(workspaceRoot, ".git", "info", "exclude")))
	if strings.Count(exclude, writebackExcludePattern+"\n") != 1 {
		t.Fatalf("git exclude = %q", exclude)
	}

	restored, err := lock.RestoreCAS(context.Background(), applied)
	if err != nil || !restored.Restored || restored.Replayed {
		t.Fatalf("RestoreCAS() = %+v, %v", restored, err)
	}
	if got := readFile(t, absoluteTarget); string(got) != string(baseContent) {
		t.Fatalf("target after restore = %q", got)
	}
	replayed, err := lock.RestoreCAS(context.Background(), applied)
	if err != nil || !replayed.Replayed || replayed.Restored {
		t.Fatalf("RestoreCAS(replay) = %+v, %v", replayed, err)
	}
	if err := lock.Cleanup(context.Background(), applied); err != nil {
		t.Fatal(err)
	}
	if err := lock.Cleanup(context.Background(), applied); err != nil {
		t.Fatal(err)
	}
	for _, locator := range []string{prepared.TemporaryRef, applied.BackupRef} {
		if _, err := os.Lstat(filepath.Join(workspaceRoot, filepath.FromSlash(locator))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("managed file %q still exists: %v", locator, err)
		}
	}
}

func TestWriterCommitCASRejectsBaseAndIdentityChanges(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*testing.T, string)
		code string
	}{
		{
			name: "base hash",
			edit: func(t *testing.T, target string) {
				t.Helper()
				if err := os.WriteFile(target, []byte("user edit\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			code: "TARGET_BASE_HASH_CONFLICT",
		},
		{
			name: "identity",
			edit: func(t *testing.T, target string) {
				t.Helper()
				if err := os.Rename(target, target+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target, []byte("replacement\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			code: "TARGET_IDENTITY_CONFLICT",
		},
		{
			name: "mode",
			edit: func(t *testing.T, target string) {
				t.Helper()
				if err := os.Chmod(target, 0o640); err != nil {
					t.Fatal(err)
				}
			},
			code: "TARGET_MODE_CONFLICT",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspaceRoot := newWritebackWorkspace(t)
			targetPath := "notes/a.md"
			absoluteTarget := filepath.Join(workspaceRoot, filepath.FromSlash(targetPath))
			base := []byte("base\n")
			if err := os.WriteFile(absoluteTarget, base, 0o600); err != nil {
				t.Fatal(err)
			}
			writer := newTestWriter(t, workspaceRoot)
			lock, err := writer.AcquireTarget(context.Background(), "workspace", targetPath)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			prepared, err := lock.Prepare(context.Background(), prepareCommand(targetPath, base, []byte("result\n")))
			if err != nil {
				t.Fatal(err)
			}
			test.edit(t, absoluteTarget)
			_, err = lock.CommitCAS(context.Background(), prepared)
			requireWritebackError(t, err, foundation.ErrorVersionConflict, test.code)
			if got := string(readFile(t, absoluteTarget)); got == "result\n" {
				t.Fatal("conflicting target was overwritten")
			}
		})
	}
}

func TestWriterRejectsTamperedPreparedAndRestoreUserEdit(t *testing.T) {
	t.Run("prepared temp", func(t *testing.T) {
		workspaceRoot := newWritebackWorkspace(t)
		targetPath := "notes/a.md"
		absoluteTarget := filepath.Join(workspaceRoot, filepath.FromSlash(targetPath))
		base := []byte("base\n")
		if err := os.WriteFile(absoluteTarget, base, 0o600); err != nil {
			t.Fatal(err)
		}
		lock, err := newTestWriter(t, workspaceRoot).AcquireTarget(context.Background(), "workspace", targetPath)
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := lock.Prepare(context.Background(), prepareCommand(targetPath, base, []byte("result\n")))
		if err != nil {
			t.Fatal(err)
		}
		temporaryPath := filepath.Join(workspaceRoot, filepath.FromSlash(prepared.TemporaryRef))
		if err := os.WriteFile(temporaryPath, []byte("tampered\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = lock.CommitCAS(context.Background(), prepared)
		requireWritebackError(t, err, foundation.ErrorConsistencyViolation, "WRITEBACK_MANAGED_FILE_CONTENT_CONFLICT")
		if got := string(readFile(t, absoluteTarget)); got != string(base) {
			t.Fatalf("target changed to %q", got)
		}
		if err := lock.Close(); err != nil {
			t.Fatalf("Close() must preserve durable evidence: %v", err)
		}
		if _, err := os.Lstat(temporaryPath); err != nil {
			t.Fatalf("tampered temp was removed: %v", err)
		}
	})

	t.Run("user edit after commit", func(t *testing.T) {
		workspaceRoot := newWritebackWorkspace(t)
		targetPath := "notes/a.md"
		absoluteTarget := filepath.Join(workspaceRoot, filepath.FromSlash(targetPath))
		base := []byte("base\n")
		if err := os.WriteFile(absoluteTarget, base, 0o600); err != nil {
			t.Fatal(err)
		}
		lock, err := newTestWriter(t, workspaceRoot).AcquireTarget(context.Background(), "workspace", targetPath)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		prepared, err := lock.Prepare(context.Background(), prepareCommand(targetPath, base, []byte("result\n")))
		if err != nil {
			t.Fatal(err)
		}
		applied, err := lock.CommitCAS(context.Background(), prepared)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absoluteTarget, []byte("user after apply\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = lock.RestoreCAS(context.Background(), applied)
		requireWritebackError(t, err, foundation.ErrorVersionConflict, "WRITEBACK_RESTORE_CONFLICT")
		if got := string(readFile(t, absoluteTarget)); got != "user after apply\n" {
			t.Fatalf("user edit overwritten: %q", got)
		}
	})

	t.Run("mode edit after commit", func(t *testing.T) {
		workspaceRoot := newWritebackWorkspace(t)
		targetPath := "notes/a.md"
		absoluteTarget := filepath.Join(workspaceRoot, filepath.FromSlash(targetPath))
		base := []byte("base\n")
		if err := os.WriteFile(absoluteTarget, base, 0o600); err != nil {
			t.Fatal(err)
		}
		lock, err := newTestWriter(t, workspaceRoot).AcquireTarget(context.Background(), "workspace", targetPath)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		prepared, err := lock.Prepare(context.Background(), prepareCommand(targetPath, base, []byte("result\n")))
		if err != nil {
			t.Fatal(err)
		}
		applied, err := lock.CommitCAS(context.Background(), prepared)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(absoluteTarget, 0o640); err != nil {
			t.Fatal(err)
		}
		_, err = lock.RestoreCAS(context.Background(), applied)
		requireWritebackError(t, err, foundation.ErrorVersionConflict, "WRITEBACK_RESTORE_CONFLICT")
		if got := string(readFile(t, absoluteTarget)); got != "result\n" {
			t.Fatalf("mode edit target was overwritten: %q", got)
		}
	})
}

func TestWriterRestoreAndCleanupRejectTamperedBackup(t *testing.T) {
	workspaceRoot := newWritebackWorkspace(t)
	targetPath := "notes/a.md"
	absoluteTarget := filepath.Join(workspaceRoot, filepath.FromSlash(targetPath))
	base := []byte("base\n")
	if err := os.WriteFile(absoluteTarget, base, 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := newTestWriter(t, workspaceRoot).AcquireTarget(context.Background(), "workspace", targetPath)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	prepared, err := lock.Prepare(context.Background(), prepareCommand(targetPath, base, []byte("result\n")))
	if err != nil {
		t.Fatal(err)
	}
	applied, err := lock.CommitCAS(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(workspaceRoot, filepath.FromSlash(applied.BackupRef))
	if err := os.WriteFile(backupPath, []byte("tampered backup\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = lock.RestoreCAS(context.Background(), applied)
	requireWritebackError(t, err, foundation.ErrorConsistencyViolation, "WRITEBACK_MANAGED_FILE_CONTENT_CONFLICT")
	if got := string(readFile(t, absoluteTarget)); got != "result\n" {
		t.Fatalf("target changed after bad backup: %q", got)
	}
	err = lock.Cleanup(context.Background(), applied)
	requireWritebackError(t, err, foundation.ErrorConsistencyViolation, "WRITEBACK_CLEANUP_CONTENT_CONFLICT")
	if _, err := os.Lstat(backupPath); err != nil {
		t.Fatalf("tampered backup was removed: %v", err)
	}
}

func TestWriterPrepareBindingAndClosePreservesDurableIntent(t *testing.T) {
	workspaceRoot := newWritebackWorkspace(t)
	targetPath := "notes/a.md"
	absoluteTarget := filepath.Join(workspaceRoot, filepath.FromSlash(targetPath))
	base := []byte("base\n")
	if err := os.WriteFile(absoluteTarget, base, 0o600); err != nil {
		t.Fatal(err)
	}
	lock, err := newTestWriter(t, workspaceRoot).AcquireTarget(context.Background(), "workspace", targetPath)
	if err != nil {
		t.Fatal(err)
	}
	command := prepareCommand(targetPath, base, []byte("result\n"))
	prepared, err := lock.Prepare(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	replayed, err := lock.Prepare(context.Background(), command)
	if err != nil || replayed != prepared {
		t.Fatalf("Prepare replay = %+v, %v", replayed, err)
	}
	changed := command
	changed.Content = []byte("other\n")
	changed.ApprovedChangeHash = domain.ComputeChangeHash(targetPath, changed.ExpectedBaseHash, string(changed.Content))
	_, err = lock.Prepare(context.Background(), changed)
	requireWritebackError(t, err, foundation.ErrorVersionConflict, "WRITEBACK_PREPARED_BINDING_CONFLICT")
	temporaryPath := filepath.Join(workspaceRoot, filepath.FromSlash(prepared.TemporaryRef))
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(temporaryPath); err != nil {
		t.Fatalf("durable temp was removed: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(workspaceRoot, filepath.FromSlash(prepared.BackupRef))); err != nil {
		t.Fatalf("reserved backup was removed: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWriterTargetIdentitySecurityRejectsCrossDeviceAndForeignOwner(t *testing.T) {
	identity := fileIdentity{Device: 10, Inode: 20, Owner: 30}
	requireWritebackError(t, validateTargetIdentitySecurity(identity, 11, 30), foundation.ErrorPermissionDenied, "WRITEBACK_TARGET_CROSS_DEVICE")
	requireWritebackError(t, validateTargetIdentitySecurity(identity, 10, 31), foundation.ErrorPermissionDenied, "WRITEBACK_TARGET_OWNER_INVALID")
	if err := validateTargetIdentitySecurity(identity, 10, 30); err != nil {
		t.Fatal(err)
	}
}

func TestWriterRejectsWorkspaceRepositoryBindingMismatch(t *testing.T) {
	workspaceRoot := newWritebackWorkspace(t)
	if err := os.WriteFile(filepath.Join(workspaceRoot, "notes", "a.md"), []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	validator, err := NewDefaultMarkdownValidator()
	if err != nil {
		t.Fatal(err)
	}
	writer, err := NewWriter(writerWorkspaceRepository{workspace: workspacedomain.Workspace{ID: "other-workspace", RootPath: workspaceRoot}}, validator)
	if err != nil {
		t.Fatal(err)
	}
	_, err = writer.AcquireTarget(context.Background(), "workspace", "notes/a.md")
	requireWritebackError(t, err, foundation.ErrorConsistencyViolation, "WRITEBACK_WORKSPACE_BINDING_INVALID")
}

func newTestWriter(t *testing.T, workspaceRoot string) *Writer {
	t.Helper()
	validator, err := NewDefaultMarkdownValidator()
	if err != nil {
		t.Fatal(err)
	}
	writer, err := NewWriter(writerWorkspaceRepository{workspace: workspacedomain.Workspace{ID: "workspace", RootPath: workspaceRoot}}, validator)
	if err != nil {
		t.Fatal(err)
	}
	return writer
}

type writerWorkspaceRepository struct {
	workspace workspacedomain.Workspace
}

func (r writerWorkspaceRepository) GetWorkspaceByID(context.Context, foundation.ID) (workspacedomain.Workspace, error) {
	return r.workspace, nil
}

func newWritebackWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, directory := range []string{filepath.Join(root, ".git", "info"), filepath.Join(root, "notes")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func prepareCommand(targetPath string, baseContent, resultContent []byte) domain.PrepareWrite {
	baseHash := hashBytes(baseContent)
	return domain.PrepareWrite{
		ExecutionID:        "execution",
		ExpectedBaseHash:   baseHash,
		ApprovedChangeHash: domain.ComputeChangeHash(targetPath, baseHash, string(resultContent)),
		Content:            append([]byte(nil), resultContent...),
	}
}

func hashBytes(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func sameInode(left, right os.FileInfo) bool {
	leftStat, leftOK := left.Sys().(*syscall.Stat_t)
	rightStat, rightOK := right.Sys().(*syscall.Stat_t)
	return leftOK && rightOK && uint64(leftStat.Dev) == uint64(rightStat.Dev) && uint64(leftStat.Ino) == uint64(rightStat.Ino)
}

func requireWritebackError(t *testing.T, err error, kind foundation.ErrorKind, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != kind || classified.Code != code {
		t.Fatalf("error = %#v, want kind=%q code=%q", err, kind, code)
	}
}
