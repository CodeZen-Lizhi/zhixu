package postgres

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/lib/pq"
	"gorm.io/gorm"
)

var _ domain.WritebackRepository = (*GORMRepository)(nil)
var _ domain.WritebackSagaRepository = (*GORMRepository)(nil)
var _ domain.WritebackExecutionLookup = (*GORMRepository)(nil)
var _ workflowapplication.ScopedCancellationSafetyGuard = (*GORMRepository)(nil)

const gormWritebackColumns = writebackColumns
const gormProposalCommitColumns = proposalCommitColumns

// SafeToCancelWorkflowNodeScoped reads only from the caller-owned transaction.
// It must not open a root transaction because Workflow owns the terminal transition.
func (repository *GORMRepository) SafeToCancelWorkflowNodeScoped(ctx context.Context, scope foundation.TransactionScope, nodeRunID foundation.ID) (bool, error) {
	parsed, err := foundation.ParseID(string(nodeRunID))
	if err != nil || parsed != nodeRunID {
		return false, foundation.NewError(foundation.ErrorInvalidInput, "WRITEBACK_CANCELLATION_SAFETY_INVALID", false, errors.Join(err, domain.ErrWritebackInvalidInput))
	}
	if err := repository.ready(ctx); err != nil {
		return false, err
	}
	tx, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return false, foundation.NewError(foundation.ErrorDependencyUnavailable, "WRITEBACK_CANCELLATION_TRANSACTION_INVALID", true, err)
	}
	rows, err := gormChangeRawRows(ctx, tx, `SELECT status,cleanup_completed_at IS NOT NULL FROM change_control.writeback_execution WHERE node_run_id=?`, string(nodeRunID))
	if err != nil {
		return false, classifyGORMWriteback(ctx, err, "WRITEBACK_CANCELLATION_SAFETY_QUERY_FAILED")
	}
	defer rows.Close()
	for rows.Next() {
		var status domain.WritebackStatus
		var cleanup bool
		if err := rows.Scan(&status, &cleanup); err != nil {
			return false, classifyGORMWriteback(ctx, err, "WRITEBACK_CANCELLATION_SAFETY_QUERY_FAILED")
		}
		if !domain.IsWritebackCancellationSafe(status, cleanup) {
			return false, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, classifyGORMWriteback(ctx, err, "WRITEBACK_CANCELLATION_SAFETY_QUERY_FAILED")
	}
	return true, nil
}

