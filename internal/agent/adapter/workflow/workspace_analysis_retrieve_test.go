package workflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	retrievalapplication "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func TestWorkspaceAnalysisRetrieveExecutorBuildsExactPlanSearchAndSafeOutput(t *testing.T) {
	fixture := newWorkspaceAnalysisRetrieveFixture(t, false)
	executor := fixture.executor(t)

	result, err := executor.Execute(context.Background(), fixture.execution)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if fixture.inputs.calls != 1 || fixture.stages.calls != 1 || fixture.context.calls != 1 ||
		fixture.runs.calls != 1 || fixture.retrieval.calls != 1 || fixture.planner.calls != 1 || fixture.tools.calls != 1 ||
		fixture.planner.checkpointCalls != 1 || fixture.receipts.calls != 1 {
		t.Fatalf("calls inputs=%d stages=%d context=%d runs=%d retrieval=%d checkpoints=%d planner=%d tools=%d receipts=%d",
			fixture.inputs.calls, fixture.stages.calls, fixture.context.calls, fixture.runs.calls,
			fixture.retrieval.calls, fixture.planner.checkpointCalls, fixture.planner.calls, fixture.tools.calls, fixture.receipts.calls)
	}
	if fixture.stages.nodeKey != conversationworkflow.WorkspaceAnalysisNodeInspectWorkspace ||
		fixture.inputs.workspaceID != fixture.execution.WorkspaceID || fixture.inputs.runID != fixture.execution.RunID {
		t.Fatalf("workflow queries inputs=%+v stage=%+v", fixture.inputs, fixture.stages)
	}
	if fixture.planner.checkpointQuery != (agentapplication.WorkspaceAnalysisRetrievalPlanCheckpointQuery{
		WorkspaceID: fixture.execution.WorkspaceID, WorkflowRunID: fixture.execution.RunID,
		AnalysisRunID: fixture.run.ID, NodeRunID: fixture.execution.NodeRunID,
	}) {
		t.Fatalf("checkpoint query=%+v", fixture.planner.checkpointQuery)
	}
	deadlines, err := agentdomain.DeriveWorkspaceAnalysisV1Deadlines(fixture.run.Timeouts)
	if err != nil {
		t.Fatal(err)
	}
	if !fixture.planner.hasDeadline ||
		!fixture.planner.deadline.Equal(fixture.clock.Add(deadlines.RetrieveEvidenceDeadline())) {
		t.Fatalf("planner deadline=%s present=%t", fixture.planner.deadline, fixture.planner.hasDeadline)
	}

	plannerRequest := fixture.planner.request
	if plannerRequest.Identity.NodeKey != agentdomain.WorkspaceAnalysisOperationNodeRetrieveEvidence ||
		plannerRequest.Identity.NodeAttemptID != fixture.execution.NodeAttemptID || plannerRequest.AnalysisRunID != fixture.run.ID ||
		plannerRequest.Identity.LeaseFence != int64(fixture.execution.AttemptNo) ||
		plannerRequest.Retrieval.IndexVersionID != fixture.index.Index.ID || plannerRequest.Retrieval.EmbeddingVersionID == nil ||
		*plannerRequest.Retrieval.EmbeddingVersionID != fixture.index.EmbeddingVersion.ID ||
		plannerRequest.ProfileRef != DefaultProfileRef() || plannerRequest.PromptRef != WorkspaceAnalysisPlanPromptRef() ||
		plannerRequest.ModelSettingsRevision == nil || *plannerRequest.ModelSettingsRevision != 7 {
		t.Fatalf("planner request = %+v", plannerRequest)
	}
	plannerInput := string(plannerRequest.Input)
	for _, expected := range []string{
		`"question":"Explain the workspace architecture"`, `"question_text":"Earlier question"`,
		`"assistant_text":"Earlier grounded answer"`, `"retrieval_mode":"hybrid"`,
		`"answer_depth":"detailed"`, `"output_format":"outline"`, `"git_status"`, `"head"`,
	} {
		if !strings.Contains(plannerInput, expected) {
			t.Fatalf("planner input omitted %q: %s", expected, plannerInput)
		}
	}
	for _, forbidden := range []string{
		string(fixture.execution.WorkspaceID), string(fixture.execution.RunID), string(fixture.execution.NodeRunID),
		string(fixture.execution.NodeAttemptID), string(fixture.root.ConversationID), string(fixture.root.QuestionID),
		string(fixture.root.AnswerID), string(fixture.run.ID), string(fixture.index.Index.ID),
		`"workspace_id"`, `"analysis_run_id"`, `"path_prefixes"`, `"diff"`, `"private_binding"`, `"prompt"`,
	} {
		if strings.Contains(plannerInput, forbidden) {
			t.Fatalf("planner input leaked %q: %s", forbidden, plannerInput)
		}
	}

	command := fixture.tools.command
	if command.OperationKey != (agentdomain.WorkspaceAnalysisOperationKey{
		AnalysisRunID: fixture.run.ID, NodeKey: agentdomain.WorkspaceAnalysisOperationNodeRetrieveEvidence,
		Kind: agentdomain.WorkspaceAnalysisOperationKnowledgeSearch, Ordinal: 1,
	}) || command.Tool.Invocation != toolsdomain.InvocationSourceTrustedWorkflow || command.Tool.CallNo != 1 ||
		command.Tool.Identity.LeaseFence != int64(fixture.execution.AttemptNo) ||
		command.Tool.IdempotencyKey != "" || command.Tool.Request.ToolName != "SearchKnowledge" ||
		command.Tool.Request.Reason != workspaceAnalysisRetrieveReason ||
		string(command.Tool.Request.Arguments) != `{"query":"workspace architecture","mode":"hybrid","limit":5}` {
		t.Fatalf("search command = %+v", command)
	}

	output, err := conversationworkflow.DecodeWorkspaceAnalysisRetrieveEvidenceOutput(result.Output)
	if err != nil {
		t.Fatalf("DecodeWorkspaceAnalysisRetrieveEvidenceOutput: %v", err)
	}
	if output.GitToolCallID != fixture.predecessor.ToolCallID || output.GitToolReceiptID != fixture.predecessor.ToolReceiptID ||
		output.GitToolReceiptHash != fixture.predecessor.ToolReceiptHash || output.PlannerModelRunID != fixture.plan.Run.ID ||
		output.PlannerModelCallID != fixture.plan.Call.ID || output.PlannerModelResultID != fixture.plan.ModelResult.ID ||
		output.PlannerModelResultHash != fixture.plan.ModelResult.DocumentHash || output.SearchToolCallID != fixture.toolResult.Call.ID ||
		output.SearchToolReceiptID != fixture.toolResult.ResultReceiptID || output.SearchToolReceiptHash != fixture.toolResult.Call.ResponseHash ||
		output.SearchSummary.EffectiveMode != "hybrid" || output.SearchSummary.HitCount != 2 ||
		strings.Join(output.SearchSummary.EvidenceRefs, ",") != "E1,E2" {
		t.Fatalf("output = %+v", output)
	}
	serialized := string(result.Output)
	for _, forbidden := range []string{
		"private/path.go", "second private excerpt", "Explain the workspace architecture", "Earlier grounded answer",
		"workspace architecture", `"snippet"`, `"query"`, `"history"`, `"path"`, `"private_binding"`,
	} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("node output leaked %q: %s", forbidden, serialized)
		}
	}
}

