package postgres

import (
	"context"
	"errors"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	"gorm.io/gorm"
)

// StartTrustedWriteCall registers the durable writeback execution seam. A
// matching historic call is replayed but never reassigned to the new attempt.
func (repository *GORMRepository) StartTrustedWriteCall(ctx context.Context, command application.TrustedWriteStartCommand) (application.StartCallResult, error) {
	call, err := validateTrustedWriteStartCommand(command)
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
		if validationErr := validateTrustedWritePolicy(policy, command); validationErr != nil {
			return validationErr
		}
		created, queryErr := gormScanToolCallQuery(database, `INSERT INTO workflow.tool_call(
			id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,call_no,
			requested_tool_name,tool_version,definition_hash,input_schema_id,input_schema_version,
			output_schema_id,output_schema_version,capability,side_effect_level,invocation_policy,
			idempotency_key,request_hash,request_bytes,request_summary,side_effect_type,side_effect_id,status,retryable,version,started_at
		) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'STARTED',false,1,clock_timestamp())
		ON CONFLICT DO NOTHING RETURNING `+gormToolCallColumns,
			string(call.ID), string(call.WorkspaceID), string(call.WorkflowRunID), string(call.NodeRunID), string(call.NodeAttemptID), call.CallNo,
			call.RequestedToolName, call.Tool.Version, call.DefinitionHash, call.InputSchema.ID, call.InputSchema.Version,
			call.OutputSchema.ID, call.OutputSchema.Version, string(call.Capability), string(call.SideEffectLevel), string(call.InvocationPolicy),
			call.IdempotencyKey, call.RequestHash, call.RequestBytes, gormToolsOptionalJSONB(call.RequestSummary), call.SideEffectType, call.SideEffectID,
		)
		if queryErr == nil {
			result, completed = application.StartCallResult{Call: created, Disposition: application.StartCallCreated}, true
			return nil
		}
		if !gormToolsNoRows(queryErr) {
			return classifyGORMTools(callbackCtx, queryErr)
		}
		existing, loadErr := gormLoadSideEffectCallByKey(callbackCtx, database, call.WorkspaceID, call.IdempotencyKey)
		if gormToolsNoRows(loadErr) {
			return idempotencyConflict(errors.New("trusted write call conflicts with an active call number"))
		}
		if loadErr != nil {
			return classifyGORMTools(callbackCtx, loadErr)
		}
		if !sameTrustedWriteStartBinding(existing, call) {
			return idempotencyConflict(errors.New("trusted write idempotency binding differs"))
		}
		result, completed = application.StartCallResult{Call: existing, Disposition: application.StartCallReplayed}, true
		return nil
	})
	if err == nil {
		return result, nil
	}
	if completed && gormToolsCommitFailure(err) {
		return repository.gormRecoverTrustedWriteStartAfterCommitError(ctx, call, err)
	}
	return application.StartCallResult{}, classifyGORMTools(ctx, err)
}

// LoadTrustedWriteCall validates the current Workflow lease but reads the
// stable writeback record by its side-effect idempotency key.
func (repository *GORMRepository) LoadTrustedWriteCall(ctx context.Context, command application.TrustedWriteLoadCommand) (domain.ToolCall, error) {
	if err := command.Identity.Validate(); err != nil || command.Tool.Validate() != nil || command.Capability == "" || command.IdempotencyKey == "" || command.IdempotencyKey != strings.TrimSpace(command.IdempotencyKey) || len(command.IdempotencyKey) > domain.MaxToolIdempotencyKeyBytes {
		return domain.ToolCall{}, foundation.NewError(foundation.ErrorInvalidInput, domain.ErrorCodeCallInvalid, false, errors.New("trusted write load command is invalid"))
	}
	if err := repository.ready(ctx); err != nil {
		return domain.ToolCall{}, err
	}
	var call domain.ToolCall
	err := repository.readWithin(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, database *gorm.DB, scope foundation.TransactionScope) error {
		policy, policyErr := repository.gormResolveToolPolicyScoped(callbackCtx, scope, command.Identity)
		if policyErr != nil {
			return policyErr
		}
		if !containsWorkflowBinding(command.AllowedWorkflows, policy.WorkflowKey, command.Identity.DefinitionVersion) {
			return denied(ErrorCodeWorkflowBindingDenied, errors.New("trusted write contract does not allow current workflow definition"))
		}
		if !containsCapability(policy.Permissions, command.Capability) {
			return denied(ErrorCodePermissionDenied, errors.New("trusted write workflow lacks exact capability"))
		}
		loaded, loadErr := gormLoadSideEffectCallByKey(callbackCtx, database, command.Identity.WorkspaceID, command.IdempotencyKey)
		if gormToolsNoRows(loadErr) {
			return notFound(loadErr)
		}
		if loadErr != nil {
			return classifyGORMTools(callbackCtx, loadErr)
		}
		if loaded.WorkflowRunID != command.Identity.WorkflowRunID || loaded.NodeRunID != command.Identity.NodeRunID || loaded.Tool == nil || *loaded.Tool != command.Tool || loaded.Capability != command.Capability || loaded.SideEffectType != "writeback_execution" || loaded.InvocationPolicy != domain.InvocationTrustedWorkflowOnly || loaded.SideEffectLevel != domain.SideEffectDomainWrite {
			return consistency(errors.New("trusted write load resolved a non-writeback call"))
		}
		call = loaded
		return nil
	})
	if err != nil {
		return domain.ToolCall{}, classifyGORMTools(ctx, err)
	}
	return call, nil
}

func (repository *GORMRepository) gormRecoverTrustedWriteStartAfterCommitError(ctx context.Context, call domain.ToolCall, commitErr error) (application.StartCallResult, error) {
	var result application.StartCallResult
	err := repository.readWithin(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, database *gorm.DB, _ foundation.TransactionScope) error {
		existing, loadErr := gormLoadSideEffectCallByKey(callbackCtx, database, call.WorkspaceID, call.IdempotencyKey)
		if loadErr != nil || !sameTrustedWriteStartBinding(existing, call) {
			return errors.New("trusted write Tool Call closure was not proven")
		}
		result = application.StartCallResult{Call: existing, Disposition: application.StartCallReplayed}
		return nil
	})
	if err == nil {
		return result, nil
	}
	return application.StartCallResult{}, classifyGORMTools(ctx, commitErr)
}

var _ application.TrustedWriteCallRepository = (*GORMRepository)(nil)
