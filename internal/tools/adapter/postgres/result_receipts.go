package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	"github.com/jackc/pgx/v5"
)

const resultReceiptColumns = `
	r.id::text,r.tool_call_id::text,r.workspace_id::text,r.workflow_run_id::text,
	r.node_run_id::text,r.node_attempt_id::text,r.tool_name,r.tool_version,
	r.output_schema_id,r.output_schema_version,r.definition_hash,r.persistence_policy,
	r.max_output_bytes,r.max_private_binding_bytes,r.output_document,r.output_hash,r.output_bytes,
	r.server_binding_schema_id,r.server_binding_schema_version,r.server_binding_document,
	r.server_binding_hash,r.server_binding_bytes,r.created_at`

type receiptExecutionFence struct {
	workflowDefinitionID foundation.ID
	workflowStatus       string
	databaseNow          time.Time
	nodeKey              string
	nodeStatus           string
	nodeAttempt          int64
	nodeOwner            *string
	nodeLease            *time.Time
	attemptStatus        string
	attemptNo            int64
	attemptOwner         *string
	attemptLease         *time.Time
}

type receiptAnalysisRun struct {
	id                foundation.ID
	status            string
	definitionVersion int64
	definitionHash    string
	version           int64
}

type receiptOperation struct {
	id, workspaceID, analysisRunID, workflowRunID, nodeRunID foundation.ID
	nodeKey, callKind, requestHash, status                   string
	version                                                  int64
	firstAttemptID, latestAttemptID, toolCallID              *string
	resultKind, resultID, resultHash                         *string
	errorCode                                                *string
}

type receiptReservation struct {
	id, workspaceID, analysisRunID, operationID                foundation.ID
	callKind, status                                           string
	toolCallID                                                 *string
	reservedModelCalls, reservedToolCalls, reservedSourceReads int64
	reservedInputTokens, reservedOutputTokens                  int64
}

type receiptCompletionLocks struct {
	fence       receiptExecutionFence
	analysisRun receiptAnalysisRun
	operation   receiptOperation
	reservation receiptReservation
	call        domain.ToolCall
}

// FinalizeCallWithReceipt 原子完成 Workspace Analysis Tool Call、receipt、预算和逻辑操作。
func (repository *Repository) FinalizeCallWithReceipt(
	ctx context.Context,
	command application.FinalizeCallWithReceiptCommand,
) (application.ResultReceiptMutationResult, error) {
	call, requestedReceipt, err := validateFinalizeCallWithReceiptCommand(command)
	if err != nil {
		return application.ResultReceiptMutationResult{}, err
	}
	tx, err := repository.begin(ctx)
	if err != nil {
		return application.ResultReceiptMutationResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	locked, err := lockReceiptCompletion(ctx, tx, call)
	if err != nil {
		return application.ResultReceiptMutationResult{}, err
	}
	if err := validateReceiptCompletionBindings(locked, call); err != nil {
		return application.ResultReceiptMutationResult{}, err
	}
	if !receiptExecutionIdentityMatches(locked, call, command.Identity) {
		return application.ResultReceiptMutationResult{}, stale(errors.New("workspace analysis receipt completion identity is stale"))
	}
	if locked.operation.status == "SUCCEEDED" {
		result, replayErr := replayLockedReceiptCompletion(ctx, tx, locked, call, command.Definition, requestedReceipt)
		if replayErr != nil {
			return application.ResultReceiptMutationResult{}, replayErr
		}
		if err := repository.appendWorkspaceAnalysisToolCompleted(ctx, tx, locked.operation, locked.call, "succeeded", true); err != nil {
			return application.ResultReceiptMutationResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return application.ResultReceiptMutationResult{}, classify(err)
		}
		return result, nil
	}
	if locked.operation.status != "STARTED" || locked.reservation.status != "RESERVED" || locked.call.Status != domain.CallStarted {
		return application.ResultReceiptMutationResult{}, receiptConflict(errors.New("workspace analysis receipt completion is not startable or replayable"))
	}
	if !activeReceiptExecutionFence(locked, call, command.Identity) {
		return application.ResultReceiptMutationResult{}, stale(errors.New("workspace analysis receipt completion attempt fence is stale"))
	}

	callMutation, err := finalizeCallTx(ctx, tx, command.ExpectedVersion, call)
	if err != nil {
		return application.ResultReceiptMutationResult{}, err
	}
	if callMutation.Replayed || callMutation.Call.Status != domain.CallSucceeded || callMutation.Call.CompletedAt == nil {
		return application.ResultReceiptMutationResult{}, consistency(errors.New("receipt completion call CAS returned an invalid result"))
	}
	receipt, err := resultReceiptAtDatabaseCompletion(requestedReceipt, callMutation.Call, command.Definition)
	if err != nil {
		return application.ResultReceiptMutationResult{}, err
	}
	if err := insertResultReceipt(ctx, tx, receipt); err != nil {
		return application.ResultReceiptMutationResult{}, err
	}
	completedAt := receipt.CreatedAt
	if err := settleReceiptReservation(ctx, tx, locked, completedAt); err != nil {
		return application.ResultReceiptMutationResult{}, err
	}
	if err := settleReceiptRunBudget(ctx, tx, locked, completedAt); err != nil {
		return application.ResultReceiptMutationResult{}, err
	}
	if err := completeReceiptOperation(ctx, tx, locked, receipt, completedAt); err != nil {
		return application.ResultReceiptMutationResult{}, err
	}
	if err := repository.appendWorkspaceAnalysisToolCompleted(ctx, tx, locked.operation, callMutation.Call, "succeeded", false); err != nil {
		return application.ResultReceiptMutationResult{}, err
	}
	if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		return application.ResultReceiptMutationResult{}, classify(err)
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		return repository.recoverReceiptCompletionAfterCommitError(ctx, call, command.Definition, receipt, locked.operation.id, commitErr)
	}
	return application.ResultReceiptMutationResult{Call: callMutation.Call, Receipt: receipt, OperationID: locked.operation.id}, nil
}

