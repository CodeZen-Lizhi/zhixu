package postgres

import (
	"context"
	"database/sql"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	"gorm.io/gorm"
)

// RecordRefused persists a deterministic pre-executor refusal. It never uses
// an execution idempotency key and keeps the Workflow policy check in scope.
func (repository *GORMRepository) RecordRefused(ctx context.Context, command application.RecordRefusedCommand) (application.ToolCallMutationResult, error) {
	call, err := validateRefusedCommand(command)
	if err != nil {
		return application.ToolCallMutationResult{}, err
	}
	if err := repository.ready(ctx); err != nil {
		return application.ToolCallMutationResult{}, err
	}
	var result application.ToolCallMutationResult
	completed := false
	err = repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, database *gorm.DB, scope foundation.TransactionScope) error {
		if _, policyErr := repository.gormResolveToolPolicyScoped(callbackCtx, scope, command.Identity); policyErr != nil {
			return policyErr
		}
		created, queryErr := gormScanToolCallQuery(database, `WITH database_time AS (SELECT clock_timestamp() AS value)
			INSERT INTO workflow.tool_call(
				id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,call_no,
				requested_tool_name,tool_version,definition_hash,input_schema_id,input_schema_version,
				output_schema_id,output_schema_version,capability,side_effect_level,invocation_policy,
				idempotency_key,request_hash,request_bytes,request_summary,
				status,error_code,retryable,version,started_at,completed_at,duration_ms
			) SELECT ?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL,?,?,?,'REFUSED',?,false,1,database_time.value,database_time.value,0
			FROM database_time ON CONFLICT DO NOTHING RETURNING `+gormToolCallColumns,
			string(call.ID), string(call.WorkspaceID), string(call.WorkflowRunID), string(call.NodeRunID), string(call.NodeAttemptID), call.CallNo,
			call.RequestedToolName, optionalToolVersion(call.Tool), optionalText(call.DefinitionHash), optionalSchemaID(call.InputSchema), optionalSchemaVersion(call.InputSchema),
			optionalSchemaID(call.OutputSchema), optionalSchemaVersion(call.OutputSchema), optionalText(string(call.Capability)), optionalText(string(call.SideEffectLevel)), optionalText(string(call.InvocationPolicy)),
			call.RequestHash, call.RequestBytes, gormToolsOptionalJSONB(call.RequestSummary), call.ErrorCode,
		)
		if queryErr == nil {
			result = application.ToolCallMutationResult{Call: created}
			completed = true
			return nil
		}
		if !gormToolsNoRows(queryErr) {
			return classifyGORMTools(callbackCtx, queryErr)
		}
		existing, loadErr := gormLoadCallByAttemptNumber(callbackCtx, database, call.NodeAttemptID, call.CallNo)
		if gormToolsNoRows(loadErr) {
			return idempotencyConflict(errors.New("refused call identity conflicts with existing record"))
		}
		if loadErr != nil {
			return classifyGORMTools(callbackCtx, loadErr)
		}
		if !sameTerminalResult(existing, call) {
			return idempotencyConflict(errors.New("refused call replay binding differs"))
		}
		result = application.ToolCallMutationResult{Call: existing, Replayed: true}
		completed = true
		return nil
	})
	if err == nil {
		return result, nil
	}
	if completed && gormToolsCommitFailure(err) {
		return repository.gormRecoverRefusedAfterCommitError(ctx, call, err)
	}
	return application.ToolCallMutationResult{}, classifyGORMTools(ctx, err)
}

