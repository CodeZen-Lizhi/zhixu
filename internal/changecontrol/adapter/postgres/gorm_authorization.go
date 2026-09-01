package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"gorm.io/gorm"
)

var _ domain.AuthorizationRepository = (*GORMRepository)(nil)

const gormChangeAuthorizationColumns = `id::text,workspace_id::text,workflow_run_id::text,node_run_id::text,
	proposal_id::text,revision_id::text,approval_id::text,tool_name,capability,scope,approved_change_hash,
	target_mode,target_version,token_hash,idempotency_key,status,issued_at,expires_at,revoked_at,consumed_at,version`

// ValidateWorkflowContext confirms that the persisted workflow node is live
// and belongs to the requested workspace.
func (repository *GORMRepository) ValidateWorkflowContext(ctx context.Context, workspaceID, runID, nodeID foundation.ID) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	row, err := gormChangeRawRow(ctx, repository.database, `
		SELECT 1
		FROM workflow.run AS run
		JOIN workflow.node_run AS node ON node.run_id=run.id
		WHERE run.id=? AND node.id=? AND run.workspace_id=?
		  AND run.status IN ('pending','running','waiting_for_human')
		  AND node.status IN ('pending','running','waiting_for_human')`, string(runID), string(nodeID), string(workspaceID))
	if err != nil {
		return classifyGORMAuthorization(ctx, err, "WRITE_AUTHORIZATION_WORKFLOW_CONTEXT_QUERY_FAILED")
	}
	var one int
	if err := row.Scan(&one); gormChangeNoRows(err) {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "WRITE_AUTHORIZATION_WORKFLOW_CONTEXT_INVALID", false, err)
	} else if err != nil {
		return classifyGORMAuthorization(ctx, err, "WRITE_AUTHORIZATION_WORKFLOW_CONTEXT_QUERY_FAILED")
	}
	return nil
}

// GetAuthorization locks the exact authorization while applying expiry using
// PostgreSQL time rather than a caller or process clock.
func (repository *GORMRepository) GetAuthorization(ctx context.Context, workspaceID foundation.ID, idempotencyKey, tokenHash string) (domain.ToolAuthorization, error) {
	var authorization domain.ToolAuthorization
	err := repository.within(ctx, foundation.TransactionOptions{},
		"WRITE_AUTHORIZATION_TRANSACTION_FAILED", "WRITE_AUTHORIZATION_COMMIT_FAILED", classifyGORMAuthorization,
		func(callbackCtx context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
			now, err := gormChangeAuthorizationNow(callbackCtx, tx)
			if err != nil {
				return err
			}
			authorization, err = gormChangeLoadAuthorization(callbackCtx, tx, workspaceID, idempotencyKey, tokenHash)
			if gormChangeNoRows(err) {
				return foundation.NewError(foundation.ErrorPermissionDenied, "WRITE_AUTHORIZATION_NOT_FOUND", false, err)
			}
			if err != nil {
				return classifyGORMAuthorization(callbackCtx, err, "WRITE_AUTHORIZATION_QUERY_FAILED")
			}
			if authorization.Status == domain.AuthorizationIssued && !now.Before(authorization.ExpiresAt) {
				row, queryErr := gormChangeRawRow(callbackCtx, tx, `
				UPDATE change_control.tool_authorization
				SET status='expired',version=version+1
				WHERE id=? AND status='issued'
				RETURNING `+gormChangeAuthorizationColumns, string(authorization.ID))
				if queryErr != nil {
					return classifyGORMAuthorization(callbackCtx, queryErr, "WRITE_AUTHORIZATION_EXPIRE_FAILED")
				}
				expired, scanErr := gormChangeScanAuthorization(row)
				if scanErr != nil {
					return classifyGORMAuthorization(callbackCtx, scanErr, "WRITE_AUTHORIZATION_EXPIRE_FAILED")
				}
				authorization = expired
			}
			return nil
		})
	if err != nil {
		return domain.ToolAuthorization{}, err
	}
	return authorization, nil
}

