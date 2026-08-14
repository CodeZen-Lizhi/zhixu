package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agenteino "github.com/CodeZen-Lizhi/zhixu/internal/agent/adapter/eino"
	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

const (
	testWorkspaceID   foundation.ID = "82000000-0000-4000-8000-000000000001"
	testWorkflowRunID foundation.ID = "82000000-0000-4000-8000-000000000002"
	testNodeRunID     foundation.ID = "82000000-0000-4000-8000-000000000003"
	testAttemptID     foundation.ID = "82000000-0000-4000-8000-000000000004"
	testIndexID       foundation.ID = "82000000-0000-4000-8000-000000000005"
	testModelRunID    foundation.ID = "82000000-0000-4000-8000-000000000006"
	testCallID        foundation.ID = "82000000-0000-4000-8000-000000000007"
	testCallID2       foundation.ID = "82000000-0000-4000-8000-000000000008"
	testCallID3       foundation.ID = "82000000-0000-4000-8000-000000000009"
	testCandidateID   foundation.ID = "82000000-0000-4000-8000-000000000010"
	testExistingID    foundation.ID = "82000000-0000-4000-8000-000000000011"
	testChunkID       foundation.ID = "82000000-0000-4000-8000-000000000012"
	testSourceID      foundation.ID = "82000000-0000-4000-8000-000000000013"
	testSpanID        foundation.ID = "82000000-0000-4000-8000-000000000014"
	testChunkID2      foundation.ID = "82000000-0000-4000-8000-000000000015"
	testSourceID2     foundation.ID = "82000000-0000-4000-8000-000000000016"
	testSpanID2       foundation.ID = "82000000-0000-4000-8000-000000000017"
)

func TestExecutorPersistsRunCallAndValidatesServerModelRunRef(t *testing.T) {
	modelRef := testModelRef()
	responseDocument := relationDocument(t, testModelRunID, knowledgedomain.AssessmentNew)
	model := agentapplication.NewDeterministicChatModel(agentapplication.DeterministicChatStep{Response: agentapplication.ChatResponse{
		Model: modelRef, Content: responseDocument, Usage: agentdomain.TokenUsage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5},
	}})
	repository := &workflowRepository{}
	executor := newWorkflowExecutor(t, model, repository)

	result, err := executor.Execute(context.Background(), testExecution(t, validInput()))
	if err != nil {
		t.Fatal(err)
	}
	if repository.run.Status != agentdomain.ModelRunSucceeded || repository.run.FinalResultType != agentdomain.ResultTypeRelationAssessment || len(repository.calls) != 1 {
		t.Fatalf("run=%+v calls=%+v", repository.run, repository.calls)
	}
	call := repository.calls[0]
	if call.Status != agentdomain.ModelCallSucceeded || call.RequestHash == "" || call.ResponseHash == "" || call.RequestBytes == 0 || call.ResponseBytes == 0 {
		t.Fatalf("call=%+v", call)
	}
	var output RelationAssessmentWorkflowOutput
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	if output.ModelRunRef != testModelRunID || output.ResultType != agentdomain.ResultTypeRelationAssessment ||
		output.Phase != agentdomain.ModelCallInitial || output.CallCount != 1 || string(output.BusinessJSON) != string(responseDocument) ||
		output.Action.Decision != knowledgedomain.AssessmentDecisionProposeNewClaim {
		t.Fatalf("output=%+v business=%s", output, output.BusinessJSON)
	}
	if strings.Contains(string(result.Output), "bounded ZHIXU Relation Assessment component") || strings.Contains(string(result.Output), "UNTRUSTED TASK INPUT") {
		t.Fatalf("workflow output leaked prompt content: %s", result.Output)
	}
	calls := model.Calls()
	if len(calls) != 1 || !strings.Contains(calls[0].Messages[len(calls[0].Messages)-1].Content, string(testModelRunID)) ||
		!strings.Contains(calls[0].Messages[len(calls[0].Messages)-1].Content, "server-opened-candidate-1") {
		t.Fatalf("model did not receive server run id: %+v", calls)
	}
}

