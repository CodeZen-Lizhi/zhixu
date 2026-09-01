package postgres

import (
	"context"
	"errors"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	"gorm.io/gorm"
)

const gormResultReceiptFailureColumns = `id::text,tool_call_id::text,operation_id::text,analysis_run_id::text,workspace_id::text,workflow_run_id::text,node_run_id::text,node_attempt_id::text,failure_code,expected_output_hash,observed_output_hash,observed_output_bytes,observed_binding_hash,observed_binding_bytes,validator_version,checked_at,created_at`

func (repository *GORMWorkspaceAnalysisRepository) FinalizeCallWithReceiptFailure(ctx context.Context, command application.FinalizeCallWithReceiptFailureCommand) (application.ResultReceiptFailureMutationResult, error) {
	call, err := validateFinalizeCallWithReceiptFailureCommand(command)
	if err != nil {
		return application.ResultReceiptFailureMutationResult{}, err
	}
	var result application.ResultReceiptFailureMutationResult
	var closureQuery agentapplication.WorkspaceAnalysisToolClosureQuery
	callbackCompleted := false
	err = repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, database *gorm.DB, scope foundation.TransactionScope) error {
		_, identity, err := repository.lockExecution(callbackCtx, scope, command.Identity)
		if err != nil {
			return err
		}
		snapshot, found, err := repository.participant.LockWorkspaceAnalysisToolOperationByCallScoped(callbackCtx, scope, agentapplication.WorkspaceAnalysisToolCallLockQuery{Identity: identity, ToolCallID: call.ID, RequestHash: call.RequestHash})
		if err != nil {
			return err
		}
		if !found || snapshot.Reservation == nil {
			return receiptNotFound(errors.New("Workspace Analysis Tool operation is absent"))
		}
		locked, err := gormLockWorkspaceAnalysisToolCall(callbackCtx, database, call.WorkspaceID, call.ID)
		if err != nil {
			return err
		}
		if !sameStartBinding(locked, call, true) {
			return receiptConflict(errors.New("Workspace Analysis receipt failure binding differs"))
		}
		closureQuery = gormWorkspaceAnalysisToolClosureQuery(snapshot, locked)
		if snapshot.Operation.Status == agentdomain.WorkspaceAnalysisOperationFailed {
			failure, loadErr := gormLoadResultReceiptFailure(callbackCtx, database, locked, command.Definition, snapshot.Operation.AnalysisRunID, snapshot.Operation.ID)
			if loadErr != nil {
				return loadErr
			}
			if err := repository.appendCompleted(callbackCtx, scope, snapshot.Operation, locked, "failed", true); err != nil {
				return err
			}
			result = application.ResultReceiptFailureMutationResult{Call: locked, Failure: failure, OperationID: snapshot.Operation.ID, Replayed: true}
			callbackCompleted = true
			return nil
		}
		if snapshot.Operation.Status != agentdomain.WorkspaceAnalysisOperationStarted || snapshot.Reservation.Status != agentdomain.WorkspaceAnalysisBudgetReserved || locked.Status != domain.CallStarted {
			return receiptConflict(errors.New("Workspace Analysis receipt failure is not startable"))
		}
		completed, err := gormFinalizeWorkspaceAnalysisCall(callbackCtx, database, call, command.ExpectedVersion)
		if err != nil {
			return err
		}
		if completed.Status != domain.CallSucceeded {
			return consistency(errors.New("receipt failure requires a successful Tool Call"))
		}
		failure, err := resultReceiptFailureAtDatabaseCompletion(command, receiptCompletionLocks{analysisRun: receiptAnalysisRun{id: snapshot.Operation.AnalysisRunID}, operation: receiptOperation{id: snapshot.Operation.ID}}, completed)
		if err != nil {
			return err
		}
		if err := gormInsertResultReceiptFailure(callbackCtx, database, failure); err != nil {
			return err
		}
		settled, err := repository.participant.SettleWorkspaceAnalysisToolOperationScoped(callbackCtx, scope, gormSettlementCommand(identity, snapshot, completed, agentapplication.WorkspaceAnalysisToolSettlementReceiptInvalid, nil, string(agentdomain.WorkspaceAnalysisRunReceiptInvalid)))
		if err != nil {
			return err
		}
		if err := repository.appendCompleted(callbackCtx, scope, settled.Operation, completed, "failed", false); err != nil {
			return err
		}
		if err := gormToolsExec(database, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
			return classifyGORMTools(callbackCtx, err)
		}
		result = application.ResultReceiptFailureMutationResult{Call: completed, Failure: failure, OperationID: settled.Operation.ID}
		callbackCompleted = true
		return nil
	})
	if err != nil {
		if callbackCompleted && gormToolsCommitFailure(err) {
			return repository.gormRecoverReceiptFailureAfterCommitError(ctx, call, command, closureQuery, err)
		}
		return application.ResultReceiptFailureMutationResult{}, classifyGORMTools(ctx, err)
	}
	return result, nil
}

