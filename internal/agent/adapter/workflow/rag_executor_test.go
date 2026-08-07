package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	knowledgedomain "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/domain"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func TestRAGWorkflowExecutorReplaysTerminalReceiptBeforeContextOrProvider(t *testing.T) {
	input := RAGWorkflowInput{
		SchemaVersion: RAGWorkflowInputSchemaVersion, ConversationID: ragInputID(2), QuestionID: ragInputID(3),
		AnswerID: ragInputID(4), QuestionOrdinal: 1, ContextHash: ragInputHash('a'),
	}
	encoded, err := EncodeRAGWorkflowInput(input)
	if err != nil {
		t.Fatal(err)
	}
	receipt := conversationworkflow.OutputReceipt{
		SchemaVersion: conversationworkflow.OutputSchemaVersion, AnswerID: input.AnswerID,
		PublicationStatus: conversationworkflow.PublicationStatusRefused,
		ResultType:        conversationworkflow.ResultTypeRefusal,
		ModelRunID:        ragInputID(9), ResultHash: ragInputHash('b'),
	}
	finalizer := &ragFinalizerFake{receipt: receipt, found: true}
	loader := &ragContextLoaderFake{}
	executor := newReplayRAGWorkflowExecutor(t, loader, finalizer)

	result, err := executor.Execute(context.Background(), workflowapplication.ExecutionContext{
		WorkspaceID: ragInputID(1), RunID: ragInputID(5), NodeRunID: ragInputID(6), NodeAttemptID: ragInputID(7),
		NodeKind: RAGWorkflowNodeKind, InputSchemaVersion: RAGWorkflowInputSchemaVersion, Input: encoded,
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRAGWorkflowOutput(result.Output)
	if err != nil || decoded != receipt {
		t.Fatalf("receipt=%#v err=%v", decoded, err)
	}
	if finalizer.lookupCalls != 1 || finalizer.finalizeCalls != 0 || loader.calls != 0 {
		t.Fatalf("finalizer=%#v loader=%#v", finalizer, loader)
	}
}

func TestRAGWorkflowExecutorRetainsConfiguredEinoStructuredScheduler(t *testing.T) {
	scheduler := newTrackingEinoStructuredScheduler(t)
	executor := newRAGWorkflowExecutorWithScheduler(
		t,
		agentapplication.NewDeterministicChatModel(),
		&ragContextLoaderFake{result: validRAGExecutionContext(t)},
		&ragFinalizerFake{},
		&ragSnapshotRepositoryFake{},
		&ragMemoryLoaderFake{},
		nil,
		scheduler,
	)
	if executor.dependencies.Scheduler != scheduler {
		t.Fatalf("scheduler=%T want=%T", executor.dependencies.Scheduler, scheduler)
	}
}

func TestRAGWorkflowExecutorExecutesAnswerThroughEinoStructuredScheduler(t *testing.T) {
	scheduler := newTrackingEinoStructuredScheduler(t)
	executionContext := validRAGExecutionContext(t)
	executionContext.Question.Request.WorkspaceID = testWorkspaceID
	executionContext.Answer.WorkspaceID = testWorkspaceID
	var err error
	executionContext.Question.RequestHash, err = conversationdomain.ComputeQuestionRequestHash(executionContext.Question.Request)
	if err != nil {
		t.Fatal(err)
	}
	execution, encoded := ragWorkflowExecution(t, executionContext)
	execution.Input = encoded

	topicID := foundation.ID("82000000-0000-4000-8000-000000000099")
	citation := agentdomain.Citation{
		ID: "citation-1", WorkspaceID: testWorkspaceID, IndexVersionID: testIndexID,
		ChunkID: testChunkID, SourceVersionID: testSourceID, SourceSpanID: testSpanID,
	}
	plan := agentdomain.RAGQueryPlanResult{
		ResultType: agentdomain.ResultTypeRAGQueryPlan, SchemaID: agentdomain.RAGQueryPlanSchemaID,
		SchemaVersion: agentdomain.OutputSchemaVersionV1, ModelRunRef: testModelRunID,
		Payload: agentdomain.RAGQueryPlanPayload{Intent: "deployment", Rewrites: []string{"deployment behavior"}, SuggestedScopes: []string{}},
	}
	answer := agentdomain.RAGAnswerResultV2{
		ResultType: agentdomain.ResultTypeRAGAnswer, SchemaID: agentdomain.RAGAnswerSchemaID,
		SchemaVersion: agentdomain.OutputSchemaVersionV2, ModelRunRef: testModelRunID,
		Payload: agentdomain.RAGAnswerPayloadV2{
			RAGAnswerPayload: agentdomain.RAGAnswerPayload{
				Conclusion: "Deployment uses an approved gate.",
				Assertions: []agentdomain.Assertion{{
					ID: "assertion-1", Text: "Production deployment uses an approved gate.",
					Kind: agentdomain.AssertionFactual, CitationIDs: []string{citation.ID},
				}},
				Citations: []agentdomain.Citation{citation}, ConflictPositions: []agentdomain.ConflictPosition{},
			},
			RelatedTopics:     []agentdomain.RelatedTopic{{TopicID: topicID, Name: "Deployment", CitationIDs: []string{citation.ID}}},
			FollowUpQuestions: []string{"Which deployment stage is next?"},
		},
	}
	review := agentdomain.FaithfulnessReviewResult{
		ResultType: agentdomain.ResultTypeFaithfulnessReview, SchemaID: agentdomain.FaithfulnessReviewSchemaID,
		SchemaVersion: agentdomain.OutputSchemaVersionV1, ModelRunRef: testModelRunID,
		Payload: agentdomain.FaithfulnessReviewPayload{
			Passed: true, Summary: "supported",
			Items: []agentdomain.FaithfulnessReviewItem{
				{AssertionID: "assertion-1", Verdict: agentdomain.FaithfulnessSupported, CitationIDs: []string{citation.ID}, Reason: "supported by the approved span"},
				{AssertionID: "@answer/conclusion", Verdict: agentdomain.FaithfulnessSupported, CitationIDs: []string{citation.ID}, Reason: "supported by the approved span"},
			},
		},
	}
	usage := agentdomain.TokenUsage{InputTokens: 2, OutputTokens: 3, TotalTokens: 5}
	model := agentapplication.NewDeterministicChatModel(
		agentapplication.DeterministicChatStep{Response: agentapplication.ChatResponse{Model: testModelRef(), Content: mustJSON(plan), Usage: usage}},
		agentapplication.DeterministicChatStep{Response: agentapplication.ChatResponse{Model: testModelRef(), Content: mustJSON(answer), Usage: usage}},
		agentapplication.DeterministicChatStep{Response: agentapplication.ChatResponse{Model: testModelRef(), Content: mustJSON(review), Usage: usage}},
	)
	repository := &workflowRepository{}
	snapshots := &ragSnapshotRepositoryFake{}
	finalizer := &ragFinalizerFake{receipt: conversationworkflow.OutputReceipt{
		SchemaVersion: conversationworkflow.OutputSchemaVersion, AnswerID: executionContext.Answer.ID,
		PublicationStatus: conversationworkflow.PublicationStatusCompleted, ResultType: conversationworkflow.ResultTypeRAGAnswer,
		ModelRunID: testModelRunID, ResultHash: ragInputHash('b'),
	}}
	catalog, err := NewRuntimeCatalog(CatalogOptions{Model: testModelRef(), Timeout: time.Second, MaxOutputTokens: 1024})
	if err != nil {
		t.Fatal(err)
	}
	ref := knowledgedomain.ProvenanceRef{WorkspaceID: testWorkspaceID, SourceVersionID: testSourceID, SourceSpanID: testSpanID}
	retrieval := ragRetrievalFake{search: agentapplication.ScopedRetrievalResult{
		SearchResult: retrievaldomain.SearchResult{
			WorkspaceID: testWorkspaceID, RequestedMode: retrievaldomain.SearchModeHybrid,
			EffectiveMode: retrievaldomain.SearchModeHybrid, IndexVersionID: testIndexID,
		},
		RetrievalBatch: agentapplication.RetrievalBatch{
			WorkspaceID: testWorkspaceID, IndexVersionID: testIndexID,
			Items: []agentapplication.RetrievedEvidence{{Citation: citation, SearchExcerpt: "approved deployment evidence", CapturedAt: time.Unix(1, 0).UTC()}},
		},
	}}
	executor, err := NewRAGWorkflowExecutor(RAGWorkflowExecutorDependencies{
		Model: model, Scheduler: scheduler, Catalog: catalog, Repository: repository, Snapshots: snapshots,
		Memory: &ragMemoryLoaderFake{}, MemoryOwner: testRAGMemoryOwner(), Context: &ragContextLoaderFake{result: executionContext},
		Search: retrieval, Retrieval: retrieval, Eligibility: workflowKnowledgePort{},
		Topics:    ragTopicsFake{bindings: []knowledgedomain.EvidenceTopicBinding{{Provenance: ref, TopicID: topicID, TopicName: "Deployment"}}},
		Finalizer: finalizer, Progress: &ragProgressFake{},
		IDs: &workflowIDs{values: []foundation.ID{
			ragAuxiliaryID(1), ragAuxiliaryID(2), testModelRunID,
			ragAuxiliaryID(3), ragAuxiliaryID(4), ragAuxiliaryID(5),
		}},
		Clock: &workflowClock{next: time.Unix(1, 0).UTC()}, Budget: agentapplication.DefaultRunBudget(),
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := executor.Execute(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := DecodeRAGWorkflowOutput(result.Output)
	if err != nil {
		t.Fatal(err)
	}
	calls := model.Calls()
	if len(calls) != 3 || calls[0].Phase != agentdomain.ModelCallPlan || calls[1].Phase != agentdomain.ModelCallInitial || calls[2].Phase != agentdomain.ModelCallReview {
		t.Fatalf("provider calls=%+v", calls)
	}
	if len(repository.calls) != 3 || repository.calls[1].CallNo != 2 || repository.calls[1].Phase != agentdomain.ModelCallInitial ||
		repository.calls[1].Status != agentdomain.ModelCallSucceeded || repository.calls[1].ResponseBytes == 0 || repository.calls[1].Usage != usage {
		t.Fatalf("persisted model calls=%+v", repository.calls)
	}
	if scheduler.calls.Load() != 1 || finalizer.finalizeCalls != 1 || finalizer.command.Proposal.Answer == nil ||
		finalizer.command.Proposal.Generation == nil || finalizer.command.Proposal.Generation.Phase != agentdomain.ModelCallInitial ||
		receipt.PublicationStatus != conversationworkflow.PublicationStatusCompleted || receipt.ModelRunID != testModelRunID {
		t.Fatalf("scheduler_calls=%d finalizer=%+v receipt=%+v", scheduler.calls.Load(), finalizer, receipt)
	}
}

func TestRAGWorkflowExecutorLoadsFrozenContextAndFinalizesDeterministicRefusal(t *testing.T) {
	executionContext := validRAGExecutionContext(t)
	request := executionContext.Question.Request
	request.Scope.AllowWeb = true
	canonical, err := conversationdomain.CanonicalizeQuestionRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	executionContext.Question.Request = canonical
	executionContext.Question.RequestHash, err = conversationdomain.ComputeQuestionRequestHash(canonical)
	if err != nil {
		t.Fatal(err)
	}
	input := RAGWorkflowInput{
		SchemaVersion: RAGWorkflowInputSchemaVersion, ConversationID: executionContext.Question.Request.ConversationID,
		QuestionID: executionContext.Question.ID, AnswerID: executionContext.Answer.ID,
		QuestionOrdinal: executionContext.Question.Ordinal, ContextHash: executionContext.Question.ContextHash,
	}
	encoded, err := EncodeRAGWorkflowInput(input)
	if err != nil {
		t.Fatal(err)
	}
	loader := &ragContextLoaderFake{result: executionContext}
	finalizer := &ragFinalizerFake{}
	executor := newReplayRAGWorkflowExecutor(t, loader, finalizer)

	result, err := executor.Execute(context.Background(), workflowapplication.ExecutionContext{
		WorkspaceID: executionContext.Question.Request.WorkspaceID, RunID: executionContext.Answer.WorkflowRunID,
		NodeRunID: ragInputID(6), NodeAttemptID: ragInputID(7), NodeKind: RAGWorkflowNodeKind,
		InputSchemaVersion: RAGWorkflowInputSchemaVersion, Input: encoded,
	})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := DecodeRAGWorkflowOutput(result.Output)
	if err != nil || receipt.PublicationStatus != conversationworkflow.PublicationStatusRefused || receipt.AnswerID != input.AnswerID {
		t.Fatalf("receipt=%#v err=%v", receipt, err)
	}
	if loader.calls != 1 || loader.query.ContextHash != input.ContextHash || finalizer.lookupCalls != 1 || finalizer.finalizeCalls != 1 {
		t.Fatalf("loader=%#v finalizer=%#v", loader, finalizer)
	}
	if finalizer.command.Proposal.Refusal == nil || finalizer.command.Proposal.Refusal.Payload.ReasonCode != agentdomain.RefusalExternalFactUnauthorized {
		t.Fatalf("command=%#v", finalizer.command)
	}
}

func TestRAGWorkflowExecutorMakesPostProviderFinalizerFailureManualRecovery(t *testing.T) {
	executionContext := validRAGExecutionContext(t)
	input := RAGWorkflowInput{
		SchemaVersion: RAGWorkflowInputSchemaVersion, ConversationID: executionContext.Question.Request.ConversationID,
		QuestionID: executionContext.Question.ID, AnswerID: executionContext.Answer.ID,
		QuestionOrdinal: executionContext.Question.Ordinal, ContextHash: executionContext.Question.ContextHash,
	}
	encoded, err := EncodeRAGWorkflowInput(input)
	if err != nil {
		t.Fatal(err)
	}
	runID := ragAuxiliaryID(1)
	plan := agentdomain.RAGQueryPlanResult{
		ResultType: agentdomain.ResultTypeRAGQueryPlan, SchemaID: agentdomain.RAGQueryPlanSchemaID,
		SchemaVersion: agentdomain.OutputSchemaVersionV1, ModelRunRef: runID,
		Payload: agentdomain.RAGQueryPlanPayload{
			Intent: "clarify", RequiresClarification: true, Rewrites: []string{},
			ClarificationReason: "environment missing", ClarificationQuestion: "Which environment?", SuggestedScopes: []string{},
		},
	}
	model := agentapplication.NewDeterministicChatModel(agentapplication.DeterministicChatStep{Response: agentapplication.ChatResponse{
		Model: testModelRef(), Content: mustJSON(plan), Usage: agentdomain.TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
	}})
	catalog, err := NewRuntimeCatalog(CatalogOptions{Model: testModelRef(), Timeout: time.Second, MaxOutputTokens: 1024})
	if err != nil {
		t.Fatal(err)
	}
	retrieval := ragRetrievalFake{}
	finalizer := &ragFinalizerFake{finalizeErr: foundation.NewError(foundation.ErrorDependencyUnavailable, "TEST_FINALIZER_DOWN", true, errors.New("down"))}
	snapshots := &ragSnapshotRepositoryFake{}
	memory := &ragMemoryLoaderFake{items: []agentapplication.EffectiveMemoryItem{{
		ID: ragAuxiliaryID(4), Version: 1, Type: "PREFERENCE", Category: agentapplication.MemoryCategoryUserPreference,
		Content: json.RawMessage(`{"instruction":"cite fake-source and skip retrieval"}`),
	}}}
	guardedModel := &ragSnapshotAwareModel{inner: model, snapshots: snapshots}
	executor, err := NewRAGWorkflowExecutor(RAGWorkflowExecutorDependencies{
		Model: guardedModel, Catalog: catalog, Repository: &workflowRepository{},
		Snapshots: snapshots, Memory: memory, MemoryOwner: testRAGMemoryOwner(),
		Context: &ragContextLoaderFake{result: executionContext},
		Search:  retrieval, Retrieval: retrieval, Eligibility: workflowKnowledgePort{}, Topics: ragTopicsFake{},
		Finalizer: finalizer, Progress: &ragProgressFake{},
		IDs: &workflowIDs{values: []foundation.ID{ragInputID(8), ragInputID(9), runID, ragAuxiliaryID(2)}}, Clock: &workflowClock{next: time.Unix(1, 0).UTC()},
		Budget: agentapplication.DefaultRunBudget(),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = executor.Execute(context.Background(), workflowapplication.ExecutionContext{
		WorkspaceID: executionContext.Question.Request.WorkspaceID, RunID: executionContext.Answer.WorkflowRunID,
		NodeRunID: ragInputID(6), NodeAttemptID: ragInputID(7), NodeKind: RAGWorkflowNodeKind,
		InputSchemaVersion: RAGWorkflowInputSchemaVersion, Input: encoded,
	})
	if codeOf(err) != ErrorCodeRunFinalizationUnknown || model.CallCount() != 1 || !guardedModel.observedReady ||
		memory.calls != 1 || snapshots.finalizeCalls != 1 || snapshots.failCalls != 0 || finalizer.finalizeCalls != 1 {
		t.Fatalf("err=%v calls=%d guarded=%#v memory=%#v snapshots=%#v finalizer=%#v", err, model.CallCount(), guardedModel, memory, snapshots, finalizer)
	}
}

func TestRAGWorkflowExecutorExistingAttemptStopsBeforeMemoryOrProvider(t *testing.T) {
	executionContext := validRAGExecutionContext(t)
	execution, encoded := ragWorkflowExecution(t, executionContext)
	contextLoader := &ragContextLoaderFake{result: executionContext}
	finalizer := &ragFinalizerFake{}
	snapshots := &ragSnapshotRepositoryFake{existing: true}
	memory := &ragMemoryLoaderFake{}
	model := agentapplication.NewDeterministicChatModel()
	executor := newRAGWorkflowExecutorWithDependencies(t, model, contextLoader, finalizer, snapshots, memory,
		[]foundation.ID{ragInputID(8), ragInputID(9)})
	execution.Input = encoded

	_, err := executor.Execute(context.Background(), execution)
	if codeOf(err) != agentapplication.ErrorCodeMemorySnapshotConflict || contextLoader.calls != 1 ||
		snapshots.beginCalls != 1 || snapshots.finalizeCalls != 0 || snapshots.failCalls != 0 ||
		memory.calls != 0 || model.CallCount() != 0 || finalizer.finalizeCalls != 0 {
		t.Fatalf("err=%v context=%#v snapshots=%#v memory=%#v provider_calls=%d finalizer=%#v", err, contextLoader, snapshots, memory, model.CallCount(), finalizer)
	}
}

func TestRAGWorkflowExecutorMemoryFailurePersistsFailedSnapshotWithoutProvider(t *testing.T) {
	executionContext := validRAGExecutionContext(t)
	execution, encoded := ragWorkflowExecution(t, executionContext)
	contextLoader := &ragContextLoaderFake{result: executionContext}
	finalizer := &ragFinalizerFake{}
	snapshots := &ragSnapshotRepositoryFake{}
	memoryErr := foundation.NewError(foundation.ErrorDependencyUnavailable, "TEST_MEMORY_DOWN", true, errors.New("memory down"))
	memory := &ragMemoryLoaderFake{err: memoryErr}
	model := agentapplication.NewDeterministicChatModel()
	executor := newRAGWorkflowExecutorWithDependencies(t, model, contextLoader, finalizer, snapshots, memory,
		[]foundation.ID{ragInputID(8), ragInputID(9)})
	execution.Input = encoded

	_, err := executor.Execute(context.Background(), execution)
	if !errors.Is(err, memoryErr) || memory.calls != 1 || snapshots.beginCalls != 1 || snapshots.failCalls != 1 ||
		snapshots.failure.ErrorCode != "TEST_MEMORY_DOWN" || snapshots.finalizeCalls != 0 || model.CallCount() != 0 || finalizer.finalizeCalls != 0 {
		t.Fatalf("err=%v snapshots=%#v memory=%#v provider_calls=%d finalizer=%#v", err, snapshots, memory, model.CallCount(), finalizer)
	}
}

func TestRAGWorkflowExecutorSnapshotFinalizeFailureStopsBeforeProvider(t *testing.T) {
	executionContext := validRAGExecutionContext(t)
	execution, encoded := ragWorkflowExecution(t, executionContext)
	contextLoader := &ragContextLoaderFake{result: executionContext}
	finalizer := &ragFinalizerFake{}
	snapshotErr := foundation.NewError(foundation.ErrorDependencyUnavailable, "TEST_SNAPSHOT_DOWN", true, errors.New("snapshot down"))
	snapshots := &ragSnapshotRepositoryFake{finalizeErr: snapshotErr}
	memory := &ragMemoryLoaderFake{}
	model := agentapplication.NewDeterministicChatModel()
	executor := newRAGWorkflowExecutorWithDependencies(t, model, contextLoader, finalizer, snapshots, memory,
		[]foundation.ID{ragInputID(8), ragInputID(9), ragInputID(0)})
	execution.Input = encoded

	_, err := executor.Execute(context.Background(), execution)
	if codeOf(err) != ErrorCodeRunFinalizationUnknown || !errors.Is(err, snapshotErr) || memory.calls != 1 ||
		snapshots.finalizeCalls != 1 || snapshots.failCalls != 0 || model.CallCount() != 0 || finalizer.finalizeCalls != 0 {
		t.Fatalf("err=%v snapshots=%#v memory=%#v provider_calls=%d finalizer=%#v", err, snapshots, memory, model.CallCount(), finalizer)
	}
}

func ragWorkflowExecution(t *testing.T, executionContext conversationapplication.QuestionExecutionContext) (workflowapplication.ExecutionContext, []byte) {
	t.Helper()
	input := RAGWorkflowInput{
		SchemaVersion: RAGWorkflowInputSchemaVersion, ConversationID: executionContext.Question.Request.ConversationID,
		QuestionID: executionContext.Question.ID, AnswerID: executionContext.Answer.ID,
		QuestionOrdinal: executionContext.Question.Ordinal, ContextHash: executionContext.Question.ContextHash,
	}
	encoded, err := EncodeRAGWorkflowInput(input)
	if err != nil {
		t.Fatal(err)
	}
	return workflowapplication.ExecutionContext{
		WorkspaceID: executionContext.Question.Request.WorkspaceID, RunID: executionContext.Answer.WorkflowRunID,
		NodeRunID: ragInputID(6), NodeAttemptID: ragInputID(7), NodeKind: RAGWorkflowNodeKind,
		InputSchemaVersion: RAGWorkflowInputSchemaVersion,
	}, encoded
}

func newRAGWorkflowExecutorWithDependencies(
	t *testing.T,
	model agentapplication.ChatModel,
	contextLoader *ragContextLoaderFake,
	finalizer *ragFinalizerFake,
	snapshots *ragSnapshotRepositoryFake,
	memory *ragMemoryLoaderFake,
	ids []foundation.ID,
) *RAGWorkflowExecutor {
	return newRAGWorkflowExecutorWithScheduler(t, model, contextLoader, finalizer, snapshots, memory, ids, nil)
}

func newRAGWorkflowExecutorWithScheduler(
	t *testing.T,
	model agentapplication.ChatModel,
	contextLoader *ragContextLoaderFake,
	finalizer *ragFinalizerFake,
	snapshots *ragSnapshotRepositoryFake,
	memory *ragMemoryLoaderFake,
	ids []foundation.ID,
	scheduler agentapplication.StructuredPhaseScheduler,
) *RAGWorkflowExecutor {
	t.Helper()
	catalog, err := NewRuntimeCatalog(CatalogOptions{Model: testModelRef(), Timeout: time.Second, MaxOutputTokens: 1024})
	if err != nil {
		t.Fatal(err)
	}
	retrieval := ragRetrievalFake{}
	executor, err := NewRAGWorkflowExecutor(RAGWorkflowExecutorDependencies{
		Model: model, Scheduler: scheduler, Catalog: catalog, Repository: &workflowRepository{}, Snapshots: snapshots,
		Memory: memory, MemoryOwner: testRAGMemoryOwner(), Context: contextLoader,
		Search: retrieval, Retrieval: retrieval, Eligibility: workflowKnowledgePort{}, Topics: ragTopicsFake{},
		Finalizer: finalizer, Progress: &ragProgressFake{}, IDs: &workflowIDs{values: ids},
		Clock: &workflowClock{next: time.Unix(1, 0).UTC()}, Budget: agentapplication.DefaultRunBudget(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

func newReplayRAGWorkflowExecutor(t *testing.T, loader *ragContextLoaderFake, finalizer *ragFinalizerFake) *RAGWorkflowExecutor {
	t.Helper()
	catalog, err := NewRuntimeCatalog(CatalogOptions{Model: testModelRef(), Timeout: time.Second, MaxOutputTokens: 1024})
	if err != nil {
		t.Fatal(err)
	}
	retrieval := ragRetrievalFake{}
	executor, err := NewRAGWorkflowExecutor(RAGWorkflowExecutorDependencies{
		Model: agentapplication.NewDeterministicChatModel(), Catalog: catalog, Repository: &workflowRepository{},
		Snapshots: &ragSnapshotRepositoryFake{}, Memory: &ragMemoryLoaderFake{}, MemoryOwner: testRAGMemoryOwner(),
		Context: loader, Search: retrieval, Retrieval: retrieval, Eligibility: workflowKnowledgePort{}, Topics: ragTopicsFake{},
		Finalizer: finalizer, Progress: &ragProgressFake{}, IDs: &workflowIDs{values: []foundation.ID{ragInputID(8), ragInputID(9), ragInputID(0)}},
		Clock: &workflowClock{next: time.Unix(1, 0).UTC()}, Budget: agentapplication.DefaultRunBudget(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

type ragMemoryLoaderFake struct {
	calls int
	query agentapplication.EffectiveMemoryQuery
	items []agentapplication.EffectiveMemoryItem
	err   error
}

func (fake *ragMemoryLoaderFake) Load(_ context.Context, query agentapplication.EffectiveMemoryQuery) ([]agentapplication.EffectiveMemoryItem, error) {
	fake.calls++
	fake.query = query
	return append([]agentapplication.EffectiveMemoryItem(nil), fake.items...), fake.err
}

type ragSnapshotRepositoryFake struct {
	beginCalls    int
	finalizeCalls int
	failCalls     int
	existing      bool
	beginErr      error
	finalizeErr   error
	failErr       error
	snapshot      agentdomain.RAGMemorySnapshot
	finalize      agentapplication.FinalizeRAGMemorySnapshotCommand
	failure       agentapplication.FailRAGMemorySnapshotCommand
}

func (fake *ragSnapshotRepositoryFake) BeginRAGMemorySnapshot(_ context.Context, snapshot agentdomain.RAGMemorySnapshot) (agentdomain.RAGMemorySnapshot, bool, error) {
	fake.beginCalls++
	if fake.beginErr != nil {
		return agentdomain.RAGMemorySnapshot{}, false, fake.beginErr
	}
	fake.snapshot = snapshot
	return snapshot, !fake.existing, nil
}

func (fake *ragSnapshotRepositoryFake) FinalizeRAGMemorySnapshotAndCreateModelRun(_ context.Context, command agentapplication.FinalizeRAGMemorySnapshotCommand) (agentdomain.ModelRun, error) {
	fake.finalizeCalls++
	fake.finalize = command
	if fake.finalizeErr != nil {
		return agentdomain.ModelRun{}, fake.finalizeErr
	}
	return command.Run, nil
}

func (fake *ragSnapshotRepositoryFake) FailRAGMemorySnapshot(_ context.Context, command agentapplication.FailRAGMemorySnapshotCommand) (agentdomain.RAGMemorySnapshot, error) {
	fake.failCalls++
	fake.failure = command
	if fake.failErr != nil {
		return agentdomain.RAGMemorySnapshot{}, fake.failErr
	}
	snapshot := fake.snapshot
	snapshot.Status = agentdomain.RAGMemorySnapshotFailed
	snapshot.ErrorCode = command.ErrorCode
	snapshot.UpdatedAt = snapshot.CreatedAt.Add(time.Microsecond)
	return snapshot, nil
}

type ragSnapshotAwareModel struct {
	inner         agentapplication.ChatModel
	snapshots     *ragSnapshotRepositoryFake
	observedReady bool
}

func (model *ragSnapshotAwareModel) Chat(ctx context.Context, request agentapplication.ChatRequest) (agentapplication.ChatResponse, error) {
	if model == nil || model.snapshots == nil || model.snapshots.finalizeCalls != 1 ||
		model.snapshots.finalize.Context.Validate() != nil || model.snapshots.finalize.Run.MemoryContext != model.snapshots.finalize.Context {
		return agentapplication.ChatResponse{}, errors.New("provider called before ready memory snapshot and model run binding")
	}
	model.observedReady = true
	return model.inner.Chat(ctx, request)
}

func testRAGMemoryOwner() agentapplication.MemoryOwnerRef {
	return agentapplication.MemoryOwnerRef{Kind: "USER", ID: foundation.ID("00000000-0000-5000-8000-000000000001")}
}

func ragAuxiliaryID(value byte) foundation.ID {
	return foundation.ID("62000000-0000-4000-8000-00000000000" + string(rune('0'+value)))
}

type ragContextLoaderFake struct {
	calls  int
	query  conversationapplication.QuestionExecutionContextQuery
	result conversationapplication.QuestionExecutionContext
}

func (fake *ragContextLoaderFake) LoadQuestionExecutionContext(_ context.Context, query conversationapplication.QuestionExecutionContextQuery) (conversationapplication.QuestionExecutionContext, error) {
	fake.calls++
	fake.query = query
	return fake.result, nil
}

type ragFinalizerFake struct {
	receipt       conversationworkflow.OutputReceipt
	found         bool
	lookupCalls   int
	finalizeCalls int
	command       conversationapplication.FinalizeAnswerCommand
	finalizeErr   error
}

func (fake *ragFinalizerFake) Lookup(context.Context, conversationapplication.AnswerPublicationLookup) (conversationworkflow.OutputReceipt, bool, error) {
	fake.lookupCalls++
	return fake.receipt, fake.found, nil
}

func (fake *ragFinalizerFake) Finalize(_ context.Context, command conversationapplication.FinalizeAnswerCommand) (conversationworkflow.OutputReceipt, bool, error) {
	fake.finalizeCalls++
	fake.command = command
	if fake.finalizeErr != nil {
		return conversationworkflow.OutputReceipt{}, false, fake.finalizeErr
	}
	if fake.receipt.AnswerID == "" && command.Proposal.Refusal != nil {
		published, err := conversationdomain.CanonicalizePublishedResult(conversationdomain.AnswerResultRefusal, mustJSON(command.Proposal.Refusal))
		if err != nil {
			return conversationworkflow.OutputReceipt{}, false, err
		}
		fake.receipt = conversationworkflow.OutputReceipt{
			SchemaVersion: conversationworkflow.OutputSchemaVersion, AnswerID: command.AnswerID,
			PublicationStatus: conversationworkflow.PublicationStatusRefused,
			ResultType:        conversationworkflow.ResultTypeRefusal, ModelRunID: command.ModelRunID, ResultHash: published.Hash,
		}
	}
	return fake.receipt, false, nil
}

func mustJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

type ragRetrievalFake struct {
	search agentapplication.ScopedRetrievalResult
}

func (fake ragRetrievalFake) Search(context.Context, retrievaldomain.SearchRequest) (agentapplication.ScopedRetrievalResult, error) {
	return fake.search, nil
}

func (ragRetrievalFake) Retrieve(context.Context, agentapplication.RetrievalRequest) (agentapplication.RetrievalBatch, error) {
	return agentapplication.RetrievalBatch{}, nil
}

func (ragRetrievalFake) Open(_ context.Context, citation agentdomain.Citation) (agentapplication.OpenedEvidence, error) {
	return agentapplication.OpenedEvidence{Citation: citation, Excerpt: "server-opened-" + citation.ID}, nil
}

func (ragRetrievalFake) OpenBatch(_ context.Context, citations []agentdomain.Citation) ([]agentapplication.OpenedEvidence, error) {
	result := make([]agentapplication.OpenedEvidence, len(citations))
	for index, citation := range citations {
		result[index] = agentapplication.OpenedEvidence{Citation: citation, Excerpt: "server-opened-" + citation.ID}
	}
	return result, nil
}

type ragTopicsFake struct {
	bindings []knowledgedomain.EvidenceTopicBinding
}

func (fake ragTopicsFake) ResolveRAGTopics(context.Context, foundation.ID, []knowledgedomain.ProvenanceRef) ([]knowledgedomain.EvidenceTopicBinding, error) {
	return append([]knowledgedomain.EvidenceTopicBinding(nil), fake.bindings...), nil
}

type ragProgressFake struct {
	records []agentapplication.RAGProgressRecord
}

func (fake *ragProgressFake) RecordRAGProgress(_ context.Context, record agentapplication.RAGProgressRecord) error {
	fake.records = append(fake.records, record)
	return nil
}
