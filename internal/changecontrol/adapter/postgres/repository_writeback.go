package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var _ domain.WritebackRepository = (*Repository)(nil)
var _ domain.WritebackSagaRepository = (*Repository)(nil)
var _ domain.WritebackExecutionLookup = (*Repository)(nil)
var _ workflowapplication.CancellationSafetyGuard = (*Repository)(nil)

// SafeToCancelWorkflowNode 只在未创建 Writeback，或 Durable Execution 已完成、
// 已补偿、明确人工恢复，或 verifying 的恢复证据已清理时允许 Workflow 终态取消。
func (r *Repository) SafeToCancelWorkflowNode(ctx context.Context, transaction any, nodeRunID foundation.ID) (bool, error) {
	parsed, err := foundation.ParseID(string(nodeRunID))
	if err != nil || parsed != nodeRunID {
		return false, foundation.NewError(foundation.ErrorInvalidInput, "WRITEBACK_CANCELLATION_SAFETY_INVALID", false, errors.Join(err, domain.ErrWritebackInvalidInput))
	}
	tx, ok := transaction.(pgx.Tx)
	if !ok || tx == nil {
		return false, foundation.NewError(foundation.ErrorInvalidInput, "WRITEBACK_CANCELLATION_TRANSACTION_INVALID", false, domain.ErrWritebackInvalidInput)
	}
	rows, err := tx.Query(ctx, `
		SELECT status,cleanup_completed_at IS NOT NULL
		FROM change_control.writeback_execution
		WHERE node_run_id=$1`, string(nodeRunID))
	if err != nil {
		return false, classifyWriteback(err, "WRITEBACK_CANCELLATION_SAFETY_QUERY_FAILED")
	}
	defer rows.Close()
	for rows.Next() {
		var status domain.WritebackStatus
		var cleanupCompleted bool
		if err := rows.Scan(&status, &cleanupCompleted); err != nil {
			return false, classifyWriteback(err, "WRITEBACK_CANCELLATION_SAFETY_QUERY_FAILED")
		}
		if !domain.IsWritebackCancellationSafe(status, cleanupCompleted) {
			return false, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, classifyWriteback(err, "WRITEBACK_CANCELLATION_SAFETY_QUERY_FAILED")
	}
	return true, nil
}

// BeginWriteback 在单一事务中校验 lease、消费双授权、创建 Execution 并推进 Proposal。
// Credential 只在当前调用栈参与哈希绑定比较，绝不写入任何持久化字段。
func (r *Repository) BeginWriteback(ctx context.Context, command domain.BeginWriteback) (domain.WritebackExecution, error) {
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	command.LeaseOwner = strings.TrimSpace(command.LeaseOwner)
	if err := domain.ValidateBeginWriteback(command); err != nil {
		return domain.WritebackExecution{}, writebackDomainError(err)
	}
	if err := domain.ValidateWritebackLeaseOwner(command.LeaseOwner); err != nil {
		return domain.WritebackExecution{}, writebackDomainError(err)
	}
	command.WriteAuthorization.Credential = hashWritebackCredential(command.WriteAuthorization.Credential)
	command.GitAuthorization.Credential = hashWritebackCredential(command.GitAuthorization.Credential)
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.WritebackExecution{}, classifyWriteback(err, "WRITEBACK_BEGIN_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&now); err != nil {
		return domain.WritebackExecution{}, classifyWriteback(err, "WRITEBACK_BEGIN_CLOCK_QUERY_FAILED")
	}
	now = now.UTC()

	writeAuth, gitAuth, err := lockBeginAuthorizations(ctx, tx, command)
	if err != nil {
		return domain.WritebackExecution{}, err
	}
	if writeAuth.WorkspaceID != command.WorkspaceID || writeAuth.WorkflowRunID != command.WorkflowRunID || writeAuth.NodeRunID != command.NodeRunID || writeAuth.ProposalID != command.ProposalID || gitAuth.WorkspaceID != command.WorkspaceID || gitAuth.WorkflowRunID != command.WorkflowRunID || gitAuth.NodeRunID != command.NodeRunID || gitAuth.ProposalID != command.ProposalID {
		return domain.WritebackExecution{}, writebackDomainError(domain.ErrWritebackIdentityConflict)
	}
	if writeAuth.RevisionID != gitAuth.RevisionID || writeAuth.ApprovalID != gitAuth.ApprovalID || !strings.EqualFold(writeAuth.ApprovedChangeHash, gitAuth.ApprovedChangeHash) || !strings.EqualFold(writeAuth.TargetVersion, gitAuth.TargetVersion) || writeAuth.Scope != gitAuth.Scope {
		return domain.WritebackExecution{}, writebackDomainError(domain.ErrWritebackIdentityConflict)
	}

	// Proposal、Revision、Approval 以固定顺序锁定，正文/目标/Hash 全部从此处派生。
	var proposalWorkspace, proposalStatus, targetPath, targetMode, baseHash, content, revisionChangeHash string
	var proposalWorkflowRunID *string
	var revisionProposal, approvalProposal, approvalRevision string
	var approvalChangeHash, approvalDecision string
	var approvalGitHead *string
	var revisionID, approvalID string
	err = tx.QueryRow(ctx, `
			SELECT p.workspace_id::text,p.status,p.workflow_run_id::text,r.id::text,r.proposal_id::text,r.target_path,r.target_mode,r.base_hash,r.content,r.change_hash,
			       a.id::text,a.proposal_id::text,a.revision_id::text,a.change_hash,a.decision,a.approved_git_head
		FROM change_control.proposal p
		JOIN change_control.proposal_revision r ON r.id=$2 AND r.proposal_id=p.id
		JOIN change_control.approval a ON a.id=$3 AND a.proposal_id=p.id AND a.revision_id=r.id
		WHERE p.id=$1
		FOR UPDATE OF p,r,a`, string(command.ProposalID), string(writeAuth.RevisionID), string(writeAuth.ApprovalID)).Scan(
		&proposalWorkspace, &proposalStatus, &proposalWorkflowRunID, &revisionID, &revisionProposal, &targetPath, &targetMode, &baseHash, &content, &revisionChangeHash,
		&approvalID, &approvalProposal, &approvalRevision, &approvalChangeHash, &approvalDecision, &approvalGitHead)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WritebackExecution{}, foundation.NewError(foundation.ErrorNotFound, "WRITEBACK_APPROVAL_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.WritebackExecution{}, classifyWriteback(err, "WRITEBACK_BEGIN_APPROVAL_QUERY_FAILED")
	}
	normalizedTargetMode, modeErr := domain.ValidateTargetMode(domain.TargetMode(targetMode))
	if modeErr != nil || proposalWorkspace != string(command.WorkspaceID) || proposalWorkflowRunID == nil || *proposalWorkflowRunID != string(command.WorkflowRunID) || revisionProposal != string(command.ProposalID) || approvalProposal != string(command.ProposalID) || approvalRevision != revisionID || foundation.ID(revisionID) != writeAuth.RevisionID || foundation.ID(approvalID) != writeAuth.ApprovalID || approvalDecision != string(domain.DecisionApproved) || approvalGitHead == nil || !strings.EqualFold(revisionChangeHash, writeAuth.ApprovedChangeHash) || !strings.EqualFold(approvalChangeHash, writeAuth.ApprovedChangeHash) || !strings.EqualFold(baseHash, writeAuth.TargetVersion) || domain.NormalizeTargetMode(writeAuth.TargetMode) != normalizedTargetMode || domain.NormalizeTargetMode(gitAuth.TargetMode) != normalizedTargetMode || writeAuth.Scope != domain.ExpectedAuthorizationScopeForTarget(targetPath, normalizedTargetMode) || gitAuth.Scope != domain.ExpectedAuthorizationScopeForTarget(targetPath, normalizedTargetMode) {
		return domain.WritebackExecution{}, writebackDomainError(domain.ErrWritebackIdentityConflict)
	}

	if err := validateBeginNodeLease(ctx, tx, command, now, writeAuth); err != nil {
		return domain.WritebackExecution{}, err
	}

	resultHash := domain.ComputeWritebackResultHash([]byte(content))
	computedChangeHash, hashErr := domain.ComputeChangeHashForTarget(command.WorkspaceID, targetPath, normalizedTargetMode, baseHash, content)
	if hashErr != nil || !strings.EqualFold(computedChangeHash, writeAuth.ApprovedChangeHash) {
		return domain.WritebackExecution{}, writebackDomainError(domain.ErrWritebackIdentityConflict)
	}
	// 幂等重放必须在授权消费前返回同一个 Durable Execution。
	var existing domain.WritebackExecution
	existing, existingErr := scanWritebackExecution(tx.QueryRow(ctx, `SELECT `+writebackColumns+` FROM change_control.writeback_execution WHERE workspace_id=$1 AND idempotency_key=$2 FOR UPDATE`, string(command.WorkspaceID), command.IdempotencyKey))
	if existingErr == nil {
		requested := existing
		requested.ID = existing.ID
		requested.WorkflowRunID, requested.NodeRunID = command.WorkflowRunID, command.NodeRunID
		requested.ProposalID, requested.RevisionID, requested.ApprovalID = command.ProposalID, writeAuth.RevisionID, writeAuth.ApprovalID
		requested.WriteAuthorizationID, requested.GitAuthorizationID = writeAuth.ID, gitAuth.ID
		requested.TargetMode = normalizedTargetMode
		requested.TargetPath, requested.BaseHash, requested.ResultHash, requested.ApprovedChangeHash, requested.ApprovedGitHead = targetPath, baseHash, resultHash, writeAuth.ApprovedChangeHash, *approvalGitHead
		if err := domain.ValidateWritebackIdentity(existing, requested); err != nil {
			return domain.WritebackExecution{}, writebackDomainError(err)
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.WritebackExecution{}, classifyWriteback(err, "WRITEBACK_BEGIN_COMMIT_FAILED")
		}
		return existing, nil
	}
	if !errors.Is(existingErr, pgx.ErrNoRows) {
		return domain.WritebackExecution{}, classifyWriteback(existingErr, "WRITEBACK_BEGIN_IDEMPOTENCY_QUERY_FAILED")
	}
	// consumed 只允许用于上面的 exact Execution replay。若授权曾被旧消费路径
	// 单独消费却没有 Durable Execution，不能再次把同一凭据用于首次副作用。
	if writeAuth.Status != domain.AuthorizationIssued || gitAuth.Status != domain.AuthorizationIssued {
		return domain.WritebackExecution{}, foundation.NewError(foundation.ErrorPermissionDenied, "WRITEBACK_AUTHORIZATION_ALREADY_CONSUMED", false, errors.New("authorization was consumed without this writeback execution"))
	}
	if proposalStatus != string(domain.StatusApproved) {
		return domain.WritebackExecution{}, foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_PROPOSAL_STATE_CONFLICT", false, errors.New("proposal is no longer approved"))
	}

	// 授权消费与 Execution INSERT 处于同一事务；任一步失败都会回滚前面的消费。
	if err := consumeBeginAuthorization(ctx, tx, writeAuth, now); err != nil {
		return domain.WritebackExecution{}, err
	}
	if err := consumeBeginAuthorization(ctx, tx, gitAuth, now); err != nil {
		return domain.WritebackExecution{}, err
	}
	executionID := command.ExecutionID
	if executionID == "" {
		executionID, err = foundation.NewUUIDGenerator(nil).New()
		if err != nil {
			return domain.WritebackExecution{}, classifyWriteback(err, "WRITEBACK_BEGIN_ID_GENERATION_FAILED")
		}
	}
	requested := domain.CreateWriteback{ID: executionID, WorkspaceID: command.WorkspaceID, WorkflowRunID: command.WorkflowRunID, NodeRunID: command.NodeRunID, ProposalID: command.ProposalID, RevisionID: writeAuth.RevisionID, ApprovalID: writeAuth.ApprovalID, WriteAuthorizationID: writeAuth.ID, GitAuthorizationID: gitAuth.ID, TargetPath: targetPath, TargetMode: normalizedTargetMode, BaseHash: baseHash, ResultHash: resultHash, ApprovedChangeHash: strings.ToLower(writeAuth.ApprovedChangeHash), ApprovedGitHead: strings.ToLower(*approvalGitHead), IdempotencyKey: command.IdempotencyKey, CreatedAt: now}
	if err := domain.ValidateWritebackCreate(requested); err != nil {
		return domain.WritebackExecution{}, writebackDomainError(err)
	}
	persisted, err := scanWritebackExecution(tx.QueryRow(ctx, writebackInsert+` RETURNING `+writebackColumns,
		string(requested.ID), string(requested.WorkspaceID), string(requested.WorkflowRunID), string(requested.NodeRunID), string(requested.ProposalID), string(requested.RevisionID), string(requested.ApprovalID), string(requested.WriteAuthorizationID), string(requested.GitAuthorizationID), requested.TargetPath, string(domain.NormalizeTargetMode(requested.TargetMode)), requested.BaseHash, requested.ResultHash, requested.ApprovedChangeHash, requested.ApprovedGitHead, string(domain.WritebackStatusPrepared), requested.IdempotencyKey, nil, nil, int64(1), now, now))
	if err != nil {
		return domain.WritebackExecution{}, classifyWriteback(err, "WRITEBACK_BEGIN_CREATE_FAILED")
	}
	proposalTag, err := tx.Exec(ctx, `UPDATE change_control.proposal SET status=$2,updated_at=$3,version=version+1 WHERE id=$1 AND workspace_id=$4 AND status=$5`, string(command.ProposalID), string(domain.StatusApplying), now, string(command.WorkspaceID), string(domain.StatusApproved))
	if err != nil {
		return domain.WritebackExecution{}, classifyWriteback(err, "WRITEBACK_BEGIN_PROPOSAL_FAILED")
	}
	if proposalTag.RowsAffected() != 1 {
		return domain.WritebackExecution{}, foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_PROPOSAL_STATE_CONFLICT", false, errors.New("proposal is no longer approved"))
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.WritebackExecution{}, classifyWriteback(err, "WRITEBACK_BEGIN_COMMIT_FAILED")
	}
	return persisted, nil
}

// ValidateWritebackLease 验证 execution 对应 Node 当前仍由 owner 持有有效租约。
func (r *Repository) ValidateWritebackLease(ctx context.Context, id foundation.ID, owner string) error {
	if err := domain.ValidateWritebackLeaseOwner(owner); err != nil {
		return writebackDomainError(err)
	}
	var one int
	err := r.db.QueryRow(ctx, `
		SELECT 1 FROM change_control.writeback_execution e
		JOIN workflow.node_run n ON n.id=e.node_run_id
		WHERE e.id=$1 AND n.status='running' AND n.lease_owner=$2
		  AND n.lease_until > CURRENT_TIMESTAMP LIMIT 1`, string(id), strings.TrimSpace(owner)).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_LEASE_LOST", true, domain.ErrWritebackLeaseLost)
	}
	if err != nil {
		return classifyWriteback(err, "WRITEBACK_LEASE_QUERY_FAILED")
	}
	return nil
}