func TestWorkspaceAnalysisRetrieveExecutorRejectsDriftBeforeExternalCalls(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*workspaceAnalysisRetrieveFixture, *testing.T)
	}{
		{
			name: "predecessor",
			mutate: func(fixture *workspaceAnalysisRetrieveFixture, t *testing.T) {
				changed := fixture.predecessor
				changed.ToolReceiptHash = strings.Repeat("e", 64)
				fixture.execution.Input = mustEncodeWorkspaceAnalysisInspectOutput(t, changed)
			},
		},
		{
			name: "predecessor canonical bytes",
			mutate: func(fixture *workspaceAnalysisRetrieveFixture, _ *testing.T) {
				fixture.execution.Input = append(fixture.execution.Input, ' ')
			},
		},
		{
			name: "root",
			mutate: func(fixture *workspaceAnalysisRetrieveFixture, t *testing.T) {
				changed := fixture.root
				changed.QuestionOrdinal++
				fixture.inputs.raw = mustEncodeWorkspaceAnalysisRoot(t, changed)
			},
		},
		{
			name: "question mode",
			mutate: func(fixture *workspaceAnalysisRetrieveFixture, t *testing.T) {
				request := fixture.question.Question.Request
				request.Mode = conversationdomain.QuestionModeRAG
				request, err := conversationdomain.CanonicalizeQuestionRequest(request)
				if err != nil {
					t.Fatal(err)
				}
				fixture.question.Question.Request = request
				fixture.question.Question.RequestHash, err = conversationdomain.ComputeQuestionRequestHash(request)
				if err != nil {
					t.Fatal(err)
				}
				fixture.context.result = fixture.question
			},
		},
		{
			name: "answer workflow",
			mutate: func(fixture *workspaceAnalysisRetrieveFixture, _ *testing.T) {
				fixture.question.Answer.WorkflowRunID = workspaceAnalysisRetrieveID(89)
				fixture.context.result = fixture.question
			},
		},
		{
			name: "analysis run",
			mutate: func(fixture *workspaceAnalysisRetrieveFixture, _ *testing.T) {
				fixture.run.DefinitionHash = strings.Repeat("e", 64)
				fixture.runs.run = fixture.run
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newWorkspaceAnalysisRetrieveFixture(t, false)
			test.mutate(fixture, t)
			_, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
			if err == nil || fixture.planner.calls != 0 || fixture.tools.calls != 0 {
				t.Fatalf("error=%v planner=%d tools=%d", err, fixture.planner.calls, fixture.tools.calls)
			}
		})
	}
}

func TestWorkspaceAnalysisRetrieveExecutorClarificationIsStableAndNeverSearches(t *testing.T) {
	fixture := newWorkspaceAnalysisRetrieveFixture(t, true)
	executor := fixture.executor(t)
	_, firstErr := executor.Execute(context.Background(), fixture.execution)
	if codeOf(firstErr) != conversationdomain.WorkspaceAnalysisClarificationRequired || fixture.tools.calls != 0 ||
		fixture.finalizer.terminationCalls != 1 {
		t.Fatalf("first error=%v tools=%d finalizer=%d", firstErr, fixture.tools.calls, fixture.finalizer.terminationCalls)
	}
	fixture.plan.Replayed = true
	fixture.planner.result = fixture.plan
	_, secondErr := executor.Execute(context.Background(), fixture.execution)
	if codeOf(secondErr) != conversationdomain.WorkspaceAnalysisClarificationRequired ||
		firstErr.Error() != secondErr.Error() || fixture.tools.calls != 0 || fixture.planner.calls != 2 ||
		fixture.finalizer.terminationCalls != 2 {
		t.Fatalf("first=%v second=%v planner=%d tools=%d finalizer=%d", firstErr, secondErr,
			fixture.planner.calls, fixture.tools.calls, fixture.finalizer.terminationCalls)
	}
	command := fixture.finalizer.terminationCommand
	if command.Reason != agentdomain.WorkspaceAnalysisRunNeedsClarification || command.OperationID == nil ||
		*command.OperationID != fixture.plan.ModelResult.OperationID || command.Artifact == nil ||
		command.Artifact.Kind != conversationapplication.WorkspaceAnalysisTerminationArtifactModelResult ||
		command.Artifact.ID != fixture.plan.ModelResult.ID || command.Artifact.Hash != fixture.plan.ModelResult.DocumentHash {
		t.Fatalf("clarification command=%#v", command)
	}
}

func TestWorkspaceAnalysisRetrieveExecutorReplacementReplaysSameNodeBytes(t *testing.T) {
	fixture := newWorkspaceAnalysisRetrieveFixture(t, false)
	executor := fixture.executor(t)
	first, err := executor.Execute(context.Background(), fixture.execution)
	if err != nil {
		t.Fatal(err)
	}

	replacement := fixture.execution
	replacement.NodeAttemptID = workspaceAnalysisRetrieveID(90)
	replacement.AttemptNo++
	replacement.RetryNo++
	replacement.NodeVersion++
	replacement.LeaseOwner = "worker-replacement"
	fixture.planner.checkpoint = workspaceAnalysisRetrievePlanCheckpoint(fixture.plan)
	fixture.planner.checkpointFound = true
	replacementIndex := workspaceAnalysisRetrieveIndexFixture(t, fixture.execution.WorkspaceID, fixture.clock.Add(time.Minute))
	replacementIndex.Index.ID = workspaceAnalysisRetrieveID(91)
	replacementEmbeddingID := workspaceAnalysisRetrieveID(92)
	replacementIndex.Index.EmbeddingVersionID = &replacementEmbeddingID
	replacementIndex.EmbeddingVersion.ID = replacementEmbeddingID
	fixture.retrieval.index = replacementIndex
	fixture.plan.Replayed = true
	fixture.planner.result = fixture.plan
	fixture.toolResult.Replayed = true
	fixture.tools.result = fixture.toolResult
	second, err := executor.Execute(context.Background(), replacement)
	if err != nil {
		t.Fatal(err)
	}
	if string(first.Output) != string(second.Output) || fixture.planner.calls != 2 || fixture.planner.checkpointCalls != 2 ||
		fixture.retrieval.calls != 1 || fixture.tools.calls != 2 ||
		fixture.planner.request.Identity.NodeAttemptID != replacement.NodeAttemptID ||
		fixture.planner.request.Retrieval.IndexVersionID != fixture.index.Index.ID ||
		fixture.tools.command.Tool.Identity.NodeAttemptID != replacement.NodeAttemptID ||
		fixture.planner.request.Identity.LeaseFence != int64(replacement.AttemptNo) ||
		fixture.tools.command.Tool.Identity.LeaseFence != int64(replacement.AttemptNo) {
		t.Fatalf("first=%s second=%s active=%d checkpoints=%d planner=%d tools=%d",
			first.Output, second.Output, fixture.retrieval.calls, fixture.planner.checkpointCalls, fixture.planner.calls, fixture.tools.calls)
	}
}

