package application

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	foundationredaction "github.com/CodeZen-Lizhi/zhixu/internal/foundation/redaction"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
)

const (
	// MaxToolCallNumber 对齐 PostgreSQL smallint，并限制单 Attempt 的调用数量。
	MaxToolCallNumber = 32767
	// toolFinalizationTimeout 限制 caller 取消后同步保存 Tool 终态的恢复窗口。
	toolFinalizationTimeout = 5 * time.Second

	errorCodeExecutionServiceUnavailable  = "TOOL_EXECUTION_SERVICE_UNAVAILABLE"
	errorCodeExecutionCommandInvalid      = "TOOL_EXECUTION_COMMAND_INVALID"
	errorCodePolicyInvalid                = "TOOL_POLICY_INVALID"
	errorCodeAllowedVersionAmbiguous      = "TOOL_ALLOWED_VERSION_AMBIGUOUS"
	errorCodeInvocationDenied             = "TOOL_INVOCATION_DENIED"
	errorCodeWorkflowBindingDenied        = "TOOL_WORKFLOW_BINDING_DENIED"
	errorCodeToolNotAllowed               = "TOOL_NOT_ALLOWED"
	errorCodePermissionDenied             = "TOOL_PERMISSION_DENIED"
	errorCodeInputTooLarge                = "TOOL_INPUT_TOO_LARGE"
	errorCodeInputInvalid                 = "TOOL_INPUT_INVALID"
	errorCodeIdempotencyRequired          = "TOOL_IDEMPOTENCY_REQUIRED"
	errorCodeIdempotencyUnexpected        = "TOOL_IDEMPOTENCY_UNEXPECTED"
	errorCodeOutputTooLarge               = "TOOL_OUTPUT_TOO_LARGE"
	errorCodeOutputInvalid                = "TOOL_OUTPUT_INVALID"
	errorCodeExecutionFailed              = "TOOL_EXECUTION_FAILED"
	errorCodeExecutionCancelled           = "TOOL_CANCELED"
	errorCodeExecutionTimeout             = "TOOL_TIMEOUT"
	errorCodeOutcomeUnknown               = "TOOL_OUTCOME_UNKNOWN"
	errorCodeCallInProgress               = "TOOL_CALL_IN_PROGRESS"
	errorCodeResultReplayUnavailable      = "TOOL_RESULT_REPLAY_UNAVAILABLE"
	errorCodeResultPersistenceUnavailable = "TOOL_RESULT_PERSISTENCE_UNAVAILABLE"
	errorCodeRecoveryInvalid              = "TOOL_RECOVERY_INVALID"
	errorCodeRefusalPersistenceFailed     = "TOOL_REFUSAL_PERSISTENCE_FAILED"
	errorCodeFinalizationUnknown          = "TOOL_FINALIZATION_UNKNOWN"
)

// ExecuteToolCommand 组合模型请求与仅由服务端提供的执行身份和幂等信息。
type ExecuteToolCommand struct {
	Identity       domain.TrustedExecutionIdentity
	Invocation     domain.InvocationSource
	Request        domain.ToolRequestV1
	CallNo         int
	IdempotencyKey string
}

// ToolExecutionResult 返回已持久化调用事实和经过严格验证的不可信 Tool 输出。
type ToolExecutionResult struct {
	Call            domain.ToolCall
	ResultReceiptID foundation.ID
	OperationID     foundation.ID `json:"-"`
	Output          json.RawMessage
	UntrustedData   bool
	Replayed        bool
}

// ExecutionService 按固定安全顺序编排策略、Registry、持久化和 typed Executor。
type ExecutionService struct {
	registry *Registry
	policies WorkflowPolicyReader
	calls    ToolCallRepository
	ids      foundation.IDGenerator
	clock    foundation.Clock
}

// NewExecutionService 构造 Worker 内部 Tool 执行服务；任一安全依赖缺失都 fail closed。
func NewExecutionService(
	registry *Registry,
	policies WorkflowPolicyReader,
	calls ToolCallRepository,
	ids foundation.IDGenerator,
	clock foundation.Clock,
) (*ExecutionService, error) {
	if registry == nil || isNilInterface(policies) || isNilInterface(calls) || isNilInterface(ids) || isNilInterface(clock) {
		return nil, executionError(foundation.ErrorDependencyUnavailable, errorCodeExecutionServiceUnavailable, false, errors.New("tool execution dependency is missing"))
	}
	return &ExecutionService{registry: registry, policies: policies, calls: calls, ids: ids, clock: clock}, nil
}

// Execute 执行一次服务端持久 Workflow 允许的精确 Tool 调用。
func (service *ExecutionService) Execute(ctx context.Context, command ExecuteToolCommand) (ToolExecutionResult, error) {
	if err := service.validateExecuteToolCommand(ctx, command); err != nil {
		return ToolExecutionResult{}, err
	}
	prepared, err := service.prepareToolExecution(ctx, command, true)
	if err != nil {
		return ToolExecutionResult{}, err
	}

	started, err := service.startedCall(command, prepared.contract, prepared.arguments)
	if err != nil {
		return ToolExecutionResult{}, err
	}
	startResult, err := service.calls.StartCall(ctx, StartCallCommand{
		Identity: command.Identity, Call: started, AllowedWorkflows: prepared.contract.Definition.AllowedWorkflows,
	})
	if err != nil {
		return ToolExecutionResult{}, err
	}
	return service.executeStarted(ctx, command, prepared, startResult, nil)
}

type preparedToolExecution struct {
	contract  Contract
	executor  Executor
	arguments json.RawMessage
}

type workspaceAnalysisToolExecutionContext struct {
	OperationKey     agentdomain.WorkspaceAnalysisOperationKey
	OperationID      foundation.ID
	ReceiptFailureID foundation.ID
}

func (service *ExecutionService) validateExecuteToolCommand(ctx context.Context, command ExecuteToolCommand) error {
	if err := service.validateExecutionCommandPrerequisites(ctx, command); err != nil {
		return err
	}
	if err := command.Request.Validate(); err != nil {
		return err
	}
	return nil
}

// validateExecutionCommandPrerequisites validates the trusted execution
// envelope without interpreting model-provided Tool input. Workspace Analysis
// uses its own pre-executor refusal boundary for that input.
func (service *ExecutionService) validateExecutionCommandPrerequisites(ctx context.Context, command ExecuteToolCommand) error {
	if service == nil || service.registry == nil || isNilInterface(service.policies) || isNilInterface(service.calls) || isNilInterface(service.ids) || isNilInterface(service.clock) {
		return executionError(foundation.ErrorDependencyUnavailable, errorCodeExecutionServiceUnavailable, false, errors.New("tool execution service is not initialized"))
	}
	if ctx == nil {
		return executionError(foundation.ErrorInvalidInput, errorCodeExecutionCommandInvalid, false, errors.New("tool execution context is nil"))
	}
	if err := command.Identity.Validate(); err != nil {
		return err
	}
	if command.CallNo < 1 || command.CallNo > MaxToolCallNumber || !validInvocationSource(command.Invocation) || !validOptionalReference(command.IdempotencyKey, domain.MaxToolIdempotencyKeyBytes) {
		return executionError(foundation.ErrorInvalidInput, errorCodeExecutionCommandInvalid, false, errors.New("tool execution command is invalid"))
	}
	return nil
}

