package postgres

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"gorm.io/gorm"
)

// GORMCompletionRepository 拥有 Activation、Delivery 和 Change Control 完成的唯一事务。
type GORMCompletionRepository struct {
	database   *gorm.DB
	unitOfWork foundation.UnitOfWork
	completion application.ScopedReindexCompletion
}

var _ application.CompletionStore = (*GORMCompletionRepository)(nil)

// NewGORMCompletionRepository 要求同 Pool 的两阶段 Change Control owner 能力。
func NewGORMCompletionRepository(pool *platformpostgres.Pool, completion application.ScopedReindexCompletion) (*GORMCompletionRepository, error) {
	database, unitOfWork, err := gormRetrievalDependencies(pool)
	if err != nil {
		return nil, err
	}
	if nilGORMRetrievalDependency(completion) {
		return nil, dependency("REINDEX_COMPLETION_DEPENDENCY_UNAVAILABLE", errors.New("scoped completion owner is unavailable"))
	}
	return &GORMCompletionRepository{database: database, unitOfWork: unitOfWork, completion: completion}, nil
}

// CompleteReindexTx 原子切换 Active 并完成 Delivery/Execution/Proposal；未知提交结果只允许后续精确重放。
func (repository *GORMCompletionRepository) CompleteReindexTx(ctx context.Context, command domain.CompleteReindexCommand) (domain.CompleteReindexResult, error) {
	if repository == nil || !validGORMRetrievalDatabase(repository.database) ||
		nilGORMRetrievalDependency(repository.unitOfWork) || nilGORMRetrievalDependency(repository.completion) {
		return domain.CompleteReindexResult{}, dependency("REINDEX_COMPLETION_DEPENDENCY_UNAVAILABLE", errors.New("GORM completion repository is unavailable"))
	}
	if ctx == nil {
		return domain.CompleteReindexResult{}, foundation.NewError(foundation.ErrorInvalidInput, "RETRIEVAL_CONTEXT_INVALID", false, errors.New("retrieval context is nil"))
	}
	if err := domain.ValidateCompleteReindexCommand(command); err != nil {
		return domain.CompleteReindexResult{}, err
	}
	identity, err := repository.loadCompletionIdentity(ctx, command.Fence.DeliveryID)
	if err != nil {
		return domain.CompleteReindexResult{}, err
	}
	if identity.workspaceID != command.WorkspaceID {
		return domain.CompleteReindexResult{}, consistency("REINDEX_COMPLETION_BINDING_CONFLICT", errors.New("completion workspace does not match delivery"))
	}
	var result domain.CompleteReindexResult
	err = withinGORMRetrieval(ctx, repository.unitOfWork, foundation.TransactionOptions{},
		"REINDEX_COMPLETION_TRANSACTION_FAILED", "REINDEX_COMPLETION_COMMIT_FAILED",
		func(callbackCtx context.Context, tx *gorm.DB, scope foundation.TransactionScope) error {
			var err error
			result, err = repository.completeScoped(callbackCtx, tx, scope, identity, command)
			return err
		})
	if err != nil {
		return domain.CompleteReindexResult{}, err
	}
	return result, nil
}

