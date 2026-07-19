package application

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

const (
	errorCodeTrustedWriteAuditUnavailable = "TOOL_TRUSTED_WRITE_AUDIT_UNAVAILABLE"
	errorCodeTrustedWriteAuditInvalid     = "TOOL_TRUSTED_WRITE_AUDIT_INVALID"
	errorCodeTrustedWriteAuditMissing     = "TOOL_TRUSTED_WRITE_AUDIT_MISSING"
	errorCodeTrustedWriteAuditConflict    = "TOOL_TRUSTED_WRITE_AUDIT_CONFLICT"
)

// TrustedWriteAuditCommand 描述 Safe Writeback 在副作用前登记的固定逻辑 Tool Call。
type TrustedWriteAuditCommand struct {
	Identity         domain.TrustedExecutionIdentity
	Tool             domain.ToolRef
	CallNo           int
	Arguments        json.RawMessage
	IdempotencyKey   string
	WritebackReceipt foundation.ID
}

// TrustedWriteAuditReceipt 是 writeback_execution 权威检查点生成的 canonical Tool 输出。
type TrustedWriteAuditReceipt struct {
	Command TrustedWriteAuditCommand
	Output  json.RawMessage
}

// TrustedWriteAuditService 在不执行文件或 Git 的前提下维护 trusted write Tool Call 生命周期。
type TrustedWriteAuditService struct {
	registry *Registry
	calls    TrustedWriteCallRepository
	ids      foundation.IDGenerator
	clock    foundation.Clock
}

// NewTrustedWriteAuditService 创建 Safe Writeback 专用审计服务；所有依赖必须真实可用。
func NewTrustedWriteAuditService(registry *Registry, calls TrustedWriteCallRepository, ids foundation.IDGenerator, clock foundation.Clock) (*TrustedWriteAuditService, error) {
	if registry == nil || isNilInterface(calls) || isNilInterface(ids) || isNilInterface(clock) {
		return nil, executionError(foundation.ErrorDependencyUnavailable, errorCodeTrustedWriteAuditUnavailable, false, errors.New("trusted write audit dependency is missing"))
	}
	return &TrustedWriteAuditService{registry: registry, calls: calls, ids: ids, clock: clock}, nil
}

// EnsureStarted 在副作用前创建 STARTED，或重放同一 writeback receipt 的历史 STARTED/SUCCEEDED。
func (service *TrustedWriteAuditService) EnsureStarted(ctx context.Context, command TrustedWriteAuditCommand) (domain.ToolCall, error) {
	if service == nil || service.registry == nil || isNilInterface(service.calls) || isNilInterface(service.ids) || isNilInterface(service.clock) || ctx == nil {
		return domain.ToolCall{}, executionError(foundation.ErrorDependencyUnavailable, errorCodeTrustedWriteAuditUnavailable, false, errors.New("trusted write audit service is unavailable"))
	}
	contract, request, arguments, requestHash, summary, err := service.prepare(command)
	if err != nil {
		return domain.ToolCall{}, err
	}
	callID, err := service.ids.New()
	if err != nil {
		return domain.ToolCall{}, err
	}
	toolRef := contract.Definition.Ref
	inputSchema := contract.Definition.InputSchema
	outputSchema := contract.Definition.OutputSchema
	requestDocument, err := canonicalRequestDocument(request, contract.Definition.Ref, contract.Definition.InputSchema, arguments)
	if err != nil {
		return domain.ToolCall{}, err
	}
	call := domain.ToolCall{
		ID: callID, WorkspaceID: command.Identity.WorkspaceID, WorkflowRunID: command.Identity.WorkflowRunID,
		NodeRunID: command.Identity.NodeRunID, NodeAttemptID: command.Identity.NodeAttemptID, CallNo: command.CallNo,
		RequestedToolName: request.ToolName, Tool: &toolRef, DefinitionHash: contract.Definition.DefinitionHash,
		InputSchema: &inputSchema, OutputSchema: &outputSchema, Capability: contract.Definition.RequiredCapability,
		SideEffectLevel: contract.Definition.SideEffectLevel, InvocationPolicy: contract.Definition.InvocationPolicy,
		IdempotencyKey: command.IdempotencyKey, RequestHash: requestHash, RequestBytes: int64(len(requestDocument)), RequestSummary: summary,
		SideEffectType: "writeback_execution", SideEffectID: string(command.WritebackReceipt),
		Status: domain.CallStarted, Version: 1, StartedAt: service.clock.Now().UTC(),
	}
	if err := domain.ValidateToolCall(call); err != nil {
		return domain.ToolCall{}, err
	}
	started, err := service.calls.StartTrustedWriteCall(ctx, TrustedWriteStartCommand{
		Identity: command.Identity, Call: call, AllowedWorkflows: contract.Definition.AllowedWorkflows,
	})
	if err != nil {
		return domain.ToolCall{}, err
	}
	if started.Disposition != StartCallCreated && started.Disposition != StartCallReplayed {
		return domain.ToolCall{}, executionError(foundation.ErrorConsistencyViolation, errorCodeTrustedWriteAuditConflict, false, errors.New("trusted write repository returned invalid disposition"))
	}
	if err := validateTrustedWriteReplay(started.Call, command, contract, requestHash); err != nil {
		return domain.ToolCall{}, err
	}
	return started.Call, nil
}

