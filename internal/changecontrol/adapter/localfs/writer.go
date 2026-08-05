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
var _ domain.CreateOnlyWorkspaceStore = (*Writer)(nil)

type writerOperations struct {
	random           io.Reader
	lockPollInterval time.Duration
	syncFile         func(*os.File) error
	syncDirectory    func(*os.Root, string) error
	rename           func(*os.Root, string, string) error
	link             func(*os.Root, string, string) error
	verifyResult     func(context.Context, *targetLock, domain.AppliedWrite) error
}

func defaultWriterOperations() writerOperations {
	return writerOperations{
		random:           rand.Reader,
		lockPollInterval: defaultLockPollInterval,
		syncFile:         func(file *os.File) error { return file.Sync() },
		syncDirectory:    syncDirectoryRoot,
		rename:           func(root *os.Root, oldPath, newPath string) error { return root.Rename(oldPath, newPath) },
		link:             func(root *os.Root, oldPath, newPath string) error { return root.Link(oldPath, newPath) },
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
	if operations.link == nil {
		operations.link = defaults.link
	}
	if operations.verifyResult == nil {
		operations.verifyResult = defaults.verifyResult
	}
	return &Writer{workspaces: workspaces, validator: validator, ops: operations}, nil
}

// AcquireCreateOnlyTarget 锁定规范路径并在锁内确认目标不存在。
// 发布阶段使用 link(temp,target) 的 no-replace 原语，绝不退化为 rename 覆盖。
func (w *Writer) AcquireCreateOnlyTarget(ctx context.Context, workspaceID foundation.ID, targetPath string) (domain.TargetLock, error) {
	if ctx == nil {
		return nil, writebackError(foundation.ErrorInvalidInput, "WRITEBACK_CONTEXT_INVALID", false, errors.New("context is nil"))
	}
	if err := ctx.Err(); err != nil {
		return nil, writebackContextError("WRITEBACK_TARGET_LOCK_CANCELLED", err)
	}
	if err := domain.ValidateWorkspaceTarget(workspaceID, targetPath); err != nil {
		return nil, writebackError(foundation.ErrorInvalidInput, "WRITEBACK_TARGET_INVALID", false, err)
	}
	canonicalRoot, root, rootIdentity, err := w.openWorkspaceRoot(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	closeRoot := true
	defer func() {
		if closeRoot {
			_ = root.Close()
		}
	}()
	if err := validateTargetParents(root, targetPath, rootIdentity.Device); err != nil {
		return nil, err
	}
	if err := ensureLockDirectory(root, rootIdentity.Device); err != nil {
		return nil, err
	}
	pathLockToken := lockTokenForPath(rootIdentity, targetPath)
	pathLockFile, err := openAndAcquireTargetLock(ctx, root, rootIdentity.Device, pathLockToken, true, w.ops.lockPollInterval)
	if err != nil {
		return nil, err
	}
	closePathLock := true
	defer func() {
		if closePathLock {
			_ = syscall.Flock(int(pathLockFile.Fd()), syscall.LOCK_UN)
			_ = pathLockFile.Close()
		}
	}()
	lockToken := lockTokenForCreateOnly(rootIdentity, targetPath)
	lockFile, err := openAndAcquireTargetLock(ctx, root, rootIdentity.Device, lockToken, true, w.ops.lockPollInterval)
	if err != nil {
		return nil, err
	}
	closeLock := true
	defer func() {
		if closeLock {
			_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
			_ = lockFile.Close()
		}
	}()
	if err := validateTargetParents(root, targetPath, rootIdentity.Device); err != nil {
		return nil, err
	}
	if err := ensureTargetAbsent(root, targetPath); err != nil {
		return nil, err
	}
	if err := canonicalRoot.EnsureGitExcludePatterns("/.knowledge/", writebackExcludePattern); err != nil {
		return nil, err
	}
	closeRoot, closePathLock, closeLock = false, false, false
	return &targetLock{
		root: root, rootDevice: rootIdentity.Device, targetPath: targetPath, parentPath: path.Dir(targetPath),
		targetMode: domain.TargetModeCreateOnly, mode: 0o644, pathLockFile: pathLockFile, lockFile: lockFile,
		lockToken: lockToken, validator: w.validator, ops: w.ops, managedFiles: make(map[string]fileIdentity),
	}, nil
}

// ResumeCreateOnlyTarget 恢复 CREATE_ONLY 的受控 temp 与 no-replace 发布检查点。
func (w *Writer) ResumeCreateOnlyTarget(ctx context.Context, workspaceID foundation.ID, targetPath string, resume domain.ResumeWrite) (domain.TargetLock, domain.PreparedWrite, *domain.AppliedWrite, error) {
	if domain.NormalizeTargetMode(resume.Prepared.TargetMode) != domain.TargetModeCreateOnly {
		return nil, domain.PreparedWrite{}, nil, writebackError(foundation.ErrorInvalidInput, "CREATE_ONLY_RESUME_MODE_INVALID", false, domain.ErrWritebackInvalidInput)
	}
	return w.ResumeTarget(ctx, workspaceID, targetPath, resume)
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
	canonicalRoot, root, rootIdentity, err := w.openWorkspaceRoot(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	closeRoot := true
	defer func() {
		if closeRoot {
			_ = root.Close()
		}
	}()
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
	pathLockToken := lockTokenForPath(rootIdentity, targetPath)
	pathLockFile, err := openAndAcquireTargetLock(ctx, root, rootIdentity.Device, pathLockToken, true, w.ops.lockPollInterval)
	if err != nil {
		return nil, err
	}
	closePathLock := true
	defer func() {
		if closePathLock {
			_ = pathLockFile.Close()
		}
	}()
	pathLocked := true
	defer func() {
		if pathLocked {
			_ = syscall.Flock(int(pathLockFile.Fd()), syscall.LOCK_UN)
		}
	}()
	lockToken := lockTokenForIdentity(identity)
	lockFile, err := openAndAcquireTargetLock(ctx, root, rootIdentity.Device, lockToken, true, w.ops.lockPollInterval)
	if err != nil {
		return nil, err
	}
	closeLock := true
	defer func() {
		if closeLock {
			_ = lockFile.Close()
		}
	}()
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
	closeRoot = false
	closePathLock = false
	pathLocked = false
	closeLock = false
	locked = false
	return &targetLock{
		root:         root,
		rootDevice:   rootIdentity.Device,
		targetPath:   targetPath,
		parentPath:   path.Dir(targetPath),
		identity:     identity,
		mode:         mode,
		pathLockFile: pathLockFile,
		lockFile:     lockFile,
		lockToken:    lockToken,
		validator:    w.validator,
		ops:          w.ops,
		managedFiles: make(map[string]fileIdentity),
	}, nil
}

// ResumeTarget 使用持久化 file_prepared/file_applied 摘要重新获取原始 inode 锁并恢复受控文件绑定。
func (w *Writer) ResumeTarget(ctx context.Context, workspaceID foundation.ID, targetPath string, resume domain.ResumeWrite) (domain.TargetLock, domain.PreparedWrite, *domain.AppliedWrite, error) {
	if ctx == nil {
		return nil, domain.PreparedWrite{}, nil, writebackError(foundation.ErrorInvalidInput, "WRITEBACK_CONTEXT_INVALID", false, errors.New("context is nil"))
	}
	if err := ctx.Err(); err != nil {
		return nil, domain.PreparedWrite{}, nil, writebackContextError("WRITEBACK_TARGET_LOCK_CANCELLED", err)
	}
	if err := domain.ValidateWorkspaceTarget(workspaceID, targetPath); err != nil {
		return nil, domain.PreparedWrite{}, nil, writebackError(foundation.ErrorInvalidInput, "WRITEBACK_TARGET_INVALID", false, err)
	}
	if resume.Applied != nil {
		applied := *resume.Applied
		resume.Applied = &applied
	}
	if err := domain.ValidateResumeWrite(targetPath, resume); err != nil {
		return nil, domain.PreparedWrite{}, nil, manualRecoveryError("WRITEBACK_RESUME_BINDING_INVALID", err)
	}
	canonicalRoot, root, rootIdentity, err := w.openWorkspaceRoot(ctx, workspaceID)
	if err != nil {
		return nil, domain.PreparedWrite{}, nil, err
	}
	closeRoot := true
	defer func() {
		if closeRoot {
			_ = root.Close()
		}
	}()
	if err := validateTargetParents(root, targetPath, rootIdentity.Device); err != nil {
		return nil, domain.PreparedWrite{}, nil, manualRecoveryError("WRITEBACK_RESUME_TARGET_INVALID", err)
	}
	if err := ensureLockDirectory(root, rootIdentity.Device); err != nil {
		return nil, domain.PreparedWrite{}, nil, err
	}
	pathLockToken := lockTokenForPath(rootIdentity, targetPath)
	pathLockFile, err := openAndAcquireTargetLock(ctx, root, rootIdentity.Device, pathLockToken, false, w.ops.lockPollInterval)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, domain.PreparedWrite{}, nil, manualRecoveryError("WRITEBACK_RESUME_LOCK_MISSING", err)
		}
		return nil, domain.PreparedWrite{}, nil, err
	}
	closePathLock := true
	defer func() {
		if closePathLock {
			_ = syscall.Flock(int(pathLockFile.Fd()), syscall.LOCK_UN)
			_ = pathLockFile.Close()
		}
	}()
	lockFile, err := openAndAcquireTargetLock(ctx, root, rootIdentity.Device, resume.Prepared.LockToken, false, w.ops.lockPollInterval)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, domain.PreparedWrite{}, nil, manualRecoveryError("WRITEBACK_RESUME_LOCK_MISSING", err)
		}
		return nil, domain.PreparedWrite{}, nil, err
	}
	closeLock := true
	defer func() {
		if closeLock {
			_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
			_ = lockFile.Close()
		}
	}()
	var currentIdentity fileIdentity
	currentMode := resume.Prepared.Mode
	if domain.NormalizeTargetMode(resume.Prepared.TargetMode) != domain.TargetModeCreateOnly {
		current, openErr := openSafeRegular(root, targetPath, rootIdentity.Device)
		if openErr != nil {
			return nil, domain.PreparedWrite{}, nil, manualRecoveryError("WRITEBACK_RESUME_TARGET_INVALID", openErr)
		}
		currentIdentity = current.identity
		currentMode = current.mode
		if err := current.file.Close(); err != nil {
			return nil, domain.PreparedWrite{}, nil, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_TARGET_CLOSE_FAILED", true, err)
		}
	}
	if err := canonicalRoot.EnsureGitExcludePatterns("/.knowledge/", writebackExcludePattern); err != nil {
		return nil, domain.PreparedWrite{}, nil, err
	}
	lock := &targetLock{
		root:         root,
		rootDevice:   rootIdentity.Device,
		targetPath:   targetPath,
		parentPath:   path.Dir(targetPath),
		identity:     currentIdentity,
		mode:         currentMode,
		targetMode:   resume.Prepared.TargetMode,
		pathLockFile: pathLockFile,
		lockFile:     lockFile,
		lockToken:    resume.Prepared.LockToken,
		validator:    w.validator,
		ops:          w.ops,
		managedFiles: make(map[string]fileIdentity),
	}
	prepared, applied, err := lock.rehydrate(ctx, resume)
	if err != nil {
		_ = lock.Close()
		closeRoot = false
		closePathLock = false
		closeLock = false
		return nil, domain.PreparedWrite{}, nil, err
	}
	closeRoot = false
	closePathLock = false
	closeLock = false
	return lock, prepared, applied, nil
}

