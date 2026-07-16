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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/filesystem"
)

const (
	writebackExcludePattern = "**/.zhixu-writeback-*"
	writebackLockDirectory  = ".knowledge/locks"
	defaultLockPollInterval = 10 * time.Millisecond
)

// Writer 将 Change Control WorkspaceStore 端口实现为本地 POSIX 文件系统操作。
type Writer struct {
	workspaces WorkspaceRepository
	validator  domain.ContentValidator
	ops        writerOperations
}

var _ domain.WorkspaceStore = (*Writer)(nil)

type writerOperations struct {
	random           io.Reader
	lockPollInterval time.Duration
	syncFile         func(*os.File) error
	syncDirectory    func(*os.Root, string) error
	rename           func(*os.Root, string, string) error
	verifyResult     func(context.Context, *targetLock, domain.AppliedWrite) error
}

func defaultWriterOperations() writerOperations {
	return writerOperations{
		random:           rand.Reader,
		lockPollInterval: defaultLockPollInterval,
		syncFile:         func(file *os.File) error { return file.Sync() },
		syncDirectory:    syncDirectoryRoot,
		rename:           func(root *os.Root, oldPath, newPath string) error { return root.Rename(oldPath, newPath) },
		verifyResult:     verifyAppliedTarget,
	}
}

// NewWriter 创建 LocalFS WorkspaceStore。
func NewWriter(workspaces WorkspaceRepository, validator domain.ContentValidator) (*Writer, error) {
	return newWriterWithOperations(workspaces, validator, defaultWriterOperations())
}

func newWriterWithOperations(workspaces WorkspaceRepository, validator domain.ContentValidator, operations writerOperations) (*Writer, error) {
	if workspaces == nil {
		return nil, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_WORKSPACE_REPOSITORY_UNAVAILABLE", false, errors.New("workspace repository is required"))
	}
	if validator == nil {
		return nil, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_CONTENT_VALIDATOR_UNAVAILABLE", false, errors.New("content validator is required"))
	}
	defaults := defaultWriterOperations()
	if operations.random == nil {
		operations.random = defaults.random
	}
	if operations.lockPollInterval <= 0 {
		operations.lockPollInterval = defaults.lockPollInterval
	}
	if operations.syncFile == nil {
		operations.syncFile = defaults.syncFile
	}
	if operations.syncDirectory == nil {
		operations.syncDirectory = defaults.syncDirectory
	}
	if operations.rename == nil {
		operations.rename = defaults.rename
	}
	if operations.verifyResult == nil {
		operations.verifyResult = defaults.verifyResult
	}
	return &Writer{workspaces: workspaces, validator: validator, ops: operations}, nil
}

// AcquireTarget 校验 Workspace 目标并获取基于 device/inode 的跨进程 advisory lock。
func (w *Writer) AcquireTarget(ctx context.Context, workspaceID foundation.ID, targetPath string) (domain.TargetLock, error) {
	if ctx == nil {
		return nil, writebackError(foundation.ErrorInvalidInput, "WRITEBACK_CONTEXT_INVALID", false, errors.New("context is nil"))
	}
	if err := ctx.Err(); err != nil {
		return nil, writebackContextError("WRITEBACK_TARGET_LOCK_CANCELLED", err)
	}
	if err := domain.ValidateWorkspaceTarget(workspaceID, targetPath); err != nil {
		return nil, writebackError(foundation.ErrorInvalidInput, "WRITEBACK_TARGET_INVALID", false, err)
	}
	workspace, err := w.workspaces.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	if workspace.ID != workspaceID {
		return nil, writebackError(foundation.ErrorConsistencyViolation, "WRITEBACK_WORKSPACE_BINDING_INVALID", false, errors.New("workspace repository returned a different workspace"))
	}
	canonicalRoot, err := filesystem.NewRoot(workspace.RootPath)
	if err != nil {
		return nil, writebackError(foundation.ErrorDependencyUnavailable, "WORKSPACE_ROOT_UNAVAILABLE", false, err)
	}
	root, err := os.OpenRoot(canonicalRoot.Path())
	if err != nil {
		return nil, writebackError(foundation.ErrorDependencyUnavailable, "WORKSPACE_ROOT_UNAVAILABLE", false, err)
	}
	closeRoot := true
	defer func() {
		if closeRoot {
			_ = root.Close()
		}
	}()
	rootIdentity, err := directoryIdentity(root, ".")
	if err != nil {
		return nil, writebackError(foundation.ErrorDependencyUnavailable, "WORKSPACE_ROOT_UNAVAILABLE", false, err)
	}
	if err := validateTargetParents(root, targetPath, rootIdentity.Device); err != nil {
		return nil, err
	}
	target, err := openSafeRegular(root, targetPath, rootIdentity.Device)
	if err != nil {
		return nil, err
	}
	identity := target.identity
	mode := target.mode
	if err := target.file.Close(); err != nil {
		return nil, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_TARGET_CLOSE_FAILED", true, err)
	}
	if err := ensureLockDirectory(root, rootIdentity.Device); err != nil {
		return nil, err
	}
	lockPath := path.Join(writebackLockDirectory, lockFileName(identity))
	lockFile, err := root.OpenFile(lockPath, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_TARGET_LOCK_OPEN_FAILED", true, err)
	}
	closeLock := true
	defer func() {
		if closeLock {
			_ = lockFile.Close()
		}
	}()
	lockInfo, err := lockFile.Stat()
	if err != nil || !lockInfo.Mode().IsRegular() {
		return nil, writebackError(foundation.ErrorPermissionDenied, "WRITEBACK_TARGET_LOCK_UNSAFE", false, errors.New("lock file is not a regular file"))
	}
	lockIdentity, err := identityFromInfo(lockInfo)
	if err != nil || lockIdentity.Owner != uint32(os.Geteuid()) || lockIdentity.Device != rootIdentity.Device {
		return nil, writebackError(foundation.ErrorPermissionDenied, "WRITEBACK_TARGET_LOCK_UNSAFE", false, errors.New("lock file ownership or device is unsafe"))
	}
	if err := lockFile.Chmod(0o600); err != nil {
		return nil, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_TARGET_LOCK_OPEN_FAILED", true, err)
	}
	if err := acquireFileLock(ctx, lockFile, w.ops.lockPollInterval); err != nil {
		return nil, err
	}
	locked := true
	defer func() {
		if locked {
			_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		}
	}()
	if err := validateTargetParents(root, targetPath, rootIdentity.Device); err != nil {
		return nil, err
	}
	rechecked, err := openSafeRegular(root, targetPath, rootIdentity.Device)
	if err != nil {
		return nil, err
	}
	recheckedIdentity := rechecked.identity
	closeErr := rechecked.file.Close()
	if closeErr != nil {
		return nil, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_TARGET_CLOSE_FAILED", true, closeErr)
	}
	if recheckedIdentity != identity {
		return nil, writebackError(foundation.ErrorVersionConflict, "TARGET_IDENTITY_CONFLICT", false, domain.ErrTargetIdentityConflict)
	}
	if err := canonicalRoot.EnsureGitExcludePatterns("/.knowledge/", writebackExcludePattern); err != nil {
		return nil, err
	}
	lockToken, err := randomHex(w.ops.random, sha256.Size)
	if err != nil {
		return nil, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_RANDOM_GENERATION_FAILED", true, err)
	}
	closeRoot = false
	closeLock = false
	locked = false
	return &targetLock{
		root:         root,
		rootDevice:   rootIdentity.Device,
		targetPath:   targetPath,
		parentPath:   path.Dir(targetPath),
		identity:     identity,
		mode:         mode,
		lockFile:     lockFile,
		lockToken:    lockToken,
		validator:    w.validator,
		ops:          w.ops,
		managedFiles: make(map[string]fileIdentity),
	}, nil
}

