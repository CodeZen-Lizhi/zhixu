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
	runID := ragInputID(8)
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
	executor, err := NewRAGWorkflowExecutor(RAGWorkflowExecutorDependencies{
		Model: model, Catalog: catalog, Repository: &workflowRepository{}, Context: &ragContextLoaderFake{result: executionContext},
		Search: retrieval, Retrieval: retrieval, Eligibility: workflowKnowledgePort{}, Topics: ragTopicsFake{},
		Finalizer: finalizer, Progress: &ragProgressFake{},
		IDs: &workflowIDs{values: []foundation.ID{runID, ragInputID(9)}}, Clock: &workflowClock{next: time.Unix(1, 0).UTC()},
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
	if codeOf(err) != ErrorCodeRunFinalizationUnknown || model.CallCount() != 1 || finalizer.finalizeCalls != 1 {
		t.Fatalf("err=%v calls=%d finalizer=%#v", err, model.CallCount(), finalizer)
	}
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
		Context: loader, Search: retrieval, Retrieval: retrieval, Eligibility: workflowKnowledgePort{}, Topics: ragTopicsFake{},
		Finalizer: finalizer, Progress: &ragProgressFake{}, IDs: &workflowIDs{values: []foundation.ID{ragInputID(8)}},
		Clock: &workflowClock{next: time.Unix(1, 0).UTC()}, Budget: agentapplication.DefaultRunBudget(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return executor
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

type ragRetrievalFake struct{}

func (ragRetrievalFake) Search(context.Context, retrievaldomain.SearchRequest) (agentapplication.ScopedRetrievalResult, error) {
	return agentapplication.ScopedRetrievalResult{}, nil
}

func (ragRetrievalFake) Retrieve(context.Context, agentapplication.RetrievalRequest) (agentapplication.RetrievalBatch, error) {
	return agentapplication.RetrievalBatch{}, nil
}

func (ragRetrievalFake) Open(context.Context, agentdomain.Citation) (agentapplication.OpenedEvidence, error) {
	return agentapplication.OpenedEvidence{}, nil
}

func (ragRetrievalFake) OpenBatch(context.Context, []agentdomain.Citation) ([]agentapplication.OpenedEvidence, error) {
	return nil, nil
}

type ragTopicsFake struct{}

func (ragTopicsFake) ResolveRAGTopics(context.Context, foundation.ID, []knowledgedomain.ProvenanceRef) ([]knowledgedomain.EvidenceTopicBinding, error) {
	return nil, nil
}

type ragProgressFake struct {
	records []agentapplication.RAGProgressRecord
}

func (fake *ragProgressFake) RecordRAGProgress(_ context.Context, record agentapplication.RAGProgressRecord) error {
	fake.records = append(fake.records, record)
	return nil
}
