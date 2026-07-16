package domain

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// WritebackStatus 是 Safe Writeback Durable Operation 的持久化状态。
type WritebackStatus string

const (
	WritebackStatusPrepared         WritebackStatus = "prepared"
	WritebackStatusFileApplied      WritebackStatus = "file_applied"
	WritebackStatusGitCommitted     WritebackStatus = "git_committed"
	WritebackStatusVerifying        WritebackStatus = "verifying"
	WritebackStatusNeedsRevision    WritebackStatus = "needs_revision"
	WritebackStatusApplyFailed      WritebackStatus = "apply_failed"
	WritebackStatusCompensatingFile WritebackStatus = "compensating_file"
	WritebackStatusCompensated      WritebackStatus = "compensated"
	WritebackStatusManualRecovery   WritebackStatus = "manual_recovery_required"
	WritebackStatusPublishRecovery  WritebackStatus = "publish_recovery_required"
	WritebackStatusVerifyFailed     WritebackStatus = "verify_failed"
	WritebackStatusRolledBack       WritebackStatus = "rolled_back"
	WritebackStatusCompleted        WritebackStatus = "completed"
	// 语义更完整的别名，便于 Adapter/Workflow 使用稳定状态名。
	WritebackStatusManualRecoveryRequired  = WritebackStatusManualRecovery
	WritebackStatusPublishRecoveryRequired = WritebackStatusPublishRecovery
)

var (
	// ErrWritebackInvalidInput 表示写回命令缺少必要绑定或字段格式非法。
	ErrWritebackInvalidInput = errors.New("writeback input is invalid")
	// ErrWritebackIdentityConflict 表示同一幂等键绑定了不同的不可变事实。
	ErrWritebackIdentityConflict = errors.New("writeback identity does not match")
	// ErrWritebackVersionConflict 表示检查点使用了过期执行版本。
	ErrWritebackVersionConflict = errors.New("writeback version does not match")
	// ErrWritebackInvalidTransition 表示不在唯一状态机中的状态迁移。
	ErrWritebackInvalidTransition = errors.New("writeback status transition is not allowed")
	// ErrWritebackPublishBindingConflict 表示 Commit Mapping 与执行绑定不一致。
	ErrWritebackPublishBindingConflict = errors.New("proposal commit is not bound to writeback")
)

// WritebackExecution 记录一次可恢复的 Safe Writeback Durable Operation。
// 仅保存稳定引用、哈希和受控相对定位符，不保存正文、Credential 或绝对路径。
type WritebackExecution struct {
	ID, WorkspaceID, WorkflowRunID, NodeRunID foundation.ID
	ProposalID, RevisionID, ApprovalID        foundation.ID
	WriteAuthorizationID, GitAuthorizationID  foundation.ID
	TargetPath                                string
	BaseHash, ResultHash, ApprovedChangeHash  string
	ApprovedGitHead                           string
	GitCommit, ParentGitCommit, DiffHash      string
	Status                                    WritebackStatus
	IdempotencyKey                            string
	FailureCode                               string
	ManualRecoveryRequired                    bool
	TemporaryRef, BackupRef                   string
	Version                                   int64
	CreatedAt, UpdatedAt                      time.Time
	CompletedAt                               *time.Time
}

// ProposalCommit 是 Proposal Revision 与 Git Commit 的不可变映射。
type ProposalCommit struct {
	ID, WorkspaceID, WritebackExecutionID foundation.ID
	ProposalID, RevisionID, ApprovalID    foundation.ID
	GitCommit, ParentGitCommit            string
	TargetPath, DiffHash, ResultHash      string
	CreatedAt                             time.Time
}

// CreateWriteback 是创建 prepared 执行记录的完整不可变绑定。
type CreateWriteback struct {
	ID, WorkspaceID, WorkflowRunID, NodeRunID foundation.ID
	ProposalID, RevisionID, ApprovalID        foundation.ID
	WriteAuthorizationID, GitAuthorizationID  foundation.ID
	TargetPath                                string
	BaseHash, ResultHash, ApprovedChangeHash  string
	ApprovedGitHead                           string
	IdempotencyKey                            string
	TemporaryRef, BackupRef                   string
	CreatedAt                                 time.Time
}

