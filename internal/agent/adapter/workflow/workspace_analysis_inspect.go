package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const workspaceAnalysisInspectReason = "inspect workspace git aggregate"

var (
	workspaceAnalysisGitStatusRef          = toolsdomain.ToolRef{Name: "ReadGitStatus", Version: 2}
	workspaceAnalysisGitStatusInputSchema  = toolsdomain.SchemaRef{ID: "tool.read_git_status.input", Version: 1}
	workspaceAnalysisGitStatusOutputSchema = toolsdomain.SchemaRef{ID: "tool.read_git_status.output", Version: 1}
)

type workspaceAnalysisToolExecutor interface {
	ExecuteWorkspaceAnalysisTool(context.Context, toolsapplication.ExecuteWorkspaceAnalysisToolCommand) (toolsapplication.ToolExecutionResult, error)
}

// WorkspaceAnalysisInspectExecutorDependencies 是 inspect_workspace 唯一允许的权威读取与 Tool 执行边界。
type WorkspaceAnalysisInspectExecutorDependencies struct {
	Context   conversationapplication.QuestionExecutionContextLoader
	Runs      agentapplication.WorkspaceAnalysisRunLoader
	Tools     workspaceAnalysisToolExecutor
	Finalizer conversationapplication.WorkspaceAnalysisFinalizer
	Clock     foundation.Clock
}

// WorkspaceAnalysisInspectExecutor 执行一次可恢复的 ReadGitStatus@2 并返回安全聚合回执。
type WorkspaceAnalysisInspectExecutor struct {
	dependencies WorkspaceAnalysisInspectExecutorDependencies
	clock        foundation.Clock
}

// NewWorkspaceAnalysisInspectExecutor 创建只接受 workspace-analysis@1 inspect_workspace 节点的 Executor。
func NewWorkspaceAnalysisInspectExecutor(dependencies WorkspaceAnalysisInspectExecutorDependencies) (*WorkspaceAnalysisInspectExecutor, error) {
	if nilDependency(dependencies.Context) || nilDependency(dependencies.Runs) || nilDependency(dependencies.Tools) ||
		nilDependency(dependencies.Finalizer) {
		return nil, workflowError(
			foundation.ErrorDependencyUnavailable,
			ErrorCodeCapabilityUnavailable,
			false,
			errors.New("workspace analysis inspect dependencies are unavailable"),
		)
	}
	clock := dependencies.Clock
	if nilDependency(clock) {
		clock = foundation.SystemClock{}
	}
	return &WorkspaceAnalysisInspectExecutor{dependencies: dependencies, clock: clock}, nil
}