// RequireStarted 只读取并校验既有 STARTED/SUCCEEDED；用于发现已有文件或 Commit receipt 后禁止补造审计。
func (service *TrustedWriteAuditService) RequireStarted(ctx context.Context, command TrustedWriteAuditCommand) (domain.ToolCall, error) {
	if service == nil || isNilInterface(service.calls) || ctx == nil {
		return domain.ToolCall{}, executionError(foundation.ErrorDependencyUnavailable, errorCodeTrustedWriteAuditUnavailable, false, errors.New("trusted write audit service is unavailable"))
	}
	contract, _, _, requestHash, _, err := service.prepare(command)
	if err != nil {
		return domain.ToolCall{}, err
	}
	call, err := service.calls.LoadTrustedWriteCall(ctx, TrustedWriteLoadCommand{
		Identity: command.Identity, Tool: command.Tool, Capability: contract.Definition.RequiredCapability,
		AllowedWorkflows: append([]domain.WorkflowBinding(nil), contract.Definition.AllowedWorkflows...),
		IdempotencyKey:   command.IdempotencyKey,
	})
	if err != nil {
		var classified *foundation.Error
		if errors.As(err, &classified) && classified.Kind == foundation.ErrorNotFound {
			return domain.ToolCall{}, executionError(foundation.ErrorManualRecoveryRequired, errorCodeTrustedWriteAuditMissing, false, err)
		}
		return domain.ToolCall{}, err
	}
	if err := validateTrustedWriteReplay(call, command, contract, requestHash); err != nil {
		return domain.ToolCall{}, err
	}
	return call, nil
}

// RecordSucceeded 根据 canonical writeback receipt 将既有 STARTED 归约成功；重复调用只重放同一终态。
func (service *TrustedWriteAuditService) RecordSucceeded(ctx context.Context, receipt TrustedWriteAuditReceipt) (domain.ToolCall, error) {
	call, err := service.RequireStarted(ctx, receipt.Command)
	if err != nil {
		return domain.ToolCall{}, err
	}
	contract, err := service.registry.ResolveContract(receipt.Command.Tool)
	if err != nil {
		return domain.ToolCall{}, err
	}
	output, summary, err := validateExecutorResult(contract, ExecutorResult{
		Output: receipt.Output, ResultRef: "writeback-execution:" + string(receipt.Command.WritebackReceipt),
		SideEffectType: "writeback_execution", SideEffectID: string(receipt.Command.WritebackReceipt),
	})
	if err != nil {
		return domain.ToolCall{}, err
	}
	if call.Status == domain.CallSucceeded {
		if call.ResponseHash != hashBytes(output) || call.ResponseBytes != int64(len(output)) || !jsonDocumentsEqual(call.ResponseSummary, summary) ||
			call.ResultRef != "writeback-execution:"+string(receipt.Command.WritebackReceipt) {
			return domain.ToolCall{}, executionError(foundation.ErrorConsistencyViolation, errorCodeTrustedWriteAuditConflict, false, errors.New("trusted write success receipt differs"))
		}
		return call, nil
	}
	if call.Status != domain.CallStarted {
		return domain.ToolCall{}, executionError(foundation.ErrorManualRecoveryRequired, errorCodeTrustedWriteAuditConflict, false, errors.New("trusted write call is already terminal without success"))
	}
	completedAt := service.clock.Now().UTC()
	succeeded := call
	succeeded.Status = domain.CallSucceeded
	succeeded.ResponseHash = hashBytes(output)
	succeeded.ResponseBytes = int64(len(output))
	succeeded.ResponseSummary = summary
	succeeded.ResultRef = "writeback-execution:" + string(receipt.Command.WritebackReceipt)
	succeeded.CompletedAt = &completedAt
	succeeded.DurationMillis = durationMillis(succeeded.StartedAt, completedAt)
	succeeded.Version++
	if err := domain.ValidateToolCall(succeeded); err != nil {
		return domain.ToolCall{}, err
	}
	mutation, err := service.calls.FinalizeCall(ctx, FinalizeCallCommand{ExpectedVersion: call.Version, Call: succeeded})
	if err != nil {
		return domain.ToolCall{}, err
	}
	return mutation.Call, nil
}