// CheckpointWriteback 是执行状态机的单步检查点命令。
type CheckpointWriteback struct {
	ExecutionID                foundation.ID
	ExpectedVersion            int64
	Status                     WritebackStatus
	ResultHash                 string
	FailureCode                string
	ManualRecoveryRequired     bool
	TemporaryRef, BackupRef    string
	GitCommit, ParentGitCommit string
	DiffHash                   string
	CompletedAt                *time.Time
	At                         time.Time
}

// PublishWriteback 在同一事务内发布 Commit Mapping、Proposal verifying 和重索引 Outbox。
type PublishWriteback struct {
	ExecutionID     foundation.ID
	ExpectedVersion int64
	Commit          ProposalCommit
	Event           WritebackOutboxEvent
}

// PublishWritebackResult 返回发布后的执行和 Commit 映射；重复发布时 Replayed=true。
type PublishWritebackResult struct {
	Execution WritebackExecution
	Commit    ProposalCommit
	Replayed  bool
}

// WritebackOutboxEvent 是 Publish 事务写入 Workflow Outbox 的稳定索引请求摘要。
// Payload 只能包含稳定引用和版本请求，禁止正文、Credential 或模型私有数据。
type WritebackOutboxEvent struct {
	ID             foundation.ID
	WorkspaceID    foundation.ID
	RunID          foundation.ID
	Type           string
	IdempotencyKey string
	Payload        json.RawMessage
	OccurredAt     time.Time
}

// ValidateWritebackTransition 是 Writeback 状态迁移的唯一领域事实源。
func ValidateWritebackTransition(from, to WritebackStatus) error {
	allowed := map[WritebackStatus]map[WritebackStatus]struct{}{
		WritebackStatusPrepared: {
			WritebackStatusFileApplied: {}, WritebackStatusNeedsRevision: {}, WritebackStatusApplyFailed: {},
		},
		WritebackStatusFileApplied: {
			WritebackStatusCompensatingFile: {}, WritebackStatusGitCommitted: {},
		},
		WritebackStatusCompensatingFile: {
			WritebackStatusCompensated: {}, WritebackStatusManualRecovery: {},
		},
		WritebackStatusGitCommitted: {
			WritebackStatusVerifying: {}, WritebackStatusPublishRecovery: {},
		},
		WritebackStatusPublishRecovery: {
			WritebackStatusVerifying: {}, WritebackStatusManualRecovery: {},
		},
		WritebackStatusVerifying: {
			WritebackStatusCompleted: {}, WritebackStatusVerifyFailed: {}, WritebackStatusRolledBack: {},
		},
		WritebackStatusVerifyFailed: {
			WritebackStatusVerifying: {}, WritebackStatusRolledBack: {},
		},
	}
	if _, ok := allowed[from][to]; !ok {
		return ErrWritebackInvalidTransition
	}
	return nil
}

// ValidateWritebackCreate 校验创建命令和所有不可变写回绑定。
func ValidateWritebackCreate(command CreateWriteback) error {
	if command.ID == "" || command.WorkspaceID == "" || command.WorkflowRunID == "" || command.NodeRunID == "" || command.ProposalID == "" || command.RevisionID == "" || command.ApprovalID == "" || command.WriteAuthorizationID == "" || command.GitAuthorizationID == "" {
		return ErrWritebackInvalidInput
	}
	target, err := ValidateTargetPath(command.TargetPath)
	if err != nil || target != command.TargetPath {
		return ErrWritebackInvalidInput
	}
	if !ValidHash(command.BaseHash) || !ValidHash(command.ResultHash) || !ValidHash(command.ApprovedChangeHash) || !ValidGitHead(command.ApprovedGitHead) {
		return ErrWritebackInvalidInput
	}
	if strings.TrimSpace(command.IdempotencyKey) == "" || len(strings.TrimSpace(command.IdempotencyKey)) > 128 {
		return ErrWritebackInvalidInput
	}
	if err := validateWritebackRef(command.TemporaryRef); err != nil {
		return ErrWritebackInvalidInput
	}
	if err := validateWritebackRef(command.BackupRef); err != nil {
		return ErrWritebackInvalidInput
	}
	return nil
}

