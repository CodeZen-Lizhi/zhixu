package postgres

import (
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
)

const gormWorkspaceAnalysisToolOperationColumns = `
	operation.id::text,operation.workspace_id::text,operation.analysis_run_id::text,
	operation.workflow_run_id::text,operation.node_run_id::text,
	operation.node_key,operation.operation_kind,operation.ordinal,operation.call_kind,
	operation.request_hash,operation.status,operation.first_node_attempt_id::text,
	operation.latest_node_attempt_id::text,operation.model_call_id::text,operation.tool_call_id::text,
	operation.result_kind,operation.result_id::text,operation.result_hash,operation.error_code,
	operation.version,operation.created_at,operation.updated_at,operation.started_at,operation.completed_at`

const gormWorkspaceAnalysisToolReservationColumns = `
	reservation.id::text,reservation.workspace_id::text,reservation.analysis_run_id::text,
	reservation.operation_id::text,reservation.call_kind,reservation.model_call_id::text,
	reservation.tool_call_id::text,reservation.status,
	reservation.reserved_model_calls,reservation.reserved_tool_calls,reservation.reserved_source_reads,
	reservation.reserved_input_tokens,reservation.reserved_output_tokens,reservation.reserved_cost_microunits,
	reservation.settled_model_calls,reservation.settled_tool_calls,reservation.settled_source_reads,
	reservation.settled_input_tokens,reservation.settled_output_tokens,reservation.settled_cost_microunits,
	reservation.created_at,reservation.settled_at`

type gormWorkspaceAnalysisToolOperationRecord struct {
	id            foundation.ID
	workspaceID   foundation.ID
	analysisRunID foundation.ID
	workflowRunID foundation.ID
	nodeRunID     foundation.ID

	nodeKey     domain.WorkspaceAnalysisOperationNodeKey
	kind        domain.WorkspaceAnalysisOperationKind
	ordinal     int
	callKind    domain.WorkspaceAnalysisOperationCallKind
	requestHash string
	status      domain.WorkspaceAnalysisOperationStatus

	firstAttemptID  *foundation.ID
	latestAttemptID *foundation.ID
	modelCallID     *foundation.ID
	toolCallID      *foundation.ID
	resultKind      *domain.WorkspaceAnalysisOperationResultKind
	resultID        *foundation.ID
	resultHash      *string
	errorCode       *string

	version     int64
	createdAt   time.Time
	updatedAt   time.Time
	startedAt   *time.Time
	completedAt *time.Time
}

func scanGORMWorkspaceAnalysisToolOperation(row rowScanner) (gormWorkspaceAnalysisToolOperationRecord, error) {
	var operation gormWorkspaceAnalysisToolOperationRecord
	var id, workspaceID, analysisRunID, workflowRunID, nodeRunID string
	var nodeKey, kind, callKind, status string
	var firstAttemptID, latestAttemptID, modelCallID, toolCallID *string
	var resultKind, resultID, resultHash, errorCode *string
	if err := row.Scan(
		&id, &workspaceID, &analysisRunID, &workflowRunID, &nodeRunID,
		&nodeKey, &kind, &operation.ordinal, &callKind, &operation.requestHash, &status,
		&firstAttemptID, &latestAttemptID, &modelCallID, &toolCallID,
		&resultKind, &resultID, &resultHash, &errorCode,
		&operation.version, &operation.createdAt, &operation.updatedAt, &operation.startedAt, &operation.completedAt,
	); err != nil {
		return gormWorkspaceAnalysisToolOperationRecord{}, err
	}

	operation.id = foundation.ID(id)
	operation.workspaceID = foundation.ID(workspaceID)
	operation.analysisRunID = foundation.ID(analysisRunID)
	operation.workflowRunID = foundation.ID(workflowRunID)
	operation.nodeRunID = foundation.ID(nodeRunID)
	operation.nodeKey = domain.WorkspaceAnalysisOperationNodeKey(nodeKey)
	operation.kind = domain.WorkspaceAnalysisOperationKind(kind)
	operation.callKind = domain.WorkspaceAnalysisOperationCallKind(callKind)
	operation.status = domain.WorkspaceAnalysisOperationStatus(status)
	operation.firstAttemptID = gormWorkspaceAnalysisOptionalID(firstAttemptID)
	operation.latestAttemptID = gormWorkspaceAnalysisOptionalID(latestAttemptID)
	operation.modelCallID = gormWorkspaceAnalysisOptionalID(modelCallID)
	operation.toolCallID = gormWorkspaceAnalysisOptionalID(toolCallID)
	if resultKind != nil {
		value := domain.WorkspaceAnalysisOperationResultKind(*resultKind)
		operation.resultKind = &value
	}
	operation.resultID = gormWorkspaceAnalysisOptionalID(resultID)
	operation.resultHash = resultHash
	operation.errorCode = errorCode
	operation.createdAt = gormWorkspaceAnalysisToolTime(operation.createdAt)
	operation.updatedAt = gormWorkspaceAnalysisToolTime(operation.updatedAt)
	operation.startedAt = gormWorkspaceAnalysisOptionalTime(operation.startedAt)
	operation.completedAt = gormWorkspaceAnalysisOptionalTime(operation.completedAt)

	if operation.modelCallID != nil && operation.toolCallID != nil ||
		operation.callKind == domain.WorkspaceAnalysisOperationCallModel && operation.toolCallID != nil ||
		operation.callKind == domain.WorkspaceAnalysisOperationCallTool && operation.modelCallID != nil {
		return gormWorkspaceAnalysisToolOperationRecord{}, consistency(
			errors.New("workspace analysis operation call columns are inconsistent"),
		)
	}
	return operation, nil
}