func TestWorkspaceAnalysisRetrieveExecutorRejectsCheckpointIdentityDriftBeforeSearch(t *testing.T) {
	fixture := newWorkspaceAnalysisRetrieveFixture(t, false)
	fixture.plan.Replayed = true
	fixture.planner.result = fixture.plan
	fixture.planner.checkpoint = workspaceAnalysisRetrievePlanCheckpoint(fixture.plan)
	fixture.planner.checkpoint.ModelCallID = workspaceAnalysisRetrieveID(89)
	fixture.planner.checkpointFound = true

	_, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
	if codeOf(err) != ErrorCodeOutputInvalid || fixture.retrieval.calls != 0 || fixture.planner.calls != 1 || fixture.tools.calls != 0 {
		t.Fatalf("error=%v active=%d planner=%d tools=%d", err, fixture.retrieval.calls, fixture.planner.calls, fixture.tools.calls)
	}
}

func TestWorkspaceAnalysisRetrieveExecutorRejectsPlannerAuthorityDriftBeforeSearch(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*workspaceAnalysisRetrieveFixture)
	}{
		{name: "wrong call", mutate: func(fixture *workspaceAnalysisRetrieveFixture) { fixture.plan.Call.CallNo = 2 }},
		{name: "wrong result hash", mutate: func(fixture *workspaceAnalysisRetrieveFixture) {
			fixture.plan.ModelResult.DocumentHash = strings.Repeat("f", 64)
		}},
		{name: "predecessor id reused", mutate: func(fixture *workspaceAnalysisRetrieveFixture) {
			fixture.plan.ModelResult.ID = fixture.predecessor.ToolCallID
		}},
		{name: "wrong retrieval", mutate: func(fixture *workspaceAnalysisRetrieveFixture) {
			fixture.plan.Run.Retrieval.IndexVersionID = workspaceAnalysisRetrieveID(88)
		}},
		{name: "old attempt without replay", mutate: func(fixture *workspaceAnalysisRetrieveFixture) {
			fixture.plan.Run.NodeAttemptID = workspaceAnalysisRetrieveID(88)
			fixture.plan.ModelResult.NodeAttemptID = workspaceAnalysisRetrieveID(88)
		}},
		{name: "result predates call", mutate: func(fixture *workspaceAnalysisRetrieveFixture) {
			fixture.plan.ModelResult.CreatedAt = fixture.plan.Call.StartedAt
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newWorkspaceAnalysisRetrieveFixture(t, false)
			test.mutate(fixture)
			fixture.planner.result = fixture.plan
			_, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
			if codeOf(err) != ErrorCodeOutputInvalid || fixture.planner.calls != 1 || fixture.tools.calls != 0 {
				t.Fatalf("error=%v planner=%d tools=%d", err, fixture.planner.calls, fixture.tools.calls)
			}
		})
	}
}

func TestWorkspaceAnalysisRetrieveExecutorRejectsToolReceiptHashAndSummaryDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*workspaceAnalysisRetrieveFixture)
	}{
		{name: "wrong receipt", mutate: func(fixture *workspaceAnalysisRetrieveFixture) {
			fixture.toolResult.ResultReceiptID = fixture.toolResult.Call.ID
		}},
		{name: "wrong output hash", mutate: func(fixture *workspaceAnalysisRetrieveFixture) {
			fixture.toolResult.Call.ResponseHash = strings.Repeat("f", 64)
		}},
		{name: "wrong tool", mutate: func(fixture *workspaceAnalysisRetrieveFixture) {
			wrong := toolsdomain.ToolRef{Name: "ReadGitStatus", Version: 2}
			fixture.toolResult.Call.Tool = &wrong
			fixture.toolResult.Call.RequestedToolName = wrong.Name
		}},
		{name: "untrusted marker", mutate: func(fixture *workspaceAnalysisRetrieveFixture) {
			fixture.toolResult.UntrustedData = false
		}},
		{name: "old attempt without replay", mutate: func(fixture *workspaceAnalysisRetrieveFixture) {
			fixture.toolResult.Call.NodeAttemptID = workspaceAnalysisRetrieveID(88)
		}},
		{name: "unsafe summary", mutate: func(fixture *workspaceAnalysisRetrieveFixture) {
			workspaceAnalysisRetrieveReplaceToolOutput(
				fixture,
				json.RawMessage(`{"effective_mode":"keyword","items":[],"degradations":[],"path":"private.go"}`),
			)
		}},
		{name: "mode drift", mutate: func(fixture *workspaceAnalysisRetrieveFixture) {
			workspaceAnalysisRetrieveReplaceToolOutput(
				fixture,
				json.RawMessage(`{"effective_mode":"semantic","items":[],"degradations":[]}`),
			)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newWorkspaceAnalysisRetrieveFixture(t, false)
			test.mutate(fixture)
			fixture.tools.result = fixture.toolResult
			_, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
			if err == nil || fixture.planner.calls != 1 || fixture.tools.calls != 1 {
				t.Fatalf("error=%v planner=%d tools=%d", err, fixture.planner.calls, fixture.tools.calls)
			}
		})
	}
}