// ValidateWritebackCheckpoint 校验版本、状态迁移和可变检查点字段。
func ValidateWritebackCheckpoint(current WritebackExecution, command CheckpointWriteback) error {
	if current.ID == "" || command.ExecutionID != current.ID || command.ExpectedVersion <= 0 || command.ExpectedVersion != current.Version {
		return ErrWritebackVersionConflict
	}
	if err := ValidateWritebackTransition(current.Status, command.Status); err != nil {
		return err
	}
	if command.ResultHash != "" && !ValidHash(command.ResultHash) {
		return ErrWritebackInvalidInput
	}
	if command.ResultHash != "" && !strings.EqualFold(command.ResultHash, current.ResultHash) {
		return ErrWritebackIdentityConflict
	}
	if command.Status == WritebackStatusGitCommitted {
		if !ValidGitHead(command.GitCommit) || !ValidGitHead(command.ParentGitCommit) || !ValidHash(command.DiffHash) || !ValidHash(command.ResultHash) {
			return ErrWritebackInvalidInput
		}
	}
	if strings.TrimSpace(command.FailureCode) != command.FailureCode || len(command.FailureCode) > 128 {
		return ErrWritebackInvalidInput
	}
	if err := validateWritebackRef(command.TemporaryRef); err != nil {
		return ErrWritebackInvalidInput
	}
	if err := validateWritebackRef(command.BackupRef); err != nil {
		return ErrWritebackInvalidInput
	}
	if command.Status == WritebackStatusManualRecovery && !command.ManualRecoveryRequired {
		return ErrWritebackInvalidInput
	}
	if command.Status == WritebackStatusCompleted && command.CompletedAt == nil {
		return ErrWritebackInvalidInput
	}
	if command.Status == WritebackStatusApplyFailed || command.Status == WritebackStatusVerifyFailed || command.Status == WritebackStatusNeedsRevision || command.Status == WritebackStatusPublishRecovery || command.Status == WritebackStatusManualRecovery {
		if command.FailureCode == "" {
			return ErrWritebackInvalidInput
		}
	}
	return nil
}

// ValidateWritebackIdentity 比较执行记录的完整不可变绑定。
func ValidateWritebackIdentity(existing, requested WritebackExecution) error {
	if !sameWritebackIdentity(existing, requested) {
		return ErrWritebackIdentityConflict
	}
	return nil
}

func sameWritebackIdentity(existing, requested WritebackExecution) bool {
	return existing.WorkspaceID == requested.WorkspaceID &&
		existing.WorkflowRunID == requested.WorkflowRunID &&
		existing.NodeRunID == requested.NodeRunID &&
		existing.ProposalID == requested.ProposalID &&
		existing.RevisionID == requested.RevisionID &&
		existing.ApprovalID == requested.ApprovalID &&
		existing.WriteAuthorizationID == requested.WriteAuthorizationID &&
		existing.GitAuthorizationID == requested.GitAuthorizationID &&
		existing.TargetPath == requested.TargetPath &&
		strings.EqualFold(existing.BaseHash, requested.BaseHash) &&
		strings.EqualFold(existing.ResultHash, requested.ResultHash) &&
		strings.EqualFold(existing.ApprovedChangeHash, requested.ApprovedChangeHash) &&
		strings.EqualFold(existing.ApprovedGitHead, requested.ApprovedGitHead) &&
		existing.IdempotencyKey == requested.IdempotencyKey
}