// CreateAuthorization persists an exact idempotent authorization binding.
func (repository *GORMRepository) CreateAuthorization(ctx context.Context, authorization domain.ToolAuthorization) (domain.AuthorizationIssueResult, error) {
	targetMode, modeErr := domain.ValidateTargetMode(authorization.TargetMode)
	if modeErr != nil {
		return domain.AuthorizationIssueResult{}, foundation.NewError(foundation.ErrorInvalidInput, "WRITE_AUTHORIZATION_TARGET_MODE_INVALID", false, modeErr)
	}
	authorization.TargetMode = targetMode
	requested := authorization
	replayed := false
	err := repository.within(ctx, foundation.TransactionOptions{},
		"WRITE_AUTHORIZATION_TRANSACTION_FAILED", "WRITE_AUTHORIZATION_COMMIT_FAILED", classifyGORMAuthorization,
		func(callbackCtx context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
			now, err := gormChangeAuthorizationNow(callbackCtx, tx)
			if err != nil {
				return err
			}
			ttl := authorization.ExpiresAt.Sub(authorization.IssuedAt)
			authorization.IssuedAt = now
			authorization.ExpiresAt = now.Add(ttl)
			row, queryErr := gormChangeRawRow(callbackCtx, tx, `
			INSERT INTO change_control.tool_authorization(
				id,workspace_id,workflow_run_id,node_run_id,proposal_id,revision_id,approval_id,tool_name,capability,scope,
				approved_change_hash,target_mode,target_version,token_hash,idempotency_key,status,issued_at,expires_at,revoked_at,consumed_at,version
			) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(workspace_id,idempotency_key) DO NOTHING
			RETURNING `+gormChangeAuthorizationColumns,
				string(authorization.ID), string(authorization.WorkspaceID), string(authorization.WorkflowRunID), string(authorization.NodeRunID),
				string(authorization.ProposalID), string(authorization.RevisionID), string(authorization.ApprovalID), authorization.ToolName,
				string(authorization.Capability), authorization.Scope, authorization.ApprovedChangeHash, string(authorization.TargetMode), authorization.TargetVersion,
				authorization.TokenHash, authorization.IdempotencyKey, string(authorization.Status), authorization.IssuedAt.UTC(), authorization.ExpiresAt.UTC(),
				gormChangeAuthorizationTime(authorization.RevokedAt), gormChangeAuthorizationTime(authorization.ConsumedAt), authorization.Version,
			)
			if queryErr != nil {
				return classifyGORMAuthorization(callbackCtx, queryErr, "WRITE_AUTHORIZATION_CREATE_FAILED")
			}
			persisted, scanErr := gormChangeScanAuthorization(row)
			if gormChangeNoRows(scanErr) {
				existing, loadErr := gormChangeLoadAuthorization(callbackCtx, tx, requested.WorkspaceID, requested.IdempotencyKey, "")
				if loadErr != nil {
					return classifyGORMAuthorization(callbackCtx, loadErr, "WRITE_AUTHORIZATION_IDEMPOTENCY_QUERY_FAILED")
				}
				if !gormChangeSameAuthorizationIdentity(existing, requested) {
					return foundation.NewError(foundation.ErrorVersionConflict, "WRITE_AUTHORIZATION_IDEMPOTENCY_CONFLICT", false, errors.New("authorization idempotency key is bound to another request"))
				}
				authorization, replayed = existing, true
				return nil
			}
			if scanErr != nil {
				return classifyGORMAuthorization(callbackCtx, scanErr, "WRITE_AUTHORIZATION_CREATE_FAILED")
			}
			authorization = persisted
			return nil
		})
	if err != nil {
		return domain.AuthorizationIssueResult{}, err
	}
	return domain.AuthorizationIssueResult{Authorization: authorization, Replayed: replayed}, nil
}