// FinalizeWritebackCleanup 只记录 cleanup 完成，不改变 Proposal/Execution 业务状态。
func (r *Repository) FinalizeWritebackCleanup(ctx context.Context, executionID foundation.ID, expectedVersion int64, at time.Time) (domain.WritebackExecution, error) {
	if executionID == "" || expectedVersion <= 0 {
		return domain.WritebackExecution{}, writebackDomainError(domain.ErrWritebackInvalidInput)
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.WritebackExecution{}, classifyWriteback(err, "WRITEBACK_CLEANUP_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	current, err := scanWritebackExecution(tx.QueryRow(ctx, `SELECT `+writebackColumns+` FROM change_control.writeback_execution WHERE id=$1 FOR UPDATE`, string(executionID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WritebackExecution{}, foundation.NewError(foundation.ErrorNotFound, "WRITEBACK_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.WritebackExecution{}, classifyWriteback(err, "WRITEBACK_CLEANUP_QUERY_FAILED")
	}
	if current.Status != domain.WritebackStatusVerifying {
		return domain.WritebackExecution{}, writebackDomainError(domain.ErrWritebackVersionConflict)
	}
	if current.CleanupCompletedAt != nil {
		if err := tx.Commit(ctx); err != nil {
			return domain.WritebackExecution{}, classifyWriteback(err, "WRITEBACK_CLEANUP_COMMIT_FAILED")
		}
		return current, nil
	}
	if current.Version != expectedVersion {
		return domain.WritebackExecution{}, writebackDomainError(domain.ErrWritebackVersionConflict)
	}
	var databaseNow time.Time
	if err := tx.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&databaseNow); err != nil {
		return domain.WritebackExecution{}, classifyWriteback(err, "WRITEBACK_CLEANUP_CLOCK_QUERY_FAILED")
	}
	// PostgreSQL Adapter 始终使用数据库可信时间；At 只保留在端口中供确定性 fake 使用。
	at = databaseNow
	updated, err := scanWritebackExecution(tx.QueryRow(ctx, `UPDATE change_control.writeback_execution SET cleanup_completed_at=$2,updated_at=$2,version=version+1 WHERE id=$1 AND version=$3 AND status=$4 RETURNING `+writebackColumns, string(executionID), at.UTC(), expectedVersion, string(domain.WritebackStatusVerifying)))
	if err != nil {
		return domain.WritebackExecution{}, classifyWriteback(err, "WRITEBACK_CLEANUP_UPDATE_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.WritebackExecution{}, classifyWriteback(err, "WRITEBACK_CLEANUP_COMMIT_FAILED")
	}
	return updated, nil
}

func lockBeginAuthorizations(ctx context.Context, tx pgx.Tx, command domain.BeginWriteback) (domain.ToolAuthorization, domain.ToolAuthorization, error) {
	keys := []string{command.WriteAuthorization.IdempotencyKey, command.GitAuthorization.IdempotencyKey}
	var ids []string
	rows, err := tx.Query(ctx, `SELECT id::text FROM change_control.tool_authorization WHERE workspace_id=$1 AND idempotency_key = ANY($2::text[]) ORDER BY id`, string(command.WorkspaceID), keys)
	if err != nil {
		return domain.ToolAuthorization{}, domain.ToolAuthorization{}, classifyWriteback(err, "WRITEBACK_BEGIN_AUTHORIZATION_QUERY_FAILED")
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return domain.ToolAuthorization{}, domain.ToolAuthorization{}, classifyWriteback(err, "WRITEBACK_BEGIN_AUTHORIZATION_QUERY_FAILED")
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return domain.ToolAuthorization{}, domain.ToolAuthorization{}, classifyWriteback(err, "WRITEBACK_BEGIN_AUTHORIZATION_QUERY_FAILED")
	}
	if len(ids) != 2 || ids[0] == ids[1] {
		return domain.ToolAuthorization{}, domain.ToolAuthorization{}, foundation.NewError(foundation.ErrorPermissionDenied, "WRITEBACK_AUTHORIZATION_NOT_FOUND", false, errors.New("both write authorizations are required"))
	}
	sort.Strings(ids)
	auths := make([]domain.ToolAuthorization, 0, 2)
	for _, id := range ids {
		auth, err := scanAuthorization(tx.QueryRow(ctx, `SELECT `+authorizationColumns+` FROM change_control.tool_authorization WHERE id=$1 FOR UPDATE`, id))
		if err != nil {
			return domain.ToolAuthorization{}, domain.ToolAuthorization{}, classifyWriteback(err, "WRITEBACK_BEGIN_AUTHORIZATION_QUERY_FAILED")
		}
		auths = append(auths, auth)
	}
	var writeAuth, gitAuth domain.ToolAuthorization
	for _, auth := range auths {
		switch auth.Capability {
		case domain.CapabilityWriteKnowledge:
			writeAuth = auth
		case domain.CapabilityGitWrite:
			gitAuth = auth
		}
	}
	if writeAuth.ID == "" || gitAuth.ID == "" {
		return domain.ToolAuthorization{}, domain.ToolAuthorization{}, foundation.NewError(foundation.ErrorPermissionDenied, "WRITEBACK_AUTHORIZATION_BINDING_CONFLICT", false, errors.New("authorization capabilities are incomplete"))
	}
	if err := validateBeginAuthorization(writeAuth, command.WriteAuthorization); err != nil {
		return domain.ToolAuthorization{}, domain.ToolAuthorization{}, err
	}
	if err := validateBeginAuthorization(gitAuth, command.GitAuthorization); err != nil {
		return domain.ToolAuthorization{}, domain.ToolAuthorization{}, err
	}
	return writeAuth, gitAuth, nil
}

func validateBeginAuthorization(auth domain.ToolAuthorization, request domain.AuthorizationConsume) error {
	if err := domain.ValidateAuthorizationConsumeBinding(auth, request); err != nil {
		return foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_AUTHORIZATION_BINDING_CONFLICT", false, err)
	}
	if auth.Status != domain.AuthorizationIssued && auth.Status != domain.AuthorizationConsumed {
		return foundation.NewError(foundation.ErrorPermissionDenied, "WRITEBACK_AUTHORIZATION_NOT_USABLE", false, errors.New("authorization is not issued or consumed"))
	}
	return nil
}

func validateBeginNodeLease(ctx context.Context, tx pgx.Tx, command domain.BeginWriteback, now time.Time, auth domain.ToolAuthorization) error {
	var status, owner string
	var leaseUntil *time.Time
	err := tx.QueryRow(ctx, `SELECT n.status,n.lease_owner,n.lease_until FROM workflow.run r JOIN workflow.node_run n ON n.run_id=r.id WHERE r.id=$1 AND n.id=$2 AND r.workspace_id=$3 FOR UPDATE OF r,n`, string(command.WorkflowRunID), string(command.NodeRunID), string(command.WorkspaceID)).Scan(&status, &owner, &leaseUntil)
	if errors.Is(err, pgx.ErrNoRows) {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "WRITEBACK_WORKFLOW_CONTEXT_INVALID", false, err)
	}
	if err != nil {
		return classifyWriteback(err, "WRITEBACK_WORKFLOW_CONTEXT_QUERY_FAILED")
	}
	if auth.WorkflowRunID != command.WorkflowRunID || auth.NodeRunID != command.NodeRunID || status != "running" || strings.TrimSpace(owner) != command.LeaseOwner || leaseUntil == nil || !leaseUntil.After(now) {
		return foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_LEASE_LOST", true, domain.ErrWritebackLeaseLost)
	}
	return nil
}

