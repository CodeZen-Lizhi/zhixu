package postgres

import (
	"context"
	"encoding/json"
	"errors"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	"gorm.io/gorm"
)

const gormResultReceiptColumns = `
	id::text,tool_call_id::text,workspace_id::text,workflow_run_id::text,node_run_id::text,node_attempt_id::text,
	tool_name,tool_version,output_schema_id,output_schema_version,definition_hash,persistence_policy,
	max_output_bytes,max_private_binding_bytes,output_document,output_hash,output_bytes,
	server_binding_schema_id,server_binding_schema_version,server_binding_document,server_binding_hash,server_binding_bytes,created_at`

func (repository *GORMWorkspaceAnalysisRepository) FinalizeCallWithReceipt(ctx context.Context, command application.FinalizeCallWithReceiptCommand) (application.ResultReceiptMutationResult, error) {
	call, requested, err := validateFinalizeCallWithReceiptCommand(command)
	if err != nil {
		return application.ResultReceiptMutationResult{}, err
	}
	var result application.ResultReceiptMutationResult
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
		if !found {
			return receiptNotFound(errors.New("Workspace Analysis Tool operation is absent"))
		}
		lockedCall, err := gormLockWorkspaceAnalysisToolCall(callbackCtx, database, call.WorkspaceID, call.ID)
		if err != nil {
			return err
		}
		if !sameStartBinding(lockedCall, call, true) || snapshot.Operation.ID == "" || snapshot.Reservation == nil {
			return receiptConflict(errors.New("Workspace Analysis Tool receipt binding differs"))
		}
		closureQuery = gormWorkspaceAnalysisToolClosureQuery(snapshot, lockedCall)
		if snapshot.Operation.Status == agentdomain.WorkspaceAnalysisOperationSucceeded {
			receipt, loadErr := gormLoadResultReceipt(callbackCtx, database, lockedCall, command.Definition)
			if loadErr != nil || !sameRequestedResultReceipt(receipt, requested) {
				return receiptConflict(errors.New("Workspace Analysis receipt replay differs"))
			}
			if err := repository.appendCompleted(callbackCtx, scope, snapshot.Operation, lockedCall, "succeeded", true); err != nil {
				return err
			}
			result = application.ResultReceiptMutationResult{Call: lockedCall, Receipt: receipt, OperationID: snapshot.Operation.ID, Replayed: true}
			callbackCompleted = true
			return nil
		}
		if snapshot.Operation.Status != agentdomain.WorkspaceAnalysisOperationStarted || snapshot.Reservation.Status != agentdomain.WorkspaceAnalysisBudgetReserved || lockedCall.Status != domain.CallStarted {
			return receiptConflict(errors.New("Workspace Analysis receipt is not startable"))
		}
		completed, err := gormFinalizeWorkspaceAnalysisCall(callbackCtx, database, call, command.ExpectedVersion)
		if err != nil {
			return err
		}
		receipt, err := resultReceiptAtDatabaseCompletion(requested, completed, command.Definition)
		if err != nil {
			return err
		}
		if err := gormInsertResultReceipt(callbackCtx, database, receipt); err != nil {
			return err
		}
		settled, err := repository.participant.SettleWorkspaceAnalysisToolOperationScoped(callbackCtx, scope, gormSettlementCommand(identity, snapshot, completed, agentapplication.WorkspaceAnalysisToolSettlementSucceeded, &agentdomain.WorkspaceAnalysisOperationResultRef{Kind: agentdomain.WorkspaceAnalysisOperationResultToolReceipt, ID: receipt.ID, Hash: receipt.OutputHash}, ""))
		if err != nil {
			return err
		}
		if err := repository.appendCompleted(callbackCtx, scope, settled.Operation, completed, "succeeded", false); err != nil {
			return err
		}
		if err := gormToolsExec(database, "SET CONSTRAINTS ALL IMMEDIATE"); err != nil {
			return classifyGORMTools(callbackCtx, err)
		}
		result = application.ResultReceiptMutationResult{Call: completed, Receipt: receipt, OperationID: settled.Operation.ID}
		callbackCompleted = true
		return nil
	})
	if err != nil {
		if callbackCompleted && gormToolsCommitFailure(err) {
			return repository.gormRecoverReceiptCompletionAfterCommitError(ctx, call, command.Definition, requested, closureQuery, err)
		}
		return application.ResultReceiptMutationResult{}, classifyGORMTools(ctx, err)
	}
	return result, nil
}

