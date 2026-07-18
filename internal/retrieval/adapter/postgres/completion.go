package postgres

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	reindexcontract "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/contract"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/jackc/pgx/v5"
)

var _ application.CompletionStore = (*Repository)(nil)

type completionIdentity struct {
	workspaceID foundation.ID
	executionID foundation.ID
	proposalID  foundation.ID
}

type completionProposal struct {
	id            foundation.ID
	workspaceID   foundation.ID
	workflowRunID *foundation.ID
	status        string
	version       int64
}

type completionExecution struct {
	id, workspaceID, workflowRunID, nodeRunID foundation.ID
	proposalID, revisionID, approvalID        foundation.ID
	targetPath, resultHash, gitCommit         string
	status                                    string
	cleanupCompletedAt                        *time.Time
	version                                   int64
}

// CompleteReindexTx 原子追加 Activation、切换 Active Index 并完成 Delivery/Execution/Proposal。
func (r *Repository) CompleteReindexTx(ctx context.Context, command domain.CompleteReindexCommand) (domain.CompleteReindexResult, error) {
	if err := domain.ValidateCompleteReindexCommand(command); err != nil {
		return domain.CompleteReindexResult{}, err
	}
	identity, err := r.loadCompletionIdentity(ctx, command.Fence.DeliveryID)
	if err != nil {
		return domain.CompleteReindexResult{}, err
	}
	if identity.workspaceID != command.WorkspaceID {
		return domain.CompleteReindexResult{}, consistency("REINDEX_COMPLETION_BINDING_CONFLICT", errors.New("completion workspace does not match delivery"))
	}

	tx, err := r.db.Begin(ctx)
	if err != nil {
		return domain.CompleteReindexResult{}, classify(err, "REINDEX_COMPLETION_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, string(command.WorkspaceID)); err != nil {
		return domain.CompleteReindexResult{}, classify(err, "REINDEX_COMPLETION_WORKSPACE_LOCK_FAILED")
	}
	proposal, err := lockCompletionProposal(ctx, tx, identity.proposalID)
	if err != nil {
		return domain.CompleteReindexResult{}, err
	}
	execution, err := lockCompletionExecution(ctx, tx, identity.executionID)
	if err != nil {
		return domain.CompleteReindexResult{}, err
	}
	delivery, err := lockDelivery(ctx, tx, command.Fence.DeliveryID)
	if err != nil {
		return domain.CompleteReindexResult{}, err
	}
	if delivery.CurrentAttemptID == nil {
		return domain.CompleteReindexResult{}, consistency("REINDEX_COMPLETION_ATTEMPT_MISSING", errors.New("completion delivery has no current attempt"))
	}
	attempt, err := currentAttempt(ctx, tx, delivery)
	if err != nil {
		return domain.CompleteReindexResult{}, err
	}
	locked, err := lockCompletionIndexes(ctx, tx, delivery)
	if err != nil {
		return domain.CompleteReindexResult{}, err
	}

	if delivery.Status == domain.DeliveryStatusSucceeded {
		result, replayErr := replayCompletedReindex(ctx, tx, command, proposal, execution, delivery, attempt, locked)
		if replayErr != nil {
			return domain.CompleteReindexResult{}, replayErr
		}
		if err := tx.Commit(ctx); err != nil {
			return domain.CompleteReindexResult{}, classify(err, "REINDEX_COMPLETION_COMMIT_FAILED")
		}
		return result, nil
	}
	if !fenceMatches(command.Fence, delivery, attempt) {
		return domain.CompleteReindexResult{}, conflict("REINDEX_COMPLETION_FENCE_STALE", errors.New("completion fence is stale"))
	}
	var databaseNow time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
		return domain.CompleteReindexResult{}, classify(err, "REINDEX_COMPLETION_CLOCK_QUERY_FAILED")
	}
	databaseNow = databaseNow.UTC()
	if !databaseNow.Before(attempt.LeaseUntil) {
		return domain.CompleteReindexResult{}, completionLeaseExpired()
	}
	if err := validateFirstCompletion(ctx, tx, proposal, execution, delivery, attempt, locked.target); err != nil {
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
	activated, err := activateTx(ctx, tx, activationCommand, domain.ActivationKindActivate, &locked)
	if err != nil {
		return domain.CompleteReindexResult{}, err
	}
	attempt, err = scanDeliveryAttempt(tx.QueryRow(ctx, `WITH database_clock AS (SELECT clock_timestamp() AS now)
		UPDATE retrieval.reindex_delivery_attempt SET status='succeeded',ended_at=database_clock.now
		FROM database_clock
		WHERE id=$1 AND delivery_id=$2 AND status='processing' AND database_clock.now < lease_until
		RETURNING `+deliveryAttemptColumns, string(attempt.ID), string(delivery.ID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.CompleteReindexResult{}, completionLeaseExpired()
	}
	if err != nil {
		return domain.CompleteReindexResult{}, classify(err, "REINDEX_COMPLETION_ATTEMPT_UPDATE_FAILED")
	}
	delivery, err = scanDelivery(tx.QueryRow(ctx, `UPDATE retrieval.reindex_delivery SET
		status='succeeded',activation_id=$2,completed_at=$4,version=version+1,updated_at=$4
		WHERE id=$1 AND status='processing' AND version=$3 RETURNING `+deliveryColumns,
		string(delivery.ID), string(activated.Activation.ID), command.Fence.DeliveryVersion, databaseNow))
	if err != nil {
		return domain.CompleteReindexResult{}, classify(err, "REINDEX_COMPLETION_DELIVERY_UPDATE_FAILED")
	}
	executionTag, err := tx.Exec(ctx, `UPDATE change_control.writeback_execution SET
		status='completed',completed_at=$3,version=version+1,updated_at=$3
		WHERE id=$1 AND status='verifying' AND version=$2`, string(execution.id), execution.version, databaseNow)
	if err != nil {
		return domain.CompleteReindexResult{}, classify(err, "REINDEX_COMPLETION_EXECUTION_UPDATE_FAILED")
	}
	if executionTag.RowsAffected() != 1 {
		return domain.CompleteReindexResult{}, conflict("REINDEX_COMPLETION_EXECUTION_STALE", errors.New("writeback execution changed during completion"))
	}
	proposalTag, err := tx.Exec(ctx, `UPDATE change_control.proposal SET
		status='completed',version=version+1,updated_at=$3
		WHERE id=$1 AND status='verifying' AND version=$2`, string(proposal.id), proposal.version, databaseNow)
	if err != nil {
		return domain.CompleteReindexResult{}, classify(err, "REINDEX_COMPLETION_PROPOSAL_UPDATE_FAILED")
	}
	if proposalTag.RowsAffected() != 1 {
		return domain.CompleteReindexResult{}, conflict("REINDEX_COMPLETION_PROPOSAL_STALE", errors.New("proposal changed during completion"))
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.CompleteReindexResult{}, classify(err, "REINDEX_COMPLETION_COMMIT_FAILED")
	}
	return domain.CompleteReindexResult{
		Delivery: delivery, Attempt: attempt, Activation: activated.Activation,
		ActiveIndexVersion: activated.ActiveIndexVersion, PreviousIndexVersion: activated.PreviousIndexVersion,
	}, nil
}

func (r *Repository) loadCompletionIdentity(ctx context.Context, deliveryID foundation.ID) (completionIdentity, error) {
	var value completionIdentity
	err := r.db.QueryRow(ctx, `SELECT delivery.workspace_id::text,delivery.writeback_execution_id::text,execution.proposal_id::text
		FROM retrieval.reindex_delivery delivery
		JOIN change_control.writeback_execution execution ON execution.id=delivery.writeback_execution_id
		WHERE delivery.id=$1`, string(deliveryID)).Scan(&value.workspaceID, &value.executionID, &value.proposalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return value, notFound("REINDEX_COMPLETION_DELIVERY_NOT_FOUND", err)
	}
	if err != nil {
		return value, classify(err, "REINDEX_COMPLETION_IDENTITY_QUERY_FAILED")
	}
	return value, nil
}

func lockCompletionProposal(ctx context.Context, tx pgx.Tx, id foundation.ID) (completionProposal, error) {
	var value completionProposal
	var workflowRunID *string
	err := tx.QueryRow(ctx, `SELECT id::text,workspace_id::text,workflow_run_id::text,status,version
		FROM change_control.proposal WHERE id=$1 FOR UPDATE`, string(id)).Scan(
		&value.id, &value.workspaceID, &workflowRunID, &value.status, &value.version,
	)
	if err != nil {
		return value, classify(err, "REINDEX_COMPLETION_PROPOSAL_LOCK_FAILED")
	}
	value.workflowRunID = scannedOptionalID(workflowRunID)
	return value, nil
}

func lockCompletionExecution(ctx context.Context, tx pgx.Tx, id foundation.ID) (completionExecution, error) {
	var value completionExecution
	err := tx.QueryRow(ctx, `SELECT id::text,workspace_id::text,workflow_run_id::text,node_run_id::text,
		proposal_id::text,revision_id::text,approval_id::text,target_path,result_hash,git_commit,status,
		cleanup_completed_at,version FROM change_control.writeback_execution WHERE id=$1 FOR UPDATE`, string(id)).Scan(
		&value.id, &value.workspaceID, &value.workflowRunID, &value.nodeRunID,
		&value.proposalID, &value.revisionID, &value.approvalID, &value.targetPath, &value.resultHash,
		&value.gitCommit, &value.status, &value.cleanupCompletedAt, &value.version,
	)
	if err != nil {
		return value, classify(err, "REINDEX_COMPLETION_EXECUTION_LOCK_FAILED")
	}
	return value, nil
}

func lockCompletionIndexes(ctx context.Context, tx pgx.Tx, delivery domain.Delivery) (activationLockedIndexes, error) {
	if delivery.IndexVersionID == nil {
		return activationLockedIndexes{}, consistency("REINDEX_COMPLETION_INDEX_MISSING", errors.New("completion delivery has no index checkpoint"))
	}
	ids := []foundation.ID{*delivery.IndexVersionID}
	var activeID foundation.ID
	err := tx.QueryRow(ctx, `SELECT id::text FROM retrieval.index_version WHERE workspace_id=$1 AND status='active'`, string(delivery.WorkspaceID)).Scan(&activeID)
	if err == nil && activeID != *delivery.IndexVersionID {
		ids = append(ids, activeID)
	} else if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return activationLockedIndexes{}, classify(err, "REINDEX_COMPLETION_ACTIVE_QUERY_FAILED")
	}
	sort.Slice(ids, func(left, right int) bool { return ids[left] < ids[right] })
	locked := make(map[foundation.ID]domain.IndexVersion, len(ids))
	for _, id := range ids {
		value, queryErr := getIndexForUpdate(ctx, tx, delivery.WorkspaceID, id)
		if queryErr != nil {
			return activationLockedIndexes{}, queryErr
		}
		locked[id] = value
	}
	result := activationLockedIndexes{target: locked[*delivery.IndexVersionID]}
	if activeID != "" && activeID != *delivery.IndexVersionID {
		value := locked[activeID]
		result.current = &value
	}
	return result, nil
}

func validateFirstCompletion(ctx context.Context, tx pgx.Tx, proposal completionProposal, execution completionExecution, delivery domain.Delivery, attempt domain.DeliveryAttempt, target domain.IndexVersion) error {
	if proposal.workspaceID != delivery.WorkspaceID || proposal.workflowRunID == nil || *proposal.workflowRunID != execution.workflowRunID ||
		proposal.id != execution.proposalID || proposal.status != "verifying" || execution.id != delivery.WritebackExecutionID ||
		execution.workspaceID != delivery.WorkspaceID || execution.status != "verifying" {
		return consistency("REINDEX_COMPLETION_GATE_FAILED", errors.New("proposal or writeback lifecycle binding is invalid"))
	}
	if execution.cleanupCompletedAt == nil {
		return completionPrerequisitePending("writeback cleanup has not completed")
	}
	if delivery.Regression == nil || !domain.SnapshotRegressionCodeMatchesIndex(delivery.Regression.Code, target) ||
		delivery.SourceVersionID == nil || delivery.ParseProjectionID == nil || delivery.ExcludedSourceCount == nil ||
		attempt.IngestionAttemptID == nil || target.Status != domain.IndexStatusReady || target.ProcessingContract == nil || target.ExpectedSourceCount == nil {
		return consistency("REINDEX_COMPLETION_GATE_FAILED", errors.New("delivery regression or ready index gate is not satisfied"))
	}
	return validateCompletionReadOnlyFacts(ctx, tx, proposal, execution, delivery, attempt, target)
}

func validateCompletionReadOnlyFacts(ctx context.Context, tx pgx.Tx, proposal completionProposal, execution completionExecution, delivery domain.Delivery, attempt domain.DeliveryAttempt, target domain.IndexVersion) error {
	var eventType string
	var payload []byte
	var publishedAt *time.Time
	var commitProposalID, commitRevisionID, commitApprovalID foundation.ID
	var commitGit, commitTarget, commitResult string
	var runStatus, nodeStatus, sourceHash, ingestionStatus, securityStatus string
	var manifestBound bool
	err := tx.QueryRow(ctx, `SELECT event.event_type,event.payload::text,event.published_at,
		commit_mapping.proposal_id::text,commit_mapping.revision_id::text,commit_mapping.approval_id::text,
		commit_mapping.git_commit,commit_mapping.target_path,commit_mapping.result_hash,
		workflow_run.status,workflow_node.status,source_version.content_hash,
		ingestion_attempt.status,ingestion_attempt.security_status,
		EXISTS(SELECT 1 FROM retrieval.index_manifest_source source_manifest
			WHERE source_manifest.index_version_id=$12 AND source_manifest.source_version_id=$13
			AND source_manifest.parse_projection_id=$14 AND source_manifest.selection_status='included')
		FROM workflow.outbox_event event
		JOIN change_control.proposal_commit commit_mapping ON commit_mapping.writeback_execution_id=$1
		JOIN workflow.run workflow_run ON workflow_run.id=$2
		JOIN workflow.node_run workflow_node ON workflow_node.id=$3 AND workflow_node.run_id=workflow_run.id
		JOIN core.source_version source_version ON source_version.id=$4
		JOIN ingestion.attempt ingestion_attempt ON ingestion_attempt.id=$5
			AND ingestion_attempt.workspace_id=$6 AND ingestion_attempt.source_version_id=$4
			AND ingestion_attempt.parse_projection_id=$7
		WHERE event.id=$8 AND event.workspace_id=$6
			AND commit_mapping.workspace_id=$6 AND commit_mapping.proposal_id=$9
			AND commit_mapping.revision_id=$10 AND commit_mapping.approval_id=$11`,
		string(execution.id), string(execution.workflowRunID), string(execution.nodeRunID), string(*delivery.SourceVersionID),
		string(*attempt.IngestionAttemptID), string(delivery.WorkspaceID), string(*delivery.ParseProjectionID),
		string(delivery.OutboxEventID), string(execution.proposalID), string(execution.revisionID), string(execution.approvalID),
		string(target.ID), string(*delivery.SourceVersionID), string(*delivery.ParseProjectionID)).Scan(
		&eventType, &payload, &publishedAt, &commitProposalID, &commitRevisionID, &commitApprovalID,
		&commitGit, &commitTarget, &commitResult, &runStatus, &nodeStatus, &sourceHash,
		&ingestionStatus, &securityStatus, &manifestBound,
	)
	if err != nil {
		return classify(err, "REINDEX_COMPLETION_FACT_QUERY_FAILED")
	}
	request, err := reindexcontract.DecodeStrict(payload)
	if err != nil {
		return consistency("REINDEX_COMPLETION_BINDING_CONFLICT", err)
	}
	if err := reindexcontract.ValidateBinding(request, reindexcontract.Binding{
		WorkspaceID: delivery.WorkspaceID, WorkflowRunID: execution.workflowRunID, NodeRunID: execution.nodeRunID,
		ProposalID: execution.proposalID, RevisionID: execution.revisionID, ApprovalID: execution.approvalID,
		WritebackExecutionID: execution.id, TargetPath: execution.targetPath, ResultHash: execution.resultHash, GitCommit: execution.gitCommit,
	}); err != nil {
		return consistency("REINDEX_COMPLETION_BINDING_CONFLICT", err)
	}
	if eventType != reindexcontract.EventTypeReindexRequested || publishedAt == nil || proposal.id != commitProposalID ||
		commitProposalID != execution.proposalID || commitRevisionID != execution.revisionID || commitApprovalID != execution.approvalID ||
		commitGit != execution.gitCommit || commitTarget != execution.targetPath || commitResult != execution.resultHash ||
		sourceHash != execution.resultHash ||
		ingestionStatus != "chunked" || securityStatus != "passed" || !manifestBound {
		return consistency("REINDEX_COMPLETION_GATE_FAILED", errors.New("outbox, commit, source, or ingestion binding is invalid"))
	}
	if runStatus != "succeeded" || nodeStatus != "succeeded" {
		if runStatus == "failed" || runStatus == "cancelled" || nodeStatus == "failed" || nodeStatus == "cancelled" {
			return consistency("REINDEX_COMPLETION_GATE_FAILED", errors.New("origin workflow terminated without success"))
		}
		return completionPrerequisitePending("origin workflow has not completed")
	}
	return nil
}

func completionPrerequisitePending(message string) error {
	return application.NewCompletionPrerequisitePending(errors.New(message))
}

func completionLeaseExpired() error {
	return foundation.NewError(foundation.ErrorRetryableFailure, "REINDEX_LEASE_EXPIRED", true, errors.New("delivery database lease has expired"))
}

func replayCompletedReindex(ctx context.Context, tx pgx.Tx, command domain.CompleteReindexCommand, proposal completionProposal, execution completionExecution, delivery domain.Delivery, attempt domain.DeliveryAttempt, locked activationLockedIndexes) (domain.CompleteReindexResult, error) {
	if !committedFenceIdentityMatches(command.Fence, delivery, attempt) || attempt.Status != domain.DeliveryAttemptSucceeded ||
		delivery.ActivationID == nil || delivery.SourceVersionID == nil || delivery.ParseProjectionID == nil ||
		delivery.ExcludedSourceCount == nil || delivery.Regression == nil || attempt.IngestionAttemptID == nil ||
		execution.status != "completed" || execution.cleanupCompletedAt == nil || proposal.status != "completed" ||
		proposal.workspaceID != delivery.WorkspaceID || proposal.workflowRunID == nil || *proposal.workflowRunID != execution.workflowRunID ||
		proposal.id != execution.proposalID || execution.id != delivery.WritebackExecutionID || execution.workspaceID != delivery.WorkspaceID ||
		!domain.SnapshotRegressionCodeMatchesIndex(delivery.Regression.Code, locked.target) {
		return domain.CompleteReindexResult{}, conflict("REINDEX_COMPLETION_REPLAY_CONFLICT", errors.New("terminal completion does not match the requested fence"))
	}
	activation, found, err := getActivationByKey(ctx, tx, command.WorkspaceID, command.ActivationIdempotencyKey)
	if err != nil {
		return domain.CompleteReindexResult{}, err
	}
	if !found || activation.ID != *delivery.ActivationID || activation.Kind != domain.ActivationKindActivate ||
		activation.TargetIndexVersionID != locked.target.ID || activation.ReasonCode != command.ActivationReasonCode {
		return domain.CompleteReindexResult{}, conflict("REINDEX_COMPLETION_REPLAY_CONFLICT", errors.New("terminal activation binding is inconsistent"))
	}
	if err := validateCompletionReadOnlyFacts(ctx, tx, proposal, execution, delivery, attempt, locked.target); err != nil {
		return domain.CompleteReindexResult{}, err
	}
	historicalTarget := activationIndexSnapshot(locked.target, domain.IndexStatusActive, activation.TargetVersion, activation.CreatedAt)
	var previous *domain.IndexVersion
	if activation.PreviousIndexVersionID != nil {
		value, queryErr := getIndexTx(ctx, tx, command.WorkspaceID, *activation.PreviousIndexVersionID)
		if queryErr != nil || activation.PreviousVersion == nil {
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
