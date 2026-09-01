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
	"gorm.io/gorm"
)

var _ application.ProcessorContextLoader = (*GORMDeliveryRepository)(nil)

// LoadProcessorContext restores and verifies the complete Reindex recovery
// binding from one repeatable-read, read-only PostgreSQL snapshot.
func (repository *GORMDeliveryRepository) LoadProcessorContext(ctx context.Context, fence domain.DeliveryFence) (application.ProcessorContextLoadResult, error) {
	if repository == nil || !validGORMRetrievalDatabase(repository.database) || nilInterface(repository.unitOfWork) || nilInterface(repository.ids) {
		return application.ProcessorContextLoadResult{}, dependency("REINDEX_PROCESSOR_CONTEXT_DATABASE_UNAVAILABLE", errors.New("processor context GORM repository is unavailable"))
	}
	if ctx == nil {
		return application.ProcessorContextLoadResult{}, foundation.NewError(foundation.ErrorInvalidInput, "RETRIEVAL_CONTEXT_INVALID", false, errors.New("retrieval context is nil"))
	}
	if err := domain.ValidateDeliveryFence(fence); err != nil {
		return application.ProcessorContextLoadResult{}, consistency(processorContextInvalidCode, errors.New("processor fence is invalid"))
	}

	var result application.ProcessorContextLoadResult
	err := repository.withinOptions(ctx, foundation.TransactionOptions{
		Isolation: foundation.TransactionIsolationRepeatableRead,
		ReadOnly:  true,
	}, "REINDEX_PROCESSOR_CONTEXT_TRANSACTION_FAILED", "REINDEX_PROCESSOR_CONTEXT_COMMIT_FAILED", func(callbackCtx context.Context, tx *gorm.DB) error {
		row, err := gormRetrievalRawRow(callbackCtx, tx, `SELECT `+deliveryColumns+` FROM retrieval.reindex_delivery WHERE id=?`, string(fence.DeliveryID))
		if err != nil {
			return classifyGORMRetrieval(callbackCtx, err, "REINDEX_PROCESSOR_CONTEXT_DELIVERY_QUERY_FAILED")
		}
		delivery, err := scanDelivery(row)
		if gormRetrievalNoRows(err) {
			return notFound("REINDEX_DELIVERY_NOT_FOUND", err)
		}
		if err != nil {
			return classifyGORMRetrieval(callbackCtx, err, "REINDEX_PROCESSOR_CONTEXT_DELIVERY_QUERY_FAILED")
		}
		if domain.ValidateDelivery(delivery) != nil || delivery.CurrentAttemptID == nil {
			return consistency(processorContextInvalidCode, errors.New("processor delivery is invalid"))
		}

		row, err = gormRetrievalRawRow(callbackCtx, tx, `SELECT `+deliveryAttemptColumns+`
			FROM retrieval.reindex_delivery_attempt WHERE id=? AND delivery_id=?`, string(*delivery.CurrentAttemptID), string(delivery.ID))
		if err != nil {
			return classifyGORMRetrieval(callbackCtx, err, "REINDEX_PROCESSOR_CONTEXT_ATTEMPT_QUERY_FAILED")
		}
		attempt, err := scanDeliveryAttempt(row)
		if err != nil {
			return classifyGORMRetrieval(callbackCtx, err, "REINDEX_PROCESSOR_CONTEXT_ATTEMPT_QUERY_FAILED")
		}
		if domain.ValidateDeliveryAttempt(attempt) != nil {
			return consistency(processorContextInvalidCode, errors.New("processor delivery attempt is invalid"))
		}
		if !processorFenceIdentityMatches(fence, delivery, attempt) {
			result = application.ProcessorContextLoadResult{Disposition: application.ProcessorContextStale}
			return nil
		}
		if processorDeliveryCommitted(delivery.Status) {
			result = application.ProcessorContextLoadResult{Disposition: application.ProcessorContextCommitted}
			return nil
		}
		if delivery.Status != domain.DeliveryStatusProcessing || attempt.Status != domain.DeliveryAttemptProcessing {
			result = application.ProcessorContextLoadResult{Disposition: application.ProcessorContextStale}
			return nil
		}

		row, err = gormRetrievalRawRow(callbackCtx, tx, `SELECT clock_timestamp()`)
		if err != nil {
			return classifyGORMRetrieval(callbackCtx, err, "REINDEX_PROCESSOR_CONTEXT_CLOCK_FAILED")
		}
		var databaseNow time.Time
		if err = row.Scan(&databaseNow); err != nil {
			return classifyGORMRetrieval(callbackCtx, err, "REINDEX_PROCESSOR_CONTEXT_CLOCK_FAILED")
		}
		if !databaseNow.Before(attempt.LeaseUntil) {
			return foundation.NewError(foundation.ErrorRetryableFailure, "REINDEX_LEASE_EXPIRED", true, errors.New("processor delivery lease expired"))
		}
		if delivery.Version < fence.DeliveryVersion {
			return consistency(processorContextInvalidCode, errors.New("processor fence version is ahead of delivery"))
		}

		contextValue, err := gormLoadProcessorBinding(callbackCtx, tx, delivery, attempt)
		if err != nil {
			return err
		}
		if delivery.ParseProjectionID != nil {
			ingestionAttemptID, evidenceErr := gormProcessorIngestionEvidenceID(callbackCtx, tx, delivery, attempt)
			if evidenceErr != nil {
				return evidenceErr
			}
			ingestionAttempt, ingestionErr := gormLoadProcessorIngestionAttempt(callbackCtx, tx, ingestionAttemptID)
			if ingestionErr != nil {
				return ingestionErr
			}
			contextValue.IngestionAttempt = &ingestionAttempt
		}
		if delivery.IndexVersionID != nil {
			index, indexErr := gormLoadProcessorIndex(callbackCtx, tx, delivery.WorkspaceID, *delivery.IndexVersionID)
			if indexErr != nil {
				return indexErr
			}
			contextValue.IndexVersion = &index
		}
		result = application.ProcessorContextLoadResult{
			Disposition: application.ProcessorContextCurrent,
			Context:     contextValue,
		}
		return nil
	})
	if err != nil {
		return application.ProcessorContextLoadResult{}, err
	}
	return result, nil
}

