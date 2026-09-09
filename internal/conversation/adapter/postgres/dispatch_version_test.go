package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	conversationworkflow "github.com/CodeZen-Lizhi/zhixu/internal/conversation/workflow"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	workflowapplication "github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	workflowdomain "github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

func TestQuestionDispatchVersionV2CompositionSelectsNewAnalysisWithoutChangingRAG(t *testing.T) {
	tests := []struct {
		mode       conversationdomain.QuestionMode
		definition workflowdomain.RegisteredDefinition
		root       string
		prefix     string
	}{
		{conversationdomain.QuestionModeWorkspaceAnalysis, conversationworkflow.RegisteredWorkspaceAnalysisDefinitionV2(), "decide_next", "workspace-analysis-question:"},
		{conversationdomain.QuestionModeRAG, conversationworkflow.RegisteredDefinitionV2(), "rag-answer", "rag-question:"},
	}
	for _, test := range tests {
		t.Run(string(test.mode), func(t *testing.T) {
			runtime := &questionDispatchVersionRuntime{}
			dispatcher := newQuestionDispatchVersionDispatcher(t, runtime)
			question, answerID := questionDispatchVersionFixture(test.mode)
			if _, err := dispatcher.startQuestionWorkflow(t.Context(), nil, nil, question, answerID); err != nil {
				t.Fatal(err)
			}
			if len(runtime.requests) != 1 {
				t.Fatalf("runtime starts = %d, want 1", len(runtime.requests))
			}
			request := runtime.requests[0]
			if request.Definition.Key != test.definition.Key || request.Definition.Version != 2 ||
				request.DefinitionGraphHash != test.definition.GraphHash || request.FirstNode.NodeKey != test.root ||
				request.Run.IdempotencyKey != test.prefix+string(question.ID) {
				t.Fatalf("new question selected the wrong workflow: %#v", request)
			}
			graph, err := workflowapplication.DecodeCanonicalGraph(request.Definition.Graph)
			if err != nil || !reflect.DeepEqual(graph, test.definition.Graph) {
				t.Fatalf("new question graph differs from its registered definition: %v", err)
			}
			input, err := decodeQuestionWorkflowInput(test.mode, request.Run.Input)
			if err != nil || input.ConversationID != question.Request.ConversationID || input.QuestionID != question.ID ||
				input.AnswerID != answerID || input.QuestionOrdinal != question.Ordinal || input.ContextHash != question.ContextHash {
				t.Fatalf("new question changed its input binding: %#v, err=%v", input, err)
			}
		})
	}
}

func TestQuestionDispatchVersionAnalysisReplayKeepsPersistedDefinitionAndHash(t *testing.T) {
	for _, definition := range []workflowdomain.RegisteredDefinition{
		conversationworkflow.RegisteredWorkspaceAnalysisDefinition(),
		conversationworkflow.RegisteredWorkspaceAnalysisDefinitionV2(),
	} {
		t.Run(fmt.Sprintf("v%d", definition.Version), func(t *testing.T) {
			question, answerID := questionDispatchVersionFixture(conversationdomain.QuestionModeWorkspaceAnalysis)
			original, err := buildQuestionWorkflowDispatchPlanForVersion(question, answerID, definition.Version)
			if err != nil || !reflect.DeepEqual(original.definition, definition) {
				t.Fatalf("explicit version selection changed the registered definition: %v", err)
			}
			replayed, err := buildReplayedQuestionWorkflowDispatchPlan(question, answerID, definition.Key, definition.Version,
				questionDispatchVersionGraph(t, definition.Graph))
			if err != nil || !reflect.DeepEqual(replayed, original) {
				t.Fatalf("historical analysis replay changed its plan: %v", err)
			}
			runtime := &questionDispatchVersionRuntime{replayed: true}
			dispatcher := newQuestionDispatchVersionDispatcher(t, runtime)
			if _, err := dispatcher.startQuestionWorkflowWithPlan(t.Context(), nil, question, replayed); err != nil {
				t.Fatal(err)
			}
			if len(runtime.requests) != 1 {
				t.Fatalf("runtime starts = %d, want 1", len(runtime.requests))
			}
			request := runtime.requests[0]
			expectedHash, err := workflowapplication.ComputeRuntimeStartRequestHash(question.Request.WorkspaceID, definition, original.input)
			if err != nil || request.RequestHash != expectedHash || request.Definition.Version != definition.Version ||
				request.DefinitionGraphHash != definition.GraphHash || request.Run.IdempotencyKey != original.idempotencyKey ||
				request.FirstNode.NodeKey != original.root.Key || request.FirstNode.NodeType != original.root.Kind {
				t.Fatalf("v2 composition rebound a historical analysis: %#v, err=%v", request, err)
			}
		})
	}
}

