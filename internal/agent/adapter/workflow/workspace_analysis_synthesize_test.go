package workflow

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	toolsapplication "github.com/CodeZen-Lizhi/zhixu/internal/tools/application"
	toolsdomain "github.com/CodeZen-Lizhi/zhixu/internal/tools/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func TestWorkspaceAnalysisSynthesizeExecutorBuildsIdentitylessInputAndStableOutput(t *testing.T) {
	fixture := newWorkspaceAnalysisSynthesizeFixture(t, 3)
	result, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if fixture.stages.calls != 3 || fixture.inputs.calls != 1 || fixture.context.calls != 1 ||
		fixture.runs.calls != 1 || fixture.authority.calls != 1 || fixture.synthesis.calls != 1 {
		t.Fatalf("calls stages=%d inputs=%d context=%d runs=%d authority=%d synthesis=%d",
			fixture.stages.calls, fixture.inputs.calls, fixture.context.calls, fixture.runs.calls,
			fixture.authority.calls, fixture.synthesis.calls)
	}
	deadlines, err := agentdomain.DeriveWorkspaceAnalysisV1Deadlines(fixture.run.Timeouts)
	if err != nil {
		t.Fatal(err)
	}
	if !fixture.synthesis.hasDeadline || !fixture.synthesis.deadline.Equal(fixture.clock.Add(deadlines.SynthesizeAnswerDeadline())) {
		t.Fatalf("synthesis deadline=%s present=%t", fixture.synthesis.deadline, fixture.synthesis.hasDeadline)
	}
	request := fixture.synthesis.request
	if request.AnalysisRunID != fixture.run.ID || request.AnswerID != fixture.root.AnswerID ||
		request.Identity.NodeAttemptID != fixture.execution.NodeAttemptID || request.AttemptNo != fixture.execution.AttemptNo ||
		request.Identity.LeaseFence != int64(fixture.execution.AttemptNo) ||
		request.PromptRef != WorkspaceAnalysisSynthesisPromptRef() || request.ProfileRef != DefaultProfileRef() ||
		!reflect.DeepEqual(request.AllowedEvidenceRefs, []string{"E1", "E2", "E3"}) {
		t.Fatalf("synthesis request=%+v", request)
	}
	for _, forbidden := range []string{
		string(fixture.execution.WorkspaceID), string(fixture.execution.RunID), string(fixture.run.ID),
		string(fixture.authority.result.SearchReceipt.ID), fixture.authority.result.SearchReceipt.OutputHash,
		"citation_id", "index_version_id", "chunk_id", "source_version_id", "source_span_id",
		"private_binding", "private/path.go",
	} {
		if strings.Contains(string(request.Input), forbidden) {
			t.Fatalf("model input leaked %q: %s", forbidden, request.Input)
		}
	}
	var input workspaceAnalysisSynthesisInput
	if err := json.Unmarshal(request.Input, &input); err != nil {
		t.Fatal(err)
	}
	if !input.UntrustedData || input.Question != fixture.question.Question.Request.QuestionText ||
		len(input.Evidence) != 3 || input.Evidence[0].EvidenceRef != "E1" ||
		input.Evidence[0].Excerpt != "OPENED_EVIDENCE_1" || input.Search.HitCount != 3 ||
		input.GitStatus != fixture.inspect.GitStatus {
		t.Fatalf("model input=%+v", input)
	}

	decoded, err := conversationworkflow.DecodeWorkspaceAnalysisSynthesisOutput(result.Output)
	if err != nil {
		t.Fatalf("DecodeWorkspaceAnalysisSynthesisOutput: %v", err)
	}
	want := fixture.synthesis.result
	if decoded.CandidateID != want.Candidate.ID || decoded.CandidateHash != want.Candidate.DocumentHash ||
		decoded.SynthesisModelRunID != want.Run.ID || decoded.SynthesisModelCallID != want.Call.ID ||
		decoded.DraftSessionID != want.Draft.ID || decoded.DraftGeneration != want.Draft.Generation {
		t.Fatalf("output=%+v", decoded)
	}
	if strings.Contains(string(result.Output), "OPENED_EVIDENCE") || strings.Contains(string(result.Output), "answer_markdown") {
		t.Fatalf("node output leaked evidence or candidate body: %s", result.Output)
	}
}