type targetLock struct {
	mu            sync.Mutex
	root          *os.Root
	rootDevice    uint64
	targetPath    string
	parentPath    string
	identity      fileIdentity
	mode          uint32
	lockFile      *os.File
	lockToken     string
	validator     domain.ContentValidator
	ops           writerOperations
	prepare       *domain.PrepareWrite
	prepared      *domain.PreparedWrite
	applied       *domain.AppliedWrite
	managedFiles  map[string]fileIdentity
	resultUnknown bool
	closed        bool
}

var _ domain.TargetLock = (*targetLock)(nil)

// Prepare 校验批准绑定并创建同目录、已 fsync 的受控临时文件。
func (l *targetLock) Prepare(ctx context.Context, command domain.PrepareWrite) (domain.PreparedWrite, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.usable(ctx); err != nil {
		return domain.PreparedWrite{}, err
	}
	resultHash, err := domain.ValidatePrepareWrite(l.targetPath, command)
	if err != nil {
		return domain.PreparedWrite{}, classifyDomainWritebackError("WRITEBACK_PREPARE_INVALID", err)
	}
	content := append([]byte(nil), command.Content...)
	command.Content = content
	if l.prepared != nil {
		if err := domain.ValidatePreparedWriteBinding(l.targetPath, command, *l.prepared); err != nil || !equalPrepareWrite(command, *l.prepare) {
			return domain.PreparedWrite{}, writebackError(foundation.ErrorVersionConflict, "WRITEBACK_PREPARED_BINDING_CONFLICT", false, domain.ErrWritebackIdentityConflict)
		}
		if err := l.verifyManagedFile(ctx, l.prepared.TemporaryRef, l.prepared.ResultHash, l.prepared.ByteSize, l.prepared.Mode); err != nil {
			return domain.PreparedWrite{}, err
		}
		return *l.prepared, nil
	}
	if err := l.validator.Validate(ctx, content); err != nil {
		return domain.PreparedWrite{}, err
	}
	temporaryRef, file, err := l.createManagedFile(command.ExecutionID, ".tmp")
	if err != nil {
		return domain.PreparedWrite{}, err
	}
	removeTemporary := true
	defer func() {
		_ = file.Close()
		if removeTemporary {
			_ = l.removeManagedFile(temporaryRef)
		}
	}()
	if err := writeAllWithContext(ctx, file, content); err != nil {
		return domain.PreparedWrite{}, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_TEMP_WRITE_FAILED", true, err)
	}
	if err := file.Chmod(fileModeFromUnix(l.mode)); err != nil {
		return domain.PreparedWrite{}, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_TEMP_MODE_FAILED", true, err)
	}
	if err := l.ops.syncFile(file); err != nil {
		return domain.PreparedWrite{}, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_TEMP_SYNC_FAILED", true, err)
	}
	if err := file.Close(); err != nil {
		return domain.PreparedWrite{}, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_TEMP_CLOSE_FAILED", true, err)
	}
	prepared := domain.PreparedWrite{
		ExecutionID:        command.ExecutionID,
		TemporaryRef:       temporaryRef,
		ExpectedBaseHash:   strings.ToLower(command.ExpectedBaseHash),
		ApprovedChangeHash: strings.ToLower(command.ApprovedChangeHash),
		ResultHash:         resultHash,
		ByteSize:           int64(len(content)),
		Mode:               l.mode,
		LockToken:          l.lockToken,
	}
	if err := domain.ValidatePreparedWriteBinding(l.targetPath, command, prepared); err != nil {
		return domain.PreparedWrite{}, classifyDomainWritebackError("WRITEBACK_PREPARED_BINDING_CONFLICT", err)
	}
	if err := l.verifyManagedFile(ctx, prepared.TemporaryRef, prepared.ResultHash, prepared.ByteSize, prepared.Mode); err != nil {
		return domain.PreparedWrite{}, err
	}
	l.prepare = &command
	l.prepared = &prepared
	removeTemporary = false
	return prepared, nil
}

