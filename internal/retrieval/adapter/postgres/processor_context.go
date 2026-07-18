package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	ingestiondomain "github.com/CodeZen-Lizhi/zhixu/internal/ingestion/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	reindexcontract "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/contract"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/jackc/pgx/v5"
)

const processorContextInvalidCode = "REINDEX_PROCESSOR_CONTEXT_INVALID"

var _ application.ProcessorContextLoader = (*DeliveryRepository)(nil)

// LoadProcessorContext 在短 repeatable-read 事务中严格恢复 Reindex 的跨域持久绑定。
func (r *DeliveryRepository) LoadProcessorContext(ctx context.Context, fence domain.DeliveryFence) (application.ProcessorContextLoadResult, error) {
	if r == nil || r.db == nil {
		return application.ProcessorContextLoadResult{}, dependency("REINDEX_PROCESSOR_CONTEXT_DATABASE_UNAVAILABLE", errors.New("processor context database is unavailable"))
	}
	if err := domain.ValidateDeliveryFence(fence); err != nil {
		return application.ProcessorContextLoadResult{}, consistency(processorContextInvalidCode, errors.New("processor fence is invalid"))
	}
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return application.ProcessorContextLoadResult{}, classify(err, "REINDEX_PROCESSOR_CONTEXT_TRANSACTION_FAILED")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET TRANSACTION ISOLATION LEVEL REPEATABLE READ READ ONLY`); err != nil {
		return application.ProcessorContextLoadResult{}, classify(err, "REINDEX_PROCESSOR_CONTEXT_SNAPSHOT_FAILED")
	}

	delivery, err := scanDelivery(tx.QueryRow(ctx, `SELECT `+deliveryColumns+` FROM retrieval.reindex_delivery WHERE id=$1`, string(fence.DeliveryID)))
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ProcessorContextLoadResult{}, notFound("REINDEX_DELIVERY_NOT_FOUND", err)
	}
	if err != nil {
		return application.ProcessorContextLoadResult{}, classify(err, "REINDEX_PROCESSOR_CONTEXT_DELIVERY_QUERY_FAILED")
	}
	if domain.ValidateDelivery(delivery) != nil || delivery.CurrentAttemptID == nil {
		return application.ProcessorContextLoadResult{}, consistency(processorContextInvalidCode, errors.New("processor delivery is invalid"))
	}
	attempt, err := scanDeliveryAttempt(tx.QueryRow(ctx, `SELECT `+deliveryAttemptColumns+`
		FROM retrieval.reindex_delivery_attempt WHERE id=$1 AND delivery_id=$2`, string(*delivery.CurrentAttemptID), string(delivery.ID)))
	if err != nil {
		return application.ProcessorContextLoadResult{}, classify(err, "REINDEX_PROCESSOR_CONTEXT_ATTEMPT_QUERY_FAILED")
	}
	if domain.ValidateDeliveryAttempt(attempt) != nil {
		return application.ProcessorContextLoadResult{}, consistency(processorContextInvalidCode, errors.New("processor delivery attempt is invalid"))
	}
	if !processorFenceIdentityMatches(fence, delivery, attempt) {
		return commitProcessorContext(ctx, tx, application.ProcessorContextLoadResult{Disposition: application.ProcessorContextStale})
	}
	if processorDeliveryCommitted(delivery.Status) {
		return commitProcessorContext(ctx, tx, application.ProcessorContextLoadResult{Disposition: application.ProcessorContextCommitted})
	}
	if delivery.Status != domain.DeliveryStatusProcessing || attempt.Status != domain.DeliveryAttemptProcessing {
		return commitProcessorContext(ctx, tx, application.ProcessorContextLoadResult{Disposition: application.ProcessorContextStale})
	}
	var databaseNow time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&databaseNow); err != nil {
		return application.ProcessorContextLoadResult{}, classify(err, "REINDEX_PROCESSOR_CONTEXT_CLOCK_FAILED")
	}
	if !databaseNow.Before(attempt.LeaseUntil) {
		return application.ProcessorContextLoadResult{}, foundation.NewError(foundation.ErrorRetryableFailure, "REINDEX_LEASE_EXPIRED", true, errors.New("processor delivery lease expired"))
	}
	if delivery.Version < fence.DeliveryVersion {
		return application.ProcessorContextLoadResult{}, consistency(processorContextInvalidCode, errors.New("processor fence version is ahead of delivery"))
	}

	contextValue, err := loadProcessorBinding(ctx, tx, delivery, attempt)
	if err != nil {
		return application.ProcessorContextLoadResult{}, err
	}
	if delivery.ParseProjectionID != nil {
		ingestionAttemptID, err := processorIngestionEvidenceID(ctx, tx, delivery, attempt)
		if err != nil {
			return application.ProcessorContextLoadResult{}, err
		}
		ingestionAttempt, err := loadProcessorIngestionAttempt(ctx, tx, ingestionAttemptID)
		if err != nil {
			return application.ProcessorContextLoadResult{}, err
		}
		contextValue.IngestionAttempt = &ingestionAttempt
	}
	if delivery.IndexVersionID != nil {
		index, err := getIndexTx(ctx, tx, delivery.WorkspaceID, *delivery.IndexVersionID)
		if err != nil {
			return application.ProcessorContextLoadResult{}, err
		}
		contextValue.IndexVersion = &index
	}
	return commitProcessorContext(ctx, tx, application.ProcessorContextLoadResult{
		Disposition: application.ProcessorContextCurrent,
		Context:     contextValue,
	})
}

func loadProcessorBinding(ctx context.Context, tx pgx.Tx, delivery domain.Delivery, attempt domain.DeliveryAttempt) (application.ProcessorContext, error) {
	var eventType, payload string
	var eventWorkspace foundation.ID
	var eventRunID *string
	var publishedAt *time.Time
	var executionWorkspace, executionRun, executionNode, executionProposal foundation.ID
	var executionRevision, executionApproval foundation.ID
	var executionTarget, executionResult, executionStatus string
	var executionGit *string
	var commitWorkspace, commitExecution, commitProposal, commitRevision, commitApproval foundation.ID
	var commitTarget, commitResult, commitGit string
	var sourceID *string
	err := tx.QueryRow(ctx, `SELECT
		event.event_type,event.payload::text,event.workspace_id::text,event.run_id::text,event.published_at,
		execution.workspace_id::text,execution.workflow_run_id::text,execution.node_run_id::text,
		execution.proposal_id::text,execution.revision_id::text,execution.approval_id::text,
		execution.target_path,execution.result_hash,execution.git_commit,execution.status,
		commit_mapping.workspace_id::text,commit_mapping.writeback_execution_id::text,
		commit_mapping.proposal_id::text,commit_mapping.revision_id::text,commit_mapping.approval_id::text,
		commit_mapping.target_path,commit_mapping.result_hash,commit_mapping.git_commit,
		source_version.source_id::text
		FROM workflow.outbox_event event
		JOIN change_control.writeback_execution execution ON execution.id=$2
		JOIN change_control.proposal_commit commit_mapping ON commit_mapping.writeback_execution_id=execution.id
		LEFT JOIN core.source_version source_version ON source_version.id=$3
		WHERE event.id=$1`, string(delivery.OutboxEventID), string(delivery.WritebackExecutionID), optionalID(delivery.SourceVersionID)).Scan(
		&eventType, &payload, &eventWorkspace, &eventRunID, &publishedAt,
		&executionWorkspace, &executionRun, &executionNode, &executionProposal, &executionRevision, &executionApproval,
		&executionTarget, &executionResult, &executionGit, &executionStatus,
		&commitWorkspace, &commitExecution, &commitProposal, &commitRevision, &commitApproval,
		&commitTarget, &commitResult, &commitGit, &sourceID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.ProcessorContext{}, consistency(processorContextInvalidCode, errors.New("processor outbox, writeback, or commit mapping is missing"))
	}
	if err != nil {
		return application.ProcessorContext{}, classify(err, "REINDEX_PROCESSOR_CONTEXT_BINDING_QUERY_FAILED")
	}
	request, err := reindexcontract.DecodeStrict([]byte(payload))
	if err != nil {
		return application.ProcessorContext{}, consistency(processorContextInvalidCode, errors.New("processor outbox payload is invalid"))
	}
	if executionGit == nil || eventRunID == nil || publishedAt == nil || eventType != reindexcontract.EventTypeReindexRequested ||
		eventWorkspace != delivery.WorkspaceID || foundation.ID(*eventRunID) != request.WorkflowRunID ||
		executionStatus != "verifying" || executionWorkspace != delivery.WorkspaceID ||
		commitWorkspace != executionWorkspace || commitExecution != delivery.WritebackExecutionID ||
		commitProposal != executionProposal || commitRevision != executionRevision || commitApproval != executionApproval ||
		commitTarget != executionTarget || commitResult != executionResult || commitGit != *executionGit {
		return application.ProcessorContext{}, consistency(processorContextInvalidCode, errors.New("processor persistent binding is inconsistent"))
	}
	binding := reindexcontract.Binding{
		WorkspaceID: executionWorkspace, WorkflowRunID: executionRun, NodeRunID: executionNode,
		ProposalID: executionProposal, RevisionID: executionRevision, ApprovalID: executionApproval,
		WritebackExecutionID: delivery.WritebackExecutionID, TargetPath: executionTarget,
		ResultHash: executionResult, GitCommit: *executionGit,
	}
	if err := reindexcontract.ValidateBinding(request, binding); err != nil || request.WorkspaceID != delivery.WorkspaceID ||
		request.WritebackExecutionID != delivery.WritebackExecutionID {
		return application.ProcessorContext{}, consistency(processorContextInvalidCode, errors.New("processor request does not match writeback binding"))
	}
	contextValue := application.ProcessorContext{Delivery: delivery, Attempt: attempt, Request: request, Binding: binding}
	if delivery.SourceVersionID != nil {
		if sourceID == nil {
			return application.ProcessorContext{}, consistency(processorContextInvalidCode, errors.New("processor source checkpoint is missing"))
		}
		contextValue.TargetSourceID = foundation.ID(*sourceID)
	} else if sourceID != nil {
		return application.ProcessorContext{}, consistency(processorContextInvalidCode, errors.New("processor pre-capture context has a source"))
	}
	return contextValue, nil
}

func processorIngestionEvidenceID(ctx context.Context, tx pgx.Tx, delivery domain.Delivery, attempt domain.DeliveryAttempt) (foundation.ID, error) {
	if attempt.IngestionAttemptID != nil {
		return *attempt.IngestionAttemptID, nil
	}
	var id foundation.ID
	err := tx.QueryRow(ctx, `SELECT ingestion_attempt_id::text
		FROM retrieval.reindex_delivery_attempt
		WHERE delivery_id=$1 AND ingestion_attempt_id IS NOT NULL
		ORDER BY attempt_no DESC,id DESC LIMIT 1`, string(delivery.ID)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", consistency(processorContextInvalidCode, errors.New("processor ingestion checkpoint has no attempt evidence"))
	}
	if err != nil {
		return "", classify(err, "REINDEX_PROCESSOR_CONTEXT_INGESTION_EVIDENCE_QUERY_FAILED")
	}
	return id, nil
}

func loadProcessorIngestionAttempt(ctx context.Context, tx pgx.Tx, id foundation.ID) (ingestiondomain.AttemptRecord, error) {
	var record ingestiondomain.AttemptRecord
	var workflowRunID, projectionID *string
	var warnings []byte
	err := tx.QueryRow(ctx, `SELECT id::text,workspace_id::text,source_version_id::text,workflow_run_id::text,
		status,security_status,failure_stage,error_code,retryable,parser_id,parser_version,parser_config_hash,
		chunk_strategy_version,schema_version,idempotency_key,attempt_number,warnings,parse_projection_id::text,
		started_at,completed_at,version
		FROM ingestion.attempt WHERE id=$1`, string(id)).Scan(
		&record.ID, &record.WorkspaceID, &record.SourceVersionID, &workflowRunID,
		&record.Status, &record.SecurityStatus, &record.FailureStage, &record.ErrorCode, &record.Retryable,
		&record.ParserID, &record.ParserVersion, &record.ParserConfigHash, &record.ChunkStrategyVersion,
		&record.SchemaVersion, &record.IdempotencyKey, &record.AttemptNumber, &warnings, &projectionID,
		&record.StartedAt, &record.CompletedAt, &record.Version,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ingestiondomain.AttemptRecord{}, consistency(processorContextInvalidCode, errors.New("processor ingestion attempt is missing"))
	}
	if err != nil {
		return ingestiondomain.AttemptRecord{}, classify(err, "REINDEX_PROCESSOR_CONTEXT_INGESTION_QUERY_FAILED")
	}
	if workflowRunID != nil {
		workflowID := foundation.ID(*workflowRunID)
		record.WorkflowRunID = &workflowID
	}
	if projectionID != nil {
		projection := foundation.ID(*projectionID)
		record.ParseProjectionID = &projection
	}
	if err := json.Unmarshal(warnings, &record.Warnings); err != nil {
		return ingestiondomain.AttemptRecord{}, consistency(processorContextInvalidCode, errors.New("processor ingestion warnings are invalid"))
	}
	return record, nil
}

func processorFenceIdentityMatches(fence domain.DeliveryFence, delivery domain.Delivery, attempt domain.DeliveryAttempt) bool {
	return delivery.ID == fence.DeliveryID && delivery.DispatchNo == fence.DispatchNo && delivery.AttemptNo == fence.AttemptNo &&
		delivery.CurrentAttemptID != nil && *delivery.CurrentAttemptID == fence.AttemptID && attempt.ID == fence.AttemptID &&
		attempt.DeliveryID == fence.DeliveryID && attempt.DispatchNo == fence.DispatchNo && attempt.AttemptNo == fence.AttemptNo &&
		attempt.LeaseOwner == fence.Owner
}

func processorDeliveryCommitted(status domain.DeliveryStatus) bool {
	return status == domain.DeliveryStatusRetryWait || status == domain.DeliveryStatusSucceeded ||
		status == domain.DeliveryStatusFailed || status == domain.DeliveryStatusManualRecovery
}

func commitProcessorContext(ctx context.Context, tx pgx.Tx, result application.ProcessorContextLoadResult) (application.ProcessorContextLoadResult, error) {
	if err := tx.Commit(ctx); err != nil {
		return application.ProcessorContextLoadResult{}, classify(err, "REINDEX_PROCESSOR_CONTEXT_COMMIT_FAILED")
	}
	return result, nil
}