func (service *ExecutionService) prepareToolExecution(
	ctx context.Context,
	command ExecuteToolCommand,
	persistRefusal bool,
) (preparedToolExecution, error) {
	policy, err := service.policies.ResolveToolPolicy(ctx, command.Identity)
	if err != nil {
		// 持久策略无法解析时，当前 Attempt/lease 可能已失效或数据库不可用；
		// 此时不能伪造一个仍具备活动执行身份的 REFUSED 事实。
		return preparedToolExecution{}, classifiedOr(err, foundation.ErrorVersionConflict, "TOOL_CONTEXT_STALE", false)
	}
	refuse := func(contract *Contract, cause error) error {
		if !persistRefusal {
			return cause
		}
		return service.refuse(ctx, command, contract, cause)
	}
	if err := validateWorkflowToolPolicy(policy, command.Identity); err != nil {
		return preparedToolExecution{}, refuse(nil, err)
	}

	ref, err := resolveAllowedTool(policy.AllowedTools, command.Request.ToolName)
	if err != nil {
		return preparedToolExecution{}, refuse(nil, err)
	}
	contract, err := service.registry.ResolveContract(ref)
	if err != nil {
		return preparedToolExecution{}, refuse(nil, err)
	}
	if !contract.Definition.AllowsInvocation(command.Invocation) {
		return preparedToolExecution{}, refuse(&contract, executionError(foundation.ErrorPermissionDenied, errorCodeInvocationDenied, false, errors.New("tool invocation source is not allowed")))
	}
	if !workflowBindingAllowed(contract.Definition.AllowedWorkflows, policy.WorkflowKey, command.Identity.DefinitionVersion) {
		return preparedToolExecution{}, refuse(&contract, executionError(foundation.ErrorPermissionDenied, errorCodeWorkflowBindingDenied, false, errors.New("tool contract does not allow workflow definition")))
	}
	if !toolRefAllowed(policy.AllowedTools, contract.Definition.Ref) {
		return preparedToolExecution{}, refuse(&contract, executionError(foundation.ErrorPermissionDenied, errorCodeToolNotAllowed, false, errors.New("workflow node does not allow exact tool version")))
	}
	if contract.Definition.RequiredCapability != "" && !capabilityAllowed(policy.Permissions, contract.Definition.RequiredCapability) {
		return preparedToolExecution{}, refuse(&contract, executionError(foundation.ErrorPermissionDenied, errorCodePermissionDenied, false, errors.New("workflow node lacks exact tool capability")))
	}
	executor, err := service.registry.ResolveExecutor(contract.Definition.Ref)
	if err != nil {
		return preparedToolExecution{}, refuse(&contract, err)
	}
	if contract.Definition.ResultPersistencePolicy == domain.ResultPersistenceCanonical {
		if _, err := service.resultReceiptRepository(); err != nil {
			return preparedToolExecution{}, err
		}
	}

	if int64(len(command.Request.Arguments)) > contract.Definition.MaxInputBytes {
		return preparedToolExecution{}, refuse(&contract, executionError(foundation.ErrorInvalidInput, errorCodeInputTooLarge, false, errors.New("tool input exceeds definition byte limit")))
	}
	arguments, err := contract.DecodeInput(command.Request.Arguments)
	if err != nil || len(arguments) == 0 || int64(len(arguments)) > contract.Definition.MaxInputBytes {
		return preparedToolExecution{}, refuse(&contract, executionError(foundation.ErrorInvalidInput, errorCodeInputInvalid, false, errOr(err, "tool input decoder returned invalid output")))
	}
	if err := validateIdempotency(contract.Definition.IdempotencyMode, command.IdempotencyKey); err != nil {
		return preparedToolExecution{}, refuse(&contract, err)
	}
	return preparedToolExecution{contract: contract, executor: executor, arguments: arguments}, nil
}

func (service *ExecutionService) executeStarted(
	ctx context.Context,
	command ExecuteToolCommand,
	prepared preparedToolExecution,
	startResult StartCallResult,
	workspaceAnalysis *workspaceAnalysisToolExecutionContext,
) (ToolExecutionResult, error) {
	contract, executor, arguments := prepared.contract, prepared.executor, prepared.arguments
	workspaceAnalysisOperationID := foundation.ID("")
	if workspaceAnalysis != nil {
		workspaceAnalysisOperationID = workspaceAnalysis.OperationID
	}
	if startResult.Disposition == StartCallReplayed {
		result, err := service.replay(ctx, command, contract, executor, arguments, startResult.Call)
		if err != nil {
			return ToolExecutionResult{}, workspaceAnalysisToolTerminalErrorFor(workspaceAnalysisOperationID, startResult.Call, err)
		}
		result.OperationID = workspaceAnalysisOperationID
		return result, nil
	}
	if startResult.Disposition != StartCallCreated || startResult.Call.Status != domain.CallStarted {
		return ToolExecutionResult{}, executionError(foundation.ErrorConsistencyViolation, errorCodePolicyInvalid, false, errors.New("tool call repository returned invalid start disposition"))
	}

	executionContext, cancel := context.WithTimeout(ctx, contract.Definition.Timeout)
	defer cancel()
	executorResult, executeErr := executor.Execute(executionContext, ExecutorRequest{
		Identity: command.Identity, Tool: contract.Definition.Ref, Arguments: append(json.RawMessage(nil), arguments...),
		Reason: strings.TrimSpace(command.Request.Reason), IdempotencyKey: command.IdempotencyKey,
	})
	if executeErr == nil && executionContext.Err() != nil {
		executeErr = executionContext.Err()
	}
	persistenceContext, persistenceCancel := context.WithTimeout(context.WithoutCancel(ctx), toolFinalizationTimeout)
	defer persistenceCancel()
	if executeErr != nil {
		return ToolExecutionResult{}, service.finishStartedExecutionError(
			persistenceContext, command.Identity, startResult.Call, contract.Definition, executeErr, workspaceAnalysisOperationID,
		)
	}
	output, summary, err := validateExecutorResult(contract, executorResult)
	if err != nil {
		if workspaceAnalysis != nil && validReceiptFailureOutputObservation(executorResult.Output) {
			return ToolExecutionResult{}, service.finishWorkspaceAnalysisReceiptFailure(
				persistenceContext,
				command.Identity,
				*workspaceAnalysis,
				startResult.Call,
				contract.Definition,
				executorResult.Output,
				nil,
				nil,
				domain.ResultReceiptFailureContractInvalid,
				err,
			)
		}
		return ToolExecutionResult{}, service.finishStartedExecutionError(
			persistenceContext, command.Identity, startResult.Call, contract.Definition, err, workspaceAnalysisOperationID,
		)
	}
	if err := validateExecutorPrivateBinding(contract.Definition, executorResult); err != nil {
		if workspaceAnalysis != nil && validReceiptFailureBindingObservation(executorResult.PrivateBinding) {
			return ToolExecutionResult{}, service.finishWorkspaceAnalysisReceiptFailure(
				persistenceContext,
				command.Identity,
				*workspaceAnalysis,
				startResult.Call,
				contract.Definition,
				output,
				summary,
				executorResult.PrivateBinding,
				domain.ResultReceiptFailureBindingInvalid,
				err,
			)
		}
		return ToolExecutionResult{}, service.finishStartedExecutionError(
			persistenceContext, command.Identity, startResult.Call, contract.Definition, err, workspaceAnalysisOperationID,
		)
	}

	completedAt := service.now()
	succeeded := startResult.Call
	succeeded.Status = domain.CallSucceeded
	succeeded.ResponseHash = hashBytes(output)
	succeeded.ResponseBytes = int64(len(output))
	succeeded.ResponseSummary = summary
	succeeded.ResultRef = executorResult.ResultRef
	succeeded.SideEffectType = executorResult.SideEffectType
	succeeded.SideEffectID = executorResult.SideEffectID
	succeeded.CompletedAt = &completedAt
	succeeded.DurationMillis = durationMillis(succeeded.StartedAt, completedAt)
	succeeded.Version++
	if err := domain.ValidateToolCall(succeeded); err != nil {
		return ToolExecutionResult{}, service.finishStartedExecutionError(
			persistenceContext, command.Identity, startResult.Call, contract.Definition,
			executionError(foundation.ErrorConsistencyViolation, errorCodeOutputInvalid, false, err), workspaceAnalysisOperationID,
		)
	}
	if contract.Definition.ResultPersistencePolicy == domain.ResultPersistenceCanonical {
		receiptID, idErr := service.ids.New()
		if idErr != nil {
			return ToolExecutionResult{}, service.finishStartedExecutionError(
				persistenceContext, command.Identity, startResult.Call, contract.Definition, idErr, workspaceAnalysisOperationID,
			)
		}
		receipts, repositoryErr := service.resultReceiptRepository()
		if repositoryErr != nil {
			return ToolExecutionResult{}, repositoryErr
		}
		mutation, finalizeErr := receipts.FinalizeCallWithReceipt(persistenceContext, FinalizeCallWithReceiptCommand{
			ExpectedVersion: startResult.Call.Version,
			Identity:        command.Identity,
			Call:            succeeded,
			Definition:      contract.Definition,
			ReceiptID:       receiptID,
			Output:          append(json.RawMessage(nil), output...),
			PrivateBinding:  cloneExecutorPrivateBinding(executorResult.PrivateBinding),
		})
		if finalizeErr != nil {
			// 该事务拥有 Call、receipt、reservation 与 operation 的唯一关闭权；
			// 提交不明确时不能用独立 MarkUnknown 事务覆盖或返回尚未证明持久化的输出。
			return ToolExecutionResult{}, finalizeErr
		}
		if !sameCanonicalFinalizationCall(succeeded, mutation.Call) {
			return ToolExecutionResult{}, executionError(
				foundation.ErrorConsistencyViolation,
				errorCodeResultReplayUnavailable,
				false,
				errors.New("canonical result repository returned a different tool call"),
			)
		}
		if workspaceAnalysisOperationID != "" && mutation.OperationID != workspaceAnalysisOperationID {
			return ToolExecutionResult{}, workspaceAnalysisFinalizationUnknown(
				errors.New("canonical result repository returned a different workspace analysis operation"),
			)
		}
		if err := validatePersistedReceiptResult(contract, mutation.Call, mutation.Receipt); err != nil {
			return ToolExecutionResult{}, err
		}
		return ToolExecutionResult{
			Call: mutation.Call, ResultReceiptID: mutation.Receipt.ID,
			OperationID: workspaceAnalysisOperationID,
			Output:      append(json.RawMessage(nil), mutation.Receipt.Output...), UntrustedData: true, Replayed: mutation.Replayed,
		}, nil
	}

	mutation, err := service.calls.FinalizeCall(persistenceContext, FinalizeCallCommand{ExpectedVersion: startResult.Call.Version, Call: succeeded})
	if err != nil {
		return ToolExecutionResult{}, service.finalizationUnknown(persistenceContext, startResult.Call, err)
	}
	return ToolExecutionResult{Call: mutation.Call, Output: append(json.RawMessage(nil), output...), UntrustedData: true, Replayed: mutation.Replayed}, nil
}