// CommitCAS 在最终身份和 Base Hash 校验后备份旧文件并原子替换目标。
func (l *targetLock) CommitCAS(ctx context.Context, prepared domain.PreparedWrite) (domain.AppliedWrite, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.usable(ctx); err != nil {
		return domain.AppliedWrite{}, err
	}
	if l.prepared == nil || l.prepare == nil || domain.ValidatePreparedWriteBinding(l.targetPath, *l.prepare, prepared) != nil || prepared != *l.prepared {
		return domain.AppliedWrite{}, writebackError(foundation.ErrorVersionConflict, "WRITEBACK_PREPARED_BINDING_CONFLICT", false, domain.ErrWritebackIdentityConflict)
	}
	if l.applied != nil {
		if l.resultUnknown {
			return *l.applied, manualRecoveryError("WRITEBACK_RESULT_STILL_UNKNOWN", domain.ErrWritebackManualRecoveryRequired)
		}
		if err := domain.ValidateAppliedWriteBinding(prepared, *l.applied); err != nil {
			return domain.AppliedWrite{}, writebackError(foundation.ErrorConsistencyViolation, "WRITEBACK_APPLIED_BINDING_INVALID", false, err)
		}
		if err := l.ops.verifyResult(ctx, l, *l.applied); err != nil {
			return *l.applied, err
		}
		return *l.applied, nil
	}
	if err := validateTargetParents(l.root, l.targetPath, l.rootDevice); err != nil {
		return domain.AppliedWrite{}, err
	}
	target, err := openSafeRegular(l.root, l.targetPath, l.rootDevice)
	if err != nil {
		return domain.AppliedWrite{}, err
	}
	defer target.file.Close()
	if target.identity != l.identity {
		return domain.AppliedWrite{}, writebackError(foundation.ErrorVersionConflict, "TARGET_IDENTITY_CONFLICT", false, domain.ErrTargetIdentityConflict)
	}
	if err := l.verifyManagedFile(ctx, prepared.TemporaryRef, prepared.ResultHash, prepared.ByteSize, prepared.Mode); err != nil {
		return domain.AppliedWrite{}, err
	}
	backupRef, backup, err := l.createManagedFile(prepared.ExecutionID, ".bak")
	if err != nil {
		return domain.AppliedWrite{}, err
	}
	removeBackup := true
	defer func() {
		_ = backup.Close()
		if removeBackup {
			_ = l.removeManagedFile(backupRef)
		}
	}()
	baseHash, _, err := copyAndHashWithContext(ctx, backup, target.file)
	if err != nil {
		return domain.AppliedWrite{}, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_BACKUP_WRITE_FAILED", true, err)
	}
	if !strings.EqualFold(baseHash, prepared.ExpectedBaseHash) {
		return domain.AppliedWrite{}, writebackError(foundation.ErrorVersionConflict, "TARGET_BASE_HASH_CONFLICT", false, domain.ErrTargetBaseHashConflict)
	}
	if err := backup.Chmod(fileModeFromUnix(prepared.Mode)); err != nil {
		return domain.AppliedWrite{}, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_BACKUP_MODE_FAILED", true, err)
	}
	if err := l.ops.syncFile(backup); err != nil {
		return domain.AppliedWrite{}, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_BACKUP_SYNC_FAILED", true, err)
	}
	if err := backup.Close(); err != nil {
		return domain.AppliedWrite{}, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_BACKUP_CLOSE_FAILED", true, err)
	}
	applied := domain.AppliedWrite{
		ExecutionID:        prepared.ExecutionID,
		TemporaryRef:       prepared.TemporaryRef,
		BackupRef:          backupRef,
		BaseHash:           prepared.ExpectedBaseHash,
		ApprovedChangeHash: prepared.ApprovedChangeHash,
		ResultHash:         prepared.ResultHash,
		ByteSize:           prepared.ByteSize,
		Mode:               prepared.Mode,
		LockToken:          prepared.LockToken,
	}
	if err := domain.ValidateAppliedWriteBinding(prepared, applied); err != nil {
		return domain.AppliedWrite{}, classifyDomainWritebackError("WRITEBACK_APPLIED_BINDING_INVALID", err)
	}
	removeBackup = false
	l.applied = &applied
	if err := l.ops.rename(l.root, prepared.TemporaryRef, l.targetPath); err != nil {
		l.resultUnknown = true
		return applied, manualRecoveryError("WRITEBACK_RENAME_RESULT_UNKNOWN", err)
	}
	if err := l.ops.syncDirectory(l.root, l.parentPath); err != nil {
		l.resultUnknown = true
		return applied, manualRecoveryError("WRITEBACK_PARENT_SYNC_RESULT_UNKNOWN", err)
	}
	if err := l.ops.verifyResult(ctx, l, applied); err != nil {
		l.resultUnknown = true
		return applied, manualRecoveryError("WRITEBACK_RESULT_VERIFY_UNKNOWN", err)
	}
	return applied, nil
}

