package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	"github.com/jackc/pgx/v5"
)

const resultReceiptFailureColumns = `
	f.id::text,f.tool_call_id::text,f.operation_id::text,f.analysis_run_id::text,
	f.workspace_id::text,f.workflow_run_id::text,f.node_run_id::text,f.node_attempt_id::text,
	f.failure_code,f.expected_output_hash,f.observed_output_hash,f.observed_output_bytes,
	f.observed_binding_hash,f.observed_binding_bytes,f.validator_version,f.checked_at,f.created_at`

// FinalizeCallWithReceiptFailure atomically closes a canonical Workspace Analysis Tool
// call when its returned value cannot form a valid immutable result receipt.
func (repository *Repository) FinalizeCallWithReceiptFailure(
	ctx context.Context,
	command application.FinalizeCallWithReceiptFailureCommand,
) (application.ResultReceiptFailureMutationResult, error) {
	call, err := validateFinalizeCallWithReceiptFailureCommand(command)
	if err != nil {
		return application.ResultReceiptFailureMutationResult{}, err
	}
	tx, err := repository.begin(ctx)
	if err != nil {
		return application.ResultReceiptFailureMutationResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	locked, err := lockReceiptCompletion(ctx, tx, call)
	if err != nil {
		return application.ResultReceiptFailureMutationResult{}, err
	}
	if err := validateReceiptCompletionBindings(locked, call); err != nil {
		return application.ResultReceiptFailureMutationResult{}, err
	}
	if !receiptExecutionIdentityMatches(locked, call, command.Identity) {
		return application.ResultReceiptFailureMutationResult{}, stale(errors.New("workspace analysis receipt failure identity is stale"))
	}

	if locked.operation.status == "FAILED" {
		result, replayErr := replayLockedReceiptFailure(ctx, tx, locked, call, command)
		if replayErr != nil {
			return application.ResultReceiptFailureMutationResult{}, replayErr
		}
		if err := repository.appendWorkspaceAnalysisToolCompleted(ctx, tx, locked.operation, locked.call, "failed", true); err != nil {
			return application.ResultReceiptFailureMutationResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return application.ResultReceiptFailureMutationResult{}, classify(err)
		}
		return result, nil
	}
	if locked.operation.status != "STARTED" || locked.reservation.status != "RESERVED" || locked.call.Status != domain.CallStarted {
		return application.ResultReceiptFailureMutationResult{}, receiptConflict(errors.New("workspace analysis receipt failure is not startable or replayable"))
	}
	if !activeReceiptExecutionFence(locked, call, command.Identity) {
		return application.ResultReceiptFailureMutationResult{}, stale(errors.New("workspace analysis receipt failure attempt fence is stale"))
	}

	callMutation, err := finalizeCallTx(ctx, tx, command.ExpectedVersion, call)
	if err != nil {
		return application.ResultReceiptFailureMutationResult{}, err
	}
	if callMutation.Replayed || callMutation.Call.Status != domain.CallSucceeded || callMutation.Call.CompletedAt == nil {
		return application.ResultReceiptFailureMutationResult{}, consistency(errors.New("receipt failure call CAS returned an invalid result"))
	}
	failure, err := resultReceiptFailureAtDatabaseCompletion(command, locked, callMutation.Call)
	if err != nil {
		return application.ResultReceiptFailureMutationResult{}, err
	}
	if err := insertResultReceiptFailure(ctx, tx, failure); err != nil {
		return application.ResultReceiptFailureMutationResult{}, err
	}
	completedAt := *callMutation.Call.CompletedAt
	if err := settleReceiptReservation(ctx, tx, locked, completedAt); err != nil {
		return application.ResultReceiptFailureMutationResult{}, err
	}
	if err := settleReceiptRunBudget(ctx, tx, locked, completedAt); err != nil {
		return application.ResultReceiptFailureMutationResult{}, err
	}
	if err := completeReceiptFailureOperation(ctx, tx, locked, completedAt); err != nil {
		return application.ResultReceiptFailureMutationResult{}, err
	}
	if err := repository.appendWorkspaceAnalysisToolCompleted(ctx, tx, locked.operation, callMutation.Call, "failed", false); err != nil {
		return application.ResultReceiptFailureMutationResult{}, err
	}
	if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		return application.ResultReceiptFailureMutationResult{}, classify(err)
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		return repository.recoverReceiptFailureAfterCommitError(ctx, call, command, locked.operation.id, commitErr)
	}
	return application.ResultReceiptFailureMutationResult{
		Call: callMutation.Call, Failure: failure, OperationID: locked.operation.id,
	}, nil
}