func gormLoadProcessorBinding(ctx context.Context, tx *gorm.DB, delivery domain.Delivery, attempt domain.DeliveryAttempt) (application.ProcessorContext, error) {
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

	row, err := gormRetrievalRawRow(ctx, tx, `SELECT
		event.event_type,event.payload::text,event.workspace_id::text,event.run_id::text,event.published_at,
		execution.workspace_id::text,execution.workflow_run_id::text,execution.node_run_id::text,
		execution.proposal_id::text,execution.revision_id::text,execution.approval_id::text,
		execution.target_path,execution.result_hash,execution.git_commit,execution.status,
		commit_mapping.workspace_id::text,commit_mapping.writeback_execution_id::text,
		commit_mapping.proposal_id::text,commit_mapping.revision_id::text,commit_mapping.approval_id::text,
		commit_mapping.target_path,commit_mapping.result_hash,commit_mapping.git_commit,
		source_version.source_id::text
		FROM workflow.outbox_event event
		JOIN change_control.writeback_execution execution ON execution.id=?
		JOIN change_control.proposal_commit commit_mapping ON commit_mapping.writeback_execution_id=execution.id
		LEFT JOIN core.source_version source_version ON source_version.id=?
		WHERE event.id=?`, string(delivery.WritebackExecutionID), optionalID(delivery.SourceVersionID), string(delivery.OutboxEventID))
	if err != nil {
		return application.ProcessorContext{}, classifyGORMRetrieval(ctx, err, "REINDEX_PROCESSOR_CONTEXT_BINDING_QUERY_FAILED")
	}
	err = row.Scan(
		&eventType, &payload, &eventWorkspace, &eventRunID, &publishedAt,
		&executionWorkspace, &executionRun, &executionNode, &executionProposal, &executionRevision, &executionApproval,
		&executionTarget, &executionResult, &executionGit, &executionStatus,
		&commitWorkspace, &commitExecution, &commitProposal, &commitRevision, &commitApproval,
		&commitTarget, &commitResult, &commitGit, &sourceID,
	)
	if gormRetrievalNoRows(err) {
		return application.ProcessorContext{}, consistency(processorContextInvalidCode, errors.New("processor outbox, writeback, or commit mapping is missing"))
	}
	if err != nil {
		return application.ProcessorContext{}, classifyGORMRetrieval(ctx, err, "REINDEX_PROCESSOR_CONTEXT_BINDING_QUERY_FAILED")
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

func gormProcessorIngestionEvidenceID(ctx context.Context, tx *gorm.DB, delivery domain.Delivery, attempt domain.DeliveryAttempt) (foundation.ID, error) {
	if attempt.IngestionAttemptID != nil {
		return *attempt.IngestionAttemptID, nil
	}
	row, err := gormRetrievalRawRow(ctx, tx, `SELECT ingestion_attempt_id::text
		FROM retrieval.reindex_delivery_attempt
		WHERE delivery_id=? AND ingestion_attempt_id IS NOT NULL
		ORDER BY attempt_no DESC,id DESC LIMIT 1`, string(delivery.ID))
	if err != nil {
		return "", classifyGORMRetrieval(ctx, err, "REINDEX_PROCESSOR_CONTEXT_INGESTION_EVIDENCE_QUERY_FAILED")
	}
	var rawID string
	if err = row.Scan(&rawID); gormRetrievalNoRows(err) {
		return "", consistency(processorContextInvalidCode, errors.New("processor ingestion checkpoint has no attempt evidence"))
	}
	if err != nil {
		return "", classifyGORMRetrieval(ctx, err, "REINDEX_PROCESSOR_CONTEXT_INGESTION_EVIDENCE_QUERY_FAILED")
	}
	return foundation.ID(rawID), nil
}

func gormLoadProcessorIngestionAttempt(ctx context.Context, tx *gorm.DB, id foundation.ID) (ingestiondomain.AttemptRecord, error) {
	row, err := gormRetrievalRawRow(ctx, tx, `SELECT id::text,workspace_id::text,source_version_id::text,workflow_run_id::text,
		status,security_status,failure_stage,error_code,retryable,parser_id,parser_version,parser_config_hash,
		chunk_strategy_version,schema_version,idempotency_key,attempt_number,warnings,parse_projection_id::text,
		started_at,completed_at,version
		FROM ingestion.attempt WHERE id=?`, string(id))
	if err != nil {
		return ingestiondomain.AttemptRecord{}, classifyGORMRetrieval(ctx, err, "REINDEX_PROCESSOR_CONTEXT_INGESTION_QUERY_FAILED")
	}

	var record ingestiondomain.AttemptRecord
	var workflowRunID, projectionID *string
	var warnings []byte
	err = row.Scan(
		&record.ID, &record.WorkspaceID, &record.SourceVersionID, &workflowRunID,
		&record.Status, &record.SecurityStatus, &record.FailureStage, &record.ErrorCode, &record.Retryable,
		&record.ParserID, &record.ParserVersion, &record.ParserConfigHash, &record.ChunkStrategyVersion,
		&record.SchemaVersion, &record.IdempotencyKey, &record.AttemptNumber, &warnings, &projectionID,
		&record.StartedAt, &record.CompletedAt, &record.Version,
	)
	if gormRetrievalNoRows(err) {
		return ingestiondomain.AttemptRecord{}, consistency(processorContextInvalidCode, errors.New("processor ingestion attempt is missing"))
	}
	if err != nil {
		return ingestiondomain.AttemptRecord{}, classifyGORMRetrieval(ctx, err, "REINDEX_PROCESSOR_CONTEXT_INGESTION_QUERY_FAILED")
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

func gormLoadProcessorIndex(ctx context.Context, tx *gorm.DB, workspaceID, indexID foundation.ID) (domain.IndexVersion, error) {
	row, err := gormRetrievalRawRow(ctx, tx, `SELECT `+indexColumns+` FROM retrieval.index_version WHERE workspace_id=? AND id=?`, string(workspaceID), string(indexID))
	if err != nil {
		return domain.IndexVersion{}, classifyGORMRetrieval(ctx, err, "RETRIEVAL_INDEX_QUERY_FAILED")
	}
	value, err := scanIndex(row)
	if gormRetrievalNoRows(err) {
		return domain.IndexVersion{}, notFound("RETRIEVAL_INDEX_NOT_FOUND", err)
	}
	if err != nil {
		return domain.IndexVersion{}, classifyGORMRetrieval(ctx, err, "RETRIEVAL_INDEX_QUERY_FAILED")
	}
	return value, nil
}
