package localfs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

func TestWriterResumePreparedAppliedCompensationAndCleanupAcrossInstances(t *testing.T) {
	workspaceRoot, targetPath, absoluteTarget, baseContent, resultContent := newFaultWritebackWorkspace(t)
	command := faultPrepareCommand(targetPath, baseContent, resultContent)

	firstLock, err := newTestWriter(t, workspaceRoot).AcquireTarget(context.Background(), "workspace", targetPath)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := firstLock.Prepare(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if err := firstLock.Close(); err != nil {
		t.Fatal(err)
	}

	secondLock, resumedPrepared, resumedApplied, err := newTestWriter(t, workspaceRoot).ResumeTarget(
		context.Background(), "workspace", targetPath, domain.ResumeWrite{Prepared: prepared},
	)
	if err != nil {
		t.Fatal(err)
	}
	if resumedPrepared != prepared || resumedApplied != nil {
		t.Fatalf("prepared resume = %+v applied=%+v", resumedPrepared, resumedApplied)
	}
	applied, err := secondLock.CommitCAS(context.Background(), resumedPrepared)
	if err != nil {
		t.Fatal(err)
	}
	if err := secondLock.Close(); err != nil {
		t.Fatal(err)
	}

	thirdLock, replayedPrepared, recoveredApplied, err := newTestWriter(t, workspaceRoot).ResumeTarget(
		context.Background(), "workspace", targetPath, domain.ResumeWrite{Prepared: prepared},
	)
	if err != nil {
		t.Fatal(err)
	}
	if replayedPrepared != prepared || recoveredApplied == nil || *recoveredApplied != applied {
		t.Fatalf("applied recovery = prepared=%+v applied=%+v", replayedPrepared, recoveredApplied)
	}
	if got := string(readFile(t, absoluteTarget)); got != string(resultContent) {
		t.Fatalf("recovered target = %q", got)
	}
	if err := thirdLock.Close(); err != nil {
		t.Fatal(err)
	}

	fourthLock, _, replayedApplied, err := newTestWriter(t, workspaceRoot).ResumeTarget(
		context.Background(), "workspace", targetPath, domain.ResumeWrite{Prepared: prepared, Applied: &applied},
	)
	if err != nil {
		t.Fatal(err)
	}
	if replayedApplied == nil || *replayedApplied != applied {
		t.Fatalf("repeated recovery applied = %+v", replayedApplied)
	}
	restored, err := fourthLock.RestoreCAS(context.Background(), applied)
	if err != nil || !restored.Restored {
		t.Fatalf("RestoreCAS() = %+v, %v", restored, err)
	}
	if err := fourthLock.Close(); err != nil {
		t.Fatal(err)
	}

	fifthLock, _, compensatedApplied, err := newTestWriter(t, workspaceRoot).ResumeTarget(
		context.Background(), "workspace", targetPath, domain.ResumeWrite{Prepared: prepared, Applied: &applied, RestoreMayHaveCompleted: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if compensatedApplied == nil || *compensatedApplied != applied {
		t.Fatalf("compensated recovery applied = %+v", compensatedApplied)
	}
	replayedRestore, err := fifthLock.RestoreCAS(context.Background(), applied)
	if err != nil || !replayedRestore.Replayed {
		t.Fatalf("RestoreCAS(replay) = %+v, %v", replayedRestore, err)
	}
	if err := fifthLock.Cleanup(context.Background(), applied); err != nil {
		t.Fatal(err)
	}
	if err := fifthLock.Cleanup(context.Background(), applied); err != nil {
		t.Fatal(err)
	}
	if err := fifthLock.Close(); err != nil {
		t.Fatal(err)
	}
	if got := string(readFile(t, absoluteTarget)); got != string(baseContent) {
		t.Fatalf("target after compensation = %q", got)
	}
	for _, locator := range []string{prepared.TemporaryRef, prepared.BackupRef} {
		if _, err := os.Lstat(filepath.Join(workspaceRoot, filepath.FromSlash(locator))); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("cleanup left %q: %v", locator, err)
		}
	}

	sixthLock, _, compensatedCleanup, err := newTestWriter(t, workspaceRoot).ResumeTarget(
		context.Background(), "workspace", targetPath,
		domain.ResumeWrite{Prepared: prepared, Applied: &applied, RestoreMayHaveCompleted: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if compensatedCleanup == nil || *compensatedCleanup != applied {
		t.Fatalf("compensated cleanup replay applied = %+v", compensatedCleanup)
	}
	replayedAfterCleanup, err := sixthLock.RestoreCAS(context.Background(), applied)
	if err != nil || !replayedAfterCleanup.Replayed {
		t.Fatalf("RestoreCAS(after cleanup replay) = %+v, %v", replayedAfterCleanup, err)
	}
	if err := sixthLock.Cleanup(context.Background(), applied); err != nil {
		t.Fatalf("Cleanup(compensated replay) = %v", err)
	}
	if err := sixthLock.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWriterResumeCleanupReplayAfterFinalizeFailure(t *testing.T) {
	workspaceRoot, targetPath, absoluteTarget, baseContent, resultContent := newFaultWritebackWorkspace(t)
	injected := errors.New("cleanup directory sync failed")
	syncCalls := 0
	writer := newFaultWriter(t, workspaceRoot, func(operations *writerOperations) {
		operations.syncDirectory = func(root *os.Root, relative string) error {
			syncCalls++
			if syncCalls == 3 {
				return injected
			}
			return syncDirectoryRoot(root, relative)
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
	err = lock.Cleanup(context.Background(), applied)
	requireWritebackError(t, err, foundation.ErrorDependencyUnavailable, "WRITEBACK_CLEANUP_SYNC_FAILED")
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if got := string(readFile(t, absoluteTarget)); got != string(resultContent) {
		t.Fatalf("published target = %q", got)
	}
	_, _, _, err = newTestWriter(t, workspaceRoot).ResumeTarget(
		context.Background(), "workspace", targetPath, domain.ResumeWrite{Prepared: prepared, Applied: &applied},
	)
	requireWritebackError(t, err, foundation.ErrorManualRecoveryRequired, "WRITEBACK_RESUME_BACKUP_INVALID")

	// 模拟 Cleanup 已完成但数据库 Finalize 失败；新进程必须把双 locator 缺失识别为幂等重放。
	replayWriter := newFaultWriter(t, workspaceRoot, func(operations *writerOperations) {
		operations.syncDirectory = func(*os.Root, string) error { return injected }
	})
	replayedLock, replayedPrepared, replayedApplied, err := replayWriter.ResumeTarget(
		context.Background(), "workspace", targetPath, domain.ResumeWrite{Prepared: prepared, Applied: &applied, CleanupMayHaveCompleted: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if replayedPrepared != prepared || replayedApplied == nil || *replayedApplied != applied {
		t.Fatalf("cleanup replay = prepared=%+v applied=%+v", replayedPrepared, replayedApplied)
	}
	err = replayedLock.Cleanup(context.Background(), applied)
	requireWritebackError(t, err, foundation.ErrorDependencyUnavailable, "WRITEBACK_CLEANUP_SYNC_FAILED")
	if err := replayedLock.Close(); err != nil {
		t.Fatal(err)
	}

	finalLock, _, finalApplied, err := newTestWriter(t, workspaceRoot).ResumeTarget(
		context.Background(), "workspace", targetPath, domain.ResumeWrite{Prepared: prepared, Applied: &applied, CleanupMayHaveCompleted: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if finalApplied == nil || *finalApplied != applied {
		t.Fatalf("final cleanup replay applied = %+v", finalApplied)
	}
	if err := finalLock.Cleanup(context.Background(), applied); err != nil {
		t.Fatalf("Cleanup(final replay) = %v", err)
	}
	if err := finalLock.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWriterResumeCleanupReplayRejectsIdenticalResultWithDifferentIdentity(t *testing.T) {
	workspaceRoot, targetPath, absoluteTarget, baseContent, resultContent := newFaultWritebackWorkspace(t)
	lock, err := newTestWriter(t, workspaceRoot).AcquireTarget(context.Background(), "workspace", targetPath)
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
	if err := lock.Cleanup(context.Background(), applied); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}

	replaceRegularWithSameContent(t, absoluteTarget, resultContent, applied.Mode)
	_, _, _, err = newTestWriter(t, workspaceRoot).ResumeTarget(
		context.Background(), "workspace", targetPath,
		domain.ResumeWrite{Prepared: prepared, Applied: &applied, CleanupMayHaveCompleted: true},
	)
	requireWritebackError(t, err, foundation.ErrorManualRecoveryRequired, "WRITEBACK_RESUME_RESULT_UNKNOWN")
}

func TestWriterResumeRestoreReplayRejectsIdenticalBaseWithDifferentIdentity(t *testing.T) {
	workspaceRoot, targetPath, absoluteTarget, baseContent, resultContent := newFaultWritebackWorkspace(t)
	lock, err := newTestWriter(t, workspaceRoot).AcquireTarget(context.Background(), "workspace", targetPath)
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
	if _, err := lock.RestoreCAS(context.Background(), applied); err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}

	replaceRegularWithSameContent(t, absoluteTarget, baseContent, applied.Mode)
	_, _, _, err = newTestWriter(t, workspaceRoot).ResumeTarget(
		context.Background(), "workspace", targetPath,
		domain.ResumeWrite{Prepared: prepared, Applied: &applied, RestoreMayHaveCompleted: true},
	)
	requireWritebackError(t, err, foundation.ErrorManualRecoveryRequired, "WRITEBACK_RESUME_RESULT_UNKNOWN")
}

func TestWriterResumeAfterRenameBeforeCheckpointAcrossInstances(t *testing.T) {
	workspaceRoot, targetPath, absoluteTarget, baseContent, resultContent := newFaultWritebackWorkspace(t)
	injected := errors.New("rename completed before process failure")
	writer := newFaultWriter(t, workspaceRoot, func(operations *writerOperations) {
		operations.rename = func(root *os.Root, oldPath, newPath string) error {
			if err := root.Rename(oldPath, newPath); err != nil {
				return err
			}
			return injected
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
	unknownApplied, err := lock.CommitCAS(context.Background(), prepared)
	requireWritebackError(t, err, foundation.ErrorManualRecoveryRequired, "WRITEBACK_RENAME_RESULT_UNKNOWN")
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}

	recoveredLock, recoveredPrepared, recoveredApplied, err := newTestWriter(t, workspaceRoot).ResumeTarget(
		context.Background(), "workspace", targetPath, domain.ResumeWrite{Prepared: prepared},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer recoveredLock.Close()
	if recoveredPrepared != prepared || recoveredApplied == nil || *recoveredApplied != unknownApplied {
		t.Fatalf("recovered = prepared=%+v applied=%+v", recoveredPrepared, recoveredApplied)
	}
	if got := string(readFile(t, absoluteTarget)); got != string(resultContent) {
		t.Fatalf("target after recovery = %q", got)
	}
}

func TestWriterResumeRejectsTamperedEvidenceAndPreservesIt(t *testing.T) {
	for _, test := range []struct {
		name     string
		mutate   func(*testing.T, string, domain.PreparedWrite)
		wantCode string
	}{
		{
			name: "temp content",
			mutate: func(t *testing.T, root string, prepared domain.PreparedWrite) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(prepared.TemporaryRef)), []byte("tampered\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantCode: "WRITEBACK_RESUME_RESULT_UNKNOWN",
		},
		{
			name: "backup content",
			mutate: func(t *testing.T, root string, prepared domain.PreparedWrite) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(prepared.BackupRef)), []byte("tampered\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			wantCode: "WRITEBACK_RESUME_RESULT_UNKNOWN",
		},
		{
			name: "backup symlink",
			mutate: func(t *testing.T, root string, prepared domain.PreparedWrite) {
				t.Helper()
				backupPath := filepath.Join(root, filepath.FromSlash(prepared.BackupRef))
				if err := os.Remove(backupPath); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("a.md", backupPath); err != nil {
					t.Fatal(err)
				}
			},
			wantCode: "WRITEBACK_RESUME_BACKUP_INVALID",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspaceRoot, targetPath, absoluteTarget, baseContent, resultContent := newFaultWritebackWorkspace(t)
			lock, err := newTestWriter(t, workspaceRoot).AcquireTarget(context.Background(), "workspace", targetPath)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := lock.Prepare(context.Background(), faultPrepareCommand(targetPath, baseContent, resultContent))
			if err != nil {
				t.Fatal(err)
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, workspaceRoot, prepared)
			resumed, _, _, err := newTestWriter(t, workspaceRoot).ResumeTarget(context.Background(), "workspace", targetPath, domain.ResumeWrite{Prepared: prepared})
			if resumed != nil {
				_ = resumed.Close()
				t.Fatal("tampered evidence returned a lock")
			}
			requireWritebackError(t, err, foundation.ErrorManualRecoveryRequired, test.wantCode)
			if got := string(readFile(t, absoluteTarget)); got != string(baseContent) {
				t.Fatalf("target changed while rejecting evidence: %q", got)
			}
		})
	}
}

func TestWriterResumeRejectsLocatorTamperAndUserEdit(t *testing.T) {
	t.Run("locator", func(t *testing.T) {
		workspaceRoot, targetPath, _, baseContent, resultContent := newFaultWritebackWorkspace(t)
		lock, err := newTestWriter(t, workspaceRoot).AcquireTarget(context.Background(), "workspace", targetPath)
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := lock.Prepare(context.Background(), faultPrepareCommand(targetPath, baseContent, resultContent))
		if err != nil {
			t.Fatal(err)
		}
		if err := lock.Close(); err != nil {
			t.Fatal(err)
		}
		prepared.BackupRef = prepared.TemporaryRef
		_, _, _, err = newTestWriter(t, workspaceRoot).ResumeTarget(context.Background(), "workspace", targetPath, domain.ResumeWrite{Prepared: prepared})
		requireWritebackError(t, err, foundation.ErrorManualRecoveryRequired, "WRITEBACK_RESUME_BINDING_INVALID")
	})

	t.Run("user edit after apply", func(t *testing.T) {
		workspaceRoot, targetPath, absoluteTarget, baseContent, resultContent := newFaultWritebackWorkspace(t)
		lock, err := newTestWriter(t, workspaceRoot).AcquireTarget(context.Background(), "workspace", targetPath)
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
		if err := lock.Close(); err != nil {
			t.Fatal(err)
		}
		userContent := []byte("# user edit\n")
		if err := os.WriteFile(absoluteTarget, userContent, 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, _, err = newTestWriter(t, workspaceRoot).ResumeTarget(
			context.Background(), "workspace", targetPath, domain.ResumeWrite{Prepared: prepared, Applied: &applied},
		)
		requireWritebackError(t, err, foundation.ErrorManualRecoveryRequired, "WRITEBACK_RESUME_RESULT_UNKNOWN")
		if got := string(readFile(t, absoluteTarget)); got != string(userContent) {
			t.Fatalf("user edit was overwritten: %q", got)
		}
	})
}

func TestWriterResumeRejectsIdenticalBaseWithDifferentIdentity(t *testing.T) {
	workspaceRoot, targetPath, absoluteTarget, baseContent, resultContent := newFaultWritebackWorkspace(t)
	lock, err := newTestWriter(t, workspaceRoot).AcquireTarget(context.Background(), "workspace", targetPath)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := lock.Prepare(context.Background(), faultPrepareCommand(targetPath, baseContent, resultContent))
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(absoluteTarget, absoluteTarget+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(absoluteTarget, baseContent, 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, _, err = newTestWriter(t, workspaceRoot).ResumeTarget(
		context.Background(), "workspace", targetPath, domain.ResumeWrite{Prepared: prepared},
	)
	requireWritebackError(t, err, foundation.ErrorManualRecoveryRequired, "WRITEBACK_RESUME_TARGET_IDENTITY_CONFLICT")
}

func TestWriterRestoreRejectsIdenticalResultWithDifferentIdentity(t *testing.T) {
	workspaceRoot, targetPath, absoluteTarget, baseContent, resultContent := newFaultWritebackWorkspace(t)
	lock, err := newTestWriter(t, workspaceRoot).AcquireTarget(context.Background(), "workspace", targetPath)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	prepared, err := lock.Prepare(context.Background(), faultPrepareCommand(targetPath, baseContent, resultContent))
	if err != nil {
		t.Fatal(err)
	}
	applied, err := lock.CommitCAS(context.Background(), prepared)
	if err != nil {
		t.Fatal(err)
	}
	replaceRegularWithSameContent(t, absoluteTarget, resultContent, applied.Mode)
	_, err = lock.RestoreCAS(context.Background(), applied)
	requireWritebackError(t, err, foundation.ErrorVersionConflict, "WRITEBACK_RESTORE_CONFLICT")
	if got := string(readFile(t, absoluteTarget)); got != string(resultContent) {
		t.Fatalf("replacement target was overwritten: %q", got)
	}
}

func TestWriterResumeRejectsIdenticalResultAndEvidenceWithDifferentIdentity(t *testing.T) {
	t.Run("applied target", func(t *testing.T) {
		workspaceRoot, targetPath, absoluteTarget, baseContent, resultContent := newFaultWritebackWorkspace(t)
		lock, err := newTestWriter(t, workspaceRoot).AcquireTarget(context.Background(), "workspace", targetPath)
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
		if err := lock.Close(); err != nil {
			t.Fatal(err)
		}
		replaceRegularWithSameContent(t, absoluteTarget, resultContent, applied.Mode)
		_, _, _, err = newTestWriter(t, workspaceRoot).ResumeTarget(
			context.Background(), "workspace", targetPath, domain.ResumeWrite{Prepared: prepared, Applied: &applied},
		)
		requireWritebackError(t, err, foundation.ErrorManualRecoveryRequired, "WRITEBACK_RESUME_RESULT_UNKNOWN")
		if got := string(readFile(t, absoluteTarget)); got != string(resultContent) {
			t.Fatalf("replacement target was overwritten: %q", got)
		}
	})

	for _, test := range []struct {
		name    string
		locator func(domain.PreparedWrite) string
		content func([]byte, []byte) []byte
	}{
		{name: "result temp", locator: func(prepared domain.PreparedWrite) string { return prepared.TemporaryRef }, content: func(_, result []byte) []byte { return result }},
		{name: "base backup", locator: func(prepared domain.PreparedWrite) string { return prepared.BackupRef }, content: func(base, _ []byte) []byte { return base }},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspaceRoot, targetPath, absoluteTarget, baseContent, resultContent := newFaultWritebackWorkspace(t)
			lock, err := newTestWriter(t, workspaceRoot).AcquireTarget(context.Background(), "workspace", targetPath)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := lock.Prepare(context.Background(), faultPrepareCommand(targetPath, baseContent, resultContent))
			if err != nil {
				t.Fatal(err)
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			managedPath := filepath.Join(workspaceRoot, filepath.FromSlash(test.locator(prepared)))
			replaceRegularWithSameContent(t, managedPath, test.content(baseContent, resultContent), prepared.Mode)
			_, _, _, err = newTestWriter(t, workspaceRoot).ResumeTarget(
				context.Background(), "workspace", targetPath, domain.ResumeWrite{Prepared: prepared},
			)
			requireWritebackError(t, err, foundation.ErrorManualRecoveryRequired, "WRITEBACK_RESUME_RESULT_UNKNOWN")
			if got := string(readFile(t, absoluteTarget)); got != string(baseContent) {
				t.Fatalf("target changed while rejecting replaced evidence: %q", got)
			}
		})
	}
}

func replaceRegularWithSameContent(t *testing.T, target string, content []byte, mode uint32) {
	t.Helper()
	beforeInfo, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	before, err := identityFromInfo(beforeInfo)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(target, target+".replaced"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, content, os.FileMode(mode)); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, os.FileMode(mode)); err != nil {
		t.Fatal(err)
	}
	afterInfo, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	after, err := identityFromInfo(afterInfo)
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("replacement unexpectedly preserved file identity")
	}
}