func TestWorkspaceAnalysisRetrieveExecutorRejectsPersistedSearchReceiptDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*workspaceAnalysisRetrieveFixture)
	}{
		{name: "receipt id", mutate: func(fixture *workspaceAnalysisRetrieveFixture) {
			fixture.receipts.receipt.ID = workspaceAnalysisRetrieveID(42)
		}},
		{name: "tool call", mutate: func(fixture *workspaceAnalysisRetrieveFixture) {
			fixture.receipts.receipt.ToolCallID = workspaceAnalysisRetrieveID(42)
		}},
		{name: "output", mutate: func(fixture *workspaceAnalysisRetrieveFixture) {
			fixture.receipts.receipt.Output = json.RawMessage(`{"degradations":[],"effective_mode":"hybrid","items":[]}`)
			digest := sha256.Sum256(fixture.receipts.receipt.Output)
			fixture.receipts.receipt.OutputHash = hex.EncodeToString(digest[:])
			fixture.receipts.receipt.OutputBytes = int64(len(fixture.receipts.receipt.Output))
		}},
		{name: "index version", mutate: func(fixture *workspaceAnalysisRetrieveFixture) {
			binding := strings.ReplaceAll(
				string(fixture.receipts.receipt.PrivateBinding.Document),
				string(fixture.index.Index.ID),
				string(workspaceAnalysisRetrieveID(88)),
			)
			fixture.receipts.receipt.PrivateBinding.Document = json.RawMessage(binding)
			digest := sha256.Sum256(fixture.receipts.receipt.PrivateBinding.Document)
			fixture.receipts.receipt.PrivateBinding.Hash = hex.EncodeToString(digest[:])
			fixture.receipts.receipt.PrivateBinding.Bytes = int64(len(fixture.receipts.receipt.PrivateBinding.Document))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newWorkspaceAnalysisRetrieveFixture(t, false)
			test.mutate(fixture)
			_, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
			if codeOf(err) != ErrorCodeOutputInvalid || fixture.planner.calls != 1 || fixture.tools.calls != 1 || fixture.receipts.calls != 1 {
				t.Fatalf("error=%v planner=%d tools=%d receipts=%d", err, fixture.planner.calls, fixture.tools.calls, fixture.receipts.calls)
			}
		})
	}
}

func TestWorkspaceAnalysisRetrieveExecutorAllowsZeroHitReceiptForEvidenceInsufficientPath(t *testing.T) {
	fixture := newWorkspaceAnalysisRetrieveFixture(t, false)
	zeroOutput := json.RawMessage(`{"degradations":[],"effective_mode":"hybrid","items":[]}`)
	workspaceAnalysisRetrieveReplaceToolOutput(fixture, zeroOutput)
	fixture.tools.result = fixture.toolResult
	fixture.receipts.receipt.Output = append(json.RawMessage(nil), zeroOutput...)
	fixture.receipts.receipt.OutputHash = fixture.toolResult.Call.ResponseHash
	fixture.receipts.receipt.OutputBytes = fixture.toolResult.Call.ResponseBytes
	zeroBinding := json.RawMessage(`{"items":[],"selected_refs":[]}`)
	bindingDigest := sha256.Sum256(zeroBinding)
	fixture.receipts.receipt.PrivateBinding.Document = zeroBinding
	fixture.receipts.receipt.PrivateBinding.Hash = hex.EncodeToString(bindingDigest[:])
	fixture.receipts.receipt.PrivateBinding.Bytes = int64(len(zeroBinding))

	result, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	output, err := conversationworkflow.DecodeWorkspaceAnalysisRetrieveEvidenceOutput(result.Output)
	if err != nil || output.SearchSummary.HitCount != 0 || len(output.SearchSummary.EvidenceRefs) != 0 {
		t.Fatalf("output=%+v err=%v", output, err)
	}
}

func TestWorkspaceAnalysisRetrieveExecutorRejectsExpiredRunBeforeExternalCalls(t *testing.T) {
	fixture := newWorkspaceAnalysisRetrieveFixture(t, false)
	fixture.clock = fixture.run.DeadlineAt

	_, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
	if codeOf(err) != string(agentdomain.WorkspaceAnalysisRunDeadlineExceeded) ||
		fixture.retrieval.calls != 0 || fixture.planner.calls != 0 || fixture.tools.calls != 0 || fixture.receipts.calls != 0 {
		t.Fatalf("error=%v retrieval=%d planner=%d tools=%d receipts=%d", err,
			fixture.retrieval.calls, fixture.planner.calls, fixture.tools.calls, fixture.receipts.calls)
	}
}

func TestWorkspaceAnalysisRetrieveExecutorClampsNodeContextToRemainingRunDeadline(t *testing.T) {
	fixture := newWorkspaceAnalysisRetrieveFixture(t, false)
	fixture.clock = fixture.run.DeadlineAt.Add(-time.Second)

	if _, err := fixture.executor(t).Execute(context.Background(), fixture.execution); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !fixture.planner.hasDeadline || !fixture.planner.deadline.Equal(fixture.run.DeadlineAt) {
		t.Fatalf("planner deadline=%s present=%t run=%s", fixture.planner.deadline, fixture.planner.hasDeadline, fixture.run.DeadlineAt)
	}
}

type workspaceAnalysisRetrieveFixture struct {
	execution   workflowapplication.ExecutionContext
	root        conversationworkflow.WorkspaceAnalysisInput
	predecessor conversationworkflow.WorkspaceAnalysisInspectOutput
	question    conversationapplication.QuestionExecutionContext
	run         agentdomain.WorkspaceAnalysisRun
	index       retrievalapplication.SearchIndex
	plan        agentapplication.WorkspaceAnalysisRetrievalPlanResult
	toolResult  toolsapplication.ToolExecutionResult
	inputs      *workspaceAnalysisRetrieveInputFake
	stages      *workspaceAnalysisRetrieveStageFake
	context     *workspaceAnalysisRetrieveContextFake
	runs        *workspaceAnalysisRetrieveRunFake
	retrieval   *workspaceAnalysisRetrieveIndexFake
	planner     *workspaceAnalysisRetrievePlannerFake
	tools       *workspaceAnalysisRetrieveToolFake
	receipts    *workspaceAnalysisRetrieveReceiptFake
	finalizer   *workspaceAnalysisFinalizerFake
	clock       time.Time
}