func consumeBeginAuthorization(ctx context.Context, tx pgx.Tx, auth domain.ToolAuthorization, now time.Time) error {
	if auth.Status == domain.AuthorizationConsumed {
		return nil
	}
	if auth.Status != domain.AuthorizationIssued || !now.Before(auth.ExpiresAt) {
		return foundation.NewError(foundation.ErrorPermissionDenied, "WRITEBACK_AUTHORIZATION_EXPIRED", false, errors.New("authorization is expired"))
	}
	tag, err := tx.Exec(ctx, `UPDATE change_control.tool_authorization SET status='consumed',consumed_at=$2,version=version+1 WHERE id=$1 AND status='issued'`, string(auth.ID), now)
	if err != nil {
		return classifyWriteback(err, "WRITEBACK_AUTHORIZATION_CONSUME_FAILED")
	}
	if tag.RowsAffected() != 1 {
		return foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_AUTHORIZATION_CONSUME_CONFLICT", false, errors.New("authorization changed before consumption"))
	}
	return nil
}

func hashWritebackCredential(credential string) string {
	digest := sha256.Sum256([]byte(credential))
	return hex.EncodeToString(digest[:])
}

// CreateWritebackExecution 在任何文件副作用前创建或重放 Durable Writeback Execution。
func (r *Repository) CreateWritebackExecution(ctx context.Context, command domain.CreateWriteback) (domain.WritebackExecution, error) {
	command = normalizeCreateWriteback(command)
	if err := domain.ValidateWritebackCreate(command); err != nil {
		return domain.WritebackExecution{}, writebackDomainError(err)
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.WritebackExecution{}, classifyWriteback(err, "WRITEBACK_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if existing, found, lookupErr := lookupWritebackExecution(ctx, tx, command); lookupErr != nil {
		return domain.WritebackExecution{}, lookupErr
	} else if found {
		requested := executionFromCreate(command, existing.CreatedAt)
		if identityErr := domain.ValidateWritebackIdentity(existing, requested); identityErr != nil {
			return domain.WritebackExecution{}, writebackDomainError(identityErr)
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.WritebackExecution{}, classifyWriteback(err, "WRITEBACK_COMMIT_FAILED")
		}
		return existing, nil
	}

	var databaseNow time.Time
	if err := tx.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&databaseNow); err != nil {
		return domain.WritebackExecution{}, classifyWriteback(err, "WRITEBACK_CLOCK_QUERY_FAILED")
	}
	requested := executionFromCreate(command, databaseNow.UTC())
	persisted, err := scanWritebackExecution(tx.QueryRow(ctx, writebackInsert+` ON CONFLICT DO NOTHING RETURNING `+writebackColumns,
		string(requested.ID), string(requested.WorkspaceID), string(requested.WorkflowRunID), string(requested.NodeRunID),
		string(requested.ProposalID), string(requested.RevisionID), string(requested.ApprovalID),
		string(requested.WriteAuthorizationID), string(requested.GitAuthorizationID), requested.TargetPath,
		string(domain.NormalizeTargetMode(requested.TargetMode)), requested.BaseHash, requested.ResultHash, requested.ApprovedChangeHash, requested.ApprovedGitHead,
		string(requested.Status), requested.IdempotencyKey, nullableString(requested.TemporaryRef), nullableString(requested.BackupRef),
		requested.Version, requested.CreatedAt, requested.UpdatedAt))
	if errors.Is(err, pgx.ErrNoRows) {
		existing, queryErr := findWritebackConflict(ctx, tx, requested)
		if queryErr != nil {
			return domain.WritebackExecution{}, queryErr
		}
		if identityErr := domain.ValidateWritebackIdentity(existing, requested); identityErr != nil {
			return domain.WritebackExecution{}, writebackDomainError(identityErr)
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.WritebackExecution{}, classifyWriteback(err, "WRITEBACK_COMMIT_FAILED")
		}
		return existing, nil
	}
	if err != nil {
		return domain.WritebackExecution{}, classifyWriteback(err, "WRITEBACK_CREATE_FAILED")
	}

	proposalTag, err := tx.Exec(ctx, `
		UPDATE change_control.proposal
		SET status=$2,updated_at=$3,version=version+1
		WHERE id=$1 AND workspace_id=$4 AND status=$5`,
		string(command.ProposalID), string(domain.StatusApplying), databaseNow.UTC(), string(command.WorkspaceID), string(domain.StatusApproved))
	if err != nil {
		return domain.WritebackExecution{}, classifyWriteback(err, "WRITEBACK_PROPOSAL_START_FAILED")
	}
	if proposalTag.RowsAffected() != 1 {
		return domain.WritebackExecution{}, foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_PROPOSAL_STATE_CONFLICT", false, errors.New("proposal is no longer approved"))
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.WritebackExecution{}, classifyWriteback(err, "WRITEBACK_COMMIT_FAILED")
	}
	return persisted, nil
}

