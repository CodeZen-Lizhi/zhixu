package workflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/capability"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func TestWorkspaceAnalysisReadEvidenceExecutorReadsFrozenSourcesInOrder(t *testing.T) {
	for _, count := range []int{1, 3} {
		t.Run(fmt.Sprintf("%d read(s)", count), func(t *testing.T) {
			fixture := newWorkspaceAnalysisReadEvidenceFixture(t, count)
			executor := fixture.executor(t)

			result, err := executor.Execute(context.Background(), fixture.execution)
			if err != nil {
				t.Fatalf("Execute: %v", err)
			}
			if fixture.inputs.calls != 1 || fixture.stages.calls != 1 || fixture.context.calls != 1 ||
				fixture.runs.calls != 1 || fixture.receipts.calls != 1 || fixture.tools.calls != count {
				t.Fatalf("calls inputs=%d stages=%d context=%d runs=%d receipts=%d tools=%d",
					fixture.inputs.calls, fixture.stages.calls, fixture.context.calls, fixture.runs.calls,
					fixture.receipts.calls, fixture.tools.calls)
			}
			if fixture.stages.nodeKey != conversationworkflow.WorkspaceAnalysisNodeRetrieveEvidence ||
				fixture.receipts.query.WorkspaceID != fixture.execution.WorkspaceID ||
				fixture.receipts.query.WorkflowRunID != fixture.execution.RunID ||
				fixture.receipts.query.AnalysisRunID != fixture.run.ID ||
				fixture.receipts.query.ReceiptID != fixture.predecessor.SearchToolReceiptID ||
				fixture.receipts.query.ReceiptHash != fixture.predecessor.SearchToolReceiptHash {
				t.Fatalf("authority queries stage=%+v receipt=%+v", fixture.stages, fixture.receipts)
			}
			deadlines, err := agentdomain.DeriveWorkspaceAnalysisV1Deadlines(fixture.run.Timeouts)
			if err != nil {
				t.Fatal(err)
			}
			wantDeadline := fixture.clock.Add(deadlines.ReadEvidenceDeadline())
			for index := range fixture.tools.deadlines {
				if !fixture.tools.hasDeadlines[index] || !fixture.tools.deadlines[index].Equal(wantDeadline) {
					t.Fatalf("tool %d deadline=%s present=%t", index+1, fixture.tools.deadlines[index], fixture.tools.hasDeadlines[index])
				}
			}

			for index, command := range fixture.tools.commands {
				ordinal := index + 1
				wantRef := workspaceAnalysisReadEvidenceRef(ordinal)
				if command.OperationKey != (agentdomain.WorkspaceAnalysisOperationKey{
					AnalysisRunID: fixture.run.ID,
					NodeKey:       agentdomain.WorkspaceAnalysisOperationNodeReadEvidence,
					Kind:          agentdomain.WorkspaceAnalysisOperationSourceRead,
					Ordinal:       ordinal,
				}) || command.Tool.CallNo != ordinal || command.Tool.Invocation != toolsdomain.InvocationSourceTrustedWorkflow ||
					command.Tool.Identity.LeaseFence != int64(fixture.execution.AttemptNo) ||
					command.Tool.Identity.NodeAttemptID != fixture.execution.NodeAttemptID ||
					command.Tool.Request.ToolName != "ReadSource" || command.Tool.Request.Reason != workspaceAnalysisReadEvidenceReason ||
					string(command.Tool.Request.Arguments) != `{"evidence_ref":"`+wantRef+`"}` {
					t.Fatalf("command %d = %+v", ordinal, command)
				}
			}

			output, err := conversationworkflow.DecodeWorkspaceAnalysisReadEvidenceOutput(result.Output)
			if err != nil {
				t.Fatalf("DecodeWorkspaceAnalysisReadEvidenceOutput: %v", err)
			}
			if len(output.Reads) != count {
				t.Fatalf("reads = %#v", output.Reads)
			}
			for index, read := range output.Reads {
				toolResult := fixture.toolResults[index]
				if read != (conversationworkflow.WorkspaceAnalysisReadEvidenceReceipt{
					EvidenceRef: workspaceAnalysisReadEvidenceRef(index + 1), ToolCallID: toolResult.Call.ID,
					ToolReceiptID: toolResult.ResultReceiptID, ToolReceiptHash: toolResult.Call.ResponseHash,
				}) {
					t.Fatalf("read %d = %#v", index+1, read)
				}
			}
			for _, forbidden := range []string{
				"excerpt", "snippet", "private_binding", "citation_id", "index_version_id", "chunk_id",
				"source_version_id", "source_span_id", "path", "SOURCE_BODY_CANARY",
			} {
				if strings.Contains(string(result.Output), forbidden) {
					t.Fatalf("output leaked %q: %s", forbidden, result.Output)
				}
			}
		})
	}
}