func TestExecutorRetainsConfiguredEinoStructuredScheduler(t *testing.T) {
	scheduler := newTrackingEinoStructuredScheduler(t)
	executor := newWorkflowExecutorWithScheduler(t, agentapplication.NewDeterministicChatModel(), &workflowRepository{}, scheduler)
	if executor.scheduler != scheduler {
		t.Fatalf("scheduler=%T want=%T", executor.scheduler, scheduler)
	}
}

func TestExecutorExecutesRelationAssessmentThroughEinoStructuredScheduler(t *testing.T) {
	scheduler := newTrackingEinoStructuredScheduler(t)
	responseDocument := relationDocument(t, testModelRunID, knowledgedomain.AssessmentNew)
	model := agentapplication.NewDeterministicChatModel(agentapplication.DeterministicChatStep{Response: agentapplication.ChatResponse{
		Model: testModelRef(), Content: responseDocument, Usage: agentdomain.TokenUsage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5},
	}})
	repository := &workflowRepository{}
	executor := newWorkflowExecutorWithScheduler(t, model, repository, scheduler)

	result, err := executor.Execute(context.Background(), testExecution(t, validInput()))
	if err != nil {
		t.Fatal(err)
	}
	var output RelationAssessmentWorkflowOutput
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	calls := model.Calls()
	if len(calls) != 1 || calls[0].Phase != agentdomain.ModelCallInitial || output.Phase != agentdomain.ModelCallInitial || output.CallCount != 1 {
		t.Fatalf("calls=%+v output=%+v", calls, output)
	}
	if scheduler.calls.Load() != 1 || repository.run.Status != agentdomain.ModelRunSucceeded || len(repository.calls) != 1 ||
		output.Action.Decision != knowledgedomain.AssessmentDecisionProposeNewClaim || string(output.BusinessJSON) != string(responseDocument) {
		t.Fatalf("scheduler_calls=%d run=%+v persistedCalls=%+v output=%+v", scheduler.calls.Load(), repository.run, repository.calls, output)
	}
}

func TestExecutorRejectsMismatchedRunRefAndFinalizesFailure(t *testing.T) {
	wrongRun := foundation.ID("82000000-0000-4000-8000-000000000099")
	model := agentapplication.NewDeterministicChatModel(agentapplication.DeterministicChatStep{Response: agentapplication.ChatResponse{
		Model: testModelRef(), Content: relationDocument(t, wrongRun, knowledgedomain.AssessmentNew),
		Usage: agentdomain.TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	}})
	repository := &workflowRepository{}
	executor := newWorkflowExecutor(t, model, repository)
	_, err := executor.Execute(context.Background(), testExecution(t, validInput()))
	if codeOf(err) != "AGENT_RELATION_OUTPUT_MISMATCH" || repository.run.Status != agentdomain.ModelRunFailed || repository.run.FinalErrorCode != "AGENT_RELATION_OUTPUT_MISMATCH" {
		t.Fatalf("err=%v run=%+v", err, repository.run)
	}
}

func TestExecutorThirdCallUsesStrictReducedRelationSchema(t *testing.T) {
	usage := agentdomain.TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}
	model := agentapplication.NewDeterministicChatModel(
		agentapplication.DeterministicChatStep{Response: agentapplication.ChatResponse{Model: testModelRef(), Content: []byte("{}"), Usage: usage}},
		agentapplication.DeterministicChatStep{Response: agentapplication.ChatResponse{Model: testModelRef(), Content: []byte("{}"), Usage: usage}},
		agentapplication.DeterministicChatStep{Response: agentapplication.ChatResponse{Model: testModelRef(), Content: relationDocument(t, testModelRunID, knowledgedomain.AssessmentLowConfidence), Usage: usage}},
	)
	repository := &workflowRepository{}
	executor := newWorkflowExecutor(t, model, repository)
	result, err := executor.Execute(context.Background(), testExecution(t, validInputWithExisting()))
	if err != nil {
		t.Fatal(err)
	}
	var output RelationAssessmentWorkflowOutput
	if err := json.Unmarshal(result.Output, &output); err != nil {
		t.Fatal(err)
	}
	calls := model.Calls()
	if output.Phase != agentdomain.ModelCallReduced || output.CallCount != 3 || len(calls) != 3 || len(repository.calls) != 3 ||
		calls[2].Phase != agentdomain.ModelCallReduced || calls[2].SchemaRef.ID != agentapplication.RelationAssessmentReducedSchemaID ||
		len(calls[2].OutputSchema) >= len(calls[0].OutputSchema) {
		t.Fatalf("output=%+v calls=%+v repository=%+v", output, calls, repository.calls)
	}
}