func (repository *GORMCompletionRepository) completeScoped(ctx context.Context, tx *gorm.DB, scope foundation.TransactionScope, identity completionIdentity, command domain.CompleteReindexCommand) (domain.CompleteReindexResult, error) {
	if _, err := gormRetrievalExec(ctx, tx, `SELECT pg_advisory_xact_lock(hashtextextended(?,0))`, string(command.WorkspaceID)); err != nil {
		return domain.CompleteReindexResult{}, classifyGORMRetrieval(ctx, err, "REINDEX_COMPLETION_WORKSPACE_LOCK_FAILED")
	}
	facts, err := repository.completion.LockReindexCompletionScoped(ctx, scope, application.ReindexCompletionLockRequest{
		WorkspaceID: command.WorkspaceID, WritebackExecutionID: identity.executionID,
	})
	if err != nil {
		return domain.CompleteReindexResult{}, err
	}
	if facts.WorkspaceID != identity.workspaceID || facts.WritebackExecutionID != identity.executionID || facts.ProposalID != identity.proposalID {
		return domain.CompleteReindexResult{}, consistency("REINDEX_COMPLETION_BINDING_CONFLICT", errors.New("locked owner identity does not match delivery"))
	}
	proposal, execution := gormCompletionOwnerFacts(facts)
	delivery, err := gormDeliveryLock(ctx, tx, command.Fence.DeliveryID)
	if err != nil {
		return domain.CompleteReindexResult{}, err
	}
	if delivery.CurrentAttemptID == nil {
		return domain.CompleteReindexResult{}, consistency("REINDEX_COMPLETION_ATTEMPT_MISSING", errors.New("completion delivery has no current attempt"))
	}
	attempt, err := gormDeliveryCurrentAttempt(ctx, tx, delivery)
	if err != nil {
		return domain.CompleteReindexResult{}, err
	}
	locked, err := gormLockCompletionIndexes(ctx, tx, delivery)
	if err != nil {
		return domain.CompleteReindexResult{}, err
	}
	if delivery.Status == domain.DeliveryStatusSucceeded {
		if facts.Disposition != application.ReindexCompletionReplay {
			return domain.CompleteReindexResult{}, conflict("REINDEX_COMPLETION_REPLAY_CONFLICT", errors.New("terminal owner disposition does not permit replay"))
		}
		return gormReplayCompletedReindex(ctx, tx, command, proposal, execution, delivery, attempt, locked)
	}
	if !fenceMatches(command.Fence, delivery, attempt) {
		return domain.CompleteReindexResult{}, conflict("REINDEX_COMPLETION_FENCE_STALE", errors.New("completion fence is stale"))
	}
	row, err := gormRetrievalRawRow(ctx, tx, `SELECT clock_timestamp()`)
	if err != nil {
		return domain.CompleteReindexResult{}, classifyGORMRetrieval(ctx, err, "REINDEX_COMPLETION_CLOCK_QUERY_FAILED")
	}
	var databaseNow time.Time
	if err := row.Scan(&databaseNow); err != nil {
		return domain.CompleteReindexResult{}, classifyGORMRetrieval(ctx, err, "REINDEX_COMPLETION_CLOCK_QUERY_FAILED")
	}
	databaseNow = databaseNow.UTC()
	if !databaseNow.Before(attempt.LeaseUntil) {
		return domain.CompleteReindexResult{}, completionLeaseExpired()
	}
	if facts.Disposition != application.ReindexCompletionCurrent {
		return domain.CompleteReindexResult{}, consistency("REINDEX_COMPLETION_GATE_FAILED", errors.New("owner lifecycle is not current"))
	}
	if err := validateFirstCompletionState(proposal, execution, delivery, attempt, locked.target); err != nil {
		return domain.CompleteReindexResult{}, err
	}
	if err := gormValidateCompletionReadOnlyFacts(ctx, tx, proposal, execution, delivery, attempt, locked.target); err != nil {
		return domain.CompleteReindexResult{}, err
	}
	activationCommand := domain.ActivationCommand{
		ActivationID: command.ActivationID, WorkspaceID: command.WorkspaceID,
		TargetIndexVersionID: locked.target.ID, ExpectedTargetVersion: locked.target.Version,
		IdempotencyKey: command.ActivationIdempotencyKey, ReasonCode: command.ActivationReasonCode, At: databaseNow,
	}
	if locked.current != nil {
		activationCommand.ExpectedCurrentIndexVersionID = &locked.current.ID
		activationCommand.ExpectedCurrentVersion = &locked.current.Version
	}
	activated, err := gormRetrievalActivateTx(ctx, tx, activationCommand, domain.ActivationKindActivate, &locked)
	if err != nil {
		return domain.CompleteReindexResult{}, err
	}
	row, err = gormRetrievalRawRow(ctx, tx, `WITH database_clock AS (SELECT clock_timestamp() AS now)
		UPDATE retrieval.reindex_delivery_attempt SET status='succeeded',ended_at=database_clock.now
		FROM database_clock
		WHERE id=? AND delivery_id=? AND status='processing' AND database_clock.now < lease_until
		RETURNING `+deliveryAttemptColumns, string(attempt.ID), string(delivery.ID))
	if err == nil {
		attempt, err = scanDeliveryAttempt(row)
	}
	if gormRetrievalNoRows(err) {
		return domain.CompleteReindexResult{}, completionLeaseExpired()
	}
	if err != nil {
		return domain.CompleteReindexResult{}, classifyGORMRetrieval(ctx, err, "REINDEX_COMPLETION_ATTEMPT_UPDATE_FAILED")
	}
	row, err = gormRetrievalRawRow(ctx, tx, `UPDATE retrieval.reindex_delivery SET
		status='succeeded',activation_id=?,completed_at=?,version=version+1,updated_at=?
		WHERE id=? AND status='processing' AND version=? RETURNING `+deliveryColumns,
		string(activated.Activation.ID), databaseNow, databaseNow, string(delivery.ID), command.Fence.DeliveryVersion)
	if err == nil {
		delivery, err = scanDelivery(row)
	}
	if err != nil {
		return domain.CompleteReindexResult{}, classifyGORMRetrieval(ctx, err, "REINDEX_COMPLETION_DELIVERY_UPDATE_FAILED")
	}
	if err := repository.completion.CompleteReindexScoped(ctx, scope, application.ReindexCompletionTransition{
		WorkspaceID: command.WorkspaceID, ProposalID: proposal.id, ExpectedProposalVersion: proposal.version,
		WritebackExecutionID: execution.id, ExpectedExecutionVersion: execution.version, CompletedAt: databaseNow,
	}); err != nil {
		return domain.CompleteReindexResult{}, err
	}
	return domain.CompleteReindexResult{
		Delivery: delivery, Attempt: attempt, Activation: activated.Activation,
		ActiveIndexVersion: activated.ActiveIndexVersion, PreviousIndexVersion: activated.PreviousIndexVersion,
	}, nil
}