func (service *ExecutionService) replay(
	ctx context.Context,
	command ExecuteToolCommand,
	contract Contract,
	executor Executor,
	arguments json.RawMessage,
	call domain.ToolCall,
) (ToolExecutionResult, error) {
	if call.Status != domain.CallSucceeded {
		return replayedExecutionResult(call)
	}
	if contract.Definition.ResultPersistencePolicy == domain.ResultPersistenceCanonical {
		return service.replayPersistedReceipt(ctx, contract, call)
	}
	loader, ok := executor.(ResultReceiptLoader)
	if !ok || isNilInterface(loader) {
		return ToolExecutionResult{}, executionError(foundation.ErrorConsistencyViolation, errorCodeResultReplayUnavailable, false, errors.New("tool executor cannot load a canonical result receipt"))
	}
	replayContext, cancel := context.WithTimeout(ctx, contract.Definition.Timeout)
	defer cancel()
	receipt, err := loader.LoadResultReceipt(replayContext, ExecutorRequest{
		Identity: command.Identity, Tool: contract.Definition.Ref, Arguments: append(json.RawMessage(nil), arguments...),
		Reason: strings.TrimSpace(command.Request.Reason), IdempotencyKey: command.IdempotencyKey,
	}, call)
	if err == nil && replayContext.Err() != nil {
		err = replayContext.Err()
	}
	if err != nil {
		return ToolExecutionResult{}, classifiedOr(err, foundation.ErrorDependencyUnavailable, errorCodeResultReplayUnavailable, false)
	}
	output, summary, err := validateExecutorResult(contract, receipt)
	if err != nil {
		return ToolExecutionResult{}, err
	}
	if hashBytes(output) != call.ResponseHash || int64(len(output)) != call.ResponseBytes || !jsonDocumentsEqual(summary, call.ResponseSummary) ||
		receipt.ResultRef != call.ResultRef || receipt.SideEffectType != call.SideEffectType || receipt.SideEffectID != call.SideEffectID {
		return ToolExecutionResult{}, executionError(foundation.ErrorConsistencyViolation, errorCodeResultReplayUnavailable, false, errors.New("canonical result receipt differs from persisted tool call"))
	}
	return ToolExecutionResult{Call: call, Output: append(json.RawMessage(nil), output...), UntrustedData: true, Replayed: true}, nil
}

func (service *ExecutionService) replayPersistedReceipt(ctx context.Context, contract Contract, call domain.ToolCall) (ToolExecutionResult, error) {
	repository, err := service.resultReceiptRepository()
	if err != nil {
		return ToolExecutionResult{}, err
	}
	replayContext, cancel := context.WithTimeout(ctx, contract.Definition.Timeout)
	defer cancel()
	receipt, err := repository.LoadResultReceipt(replayContext, LoadResultReceiptCommand{Call: call, Definition: contract.Definition})
	if err == nil && replayContext.Err() != nil {
		err = replayContext.Err()
	}
	if err != nil {
		return ToolExecutionResult{}, classifiedOr(err, foundation.ErrorDependencyUnavailable, errorCodeResultReplayUnavailable, false)
	}
	if err := validatePersistedReceiptResult(contract, call, receipt); err != nil {
		return ToolExecutionResult{}, err
	}
	return ToolExecutionResult{
		Call: call, ResultReceiptID: receipt.ID,
		Output: append(json.RawMessage(nil), receipt.Output...), UntrustedData: true, Replayed: true,
	}, nil
}

func (service *ExecutionService) resultReceiptRepository() (ResultReceiptRepository, error) {
	repository, ok := service.calls.(ResultReceiptRepository)
	if !ok || isNilInterface(repository) {
		return nil, executionError(foundation.ErrorDependencyUnavailable, errorCodeResultPersistenceUnavailable, false, errors.New("canonical tool result persistence is unavailable"))
	}
	return repository, nil
}

func (service *ExecutionService) resultReceiptFailureRepository() (ResultReceiptFailureRepository, error) {
	repository, ok := service.calls.(ResultReceiptFailureRepository)
	if !ok || isNilInterface(repository) {
		return nil, executionError(
			foundation.ErrorDependencyUnavailable,
			errorCodeResultPersistenceUnavailable,
			false,
			errors.New("canonical tool result receipt failure persistence is unavailable"),
		)
	}
	return repository, nil
}

