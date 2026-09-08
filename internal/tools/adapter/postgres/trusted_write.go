package postgres

import (
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

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