func TestWorkspaceAnalysisReadEvidenceExecutorRejectsReadResultSlotDrift(t *testing.T) {
	fixture := newWorkspaceAnalysisReadEvidenceFixture(t, 2)
	fixture.toolResults[1].Call.CallNo = 1
	fixture.tools.results = fixture.toolResults

	_, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
	if codeOf(err) != ErrorCodeOutputInvalid || fixture.tools.calls != 2 {
		t.Fatalf("err=%v calls=%d", err, fixture.tools.calls)
	}
}

func TestWorkspaceAnalysisReadEvidenceExecutorRejectsOldAttemptWithoutReplay(t *testing.T) {
	fixture := newWorkspaceAnalysisReadEvidenceFixture(t, 1)
	fixture.toolResults[0].Call.NodeAttemptID = workspaceAnalysisReadEvidenceID(91)
	fixture.tools.results = fixture.toolResults

	_, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
	if codeOf(err) != ErrorCodeOutputInvalid || fixture.tools.calls != 1 {
		t.Fatalf("err=%v calls=%d", err, fixture.tools.calls)
	}
}

func TestWorkspaceAnalysisReadEvidenceExecutorRejectsZeroHitsWithoutSourceCall(t *testing.T) {
	fixture := newWorkspaceAnalysisReadEvidenceFixture(t, 0)

	_, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
	if codeOf(err) != string(conversationdomain.WorkspaceAnalysisEvidenceInsufficient) ||
		fixture.receipts.calls != 1 || fixture.tools.calls != 0 || fixture.finalizer.terminationCalls != 1 {
		t.Fatalf("err=%v receipts=%d tools=%d finalizes=%d", err, fixture.receipts.calls, fixture.tools.calls, fixture.finalizer.terminationCalls)
	}
	command := fixture.finalizer.terminationCommand
	if command.WorkspaceAnalysisPublicationLookup != workspaceAnalysisPublicationLookup(fixture.execution, fixture.root, fixture.run.ID) ||
		command.ExpectedAnswerVersion != fixture.question.Answer.Version ||
		command.Reason != agentdomain.WorkspaceAnalysisRunEvidenceInsufficient || command.OperationID == nil ||
		*command.OperationID != fixture.receipts.operationID || command.Artifact == nil ||
		command.Artifact.Kind != conversationapplication.WorkspaceAnalysisTerminationArtifactToolReceipt ||
		command.Artifact.ID != fixture.search.ID || command.Artifact.Hash != fixture.search.OutputHash {
		t.Fatalf("termination command = %#v", command)
	}
}

func TestWorkspaceAnalysisReadEvidenceExecutorRejectsAuthorityBindingDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *workspaceAnalysisReadEvidenceFixture)
		code   string
	}{
		{
			name: "root",
			mutate: func(t *testing.T, fixture *workspaceAnalysisReadEvidenceFixture) {
				root := fixture.root
				root.AnswerID = workspaceAnalysisReadEvidenceID(80)
				fixture.inputs.raw = mustEncodeWorkspaceAnalysisRoot(t, root)
			},
			code: ErrorCodeInputInvalid,
		},
		{
			name: "question mode",
			mutate: func(_ *testing.T, fixture *workspaceAnalysisReadEvidenceFixture) {
				fixture.context.result.Question.Request.Mode = conversationdomain.QuestionModeRAG
			},
			code: ErrorCodeInputInvalid,
		},
		{
			name: "analysis run",
			mutate: func(_ *testing.T, fixture *workspaceAnalysisReadEvidenceFixture) {
				fixture.runs.run.AnswerID = workspaceAnalysisReadEvidenceID(81)
			},
			code: ErrorCodeInputInvalid,
		},
		{
			name: "authoritative predecessor",
			mutate: func(t *testing.T, fixture *workspaceAnalysisReadEvidenceFixture) {
				drifted := fixture.predecessor
				drifted.SearchToolReceiptHash = strings.Repeat("f", 64)
				fixture.stages.raw = mustEncodeWorkspaceAnalysisRetrieveOutput(t, drifted)
			},
			code: string(conversationdomain.WorkspaceAnalysisReceiptInvalid),
		},
		{
			name: "search call",
			mutate: func(_ *testing.T, fixture *workspaceAnalysisReadEvidenceFixture) {
				fixture.receipts.receipt.ToolCallID = workspaceAnalysisReadEvidenceID(82)
			},
			code: string(conversationdomain.WorkspaceAnalysisReceiptInvalid),
		},
		{
			name: "search receipt hash",
			mutate: func(_ *testing.T, fixture *workspaceAnalysisReadEvidenceFixture) {
				fixture.receipts.receipt.OutputHash = strings.Repeat("0", 64)
			},
			code: string(conversationdomain.WorkspaceAnalysisReceiptInvalid),
		},
		{
			name: "search private binding",
			mutate: func(_ *testing.T, fixture *workspaceAnalysisReadEvidenceFixture) {
				fixture.receipts.receipt.PrivateBinding = nil
			},
			code: string(conversationdomain.WorkspaceAnalysisReceiptInvalid),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newWorkspaceAnalysisReadEvidenceFixture(t, 3)
			test.mutate(t, fixture)

			_, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
			if codeOf(err) != test.code || fixture.tools.calls != 0 {
				t.Fatalf("err=%v tools=%d", err, fixture.tools.calls)
			}
		})
	}
}