func (repository *GORMCompletionRepository) loadCompletionIdentity(ctx context.Context, id foundation.ID) (completionIdentity, error) {
	row, err := gormRetrievalRawRow(ctx, repository.database, `SELECT delivery.workspace_id::text,delivery.writeback_execution_id::text,execution.proposal_id::text
		FROM retrieval.reindex_delivery delivery
		JOIN change_control.writeback_execution execution ON execution.id=delivery.writeback_execution_id
		WHERE delivery.id=?`, string(id))
	var identity completionIdentity
	if err == nil {
		err = row.Scan(&identity.workspaceID, &identity.executionID, &identity.proposalID)
	}
	if gormRetrievalNoRows(err) {
		return completionIdentity{}, notFound("REINDEX_COMPLETION_DELIVERY_NOT_FOUND", err)
	}
	if err != nil {
		return completionIdentity{}, classifyGORMRetrieval(ctx, err, "REINDEX_COMPLETION_IDENTITY_QUERY_FAILED")
	}
	return identity, nil
}

func gormCompletionOwnerFacts(facts application.ReindexCompletionFacts) (completionProposal, completionExecution) {
	return completionProposal{
			id: facts.ProposalID, workspaceID: facts.WorkspaceID, workflowRunID: facts.ProposalWorkflowRunID,
			status: string(facts.ProposalStatus), version: facts.ProposalVersion,
		}, completionExecution{
			id: facts.WritebackExecutionID, workspaceID: facts.WorkspaceID, workflowRunID: facts.WorkflowRunID, nodeRunID: facts.NodeRunID,
			proposalID: facts.ProposalID, revisionID: facts.RevisionID, approvalID: facts.ApprovalID,
			targetPath: facts.TargetPath, resultHash: facts.ResultHash, gitCommit: facts.GitCommit,
			status: string(facts.ExecutionStatus), cleanupCompletedAt: facts.CleanupCompletedAt, version: facts.ExecutionVersion,
		}
}

func gormLockCompletionIndexes(ctx context.Context, tx *gorm.DB, delivery domain.Delivery) (activationLockedIndexes, error) {
	if delivery.IndexVersionID == nil {
		return activationLockedIndexes{}, consistency("REINDEX_COMPLETION_INDEX_MISSING", errors.New("completion delivery has no index checkpoint"))
	}
	ids := []foundation.ID{*delivery.IndexVersionID}
	row, err := gormRetrievalRawRow(ctx, tx, `SELECT id::text FROM retrieval.index_version WHERE workspace_id=? AND status='active'`, string(delivery.WorkspaceID))
	var activeID foundation.ID
	if err == nil {
		err = row.Scan(&activeID)
	}
	if err == nil && activeID != *delivery.IndexVersionID {
		ids = append(ids, activeID)
	} else if err != nil && !gormRetrievalNoRows(err) {
		return activationLockedIndexes{}, classifyGORMRetrieval(ctx, err, "REINDEX_COMPLETION_ACTIVE_QUERY_FAILED")
	}
	slices.Sort(ids)
	var locked activationLockedIndexes
	// 最多两个 Index，排序后逐一取锁以保留既有 Completion 锁序。
	for _, id := range ids {
		value, err := gormRetrievalLoadIndex(ctx, tx, delivery.WorkspaceID, id, true)
		if gormRetrievalNoRows(err) {
			return activationLockedIndexes{}, notFound("RETRIEVAL_INDEX_NOT_FOUND", err)
		}
		if err != nil {
			return activationLockedIndexes{}, classifyGORMRetrieval(ctx, err, "RETRIEVAL_INDEX_QUERY_FAILED")
		}
		if id == *delivery.IndexVersionID {
			locked.target = value
		} else {
			locked.current = &value
		}
	}
	return locked, nil
}