func (repository *GORMRepository) BeginWriteback(ctx context.Context, command domain.BeginWriteback) (domain.WritebackExecution, error) {
	command.IdempotencyKey, command.LeaseOwner = strings.TrimSpace(command.IdempotencyKey), strings.TrimSpace(command.LeaseOwner)
	if err := domain.ValidateBeginWriteback(command); err != nil {
		return domain.WritebackExecution{}, writebackDomainError(err)
	}
	if err := domain.ValidateWritebackLeaseOwner(command.LeaseOwner); err != nil {
		return domain.WritebackExecution{}, writebackDomainError(err)
	}
	command.WriteAuthorization.Credential = hashWritebackCredential(command.WriteAuthorization.Credential)
	command.GitAuthorization.Credential = hashWritebackCredential(command.GitAuthorization.Credential)
	var result domain.WritebackExecution
	err := repository.within(ctx, foundation.TransactionOptions{},
		"WRITEBACK_BEGIN_TRANSACTION_FAILED", "WRITEBACK_BEGIN_COMMIT_FAILED", classifyGORMWriteback,
		func(callbackCtx context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
			now, err := gormWritebackNow(callbackCtx, tx, "WRITEBACK_BEGIN_CLOCK_QUERY_FAILED")
			if err != nil {
				return err
			}
			writeAuth, gitAuth, err := gormLockBeginAuthorizations(callbackCtx, tx, command)
			if err != nil {
				return err
			}
			if writeAuth.WorkspaceID != command.WorkspaceID || writeAuth.WorkflowRunID != command.WorkflowRunID || writeAuth.NodeRunID != command.NodeRunID || writeAuth.ProposalID != command.ProposalID || gitAuth.WorkspaceID != command.WorkspaceID || gitAuth.WorkflowRunID != command.WorkflowRunID || gitAuth.NodeRunID != command.NodeRunID || gitAuth.ProposalID != command.ProposalID || writeAuth.RevisionID != gitAuth.RevisionID || writeAuth.ApprovalID != gitAuth.ApprovalID || !strings.EqualFold(writeAuth.ApprovedChangeHash, gitAuth.ApprovedChangeHash) || !strings.EqualFold(writeAuth.TargetVersion, gitAuth.TargetVersion) || writeAuth.Scope != gitAuth.Scope {
				return writebackDomainError(domain.ErrWritebackIdentityConflict)
			}
			var workspace, proposalStatus, workflowRun, revisionID, revisionProposal, targetPath, targetMode, baseHash, content, revisionHash string
			var approvalID, approvalProposal, approvalRevision, approvalHash, decision string
			var approvedGitHead *string
			row, queryErr := gormChangeRawRow(callbackCtx, tx, `
			SELECT p.workspace_id::text,p.status,d.workflow_run_id::text,r.id::text,r.proposal_id::text,r.target_path,r.target_mode,r.base_hash,r.content,r.change_hash,
			       a.id::text,a.proposal_id::text,a.revision_id::text,a.change_hash,a.decision,a.approved_git_head
			FROM change_control.proposal p JOIN change_control.proposal_revision r ON r.id=? AND r.proposal_id=p.id
			JOIN change_control.approval a ON a.id=? AND a.proposal_id=p.id AND a.revision_id=r.id
			JOIN change_control.proposal_revision_dispatch d ON d.proposal_id=p.id AND d.revision_id=r.id AND d.approval_id=a.id
			WHERE p.id=? AND p.current_revision_id=r.id FOR UPDATE OF p,r,a`, string(writeAuth.RevisionID), string(writeAuth.ApprovalID), string(command.ProposalID))
			if queryErr != nil {
				return classifyGORMWriteback(callbackCtx, queryErr, "WRITEBACK_BEGIN_APPROVAL_QUERY_FAILED")
			}
			if err := row.Scan(&workspace, &proposalStatus, &workflowRun, &revisionID, &revisionProposal, &targetPath, &targetMode, &baseHash, &content, &revisionHash, &approvalID, &approvalProposal, &approvalRevision, &approvalHash, &decision, &approvedGitHead); gormChangeNoRows(err) {
				return foundation.NewError(foundation.ErrorNotFound, "WRITEBACK_APPROVAL_NOT_FOUND", false, err)
			} else if err != nil {
				return classifyGORMWriteback(callbackCtx, err, "WRITEBACK_BEGIN_APPROVAL_QUERY_FAILED")
			}
			normalizedMode, modeErr := domain.ValidateTargetMode(domain.TargetMode(targetMode))
			if modeErr != nil || workspace != string(command.WorkspaceID) || workflowRun != string(command.WorkflowRunID) || revisionProposal != string(command.ProposalID) || approvalProposal != string(command.ProposalID) || approvalRevision != revisionID || foundation.ID(revisionID) != writeAuth.RevisionID || foundation.ID(approvalID) != writeAuth.ApprovalID || decision != string(domain.DecisionApproved) || approvedGitHead == nil || !strings.EqualFold(revisionHash, writeAuth.ApprovedChangeHash) || !strings.EqualFold(approvalHash, writeAuth.ApprovedChangeHash) || !strings.EqualFold(baseHash, writeAuth.TargetVersion) || domain.NormalizeTargetMode(writeAuth.TargetMode) != normalizedMode || domain.NormalizeTargetMode(gitAuth.TargetMode) != normalizedMode || writeAuth.Scope != domain.ExpectedAuthorizationScopeForTarget(targetPath, normalizedMode) || gitAuth.Scope != domain.ExpectedAuthorizationScopeForTarget(targetPath, normalizedMode) {
				return writebackDomainError(domain.ErrWritebackIdentityConflict)
			}
			if err := gormValidateBeginNodeLease(callbackCtx, tx, command, now, writeAuth); err != nil {
				return err
			}
			resultHash := domain.ComputeWritebackResultHash([]byte(content))
			changeHash, hashErr := domain.ComputeChangeHashForTarget(command.WorkspaceID, targetPath, normalizedMode, baseHash, content)
			if hashErr != nil || !strings.EqualFold(changeHash, writeAuth.ApprovedChangeHash) {
				return writebackDomainError(domain.ErrWritebackIdentityConflict)
			}
			existing, found, err := gormFindWritebackByKey(callbackCtx, tx, command.WorkspaceID, command.IdempotencyKey, true)
			if err != nil {
				return err
			}
			if found {
				requested := existing
				requested.WorkflowRunID, requested.NodeRunID = command.WorkflowRunID, command.NodeRunID
				requested.ProposalID, requested.RevisionID, requested.ApprovalID = command.ProposalID, writeAuth.RevisionID, writeAuth.ApprovalID
				requested.WriteAuthorizationID, requested.GitAuthorizationID = writeAuth.ID, gitAuth.ID
				requested.TargetPath, requested.TargetMode, requested.BaseHash, requested.ResultHash, requested.ApprovedChangeHash, requested.ApprovedGitHead = targetPath, normalizedMode, baseHash, resultHash, writeAuth.ApprovedChangeHash, *approvedGitHead
				if err := domain.ValidateWritebackIdentity(existing, requested); err != nil {
					return writebackDomainError(err)
				}
				result = existing
				return nil
			}
			if writeAuth.Status != domain.AuthorizationIssued || gitAuth.Status != domain.AuthorizationIssued {
				return foundation.NewError(foundation.ErrorPermissionDenied, "WRITEBACK_AUTHORIZATION_ALREADY_CONSUMED", false, errors.New("authorization was consumed without this writeback execution"))
			}
			if proposalStatus != string(domain.StatusApproved) {
				return foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_PROPOSAL_STATE_CONFLICT", false, errors.New("proposal is no longer approved"))
			}
			if err := gormConsumeBeginAuthorization(callbackCtx, tx, writeAuth, now); err != nil {
				return err
			}
			if err := gormConsumeBeginAuthorization(callbackCtx, tx, gitAuth, now); err != nil {
				return err
			}
			executionID := command.ExecutionID
			if executionID == "" {
				executionID, err = foundation.NewUUIDGenerator(nil).New()
				if err != nil {
					return classifyGORMWriteback(callbackCtx, err, "WRITEBACK_BEGIN_ID_GENERATION_FAILED")
				}
			}
			requested := domain.CreateWriteback{ID: executionID, WorkspaceID: command.WorkspaceID, WorkflowRunID: command.WorkflowRunID, NodeRunID: command.NodeRunID, ProposalID: command.ProposalID, RevisionID: writeAuth.RevisionID, ApprovalID: writeAuth.ApprovalID, WriteAuthorizationID: writeAuth.ID, GitAuthorizationID: gitAuth.ID, TargetPath: targetPath, TargetMode: normalizedMode, BaseHash: baseHash, ResultHash: resultHash, ApprovedChangeHash: strings.ToLower(writeAuth.ApprovedChangeHash), ApprovedGitHead: strings.ToLower(*approvedGitHead), IdempotencyKey: command.IdempotencyKey, CreatedAt: now}
			if err := domain.ValidateWritebackCreate(requested); err != nil {
				return writebackDomainError(err)
			}
			result, err = gormInsertWriteback(callbackCtx, tx, executionFromCreate(requested, now), false)
			if err != nil {
				return classifyGORMWriteback(callbackCtx, err, "WRITEBACK_BEGIN_CREATE_FAILED")
			}
			changed, err := gormChangeExec(callbackCtx, tx, `UPDATE change_control.proposal SET status=?,updated_at=?,version=version+1 WHERE id=? AND workspace_id=? AND status=? AND current_revision_id=?`, string(domain.StatusApplying), now, string(command.ProposalID), string(command.WorkspaceID), string(domain.StatusApproved), revisionID)
			if err != nil {
				return classifyGORMWriteback(callbackCtx, err, "WRITEBACK_BEGIN_PROPOSAL_FAILED")
			}
			if changed != 1 {
				return foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_PROPOSAL_STATE_CONFLICT", false, errors.New("proposal is no longer approved"))
			}
			return nil
		})
	if err != nil {
		return domain.WritebackExecution{}, err
	}
	return result, nil
}