func TestQuestionDispatchVersionRAGReplayKeepsHistoricalDefinition(t *testing.T) {
	for _, definition := range []workflowdomain.RegisteredDefinition{
		conversationworkflow.RegisteredDefinitionV1(),
		conversationworkflow.RegisteredDefinitionV2(),
	} {
		t.Run(fmt.Sprintf("v%d", definition.Version), func(t *testing.T) {
			question, answerID := questionDispatchVersionFixture(conversationdomain.QuestionModeRAG)
			plan, err := buildReplayedQuestionWorkflowDispatchPlan(question, answerID, definition.Key, definition.Version,
				questionDispatchVersionGraph(t, definition.Graph))
			if err != nil {
				t.Fatalf("valid historical RAG v%d replay rejected: %v", definition.Version, err)
			}
			if !reflect.DeepEqual(plan.definition, definition) || plan.root.Key != conversationworkflow.NodeKey ||
				plan.idempotencyKey != "rag-question:"+string(question.ID) {
				t.Fatalf("historical RAG replay changed definition or idempotency: %#v", plan)
			}
			input, err := conversationworkflow.DecodeInput(plan.input)
			if err != nil || input.AnswerID != answerID || input.QuestionID != question.ID || input.ContextHash != question.ContextHash {
				t.Fatalf("historical RAG replay changed its input: %#v, err=%v", input, err)
			}
		})
	}
}

func TestQuestionDispatchVersionReplayRejectsDefinitionAndGraphDrift(t *testing.T) {
	analysisV1 := conversationworkflow.RegisteredWorkspaceAnalysisDefinition()
	analysisV2 := conversationworkflow.RegisteredWorkspaceAnalysisDefinitionV2()
	ragV2 := conversationworkflow.RegisteredDefinitionV2()
	changedGraph := conversationworkflow.RegisteredWorkspaceAnalysisDefinitionV2().Graph
	changedGraph.Nodes[0].AllowedTools[0].Version++
	tests := []struct {
		name    string
		mode    conversationdomain.QuestionMode
		key     string
		version int64
		graph   json.RawMessage
	}{
		{"wrong key", conversationdomain.QuestionModeWorkspaceAnalysis, "unregistered-analysis", 2, questionDispatchVersionGraph(t, analysisV2.Graph)},
		{"v1 graph under v2", conversationdomain.QuestionModeWorkspaceAnalysis, analysisV2.Key, 2, questionDispatchVersionGraph(t, analysisV1.Graph)},
		{"v2 graph under v1", conversationdomain.QuestionModeWorkspaceAnalysis, analysisV1.Key, 1, questionDispatchVersionGraph(t, analysisV2.Graph)},
		{"changed tool hash", conversationdomain.QuestionModeWorkspaceAnalysis, analysisV2.Key, 2, questionDispatchVersionGraph(t, changedGraph)},
		{"analysis rebound to RAG", conversationdomain.QuestionModeWorkspaceAnalysis, ragV2.Key, 2, questionDispatchVersionGraph(t, ragV2.Graph)},
		{"RAG rebound to analysis", conversationdomain.QuestionModeRAG, analysisV2.Key, 2, questionDispatchVersionGraph(t, analysisV2.Graph)},
		{"unsupported RAG version", conversationdomain.QuestionModeRAG, ragV2.Key, 3, questionDispatchVersionGraph(t, ragV2.Graph)},
		{"missing graph", conversationdomain.QuestionModeWorkspaceAnalysis, analysisV2.Key, 2, nil},
		{"empty graph", conversationdomain.QuestionModeWorkspaceAnalysis, analysisV2.Key, 2, json.RawMessage(`{"nodes":[]}`)},
		{"unknown graph field", conversationdomain.QuestionModeWorkspaceAnalysis, analysisV2.Key, 2, json.RawMessage(strings.Replace(string(questionDispatchVersionGraph(t, analysisV2.Graph)), "{", `{"unexpected":true,`, 1))},
		{"trailing graph", conversationdomain.QuestionModeWorkspaceAnalysis, analysisV2.Key, 2, append(questionDispatchVersionGraph(t, analysisV2.Graph), []byte(` {}`)...)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			question, answerID := questionDispatchVersionFixture(test.mode)
			plan, err := buildReplayedQuestionWorkflowDispatchPlan(question, answerID, test.key, test.version, test.graph)
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Kind != foundation.ErrorConsistencyViolation ||
				classified.Code != ErrorCodeQuestionDispatchCorrupt || classified.Retryable ||
				!reflect.DeepEqual(plan, questionWorkflowDispatchPlan{}) {
				t.Fatalf("definition drift was not rejected without a partial plan: plan=%#v, err=%#v", plan, err)
			}
		})
	}
}

