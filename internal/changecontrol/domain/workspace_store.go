package domain

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// MaxWritebackContentBytes 是 Safe Writeback v1 允许准备的最大正文大小。
	MaxWritebackContentBytes = 10 * 1024 * 1024
	writebackLocatorPrefix   = ".zhixu-writeback-"
)

var (
	// ErrTargetLockUnavailable 表示目标文件当前被另一个协作写入者占用，可在退避后重试。
	ErrTargetLockUnavailable = errors.New("writeback target lock is unavailable")
	// ErrTargetBaseHashConflict 表示原子替换前目标内容已偏离批准的 Base Hash。
	ErrTargetBaseHashConflict = errors.New("writeback target base hash does not match")
	// ErrTargetIdentityConflict 表示目标路径当前指向的文件身份已发生变化。
	ErrTargetIdentityConflict = errors.New("writeback target identity does not match")
	// ErrWritebackManualRecoveryRequired 表示文件副作用结果无法证明，必须停止自动重试并人工恢复。
	ErrWritebackManualRecoveryRequired = errors.New("writeback requires manual recovery")
	// ErrWritebackRestoreConflict 表示目标在系统写入后又被修改，恢复操作不得覆盖当前内容。
	ErrWritebackRestoreConflict = errors.New("writeback restore target does not match applied result")
)

// WorkspaceStore 是 Change Control 使用的 Workspace 文件副作用端口。
// 实现必须通过服务端 Workspace ID 解析根目录，并在返回前持有目标级互斥锁。
type WorkspaceStore interface {
	AcquireTarget(context.Context, foundation.ID, string) (TargetLock, error)
}

// TargetLock 封装单个 Workspace 目标的 Prepare、CAS、恢复和清理生命周期。
// Close 必须释放实现持有的锁和文件描述符；实现不得依赖调用方提供任意绝对路径。
type TargetLock interface {
	Prepare(context.Context, PrepareWrite) (PreparedWrite, error)
	CommitCAS(context.Context, PreparedWrite) (AppliedWrite, error)
	RestoreCAS(context.Context, AppliedWrite) (RestoreResult, error)
	Cleanup(context.Context, AppliedWrite) error
	Close() error
}

// ContentValidator 校验待写入正文是否满足真实 Markdown 解析契约。
// 实现只能读取传入的不可变字节，不得自行读取目标路径或执行写入。
type ContentValidator interface {
	Validate(context.Context, []byte) error
}

// PrepareWrite 将待写正文绑定到唯一 Writeback Execution、Base Hash 和批准 Change Hash。
// Content 由调用方持有，Adapter 不得修改该切片。
type PrepareWrite struct {
	ExecutionID        foundation.ID
	ExpectedBaseHash   string
	ApprovedChangeHash string
	Content            []byte
}

// PreparedWrite 是当前 TargetLock 生成的受控临时写入摘要。
// TemporaryRef 是 Workspace 相对 locator，LockToken 是实现生成的不可伪造锁摘要。
type PreparedWrite struct {
	ExecutionID        foundation.ID
	TemporaryRef       string
	ExpectedBaseHash   string
	ApprovedChangeHash string
	ResultHash         string
	ByteSize           int64
	Mode               uint32
	LockToken          string
}

// AppliedWrite 是完成原子替换后用于 Git 阶段、恢复和清理的稳定摘要。
// BackupRef 必须指向当前锁和 Execution 生成的受控独立备份。
type AppliedWrite struct {
	ExecutionID        foundation.ID
	TemporaryRef       string
	BackupRef          string
	BaseHash           string
	ApprovedChangeHash string
	ResultHash         string
	ByteSize           int64
	Mode               uint32
	LockToken          string
}

// RestoreResult 表示恢复实际执行或识别到已恢复重放；两个状态必须且只能有一个成立。
type RestoreResult struct {
	Restored bool
	Replayed bool
}

// ValidateWorkspaceTarget 校验 AcquireTarget 的 Workspace 和规范 Markdown 相对路径绑定。
func ValidateWorkspaceTarget(workspaceID foundation.ID, targetPath string) error {
	if workspaceID == "" {
		return ErrWritebackInvalidInput
	}
	return validateWritebackTargetPath(targetPath)
}

func validateWritebackTargetPath(targetPath string) error {
	clean, err := ValidateTargetPath(targetPath)
	if err != nil || clean != targetPath {
		return ErrWritebackInvalidInput
	}
	first, _, _ := strings.Cut(clean, "/")
	if first == ".knowledge" {
		return ErrWritebackInvalidInput
	}
	switch strings.ToLower(path.Ext(clean)) {
	case ".md", ".markdown":
		return nil
	default:
		return ErrWritebackInvalidInput
	}
}