// StartCall records the immutable execution binding and grants executor rights
// only to the transaction that created the STARTED call.
func (repository *GORMRepository) StartCall(ctx context.Context, command application.StartCallCommand) (application.StartCallResult, error) {
	call, err := validateStartCommand(command)
	if err != nil {
		return application.StartCallResult{}, err
	}
	if err := repository.ready(ctx); err != nil {
		return application.StartCallResult{}, err
	}
	var result application.StartCallResult
	completed := false
	err = repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, database *gorm.DB, scope foundation.TransactionScope) error {
		policy, policyErr := repository.gormResolveToolPolicyScoped(callbackCtx, scope, command.Identity)
		if policyErr != nil {
			return policyErr
		}
		if validationErr := validateStartPolicy(policy, command); validationErr != nil {
			return validationErr
		}
		created, queryErr := gormScanToolCallQuery(database, `INSERT INTO workflow.tool_call(
			id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,call_no,
			requested_tool_name,tool_version,definition_hash,input_schema_id,input_schema_version,
			output_schema_id,output_schema_version,capability,side_effect_level,invocation_policy,
			idempotency_key,request_hash,request_bytes,request_summary,status,retryable,version,started_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'STARTED',false,1,clock_timestamp())
		ON CONFLICT DO NOTHING RETURNING `+gormToolCallColumns,
			string(call.ID), string(call.WorkspaceID), string(call.WorkflowRunID), string(call.NodeRunID), string(call.NodeAttemptID), call.CallNo,
			call.RequestedToolName, call.Tool.Version, call.DefinitionHash, call.InputSchema.ID, call.InputSchema.Version,
			call.OutputSchema.ID, call.OutputSchema.Version, optionalText(string(call.Capability)), string(call.SideEffectLevel), string(call.InvocationPolicy),
			optionalText(call.IdempotencyKey), call.RequestHash, call.RequestBytes, gormToolsOptionalJSONB(call.RequestSummary),
		)
		if queryErr == nil {
			result = application.StartCallResult{Call: created, Disposition: application.StartCallCreated}
			completed = true
			return nil
		}
		if !gormToolsNoRows(queryErr) {
			return classifyGORMTools(callbackCtx, queryErr)
		}
		existing, replayErr := gormFindReplayCandidate(callbackCtx, database, call)
		if replayErr != nil {
			return replayErr
		}
		result = application.StartCallResult{Call: existing, Disposition: application.StartCallReplayed}
		completed = true
		return nil
	})
	if err == nil {
		return result, nil
	}
	if completed && gormToolsCommitFailure(err) {
		return repository.gormRecoverStartedAfterCommitError(ctx, call, err)
	}
	return application.StartCallResult{}, classifyGORMTools(ctx, err)
}

func (repository *GORMRepository) FinalizeCall(ctx context.Context, command application.FinalizeCallCommand) (application.ToolCallMutationResult, error) {
	call, err := validateTerminalCommand(command.ExpectedVersion, command.Call, false)
	if err != nil {
		return application.ToolCallMutationResult{}, err
	}
	return repository.gormFinalize(ctx, command.ExpectedVersion, call)
}

func (repository *GORMRepository) MarkUnknown(ctx context.Context, command application.MarkUnknownCommand) (application.ToolCallMutationResult, error) {
	call, err := validateTerminalCommand(command.ExpectedVersion, command.Call, true)
	if err != nil {
		return application.ToolCallMutationResult{}, err
	}
	return repository.gormFinalize(ctx, command.ExpectedVersion, call)
}

func (repository *GORMRepository) gormFinalize(ctx context.Context, expectedVersion int64, call domain.ToolCall) (application.ToolCallMutationResult, error) {
	if err := repository.ready(ctx); err != nil {
		return application.ToolCallMutationResult{}, err
	}
	var result application.ToolCallMutationResult
	completed := false
	err := repository.within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, database *gorm.DB, _ foundation.TransactionScope) error {
		mutation, mutationErr := gormFinalizeCall(callbackCtx, database, expectedVersion, call)
		if mutationErr != nil {
			return mutationErr
		}
		result, completed = mutation, true
		return nil
	})
	if err == nil {
		return result, nil
	}
	if completed && gormToolsCommitFailure(err) {
		return repository.gormRecoverTerminalAfterCommitError(ctx, call, err)
	}
	return application.ToolCallMutationResult{}, classifyGORMTools(ctx, err)
}

