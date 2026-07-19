//go:build integration

package postgres

import (
	"strings"
	"testing"
	"time"

	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

func TestRepositoryLoadsBoundedQuestionExecutionContext(t *testing.T) {
	repository, pool, ctx := newConversationTestRepository(t)
	workspaceID := conversationTurnID(30)
	conversationID := conversationTurnID(31)
	seedConversationWorkspaces(t, ctx, pool, workspaceID)
	now := time.Date(2026, 7, 20, 13, 0, 0, 0, time.UTC)
	if _, err := repository.CreateConversation(ctx, conversationCreateRecord(
		t, workspaceID, conversationID, "Execution context", "execution-context-conversation", now,
	)); err != nil {
		t.Fatal(err)
	}
	fixture := seedConversationRuntimeBase(t, ctx, pool, workspaceID, conversationID, 4000, now)
	seedPublishedTurn(t, ctx, pool, fixture, 1, conversationdomain.AnswerPublicationCompleted,
		"First bounded question", "First approved answer", now.Add(time.Minute))
	seedPublishedTurn(t, ctx, pool, fixture, 2, conversationdomain.AnswerPublicationRefused,
		"Second bounded question", "No approved evidence", now.Add(2*time.Minute))
	request, err := conversationdomain.CanonicalizeQuestionRequest(conversationdomain.QuestionRequest{
		WorkspaceID: workspaceID, ConversationID: conversationID, QuestionText: "Use the frozen execution context",
		Scope: conversationdomain.QuestionScope{
			RetrievalMode: retrievaldomain.SearchModeKeyword,
			Filter:        retrievaldomain.SearchFilter{PathPrefixes: []string{"docs/architecture"}},
		},
		AnswerDepth: conversationdomain.AnswerDepthDetailed, OutputFormat: conversationdomain.OutputFormatOutline,
	})
	if err != nil {
		t.Fatal(err)
	}
	requestHash, err := conversationdomain.ComputeQuestionRequestHash(request)
	if err != nil {
		t.Fatal(err)
	}
	dispatched, err := newQuestionDispatcherIntegration(t, pool).SubmitQuestion(ctx, conversationapplication.SubmitQuestionRecord{
		Request: request, IdempotencyKey: "execution-context-question", RequestHash: requestHash,
	})
	if err != nil {
		t.Fatal(err)
	}

	counted, counter := countingConversationRepository(t, pool)
	loaded, err := counted.LoadQuestionExecutionContext(ctx, conversationapplication.QuestionExecutionContextQuery{
		WorkspaceID: workspaceID, WorkflowRunID: dispatched.Workflow.RunID,
		ConversationID: conversationID, QuestionID: dispatched.Question.ID, AnswerID: dispatched.Answer.ID,
		QuestionOrdinal: dispatched.Question.Ordinal, ContextHash: dispatched.Question.ContextHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	if counter.total() != 2 {
		t.Fatalf("LoadQuestionExecutionContext used %d database calls, want 2", counter.total())
	}
	if loaded.Question.ID != dispatched.Question.ID || loaded.Answer.ID != dispatched.Answer.ID ||
		loaded.Answer.WorkflowRunID != dispatched.Workflow.RunID || loaded.Question.Request.AnswerDepth != conversationdomain.AnswerDepthDetailed ||
		loaded.Question.Request.OutputFormat != conversationdomain.OutputFormatOutline ||
		loaded.Question.Request.Scope.RetrievalMode != retrievaldomain.SearchModeKeyword ||
		len(loaded.Question.Request.Scope.Filter.PathPrefixes) != 1 || loaded.Question.Request.Scope.Filter.PathPrefixes[0] != "docs/architecture" ||
		len(loaded.History) != 2 || loaded.History[0].Ordinal != 1 || loaded.History[1].Ordinal != 2 {
		t.Fatalf("execution context = %#v", loaded)
	}
	hash, through, _, err := conversationdomain.ComputeContextHash(loaded.History)
	if err != nil || hash != dispatched.Question.ContextHash || through != dispatched.Question.ContextThroughOrdinal {
		t.Fatalf("execution context hash=%q through=%d error=%v", hash, through, err)
	}
	for _, id := range []foundation.ID{loaded.Question.ID, loaded.Answer.ID, loaded.Answer.WorkflowRunID} {
		if id == "" {
			t.Fatal("execution context returned an empty identity")
		}
	}
}

func TestRepositoryQuestionExecutionContextRejectsBindingDrift(t *testing.T) {
	repository, pool, ctx := newConversationTestRepository(t)
	workspaceID := conversationTurnID(40)
	conversationID := conversationTurnID(41)
	seedConversationWorkspaces(t, ctx, pool, workspaceID)
	now := time.Date(2026, 7, 20, 14, 0, 0, 0, time.UTC)
	if _, err := repository.CreateConversation(ctx, conversationCreateRecord(
		t, workspaceID, conversationID, "Binding drift", "execution-context-binding", now,
	)); err != nil {
		t.Fatal(err)
	}
	dispatched, err := newQuestionDispatcherIntegration(t, pool).SubmitQuestion(ctx, questionDispatchRecord(
		t, workspaceID, conversationID, "Reject every drifted execution binding", "execution-context-binding-question",
	))
	if err != nil {
		t.Fatal(err)
	}
	valid := conversationapplication.QuestionExecutionContextQuery{
		WorkspaceID: workspaceID, WorkflowRunID: dispatched.Workflow.RunID,
		ConversationID: conversationID, QuestionID: dispatched.Question.ID, AnswerID: dispatched.Answer.ID,
		QuestionOrdinal: dispatched.Question.Ordinal, ContextHash: dispatched.Question.ContextHash,
	}
	tests := []struct {
		name   string
		mutate func(*conversationapplication.QuestionExecutionContextQuery)
	}{
		{name: "workspace", mutate: func(query *conversationapplication.QuestionExecutionContextQuery) {
			query.WorkspaceID = conversationTurnID(50)
		}},
		{name: "workflow run", mutate: func(query *conversationapplication.QuestionExecutionContextQuery) {
			query.WorkflowRunID = conversationTurnID(51)
		}},
		{name: "conversation", mutate: func(query *conversationapplication.QuestionExecutionContextQuery) {
			query.ConversationID = conversationTurnID(52)
		}},
		{name: "question", mutate: func(query *conversationapplication.QuestionExecutionContextQuery) {
			query.QuestionID = conversationTurnID(53)
		}},
		{name: "answer", mutate: func(query *conversationapplication.QuestionExecutionContextQuery) {
			query.AnswerID = conversationTurnID(54)
		}},
		{name: "ordinal", mutate: func(query *conversationapplication.QuestionExecutionContextQuery) { query.QuestionOrdinal++ }},
		{name: "context hash", mutate: func(query *conversationapplication.QuestionExecutionContextQuery) {
			query.ContextHash = strings.Repeat("f", 64)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			query := valid
			test.mutate(&query)
			_, err := repository.LoadQuestionExecutionContext(ctx, query)
			requireConversationRepositoryError(t, err, foundation.ErrorConsistencyViolation, ErrorCodeExecutionContextCorrupt)
		})
	}
}

func TestRepositoryQuestionExecutionContextRecomputesPersistedHistoryHash(t *testing.T) {
	repository, pool, ctx := newConversationTestRepository(t)
	workspaceID := conversationTurnID(60)
	conversationID := conversationTurnID(61)
	seedConversationWorkspaces(t, ctx, pool, workspaceID)
	now := time.Date(2026, 7, 20, 15, 0, 0, 0, time.UTC)
	if _, err := repository.CreateConversation(ctx, conversationCreateRecord(
		t, workspaceID, conversationID, "Hash drift", "execution-context-hash", now,
	)); err != nil {
		t.Fatal(err)
	}
	fixture := seedConversationRuntimeBase(t, ctx, pool, workspaceID, conversationID, 5000, now)
	seedPublishedTurn(t, ctx, pool, fixture, 1, conversationdomain.AnswerPublicationCompleted,
		"First hash question", "First hash answer", now.Add(time.Minute))
	seedPublishedTurn(t, ctx, pool, fixture, 2, conversationdomain.AnswerPublicationRefused,
		"Second hash question", "Second hash refusal", now.Add(2*time.Minute))
	seedWorkflowRun(t, ctx, pool, fixture, 3, now.Add(3*time.Minute))
	request, err := conversationdomain.CanonicalizeQuestionRequest(conversationdomain.QuestionRequest{
		WorkspaceID: workspaceID, ConversationID: conversationID, QuestionText: "This context hash is forged",
		Scope: conversationdomain.QuestionScope{RetrievalMode: retrievaldomain.SearchModeKeyword},
	})
	if err != nil {
		t.Fatal(err)
	}
	requestHash, err := conversationdomain.ComputeQuestionRequestHash(request)
	if err != nil {
		t.Fatal(err)
	}
	forgedHash := strings.Repeat("f", 64)
	scope, err := conversationdomain.EncodeQuestionScope(request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agent.question(
		id,workspace_id,conversation_id,ordinal,question_text,scope,answer_depth,output_format,
		context_through_ordinal,context_hash,idempotency_key,request_hash,created_at
	) VALUES($1,$2,$3,3,$4,$5::jsonb,$6,$7,2,$8,'execution-context-forged',$9,$10)`,
		string(fixture.questionID(3)), string(workspaceID), string(conversationID), request.QuestionText, scope,
		string(request.AnswerDepth), string(request.OutputFormat), forgedHash, requestHash, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agent.answer(
		id,workspace_id,conversation_id,question_id,workflow_run_id,publication_status,version,created_at,updated_at
	) VALUES($1,$2,$3,$4,$5,'pending',1,$6,$6)`, string(fixture.answerID(3)), string(workspaceID),
		string(conversationID), string(fixture.questionID(3)), string(fixture.runID(3)), now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}

	_, err = repository.LoadQuestionExecutionContext(ctx, conversationapplication.QuestionExecutionContextQuery{
		WorkspaceID: workspaceID, WorkflowRunID: fixture.runID(3), ConversationID: conversationID,
		QuestionID: fixture.questionID(3), AnswerID: fixture.answerID(3), QuestionOrdinal: 3, ContextHash: forgedHash,
	})
	requireConversationRepositoryError(t, err, foundation.ErrorConsistencyViolation, ErrorCodeExecutionContextCorrupt)
}