func TestExecutorMarksRunUnknownWhenCallCompletionIsUnknown(t *testing.T) {
	model := agentapplication.NewDeterministicChatModel(agentapplication.DeterministicChatStep{Response: agentapplication.ChatResponse{
		Model: testModelRef(), Content: relationDocument(t, testModelRunID, knowledgedomain.AssessmentNew),
		Usage: agentdomain.TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	}})
	repository := &workflowRepository{failCallCompletion: true}
	executor := newWorkflowExecutor(t, model, repository)
	_, err := executor.Execute(context.Background(), testExecution(t, validInput()))
	if codeOf(err) != agentapplication.ErrorCodeModelCallPersistenceUnknown || repository.run.Status != agentdomain.ModelRunUnknown ||
		repository.run.FinalErrorCode != agentapplication.ErrorCodeModelCallPersistenceUnknown || model.CallCount() != 1 {
		t.Fatalf("err=%v run=%+v calls=%d", err, repository.run, model.CallCount())
	}
}

func TestExecutorDoesNotHideFailureFinalizationUnknown(t *testing.T) {
	providerErr := foundation.NewError(foundation.ErrorNonRetryableFailure, "MODEL_REJECTED", false, errors.New("private provider body"))
	model := agentapplication.NewDeterministicChatModel(agentapplication.DeterministicChatStep{Err: providerErr})
	repository := &workflowRepository{failRunFinalization: true}
	executor := newWorkflowExecutor(t, model, repository)
	_, err := executor.Execute(context.Background(), testExecution(t, validInput()))
	if codeOf(err) != ErrorCodeRunFinalizationUnknown || strings.Contains(err.Error(), "private provider body") {
		t.Fatalf("err=%v", err)
	}
}

func TestWorkflowSchemasAreTaskSpecificReducedAndPairBound(t *testing.T) {
	catalog, err := NewRuntimeCatalog(CatalogOptions{Model: testModelRef(), Timeout: time.Second, MaxOutputTokens: 128})
	if err != nil {
		t.Fatal(err)
	}
	primaryRef := agentdomain.SchemaRef{ID: agentdomain.RelationAssessmentSchemaID, Version: agentdomain.OutputSchemaVersionV1}
	reducedRef := agentdomain.SchemaRef{ID: agentapplication.RelationAssessmentReducedSchemaID, Version: agentdomain.OutputSchemaVersionV1}
	snapshot, err := catalog.Snapshot(DefaultPromptRef(), primaryRef, reducedRef, DefaultProfileRef())
	if err != nil {
		t.Fatal(err)
	}
	var primary, reduced map[string]any
	if json.Unmarshal(snapshot.Schema.JSONSchema, &primary) != nil || json.Unmarshal(snapshot.ReducedSchema.JSONSchema, &reduced) != nil {
		t.Fatal("schema documents are not valid json")
	}
	primaryAssessment := schemaProperty(primary, "payload", "assessment")
	reducedAssessment := schemaProperty(reduced, "payload", "assessment")
	if len(primaryAssessment["enum"].([]any)) != 5 || reducedAssessment["const"] != "LOW_CONFIDENCE" || len(snapshot.ReducedSchema.JSONSchema) >= len(snapshot.Schema.JSONSchema) {
		t.Fatalf("primary=%s reduced=%s", snapshot.Schema.JSONSchema, snapshot.ReducedSchema.JSONSchema)
	}
	if _, err := snapshot.ReducedSchema.Decode(relationDocument(t, testModelRunID, knowledgedomain.AssessmentComplementary)); err == nil {
		t.Fatal("reduced schema accepted a non-low-confidence assessment")
	}
	if _, err := snapshot.ReducedSchema.Decode(relationDocument(t, testModelRunID, knowledgedomain.AssessmentLowConfidence)); err != nil {
		t.Fatalf("safe reduced result rejected: %v", err)
	}

	tests := []RelationAssessmentWorkflowInput{validInput(), validInput(), validInput()}
	tests[0].ReducedSchemaRef = agentdomain.SchemaRef{ID: agentdomain.RefusalSchemaID, Version: agentdomain.OutputSchemaVersionV1}
	tests[1].SchemaRef = agentdomain.SchemaRef{ID: agentdomain.RAGAnswerSchemaID, Version: agentdomain.OutputSchemaVersionV1}
	tests[1].ReducedSchemaRef = agentdomain.SchemaRef{ID: agentdomain.RefusalSchemaID, Version: agentdomain.OutputSchemaVersionV1}
	tests[2].SchemaRef = agentdomain.SchemaRef{ID: agentdomain.FaithfulnessReviewSchemaID, Version: agentdomain.OutputSchemaVersionV1}
	tests[2].ReducedSchemaRef = agentdomain.SchemaRef{ID: agentdomain.RefusalSchemaID, Version: agentdomain.OutputSchemaVersionV1}
	for index, input := range tests {
		encoded, _ := json.Marshal(input)
		if _, err := DecodeRelationAssessmentWorkflowInput(encoded); codeOf(err) != ErrorCodeInputInvalid {
			t.Fatalf("unsafe pair[%d] err=%v", index, err)
		}
	}
}