func (repository *GORMRepository) ValidateWritebackLease(ctx context.Context, id foundation.ID, owner string) error {
	if err := repository.ready(ctx); err != nil {
		return err
	}
	if err := domain.ValidateWritebackLeaseOwner(owner); err != nil {
		return writebackDomainError(err)
	}
	row, err := gormChangeRawRow(ctx, repository.database, `SELECT 1 FROM change_control.writeback_execution e JOIN workflow.node_run n ON n.id=e.node_run_id WHERE e.id=? AND n.status='running' AND n.lease_owner=? AND n.lease_until>CURRENT_TIMESTAMP LIMIT 1`, string(id), strings.TrimSpace(owner))
	if err != nil {
		return classifyGORMWriteback(ctx, err, "WRITEBACK_LEASE_QUERY_FAILED")
	}
	var one int
	if err := row.Scan(&one); gormChangeNoRows(err) {
		return foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_LEASE_LOST", true, domain.ErrWritebackLeaseLost)
	} else if err != nil {
		return classifyGORMWriteback(ctx, err, "WRITEBACK_LEASE_QUERY_FAILED")
	}
	return nil
}

func (repository *GORMRepository) FinalizeWritebackCleanup(ctx context.Context, id foundation.ID, expectedVersion int64, _ time.Time) (domain.WritebackExecution, error) {
	if id == "" || expectedVersion <= 0 {
		return domain.WritebackExecution{}, writebackDomainError(domain.ErrWritebackInvalidInput)
	}
	var result domain.WritebackExecution
	err := repository.within(ctx, foundation.TransactionOptions{},
		"WRITEBACK_CLEANUP_TRANSACTION_FAILED", "WRITEBACK_CLEANUP_COMMIT_FAILED", classifyGORMWriteback,
		func(callbackCtx context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
			current, err := gormLoadWriteback(callbackCtx, tx, `SELECT `+gormWritebackColumns+` FROM change_control.writeback_execution WHERE id=? FOR UPDATE`, string(id))
			if gormChangeNoRows(err) {
				return foundation.NewError(foundation.ErrorNotFound, "WRITEBACK_NOT_FOUND", false, err)
			}
			if err != nil {
				return classifyGORMWriteback(callbackCtx, err, "WRITEBACK_CLEANUP_QUERY_FAILED")
			}
			if current.Status != domain.WritebackStatusVerifying {
				return writebackDomainError(domain.ErrWritebackVersionConflict)
			}
			if current.CleanupCompletedAt != nil {
				result = current
				return nil
			}
			if current.Version != expectedVersion {
				return writebackDomainError(domain.ErrWritebackVersionConflict)
			}
			now, err := gormWritebackNow(callbackCtx, tx, "WRITEBACK_CLEANUP_CLOCK_QUERY_FAILED")
			if err != nil {
				return err
			}
			result, err = gormLoadWriteback(callbackCtx, tx, `UPDATE change_control.writeback_execution SET cleanup_completed_at=?,updated_at=?,version=version+1 WHERE id=? AND version=? AND status=? RETURNING `+gormWritebackColumns, now, now, string(id), expectedVersion, string(domain.WritebackStatusVerifying))
			if gormChangeNoRows(err) {
				return writebackDomainError(domain.ErrWritebackVersionConflict)
			}
			if err != nil {
				return classifyGORMWriteback(callbackCtx, err, "WRITEBACK_CLEANUP_UPDATE_FAILED")
			}
			return nil
		})
	if err != nil {
		return domain.WritebackExecution{}, err
	}
	return result, nil
}

func (repository *GORMRepository) CreateWritebackExecution(ctx context.Context, command domain.CreateWriteback) (domain.WritebackExecution, error) {
	command = normalizeCreateWriteback(command)
	if err := domain.ValidateWritebackCreate(command); err != nil {
		return domain.WritebackExecution{}, writebackDomainError(err)
	}
	var result domain.WritebackExecution
	err := repository.within(ctx, foundation.TransactionOptions{},
		"WRITEBACK_TRANSACTION_FAILED", "WRITEBACK_COMMIT_FAILED", classifyGORMWriteback,
		func(callbackCtx context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
			existing, found, err := gormFindWritebackConflict(callbackCtx, tx, executionFromCreate(command, time.Time{}))
			if err != nil {
				return err
			}
			if found {
				requested := executionFromCreate(command, existing.CreatedAt)
				if err := domain.ValidateWritebackIdentity(existing, requested); err != nil {
					return writebackDomainError(err)
				}
				result = existing
				return nil
			}
			now, err := gormWritebackNow(callbackCtx, tx, "WRITEBACK_CLOCK_QUERY_FAILED")
			if err != nil {
				return err
			}
			requested := executionFromCreate(command, now)
			result, err = gormInsertWriteback(callbackCtx, tx, requested, true)
			if gormChangeNoRows(err) {
				existing, found, err := gormFindWritebackConflict(callbackCtx, tx, requested)
				if err != nil {
					return err
				}
				if !found {
					return foundation.NewError(foundation.ErrorConsistencyViolation, "WRITEBACK_CONFLICT_RECORD_MISSING", false, err)
				}
				if err := domain.ValidateWritebackIdentity(existing, requested); err != nil {
					return writebackDomainError(err)
				}
				result = existing
				return nil
			}
			if err != nil {
				return classifyGORMWriteback(callbackCtx, err, "WRITEBACK_CREATE_FAILED")
			}
			changed, err := gormChangeExec(callbackCtx, tx, `UPDATE change_control.proposal SET status=?,updated_at=?,version=version+1 WHERE id=? AND workspace_id=? AND status=?`, string(domain.StatusApplying), now, string(command.ProposalID), string(command.WorkspaceID), string(domain.StatusApproved))
			if err != nil {
				return classifyGORMWriteback(callbackCtx, err, "WRITEBACK_PROPOSAL_START_FAILED")
			}
			if changed != 1 {
				return foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_PROPOSAL_STATE_CONFLICT", false, errors.New("proposal is no longer approved"))
			}
			return nil
		})
	if err != nil {
		return domain.WritebackExecution{}, err
	}
	return result, nil
}