// RestoreCAS 只在目标仍为系统 Result Hash 时恢复已验证的 Base backup。
func (l *targetLock) RestoreCAS(ctx context.Context, applied domain.AppliedWrite) (domain.RestoreResult, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.usable(ctx); err != nil {
		return domain.RestoreResult{}, err
	}
	if l.prepared == nil || l.applied == nil || domain.ValidateAppliedWriteBinding(*l.prepared, applied) != nil || applied != *l.applied {
		return domain.RestoreResult{}, writebackError(foundation.ErrorVersionConflict, "WRITEBACK_APPLIED_BINDING_CONFLICT", false, domain.ErrWritebackIdentityConflict)
	}
	currentHash, _, _, err := hashSafeRegular(ctx, l.root, l.targetPath, l.rootDevice)
	if err != nil {
		return domain.RestoreResult{}, err
	}
	if strings.EqualFold(currentHash, applied.BaseHash) {
		l.resultUnknown = false
		return domain.RestoreResult{Replayed: true}, nil
	}
	if !strings.EqualFold(currentHash, applied.ResultHash) {
		return domain.RestoreResult{}, writebackError(foundation.ErrorVersionConflict, "WRITEBACK_RESTORE_CONFLICT", false, domain.ErrWritebackRestoreConflict)
	}
	if err := l.verifyManagedFileHash(ctx, applied.BackupRef, applied.BaseHash, applied.Mode); err != nil {
		return domain.RestoreResult{}, err
	}
	restoreRef, restore, err := l.createManagedFile(applied.ExecutionID, ".tmp")
	if err != nil {
		return domain.RestoreResult{}, err
	}
	removeRestore := true
	defer func() {
		_ = restore.Close()
		if removeRestore {
			_ = l.removeManagedFile(restoreRef)
		}
	}()
	backup, err := l.openManagedRegular(applied.BackupRef)
	if err != nil {
		return domain.RestoreResult{}, err
	}
	_, _, copyErr := copyAndHashWithContext(ctx, restore, backup.file)
	closeBackupErr := backup.file.Close()
	if copyErr == nil {
		copyErr = closeBackupErr
	}
	if copyErr != nil {
		return domain.RestoreResult{}, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_RESTORE_TEMP_WRITE_FAILED", true, copyErr)
	}
	if err := restore.Chmod(fileModeFromUnix(applied.Mode)); err != nil {
		return domain.RestoreResult{}, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_RESTORE_TEMP_MODE_FAILED", true, err)
	}
	if err := l.ops.syncFile(restore); err != nil {
		return domain.RestoreResult{}, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_RESTORE_TEMP_SYNC_FAILED", true, err)
	}
	if err := restore.Close(); err != nil {
		return domain.RestoreResult{}, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_RESTORE_TEMP_CLOSE_FAILED", true, err)
	}
	if err := l.verifyManagedFileHash(ctx, restoreRef, applied.BaseHash, applied.Mode); err != nil {
		return domain.RestoreResult{}, err
	}
	if err := l.ops.rename(l.root, restoreRef, l.targetPath); err != nil {
		return domain.RestoreResult{}, manualRecoveryError("WRITEBACK_RESTORE_RENAME_RESULT_UNKNOWN", err)
	}
	removeRestore = false
	if err := l.ops.syncDirectory(l.root, l.parentPath); err != nil {
		return domain.RestoreResult{}, manualRecoveryError("WRITEBACK_RESTORE_PARENT_SYNC_RESULT_UNKNOWN", err)
	}
	restoredHash, restoredMode, _, err := hashSafeRegular(ctx, l.root, l.targetPath, l.rootDevice)
	if err != nil || !strings.EqualFold(restoredHash, applied.BaseHash) || restoredMode != applied.Mode {
		if err == nil {
			err = errors.New("restored target does not match base backup")
		}
		return domain.RestoreResult{}, manualRecoveryError("WRITEBACK_RESTORE_VERIFY_UNKNOWN", err)
	}
	l.resultUnknown = false
	return domain.RestoreResult{Restored: true}, nil
}

