package postgres

import (
	"context"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/changecontrol/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	retrievalapp "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	reindexcontract "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/contract"
	"gorm.io/gorm"
)

var (
	_ retrievalapp.ScopedReindexBindingVerifier = (*GORMRepository)(nil)
	_ retrievalapp.ScopedReindexCompletion      = (*GORMRepository)(nil)
)

// VerifyReindexBindingScoped 读取执行与不可变 Commit 映射，保持派发的完整身份绑定。
func (repository *GORMRepository) VerifyReindexBindingScoped(ctx context.Context, scope foundation.TransactionScope, request reindexcontract.RequestV1) (retrievalapp.ReindexBindingFacts, error) {
	transaction, err := repository.reindexTransaction(ctx, scope)
	if err != nil {
		return retrievalapp.ReindexBindingFacts{}, err
	}
	requested := reindexcontract.Binding{
		WorkspaceID: request.WorkspaceID, WorkflowRunID: request.WorkflowRunID, NodeRunID: request.NodeRunID,
		ProposalID: request.ProposalID, RevisionID: request.RevisionID, ApprovalID: request.ApprovalID,
		WritebackExecutionID: request.WritebackExecutionID, TargetPath: request.TargetPath,
		ResultHash: request.ResultHash, GitCommit: request.GitCommit,
	}
	if err := reindexcontract.ValidateBinding(request, requested); err != nil {
		return retrievalapp.ReindexBindingFacts{}, reindexBindingInvalid(err)
	}
	var facts retrievalapp.ReindexBindingFacts
	row, err := gormChangeRawRow(ctx, transaction, `SELECT
		execution.workspace_id::text,execution.workflow_run_id::text,execution.node_run_id::text,
		execution.proposal_id::text,execution.revision_id::text,execution.approval_id::text,
		execution.id::text,execution.target_path,execution.result_hash,execution.git_commit,
		execution.status,proposal.status,
		commit_mapping.workspace_id::text,execution.workflow_run_id::text,execution.node_run_id::text,
		commit_mapping.proposal_id::text,commit_mapping.revision_id::text,commit_mapping.approval_id::text,
		commit_mapping.writeback_execution_id::text,commit_mapping.target_path,commit_mapping.result_hash,commit_mapping.git_commit
		FROM change_control.writeback_execution execution
		JOIN change_control.proposal proposal ON proposal.id=execution.proposal_id AND proposal.workspace_id=execution.workspace_id
		JOIN change_control.proposal_commit commit_mapping ON commit_mapping.writeback_execution_id=execution.id
		WHERE execution.id=? AND execution.workspace_id=?`, string(request.WritebackExecutionID), string(request.WorkspaceID))
	if err == nil {
		err = row.Scan(
			&facts.Execution.WorkspaceID, &facts.Execution.WorkflowRunID, &facts.Execution.NodeRunID,
			&facts.Execution.ProposalID, &facts.Execution.RevisionID, &facts.Execution.ApprovalID,
			&facts.Execution.WritebackExecutionID, &facts.Execution.TargetPath, &facts.Execution.ResultHash, &facts.Execution.GitCommit,
			&facts.ExecutionStatus, &facts.ProposalStatus,
			&facts.Commit.WorkspaceID, &facts.Commit.WorkflowRunID, &facts.Commit.NodeRunID,
			&facts.Commit.ProposalID, &facts.Commit.RevisionID, &facts.Commit.ApprovalID,
			&facts.Commit.WritebackExecutionID, &facts.Commit.TargetPath, &facts.Commit.ResultHash, &facts.Commit.GitCommit,
		)
	}
	if gormChangeNoRows(err) {
		return retrievalapp.ReindexBindingFacts{}, reindexBindingInvalid(errors.New("writeback execution or commit mapping is missing"))
	}
	if err != nil {
		return retrievalapp.ReindexBindingFacts{}, classifyReindexOwner(err, "REINDEX_WRITEBACK_BINDING_QUERY_FAILED")
	}
	if err := reindexcontract.ValidateBinding(request, facts.Execution); err != nil {
		return retrievalapp.ReindexBindingFacts{}, reindexBindingInvalid(err)
	}
	if err := reindexcontract.ValidateBinding(request, facts.Commit); err != nil {
		return retrievalapp.ReindexBindingFacts{}, reindexBindingInvalid(err)
	}
	return facts, nil
}