func TestWorkspaceAnalysisSynthesizeExecutorRejectsReceiptDriftBeforeRunner(t *testing.T) {
	fixture := newWorkspaceAnalysisSynthesizeFixture(t, 2)
	fixture.authority.result.ReadSourceReceipts[0].OutputHash = strings.Repeat("f", 64)

	_, err := fixture.executor(t).Execute(context.Background(), fixture.execution)
	if codeOf(err) != string(conversationdomain.WorkspaceAnalysisReceiptInvalid) {
		t.Fatalf("error=%v", err)
	}
	if fixture.synthesis.calls != 0 {
		t.Fatalf("synthesis calls=%d", fixture.synthesis.calls)
	}
}

func TestWorkspaceAnalysisSynthesizeExecutorReplacementReplaysStableCandidateAndDraft(t *testing.T) {
	fixture := newWorkspaceAnalysisSynthesizeFixture(t, 3)
	executor := fixture.executor(t)
	first, err := executor.Execute(context.Background(), fixture.execution)
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	firstPersistent := fixture.synthesis.result
	replacement := fixture.execution
	replacement.NodeAttemptID = workspaceAnalysisSynthesisTestID(90)
	replacement.AttemptNo = 2
	replacement.DispatchNo = 2
	replacement.NodeVersion++
	replacement.LeaseOwner = "worker-synthesis-replacement"
	fixture.synthesis.fixed = &firstPersistent
	fixture.synthesis.result.Replayed = true
	fixture.synthesis.calls = 0

	second, err := executor.Execute(context.Background(), replacement)
	if err != nil {
		t.Fatalf("replacement Execute: %v", err)
	}
	if !bytes.Equal(first.Output, second.Output) || fixture.synthesis.calls != 1 ||
		fixture.synthesis.request.Identity.NodeAttemptID != replacement.NodeAttemptID ||
		fixture.synthesis.request.Identity.LeaseFence != int64(replacement.AttemptNo) {
		t.Fatalf("replacement output/call drifted first=%s second=%s request=%+v calls=%d",
			first.Output, second.Output, fixture.synthesis.request, fixture.synthesis.calls)
	}
}

type workspaceAnalysisSynthesizeFixture struct {
	execution workflowapplication.ExecutionContext
	root      conversationworkflow.WorkspaceAnalysisInput
	inspect   conversationworkflow.WorkspaceAnalysisInspectOutput
	retrieve  conversationworkflow.WorkspaceAnalysisRetrieveEvidenceOutput
	read      conversationworkflow.WorkspaceAnalysisReadEvidenceOutput
	question  conversationapplication.QuestionExecutionContext
	run       agentdomain.WorkspaceAnalysisRun
	inputs    *workspaceAnalysisRetrieveInputFake
	context   *workspaceAnalysisRetrieveContextFake
	runs      *workspaceAnalysisRetrieveRunFake
	stages    *workspaceAnalysisSynthesisStageFake
	authority *workspaceAnalysisSynthesisAuthorityFake
	synthesis *workspaceAnalysisSynthesisRunnerFake
	finalizer *workspaceAnalysisFinalizerFake
	clock     time.Time
}