func gormFinalizeCall(ctx context.Context, database *gorm.DB, expectedVersion int64, call domain.ToolCall) (application.ToolCallMutationResult, error) {
	updated, err := gormScanToolCallQuery(database, `UPDATE workflow.tool_call SET
		response_hash=?,response_bytes=?,response_summary=?,result_ref=?,side_effect_type=?,side_effect_id=?,status=?,error_code=?,retryable=?,
		version=?,completed_at=clock_timestamp(),duration_ms=GREATEST(0,floor(extract(epoch FROM (clock_timestamp()-started_at))*1000)::bigint)
		WHERE id=? AND workspace_id=? AND workflow_run_id=? AND node_run_id=? AND node_attempt_id=? AND call_no=?
		  AND status='STARTED' AND version=? AND requested_tool_name=?
		  AND tool_version IS NOT DISTINCT FROM ? AND definition_hash IS NOT DISTINCT FROM ?
		  AND input_schema_id IS NOT DISTINCT FROM ? AND input_schema_version IS NOT DISTINCT FROM ?
		  AND output_schema_id IS NOT DISTINCT FROM ? AND output_schema_version IS NOT DISTINCT FROM ?
		  AND capability IS NOT DISTINCT FROM ? AND side_effect_level IS NOT DISTINCT FROM ? AND invocation_policy IS NOT DISTINCT FROM ?
		  AND idempotency_key IS NOT DISTINCT FROM ? AND request_hash=? AND request_bytes=? AND request_summary=?
		RETURNING `+gormToolCallColumns,
		optionalText(call.ResponseHash), call.ResponseBytes, gormToolsOptionalJSONB(call.ResponseSummary), optionalText(call.ResultRef), optionalText(call.SideEffectType), optionalText(call.SideEffectID),
		string(call.Status), optionalText(call.ErrorCode), call.Retryable, call.Version,
		string(call.ID), string(call.WorkspaceID), string(call.WorkflowRunID), string(call.NodeRunID), string(call.NodeAttemptID), call.CallNo, expectedVersion,
		call.RequestedToolName, optionalToolVersion(call.Tool), optionalText(call.DefinitionHash), optionalSchemaID(call.InputSchema), optionalSchemaVersion(call.InputSchema),
		optionalSchemaID(call.OutputSchema), optionalSchemaVersion(call.OutputSchema), optionalText(string(call.Capability)), optionalText(string(call.SideEffectLevel)), optionalText(string(call.InvocationPolicy)),
		optionalText(call.IdempotencyKey), call.RequestHash, call.RequestBytes, gormToolsOptionalJSONB(call.RequestSummary),
	)
	if err == nil {
		return application.ToolCallMutationResult{Call: updated}, nil
	}
	if !gormToolsNoRows(err) {
		return application.ToolCallMutationResult{}, classifyGORMTools(ctx, err)
	}
	existing, loadErr := gormLoadCallByID(ctx, database, call.WorkspaceID, call.ID)
	if gormToolsNoRows(loadErr) {
		return application.ToolCallMutationResult{}, notFound(loadErr)
	}
	if loadErr != nil {
		return application.ToolCallMutationResult{}, classifyGORMTools(ctx, loadErr)
	}
	if !sameTerminalResult(existing, call) {
		return application.ToolCallMutationResult{}, versionConflict(errors.New("tool call terminal result differs"))
	}
	return application.ToolCallMutationResult{Call: existing, Replayed: true}, nil
}

