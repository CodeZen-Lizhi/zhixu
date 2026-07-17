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

func TestWriterFaultInjectionSyncBeforeRenameKeepsTarget(t *testing.T) {
	injected := errors.New("injected sync failure")
	for _, test := range []struct {
		name       string
		failAtSync int
		wantCode   string
	}{
		{name: "temporary sync", failAtSync: 1, wantCode: "WRITEBACK_TEMP_SYNC_FAILED"},
		{name: "backup sync", failAtSync: 2, wantCode: "WRITEBACK_BACKUP_SYNC_FAILED"},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspaceRoot, targetPath, absoluteTarget, baseContent, resultContent := newFaultWritebackWorkspace(t)
			syncCalls := 0
			writer := newFaultWriter(t, workspaceRoot, func(operations *writerOperations) {
				operations.syncFile = func(file *os.File) error {
					syncCalls++
					if syncCalls == test.failAtSync {
						return injected
					}
					return file.Sync()
				}
			})
			lock, err := writer.AcquireTarget(context.Background(), "workspace", targetPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if closeErr := lock.Close(); closeErr != nil {
					t.Errorf("Close() = %v", closeErr)
				}
			})

			_, err = lock.Prepare(context.Background(), faultPrepareCommand(targetPath, baseContent, resultContent))
			requireFaultWritebackError(t, err, foundation.ErrorDependencyUnavailable, test.wantCode)
			if got := string(readFaultFile(t, absoluteTarget)); got != string(baseContent) {
				t.Fatalf("target was replaced before rename: %q", got)
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			matches, err := filepath.Glob(filepath.Join(workspaceRoot, "notes", ".zhixu-writeback-*"))
			if err != nil {
				t.Fatal(err)
			}
			if len(matches) != 0 {
				t.Fatalf("managed files remain after prepare failure: %v", matches)
			}
		})
	}
}

func TestWriterFaultInjectionUnknownCommitResultRequiresManualRecovery(t *testing.T) {
	injected := errors.New("injected unknown result")
	for _, test := range []struct {
		name     string
		wantCode string
		mutate   func(*writerOperations)
	}{
		{
			name:     "rename completed but returned error",
			wantCode: "WRITEBACK_RENAME_RESULT_UNKNOWN",
			mutate: func(operations *writerOperations) {
				operations.rename = func(root *os.Root, oldPath, newPath string) error {
					if err := root.Rename(oldPath, newPath); err != nil {
						return err
					}
					return injected
				}
			},
		},
		{
			name:     "parent sync",
			wantCode: "WRITEBACK_PARENT_SYNC_RESULT_UNKNOWN",
			mutate: func(operations *writerOperations) {
				calls := 0
				operations.syncDirectory = func(root *os.Root, relative string) error {
					calls++
					if calls == 2 {
						return injected
					}
					return syncDirectoryRoot(root, relative)
				}
			},
		},
		{
			name:     "result verify",
			wantCode: "WRITEBACK_RESULT_VERIFY_UNKNOWN",
			mutate: func(operations *writerOperations) {
				operations.verifyResult = func(context.Context, *targetLock, domain.AppliedWrite) error {
					return injected
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspaceRoot, targetPath, absoluteTarget, baseContent, resultContent := newFaultWritebackWorkspace(t)
			writer := newFaultWriter(t, workspaceRoot, test.mutate)
			lock, err := writer.AcquireTarget(context.Background(), "workspace", targetPath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if closeErr := lock.Close(); closeErr != nil {
					t.Errorf("Close() = %v", closeErr)
				}
			})

			prepared, err := lock.Prepare(context.Background(), faultPrepareCommand(targetPath, baseContent, resultContent))
			if err != nil {
				t.Fatal(err)
			}
			applied, err := lock.CommitCAS(context.Background(), prepared)
			requireFaultWritebackError(t, err, foundation.ErrorManualRecoveryRequired, test.wantCode)
			if !errors.Is(err, domain.ErrWritebackManualRecoveryRequired) {
				t.Fatalf("CommitCAS() error = %v, want manual recovery cause", err)
			}
			if bindingErr := domain.ValidateAppliedWriteBinding(prepared, applied); bindingErr != nil {
				t.Fatalf("CommitCAS() applied summary is not recoverable: %+v, %v", applied, bindingErr)
			}
			if got := string(readFaultFile(t, absoluteTarget)); got != string(resultContent) {
				t.Fatalf("target after uncertain result = %q, want %q", got, resultContent)
			}
			backupPath := filepath.Join(workspaceRoot, filepath.FromSlash(applied.BackupRef))
			if got := string(readFaultFile(t, backupPath)); got != string(baseContent) {
				t.Fatalf("backup after uncertain result = %q, want %q", got, baseContent)
			}

			replayed, replayErr := lock.CommitCAS(context.Background(), prepared)
			requireFaultWritebackError(t, replayErr, foundation.ErrorManualRecoveryRequired, "WRITEBACK_RESULT_STILL_UNKNOWN")
			if replayed != applied {
				t.Fatalf("CommitCAS() retry changed applied summary: got %+v want %+v", replayed, applied)
			}
		})
	}
}

