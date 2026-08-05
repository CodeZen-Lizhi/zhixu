package domain

import (
	"encoding/hex"
	"path"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const (
	// MaxChangedFiles 限制每个运行持久化的冲突与 follow-up 元数据数量。
	MaxChangedFiles = 500
	// MaxListLimit 限制单个公开运行分页大小。
	MaxListLimit = 100
)

// Trigger 标识同步运行的触发来源。
type Trigger string

const (
	TriggerManual    Trigger = "MANUAL"
	TriggerAutomatic Trigger = "AUTOMATIC"
	TriggerRetry     Trigger = "RETRY"
)

// RunStatus 是 Git 操作的持久状态。
type RunStatus string

const (
	RunPending                RunStatus = "PENDING"
	RunFetching               RunStatus = "FETCHING"
	RunComparing              RunStatus = "COMPARING"
	RunFastForwarding         RunStatus = "FAST_FORWARDING"
	RunPushing                RunStatus = "PUSHING"
	RunVerifying              RunStatus = "VERIFYING"
	RunSucceeded              RunStatus = "SUCCEEDED"
	RunConflict               RunStatus = "CONFLICT"
	RunFailed                 RunStatus = "FAILED"
	RunStale                  RunStatus = "STALE"
	RunManualRecoveryRequired RunStatus = "MANUAL_RECOVERY_REQUIRED"
)

// Active 报告运行是否仍占用 Workspace 的活动运行槽位。
func (status RunStatus) Active() bool {
	switch status {
	case RunPending, RunFetching, RunComparing, RunFastForwarding, RunPushing, RunVerifying:
		return true
	default:
		return false
	}
}

// Terminal 报告 Git 结果是否已经持久化且不再执行。
func (status RunStatus) Terminal() bool {
	switch status {
	case RunSucceeded, RunConflict, RunFailed, RunStale, RunManualRecoveryRequired:
		return true
	default:
		return false
	}
}

// Direction 标识比较后选择的安全 Git 变更方向。
type Direction string

const (
	DirectionUnknown Direction = "UNKNOWN"
	DirectionNone    Direction = "NONE"
	DirectionPull    Direction = "PULL"
	DirectionPush    Direction = "PUSH"
)

// FailureClass 是与内部错误解耦的稳定用户可见分类。
type FailureClass string

const (
	FailureNone           FailureClass = "NONE"
	FailureDirty          FailureClass = "DIRTY"
	FailureDetached       FailureClass = "DETACHED"
	FailureDiverged       FailureClass = "DIVERGED"
	FailureAuthentication FailureClass = "AUTHENTICATION"
	FailureOffline        FailureClass = "OFFLINE"
	FailureRefDrift       FailureClass = "REF_DRIFT"
	FailureNonFastForward FailureClass = "NON_FAST_FORWARD"
	FailureStaleConfig    FailureClass = "STALE_CONFIG"
	FailureResultUnknown  FailureClass = "RESULT_UNKNOWN"
	FailureDependency     FailureClass = "DEPENDENCY"
	FailureInternal       FailureClass = "INTERNAL"
)

// IndexStatus 独立于 Git 成功状态跟踪外部捕获与索引。
type IndexStatus string

const (
	IndexNotRequired IndexStatus = "NOT_REQUIRED"
	IndexPending     IndexStatus = "PENDING"
	IndexRunning     IndexStatus = "RUNNING"
	IndexSucceeded   IndexStatus = "SUCCEEDED"
	IndexFailed      IndexStatus = "FAILED"
)

// Relation 是持久 Fetch 后的提交祖先关系。
type Relation string

const (
	RelationSame        Relation = "SAME"
	RelationRemoteAhead Relation = "REMOTE_AHEAD"
	RelationLocalAhead  Relation = "LOCAL_AHEAD"
	RelationDiverged    Relation = "DIVERGED"
)

// FileChangeKind 标识一条有界的 Git 路径级变更。
type FileChangeKind string

const (
	FileAdded    FileChangeKind = "ADDED"
	FileModified FileChangeKind = "MODIFIED"
	FileDeleted  FileChangeKind = "DELETED"
	FileRenamed  FileChangeKind = "RENAMED"
)

// FileChange 是不含文件内容和绝对路径的安全冲突元数据。
type FileChange struct {
	Path    string         `json:"path"`
	OldPath string         `json:"old_path,omitempty"`
	Kind    FileChangeKind `json:"kind"`
}

// Validate 校验相对规范路径元数据。
func (change FileChange) Validate() error {
	if !validRelativePath(change.Path) {
		return invalid(ErrorCodeInvalid, "Git changed path is invalid")
	}
	switch change.Kind {
	case FileAdded, FileModified, FileDeleted:
		if change.OldPath != "" {
			return invalid(ErrorCodeInvalid, "non-rename Git change has an old path")
		}
	case FileRenamed:
		if !validRelativePath(change.OldPath) || change.OldPath == change.Path {
			return invalid(ErrorCodeInvalid, "Git rename metadata is invalid")
		}
	default:
		return invalid(ErrorCodeInvalid, "Git change kind is invalid")
	}
	return nil
}

// Comparison 是状态机使用的 Fetch 后本地与远端状态。
type Comparison struct {
	Attached      bool
	Branch        string
	HeadOID       string
	RemoteOID     string
	WorktreeClean bool
	Relation      Relation
	Ahead         int
	Behind        int
	ChangedFiles  []FileChange
}

// Validate 校验完整的 Fetch 后比较结果。
func (comparison Comparison) Validate() error {
	if !validOID(comparison.HeadOID) || !validOID(comparison.RemoteOID) || len(comparison.ChangedFiles) > MaxChangedFiles ||
		comparison.Ahead < 0 || comparison.Behind < 0 {
		return invalid(ErrorCodeInvalid, "Git comparison is invalid")
	}
	if comparison.Attached {
		if !validBranch(comparison.Branch) {
			return invalid(ErrorCodeInvalid, "attached Git branch is invalid")
		}
	} else if comparison.Branch != "" {
		return invalid(ErrorCodeInvalid, "detached Git state has a branch")
	}
	for _, change := range comparison.ChangedFiles {
		if err := change.Validate(); err != nil {
			return err
		}
	}
	switch comparison.Relation {
	case RelationSame:
		if comparison.HeadOID != comparison.RemoteOID || comparison.Ahead != 0 || comparison.Behind != 0 {
			return invalid(ErrorCodeInvalid, "same Git comparison is inconsistent")
		}
	case RelationRemoteAhead:
		if comparison.HeadOID == comparison.RemoteOID || comparison.Ahead != 0 || comparison.Behind < 1 {
			return invalid(ErrorCodeInvalid, "remote-ahead Git comparison is inconsistent")
		}
	case RelationLocalAhead:
		if comparison.HeadOID == comparison.RemoteOID || comparison.Ahead < 1 || comparison.Behind != 0 {
			return invalid(ErrorCodeInvalid, "local-ahead Git comparison is inconsistent")
		}
	case RelationDiverged:
		if comparison.HeadOID == comparison.RemoteOID || comparison.Ahead < 1 || comparison.Behind < 1 {
			return invalid(ErrorCodeInvalid, "diverged Git comparison is inconsistent")
		}
	default:
		return invalid(ErrorCodeInvalid, "Git relation is invalid")
	}
	return nil
}

// MutationResult 报告 Git 变更是否返回了确定的进程结果。
type MutationResult struct {
	ResultKnown bool
}

// Verification 是声明 Git 成功前使用的变更后证明。
type Verification struct {
	HeadOID       string
	RemoteOID     string
	WorktreeClean bool
}

// Validate 校验 post-check 引用证明。
func (verification Verification) Validate() error {
	if !validOID(verification.HeadOID) || !validOID(verification.RemoteOID) {
		return invalid(ErrorCodeInvalid, "Git verification is invalid")
	}
	return nil
}

// SyncRun 是持久 Git 结果及其独立索引 follow-up。
type SyncRun struct {
	ID                foundation.ID
	WorkspaceID       foundation.ID
	ConfigRevision    int64
	RemoteURL         string
	Branch            string
	Trigger           Trigger
	RetryOfRunID      foundation.ID
	IdempotencyKey    string
	RequestHash       string
	Status            RunStatus
	Direction         Direction
	FailureClass      FailureClass
	ErrorCode         string
	Retryable         bool
	ExpectedHeadOID   string
	ExpectedRemoteOID string
	VerifiedHeadOID   string
	VerifiedRemoteOID string
	ChangedFiles      []FileChange
	IndexStatus       IndexStatus
	IndexErrorCode    string
	IndexRetryable    bool
	IndexVersionID    foundation.ID
	AttemptCount      int
	Version           int64
	CreatedAt         time.Time
	UpdatedAt         time.Time
	CompletedAt       *time.Time
}

// Validate 校验持久运行投影与终态字段形状。
func (run SyncRun) Validate() error {
	if !validID(run.ID) || !validID(run.WorkspaceID) || run.ConfigRevision <= 0 || !validRemoteURL(run.RemoteURL) ||
		!validBranch(run.Branch) || !canonicalText(run.IdempotencyKey, 128) || !validHash(run.RequestHash) ||
		run.AttemptCount < 0 || run.Version <= 0 || run.CreatedAt.IsZero() || run.UpdatedAt.Before(run.CreatedAt) ||
		len(run.ChangedFiles) > MaxChangedFiles {
		return invalid(ErrorCodeInvalid, "Git sync run is invalid")
	}
	if run.Trigger != TriggerManual && run.Trigger != TriggerAutomatic && run.Trigger != TriggerRetry {
		return invalid(ErrorCodeInvalid, "Git sync trigger is invalid")
	}
	if run.Trigger == TriggerRetry {
		if !validID(run.RetryOfRunID) {
			return invalid(ErrorCodeInvalid, "retry run binding is invalid")
		}
	} else if run.RetryOfRunID != "" {
		return invalid(ErrorCodeInvalid, "non-retry run has retry binding")
	}
	if !validRunStatus(run.Status) || !validDirection(run.Direction) || !validFailureClass(run.FailureClass) || !validIndexStatus(run.IndexStatus) {
		return invalid(ErrorCodeInvalid, "Git sync run state is invalid")
	}
	for _, oid := range []string{run.ExpectedHeadOID, run.ExpectedRemoteOID, run.VerifiedHeadOID, run.VerifiedRemoteOID} {
		if oid != "" && !validOID(oid) {
			return invalid(ErrorCodeInvalid, "Git sync run ref is invalid")
		}
	}
	for _, change := range run.ChangedFiles {
		if err := change.Validate(); err != nil {
			return err
		}
	}
	if run.Status.Active() {
		if run.CompletedAt != nil || run.FailureClass != FailureNone || run.ErrorCode != "" || run.Retryable {
			return invalid(ErrorCodeInvalid, "active Git sync run has terminal fields")
		}
	} else if run.CompletedAt == nil || run.CompletedAt.Before(run.CreatedAt) {
		return invalid(ErrorCodeInvalid, "terminal Git sync run is incomplete")
	}
	if run.Status == RunSucceeded {
		if run.FailureClass != FailureNone || run.ErrorCode != "" || run.Retryable ||
			run.VerifiedHeadOID == "" || run.VerifiedRemoteOID == "" || run.VerifiedHeadOID != run.VerifiedRemoteOID {
			return invalid(ErrorCodeInvalid, "successful Git sync run is inconsistent")
		}
	} else if run.Status.Terminal() && (run.FailureClass == FailureNone || run.ErrorCode == "") {
		return invalid(ErrorCodeInvalid, "failed Git sync run lacks a failure")
	}
	if run.Status == RunSucceeded && run.Direction == DirectionPull {
		if run.IndexStatus == IndexNotRequired {
			return invalid(ErrorCodeInvalid, "successful Git pull lacks an index follow-up")
		}
	} else if run.IndexStatus != IndexNotRequired {
		return invalid(ErrorCodeInvalid, "non-pull Git result has an index follow-up")
	}
	switch run.IndexStatus {
	case IndexNotRequired, IndexPending, IndexRunning:
		if run.IndexErrorCode != "" || run.IndexRetryable || run.IndexVersionID != "" {
			return invalid(ErrorCodeInvalid, "pending Git index state has terminal fields")
		}
	case IndexSucceeded:
		if run.IndexErrorCode != "" || run.IndexRetryable || (run.IndexVersionID != "" && !validID(run.IndexVersionID)) {
			return invalid(ErrorCodeInvalid, "successful Git index state is inconsistent")
		}
	case IndexFailed:
		if !canonicalText(run.IndexErrorCode, 128) || run.IndexVersionID != "" {
			return invalid(ErrorCodeInvalid, "failed Git index state is inconsistent")
		}
	}
	return nil
}

// AttemptStatus 是持久 Worker Attempt 的结果。
type AttemptStatus string

const (
	AttemptRunning   AttemptStatus = "RUNNING"
	AttemptSucceeded AttemptStatus = "SUCCEEDED"
	AttemptFailed    AttemptStatus = "FAILED"
)

// SyncAttempt 是包含冻结 checkpoint 的单次租约执行尝试。
type SyncAttempt struct {
	ID                foundation.ID
	WorkspaceID       foundation.ID
	RunID             foundation.ID
	AttemptNo         int
	Status            AttemptStatus
	Phase             RunStatus
	ExpectedHeadOID   string
	ExpectedRemoteOID string
	ResultKnown       *bool
	ErrorCode         string
	Retryable         bool
	LeaseOwner        string
	LeaseExpiresAt    time.Time
	StartedAt         time.Time
	CompletedAt       *time.Time
	Version           int64
}

// OutboxKind 标识持久 Git 运行或索引 follow-up 投递。
type OutboxKind string

const (
	OutboxExecuteRun    OutboxKind = "EXECUTE_RUN"
	OutboxIndexFollowup OutboxKind = "INDEX_FOLLOWUP"
)

// OutboxLease 是单次投递基于数据库时间的所有权栅栏。
type OutboxLease struct {
	ID           foundation.ID
	WorkspaceID  foundation.ID
	RunID        foundation.ID
	Kind         OutboxKind
	Owner        string
	AttemptCount int
	Version      int64
	LeaseUntil   time.Time
}

func validRunStatus(status RunStatus) bool {
	switch status {
	case RunPending, RunFetching, RunComparing, RunFastForwarding, RunPushing, RunVerifying,
		RunSucceeded, RunConflict, RunFailed, RunStale, RunManualRecoveryRequired:
		return true
	default:
		return false
	}
}

func validDirection(direction Direction) bool {
	return direction == DirectionUnknown || direction == DirectionNone || direction == DirectionPull || direction == DirectionPush
}

func validFailureClass(class FailureClass) bool {
	switch class {
	case FailureNone, FailureDirty, FailureDetached, FailureDiverged, FailureAuthentication, FailureOffline,
		FailureRefDrift, FailureNonFastForward, FailureStaleConfig, FailureResultUnknown, FailureDependency, FailureInternal:
		return true
	default:
		return false
	}
}

func validIndexStatus(status IndexStatus) bool {
	return status == IndexNotRequired || status == IndexPending || status == IndexRunning || status == IndexSucceeded || status == IndexFailed
}

func validOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func validHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func validRelativePath(value string) bool {
	return canonicalText(value, 4096) && value != "." && !strings.HasPrefix(value, "/") && path.Clean(value) == value &&
		!strings.HasPrefix(value, "../") && !strings.ContainsRune(value, '\x00')
}
