//go:build integration

package migration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	agentpostgres "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/postgres"
	agentworkflow "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/workflow"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/tools/adapter/catalog"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func TestWorkspaceAnalysisToolExecutionAuthorizesPersistsAndReplaysWithoutReexecution(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()

	prepareWorkspaceAnalysisToolAuthorizationRuntime(t, ctx, pool)
	runtime := openMigrationRuntimePool(t, ctx, pool)
	defer runtime.Close()
	pool = runtime.DB()
	repository := newWorkspaceAnalysisMigrationToolsRepository(t, runtime)
	contract := workspaceAnalysisGitExecutionContract(t)
	executor := &workspaceAnalysisGitExecutionStub{}
	registry := toolsapplication.NewExecutionRegistry()
	if err := registry.RegisterContract(contract); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterExecutor(contract.Definition.Ref, executor); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	service, err := toolsapplication.NewExecutionService(
		registry,
		repository,
		repository,
		foundation.NewUUIDGenerator(nil),
		foundation.SystemClock{},
	)
	if err != nil {
		t.Fatal(err)
	}

	command := workspaceAnalysisGitExecutionCommand()
	first, err := service.ExecuteWorkspaceAnalysisTool(ctx, command)
	if err != nil {
		t.Fatalf("first ExecuteWorkspaceAnalysisTool: %v", err)
	}
	if first.Replayed || first.Call.Status != toolsdomain.CallSucceeded || executor.calls != 1 ||
		first.ResultReceiptID == "" || string(first.Output) != workspaceAnalysisGitReceiptOutput {
		t.Fatalf("first result=%+v calls=%d output=%s", first.Call, executor.calls, first.Output)
	}
	assertWorkspaceAnalysisToolAuthorizationState(
		t, ctx, pool, first.Call.ID,
		"SUCCEEDED", "SETTLED", "SUCCEEDED", "", 0, 1, 1,
	)

	second, err := service.ExecuteWorkspaceAnalysisTool(ctx, command)
	if err != nil {
		t.Fatalf("replayed ExecuteWorkspaceAnalysisTool: %v", err)
	}
	if !second.Replayed || second.Call.ID != first.Call.ID || executor.calls != 1 ||
		second.ResultReceiptID != first.ResultReceiptID || string(second.Output) != workspaceAnalysisGitReceiptOutput {
		t.Fatalf("replay result=%+v calls=%d output=%s", second.Call, executor.calls, second.Output)
	}
}

func TestWorkspaceAnalysisInspectNodeRecoversCanonicalReceiptAcrossReplacementAttempt(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()

	prepareWorkspaceAnalysisToolAuthorizationRuntime(t, ctx, pool)
	runtime := openMigrationRuntimePool(t, ctx, pool)
	defer runtime.Close()
	pool = runtime.DB()
	toolRepository := newWorkspaceAnalysisMigrationToolsRepository(t, runtime)
	agentRepository, err := agentpostgres.NewGORMRepository(runtime)
	if err != nil {
		t.Fatal(err)
	}
	contract := workspaceAnalysisGitExecutionContract(t)
	gitExecutor := &workspaceAnalysisGitExecutionStub{}
	registry := toolsapplication.NewExecutionRegistry()
	if err := registry.RegisterContract(contract); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterExecutor(contract.Definition.Ref, gitExecutor); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	toolService, err := toolsapplication.NewExecutionService(
		registry, toolRepository, toolRepository, foundation.NewUUIDGenerator(nil), foundation.SystemClock{},
	)
	if err != nil {
		t.Fatal(err)
	}
	contextLoader := &workspaceAnalysisInspectContextStub{t: t}
	finalizer := &workspaceAnalysisToolExecutionFinalizerStub{}
	nodeExecutor, err := agentworkflow.NewWorkspaceAnalysisInspectExecutor(agentworkflow.WorkspaceAnalysisInspectExecutorDependencies{
		Context: contextLoader, Runs: agentRepository, Tools: toolService, Finalizer: finalizer,
	})
	if err != nil {
		t.Fatal(err)
	}

	execution := workspaceAnalysisInspectExecutionContext(t, "83000000-0000-4000-8000-000000000021", "wa-worker", 2, 1, 1, 0)
	first, err := nodeExecutor.Execute(ctx, execution)
	if err != nil {
		t.Fatalf("first inspect Execute: %v", err)
	}
	firstOutput, err := conversationworkflow.DecodeWorkspaceAnalysisInspectOutput(first.Output)
	if err != nil {
		t.Fatal(err)
	}
	if gitExecutor.calls != 1 || firstOutput.ToolCallID == "" || firstOutput.ToolReceiptID == "" ||
		firstOutput.ToolReceiptHash == "" || firstOutput.GitStatus.Branch != "main" {
		t.Fatalf("output=%+v git calls=%d", firstOutput, gitExecutor.calls)
	}

	advanceWorkspaceAnalysisGitAuthorizationAttempt(t, ctx, pool)
	replacement := workspaceAnalysisInspectExecutionContext(t, "83000000-0000-4000-8000-000000000136", "wa-worker", 4, 2, 2, 1)
	second, err := nodeExecutor.Execute(ctx, replacement)
	if err != nil {
		t.Fatalf("replacement inspect Execute: %v", err)
	}
	if string(second.Output) != string(first.Output) || gitExecutor.calls != 1 || contextLoader.calls != 2 || finalizer.calls != 0 {
		t.Fatalf("first=%s second=%s git calls=%d context calls=%d finalizer calls=%d", first.Output, second.Output, gitExecutor.calls, contextLoader.calls, finalizer.calls)
	}
}