func TestWorkspaceAnalysisReadEvidenceExecutorStopsAfterSelectedSourceFailure(t *testing.T) {
	fixture := newWorkspaceAnalysisReadEvidenceFixture(t, 3)
	sentinel := errors.New("second selected source failed")
	fixture.tools.failAt = 2
	fixture.tools.err = sentinel

	_, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
	if !errors.Is(err, sentinel) || fixture.tools.calls != 2 || len(fixture.tools.commands) != 2 {
		t.Fatalf("err=%v calls=%d commands=%d", err, fixture.tools.calls, len(fixture.tools.commands))
	}
}

func TestWorkspaceAnalysisReadEvidenceExecutorRejectsSemanticallyEquivalentNonExactPredecessor(t *testing.T) {
	fixture := newWorkspaceAnalysisReadEvidenceFixture(t, 1)
	var reordered map[string]json.RawMessage
	if err := json.Unmarshal(fixture.execution.Input, &reordered); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(reordered)
	if err != nil {
		t.Fatal(err)
	}
	fixture.execution.Input = append(json.RawMessage(" \n"), raw...)

	if _, err := fixture.executor(t).Execute(context.Background(), fixture.execution); codeOf(err) != string(conversationdomain.WorkspaceAnalysisReceiptInvalid) || fixture.inputs.calls != 0 || fixture.tools.calls != 0 {
		t.Fatalf("error=%v inputs=%d tools=%d", err, fixture.inputs.calls, fixture.tools.calls)
	}
}

func TestWorkspaceAnalysisReadEvidenceExecutorReplacementReplaysStableOutput(t *testing.T) {
	fixture := newWorkspaceAnalysisReadEvidenceFixture(t, 3)
	executor := fixture.executor(t)
	first, err := executor.Execute(context.Background(), fixture.execution)
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}

	replacement := fixture.execution
	replacement.NodeAttemptID = workspaceAnalysisReadEvidenceID(90)
	replacement.NodeVersion = 4
	replacement.AttemptNo = 2
	replacement.DispatchNo = 2
	replacement.LeaseOwner = "worker-read-evidence-replacement"
	replayed := make([]toolsapplication.ToolExecutionResult, len(fixture.toolResults))
	for index, result := range fixture.toolResults {
		result.Replayed = true
		replayed[index] = result
	}
	fixture.tools.calls = 0
	fixture.tools.commands = nil
	fixture.tools.results = replayed

	second, err := executor.Execute(context.Background(), replacement)
	if err != nil {
		t.Fatalf("replacement Execute: %v", err)
	}
	if !bytes.Equal(first.Output, second.Output) || fixture.tools.calls != 3 {
		t.Fatalf("stable=%t calls=%d first=%s second=%s", bytes.Equal(first.Output, second.Output), fixture.tools.calls, first.Output, second.Output)
	}
	for index, command := range fixture.tools.commands {
		if command.Tool.Identity.NodeAttemptID != replacement.NodeAttemptID ||
			command.Tool.Identity.LeaseFence != int64(replacement.AttemptNo) || !replayed[index].Replayed ||
			replayed[index].Call.NodeAttemptID == replacement.NodeAttemptID {
			t.Fatalf("replacement command/result %d = %+v / %+v", index+1, command, replayed[index])
		}
	}
}