// ComputeWritebackResultHash 对即将写入的原始字节计算稳定 SHA-256。
func ComputeWritebackResultHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// ValidatePrepareWrite 校验正文与 Execution、Base Hash、批准 Change Hash 和目标路径的完整绑定。
// 返回值是由原始正文重新计算的 Result Hash，调用方不得信任外部传入的结果哈希。
func ValidatePrepareWrite(targetPath string, command PrepareWrite) (string, error) {
	if command.ExecutionID == "" || !ValidHash(command.ExpectedBaseHash) || !ValidHash(command.ApprovedChangeHash) {
		return "", ErrWritebackInvalidInput
	}
	if err := validateWritebackTargetPath(targetPath); err != nil {
		return "", ErrWritebackInvalidInput
	}
	if len(command.Content) == 0 || len(command.Content) > MaxWritebackContentBytes {
		return "", ErrWritebackInvalidInput
	}
	expectedChangeHash := ComputeChangeHash(targetPath, command.ExpectedBaseHash, string(command.Content))
	if !strings.EqualFold(expectedChangeHash, command.ApprovedChangeHash) {
		return "", ErrWritebackIdentityConflict
	}
	return ComputeWritebackResultHash(command.Content), nil
}

// ValidatePreparedWriteBinding 确认 PreparedWrite 只能由给定目标和 PrepareWrite 派生。
func ValidatePreparedWriteBinding(targetPath string, command PrepareWrite, prepared PreparedWrite) error {
	resultHash, err := ValidatePrepareWrite(targetPath, command)
	if err != nil {
		return err
	}
	if prepared.ExecutionID != command.ExecutionID ||
		!strings.EqualFold(prepared.ExpectedBaseHash, command.ExpectedBaseHash) ||
		!strings.EqualFold(prepared.ApprovedChangeHash, command.ApprovedChangeHash) ||
		!strings.EqualFold(prepared.ResultHash, resultHash) ||
		prepared.ByteSize != int64(len(command.Content)) ||
		path.Dir(prepared.TemporaryRef) != path.Dir(targetPath) {
		return ErrWritebackIdentityConflict
	}
	if err := validatePreparedWrite(prepared); err != nil {
		return ErrWritebackIdentityConflict
	}
	return nil
}

// ValidateAppliedWriteBinding 确认 AppliedWrite 保留当前 PreparedWrite 的不可变绑定和受控备份。
func ValidateAppliedWriteBinding(prepared PreparedWrite, applied AppliedWrite) error {
	if err := validatePreparedWrite(prepared); err != nil {
		return ErrWritebackIdentityConflict
	}
	if applied.ExecutionID != prepared.ExecutionID ||
		applied.TemporaryRef != prepared.TemporaryRef ||
		!strings.EqualFold(applied.BaseHash, prepared.ExpectedBaseHash) ||
		!strings.EqualFold(applied.ApprovedChangeHash, prepared.ApprovedChangeHash) ||
		!strings.EqualFold(applied.ResultHash, prepared.ResultHash) ||
		applied.ByteSize != prepared.ByteSize ||
		applied.Mode != prepared.Mode ||
		applied.LockToken != prepared.LockToken ||
		path.Dir(applied.BackupRef) != path.Dir(prepared.TemporaryRef) {
		return ErrWritebackIdentityConflict
	}
	if err := validateWritebackLocator(applied.BackupRef, ".bak"); err != nil {
		return ErrWritebackIdentityConflict
	}
	return nil
}

// ValidateRestoreResult 校验恢复结果不能同时表示实际恢复和重放，也不能表示空结果。
func ValidateRestoreResult(result RestoreResult) error {
	if result.Restored == result.Replayed {
		return ErrWritebackInvalidInput
	}
	return nil
}

func validatePreparedWrite(prepared PreparedWrite) error {
	if prepared.ExecutionID == "" || !ValidHash(prepared.ExpectedBaseHash) || !ValidHash(prepared.ApprovedChangeHash) || !ValidHash(prepared.ResultHash) || prepared.ByteSize <= 0 || prepared.ByteSize > MaxWritebackContentBytes || prepared.Mode&^uint32(0o7777) != 0 || !ValidHash(prepared.LockToken) {
		return ErrWritebackInvalidInput
	}
	return validateWritebackLocator(prepared.TemporaryRef, ".tmp")
}

func validateWritebackLocator(value, suffix string) error {
	clean, err := ValidateTargetPath(value)
	if err != nil || clean != value {
		return ErrWritebackInvalidInput
	}
	base := path.Base(clean)
	if !strings.HasPrefix(base, writebackLocatorPrefix) || !strings.HasSuffix(base, suffix) || len(base) <= len(writebackLocatorPrefix)+len(suffix) {
		return ErrWritebackInvalidInput
	}
	return nil
}