func (w *Writer) openWorkspaceRoot(ctx context.Context, workspaceID foundation.ID) (filesystem.Root, *os.Root, fileIdentity, error) {
	workspace, err := w.workspaces.GetWorkspaceByID(ctx, workspaceID)
	if err != nil {
		return filesystem.Root{}, nil, fileIdentity{}, err
	}
	if workspace.ID != workspaceID {
		return filesystem.Root{}, nil, fileIdentity{}, writebackError(foundation.ErrorConsistencyViolation, "WRITEBACK_WORKSPACE_BINDING_INVALID", false, errors.New("workspace repository returned a different workspace"))
	}
	canonicalRoot, err := filesystem.NewRoot(workspace.RootPath)
	if err != nil {
		return filesystem.Root{}, nil, fileIdentity{}, writebackError(foundation.ErrorDependencyUnavailable, "WORKSPACE_ROOT_UNAVAILABLE", false, err)
	}
	root, err := os.OpenRoot(canonicalRoot.Path())
	if err != nil {
		return filesystem.Root{}, nil, fileIdentity{}, writebackError(foundation.ErrorDependencyUnavailable, "WORKSPACE_ROOT_UNAVAILABLE", false, err)
	}
	rootIdentity, err := directoryIdentity(root, ".")
	if err != nil {
		_ = root.Close()
		return filesystem.Root{}, nil, fileIdentity{}, writebackError(foundation.ErrorDependencyUnavailable, "WORKSPACE_ROOT_UNAVAILABLE", false, err)
	}
	return canonicalRoot, root, rootIdentity, nil
}

