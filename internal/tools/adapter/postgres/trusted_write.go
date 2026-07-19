package postgres

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

// StartTrustedWriteCall 在 Safe Writeback 副作用前登记预期 writeback_execution receipt。
// 当前 Attempt 只证明恢复资格；同一幂等绑定的历史 Call 保留首次 STARTED 的 Attempt 身份。
func (repository *Repository) StartTrustedWriteCall(ctx context.Context, command application.TrustedWriteStartCommand) (application.StartCallResult, error) {
	call, err := validateTrustedWriteStartCommand(command)
	if err != nil {
		return application.StartCallResult{}, err
	}
	tx, err := repository.begin(ctx)
	if err != nil {
		return application.StartCallResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	persisted, err := loadPersistedPolicy(ctx, tx, command.Identity)
	if err != nil {
		return application.StartCallResult{}, err
	}
	if err := validateTrustedWritePolicy(persisted.policy, command); err != nil {
		return application.StartCallResult{}, err
	}
	created, err := scanToolCall(tx.QueryRow(ctx, `INSERT INTO workflow.tool_call(
			id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,call_no,
			requested_tool_name,tool_version,definition_hash,input_schema_id,input_schema_version,
			output_schema_id,output_schema_version,capability,side_effect_level,invocation_policy,
			idempotency_key,request_hash,request_bytes,request_summary,side_effect_type,side_effect_id,
			status,retryable,version,started_at
		)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,'STARTED',false,1,clock_timestamp())
		ON CONFLICT DO NOTHING
		RETURNING `+toolCallColumns,
		string(call.ID), string(call.WorkspaceID), string(call.WorkflowRunID), string(call.NodeRunID), string(call.NodeAttemptID), call.CallNo,
		call.RequestedToolName, call.Tool.Version, call.DefinitionHash, call.InputSchema.ID, call.InputSchema.Version,
		call.OutputSchema.ID, call.OutputSchema.Version, string(call.Capability), string(call.SideEffectLevel), string(call.InvocationPolicy),
		call.IdempotencyKey, call.RequestHash, call.RequestBytes, call.RequestSummary, call.SideEffectType, call.SideEffectID,
	))
	if err == nil {
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return repository.recoverTrustedWriteStartAfterCommitError(ctx, call, commitErr)
		}
		return application.StartCallResult{Call: created, Disposition: application.StartCallCreated}, nil
	}
	if !noRows(err) {
		return application.StartCallResult{}, classify(err)
	}
	existing, err := loadSideEffectCallByKey(ctx, tx, call.WorkspaceID, call.IdempotencyKey)
	if err != nil {
		if noRows(err) {
			return application.StartCallResult{}, idempotencyConflict(errors.New("trusted write call conflicts with an active call number"))
		}
		return application.StartCallResult{}, classify(err)
	}
	if !sameTrustedWriteStartBinding(existing, call) {
		return application.StartCallResult{}, idempotencyConflict(errors.New("trusted write idempotency binding differs"))
	}
	if err := tx.Commit(ctx); err != nil {
		return application.StartCallResult{}, classify(err)
	}
	return application.StartCallResult{Call: existing, Disposition: application.StartCallReplayed}, nil
}

