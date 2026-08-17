package application

import (
	"context"
	"errors"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

const (
	errorCodeWorkspaceAnalysisToolCommandInvalid       = "TOOL_WORKSPACE_ANALYSIS_COMMAND_INVALID"
	errorCodeWorkspaceAnalysisToolContractDenied       = "TOOL_WORKSPACE_ANALYSIS_CONTRACT_DENIED"
	errorCodeWorkspaceAnalysisAuthorizationInvalid     = "TOOL_WORKSPACE_ANALYSIS_AUTHORIZATION_INVALID"
	errorCodeWorkspaceAnalysisAuthorizationUnavailable = "TOOL_WORKSPACE_ANALYSIS_AUTHORIZATION_UNAVAILABLE"
)

// ExecuteWorkspaceAnalysisToolCommand 为一个静态 Workspace Analysis Operation 请求唯一 exact Tool。
// Tool 名称和参数由节点 Executor 在服务端构造，不得来自模型选择。
type ExecuteWorkspaceAnalysisToolCommand struct {
	OperationKey agentdomain.WorkspaceAnalysisOperationKey
	Tool         ExecuteToolCommand
}

// ExecuteWorkspaceAnalysisTool 先原子授权逻辑 Operation、预算与 STARTED Call，再执行唯一获权的 Tool。
// 重复投递只恢复既有 Call/receipt，永远不会再次进入 Executor。
func (service *ExecutionService) ExecuteWorkspaceAnalysisTool(
	ctx context.Context,
	command ExecuteWorkspaceAnalysisToolCommand,
) (ToolExecutionResult, error) {
	if err := service.validateExecutionCommandPrerequisites(ctx, command.Tool); err != nil {
		return ToolExecutionResult{}, err
	}
	if err := command.OperationKey.Validate(); err != nil ||
		command.OperationKey.NodeKey != agentdomain.WorkspaceAnalysisOperationNodeKey(command.Tool.Identity.NodeKey) {
		if err == nil {
			err = errors.New("workspace analysis operation key is invalid")
		}
		return ToolExecutionResult{}, executionError(
			foundation.ErrorInvalidInput, errorCodeWorkspaceAnalysisToolCommandInvalid, false, err,
		)
	}

	prepared, err := service.prepareToolExecution(ctx, command.Tool, false)
	if err != nil {
		if workspaceAnalysisDeterministicRefusal(err) {
			return ToolExecutionResult{}, service.refuseWorkspaceAnalysisTool(ctx, command, err)
		}
		return ToolExecutionResult{}, err
	}
	if err := validateWorkspaceAnalysisToolSlot(command, prepared.contract.Definition); err != nil {
		if workspaceAnalysisDeterministicRefusal(err) {
			return ToolExecutionResult{}, service.refuseWorkspaceAnalysisTool(ctx, command, err)
		}
		return ToolExecutionResult{}, err
	}
	if _, err := service.resultReceiptFailureRepository(); err != nil {
		return ToolExecutionResult{}, err
	}
	repository, err := service.workspaceAnalysisToolOperationRepository()
	if err != nil {
		return ToolExecutionResult{}, err
	}

	started, err := service.startedCall(command.Tool, prepared.contract, prepared.arguments)
	if err != nil {
		return ToolExecutionResult{}, err
	}
	operationID, err := service.ids.New()
	if err != nil {
		return ToolExecutionResult{}, executionError(
			foundation.ErrorDependencyUnavailable, errorCodeWorkspaceAnalysisAuthorizationUnavailable, true, err,
		)
	}
	reservationID, err := service.ids.New()
	if err != nil {
		return ToolExecutionResult{}, executionError(
			foundation.ErrorDependencyUnavailable, errorCodeWorkspaceAnalysisAuthorizationUnavailable, true, err,
		)
	}
	receiptFailureID, err := service.ids.New()
	if err != nil {
		return ToolExecutionResult{}, executionError(
			foundation.ErrorDependencyUnavailable, errorCodeWorkspaceAnalysisAuthorizationUnavailable, true, err,
		)
	}
	authorized, err := repository.AuthorizeWorkspaceAnalysisToolCall(ctx, AuthorizeWorkspaceAnalysisToolCallCommand{
		Identity: command.Tool.Identity, OperationKey: command.OperationKey,
		OperationID: operationID, ReservationID: reservationID,
		Call: started, Definition: prepared.contract.Definition,
	})
	if err != nil {
		return ToolExecutionResult{}, err
	}
	if err := validateWorkspaceAnalysisToolAuthorization(
		started,
		command.OperationKey,
		prepared.contract.Definition,
		operationID,
		reservationID,
		authorized,
	); err != nil {
		return ToolExecutionResult{}, err
	}
	if authorized.Disposition == WorkspaceAnalysisToolAuthorizationReplayFailure && authorized.ReceiptFailure != nil {
		return ToolExecutionResult{}, workspaceAnalysisReceiptFailureTerminalErrorFor(
			authorized.OperationID,
			*authorized.ReceiptFailure,
			workspaceAnalysisReceiptFailureCause(*authorized.ReceiptFailure, prepared.contract.Definition),
		)
	}
	disposition := StartCallReplayed
	if authorized.Disposition == WorkspaceAnalysisToolAuthorizationCreated {
		disposition = StartCallCreated
	}
	return service.executeStarted(ctx, command.Tool, prepared, StartCallResult{
		Call: authorized.Call, Disposition: disposition,
	}, &workspaceAnalysisToolExecutionContext{
		OperationKey:     command.OperationKey,
		OperationID:      authorized.OperationID,
		ReceiptFailureID: receiptFailureID,
	})
}

func validateWorkspaceAnalysisToolSlot(command ExecuteWorkspaceAnalysisToolCommand, definition domain.Definition) error {
	contract, err := agentdomain.WorkspaceAnalysisOperationContractForKey(command.OperationKey)
	if err != nil {
		return err
	}
	expectedRef, expectedCallNo, found := workspaceAnalysisToolSlot(command.OperationKey)
	if !found || contract.CallKind != agentdomain.WorkspaceAnalysisOperationCallTool || definition.Ref != expectedRef ||
		command.Tool.CallNo != expectedCallNo || definition.RequiredCapability != capability.ReadLocal ||
		definition.SideEffectLevel != domain.SideEffectNone || definition.InvocationPolicy != domain.InvocationTrustedWorkflowOnly ||
		definition.ResultPersistencePolicy != domain.ResultPersistenceCanonical || definition.IdempotencyMode != domain.IdempotencyNone ||
		!workspaceAnalysisToolTimeoutRepresentable(definition.Timeout) ||
		len(definition.AllowedWorkflows) != 1 ||
		definition.AllowedWorkflows[0] != (domain.WorkflowBinding{Key: "workspace-analysis", Version: 1}) {
		return executionError(
			foundation.ErrorConsistencyViolation,
			errorCodeWorkspaceAnalysisToolContractDenied,
			false,
			errors.New("workspace analysis operation does not match its frozen exact tool slot"),
		)
	}
	return nil
}

func (service *ExecutionService) refuseWorkspaceAnalysisTool(
	ctx context.Context,
	command ExecuteWorkspaceAnalysisToolCommand,
	cause error,
) error {
	if !workspaceAnalysisDeterministicRefusal(cause) {
		return cause
	}
	repository, ok := service.calls.(WorkspaceAnalysisToolRefusalRepository)
	if !ok || isNilInterface(repository) {
		return executionError(
			foundation.ErrorDependencyUnavailable,
			errorCodeRefusalPersistenceFailed,
			false,
			errors.New("workspace analysis tool refusal persistence is unavailable"),
		)
	}
	refusalID, err := service.ids.New()
	if err != nil {
		return executionError(foundation.ErrorDependencyUnavailable, errorCodeRefusalPersistenceFailed, true, err)
	}
	code := stableErrorCode(cause, errorCodeWorkspaceAnalysisToolCommandInvalid)
	result, err := repository.RecordWorkspaceAnalysisToolRefusal(ctx, RecordWorkspaceAnalysisToolRefusalCommand{
		RefusalID: refusalID, Identity: command.Tool.Identity, OperationKey: command.OperationKey, ErrorCode: code,
	})
	if err != nil {
		return executionError(foundation.ErrorConsistencyViolation, errorCodeRefusalPersistenceFailed, false, err)
	}
	if result.RefusalID == "" || result.ErrorCode != code {
		return executionError(
			foundation.ErrorConsistencyViolation,
			errorCodeRefusalPersistenceFailed,
			false,
			errors.New("workspace analysis tool refusal persistence binding drifted"),
		)
	}
	return cause
}

// workspaceAnalysisDeterministicRefusal is intentionally narrower than normal
// Tool refusal handling. Every accepted code is fully known before Executor
// admission; lease/cancel/budget/deadline/executor/receipt outcomes are not.
func workspaceAnalysisDeterministicRefusal(cause error) bool {
	switch stableErrorCode(cause, "") {
	case errorCodeAllowedVersionAmbiguous,
		errorCodeInvocationDenied,
		errorCodeWorkflowBindingDenied,
		errorCodeToolNotAllowed,
		errorCodePermissionDenied,
		errorCodeInputTooLarge,
		errorCodeInputInvalid,
		errorCodeIdempotencyRequired,
		errorCodeIdempotencyUnexpected,
		errorCodeWorkspaceAnalysisToolContractDenied:
		return true
	default:
		return false
	}
}

func workspaceAnalysisToolSlot(key agentdomain.WorkspaceAnalysisOperationKey) (domain.ToolRef, int, bool) {
	switch key.Kind {
	case agentdomain.WorkspaceAnalysisOperationGitStatus:
		return domain.ToolRef{Name: "ReadGitStatus", Version: 2}, 1, key.Ordinal == 1
	case agentdomain.WorkspaceAnalysisOperationKnowledgeSearch:
		return domain.ToolRef{Name: "SearchKnowledge", Version: 2}, 1, key.Ordinal == 1
	case agentdomain.WorkspaceAnalysisOperationSourceRead:
		return domain.ToolRef{Name: "ReadSource", Version: 3}, key.Ordinal, key.Ordinal >= 1 && key.Ordinal <= 3
	case agentdomain.WorkspaceAnalysisOperationCitationValidation:
		return domain.ToolRef{Name: "ValidateCitation", Version: 3}, 1, key.Ordinal == 1
	default:
		return domain.ToolRef{}, 0, false
	}
}

func validateWorkspaceAnalysisToolAuthorization(
	requested domain.ToolCall,
	operationKey agentdomain.WorkspaceAnalysisOperationKey,
	definition domain.Definition,
	requestedOperationID foundation.ID,
	requestedReservationID foundation.ID,
	authorized WorkspaceAnalysisToolAuthorizationResult,
) error {
	if err := domain.ValidateToolCall(authorized.Call); err != nil || !validApplicationID(authorized.OperationID) ||
		!validApplicationID(authorized.ReservationID) || authorized.OperationID == authorized.ReservationID ||
		authorized.OperationID == authorized.Call.ID || authorized.ReservationID == authorized.Call.ID ||
		!sameWorkspaceAnalysisToolRequest(requested, authorized.Call) {
		if err == nil {
			err = errors.New("workspace analysis authorization returned a different persistent binding")
		}
		return executionError(
			foundation.ErrorConsistencyViolation, errorCodeWorkspaceAnalysisAuthorizationInvalid, false, err,
		)
	}

	valid := false
	hasReceiptFailure := authorized.ReceiptFailure != nil
	switch authorized.Disposition {
	case WorkspaceAnalysisToolAuthorizationCreated:
		valid = authorized.Call.Status == domain.CallStarted && authorized.Call.Version == 1 &&
			authorized.Call.ID == requested.ID && authorized.Call.NodeAttemptID == requested.NodeAttemptID &&
			authorized.OperationID == requestedOperationID && authorized.ReservationID == requestedReservationID && !hasReceiptFailure
	case WorkspaceAnalysisToolAuthorizationReconcile:
		valid = authorized.Call.Status == domain.CallStarted && !hasReceiptFailure
	case WorkspaceAnalysisToolAuthorizationReuseResult:
		valid = authorized.Call.Status == domain.CallSucceeded && !hasReceiptFailure
	case WorkspaceAnalysisToolAuthorizationReplayFailure:
		valid = authorized.Call.Status == domain.CallFailed && !hasReceiptFailure
		if authorized.Call.Status == domain.CallSucceeded && hasReceiptFailure {
			failure := *authorized.ReceiptFailure
			valid = failure.OperationID == authorized.OperationID && failure.AnalysisRunID == operationKey.AnalysisRunID &&
				domain.ValidateResultReceiptFailure(failure, authorized.Call, definition) == nil
		}
	case WorkspaceAnalysisToolAuthorizationTerminateUnknown:
		valid = authorized.Call.Status == domain.CallUnknown && !hasReceiptFailure
	}
	if !valid {
		return executionError(
			foundation.ErrorConsistencyViolation,
			errorCodeWorkspaceAnalysisAuthorizationInvalid,
			false,
			errors.New("workspace analysis authorization disposition does not match its call state"),
		)
	}
	return nil
}

func workspaceAnalysisReceiptFailureCause(
	failure domain.ResultReceiptFailure,
	definition domain.Definition,
) error {
	errorCode := errorCodeOutputInvalid
	if failure.FailureCode == domain.ResultReceiptFailureContractInvalid && failure.ObservedOutputBytes != nil &&
		*failure.ObservedOutputBytes > definition.MaxOutputBytes {
		errorCode = errorCodeOutputTooLarge
	}
	return executionError(
		foundation.ErrorNonRetryableFailure,
		errorCode,
		false,
		errors.New("workspace analysis canonical result receipt validation failed"),
	)
}

func sameWorkspaceAnalysisToolRequest(expected, actual domain.ToolCall) bool {
	return expected.WorkspaceID == actual.WorkspaceID && expected.WorkflowRunID == actual.WorkflowRunID &&
		expected.NodeRunID == actual.NodeRunID && expected.CallNo == actual.CallNo &&
		expected.RequestedToolName == actual.RequestedToolName && equalToolRef(expected.Tool, actual.Tool) &&
		expected.DefinitionHash == actual.DefinitionHash && equalSchemaRef(expected.InputSchema, actual.InputSchema) &&
		equalSchemaRef(expected.OutputSchema, actual.OutputSchema) && expected.Capability == actual.Capability &&
		expected.SideEffectLevel == actual.SideEffectLevel && expected.InvocationPolicy == actual.InvocationPolicy &&
		expected.IdempotencyKey == actual.IdempotencyKey && expected.RequestHash == actual.RequestHash &&
		expected.RequestBytes == actual.RequestBytes && jsonDocumentsEqual(expected.RequestSummary, actual.RequestSummary)
}

func validApplicationID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

// 保持编译器对 frozen timeout 精度的显式约束；Workspace Analysis v1 仅接受毫秒可表达的 Tool timeout。
func workspaceAnalysisToolTimeoutRepresentable(value time.Duration) bool {
	return value > 0 && value%time.Millisecond == 0
}