// Execute 从持久 Workflow/Question/Analysis Run 恢复身份后构造固定 Tool 请求。
func (executor *WorkspaceAnalysisInspectExecutor) Execute(
	ctx context.Context,
	execution workflowapplication.ExecutionContext,
) (workflowapplication.ExecutionResult, error) {
	if executor == nil || nilDependency(executor.dependencies.Context) || nilDependency(executor.dependencies.Runs) ||
		nilDependency(executor.dependencies.Tools) || nilDependency(executor.dependencies.Finalizer) {
		return workflowapplication.ExecutionResult{}, workflowError(
			foundation.ErrorDependencyUnavailable,
			ErrorCodeCapabilityUnavailable,
			false,
			errors.New("workspace analysis inspect executor is unavailable"),
		)
	}
	if ctx == nil {
		return workflowapplication.ExecutionResult{}, workflowError(
			foundation.ErrorInvalidInput, ErrorCodeInputInvalid, false, errors.New("workspace analysis inspect context is nil"),
		)
	}
	nodeStartedAt, err := workspaceAnalysisExecutionStartedAt(executor.clock)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	definition := conversationworkflow.RegisteredWorkspaceAnalysisDefinition()
	node, found := workspaceAnalysisNode(definition, conversationworkflow.WorkspaceAnalysisNodeInspectWorkspace)
	identity := workspaceAnalysisToolIdentity(execution)
	if !found || execution.DefinitionVersion != definition.Version || execution.DefinitionHash != definition.GraphHash ||
		execution.NodeKey != node.Key || execution.NodeKind != node.Kind || execution.InputSchemaVersion != node.InputSchemaVersion ||
		!validExecutionID(execution.WorkspaceID) || !validExecutionID(execution.DefinitionID) || !validExecutionID(execution.RunID) ||
		!validExecutionID(execution.NodeRunID) || !validExecutionID(execution.NodeAttemptID) || execution.NodeVersion < 1 ||
		execution.AttemptNo < 1 || execution.DispatchNo < 1 || execution.RetryNo < 0 || strings.TrimSpace(execution.LeaseOwner) == "" ||
		identity.Validate() != nil {
		return workflowapplication.ExecutionResult{}, workflowError(
			foundation.ErrorInvalidInput,
			ErrorCodeInputInvalid,
			false,
			errors.New("workspace analysis inspect execution binding is invalid"),
		)
	}

	input, err := conversationworkflow.DecodeWorkspaceAnalysisInput(execution.Input)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	questionContext, err := executor.dependencies.Context.LoadQuestionExecutionContext(ctx, conversationapplication.QuestionExecutionContextQuery{
		WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID,
		ConversationID: input.ConversationID, QuestionID: input.QuestionID, AnswerID: input.AnswerID,
		QuestionOrdinal: input.QuestionOrdinal, ContextHash: input.ContextHash,
	})
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if err := validateWorkspaceAnalysisQuestionContext(questionContext, execution, input); err != nil {
		return workflowapplication.ExecutionResult{}, err
	}

	analysisRun, err := executor.dependencies.Runs.LoadWorkspaceAnalysisRunForExecution(ctx, agentapplication.WorkspaceAnalysisRunExecutionQuery{
		WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID,
		ConversationID: input.ConversationID, QuestionID: input.QuestionID, AnswerID: input.AnswerID,
	})
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	if err := validateWorkspaceAnalysisInspectRun(analysisRun, execution, input); err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	deadlines, err := agentdomain.DeriveWorkspaceAnalysisV1Deadlines(analysisRun.Timeouts)
	if err != nil {
		return workflowapplication.ExecutionResult{}, workflowError(
			foundation.ErrorConsistencyViolation, ErrorCodeInputInvalid, false, err,
		)
	}
	nodeContext, cancel, err := workspaceAnalysisNodeContext(
		ctx, nodeStartedAt, analysisRun.DeadlineAt, deadlines.InspectWorkspaceDeadline(),
	)
	if err != nil {
		return workflowapplication.ExecutionResult{}, finalizeWorkspaceAnalysisPreAuthorizationDeadlineError(
			ctx, executor.dependencies.Finalizer, execution, input,
			questionContext.Answer.Version, analysisRun.ID, err,
		)
	}
	defer cancel()

	toolResult, err := executor.dependencies.Tools.ExecuteWorkspaceAnalysisTool(nodeContext, toolsapplication.ExecuteWorkspaceAnalysisToolCommand{
		OperationKey: agentdomain.WorkspaceAnalysisOperationKey{
			AnalysisRunID: analysisRun.ID,
			NodeKey:       agentdomain.WorkspaceAnalysisOperationNodeInspectWorkspace,
			Kind:          agentdomain.WorkspaceAnalysisOperationGitStatus,
			Ordinal:       1,
		},
		Tool: toolsapplication.ExecuteToolCommand{
			Identity: identity, Invocation: toolsdomain.InvocationSourceTrustedWorkflow, CallNo: 1,
			Request: toolsdomain.ToolRequestV1{
				SchemaVersion: toolsdomain.ToolRequestSchemaVersionV1,
				ToolName:      workspaceAnalysisGitStatusRef.Name,
				Arguments:     json.RawMessage(`{}`),
				Reason:        workspaceAnalysisInspectReason,
			},
		},
	})
	if err != nil {
		return workflowapplication.ExecutionResult{}, finalizeWorkspaceAnalysisToolTerminalError(
			nodeContext, executor.dependencies.Finalizer, execution, input,
			questionContext.Answer.Version, analysisRun.ID, err,
		)
	}
	return workspaceAnalysisInspectExecutionResult(execution, toolResult)
}