// ValidateProposalCommitBinding 确认 Commit Mapping 只能指向当前执行及其审批版本。
func ValidateProposalCommitBinding(execution WritebackExecution, commit ProposalCommit) error {
	if execution.ID == "" || commit.ID == "" || commit.WritebackExecutionID != execution.ID || commit.WorkspaceID != execution.WorkspaceID || commit.ProposalID != execution.ProposalID || commit.RevisionID != execution.RevisionID || commit.ApprovalID != execution.ApprovalID || commit.TargetPath != execution.TargetPath || !strings.EqualFold(commit.ResultHash, execution.ResultHash) || !strings.EqualFold(commit.GitCommit, execution.GitCommit) || !strings.EqualFold(commit.ParentGitCommit, execution.ParentGitCommit) || !strings.EqualFold(commit.DiffHash, execution.DiffHash) || !ValidGitHead(commit.GitCommit) || !ValidGitHead(commit.ParentGitCommit) || !ValidHash(commit.DiffHash) {
		return ErrWritebackPublishBindingConflict
	}
	return nil
}

// ValidateWritebackPublish 校验发布只能从 Git 已提交检查点进入 verifying，或重放既有发布。
func ValidateWritebackPublish(execution WritebackExecution, command PublishWriteback) error {
	if execution.ID == "" || command.ExecutionID != execution.ID || command.ExpectedVersion <= 0 || command.ExpectedVersion != execution.Version {
		return ErrWritebackVersionConflict
	}
	if execution.Status != WritebackStatusGitCommitted && execution.Status != WritebackStatusVerifying && execution.Status != WritebackStatusPublishRecovery {
		return ErrWritebackInvalidTransition
	}
	if err := ValidateProposalCommitBinding(execution, command.Commit); err != nil {
		return err
	}
	event := command.Event
	if event.ID == "" || event.WorkspaceID != execution.WorkspaceID || event.RunID != execution.WorkflowRunID || event.Type != "retrieval.revision.reindex_requested" || event.IdempotencyKey != ExpectedWritebackReindexKey(execution) || !validWritebackOutboxPayload(execution, event.Payload) {
		return ErrWritebackPublishBindingConflict
	}
	return nil
}

// ExpectedWritebackReindexKey 返回一次 Proposal Revision/Commit 唯一的重索引事件身份。
func ExpectedWritebackReindexKey(execution WritebackExecution) string {
	return fmt.Sprintf("reindex:%s:%s:%s:%s", execution.WorkspaceID, execution.ProposalID, execution.RevisionID, strings.ToLower(execution.GitCommit))
}

// ValidGitHead 判断 Git SHA-1 或 SHA-256 的十六进制表示。
func ValidGitHead(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, r := range value {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

func isJSONObject(payload []byte) bool {
	if len(bytes.TrimSpace(payload)) == 0 {
		return false
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(payload, &value); err != nil || value == nil {
		return false
	}
	return true
}

func validWritebackOutboxPayload(execution WritebackExecution, payload []byte) bool {
	if !isJSONObject(payload) {
		return false
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(payload, &value); err != nil || len(value) != 11 {
		return false
	}
	required := map[string]string{
		"workspace_id":           string(execution.WorkspaceID),
		"workflow_run_id":        string(execution.WorkflowRunID),
		"node_run_id":            string(execution.NodeRunID),
		"proposal_id":            string(execution.ProposalID),
		"revision_id":            string(execution.RevisionID),
		"approval_id":            string(execution.ApprovalID),
		"writeback_execution_id": string(execution.ID),
		"target_path":            execution.TargetPath,
		"result_hash":            strings.ToLower(execution.ResultHash),
		"git_commit":             strings.ToLower(execution.GitCommit),
	}
	for key, expected := range required {
		var actual string
		if err := json.Unmarshal(value[key], &actual); err != nil || actual != expected {
			return false
		}
	}
	schemaVersion, ok := value["schema_version"]
	if !ok || !bytes.Equal(bytes.TrimSpace(schemaVersion), []byte("1")) {
		return false
	}
	return true
}

func validateWritebackRef(value string) error {
	if value == "" {
		return nil
	}
	if strings.ContainsRune(value, '\x00') || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return ErrWritebackInvalidInput
	}
	clean, err := ValidateTargetPath(value)
	if err != nil || clean != value {
		return ErrWritebackInvalidInput
	}
	return nil
}