func TestRuntimeCatalogPublishesFourIndependentTaskPayloadSchemas(t *testing.T) {
	catalog, err := NewRuntimeCatalog(CatalogOptions{Model: testModelRef(), Timeout: time.Second, MaxOutputTokens: 128})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		ref        agentdomain.SchemaRef
		properties int
	}{
		{agentdomain.SchemaRef{ID: agentdomain.RelationAssessmentSchemaID, Version: agentdomain.OutputSchemaVersionV1}, 8},
		{agentdomain.SchemaRef{ID: agentdomain.RAGAnswerSchemaID, Version: agentdomain.OutputSchemaVersionV1}, 5},
		{agentdomain.SchemaRef{ID: agentdomain.RefusalSchemaID, Version: agentdomain.OutputSchemaVersionV1}, 5},
		{agentdomain.SchemaRef{ID: agentdomain.FaithfulnessReviewSchemaID, Version: agentdomain.OutputSchemaVersionV1}, 3},
	}
	for _, test := range tests {
		snapshot, err := catalog.Snapshot(DefaultPromptRef(), test.ref, test.ref, DefaultProfileRef())
		if err != nil {
			t.Fatalf("schema=%s err=%v", test.ref.ID, err)
		}
		var document map[string]any
		if err := json.Unmarshal(snapshot.Schema.JSONSchema, &document); err != nil {
			t.Fatal(err)
		}
		if document["additionalProperties"] != false {
			t.Fatalf("schema=%s root is not strict", test.ref.ID)
		}
		payload := schemaProperty(document, "payload")
		properties := payload["properties"].(map[string]any)
		if payload["additionalProperties"] != false || len(properties) != test.properties || len(payload["required"].([]any)) != test.properties {
			t.Fatalf("schema=%s payload=%+v", test.ref.ID, payload)
		}
	}
}

func TestRuntimeCatalogFreezesTrustedRelationSemanticsAndSafetyRules(t *testing.T) {
	catalog, err := NewRuntimeCatalog(CatalogOptions{Model: testModelRef(), Timeout: time.Second, MaxOutputTokens: 128})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := catalog.Snapshot(
		DefaultPromptRef(),
		agentdomain.SchemaRef{ID: agentdomain.RelationAssessmentSchemaID, Version: agentdomain.OutputSchemaVersionV1},
		agentdomain.SchemaRef{ID: agentapplication.RelationAssessmentReducedSchemaID, Version: agentdomain.OutputSchemaVersionV1},
		DefaultProfileRef(),
	)
	if err != nil {
		t.Fatal(err)
	}
	trusted := snapshot.Prompt.System + "\n" + snapshot.Prompt.InitialInstruction + "\n" + snapshot.Prompt.ReducedInstruction
	for _, required := range []string{
		"NEW means", "COMPLEMENTARY means", "DUPLICATE means", "CONFLICT means", "LOW_CONFIDENCE means",
		"never chooses a winner", "never use outside knowledge", "conflict_disclosure", "LOW_CONFIDENCE document",
	} {
		if !strings.Contains(trusted, required) {
			t.Fatalf("trusted relation prompt lacks %q: %s", required, trusted)
		}
	}
}