func workspaceAnalysisNode(definition workflowdomain.RegisteredDefinition, key string) (workflowdomain.NodeDefinition, bool) {
	for _, node := range definition.Graph.Nodes {
		if node.Key == key {
			return node, true
		}
	}
	return workflowdomain.NodeDefinition{}, false
}

func workspaceAnalysisToolIdentity(execution workflowapplication.ExecutionContext) toolsdomain.TrustedExecutionIdentity {
	return toolsdomain.TrustedExecutionIdentity{
		WorkspaceID: execution.WorkspaceID, DefinitionID: execution.DefinitionID,
		DefinitionVersion: execution.DefinitionVersion, DefinitionHash: execution.DefinitionHash,
		WorkflowRunID: execution.RunID, NodeKey: execution.NodeKey, NodeRunID: execution.NodeRunID,
		NodeAttemptID: execution.NodeAttemptID, LeaseOwner: execution.LeaseOwner, LeaseFence: int64(execution.AttemptNo),
	}
}

func validateWorkspaceAnalysisInspectRun(
	run agentdomain.WorkspaceAnalysisRun,
	execution workflowapplication.ExecutionContext,
	input conversationworkflow.WorkspaceAnalysisInput,
) error {
	if err := agentdomain.ValidateWorkspaceAnalysisRun(run); err != nil {
		return workflowError(foundation.ErrorConsistencyViolation, ErrorCodeInputInvalid, false, err)
	}
	if run.WorkspaceID != execution.WorkspaceID || run.WorkflowRunID != execution.RunID ||
		run.ConversationID != input.ConversationID || run.QuestionID != input.QuestionID || run.AnswerID != input.AnswerID ||
		run.DefinitionKey != conversationworkflow.WorkspaceAnalysisDefinitionKey ||
		run.DefinitionVersion != conversationworkflow.WorkspaceAnalysisDefinitionVersion || run.DefinitionHash != execution.DefinitionHash ||
		(run.Status != agentdomain.WorkspaceAnalysisRunQueued && run.Status != agentdomain.WorkspaceAnalysisRunRunning) {
		return workflowError(
			foundation.ErrorConsistencyViolation,
			ErrorCodeInputInvalid,
			false,
			errors.New("workspace analysis run differs from the active inspect execution"),
		)
	}
	return nil
}

func workspaceAnalysisInspectExecutionResult(
	execution workflowapplication.ExecutionContext,
	result toolsapplication.ToolExecutionResult,
) (workflowapplication.ExecutionResult, error) {
	call := result.Call
	outputHash := sha256.Sum256(result.Output)
	requestDocument, requestErr := workspaceAnalysisInspectRequestDocument()
	requestHash := sha256.Sum256(requestDocument)
	validAttempt := result.Replayed || call.NodeAttemptID == execution.NodeAttemptID
	if err := toolsdomain.ValidateToolCall(call); err != nil || requestErr != nil || !validAttempt ||
		call.Status != toolsdomain.CallSucceeded ||
		call.WorkspaceID != execution.WorkspaceID || call.WorkflowRunID != execution.RunID || call.NodeRunID != execution.NodeRunID ||
		call.CallNo != 1 || call.RequestedToolName != workspaceAnalysisGitStatusRef.Name || call.Tool == nil ||
		*call.Tool != workspaceAnalysisGitStatusRef || call.InputSchema == nil || *call.InputSchema != workspaceAnalysisGitStatusInputSchema ||
		call.OutputSchema == nil || *call.OutputSchema != workspaceAnalysisGitStatusOutputSchema ||
		call.Capability != capability.ReadLocal || call.SideEffectLevel != toolsdomain.SideEffectNone ||
		call.InvocationPolicy != toolsdomain.InvocationTrustedWorkflowOnly || call.IdempotencyKey != "" ||
		call.RequestHash != hex.EncodeToString(requestHash[:]) || call.RequestBytes != int64(len(requestDocument)) ||
		!validExecutionID(result.ResultReceiptID) ||
		result.ResultReceiptID == call.ID || !result.UntrustedData || call.ResponseBytes != int64(len(result.Output)) ||
		call.ResponseHash != hex.EncodeToString(outputHash[:]) {
		if err == nil {
			err = errors.New("workspace analysis git result binding is invalid")
		}
		return workflowapplication.ExecutionResult{}, workflowError(
			foundation.ErrorConsistencyViolation, ErrorCodeOutputInvalid, false, err,
		)
	}
	summary, err := conversationworkflow.DecodeWorkspaceAnalysisGitStatusSummary(result.Output)
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	output, err := conversationworkflow.EncodeWorkspaceAnalysisInspectOutput(conversationworkflow.WorkspaceAnalysisInspectOutput{
		SchemaVersion: conversationworkflow.WorkspaceAnalysisOutputSchemaVersion,
		ToolCallID:    call.ID, ToolReceiptID: result.ResultReceiptID, ToolReceiptHash: call.ResponseHash,
		GitStatus: summary,
	})
	if err != nil {
		return workflowapplication.ExecutionResult{}, err
	}
	return workflowapplication.ExecutionResult{Output: output}, nil
}