// Cleanup 幂等删除当前锁生成的受控 temp/backup，并同步目标父目录。
func (l *targetLock) Cleanup(ctx context.Context, applied domain.AppliedWrite) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.usable(ctx); err != nil {
		return err
	}
	if l.prepared == nil || l.applied == nil || domain.ValidateAppliedWriteBinding(*l.prepared, applied) != nil || applied != *l.applied {
		return writebackError(foundation.ErrorVersionConflict, "WRITEBACK_APPLIED_BINDING_CONFLICT", false, domain.ErrWritebackIdentityConflict)
	}
	if l.resultUnknown {
		return manualRecoveryError("WRITEBACK_RESULT_STILL_UNKNOWN", domain.ErrWritebackManualRecoveryRequired)
	}
	removed := false
	for _, locator := range []string{applied.TemporaryRef, applied.BackupRef} {
		expectedHash := applied.ResultHash
		if locator == applied.BackupRef {
			expectedHash = applied.BaseHash
		}
		wasRemoved, err := l.removeManagedFileChecked(ctx, locator, expectedHash, applied.Mode)
		if err != nil {
			return err
		}
		removed = removed || wasRemoved
	}
	if removed {
		if err := l.ops.syncDirectory(l.root, l.parentPath); err != nil {
			return writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_CLEANUP_SYNC_FAILED", true, err)
		}
	}
	return nil
}

// Close 清理未提交临时文件并释放 advisory lock 与 Root 文件描述符。
func (l *targetLock) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	var failures []error
	if l.applied == nil && l.prepared != nil {
		if _, err := l.removeManagedFileChecked(context.Background(), l.prepared.TemporaryRef, l.prepared.ResultHash, l.prepared.Mode); err != nil {
			failures = append(failures, err)
		}
	}
	if l.applied == nil {
		for locator := range l.managedFiles {
			if l.prepared != nil && locator == l.prepared.TemporaryRef {
				continue
			}
			if err := l.removeManagedFile(locator); err != nil {
				failures = append(failures, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_CLOSE_CLEANUP_FAILED", true, err))
			}
		}
	}
	if l.lockFile != nil {
		if err := syscall.Flock(int(l.lockFile.Fd()), syscall.LOCK_UN); err != nil {
			failures = append(failures, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_TARGET_UNLOCK_FAILED", true, err))
		}
		if err := l.lockFile.Close(); err != nil {
			failures = append(failures, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_TARGET_LOCK_CLOSE_FAILED", true, err))
		}
	}
	if l.root != nil {
		if err := l.root.Close(); err != nil {
			failures = append(failures, writebackError(foundation.ErrorDependencyUnavailable, "WORKSPACE_ROOT_CLOSE_FAILED", true, err))
		}
	}
	l.closed = true
	return errors.Join(failures...)
}

func (l *targetLock) usable(ctx context.Context) error {
	if l == nil || l.closed || l.root == nil || l.lockFile == nil {
		return writebackError(foundation.ErrorNonRetryableFailure, "WRITEBACK_TARGET_LOCK_CLOSED", false, errors.New("target lock is closed"))
	}
	if ctx == nil {
		return writebackError(foundation.ErrorInvalidInput, "WRITEBACK_CONTEXT_INVALID", false, errors.New("context is nil"))
	}
	if err := ctx.Err(); err != nil {
		return writebackContextError("WRITEBACK_OPERATION_CANCELLED", err)
	}
	return nil
}

type fileIdentity struct {
	Device uint64
	Inode  uint64
	Owner  uint32
}

type openedRegular struct {
	file     *os.File
	identity fileIdentity
	mode     uint32
}

func directoryIdentity(root *os.Root, relative string) (fileIdentity, error) {
	directory, err := root.Open(relative)
	if err != nil {
		return fileIdentity{}, err
	}
	defer directory.Close()
	info, err := directory.Stat()
	if err != nil {
		return fileIdentity{}, err
	}
	if !info.IsDir() {
		return fileIdentity{}, errors.New("path is not a directory")
	}
	return identityFromInfo(info)
}

func identityFromInfo(info os.FileInfo) (fileIdentity, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return fileIdentity{}, errors.New("filesystem identity is unavailable")
	}
	return fileIdentity{Device: uint64(stat.Dev), Inode: uint64(stat.Ino), Owner: stat.Uid}, nil
}

func unixModeFromInfo(info os.FileInfo) (uint32, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return 0, errors.New("filesystem mode is unavailable")
	}
	return uint32(stat.Mode) & 0o7777, nil
}

func fileModeFromUnix(mode uint32) os.FileMode {
	result := os.FileMode(mode & 0o777)
	if mode&0o4000 != 0 {
		result |= os.ModeSetuid
	}
	if mode&0o2000 != 0 {
		result |= os.ModeSetgid
	}
	if mode&0o1000 != 0 {
		result |= os.ModeSticky
	}
	return result
}

func validateTargetParents(root *os.Root, targetPath string, rootDevice uint64) error {
	directory := path.Dir(targetPath)
	if directory == "." {
		return nil
	}
	current := ""
	for _, component := range strings.Split(directory, "/") {
		current = path.Join(current, component)
		info, err := root.Lstat(current)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return writebackError(foundation.ErrorNotFound, "WRITEBACK_TARGET_PARENT_NOT_FOUND", false, err)
			}
			return writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_TARGET_PARENT_READ_FAILED", true, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return writebackError(foundation.ErrorPermissionDenied, "WRITEBACK_TARGET_PARENT_UNSAFE", false, errors.New("target parent is a symlink or non-directory"))
		}
		identity, err := identityFromInfo(info)
		if err != nil || identity.Device != rootDevice {
			return writebackError(foundation.ErrorPermissionDenied, "WRITEBACK_TARGET_CROSS_DEVICE", false, errors.New("target parent crosses workspace device"))
		}
	}
	return nil
}

