package workflow

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func TestBuildRAGModelInputsUsesOnlyValidatedConversationFacts(t *testing.T) {
	execution := validRAGExecutionContext(t)
	nonEvidence := agentapplication.NonEvidenceContext{
		UntrustedData:   true,
		UserPreferences: []json.RawMessage{json.RawMessage(`{"tone":"concise"}`)},
		TaskContext:     []json.RawMessage{json.RawMessage(`{"goal":"prepare rollout"}`)},
	}
	plan, answer, err := buildRAGModelInputs(execution, nonEvidence)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plan, answer) {
		t.Fatal("plan and answer base inputs differ")
	}
	var document map[string]any
	if err := json.Unmarshal(plan, &document); err != nil {
		t.Fatal(err)
	}
	if document["schema_version"] != float64(2) || document["question"] != execution.Question.Request.QuestionText || document["untrusted_data"] != true || document["answer_depth"] != "standard" {
		t.Fatalf("document=%#v", document)
	}
	contextDocument, ok := document["non_evidence_context"].(map[string]any)
	if !ok || contextDocument["untrusted_data"] != true || len(contextDocument["user_preferences"].([]any)) != 1 || len(contextDocument["task_context"].([]any)) != 1 {
		t.Fatalf("non_evidence_context=%#v", document["non_evidence_context"])
	}
	encoded := string(plan)
	for _, forbidden := range []string{string(execution.Question.Request.WorkspaceID), string(execution.Question.ID), execution.Question.ContextHash} {
		if bytes.Contains(plan, []byte(forbidden)) {
			t.Fatalf("model input leaked persistence binding %q: %s", forbidden, encoded)
		}
	}
}

func TestBuildRAGModelInputsRejectsContextHashDrift(t *testing.T) {
	execution := validRAGExecutionContext(t)
	execution.Question.ContextHash = ragInputHash('f')
	if _, _, err := buildRAGModelInputs(execution, emptyNonEvidenceContext()); err == nil {
		t.Fatal("context hash drift was accepted")
	}
}

func emptyNonEvidenceContext() agentapplication.NonEvidenceContext {
	return agentapplication.NonEvidenceContext{UntrustedData: true, UserPreferences: []json.RawMessage{}, TaskContext: []json.RawMessage{}}
}

func validRAGExecutionContext(t *testing.T) conversationapplication.QuestionExecutionContext {
	t.Helper()
	workspaceID := ragInputID(1)
	conversationID := ragInputID(2)
	questionID := ragInputID(3)
	answerID := ragInputID(4)
	workflowRunID := ragInputID(5)
	now := time.Unix(100, 0).UTC()
	request, err := conversationdomain.CanonicalizeQuestionRequest(conversationdomain.QuestionRequest{
		WorkspaceID: workspaceID, ConversationID: conversationID, QuestionText: "How should deployment work?",
		Scope:       conversationdomain.QuestionScope{RetrievalMode: retrievaldomain.SearchModeHybrid},
		AnswerDepth: conversationdomain.AnswerDepthStandard, OutputFormat: conversationdomain.OutputFormatMarkdown,
	})
	if err != nil {
		t.Fatal(err)
	}
	requestHash, err := conversationdomain.ComputeQuestionRequestHash(request)
	if err != nil {
		t.Fatal(err)
	}
	contextHash, _, _, err := conversationdomain.ComputeContextHash(nil)
	if err != nil {
		t.Fatal(err)
	}
	question := conversationdomain.Question{
		ID: questionID, Request: request, Ordinal: 1, ContextThroughOrdinal: 0,
		ContextHash: contextHash, RequestHash: requestHash, CreatedAt: now,
	}
	answer := conversationdomain.Answer{
		ID: answerID, WorkspaceID: workspaceID, ConversationID: conversationID, QuestionID: questionID,
		WorkflowRunID: workflowRunID, PublicationStatus: conversationdomain.AnswerPublicationPending,
		Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	return conversationapplication.QuestionExecutionContext{Question: question, Answer: answer, History: []conversationdomain.PublishedTurn{}}
}

func ragInputID(value byte) foundation.ID {
	return foundation.ID("61000000-0000-4000-8000-00000000000" + string(rune('0'+value)))
}

func ragInputHash(value byte) string { return string(bytes.Repeat([]byte{value}, 64)) }