func TestDecodeInputRejectsUnknownAndTrailingValues(t *testing.T) {
	encoded, _ := json.Marshal(validInput())
	withUnknown := append(encoded[:len(encoded)-1], []byte(",\"unknown\":true}")...)
	if _, err := DecodeRelationAssessmentWorkflowInput(withUnknown); err == nil {
		t.Fatal("unknown field accepted")
	}
	if _, err := DecodeRelationAssessmentWorkflowInput(append(encoded, []byte(" {}")...)); err == nil {
		t.Fatal("trailing document accepted")
	}
	withCallerExcerpt := strings.Replace(string(encoded), `"citation":`, `"excerpt":"tampered","citation":`, 1)
	if _, err := DecodeRelationAssessmentWorkflowInput([]byte(withCallerExcerpt)); err == nil {
		t.Fatal("caller-controlled evidence excerpt accepted")
	}
	wrongIndex := validInput()
	wrongIndex.Candidate.Evidence[0].Citation.IndexVersionID = "82000000-0000-4000-8000-000000000099"
	encoded, _ = json.Marshal(wrongIndex)
	if _, err := DecodeRelationAssessmentWorkflowInput(encoded); codeOf(err) != ErrorCodeInputInvalid {
		t.Fatalf("wrong index err=%v", err)
	}
}

func TestExecutorRejectsCrossWorkspaceCitationBeforeProvider(t *testing.T) {
	input := validInput()
	input.Candidate.Evidence[0].Citation.WorkspaceID = "82000000-0000-4000-8000-000000000099"
	model := agentapplication.NewDeterministicChatModel()
	repository := &workflowRepository{}
	executor := newWorkflowExecutor(t, model, repository)
	_, err := executor.Execute(context.Background(), testExecution(t, input))
	if codeOf(err) != ErrorCodeInputInvalid || model.CallCount() != 0 || repository.run.Status != agentdomain.ModelRunFailed {
		t.Fatalf("err=%v calls=%d run=%+v", err, model.CallCount(), repository.run)
	}
}

func TestRegisteredDefinitionResolvesThroughFrozenWorkflowRegistries(t *testing.T) {
	executor := newWorkflowExecutor(t, agentapplication.NewDeterministicChatModel(), &workflowRepository{})
	validation, err := workflowapplication.NewValidationCatalog([]int{1}, []workflowdomain.Permission{})
	if err != nil {
		t.Fatal(err)
	}
	executors, err := workflowapplication.NewExecutorRegistry(validation)
	if err != nil {
		t.Fatal(err)
	}
	if err := executors.Register(RelationAssessmentNodeKind, RelationAssessmentInputSchemaVersion, executor); err != nil {
		t.Fatal(err)
	}
	if err := executors.Freeze(); err != nil {
		t.Fatal(err)
	}
	definitions, err := workflowapplication.NewDefinitionRegistry(validation, executors)
	if err != nil {
		t.Fatal(err)
	}
	if err := definitions.Register(RegisteredDefinition()); err != nil {
		t.Fatal(err)
	}
	if err := definitions.Freeze(); err != nil {
		t.Fatal(err)
	}
	resolved, err := definitions.Resolve(RelationAssessmentDefinitionKey, RelationAssessmentDefinitionVersion)
	if err != nil || len(resolved.Graph.Nodes) != 1 || resolved.Graph.Nodes[0].Kind != RelationAssessmentNodeKind {
		t.Fatalf("definition=%+v err=%v", resolved, err)
	}
}

func schemaProperty(document map[string]any, path ...string) map[string]any {
	current := document
	for _, component := range path {
		current = current["properties"].(map[string]any)[component].(map[string]any)
	}
	return current
}

func newWorkflowExecutor(t *testing.T, model agentapplication.ChatModel, repository *workflowRepository) *Executor {
	return newWorkflowExecutorWithScheduler(t, model, repository, nil)
}