func (service *ExecutionService) finishWorkspaceAnalysisReceiptFailure(
	ctx context.Context,
	identity domain.TrustedExecutionIdentity,
	execution workspaceAnalysisToolExecutionContext,
	started domain.ToolCall,
	definition domain.Definition,
	observedOutput json.RawMessage,
	validatedSummary json.RawMessage,
	observedBinding *ExecutorPrivateBinding,
	failureCode domain.ResultReceiptFailureCode,
	cause error,
) error {
	completedAt := service.now()
	summary := append(json.RawMessage(nil), validatedSummary...)
	if len(summary) == 0 {
		var err error
		summary, err = responseSummary(definition, observedOutput, 0)
		if err != nil {
			return workspaceAnalysisFinalizationUnknown(errors.Join(cause, err))
		}
	}
	succeeded := started
	succeeded.Status = domain.CallSucceeded
	succeeded.ResponseHash = hashBytes(observedOutput)
	succeeded.ResponseBytes = int64(len(observedOutput))
	succeeded.ResponseSummary = summary
	succeeded.CompletedAt = &completedAt
	succeeded.DurationMillis = durationMillis(succeeded.StartedAt, completedAt)
	succeeded.Version++
	if err := domain.ValidateToolCall(succeeded); err != nil {
		return workspaceAnalysisFinalizationUnknown(errors.Join(cause, err))
	}

	outputBytes := int64(len(observedOutput))
	failureDraft := domain.ResultReceiptFailureDraft{
		ID:                  execution.ReceiptFailureID,
		OperationID:         execution.OperationID,
		AnalysisRunID:       execution.OperationKey.AnalysisRunID,
		FailureCode:         failureCode,
		ObservedOutputHash:  succeeded.ResponseHash,
		ObservedOutputBytes: &outputBytes,
		CheckedAt:           completedAt,
		CreatedAt:           completedAt,
	}
	if failureCode == domain.ResultReceiptFailureBindingInvalid {
		bindingBytes := int64(0)
		bindingDocument := json.RawMessage(nil)
		if observedBinding != nil {
			bindingDocument = observedBinding.Document
			bindingBytes = int64(len(bindingDocument))
		}
		failureDraft.ObservedBindingHash = hashBytes(bindingDocument)
		failureDraft.ObservedBindingBytes = &bindingBytes
	}
	repository, err := service.resultReceiptFailureRepository()
	if err != nil {
		return err
	}
	mutation, err := repository.FinalizeCallWithReceiptFailure(ctx, FinalizeCallWithReceiptFailureCommand{
		ExpectedVersion: started.Version,
		Identity:        identity,
		Call:            succeeded,
		Definition:      definition,
		Failure:         failureDraft,
	})
	if err != nil {
		// 该 UoW 同时拥有 Call、failure、reservation 与 operation；提交不确定时禁止用其他事务覆盖。
		return err
	}
	if err := validateReceiptFailureMutation(succeeded, definition, failureDraft, mutation); err != nil {
		return workspaceAnalysisFinalizationUnknown(err)
	}
	return workspaceAnalysisReceiptFailureTerminalErrorFor(
		mutation.OperationID,
		mutation.Failure,
		workspaceAnalysisReceiptFailureCause(mutation.Failure, definition),
	)
}

func validateReceiptFailureMutation(
	expectedCall domain.ToolCall,
	definition domain.Definition,
	expectedFailure domain.ResultReceiptFailureDraft,
	mutation ResultReceiptFailureMutationResult,
) error {
	if !sameCanonicalFinalizationCall(expectedCall, mutation.Call) || mutation.OperationID != expectedFailure.OperationID ||
		mutation.Failure.OperationID != expectedFailure.OperationID ||
		mutation.Failure.AnalysisRunID != expectedFailure.AnalysisRunID ||
		mutation.Failure.FailureCode != expectedFailure.FailureCode ||
		mutation.Failure.ObservedOutputHash != expectedFailure.ObservedOutputHash ||
		!equalOptionalInt64(mutation.Failure.ObservedOutputBytes, expectedFailure.ObservedOutputBytes) ||
		mutation.Failure.ObservedBindingHash != expectedFailure.ObservedBindingHash ||
		!equalOptionalInt64(mutation.Failure.ObservedBindingBytes, expectedFailure.ObservedBindingBytes) ||
		(!mutation.Replayed && mutation.Failure.ID != expectedFailure.ID) {
		return errors.New("canonical result receipt failure repository returned a different binding")
	}
	if err := domain.ValidateResultReceiptFailure(mutation.Failure, mutation.Call, definition); err != nil {
		return errors.Join(errors.New("canonical result receipt failure is invalid"), err)
	}
	return nil
}

func validReceiptFailureOutputObservation(output json.RawMessage) bool {
	bytes := int64(len(output))
	return bytes > 0 && bytes <= domain.ResultReceiptFailureMaxObservedOutputBytes
}

func validReceiptFailureBindingObservation(binding *ExecutorPrivateBinding) bool {
	if binding == nil {
		return true
	}
	return int64(len(binding.Document)) <= domain.ResultReceiptFailureMaxObservedBindingBytes
}

func equalOptionalInt64(left, right *int64) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

// RecoverStaleStarted 将失去可信 Run/Node/Attempt lease 的 STARTED 调用有界归约为 UNKNOWN。
func (service *ExecutionService) RecoverStaleStarted(ctx context.Context, limit int) ([]domain.ToolCall, error) {
	if service == nil || isNilInterface(service.calls) || isNilInterface(service.clock) || ctx == nil || limit < 1 || limit > MaxToolStaleRecoveryLimit {
		return nil, executionError(foundation.ErrorInvalidInput, errorCodeRecoveryInvalid, false, errors.New("tool recovery request is invalid"))
	}
	recovered, err := service.calls.RecoverStaleStarted(ctx, limit)
	if err != nil {
		return nil, err
	}
	for _, call := range recovered {
		if call.Status != domain.CallUnknown || call.ErrorCode != errorCodeOutcomeUnknown {
			return nil, executionError(foundation.ErrorConsistencyViolation, errorCodeRecoveryInvalid, false, errors.New("tool recovery repository returned an invalid terminal call"))
		}
	}
	return recovered, nil
}

func (service *ExecutionService) startedCall(command ExecuteToolCommand, contract Contract, arguments json.RawMessage) (domain.ToolCall, error) {
	callID, err := service.ids.New()
	if err != nil {
		return domain.ToolCall{}, err
	}
	requestDocument, err := canonicalRequestDocument(command.Request, contract.Definition.Ref, contract.Definition.InputSchema, arguments)
	if err != nil {
		return domain.ToolCall{}, executionError(foundation.ErrorConsistencyViolation, errorCodeExecutionCommandInvalid, false, err)
	}
	toolRef := contract.Definition.Ref
	inputSchema := contract.Definition.InputSchema
	outputSchema := contract.Definition.OutputSchema
	summary, err := requestSummary(command, contract.Definition, arguments)
	if err != nil {
		return domain.ToolCall{}, executionError(foundation.ErrorConsistencyViolation, errorCodeExecutionCommandInvalid, false, err)
	}
	call := domain.ToolCall{
		ID: callID, WorkspaceID: command.Identity.WorkspaceID, WorkflowRunID: command.Identity.WorkflowRunID,
		NodeRunID: command.Identity.NodeRunID, NodeAttemptID: command.Identity.NodeAttemptID, CallNo: command.CallNo,
		RequestedToolName: command.Request.ToolName, Tool: &toolRef, DefinitionHash: contract.Definition.DefinitionHash,
		InputSchema: &inputSchema, OutputSchema: &outputSchema, Capability: contract.Definition.RequiredCapability,
		SideEffectLevel: contract.Definition.SideEffectLevel, InvocationPolicy: contract.Definition.InvocationPolicy,
		IdempotencyKey: command.IdempotencyKey, RequestHash: hashBytes(requestDocument), RequestBytes: int64(len(requestDocument)),
		RequestSummary: summary, Status: domain.CallStarted, Version: 1, StartedAt: service.now(),
	}
	if err := domain.ValidateToolCall(call); err != nil {
		return domain.ToolCall{}, err
	}
	return call, nil
}