func (repository *GORMRepository) GetWritebackExecution(ctx context.Context, id foundation.ID) (domain.WritebackExecution, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.WritebackExecution{}, err
	}
	execution, err := gormLoadWriteback(ctx, repository.database, `SELECT `+gormWritebackColumns+` FROM change_control.writeback_execution WHERE id=?`, string(id))
	if gormChangeNoRows(err) {
		return domain.WritebackExecution{}, foundation.NewError(foundation.ErrorNotFound, "WRITEBACK_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.WritebackExecution{}, classifyGORMWriteback(ctx, err, "WRITEBACK_QUERY_FAILED")
	}
	return execution, nil
}

func (repository *GORMRepository) FindWritebackExecutionByKey(ctx context.Context, workspaceID foundation.ID, key string) (domain.WritebackExecution, bool, error) {
	if err := repository.ready(ctx); err != nil {
		return domain.WritebackExecution{}, false, err
	}
	parsed, err := foundation.ParseID(string(workspaceID))
	canonical := strings.TrimSpace(key)
	if err != nil || parsed != workspaceID || canonical == "" || canonical != key || len(canonical) > 128 {
		return domain.WritebackExecution{}, false, foundation.NewError(foundation.ErrorInvalidInput, "WRITEBACK_LOOKUP_INVALID", false, errors.Join(err, domain.ErrWritebackInvalidInput))
	}
	execution, found, err := gormFindWritebackByKey(ctx, repository.database, workspaceID, key, false)
	if err != nil {
		return domain.WritebackExecution{}, false, err
	}
	return execution, found, nil
}

// CheckpointWritebackExecution intentionally has no replay branch. A caller that
// loses its commit response must recover the execution and resume the workflow.
func (repository *GORMRepository) CheckpointWritebackExecution(ctx context.Context, command domain.CheckpointWriteback) (domain.WritebackExecution, error) {
	var result domain.WritebackExecution
	err := repository.within(ctx, foundation.TransactionOptions{},
		"WRITEBACK_CHECKPOINT_TRANSACTION_FAILED", "WRITEBACK_COMMIT_FAILED", classifyGORMWriteback,
		func(callbackCtx context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
			proposalID, err := gormLockWritebackProposalFirst(callbackCtx, tx, command.ExecutionID)
			if err != nil {
				return err
			}
			current, err := gormLoadWriteback(callbackCtx, tx, `SELECT `+gormWritebackColumns+` FROM change_control.writeback_execution WHERE id=? FOR UPDATE`, string(command.ExecutionID))
			if gormChangeNoRows(err) {
				return foundation.NewError(foundation.ErrorNotFound, "WRITEBACK_NOT_FOUND", false, err)
			}
			if err != nil {
				return classifyGORMWriteback(callbackCtx, err, "WRITEBACK_QUERY_FAILED")
			}
			if current.ProposalID != proposalID {
				return foundation.NewError(foundation.ErrorConsistencyViolation, "WRITEBACK_PROPOSAL_BINDING_CONFLICT", false, domain.ErrWritebackIdentityConflict)
			}
			command = normalizeCheckpointWriteback(current, command)
			if err := domain.ValidateWritebackCheckpoint(current, command); err != nil {
				return writebackDomainError(err)
			}
			if command.ResultHash != "" && !strings.EqualFold(command.ResultHash, current.ResultHash) {
				return writebackDomainError(domain.ErrWritebackIdentityConflict)
			}
			now, err := gormWritebackNow(callbackCtx, tx, "WRITEBACK_CLOCK_QUERY_FAILED")
			if err != nil {
				return err
			}
			result, err = gormLoadWriteback(callbackCtx, tx, `UPDATE change_control.writeback_execution SET status=?,failure_code=?,manual_recovery_required=?,temporary_ref=?,backup_ref=?,file_byte_size=?,file_mode=?,file_lock_token=?,file_result_lock_token=?,file_backup_lock_token=?,base_blob_id=?,result_blob_id=?,base_mode=?,git_commit=?,parent_git_commit=?,diff_hash=?,completed_at=?,updated_at=?,version=version+1 WHERE id=? AND version=? AND status=? RETURNING `+gormWritebackColumns,
				string(command.Status), nullableString(command.FailureCode), command.ManualRecoveryRequired, nullableString(command.TemporaryRef), nullableString(command.BackupRef), nullableInt64(command.FileByteSize), nullableUint32(command.FileMode), nullableString(command.FileLockToken), nullableString(command.FileResultLockToken), nullableString(command.FileBackupLockToken), nullableString(command.BaseBlobID), nullableString(command.ResultBlobID), nullableString(command.BaseMode), nullableString(command.GitCommit), nullableString(command.ParentGitCommit), nullableString(command.DiffHash), command.CompletedAt, now, string(current.ID), command.ExpectedVersion, string(current.Status))
			if gormChangeNoRows(err) {
				return writebackDomainError(domain.ErrWritebackVersionConflict)
			}
			if err != nil {
				return classifyGORMWriteback(callbackCtx, err, "WRITEBACK_CHECKPOINT_FAILED")
			}
			if status, ok := proposalStatusForCheckpoint(command.Status); ok {
				return gormUpdateProposalForWriteback(callbackCtx, tx, current.ProposalID, status, now)
			}
			return nil
		})
	if err != nil {
		return domain.WritebackExecution{}, err
	}
	return result, nil
}