// ListTimeline reads a bounded Tool-owned timeline in deterministic order.
func (repository *GORMRepository) ListTimeline(ctx context.Context, query application.ToolCallTimelineQuery) ([]domain.ToolCall, error) {
	if !validID(query.WorkspaceID) || !validID(query.WorkflowRunID) || (query.NodeRunID != "" && !validID(query.NodeRunID)) || query.Limit < 1 || query.Limit > application.MaxToolCallTimelineLimit {
		return nil, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeCallInvalid, false, errors.New("tool call timeline query is invalid"))
	}
	if err := repository.ready(ctx); err != nil {
		return nil, err
	}
	result := make([]domain.ToolCall, 0)
	err := repository.readWithin(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, database *gorm.DB, _ foundation.TransactionScope) error {
		var rows *sql.Rows
		var queryErr error
		if query.NodeRunID == "" {
			rows, queryErr = gormToolsRawRows(database, `SELECT `+gormToolCallColumns+` FROM workflow.tool_call WHERE workspace_id=? AND workflow_run_id=? ORDER BY started_at,call_no,id LIMIT ?`, string(query.WorkspaceID), string(query.WorkflowRunID), query.Limit)
		} else {
			rows, queryErr = gormToolsRawRows(database, `SELECT `+gormToolCallColumns+` FROM workflow.tool_call WHERE workspace_id=? AND workflow_run_id=? AND node_run_id=? ORDER BY started_at,call_no,id LIMIT ?`, string(query.WorkspaceID), string(query.WorkflowRunID), string(query.NodeRunID), query.Limit)
		}
		if queryErr != nil {
			return classifyGORMTools(callbackCtx, queryErr)
		}
		var previous domain.ToolCall
		for rows.Next() {
			call, scanErr := scanGORMToolCall(rows)
			if scanErr != nil {
				return gormToolsCloseRows(callbackCtx, rows, scanErr)
			}
			if len(result) > 0 && !timelineLess(previous, call) {
				return gormToolsCloseRows(callbackCtx, rows, consistency(errors.New("tool call timeline is not stably ordered")))
			}
			previous = call
			result = append(result, call)
		}
		if scanErr := rows.Err(); scanErr != nil {
			return gormToolsCloseRows(callbackCtx, rows, scanErr)
		}
		return gormToolsCloseRows(callbackCtx, rows, nil)
	})
	if err != nil {
		return nil, classifyGORMTools(ctx, err)
	}
	return result, nil
}

func gormFindReplayCandidate(ctx context.Context, database *gorm.DB, call domain.ToolCall) (domain.ToolCall, error) {
	if call.IdempotencyKey != "" && (call.SideEffectLevel == domain.SideEffectDomainWrite || call.SideEffectLevel == domain.SideEffectUnknown) {
		existing, err := gormLoadSideEffectCallByKey(ctx, database, call.WorkspaceID, call.IdempotencyKey)
		if err == nil {
			if sameStartBinding(existing, call, false) {
				return existing, nil
			}
			return domain.ToolCall{}, idempotencyConflict(errors.New("side effect idempotency binding differs"))
		}
		if !gormToolsNoRows(err) {
			return domain.ToolCall{}, classifyGORMTools(ctx, err)
		}
	}
	existing, err := gormLoadCallByAttemptNumber(ctx, database, call.NodeAttemptID, call.CallNo)
	if err == nil {
		if sameStartBinding(existing, call, true) {
			return existing, nil
		}
		return domain.ToolCall{}, idempotencyConflict(errors.New("tool call number binding differs"))
	}
	if !gormToolsNoRows(err) {
		return domain.ToolCall{}, classifyGORMTools(ctx, err)
	}
	active, err := gormLoadActiveCallByAttempt(ctx, database, call.NodeAttemptID)
	if err == nil {
		return domain.ToolCall{}, idempotencyConflict(errors.New("node attempt already has an active tool call: " + string(active.Status)))
	}
	if !gormToolsNoRows(err) {
		return domain.ToolCall{}, classifyGORMTools(ctx, err)
	}
	byID, err := gormLoadCallByID(ctx, database, call.WorkspaceID, call.ID)
	if err == nil {
		if sameStartBinding(byID, call, true) {
			return byID, nil
		}
		return domain.ToolCall{}, idempotencyConflict(errors.New("tool call id binding differs"))
	}
	if !gormToolsNoRows(err) {
		return domain.ToolCall{}, classifyGORMTools(ctx, err)
	}
	return domain.ToolCall{}, idempotencyConflict(errors.New("tool call conflicts with an existing execution"))
}

func gormScanToolCallQuery(database *gorm.DB, query string, arguments ...any) (domain.ToolCall, error) {
	row, err := gormToolsRawRow(database, query, arguments...)
	if err != nil {
		return domain.ToolCall{}, err
	}
	return scanGORMToolCall(row)
}