func (service *ExecutionService) refuse(ctx context.Context, command ExecuteToolCommand, contract *Contract, refusal error) error {
	call, err := service.refusedCall(command, contract, refusal)
	if err != nil {
		return err
	}
	if _, err := service.calls.RecordRefused(ctx, RecordRefusedCommand{Identity: command.Identity, Call: call}); err != nil {
		return executionError(foundation.ErrorConsistencyViolation, errorCodeRefusalPersistenceFailed, false, err)
	}
	return refusal
}

func (service *ExecutionService) refusedCall(command ExecuteToolCommand, contract *Contract, refusal error) (domain.ToolCall, error) {
	callID, err := service.ids.New()
	if err != nil {
		return domain.ToolCall{}, err
	}
	requestDocument, err := canonicalRequestDocument(command.Request, domain.ToolRef{Name: command.Request.ToolName, Version: domain.ToolRequestSchemaVersionV1}, domain.SchemaRef{ID: "tool.request", Version: domain.ToolRequestSchemaVersionV1}, command.Request.Arguments)
	if err != nil {
		return domain.ToolCall{}, executionError(foundation.ErrorConsistencyViolation, errorCodeExecutionCommandInvalid, false, err)
	}
	startedAt := service.now()
	completedAt := startedAt
	summary, err := refusalRequestSummary(command)
	if err != nil {
		return domain.ToolCall{}, executionError(foundation.ErrorConsistencyViolation, errorCodeExecutionCommandInvalid, false, err)
	}
	call := domain.ToolCall{
		ID: callID, WorkspaceID: command.Identity.WorkspaceID, WorkflowRunID: command.Identity.WorkflowRunID,
		NodeRunID: command.Identity.NodeRunID, NodeAttemptID: command.Identity.NodeAttemptID, CallNo: command.CallNo,
		RequestedToolName: command.Request.ToolName, RequestHash: hashBytes(requestDocument), RequestBytes: int64(len(requestDocument)),
		RequestSummary: summary, Status: domain.CallRefused, ErrorCode: stableErrorCode(refusal, errorCodeExecutionFailed),
		Version: 1, StartedAt: startedAt, CompletedAt: &completedAt,
	}
	if contract != nil {
		toolRef := contract.Definition.Ref
		inputSchema := contract.Definition.InputSchema
		outputSchema := contract.Definition.OutputSchema
		call.Tool = &toolRef
		call.DefinitionHash = contract.Definition.DefinitionHash
		call.InputSchema = &inputSchema
		call.OutputSchema = &outputSchema
		call.Capability = contract.Definition.RequiredCapability
		call.SideEffectLevel = contract.Definition.SideEffectLevel
		call.InvocationPolicy = contract.Definition.InvocationPolicy
	}
	if err := domain.ValidateToolCall(call); err != nil {
		return domain.ToolCall{}, err
	}
	return call, nil
}

func (service *ExecutionService) finishStartedExecutionError(
	ctx context.Context,
	identity domain.TrustedExecutionIdentity,
	started domain.ToolCall,
	definition domain.Definition,
	cause error,
	workspaceAnalysisOperationID foundation.ID,
) error {
	if workspaceAnalysisOperationID == "" {
		return service.finishExecutionError(ctx, started, definition, cause)
	}
	return service.finishWorkspaceAnalysisExecutionError(ctx, identity, workspaceAnalysisOperationID, started, definition, cause)
}

func (service *ExecutionService) finishWorkspaceAnalysisExecutionError(
	ctx context.Context,
	identity domain.TrustedExecutionIdentity,
	operationID foundation.ID,
	started domain.ToolCall,
	definition domain.Definition,
	cause error,
) error {
	repository, err := service.workspaceAnalysisToolOperationRepository()
	if err != nil {
		return err
	}
	completedAt := service.now()
	terminal := started
	terminal.CompletedAt = &completedAt
	terminal.DurationMillis = durationMillis(terminal.StartedAt, completedAt)
	terminal.Version++
	if mustMarkUnknown(definition.SideEffectLevel, cause) {
		terminal.Status = domain.CallUnknown
		terminal.ErrorCode = errorCodeOutcomeUnknown
		mutation, err := repository.FinalizeWorkspaceAnalysisToolCall(ctx, FinalizeWorkspaceAnalysisToolCallCommand{
			ExpectedVersion: started.Version, Identity: identity, Call: terminal,
		})
		if err != nil || !sameWorkspaceAnalysisTerminalCall(terminal, mutation.Call) {
			return workspaceAnalysisFinalizationUnknown(errors.Join(cause, errOr(err, "workspace analysis terminal finalization drifted")))
		}
		return workspaceAnalysisToolTerminalErrorFor(
			operationID,
			mutation.Call,
			executionError(foundation.ErrorManualRecoveryRequired, errorCodeOutcomeUnknown, false, cause),
		)
	}

	classified := classifyExecutionFailure(cause)
	terminal.Status = domain.CallFailed
	terminal.ErrorCode = classified.Code
	terminal.Retryable = classified.Retryable
	mutation, err := repository.FinalizeWorkspaceAnalysisToolCall(ctx, FinalizeWorkspaceAnalysisToolCallCommand{
		ExpectedVersion: started.Version, Identity: identity, Call: terminal,
	})
	if err != nil || !sameWorkspaceAnalysisTerminalCall(terminal, mutation.Call) {
		// 专用 Repository 已按完整事务闭包尝试 commit-loss recovery；不得再用通用 MarkUnknown
		// 单独覆盖 Call，否则会破坏 Operation/Reservation 的原子性。
		return workspaceAnalysisFinalizationUnknown(errors.Join(cause, errOr(err, "workspace analysis terminal finalization drifted")))
	}
	return workspaceAnalysisToolTerminalErrorFor(operationID, mutation.Call, classified)
}

func workspaceAnalysisFinalizationUnknown(cause error) error {
	return executionError(foundation.ErrorManualRecoveryRequired, errorCodeFinalizationUnknown, false, cause)
}

func sameWorkspaceAnalysisTerminalCall(expected, actual domain.ToolCall) bool {
	return domain.ValidateToolCall(actual) == nil && expected.ID == actual.ID &&
		expected.WorkspaceID == actual.WorkspaceID && expected.WorkflowRunID == actual.WorkflowRunID &&
		expected.NodeRunID == actual.NodeRunID && expected.NodeAttemptID == actual.NodeAttemptID &&
		expected.CallNo == actual.CallNo && expected.Status == actual.Status &&
		expected.ErrorCode == actual.ErrorCode && expected.Retryable == actual.Retryable &&
		expected.Version == actual.Version && expected.StartedAt.Equal(actual.StartedAt) &&
		sameWorkspaceAnalysisToolRequest(expected, actual)
}