func (repository *GORMRepository) PublishWriteback(ctx context.Context, command domain.PublishWriteback) (domain.PublishWritebackResult, error) {
	command = normalizePublishWriteback(command)
	var result domain.PublishWritebackResult
	err := repository.within(ctx, foundation.TransactionOptions{},
		"WRITEBACK_PUBLISH_TRANSACTION_FAILED", "WRITEBACK_COMMIT_FAILED", classifyGORMWriteback,
		func(callbackCtx context.Context, _ foundation.TransactionScope, tx *gorm.DB) error {
			proposalID, err := gormLockWritebackProposalFirst(callbackCtx, tx, command.ExecutionID)
			if err != nil {
				return err
			}
			current, err := gormLoadWriteback(callbackCtx, tx, `SELECT `+gormWritebackColumns+` FROM change_control.writeback_execution WHERE id=? FOR UPDATE`, string(command.ExecutionID))
			if gormChangeNoRows(err) {
				return foundation.NewError(foundation.ErrorNotFound, "WRITEBACK_NOT_FOUND", false, err)
			}
			if err != nil {
				return classifyGORMWriteback(callbackCtx, err, "WRITEBACK_QUERY_FAILED")
			}
			if current.ProposalID != proposalID {
				return foundation.NewError(foundation.ErrorConsistencyViolation, "WRITEBACK_PROPOSAL_BINDING_CONFLICT", false, domain.ErrWritebackIdentityConflict)
			}
			if current.Status == domain.WritebackStatusVerifying {
				result, err = gormReplayPublishedWriteback(callbackCtx, tx, current, command)
				return err
			}
			if err := domain.ValidateWritebackPublish(current, command); err != nil {
				return writebackDomainError(err)
			}
			now, err := gormWritebackNow(callbackCtx, tx, "WRITEBACK_CLOCK_QUERY_FAILED")
			if err != nil {
				return err
			}
			command.Commit.CreatedAt = now
			commit, err := gormInsertOrReplayProposalCommit(callbackCtx, tx, current, command.Commit)
			if err != nil {
				return err
			}
			if err := gormInsertOrReplayWritebackEvent(callbackCtx, tx, current, command.Event, now); err != nil {
				return err
			}
			updated, err := gormLoadWriteback(callbackCtx, tx, `UPDATE change_control.writeback_execution SET status=?,updated_at=?,version=version+1 WHERE id=? AND version=? AND status IN (?,?) RETURNING `+gormWritebackColumns, string(domain.WritebackStatusVerifying), now, string(current.ID), command.ExpectedVersion, string(domain.WritebackStatusGitCommitted), string(domain.WritebackStatusPublishRecovery))
			if gormChangeNoRows(err) {
				return writebackDomainError(domain.ErrWritebackVersionConflict)
			}
			if err != nil {
				return classifyGORMWriteback(callbackCtx, err, "WRITEBACK_PUBLISH_STATE_FAILED")
			}
			if err := gormUpdateProposalForWriteback(callbackCtx, tx, current.ProposalID, domain.StatusVerifying, now); err != nil {
				return err
			}
			result = domain.PublishWritebackResult{Execution: updated, Commit: commit}
			return nil
		})
	if err != nil {
		return domain.PublishWritebackResult{}, err
	}
	return result, nil
}

func gormWritebackNow(ctx context.Context, tx *gorm.DB, code string) (time.Time, error) {
	row, err := gormChangeRawRow(ctx, tx, `SELECT CURRENT_TIMESTAMP`)
	if err != nil {
		return time.Time{}, classifyGORMWriteback(ctx, err, code)
	}
	var now time.Time
	if err := row.Scan(&now); err != nil {
		return time.Time{}, classifyGORMWriteback(ctx, err, code)
	}
	return now.UTC(), nil
}

func gormLoadWriteback(ctx context.Context, tx *gorm.DB, query string, arguments ...any) (domain.WritebackExecution, error) {
	row, err := gormChangeRawRow(ctx, tx, query, arguments...)
	if err != nil {
		return domain.WritebackExecution{}, err
	}
	return scanWritebackExecution(row)
}

func gormFindWritebackByKey(ctx context.Context, tx *gorm.DB, workspaceID foundation.ID, key string, lock bool) (domain.WritebackExecution, bool, error) {
	query := `SELECT ` + gormWritebackColumns + ` FROM change_control.writeback_execution WHERE workspace_id=? AND idempotency_key=?`
	if lock {
		query += ` FOR UPDATE`
	}
	execution, err := gormLoadWriteback(ctx, tx, query, string(workspaceID), key)
	if gormChangeNoRows(err) {
		return domain.WritebackExecution{}, false, nil
	}
	if err != nil {
		code := "WRITEBACK_LOOKUP_FAILED"
		if lock {
			code = "WRITEBACK_BEGIN_IDEMPOTENCY_QUERY_FAILED"
		}
		return domain.WritebackExecution{}, false, classifyGORMWriteback(ctx, err, code)
	}
	return execution, true, nil
}