type targetLock struct {
	mu            sync.Mutex
	root          *os.Root
	rootDevice    uint64
	targetPath    string
	parentPath    string
	targetMode    domain.TargetMode
	identity      fileIdentity
	mode          uint32
	pathLockFile  *os.File
	lockFile      *os.File
	lockToken     string
	validator     domain.ContentValidator
	ops           writerOperations
	prepare       *domain.PrepareWrite
	prepared      *domain.PreparedWrite
	applied       *domain.AppliedWrite
	managedFiles  map[string]fileIdentity
	resultUnknown bool
	cleanupReplay bool
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
	if domain.NormalizeTargetMode(command.TargetMode) != domain.NormalizeTargetMode(l.targetMode) {
		return domain.PreparedWrite{}, writebackError(foundation.ErrorVersionConflict, "WRITEBACK_TARGET_MODE_CONFLICT", false, domain.ErrWritebackIdentityConflict)
	}
	content := append([]byte(nil), command.Content...)
	command.Content = content
	if l.prepared != nil {
		if err := domain.ValidatePreparedWriteBinding(l.targetPath, command, *l.prepared); err != nil || (l.prepare != nil && !equalPrepareWrite(command, *l.prepare)) {
			return domain.PreparedWrite{}, writebackError(foundation.ErrorVersionConflict, "WRITEBACK_PREPARED_BINDING_CONFLICT", false, domain.ErrWritebackIdentityConflict)
		}
		if l.prepare == nil {
			if err := l.validator.Validate(ctx, content); err != nil {
				return domain.PreparedWrite{}, err
			}
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
	if domain.NormalizeTargetMode(l.targetMode) == domain.TargetModeCreateOnly {
		if err := ensureTargetAbsent(l.root, l.targetPath); err != nil {
			return domain.PreparedWrite{}, err
		}
		if err := l.ops.syncDirectory(l.root, l.parentPath); err != nil {
			return domain.PreparedWrite{}, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_PREPARE_DIRECTORY_SYNC_FAILED", true, err)
		}
		resultIdentity, ok := l.managedFiles[temporaryRef]
		if !ok {
			return domain.PreparedWrite{}, writebackError(foundation.ErrorConsistencyViolation, "WRITEBACK_TEMP_IDENTITY_MISSING", false, errors.New("prepared temp identity is unavailable"))
		}
		prepared := domain.PreparedWrite{
			ExecutionID: command.ExecutionID, TargetMode: domain.TargetModeCreateOnly,
			TemporaryRef: temporaryRef, ExpectedBaseHash: command.ExpectedBaseHash,
			ApprovedChangeHash: strings.ToLower(command.ApprovedChangeHash), ResultHash: resultHash,
			ByteSize: int64(len(content)), Mode: l.mode, LockToken: l.lockToken,
			ResultLockToken: lockTokenForIdentity(resultIdentity),
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
	backupRef, backup, err := l.createManagedFile(command.ExecutionID, ".bak")
	if err != nil {
		return domain.PreparedWrite{}, err
	}
	removeBackup := true
	defer func() {
		_ = backup.Close()
		if removeBackup {
			_ = l.removeManagedFile(backupRef)
		}
	}()
	target, err := openSafeRegular(l.root, l.targetPath, l.rootDevice)
	if err != nil {
		return domain.PreparedWrite{}, err
	}
	if target.identity != l.identity || target.mode != l.mode {
		_ = target.file.Close()
		return domain.PreparedWrite{}, writebackError(foundation.ErrorVersionConflict, "TARGET_IDENTITY_CONFLICT", false, domain.ErrTargetIdentityConflict)
	}
	baseHash, _, copyErr := copyAndHashWithContext(ctx, backup, target.file)
	closeTargetErr := target.file.Close()
	if copyErr == nil {
		copyErr = closeTargetErr
	}
	if copyErr != nil {
		return domain.PreparedWrite{}, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_BACKUP_WRITE_FAILED", true, copyErr)
	}
	if !strings.EqualFold(baseHash, command.ExpectedBaseHash) {
		return domain.PreparedWrite{}, writebackError(foundation.ErrorVersionConflict, "TARGET_BASE_HASH_CONFLICT", false, domain.ErrTargetBaseHashConflict)
	}
	if err := backup.Chmod(fileModeFromUnix(l.mode)); err != nil {
		return domain.PreparedWrite{}, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_BACKUP_MODE_FAILED", true, err)
	}
	if err := l.ops.syncFile(backup); err != nil {
		return domain.PreparedWrite{}, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_BACKUP_SYNC_FAILED", true, err)
	}
	if err := backup.Close(); err != nil {
		return domain.PreparedWrite{}, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_BACKUP_CLOSE_FAILED", true, err)
	}
	if err := l.verifyManagedFileHash(ctx, backupRef, command.ExpectedBaseHash, l.mode); err != nil {
		return domain.PreparedWrite{}, err
	}
	if err := l.ops.syncDirectory(l.root, l.parentPath); err != nil {
		return domain.PreparedWrite{}, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_PREPARE_DIRECTORY_SYNC_FAILED", true, err)
	}
	resultIdentity, ok := l.managedFiles[temporaryRef]
	if !ok {
		return domain.PreparedWrite{}, writebackError(foundation.ErrorConsistencyViolation, "WRITEBACK_TEMP_IDENTITY_MISSING", false, errors.New("prepared temp identity is unavailable"))
	}
	backupIdentity, ok := l.managedFiles[backupRef]
	if !ok {
		return domain.PreparedWrite{}, writebackError(foundation.ErrorConsistencyViolation, "WRITEBACK_BACKUP_IDENTITY_MISSING", false, errors.New("prepared backup identity is unavailable"))
	}
	prepared := domain.PreparedWrite{
		ExecutionID:        command.ExecutionID,
		TargetMode:         l.targetMode,
		TemporaryRef:       temporaryRef,
		BackupRef:          backupRef,
		ExpectedBaseHash:   strings.ToLower(command.ExpectedBaseHash),
		ApprovedChangeHash: strings.ToLower(command.ApprovedChangeHash),
		ResultHash:         resultHash,
		ByteSize:           int64(len(content)),
		Mode:               l.mode,
		LockToken:          l.lockToken,
		ResultLockToken:    lockTokenForIdentity(resultIdentity),
		BackupLockToken:    lockTokenForIdentity(backupIdentity),
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
	removeBackup = false
	return prepared, nil
}

// CommitCAS 在最终身份和 Base Hash 校验后备份旧文件并原子替换目标。
func (l *targetLock) CommitCAS(ctx context.Context, prepared domain.PreparedWrite) (domain.AppliedWrite, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if err := l.usable(ctx); err != nil {
		return domain.AppliedWrite{}, err
	}
	if l.prepared == nil || domain.ValidatePreparedWriteSummary(l.targetPath, prepared) != nil || prepared != *l.prepared {
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
	if domain.NormalizeTargetMode(l.targetMode) == domain.TargetModeCreateOnly {
		return l.commitCreateOnly(ctx, prepared)
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
	if target.mode != prepared.Mode {
		return domain.AppliedWrite{}, writebackError(foundation.ErrorVersionConflict, "TARGET_MODE_CONFLICT", false, domain.ErrTargetIdentityConflict)
	}
	if err := l.verifyManagedFile(ctx, prepared.TemporaryRef, prepared.ResultHash, prepared.ByteSize, prepared.Mode); err != nil {
		return domain.AppliedWrite{}, err
	}
	baseHash, _, err := hashReaderWithContext(ctx, target.file)
	if err != nil {
		return domain.AppliedWrite{}, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_TARGET_HASH_FAILED", true, err)
	}
	if !strings.EqualFold(baseHash, prepared.ExpectedBaseHash) {
		return domain.AppliedWrite{}, writebackError(foundation.ErrorVersionConflict, "TARGET_BASE_HASH_CONFLICT", false, domain.ErrTargetBaseHashConflict)
	}
	if err := l.verifyManagedFileHash(ctx, prepared.BackupRef, prepared.ExpectedBaseHash, prepared.Mode); err != nil {
		return domain.AppliedWrite{}, err
	}
	applied := domain.AppliedWrite{
		ExecutionID:        prepared.ExecutionID,
		TargetMode:         prepared.TargetMode,
		TemporaryRef:       prepared.TemporaryRef,
		BackupRef:          prepared.BackupRef,
		BaseHash:           prepared.ExpectedBaseHash,
		ApprovedChangeHash: prepared.ApprovedChangeHash,
		ResultHash:         prepared.ResultHash,
		ByteSize:           prepared.ByteSize,
		Mode:               prepared.Mode,
		LockToken:          prepared.LockToken,
		ResultLockToken:    prepared.ResultLockToken,
		BackupLockToken:    prepared.BackupLockToken,
	}
	if err := domain.ValidateAppliedWriteBinding(prepared, applied); err != nil {
		return domain.AppliedWrite{}, classifyDomainWritebackError("WRITEBACK_APPLIED_BINDING_INVALID", err)
	}
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

func (l *targetLock) commitCreateOnly(ctx context.Context, prepared domain.PreparedWrite) (domain.AppliedWrite, error) {
	if err := validateTargetParents(l.root, l.targetPath, l.rootDevice); err != nil {
		return domain.AppliedWrite{}, err
	}
	if err := ensureTargetAbsent(l.root, l.targetPath); err != nil {
		return domain.AppliedWrite{}, err
	}
	if err := l.verifyManagedFile(ctx, prepared.TemporaryRef, prepared.ResultHash, prepared.ByteSize, prepared.Mode); err != nil {
		return domain.AppliedWrite{}, err
	}
	applied := appliedFromPrepared(prepared)
	if err := domain.ValidateAppliedWriteBinding(prepared, applied); err != nil {
		return domain.AppliedWrite{}, classifyDomainWritebackError("WRITEBACK_APPLIED_BINDING_INVALID", err)
	}
	l.applied = &applied
	if err := l.ops.link(l.root, prepared.TemporaryRef, l.targetPath); err != nil {
		if errors.Is(err, os.ErrExist) {
			l.applied = nil
			return domain.AppliedWrite{}, writebackError(foundation.ErrorVersionConflict, "CREATE_ONLY_TARGET_EXISTS", false, domain.ErrTargetExistenceConflict)
		}
		l.resultUnknown = true
		return applied, manualRecoveryError("CREATE_ONLY_LINK_RESULT_UNKNOWN", err)
	}
	if err := l.ops.syncDirectory(l.root, l.parentPath); err != nil {
		l.resultUnknown = true
		return applied, manualRecoveryError("CREATE_ONLY_PARENT_SYNC_RESULT_UNKNOWN", err)
	}
	if err := l.ops.verifyResult(ctx, l, applied); err != nil {
		l.resultUnknown = true
		return applied, manualRecoveryError("CREATE_ONLY_RESULT_VERIFY_UNKNOWN", err)
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
	if domain.NormalizeTargetMode(applied.TargetMode) == domain.TargetModeCreateOnly {
		return l.restoreCreateOnly(ctx, applied)
	}
	currentHash, currentMode, _, currentIdentity, err := inspectSafeRegular(ctx, l.root, l.targetPath, l.rootDevice)
	if err != nil {
		return domain.RestoreResult{}, err
	}
	if currentMode != applied.Mode {
		return domain.RestoreResult{}, writebackError(foundation.ErrorVersionConflict, "WRITEBACK_RESTORE_CONFLICT", false, domain.ErrWritebackRestoreConflict)
	}
	if strings.EqualFold(currentHash, applied.BaseHash) {
		if lockTokenForIdentity(currentIdentity) != strings.ToLower(applied.BackupLockToken) {
			return domain.RestoreResult{}, writebackError(foundation.ErrorVersionConflict, "WRITEBACK_RESTORE_CONFLICT", false, domain.ErrWritebackRestoreConflict)
		}
		l.resultUnknown = false
		return domain.RestoreResult{Replayed: true}, nil
	}
	if !strings.EqualFold(currentHash, applied.ResultHash) {
		return domain.RestoreResult{}, writebackError(foundation.ErrorVersionConflict, "WRITEBACK_RESTORE_CONFLICT", false, domain.ErrWritebackRestoreConflict)
	}
	if lockTokenForIdentity(currentIdentity) != strings.ToLower(applied.ResultLockToken) {
		return domain.RestoreResult{}, writebackError(foundation.ErrorVersionConflict, "WRITEBACK_RESTORE_CONFLICT", false, domain.ErrWritebackRestoreConflict)
	}
	if err := l.verifyManagedFileHash(ctx, applied.BackupRef, applied.BaseHash, applied.Mode); err != nil {
		return domain.RestoreResult{}, err
	}
	if err := l.ops.rename(l.root, applied.BackupRef, l.targetPath); err != nil {
		l.resultUnknown = true
		return domain.RestoreResult{}, manualRecoveryError("WRITEBACK_RESTORE_RENAME_RESULT_UNKNOWN", err)
	}
	if err := l.ops.syncDirectory(l.root, l.parentPath); err != nil {
		l.resultUnknown = true
		return domain.RestoreResult{}, manualRecoveryError("WRITEBACK_RESTORE_PARENT_SYNC_RESULT_UNKNOWN", err)
	}
	restoredHash, restoredMode, _, err := hashSafeRegular(ctx, l.root, l.targetPath, l.rootDevice)
	if err != nil || !strings.EqualFold(restoredHash, applied.BaseHash) || restoredMode != applied.Mode {
		if err == nil {
			err = errors.New("restored target does not match base backup")
		}
		l.resultUnknown = true
		return domain.RestoreResult{}, manualRecoveryError("WRITEBACK_RESTORE_VERIFY_UNKNOWN", err)
	}
	l.resultUnknown = false
	return domain.RestoreResult{Restored: true}, nil
}

func (l *targetLock) restoreCreateOnly(ctx context.Context, applied domain.AppliedWrite) (domain.RestoreResult, error) {
	if err := validateTargetParents(l.root, l.targetPath, l.rootDevice); err != nil {
		return domain.RestoreResult{}, err
	}
	if _, err := l.root.Lstat(l.targetPath); errors.Is(err, os.ErrNotExist) {
		l.resultUnknown = false
		return domain.RestoreResult{Replayed: true}, nil
	} else if err != nil {
		return domain.RestoreResult{}, writebackError(foundation.ErrorDependencyUnavailable, "CREATE_ONLY_RESTORE_READ_FAILED", true, err)
	}
	currentHash, currentMode, _, currentIdentity, err := inspectSafeRegular(ctx, l.root, l.targetPath, l.rootDevice)
	if err != nil {
		return domain.RestoreResult{}, err
	}
	if !strings.EqualFold(currentHash, applied.ResultHash) || currentMode != applied.Mode || lockTokenForIdentity(currentIdentity) != strings.ToLower(applied.ResultLockToken) {
		return domain.RestoreResult{}, writebackError(foundation.ErrorVersionConflict, "CREATE_ONLY_RESTORE_CONFLICT", false, domain.ErrWritebackRestoreConflict)
	}
	if err := l.root.Remove(l.targetPath); err != nil {
		l.resultUnknown = true
		return domain.RestoreResult{}, manualRecoveryError("CREATE_ONLY_REMOVE_RESULT_UNKNOWN", err)
	}
	if err := l.ops.syncDirectory(l.root, l.parentPath); err != nil {
		l.resultUnknown = true
		return domain.RestoreResult{}, manualRecoveryError("CREATE_ONLY_REMOVE_SYNC_RESULT_UNKNOWN", err)
	}
	if err := ensureTargetAbsent(l.root, l.targetPath); err != nil {
		l.resultUnknown = true
		return domain.RestoreResult{}, manualRecoveryError("CREATE_ONLY_REMOVE_VERIFY_UNKNOWN", err)
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
	locators := []string{applied.TemporaryRef, applied.BackupRef}
	if domain.NormalizeTargetMode(applied.TargetMode) == domain.TargetModeCreateOnly {
		locators = []string{applied.TemporaryRef}
	}
	for _, locator := range locators {
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
		l.cleanupReplay = true
	}
	if l.cleanupReplay {
		if err := l.ops.syncDirectory(l.root, l.parentPath); err != nil {
			return writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_CLEANUP_SYNC_FAILED", true, err)
		}
		l.cleanupReplay = false
	}
	return nil
}

// Close 释放 advisory lock 与 Root 文件描述符；已返回的 durable temp/backup 必须保留给重启恢复。
func (l *targetLock) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	var failures []error
	for _, lockFile := range []*os.File{l.lockFile, l.pathLockFile} {
		if lockFile == nil {
			continue
		}
		if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN); err != nil {
			failures = append(failures, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_TARGET_UNLOCK_FAILED", true, err))
		}
		if err := lockFile.Close(); err != nil {
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
	if l == nil || l.closed || l.root == nil || l.lockFile == nil || l.pathLockFile == nil {
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

func (l *targetLock) rehydrate(ctx context.Context, resume domain.ResumeWrite) (domain.PreparedWrite, *domain.AppliedWrite, error) {
	if err := l.usable(ctx); err != nil {
		return domain.PreparedWrite{}, nil, err
	}
	if domain.NormalizeTargetMode(resume.Prepared.TargetMode) == domain.TargetModeCreateOnly {
		return l.rehydrateCreateOnly(ctx, resume)
	}
	prepared := resume.Prepared
	targetHash, targetMode, targetSize, targetIdentity, err := inspectSafeRegular(ctx, l.root, l.targetPath, l.rootDevice)
	if err != nil {
		return domain.PreparedWrite{}, nil, err
	}
	if targetMode != prepared.Mode {
		return domain.PreparedWrite{}, nil, manualRecoveryError("WRITEBACK_RESUME_TARGET_MODE_CONFLICT", errors.New("target mode differs from durable file intent"))
	}
	backupState, backupErr := l.registerManagedFileState(ctx, prepared.BackupRef)
	backupExists := backupErr == nil
	if backupErr != nil && !errors.Is(backupErr, os.ErrNotExist) {
		return domain.PreparedWrite{}, nil, manualRecoveryError("WRITEBACK_RESUME_BACKUP_INVALID", backupErr)
	}
	tempState, tempErr := l.registerManagedFileState(ctx, prepared.TemporaryRef)
	tempExists := tempErr == nil
	if tempErr != nil && !errors.Is(tempErr, os.ErrNotExist) {
		return domain.PreparedWrite{}, nil, manualRecoveryError("WRITEBACK_RESUME_TEMP_INVALID", tempErr)
	}
	backupIsBase := backupExists && strings.EqualFold(backupState.hash, prepared.ExpectedBaseHash) && backupState.mode == prepared.Mode && lockTokenForIdentity(backupState.identity) == strings.ToLower(prepared.BackupLockToken)
	tempIsResult := tempExists && strings.EqualFold(tempState.hash, prepared.ResultHash) && tempState.size == prepared.ByteSize && tempState.mode == prepared.Mode && lockTokenForIdentity(tempState.identity) == strings.ToLower(prepared.ResultLockToken)
	targetIsBase := strings.EqualFold(targetHash, prepared.ExpectedBaseHash)
	targetHasResultContent := strings.EqualFold(targetHash, prepared.ResultHash) && targetSize == prepared.ByteSize
	targetIsResult := targetHasResultContent && lockTokenForIdentity(targetIdentity) == strings.ToLower(prepared.ResultLockToken)
	targetIsRestoredBase := targetIsBase && lockTokenForIdentity(targetIdentity) == strings.ToLower(prepared.BackupLockToken)
	derivedApplied := appliedFromPrepared(prepared)

	if resume.CleanupMayHaveCompleted && resume.Applied != nil && targetIsResult && !tempExists && !backupExists {
		if *resume.Applied != derivedApplied {
			return domain.PreparedWrite{}, nil, manualRecoveryError("WRITEBACK_RESUME_APPLIED_BINDING_CONFLICT", domain.ErrWritebackIdentityConflict)
		}
		l.identity = targetIdentity
		l.mode = prepared.Mode
		l.prepared = &prepared
		l.applied = &derivedApplied
		l.cleanupReplay = true
		return prepared, &derivedApplied, nil
	}
	// RestoreCAS 直接 rename 已持久化且已 fsync 的 Base backup。若进程在
	// rename 成功后、记录 compensated 前退出，目标已是 Base 且 backup 已消失；
	// 此时只允许在调用方明确声明 Restore 响应可能丢失且 inode 仍为 backup 时识别重放。
	if resume.RestoreMayHaveCompleted && resume.Applied != nil && targetIsRestoredBase && !tempExists && !backupExists {
		if *resume.Applied != derivedApplied {
			return domain.PreparedWrite{}, nil, manualRecoveryError("WRITEBACK_RESUME_APPLIED_BINDING_CONFLICT", domain.ErrWritebackIdentityConflict)
		}
		l.identity = targetIdentity
		l.mode = prepared.Mode
		l.prepared = &prepared
		l.applied = &derivedApplied
		return prepared, &derivedApplied, nil
	}
	if (resume.CleanupMayHaveCompleted || resume.RestoreMayHaveCompleted) && !tempExists && !backupExists {
		return domain.PreparedWrite{}, nil, manualRecoveryError("WRITEBACK_RESUME_RESULT_UNKNOWN", errors.New("target identity does not match the declared response-loss recovery intent"))
	}
	if !backupExists {
		return domain.PreparedWrite{}, nil, manualRecoveryError("WRITEBACK_RESUME_BACKUP_INVALID", backupErr)
	}

	if resume.Applied == nil && targetIsBase && tempIsResult && backupIsBase {
		if lockTokenForIdentity(targetIdentity) != strings.ToLower(prepared.LockToken) {
			return domain.PreparedWrite{}, nil, manualRecoveryError("WRITEBACK_RESUME_TARGET_IDENTITY_CONFLICT", domain.ErrTargetIdentityConflict)
		}
		l.identity = targetIdentity
		l.mode = prepared.Mode
		l.prepared = &prepared
		return prepared, nil, nil
	}
	if targetIsResult && !tempExists && backupIsBase {
		if resume.Applied != nil && *resume.Applied != derivedApplied {
			return domain.PreparedWrite{}, nil, manualRecoveryError("WRITEBACK_RESUME_APPLIED_BINDING_CONFLICT", domain.ErrWritebackIdentityConflict)
		}
		l.identity = targetIdentity
		l.mode = prepared.Mode
		l.prepared = &prepared
		l.applied = &derivedApplied
		return prepared, &derivedApplied, nil
	}
	return domain.PreparedWrite{}, nil, manualRecoveryError("WRITEBACK_RESUME_RESULT_UNKNOWN", errors.New("target, temp, and backup do not match a recoverable checkpoint"))
}

func (l *targetLock) rehydrateCreateOnly(ctx context.Context, resume domain.ResumeWrite) (domain.PreparedWrite, *domain.AppliedWrite, error) {
	prepared := resume.Prepared
	tempState, tempErr := l.registerManagedFileState(ctx, prepared.TemporaryRef)
	tempExists := tempErr == nil
	if tempErr != nil && !errors.Is(tempErr, os.ErrNotExist) {
		return domain.PreparedWrite{}, nil, manualRecoveryError("CREATE_ONLY_RESUME_TEMP_INVALID", tempErr)
	}
	tempIsResult := tempExists && strings.EqualFold(tempState.hash, prepared.ResultHash) && tempState.size == prepared.ByteSize && tempState.mode == prepared.Mode && lockTokenForIdentity(tempState.identity) == strings.ToLower(prepared.ResultLockToken)
	if tempExists && !tempIsResult {
		return domain.PreparedWrite{}, nil, manualRecoveryError("CREATE_ONLY_RESUME_TEMP_CONFLICT", domain.ErrWritebackIdentityConflict)
	}
	targetExists := false
	var targetIdentity fileIdentity
	if _, err := l.root.Lstat(l.targetPath); err == nil {
		targetExists = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return domain.PreparedWrite{}, nil, manualRecoveryError("CREATE_ONLY_RESUME_TARGET_INVALID", err)
	}
	targetIsResult := false
	if targetExists {
		hash, mode, size, identity, err := inspectSafeRegular(ctx, l.root, l.targetPath, l.rootDevice)
		if err != nil {
			return domain.PreparedWrite{}, nil, manualRecoveryError("CREATE_ONLY_RESUME_TARGET_INVALID", err)
		}
		targetIdentity = identity
		targetIsResult = strings.EqualFold(hash, prepared.ResultHash) && mode == prepared.Mode && size == prepared.ByteSize && lockTokenForIdentity(identity) == strings.ToLower(prepared.ResultLockToken)
		if !targetIsResult {
			return domain.PreparedWrite{}, nil, manualRecoveryError("CREATE_ONLY_RESUME_TARGET_CONFLICT", domain.ErrWritebackIdentityConflict)
		}
	}
	derivedApplied := appliedFromPrepared(prepared)
	if !targetExists && tempIsResult && resume.Applied == nil {
		l.prepared = &prepared
		return prepared, nil, nil
	}
	if targetIsResult && tempIsResult {
		if resume.Applied != nil && *resume.Applied != derivedApplied {
			return domain.PreparedWrite{}, nil, manualRecoveryError("WRITEBACK_RESUME_APPLIED_BINDING_CONFLICT", domain.ErrWritebackIdentityConflict)
		}
		l.identity, l.mode, l.prepared, l.applied = targetIdentity, prepared.Mode, &prepared, &derivedApplied
		return prepared, &derivedApplied, nil
	}
	if targetIsResult && !tempExists && resume.CleanupMayHaveCompleted && resume.Applied != nil {
		if *resume.Applied != derivedApplied {
			return domain.PreparedWrite{}, nil, manualRecoveryError("WRITEBACK_RESUME_APPLIED_BINDING_CONFLICT", domain.ErrWritebackIdentityConflict)
		}
		l.identity, l.mode, l.prepared, l.applied, l.cleanupReplay = targetIdentity, prepared.Mode, &prepared, &derivedApplied, true
		return prepared, &derivedApplied, nil
	}
	if !targetExists && resume.RestoreMayHaveCompleted && resume.Applied != nil {
		if *resume.Applied != derivedApplied {
			return domain.PreparedWrite{}, nil, manualRecoveryError("WRITEBACK_RESUME_APPLIED_BINDING_CONFLICT", domain.ErrWritebackIdentityConflict)
		}
		l.mode, l.prepared, l.applied = prepared.Mode, &prepared, &derivedApplied
		return prepared, &derivedApplied, nil
	}
	return domain.PreparedWrite{}, nil, manualRecoveryError("CREATE_ONLY_RESUME_RESULT_UNKNOWN", errors.New("target and controlled temp do not match a create-only checkpoint"))
}

type managedFileState struct {
	hash     string
	size     int64
	mode     uint32
	identity fileIdentity
}

func (l *targetLock) registerManagedFileState(ctx context.Context, locator string) (managedFileState, error) {
	if path.Dir(locator) != l.parentPath {
		return managedFileState{}, domain.ErrWritebackIdentityConflict
	}
	file, err := openSafeRegular(l.root, locator, l.rootDevice)
	if err != nil {
		return managedFileState{}, err
	}
	defer file.file.Close()
	hash, size, err := hashReaderWithContext(ctx, file.file)
	if err != nil {
		return managedFileState{}, err
	}
	l.managedFiles[locator] = file.identity
	return managedFileState{hash: hash, size: size, mode: file.mode, identity: file.identity}, nil
}

func (l *targetLock) createManagedFileAt(locator string) (*os.File, error) {
	if path.Dir(locator) != l.parentPath {
		return nil, writebackError(foundation.ErrorPermissionDenied, "WRITEBACK_MANAGED_FILE_REFERENCE_INVALID", false, domain.ErrWritebackIdentityConflict)
	}
	if _, err := l.root.Lstat(locator); err == nil {
		return nil, writebackError(foundation.ErrorConsistencyViolation, "WRITEBACK_MANAGED_FILE_IDENTITY_CONFLICT", false, errors.New("managed file already exists"))
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_MANAGED_FILE_CREATE_FAILED", true, err)
	}
	file, err := l.root.OpenFile(locator, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_MANAGED_FILE_CREATE_FAILED", true, err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		_ = l.root.Remove(locator)
		return nil, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_MANAGED_FILE_CREATE_FAILED", true, err)
	}
	identity, err := identityFromInfo(info)
	if err != nil || !info.Mode().IsRegular() || identity.Device != l.rootDevice || identity.Owner != uint32(os.Geteuid()) {
		_ = file.Close()
		_ = l.root.Remove(locator)
		return nil, writebackError(foundation.ErrorPermissionDenied, "WRITEBACK_MANAGED_FILE_INVALID", false, errors.New("managed file identity is unsafe"))
	}
	l.managedFiles[locator] = identity
	return file, nil
}

func appliedFromPrepared(prepared domain.PreparedWrite) domain.AppliedWrite {
	return domain.AppliedWrite{
		ExecutionID:        prepared.ExecutionID,
		TargetMode:         prepared.TargetMode,
		TemporaryRef:       prepared.TemporaryRef,
		BackupRef:          prepared.BackupRef,
		BaseHash:           prepared.ExpectedBaseHash,
		ApprovedChangeHash: prepared.ApprovedChangeHash,
		ResultHash:         prepared.ResultHash,
		ByteSize:           prepared.ByteSize,
		Mode:               prepared.Mode,
		LockToken:          prepared.LockToken,
		ResultLockToken:    prepared.ResultLockToken,
		BackupLockToken:    prepared.BackupLockToken,
	}
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

func lockTokenForIdentity(identity fileIdentity) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("inode\x00%d:%d", identity.Device, identity.Inode)))
	return hex.EncodeToString(sum[:])
}

func lockTokenForPath(rootIdentity fileIdentity, targetPath string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("path\x00%d:%d\x00%s", rootIdentity.Device, rootIdentity.Inode, targetPath)))
	return hex.EncodeToString(sum[:])
}

func lockTokenForCreateOnly(rootIdentity fileIdentity, targetPath string) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("create-only\x00%d:%d\x00%s", rootIdentity.Device, rootIdentity.Inode, targetPath)))
	return hex.EncodeToString(sum[:])
}

func ensureTargetAbsent(root *os.Root, targetPath string) error {
	if _, err := root.Lstat(targetPath); err == nil {
		return writebackError(foundation.ErrorVersionConflict, "CREATE_ONLY_TARGET_EXISTS", false, domain.ErrTargetExistenceConflict)
	} else if !errors.Is(err, os.ErrNotExist) {
		return writebackError(foundation.ErrorDependencyUnavailable, "CREATE_ONLY_TARGET_READ_FAILED", true, err)
	}
	return nil
}

func openAndAcquireTargetLock(ctx context.Context, root *os.Root, rootDevice uint64, lockToken string, create bool, pollInterval time.Duration) (*os.File, error) {
	if !domain.ValidHash(lockToken) {
		return nil, writebackError(foundation.ErrorPermissionDenied, "WRITEBACK_TARGET_LOCK_UNSAFE", false, domain.ErrWritebackIdentityConflict)
	}
	flags := os.O_RDWR | syscall.O_NOFOLLOW | syscall.O_CLOEXEC
	if create {
		flags |= os.O_CREATE
	}
	lockPath := path.Join(writebackLockDirectory, strings.ToLower(lockToken)+".lock")
	lockFile, err := root.OpenFile(lockPath, flags, 0o600)
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
	if err != nil || !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm() != 0o600 {
		return nil, writebackError(foundation.ErrorPermissionDenied, "WRITEBACK_TARGET_LOCK_UNSAFE", false, errors.New("lock file is not a regular 0600 file"))
	}
	lockIdentity, err := identityFromInfo(lockInfo)
	if err != nil || lockIdentity.Owner != uint32(os.Geteuid()) || lockIdentity.Device != rootDevice {
		return nil, writebackError(foundation.ErrorPermissionDenied, "WRITEBACK_TARGET_LOCK_UNSAFE", false, errors.New("lock file ownership or device is unsafe"))
	}
	if err := acquireFileLock(ctx, lockFile, pollInterval); err != nil {
		return nil, err
	}
	closeLock = false
	return lockFile, nil
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
	prefix, err := domain.WritebackLocatorPrefix(executionID, l.lockToken)
	if err != nil {
		return "", nil, classifyDomainWritebackError("WRITEBACK_MANAGED_FILE_REFERENCE_INVALID", err)
	}
	for range 10 {
		randomPart, err := randomHex(l.ops.random, 16)
		if err != nil {
			return "", nil, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_RANDOM_GENERATION_FAILED", true, err)
		}
		name := prefix + randomPart + suffix
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
	hash, mode, size, identity, err := inspectSafeRegular(ctx, lock.root, lock.targetPath, lock.rootDevice)
	if err != nil {
		return err
	}
	if !strings.EqualFold(hash, applied.ResultHash) || size != applied.ByteSize || mode != applied.Mode || lockTokenForIdentity(identity) != strings.ToLower(applied.ResultLockToken) {
		return errors.New("applied target does not match prepared result")
	}
	return nil
}

func hashSafeRegular(ctx context.Context, root *os.Root, relative string, rootDevice uint64) (string, uint32, int64, error) {
	hash, mode, size, _, err := inspectSafeRegular(ctx, root, relative, rootDevice)
	return hash, mode, size, err
}

func inspectSafeRegular(ctx context.Context, root *os.Root, relative string, rootDevice uint64) (string, uint32, int64, fileIdentity, error) {
	file, err := openSafeRegular(root, relative, rootDevice)
	if err != nil {
		return "", 0, 0, fileIdentity{}, err
	}
	defer file.file.Close()
	hash, size, err := hashReaderWithContext(ctx, file.file)
	if err != nil {
		return "", 0, 0, fileIdentity{}, writebackError(foundation.ErrorDependencyUnavailable, "WRITEBACK_TARGET_HASH_FAILED", true, err)
	}
	return hash, file.mode, size, file.identity, nil
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
		left.WorkspaceID == right.WorkspaceID &&
		domain.NormalizeTargetMode(left.TargetMode) == domain.NormalizeTargetMode(right.TargetMode) &&
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
