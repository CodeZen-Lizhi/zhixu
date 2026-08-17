package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func TestWorkspaceAnalysisInspectExecutorBuildsFixedToolCommandAndSafeOutput(t *testing.T) {
	execution, input := workspaceAnalysisInspectExecutionFixture(t)
	contextLoader := &workspaceAnalysisInspectContextFake{result: workspaceAnalysisInspectQuestionContext(t, execution, input)}
	runLoader := &workspaceAnalysisInspectRunFake{run: workspaceAnalysisInspectRunFixture(t, execution, input)}
	tool := &workspaceAnalysisInspectToolFake{result: workspaceAnalysisInspectToolResult(execution)}
	executor, err := NewWorkspaceAnalysisInspectExecutor(WorkspaceAnalysisInspectExecutorDependencies{
		Context: contextLoader, Runs: runLoader, Tools: tool, Finalizer: &workspaceAnalysisFinalizerFake{},
		Clock: foundation.FixedClock{Value: workspaceAnalysisInspectNow()},
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := executor.Execute(context.Background(), execution)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if contextLoader.calls != 1 || runLoader.calls != 1 || tool.calls != 1 {
		t.Fatalf("context=%d run=%d tool=%d", contextLoader.calls, runLoader.calls, tool.calls)
	}
	deadlines, err := agentdomain.DeriveWorkspaceAnalysisV1Deadlines(runLoader.run.Timeouts)
	if err != nil {
		t.Fatal(err)
	}
	if !tool.hasDeadline || !tool.deadline.Equal(workspaceAnalysisInspectNow().Add(deadlines.InspectWorkspaceDeadline())) {
		t.Fatalf("tool deadline=%s present=%t", tool.deadline, tool.hasDeadline)
	}
	command := tool.command
	if command.OperationKey.AnalysisRunID != runLoader.run.ID ||
		command.OperationKey.NodeKey != agentdomain.WorkspaceAnalysisOperationNodeInspectWorkspace ||
		command.OperationKey.Kind != agentdomain.WorkspaceAnalysisOperationGitStatus || command.OperationKey.Ordinal != 1 ||
		command.Tool.Invocation != toolsdomain.InvocationSourceTrustedWorkflow || command.Tool.CallNo != 1 ||
		command.Tool.IdempotencyKey != "" || command.Tool.Request.ToolName != "ReadGitStatus" ||
		string(command.Tool.Request.Arguments) != `{}` || command.Tool.Request.Reason != workspaceAnalysisInspectReason ||
		command.Tool.Identity.NodeAttemptID != execution.NodeAttemptID || command.Tool.Identity.LeaseFence != int64(execution.AttemptNo) {
		t.Fatalf("tool command = %+v", command)
	}
	output, err := conversationworkflow.DecodeWorkspaceAnalysisInspectOutput(result.Output)
	if err != nil {
		t.Fatalf("DecodeWorkspaceAnalysisInspectOutput: %v", err)
	}
	if output.ToolCallID != tool.result.Call.ID || output.ToolReceiptID != tool.result.ResultReceiptID ||
		output.ToolReceiptHash != tool.result.Call.ResponseHash || output.GitStatus.Branch != "main" ||
		!output.GitStatus.Clean || output.GitStatus.Head != strings.Repeat("b", 40) {
		t.Fatalf("output = %+v", output)
	}
}

func TestWorkspaceAnalysisInspectExecutorReplacementReplayProducesSameNodeOutput(t *testing.T) {
	firstExecution, input := workspaceAnalysisInspectExecutionFixture(t)
	firstResult := workspaceAnalysisInspectToolResult(firstExecution)
	contextLoader := &workspaceAnalysisInspectContextFake{result: workspaceAnalysisInspectQuestionContext(t, firstExecution, input)}
	runLoader := &workspaceAnalysisInspectRunFake{run: workspaceAnalysisInspectRunFixture(t, firstExecution, input)}
	tool := &workspaceAnalysisInspectToolFake{result: firstResult}
	executor, err := NewWorkspaceAnalysisInspectExecutor(WorkspaceAnalysisInspectExecutorDependencies{
		Context: contextLoader, Runs: runLoader, Tools: tool, Finalizer: &workspaceAnalysisFinalizerFake{},
		Clock: foundation.FixedClock{Value: workspaceAnalysisInspectNow()},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := executor.Execute(context.Background(), firstExecution)
	if err != nil {
		t.Fatal(err)
	}

	replacement := firstExecution
	replacement.NodeAttemptID = workspaceAnalysisInspectID(10)
	replacement.AttemptNo++
	replacement.RetryNo++
	replacement.NodeVersion++
	replacement.LeaseOwner = "worker-replacement"
	tool.result.Replayed = true
	second, err := executor.Execute(context.Background(), replacement)
	if err != nil {
		t.Fatal(err)
	}
	if string(first.Output) != string(second.Output) || tool.calls != 2 || tool.command.Tool.Identity.NodeAttemptID != replacement.NodeAttemptID {
		t.Fatalf("first=%s second=%s calls=%d command=%+v", first.Output, second.Output, tool.calls, tool.command)
	}
}

func TestWorkspaceAnalysisInspectExecutorRejectsExecutionBindingBeforeReads(t *testing.T) {
	valid, _ := workspaceAnalysisInspectExecutionFixture(t)
	tests := []struct {
		name   string
		mutate func(*workflowapplication.ExecutionContext)
	}{
		{name: "definition hash", mutate: func(value *workflowapplication.ExecutionContext) { value.DefinitionHash = strings.Repeat("c", 64) }},
		{name: "node key", mutate: func(value *workflowapplication.ExecutionContext) { value.NodeKey = "retrieve_evidence" }},
		{name: "node kind", mutate: func(value *workflowapplication.ExecutionContext) {
			value.NodeKind = "agent.workspace-analysis.retrieve_evidence"
		}},
		{name: "schema", mutate: func(value *workflowapplication.ExecutionContext) { value.InputSchemaVersion++ }},
		{name: "attempt", mutate: func(value *workflowapplication.ExecutionContext) { value.NodeAttemptID = "bad" }},
		{name: "lease", mutate: func(value *workflowapplication.ExecutionContext) { value.LeaseOwner = "" }},
		{name: "node version", mutate: func(value *workflowapplication.ExecutionContext) { value.NodeVersion = 0 }},
		{name: "attempt fence", mutate: func(value *workflowapplication.ExecutionContext) { value.AttemptNo = 0 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			execution := valid
			test.mutate(&execution)
			_, input := workspaceAnalysisInspectExecutionFixture(t)
			contextLoader := &workspaceAnalysisInspectContextFake{result: workspaceAnalysisInspectQuestionContext(t, valid, input)}
			runLoader := &workspaceAnalysisInspectRunFake{}
			tool := &workspaceAnalysisInspectToolFake{}
			executor, err := NewWorkspaceAnalysisInspectExecutor(WorkspaceAnalysisInspectExecutorDependencies{
				Context: contextLoader, Runs: runLoader, Tools: tool, Finalizer: &workspaceAnalysisFinalizerFake{},
				Clock: foundation.FixedClock{Value: workspaceAnalysisInspectNow()},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = executor.Execute(context.Background(), execution)
			if codeOf(err) != ErrorCodeInputInvalid || contextLoader.calls != 0 || runLoader.calls != 0 || tool.calls != 0 {
				t.Fatalf("error=%v context=%d run=%d tool=%d", err, contextLoader.calls, runLoader.calls, tool.calls)
			}
		})
	}
}

func TestWorkspaceAnalysisInspectExecutorRejectsModeRunAndReceiptDrift(t *testing.T) {
	execution, input := workspaceAnalysisInspectExecutionFixture(t)
	tests := []struct {
		name   string
		mutate func(*conversationapplication.QuestionExecutionContext, *agentdomain.WorkspaceAnalysisRun, *toolsapplication.ToolExecutionResult)
	}{
		{name: "mode", mutate: func(question *conversationapplication.QuestionExecutionContext, _ *agentdomain.WorkspaceAnalysisRun, _ *toolsapplication.ToolExecutionResult) {
			question.Question.Request.Mode = conversationdomain.QuestionModeRAG
		}},
		{name: "answer binding", mutate: func(question *conversationapplication.QuestionExecutionContext, _ *agentdomain.WorkspaceAnalysisRun, _ *toolsapplication.ToolExecutionResult) {
			question.Answer.QuestionID = workspaceAnalysisInspectID(99)
		}},
		{name: "run definition", mutate: func(_ *conversationapplication.QuestionExecutionContext, run *agentdomain.WorkspaceAnalysisRun, _ *toolsapplication.ToolExecutionResult) {
			run.DefinitionHash = strings.Repeat("c", 64)
		}},
		{name: "receipt missing", mutate: func(_ *conversationapplication.QuestionExecutionContext, _ *agentdomain.WorkspaceAnalysisRun, result *toolsapplication.ToolExecutionResult) {
			result.ResultReceiptID = ""
		}},
		{name: "request hash", mutate: func(_ *conversationapplication.QuestionExecutionContext, _ *agentdomain.WorkspaceAnalysisRun, result *toolsapplication.ToolExecutionResult) {
			result.Call.RequestHash = strings.Repeat("f", 64)
		}},
		{name: "old attempt without replay", mutate: func(_ *conversationapplication.QuestionExecutionContext, _ *agentdomain.WorkspaceAnalysisRun, result *toolsapplication.ToolExecutionResult) {
			result.Call.NodeAttemptID = workspaceAnalysisInspectID(98)
		}},
		{name: "unsafe output", mutate: func(_ *conversationapplication.QuestionExecutionContext, _ *agentdomain.WorkspaceAnalysisRun, result *toolsapplication.ToolExecutionResult) {
			result.Output = json.RawMessage(`{"branch":"main","clean":true,"conflict_count":0,"head":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","object_format":"sha1","staged_count":0,"unstaged_count":0,"untracked_count":0,"path":"secret"}`)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			question := workspaceAnalysisInspectQuestionContext(t, execution, input)
			run := workspaceAnalysisInspectRunFixture(t, execution, input)
			toolResult := workspaceAnalysisInspectToolResult(execution)
			test.mutate(&question, &run, &toolResult)
			contextLoader := &workspaceAnalysisInspectContextFake{result: question}
			runLoader := &workspaceAnalysisInspectRunFake{run: run}
			tool := &workspaceAnalysisInspectToolFake{result: toolResult}
			executor, err := NewWorkspaceAnalysisInspectExecutor(WorkspaceAnalysisInspectExecutorDependencies{
				Context: contextLoader, Runs: runLoader, Tools: tool, Finalizer: &workspaceAnalysisFinalizerFake{},
				Clock: foundation.FixedClock{Value: workspaceAnalysisInspectNow()},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := executor.Execute(context.Background(), execution); err == nil {
				t.Fatal("Execute accepted drift")
			}
		})
	}
}

type workspaceAnalysisInspectContextFake struct {
	result conversationapplication.QuestionExecutionContext
	query  conversationapplication.QuestionExecutionContextQuery
	calls  int
}

func (fake *workspaceAnalysisInspectContextFake) LoadQuestionExecutionContext(_ context.Context, query conversationapplication.QuestionExecutionContextQuery) (conversationapplication.QuestionExecutionContext, error) {
	fake.calls++
	fake.query = query
	return fake.result, nil
}

type workspaceAnalysisInspectRunFake struct {
	run   agentdomain.WorkspaceAnalysisRun
	query agentapplication.WorkspaceAnalysisRunExecutionQuery
	calls int
}

func (fake *workspaceAnalysisInspectRunFake) LoadWorkspaceAnalysisRunForExecution(_ context.Context, query agentapplication.WorkspaceAnalysisRunExecutionQuery) (agentdomain.WorkspaceAnalysisRun, error) {
	fake.calls++
	fake.query = query
	return fake.run, nil
}

type workspaceAnalysisInspectToolFake struct {
	result      toolsapplication.ToolExecutionResult
	command     toolsapplication.ExecuteWorkspaceAnalysisToolCommand
	err         error
	deadline    time.Time
	hasDeadline bool
	calls       int
}

func (fake *workspaceAnalysisInspectToolFake) ExecuteWorkspaceAnalysisTool(ctx context.Context, command toolsapplication.ExecuteWorkspaceAnalysisToolCommand) (toolsapplication.ToolExecutionResult, error) {
	fake.calls++
	fake.command = command
	fake.deadline, fake.hasDeadline = ctx.Deadline()
	if fake.err != nil {
		return toolsapplication.ToolExecutionResult{}, fake.err
	}
	return fake.result, nil
}

func workspaceAnalysisInspectExecutionFixture(t *testing.T) (workflowapplication.ExecutionContext, conversationworkflow.WorkspaceAnalysisInput) {
	t.Helper()
	contextHash, _, _, err := conversationdomain.ComputeContextHash(nil)
	if err != nil {
		t.Fatal(err)
	}
	input := conversationworkflow.WorkspaceAnalysisInput{
		SchemaVersion:  conversationworkflow.WorkspaceAnalysisInputSchemaVersion,
		ConversationID: workspaceAnalysisInspectID(3), QuestionID: workspaceAnalysisInspectID(4),
		AnswerID: workspaceAnalysisInspectID(5), QuestionOrdinal: 1, ContextHash: contextHash,
	}
	raw, err := conversationworkflow.EncodeWorkspaceAnalysisInput(input)
	if err != nil {
		t.Fatal(err)
	}
	definition := conversationworkflow.RegisteredWorkspaceAnalysisDefinition()
	node, found := workspaceAnalysisNode(definition, conversationworkflow.WorkspaceAnalysisNodeInspectWorkspace)
	if !found {
		t.Fatal("inspect node missing")
	}
	return workflowapplication.ExecutionContext{
		WorkspaceID: workspaceAnalysisInspectID(1), DefinitionID: workspaceAnalysisInspectID(6),
		DefinitionVersion: definition.Version, DefinitionHash: definition.GraphHash, RunID: workspaceAnalysisInspectID(2),
		NodeKey: node.Key, NodeRunID: workspaceAnalysisInspectID(7), NodeAttemptID: workspaceAnalysisInspectID(8), NodeKind: node.Kind,
		NodeVersion: 2, InputSchemaVersion: node.InputSchemaVersion, AttemptNo: 1, DispatchNo: 1,
		LeaseOwner: "worker-inspect", Input: raw,
	}, input
}

func workspaceAnalysisInspectQuestionContext(
	t *testing.T,
	execution workflowapplication.ExecutionContext,
	input conversationworkflow.WorkspaceAnalysisInput,
) conversationapplication.QuestionExecutionContext {
	t.Helper()
	request, err := conversationdomain.CanonicalizeQuestionRequest(conversationdomain.QuestionRequest{
		WorkspaceID: execution.WorkspaceID, ConversationID: input.ConversationID,
		Mode: conversationdomain.QuestionModeWorkspaceAnalysis, QuestionText: "Explain the workspace architecture",
		Scope:       conversationdomain.QuestionScope{RetrievalMode: retrievaldomain.SearchModeHybrid},
		AnswerDepth: conversationdomain.AnswerDepthDetailed, OutputFormat: conversationdomain.OutputFormatOutline,
	})
	if err != nil {
		t.Fatal(err)
	}
	requestHash, err := conversationdomain.ComputeQuestionRequestHash(request)
	if err != nil {
		t.Fatal(err)
	}
	_, through, _, err := conversationdomain.ComputeContextHash(nil)
	if err != nil {
		t.Fatal(err)
	}
	now := workspaceAnalysisInspectNow()
	return conversationapplication.QuestionExecutionContext{
		Question: conversationdomain.Question{
			ID: input.QuestionID, Request: request, Ordinal: input.QuestionOrdinal,
			ContextThroughOrdinal: through, ContextHash: input.ContextHash, RequestHash: requestHash, CreatedAt: now,
		},
		Answer: conversationdomain.Answer{
			ID: input.AnswerID, WorkspaceID: execution.WorkspaceID, ConversationID: input.ConversationID,
			QuestionID: input.QuestionID, WorkflowRunID: execution.RunID,
			PublicationStatus: conversationdomain.AnswerPublicationPending, Version: 1, CreatedAt: now, UpdatedAt: now,
		},
		History: nil,
	}
}

func workspaceAnalysisInspectRunFixture(t *testing.T, execution workflowapplication.ExecutionContext, input conversationworkflow.WorkspaceAnalysisInput) agentdomain.WorkspaceAnalysisRun {
	t.Helper()
	timeouts := agentdomain.WorkspaceAnalysisV1Timeouts{
		PlanModelTimeout: 2 * time.Second, SynthesisModelTimeout: 3 * time.Second, ReviewModelTimeout: 2 * time.Second,
		GitToolTimeout: time.Second, SearchToolTimeout: time.Second, SourceReadToolTimeout: time.Second,
		ValidateCitationToolTimeout: time.Second,
	}
	deadlines, err := agentdomain.DeriveWorkspaceAnalysisV1Deadlines(timeouts)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := workspaceAnalysisInspectNow()
	return agentdomain.WorkspaceAnalysisRun{
		ID: workspaceAnalysisInspectID(9), WorkspaceID: execution.WorkspaceID, ConversationID: input.ConversationID,
		QuestionID: input.QuestionID, AnswerID: input.AnswerID, WorkflowRunID: execution.RunID,
		DefinitionKey:     conversationworkflow.WorkspaceAnalysisDefinitionKey,
		DefinitionVersion: conversationworkflow.WorkspaceAnalysisDefinitionVersion, DefinitionHash: execution.DefinitionHash,
		ToolCatalogHash: strings.Repeat("b", 64), PolicyVersion: agentdomain.WorkspaceAnalysisPolicyVersionV1,
		ConfigRevision: 1, DeadlineAt: createdAt.Add(deadlines.RunDeadline()), Timeouts: timeouts,
		Limits: agentdomain.WorkspaceAnalysisBudgetLimits{
			Nodes: agentdomain.WorkspaceAnalysisV1MaxNodes, ToolConcurrency: agentdomain.WorkspaceAnalysisV1MaxToolConcurrency,
			Amount: agentdomain.WorkspaceAnalysisBudgetAmount{
				ModelCalls: agentdomain.WorkspaceAnalysisV1MaxModelCalls, ToolCalls: agentdomain.WorkspaceAnalysisV1MaxToolCalls,
				SourceReads: agentdomain.WorkspaceAnalysisV1MaxSourceReads, InputTokens: agentdomain.WorkspaceAnalysisV1MaxRunInputTokens,
				OutputTokens: agentdomain.WorkspaceAnalysisV1MaxRunOutputTokens,
			},
		},
		Status: agentdomain.WorkspaceAnalysisRunQueued, Version: 1, CreatedAt: createdAt, UpdatedAt: createdAt,
	}
}

func workspaceAnalysisInspectToolResult(execution workflowapplication.ExecutionContext) toolsapplication.ToolExecutionResult {
	output := json.RawMessage(`{"branch":"main","clean":true,"conflict_count":0,"head":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","object_format":"sha1","staged_count":0,"unstaged_count":0,"untracked_count":0}`)
	responseDigest := sha256.Sum256(output)
	completedAt := workspaceAnalysisInspectNow().Add(time.Second)
	tool := workspaceAnalysisGitStatusRef
	inputSchema := workspaceAnalysisGitStatusInputSchema
	outputSchema := workspaceAnalysisGitStatusOutputSchema
	requestDocument, err := workspaceAnalysisInspectRequestDocument()
	if err != nil {
		panic(err)
	}
	requestDigest := sha256.Sum256(requestDocument)
	return toolsapplication.ToolExecutionResult{
		Call: toolsdomain.ToolCall{
			ID: workspaceAnalysisInspectID(11), WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID,
			NodeRunID: execution.NodeRunID, NodeAttemptID: execution.NodeAttemptID, CallNo: 1,
			RequestedToolName: tool.Name, Tool: &tool, DefinitionHash: strings.Repeat("c", 64),
			InputSchema: &inputSchema, OutputSchema: &outputSchema, Capability: capability.ReadLocal,
			SideEffectLevel: toolsdomain.SideEffectNone, InvocationPolicy: toolsdomain.InvocationTrustedWorkflowOnly,
			RequestHash: hex.EncodeToString(requestDigest[:]), RequestBytes: int64(len(requestDocument)), RequestSummary: json.RawMessage(`{}`),
			ResponseHash: hex.EncodeToString(responseDigest[:]), ResponseBytes: int64(len(output)), ResponseSummary: json.RawMessage(`{}`),
			Status: toolsdomain.CallSucceeded, Version: 2, StartedAt: completedAt.Add(-time.Second),
			CompletedAt: &completedAt, DurationMillis: 1000,
		},
		ResultReceiptID: workspaceAnalysisInspectID(12), Output: output, UntrustedData: true,
	}
}

func workspaceAnalysisInspectNow() time.Time {
	return time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
}

func workspaceAnalysisInspectID(value int) foundation.ID {
	return foundation.ID("92000000-0000-4000-8000-" + leftPadWorkspaceAnalysisInspect(value))
}

func leftPadWorkspaceAnalysisInspect(value int) string {
	return strings.Repeat("0", 9) + string([]byte{
		byte('0' + value/100%10), byte('0' + value/10%10), byte('0' + value%10),
	})
}

var (
	_ conversationapplication.QuestionExecutionContextLoader = (*workspaceAnalysisInspectContextFake)(nil)
	_ agentapplication.WorkspaceAnalysisRunLoader            = (*workspaceAnalysisInspectRunFake)(nil)
	_ workspaceAnalysisToolExecutor                          = (*workspaceAnalysisInspectToolFake)(nil)
)