// ConsumeAuthorization atomically validates, expires or consumes one issued
// authorization. It intentionally revalidates all durable approval bindings.
func (repository *GORMRepository) ConsumeAuthorization(ctx context.Context, request domain.AuthorizationConsume) (domain.AuthorizationConsumeResult, error) {
	var result domain.AuthorizationConsumeResult
	var committedError error
	err := repository.within(ctx, foundation.TransactionOptions{},
		"WRITE_AUTHORIZATION_CONSUME_TRANSACTION_FAILED", "WRITE_AUTHORIZATION_COMMIT_FAILED", classifyGORMAuthorization,
		func(callbackCtx context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
			now, err := gormChangeAuthorizationNow(callbackCtx, tx)
			if err != nil {
				return err
			}
			authorization, loadErr := gormChangeLoadAuthorization(callbackCtx, tx, request.WorkspaceID, request.IdempotencyKey, request.Credential)
			if gormChangeNoRows(loadErr) {
				return foundation.NewError(foundation.ErrorPermissionDenied, "WRITE_AUTHORIZATION_NOT_FOUND", false, loadErr)
			}
			if loadErr != nil {
				return classifyGORMAuthorization(callbackCtx, loadErr, "WRITE_AUTHORIZATION_QUERY_FAILED")
			}
			if domain.ValidateAuthorizationConsumeBinding(authorization, request) != nil {
				return foundation.NewError(foundation.ErrorVersionConflict, "WRITE_AUTHORIZATION_BINDING_CONFLICT", false, errors.New("authorization binding does not match request"))
			}
			switch authorization.Status {
			case domain.AuthorizationConsumed:
				if authorization.IdempotencyKey != request.IdempotencyKey {
					return foundation.NewError(foundation.ErrorVersionConflict, "WRITE_AUTHORIZATION_ALREADY_CONSUMED", false, errors.New("authorization was already consumed"))
				}
				result = domain.AuthorizationConsumeResult{Authorization: authorization, Replayed: true}
				return nil
			case domain.AuthorizationRevoked:
				return foundation.NewError(foundation.ErrorPermissionDenied, "WRITE_AUTHORIZATION_REVOKED", false, errors.New("authorization was revoked"))
			case domain.AuthorizationExpired:
				committedError = foundation.NewError(foundation.ErrorPermissionDenied, "WRITE_AUTHORIZATION_EXPIRED", false, errors.New("authorization has expired"))
				return nil
			case domain.AuthorizationIssued:
			default:
				return foundation.NewError(foundation.ErrorConsistencyViolation, "WRITE_AUTHORIZATION_QUERY_FAILED", false, errors.New("authorization status is invalid"))
			}
			if !now.Before(authorization.ExpiresAt) {
				if _, updateErr := gormChangeExec(callbackCtx, tx, `UPDATE change_control.tool_authorization SET status='expired',version=version+1 WHERE id=? AND status='issued'`, string(authorization.ID)); updateErr != nil {
					return classifyGORMAuthorization(callbackCtx, updateErr, "WRITE_AUTHORIZATION_EXPIRE_FAILED")
				}
				committedError = foundation.NewError(foundation.ErrorPermissionDenied, "WRITE_AUTHORIZATION_EXPIRED", false, errors.New("authorization has expired"))
				return nil
			}
			if err := gormChangeVerifyAuthorizationState(callbackCtx, tx, authorization); err != nil {
				return err
			}
			row, queryErr := gormChangeRawRow(callbackCtx, tx, `
			UPDATE change_control.tool_authorization
			SET status='consumed',consumed_at=?,version=version+1
			WHERE id=? AND status='issued'
			RETURNING `+gormChangeAuthorizationColumns, now.UTC(), string(authorization.ID))
			if queryErr != nil {
				return classifyGORMAuthorization(callbackCtx, queryErr, "WRITE_AUTHORIZATION_CONSUME_FAILED")
			}
			consumed, scanErr := gormChangeScanAuthorization(row)
			if gormChangeNoRows(scanErr) {
				return foundation.NewError(foundation.ErrorVersionConflict, "WRITE_AUTHORIZATION_CONSUME_CONFLICT", false, scanErr)
			}
			if scanErr != nil {
				return classifyGORMAuthorization(callbackCtx, scanErr, "WRITE_AUTHORIZATION_CONSUME_FAILED")
			}
			result = domain.AuthorizationConsumeResult{Authorization: consumed}
			return nil
		})
	if err != nil {
		return domain.AuthorizationConsumeResult{}, err
	}
	if committedError != nil {
		return domain.AuthorizationConsumeResult{}, committedError
	}
	return result, nil
}

