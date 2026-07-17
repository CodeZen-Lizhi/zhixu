// Package postgres persists Change Control aggregates in PostgreSQL.
package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DB 是 Repository 所需的最小 pgx 边界。
type DB interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Begin(context.Context) (pgx.Tx, error)
}

// Repository 是 Change Control 的 PostgreSQL 实现。
type Repository struct{ db DB }

var _ domain.Repository = (*Repository)(nil)
var _ domain.AuthorizationRepository = (*Repository)(nil)

// NewRepository 创建 PostgreSQL Repository。
func NewRepository(db DB) (*Repository, error) {
	if db == nil {
		return nil, foundation.NewError(foundation.ErrorDependencyUnavailable, "CHANGE_CONTROL_DATABASE_UNAVAILABLE", true, errors.New("database is nil"))
	}
	return &Repository{db: db}, nil
}

// CreateProposal 在同一事务中创建 Proposal 和不可变 Revision。
func (r *Repository) CreateProposal(ctx context.Context, proposal domain.Proposal) (domain.Proposal, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var insertedID string
	err = tx.QueryRow(ctx, `
		INSERT INTO change_control.proposal(id,workspace_id,idempotency_key,request_hash,status,version,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT(workspace_id,idempotency_key) DO NOTHING
		RETURNING id::text`,
		string(proposal.ID), string(proposal.WorkspaceID), proposal.IdempotencyKey, proposal.RequestHash,
		string(proposal.Status), proposal.Version, proposal.CreatedAt.UTC(), proposal.UpdatedAt.UTC()).Scan(&insertedID)
	if errors.Is(err, pgx.ErrNoRows) {
		var existingID, requestHash string
		if queryErr := tx.QueryRow(ctx, `SELECT id::text,request_hash FROM change_control.proposal WHERE workspace_id=$1 AND idempotency_key=$2`, string(proposal.WorkspaceID), proposal.IdempotencyKey).Scan(&existingID, &requestHash); queryErr != nil {
			return domain.Proposal{}, classify(queryErr, "PROPOSAL_IDEMPOTENCY_QUERY_FAILED")
		}
		if requestHash != proposal.RequestHash {
			return domain.Proposal{}, foundation.NewError(foundation.ErrorVersionConflict, "IDEMPOTENCY_KEY_REUSED", false, errors.New("idempotency key is bound to another proposal request"))
		}
		_ = tx.Rollback(ctx)
		return r.GetProposal(ctx, foundation.ID(existingID))
	}
	if err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_CREATE_FAILED")
	}
	revision := proposal.Revision
	if _, err := tx.Exec(ctx, `
		INSERT INTO change_control.proposal_revision(
			id,proposal_id,revision_no,target_path,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,created_at
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		string(revision.ID), string(proposal.ID), revision.RevisionNo, revision.TargetPath,
		revision.BaseHash, revision.Content, revision.EvidenceSummary, revision.Risk,
		revision.RollbackPlan, revision.ChangeHash, revision.CreatedAt.UTC()); err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_REVISION_CREATE_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_COMMIT_FAILED")
	}
	return proposal, nil
}

// GetProposal 使用单条查询返回一致的 Proposal、Revision 和 Approval 快照。
func (r *Repository) GetProposal(ctx context.Context, proposalID foundation.ID) (domain.Proposal, error) {
	row := r.db.QueryRow(ctx, `
		SELECT p.id::text,p.workspace_id::text,p.idempotency_key,p.request_hash,p.workflow_run_id::text,p.status,p.version,p.created_at,p.updated_at,
			r.id::text,r.revision_no,r.target_path,r.base_hash,r.content,r.evidence_summary,r.risk,r.rollback_plan,r.change_hash,r.created_at,
			a.id::text,a.change_hash,a.decision,a.approved_git_head,a.decided_at
		FROM change_control.proposal p
		JOIN LATERAL (
			SELECT id,revision_no,target_path,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,created_at
			FROM change_control.proposal_revision
			WHERE proposal_id=p.id ORDER BY revision_no DESC LIMIT 1
		) r ON true
		LEFT JOIN change_control.approval a ON a.revision_id=r.id
		WHERE p.id=$1`, string(proposalID))
	proposal, err := scanProposal(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Proposal{}, foundation.NewError(foundation.ErrorNotFound, "PROPOSAL_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.Proposal{}, classify(err, "PROPOSAL_QUERY_FAILED")
	}
	return proposal, nil
}

// Approve 只允许 ready_for_review Proposal 被一次性批准或拒绝。
func (r *Repository) Approve(ctx context.Context, approval domain.Approval) (domain.Approval, error) {
	if approval.ApprovedGitHead != nil {
		normalized := strings.ToLower(strings.TrimSpace(*approval.ApprovedGitHead))
		approval.ApprovedGitHead = &normalized
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.Approval{}, classify(err, "APPROVAL_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status, revisionHash string
	var proposalVersion int64
	err = tx.QueryRow(ctx, `
		SELECT p.status,p.version,r.change_hash
		FROM change_control.proposal p
		JOIN change_control.proposal_revision r ON r.proposal_id=p.id AND r.id=$2
		WHERE p.id=$1 FOR UPDATE`, string(approval.ProposalID), string(approval.RevisionID)).Scan(&status, &proposalVersion, &revisionHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Approval{}, foundation.NewError(foundation.ErrorNotFound, "PROPOSAL_REVISION_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.Approval{}, classify(err, "APPROVAL_QUERY_FAILED")
	}
	if status != string(domain.StatusReady) {
		existing, existingErr := getApproval(ctx, tx, approval.RevisionID)
		if existingErr == nil && existing.ProposalID == approval.ProposalID && existing.ChangeHash == approval.ChangeHash && existing.Decision == approval.Decision && sameOptionalString(existing.ApprovedGitHead, approval.ApprovedGitHead) {
			return existing, nil
		}
		if existingErr != nil && !errors.Is(existingErr, pgx.ErrNoRows) {
			return domain.Approval{}, classify(existingErr, "APPROVAL_QUERY_FAILED")
		}
		return domain.Approval{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_NOT_READY_FOR_REVIEW", false, errors.New("proposal is not ready for review"))
	}
	if revisionHash != approval.ChangeHash {
		return domain.Approval{}, foundation.NewError(foundation.ErrorConsistencyViolation, "CHANGE_HASH_MISMATCH", false, errors.New("change hash mismatch"))
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO change_control.approval(id,proposal_id,revision_id,change_hash,decision,approved_git_head,decided_at)
		VALUES($1,$2,$3,$4,$5,$6,$7)`, string(approval.ID), string(approval.ProposalID), string(approval.RevisionID), approval.ChangeHash, string(approval.Decision), approval.ApprovedGitHead, approval.DecidedAt.UTC()); err != nil {
		return domain.Approval{}, classify(err, "APPROVAL_CREATE_FAILED")
	}
	newStatus := domain.StatusRejected
	if approval.Decision == domain.DecisionApproved {
		newStatus = domain.StatusApproved
	}
	command, err := tx.Exec(ctx, `
		UPDATE change_control.proposal SET status=$1,updated_at=$2,version=version+1
		WHERE id=$3 AND status=$4 AND version=$5`, string(newStatus), approval.DecidedAt.UTC(), string(approval.ProposalID), string(domain.StatusReady), proposalVersion)
	if err != nil {
		return domain.Approval{}, classify(err, "PROPOSAL_DECISION_UPDATE_FAILED")
	}
	if command.RowsAffected() != 1 {
		return domain.Approval{}, foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_NOT_READY_FOR_REVIEW", false, errors.New("proposal state changed"))
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.Approval{}, classify(err, "APPROVAL_COMMIT_FAILED")
	}
	return approval, nil
}