func newWorkflowExecutorWithScheduler(
	t *testing.T,
	model agentapplication.ChatModel,
	repository *workflowRepository,
	scheduler agentapplication.StructuredPhaseScheduler,
) *Executor {
	t.Helper()
	if scheduler == nil {
		scheduler = newTrackingEinoStructuredScheduler(t)
	}
	catalog, err := NewRuntimeCatalog(CatalogOptions{Model: testModelRef(), Timeout: time.Second, MaxOutputTokens: 128})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := NewExecutor(ExecutorDependencies{
		Model: model, Scheduler: scheduler, Catalog: catalog, Repository: repository, Knowledge: workflowKnowledgePort{}, Evidence: workflowEvidenceOpener{},
		IDs:   &workflowIDs{values: []foundation.ID{testModelRunID, testCallID, testCallID2, testCallID3}},
		Clock: &workflowClock{next: time.Date(2026, 7, 19, 2, 0, 0, 0, time.UTC)}, Budget: agentapplication.DefaultRunBudget(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

func testExecution(t *testing.T, input RelationAssessmentWorkflowInput) workflowapplication.ExecutionContext {
	t.Helper()
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	return workflowapplication.ExecutionContext{
		WorkspaceID: testWorkspaceID, RunID: testWorkflowRunID, NodeRunID: testNodeRunID, NodeAttemptID: testAttemptID,
		NodeKind: RelationAssessmentNodeKind, InputSchemaVersion: RelationAssessmentInputSchemaVersion, Input: encoded,
	}
}

func validInput() RelationAssessmentWorkflowInput {
	return RelationAssessmentWorkflowInput{
		ProfileRef: DefaultProfileRef(), PromptRef: DefaultPromptRef(),
		SchemaRef:        agentdomain.SchemaRef{ID: agentdomain.RelationAssessmentSchemaID, Version: agentdomain.OutputSchemaVersionV1},
		ReducedSchemaRef: agentdomain.SchemaRef{ID: agentapplication.RelationAssessmentReducedSchemaID, Version: agentdomain.OutputSchemaVersionV1},
		Retrieval:        agentdomain.RetrievalRef{IndexVersionID: testIndexID},
		Candidate: RelationClaimInput{
			Node: RelationNodeInput{Type: knowledgedomain.NodeTypeClaim, ID: testCandidateID}, Statement: "candidate claim",
			Applicability: json.RawMessage("{}"), Evidence: []RelationEvidenceInput{{Citation: testCitation("candidate-1", testChunkID, testSourceID, testSpanID)}},
		},
	}
}

func validInputWithExisting() RelationAssessmentWorkflowInput {
	input := validInput()
	input.Existing = &RelationClaimInput{
		Node: RelationNodeInput{Type: knowledgedomain.NodeTypeClaim, ID: testExistingID}, Statement: "existing claim",
		Applicability: json.RawMessage("{}"), Evidence: []RelationEvidenceInput{{Citation: testCitation("existing-1", testChunkID2, testSourceID2, testSpanID2)}},
	}
	return input
}

func testCitation(id string, chunkID, sourceID, spanID foundation.ID) agentdomain.Citation {
	return agentdomain.Citation{ID: id, WorkspaceID: testWorkspaceID, IndexVersionID: testIndexID, ChunkID: chunkID, SourceVersionID: sourceID, SourceSpanID: spanID}
}

func relationDocument(t *testing.T, runID foundation.ID, assessment knowledgedomain.RelationAssessment) []byte {
	t.Helper()
	payload := agentdomain.RelationAssessmentPayload{
		Assessment: assessment, CandidateEvidenceRefs: []string{"candidate-1"},
		ExistingEvidenceRefs: []string{}, ConflictDisclosures: []agentdomain.RelationConflictDisclosure{},
		Applicability: agentdomain.ApplicabilityComparison{Candidate: json.RawMessage("{}")},
		Reason:        "bounded assessment", ConfidenceFactors: []string{"evidence"}, UncertaintyReasons: []string{},
	}
	if assessment != knowledgedomain.AssessmentNew {
		payload.ExistingEvidenceRefs = []string{"existing-1"}
		payload.Applicability.Existing = json.RawMessage("{}")
		payload.Applicability.Summary = "same applicability"
	}
	if assessment == knowledgedomain.AssessmentLowConfidence {
		payload.UncertaintyReasons = []string{"insufficient context"}
	}
	document, err := json.Marshal(agentdomain.RelationAssessmentResult{
		ResultType: agentdomain.ResultTypeRelationAssessment, SchemaID: agentdomain.RelationAssessmentSchemaID,
		SchemaVersion: agentdomain.OutputSchemaVersionV1, ModelRunRef: runID, Payload: payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func testModelRef() agentdomain.ModelRef {
	return agentdomain.ModelRef{AdapterName: "test-chat", AdapterVersion: "v1", ModelID: "test-model", ModelVersion: "v1"}
}

func codeOf(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}

type trackingStructuredScheduler struct {
	delegate agentapplication.StructuredPhaseScheduler
	calls    atomic.Int64
}

func newTrackingEinoStructuredScheduler(t *testing.T) *trackingStructuredScheduler {
	t.Helper()
	scheduler, err := agenteino.NewStructuredPhaseScheduler(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return &trackingStructuredScheduler{delegate: scheduler}
}

func (scheduler *trackingStructuredScheduler) Schedule(ctx context.Context, run *agentapplication.StructuredPhaseRun) error {
	scheduler.calls.Add(1)
	return scheduler.delegate.Schedule(ctx, run)
}

type workflowRepository struct {
	run                 agentdomain.ModelRun
	calls               []agentdomain.ModelCall
	failCallCompletion  bool
	failRunFinalization bool
}

func (repository *workflowRepository) CreateModelRun(_ context.Context, run agentdomain.ModelRun) (agentdomain.ModelRun, bool, error) {
	if err := agentdomain.ValidateModelRun(run); err != nil {
		return agentdomain.ModelRun{}, false, err
	}
	repository.run = run
	return run, false, nil
}

func (repository *workflowRepository) GetModelRun(context.Context, foundation.ID, foundation.ID) (agentapplication.ModelRunRecord, error) {
	return agentapplication.ModelRunRecord{Run: repository.run, Calls: append([]agentdomain.ModelCall(nil), repository.calls...)}, nil
}

func (repository *workflowRepository) StartModelCall(_ context.Context, _ foundation.ID, call agentdomain.ModelCall) (agentdomain.ModelCall, bool, error) {
	if err := agentdomain.ValidateModelCall(call); err != nil {
		return agentdomain.ModelCall{}, false, err
	}
	repository.calls = append(repository.calls, call)
	return call, false, nil
}

func (repository *workflowRepository) CompleteModelCall(_ context.Context, command agentapplication.CompleteModelCallCommand) (agentdomain.ModelCall, bool, error) {
	if repository.failCallCompletion {
		return agentdomain.ModelCall{}, false, errors.New("completion response lost")
	}
	if err := agentdomain.ValidateModelCall(command.Call); err != nil {
		return agentdomain.ModelCall{}, false, err
	}
	repository.calls[command.Call.CallNo-1] = command.Call
	return command.Call, false, nil
}

func (repository *workflowRepository) FinalizeModelRun(_ context.Context, command agentapplication.FinalizeModelRunCommand) (agentdomain.ModelRun, bool, error) {
	if repository.failRunFinalization {
		return agentdomain.ModelRun{}, false, errors.New("finalization response lost")
	}
	if err := agentdomain.ValidateModelRun(command.Run); err != nil {
		return agentdomain.ModelRun{}, false, err
	}
	repository.run = command.Run
	return command.Run, false, nil
}

func (*workflowRepository) MarkStaleModelCallsUnknown(context.Context, agentapplication.UnknownRecoveryQuery) ([]agentdomain.ModelCall, error) {
	return nil, nil
}

func (*workflowRepository) MarkStaleModelRunsUnknown(context.Context, agentapplication.UnknownRecoveryQuery) ([]agentdomain.ModelRun, error) {
	return nil, nil
}

type workflowIDs struct {
	values []foundation.ID
	next   int
}

func (ids *workflowIDs) New() (foundation.ID, error) {
	if ids.next >= len(ids.values) {
		return "", fmt.Errorf("id sequence exhausted")
	}
	value := ids.values[ids.next]
	ids.next++
	return value, nil
}

type workflowClock struct{ next time.Time }

func (clock *workflowClock) Now() time.Time {
	value := clock.next
	clock.next = clock.next.Add(time.Millisecond)
	return value
}

type workflowKnowledgePort struct{}

func (workflowKnowledgePort) LoadFormalClaims(_ context.Context, workspaceID foundation.ID, claimIDs []foundation.ID) ([]knowledgedomain.ClaimWithSources, error) {
	applicability, err := knowledgedomain.ParseApplicability(json.RawMessage("{}"))
	if err != nil {
		return nil, err
	}
	statement, normalized, err := knowledgedomain.NormalizeStatement("existing claim")
	if err != nil {
		return nil, err
	}
	claim := knowledgedomain.Claim{
		ID: claimIDs[0], WorkspaceID: workspaceID, Statement: statement, NormalizedStatement: normalized,
		Applicability: applicability, Status: knowledgedomain.ClaimStatusConfirmed, ConfidenceFactors: json.RawMessage("{}"), Version: 1,
		CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(2, 0).UTC(),
	}
	claim.Fingerprint = knowledgedomain.ComputeClaimFingerprint(workspaceID, normalized, applicability)
	source := knowledgedomain.ClaimSource{
		ID: "83000000-0000-4000-8000-000000000099", WorkspaceID: workspaceID, ClaimID: claim.ID,
		Provenance:  knowledgedomain.ProvenanceRef{WorkspaceID: workspaceID, SourceVersionID: testSourceID2, SourceSpanID: testSpanID2},
		SupportType: knowledgedomain.ClaimSupportSupports, Reason: "formal source", CreatedAt: time.Unix(1, 0).UTC(),
	}
	source.EvidenceHash = knowledgedomain.ComputeClaimSourceEvidenceHash(source, applicability)
	return []knowledgedomain.ClaimWithSources{{Claim: claim, Sources: []knowledgedomain.ClaimSource{source}}}, nil
}

func (workflowKnowledgePort) CheckEvidenceEligibility(_ context.Context, query knowledgedomain.EvidenceEligibilityQuery) ([]knowledgedomain.ProvenanceEligibility, error) {
	result := make([]knowledgedomain.ProvenanceEligibility, len(query.Provenance))
	for index, provenance := range query.Provenance {
		ownerID := foundation.ID(fmt.Sprintf("83000000-0000-4000-8000-%012d", index*2+1))
		if provenance.SourceVersionID == testSourceID2 && provenance.SourceSpanID == testSpanID2 {
			ownerID = testExistingID
		}
		evidenceID := foundation.ID(fmt.Sprintf("83000000-0000-4000-8000-%012d", index*2+2))
		result[index] = knowledgedomain.ProvenanceEligibility{
			Provenance: provenance, Eligibility: knowledgedomain.EvidenceEligible,
			Bindings: []knowledgedomain.EvidenceEligibilityBinding{{
				OwnerType: knowledgedomain.EvidenceOwnerClaim, OwnerID: ownerID, EvidenceID: evidenceID,
				ClaimStatus: knowledgedomain.ClaimStatusConfirmed, SupportType: knowledgedomain.ClaimSupportSupports,
			}},
		}
	}
	return result, nil
}

func (workflowKnowledgePort) MapAssessment(assessment knowledgedomain.RelationAssessment, source, target knowledgedomain.NodeRef) (knowledgedomain.AssessmentAction, error) {
	return knowledgedomain.MapAssessment(assessment, source, target, false)
}

type workflowEvidenceOpener struct{}

func (workflowEvidenceOpener) Open(_ context.Context, citation agentdomain.Citation) (agentapplication.OpenedEvidence, error) {
	return agentapplication.OpenedEvidence{Citation: citation, Excerpt: "server-opened-" + citation.ID}, nil
}

func (workflowEvidenceOpener) OpenBatch(_ context.Context, citations []agentdomain.Citation) ([]agentapplication.OpenedEvidence, error) {
	result := make([]agentapplication.OpenedEvidence, len(citations))
	for index, citation := range citations {
		result[index] = agentapplication.OpenedEvidence{Citation: citation, Excerpt: "server-opened-" + citation.ID}
	}
	return result, nil
}