func workspaceAnalysisInspectRequestDocument() ([]byte, error) {
	request := struct {
		SchemaVersion int                   `json:"schema_version"`
		ToolName      string                `json:"tool_name"`
		ToolVersion   int64                 `json:"tool_version"`
		InputSchema   toolsdomain.SchemaRef `json:"input_schema"`
		Arguments     json.RawMessage       `json:"arguments"`
		Reason        string                `json:"reason"`
	}{
		SchemaVersion: toolsdomain.ToolRequestSchemaVersionV1,
		ToolName:      workspaceAnalysisGitStatusRef.Name,
		ToolVersion:   workspaceAnalysisGitStatusRef.Version,
		InputSchema:   workspaceAnalysisGitStatusInputSchema,
		Arguments:     json.RawMessage(`{}`),
		Reason:        workspaceAnalysisInspectReason,
	}
	return json.Marshal(request)
}

func workspaceAnalysisExecutionStartedAt(clock foundation.Clock) (time.Time, error) {
	if nilDependency(clock) {
		return time.Time{}, workflowError(
			foundation.ErrorDependencyUnavailable, ErrorCodeCapabilityUnavailable, false,
			errors.New("workspace analysis execution clock is unavailable"),
		)
	}
	startedAt := clock.Now().UTC()
	if startedAt.IsZero() {
		return time.Time{}, workflowError(
			foundation.ErrorConsistencyViolation, ErrorCodeInputInvalid, false,
			errors.New("workspace analysis execution clock returned zero time"),
		)
	}
	return startedAt, nil
}

func workspaceAnalysisNodeContext(
	parent context.Context,
	startedAt time.Time,
	runDeadline time.Time,
	nodeTimeout time.Duration,
) (context.Context, context.CancelFunc, error) {
	if parent == nil || startedAt.IsZero() || runDeadline.IsZero() || nodeTimeout <= 0 {
		return nil, nil, workflowError(
			foundation.ErrorConsistencyViolation, ErrorCodeInputInvalid, false,
			errors.New("workspace analysis node deadline inputs are invalid"),
		)
	}
	deadline := startedAt.Add(nodeTimeout)
	if runDeadline.Before(deadline) {
		deadline = runDeadline
	}
	if !deadline.After(startedAt) {
		return nil, nil, workflowError(
			foundation.ErrorNonRetryableFailure,
			string(agentdomain.WorkspaceAnalysisRunDeadlineExceeded),
			false,
			context.DeadlineExceeded,
		)
	}
	bounded, cancel := context.WithDeadline(parent, deadline)
	return bounded, cancel, nil
}

var _ workflowapplication.Executor = (*WorkspaceAnalysisInspectExecutor)(nil)