func gormValidateCompletionReadOnlyFacts(ctx context.Context, tx *gorm.DB, proposal completionProposal, execution completionExecution, delivery domain.Delivery, attempt domain.DeliveryAttempt, target domain.IndexVersion) error {
	row, err := gormRetrievalRawRow(ctx, tx, `SELECT event.event_type,event.payload::text,event.published_at,
		commit_mapping.proposal_id::text,commit_mapping.revision_id::text,commit_mapping.approval_id::text,
		commit_mapping.git_commit,commit_mapping.target_path,commit_mapping.result_hash,
		workflow_run.status,workflow_node.status,source_version.content_hash,
		ingestion_attempt.status,ingestion_attempt.security_status,
		EXISTS(SELECT 1 FROM retrieval.index_manifest_source source_manifest
			WHERE source_manifest.index_version_id=? AND source_manifest.source_version_id=?
			AND source_manifest.parse_projection_id=? AND source_manifest.selection_status='included')
		FROM workflow.outbox_event event
		JOIN change_control.proposal_commit commit_mapping ON commit_mapping.writeback_execution_id=?
		JOIN workflow.run workflow_run ON workflow_run.id=?
		JOIN workflow.node_run workflow_node ON workflow_node.id=? AND workflow_node.run_id=workflow_run.id
		JOIN core.source_version source_version ON source_version.id=?
		JOIN ingestion.attempt ingestion_attempt ON ingestion_attempt.id=?
			AND ingestion_attempt.workspace_id=? AND ingestion_attempt.source_version_id=?
			AND ingestion_attempt.parse_projection_id=?
		WHERE event.id=? AND event.workspace_id=?
			AND commit_mapping.workspace_id=? AND commit_mapping.proposal_id=?
			AND commit_mapping.revision_id=? AND commit_mapping.approval_id=?`,
		string(target.ID), string(*delivery.SourceVersionID), string(*delivery.ParseProjectionID),
		string(execution.id), string(execution.workflowRunID), string(execution.nodeRunID), string(*delivery.SourceVersionID),
		string(*attempt.IngestionAttemptID), string(delivery.WorkspaceID), string(*delivery.SourceVersionID), string(*delivery.ParseProjectionID),
		string(delivery.OutboxEventID), string(delivery.WorkspaceID), string(delivery.WorkspaceID),
		string(execution.proposalID), string(execution.revisionID), string(execution.approvalID))
	if err != nil {
		return classifyGORMRetrieval(ctx, err, "REINDEX_COMPLETION_FACT_QUERY_FAILED")
	}
	facts, err := scanCompletionReadOnlyFacts(row)
	if err != nil {
		return classifyGORMRetrieval(ctx, err, "REINDEX_COMPLETION_FACT_QUERY_FAILED")
	}
	return validateCompletionFacts(proposal, execution, delivery, facts)
}

func gormReplayCompletedReindex(ctx context.Context, tx *gorm.DB, command domain.CompleteReindexCommand, proposal completionProposal, execution completionExecution, delivery domain.Delivery, attempt domain.DeliveryAttempt, locked activationLockedIndexes) (domain.CompleteReindexResult, error) {
	if err := validateCompletedReindexState(command, proposal, execution, delivery, attempt, locked.target); err != nil {
		return domain.CompleteReindexResult{}, err
	}
	activation, found, err := gormRetrievalLoadActivation(ctx, tx, command.WorkspaceID, command.ActivationIdempotencyKey)
	if err != nil {
		return domain.CompleteReindexResult{}, err
	}
	if !found || activation.ID != *delivery.ActivationID || activation.Kind != domain.ActivationKindActivate ||
		activation.TargetIndexVersionID != locked.target.ID || activation.ReasonCode != command.ActivationReasonCode {
		return domain.CompleteReindexResult{}, conflict("REINDEX_COMPLETION_REPLAY_CONFLICT", errors.New("terminal activation binding is inconsistent"))
	}
	if err := gormValidateCompletionReadOnlyFacts(ctx, tx, proposal, execution, delivery, attempt, locked.target); err != nil {
		return domain.CompleteReindexResult{}, err
	}
	historicalTarget := activationIndexSnapshot(locked.target, domain.IndexStatusActive, activation.TargetVersion, activation.CreatedAt)
	var previous *domain.IndexVersion
	if activation.PreviousIndexVersionID != nil {
		value, err := gormRetrievalLoadIndex(ctx, tx, command.WorkspaceID, *activation.PreviousIndexVersionID, false)
		if err != nil || activation.PreviousVersion == nil {
			return domain.CompleteReindexResult{}, conflict("REINDEX_COMPLETION_REPLAY_CONFLICT", errors.New("terminal previous index binding is inconsistent"))
		}
		historicalPrevious := activationIndexSnapshot(value, domain.IndexStatusRetiring, *activation.PreviousVersion, activation.CreatedAt)
		previous = &historicalPrevious
	}
	return domain.CompleteReindexResult{
		Delivery: delivery, Attempt: attempt, Activation: activation,
		ActiveIndexVersion: historicalTarget, PreviousIndexVersion: previous, Replayed: true,
	}, nil
}
