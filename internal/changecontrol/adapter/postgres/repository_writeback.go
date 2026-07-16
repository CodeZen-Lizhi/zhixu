package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var _ domain.WritebackRepository = (*Repository)(nil)

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
		requested.BaseHash, requested.ResultHash, requested.ApprovedChangeHash, requested.ApprovedGitHead,
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

// CheckpointWritebackExecution 原子推进单步执行状态，并同步对应 Proposal 状态。
func (r *Repository) CheckpointWritebackExecution(ctx context.Context, command domain.CheckpointWriteback) (domain.WritebackExecution, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.WritebackExecution{}, classifyWriteback(err, "WRITEBACK_CHECKPOINT_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()

	current, err := scanWritebackExecution(tx.QueryRow(ctx, `SELECT `+writebackColumns+` FROM change_control.writeback_execution WHERE id=$1 FOR UPDATE`, string(command.ExecutionID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.WritebackExecution{}, foundation.NewError(foundation.ErrorNotFound, "WRITEBACK_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.WritebackExecution{}, classifyWriteback(err, "WRITEBACK_QUERY_FAILED")
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
			git_commit=$7,parent_git_commit=$8,diff_hash=$9,completed_at=$10,updated_at=$11,version=version+1
		WHERE id=$1 AND version=$12 AND status=$13
		RETURNING `+writebackColumns,
		string(current.ID), string(command.Status), nullableString(command.FailureCode), command.ManualRecoveryRequired,
		nullableString(command.TemporaryRef), nullableString(command.BackupRef), nullableString(command.GitCommit),
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

	current, err := scanWritebackExecution(tx.QueryRow(ctx, `SELECT `+writebackColumns+` FROM change_control.writeback_execution WHERE id=$1 FOR UPDATE`, string(command.ExecutionID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.PublishWritebackResult{}, foundation.NewError(foundation.ErrorNotFound, "WRITEBACK_NOT_FOUND", false, err)
	}
	if err != nil {
		return domain.PublishWritebackResult{}, classifyWriteback(err, "WRITEBACK_QUERY_FAILED")
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

const writebackColumns = `id::text,workspace_id::text,workflow_run_id::text,node_run_id::text,proposal_id::text,revision_id::text,approval_id::text,write_authorization_id::text,git_authorization_id::text,target_path,base_hash,result_hash,approved_change_hash,approved_git_head,git_commit,parent_git_commit,diff_hash,status,idempotency_key,failure_code,manual_recovery_required,temporary_ref,backup_ref,version,created_at,updated_at,completed_at`

const writebackInsert = `INSERT INTO change_control.writeback_execution(
	id,workspace_id,workflow_run_id,node_run_id,proposal_id,revision_id,approval_id,write_authorization_id,git_authorization_id,
	target_path,base_hash,result_hash,approved_change_hash,approved_git_head,status,idempotency_key,temporary_ref,backup_ref,version,created_at,updated_at
) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`

const proposalCommitColumns = `id::text,workspace_id::text,writeback_execution_id::text,proposal_id::text,revision_id::text,approval_id::text,git_commit,parent_git_commit,target_path,diff_hash,result_hash,created_at`

func scanWritebackExecution(row pgx.Row) (domain.WritebackExecution, error) {
	var execution domain.WritebackExecution
	var id, workspaceID, runID, nodeID, proposalID, revisionID, approvalID, writeAuthorizationID, gitAuthorizationID string
	var status string
	var gitCommit, parentGitCommit, diffHash, failureCode, temporaryRef, backupRef *string
	err := row.Scan(
		&id, &workspaceID, &runID, &nodeID, &proposalID, &revisionID, &approvalID, &writeAuthorizationID, &gitAuthorizationID,
		&execution.TargetPath, &execution.BaseHash, &execution.ResultHash, &execution.ApprovedChangeHash, &execution.ApprovedGitHead,
		&gitCommit, &parentGitCommit, &diffHash, &status, &execution.IdempotencyKey, &failureCode, &execution.ManualRecoveryRequired,
		&temporaryRef, &backupRef, &execution.Version, &execution.CreatedAt, &execution.UpdatedAt, &execution.CompletedAt,
	)
	if err != nil {
		return domain.WritebackExecution{}, err
	}
	execution.ID, execution.WorkspaceID, execution.WorkflowRunID, execution.NodeRunID = foundation.ID(id), foundation.ID(workspaceID), foundation.ID(runID), foundation.ID(nodeID)
	execution.ProposalID, execution.RevisionID, execution.ApprovalID = foundation.ID(proposalID), foundation.ID(revisionID), foundation.ID(approvalID)
	execution.WriteAuthorizationID, execution.GitAuthorizationID = foundation.ID(writeAuthorizationID), foundation.ID(gitAuthorizationID)
	execution.Status = domain.WritebackStatus(status)
	execution.GitCommit, execution.ParentGitCommit, execution.DiffHash = stringValue(gitCommit), stringValue(parentGitCommit), stringValue(diffHash)
	execution.FailureCode, execution.TemporaryRef, execution.BackupRef = stringValue(failureCode), stringValue(temporaryRef), stringValue(backupRef)
	return execution, nil
}

func scanProposalCommit(row pgx.Row) (domain.ProposalCommit, error) {
	var commit domain.ProposalCommit
	var id, workspaceID, executionID, proposalID, revisionID, approvalID string
	if err := row.Scan(&id, &workspaceID, &executionID, &proposalID, &revisionID, &approvalID, &commit.GitCommit, &commit.ParentGitCommit, &commit.TargetPath, &commit.DiffHash, &commit.ResultHash, &commit.CreatedAt); err != nil {
		return domain.ProposalCommit{}, err
	}
	commit.ID, commit.WorkspaceID, commit.WritebackExecutionID = foundation.ID(id), foundation.ID(workspaceID), foundation.ID(executionID)
	commit.ProposalID, commit.RevisionID, commit.ApprovalID = foundation.ID(proposalID), foundation.ID(revisionID), foundation.ID(approvalID)
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
		INSERT INTO change_control.proposal_commit(id,workspace_id,writeback_execution_id,proposal_id,revision_id,approval_id,git_commit,parent_git_commit,target_path,diff_hash,result_hash,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT DO NOTHING RETURNING `+proposalCommitColumns,
		string(requested.ID), string(requested.WorkspaceID), string(requested.WritebackExecutionID), string(requested.ProposalID),
		string(requested.RevisionID), string(requested.ApprovalID), requested.GitCommit, requested.ParentGitCommit,
		requested.TargetPath, requested.DiffHash, requested.ResultHash, requested.CreatedAt.UTC()))
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
		TargetPath: command.TargetPath, BaseHash: command.BaseHash, ResultHash: command.ResultHash,
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
		left.TargetPath == right.TargetPath && strings.EqualFold(left.DiffHash, right.DiffHash) && strings.EqualFold(left.ResultHash, right.ResultHash)
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