func gormInsertResultReceiptFailure(ctx context.Context, database *gorm.DB, failure domain.ResultReceiptFailure) error {
	return classifyGORMToolsReceiptWrite(ctx, gormToolsExec(database, `INSERT INTO workflow.tool_result_receipt_failure(id,tool_call_id,operation_id,analysis_run_id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,failure_code,expected_output_hash,observed_output_hash,observed_output_bytes,observed_binding_hash,observed_binding_bytes,validator_version,checked_at,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, string(failure.ID), string(failure.ToolCallID), string(failure.OperationID), string(failure.AnalysisRunID), string(failure.WorkspaceID), string(failure.WorkflowRunID), string(failure.NodeRunID), string(failure.NodeAttemptID), string(failure.FailureCode), failure.ExpectedOutputHash, nullableGORMText(failure.ObservedOutputHash), failure.ObservedOutputBytes, nullableGORMText(failure.ObservedBindingHash), failure.ObservedBindingBytes, failure.ValidatorVersion, failure.CheckedAt, failure.CreatedAt))
}

func gormLoadResultReceiptFailure(ctx context.Context, database *gorm.DB, call domain.ToolCall, definition domain.Definition, analysisRunID, operationID foundation.ID) (domain.ResultReceiptFailure, error) {
	row, err := gormToolsRawRow(database, `SELECT `+gormResultReceiptFailureColumns+` FROM workflow.tool_result_receipt_failure WHERE tool_call_id=? AND analysis_run_id=? AND operation_id=?`, string(call.ID), string(analysisRunID), string(operationID))
	if err != nil {
		return domain.ResultReceiptFailure{}, classifyGORMTools(ctx, err)
	}
	failure, err := scanGORMResultReceiptFailure(row)
	if gormToolsNoRows(err) {
		return domain.ResultReceiptFailure{}, receiptNotFound(err)
	}
	if err != nil {
		return domain.ResultReceiptFailure{}, classifyGORMTools(ctx, err)
	}
	if err := domain.ValidateResultReceiptFailure(failure, call, definition); err != nil {
		return domain.ResultReceiptFailure{}, consistency(err)
	}
	return failure, nil
}

func scanGORMResultReceiptFailure(row rowScanner) (domain.ResultReceiptFailure, error) {
	var failure domain.ResultReceiptFailure
	var id, callID, operationID, analysisRunID, workspaceID, workflowRunID, nodeRunID, nodeAttemptID string
	var outputHash, bindingHash *string
	if err := row.Scan(&id, &callID, &operationID, &analysisRunID, &workspaceID, &workflowRunID, &nodeRunID, &nodeAttemptID, &failure.FailureCode, &failure.ExpectedOutputHash, &outputHash, &failure.ObservedOutputBytes, &bindingHash, &failure.ObservedBindingBytes, &failure.ValidatorVersion, &failure.CheckedAt, &failure.CreatedAt); err != nil {
		return domain.ResultReceiptFailure{}, err
	}
	failure.ID, failure.ToolCallID, failure.OperationID, failure.AnalysisRunID, failure.WorkspaceID, failure.WorkflowRunID, failure.NodeRunID, failure.NodeAttemptID = foundation.ID(id), foundation.ID(callID), foundation.ID(operationID), foundation.ID(analysisRunID), foundation.ID(workspaceID), foundation.ID(workflowRunID), foundation.ID(nodeRunID), foundation.ID(nodeAttemptID)
	if outputHash != nil {
		failure.ObservedOutputHash = *outputHash
	}
	if bindingHash != nil {
		failure.ObservedBindingHash = *bindingHash
	}
	failure.CheckedAt, failure.CreatedAt = failure.CheckedAt.UTC().Truncate(time.Microsecond), failure.CreatedAt.UTC().Truncate(time.Microsecond)
	return failure, nil
}

func (repository *GORMWorkspaceAnalysisRepository) gormRecoverReceiptFailureAfterCommitError(
	ctx context.Context,
	requestedCall domain.ToolCall,
	command application.FinalizeCallWithReceiptFailureCommand,
	query agentapplication.WorkspaceAnalysisToolClosureQuery,
	commitErr error,
) (application.ResultReceiptFailureMutationResult, error) {
	var result application.ResultReceiptFailureMutationResult
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, database *gorm.DB, scope foundation.TransactionScope) error {
		closure, call, err := repository.gormVerifyWorkspaceAnalysisToolClosure(callbackCtx, database, scope, query, requestedCall)
		if err != nil {
			return err
		}
		if closure.Operation.Status != agentdomain.WorkspaceAnalysisOperationFailed ||
			closure.Operation.ErrorCode != string(agentdomain.WorkspaceAnalysisRunReceiptInvalid) ||
			call.Status != domain.CallSucceeded || !sameTerminalResult(call, requestedCall) {
			return consistency(errors.New("Workspace Analysis recovered receipt failure closure differs"))
		}
		failure, err := gormLoadResultReceiptFailure(callbackCtx, database, call, command.Definition, closure.Operation.AnalysisRunID, closure.Operation.ID)
		if err != nil {
			return err
		}
		requestedFailure, err := resultReceiptFailureAtDatabaseCompletion(command, receiptCompletionLocks{
			analysisRun: receiptAnalysisRun{id: closure.Operation.AnalysisRunID},
			operation:   receiptOperation{id: closure.Operation.ID},
		}, call)
		if err != nil || !sameRequestedResultReceiptFailure(failure, requestedFailure) {
			if err == nil {
				err = errors.New("Workspace Analysis recovered receipt failure binding differs")
			}
			return receiptFinalizationUnknown(errors.Join(commitErr, err))
		}
		if err := repository.appendCompleted(callbackCtx, scope, closure.Operation, call, "failed", true); err != nil {
			return err
		}
		result = application.ResultReceiptFailureMutationResult{Call: call, Failure: failure, OperationID: closure.Operation.ID, Replayed: true}
		return nil
	})
	if err == nil {
		return result, nil
	}
	return application.ResultReceiptFailureMutationResult{}, receiptFinalizationUnknown(errors.Join(commitErr, err))
}

var _ application.ResultReceiptFailureRepository = (*GORMWorkspaceAnalysisRepository)(nil)

var _ = time.Microsecond