func newWorkspaceAnalysisSynthesizeFixture(t *testing.T, count int) *workspaceAnalysisSynthesizeFixture {
	t.Helper()
	readFixture := newWorkspaceAnalysisReadEvidenceFixture(t, count)
	if count < 1 || count > 3 {
		t.Fatalf("invalid synthesis evidence count %d", count)
	}
	definition := conversationworkflow.RegisteredWorkspaceAnalysisDefinition()
	node, found := workspaceAnalysisNode(definition, conversationworkflow.WorkspaceAnalysisNodeSynthesizeAnswer)
	if !found {
		t.Fatal("synthesize_answer node missing")
	}
	execution := readFixture.execution
	execution.NodeKey = node.Key
	execution.NodeKind = node.Kind
	execution.NodeRunID = workspaceAnalysisSynthesisTestID(1)
	execution.NodeAttemptID = workspaceAnalysisSynthesisTestID(2)
	execution.NodeVersion = 2
	execution.AttemptNo = 1
	execution.DispatchNo = 1
	execution.RetryNo = 0
	execution.LeaseOwner = "worker-synthesis"

	inspect := conversationworkflow.WorkspaceAnalysisInspectOutput{
		SchemaVersion: conversationworkflow.WorkspaceAnalysisOutputSchemaVersion,
		ToolCallID:    readFixture.predecessor.GitToolCallID, ToolReceiptID: readFixture.predecessor.GitToolReceiptID,
		ToolReceiptHash: readFixture.predecessor.GitToolReceiptHash,
		GitStatus: conversationworkflow.WorkspaceAnalysisGitStatusSummary{
			Branch: "main", Head: strings.Repeat("a", 40), ObjectFormat: "sha1", Clean: false,
			StagedCount: 1,
		},
	}
	readReceipts, evidence := workspaceAnalysisSynthesisReadReceipts(t, readFixture.search, count)
	reads := make([]conversationworkflow.WorkspaceAnalysisReadEvidenceReceipt, count)
	for index, receipt := range readReceipts {
		reads[index] = conversationworkflow.WorkspaceAnalysisReadEvidenceReceipt{
			EvidenceRef: evidence[index].EvidenceRef, ToolCallID: receipt.ToolCallID,
			ToolReceiptID: receipt.ID, ToolReceiptHash: receipt.OutputHash,
		}
	}
	readOutput := conversationworkflow.WorkspaceAnalysisReadEvidenceOutput{
		SchemaVersion: conversationworkflow.WorkspaceAnalysisOutputSchemaVersion, Reads: reads,
	}
	execution.Input = mustEncodeWorkspaceAnalysisReadOutput(t, readOutput)
	inspectRaw := mustEncodeWorkspaceAnalysisInspectOutput(t, inspect)
	retrieveRaw := mustEncodeWorkspaceAnalysisRetrieveOutput(t, readFixture.predecessor)
	readRaw := append(json.RawMessage(nil), execution.Input...)
	stage := &workspaceAnalysisSynthesisStageFake{outputs: map[string]json.RawMessage{
		conversationworkflow.WorkspaceAnalysisNodeInspectWorkspace: inspectRaw,
		conversationworkflow.WorkspaceAnalysisNodeRetrieveEvidence: retrieveRaw,
		conversationworkflow.WorkspaceAnalysisNodeReadEvidence:     readRaw,
	}}
	authority := toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthority{
		WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID, AnalysisRunID: readFixture.run.ID,
		EvidenceRefs: append([]string{}, readFixture.searchSelectedRefs(t)...), SearchReceipt: readFixture.search,
		ReadSourceReceipts: readReceipts, Evidence: evidence,
	}
	synthesis := &workspaceAnalysisSynthesisRunnerFake{}
	fixture := &workspaceAnalysisSynthesizeFixture{
		execution: execution, root: readFixture.root, inspect: inspect, retrieve: readFixture.predecessor,
		read: readOutput, question: readFixture.question, run: readFixture.run,
		inputs: readFixture.inputs, context: readFixture.context,
		runs: readFixture.runs, stages: stage,
		authority: &workspaceAnalysisSynthesisAuthorityFake{result: authority}, synthesis: synthesis,
		finalizer: &workspaceAnalysisFinalizerFake{}, clock: workspaceAnalysisRetrieveNow(),
	}
	synthesis.build = func(request agentapplication.WorkspaceAnalysisSynthesisRequest) agentapplication.WorkspaceAnalysisSynthesisResult {
		return workspaceAnalysisSynthesisResultFixture(t, execution, request, fixture.clock)
	}
	return fixture
}

