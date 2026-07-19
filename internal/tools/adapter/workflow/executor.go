package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

// ExecutionService 是持久 Tool Node 唯一允许调用的共享安全流水线。
type ExecutionService interface {
	// Execute 使用服务端持久身份执行并记录一次 Tool Call。
	Execute(context.Context, toolsapplication.ExecuteToolCommand) (toolsapplication.ToolExecutionResult, error)
}

// Executor 将 River Claim 的持久身份绑定到一个不含模型自由文本的安全 Tool Invocation。
type Executor struct {
	service ExecutionService
}

// NewExecutor 构造持久 Tool Workflow Node Executor。
func NewExecutor(service ExecutionService) (*Executor, error) {
	if isNilInterface(service) {
		return nil, workflowError(foundation.ErrorDependencyUnavailable, ErrorCodeExecutorUnavailable, false, errors.New("tool execution service is unavailable"))
	}
	return &Executor{service: service}, nil
}

// Execute 严格解码持久 Tool Invocation，使用 River Claim 身份调用共享 ExecutionService，并只持久化脱敏结果摘要。
func (executor *Executor) Execute(ctx context.Context, execution workflowapplication.ExecutionContext) (workflowapplication.ExecutionResult, error) {
	if executor == nil || isNilInterface(executor.service) {
		return workflowapplication.ExecutionResult{}, workflowError(foundation.ErrorDependencyUnavailable, ErrorCodeExecutorUnavailable, false, errors.New("tool workflow executor is unavailable"))
	}
	identity, err := trustedIdentity(execution)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	invocation, err := DecodePersistedToolInvocationV1(execution.Input)
	if err != nil {
		return workflowapplication.ExecutionResult{}, workflowError(foundation.ErrorInvalidInput, ErrorCodeInputInvalid, false, err)
	}
	request := toolsdomain.ToolRequestV1{
		SchemaVersion: toolsdomain.ToolRequestSchemaVersionV1,
		ToolName:      invocation.ToolName,
		Arguments:     append(json.RawMessage(nil), invocation.Arguments...),
		Reason:        persistedReason,
	}
	result, err := executor.service.Execute(ctx, toolsapplication.ExecuteToolCommand{
		Identity: identity, Invocation: toolsdomain.InvocationSourceModelRequest, Request: request, CallNo: 1,
	})
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if result.Call.Status != toolsdomain.CallSucceeded || result.Call.Tool == nil || !result.UntrustedData || len(result.Output) == 0 {
		return workflowapplication.ExecutionResult{}, workflowError(foundation.ErrorConsistencyViolation, ErrorCodeOutputInvalid, false, errors.New("tool execution returned an incomplete success"))
	}
	if err := toolsdomain.ValidateToolCall(result.Call); err != nil {
		return workflowapplication.ExecutionResult{}, workflowError(foundation.ErrorConsistencyViolation, ErrorCodeOutputInvalid, false, err)
	}
	outputDigest := sha256.Sum256(result.Output)
	if result.Call.ResponseBytes != int64(len(result.Output)) || result.Call.ResponseHash != hex.EncodeToString(outputDigest[:]) {
		return workflowapplication.ExecutionResult{}, workflowError(foundation.ErrorConsistencyViolation, ErrorCodeOutputInvalid, false, errors.New("tool result differs from the persisted call receipt"))
	}
	responseSummary, err := decodeToolResponseSummary(result.Call.ResponseSummary)
	if err != nil || result.Call.OutputSchema == nil || responseSummary.SchemaID != result.Call.OutputSchema.ID || responseSummary.SchemaVersion != result.Call.OutputSchema.Version {
		return workflowapplication.ExecutionResult{}, workflowError(foundation.ErrorConsistencyViolation, ErrorCodeOutputInvalid, false, errors.New("tool response summary differs from the persisted call contract"))
	}
	persisted := ToolResultV1{
		SchemaVersion: OutputSchemaVersion, ToolCallID: result.Call.ID, Tool: *result.Call.Tool, Status: result.Call.Status,
		ResultRef: result.Call.ResultRef, ResponseHash: result.Call.ResponseHash, ResponseBytes: result.Call.ResponseBytes,
		ResponseSummary: responseSummary, UntrustedData: true,
	}
	output, err := json.Marshal(persisted)
	if err != nil {
		return workflowapplication.ExecutionResult{}, workflowError(foundation.ErrorNonRetryableFailure, ErrorCodeOutputInvalid, false, err)
	}
	if _, err := DecodeToolResultV1(output); err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	return workflowapplication.ExecutionResult{Output: output}, nil
}

func trustedIdentity(execution workflowapplication.ExecutionContext) (toolsdomain.TrustedExecutionIdentity, error) {
	identity := toolsdomain.TrustedExecutionIdentity{
		WorkspaceID: execution.WorkspaceID, DefinitionID: execution.DefinitionID, DefinitionVersion: execution.DefinitionVersion,
		DefinitionHash: execution.DefinitionHash, WorkflowRunID: execution.RunID, NodeKey: execution.NodeKey,
		NodeRunID: execution.NodeRunID, NodeAttemptID: execution.NodeAttemptID, LeaseOwner: execution.LeaseOwner,
		LeaseFence: int64(execution.AttemptNo),
	}
	if execution.NodeKind != NodeKind || execution.InputSchemaVersion != InputSchemaVersion || execution.AttemptNo < 1 ||
		execution.DispatchNo < 1 || execution.RetryNo < 0 || execution.NodeVersion < 1 || identity.Validate() != nil {
		return toolsdomain.TrustedExecutionIdentity{}, workflowError(foundation.ErrorConsistencyViolation, ErrorCodeContextInvalid, false, errors.New("persisted tool workflow identity is invalid"))
	}
	return identity, nil
}

var _ workflowapplication.Executor = (*Executor)(nil)