func openSafeRegular(root *os.Root, relative string, rootDevice uint64) (openedRegular, error) {
	info, err := root.Lstat(relative)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return openedRegular{}, writebackError(foundation.ErrorNotFound, "WRITEBACK_TARGET_NOT_FOUND", false, err)
		}
		return openedRegular{}, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_TARGET_READ_FAILED", true, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return openedRegular{}, writebackError(foundation.ErrorPermissionDenied, "WRITEBACK_TARGET_UNSAFE", false, errors.New("target is a symlink or non-regular file"))
	}
	lstatIdentity, err := identityFromInfo(info)
	if err != nil {
		return openedRegular{}, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_TARGET_IDENTITY_UNAVAILABLE", false, err)
	}
	file, err := root.OpenFile(relative, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return openedRegular{}, writebackError(foundation.ErrorPermissionDenied, "WRITEBACK_TARGET_UNSAFE", false, err)
	}
	openedInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return openedRegular{}, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_TARGET_READ_FAILED", true, err)
	}
	openedIdentity, err := identityFromInfo(openedInfo)
	if err != nil || !openedInfo.Mode().IsRegular() || openedIdentity != lstatIdentity {
		_ = file.Close()
		return openedRegular{}, writebackError(foundation.ErrorVersionConflict, "TARGET_IDENTITY_CONFLICT", false, domain.ErrTargetIdentityConflict)
	}
	if openedInfo.Size() < 0 || openedInfo.Size() > int64(domain.MaxWritebackContentBytes) {
		_ = file.Close()
		return openedRegular{}, writebackError(foundation.ErrorInvalidInput, "WRITEBACK_TARGET_TOO_LARGE", false, errors.New("target exceeds writeback size limit"))
	}
	if err := validateTargetIdentitySecurity(openedIdentity, rootDevice, uint32(os.Geteuid())); err != nil {
		_ = file.Close()
		return openedRegular{}, err
	}
	mode, err := unixModeFromInfo(openedInfo)
	if err != nil {
		_ = file.Close()
		return openedRegular{}, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_TARGET_MODE_UNAVAILABLE", false, err)
	}
	return openedRegular{file: file, identity: openedIdentity, mode: mode}, nil
}

func validateTargetIdentitySecurity(identity fileIdentity, rootDevice uint64, currentOwner uint32) error {
	if identity.Device != rootDevice {
		return writebackError(foundation.ErrorPermissionDenied, "WRITEBACK_TARGET_CROSS_DEVICE", false, errors.New("target crosses workspace device"))
	}
	if identity.Owner != currentOwner {
		return writebackError(foundation.ErrorPermissionDenied, "WRITEBACK_TARGET_OWNER_INVALID", false, errors.New("target is not owned by the current process user"))
	}
	return nil
}

func ensureLockDirectory(root *os.Root, rootDevice uint64) error {
	for _, relative := range []string{".knowledge", writebackLockDirectory} {
		info, err := root.Lstat(relative)
		if errors.Is(err, os.ErrNotExist) {
			if err := root.Mkdir(relative, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_LOCK_DIRECTORY_CREATE_FAILED", true, err)
			}
			info, err = root.Lstat(relative)
		}
		if err != nil {
			return writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_LOCK_DIRECTORY_OPEN_FAILED", true, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return writebackError(foundation.ErrorPermissionDenied, "WRITEBACK_LOCK_DIRECTORY_UNSAFE", false, errors.New("lock directory is a symlink or non-directory"))
		}
		identity, identityErr := identityFromInfo(info)
		if identityErr != nil || identity.Device != rootDevice || identity.Owner != uint32(os.Geteuid()) {
			return writebackError(foundation.ErrorPermissionDenied, "WRITEBACK_LOCK_DIRECTORY_UNSAFE", false, errors.New("lock directory ownership or device is unsafe"))
		}
		directory, openErr := root.Open(relative)
		if openErr != nil {
			return writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_LOCK_DIRECTORY_OPEN_FAILED", true, openErr)
		}
		chmodErr := directory.Chmod(0o700)
		closeErr := directory.Close()
		if chmodErr != nil {
			return writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_LOCK_DIRECTORY_MODE_FAILED", true, chmodErr)
		}
		if closeErr != nil {
			return writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_LOCK_DIRECTORY_CLOSE_FAILED", true, closeErr)
		}
	}
	return nil
}

func lockFileName(identity fileIdentity) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d:%d", identity.Device, identity.Inode)))
	return hex.EncodeToString(sum[:]) + ".lock"
}

func acquireFileLock(ctx context.Context, file *os.File, pollInterval time.Duration) error {
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			return writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_TARGET_LOCK_FAILED", true, err)
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return writebackError(foundation.ErrorRetryableFailure, "WRITEBACK_TARGET_LOCK_TIMEOUT", true, errors.Join(domain.ErrTargetLockUnavailable, ctx.Err()))
			}
			return writebackError(foundation.ErrorNonRetryableFailure, "WRITEBACK_TARGET_LOCK_CANCELLED", false, errors.Join(domain.ErrTargetLockUnavailable, ctx.Err()))
		case <-ticker.C:
		}
	}
}