func validateFinalizeCallWithReceiptFailureCommand(
	command application.FinalizeCallWithReceiptFailureCommand,
) (domain.ToolCall, error) {
	if err := validateIdentityCallBinding(command.Identity, command.Call); err != nil {
		return domain.ToolCall{}, err
	}
	call, err := validateTerminalCommand(command.ExpectedVersion, command.Call, false)
	if err != nil {
		return domain.ToolCall{}, err
	}
	if call.Status != domain.CallSucceeded || call.CompletedAt == nil || !validID(command.Failure.ID) ||
		!validID(command.Failure.OperationID) || !validID(command.Failure.AnalysisRunID) {
		return domain.ToolCall{}, foundation.NewError(
			foundation.ErrorInvalidInput,
			domain.ErrorCodeResultReceiptFailureInvalid,
			false,
			errors.New("result receipt failure completion command is invalid"),
		)
	}
	return call, nil
}

func resultReceiptFailureAtDatabaseCompletion(
	command application.FinalizeCallWithReceiptFailureCommand,
	locked receiptCompletionLocks,
	call domain.ToolCall,
) (domain.ResultReceiptFailure, error) {
	if call.CompletedAt == nil {
		return domain.ResultReceiptFailure{}, consistency(errors.New("successful receipt failure call has no database completion time"))
	}
	if command.Failure.OperationID != locked.operation.id || command.Failure.AnalysisRunID != locked.analysisRun.id {
		return domain.ResultReceiptFailure{}, receiptConflict(errors.New("workspace analysis receipt failure operation binding differs"))
	}
	at := call.CompletedAt.UTC().Truncate(time.Microsecond)
	return domain.NewResultReceiptFailure(domain.ResultReceiptFailureDraft{
		ID:                   command.Failure.ID,
		OperationID:          locked.operation.id,
		AnalysisRunID:        locked.analysisRun.id,
		FailureCode:          command.Failure.FailureCode,
		ObservedOutputHash:   command.Failure.ObservedOutputHash,
		ObservedOutputBytes:  cloneOptionalInt64(command.Failure.ObservedOutputBytes),
		ObservedBindingHash:  command.Failure.ObservedBindingHash,
		ObservedBindingBytes: cloneOptionalInt64(command.Failure.ObservedBindingBytes),
		CheckedAt:            at,
		CreatedAt:            at,
	}, call, command.Definition)
}