func gormLockBeginAuthorizations(ctx context.Context, tx *gorm.DB, command domain.BeginWriteback) (domain.ToolAuthorization, domain.ToolAuthorization, error) {
	keys := []string{command.WriteAuthorization.IdempotencyKey, command.GitAuthorization.IdempotencyKey}
	rows, err := gormChangeRawRows(ctx, tx, `SELECT id::text FROM change_control.tool_authorization WHERE workspace_id=? AND idempotency_key=ANY(?::text[]) ORDER BY id`, string(command.WorkspaceID), pq.Array(keys))
	if err != nil {
		return domain.ToolAuthorization{}, domain.ToolAuthorization{}, classifyGORMWriteback(ctx, err, "WRITEBACK_BEGIN_AUTHORIZATION_QUERY_FAILED")
	}
	defer rows.Close()
	ids := make([]string, 0, 2)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return domain.ToolAuthorization{}, domain.ToolAuthorization{}, classifyGORMWriteback(ctx, err, "WRITEBACK_BEGIN_AUTHORIZATION_QUERY_FAILED")
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return domain.ToolAuthorization{}, domain.ToolAuthorization{}, classifyGORMWriteback(ctx, err, "WRITEBACK_BEGIN_AUTHORIZATION_QUERY_FAILED")
	}
	if len(ids) != 2 || ids[0] == ids[1] {
		return domain.ToolAuthorization{}, domain.ToolAuthorization{}, foundation.NewError(foundation.ErrorPermissionDenied, "WRITEBACK_AUTHORIZATION_NOT_FOUND", false, errors.New("both write authorizations are required"))
	}
	sort.Strings(ids)
	auths := make([]domain.ToolAuthorization, 0, 2)
	for _, id := range ids {
		row, err := gormChangeRawRow(ctx, tx, `SELECT `+gormChangeAuthorizationColumns+` FROM change_control.tool_authorization WHERE id=? FOR UPDATE`, id)
		if err != nil {
			return domain.ToolAuthorization{}, domain.ToolAuthorization{}, classifyGORMWriteback(ctx, err, "WRITEBACK_BEGIN_AUTHORIZATION_QUERY_FAILED")
		}
		auth, err := gormChangeScanAuthorization(row)
		if err != nil {
			return domain.ToolAuthorization{}, domain.ToolAuthorization{}, classifyGORMWriteback(ctx, err, "WRITEBACK_BEGIN_AUTHORIZATION_QUERY_FAILED")
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
	if err := gormValidateBeginAuthorization(writeAuth, command.WriteAuthorization); err != nil {
		return domain.ToolAuthorization{}, domain.ToolAuthorization{}, err
	}
	if err := gormValidateBeginAuthorization(gitAuth, command.GitAuthorization); err != nil {
		return domain.ToolAuthorization{}, domain.ToolAuthorization{}, err
	}
	return writeAuth, gitAuth, nil
}

func gormValidateBeginAuthorization(auth domain.ToolAuthorization, request domain.AuthorizationConsume) error {
	if err := domain.ValidateAuthorizationConsumeBinding(auth, request); err != nil {
		return foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_AUTHORIZATION_BINDING_CONFLICT", false, err)
	}
	if auth.Status != domain.AuthorizationIssued && auth.Status != domain.AuthorizationConsumed {
		return foundation.NewError(foundation.ErrorPermissionDenied, "WRITEBACK_AUTHORIZATION_NOT_USABLE", false, errors.New("authorization is not issued or consumed"))
	}
	return nil
}

func gormValidateBeginNodeLease(ctx context.Context, tx *gorm.DB, command domain.BeginWriteback, now time.Time, auth domain.ToolAuthorization) error {
	row, err := gormChangeRawRow(ctx, tx, `SELECT n.status,n.lease_owner,n.lease_until FROM workflow.run r JOIN workflow.node_run n ON n.run_id=r.id WHERE r.id=? AND n.id=? AND r.workspace_id=? FOR UPDATE OF r,n`, string(command.WorkflowRunID), string(command.NodeRunID), string(command.WorkspaceID))
	if err != nil {
		return classifyGORMWriteback(ctx, err, "WRITEBACK_WORKFLOW_CONTEXT_QUERY_FAILED")
	}
	var status, owner string
	var until *time.Time
	if err := row.Scan(&status, &owner, &until); gormChangeNoRows(err) {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "WRITEBACK_WORKFLOW_CONTEXT_INVALID", false, err)
	} else if err != nil {
		return classifyGORMWriteback(ctx, err, "WRITEBACK_WORKFLOW_CONTEXT_QUERY_FAILED")
	}
	if auth.WorkflowRunID != command.WorkflowRunID || auth.NodeRunID != command.NodeRunID || status != "running" || strings.TrimSpace(owner) != command.LeaseOwner || until == nil || !until.After(now) {
		return foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_LEASE_LOST", true, domain.ErrWritebackLeaseLost)
	}
	return nil
}

func gormConsumeBeginAuthorization(ctx context.Context, tx *gorm.DB, auth domain.ToolAuthorization, now time.Time) error {
	if auth.Status == domain.AuthorizationConsumed {
		return nil
	}
	if auth.Status != domain.AuthorizationIssued || !now.Before(auth.ExpiresAt) {
		return foundation.NewError(foundation.ErrorPermissionDenied, "WRITEBACK_AUTHORIZATION_EXPIRED", false, errors.New("authorization is expired"))
	}
	changed, err := gormChangeExec(ctx, tx, `UPDATE change_control.tool_authorization SET status='consumed',consumed_at=?,version=version+1 WHERE id=? AND status='issued'`, now, string(auth.ID))
	if err != nil {
		return classifyGORMWriteback(ctx, err, "WRITEBACK_AUTHORIZATION_CONSUME_FAILED")
	}
	if changed != 1 {
		return foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_AUTHORIZATION_CONSUME_CONFLICT", false, errors.New("authorization changed before consumption"))
	}
	return nil
}