func newWorkspaceAnalysisRetrieveFixture(t *testing.T, clarification bool) *workspaceAnalysisRetrieveFixture {
	t.Helper()
	now := workspaceAnalysisRetrieveNow()
	history := []conversationdomain.PublishedTurn{{
		QuestionID: workspaceAnalysisRetrieveID(60), AnswerID: workspaceAnalysisRetrieveID(61), Ordinal: 1,
		QuestionText: "Earlier question", AssistantText: "Earlier grounded answer",
		ResultType: agentdomain.ResultTypeRAGAnswer, ResultHash: strings.Repeat("9", 64),
	}}
	contextHash, through, _, err := conversationdomain.ComputeContextHash(history)
	if err != nil {
		t.Fatal(err)
	}
	request, err := conversationdomain.CanonicalizeQuestionRequest(conversationdomain.QuestionRequest{
		WorkspaceID: workspaceAnalysisRetrieveID(1), ConversationID: workspaceAnalysisRetrieveID(3),
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
	question := conversationapplication.QuestionExecutionContext{
		Question: conversationdomain.Question{
			ID: workspaceAnalysisRetrieveID(4), Request: request, Ordinal: 2, ContextThroughOrdinal: through,
			ContextHash: contextHash, RequestHash: requestHash, CreatedAt: now,
		},
		Answer: conversationdomain.Answer{
			ID: workspaceAnalysisRetrieveID(5), WorkspaceID: request.WorkspaceID, ConversationID: request.ConversationID,
			QuestionID: workspaceAnalysisRetrieveID(4), WorkflowRunID: workspaceAnalysisRetrieveID(2),
			PublicationStatus: conversationdomain.AnswerPublicationPending, Version: 1, CreatedAt: now, UpdatedAt: now,
		},
		History: history,
	}
	root := conversationworkflow.WorkspaceAnalysisInput{
		SchemaVersion:  conversationworkflow.WorkspaceAnalysisInputSchemaVersion,
		ConversationID: request.ConversationID, QuestionID: question.Question.ID, AnswerID: question.Answer.ID,
		QuestionOrdinal: question.Question.Ordinal, ContextHash: contextHash,
	}
	predecessor := conversationworkflow.WorkspaceAnalysisInspectOutput{
		SchemaVersion: conversationworkflow.WorkspaceAnalysisOutputSchemaVersion,
		ToolCallID:    workspaceAnalysisRetrieveID(10), ToolReceiptID: workspaceAnalysisRetrieveID(11),
		ToolReceiptHash: strings.Repeat("a", 64),
		GitStatus: conversationworkflow.WorkspaceAnalysisGitStatusSummary{
			Branch: "main", Clean: false, ConflictCount: 0, Head: strings.Repeat("b", 40), ObjectFormat: "sha1",
			StagedCount: 1, UnstagedCount: 2, UntrackedCount: 0,
		},
	}
	definition := conversationworkflow.RegisteredWorkspaceAnalysisDefinition()
	node, found := workspaceAnalysisNode(definition, conversationworkflow.WorkspaceAnalysisNodeRetrieveEvidence)
	if !found {
		t.Fatal("retrieve node missing")
	}
	revision := int64(7)
	execution := workflowapplication.ExecutionContext{
		WorkspaceID: request.WorkspaceID, DefinitionID: workspaceAnalysisRetrieveID(6),
		DefinitionVersion: definition.Version, DefinitionHash: definition.GraphHash, RunID: question.Answer.WorkflowRunID,
		NodeKey: node.Key, NodeRunID: workspaceAnalysisRetrieveID(7), NodeAttemptID: workspaceAnalysisRetrieveID(8), NodeKind: node.Kind,
		ModelSettingsRevision: &revision, NodeVersion: 2, InputSchemaVersion: node.InputSchemaVersion,
		AttemptNo: 1, DispatchNo: 1, LeaseOwner: "worker-retrieve", Input: mustEncodeWorkspaceAnalysisInspectOutput(t, predecessor),
	}
	run := workspaceAnalysisRetrieveRunFixture(t, execution, root, now)
	index := workspaceAnalysisRetrieveIndexFixture(t, execution.WorkspaceID, now)
	embeddingID := *index.Index.EmbeddingVersionID
	retrievalRef := agentdomain.RetrievalRef{IndexVersionID: index.Index.ID, EmbeddingVersionID: &embeddingID}
	plan := workspaceAnalysisRetrievePlanFixture(t, execution, run, retrievalRef, clarification)
	arguments := json.RawMessage(`{"query":"workspace architecture","mode":"hybrid","limit":5}`)
	toolResult := workspaceAnalysisRetrieveToolResult(t, execution, arguments)
	fixture := &workspaceAnalysisRetrieveFixture{
		execution: execution, root: root, predecessor: predecessor, question: question,
		run: run, index: index, plan: plan, toolResult: toolResult, clock: now,
	}
	fixture.inputs = &workspaceAnalysisRetrieveInputFake{raw: mustEncodeWorkspaceAnalysisRoot(t, root)}
	fixture.stages = &workspaceAnalysisRetrieveStageFake{raw: mustEncodeWorkspaceAnalysisInspectOutput(t, predecessor)}
	fixture.context = &workspaceAnalysisRetrieveContextFake{result: question}
	fixture.runs = &workspaceAnalysisRetrieveRunFake{run: run}
	fixture.retrieval = &workspaceAnalysisRetrieveIndexFake{index: index}
	fixture.planner = &workspaceAnalysisRetrievePlannerFake{result: plan}
	fixture.tools = &workspaceAnalysisRetrieveToolFake{result: toolResult}
	fixture.receipts = &workspaceAnalysisRetrieveReceiptFake{
		receipt: workspaceAnalysisRetrieveSearchReceipt(t, toolResult, index.Index.ID),
	}
	fixture.finalizer = &workspaceAnalysisFinalizerFake{}
	return fixture
}

func (fixture *workspaceAnalysisRetrieveFixture) executor(t *testing.T) *WorkspaceAnalysisRetrieveExecutor {
	t.Helper()
	executor, err := NewWorkspaceAnalysisRetrieveExecutor(WorkspaceAnalysisRetrieveExecutorDependencies{
		Context: fixture.context, Runs: fixture.runs, Inputs: fixture.inputs, Stages: fixture.stages,
		Retrieval: fixture.retrieval, Planner: fixture.planner, Tools: fixture.tools, Receipts: fixture.receipts,
		Finalizer: fixture.finalizer, Clock: foundation.FixedClock{Value: fixture.clock},
	})
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

func workspaceAnalysisRetrieveRunFixture(
	t *testing.T,
	execution workflowapplication.ExecutionContext,
	root conversationworkflow.WorkspaceAnalysisInput,
	now time.Time,
) agentdomain.WorkspaceAnalysisRun {
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
	return agentdomain.WorkspaceAnalysisRun{
		ID: workspaceAnalysisRetrieveID(9), WorkspaceID: execution.WorkspaceID, ConversationID: root.ConversationID,
		QuestionID: root.QuestionID, AnswerID: root.AnswerID, WorkflowRunID: execution.RunID,
		DefinitionKey:     conversationworkflow.WorkspaceAnalysisDefinitionKey,
		DefinitionVersion: conversationworkflow.WorkspaceAnalysisDefinitionVersion, DefinitionHash: execution.DefinitionHash,
		ToolCatalogHash: strings.Repeat("c", 64), PolicyVersion: agentdomain.WorkspaceAnalysisPolicyVersionV1,
		ConfigRevision: 1, DeadlineAt: now.Add(deadlines.RunDeadline()), Timeouts: timeouts,
		Limits: agentdomain.WorkspaceAnalysisBudgetLimits{
			Nodes: agentdomain.WorkspaceAnalysisV1MaxNodes, ToolConcurrency: agentdomain.WorkspaceAnalysisV1MaxToolConcurrency,
			Amount: agentdomain.WorkspaceAnalysisBudgetAmount{
				ModelCalls: agentdomain.WorkspaceAnalysisV1MaxModelCalls, ToolCalls: agentdomain.WorkspaceAnalysisV1MaxToolCalls,
				SourceReads: agentdomain.WorkspaceAnalysisV1MaxSourceReads, InputTokens: agentdomain.WorkspaceAnalysisV1MaxRunInputTokens,
				OutputTokens: agentdomain.WorkspaceAnalysisV1MaxRunOutputTokens,
			},
		},
		Status: agentdomain.WorkspaceAnalysisRunRunning, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
}

func workspaceAnalysisRetrieveIndexFixture(
	t *testing.T,
	workspaceID foundation.ID,
	now time.Time,
) retrievalapplication.SearchIndex {
	t.Helper()
	fusion := retrievaldomain.RRFConfig{
		SchemaVersion: retrievaldomain.RRFFusionSchemaVersionV1, Method: retrievaldomain.FusionMethodRRF, K: 60,
		LexicalCandidateLimit: 5, VectorCandidateLimit: 5, FusedCandidateLimit: 5, RerankCandidateLimit: 3,
	}
	fusionDocument, err := retrievaldomain.CanonicalRRFConfig(fusion)
	if err != nil {
		t.Fatal(err)
	}
	embedding := retrievaldomain.EmbeddingVersion{
		ID: workspaceAnalysisRetrieveID(21), Provider: "openai-compatible", AdapterName: "embedding-adapter",
		AdapterVersion: "v1", Model: "embedding-model", Dimensions: 3,
		Normalization: retrievaldomain.NormalizationL2, DistanceMetric: retrievaldomain.DistanceCosine,
		ConfigHash: strings.Repeat("f", 64), CreatedAt: now,
	}
	embeddingID := embedding.ID
	return retrievalapplication.SearchIndex{
		Index: retrievaldomain.IndexVersion{
			ID: workspaceAnalysisRetrieveID(20), WorkspaceID: workspaceID, EmbeddingVersionID: &embeddingID,
			TokenizerID: "postgres-simple", TokenizerVersion: "v1", TokenizerConfigHash: strings.Repeat("d", 64),
			FusionConfig: fusionDocument, SourceSnapshotRef: "reindex:event", ManifestHash: strings.Repeat("e", 64),
			ExpectedChunkCount: 10, IdempotencyKey: "workspace-analysis-retrieve", Status: retrievaldomain.IndexStatusActive,
			Version: 2, CreatedAt: now, UpdatedAt: now,
		},
		EmbeddingVersion: &embedding,
		Fusion:           &fusion,
	}
}

func workspaceAnalysisRetrievePlanFixture(
	t *testing.T,
	execution workflowapplication.ExecutionContext,
	analysisRun agentdomain.WorkspaceAnalysisRun,
	retrieval agentdomain.RetrievalRef,
	clarification bool,
) agentapplication.WorkspaceAnalysisRetrievalPlanResult {
	t.Helper()
	createdAt := workspaceAnalysisRetrieveNow().Add(time.Second)
	completedAt := createdAt.Add(time.Second)
	runID := workspaceAnalysisRetrieveID(30)
	payload := agentdomain.RAGQueryPlanPayload{
		Intent: "find architecture", Rewrites: []string{"workspace architecture", "workspace design"}, SuggestedScopes: []string{},
	}
	if clarification {
		payload = agentdomain.RAGQueryPlanPayload{
			Intent: "clarify architecture scope", RequiresClarification: true, Rewrites: []string{},
			ClarificationReason: "scope is ambiguous", ClarificationQuestion: "Which architecture area should be analyzed?",
			SuggestedScopes: []string{"backend", "frontend"},
		}
	}
	plan := agentdomain.WorkspaceAnalysisPlanResult{
		ResultType: agentdomain.ResultTypeWorkspaceAnalysisPlan, SchemaID: agentdomain.WorkspaceAnalysisPlanSchemaID,
		SchemaVersion: agentdomain.OutputSchemaVersionV1, ModelRunRef: runID, Payload: payload,
	}
	document, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(document)
	hash := hex.EncodeToString(digest[:])
	revision := cloneWorkspaceAnalysisInt64(execution.ModelSettingsRevision)
	schema := agentdomain.SchemaRef{ID: agentdomain.WorkspaceAnalysisPlanSchemaID, Version: agentdomain.OutputSchemaVersionV1}
	model := agentdomain.ModelRef{
		AdapterName: "openai-compatible", AdapterVersion: "v1", ModelID: "workspace-plan", ModelVersion: "2026-08-16",
	}
	profile := DefaultProfileRef()
	prompt := WorkspaceAnalysisPlanPromptRef()
	usage := agentdomain.TokenUsage{InputTokens: 20, OutputTokens: 8, TotalTokens: 28}
	run := agentdomain.ModelRun{
		ID: runID, WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID,
		NodeRunID: execution.NodeRunID, NodeAttemptID: execution.NodeAttemptID, ModelSettingsRevision: revision,
		Model: model, Profile: profile, Prompt: prompt, Schema: schema, ReducedSchema: schema, Retrieval: retrieval,
		Status: agentdomain.ModelRunSucceeded, FinalResultType: agentdomain.ResultTypeWorkspaceAnalysisPlan,
		Version: 2, CreatedAt: createdAt, UpdatedAt: completedAt, CompletedAt: &completedAt,
	}
	call := agentdomain.ModelCall{
		ID: workspaceAnalysisRetrieveID(31), ModelRunID: run.ID, CallNo: 1, Phase: agentdomain.ModelCallPlan,
		Model: model, Profile: profile, Prompt: prompt, Schema: schema,
		MaxOutputTokens: int(agentapplication.WorkspaceAnalysisV1PlanMaxOutputTokens), Status: agentdomain.ModelCallSucceeded,
		RequestHash: strings.Repeat("1", 64), ResponseHash: hash, RequestBytes: 256, ResponseBytes: int64(len(document)),
		Usage: usage, LatencyMillis: 1000, Version: 2, StartedAt: createdAt, CompletedAt: &completedAt,
	}
	modelResult := agentdomain.WorkspaceAnalysisModelResult{
		ID: workspaceAnalysisRetrieveID(32), WorkspaceID: execution.WorkspaceID, AnalysisRunID: analysisRun.ID,
		OperationID: workspaceAnalysisRetrieveID(33), NodeAttemptID: execution.NodeAttemptID,
		ModelRunID: run.ID, ModelCallID: call.ID, OperationKind: agentdomain.WorkspaceAnalysisOperationRetrievalPlan,
		Schema: schema, Document: document, DocumentHash: hash, DocumentBytes: int64(len(document)), CreatedAt: completedAt,
	}
	return agentapplication.WorkspaceAnalysisRetrievalPlanResult{
		Plan: plan, ModelResult: modelResult, Run: run, Call: call, Usage: usage,
	}
}

func workspaceAnalysisRetrievePlanCheckpoint(
	result agentapplication.WorkspaceAnalysisRetrievalPlanResult,
) agentapplication.WorkspaceAnalysisRetrievalPlanCheckpoint {
	return agentapplication.WorkspaceAnalysisRetrievalPlanCheckpoint{
		OperationID: result.ModelResult.OperationID, ModelRunID: result.Run.ID, ModelCallID: result.Call.ID,
		ModelResultID: result.ModelResult.ID, Retrieval: result.Run.Retrieval,
		RequestHash: result.Call.RequestHash, ResultHash: result.ModelResult.DocumentHash,
		Status: agentdomain.WorkspaceAnalysisOperationSucceeded,
	}
}

func workspaceAnalysisRetrieveToolResult(
	t *testing.T,
	execution workflowapplication.ExecutionContext,
	arguments json.RawMessage,
) toolsapplication.ToolExecutionResult {
	t.Helper()
	output := json.RawMessage(`{"degradations":[],"effective_mode":"hybrid","items":[{"evidence_ref":"E1","rank":1,"snippet":"private/path.go contains the architecture"},{"evidence_ref":"E2","rank":2,"snippet":"second private excerpt"}]}`)
	requestDocument, err := workspaceAnalysisSearchRequestDocument(arguments)
	if err != nil {
		t.Fatal(err)
	}
	requestDigest := sha256.Sum256(requestDocument)
	responseDigest := sha256.Sum256(output)
	completedAt := workspaceAnalysisRetrieveNow().Add(4 * time.Second)
	tool := workspaceAnalysisSearchRef
	inputSchema := workspaceAnalysisSearchInputSchema
	outputSchema := workspaceAnalysisSearchOutputSchema
	return toolsapplication.ToolExecutionResult{
		Call: toolsdomain.ToolCall{
			ID: workspaceAnalysisRetrieveID(40), WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID,
			NodeRunID: execution.NodeRunID, NodeAttemptID: execution.NodeAttemptID, CallNo: 1,
			RequestedToolName: tool.Name, Tool: &tool, DefinitionHash: strings.Repeat("2", 64),
			InputSchema: &inputSchema, OutputSchema: &outputSchema, Capability: capability.ReadLocal,
			SideEffectLevel: toolsdomain.SideEffectNone, InvocationPolicy: toolsdomain.InvocationTrustedWorkflowOnly,
			RequestHash: hex.EncodeToString(requestDigest[:]), RequestBytes: int64(len(requestDocument)), RequestSummary: json.RawMessage(`{}`),
			ResponseHash: hex.EncodeToString(responseDigest[:]), ResponseBytes: int64(len(output)), ResponseSummary: json.RawMessage(`{}`),
			Status: toolsdomain.CallSucceeded, Version: 2, StartedAt: completedAt.Add(-time.Second),
			CompletedAt: &completedAt, DurationMillis: 1000,
		},
		ResultReceiptID: workspaceAnalysisRetrieveID(41), Output: output, UntrustedData: true,
	}
}

func workspaceAnalysisRetrieveSearchReceipt(
	t *testing.T,
	result toolsapplication.ToolExecutionResult,
	indexVersionID foundation.ID,
) toolsdomain.ResultReceipt {
	t.Helper()
	var output struct {
		Items []struct {
			EvidenceRef string `json:"evidence_ref"`
		} `json:"items"`
	}
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	type citationIdentity struct {
		ChunkID         foundation.ID `json:"chunk_id"`
		CitationID      string        `json:"citation_id"`
		ContentHash     string        `json:"content_hash"`
		EvidenceRef     string        `json:"evidence_ref"`
		IndexVersionID  foundation.ID `json:"index_version_id"`
		SourceSpanID    foundation.ID `json:"source_span_id"`
		SourceVersionID foundation.ID `json:"source_version_id"`
	}
	bindingDocument := struct {
		Items        []citationIdentity `json:"items"`
		SelectedRefs []string           `json:"selected_refs"`
	}{
		Items:        make([]citationIdentity, len(output.Items)),
		SelectedRefs: make([]string, min(agentdomain.WorkspaceAnalysisV1MaxSourceReads, len(output.Items))),
	}
	for index, item := range output.Items {
		base := 50 + index*3
		hexDigit := string(rune('a' + index%6))
		bindingDocument.Items[index] = citationIdentity{
			ChunkID: workspaceAnalysisRetrieveID(base), CitationID: "cite-" + strings.Repeat(hexDigit, 64),
			ContentHash: strings.Repeat(hexDigit, 64), EvidenceRef: item.EvidenceRef, IndexVersionID: indexVersionID,
			SourceSpanID: workspaceAnalysisRetrieveID(base + 2), SourceVersionID: workspaceAnalysisRetrieveID(base + 1),
		}
		if index < len(bindingDocument.SelectedRefs) {
			bindingDocument.SelectedRefs[index] = item.EvidenceRef
		}
	}
	binding, err := json.Marshal(bindingDocument)
	if err != nil {
		t.Fatal(err)
	}
	bindingDigest := sha256.Sum256(binding)
	return toolsdomain.ResultReceipt{
		ID: result.ResultReceiptID, ToolCallID: result.Call.ID, WorkspaceID: result.Call.WorkspaceID,
		WorkflowRunID: result.Call.WorkflowRunID, NodeRunID: result.Call.NodeRunID, NodeAttemptID: result.Call.NodeAttemptID,
		Tool: workspaceAnalysisSearchRef, OutputSchema: workspaceAnalysisSearchOutputSchema,
		DefinitionHash: result.Call.DefinitionHash, PersistencePolicy: toolsdomain.ResultPersistenceCanonical,
		MaxOutputBytes:         toolsdomain.SearchKnowledgeV2ReceiptMaxOutputBytes,
		MaxPrivateBindingBytes: toolsdomain.SearchKnowledgeV2ReceiptMaxPrivateBindingBytes,
		Output:                 append(json.RawMessage(nil), result.Output...), OutputHash: result.Call.ResponseHash,
		OutputBytes: result.Call.ResponseBytes,
		PrivateBinding: &toolsdomain.ResultReceiptPrivateBinding{
			Schema:   toolsdomain.SchemaRef{ID: "tool.search_knowledge.private_binding", Version: 1},
			Document: append(json.RawMessage(nil), binding...), Hash: hex.EncodeToString(bindingDigest[:]), Bytes: int64(len(binding)),
		},
		CreatedAt: *result.Call.CompletedAt,
	}
}

func workspaceAnalysisRetrieveReplaceToolOutput(fixture *workspaceAnalysisRetrieveFixture, output json.RawMessage) {
	fixture.toolResult.Output = output
	digest := sha256.Sum256(output)
	fixture.toolResult.Call.ResponseHash = hex.EncodeToString(digest[:])
	fixture.toolResult.Call.ResponseBytes = int64(len(output))
}

type workspaceAnalysisRetrieveInputFake struct {
	raw                json.RawMessage
	workspaceID, runID foundation.ID
	calls              int
}

func (fake *workspaceAnalysisRetrieveInputFake) GetRunInput(
	_ context.Context,
	workspaceID, runID foundation.ID,
) (json.RawMessage, error) {
	fake.calls++
	fake.workspaceID, fake.runID = workspaceID, runID
	return append(json.RawMessage(nil), fake.raw...), nil
}

type workspaceAnalysisRetrieveStageFake struct {
	raw                json.RawMessage
	workspaceID, runID foundation.ID
	nodeKey            string
	calls              int
}

func (fake *workspaceAnalysisRetrieveStageFake) GetSucceededNodeOutput(
	_ context.Context,
	workspaceID, runID foundation.ID,
	nodeKey string,
) (json.RawMessage, error) {
	fake.calls++
	fake.workspaceID, fake.runID, fake.nodeKey = workspaceID, runID, nodeKey
	return append(json.RawMessage(nil), fake.raw...), nil
}

type workspaceAnalysisRetrieveContextFake struct {
	result conversationapplication.QuestionExecutionContext
	query  conversationapplication.QuestionExecutionContextQuery
	calls  int
}

func (fake *workspaceAnalysisRetrieveContextFake) LoadQuestionExecutionContext(
	_ context.Context,
	query conversationapplication.QuestionExecutionContextQuery,
) (conversationapplication.QuestionExecutionContext, error) {
	fake.calls++
	fake.query = query
	return fake.result, nil
}

type workspaceAnalysisRetrieveRunFake struct {
	run   agentdomain.WorkspaceAnalysisRun
	query agentapplication.WorkspaceAnalysisRunExecutionQuery
	calls int
}

func (fake *workspaceAnalysisRetrieveRunFake) LoadWorkspaceAnalysisRunForExecution(
	_ context.Context,
	query agentapplication.WorkspaceAnalysisRunExecutionQuery,
) (agentdomain.WorkspaceAnalysisRun, error) {
	fake.calls++
	fake.query = query
	return fake.run, nil
}

type workspaceAnalysisRetrieveIndexFake struct {
	index       retrievalapplication.SearchIndex
	workspaceID foundation.ID
	calls       int
}

func (fake *workspaceAnalysisRetrieveIndexFake) LoadActiveSearchIndex(
	_ context.Context,
	workspaceID foundation.ID,
) (retrievalapplication.SearchIndex, error) {
	fake.calls++
	fake.workspaceID = workspaceID
	return fake.index, nil
}

type workspaceAnalysisRetrievePlannerFake struct {
	result          agentapplication.WorkspaceAnalysisRetrievalPlanResult
	request         agentapplication.WorkspaceAnalysisRetrievalPlanRequest
	err             error
	checkpoint      agentapplication.WorkspaceAnalysisRetrievalPlanCheckpoint
	checkpointQuery agentapplication.WorkspaceAnalysisRetrievalPlanCheckpointQuery
	checkpointFound bool
	deadline        time.Time
	hasDeadline     bool
	checkpointCalls int
	calls           int
}

func (fake *workspaceAnalysisRetrievePlannerFake) FindWorkspaceAnalysisRetrievalPlanCheckpoint(
	_ context.Context,
	query agentapplication.WorkspaceAnalysisRetrievalPlanCheckpointQuery,
) (agentapplication.WorkspaceAnalysisRetrievalPlanCheckpoint, bool, error) {
	fake.checkpointCalls++
	fake.checkpointQuery = query
	return fake.checkpoint, fake.checkpointFound, nil
}

func (fake *workspaceAnalysisRetrievePlannerFake) Run(
	ctx context.Context,
	request agentapplication.WorkspaceAnalysisRetrievalPlanRequest,
) (agentapplication.WorkspaceAnalysisRetrievalPlanResult, error) {
	fake.calls++
	fake.request = request
	fake.deadline, fake.hasDeadline = ctx.Deadline()
	if fake.err != nil {
		return agentapplication.WorkspaceAnalysisRetrievalPlanResult{}, fake.err
	}
	return fake.result, nil
}

type workspaceAnalysisRetrieveToolFake struct {
	result  toolsapplication.ToolExecutionResult
	command toolsapplication.ExecuteWorkspaceAnalysisToolCommand
	err     error
	calls   int
}

type workspaceAnalysisRetrieveReceiptFake struct {
	receipt            toolsdomain.ResultReceipt
	workspaceID, runID foundation.ID
	calls              int
}

func (fake *workspaceAnalysisRetrieveReceiptFake) LoadSearchKnowledgeV2Receipt(
	_ context.Context,
	workspaceID foundation.ID,
	workflowRunID foundation.ID,
) (toolsdomain.ResultReceipt, error) {
	fake.calls++
	fake.workspaceID, fake.runID = workspaceID, workflowRunID
	return fake.receipt, nil
}

func (fake *workspaceAnalysisRetrieveToolFake) ExecuteWorkspaceAnalysisTool(
	_ context.Context,
	command toolsapplication.ExecuteWorkspaceAnalysisToolCommand,
) (toolsapplication.ToolExecutionResult, error) {
	fake.calls++
	fake.command = command
	if fake.err != nil {
		return toolsapplication.ToolExecutionResult{}, fake.err
	}
	return fake.result, nil
}

func mustEncodeWorkspaceAnalysisInspectOutput(
	t *testing.T,
	value conversationworkflow.WorkspaceAnalysisInspectOutput,
) json.RawMessage {
	t.Helper()
	raw, err := conversationworkflow.EncodeWorkspaceAnalysisInspectOutput(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustEncodeWorkspaceAnalysisRoot(
	t *testing.T,
	value conversationworkflow.WorkspaceAnalysisInput,
) json.RawMessage {
	t.Helper()
	raw, err := conversationworkflow.EncodeWorkspaceAnalysisInput(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func workspaceAnalysisRetrieveNow() time.Time {
	return time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
}

func workspaceAnalysisRetrieveID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("93000000-0000-4000-8000-%012d", value))
}

var (
	_ workspaceAnalysisRunInputReader                        = (*workspaceAnalysisRetrieveInputFake)(nil)
	_ workspaceAnalysisStageOutputReader                     = (*workspaceAnalysisRetrieveStageFake)(nil)
	_ conversationapplication.QuestionExecutionContextLoader = (*workspaceAnalysisRetrieveContextFake)(nil)
	_ agentapplication.WorkspaceAnalysisRunLoader            = (*workspaceAnalysisRetrieveRunFake)(nil)
	_ workspaceAnalysisActiveSearchIndexLoader               = (*workspaceAnalysisRetrieveIndexFake)(nil)
	_ WorkspaceAnalysisRetrievalPlanRunner                   = (*workspaceAnalysisRetrievePlannerFake)(nil)
	_ workspaceAnalysisToolExecutor                          = (*workspaceAnalysisRetrieveToolFake)(nil)
	_ toolsapplication.SearchKnowledgeV2ReceiptReader        = (*workspaceAnalysisRetrieveReceiptFake)(nil)
)