func (service *ExecutionService) finishExecutionError(ctx context.Context, started domain.ToolCall, definition domain.Definition, cause error) error {
	if mustMarkUnknown(definition.SideEffectLevel, cause) {
		completedAt := service.now()
		unknown := started
		unknown.Status = domain.CallUnknown
		unknown.ErrorCode = errorCodeOutcomeUnknown
		unknown.CompletedAt = &completedAt
		unknown.DurationMillis = durationMillis(unknown.StartedAt, completedAt)
		unknown.Version++
		if _, err := service.calls.MarkUnknown(ctx, MarkUnknownCommand{ExpectedVersion: started.Version, Call: unknown}); err != nil {
			return executionError(foundation.ErrorManualRecoveryRequired, errorCodeFinalizationUnknown, false, err)
		}
		return executionError(foundation.ErrorManualRecoveryRequired, errorCodeOutcomeUnknown, false, cause)
	}

	classified := classifyExecutionFailure(cause)
	completedAt := service.now()
	failed := started
	failed.Status = domain.CallFailed
	failed.ErrorCode = classified.Code
	failed.Retryable = classified.Retryable
	failed.CompletedAt = &completedAt
	failed.DurationMillis = durationMillis(failed.StartedAt, completedAt)
	failed.Version++
	if _, err := service.calls.FinalizeCall(ctx, FinalizeCallCommand{ExpectedVersion: started.Version, Call: failed}); err != nil {
		return service.finalizationUnknown(ctx, started, err)
	}
	return classified
}

func (service *ExecutionService) workspaceAnalysisToolOperationRepository() (WorkspaceAnalysisToolOperationRepository, error) {
	repository, ok := service.calls.(WorkspaceAnalysisToolOperationRepository)
	if !ok || isNilInterface(repository) {
		return nil, executionError(
			foundation.ErrorDependencyUnavailable,
			errorCodeResultPersistenceUnavailable,
			false,
			errors.New("workspace analysis tool operation persistence is unavailable"),
		)
	}
	return repository, nil
}

func (service *ExecutionService) finalizationUnknown(ctx context.Context, started domain.ToolCall, cause error) error {
	completedAt := service.now()
	unknown := started
	unknown.Status = domain.CallUnknown
	unknown.ErrorCode = errorCodeOutcomeUnknown
	unknown.CompletedAt = &completedAt
	unknown.DurationMillis = durationMillis(unknown.StartedAt, completedAt)
	unknown.Version++
	recoveryContext, recoveryCancel := context.WithTimeout(context.WithoutCancel(ctx), toolFinalizationTimeout)
	defer recoveryCancel()
	if _, err := service.calls.MarkUnknown(recoveryContext, MarkUnknownCommand{ExpectedVersion: started.Version, Call: unknown}); err != nil {
		return executionError(foundation.ErrorManualRecoveryRequired, errorCodeFinalizationUnknown, false, errors.Join(cause, err))
	}
	return executionError(foundation.ErrorManualRecoveryRequired, errorCodeFinalizationUnknown, false, cause)
}

func (service *ExecutionService) now() time.Time {
	return service.clock.Now().UTC()
}

func validateWorkflowToolPolicy(policy WorkflowToolPolicy, identity domain.TrustedExecutionIdentity) error {
	if policy.Identity != identity || policy.WorkflowKey == "" || policy.WorkflowKey != strings.TrimSpace(policy.WorkflowKey) || len(policy.WorkflowKey) > 128 ||
		policy.NodeKind == "" || policy.NodeKind != strings.TrimSpace(policy.NodeKind) || len(policy.NodeKind) > 128 || policy.AttemptLeaseTo.IsZero() {
		return executionError(foundation.ErrorConsistencyViolation, errorCodePolicyInvalid, false, errors.New("workflow tool policy identity or metadata is invalid"))
	}
	permissions := append([]capability.Capability(nil), policy.Permissions...)
	if !sort.SliceIsSorted(permissions, func(left, right int) bool { return permissions[left] < permissions[right] }) {
		return executionError(foundation.ErrorConsistencyViolation, errorCodePolicyInvalid, false, errors.New("workflow tool permissions are not canonical"))
	}
	for index, permission := range permissions {
		if !capability.IsKnown(permission) || (index > 0 && permissions[index-1] == permission) {
			return executionError(foundation.ErrorConsistencyViolation, errorCodePolicyInvalid, false, errors.New("workflow tool permissions are invalid"))
		}
	}
	allowed := append([]domain.ToolRef(nil), policy.AllowedTools...)
	if !sort.SliceIsSorted(allowed, func(left, right int) bool {
		if allowed[left].Name == allowed[right].Name {
			return allowed[left].Version < allowed[right].Version
		}
		return allowed[left].Name < allowed[right].Name
	}) {
		return executionError(foundation.ErrorConsistencyViolation, errorCodePolicyInvalid, false, errors.New("workflow allowed tools are not canonical"))
	}
	for index, ref := range allowed {
		if ref.Validate() != nil || (index > 0 && allowed[index-1] == ref) {
			return executionError(foundation.ErrorConsistencyViolation, errorCodePolicyInvalid, false, errors.New("workflow allowed tools are invalid"))
		}
	}
	return nil
}

func resolveAllowedTool(allowed []domain.ToolRef, name string) (domain.ToolRef, error) {
	var result domain.ToolRef
	for _, ref := range allowed {
		if ref.Name != name {
			continue
		}
		if result.Name != "" {
			return domain.ToolRef{}, executionError(foundation.ErrorVersionConflict, errorCodeAllowedVersionAmbiguous, false, errors.New("workflow allows multiple versions for one requested tool name"))
		}
		result = ref
	}
	if result.Name == "" {
		return domain.ToolRef{}, executionError(foundation.ErrorPermissionDenied, errorCodeToolNotAllowed, false, errors.New("workflow node does not allow requested tool"))
	}
	return result, nil
}

func validateIdempotency(mode domain.IdempotencyMode, key string) error {
	switch mode {
	case domain.IdempotencyNone:
		if key != "" {
			return executionError(foundation.ErrorInvalidInput, errorCodeIdempotencyUnexpected, false, errors.New("tool does not accept an execution idempotency key"))
		}
	case domain.IdempotencyOptional:
		return nil
	case domain.IdempotencyRequired:
		if key == "" {
			return executionError(foundation.ErrorInvalidInput, errorCodeIdempotencyRequired, false, errors.New("tool requires an execution idempotency key"))
		}
	default:
		return executionError(foundation.ErrorConsistencyViolation, errorCodePolicyInvalid, false, errors.New("tool idempotency policy is invalid"))
	}
	return nil
}

func workflowBindingAllowed(bindings []domain.WorkflowBinding, key string, version int64) bool {
	for _, binding := range bindings {
		if binding.Key == key && binding.Version == version {
			return true
		}
	}
	return false
}

func toolRefAllowed(allowed []domain.ToolRef, target domain.ToolRef) bool {
	for _, ref := range allowed {
		if ref == target {
			return true
		}
	}
	return false
}

func capabilityAllowed(permissions []capability.Capability, target capability.Capability) bool {
	for _, permission := range permissions {
		if permission == target {
			return true
		}
	}
	return false
}

func validInvocationSource(source domain.InvocationSource) bool {
	return source == domain.InvocationSourceModelRequest || source == domain.InvocationSourceTrustedWorkflow
}

func validOptionalReference(value string, maxBytes int) bool {
	return value == "" || (value == strings.TrimSpace(value) && len(value) <= maxBytes && utf8.ValidString(value) && !strings.ContainsAny(value, "\x00\r\n"))
}

