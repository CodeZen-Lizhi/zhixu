package postgres

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

const gormToolCallColumns = `
	id::text,workspace_id::text,workflow_run_id::text,node_run_id::text,node_attempt_id::text,call_no,
	requested_tool_name,tool_version,definition_hash,input_schema_id,input_schema_version,
	output_schema_id,output_schema_version,capability,side_effect_level,invocation_policy,
	idempotency_key,request_hash,request_bytes,request_summary,
	response_hash,response_bytes,response_summary,result_ref,side_effect_type,side_effect_id,
	status,error_code,retryable,version,started_at,completed_at,duration_ms`

func scanGORMToolCall(row rowScanner, queryErrors ...error) (domain.ToolCall, error) {
	if len(queryErrors) > 1 {
		return domain.ToolCall{}, consistency(errors.New("Tools GORM query returned multiple errors"))
	}
	if len(queryErrors) == 1 && queryErrors[0] != nil {
		return domain.ToolCall{}, queryErrors[0]
	}
	var call domain.ToolCall
	var id, workspaceID, workflowRunID, nodeRunID, nodeAttemptID string
	var toolVersion, inputSchemaVersion, outputSchemaVersion *int64
	var definitionHash, inputSchemaID, outputSchemaID, capabilityValue *string
	var sideEffectLevel, invocationPolicy, idempotencyKey *string
	var responseHash, resultRef, sideEffectType, sideEffectID, errorCode *string
	var status string
	var requestSummary, responseSummary *gormToolsJSONB
	if err := row.Scan(
		&id, &workspaceID, &workflowRunID, &nodeRunID, &nodeAttemptID, &call.CallNo,
		&call.RequestedToolName, &toolVersion, &definitionHash, &inputSchemaID, &inputSchemaVersion,
		&outputSchemaID, &outputSchemaVersion, &capabilityValue, &sideEffectLevel, &invocationPolicy,
		&idempotencyKey, &call.RequestHash, &call.RequestBytes, &requestSummary,
		&responseHash, &call.ResponseBytes, &responseSummary, &resultRef, &sideEffectType, &sideEffectID,
		&status, &errorCode, &call.Retryable, &call.Version, &call.StartedAt, &call.CompletedAt, &call.DurationMillis,
	); err != nil {
		return domain.ToolCall{}, err
	}
	call.ID, call.WorkspaceID = foundation.ID(id), foundation.ID(workspaceID)
	call.WorkflowRunID, call.NodeRunID, call.NodeAttemptID = foundation.ID(workflowRunID), foundation.ID(nodeRunID), foundation.ID(nodeAttemptID)
	call.Status = domain.CallStatus(status)
	if requestSummary != nil {
		call.RequestSummary = append(json.RawMessage(nil), (*requestSummary)...)
	}
	if toolVersion != nil {
		call.Tool = &domain.ToolRef{Name: call.RequestedToolName, Version: *toolVersion}
	}
	if definitionHash != nil {
		call.DefinitionHash = *definitionHash
	}
	if inputSchemaID != nil && inputSchemaVersion != nil {
		call.InputSchema = &domain.SchemaRef{ID: *inputSchemaID, Version: *inputSchemaVersion}
	}
	if outputSchemaID != nil && outputSchemaVersion != nil {
		call.OutputSchema = &domain.SchemaRef{ID: *outputSchemaID, Version: *outputSchemaVersion}
	}
	if capabilityValue != nil {
		call.Capability = capability.Capability(*capabilityValue)
	}
	if sideEffectLevel != nil {
		call.SideEffectLevel = domain.SideEffectLevel(*sideEffectLevel)
	}
	if invocationPolicy != nil {
		call.InvocationPolicy = domain.InvocationPolicy(*invocationPolicy)
	}
	if idempotencyKey != nil {
		call.IdempotencyKey = *idempotencyKey
	}
	if responseHash != nil {
		call.ResponseHash = *responseHash
	}
	if responseSummary != nil {
		call.ResponseSummary = append(json.RawMessage(nil), (*responseSummary)...)
	}
	if resultRef != nil {
		call.ResultRef = *resultRef
	}
	if sideEffectType != nil {
		call.SideEffectType = *sideEffectType
	}
	if sideEffectID != nil {
		call.SideEffectID = *sideEffectID
	}
	if errorCode != nil {
		call.ErrorCode = *errorCode
	}
	call.StartedAt = call.StartedAt.UTC()
	if call.CompletedAt != nil {
		completedAt := call.CompletedAt.UTC()
		call.CompletedAt = &completedAt
	}
	if err := domain.ValidateToolCall(call); err != nil {
		return domain.ToolCall{}, consistency(err)
	}
	return call, nil
}

func gormScanToolCallRow(row *sql.Row, queryErr error) (domain.ToolCall, error) {
	return scanGORMToolCall(row, queryErr)
}

var _ rowScanner = (*sql.Row)(nil)