func (repository *GORMWorkspaceAnalysisRepository) LoadResultReceipt(ctx context.Context, command application.LoadResultReceiptCommand) (domain.ResultReceipt, error) {
	if err := domain.ValidateToolCall(command.Call); err != nil || command.Call.Status != domain.CallSucceeded {
		if err == nil {
			err = errors.New("result receipt lookup requires success")
		}
		return domain.ResultReceipt{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeResultReceiptInvalid, false, err)
	}
	var result domain.ResultReceipt
	err := repository.readWithin(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, database *gorm.DB, _ foundation.TransactionScope) error {
		receipt, err := gormLoadResultReceipt(callbackCtx, database, command.Call, command.Definition)
		if err != nil {
			return err
		}
		result = receipt
		return nil
	})
	if err != nil {
		return domain.ResultReceipt{}, classifyGORMTools(ctx, err)
	}
	return result, nil
}

func gormLockWorkspaceAnalysisToolCall(ctx context.Context, database *gorm.DB, workspaceID, callID foundation.ID) (domain.ToolCall, error) {
	row, err := gormToolsRawRow(database, `SELECT `+gormToolCallColumns+` FROM workflow.tool_call WHERE workspace_id=? AND id=? FOR UPDATE`, string(workspaceID), string(callID))
	if err != nil {
		return domain.ToolCall{}, classifyGORMTools(ctx, err)
	}
	call, err := scanGORMToolCall(row)
	if gormToolsNoRows(err) {
		return domain.ToolCall{}, receiptNotFound(err)
	}
	if err != nil {
		return domain.ToolCall{}, classifyGORMTools(ctx, err)
	}
	return call, nil
}

func gormFinalizeWorkspaceAnalysisCall(ctx context.Context, database *gorm.DB, call domain.ToolCall, version int64) (domain.ToolCall, error) {
	row, err := gormToolsRawRow(database, `UPDATE workflow.tool_call SET status=?,error_code=?,retryable=?,response_hash=?,response_bytes=?,response_summary=?,result_ref=?,side_effect_type=?,side_effect_id=?,version=version+1,completed_at=clock_timestamp(),duration_ms=GREATEST(0, FLOOR(EXTRACT(EPOCH FROM (clock_timestamp()-started_at))*1000)::bigint) WHERE id=? AND workspace_id=? AND status='STARTED' AND version=? RETURNING `+gormToolCallColumns,
		string(call.Status), nullableGORMText(call.ErrorCode), call.Retryable, nullableGORMText(call.ResponseHash), call.ResponseBytes, gormOptionalJSONB(call.ResponseSummary), nullableGORMText(call.ResultRef), nullableGORMText(call.SideEffectType), nullableGORMText(call.SideEffectID), string(call.ID), string(call.WorkspaceID), version)
	if err != nil {
		return domain.ToolCall{}, classifyGORMTools(ctx, err)
	}
	completed, err := scanGORMToolCall(row)
	if gormToolsNoRows(err) {
		return domain.ToolCall{}, receiptConflict(err)
	}
	if err != nil {
		return domain.ToolCall{}, classifyGORMTools(ctx, err)
	}
	return completed, nil
}

func nullableGORMText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func gormOptionalJSONB(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return gormToolsJSONB(value)
}