type workspaceAnalysisToolExecutionFinalizerStub struct {
	calls int
}

func (stub *workspaceAnalysisToolExecutionFinalizerStub) LookupPublication(
	context.Context,
	conversationapplication.WorkspaceAnalysisPublicationLookup,
) (conversationworkflow.WorkspaceAnalysisPublicationOutput, bool, error) {
	stub.calls++
	return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, errors.New("unexpected workspace analysis publication lookup")
}

func (stub *workspaceAnalysisToolExecutionFinalizerStub) FinalizeSuccess(
	context.Context,
	conversationapplication.FinalizeWorkspaceAnalysisSuccessCommand,
) (conversationworkflow.WorkspaceAnalysisPublicationOutput, bool, error) {
	stub.calls++
	return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, errors.New("unexpected workspace analysis success finalization")
}

func (stub *workspaceAnalysisToolExecutionFinalizerStub) FinalizeTermination(
	context.Context,
	conversationapplication.FinalizeWorkspaceAnalysisTerminationCommand,
) (conversationworkflow.WorkspaceAnalysisPublicationOutput, bool, error) {
	stub.calls++
	return conversationworkflow.WorkspaceAnalysisPublicationOutput{}, false, errors.New("unexpected workspace analysis termination finalization")
}

func workspaceAnalysisInspectExecutionContext(
	t *testing.T,
	attemptID foundation.ID,
	leaseOwner string,
	nodeVersion int64,
	attemptNo int,
	dispatchNo int,
	retryNo int,
) workflowapplication.ExecutionContext {
	t.Helper()
	definition := conversationworkflow.RegisteredWorkspaceAnalysisDefinition()
	nodeKind := ""
	for _, node := range definition.Graph.Nodes {
		if node.Key == conversationworkflow.WorkspaceAnalysisNodeInspectWorkspace {
			nodeKind = node.Kind
			break
		}
	}
	if nodeKind == "" {
		t.Fatal("workspace analysis inspect node is missing")
	}
	input, err := conversationworkflow.EncodeWorkspaceAnalysisInput(conversationworkflow.WorkspaceAnalysisInput{
		SchemaVersion:   conversationworkflow.WorkspaceAnalysisInputSchemaVersion,
		ConversationID:  "83000000-0000-4000-8000-000000000002",
		QuestionID:      "83000000-0000-4000-8000-000000000010",
		AnswerID:        "83000000-0000-4000-8000-000000000013",
		QuestionOrdinal: 2, ContextHash: workspaceAnalysisToolExecutionEmptyContextHash(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	return workflowapplication.ExecutionContext{
		WorkspaceID:       "83000000-0000-4000-8000-000000000001",
		DefinitionID:      "83000000-0000-4000-8000-000000000011",
		DefinitionVersion: definition.Version, DefinitionHash: definition.GraphHash,
		RunID:     "83000000-0000-4000-8000-000000000012",
		NodeKey:   conversationworkflow.WorkspaceAnalysisNodeInspectWorkspace,
		NodeRunID: "83000000-0000-4000-8000-000000000020", NodeAttemptID: attemptID, NodeKind: nodeKind,
		NodeVersion: nodeVersion, InputSchemaVersion: conversationworkflow.WorkspaceAnalysisInputSchemaVersion,
		AttemptNo: attemptNo, DispatchNo: dispatchNo, RetryNo: retryNo, LeaseOwner: leaseOwner, Input: input,
	}
}

type workspaceAnalysisInspectContextStub struct {
	t     *testing.T
	calls int
}

func (stub *workspaceAnalysisInspectContextStub) LoadQuestionExecutionContext(
	_ context.Context,
	_ conversationapplication.QuestionExecutionContextQuery,
) (conversationapplication.QuestionExecutionContext, error) {
	stub.t.Helper()
	stub.calls++
	request, err := conversationdomain.CanonicalizeQuestionRequest(conversationdomain.QuestionRequest{
		WorkspaceID:    "83000000-0000-4000-8000-000000000001",
		ConversationID: "83000000-0000-4000-8000-000000000002",
		Mode:           conversationdomain.QuestionModeWorkspaceAnalysis, QuestionText: "analyze workspace",
		Scope:       conversationdomain.QuestionScope{RetrievalMode: retrievaldomain.SearchModeHybrid},
		AnswerDepth: conversationdomain.AnswerDepthDetailed, OutputFormat: conversationdomain.OutputFormatMarkdown,
	})
	if err != nil {
		stub.t.Fatal(err)
	}
	requestHash, err := conversationdomain.ComputeQuestionRequestHash(request)
	if err != nil {
		stub.t.Fatal(err)
	}
	contextHash := workspaceAnalysisToolExecutionEmptyContextHash(stub.t)
	now := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	return conversationapplication.QuestionExecutionContext{
		Question: conversationdomain.Question{
			ID: "83000000-0000-4000-8000-000000000010", Request: request, Ordinal: 2,
			ContextThroughOrdinal: 0, ContextHash: contextHash, RequestHash: requestHash, CreatedAt: now,
		},
		Answer: conversationdomain.Answer{
			ID: "83000000-0000-4000-8000-000000000013", WorkspaceID: "83000000-0000-4000-8000-000000000001",
			ConversationID: "83000000-0000-4000-8000-000000000002", QuestionID: "83000000-0000-4000-8000-000000000010",
			WorkflowRunID: "83000000-0000-4000-8000-000000000012", PublicationStatus: conversationdomain.AnswerPublicationPending,
			Version: 1, CreatedAt: now, UpdatedAt: now,
		},
		History: nil,
	}, nil
}

func workspaceAnalysisToolExecutionEmptyContextHash(t *testing.T) string {
	t.Helper()
	hash, _, _, err := conversationdomain.ComputeContextHash(nil)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

type workspaceAnalysisGitExecutionStub struct {
	calls int
}

func (executor *workspaceAnalysisGitExecutionStub) Execute(
	_ context.Context,
	request toolsapplication.ExecutorRequest,
) (toolsapplication.ExecutorResult, error) {
	executor.calls++
	if request.Tool != (toolsdomain.ToolRef{Name: "ReadGitStatus", Version: 2}) ||
		string(request.Arguments) != `{}` || request.Identity.NodeKey != "inspect_workspace" {
		return toolsapplication.ExecutorResult{}, foundation.NewError(
			foundation.ErrorConsistencyViolation,
			"WORKSPACE_ANALYSIS_GIT_EXECUTION_BINDING_INVALID",
			false,
			errors.New("workspace analysis Git executor binding differs"),
		)
	}
	return toolsapplication.ExecutorResult{Output: json.RawMessage(workspaceAnalysisGitReceiptOutput)}, nil
}

func workspaceAnalysisGitExecutionContract(t *testing.T) toolsapplication.Contract {
	t.Helper()
	contracts, err := catalog.Contracts()
	if err != nil {
		t.Fatal(err)
	}
	for _, contract := range contracts {
		if contract.Definition.Ref == (toolsdomain.ToolRef{Name: "ReadGitStatus", Version: 2}) {
			return contract
		}
	}
	t.Fatal("ReadGitStatus@2 contract is missing")
	return toolsapplication.Contract{}
}

func workspaceAnalysisGitExecutionCommand() toolsapplication.ExecuteWorkspaceAnalysisToolCommand {
	return toolsapplication.ExecuteWorkspaceAnalysisToolCommand{
		OperationKey: agentdomain.WorkspaceAnalysisOperationKey{
			AnalysisRunID: "83000000-0000-4000-8000-000000000014",
			NodeKey:       agentdomain.WorkspaceAnalysisOperationNodeInspectWorkspace,
			Kind:          agentdomain.WorkspaceAnalysisOperationGitStatus,
			Ordinal:       1,
		},
		Tool: toolsapplication.ExecuteToolCommand{
			Identity: toolsdomain.TrustedExecutionIdentity{
				WorkspaceID: "83000000-0000-4000-8000-000000000001", DefinitionID: "83000000-0000-4000-8000-000000000011",
				DefinitionVersion: 1, DefinitionHash: conversationworkflow.RegisteredWorkspaceAnalysisDefinition().GraphHash,
				WorkflowRunID: "83000000-0000-4000-8000-000000000012", NodeKey: "inspect_workspace",
				NodeRunID: "83000000-0000-4000-8000-000000000020", NodeAttemptID: "83000000-0000-4000-8000-000000000021",
				LeaseOwner: "wa-worker", LeaseFence: 1,
			},
			Invocation: toolsdomain.InvocationSourceTrustedWorkflow,
			Request: toolsdomain.ToolRequestV1{
				SchemaVersion: toolsdomain.ToolRequestSchemaVersionV1,
				ToolName:      "ReadGitStatus",
				Arguments:     json.RawMessage(`{}`),
				Reason:        "inspect workspace status",
			},
			CallNo: 1,
		},
	}
}