func canonicalRequestDocument(request domain.ToolRequestV1, ref domain.ToolRef, schema domain.SchemaRef, arguments json.RawMessage) ([]byte, error) {
	document := struct {
		SchemaVersion int              `json:"schema_version"`
		ToolName      string           `json:"tool_name"`
		ToolVersion   int64            `json:"tool_version"`
		InputSchema   domain.SchemaRef `json:"input_schema"`
		Arguments     json.RawMessage  `json:"arguments"`
		Reason        string           `json:"reason"`
	}{
		SchemaVersion: request.SchemaVersion, ToolName: request.ToolName, ToolVersion: ref.Version,
		InputSchema: schema, Arguments: append(json.RawMessage(nil), arguments...), Reason: strings.TrimSpace(request.Reason),
	}
	return json.Marshal(document)
}

func requestSummary(command ExecuteToolCommand, definition domain.Definition, arguments json.RawMessage) (json.RawMessage, error) {
	return encodeSummary(map[string]any{
		"argument_bytes": len(arguments), "reason_bytes": len(strings.TrimSpace(command.Request.Reason)),
		"schema_version": command.Request.SchemaVersion, "tool_name": command.Request.ToolName,
		"tool_version": definition.Ref.Version,
	})
}

func refusalRequestSummary(command ExecuteToolCommand) (json.RawMessage, error) {
	return encodeSummary(map[string]any{
		"argument_bytes": len(command.Request.Arguments), "reason_bytes": len(strings.TrimSpace(command.Request.Reason)),
		"schema_version": command.Request.SchemaVersion, "tool_name": command.Request.ToolName,
	})
}

func responseSummary(definition domain.Definition, output json.RawMessage, redactedCount int) (json.RawMessage, error) {
	return encodeSummary(map[string]any{
		"output_bytes": len(output), "output_hash": hashBytes(output),
		"redacted_field_count": redactedCount, "schema_id": definition.OutputSchema.ID,
		"schema_version": definition.OutputSchema.Version,
	})
}

func validateExecutorResult(contract Contract, result ExecutorResult) (json.RawMessage, json.RawMessage, error) {
	if int64(len(result.Output)) > contract.Definition.MaxOutputBytes {
		return nil, nil, executionError(foundation.ErrorNonRetryableFailure, errorCodeOutputTooLarge, false, errors.New("tool output exceeds definition byte limit"))
	}
	output, err := contract.DecodeOutput(result.Output)
	if err != nil || len(output) == 0 || int64(len(output)) > contract.Definition.MaxOutputBytes {
		return nil, nil, executionError(foundation.ErrorNonRetryableFailure, errorCodeOutputInvalid, false, errOr(err, "tool output decoder returned invalid output"))
	}
	output, redactedCount, err := sanitizeOutput(contract, output)
	if err != nil || len(output) == 0 || int64(len(output)) > contract.Definition.MaxOutputBytes {
		return nil, nil, executionError(foundation.ErrorNonRetryableFailure, errorCodeOutputInvalid, false, errOr(err, "tool output sanitizer rejected output"))
	}
	if (!domain.ValidResultReference(result.ResultRef) && result.ResultRef != "") ||
		!domain.ValidSideEffectReference(result.SideEffectType, result.SideEffectID) {
		return nil, nil, executionError(foundation.ErrorNonRetryableFailure, errorCodeOutputInvalid, false, errors.New("tool executor returned an invalid stable reference"))
	}
	summary, err := responseSummary(contract.Definition, output, redactedCount)
	if err != nil {
		return nil, nil, executionError(foundation.ErrorConsistencyViolation, errorCodeOutputInvalid, false, err)
	}
	return output, summary, nil
}

func validateExecutorPrivateBinding(definition domain.Definition, result ExecutorResult) error {
	switch definition.ResultPersistencePolicy {
	case domain.ResultPersistenceDisabled:
		if result.PrivateBinding != nil {
			return executionError(foundation.ErrorNonRetryableFailure, errorCodeOutputInvalid, false, errors.New("tool executor returned a private binding without canonical persistence"))
		}
		return nil
	case domain.ResultPersistenceCanonical:
		contract, found := domain.WorkspaceAnalysisResultReceiptContract(definition.Ref)
		if !found || contract.OutputSchema != definition.OutputSchema || contract.MaxOutputBytes != definition.MaxOutputBytes ||
			result.ResultRef != "" || result.SideEffectType != "" || result.SideEffectID != "" {
			return executionError(foundation.ErrorConsistencyViolation, errorCodeOutputInvalid, false, errors.New("canonical tool executor result does not match its persistence contract"))
		}
		if result.PrivateBinding == nil {
			if contract.RequiresPrivateBinding {
				return executionError(foundation.ErrorNonRetryableFailure, errorCodeOutputInvalid, false, errors.New("canonical tool executor omitted its private binding"))
			}
			return nil
		}
		if result.PrivateBinding.Schema != contract.PrivateBindingSchema || len(result.PrivateBinding.Document) == 0 ||
			int64(len(result.PrivateBinding.Document)) > contract.MaxPrivateBindingBytes {
			return executionError(foundation.ErrorNonRetryableFailure, errorCodeOutputInvalid, false, errors.New("canonical tool executor private binding is invalid"))
		}
		return nil
	default:
		return executionError(foundation.ErrorConsistencyViolation, errorCodeOutputInvalid, false, errors.New("tool result persistence policy is unsupported"))
	}
}

func validatePersistedReceiptResult(contract Contract, call domain.ToolCall, receipt domain.ResultReceipt) error {
	if err := domain.ValidateResultReceipt(receipt, call, contract.Definition); err != nil {
		return executionError(foundation.ErrorConsistencyViolation, errorCodeResultReplayUnavailable, false, err)
	}
	output, summary, err := validateExecutorResult(contract, ExecutorResult{Output: receipt.Output})
	if err != nil {
		return err
	}
	if hashBytes(output) != call.ResponseHash || int64(len(output)) != call.ResponseBytes || !jsonDocumentsEqual(summary, call.ResponseSummary) {
		return executionError(foundation.ErrorConsistencyViolation, errorCodeResultReplayUnavailable, false, errors.New("canonical result receipt differs from persisted tool call"))
	}
	return nil
}

func sameCanonicalFinalizationCall(expected, actual domain.ToolCall) bool {
	return expected.ID == actual.ID && expected.WorkspaceID == actual.WorkspaceID &&
		expected.WorkflowRunID == actual.WorkflowRunID && expected.NodeRunID == actual.NodeRunID &&
		expected.NodeAttemptID == actual.NodeAttemptID && expected.CallNo == actual.CallNo &&
		expected.RequestedToolName == actual.RequestedToolName && equalToolRef(expected.Tool, actual.Tool) &&
		expected.DefinitionHash == actual.DefinitionHash && equalSchemaRef(expected.InputSchema, actual.InputSchema) &&
		equalSchemaRef(expected.OutputSchema, actual.OutputSchema) && expected.Capability == actual.Capability &&
		expected.SideEffectLevel == actual.SideEffectLevel && expected.InvocationPolicy == actual.InvocationPolicy &&
		expected.IdempotencyKey == actual.IdempotencyKey && expected.RequestHash == actual.RequestHash &&
		expected.RequestBytes == actual.RequestBytes && jsonDocumentsEqual(expected.RequestSummary, actual.RequestSummary) &&
		expected.ResponseHash == actual.ResponseHash && expected.ResponseBytes == actual.ResponseBytes &&
		jsonDocumentsEqual(expected.ResponseSummary, actual.ResponseSummary) && expected.ResultRef == actual.ResultRef &&
		expected.SideEffectType == actual.SideEffectType && expected.SideEffectID == actual.SideEffectID &&
		expected.Status == actual.Status && expected.ErrorCode == actual.ErrorCode && expected.Retryable == actual.Retryable &&
		expected.Version == actual.Version && expected.StartedAt.Equal(actual.StartedAt)
}

