package application

import (
	"context"
	"errors"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

// WritebackAuditStep 是 Safe Writeback 内两个固定逻辑 Tool 的审计阶段。
type WritebackAuditStep string

const (
	// WritebackAuditApply 表示批准补丁的文件应用阶段。
	WritebackAuditApply WritebackAuditStep = "apply"
	// WritebackAuditGit 表示批准结果的 Git Commit 阶段。
	WritebackAuditGit WritebackAuditStep = "git"
)

// WritebackResumeIdentity 是 Worker 从持久 Workflow Claim 传入 Safe Writeback 的完整当前 Attempt 身份。
// 它只用于证明本次恢复资格；历史 Tool Call 仍保留首次 STARTED 时的 Attempt 身份。
type WritebackResumeIdentity struct {
	WorkspaceID       foundation.ID
	DefinitionID      foundation.ID
	DefinitionVersion int64
	DefinitionHash    string
	WorkflowRunID     foundation.ID
	NodeKey           string
	NodeRunID         foundation.ID
	NodeAttemptID     foundation.ID
	LeaseOwner        string
	LeaseFence        int64
}

// Validate 校验当前恢复身份完整且不允许复用不同层级的稳定 ID。
func (identity WritebackResumeIdentity) Validate() error {
	ids := []foundation.ID{identity.WorkspaceID, identity.DefinitionID, identity.WorkflowRunID, identity.NodeRunID, identity.NodeAttemptID}
	seen := make(map[foundation.ID]struct{}, len(ids))
	for _, id := range ids {
		parsed, err := foundation.ParseID(string(id))
		if err != nil || parsed != id {
			return errors.Join(err, domain.ErrWritebackInvalidInput)
		}
		if _, exists := seen[id]; exists {
			return domain.ErrWritebackIdentityConflict
		}
		seen[id] = struct{}{}
	}
	if identity.DefinitionVersion < 1 || !domain.ValidHash(identity.DefinitionHash) ||
		strings.TrimSpace(identity.NodeKey) == "" || len(identity.NodeKey) > 128 || strings.ContainsAny(identity.NodeKey, "\x00\r\n") ||
		domain.ValidateWritebackLeaseOwner(identity.LeaseOwner) != nil || identity.LeaseFence < 1 {
		return domain.ErrWritebackInvalidInput
	}
	return nil
}

// WritebackAuditRecorder 在既有 Safe Writeback 副作用 seam 周围维护逻辑 Tool Call。
// EnsureStarted 只可在尚未发生副作用时创建；RequireStarted 用于 response-loss 恢复并禁止事后补造 STARTED。
type WritebackAuditRecorder interface {
	// EnsureStarted 在副作用前创建或重放完整绑定的 STARTED。
	EnsureStarted(context.Context, WritebackResumeIdentity, domain.WritebackExecution, WritebackAuditStep) error
	// RequireStarted 要求历史 STARTED 已存在，不能在发现既有副作用后补造审计记录。
	RequireStarted(context.Context, WritebackResumeIdentity, domain.WritebackExecution, WritebackAuditStep) error
	// RecordSucceeded 仅根据已持久化的 writeback_execution receipt 将既有 STARTED 归约成功。
	RecordSucceeded(context.Context, WritebackResumeIdentity, domain.WritebackExecution, WritebackAuditStep) error
}