// LoadResultReceipt 按成功 Call 与冻结 Definition 读取完整且已关闭的 canonical receipt。
func (repository *Repository) LoadResultReceipt(ctx context.Context, command application.LoadResultReceiptCommand) (domain.ResultReceipt, error) {
	if err := domain.ValidateToolCall(command.Call); err != nil || command.Call.Status != domain.CallSucceeded {
		if err == nil {
			err = errors.New("result receipt lookup requires a successful tool call")
		}
		return domain.ResultReceipt{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeResultReceiptInvalid, false, err)
	}
	tx, err := repository.begin(ctx)
	if err != nil {
		return domain.ResultReceipt{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	receipt, err := loadClosedResultReceipt(ctx, tx, command.Call, command.Definition)
	if err != nil {
		return domain.ResultReceipt{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ResultReceipt{}, classify(err)
	}
	return receipt, nil
}

func validateFinalizeCallWithReceiptCommand(
	command application.FinalizeCallWithReceiptCommand,
) (domain.ToolCall, domain.ResultReceipt, error) {
	if err := validateIdentityCallBinding(command.Identity, command.Call); err != nil {
		return domain.ToolCall{}, domain.ResultReceipt{}, err
	}
	call, err := validateTerminalCommand(command.ExpectedVersion, command.Call, false)
	if err != nil {
		return domain.ToolCall{}, domain.ResultReceipt{}, err
	}
	if call.Status != domain.CallSucceeded || call.CompletedAt == nil || !validID(command.ReceiptID) {
		return domain.ToolCall{}, domain.ResultReceipt{}, foundation.NewError(
			foundation.ErrorInvalidInput,
			domain.ErrorCodeResultReceiptInvalid,
			false,
			errors.New("result receipt completion command is invalid"),
		)
	}
	draft := domain.ResultReceiptDraft{
		ID: command.ReceiptID, Output: append(json.RawMessage(nil), command.Output...), CreatedAt: *call.CompletedAt,
	}
	if command.PrivateBinding != nil {
		draft.PrivateBindingSchema = command.PrivateBinding.Schema
		draft.PrivateBinding = append(json.RawMessage(nil), command.PrivateBinding.Document...)
	}
	receipt, err := domain.NewResultReceipt(draft, call, command.Definition)
	if err != nil {
		return domain.ToolCall{}, domain.ResultReceipt{}, err
	}
	return call, receipt, nil
}

func lockReceiptCompletion(ctx context.Context, tx pgx.Tx, call domain.ToolCall) (receiptCompletionLocks, error) {
	var locked receiptCompletionLocks
	var workflowDefinitionID string
	if err := tx.QueryRow(ctx, `SELECT definition_id::text,status,clock_timestamp()
		FROM workflow.run WHERE id=$1 AND workspace_id=$2 FOR UPDATE`,
		string(call.WorkflowRunID), string(call.WorkspaceID),
	).Scan(&workflowDefinitionID, &locked.fence.workflowStatus, &locked.fence.databaseNow); err != nil {
		return receiptCompletionLocks{}, classifyReceiptLockError(err)
	}
	locked.fence.workflowDefinitionID = foundation.ID(workflowDefinitionID)
	if err := tx.QueryRow(ctx, `SELECT node_key,status,attempt,lease_owner,lease_until
		FROM workflow.node_run WHERE id=$1 AND run_id=$2 FOR UPDATE`,
		string(call.NodeRunID), string(call.WorkflowRunID),
	).Scan(
		&locked.fence.nodeKey, &locked.fence.nodeStatus, &locked.fence.nodeAttempt,
		&locked.fence.nodeOwner, &locked.fence.nodeLease,
	); err != nil {
		return receiptCompletionLocks{}, classifyReceiptLockError(err)
	}
	if err := tx.QueryRow(ctx, `SELECT status,attempt_no,lease_owner,lease_until
		FROM workflow.node_attempt WHERE id=$1 AND node_run_id=$2 FOR UPDATE`,
		string(call.NodeAttemptID), string(call.NodeRunID),
	).Scan(
		&locked.fence.attemptStatus, &locked.fence.attemptNo,
		&locked.fence.attemptOwner, &locked.fence.attemptLease,
	); err != nil {
		return receiptCompletionLocks{}, classifyReceiptLockError(err)
	}
	var analysisRunID string
	if err := tx.QueryRow(ctx, `SELECT id::text,status,definition_version,definition_hash,version
		FROM agent.workspace_analysis_run
		WHERE workspace_id=$1 AND workflow_run_id=$2 FOR UPDATE`,
		string(call.WorkspaceID), string(call.WorkflowRunID),
	).Scan(
		&analysisRunID, &locked.analysisRun.status, &locked.analysisRun.definitionVersion,
		&locked.analysisRun.definitionHash, &locked.analysisRun.version,
	); err != nil {
		return receiptCompletionLocks{}, classifyReceiptLockError(err)
	}
	locked.analysisRun.id = foundation.ID(analysisRunID)

	var operationID, operationWorkspaceID, operationAnalysisRunID, operationWorkflowRunID, operationNodeRunID string
	if err := tx.QueryRow(ctx, `SELECT id::text,workspace_id::text,analysis_run_id::text,workflow_run_id::text,node_run_id::text,
		node_key,call_kind,request_hash,status,first_node_attempt_id::text,
		latest_node_attempt_id::text,tool_call_id::text,result_kind,result_id::text,result_hash,error_code,version
		FROM agent.workspace_analysis_operation
		WHERE analysis_run_id=$1 AND tool_call_id=$2 FOR UPDATE`,
		analysisRunID, string(call.ID),
	).Scan(
		&operationID, &operationWorkspaceID, &operationAnalysisRunID, &operationWorkflowRunID, &operationNodeRunID,
		&locked.operation.nodeKey, &locked.operation.callKind, &locked.operation.requestHash, &locked.operation.status,
		&locked.operation.firstAttemptID, &locked.operation.latestAttemptID, &locked.operation.toolCallID,
		&locked.operation.resultKind, &locked.operation.resultID, &locked.operation.resultHash, &locked.operation.errorCode,
		&locked.operation.version,
	); err != nil {
		return receiptCompletionLocks{}, classifyReceiptLockError(err)
	}
	locked.operation.id = foundation.ID(operationID)
	locked.operation.workspaceID = foundation.ID(operationWorkspaceID)
	locked.operation.analysisRunID = foundation.ID(operationAnalysisRunID)
	locked.operation.workflowRunID = foundation.ID(operationWorkflowRunID)
	locked.operation.nodeRunID = foundation.ID(operationNodeRunID)

	var reservationID, reservationWorkspaceID, reservationAnalysisRunID, reservationOperationID string
	if err := tx.QueryRow(ctx, `SELECT id::text,workspace_id::text,analysis_run_id::text,operation_id::text,
		call_kind,tool_call_id::text,status,reserved_model_calls,reserved_tool_calls,reserved_source_reads,
		reserved_input_tokens,reserved_output_tokens
		FROM agent.workspace_analysis_budget_reservation
		WHERE analysis_run_id=$1 AND operation_id=$2 AND tool_call_id=$3 FOR UPDATE`,
		analysisRunID, operationID, string(call.ID),
	).Scan(
		&reservationID, &reservationWorkspaceID, &reservationAnalysisRunID, &reservationOperationID,
		&locked.reservation.callKind, &locked.reservation.toolCallID, &locked.reservation.status,
		&locked.reservation.reservedModelCalls, &locked.reservation.reservedToolCalls,
		&locked.reservation.reservedSourceReads, &locked.reservation.reservedInputTokens,
		&locked.reservation.reservedOutputTokens,
	); err != nil {
		return receiptCompletionLocks{}, classifyReceiptLockError(err)
	}
	locked.reservation.id = foundation.ID(reservationID)
	locked.reservation.workspaceID = foundation.ID(reservationWorkspaceID)
	locked.reservation.analysisRunID = foundation.ID(reservationAnalysisRunID)
	locked.reservation.operationID = foundation.ID(reservationOperationID)

	lockedCall, err := scanToolCall(tx.QueryRow(ctx, `SELECT `+toolCallColumns+`
		FROM workflow.tool_call WHERE id=$1 AND workspace_id=$2 FOR UPDATE`, string(call.ID), string(call.WorkspaceID)))
	if err != nil {
		return receiptCompletionLocks{}, classifyReceiptLockError(err)
	}
	locked.call = lockedCall
	locked.fence.databaseNow = locked.fence.databaseNow.UTC()
	return locked, nil
}

func validateReceiptCompletionBindings(locked receiptCompletionLocks, call domain.ToolCall) error {
	if !sameStartBinding(locked.call, call, true) ||
		locked.analysisRun.id != locked.operation.analysisRunID ||
		locked.operation.workspaceID != call.WorkspaceID || locked.operation.workflowRunID != call.WorkflowRunID ||
		locked.operation.nodeRunID != call.NodeRunID || locked.operation.callKind != "TOOL" ||
		locked.operation.requestHash != call.RequestHash || stringPointerValue(locked.operation.toolCallID) != string(call.ID) ||
		stringPointerValue(locked.operation.firstAttemptID) != string(call.NodeAttemptID) ||
		locked.reservation.workspaceID != call.WorkspaceID || locked.reservation.analysisRunID != locked.analysisRun.id ||
		locked.reservation.operationID != locked.operation.id || locked.reservation.callKind != "TOOL" ||
		stringPointerValue(locked.reservation.toolCallID) != string(call.ID) ||
		locked.reservation.reservedModelCalls != 0 || locked.reservation.reservedToolCalls != 1 ||
		locked.reservation.reservedSourceReads < 0 || locked.reservation.reservedSourceReads > 1 ||
		locked.reservation.reservedInputTokens != 0 || locked.reservation.reservedOutputTokens != 0 {
		return receiptConflict(errors.New("workspace analysis receipt completion binding differs"))
	}
	return nil
}

func activeReceiptExecutionFence(
	locked receiptCompletionLocks,
	call domain.ToolCall,
	identity domain.TrustedExecutionIdentity,
) bool {
	return locked.fence.workflowStatus == "running" && locked.analysisRun.status == "running" &&
		receiptExecutionIdentityMatches(locked, call, identity) && locked.fence.nodeStatus == "running" &&
		locked.fence.attemptStatus == "running" && locked.fence.nodeAttempt == locked.fence.attemptNo &&
		locked.fence.attemptNo == identity.LeaseFence && stringPointerValue(locked.operation.latestAttemptID) == string(identity.NodeAttemptID) &&
		canonicalActiveLease(locked.fence.nodeOwner, locked.fence.nodeLease, locked.fence.attemptOwner, locked.fence.attemptLease,
			identity.LeaseOwner, locked.fence.databaseNow)
}

func receiptExecutionIdentityMatches(
	locked receiptCompletionLocks,
	call domain.ToolCall,
	identity domain.TrustedExecutionIdentity,
) bool {
	return identity.DefinitionID == locked.fence.workflowDefinitionID &&
		identity.DefinitionVersion == locked.analysisRun.definitionVersion && identity.DefinitionHash == locked.analysisRun.definitionHash &&
		identity.WorkspaceID == call.WorkspaceID && identity.WorkflowRunID == call.WorkflowRunID &&
		identity.NodeRunID == call.NodeRunID && identity.NodeAttemptID == call.NodeAttemptID &&
		identity.NodeKey == locked.operation.nodeKey && locked.fence.nodeKey == identity.NodeKey
}

func canonicalActiveLease(
	nodeOwner *string,
	nodeLease *time.Time,
	attemptOwner *string,
	attemptLease *time.Time,
	expectedOwner string,
	databaseNow time.Time,
) bool {
	if nodeOwner == nil || attemptOwner == nil || nodeLease == nil || attemptLease == nil ||
		*nodeOwner == "" || *nodeOwner != strings.TrimSpace(*nodeOwner) || *nodeOwner != *attemptOwner || *nodeOwner != expectedOwner {
		return false
	}
	return nodeLease.Equal(*attemptLease) && attemptLease.After(databaseNow)
}

func resultReceiptAtDatabaseCompletion(
	requested domain.ResultReceipt,
	call domain.ToolCall,
	definition domain.Definition,
) (domain.ResultReceipt, error) {
	if call.CompletedAt == nil {
		return domain.ResultReceipt{}, consistency(errors.New("successful receipt call has no database completion time"))
	}
	draft := domain.ResultReceiptDraft{ID: requested.ID, Output: requested.Output, CreatedAt: *call.CompletedAt}
	if requested.PrivateBinding != nil {
		draft.PrivateBindingSchema = requested.PrivateBinding.Schema
		draft.PrivateBinding = requested.PrivateBinding.Document
	}
	return domain.NewResultReceipt(draft, call, definition)
}

func insertResultReceipt(ctx context.Context, tx pgx.Tx, receipt domain.ResultReceipt) error {
	var bindingSchemaID, bindingHash any
	var bindingSchemaVersion, bindingDocument, bindingBytes any
	if receipt.PrivateBinding != nil {
		bindingSchemaID = receipt.PrivateBinding.Schema.ID
		bindingSchemaVersion = receipt.PrivateBinding.Schema.Version
		bindingDocument = []byte(receipt.PrivateBinding.Document)
		bindingHash = receipt.PrivateBinding.Hash
		bindingBytes = receipt.PrivateBinding.Bytes
	}
	_, err := tx.Exec(ctx, `INSERT INTO workflow.tool_result_receipt(
		id,tool_call_id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,
		tool_name,tool_version,output_schema_id,output_schema_version,definition_hash,persistence_policy,
		max_output_bytes,max_private_binding_bytes,output_document,output_hash,output_bytes,
		server_binding_schema_id,server_binding_schema_version,server_binding_document,
		server_binding_hash,server_binding_bytes,created_at
	) VALUES (
		$1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23
	)`,
		string(receipt.ID), string(receipt.ToolCallID), string(receipt.WorkspaceID), string(receipt.WorkflowRunID),
		string(receipt.NodeRunID), string(receipt.NodeAttemptID), receipt.Tool.Name, receipt.Tool.Version,
		receipt.OutputSchema.ID, receipt.OutputSchema.Version, receipt.DefinitionHash, string(receipt.PersistencePolicy),
		receipt.MaxOutputBytes, receipt.MaxPrivateBindingBytes, []byte(receipt.Output), receipt.OutputHash, receipt.OutputBytes,
		bindingSchemaID, bindingSchemaVersion, bindingDocument, bindingHash, bindingBytes, receipt.CreatedAt,
	)
	if err != nil {
		return classifyReceiptWrite(err)
	}
	return nil
}

func settleReceiptReservation(ctx context.Context, tx pgx.Tx, locked receiptCompletionLocks, completedAt time.Time) error {
	tag, err := tx.Exec(ctx, `UPDATE agent.workspace_analysis_budget_reservation SET
		status='SETTLED',settled_model_calls=0,settled_tool_calls=1,
		settled_source_reads=reserved_source_reads,settled_input_tokens=0,settled_output_tokens=0,
		settled_cost_microunits=NULL,settled_at=$1
		WHERE id=$2 AND workspace_id=$3 AND analysis_run_id=$4 AND operation_id=$5
		  AND call_kind='TOOL' AND tool_call_id=$6 AND status='RESERVED'`,
		completedAt, string(locked.reservation.id), string(locked.reservation.workspaceID),
		string(locked.reservation.analysisRunID), string(locked.reservation.operationID), string(locked.call.ID),
	)
	if err != nil {
		return classify(err)
	}
	if tag.RowsAffected() != 1 {
		return receiptConflict(errors.New("workspace analysis receipt reservation CAS failed"))
	}
	return nil
}

func settleReceiptRunBudget(ctx context.Context, tx pgx.Tx, locked receiptCompletionLocks, completedAt time.Time) error {
	run := locked.analysisRun
	reservation := locked.reservation
	tag, err := tx.Exec(ctx, `UPDATE agent.workspace_analysis_run SET
		reserved_model_calls=reserved_model_calls-$1,reserved_tool_calls=reserved_tool_calls-$2,
		reserved_source_reads=reserved_source_reads-$3,reserved_input_tokens=reserved_input_tokens-$4,
		reserved_output_tokens=reserved_output_tokens-$5,
		settled_model_calls=settled_model_calls,settled_tool_calls=settled_tool_calls+1,
		settled_source_reads=settled_source_reads+$3,settled_input_tokens=settled_input_tokens,
		settled_output_tokens=settled_output_tokens,version=version+1,updated_at=GREATEST(updated_at,$6)
		WHERE id=$7 AND status='running' AND version=$8
		  AND reserved_model_calls>=$1 AND reserved_tool_calls>=$2 AND reserved_source_reads>=$3
		  AND reserved_input_tokens>=$4 AND reserved_output_tokens>=$5`,
		reservation.reservedModelCalls, reservation.reservedToolCalls, reservation.reservedSourceReads,
		reservation.reservedInputTokens, reservation.reservedOutputTokens, completedAt,
		string(run.id), run.version,
	)
	if err != nil {
		return classify(err)
	}
	if tag.RowsAffected() != 1 {
		return receiptConflict(errors.New("workspace analysis receipt run budget CAS failed"))
	}
	return nil
}

func completeReceiptOperation(
	ctx context.Context,
	tx pgx.Tx,
	locked receiptCompletionLocks,
	receipt domain.ResultReceipt,
	completedAt time.Time,
) error {
	operation := locked.operation
	tag, err := tx.Exec(ctx, `UPDATE agent.workspace_analysis_operation SET
		status='SUCCEEDED',result_kind='TOOL_RESULT_RECEIPT',result_id=$1,result_hash=$2,
		error_code=NULL,version=version+1,updated_at=GREATEST(updated_at,$3),completed_at=GREATEST(updated_at,$3)
		WHERE id=$4 AND workspace_id=$5 AND analysis_run_id=$6 AND workflow_run_id=$7 AND node_run_id=$8
		  AND call_kind='TOOL' AND tool_call_id=$9 AND request_hash=$10 AND status='STARTED' AND version=$11`,
		string(receipt.ID), receipt.OutputHash, completedAt, string(operation.id), string(operation.workspaceID),
		string(operation.analysisRunID), string(operation.workflowRunID), string(operation.nodeRunID),
		string(receipt.ToolCallID), operation.requestHash, operation.version,
	)
	if err != nil {
		return classify(err)
	}
	if tag.RowsAffected() != 1 {
		return receiptConflict(errors.New("workspace analysis receipt operation CAS failed"))
	}
	return nil
}

func replayLockedReceiptCompletion(
	ctx context.Context,
	tx pgx.Tx,
	locked receiptCompletionLocks,
	requestedCall domain.ToolCall,
	definition domain.Definition,
	requestedReceipt domain.ResultReceipt,
) (application.ResultReceiptMutationResult, error) {
	if locked.reservation.status != "SETTLED" || !sameTerminalResult(locked.call, requestedCall) ||
		locked.operation.resultKind == nil || *locked.operation.resultKind != "TOOL_RESULT_RECEIPT" {
		return application.ResultReceiptMutationResult{}, receiptConflict(errors.New("workspace analysis receipt terminal closure differs"))
	}
	receipt, err := loadClosedResultReceipt(ctx, tx, locked.call, definition)
	if err != nil {
		return application.ResultReceiptMutationResult{}, err
	}
	if !sameRequestedResultReceipt(receipt, requestedReceipt) ||
		stringPointerValue(locked.operation.resultID) != string(receipt.ID) ||
		stringPointerValue(locked.operation.resultHash) != receipt.OutputHash {
		return application.ResultReceiptMutationResult{}, receiptConflict(errors.New("workspace analysis receipt replay identity differs"))
	}
	return application.ResultReceiptMutationResult{Call: locked.call, Receipt: receipt, OperationID: locked.operation.id, Replayed: true}, nil
}

func loadClosedResultReceipt(
	ctx context.Context,
	tx pgx.Tx,
	call domain.ToolCall,
	definition domain.Definition,
) (domain.ResultReceipt, error) {
	receipt, err := scanResultReceipt(tx.QueryRow(ctx, `SELECT `+resultReceiptColumns+`
		FROM workflow.tool_result_receipt AS r
		JOIN agent.workspace_analysis_operation AS operation
		  ON operation.tool_call_id=r.tool_call_id
		 AND operation.status='SUCCEEDED'
		 AND operation.result_kind='TOOL_RESULT_RECEIPT'
		 AND operation.result_id=r.id
		 AND operation.result_hash=r.output_hash
		JOIN agent.workspace_analysis_budget_reservation AS reservation
		  ON reservation.operation_id=operation.id
		 AND reservation.tool_call_id=r.tool_call_id
		 AND reservation.status='SETTLED'
		WHERE r.tool_call_id=$1 AND r.workspace_id=$2 AND r.workflow_run_id=$3
		  AND r.node_run_id=$4 AND r.node_attempt_id=$5`,
		string(call.ID), string(call.WorkspaceID), string(call.WorkflowRunID),
		string(call.NodeRunID), string(call.NodeAttemptID),
	))
	if noRows(err) {
		return domain.ResultReceipt{}, receiptNotFound(err)
	}
	if err != nil {
		return domain.ResultReceipt{}, classifyScan(err)
	}
	if err := domain.ValidateResultReceipt(receipt, call, definition); err != nil {
		return domain.ResultReceipt{}, consistency(err)
	}
	return receipt, nil
}

func scanResultReceipt(row rowScanner) (domain.ResultReceipt, error) {
	var receipt domain.ResultReceipt
	var id, toolCallID, workspaceID, workflowRunID, nodeRunID, nodeAttemptID string
	var persistencePolicy string
	var bindingSchemaID, bindingHash *string
	var bindingSchemaVersion, bindingBytes *int64
	var outputDocument, bindingDocument []byte
	if err := row.Scan(
		&id, &toolCallID, &workspaceID, &workflowRunID, &nodeRunID, &nodeAttemptID,
		&receipt.Tool.Name, &receipt.Tool.Version, &receipt.OutputSchema.ID, &receipt.OutputSchema.Version,
		&receipt.DefinitionHash, &persistencePolicy, &receipt.MaxOutputBytes, &receipt.MaxPrivateBindingBytes,
		&outputDocument, &receipt.OutputHash, &receipt.OutputBytes,
		&bindingSchemaID, &bindingSchemaVersion, &bindingDocument, &bindingHash, &bindingBytes, &receipt.CreatedAt,
	); err != nil {
		return domain.ResultReceipt{}, err
	}
	receipt.ID, receipt.ToolCallID, receipt.WorkspaceID = foundation.ID(id), foundation.ID(toolCallID), foundation.ID(workspaceID)
	receipt.WorkflowRunID, receipt.NodeRunID, receipt.NodeAttemptID = foundation.ID(workflowRunID), foundation.ID(nodeRunID), foundation.ID(nodeAttemptID)
	receipt.PersistencePolicy = domain.ResultPersistencePolicy(persistencePolicy)
	receipt.Output = append(json.RawMessage(nil), outputDocument...)
	receipt.CreatedAt = receipt.CreatedAt.UTC()
	hasBinding := bindingSchemaID != nil || bindingSchemaVersion != nil || len(bindingDocument) != 0 || bindingHash != nil || bindingBytes != nil
	if hasBinding {
		if bindingSchemaID == nil || bindingSchemaVersion == nil || len(bindingDocument) == 0 || bindingHash == nil || bindingBytes == nil {
			return domain.ResultReceipt{}, consistency(errors.New("result receipt private binding columns are incomplete"))
		}
		receipt.PrivateBinding = &domain.ResultReceiptPrivateBinding{
			Schema:   domain.SchemaRef{ID: *bindingSchemaID, Version: *bindingSchemaVersion},
			Document: append(json.RawMessage(nil), bindingDocument...), Hash: *bindingHash, Bytes: *bindingBytes,
		}
	}
	return receipt, nil
}

func (repository *Repository) recoverReceiptCompletionAfterCommitError(
	ctx context.Context,
	requestedCall domain.ToolCall,
	definition domain.Definition,
	requestedReceipt domain.ResultReceipt,
	operationID foundation.ID,
	commitErr error,
) (application.ResultReceiptMutationResult, error) {
	tx, beginErr := repository.begin(ctx)
	if beginErr != nil {
		return application.ResultReceiptMutationResult{}, receiptFinalizationUnknown(errors.Join(commitErr, beginErr))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	existingCall, loadCallErr := loadCallByID(ctx, tx, requestedCall.WorkspaceID, requestedCall.ID)
	if loadCallErr != nil || !sameTerminalResult(existingCall, requestedCall) {
		return application.ResultReceiptMutationResult{}, receiptFinalizationUnknown(errors.Join(commitErr, loadCallErr))
	}
	receipt, loadReceiptErr := loadClosedResultReceipt(ctx, tx, existingCall, definition)
	if loadReceiptErr != nil || !sameRequestedResultReceipt(receipt, requestedReceipt) {
		return application.ResultReceiptMutationResult{}, receiptFinalizationUnknown(errors.Join(commitErr, loadReceiptErr))
	}
	locked, lockErr := lockReceiptCompletion(ctx, tx, existingCall)
	if lockErr != nil || locked.operation.id != operationID {
		return application.ResultReceiptMutationResult{}, receiptFinalizationUnknown(errors.Join(commitErr, lockErr))
	}
	if eventErr := repository.appendWorkspaceAnalysisToolCompleted(ctx, tx, locked.operation, locked.call, "succeeded", true); eventErr != nil {
		return application.ResultReceiptMutationResult{}, receiptFinalizationUnknown(errors.Join(commitErr, eventErr))
	}
	if err := tx.Commit(ctx); err != nil {
		return application.ResultReceiptMutationResult{}, receiptFinalizationUnknown(errors.Join(commitErr, err))
	}
	return application.ResultReceiptMutationResult{Call: existingCall, Receipt: receipt, OperationID: operationID, Replayed: true}, nil
}

func sameRequestedResultReceipt(existing, requested domain.ResultReceipt) bool {
	if existing.ID != requested.ID || existing.ToolCallID != requested.ToolCallID || existing.Tool != requested.Tool ||
		existing.OutputSchema != requested.OutputSchema || existing.DefinitionHash != requested.DefinitionHash ||
		!bytes.Equal(existing.Output, requested.Output) {
		return false
	}
	if existing.PrivateBinding == nil || requested.PrivateBinding == nil {
		return existing.PrivateBinding == nil && requested.PrivateBinding == nil
	}
	return existing.PrivateBinding.Schema == requested.PrivateBinding.Schema &&
		bytes.Equal(existing.PrivateBinding.Document, requested.PrivateBinding.Document)
}

func classifyReceiptLockError(err error) error {
	if noRows(err) {
		return receiptNotFound(err)
	}
	return classifyScan(err)
}

func stringPointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

var _ application.ResultReceiptRepository = (*Repository)(nil)