func TestQuestionDispatchVersionRejectsUnsupportedAnalysisVersion(t *testing.T) {
	question, answerID := questionDispatchVersionFixture(conversationdomain.QuestionModeWorkspaceAnalysis)
	for _, version := range []int64{-1, 0, 3} {
		t.Run(fmt.Sprintf("v%d", version), func(t *testing.T) {
			plan, err := buildQuestionWorkflowDispatchPlanForVersion(question, answerID, version)
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Kind != foundation.ErrorConsistencyViolation ||
				classified.Code != ErrorCodeQuestionDispatchCorrupt || !reflect.DeepEqual(plan, questionWorkflowDispatchPlan{}) {
				t.Fatalf("unsupported analysis version selected a workflow: plan=%#v, err=%#v", plan, err)
			}
		})
	}
}

func questionDispatchVersionFixture(mode conversationdomain.QuestionMode) (conversationdomain.Question, foundation.ID) {
	run := workspaceAnalysisAuditTestRun()
	return conversationdomain.Question{
		ID: run.QuestionID,
		Request: conversationdomain.QuestionRequest{
			WorkspaceID: run.WorkspaceID, ConversationID: run.ConversationID, Mode: mode,
		},
		Ordinal: 2, ContextHash: strings.Repeat("a", 64),
	}, run.AnswerID
}

func newQuestionDispatchVersionDispatcher(t *testing.T, runtime workflowapplication.ScopedRuntimeStarter) *GORMQuestionDispatcher {
	t.Helper()
	dispatcher, err := NewGORMQuestionDispatcherWithWorkspaceAnalysisV2AndAudit(
		questionConstructorPool(t), runtime, &questionConstructorAppender{}, foundation.NewUUIDGenerator(nil),
		foundation.SystemClock{}, &questionAnalysisRunStarter{}, &workspaceAnalysisAuditCapture{},
	)
	if err != nil {
		t.Fatal(err)
	}
	return dispatcher
}

func questionDispatchVersionGraph(t *testing.T, graph workflowdomain.CanonicalGraph) json.RawMessage {
	t.Helper()
	// PostgreSQL JSONB does not retain the original JSON formatting.
	document, err := json.MarshalIndent(graph, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return document
}

type questionDispatchVersionRuntime struct {
	requests []workflowapplication.RuntimeStartRequest
	replayed bool
}

func (runtime *questionDispatchVersionRuntime) StartScoped(_ context.Context, _ foundation.TransactionScope, request workflowapplication.RuntimeStartRequest) (workflowapplication.RuntimeStartResult, error) {
	runtime.requests = append(runtime.requests, request)
	return workflowapplication.RuntimeStartResult{
		Run: request.Run, FirstNode: request.FirstNode,
		Job: workflowapplication.JobReceipt{JobID: 1, Duplicate: runtime.replayed}, Replayed: runtime.replayed,
	}, nil
}