// LockReindexCompletionScoped 按 Proposal、Execution 的既有顺序锁定完成事实。
// 调用方先持有 Workspace 锁，并继续在同一 scope 中完成 Delivery 和索引切换。
func (repository *GORMRepository) LockReindexCompletionScoped(ctx context.Context, scope foundation.TransactionScope, request retrievalapp.ReindexCompletionLockRequest) (retrievalapp.ReindexCompletionFacts, error) {
	transaction, err := repository.reindexTransaction(ctx, scope)
	if err != nil {
		return retrievalapp.ReindexCompletionFacts{}, err
	}
	if err := validateReindexOwnerIDs(request.WorkspaceID, request.WritebackExecutionID); err != nil {
		return retrievalapp.ReindexCompletionFacts{}, err
	}
	var proposalID foundation.ID
	row, err := gormChangeRawRow(ctx, transaction, `SELECT proposal_id::text
		FROM change_control.writeback_execution WHERE id=? AND workspace_id=?`, string(request.WritebackExecutionID), string(request.WorkspaceID))
	if err == nil {
		err = row.Scan(&proposalID)
	}
	if err != nil {
		return retrievalapp.ReindexCompletionFacts{}, classifyReindexOwner(err, "REINDEX_COMPLETION_IDENTITY_QUERY_FAILED")
	}
	var facts retrievalapp.ReindexCompletionFacts
	var workflowRunID *string
	row, err = gormChangeRawRow(ctx, transaction, `SELECT id::text,workspace_id::text,workflow_run_id::text,status,version
		FROM change_control.proposal WHERE id=? AND workspace_id=? FOR UPDATE`, string(proposalID), string(request.WorkspaceID))
	if err == nil {
		err = row.Scan(&facts.ProposalID, &facts.WorkspaceID, &workflowRunID, &facts.ProposalStatus, &facts.ProposalVersion)
	}
	if err != nil {
		return retrievalapp.ReindexCompletionFacts{}, classifyReindexOwner(err, "REINDEX_COMPLETION_PROPOSAL_LOCK_FAILED")
	}
	if workflowRunID != nil {
		id := foundation.ID(*workflowRunID)
		facts.ProposalWorkflowRunID = &id
	}
	var executionWorkspaceID, executionProposalID foundation.ID
	row, err = gormChangeRawRow(ctx, transaction, `SELECT id::text,workspace_id::text,workflow_run_id::text,node_run_id::text,
		proposal_id::text,revision_id::text,approval_id::text,target_path,result_hash,git_commit,status,cleanup_completed_at,version
		FROM change_control.writeback_execution WHERE id=? AND workspace_id=? FOR UPDATE`, string(request.WritebackExecutionID), string(request.WorkspaceID))
	if err == nil {
		err = row.Scan(&facts.WritebackExecutionID, &executionWorkspaceID, &facts.WorkflowRunID, &facts.NodeRunID,
			&executionProposalID, &facts.RevisionID, &facts.ApprovalID, &facts.TargetPath, &facts.ResultHash, &facts.GitCommit,
			&facts.ExecutionStatus, &facts.CleanupCompletedAt, &facts.ExecutionVersion)
	}
	if err != nil {
		return retrievalapp.ReindexCompletionFacts{}, classifyReindexOwner(err, "REINDEX_COMPLETION_EXECUTION_LOCK_FAILED")
	}
	if executionWorkspaceID != facts.WorkspaceID || executionProposalID != facts.ProposalID ||
		facts.ProposalVersion < 1 || facts.ExecutionVersion < 1 {
		return retrievalapp.ReindexCompletionFacts{}, foundation.NewError(foundation.ErrorConsistencyViolation,
			"REINDEX_COMPLETION_BINDING_CONFLICT", false, errors.New("locked writeback and proposal identities differ"))
	}
	facts.Disposition = retrievalapp.ReindexCompletionStale
	if facts.ProposalStatus == domain.StatusVerifying && facts.ExecutionStatus == domain.WritebackStatusVerifying {
		facts.Disposition = retrievalapp.ReindexCompletionCurrent
	} else if facts.ProposalStatus == domain.StatusCompleted && facts.ExecutionStatus == domain.WritebackStatusCompleted {
		facts.Disposition = retrievalapp.ReindexCompletionReplay
	}
	return facts, nil
}