func (l *targetLock) createManagedFile(executionID foundation.ID, suffix string) (string, *os.File, error) {
	executionDigest := sha256.Sum256([]byte(executionID))
	for range 10 {
		randomPart, err := randomHex(l.ops.random, 16)
		if err != nil {
			return "", nil, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_RANDOM_GENERATION_FAILED", true, err)
		}
		name := fmt.Sprintf(".zhixu-writeback-%s-%s%s", hex.EncodeToString(executionDigest[:8]), randomPart, suffix)
		locator := path.Join(l.parentPath, name)
		file, err := l.root.OpenFile(locator, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", nil, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_MANAGED_FILE_CREATE_FAILED", true, err)
		}
		info, statErr := file.Stat()
		if statErr != nil {
			_ = file.Close()
			_ = l.root.Remove(locator)
			return "", nil, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_MANAGED_FILE_CREATE_FAILED", true, statErr)
		}
		identity, identityErr := identityFromInfo(info)
		if identityErr != nil {
			_ = file.Close()
			_ = l.root.Remove(locator)
			return "", nil, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_MANAGED_FILE_CREATE_FAILED", true, identityErr)
		}
		l.managedFiles[locator] = identity
		return locator, file, nil
	}
	return "", nil, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_MANAGED_FILE_CREATE_FAILED", true, errors.New("unable to allocate unique managed file"))
}

func (l *targetLock) openManagedRegular(locator string) (openedRegular, error) {
	expectedIdentity, ok := l.managedFiles[locator]
	if !ok || path.Dir(locator) != l.parentPath {
		return openedRegular{}, writebackError(foundation.ErrorPermissionDenied, "WRITEBACK_MANAGED_FILE_REFERENCE_INVALID", false, domain.ErrWritebackIdentityConflict)
	}
	file, err := openSafeRegular(l.root, locator, l.rootDevice)
	if err != nil {
		return openedRegular{}, writebackError(foundation.ErrorConsistencyViolation, "WRITEBACK_MANAGED_FILE_INVALID", false, err)
	}
	if file.identity != expectedIdentity {
		_ = file.file.Close()
		return openedRegular{}, writebackError(foundation.ErrorConsistencyViolation, "WRITEBACK_MANAGED_FILE_IDENTITY_CONFLICT", false, domain.ErrTargetIdentityConflict)
	}
	return file, nil
}

func (l *targetLock) verifyManagedFile(ctx context.Context, locator, expectedHash string, expectedSize int64, expectedMode uint32) error {
	file, err := l.openManagedRegular(locator)
	if err != nil {
		return err
	}
	defer file.file.Close()
	hash, size, err := hashReaderWithContext(ctx, file.file)
	if err != nil {
		return writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_MANAGED_FILE_VERIFY_FAILED", true, err)
	}
	if !strings.EqualFold(hash, expectedHash) || size != expectedSize || file.mode != expectedMode {
		return writebackError(foundation.ErrorConsistencyViolation, "WRITEBACK_MANAGED_FILE_CONTENT_CONFLICT", false, errors.New("managed file does not match prepared content"))
	}
	return nil
}

func (l *targetLock) verifyManagedFileHash(ctx context.Context, locator, expectedHash string, expectedMode uint32) error {
	file, err := l.openManagedRegular(locator)
	if err != nil {
		return err
	}
	defer file.file.Close()
	hash, _, err := hashReaderWithContext(ctx, file.file)
	if err != nil {
		return writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_MANAGED_FILE_VERIFY_FAILED", true, err)
	}
	if !strings.EqualFold(hash, expectedHash) || file.mode != expectedMode {
		return writebackError(foundation.ErrorConsistencyViolation, "WRITEBACK_MANAGED_FILE_CONTENT_CONFLICT", false, errors.New("managed file does not match expected backup"))
	}
	return nil
}

func (l *targetLock) removeManagedFile(locator string) error {
	expectedIdentity, ok := l.managedFiles[locator]
	if !ok {
		return nil
	}
	info, err := l.root.Lstat(locator)
	if errors.Is(err, os.ErrNotExist) {
		delete(l.managedFiles, locator)
		return nil
	}
	if err != nil {
		return err
	}
	identity, identityErr := identityFromInfo(info)
	if identityErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || identity != expectedIdentity {
		return errors.New("managed file identity changed before cleanup")
	}
	if err := l.root.Remove(locator); err != nil {
		return err
	}
	delete(l.managedFiles, locator)
	return nil
}