func equalToolRef(left, right *domain.ToolRef) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func equalSchemaRef(left, right *domain.SchemaRef) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func cloneExecutorPrivateBinding(binding *ExecutorPrivateBinding) *ExecutorPrivateBinding {
	if binding == nil {
		return nil
	}
	clone := *binding
	clone.Document = append(json.RawMessage(nil), binding.Document...)
	return &clone
}

func replayedExecutionResult(call domain.ToolCall) (ToolExecutionResult, error) {
	switch call.Status {
	case domain.CallSucceeded:
		return ToolExecutionResult{}, executionError(foundation.ErrorConsistencyViolation, errorCodeResultReplayUnavailable, false, errors.New("persisted tool result payload is not recoverable from the call receipt"))
	case domain.CallFailed:
		kind := foundation.ErrorNonRetryableFailure
		if call.Retryable {
			kind = foundation.ErrorRetryableFailure
		}
		return ToolExecutionResult{}, executionError(kind, call.ErrorCode, call.Retryable, errors.New("persisted tool call failed"))
	case domain.CallUnknown:
		return ToolExecutionResult{}, executionError(foundation.ErrorManualRecoveryRequired, call.ErrorCode, false, errors.New("persisted tool call outcome is unknown"))
	case domain.CallStarted:
		return ToolExecutionResult{}, executionError(foundation.ErrorVersionConflict, errorCodeCallInProgress, false, errors.New("tool call is already in progress"))
	case domain.CallRefused:
		return ToolExecutionResult{}, executionError(foundation.ErrorPermissionDenied, call.ErrorCode, false, errors.New("persisted tool call was refused"))
	default:
		return ToolExecutionResult{}, executionError(foundation.ErrorConsistencyViolation, errorCodePolicyInvalid, false, errors.New("persisted tool call has unsupported status"))
	}
}

func sanitizeOutput(contract Contract, output json.RawMessage) (json.RawMessage, int, error) {
	var document any
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		return nil, 0, errors.New("tool output cannot be sanitized")
	}
	redactedCount := 0
	for _, pointer := range contract.Definition.SensitiveFields {
		segments, err := jsonPointerSegments(pointer)
		if err != nil {
			return nil, 0, errors.New("tool sensitive field path is invalid")
		}
		redactedCount += redactAtPointer(document, segments)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, 0, errors.New("tool output cannot be encoded after sanitization")
	}
	canonical, err := contract.DecodeOutput(encoded)
	if err != nil {
		return nil, 0, errors.New("tool output no longer matches schema after sanitization")
	}
	if containsSensitiveValue(document) {
		return nil, 0, errors.New("tool output contains a sensitive canary outside declared fields")
	}
	return canonical, redactedCount, nil
}

func jsonPointerSegments(pointer string) ([]string, error) {
	if pointer == "" || pointer[0] != '/' {
		return nil, errors.New("invalid json pointer")
	}
	parts := strings.Split(pointer[1:], "/")
	for index := range parts {
		parts[index] = strings.ReplaceAll(strings.ReplaceAll(parts[index], "~1", "/"), "~0", "~")
		if parts[index] == "" {
			return nil, errors.New("invalid json pointer segment")
		}
	}
	return parts, nil
}

func redactAtPointer(value any, segments []string) int {
	if len(segments) == 0 {
		return 0
	}
	switch current := value.(type) {
	case map[string]any:
		child, exists := current[segments[0]]
		if !exists {
			return 0
		}
		if len(segments) == 1 {
			redacted, count := redactSensitiveValue(child)
			current[segments[0]] = redacted
			return count
		}
		return redactAtPointer(child, segments[1:])
	case []any:
		index, err := strconv.Atoi(segments[0])
		if err != nil || index < 0 || index >= len(current) {
			return 0
		}
		if len(segments) == 1 {
			redacted, count := redactSensitiveValue(current[index])
			current[index] = redacted
			return count
		}
		return redactAtPointer(current[index], segments[1:])
	default:
		return 0
	}
}

func redactSensitiveValue(value any) (any, int) {
	switch current := value.(type) {
	case string:
		if containsSensitiveOutput(current) {
			return "[REDACTED]", 1
		}
		return current, 0
	case []any:
		count := 0
		for index := range current {
			redactedValue, redacted := redactSensitiveValue(current[index])
			current[index] = redactedValue
			count += redacted
		}
		return current, count
	case map[string]any:
		count := 0
		for key := range current {
			redactedValue, redacted := redactSensitiveValue(current[key])
			current[key] = redactedValue
			count += redacted
		}
		return current, count
	default:
		return value, 0
	}
}

func containsSensitiveOutput(value string) bool {
	return foundationredaction.ContainsSecret(value) || foundationredaction.ContainsAbsolutePath(value)
}

func containsSensitiveValue(value any) bool {
	switch current := value.(type) {
	case string:
		return containsSensitiveOutput(current)
	case []any:
		for _, item := range current {
			if containsSensitiveValue(item) {
				return true
			}
		}
	case map[string]any:
		for _, item := range current {
			if containsSensitiveValue(item) {
				return true
			}
		}
	}
	return false
}

func jsonDocumentsEqual(left, right json.RawMessage) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	leftCanonical, leftErr := json.Marshal(leftValue)
	rightCanonical, rightErr := json.Marshal(rightValue)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftCanonical, rightCanonical)
}

func mustMarkUnknown(level domain.SideEffectLevel, err error) bool {
	if level == domain.SideEffectDomainWrite || level == domain.SideEffectUnknown {
		return true
	}
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Kind == foundation.ErrorManualRecoveryRequired
}

func classifyExecutionFailure(err error) *foundation.Error {
	switch {
	case errors.Is(err, context.Canceled):
		return executionError(foundation.ErrorNonRetryableFailure, errorCodeExecutionCancelled, false, err)
	case errors.Is(err, context.DeadlineExceeded):
		return executionError(foundation.ErrorRetryableFailure, errorCodeExecutionTimeout, true, err)
	}
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return executionError(classified.Kind, stableErrorCode(classified, errorCodeExecutionFailed), classified.Retryable, err)
	}
	return executionError(foundation.ErrorNonRetryableFailure, errorCodeExecutionFailed, false, err)
}

func classifiedOr(err error, kind foundation.ErrorKind, code string, retryable bool) error {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return err
	}
	return executionError(kind, code, retryable, err)
}

func stableErrorCode(err error, fallback string) string {
	var classified *foundation.Error
	if errors.As(err, &classified) && validStableCode(classified.Code) {
		return classified.Code
	}
	return fallback
}

func validStableCode(value string) bool {
	if value == "" || len(value) > 128 || value[0] < 'A' || value[0] > 'Z' {
		return false
	}
	for _, character := range value {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') && character != '_' {
			return false
		}
	}
	return true
}

func hashBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func durationMillis(startedAt, completedAt time.Time) int64 {
	if completedAt.Before(startedAt) {
		return 0
	}
	return completedAt.Sub(startedAt).Milliseconds()
}

func encodeSummary(value any) (json.RawMessage, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(encoded) == 0 || len(encoded) > domain.MaxToolSummaryBytes {
		return nil, errors.New("tool summary exceeds bounded persistence contract")
	}
	return encoded, nil
}

func errOr(err error, message string) error {
	if err != nil {
		return err
	}
	return errors.New(message)
}

func executionError(kind foundation.ErrorKind, code string, retryable bool, cause error) *foundation.Error {
	return foundation.NewError(kind, code, retryable, cause)
}