func TestWorkspaceAnalysisReadEvidenceExecutorStopsAtDuplicateReceiptIdentity(t *testing.T) {
	fixture := newWorkspaceAnalysisReadEvidenceFixture(t, 3)
	fixture.toolResults[1].Call.ID = fixture.toolResults[0].Call.ID
	fixture.tools.results = fixture.toolResults

	_, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
	if codeOf(err) != ErrorCodeOutputInvalid || fixture.tools.calls != 2 {
		t.Fatalf("err=%v calls=%d", err, fixture.tools.calls)
	}
}

func TestWorkspaceAnalysisReadEvidenceExecutorRejectsExpiredRunBeforeReceiptOrSourceCalls(t *testing.T) {
	fixture := newWorkspaceAnalysisReadEvidenceFixture(t, 1)
	fixture.clock = fixture.run.DeadlineAt

	_, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
	if codeOf(err) != string(agentdomain.WorkspaceAnalysisRunDeadlineExceeded) || fixture.receipts.calls != 0 || fixture.tools.calls != 0 {
		t.Fatalf("error=%v receipts=%d tools=%d", err, fixture.receipts.calls, fixture.tools.calls)
	}
}

type workspaceAnalysisReadEvidenceFixture struct {
	execution   workflowapplication.ExecutionContext
	root        conversationworkflow.WorkspaceAnalysisInput
	predecessor conversationworkflow.WorkspaceAnalysisRetrieveEvidenceOutput
	question    conversationapplication.QuestionExecutionContext
	run         agentdomain.WorkspaceAnalysisRun
	search      toolsdomain.ResultReceipt
	toolResults []toolsapplication.ToolExecutionResult
	inputs      *workspaceAnalysisRetrieveInputFake
	stages      *workspaceAnalysisRetrieveStageFake
	context     *workspaceAnalysisRetrieveContextFake
	runs        *workspaceAnalysisRetrieveRunFake
	receipts    *workspaceAnalysisReadEvidenceReceiptFake
	tools       *workspaceAnalysisReadEvidenceToolFake
	finalizer   *workspaceAnalysisFinalizerFake
	clock       time.Time
}

func newWorkspaceAnalysisReadEvidenceFixture(t *testing.T, hitCount int) *workspaceAnalysisReadEvidenceFixture {
	t.Helper()
	if hitCount < 0 || hitCount > 5 {
		t.Fatalf("invalid hit count %d", hitCount)
	}
	retrieve := newWorkspaceAnalysisRetrieveFixture(t, false)
	definition := conversationworkflow.RegisteredWorkspaceAnalysisDefinition()
	node, found := workspaceAnalysisNode(definition, conversationworkflow.WorkspaceAnalysisNodeReadEvidence)
	if !found {
		t.Fatal("read_evidence node missing")
	}
	execution := retrieve.execution
	execution.NodeKey = node.Key
	execution.NodeKind = node.Kind
	execution.NodeRunID = workspaceAnalysisReadEvidenceID(1)
	execution.NodeAttemptID = workspaceAnalysisReadEvidenceID(2)
	execution.NodeVersion = 2
	execution.AttemptNo = 1
	execution.DispatchNo = 1
	execution.RetryNo = 0
	execution.LeaseOwner = "worker-read-evidence"

	searchOutput := workspaceAnalysisReadEvidenceSearchOutput(hitCount)
	workspaceAnalysisRetrieveReplaceToolOutput(retrieve, searchOutput)
	search := workspaceAnalysisRetrieveSearchReceipt(t, retrieve.toolResult, retrieve.index.Index.ID)
	predecessor := conversationworkflow.WorkspaceAnalysisRetrieveEvidenceOutput{
		SchemaVersion:          conversationworkflow.WorkspaceAnalysisOutputSchemaVersion,
		GitToolCallID:          retrieve.predecessor.ToolCallID,
		GitToolReceiptID:       retrieve.predecessor.ToolReceiptID,
		GitToolReceiptHash:     retrieve.predecessor.ToolReceiptHash,
		PlannerModelRunID:      retrieve.plan.Run.ID,
		PlannerModelCallID:     retrieve.plan.Call.ID,
		PlannerModelResultID:   retrieve.plan.ModelResult.ID,
		PlannerModelResultHash: retrieve.plan.ModelResult.DocumentHash,
		SearchToolCallID:       retrieve.toolResult.Call.ID,
		SearchToolReceiptID:    retrieve.toolResult.ResultReceiptID,
		SearchToolReceiptHash:  retrieve.toolResult.Call.ResponseHash,
		SearchSummary:          workspaceAnalysisReadEvidenceSearchSummary(hitCount),
	}
	execution.Input = mustEncodeWorkspaceAnalysisRetrieveOutput(t, predecessor)
	readCount := min(hitCount, 3)
	toolResults := make([]toolsapplication.ToolExecutionResult, readCount)
	for index := range toolResults {
		toolResults[index] = workspaceAnalysisReadEvidenceToolResult(t, execution, index+1, false)
	}
	fixture := &workspaceAnalysisReadEvidenceFixture{
		execution: execution, root: retrieve.root, predecessor: predecessor, question: retrieve.question,
		run: retrieve.run, search: search, toolResults: toolResults, clock: workspaceAnalysisRetrieveNow(),
	}
	fixture.inputs = &workspaceAnalysisRetrieveInputFake{raw: mustEncodeWorkspaceAnalysisRoot(t, fixture.root)}
	fixture.stages = &workspaceAnalysisRetrieveStageFake{raw: mustEncodeWorkspaceAnalysisRetrieveOutput(t, predecessor)}
	fixture.context = &workspaceAnalysisRetrieveContextFake{result: fixture.question}
	fixture.runs = &workspaceAnalysisRetrieveRunFake{run: fixture.run}
	fixture.receipts = &workspaceAnalysisReadEvidenceReceiptFake{
		receipt: search, operationID: workspaceAnalysisReadEvidenceID(99),
	}
	fixture.tools = &workspaceAnalysisReadEvidenceToolFake{results: toolResults}
	fixture.finalizer = &workspaceAnalysisFinalizerFake{}
	return fixture
}