func TestWriterFaultInjectionUnknownRestoreBlocksCleanupUntilRestartRecovery(t *testing.T) {
	injected := errors.New("restore rename completed before error")
	workspaceRoot, targetPath, absoluteTarget, baseContent, resultContent := newFaultWritebackWorkspace(t)
	renameCalls := 0
	writer := newFaultWriter(t, workspaceRoot, func(operations *writerOperations) {
		operations.rename = func(root *os.Root, oldPath, newPath string) error {
			renameCalls++
			if err := root.Rename(oldPath, newPath); err != nil {
				return err
			}
			if renameCalls == 2 {
				return injected
			}
			return nil
		}
	})
	lock, err := writer.AcquireTarget(context.Background(), "workspace", targetPath)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := lock.Prepare(context.Background(), faultPrepareCommand(targetPath, baseContent, resultContent))
	if err != nil {
		t.Fatal(err)
	}
	applied, err := lock.CommitCAS(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	_, err = lock.RestoreCAS(context.Background(), applied)
	requireFaultWritebackError(t, err, foundation.ErrorManualRecoveryRequired, "WRITEBACK_RESTORE_RENAME_RESULT_UNKNOWN")
	err = lock.Cleanup(context.Background(), applied)
	requireFaultWritebackError(t, err, foundation.ErrorManualRecoveryRequired, "WRITEBACK_RESULT_STILL_UNKNOWN")
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if got := string(readFaultFile(t, absoluteTarget)); got != string(baseContent) {
		t.Fatalf("target after unknown restore = %q", got)
	}

	recovered, _, recoveredApplied, err := newTestWriter(t, workspaceRoot).ResumeTarget(
		context.Background(), "workspace", targetPath, domain.ResumeWrite{Prepared: prepared, Applied: &applied, RestoreMayHaveCompleted: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if recoveredApplied == nil || *recoveredApplied != applied {
		t.Fatalf("recovered applied = %+v", recoveredApplied)
	}
	replayed, err := recovered.RestoreCAS(context.Background(), applied)
	if err != nil || !replayed.Replayed {
		t.Fatalf("RestoreCAS(replay) = %+v, %v", replayed, err)
	}
	if err := recovered.Cleanup(context.Background(), applied); err != nil {
		t.Fatal(err)
	}
}

func TestWriterRestartContinuesPersistedRestoreTempBeforeRename(t *testing.T) {
	injected := errors.New("restore rename did not start")
	workspaceRoot, targetPath, absoluteTarget, baseContent, resultContent := newFaultWritebackWorkspace(t)
	renameCalls := 0
	writer := newFaultWriter(t, workspaceRoot, func(operations *writerOperations) {
		operations.rename = func(root *os.Root, oldPath, newPath string) error {
			renameCalls++
			if renameCalls == 2 {
				return injected
			}
			return root.Rename(oldPath, newPath)
		}
	})
	lock, err := writer.AcquireTarget(context.Background(), "workspace", targetPath)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := lock.Prepare(context.Background(), faultPrepareCommand(targetPath, baseContent, resultContent))
	if err != nil {
		t.Fatal(err)
	}
	applied, err := lock.CommitCAS(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	_, err = lock.RestoreCAS(context.Background(), applied)
	requireFaultWritebackError(t, err, foundation.ErrorManualRecoveryRequired, "WRITEBACK_RESTORE_RENAME_RESULT_UNKNOWN")
	if got := string(readFaultFile(t, filepath.Join(workspaceRoot, filepath.FromSlash(applied.BackupRef)))); got != string(baseContent) {
		t.Fatalf("persisted restore backup = %q", got)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if got := string(readFaultFile(t, absoluteTarget)); got != string(resultContent) {
		t.Fatalf("target before resumed restore = %q", got)
	}

	recovered, _, recoveredApplied, err := newTestWriter(t, workspaceRoot).ResumeTarget(
		context.Background(), "workspace", targetPath, domain.ResumeWrite{Prepared: prepared, Applied: &applied, RestoreMayHaveCompleted: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if recoveredApplied == nil || *recoveredApplied != applied {
		t.Fatalf("recovered applied = %+v", recoveredApplied)
	}
	restored, err := recovered.RestoreCAS(context.Background(), applied)
	if err != nil || !restored.Restored {
		t.Fatalf("RestoreCAS(resume) = %+v, %v", restored, err)
	}
	if got := string(readFaultFile(t, absoluteTarget)); got != string(baseContent) {
		t.Fatalf("target after resumed restore = %q", got)
	}
}

type faultWriterWorkspaceRepository struct {
	workspace workspacedomain.Workspace
}

func (r faultWriterWorkspaceRepository) GetWorkspaceByID(context.Context, foundation.ID) (workspacedomain.Workspace, error) {
	return r.workspace, nil
}

type faultContentValidator struct{}

func (faultContentValidator) Validate(context.Context, []byte) error { return nil }

func newFaultWriter(t *testing.T, workspaceRoot string, mutate func(*writerOperations)) *Writer {
	t.Helper()
	operations := defaultWriterOperations()
	if mutate != nil {
		mutate(&operations)
	}
	writer, err := newWriterWithOperations(
		faultWriterWorkspaceRepository{workspace: workspacedomain.Workspace{ID: "workspace", RootPath: workspaceRoot}},
		faultContentValidator{},
		operations,
	)
	if err != nil {
		t.Fatal(err)
	}
	return writer
}

func newFaultWritebackWorkspace(t *testing.T) (string, string, string, []byte, []byte) {
	t.Helper()
	workspaceRoot := t.TempDir()
	for _, directory := range []string{filepath.Join(workspaceRoot, ".git", "info"), filepath.Join(workspaceRoot, "notes")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	targetPath := "notes/a.md"
	absoluteTarget := filepath.Join(workspaceRoot, filepath.FromSlash(targetPath))
	baseContent := []byte("# base\n")
	resultContent := []byte("# updated\n")
	if err := os.WriteFile(absoluteTarget, baseContent, 0o600); err != nil {
		t.Fatal(err)
	}
	return workspaceRoot, targetPath, absoluteTarget, baseContent, resultContent
}

func faultPrepareCommand(targetPath string, baseContent, resultContent []byte) domain.PrepareWrite {
	baseHash := domain.ComputeWritebackResultHash(baseContent)
	return domain.PrepareWrite{
		ExecutionID:        "fault-execution",
		ExpectedBaseHash:   baseHash,
		ApprovedChangeHash: domain.ComputeChangeHash(targetPath, baseHash, string(resultContent)),
		Content:            append([]byte(nil), resultContent...),
	}
}

func readFaultFile(t *testing.T, filePath string) []byte {
	t.Helper()
	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func requireFaultWritebackError(t *testing.T, err error, kind foundation.ErrorKind, code string) {
	t.Helper()
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Kind != kind || classified.Code != code {
		t.Fatalf("error = %#v, want kind=%q code=%q", err, kind, code)
	}
}