func (service *TrustedWriteAuditService) prepare(command TrustedWriteAuditCommand) (Contract, domain.ToolRequestV1, json.RawMessage, string, json.RawMessage, error) {
	if err := command.Identity.Validate(); err != nil || command.Tool.Validate() != nil || command.CallNo < 1 || command.CallNo > MaxToolCallNumber ||
		!validOptionalReference(command.IdempotencyKey, domain.MaxToolIdempotencyKeyBytes) || command.IdempotencyKey == "" {
		return Contract{}, domain.ToolRequestV1{}, nil, "", nil, executionError(foundation.ErrorInvalidInput, errorCodeTrustedWriteAuditInvalid, false, errOr(err, "trusted write audit command is invalid"))
	}
	parsedReceipt, err := foundation.ParseID(string(command.WritebackReceipt))
	if err != nil || parsedReceipt != command.WritebackReceipt {
		return Contract{}, domain.ToolRequestV1{}, nil, "", nil, executionError(foundation.ErrorInvalidInput, errorCodeTrustedWriteAuditInvalid, false, errOr(err, "writeback receipt is invalid"))
	}
	contract, err := service.registry.ResolveContract(command.Tool)
	if err != nil {
		return Contract{}, domain.ToolRequestV1{}, nil, "", nil, err
	}
	if (command.Tool.Name != "ApplyApprovedPatch" && command.Tool.Name != "CreateGitCommit") ||
		contract.Definition.InvocationPolicy != domain.InvocationTrustedWorkflowOnly || contract.Definition.SideEffectLevel != domain.SideEffectDomainWrite ||
		contract.Definition.IdempotencyMode != domain.IdempotencyRequired {
		return Contract{}, domain.ToolRequestV1{}, nil, "", nil, executionError(foundation.ErrorPermissionDenied, errorCodeTrustedWriteAuditInvalid, false, errors.New("tool is not a safe writeback audit contract"))
	}
	request := domain.ToolRequestV1{SchemaVersion: domain.ToolRequestSchemaVersionV1, ToolName: command.Tool.Name, Arguments: append(json.RawMessage(nil), command.Arguments...), Reason: "safe writeback audit"}
	if err := request.Validate(); err != nil {
		return Contract{}, domain.ToolRequestV1{}, nil, "", nil, err
	}
	arguments, err := contract.DecodeInput(request.Arguments)
	if err != nil || len(arguments) == 0 || int64(len(arguments)) > contract.Definition.MaxInputBytes {
		return Contract{}, domain.ToolRequestV1{}, nil, "", nil, executionError(foundation.ErrorInvalidInput, errorCodeTrustedWriteAuditInvalid, false, errOr(err, "trusted write input is invalid"))
	}
	requestDocument, err := canonicalRequestDocument(request, contract.Definition.Ref, contract.Definition.InputSchema, arguments)
	if err != nil {
		return Contract{}, domain.ToolRequestV1{}, nil, "", nil, err
	}
	summary, err := requestSummary(ExecuteToolCommand{Identity: command.Identity, Invocation: domain.InvocationSourceTrustedWorkflow, Request: request, CallNo: command.CallNo, IdempotencyKey: command.IdempotencyKey}, contract.Definition, arguments)
	if err != nil {
		return Contract{}, domain.ToolRequestV1{}, nil, "", nil, err
	}
	return contract, request, arguments, hashBytes(requestDocument), summary, nil
}

func validateTrustedWriteReplay(call domain.ToolCall, command TrustedWriteAuditCommand, contract Contract, requestHash string) error {
	if call.WorkspaceID != command.Identity.WorkspaceID || call.WorkflowRunID != command.Identity.WorkflowRunID || call.NodeRunID != command.Identity.NodeRunID ||
		call.CallNo != command.CallNo || call.Tool == nil || *call.Tool != command.Tool || call.RequestedToolName != command.Tool.Name ||
		call.DefinitionHash != contract.Definition.DefinitionHash || call.InputSchema == nil || *call.InputSchema != contract.Definition.InputSchema ||
		call.OutputSchema == nil || *call.OutputSchema != contract.Definition.OutputSchema || call.Capability != contract.Definition.RequiredCapability ||
		call.SideEffectLevel != domain.SideEffectDomainWrite || call.InvocationPolicy != domain.InvocationTrustedWorkflowOnly ||
		call.IdempotencyKey != command.IdempotencyKey || call.RequestHash != requestHash || call.SideEffectType != "writeback_execution" ||
		call.SideEffectID != string(command.WritebackReceipt) || (call.Status != domain.CallStarted && call.Status != domain.CallSucceeded) {
		return executionError(foundation.ErrorManualRecoveryRequired, errorCodeTrustedWriteAuditConflict, false, errors.New("trusted write replay binding differs"))
	}
	return nil
}