// MarkNeedsRevision 记录服务端观察到的目标基线冲突。
func (r *Repository) MarkNeedsRevision(ctx context.Context, proposalID foundation.ID, at time.Time) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return classify(err, "PROPOSAL_STATE_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	command, err := tx.Exec(ctx, `
		UPDATE change_control.proposal SET status=$1,updated_at=$2,version=version+1
		WHERE id=$3 AND status IN ($4,$5)`, string(domain.StatusNeedsRevision), at.UTC(), string(proposalID), string(domain.StatusApproved), string(domain.StatusReady))
	if err != nil {
		return classify(err, "PROPOSAL_STATE_UPDATE_FAILED")
	}
	if command.RowsAffected() != 1 {
		return foundation.NewError(foundation.ErrorVersionConflict, "PROPOSAL_STATE_CONFLICT", false, errors.New("proposal is no longer approved"))
	}
	return tx.Commit(ctx)
}

// ValidateWorkflowContext 确认 Run/Node 已持久化且属于同一 Workspace。
func (r *Repository) ValidateWorkflowContext(ctx context.Context, workspaceID, runID, nodeID foundation.ID) error {
	var one int
	err := r.db.QueryRow(ctx, `
		SELECT 1
		FROM workflow.run r
		JOIN workflow.node_run n ON n.run_id=r.id
		WHERE r.id=$1 AND n.id=$2 AND r.workspace_id=$3
		  AND r.status IN ('pending','running','waiting_for_human')
		  AND n.status IN ('pending','running','waiting_for_human')`, string(runID), string(nodeID), string(workspaceID)).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "WRITE_AUTHORIZATION_WORKFLOW_CONTEXT_INVALID", false, err)
	}
	if err != nil {
		return classify(err, "WRITE_AUTHORIZATION_WORKFLOW_CONTEXT_QUERY_FAILED")
	}
	return nil
}

// GetAuthorization loads authorization metadata without exposing the raw credential.
func (r *Repository) GetAuthorization(ctx context.Context, workspaceID foundation.ID, idempotencyKey, tokenHash string) (domain.ToolAuthorization, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.ToolAuthorization{}, classifyAuthorization(err, "WRITE_AUTHORIZATION_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var databaseNow time.Time
	if err := tx.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&databaseNow); err != nil {
		return domain.ToolAuthorization{}, classifyAuthorization(err, "WRITE_AUTHORIZATION_CLOCK_QUERY_FAILED")
	}
	authorization, err := scanAuthorization(tx.QueryRow(ctx, `SELECT `+authorizationColumns+` FROM change_control.tool_authorization WHERE workspace_id=$1 AND idempotency_key=$2 FOR UPDATE`, string(workspaceID), idempotencyKey))
	if errors.Is(err, pgx.ErrNoRows) {
		authorization, err = scanAuthorization(tx.QueryRow(ctx, `SELECT `+authorizationColumns+` FROM change_control.tool_authorization WHERE token_hash=$1 FOR UPDATE`, tokenHash))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ToolAuthorization{}, foundation.NewError(foundation.ErrorPermissionDenied, "WRITE_AUTHORIZATION_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.ToolAuthorization{}, classifyAuthorization(err, "WRITE_AUTHORIZATION_QUERY_FAILED")
	}
	if authorization.Status == domain.AuthorizationIssued && !databaseNow.Before(authorization.ExpiresAt) {
		expired, updateErr := scanAuthorization(tx.QueryRow(ctx, `UPDATE change_control.tool_authorization SET status='expired',version=version+1 WHERE id=$1 AND status='issued' RETURNING `+authorizationColumns, string(authorization.ID)))
		if updateErr != nil {
			return domain.ToolAuthorization{}, classifyAuthorization(updateErr, "WRITE_AUTHORIZATION_EXPIRE_FAILED")
		}
		authorization = expired
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ToolAuthorization{}, classifyAuthorization(err, "WRITE_AUTHORIZATION_COMMIT_FAILED")
	}
	return authorization, nil
}

// CreateAuthorization 保存授权绑定；同一 Workspace/幂等键只返回原记录，不重新生成凭据。
func (r *Repository) CreateAuthorization(ctx context.Context, authorization domain.ToolAuthorization) (domain.AuthorizationIssueResult, error) {
	requested := authorization
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.AuthorizationIssueResult{}, classify(err, "WRITE_AUTHORIZATION_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var databaseNow time.Time
	if err := tx.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&databaseNow); err != nil {
		return domain.AuthorizationIssueResult{}, classifyAuthorization(err, "WRITE_AUTHORIZATION_CLOCK_QUERY_FAILED")
	}
	requestedTTL := authorization.ExpiresAt.Sub(authorization.IssuedAt)
	authorization.IssuedAt = databaseNow.UTC()
	authorization.ExpiresAt = databaseNow.UTC().Add(requestedTTL)
	authorization, err = scanAuthorization(tx.QueryRow(ctx, authorizationInsert+` RETURNING `+authorizationColumns,
		string(authorization.ID), string(authorization.WorkspaceID), string(authorization.WorkflowRunID), string(authorization.NodeRunID),
		string(authorization.ProposalID), string(authorization.RevisionID), string(authorization.ApprovalID), authorization.ToolName,
		string(authorization.Capability), authorization.Scope, authorization.ApprovedChangeHash, authorization.TargetVersion,
		authorization.TokenHash, authorization.IdempotencyKey, string(authorization.Status), authorization.IssuedAt.UTC(), authorization.ExpiresAt.UTC(),
		authorizationTimePointer(authorization.RevokedAt), authorizationTimePointer(authorization.ConsumedAt), authorization.Version))
	if errors.Is(err, pgx.ErrNoRows) {
		existing, queryErr := scanAuthorization(tx.QueryRow(ctx, `SELECT `+authorizationColumns+` FROM change_control.tool_authorization WHERE workspace_id=$1 AND idempotency_key=$2 FOR UPDATE`, string(requested.WorkspaceID), requested.IdempotencyKey))
		if queryErr != nil {
			return domain.AuthorizationIssueResult{}, classifyAuthorization(queryErr, "WRITE_AUTHORIZATION_IDEMPOTENCY_QUERY_FAILED")
		}
		if !sameAuthorizationIdentity(existing, requested) {
			return domain.AuthorizationIssueResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WRITE_AUTHORIZATION_IDEMPOTENCY_CONFLICT", false, errors.New("authorization idempotency key is bound to another request"))
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.AuthorizationIssueResult{}, classifyAuthorization(err, "WRITE_AUTHORIZATION_COMMIT_FAILED")
		}
		return domain.AuthorizationIssueResult{Authorization: existing, Replayed: true}, nil
	}
	if err != nil {
		return domain.AuthorizationIssueResult{}, classifyAuthorization(err, "WRITE_AUTHORIZATION_CREATE_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.AuthorizationIssueResult{}, classifyAuthorization(err, "WRITE_AUTHORIZATION_COMMIT_FAILED")
	}
	return domain.AuthorizationIssueResult{Authorization: authorization}, nil
}

// ConsumeAuthorization 在数据库事务中执行过期、绑定和一次性消费检查。
func (r *Repository) ConsumeAuthorization(ctx context.Context, request domain.AuthorizationConsume) (domain.AuthorizationConsumeResult, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.AuthorizationConsumeResult{}, classifyAuthorization(err, "WRITE_AUTHORIZATION_CONSUME_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var databaseNow time.Time
	if err := tx.QueryRow(ctx, `SELECT CURRENT_TIMESTAMP`).Scan(&databaseNow); err != nil {
		return domain.AuthorizationConsumeResult{}, classifyAuthorization(err, "WRITE_AUTHORIZATION_CLOCK_QUERY_FAILED")
	}
	authorization, err := scanAuthorization(tx.QueryRow(ctx, `SELECT `+authorizationColumns+` FROM change_control.tool_authorization WHERE workspace_id=$1 AND idempotency_key=$2 FOR UPDATE`, string(request.WorkspaceID), request.IdempotencyKey))
	if errors.Is(err, pgx.ErrNoRows) {
		authorization, err = scanAuthorization(tx.QueryRow(ctx, `SELECT `+authorizationColumns+` FROM change_control.tool_authorization WHERE token_hash=$1 FOR UPDATE`, request.Credential))
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.AuthorizationConsumeResult{}, foundation.NewError(foundation.ErrorPermissionDenied, "WRITE_AUTHORIZATION_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.AuthorizationConsumeResult{}, classifyAuthorization(err, "WRITE_AUTHORIZATION_QUERY_FAILED")
	}
	if !sameAuthorizationBinding(authorization, request) {
		return domain.AuthorizationConsumeResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WRITE_AUTHORIZATION_BINDING_CONFLICT", false, errors.New("authorization binding does not match request"))
	}
	if authorization.Status == domain.AuthorizationConsumed {
		if authorization.IdempotencyKey != request.IdempotencyKey {
			return domain.AuthorizationConsumeResult{}, foundation.NewError(foundation.ErrorVersionConflict, "WRITE_AUTHORIZATION_ALREADY_CONSUMED", false, errors.New("authorization was already consumed"))
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.AuthorizationConsumeResult{}, classifyAuthorization(err, "WRITE_AUTHORIZATION_COMMIT_FAILED")
		}
		return domain.AuthorizationConsumeResult{Authorization: authorization, Replayed: true}, nil
	}
	if authorization.Status == domain.AuthorizationRevoked {
		return domain.AuthorizationConsumeResult{}, foundation.NewError(foundation.ErrorPermissionDenied, "WRITE_AUTHORIZATION_REVOKED", false, errors.New("authorization was revoked"))
	}
	if authorization.Status == domain.AuthorizationExpired || !databaseNow.Before(authorization.ExpiresAt) {
		if authorization.Status == domain.AuthorizationIssued {
			if _, updateErr := tx.Exec(ctx, `UPDATE change_control.tool_authorization SET status='expired',version=version+1 WHERE id=$1 AND status='issued'`, string(authorization.ID)); updateErr != nil {
				return domain.AuthorizationConsumeResult{}, classifyAuthorization(updateErr, "WRITE_AUTHORIZATION_EXPIRE_FAILED")
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.AuthorizationConsumeResult{}, classifyAuthorization(err, "WRITE_AUTHORIZATION_COMMIT_FAILED")
		}
		return domain.AuthorizationConsumeResult{}, foundation.NewError(foundation.ErrorPermissionDenied, "WRITE_AUTHORIZATION_EXPIRED", false, errors.New("authorization has expired"))
	}
	if err := verifyCurrentAuthorizationState(ctx, tx, authorization); err != nil {
		return domain.AuthorizationConsumeResult{}, err
	}
	consumed, err := scanAuthorization(tx.QueryRow(ctx, `UPDATE change_control.tool_authorization SET status='consumed',consumed_at=$2,version=version+1 WHERE id=$1 AND status='issued' RETURNING `+authorizationColumns, string(authorization.ID), databaseNow.UTC()))
	if err != nil {
		return domain.AuthorizationConsumeResult{}, classifyAuthorization(err, "WRITE_AUTHORIZATION_CONSUME_FAILED")
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.AuthorizationConsumeResult{}, classifyAuthorization(err, "WRITE_AUTHORIZATION_COMMIT_FAILED")
	}
	return domain.AuthorizationConsumeResult{Authorization: consumed}, nil
}

// RevokeAuthorization 使 issued 授权失效；对已终止状态重复撤销保持幂等。
func (r *Repository) RevokeAuthorization(ctx context.Context, id foundation.ID, at time.Time) error {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return classifyAuthorization(err, "WRITE_AUTHORIZATION_REVOKE_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var status string
	err = tx.QueryRow(ctx, `SELECT status FROM change_control.tool_authorization WHERE id=$1 FOR UPDATE`, string(id)).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return foundation.NewError(foundation.ErrorNotFound, "WRITE_AUTHORIZATION_NOT_FOUND", false, err)
	}
	if err != nil {
		return classifyAuthorization(err, "WRITE_AUTHORIZATION_QUERY_FAILED")
	}
	if status == string(domain.AuthorizationConsumed) {
		return foundation.NewError(foundation.ErrorVersionConflict, "WRITE_AUTHORIZATION_ALREADY_CONSUMED", false, errors.New("consumed authorization cannot be revoked"))
	}
	if status == string(domain.AuthorizationRevoked) || status == string(domain.AuthorizationExpired) {
		return classifyAuthorization(tx.Commit(ctx), "WRITE_AUTHORIZATION_COMMIT_FAILED")
	}
	if _, err := tx.Exec(ctx, `UPDATE change_control.tool_authorization SET status='revoked',revoked_at=CURRENT_TIMESTAMP,version=version+1 WHERE id=$1 AND status='issued'`, string(id)); err != nil {
		return classifyAuthorization(err, "WRITE_AUTHORIZATION_REVOKE_FAILED")
	}
	return classifyAuthorization(tx.Commit(ctx), "WRITE_AUTHORIZATION_COMMIT_FAILED")
}

const authorizationColumns = `id::text,workspace_id::text,workflow_run_id::text,node_run_id::text,proposal_id::text,revision_id::text,approval_id::text,tool_name,capability,scope,approved_change_hash,target_version,token_hash,idempotency_key,status,issued_at,expires_at,revoked_at,consumed_at,version`

const authorizationInsert = `INSERT INTO change_control.tool_authorization(
	id,workspace_id,workflow_run_id,node_run_id,proposal_id,revision_id,approval_id,tool_name,capability,scope,approved_change_hash,target_version,token_hash,idempotency_key,status,issued_at,expires_at,revoked_at,consumed_at,version
) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
ON CONFLICT(workspace_id,idempotency_key) DO NOTHING`

func scanAuthorization(row pgx.Row) (domain.ToolAuthorization, error) {
	var authorization domain.ToolAuthorization
	var id, workspaceID, runID, nodeID, proposalID, revisionID, approvalID, capability, status string
	if err := row.Scan(&id, &workspaceID, &runID, &nodeID, &proposalID, &revisionID, &approvalID, &authorization.ToolName, &capability, &authorization.Scope, &authorization.ApprovedChangeHash, &authorization.TargetVersion, &authorization.TokenHash, &authorization.IdempotencyKey, &status, &authorization.IssuedAt, &authorization.ExpiresAt, &authorization.RevokedAt, &authorization.ConsumedAt, &authorization.Version); err != nil {
		return domain.ToolAuthorization{}, err
	}
	authorization.ID, authorization.WorkspaceID, authorization.WorkflowRunID, authorization.NodeRunID = foundation.ID(id), foundation.ID(workspaceID), foundation.ID(runID), foundation.ID(nodeID)
	authorization.ProposalID, authorization.RevisionID, authorization.ApprovalID = foundation.ID(proposalID), foundation.ID(revisionID), foundation.ID(approvalID)
	authorization.Capability, authorization.Status = domain.Capability(capability), domain.AuthorizationStatus(status)
	return authorization, nil
}

func sameAuthorizationIdentity(existing, requested domain.ToolAuthorization) bool {
	return existing.WorkspaceID == requested.WorkspaceID && existing.WorkflowRunID == requested.WorkflowRunID && existing.NodeRunID == requested.NodeRunID && existing.ProposalID == requested.ProposalID && existing.RevisionID == requested.RevisionID && existing.ApprovalID == requested.ApprovalID && existing.ToolName == requested.ToolName && existing.Capability == requested.Capability && existing.Scope == requested.Scope && existing.ApprovedChangeHash == requested.ApprovedChangeHash && existing.TargetVersion == requested.TargetVersion && existing.IdempotencyKey == requested.IdempotencyKey && existing.ExpiresAt.Sub(existing.IssuedAt) == requested.ExpiresAt.Sub(requested.IssuedAt)
}

func sameAuthorizationBinding(authorization domain.ToolAuthorization, request domain.AuthorizationConsume) bool {
	return domain.ValidateAuthorizationConsumeBinding(authorization, request) == nil
}

func verifyCurrentAuthorizationState(ctx context.Context, tx pgx.Tx, authorization domain.ToolAuthorization) error {
	var workspaceID, status, targetPath, baseHash, revisionHash, approvalProposal, approvalRevision, approvalHash, decision string
	err := tx.QueryRow(ctx, `
		SELECT p.workspace_id,p.status,r.target_path,r.base_hash,r.change_hash,a.proposal_id,a.revision_id,a.change_hash,a.decision
		FROM change_control.proposal p
		JOIN change_control.proposal_revision r ON r.id=$2 AND r.proposal_id=p.id
		JOIN change_control.approval a ON a.id=$3 AND a.proposal_id=p.id AND a.revision_id=r.id
		WHERE p.id=$1
		FOR UPDATE OF p`, string(authorization.ProposalID), string(authorization.RevisionID), string(authorization.ApprovalID)).Scan(&workspaceID, &status, &targetPath, &baseHash, &revisionHash, &approvalProposal, &approvalRevision, &approvalHash, &decision)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return foundation.NewError(foundation.ErrorPermissionDenied, "WRITE_AUTHORIZATION_APPROVAL_REQUIRED", false, err)
		}
		return classifyAuthorization(err, "WRITE_AUTHORIZATION_APPROVAL_QUERY_FAILED")
	}
	if workspaceID != string(authorization.WorkspaceID) || (status != string(domain.StatusApproved) && status != string(domain.StatusApplying)) || approvalProposal != string(authorization.ProposalID) || approvalRevision != string(authorization.RevisionID) || decision != string(domain.DecisionApproved) || revisionHash != authorization.ApprovedChangeHash || approvalHash != authorization.ApprovedChangeHash || baseHash != authorization.TargetVersion || authorization.Scope != domain.ExpectedAuthorizationScope(targetPath) {
		return foundation.NewError(foundation.ErrorPermissionDenied, "WRITE_AUTHORIZATION_APPROVAL_REQUIRED", false, errors.New("authorization approval state is no longer valid"))
	}
	var one int
	err = tx.QueryRow(ctx, `
		SELECT 1 FROM workflow.run r JOIN workflow.node_run n ON n.run_id=r.id
		WHERE r.id=$1 AND n.id=$2 AND r.workspace_id=$3
		  AND r.status IN ('pending','running','waiting_for_human')
		  AND n.status IN ('pending','running','waiting_for_human')
		FOR UPDATE OF r,n`, string(authorization.WorkflowRunID), string(authorization.NodeRunID), string(authorization.WorkspaceID)).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "WRITE_AUTHORIZATION_WORKFLOW_CONTEXT_INVALID", false, err)
	}
	if err != nil {
		return classifyAuthorization(err, "WRITE_AUTHORIZATION_WORKFLOW_CONTEXT_QUERY_FAILED")
	}
	return nil
}

func classifyAuthorization(err error, code string) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return foundation.NewError(foundation.ErrorVersionConflict, "WRITE_AUTHORIZATION_IDEMPOTENCY_CONFLICT", false, err)
		case "23503", "23514":
			return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, err)
		case "40001", "40P01", "57P01", "08000", "08003", "08006":
			return foundation.NewError(foundation.ErrorRetryableFailure, code, true, err)
		}
	}
	return foundation.NewError(foundation.ErrorNonRetryableFailure, code, false, err)
}

func authorizationTimePointer(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC()
}

func scanProposal(row pgx.Row) (domain.Proposal, error) {
	var proposalID, workspaceID, idempotencyKey, requestHash, status string
	var workflowRunID *string
	var revisionID, targetPath, baseHash, content, evidence, risk, rollback, changeHash string
	var approvalID, approvalHash, decision, approvedGitHead *string
	var createdAt, updatedAt, revisionCreatedAt time.Time
	var decidedAt *time.Time
	var revisionNo int
	var version int64
	err := row.Scan(
		&proposalID, &workspaceID, &idempotencyKey, &requestHash, &workflowRunID, &status, &version, &createdAt, &updatedAt,
		&revisionID, &revisionNo, &targetPath, &baseHash, &content, &evidence, &risk, &rollback, &changeHash, &revisionCreatedAt,
		&approvalID, &approvalHash, &decision, &approvedGitHead, &decidedAt,
	)
	if err != nil {
		return domain.Proposal{}, err
	}
	proposal := domain.Proposal{
		ID: foundation.ID(proposalID), WorkspaceID: foundation.ID(workspaceID), TargetPath: targetPath,
		IdempotencyKey: idempotencyKey, RequestHash: requestHash,
		Status: domain.ProposalStatus(status), Version: version, CreatedAt: createdAt, UpdatedAt: updatedAt,
		Revision: domain.Revision{
			ID: foundation.ID(revisionID), ProposalID: foundation.ID(proposalID), RevisionNo: revisionNo,
			TargetPath: targetPath, BaseHash: baseHash, Content: content, EvidenceSummary: evidence,
			Risk: risk, RollbackPlan: rollback, ChangeHash: changeHash, CreatedAt: revisionCreatedAt,
		},
	}
	if workflowRunID != nil {
		value := foundation.ID(*workflowRunID)
		proposal.WorkflowRunID = &value
	}
	if approvalID != nil && approvalHash != nil && decision != nil && decidedAt != nil {
		proposal.Approval = &domain.Approval{
			ID: foundation.ID(*approvalID), ProposalID: proposal.ID, RevisionID: proposal.Revision.ID,
			ChangeHash: *approvalHash, Decision: domain.Decision(*decision), ApprovedGitHead: approvedGitHead, DecidedAt: *decidedAt,
		}
	}
	return proposal, nil
}

func getApproval(ctx context.Context, row interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, revisionID foundation.ID) (domain.Approval, error) {
	var id, proposalID, revision, changeHash, decision string
	var approvedGitHead *string
	var decidedAt time.Time
	err := row.QueryRow(ctx, `SELECT id::text,proposal_id::text,revision_id::text,change_hash,decision,approved_git_head,decided_at FROM change_control.approval WHERE revision_id=$1`, string(revisionID)).Scan(&id, &proposalID, &revision, &changeHash, &decision, &approvedGitHead, &decidedAt)
	if err != nil {
		return domain.Approval{}, err
	}
	return domain.Approval{ID: foundation.ID(id), ProposalID: foundation.ID(proposalID), RevisionID: foundation.ID(revision), ChangeHash: changeHash, Decision: domain.Decision(decision), ApprovedGitHead: approvedGitHead, DecidedAt: decidedAt}, nil
}

func sameOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return strings.EqualFold(*left, *right)
}

func classify(err error, code string) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return foundation.NewError(foundation.ErrorVersionConflict, "APPROVAL_ALREADY_DECIDED", false, err)
		case "23503", "23514":
			return foundation.NewError(foundation.ErrorConsistencyViolation, code, false, err)
		case "40001", "40P01", "57P01", "08000", "08003", "08006":
			return foundation.NewError(foundation.ErrorRetryableFailure, code, true, err)
		}
	}
	return foundation.NewError(foundation.ErrorNonRetryableFailure, code, false, err)
}