func (l *targetLock) removeManagedFileChecked(ctx context.Context, locator, expectedHash string, expectedMode uint32) (bool, error) {
	expectedIdentity, ok := l.managedFiles[locator]
	if path.Dir(locator) != l.parentPath {
		return false, writebackError(foundation.ErrorPermissionDenied, "WRITEBACK_MANAGED_FILE_REFERENCE_INVALID", false, domain.ErrWritebackIdentityConflict)
	}
	if !ok {
		if _, err := l.root.Lstat(locator); errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, writebackError(foundation.ErrorPermissionDenied, "WRITEBACK_MANAGED_FILE_REFERENCE_INVALID", false, domain.ErrWritebackIdentityConflict)
	}
	info, err := l.root.Lstat(locator)
	if errors.Is(err, os.ErrNotExist) {
		delete(l.managedFiles, locator)
		return false, nil
	}
	if err != nil {
		return false, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_CLEANUP_READ_FAILED", true, err)
	}
	identity, identityErr := identityFromInfo(info)
	if identityErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || identity != expectedIdentity || identity.Device != l.rootDevice || identity.Owner != uint32(os.Geteuid()) {
		return false, writebackError(foundation.ErrorPermissionDenied, "WRITEBACK_CLEANUP_TARGET_UNSAFE", false, errors.New("managed cleanup target is unsafe"))
	}
	file, openErr := l.openManagedRegular(locator)
	if openErr != nil {
		return false, openErr
	}
	hash, _, hashErr := hashReaderWithContext(ctx, file.file)
	closeErr := file.file.Close()
	if hashErr == nil {
		hashErr = closeErr
	}
	if hashErr != nil {
		return false, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_CLEANUP_VERIFY_FAILED", true, hashErr)
	}
	if !strings.EqualFold(hash, expectedHash) || file.mode != expectedMode {
		return false, writebackError(foundation.ErrorConsistencyViolation, "WRITEBACK_CLEANUP_CONTENT_CONFLICT", false, errors.New("managed cleanup target was modified"))
	}
	if err := l.root.Remove(locator); err != nil {
		return false, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_CLEANUP_FAILED", true, err)
	}
	delete(l.managedFiles, locator)
	return true, nil
}

func verifyAppliedTarget(ctx context.Context, lock *targetLock, applied domain.AppliedWrite) error {
	hash, mode, size, err := hashSafeRegular(ctx, lock.root, lock.targetPath, lock.rootDevice)
	if err != nil {
		return err
	}
	if !strings.EqualFold(hash, applied.ResultHash) || size != applied.ByteSize || mode != applied.Mode {
		return errors.New("applied target does not match prepared result")
	}
	return nil
}

func hashSafeRegular(ctx context.Context, root *os.Root, relative string, rootDevice uint64) (string, uint32, int64, error) {
	file, err := openSafeRegular(root, relative, rootDevice)
	if err != nil {
		return "", 0, 0, err
	}
	defer file.file.Close()
	hash, size, err := hashReaderWithContext(ctx, file.file)
	if err != nil {
		return "", 0, 0, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_TARGET_HASH_FAILED", true, err)
	}
	return hash, file.mode, size, nil
}

func hashReaderWithContext(ctx context.Context, reader io.Reader) (string, int64, error) {
	hash := sha256.New()
	buffer := make([]byte, 64*1024)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		count, readErr := reader.Read(buffer)
		if count > 0 {
			_, _ = hash.Write(buffer[:count])
			size += int64(count)
		}
		if errors.Is(readErr, io.EOF) {
			return hex.EncodeToString(hash.Sum(nil)), size, nil
		}
		if readErr != nil {
			return "", 0, readErr
		}
	}
}

func copyAndHashWithContext(ctx context.Context, destination io.Writer, source io.Reader) (string, int64, error) {
	hash := sha256.New()
	buffer := make([]byte, 64*1024)
	var size int64
	for {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		count, readErr := source.Read(buffer)
		if count > 0 {
			if _, err := destination.Write(buffer[:count]); err != nil {
				return "", 0, err
			}
			_, _ = hash.Write(buffer[:count])
			size += int64(count)
		}
		if errors.Is(readErr, io.EOF) {
			return hex.EncodeToString(hash.Sum(nil)), size, nil
		}
		if readErr != nil {
			return "", 0, readErr
		}
	}
}

func writeAllWithContext(ctx context.Context, writer io.Writer, content []byte) error {
	for len(content) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, err := writer.Write(content)
		if err != nil {
			return err
		}
		if count == 0 {
			return io.ErrShortWrite
		}
		content = content[count:]
	}
	return nil
}

func syncDirectoryRoot(root *os.Root, relative string) error {
	directory, err := root.Open(relative)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func randomHex(reader io.Reader, byteCount int) (string, error) {
	buffer := make([]byte, byteCount)
	if _, err := io.ReadFull(reader, buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func equalPrepareWrite(left, right domain.PrepareWrite) bool {
	return left.ExecutionID == right.ExecutionID &&
		strings.EqualFold(left.ExpectedBaseHash, right.ExpectedBaseHash) &&
		strings.EqualFold(left.ApprovedChangeHash, right.ApprovedChangeHash) &&
		string(left.Content) == string(right.Content)
}

func classifyDomainWritebackError(code string, err error) error {
	switch {
	case errors.Is(err, domain.ErrWritebackInvalidInput):
		return writebackError(foundation.ErrorInvalidInput, code, false, err)
	case errors.Is(err, domain.ErrWritebackIdentityConflict):
		return writebackError(foundation.ErrorVersionConflict, code, false, err)
	default:
		return writebackError(foundation.ErrorNonRetryableFailure, code, false, err)
	}
}

func manualRecoveryError(code string, cause error) error {
	return writebackError(foundation.ErrorManualRecoveryRequired, code, false, errors.Join(domain.ErrWritebackManualRecoveryRequired, cause))
}

func writebackContextError(code string, cause error) error {
	return writebackError(foundation.ErrorNonRetryableFailure, code, false, cause)
}

func writebackError(kind foundation.ErrorKind, code string, retryable bool, cause error) error {
	return foundation.NewError(kind, code, retryable, fmt.Errorf("local filesystem writeback failed: %w", cause))
}