func (fixture *workspaceAnalysisReadEvidenceFixture) executor(t *testing.T) *WorkspaceAnalysisReadEvidenceExecutor {
	t.Helper()
	executor, err := NewWorkspaceAnalysisReadEvidenceExecutor(WorkspaceAnalysisReadEvidenceExecutorDependencies{
		Context: fixture.context, Runs: fixture.runs, Inputs: fixture.inputs, Stages: fixture.stages,
		Receipts: fixture.receipts, Tools: fixture.tools, Finalizer: fixture.finalizer,
		Clock: foundation.FixedClock{Value: fixture.clock},
	})
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

func workspaceAnalysisReadEvidenceSearchOutput(hitCount int) json.RawMessage {
	items := make([]map[string]any, hitCount)
	for index := range items {
		items[index] = map[string]any{
			"evidence_ref": workspaceAnalysisReadEvidenceRef(index + 1),
			"rank":         index + 1,
			"snippet":      fmt.Sprintf("bounded snippet %d", index+1),
		}
	}
	raw, _ := json.Marshal(map[string]any{"effective_mode": "hybrid", "items": items, "degradations": []string{}})
	return raw
}

func workspaceAnalysisReadEvidenceSearchSummary(hitCount int) conversationworkflow.WorkspaceAnalysisRetrieveSearchSummary {
	refs := make([]string, hitCount)
	for index := range refs {
		refs[index] = workspaceAnalysisReadEvidenceRef(index + 1)
	}
	return conversationworkflow.WorkspaceAnalysisRetrieveSearchSummary{
		EffectiveMode: "hybrid", EvidenceRefs: refs, HitCount: hitCount, DegradationCodes: []string{},
	}
}

func workspaceAnalysisReadEvidenceToolResult(
	t *testing.T,
	execution workflowapplication.ExecutionContext,
	ordinal int,
	replayed bool,
) toolsapplication.ToolExecutionResult {
	t.Helper()
	arguments := json.RawMessage(`{"evidence_ref":"` + workspaceAnalysisReadEvidenceRef(ordinal) + `"}`)
	requestDocument, err := workspaceAnalysisReadEvidenceRequestDocument(arguments)
	if err != nil {
		t.Fatal(err)
	}
	output := json.RawMessage(fmt.Sprintf(
		`{"evidence_ref":"E%d","content_hash":"%s","truncated":%t,"excerpt":"SOURCE_BODY_CANARY_%d"}`,
		ordinal, strings.Repeat(string(rune('a'+ordinal)), 64), ordinal%2 == 0, ordinal,
	))
	requestHash := sha256.Sum256(requestDocument)
	responseHash := sha256.Sum256(output)
	completedAt := workspaceAnalysisRetrieveNow().Add(time.Duration(5+ordinal) * time.Second)
	tool := workspaceAnalysisReadSourceRef
	inputSchema := workspaceAnalysisReadSourceInputSchema
	outputSchema := workspaceAnalysisReadSourceOutputSchema
	return toolsapplication.ToolExecutionResult{
		Call: toolsdomain.ToolCall{
			ID: workspaceAnalysisReadEvidenceID(10 + ordinal*2), WorkspaceID: execution.WorkspaceID,
			WorkflowRunID: execution.RunID, NodeRunID: execution.NodeRunID, NodeAttemptID: execution.NodeAttemptID,
			CallNo: ordinal, RequestedToolName: tool.Name, Tool: &tool, DefinitionHash: strings.Repeat("3", 64),
			InputSchema: &inputSchema, OutputSchema: &outputSchema, Capability: capability.ReadLocal,
			SideEffectLevel: toolsdomain.SideEffectNone, InvocationPolicy: toolsdomain.InvocationTrustedWorkflowOnly,
			RequestHash: hex.EncodeToString(requestHash[:]), RequestBytes: int64(len(requestDocument)), RequestSummary: json.RawMessage(`{}`),
			ResponseHash: hex.EncodeToString(responseHash[:]), ResponseBytes: int64(len(output)), ResponseSummary: json.RawMessage(`{}`),
			Status: toolsdomain.CallSucceeded, Version: 2, StartedAt: completedAt.Add(-time.Second),
			CompletedAt: &completedAt, DurationMillis: 1000,
		},
		ResultReceiptID: workspaceAnalysisReadEvidenceID(11 + ordinal*2), Output: output,
		UntrustedData: true, Replayed: replayed,
	}
}

func mustEncodeWorkspaceAnalysisRetrieveOutput(
	t *testing.T,
	value conversationworkflow.WorkspaceAnalysisRetrieveEvidenceOutput,
) json.RawMessage {
	t.Helper()
	raw, err := conversationworkflow.EncodeWorkspaceAnalysisRetrieveEvidenceOutput(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type workspaceAnalysisReadEvidenceReceiptFake struct {
	receipt     toolsdomain.ResultReceipt
	operationID foundation.ID
	err         error
	query       toolsapplication.SearchKnowledgeV2PublicationAuthorityQuery
	calls       int
}

func (fake *workspaceAnalysisReadEvidenceReceiptFake) LoadSearchKnowledgeV2PublicationAuthority(
	_ context.Context,
	query toolsapplication.SearchKnowledgeV2PublicationAuthorityQuery,
) (toolsapplication.SearchKnowledgeV2PublicationAuthority, error) {
	fake.calls++
	fake.query = query
	if fake.err != nil {
		return toolsapplication.SearchKnowledgeV2PublicationAuthority{}, fake.err
	}
	return toolsapplication.SearchKnowledgeV2PublicationAuthority{
		OperationID: fake.operationID, Receipt: fake.receipt,
	}, nil
}

type workspaceAnalysisReadEvidenceToolFake struct {
	results      []toolsapplication.ToolExecutionResult
	commands     []toolsapplication.ExecuteWorkspaceAnalysisToolCommand
	deadlines    []time.Time
	hasDeadlines []bool
	failAt       int
	err          error
	calls        int
}

func (fake *workspaceAnalysisReadEvidenceToolFake) ExecuteWorkspaceAnalysisTool(
	ctx context.Context,
	command toolsapplication.ExecuteWorkspaceAnalysisToolCommand,
) (toolsapplication.ToolExecutionResult, error) {
	fake.calls++
	fake.commands = append(fake.commands, command)
	deadline, hasDeadline := ctx.Deadline()
	fake.deadlines = append(fake.deadlines, deadline)
	fake.hasDeadlines = append(fake.hasDeadlines, hasDeadline)
	if fake.failAt == fake.calls {
		return toolsapplication.ToolExecutionResult{}, fake.err
	}
	if fake.calls > len(fake.results) {
		return toolsapplication.ToolExecutionResult{}, errors.New("unexpected read source call")
	}
	return fake.results[fake.calls-1], nil
}

func workspaceAnalysisReadEvidenceID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("95000000-0000-4000-8000-%012d", value))
}

var (
	_ toolsapplication.SearchKnowledgeV2PublicationAuthorityReader = (*workspaceAnalysisReadEvidenceReceiptFake)(nil)
	_ workspaceAnalysisToolExecutor                                = (*workspaceAnalysisReadEvidenceToolFake)(nil)
)