func insertResultReceiptFailure(ctx context.Context, tx pgx.Tx, failure domain.ResultReceiptFailure) error {
	_, err := tx.Exec(ctx, `INSERT INTO workflow.tool_result_receipt_failure(
		id,tool_call_id,operation_id,analysis_run_id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,
		failure_code,expected_output_hash,observed_output_hash,observed_output_bytes,
		observed_binding_hash,observed_binding_bytes,validator_version,checked_at,created_at
	) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		string(failure.ID), string(failure.ToolCallID), string(failure.OperationID), string(failure.AnalysisRunID),
		string(failure.WorkspaceID), string(failure.WorkflowRunID), string(failure.NodeRunID), string(failure.NodeAttemptID),
		string(failure.FailureCode), failure.ExpectedOutputHash, optionalText(failure.ObservedOutputHash),
		optionalInt64Pointer(failure.ObservedOutputBytes), optionalText(failure.ObservedBindingHash),
		optionalInt64Pointer(failure.ObservedBindingBytes), failure.ValidatorVersion, failure.CheckedAt, failure.CreatedAt,
	)
	if err != nil {
		return classifyReceiptWrite(err)
	}
	return nil
}

func completeReceiptFailureOperation(
	ctx context.Context,
	tx pgx.Tx,
	locked receiptCompletionLocks,
	completedAt time.Time,
) error {
	operation := locked.operation
	tag, err := tx.Exec(ctx, `UPDATE agent.workspace_analysis_operation SET
		status='FAILED',result_kind=NULL,result_id=NULL,result_hash=NULL,
		error_code='WORKSPACE_ANALYSIS_RECEIPT_INVALID',version=version+1,
		updated_at=GREATEST(updated_at,$1),completed_at=GREATEST(updated_at,$1)
		WHERE id=$2 AND workspace_id=$3 AND analysis_run_id=$4 AND workflow_run_id=$5 AND node_run_id=$6
		  AND call_kind='TOOL' AND tool_call_id=$7 AND request_hash=$8 AND status='STARTED' AND version=$9`,
		completedAt, string(operation.id), string(operation.workspaceID), string(operation.analysisRunID),
		string(operation.workflowRunID), string(operation.nodeRunID), string(locked.call.ID), operation.requestHash,
		operation.version,
	)
	if err != nil {
		return classify(err)
	}
	if tag.RowsAffected() != 1 {
		return receiptConflict(errors.New("workspace analysis receipt failure operation CAS failed"))
	}
	return nil
}

func replayLockedReceiptFailure(
	ctx context.Context,
	tx pgx.Tx,
	locked receiptCompletionLocks,
	requestedCall domain.ToolCall,
	command application.FinalizeCallWithReceiptFailureCommand,
) (application.ResultReceiptFailureMutationResult, error) {
	if locked.reservation.status != "SETTLED" || !sameTerminalResult(locked.call, requestedCall) ||
		locked.call.Status != domain.CallSucceeded || locked.operation.errorCode == nil ||
		*locked.operation.errorCode != "WORKSPACE_ANALYSIS_RECEIPT_INVALID" || locked.operation.resultKind != nil ||
		locked.operation.resultID != nil || locked.operation.resultHash != nil {
		return application.ResultReceiptFailureMutationResult{}, receiptConflict(errors.New("workspace analysis receipt failure terminal closure differs"))
	}
	failure, err := loadResultReceiptFailure(ctx, tx, locked.call, command.Definition, locked.analysisRun.id, locked.operation.id)
	if err != nil {
		return application.ResultReceiptFailureMutationResult{}, err
	}
	requestedFailure, err := resultReceiptFailureAtDatabaseCompletion(command, locked, locked.call)
	if err != nil || !sameRequestedResultReceiptFailure(failure, requestedFailure) {
		return application.ResultReceiptFailureMutationResult{}, receiptConflict(errors.New("workspace analysis receipt failure replay identity differs"))
	}
	return application.ResultReceiptFailureMutationResult{
		Call: locked.call, Failure: failure, OperationID: locked.operation.id, Replayed: true,
	}, nil
}

func loadResultReceiptFailure(
	ctx context.Context,
	tx pgx.Tx,
	call domain.ToolCall,
	definition domain.Definition,
	analysisRunID foundation.ID,
	operationID foundation.ID,
) (domain.ResultReceiptFailure, error) {
	failure, err := scanResultReceiptFailure(tx.QueryRow(ctx, `SELECT `+resultReceiptFailureColumns+`
		FROM workflow.tool_result_receipt_failure AS f
		WHERE f.tool_call_id=$1 AND f.analysis_run_id=$2 AND f.operation_id=$3`,
		string(call.ID), string(analysisRunID), string(operationID),
	))
	if noRows(err) {
		return domain.ResultReceiptFailure{}, receiptNotFound(err)
	}
	if err != nil {
		return domain.ResultReceiptFailure{}, classifyScan(err)
	}
	if err := domain.ValidateResultReceiptFailure(failure, call, definition); err != nil {
		return domain.ResultReceiptFailure{}, consistency(err)
	}
	return failure, nil
}

func scanResultReceiptFailure(row rowScanner) (domain.ResultReceiptFailure, error) {
	var failure domain.ResultReceiptFailure
	var id, toolCallID, operationID, analysisRunID string
	var workspaceID, workflowRunID, nodeRunID, nodeAttemptID string
	var observedOutputHash, observedBindingHash *string
	if err := row.Scan(
		&id, &toolCallID, &operationID, &analysisRunID,
		&workspaceID, &workflowRunID, &nodeRunID, &nodeAttemptID,
		&failure.FailureCode, &failure.ExpectedOutputHash, &observedOutputHash, &failure.ObservedOutputBytes,
		&observedBindingHash, &failure.ObservedBindingBytes, &failure.ValidatorVersion,
		&failure.CheckedAt, &failure.CreatedAt,
	); err != nil {
		return domain.ResultReceiptFailure{}, err
	}
	failure.ID, failure.ToolCallID = foundation.ID(id), foundation.ID(toolCallID)
	failure.OperationID, failure.AnalysisRunID = foundation.ID(operationID), foundation.ID(analysisRunID)
	failure.WorkspaceID, failure.WorkflowRunID = foundation.ID(workspaceID), foundation.ID(workflowRunID)
	failure.NodeRunID, failure.NodeAttemptID = foundation.ID(nodeRunID), foundation.ID(nodeAttemptID)
	if observedOutputHash != nil {
		failure.ObservedOutputHash = *observedOutputHash
	}
	if observedBindingHash != nil {
		failure.ObservedBindingHash = *observedBindingHash
	}
	failure.CheckedAt = failure.CheckedAt.UTC()
	failure.CreatedAt = failure.CreatedAt.UTC()
	return failure, nil
}

func (repository *Repository) recoverReceiptFailureAfterCommitError(
	ctx context.Context,
	requestedCall domain.ToolCall,
	command application.FinalizeCallWithReceiptFailureCommand,
	operationID foundation.ID,
	commitErr error,
) (application.ResultReceiptFailureMutationResult, error) {
	tx, beginErr := repository.begin(ctx)
	if beginErr != nil {
		return application.ResultReceiptFailureMutationResult{}, receiptFinalizationUnknown(errors.Join(commitErr, beginErr))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	locked, lockErr := lockReceiptCompletion(ctx, tx, requestedCall)
	if lockErr != nil || locked.operation.id != operationID {
		return application.ResultReceiptFailureMutationResult{}, receiptFinalizationUnknown(errors.Join(commitErr, lockErr))
	}
	result, replayErr := replayLockedReceiptFailure(ctx, tx, locked, requestedCall, command)
	if replayErr != nil {
		return application.ResultReceiptFailureMutationResult{}, receiptFinalizationUnknown(errors.Join(commitErr, replayErr))
	}
	if eventErr := repository.appendWorkspaceAnalysisToolCompleted(ctx, tx, locked.operation, locked.call, "failed", true); eventErr != nil {
		return application.ResultReceiptFailureMutationResult{}, receiptFinalizationUnknown(errors.Join(commitErr, eventErr))
	}
	if err := tx.Commit(ctx); err != nil {
		return application.ResultReceiptFailureMutationResult{}, receiptFinalizationUnknown(errors.Join(commitErr, err))
	}
	result.Replayed = true
	return result, nil
}

func sameRequestedResultReceiptFailure(existing, requested domain.ResultReceiptFailure) bool {
	return existing.ToolCallID == requested.ToolCallID && existing.OperationID == requested.OperationID &&
		existing.AnalysisRunID == requested.AnalysisRunID &&
		existing.WorkspaceID == requested.WorkspaceID && existing.WorkflowRunID == requested.WorkflowRunID &&
		existing.NodeRunID == requested.NodeRunID && existing.NodeAttemptID == requested.NodeAttemptID &&
		existing.FailureCode == requested.FailureCode && existing.ExpectedOutputHash == requested.ExpectedOutputHash &&
		existing.ObservedOutputHash == requested.ObservedOutputHash &&
		optionalInt64Equal(existing.ObservedOutputBytes, requested.ObservedOutputBytes) &&
		existing.ObservedBindingHash == requested.ObservedBindingHash &&
		optionalInt64Equal(existing.ObservedBindingBytes, requested.ObservedBindingBytes) &&
		existing.ValidatorVersion == requested.ValidatorVersion
}

func cloneOptionalInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func optionalInt64Pointer(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func optionalInt64Equal(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

var _ application.ResultReceiptFailureRepository = (*Repository)(nil)