func (repository *GORMRepository) RevokeAuthorization(ctx context.Context, id foundation.ID, _ time.Time) error {
	return repository.within(ctx, foundation.TransactionOptions{},
		"WRITE_AUTHORIZATION_REVOKE_TRANSACTION_FAILED", "WRITE_AUTHORIZATION_COMMIT_FAILED", classifyGORMAuthorization,
		func(callbackCtx context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
			row, err := gormChangeRawRow(callbackCtx, tx, `SELECT status FROM change_control.tool_authorization WHERE id=? FOR UPDATE`, string(id))
			if err != nil {
				return classifyGORMAuthorization(callbackCtx, err, "WRITE_AUTHORIZATION_QUERY_FAILED")
			}
			var status domain.AuthorizationStatus
			if err := row.Scan(&status); gormChangeNoRows(err) {
				return foundation.NewError(foundation.ErrorNotFound, "WRITE_AUTHORIZATION_NOT_FOUND", false, err)
			} else if err != nil {
				return classifyGORMAuthorization(callbackCtx, err, "WRITE_AUTHORIZATION_QUERY_FAILED")
			}
			switch status {
			case domain.AuthorizationConsumed:
				return foundation.NewError(foundation.ErrorVersionConflict, "WRITE_AUTHORIZATION_ALREADY_CONSUMED", false, errors.New("consumed authorization cannot be revoked"))
			case domain.AuthorizationRevoked, domain.AuthorizationExpired:
				return nil
			case domain.AuthorizationIssued:
				changed, execErr := gormChangeExec(callbackCtx, tx, `UPDATE change_control.tool_authorization SET status='revoked',revoked_at=CURRENT_TIMESTAMP,version=version+1 WHERE id=? AND status='issued'`, string(id))
				if execErr != nil {
					return classifyGORMAuthorization(callbackCtx, execErr, "WRITE_AUTHORIZATION_REVOKE_FAILED")
				}
				if changed != 1 {
					return foundation.NewError(foundation.ErrorVersionConflict, "WRITE_AUTHORIZATION_REVOKE_CONFLICT", false, errors.New("authorization changed before revocation"))
				}
				return nil
			default:
				return foundation.NewError(foundation.ErrorConsistencyViolation, "WRITE_AUTHORIZATION_QUERY_FAILED", false, errors.New("authorization status is invalid"))
			}
		})
}

func gormChangeAuthorizationNow(ctx context.Context, database *gorm.DB) (time.Time, error) {
	row, err := gormChangeRawRow(ctx, database, `SELECT CURRENT_TIMESTAMP`)
	if err != nil {
		return time.Time{}, classifyGORMAuthorization(ctx, err, "WRITE_AUTHORIZATION_CLOCK_QUERY_FAILED")
	}
	var now time.Time
	if err := row.Scan(&now); err != nil {
		return time.Time{}, classifyGORMAuthorization(ctx, err, "WRITE_AUTHORIZATION_CLOCK_QUERY_FAILED")
	}
	return now.UTC(), nil
}

func gormChangeLoadAuthorization(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, idempotencyKey, tokenHash string) (domain.ToolAuthorization, error) {
	row, err := gormChangeRawRow(ctx, database, `SELECT `+gormChangeAuthorizationColumns+` FROM change_control.tool_authorization WHERE workspace_id=? AND idempotency_key=? FOR UPDATE`, string(workspaceID), idempotencyKey)
	if err != nil {
		return domain.ToolAuthorization{}, err
	}
	authorization, scanErr := gormChangeScanAuthorization(row)
	if !gormChangeNoRows(scanErr) || tokenHash == "" {
		return authorization, scanErr
	}
	row, err = gormChangeRawRow(ctx, database, `SELECT `+gormChangeAuthorizationColumns+` FROM change_control.tool_authorization WHERE token_hash=? FOR UPDATE`, tokenHash)
	if err != nil {
		return domain.ToolAuthorization{}, err
	}
	return gormChangeScanAuthorization(row)
}

func gormChangeScanAuthorization(row interface{ Scan(...any) error }) (domain.ToolAuthorization, error) {
	return scanAuthorization(row)
}