// CompleteReindexScoped 在调用方事务中按版本 CAS 完成 Execution 与 Proposal。
// 任一更新失败必须返回错误，由拥有索引和 Delivery 的外层事务一起回滚。
func (repository *GORMRepository) CompleteReindexScoped(ctx context.Context, scope foundation.TransactionScope, command retrievalapp.ReindexCompletionTransition) error {
	transaction, err := repository.reindexTransaction(ctx, scope)
	if err != nil {
		return err
	}
	if err := validateReindexOwnerIDs(command.WorkspaceID, command.ProposalID, command.WritebackExecutionID); err != nil {
		return err
	}
	if command.ExpectedExecutionVersion < 1 || command.ExpectedProposalVersion < 1 || command.CompletedAt.IsZero() {
		return foundation.NewError(foundation.ErrorInvalidInput, "REINDEX_COMPLETION_TRANSITION_INVALID", false,
			errors.New("reindex completion version or database time is invalid"))
	}
	completedAt := command.CompletedAt.UTC()
	result := transaction.WithContext(ctx).Table("change_control.writeback_execution").
		Where("id=? AND workspace_id=? AND proposal_id=? AND status=? AND version=?", string(command.WritebackExecutionID),
			string(command.WorkspaceID), string(command.ProposalID), string(domain.WritebackStatusVerifying), command.ExpectedExecutionVersion).
		Updates(map[string]any{"status": string(domain.WritebackStatusCompleted), "completed_at": completedAt,
			"version": gorm.Expr("version+1"), "updated_at": completedAt})
	if result.Error != nil {
		return classifyReindexOwner(result.Error, "REINDEX_COMPLETION_EXECUTION_UPDATE_FAILED")
	}
	if result.RowsAffected != 1 {
		return foundation.NewError(foundation.ErrorVersionConflict, "REINDEX_COMPLETION_EXECUTION_STALE", false,
			errors.New("writeback execution changed during completion"))
	}
	result = transaction.WithContext(ctx).Table("change_control.proposal").
		Where("id=? AND workspace_id=? AND status=? AND version=?", string(command.ProposalID), string(command.WorkspaceID),
			string(domain.StatusVerifying), command.ExpectedProposalVersion).
		Updates(map[string]any{"status": string(domain.StatusCompleted), "version": gorm.Expr("version+1"), "updated_at": completedAt})
	if result.Error != nil {
		return classifyReindexOwner(result.Error, "REINDEX_COMPLETION_PROPOSAL_UPDATE_FAILED")
	}
	if result.RowsAffected != 1 {
		return foundation.NewError(foundation.ErrorVersionConflict, "REINDEX_COMPLETION_PROPOSAL_STALE", false,
			errors.New("proposal changed during completion"))
	}
	return nil
}

func (repository *GORMRepository) reindexTransaction(ctx context.Context, scope foundation.TransactionScope) (*gorm.DB, error) {
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	transaction, err := platformpostgres.GORMTransaction(scope)
	if err != nil {
		return nil, changeControlGORMUnavailable(err)
	}
	return transaction.WithContext(ctx), nil
}

func validateReindexOwnerIDs(ids ...foundation.ID) error {
	for _, id := range ids {
		parsed, err := foundation.ParseID(string(id))
		if err != nil || parsed != id {
			return foundation.NewError(foundation.ErrorInvalidInput, "REINDEX_COMPLETION_BINDING_INVALID", false,
				errors.New("reindex completion identity is invalid"))
		}
	}
	return nil
}

func reindexBindingInvalid(err error) error {
	return foundation.NewError(foundation.ErrorNonRetryableFailure, "REINDEX_OUTBOX_CONTRACT_INVALID", false, err)
}

func classifyReindexOwner(err error, code string) error {
	if err == nil {
		return nil
	}
	kind, retryable := foundation.ErrorDependencyUnavailable, true
	if gormChangeNoRows(err) {
		kind, retryable = foundation.ErrorNotFound, false
	} else {
		switch platformpostgres.SQLState(err) {
		case "40001", "40P01", "55P03":
			kind = foundation.ErrorRetryableFailure
		case "23505":
			kind, retryable = foundation.ErrorVersionConflict, false
		case "23503", "23514", "55000":
			kind, retryable = foundation.ErrorConsistencyViolation, false
		}
	}
	return foundation.NewError(kind, code, retryable, err)
}