func (operation gormWorkspaceAnalysisToolOperationRecord) domain(
	reservationID *foundation.ID,
) domain.WorkspaceAnalysisOperation {
	persisted := domain.WorkspaceAnalysisOperation{
		ID: operation.id, AnalysisRunID: operation.analysisRunID,
		NodeKey: operation.nodeKey, Kind: operation.kind, Ordinal: operation.ordinal,
		RequestHash: operation.requestHash, Status: operation.status,
		FirstNodeAttemptID: operation.firstAttemptID, LatestNodeAttemptID: operation.latestAttemptID,
		BudgetReservationID: reservationID, Version: operation.version,
		CreatedAt: operation.createdAt, UpdatedAt: operation.updatedAt,
		StartedAt: operation.startedAt, CompletedAt: operation.completedAt,
	}
	if operation.modelCallID != nil || operation.toolCallID != nil {
		call := &domain.WorkspaceAnalysisOperationCallRef{Kind: operation.callKind}
		if operation.callKind == domain.WorkspaceAnalysisOperationCallModel && operation.modelCallID != nil {
			call.ID = *operation.modelCallID
		}
		if operation.callKind == domain.WorkspaceAnalysisOperationCallTool && operation.toolCallID != nil {
			call.ID = *operation.toolCallID
		}
		persisted.Call = call
	}
	if operation.resultKind != nil || operation.resultID != nil || operation.resultHash != nil {
		result := &domain.WorkspaceAnalysisOperationResultRef{}
		if operation.resultKind != nil {
			result.Kind = *operation.resultKind
		}
		if operation.resultID != nil {
			result.ID = *operation.resultID
		}
		if operation.resultHash != nil {
			result.Hash = *operation.resultHash
		}
		persisted.Result = result
	}
	if operation.errorCode != nil {
		persisted.ErrorCode = *operation.errorCode
	}
	return persisted
}

func scanGORMWorkspaceAnalysisToolReservation(row rowScanner) (domain.WorkspaceAnalysisBudgetReservation, error) {
	var reservation domain.WorkspaceAnalysisBudgetReservation
	var id, workspaceID, analysisRunID, operationID string
	var callKind, status string
	var modelCallID, toolCallID *string
	if err := row.Scan(
		&id, &workspaceID, &analysisRunID, &operationID, &callKind,
		&modelCallID, &toolCallID, &status,
		&reservation.Reserved.ModelCalls, &reservation.Reserved.ToolCalls, &reservation.Reserved.SourceReads,
		&reservation.Reserved.InputTokens, &reservation.Reserved.OutputTokens, &reservation.Reserved.CostMicrounits,
		&reservation.Settled.ModelCalls, &reservation.Settled.ToolCalls, &reservation.Settled.SourceReads,
		&reservation.Settled.InputTokens, &reservation.Settled.OutputTokens, &reservation.Settled.CostMicrounits,
		&reservation.CreatedAt, &reservation.SettledAt,
	); err != nil {
		return domain.WorkspaceAnalysisBudgetReservation{}, err
	}
	reservation.ID = foundation.ID(id)
	reservation.WorkspaceID = foundation.ID(workspaceID)
	reservation.AnalysisRunID = foundation.ID(analysisRunID)
	reservation.OperationID = foundation.ID(operationID)
	reservation.CallKind = domain.WorkspaceAnalysisOperationCallKind(callKind)
	reservation.ModelCallID = gormWorkspaceAnalysisOptionalID(modelCallID)
	reservation.ToolCallID = gormWorkspaceAnalysisOptionalID(toolCallID)
	reservation.Status = domain.WorkspaceAnalysisBudgetReservationStatus(status)
	reservation.CreatedAt = gormWorkspaceAnalysisToolTime(reservation.CreatedAt)
	reservation.SettledAt = gormWorkspaceAnalysisOptionalTime(reservation.SettledAt)
	if err := domain.ValidateWorkspaceAnalysisBudgetReservation(reservation); err != nil {
		return domain.WorkspaceAnalysisBudgetReservation{}, consistency(err)
	}
	return reservation, nil
}

func gormWorkspaceAnalysisOptionalID(value *string) *foundation.ID {
	if value == nil {
		return nil
	}
	parsed := foundation.ID(*value)
	return &parsed
}

func gormWorkspaceAnalysisToolTime(value time.Time) time.Time {
	if value.IsZero() {
		return value
	}
	return value.UTC().Truncate(time.Microsecond)
}

func gormWorkspaceAnalysisOptionalTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	canonical := gormWorkspaceAnalysisToolTime(*value)
	return &canonical
}