// GetWritebackExecution 返回 Durable Writeback Execution 的当前持久化检查点。
func (r *Repository) GetWritebackExecution(ctx context.Context, id foundation.ID) (domain.WritebackExecution, error) {
	execution, err := scanWritebackExecution(r.db.QueryRow(ctx, `SELECT `+writebackColumns+` FROM change_control.writeback_execution WHERE id=$1`, string(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WritebackExecution{}, foundation.NewError(foundation.ErrorNotFound, "WRITEBACK_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.WritebackExecution{}, classifyWriteback(err, "WRITEBACK_QUERY_FAILED")
	}
	return execution, nil
}

// FindWritebackExecutionByKey 按 Workspace + stable key 做 exact lookup。
// Bootstrap 依赖 found=false 决定是否签发新 Credential，因此不得回退到 Proposal、Authorization 或其他索引。
func (r *Repository) FindWritebackExecutionByKey(ctx context.Context, workspaceID foundation.ID, idempotencyKey string) (domain.WritebackExecution, bool, error) {
	parsedWorkspaceID, err := foundation.ParseID(string(workspaceID))
	canonicalKey := strings.TrimSpace(idempotencyKey)
	if err != nil || parsedWorkspaceID != workspaceID || canonicalKey == "" || canonicalKey != idempotencyKey || len(canonicalKey) > 128 {
		return domain.WritebackExecution{}, false, foundation.NewError(foundation.ErrorInvalidInput, "WRITEBACK_LOOKUP_INVALID", false, errors.Join(err, domain.ErrWritebackInvalidInput))
	}
	execution, err := scanWritebackExecution(r.db.QueryRow(ctx, `SELECT `+writebackColumns+` FROM change_control.writeback_execution WHERE workspace_id=$1 AND idempotency_key=$2`, string(workspaceID), idempotencyKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WritebackExecution{}, false, nil
	}
	if err != nil {
		return domain.WritebackExecution{}, false, classifyWriteback(err, "WRITEBACK_LOOKUP_FAILED")
	}
	return execution, true, nil
}

// CheckpointWritebackExecution 原子推进单步执行状态，并同步对应 Proposal 状态。
func (r *Repository) CheckpointWritebackExecution(ctx context.Context, command domain.CheckpointWriteback) (domain.WritebackExecution, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.WritebackExecution{}, classifyWriteback(err, "WRITEBACK_CHECKPOINT_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	lockedProposalID, err := lockWritebackProposalFirst(ctx, tx, command.ExecutionID)
	if err != nil {
		return domain.WritebackExecution{}, err
	}

	current, err := scanWritebackExecution(tx.QueryRow(ctx, `SELECT `+writebackColumns+` FROM change_control.writeback_execution WHERE id=$1 FOR UPDATE`, string(command.ExecutionID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WritebackExecution{}, foundation.NewError(foundation.ErrorNotFound, "WRITEBACK_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.WritebackExecution{}, classifyWriteback(err, "WRITEBACK_QUERY_FAILED")
	}
	if current.ProposalID != lockedProposalID {
		return domain.WritebackExecution{}, foundation.NewError(foundation.ErrorConsistencyViolation, "WRITEBACK_PROPOSAL_BINDING_CONFLICT", false, domain.ErrWritebackIdentityConflict)
	}
	command = normalizeCheckpointWriteback(current, command)
	if err := domain.ValidateWritebackCheckpoint(current, command); err != nil {
		return domain.WritebackExecution{}, writebackDomainError(err)
	}
	if command.ResultHash != "" && !strings.EqualFold(command.ResultHash, current.ResultHash) {
		return domain.WritebackExecution{}, writebackDomainError(domain.ErrWritebackIdentityConflict)
	}

	var databaseNow time.Time
	if err := tx.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&databaseNow); err != nil {
		return domain.WritebackExecution{}, classifyWriteback(err, "WRITEBACK_CLOCK_QUERY_FAILED")
	}
	updated, err := scanWritebackExecution(tx.QueryRow(ctx, `
		UPDATE change_control.writeback_execution
		SET status=$2,failure_code=$3,manual_recovery_required=$4,temporary_ref=$5,backup_ref=$6,
			file_byte_size=$7,file_mode=$8,file_lock_token=$9,file_result_lock_token=$10,file_backup_lock_token=$11,
			base_blob_id=$12,result_blob_id=$13,base_mode=$14,git_commit=$15,parent_git_commit=$16,diff_hash=$17,
			completed_at=$18,updated_at=$19,version=version+1
		WHERE id=$1 AND version=$20 AND status=$21
		RETURNING `+writebackColumns,
		string(current.ID), string(command.Status), nullableString(command.FailureCode), command.ManualRecoveryRequired,
		nullableString(command.TemporaryRef), nullableString(command.BackupRef), nullableInt64(command.FileByteSize), nullableUint32(command.FileMode), nullableString(command.FileLockToken),
		nullableString(command.FileResultLockToken), nullableString(command.FileBackupLockToken), nullableString(command.BaseBlobID), nullableString(command.ResultBlobID), nullableString(command.BaseMode), nullableString(command.GitCommit),
		nullableString(command.ParentGitCommit), nullableString(command.DiffHash), command.CompletedAt, databaseNow.UTC(),
		command.ExpectedVersion, string(current.Status)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WritebackExecution{}, writebackDomainError(domain.ErrWritebackVersionConflict)
	}
	if err != nil {
		return domain.WritebackExecution{}, classifyWriteback(err, "WRITEBACK_CHECKPOINT_FAILED")
	}

	if proposalStatus, ok := proposalStatusForCheckpoint(command.Status); ok {
		if err := updateProposalForWriteback(ctx, tx, current.ProposalID, proposalStatus, databaseNow.UTC()); err != nil {
			return domain.WritebackExecution{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.WritebackExecution{}, classifyWriteback(err, "WRITEBACK_COMMIT_FAILED")
	}
	return updated, nil
}

// PublishWriteback 原子发布 Commit Mapping、verifying 状态和重索引 Outbox。
func (r *Repository) PublishWriteback(ctx context.Context, command domain.PublishWriteback) (domain.PublishWritebackResult, error) {
	command = normalizePublishWriteback(command)
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.PublishWritebackResult{}, classifyWriteback(err, "WRITEBACK_PUBLISH_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	lockedProposalID, err := lockWritebackProposalFirst(ctx, tx, command.ExecutionID)
	if err != nil {
		return domain.PublishWritebackResult{}, err
	}

	current, err := scanWritebackExecution(tx.QueryRow(ctx, `SELECT `+writebackColumns+` FROM change_control.writeback_execution WHERE id=$1 FOR UPDATE`, string(command.ExecutionID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PublishWritebackResult{}, foundation.NewError(foundation.ErrorNotFound, "WRITEBACK_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.PublishWritebackResult{}, classifyWriteback(err, "WRITEBACK_QUERY_FAILED")
	}
	if current.ProposalID != lockedProposalID {
		return domain.PublishWritebackResult{}, foundation.NewError(foundation.ErrorConsistencyViolation, "WRITEBACK_PROPOSAL_BINDING_CONFLICT", false, domain.ErrWritebackIdentityConflict)
	}
	if current.Status == domain.WritebackStatusVerifying {
		result, replayErr := replayPublishedWriteback(ctx, tx, current, command)
		if replayErr != nil {
			return domain.PublishWritebackResult{}, replayErr
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.PublishWritebackResult{}, classifyWriteback(err, "WRITEBACK_COMMIT_FAILED")
		}
		return result, nil
	}
	if err := domain.ValidateWritebackPublish(current, command); err != nil {
		return domain.PublishWritebackResult{}, writebackDomainError(err)
	}

	var databaseNow time.Time
	if err := tx.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&databaseNow); err != nil {
		return domain.PublishWritebackResult{}, classifyWriteback(err, "WRITEBACK_CLOCK_QUERY_FAILED")
	}
	command.Commit.CreatedAt = databaseNow.UTC()
	commit, err := insertOrReplayProposalCommit(ctx, tx, current, command.Commit)
	if err != nil {
		return domain.PublishWritebackResult{}, err
	}
	if err := insertOrReplayWritebackEvent(ctx, tx, current, command.Event, databaseNow.UTC()); err != nil {
		return domain.PublishWritebackResult{}, err
	}

	updated, err := scanWritebackExecution(tx.QueryRow(ctx, `
		UPDATE change_control.writeback_execution
		SET status=$2,updated_at=$3,version=version+1
		WHERE id=$1 AND version=$4 AND status IN ($5,$6)
		RETURNING `+writebackColumns,
		string(current.ID), string(domain.WritebackStatusVerifying), databaseNow.UTC(), command.ExpectedVersion,
		string(domain.WritebackStatusGitCommitted), string(domain.WritebackStatusPublishRecovery)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PublishWritebackResult{}, writebackDomainError(domain.ErrWritebackVersionConflict)
	}
	if err != nil {
		return domain.PublishWritebackResult{}, classifyWriteback(err, "WRITEBACK_PUBLISH_STATE_FAILED")
	}
	if err := updateProposalForWriteback(ctx, tx, current.ProposalID, domain.StatusVerifying, databaseNow.UTC()); err != nil {
		return domain.PublishWritebackResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.PublishWritebackResult{}, classifyWriteback(err, "WRITEBACK_COMMIT_FAILED")
	}
	return domain.PublishWritebackResult{Execution: updated, Commit: commit}, nil
}

const writebackColumns = `id::text,workspace_id::text,workflow_run_id::text,node_run_id::text,proposal_id::text,revision_id::text,approval_id::text,write_authorization_id::text,git_authorization_id::text,target_path,target_mode,base_hash,result_hash,approved_change_hash,approved_git_head,git_commit,parent_git_commit,diff_hash,status,idempotency_key,failure_code,manual_recovery_required,temporary_ref,backup_ref,file_byte_size,file_mode,file_lock_token,file_result_lock_token,file_backup_lock_token,base_blob_id,result_blob_id,base_mode,version,created_at,updated_at,completed_at,cleanup_completed_at`

const writebackInsert = `INSERT INTO change_control.writeback_execution(
	id,workspace_id,workflow_run_id,node_run_id,proposal_id,revision_id,approval_id,write_authorization_id,git_authorization_id,
	target_path,target_mode,base_hash,result_hash,approved_change_hash,approved_git_head,status,idempotency_key,temporary_ref,backup_ref,version,created_at,updated_at
) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)`

const proposalCommitColumns = `id::text,workspace_id::text,writeback_execution_id::text,proposal_id::text,revision_id::text,approval_id::text,git_commit,parent_git_commit,target_path,target_mode,diff_hash,result_hash,created_at`

func scanWritebackExecution(row pgx.Row) (domain.WritebackExecution, error) {
	var execution domain.WritebackExecution
	var id, workspaceID, runID, nodeID, proposalID, revisionID, approvalID, writeAuthorizationID, gitAuthorizationID string
	var status string
	var gitCommit, parentGitCommit, diffHash, failureCode, temporaryRef, backupRef, fileLockToken, fileResultLockToken, fileBackupLockToken, baseBlobID, resultBlobID, baseMode *string
	var fileByteSize *int64
	var fileMode *int32
	err := row.Scan(
		&id, &workspaceID, &runID, &nodeID, &proposalID, &revisionID, &approvalID, &writeAuthorizationID, &gitAuthorizationID,
		&execution.TargetPath, &execution.TargetMode, &execution.BaseHash, &execution.ResultHash, &execution.ApprovedChangeHash, &execution.ApprovedGitHead,
		&gitCommit, &parentGitCommit, &diffHash, &status, &execution.IdempotencyKey, &failureCode, &execution.ManualRecoveryRequired,
		&temporaryRef, &backupRef, &fileByteSize, &fileMode, &fileLockToken, &fileResultLockToken, &fileBackupLockToken, &baseBlobID, &resultBlobID, &baseMode,
		&execution.Version, &execution.CreatedAt, &execution.UpdatedAt, &execution.CompletedAt, &execution.CleanupCompletedAt,
	)
	if err != nil {
		return domain.WritebackExecution{}, err
	}
	execution.ID, execution.WorkspaceID, execution.WorkflowRunID, execution.NodeRunID = foundation.ID(id), foundation.ID(workspaceID), foundation.ID(runID), foundation.ID(nodeID)
	execution.ProposalID, execution.RevisionID, execution.ApprovalID = foundation.ID(proposalID), foundation.ID(revisionID), foundation.ID(approvalID)
	execution.WriteAuthorizationID, execution.GitAuthorizationID = foundation.ID(writeAuthorizationID), foundation.ID(gitAuthorizationID)
	execution.Status = domain.WritebackStatus(status)
	execution.TargetMode = domain.NormalizeTargetMode(execution.TargetMode)
	execution.GitCommit, execution.ParentGitCommit, execution.DiffHash = stringValue(gitCommit), stringValue(parentGitCommit), stringValue(diffHash)
	execution.FailureCode, execution.TemporaryRef, execution.BackupRef = stringValue(failureCode), stringValue(temporaryRef), stringValue(backupRef)
	execution.FileLockToken = stringValue(fileLockToken)
	execution.FileResultLockToken, execution.FileBackupLockToken = stringValue(fileResultLockToken), stringValue(fileBackupLockToken)
	execution.BaseBlobID, execution.ResultBlobID, execution.BaseMode = stringValue(baseBlobID), stringValue(resultBlobID), stringValue(baseMode)
	if fileByteSize != nil {
		execution.FileByteSize = *fileByteSize
	}
	if fileMode != nil {
		execution.FileMode = uint32(*fileMode)
	}
	return execution, nil
}

func scanProposalCommit(row pgx.Row) (domain.ProposalCommit, error) {
	var commit domain.ProposalCommit
	var id, workspaceID, executionID, proposalID, revisionID, approvalID string
	if err := row.Scan(&id, &workspaceID, &executionID, &proposalID, &revisionID, &approvalID, &commit.GitCommit, &commit.ParentGitCommit, &commit.TargetPath, &commit.TargetMode, &commit.DiffHash, &commit.ResultHash, &commit.CreatedAt); err != nil {
		return domain.ProposalCommit{}, err
	}
	commit.ID, commit.WorkspaceID, commit.WritebackExecutionID = foundation.ID(id), foundation.ID(workspaceID), foundation.ID(executionID)
	commit.ProposalID, commit.RevisionID, commit.ApprovalID = foundation.ID(proposalID), foundation.ID(revisionID), foundation.ID(approvalID)
	commit.TargetMode = domain.NormalizeTargetMode(commit.TargetMode)
	return commit, nil
}

func findWritebackConflict(ctx context.Context, tx pgx.Tx, requested domain.WritebackExecution) (domain.WritebackExecution, error) {
	existing, err := scanWritebackExecution(tx.QueryRow(ctx, `SELECT `+writebackColumns+` FROM change_control.writeback_execution WHERE workspace_id=$1 AND idempotency_key=$2 FOR UPDATE`, string(requested.WorkspaceID), requested.IdempotencyKey))
	if errors.Is(err, pgx.ErrNoRows) {
		existing, err = scanWritebackExecution(tx.QueryRow(ctx, `SELECT `+writebackColumns+` FROM change_control.writeback_execution WHERE proposal_id=$1 AND revision_id=$2 FOR UPDATE`, string(requested.ProposalID), string(requested.RevisionID)))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		existing, err = scanWritebackExecution(tx.QueryRow(ctx, `SELECT `+writebackColumns+` FROM change_control.writeback_execution WHERE write_authorization_id=$1 OR git_authorization_id=$2 FOR UPDATE`, string(requested.WriteAuthorizationID), string(requested.GitAuthorizationID)))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WritebackExecution{}, foundation.NewError(foundation.ErrorConsistencyViolation, "WRITEBACK_CONFLICT_RECORD_MISSING", false, err)
	}
	if err != nil {
		return domain.WritebackExecution{}, classifyWriteback(err, "WRITEBACK_IDEMPOTENCY_QUERY_FAILED")
	}
	return existing, nil
}

func lookupWritebackExecution(ctx context.Context, tx pgx.Tx, command domain.CreateWriteback) (domain.WritebackExecution, bool, error) {
	existing, err := scanWritebackExecution(tx.QueryRow(ctx, `SELECT `+writebackColumns+` FROM change_control.writeback_execution WHERE workspace_id=$1 AND idempotency_key=$2 FOR UPDATE`, string(command.WorkspaceID), command.IdempotencyKey))
	if errors.Is(err, pgx.ErrNoRows) {
		existing, err = scanWritebackExecution(tx.QueryRow(ctx, `SELECT `+writebackColumns+` FROM change_control.writeback_execution WHERE proposal_id=$1 AND revision_id=$2 FOR UPDATE`, string(command.ProposalID), string(command.RevisionID)))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		existing, err = scanWritebackExecution(tx.QueryRow(ctx, `SELECT `+writebackColumns+` FROM change_control.writeback_execution WHERE write_authorization_id=$1 OR git_authorization_id=$2 FOR UPDATE`, string(command.WriteAuthorizationID), string(command.GitAuthorizationID)))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WritebackExecution{}, false, nil
	}
	if err != nil {
		return domain.WritebackExecution{}, false, classifyWriteback(err, "WRITEBACK_IDEMPOTENCY_QUERY_FAILED")
	}
	return existing, true, nil
}

func insertOrReplayProposalCommit(ctx context.Context, tx pgx.Tx, execution domain.WritebackExecution, requested domain.ProposalCommit) (domain.ProposalCommit, error) {
	commit, err := scanProposalCommit(tx.QueryRow(ctx, `
		INSERT INTO change_control.proposal_commit(id,workspace_id,writeback_execution_id,proposal_id,revision_id,approval_id,git_commit,parent_git_commit,target_path,target_mode,diff_hash,result_hash,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT DO NOTHING RETURNING `+proposalCommitColumns,
		string(requested.ID), string(requested.WorkspaceID), string(requested.WritebackExecutionID), string(requested.ProposalID),
		string(requested.RevisionID), string(requested.ApprovalID), requested.GitCommit, requested.ParentGitCommit,
		requested.TargetPath, string(domain.NormalizeTargetMode(requested.TargetMode)), requested.DiffHash, requested.ResultHash, requested.CreatedAt.UTC()))
	if errors.Is(err, pgx.ErrNoRows) {
		commit, err = scanProposalCommit(tx.QueryRow(ctx, `SELECT `+proposalCommitColumns+` FROM change_control.proposal_commit WHERE writeback_execution_id=$1 OR (proposal_id=$2 AND revision_id=$3) OR (workspace_id=$4 AND git_commit=$5) FOR UPDATE`,
			string(execution.ID), string(execution.ProposalID), string(execution.RevisionID), string(execution.WorkspaceID), execution.GitCommit))
	}
	if err != nil {
		return domain.ProposalCommit{}, classifyWriteback(err, "WRITEBACK_COMMIT_MAPPING_FAILED")
	}
	if err := domain.ValidateProposalCommitBinding(execution, commit); err != nil || !sameProposalCommitIdentity(commit, requested) {
		return domain.ProposalCommit{}, writebackDomainError(domain.ErrWritebackPublishBindingConflict)
	}
	return commit, nil
}

func insertOrReplayWritebackEvent(ctx context.Context, tx pgx.Tx, execution domain.WritebackExecution, requested domain.WritebackOutboxEvent, occurredAt time.Time) error {
	var insertedID string
	err := tx.QueryRow(ctx, `
		INSERT INTO workflow.outbox_event(id,workspace_id,run_id,event_type,idempotency_key,payload,occurred_at)
		VALUES($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT DO NOTHING RETURNING id::text`,
		string(requested.ID), string(requested.WorkspaceID), string(requested.RunID), requested.Type,
		requested.IdempotencyKey, requested.Payload, occurredAt.UTC()).Scan(&insertedID)
	if errors.Is(err, pgx.ErrNoRows) {
		var id, workspaceID, runID, eventType, idempotencyKey string
		var payload []byte
		queryErr := tx.QueryRow(ctx, `SELECT id::text,workspace_id::text,run_id::text,event_type,idempotency_key,payload FROM workflow.outbox_event WHERE id=$1 OR idempotency_key=$2 FOR UPDATE`, string(requested.ID), requested.IdempotencyKey).Scan(&id, &workspaceID, &runID, &eventType, &idempotencyKey, &payload)
		if queryErr != nil {
			return classifyWriteback(queryErr, "WRITEBACK_OUTBOX_QUERY_FAILED")
		}
		if foundation.ID(id) != requested.ID || foundation.ID(workspaceID) != execution.WorkspaceID || foundation.ID(runID) != execution.WorkflowRunID || eventType != requested.Type || idempotencyKey != requested.IdempotencyKey || !sameJSON(payload, requested.Payload) {
			return writebackDomainError(domain.ErrWritebackPublishBindingConflict)
		}
		return nil
	}
	if err != nil {
		return classifyWriteback(err, "WRITEBACK_OUTBOX_CREATE_FAILED")
	}
	return nil
}

func replayPublishedWriteback(ctx context.Context, tx pgx.Tx, execution domain.WritebackExecution, command domain.PublishWriteback) (domain.PublishWritebackResult, error) {
	validation := command
	validation.ExpectedVersion = execution.Version
	if err := domain.ValidateWritebackPublish(execution, validation); err != nil {
		return domain.PublishWritebackResult{}, writebackDomainError(err)
	}
	commit, err := scanProposalCommit(tx.QueryRow(ctx, `SELECT `+proposalCommitColumns+` FROM change_control.proposal_commit WHERE writeback_execution_id=$1 FOR UPDATE`, string(execution.ID)))
	if err != nil {
		return domain.PublishWritebackResult{}, classifyWriteback(err, "WRITEBACK_COMMIT_MAPPING_QUERY_FAILED")
	}
	if err := domain.ValidateProposalCommitBinding(execution, commit); err != nil || !sameProposalCommitIdentity(commit, command.Commit) {
		return domain.PublishWritebackResult{}, writebackDomainError(domain.ErrWritebackPublishBindingConflict)
	}
	if err := validatePublishedWritebackEvent(ctx, tx, execution, command.Event); err != nil {
		return domain.PublishWritebackResult{}, err
	}
	return domain.PublishWritebackResult{Execution: execution, Commit: commit, Replayed: true}, nil
}

func validatePublishedWritebackEvent(ctx context.Context, tx pgx.Tx, execution domain.WritebackExecution, requested domain.WritebackOutboxEvent) error {
	var id, workspaceID, runID, eventType, idempotencyKey string
	var payload []byte
	err := tx.QueryRow(ctx, `SELECT id::text,workspace_id::text,run_id::text,event_type,idempotency_key,payload FROM workflow.outbox_event WHERE id=$1 AND idempotency_key=$2 FOR UPDATE`, string(requested.ID), requested.IdempotencyKey).Scan(&id, &workspaceID, &runID, &eventType, &idempotencyKey, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "WRITEBACK_OUTBOX_MISSING", false, err)
	}
	if err != nil {
		return classifyWriteback(err, "WRITEBACK_OUTBOX_QUERY_FAILED")
	}
	if foundation.ID(id) != requested.ID || foundation.ID(workspaceID) != execution.WorkspaceID || foundation.ID(runID) != execution.WorkflowRunID || eventType != requested.Type || idempotencyKey != requested.IdempotencyKey || !sameJSON(payload, requested.Payload) {
		return writebackDomainError(domain.ErrWritebackPublishBindingConflict)
	}
	return nil
}

func updateProposalForWriteback(ctx context.Context, tx pgx.Tx, proposalID foundation.ID, target domain.ProposalStatus, at time.Time) error {
	expected := domain.StatusApplying
	if target == domain.StatusVerifying {
		expected = domain.StatusApplied
	}
	tag, err := tx.Exec(ctx, `UPDATE change_control.proposal SET status=$2,updated_at=$3,version=version+1 WHERE id=$1 AND status=$4`, string(proposalID), string(target), at.UTC(), string(expected))
	if err != nil {
		return classifyWriteback(err, "WRITEBACK_PROPOSAL_STATE_FAILED")
	}
	if tag.RowsAffected() != 1 {
		return foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_PROPOSAL_STATE_CONFLICT", false, errors.New("proposal state does not match writeback checkpoint"))
	}
	return nil
}

// lockWritebackProposalFirst 统一需要同时更新 Proposal/Execution 的事务锁序。
// Execution 的 proposal_id 由数据库触发器保证不可变，因此可先无锁读取身份，
// 再锁 Proposal，最后由调用方锁 Execution，避免与 legacy Create trigger 反向死锁。
func lockWritebackProposalFirst(ctx context.Context, tx pgx.Tx, executionID foundation.ID) (foundation.ID, error) {
	var proposalID string
	if err := tx.QueryRow(ctx, `SELECT proposal_id::text FROM change_control.writeback_execution WHERE id=$1`, string(executionID)).Scan(&proposalID); errors.Is(err, pgx.ErrNoRows) {
		return "", foundation.NewError(foundation.ErrorNotFound, "WRITEBACK_NOT_FOUND", false, err)
	} else if err != nil {
		return "", classifyWriteback(err, "WRITEBACK_QUERY_FAILED")
	}
	var lockedID string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM change_control.proposal WHERE id=$1 FOR UPDATE`, proposalID).Scan(&lockedID); errors.Is(err, pgx.ErrNoRows) {
		return "", foundation.NewError(foundation.ErrorConsistencyViolation, "WRITEBACK_PROPOSAL_BINDING_CONFLICT", false, err)
	} else if err != nil {
		return "", classifyWriteback(err, "WRITEBACK_PROPOSAL_LOCK_FAILED")
	}
	return foundation.ID(lockedID), nil
}

func proposalStatusForCheckpoint(status domain.WritebackStatus) (domain.ProposalStatus, bool) {
	switch status {
	case domain.WritebackStatusGitCommitted:
		return domain.StatusApplied, true
	case domain.WritebackStatusNeedsRevision:
		return domain.StatusNeedsRevision, true
	case domain.WritebackStatusApplyFailed:
		return domain.StatusApplyFailed, true
	default:
		return "", false
	}
}

func executionFromCreate(command domain.CreateWriteback, databaseNow time.Time) domain.WritebackExecution {
	return domain.WritebackExecution{
		ID: command.ID, WorkspaceID: command.WorkspaceID, WorkflowRunID: command.WorkflowRunID, NodeRunID: command.NodeRunID,
		ProposalID: command.ProposalID, RevisionID: command.RevisionID, ApprovalID: command.ApprovalID,
		WriteAuthorizationID: command.WriteAuthorizationID, GitAuthorizationID: command.GitAuthorizationID,
		TargetPath: command.TargetPath, TargetMode: domain.NormalizeTargetMode(command.TargetMode), BaseHash: command.BaseHash, ResultHash: command.ResultHash,
		ApprovedChangeHash: command.ApprovedChangeHash, ApprovedGitHead: command.ApprovedGitHead,
		Status: domain.WritebackStatusPrepared, IdempotencyKey: command.IdempotencyKey,
		TemporaryRef: command.TemporaryRef, BackupRef: command.BackupRef, Version: 1,
		CreatedAt: databaseNow, UpdatedAt: databaseNow,
	}
}

func normalizeCreateWriteback(command domain.CreateWriteback) domain.CreateWriteback {
	command.BaseHash = strings.ToLower(command.BaseHash)
	command.ResultHash = strings.ToLower(command.ResultHash)
	command.ApprovedChangeHash = strings.ToLower(command.ApprovedChangeHash)
	command.ApprovedGitHead = strings.ToLower(command.ApprovedGitHead)
	command.IdempotencyKey = strings.TrimSpace(command.IdempotencyKey)
	return command
}

func normalizeCheckpointWriteback(current domain.WritebackExecution, command domain.CheckpointWriteback) domain.CheckpointWriteback {
	command.ResultHash = strings.ToLower(command.ResultHash)
	command.FileLockToken = strings.ToLower(command.FileLockToken)
	command.FileResultLockToken = strings.ToLower(command.FileResultLockToken)
	command.FileBackupLockToken = strings.ToLower(command.FileBackupLockToken)
	command.BaseBlobID = strings.ToLower(command.BaseBlobID)
	command.ResultBlobID = strings.ToLower(command.ResultBlobID)
	command.GitCommit = strings.ToLower(command.GitCommit)
	command.ParentGitCommit = strings.ToLower(command.ParentGitCommit)
	command.DiffHash = strings.ToLower(command.DiffHash)
	if command.ResultHash == "" {
		command.ResultHash = current.ResultHash
	}
	if command.TemporaryRef == "" {
		command.TemporaryRef = current.TemporaryRef
	}
	if command.BackupRef == "" {
		command.BackupRef = current.BackupRef
	}
	if command.FileByteSize == 0 {
		command.FileByteSize = current.FileByteSize
	}
	if command.FileMode == 0 {
		command.FileMode = current.FileMode
	}
	if command.FileLockToken == "" {
		command.FileLockToken = current.FileLockToken
	}
	if command.FileResultLockToken == "" {
		command.FileResultLockToken = current.FileResultLockToken
	}
	if command.FileBackupLockToken == "" {
		command.FileBackupLockToken = current.FileBackupLockToken
	}
	if command.BaseBlobID == "" {
		command.BaseBlobID = current.BaseBlobID
	}
	if command.ResultBlobID == "" {
		command.ResultBlobID = current.ResultBlobID
	}
	if command.BaseMode == "" {
		command.BaseMode = current.BaseMode
	}
	if command.GitCommit == "" {
		command.GitCommit = current.GitCommit
	}
	if command.ParentGitCommit == "" {
		command.ParentGitCommit = current.ParentGitCommit
	}
	if command.DiffHash == "" {
		command.DiffHash = current.DiffHash
	}
	if command.FailureCode == "" {
		command.FailureCode = current.FailureCode
	}
	command.ManualRecoveryRequired = command.ManualRecoveryRequired || current.ManualRecoveryRequired
	if command.CompletedAt == nil {
		command.CompletedAt = current.CompletedAt
	}
	return command
}

func normalizePublishWriteback(command domain.PublishWriteback) domain.PublishWriteback {
	command.Commit.GitCommit = strings.ToLower(command.Commit.GitCommit)
	command.Commit.ParentGitCommit = strings.ToLower(command.Commit.ParentGitCommit)
	command.Commit.DiffHash = strings.ToLower(command.Commit.DiffHash)
	command.Commit.ResultHash = strings.ToLower(command.Commit.ResultHash)
	command.Event.IdempotencyKey = strings.TrimSpace(command.Event.IdempotencyKey)
	return command
}

func sameProposalCommitIdentity(left, right domain.ProposalCommit) bool {
	return left.ID == right.ID && left.WorkspaceID == right.WorkspaceID && left.WritebackExecutionID == right.WritebackExecutionID &&
		left.ProposalID == right.ProposalID && left.RevisionID == right.RevisionID && left.ApprovalID == right.ApprovalID &&
		strings.EqualFold(left.GitCommit, right.GitCommit) && strings.EqualFold(left.ParentGitCommit, right.ParentGitCommit) &&
		left.TargetPath == right.TargetPath && domain.NormalizeTargetMode(left.TargetMode) == domain.NormalizeTargetMode(right.TargetMode) && strings.EqualFold(left.DiffHash, right.DiffHash) && strings.EqualFold(left.ResultHash, right.ResultHash)
}

func sameJSON(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return bytes.Equal(bytes.TrimSpace(left), bytes.TrimSpace(right))
	}
	leftJSON, leftErr := json.Marshal(leftValue)
	rightJSON, rightErr := json.Marshal(rightValue)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableInt64(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullableUint32(value uint32) any {
	if value == 0 {
		return nil
	}
	return int32(value)
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func writebackDomainError(err error) error {
	switch {
	case errors.Is(err, domain.ErrWritebackInvalidInput):
		return foundation.NewError(foundation.ErrorInvalidInput, "WRITEBACK_INVALID", false, err)
	case errors.Is(err, domain.ErrWritebackIdentityConflict):
		return foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_IDENTITY_CONFLICT", false, err)
	case errors.Is(err, domain.ErrWritebackVersionConflict):
		return foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_VERSION_CONFLICT", false, err)
	case errors.Is(err, domain.ErrWritebackInvalidTransition):
		return foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_STATUS_CONFLICT", false, err)
	case errors.Is(err, domain.ErrWritebackPublishBindingConflict):
		return foundation.NewError(foundation.ErrorConsistencyViolation, "WRITEBACK_PUBLISH_BINDING_CONFLICT", false, err)
	case errors.Is(err, domain.ErrWritebackLeaseLost):
		return foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_LEASE_LOST", true, err)
	default:
		return foundation.NewError(foundation.ErrorNonRetryableFailure, "WRITEBACK_FAILED", false, err)
	}
}

func classifyWriteback(err error, code string) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_IDENTITY_CONFLICT", false, err)
		case "23503", "23514", "55000":
			return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, err)
		case "40001", "40P01", "57P01", "08000", "08003", "08006":
			return foundation.NewError(foundation.ErrorRetryableFailure, code, true, err)
		}
	}
	return foundation.NewError(foundation.ErrorNonRetryableFailure, code, false, err)
}