func gormInsertWriteback(ctx context.Context, tx *gorm.DB, requested domain.WritebackExecution, onConflict bool) (domain.WritebackExecution, error) {
	query := `INSERT INTO change_control.writeback_execution(id,workspace_id,workflow_run_id,node_run_id,proposal_id,revision_id,approval_id,write_authorization_id,git_authorization_id,target_path,target_mode,base_hash,result_hash,approved_change_hash,approved_git_head,status,idempotency_key,temporary_ref,backup_ref,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	if onConflict {
		query += ` ON CONFLICT DO NOTHING`
	}
	query += ` RETURNING ` + gormWritebackColumns
	return gormLoadWriteback(ctx, tx, query, string(requested.ID), string(requested.WorkspaceID), string(requested.WorkflowRunID), string(requested.NodeRunID), string(requested.ProposalID), string(requested.RevisionID), string(requested.ApprovalID), string(requested.WriteAuthorizationID), string(requested.GitAuthorizationID), requested.TargetPath, string(domain.NormalizeTargetMode(requested.TargetMode)), requested.BaseHash, requested.ResultHash, requested.ApprovedChangeHash, requested.ApprovedGitHead, string(requested.Status), requested.IdempotencyKey, nullableString(requested.TemporaryRef), nullableString(requested.BackupRef), requested.Version, requested.CreatedAt.UTC(), requested.UpdatedAt.UTC())
}

func gormFindWritebackConflict(ctx context.Context, tx *gorm.DB, requested domain.WritebackExecution) (domain.WritebackExecution, bool, error) {
	queries := []struct {
		sql  string
		args []any
	}{{`SELECT ` + gormWritebackColumns + ` FROM change_control.writeback_execution WHERE workspace_id=? AND idempotency_key=? FOR UPDATE`, []any{string(requested.WorkspaceID), requested.IdempotencyKey}}, {`SELECT ` + gormWritebackColumns + ` FROM change_control.writeback_execution WHERE proposal_id=? AND revision_id=? FOR UPDATE`, []any{string(requested.ProposalID), string(requested.RevisionID)}}, {`SELECT ` + gormWritebackColumns + ` FROM change_control.writeback_execution WHERE write_authorization_id=? OR git_authorization_id=? FOR UPDATE`, []any{string(requested.WriteAuthorizationID), string(requested.GitAuthorizationID)}}}
	for _, query := range queries {
		execution, err := gormLoadWriteback(ctx, tx, query.sql, query.args...)
		if gormChangeNoRows(err) {
			continue
		}
		if err != nil {
			return domain.WritebackExecution{}, false, classifyGORMWriteback(ctx, err, "WRITEBACK_IDEMPOTENCY_QUERY_FAILED")
		}
		return execution, true, nil
	}
	return domain.WritebackExecution{}, false, nil
}

func gormLockWritebackProposalFirst(ctx context.Context, tx *gorm.DB, executionID foundation.ID) (foundation.ID, error) {
	row, err := gormChangeRawRow(ctx, tx, `SELECT proposal_id::text FROM change_control.writeback_execution WHERE id=?`, string(executionID))
	if err != nil {
		return "", classifyGORMWriteback(ctx, err, "WRITEBACK_QUERY_FAILED")
	}
	var proposalID string
	if err := row.Scan(&proposalID); gormChangeNoRows(err) {
		return "", foundation.NewError(foundation.ErrorNotFound, "WRITEBACK_NOT_FOUND", false, err)
	} else if err != nil {
		return "", classifyGORMWriteback(ctx, err, "WRITEBACK_QUERY_FAILED")
	}
	row, err = gormChangeRawRow(ctx, tx, `SELECT id::text FROM change_control.proposal WHERE id=? FOR UPDATE`, proposalID)
	if err != nil {
		return "", classifyGORMWriteback(ctx, err, "WRITEBACK_PROPOSAL_LOCK_FAILED")
	}
	var locked string
	if err := row.Scan(&locked); gormChangeNoRows(err) {
		return "", foundation.NewError(foundation.ErrorConsistencyViolation, "WRITEBACK_PROPOSAL_BINDING_CONFLICT", false, err)
	} else if err != nil {
		return "", classifyGORMWriteback(ctx, err, "WRITEBACK_PROPOSAL_LOCK_FAILED")
	}
	return foundation.ID(locked), nil
}

func gormUpdateProposalForWriteback(ctx context.Context, tx *gorm.DB, proposalID foundation.ID, target domain.ProposalStatus, at time.Time) error {
	expected := domain.StatusApplying
	if target == domain.StatusVerifying {
		expected = domain.StatusApplied
	}
	changed, err := gormChangeExec(ctx, tx, `UPDATE change_control.proposal SET status=?,updated_at=?,version=version+1 WHERE id=? AND status=?`, string(target), at.UTC(), string(proposalID), string(expected))
	if err != nil {
		return classifyGORMWriteback(ctx, err, "WRITEBACK_PROPOSAL_STATE_FAILED")
	}
	if changed != 1 {
		return foundation.NewError(foundation.ErrorVersionConflict, "WRITEBACK_PROPOSAL_STATE_CONFLICT", false, errors.New("proposal state does not match writeback checkpoint"))
	}
	return nil
}

func gormInsertOrReplayProposalCommit(ctx context.Context, tx *gorm.DB, execution domain.WritebackExecution, requested domain.ProposalCommit) (domain.ProposalCommit, error) {
	row, err := gormChangeRawRow(ctx, tx, `INSERT INTO change_control.proposal_commit(id,workspace_id,writeback_execution_id,proposal_id,revision_id,approval_id,git_commit,parent_git_commit,target_path,target_mode,diff_hash,result_hash,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT DO NOTHING RETURNING `+gormProposalCommitColumns, string(requested.ID), string(requested.WorkspaceID), string(requested.WritebackExecutionID), string(requested.ProposalID), string(requested.RevisionID), string(requested.ApprovalID), requested.GitCommit, requested.ParentGitCommit, requested.TargetPath, string(domain.NormalizeTargetMode(requested.TargetMode)), requested.DiffHash, requested.ResultHash, requested.CreatedAt.UTC())
	if err != nil {
		return domain.ProposalCommit{}, classifyGORMWriteback(ctx, err, "WRITEBACK_COMMIT_MAPPING_FAILED")
	}
	commit, err := scanProposalCommit(row)
	if gormChangeNoRows(err) {
		row, err = gormChangeRawRow(ctx, tx, `SELECT `+gormProposalCommitColumns+` FROM change_control.proposal_commit WHERE writeback_execution_id=? OR (proposal_id=? AND revision_id=?) OR (workspace_id=? AND git_commit=?) FOR UPDATE`, string(execution.ID), string(execution.ProposalID), string(execution.RevisionID), string(execution.WorkspaceID), execution.GitCommit)
		if err != nil {
			return domain.ProposalCommit{}, classifyGORMWriteback(ctx, err, "WRITEBACK_COMMIT_MAPPING_FAILED")
		}
		commit, err = scanProposalCommit(row)
	}
	if err != nil {
		return domain.ProposalCommit{}, classifyGORMWriteback(ctx, err, "WRITEBACK_COMMIT_MAPPING_FAILED")
	}
	if err := domain.ValidateProposalCommitBinding(execution, commit); err != nil || !sameProposalCommitIdentity(commit, requested) {
		return domain.ProposalCommit{}, writebackDomainError(domain.ErrWritebackPublishBindingConflict)
	}
	return commit, nil
}

func gormInsertOrReplayWritebackEvent(ctx context.Context, tx *gorm.DB, execution domain.WritebackExecution, requested domain.WritebackOutboxEvent, at time.Time) error {
	row, err := gormChangeRawRow(ctx, tx, `INSERT INTO workflow.outbox_event(id,workspace_id,run_id,event_type,idempotency_key,payload,occurred_at) VALUES(?,?,?,?,?,?::jsonb,?) ON CONFLICT DO NOTHING RETURNING id::text`, string(requested.ID), string(requested.WorkspaceID), string(requested.RunID), requested.Type, requested.IdempotencyKey, changeControlJSONB(requested.Payload), at.UTC())
	if err != nil {
		return classifyGORMWriteback(ctx, err, "WRITEBACK_OUTBOX_CREATE_FAILED")
	}
	var id string
	err = row.Scan(&id)
	if !gormChangeNoRows(err) {
		if err != nil {
			return classifyGORMWriteback(ctx, err, "WRITEBACK_OUTBOX_CREATE_FAILED")
		}
		return nil
	}
	return gormValidateWritebackEvent(ctx, tx, execution, requested, `SELECT id::text,workspace_id::text,run_id::text,event_type,idempotency_key,payload FROM workflow.outbox_event WHERE id=? OR idempotency_key=? FOR UPDATE`, string(requested.ID), requested.IdempotencyKey)
}

func gormReplayPublishedWriteback(ctx context.Context, tx *gorm.DB, execution domain.WritebackExecution, command domain.PublishWriteback) (domain.PublishWritebackResult, error) {
	validation := command
	validation.ExpectedVersion = execution.Version
	if err := domain.ValidateWritebackPublish(execution, validation); err != nil {
		return domain.PublishWritebackResult{}, writebackDomainError(err)
	}
	row, err := gormChangeRawRow(ctx, tx, `SELECT `+gormProposalCommitColumns+` FROM change_control.proposal_commit WHERE writeback_execution_id=? FOR UPDATE`, string(execution.ID))
	if err != nil {
		return domain.PublishWritebackResult{}, classifyGORMWriteback(ctx, err, "WRITEBACK_COMMIT_MAPPING_QUERY_FAILED")
	}
	commit, err := scanProposalCommit(row)
	if err != nil {
		return domain.PublishWritebackResult{}, classifyGORMWriteback(ctx, err, "WRITEBACK_COMMIT_MAPPING_QUERY_FAILED")
	}
	if err := domain.ValidateProposalCommitBinding(execution, commit); err != nil || !sameProposalCommitIdentity(commit, command.Commit) {
		return domain.PublishWritebackResult{}, writebackDomainError(domain.ErrWritebackPublishBindingConflict)
	}
	if err := gormValidateWritebackEvent(ctx, tx, execution, command.Event, `SELECT id::text,workspace_id::text,run_id::text,event_type,idempotency_key,payload FROM workflow.outbox_event WHERE id=? AND idempotency_key=? FOR UPDATE`, string(command.Event.ID), command.Event.IdempotencyKey); err != nil {
		return domain.PublishWritebackResult{}, err
	}
	return domain.PublishWritebackResult{Execution: execution, Commit: commit, Replayed: true}, nil
}

func gormValidateWritebackEvent(ctx context.Context, tx *gorm.DB, execution domain.WritebackExecution, requested domain.WritebackOutboxEvent, query string, arguments ...any) error {
	row, err := gormChangeRawRow(ctx, tx, query, arguments...)
	if err != nil {
		return classifyGORMWriteback(ctx, err, "WRITEBACK_OUTBOX_QUERY_FAILED")
	}
	var id, workspaceID, runID, eventType, key string
	var payload []byte
	if err := row.Scan(&id, &workspaceID, &runID, &eventType, &key, &payload); gormChangeNoRows(err) {
		return foundation.NewError(foundation.ErrorConsistencyViolation, "WRITEBACK_OUTBOX_MISSING", false, err)
	} else if err != nil {
		return classifyGORMWriteback(ctx, err, "WRITEBACK_OUTBOX_QUERY_FAILED")
	}
	if foundation.ID(id) != requested.ID || foundation.ID(workspaceID) != execution.WorkspaceID || foundation.ID(runID) != execution.WorkflowRunID || eventType != requested.Type || key != requested.IdempotencyKey || !sameJSON(payload, requested.Payload) {
		return writebackDomainError(domain.ErrWritebackPublishBindingConflict)
	}
	return nil
}