func (fixture *workspaceAnalysisSynthesizeFixture) executor(t *testing.T) *WorkspaceAnalysisSynthesizeExecutor {
	t.Helper()
	executor, err := NewWorkspaceAnalysisSynthesizeExecutor(WorkspaceAnalysisSynthesizeExecutorDependencies{
		Context: fixture.context, Runs: fixture.runs, Inputs: fixture.inputs, Stages: fixture.stages,
		Evidence: fixture.authority, Synthesis: fixture.synthesis, Finalizer: fixture.finalizer,
		Clock: foundation.FixedClock{Value: fixture.clock},
	})
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

func (fixture *workspaceAnalysisReadEvidenceFixture) searchSelectedRefs(t *testing.T) []string {
	t.Helper()
	refs, err := toolsdomain.SearchKnowledgeV2ReceiptSelectedRefs(fixture.search)
	if err != nil {
		t.Fatal(err)
	}
	return refs
}

type workspaceAnalysisSynthesisCitationIdentity struct {
	ChunkID         foundation.ID `json:"chunk_id"`
	CitationID      string        `json:"citation_id"`
	ContentHash     string        `json:"content_hash"`
	EvidenceRef     string        `json:"evidence_ref"`
	IndexVersionID  foundation.ID `json:"index_version_id"`
	SourceSpanID    foundation.ID `json:"source_span_id"`
	SourceVersionID foundation.ID `json:"source_version_id"`
}

func workspaceAnalysisSynthesisReadReceipts(
	t *testing.T,
	search toolsdomain.ResultReceipt,
	count int,
) ([]toolsdomain.ResultReceipt, []toolsdomain.ReadSourceV3ReceiptEvidence) {
	t.Helper()
	var binding struct {
		Items        []workspaceAnalysisSynthesisCitationIdentity `json:"items"`
		SelectedRefs []string                                     `json:"selected_refs"`
	}
	if search.PrivateBinding == nil || json.Unmarshal(search.PrivateBinding.Document, &binding) != nil || len(binding.SelectedRefs) != count {
		t.Fatal("search receipt fixture binding is invalid")
	}
	receipts := make([]toolsdomain.ResultReceipt, count)
	evidence := make([]toolsdomain.ReadSourceV3ReceiptEvidence, count)
	contract, found := toolsdomain.WorkspaceAnalysisResultReceiptContract(workspaceAnalysisReadSourceRef)
	if !found {
		t.Fatal("read source contract missing")
	}
	for index := 0; index < count; index++ {
		identity := binding.Items[index]
		evidence[index] = toolsdomain.ReadSourceV3ReceiptEvidence{
			EvidenceRef: identity.EvidenceRef, Excerpt: fmt.Sprintf("OPENED_EVIDENCE_%d", index+1), Truncated: index%2 == 1,
		}
		output, err := json.Marshal(map[string]any{
			"content_hash": identity.ContentHash, "evidence_ref": identity.EvidenceRef,
			"excerpt": evidence[index].Excerpt, "truncated": evidence[index].Truncated,
		})
		if err != nil {
			t.Fatal(err)
		}
		private, err := json.Marshal(map[string]any{
			"chunk_id": identity.ChunkID, "citation_id": identity.CitationID, "content_hash": identity.ContentHash,
			"evidence_ref": identity.EvidenceRef, "index_version_id": identity.IndexVersionID,
			"search_receipt_hash": search.OutputHash, "search_receipt_id": search.ID,
			"source_span_id": identity.SourceSpanID, "source_version_id": identity.SourceVersionID,
		})
		if err != nil {
			t.Fatal(err)
		}
		outputHash := sha256.Sum256(output)
		privateHash := sha256.Sum256(private)
		receipts[index] = toolsdomain.ResultReceipt{
			ID:          workspaceAnalysisReadEvidenceID(11 + (index+1)*2),
			ToolCallID:  workspaceAnalysisReadEvidenceID(10 + (index+1)*2),
			WorkspaceID: search.WorkspaceID, WorkflowRunID: search.WorkflowRunID,
			NodeRunID: workspaceAnalysisReadEvidenceID(1), NodeAttemptID: workspaceAnalysisReadEvidenceID(2),
			Tool: workspaceAnalysisReadSourceRef, OutputSchema: contract.OutputSchema,
			DefinitionHash: strings.Repeat("3", 64), PersistencePolicy: toolsdomain.ResultPersistenceCanonical,
			MaxOutputBytes: contract.MaxOutputBytes, MaxPrivateBindingBytes: contract.MaxPrivateBindingBytes,
			Output: output, OutputHash: hex.EncodeToString(outputHash[:]), OutputBytes: int64(len(output)),
			PrivateBinding: &toolsdomain.ResultReceiptPrivateBinding{
				Schema: contract.PrivateBindingSchema, Document: private,
				Hash: hex.EncodeToString(privateHash[:]), Bytes: int64(len(private)),
			},
			CreatedAt: workspaceAnalysisRetrieveNow().Add(time.Duration(index+1) * time.Second),
		}
	}
	return receipts, evidence
}

func workspaceAnalysisSynthesisResultFixture(
	t *testing.T,
	execution workflowapplication.ExecutionContext,
	request agentapplication.WorkspaceAnalysisSynthesisRequest,
	now time.Time,
) agentapplication.WorkspaceAnalysisSynthesisResult {
	t.Helper()
	completedAt := now.Add(2 * time.Second)
	runID := workspaceAnalysisSynthesisTestID(60)
	result := agentdomain.WorkspaceAnalysisCandidateResult{
		ResultType: agentdomain.ResultTypeWorkspaceAnalysisCandidate,
		SchemaID:   agentdomain.WorkspaceAnalysisCandidateSchemaID, SchemaVersion: "1", ModelRunRef: runID,
		Payload: agentdomain.WorkspaceAnalysisCandidatePayload{
			AnswerMarkdown: "Grounded answer [E1].", CitationRefs: []string{"E1"}, ProposalSuggestion: nil,
		},
	}
	document, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(document)
	hash := hex.EncodeToString(digest[:])
	model := agentdomain.ModelRef{
		AdapterName: "openai-compatible", AdapterVersion: "v1", ModelID: "workspace-synthesis", ModelVersion: "2026-08-16",
	}
	schema := agentdomain.SchemaRef{ID: agentdomain.WorkspaceAnalysisCandidateSchemaID, Version: "1"}
	usage := agentdomain.TokenUsage{InputTokens: 100, OutputTokens: 40, TotalTokens: 140}
	run := agentdomain.ModelRun{
		ID: runID, WorkspaceID: execution.WorkspaceID, WorkflowRunID: execution.RunID,
		NodeRunID: execution.NodeRunID, NodeAttemptID: execution.NodeAttemptID,
		ModelSettingsRevision: cloneWorkspaceAnalysisInt64(request.ModelSettingsRevision), Model: model,
		Profile: request.ProfileRef, Prompt: request.PromptRef, Schema: schema, ReducedSchema: schema,
		Retrieval: request.Retrieval, Status: agentdomain.ModelRunSucceeded,
		FinalResultType: agentdomain.ResultTypeWorkspaceAnalysisAnswer, Version: 2,
		CreatedAt: now, UpdatedAt: completedAt, CompletedAt: &completedAt,
	}
	call := agentdomain.ModelCall{
		ID: workspaceAnalysisSynthesisTestID(61), ModelRunID: run.ID, CallNo: 1,
		Phase: agentdomain.ModelCallAnswer, Model: model, Profile: run.Profile, Prompt: run.Prompt, Schema: schema,
		MaxOutputTokens: int(agentapplication.WorkspaceAnalysisV1SynthesisMaxOutputTokens),
		Status:          agentdomain.ModelCallSucceeded, RequestHash: strings.Repeat("1", 64), RequestBytes: 100,
		ResponseHash: hash, ResponseBytes: int64(len(document)), Usage: usage, LatencyMillis: 2000,
		Version: 2, StartedAt: now, CompletedAt: &completedAt,
	}
	candidate := agentdomain.WorkspaceAnalysisCandidate{
		ID: workspaceAnalysisSynthesisTestID(62), WorkspaceID: execution.WorkspaceID,
		AnalysisRunID: request.AnalysisRunID, AnswerID: request.AnswerID,
		SynthesisOperationID: workspaceAnalysisSynthesisTestID(63), NodeAttemptID: execution.NodeAttemptID,
		SynthesisModelRunID: run.ID, SchemaID: schema.ID, SchemaVersion: 1,
		Document: document, DocumentHash: hash, DocumentBytes: int64(len(document)), CreatedAt: completedAt,
	}
	draft := agentapplication.DraftStreamSession{
		ID: workspaceAnalysisSynthesisTestID(64),
		Binding: agentapplication.DraftStreamBinding{
			WorkspaceID: execution.WorkspaceID, AnswerID: request.AnswerID, WorkflowRunID: execution.RunID,
			NodeRunID: execution.NodeRunID, NodeAttemptID: execution.NodeAttemptID,
			AttemptNo: execution.AttemptNo, LeaseOwner: execution.LeaseOwner,
		},
		Generation: 1, Status: agentapplication.DraftStreamCompleted, NextSeq: 1,
		TotalBytes: len(result.Payload.AnswerMarkdown), ExpiresAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: completedAt,
	}
	return agentapplication.WorkspaceAnalysisSynthesisResult{
		Candidate: candidate, CandidateResult: result, Run: run, Call: call, Draft: draft, Usage: usage,
	}
}

type workspaceAnalysisSynthesisStageFake struct {
	outputs map[string]json.RawMessage
	calls   int
}

func (fake *workspaceAnalysisSynthesisStageFake) GetSucceededNodeOutput(
	_ context.Context,
	_, _ foundation.ID,
	nodeKey string,
) (json.RawMessage, error) {
	fake.calls++
	return append(json.RawMessage(nil), fake.outputs[nodeKey]...), nil
}

type workspaceAnalysisSynthesisAuthorityFake struct {
	result toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthority
	query  toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthorityQuery
	calls  int
}

func (fake *workspaceAnalysisSynthesisAuthorityFake) LoadWorkspaceAnalysisSynthesisEvidence(
	_ context.Context,
	query toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthorityQuery,
) (toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthority, error) {
	fake.calls++
	fake.query = query
	return fake.result, nil
}

type workspaceAnalysisSynthesisRunnerFake struct {
	request     agentapplication.WorkspaceAnalysisSynthesisRequest
	result      agentapplication.WorkspaceAnalysisSynthesisResult
	fixed       *agentapplication.WorkspaceAnalysisSynthesisResult
	build       func(agentapplication.WorkspaceAnalysisSynthesisRequest) agentapplication.WorkspaceAnalysisSynthesisResult
	err         error
	deadline    time.Time
	hasDeadline bool
	calls       int
}

func (fake *workspaceAnalysisSynthesisRunnerFake) Run(
	ctx context.Context,
	request agentapplication.WorkspaceAnalysisSynthesisRequest,
) (agentapplication.WorkspaceAnalysisSynthesisResult, error) {
	fake.calls++
	fake.request = request
	fake.deadline, fake.hasDeadline = ctx.Deadline()
	if fake.err != nil {
		return agentapplication.WorkspaceAnalysisSynthesisResult{}, fake.err
	}
	if fake.fixed != nil {
		result := *fake.fixed
		result.Replayed = true
		fake.result = result
		return result, nil
	}
	fake.result = fake.build(request)
	return fake.result, nil
}

func mustEncodeWorkspaceAnalysisReadOutput(
	t *testing.T,
	value conversationworkflow.WorkspaceAnalysisReadEvidenceOutput,
) json.RawMessage {
	t.Helper()
	raw, err := conversationworkflow.EncodeWorkspaceAnalysisReadEvidenceOutput(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func workspaceAnalysisSynthesisTestID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("97000000-0000-4000-8000-%012d", value))
}

var (
	_ workspaceAnalysisStageOutputReader                                 = (*workspaceAnalysisSynthesisStageFake)(nil)
	_ toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthorityReader = (*workspaceAnalysisSynthesisAuthorityFake)(nil)
	_ workspaceAnalysisSynthesisRunner                                   = (*workspaceAnalysisSynthesisRunnerFake)(nil)
)