func gormToolsOptionalJSONB(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return gormToolsJSONB(value)
}

func gormLoadCallByID(ctx context.Context, database *gorm.DB, workspaceID, callID foundation.ID) (domain.ToolCall, error) {
	return gormScanToolCallQuery(database, `SELECT `+gormToolCallColumns+` FROM workflow.tool_call WHERE workspace_id=? AND id=?`, string(workspaceID), string(callID))
}

func gormLoadCallByAttemptNumber(ctx context.Context, database *gorm.DB, attemptID foundation.ID, callNo int) (domain.ToolCall, error) {
	return gormScanToolCallQuery(database, `SELECT `+gormToolCallColumns+` FROM workflow.tool_call WHERE node_attempt_id=? AND call_no=?`, string(attemptID), callNo)
}

func gormLoadActiveCallByAttempt(ctx context.Context, database *gorm.DB, attemptID foundation.ID) (domain.ToolCall, error) {
	return gormScanToolCallQuery(database, `SELECT `+gormToolCallColumns+` FROM workflow.tool_call WHERE node_attempt_id=? AND status='STARTED'`, string(attemptID))
}

func gormLoadSideEffectCallByKey(ctx context.Context, database *gorm.DB, workspaceID foundation.ID, key string) (domain.ToolCall, error) {
	return gormScanToolCallQuery(database, `SELECT `+gormToolCallColumns+` FROM workflow.tool_call
		WHERE workspace_id=? AND idempotency_key=? AND status<>'REFUSED'
		  AND side_effect_level IN ('DOMAIN_WRITE','IRREVERSIBLE_OR_UNKNOWN')`, string(workspaceID), key)
}

func (repository *GORMRepository) gormRecoverStartedAfterCommitError(ctx context.Context, call domain.ToolCall, commitErr error) (application.StartCallResult, error) {
	var result application.StartCallResult
	err := repository.readWithin(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, database *gorm.DB, _ foundation.TransactionScope) error {
		existing, loadErr := gormFindReplayCandidate(callbackCtx, database, call)
		if loadErr != nil {
			return loadErr
		}
		result = application.StartCallResult{Call: existing, Disposition: application.StartCallReplayed}
		return nil
	})
	if err == nil {
		return result, nil
	}
	return application.StartCallResult{}, classifyGORMTools(ctx, commitErr)
}

func (repository *GORMRepository) gormRecoverRefusedAfterCommitError(ctx context.Context, call domain.ToolCall, commitErr error) (application.ToolCallMutationResult, error) {
	var result application.ToolCallMutationResult
	err := repository.readWithin(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, database *gorm.DB, _ foundation.TransactionScope) error {
		existing, loadErr := gormLoadCallByAttemptNumber(callbackCtx, database, call.NodeAttemptID, call.CallNo)
		if loadErr != nil || !sameTerminalResult(existing, call) {
			return errors.New("refused Tool Call closure was not proven")
		}
		result = application.ToolCallMutationResult{Call: existing, Replayed: true}
		return nil
	})
	if err == nil {
		return result, nil
	}
	return application.ToolCallMutationResult{}, classifyGORMTools(ctx, commitErr)
}

func (repository *GORMRepository) gormRecoverTerminalAfterCommitError(ctx context.Context, call domain.ToolCall, commitErr error) (application.ToolCallMutationResult, error) {
	var result application.ToolCallMutationResult
	err := repository.readWithin(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, database *gorm.DB, _ foundation.TransactionScope) error {
		existing, loadErr := gormLoadCallByID(callbackCtx, database, call.WorkspaceID, call.ID)
		if loadErr != nil || !sameTerminalResult(existing, call) {
			return errors.New("terminal Tool Call closure was not proven")
		}
		result = application.ToolCallMutationResult{Call: existing, Replayed: true}
		return nil
	})
	if err == nil {
		return result, nil
	}
	return application.ToolCallMutationResult{}, classifyGORMTools(ctx, commitErr)
}

var _ application.ToolCallRepository = (*GORMRepository)(nil)