// LoadTrustedWriteCall 先验证当前 Attempt 的持久资格，再按稳定幂等键读取历史 Safe Writeback Tool Call。
func (repository *Repository) LoadTrustedWriteCall(ctx context.Context, command application.TrustedWriteLoadCommand) (domain.ToolCall, error) {
	if err := command.Identity.Validate(); err != nil || command.Tool.Validate() != nil || command.Capability == "" || command.IdempotencyKey == "" ||
		command.IdempotencyKey != strings.TrimSpace(command.IdempotencyKey) || len(command.IdempotencyKey) > domain.MaxToolIdempotencyKeyBytes {
		return domain.ToolCall{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeCallInvalid, false, errors.New("trusted write load command is invalid"))
	}
	tx, err := repository.begin(ctx)
	if err != nil {
		return domain.ToolCall{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	persisted, err := loadPersistedPolicy(ctx, tx, command.Identity)
	if err != nil {
		return domain.ToolCall{}, err
	}
	if !containsWorkflowBinding(command.AllowedWorkflows, persisted.policy.WorkflowKey, command.Identity.DefinitionVersion) {
		return domain.ToolCall{}, denied(ErrorCodeWorkflowBindingDenied, errors.New("trusted write contract does not allow current workflow definition"))
	}
	if !containsCapability(persisted.policy.Permissions, command.Capability) {
		return domain.ToolCall{}, denied(ErrorCodePermissionDenied, errors.New("trusted write workflow lacks exact capability"))
	}
	call, err := loadSideEffectCallByKey(ctx, tx, command.Identity.WorkspaceID, command.IdempotencyKey)
	if noRows(err) {
		return domain.ToolCall{}, notFound(err)
	}
	if err != nil {
		return domain.ToolCall{}, classify(err)
	}
	if call.WorkflowRunID != command.Identity.WorkflowRunID || call.NodeRunID != command.Identity.NodeRunID || call.Tool == nil || *call.Tool != command.Tool || call.Capability != command.Capability ||
		call.SideEffectType != "writeback_execution" || call.InvocationPolicy != domain.InvocationTrustedWorkflowOnly || call.SideEffectLevel != domain.SideEffectDomainWrite {
		return domain.ToolCall{}, consistency(errors.New("trusted write load resolved a non-writeback call"))
	}
	if err := tx.Commit(ctx); err != nil {
		return domain.ToolCall{}, classify(err)
	}
	return call, nil
}

func validateTrustedWriteStartCommand(command application.TrustedWriteStartCommand) (domain.ToolCall, error) {
	if err := validateIdentityCallBinding(command.Identity, command.Call); err != nil {
		return domain.ToolCall{}, err
	}
	call := command.Call
	if call.Status != domain.CallStarted || call.Version != 1 || call.Tool == nil ||
		(call.Tool.Name != "ApplyApprovedPatch" && call.Tool.Name != "CreateGitCommit") ||
		call.InvocationPolicy != domain.InvocationTrustedWorkflowOnly || call.SideEffectLevel != domain.SideEffectDomainWrite ||
		call.IdempotencyKey == "" || call.SideEffectType != "writeback_execution" || call.SideEffectID == "" {
		return domain.ToolCall{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeCallInvalid, false, errors.New("trusted write STARTED policy is invalid"))
	}
	call.StartedAt, call.CompletedAt, call.DurationMillis = time.Unix(1, 0).UTC(), nil, 0
	if err := domain.ValidateToolCall(call); err != nil {
		return domain.ToolCall{}, err
	}
	return call, nil
}

func validateTrustedWritePolicy(policy application.WorkflowToolPolicy, command application.TrustedWriteStartCommand) error {
	call := command.Call
	if call.Tool == nil || !containsWorkflowBinding(command.AllowedWorkflows, policy.WorkflowKey, command.Identity.DefinitionVersion) {
		return denied(ErrorCodeWorkflowBindingDenied, errors.New("trusted write contract does not allow workflow definition"))
	}
	if call.Capability == "" || !containsCapability(policy.Permissions, call.Capability) {
		return denied(ErrorCodePermissionDenied, errors.New("trusted write workflow lacks exact capability"))
	}
	return nil
}

func sameTrustedWriteStartBinding(existing, requested domain.ToolCall) bool {
	return existing.WorkspaceID == requested.WorkspaceID && existing.WorkflowRunID == requested.WorkflowRunID &&
		existing.NodeRunID == requested.NodeRunID && existing.CallNo == requested.CallNo &&
		existing.RequestedToolName == requested.RequestedToolName && sameToolRef(existing.Tool, requested.Tool) &&
		existing.DefinitionHash == requested.DefinitionHash && sameSchemaRef(existing.InputSchema, requested.InputSchema) &&
		sameSchemaRef(existing.OutputSchema, requested.OutputSchema) && existing.Capability == requested.Capability &&
		existing.SideEffectLevel == requested.SideEffectLevel && existing.InvocationPolicy == requested.InvocationPolicy &&
		existing.IdempotencyKey == requested.IdempotencyKey && existing.RequestHash == requested.RequestHash &&
		existing.RequestBytes == requested.RequestBytes && jsonEqual(existing.RequestSummary, requested.RequestSummary) &&
		existing.SideEffectType == requested.SideEffectType && existing.SideEffectID == requested.SideEffectID &&
		(existing.Status == domain.CallStarted || existing.Status == domain.CallSucceeded)
}

func (repository *Repository) recoverTrustedWriteStartAfterCommitError(ctx context.Context, call domain.ToolCall, commitErr error) (application.StartCallResult, error) {
	tx, err := repository.begin(ctx)
	if err == nil {
		defer func() { _ = tx.Rollback(ctx) }()
		if existing, loadErr := loadSideEffectCallByKey(ctx, tx, call.WorkspaceID, call.IdempotencyKey); loadErr == nil && sameTrustedWriteStartBinding(existing, call) {
			_ = tx.Commit(ctx)
			return application.StartCallResult{Call: existing, Disposition: application.StartCallReplayed}, nil
		}
	}
	return application.StartCallResult{}, classify(commitErr)
}

var _ application.TrustedWriteCallRepository = (*Repository)(nil)