func gormChangeVerifyAuthorizationState(ctx context.Context, database *gorm.DB, authorization domain.ToolAuthorization) error {
	row, err := gormChangeRawRow(ctx, database, `
		SELECT proposal.workspace_id::text,proposal.status,revision.target_path,revision.target_mode,revision.base_hash,revision.change_hash,
		       approval.proposal_id::text,approval.revision_id::text,approval.change_hash,approval.decision
		FROM change_control.proposal AS proposal
		JOIN change_control.proposal_revision AS revision ON revision.id=? AND revision.proposal_id=proposal.id
		JOIN change_control.approval AS approval ON approval.id=? AND approval.proposal_id=proposal.id AND approval.revision_id=revision.id
		JOIN change_control.proposal_revision_dispatch AS dispatch
		  ON dispatch.proposal_id=proposal.id AND dispatch.revision_id=revision.id AND dispatch.approval_id=approval.id AND dispatch.workflow_run_id=?
		WHERE proposal.id=? AND proposal.current_revision_id=revision.id
		FOR UPDATE OF proposal`, string(authorization.RevisionID), string(authorization.ApprovalID), string(authorization.WorkflowRunID), string(authorization.ProposalID))
	if err != nil {
		return classifyGORMAuthorization(ctx, err, "WRITE_AUTHORIZATION_APPROVAL_QUERY_FAILED")
	}
	var workspaceID, status, targetPath, targetMode, baseHash, revisionHash, approvalProposal, approvalRevision, approvalHash, decision string
	if err := row.Scan(&workspaceID, &status, &targetPath, &targetMode, &baseHash, &revisionHash, &approvalProposal, &approvalRevision, &approvalHash, &decision); gormChangeNoRows(err) {
		return foundation.NewError(foundation.ErrorPermissionDenied, "WRITE_AUTHORIZATION_APPROVAL_REQUIRED", false, err)
	} else if err != nil {
		return classifyGORMAuthorization(ctx, err, "WRITE_AUTHORIZATION_APPROVAL_QUERY_FAILED")
	}
	mode, modeErr := domain.ValidateTargetMode(domain.TargetMode(targetMode))
	if modeErr != nil || workspaceID != string(authorization.WorkspaceID) ||
		(status != string(domain.StatusApproved) && status != string(domain.StatusApplying)) ||
		approvalProposal != string(authorization.ProposalID) || approvalRevision != string(authorization.RevisionID) ||
		decision != string(domain.DecisionApproved) || revisionHash != authorization.ApprovedChangeHash || approvalHash != authorization.ApprovedChangeHash ||
		baseHash != authorization.TargetVersion || domain.NormalizeTargetMode(authorization.TargetMode) != mode ||
		authorization.Scope != domain.ExpectedAuthorizationScopeForTarget(targetPath, mode) {
		return foundation.NewError(foundation.ErrorPermissionDenied, "WRITE_AUTHORIZATION_APPROVAL_REQUIRED", false, errors.New("authorization approval state is no longer valid"))
	}
	row, err = gormChangeRawRow(ctx, database, `
		SELECT 1 FROM workflow.run AS run JOIN workflow.node_run AS node ON node.run_id=run.id
		WHERE run.id=? AND node.id=? AND run.workspace_id=?
		  AND run.status IN ('pending','running','waiting_for_human')
		  AND node.status IN ('pending','running','waiting_for_human')
		FOR UPDATE OF run,node`, string(authorization.WorkflowRunID), string(authorization.NodeRunID), string(authorization.WorkspaceID))
	if err != nil {
		return classifyGORMAuthorization(ctx, err, "WRITE_AUTHORIZATION_WORKFLOW_CONTEXT_QUERY_FAILED")
	}
	var one int
	if err := row.Scan(&one); gormChangeNoRows(err) {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "WRITE_AUTHORIZATION_WORKFLOW_CONTEXT_INVALID", false, err)
	} else if err != nil {
		return classifyGORMAuthorization(ctx, err, "WRITE_AUTHORIZATION_WORKFLOW_CONTEXT_QUERY_FAILED")
	}
	return nil
}

func gormChangeSameAuthorizationIdentity(existing, requested domain.ToolAuthorization) bool {
	return existing.WorkspaceID == requested.WorkspaceID && existing.WorkflowRunID == requested.WorkflowRunID &&
		existing.NodeRunID == requested.NodeRunID && existing.ProposalID == requested.ProposalID &&
		existing.RevisionID == requested.RevisionID && existing.ApprovalID == requested.ApprovalID &&
		existing.ToolName == requested.ToolName && existing.Capability == requested.Capability && existing.Scope == requested.Scope &&
		existing.ApprovedChangeHash == requested.ApprovedChangeHash && domain.NormalizeTargetMode(existing.TargetMode) == domain.NormalizeTargetMode(requested.TargetMode) &&
		existing.TargetVersion == requested.TargetVersion && existing.IdempotencyKey == requested.IdempotencyKey &&
		existing.ExpiresAt.Sub(existing.IssuedAt) == requested.ExpiresAt.Sub(requested.IssuedAt)
}

func gormChangeAuthorizationTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return value.UTC()
}