func gormSettlementCommand(identity agentapplication.WorkspaceAnalysisToolExecutionIdentity, snapshot agentapplication.WorkspaceAnalysisToolOperationSnapshot, call domain.ToolCall, kind agentapplication.WorkspaceAnalysisToolSettlementKind, result *agentdomain.WorkspaceAnalysisOperationResultRef, code string) agentapplication.SettleWorkspaceAnalysisToolOperationCommand {
	return agentapplication.SettleWorkspaceAnalysisToolOperationCommand{Identity: identity, OperationKey: snapshot.Operation.LogicalKey(), OperationID: snapshot.Operation.ID, ReservationID: snapshot.Reservation.ID, ToolCallID: call.ID, RequestHash: call.RequestHash, ExpectedRunVersion: snapshot.Run.Version, ExpectedOperationVersion: snapshot.Operation.Version, Kind: kind, Result: result, ErrorCode: code, CompletedAt: *call.CompletedAt}
}

func gormInsertResultReceipt(ctx context.Context, database *gorm.DB, receipt domain.ResultReceipt) error {
	var schemaID, schemaVersion, document, hash, bytesValue any
	if receipt.PrivateBinding != nil {
		schemaID, schemaVersion, document, hash, bytesValue = receipt.PrivateBinding.Schema.ID, receipt.PrivateBinding.Schema.Version, []byte(receipt.PrivateBinding.Document), receipt.PrivateBinding.Hash, receipt.PrivateBinding.Bytes
	}
	return classifyGORMToolsReceiptWrite(ctx, gormToolsExec(database, `INSERT INTO workflow.tool_result_receipt(id,tool_call_id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,tool_name,tool_version,output_schema_id,output_schema_version,definition_hash,persistence_policy,max_output_bytes,max_private_binding_bytes,output_document,output_hash,output_bytes,server_binding_schema_id,server_binding_schema_version,server_binding_document,server_binding_hash,server_binding_bytes,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, string(receipt.ID), string(receipt.ToolCallID), string(receipt.WorkspaceID), string(receipt.WorkflowRunID), string(receipt.NodeRunID), string(receipt.NodeAttemptID), receipt.Tool.Name, receipt.Tool.Version, receipt.OutputSchema.ID, receipt.OutputSchema.Version, receipt.DefinitionHash, string(receipt.PersistencePolicy), receipt.MaxOutputBytes, receipt.MaxPrivateBindingBytes, []byte(receipt.Output), receipt.OutputHash, receipt.OutputBytes, schemaID, schemaVersion, document, hash, bytesValue, receipt.CreatedAt))
}

func gormLoadResultReceipt(ctx context.Context, database *gorm.DB, call domain.ToolCall, definition domain.Definition) (domain.ResultReceipt, error) {
	row, err := gormToolsRawRow(database, `SELECT `+gormResultReceiptColumns+` FROM workflow.tool_result_receipt WHERE tool_call_id=? AND workspace_id=? AND workflow_run_id=? AND node_run_id=? AND node_attempt_id=?`, string(call.ID), string(call.WorkspaceID), string(call.WorkflowRunID), string(call.NodeRunID), string(call.NodeAttemptID))
	if err != nil {
		return domain.ResultReceipt{}, classifyGORMTools(ctx, err)
	}
	receipt, err := scanGORMResultReceipt(row)
	if gormToolsNoRows(err) {
		return domain.ResultReceipt{}, receiptNotFound(err)
	}
	if err != nil {
		return domain.ResultReceipt{}, classifyGORMTools(ctx, err)
	}
	if err := domain.ValidateResultReceipt(receipt, call, definition); err != nil {
		return domain.ResultReceipt{}, consistency(err)
	}
	return receipt, nil
}

func scanGORMResultReceipt(row rowScanner) (domain.ResultReceipt, error) {
	var receipt domain.ResultReceipt
	var id, callID, workspaceID, workflowRunID, nodeRunID, nodeAttemptID, policy string
	var output, binding gormToolsBytes
	var bindingSchemaID, bindingHash *string
	var bindingSchemaVersion, bindingBytes *int64
	if err := row.Scan(&id, &callID, &workspaceID, &workflowRunID, &nodeRunID, &nodeAttemptID, &receipt.Tool.Name, &receipt.Tool.Version, &receipt.OutputSchema.ID, &receipt.OutputSchema.Version, &receipt.DefinitionHash, &policy, &receipt.MaxOutputBytes, &receipt.MaxPrivateBindingBytes, &output, &receipt.OutputHash, &receipt.OutputBytes, &bindingSchemaID, &bindingSchemaVersion, &binding, &bindingHash, &bindingBytes, &receipt.CreatedAt); err != nil {
		return domain.ResultReceipt{}, err
	}
	receipt.ID, receipt.ToolCallID, receipt.WorkspaceID, receipt.WorkflowRunID, receipt.NodeRunID, receipt.NodeAttemptID = foundation.ID(id), foundation.ID(callID), foundation.ID(workspaceID), foundation.ID(workflowRunID), foundation.ID(nodeRunID), foundation.ID(nodeAttemptID)
	receipt.PersistencePolicy = domain.ResultPersistencePolicy(policy)
	receipt.Output = append(json.RawMessage(nil), output...)
	receipt.CreatedAt = receipt.CreatedAt.UTC()
	if bindingSchemaID != nil || bindingSchemaVersion != nil || len(binding) != 0 || bindingHash != nil || bindingBytes != nil {
		if bindingSchemaID == nil || bindingSchemaVersion == nil || len(binding) == 0 || bindingHash == nil || bindingBytes == nil {
			return domain.ResultReceipt{}, consistency(errors.New("result receipt binding columns are incomplete"))
		}
		receipt.PrivateBinding = &domain.ResultReceiptPrivateBinding{Schema: domain.SchemaRef{ID: *bindingSchemaID, Version: *bindingSchemaVersion}, Document: append(json.RawMessage(nil), binding...), Hash: *bindingHash, Bytes: *bindingBytes}
	}
	return receipt, nil
}

func (repository *GORMWorkspaceAnalysisRepository) gormRecoverReceiptCompletionAfterCommitError(
	ctx context.Context,
	requestedCall domain.ToolCall,
	definition domain.Definition,
	requestedReceipt domain.ResultReceipt,
	query agentapplication.WorkspaceAnalysisToolClosureQuery,
	commitErr error,
) (application.ResultReceiptMutationResult, error) {
	var result application.ResultReceiptMutationResult
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, database *gorm.DB, scope foundation.TransactionScope) error {
		closure, call, err := repository.gormVerifyWorkspaceAnalysisToolClosure(callbackCtx, database, scope, query, requestedCall)
		if err != nil {
			return err
		}
		if closure.Operation.Status != agentdomain.WorkspaceAnalysisOperationSucceeded || !sameTerminalResult(call, requestedCall) {
			return consistency(errors.New("Workspace Analysis recovered receipt Call closure differs"))
		}
		receipt, err := gormLoadResultReceipt(callbackCtx, database, call, definition)
		if err != nil || !sameRequestedResultReceipt(receipt, requestedReceipt) {
			if err == nil {
				err = errors.New("Workspace Analysis recovered receipt binding differs")
			}
			return receiptFinalizationUnknown(errors.Join(commitErr, err))
		}
		if err := repository.appendCompleted(callbackCtx, scope, closure.Operation, call, "succeeded", true); err != nil {
			return err
		}
		result = application.ResultReceiptMutationResult{Call: call, Receipt: receipt, OperationID: closure.Operation.ID, Replayed: true}
		return nil
	})
	if err == nil {
		return result, nil
	}
	return application.ResultReceiptMutationResult{}, receiptFinalizationUnknown(errors.Join(commitErr, err))
}

var _ application.ResultReceiptRepository = (*GORMWorkspaceAnalysisRepository)(nil)
