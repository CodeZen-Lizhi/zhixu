package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	"github.com/jackc/pgx/v5"
)

type rowScanner interface {
	Scan(...any) error
}

const toolCallColumns = `
	id::text,workspace_id::text,workflow_run_id::text,node_run_id::text,node_attempt_id::text,call_no,
	requested_tool_name,tool_version,definition_hash,input_schema_id,input_schema_version,
	output_schema_id,output_schema_version,capability,side_effect_level,invocation_policy,
	idempotency_key,request_hash,request_bytes,request_summary,
	response_hash,response_bytes,response_summary,result_ref,side_effect_type,side_effect_id,
	status,error_code,retryable,version,started_at,completed_at,duration_ms`

func scanToolCall(row rowScanner) (domain.ToolCall, error) {
	var call domain.ToolCall
	var id, workspaceID, workflowRunID, nodeRunID, nodeAttemptID string
	var toolVersion, inputSchemaVersion, outputSchemaVersion *int64
	var definitionHash, inputSchemaID, outputSchemaID, capabilityValue *string
	var sideEffectLevel, invocationPolicy, idempotencyKey *string
	var responseHash, resultRef, sideEffectType, sideEffectID, errorCode *string
	var responseSummary []byte
	var status string
	if err := row.Scan(
		&id, &workspaceID, &workflowRunID, &nodeRunID, &nodeAttemptID, &call.CallNo,
		&call.RequestedToolName, &toolVersion, &definitionHash, &inputSchemaID, &inputSchemaVersion,
		&outputSchemaID, &outputSchemaVersion, &capabilityValue, &sideEffectLevel, &invocationPolicy,
		&idempotencyKey, &call.RequestHash, &call.RequestBytes, &call.RequestSummary,
		&responseHash, &call.ResponseBytes, &responseSummary, &resultRef, &sideEffectType, &sideEffectID,
		&status, &errorCode, &call.Retryable, &call.Version, &call.StartedAt, &call.CompletedAt, &call.DurationMillis,
	); err != nil {
		return domain.ToolCall{}, err
	}
	call.ID, call.WorkspaceID = foundation.ID(id), foundation.ID(workspaceID)
	call.WorkflowRunID, call.NodeRunID, call.NodeAttemptID = foundation.ID(workflowRunID), foundation.ID(nodeRunID), foundation.ID(nodeAttemptID)
	call.Status = domain.CallStatus(status)
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
	if len(responseSummary) != 0 {
		call.ResponseSummary = json.RawMessage(responseSummary)
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
		value := call.CompletedAt.UTC()
		call.CompletedAt = &value
	}
	if err := domain.ValidateToolCall(call); err != nil {
		return domain.ToolCall{}, consistency(err)
	}
	return call, nil
}

func loadCallByID(ctx context.Context, tx pgx.Tx, workspaceID, callID foundation.ID) (domain.ToolCall, error) {
	return scanToolCall(tx.QueryRow(ctx, `SELECT `+toolCallColumns+` FROM workflow.tool_call WHERE workspace_id=$1 AND id=$2`, string(workspaceID), string(callID)))
}

func loadCallByAttemptNumber(ctx context.Context, tx pgx.Tx, attemptID foundation.ID, callNo int) (domain.ToolCall, error) {
	return scanToolCall(tx.QueryRow(ctx, `SELECT `+toolCallColumns+` FROM workflow.tool_call WHERE node_attempt_id=$1 AND call_no=$2`, string(attemptID), callNo))
}

func loadActiveCallByAttempt(ctx context.Context, tx pgx.Tx, attemptID foundation.ID) (domain.ToolCall, error) {
	return scanToolCall(tx.QueryRow(ctx, `SELECT `+toolCallColumns+` FROM workflow.tool_call WHERE node_attempt_id=$1 AND status='STARTED'`, string(attemptID)))
}

func loadSideEffectCallByKey(ctx context.Context, tx pgx.Tx, workspaceID foundation.ID, key string) (domain.ToolCall, error) {
	return scanToolCall(tx.QueryRow(ctx, `SELECT `+toolCallColumns+` FROM workflow.tool_call
		WHERE workspace_id=$1 AND idempotency_key=$2 AND status<>'REFUSED'
		  AND side_effect_level IN ('DOMAIN_WRITE','IRREVERSIBLE_OR_UNKNOWN')`, string(workspaceID), key))
}

func sameStartBinding(existing, requested domain.ToolCall, includeCallNo bool) bool {
	if includeCallNo && existing.CallNo != requested.CallNo {
		return false
	}
	return existing.WorkspaceID == requested.WorkspaceID && existing.WorkflowRunID == requested.WorkflowRunID &&
		existing.NodeRunID == requested.NodeRunID && existing.NodeAttemptID == requested.NodeAttemptID &&
		existing.RequestedToolName == requested.RequestedToolName && sameToolRef(existing.Tool, requested.Tool) &&
		existing.DefinitionHash == requested.DefinitionHash && sameSchemaRef(existing.InputSchema, requested.InputSchema) &&
		sameSchemaRef(existing.OutputSchema, requested.OutputSchema) && existing.Capability == requested.Capability &&
		existing.SideEffectLevel == requested.SideEffectLevel && existing.InvocationPolicy == requested.InvocationPolicy &&
		existing.IdempotencyKey == requested.IdempotencyKey && existing.RequestHash == requested.RequestHash &&
		existing.RequestBytes == requested.RequestBytes && jsonEqual(existing.RequestSummary, requested.RequestSummary)
}

func sameTerminalResult(existing, requested domain.ToolCall) bool {
	return sameStartBinding(existing, requested, true) && existing.Status == requested.Status &&
		existing.ResponseHash == requested.ResponseHash && existing.ResponseBytes == requested.ResponseBytes &&
		jsonEqual(existing.ResponseSummary, requested.ResponseSummary) && existing.ResultRef == requested.ResultRef &&
		existing.SideEffectType == requested.SideEffectType && existing.SideEffectID == requested.SideEffectID &&
		existing.ErrorCode == requested.ErrorCode && existing.Retryable == requested.Retryable && existing.Version == requested.Version
}

func sameToolRef(left, right *domain.ToolRef) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func sameSchemaRef(left, right *domain.SchemaRef) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func jsonEqual(left, right json.RawMessage) bool {
	if len(left) == 0 || len(right) == 0 {
		return len(left) == 0 && len(right) == 0
	}
	leftValue, leftErr := decodeJSONValue(left)
	rightValue, rightErr := decodeJSONValue(right)
	return leftErr == nil && rightErr == nil && deepEqualJSON(leftValue, rightValue)
}

func deepEqualJSON(left, right any) bool {
	leftEncoded, leftErr := json.Marshal(left)
	rightEncoded, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftEncoded) == string(rightEncoded)
}

func decodeJSONValue(document json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("json document contains trailing data")
	}
	return value, nil
}

func optionalText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func optionalJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func optionalToolVersion(ref *domain.ToolRef) any {
	if ref == nil {
		return nil
	}
	return ref.Version
}

func optionalSchemaID(ref *domain.SchemaRef) any {
	if ref == nil {
		return nil
	}
	return ref.ID
}

func optionalSchemaVersion(ref *domain.SchemaRef) any {
	if ref == nil {
		return nil
	}
	return ref.Version
}

func validID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func noRows(err error) bool {
	return errors.Is(err, pgx.ErrNoRows)
}
