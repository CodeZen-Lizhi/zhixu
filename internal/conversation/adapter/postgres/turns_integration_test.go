//go:build integration

package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	conversationapplication "github.com/CodeZen-Lizhi/zhixu/internal/conversation/application"
	conversationdomain "github.com/CodeZen-Lizhi/zhixu/internal/conversation/domain"
	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	retrievaldomain "github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRepositoryReadsTurnsAnswersAndPublishedContextWithoutCrossWorkspaceLeak(t *testing.T) {
	repository, pool, ctx := newConversationTestRepository(t)
	workspaceA := conversationTurnID(1)
	workspaceB := conversationTurnID(2)
	seedConversationWorkspaces(t, ctx, pool, workspaceA, workspaceB)
	now := time.Date(2026, 7, 19, 15, 0, 0, 0, time.UTC)
	conversationRecord := conversationCreateRecord(t, workspaceA, conversationTurnID(3), "Turn reads", "turn-read-conversation", now)
	if _, err := repository.CreateConversation(ctx, conversationRecord); err != nil {
		t.Fatal(err)
	}
	fixture := seedConversationRuntimeBase(t, ctx, pool, workspaceA, conversationRecord.Conversation.ID, 1000, now)
	seedPublishedTurn(t, ctx, pool, fixture, 1, conversationdomain.AnswerPublicationCompleted, "First question", "Approved answer", now.Add(time.Minute))
	seedPublishedTurn(t, ctx, pool, fixture, 3, conversationdomain.AnswerPublicationRefused, "Third question", "No approved evidence", now.Add(3*time.Minute))
	seedPendingTurn(t, ctx, pool, fixture, 2, "Second question", now.Add(2*time.Minute))
	pendingRunID := fixture.runID(2)
	completedRunID := fixture.runID(1)
	seedRAGStageEvent(t, ctx, pool, workspaceB, nil, nil, fixture.answerID(2), "validation.completed", now.Add(4*time.Minute), "cross-workspace-stage")
	seedRAGStageEvent(t, ctx, pool, workspaceA, &conversationRecord.Conversation.ID, &pendingRunID, fixture.answerID(2), "plan.started", now.Add(5*time.Minute), "pending-plan-started")
	seedRAGStageEvent(t, ctx, pool, workspaceA, &conversationRecord.Conversation.ID, &pendingRunID, fixture.answerID(2), "retrieval.completed", now.Add(6*time.Minute), "pending-retrieval-completed")
	seedRAGStageEvent(t, ctx, pool, workspaceA, &conversationRecord.Conversation.ID, &completedRunID, fixture.answerID(1), "validation.completed", now.Add(7*time.Minute), "completed-validation")

	counted, counter := countingConversationRepository(t, pool)
	first, err := counted.ListTurns(ctx, conversationapplication.ListTurnsQuery{
		WorkspaceID: workspaceA, ConversationID: conversationRecord.Conversation.ID, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if counter.total() > 2 {
		t.Fatalf("ListTurns used %d database calls for a page", counter.total())
	}
	if len(first.Items) != 2 || first.Items[0].Question.Ordinal != 1 || first.Items[1].Question.Ordinal != 2 || first.NextCursor == nil {
		t.Fatalf("first turn page = %#v", first)
	}
	if first.Items[0].Answer == nil || first.Items[0].Answer.AssistantText != "Approved answer" || len(first.Items[0].Answer.Citations) != 1 {
		t.Fatalf("completed turn = %#v", first.Items[0])
	}
	if first.Items[1].Answer == nil || first.Items[1].Answer.Answer.PublicationStatus != conversationdomain.AnswerPublicationPending ||
		first.Items[1].Answer.Workflow.Status != "running" || first.Items[1].Answer.AssistantText != "" ||
		first.Items[1].Answer.CurrentStage == nil || *first.Items[1].Answer.CurrentStage != conversationapplication.RAGCurrentStageRetrievalCompleted {
		t.Fatalf("pending turn = %#v", first.Items[1])
	}
	second, err := counted.ListTurns(ctx, conversationapplication.ListTurnsQuery{
		WorkspaceID: workspaceA, ConversationID: conversationRecord.Conversation.ID, Cursor: first.NextCursor, Limit: 2,
	})
	if err != nil || len(second.Items) != 1 || second.Items[0].Question.Ordinal != 3 || second.NextCursor != nil {
		t.Fatalf("second turn page = %#v, %v", second, err)
	}
	if second.Items[0].Answer == nil || second.Items[0].Answer.AssistantText != "No approved evidence" ||
		second.Items[0].Answer.Citations == nil || len(second.Items[0].Answer.Citations) != 0 {
		t.Fatalf("refused turn = %#v", second.Items[0])
	}
	if second.Items[0].Answer.CurrentStage != nil {
		t.Fatalf("answer without RAG event stage = %#v", second.Items[0].Answer.CurrentStage)
	}
	latest, err := counted.ListTurns(ctx, conversationapplication.ListTurnsQuery{
		WorkspaceID: workspaceA, ConversationID: conversationRecord.Conversation.ID, Limit: 1, Latest: true,
	})
	if err != nil || len(latest.Items) != 1 || latest.Items[0].Question.Ordinal != 3 || latest.NextCursor != nil {
		t.Fatalf("latest turn projection = %#v, %v", latest, err)
	}

	completedAnswerID := fixture.answerID(1)
	answer, err := counted.GetAnswer(ctx, workspaceA, completedAnswerID)
	if err != nil || answer.Answer.ID != completedAnswerID || answer.AssistantText != "Approved answer" ||
		len(answer.Citations) != 1 || answer.Citations[0].ID != "citation-1" || answer.CurrentStage == nil ||
		*answer.CurrentStage != conversationapplication.RAGCurrentStageValidationCompleted {
		t.Fatalf("GetAnswer() = %#v, %v", answer, err)
	}
	refusedAnswer, err := counted.GetAnswer(ctx, workspaceA, fixture.answerID(3))
	if err != nil || refusedAnswer.Answer.PublicationStatus != conversationdomain.AnswerPublicationRefused ||
		refusedAnswer.Citations == nil || len(refusedAnswer.Citations) != 0 {
		t.Fatalf("GetAnswer(refused) = %#v, %v", refusedAnswer, err)
	}
	_, crossWorkspaceErr := counted.GetAnswer(ctx, workspaceB, completedAnswerID)
	_, missingErr := counted.GetAnswer(ctx, workspaceA, conversationTurnID(999))
	requireConversationRepositoryError(t, crossWorkspaceErr, foundation.ErrorNotFound, ErrorCodeAnswerNotFound)
	requireConversationRepositoryError(t, missingErr, foundation.ErrorNotFound, ErrorCodeAnswerNotFound)

	counter.reset()
	contextTurns, err := counted.LoadPublishedContext(ctx, conversationapplication.PublishedContextQuery{
		WorkspaceID: workspaceA, ConversationID: conversationRecord.Conversation.ID, ThroughOrdinal: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	if counter.total() > 2 {
		t.Fatalf("LoadPublishedContext used %d database calls", counter.total())
	}
	if len(contextTurns) != 2 || contextTurns[0].Ordinal != 1 || contextTurns[1].Ordinal != 3 ||
		contextTurns[0].AssistantText != "Approved answer" || contextTurns[1].AssistantText != "No approved evidence" {
		t.Fatalf("published context = %#v", contextTurns)
	}
}

func seedRAGStageEvent(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID foundation.ID,
	conversationID, workflowRunID *foundation.ID,
	answerID foundation.ID,
	stage string,
	at time.Time,
	sourceRef string,
) {
	t.Helper()
	eventType := "rag." + stage
	resourceRef := "answer:" + string(answerID)
	payload, err := json.Marshal(map[string]string{"answer_id": string(answerID), "stage": stage})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ops.server_event(
		workspace_id,conversation_id,workflow_run_id,event_type,resource_ref,resource_version,
		payload_summary,schema_version,source_event_ref,occurred_at,expires_at
	) VALUES($1,$2,$3,$4,$5,1,$6::jsonb,1,$7,$8::timestamptz,$8::timestamptz + interval '24 hours')`,
		string(workspaceID), optionalID(conversationID), optionalID(workflowRunID), eventType, resourceRef, payload,
		sourceRef, at); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryRejectsMismatchedLatestRAGStageEvent(t *testing.T) {
	repository, pool, ctx := newConversationTestRepository(t)
	workspaceID := conversationTurnID(4)
	seedConversationWorkspaces(t, ctx, pool, workspaceID)
	now := time.Date(2026, 7, 19, 15, 30, 0, 0, time.UTC)
	conversationRecord := conversationCreateRecord(t, workspaceID, conversationTurnID(5), "Invalid stage", "invalid-stage-conversation", now)
	if _, err := repository.CreateConversation(ctx, conversationRecord); err != nil {
		t.Fatal(err)
	}
	fixture := seedConversationRuntimeBase(t, ctx, pool, workspaceID, conversationRecord.Conversation.ID, 1500, now)
	seedPendingTurn(t, ctx, pool, fixture, 1, "Question", now.Add(time.Minute))
	payload, err := json.Marshal(map[string]string{"answer_id": string(fixture.answerID(1)), "stage": "retrieval.started"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ops.server_event(
		workspace_id,conversation_id,workflow_run_id,event_type,resource_ref,resource_version,
		payload_summary,schema_version,source_event_ref,occurred_at,expires_at
	) VALUES($1,$2,$3,'rag.plan.started',$4,1,$5::jsonb,1,'mismatched-stage',$6::timestamptz,$6::timestamptz + interval '24 hours')`,
		string(workspaceID), string(conversationRecord.Conversation.ID), string(fixture.runID(1)),
		"answer:"+string(fixture.answerID(1)), payload, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	_, err = repository.GetAnswer(ctx, workspaceID, fixture.answerID(1))
	requireConversationRepositoryError(t, err, foundation.ErrorConsistencyViolation, ErrorCodePersistenceCorrupt)
}

func optionalID(value *foundation.ID) any {
	if value == nil {
		return nil
	}
	return string(*value)
}

func TestRepositoryPublishedContextUsesLatestEightAndThirtyTwoKiBBudget(t *testing.T) {
	repository, pool, ctx := newConversationTestRepository(t)
	workspaceID := conversationTurnID(10)
	seedConversationWorkspaces(t, ctx, pool, workspaceID)
	now := time.Date(2026, 7, 19, 16, 0, 0, 0, time.UTC)
	conversationRecord := conversationCreateRecord(t, workspaceID, conversationTurnID(11), "Bounded context", "bounded-context", now)
	if _, err := repository.CreateConversation(ctx, conversationRecord); err != nil {
		t.Fatal(err)
	}
	fixture := seedConversationRuntimeBase(t, ctx, pool, workspaceID, conversationRecord.Conversation.ID, 2000, now)
	questionText := strings.Repeat("q", 4*1024)
	assistantText := strings.Repeat("a", 4*1024)
	for ordinal := int64(1); ordinal <= 10; ordinal++ {
		seedPublishedTurn(t, ctx, pool, fixture, ordinal, conversationdomain.AnswerPublicationRefused, questionText, assistantText, now.Add(time.Duration(ordinal)*time.Minute))
	}

	counted, counter := countingConversationRepository(t, pool)
	turns, err := counted.LoadPublishedContext(ctx, conversationapplication.PublishedContextQuery{
		WorkspaceID: workspaceID, ConversationID: conversationRecord.Conversation.ID, ThroughOrdinal: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if counter.total() > 2 {
		t.Fatalf("10-turn context used %d database calls", counter.total())
	}
	if len(turns) != 4 || turns[0].Ordinal != 7 || turns[3].Ordinal != 10 {
		t.Fatalf("bounded context ordinals = %#v", turns)
	}
	_, through, byteCount, err := conversationdomain.ComputeContextHash(turns)
	if err != nil || through != 10 || byteCount != conversationdomain.MaxContextBytes {
		t.Fatalf("context hash through=%d bytes=%d error=%v", through, byteCount, err)
	}
}

func TestRepositoryListTurnsRejectsQuestionWithoutAnswerSlot(t *testing.T) {
	repository, pool, ctx := newConversationTestRepository(t)
	workspaceID := conversationTurnID(20)
	seedConversationWorkspaces(t, ctx, pool, workspaceID)
	now := time.Date(2026, 7, 19, 17, 0, 0, 0, time.UTC)
	conversationRecord := conversationCreateRecord(t, workspaceID, conversationTurnID(21), "Corrupt turn", "corrupt-turn", now)
	if _, err := repository.CreateConversation(ctx, conversationRecord); err != nil {
		t.Fatal(err)
	}
	fixture := conversationRuntimeFixture{workspaceID: workspaceID, conversationID: conversationRecord.Conversation.ID, idBase: 3000}
	question := conversationQuestion(t, fixture, 1, "Question without an answer", now.Add(time.Minute))
	scope, err := conversationdomain.EncodeQuestionScope(question.Request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agent.question(
		id,workspace_id,conversation_id,ordinal,question_text,scope,answer_depth,output_format,
		context_through_ordinal,context_hash,idempotency_key,request_hash,created_at
	) VALUES($1,$2,$3,$4,$5,$6::jsonb,$7,$8,$9,$10,'missing-answer',$11,$12)`,
		string(question.ID), string(workspaceID), string(conversationRecord.Conversation.ID), question.Ordinal,
		question.Request.QuestionText, scope, string(question.Request.AnswerDepth), string(question.Request.OutputFormat),
		question.ContextThroughOrdinal, question.ContextHash, question.RequestHash, question.CreatedAt); err != nil {
		t.Fatal(err)
	}

	_, err = repository.ListTurns(ctx, conversationapplication.ListTurnsQuery{
		WorkspaceID: workspaceID, ConversationID: conversationRecord.Conversation.ID, Limit: 10,
	})
	requireConversationRepositoryError(t, err, foundation.ErrorConsistencyViolation, ErrorCodePersistenceCorrupt)
}

type conversationRuntimeFixture struct {
	workspaceID, conversationID foundation.ID
	definitionID, indexID       foundation.ID
	idBase                      int
}

func (fixture conversationRuntimeFixture) questionID(ordinal int64) foundation.ID {
	return conversationTurnID(fixture.idBase + int(ordinal)*10 + 1)
}

func (fixture conversationRuntimeFixture) answerID(ordinal int64) foundation.ID {
	return conversationTurnID(fixture.idBase + int(ordinal)*10 + 2)
}

func (fixture conversationRuntimeFixture) runID(ordinal int64) foundation.ID {
	return conversationTurnID(fixture.idBase + int(ordinal)*10 + 3)
}

func (fixture conversationRuntimeFixture) nodeID(ordinal int64) foundation.ID {
	return conversationTurnID(fixture.idBase + int(ordinal)*10 + 4)
}

func (fixture conversationRuntimeFixture) attemptID(ordinal int64) foundation.ID {
	return conversationTurnID(fixture.idBase + int(ordinal)*10 + 5)
}

func (fixture conversationRuntimeFixture) modelRunID(ordinal int64) foundation.ID {
	return conversationTurnID(fixture.idBase + int(ordinal)*10 + 6)
}

func (fixture conversationRuntimeFixture) modelCallID(ordinal int64) foundation.ID {
	return conversationTurnID(fixture.idBase + int(ordinal)*10 + 7)
}

func seedConversationRuntimeBase(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	workspaceID, conversationID foundation.ID,
	idBase int,
	at time.Time,
) conversationRuntimeFixture {
	t.Helper()
	fixture := conversationRuntimeFixture{
		workspaceID: workspaceID, conversationID: conversationID,
		definitionID: conversationTurnID(idBase + 1), indexID: conversationTurnID(idBase + 2), idBase: idBase + 100,
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
		VALUES($1,$2,$3,1,'{"nodes":[]}'::jsonb,$4)`, string(fixture.definitionID), string(workspaceID), fmt.Sprintf("conversation-read-%d", idBase), at); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO retrieval.index_version(
		id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
		source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,version,created_at,updated_at
	) VALUES($1,$2,'simple','v1',repeat('1',64),'{}'::jsonb,$3,repeat('2',64),0,$4,'building','["vector"]'::jsonb,1,$5,$5)`,
		string(fixture.indexID), string(workspaceID), fmt.Sprintf("conversation-read:index:%d", idBase), fmt.Sprintf("conversation-read-index-%d", idBase), at); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func seedPendingTurn(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture conversationRuntimeFixture,
	ordinal int64,
	questionText string,
	at time.Time,
) {
	t.Helper()
	seedWorkflowRun(t, ctx, pool, fixture, ordinal, at)
	question := conversationQuestion(t, fixture, ordinal, questionText, at)
	scope, err := conversationdomain.EncodeQuestionScope(question.Request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agent.question(
		id,workspace_id,conversation_id,ordinal,question_text,scope,answer_depth,output_format,
		context_through_ordinal,context_hash,idempotency_key,request_hash,created_at
	) VALUES($1,$2,$3,$4,$5,$6::jsonb,$7,$8,$9,$10,$11,$12,$13)`,
		string(question.ID), string(question.Request.WorkspaceID), string(question.Request.ConversationID), question.Ordinal,
		question.Request.QuestionText, scope, string(question.Request.AnswerDepth), string(question.Request.OutputFormat),
		question.ContextThroughOrdinal, question.ContextHash, fmt.Sprintf("question-%d", ordinal), question.RequestHash, question.CreatedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agent.answer(
		id,workspace_id,conversation_id,question_id,workflow_run_id,publication_status,version,created_at,updated_at
	) VALUES($1,$2,$3,$4,$5,'pending',1,$6,$6)`, string(fixture.answerID(ordinal)), string(fixture.workspaceID),
		string(fixture.conversationID), string(question.ID), string(fixture.runID(ordinal)), at); err != nil {
		t.Fatal(err)
	}
}

func seedPublishedTurn(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture conversationRuntimeFixture,
	ordinal int64,
	status conversationdomain.AnswerPublicationStatus,
	questionText, assistantText string,
	at time.Time,
) {
	t.Helper()
	seedPendingTurn(t, ctx, pool, fixture, ordinal, questionText, at)
	modelRunID := fixture.modelRunID(ordinal)
	seedTerminalModelRun(t, ctx, pool, fixture, ordinal, status, at)
	result, summary := publishedTurnDocuments(t, fixture, ordinal, status, modelRunID, assistantText)
	publishedAt := at.Add(3 * time.Millisecond)
	if _, err := pool.Exec(ctx, `UPDATE agent.answer SET
		model_run_id=$2,publication_status=$3,result_type=$4,result=$5::jsonb,result_hash=$6,
		retrieval_summary=$7::jsonb,version=2,updated_at=$8,published_at=$8
		WHERE id=$1`, string(fixture.answerID(ordinal)), string(modelRunID), string(status), string(result.Type),
		result.Document, result.Hash, summary, publishedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE workflow.run SET
		status='succeeded',version=version+1,updated_at=$2,completed_at=$2 WHERE id=$1`,
		string(fixture.runID(ordinal)), publishedAt.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}
}

func seedWorkflowRun(t *testing.T, ctx context.Context, pool *pgxpool.Pool, fixture conversationRuntimeFixture, ordinal int64, at time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at)
		VALUES($1,$2,$3,'running','{}'::jsonb,1,$4,$4)`, string(fixture.runID(ordinal)), string(fixture.workspaceID), string(fixture.definitionID), at); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_run(
		id,run_id,node_key,node_type,status,input,lease_owner,lease_until,version,created_at,updated_at
	) VALUES($1,$2,'rag','agent.rag-answer','running','{}'::jsonb,'conversation-worker',$3,1,$4,$4)`,
		string(fixture.nodeID(ordinal)), string(fixture.runID(ordinal)), at.Add(time.Hour), at); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_attempt(
		id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at
	) VALUES($1,$2,1,1,0,$3,'conversation-worker',$4,'running',$5)`, string(fixture.attemptID(ordinal)),
		string(fixture.nodeID(ordinal)), fmt.Sprintf("conversation-delivery-%d", ordinal), at.Add(time.Hour), at); err != nil {
		t.Fatal(err)
	}
}

func seedTerminalModelRun(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	fixture conversationRuntimeFixture,
	ordinal int64,
	status conversationdomain.AnswerPublicationStatus,
	at time.Time,
) {
	t.Helper()
	modelRunID := fixture.modelRunID(ordinal)
	if _, err := pool.Exec(ctx, `INSERT INTO agent.model_run(
		id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,
		adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,
		prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,
		reduced_schema_id,reduced_schema_version,retrieval_index_version_id,status,version,started_at,updated_at
	) VALUES($1,$2,$3,$4,$5,'openai-compatible','v1','model-test','2026-07-01','default','v1',
		'rag-answer','v2','agent.rag-answer','v2','agent.refusal','v1',$6,'RUNNING',1,$7,$7)`,
		string(modelRunID), string(fixture.workspaceID), string(fixture.runID(ordinal)), string(fixture.nodeID(ordinal)),
		string(fixture.attemptID(ordinal)), string(fixture.indexID), at); err != nil {
		t.Fatal(err)
	}
	callCompletedAt := at.Add(time.Millisecond)
	if _, err := pool.Exec(ctx, `INSERT INTO agent.model_call(
		id,model_run_id,call_no,phase,adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,
		prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,max_output_tokens,
		request_hash,request_bytes,status,version,started_at
	) VALUES($1,$2,1,'INITIAL','openai-compatible','v1','model-test','2026-07-01','default','v1',
		'rag-answer','v2','agent.rag-answer','v2',256,repeat('6',64),64,'STARTED',1,$3)`,
		string(fixture.modelCallID(ordinal)), string(modelRunID), at); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent.model_call SET
		response_hash=repeat('7',64),response_bytes=64,input_tokens=8,output_tokens=6,latency_ms=1,
		status='SUCCEEDED',version=2,completed_at=$2 WHERE id=$1`,
		string(fixture.modelCallID(ordinal)), callCompletedAt); err != nil {
		t.Fatal(err)
	}
	modelStatus := "SUCCEEDED"
	resultType := "rag_answer"
	var errorCode any
	switch status {
	case conversationdomain.AnswerPublicationRefused:
		modelStatus = "REFUSED"
		resultType = "refusal"
		errorCode = "NO_RELEVANT_EVIDENCE"
	case conversationdomain.AnswerPublicationClarificationRequired:
		resultType = "clarification"
	}
	if _, err := pool.Exec(ctx, `UPDATE agent.model_run SET
		status=$2,final_result_type=$3,error_code=$4,version=2,updated_at=$5,completed_at=$5 WHERE id=$1`,
		string(modelRunID), modelStatus, resultType, errorCode, at.Add(2*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
}

func conversationQuestion(t *testing.T, fixture conversationRuntimeFixture, ordinal int64, text string, at time.Time) conversationdomain.Question {
	t.Helper()
	request, err := conversationdomain.CanonicalizeQuestionRequest(conversationdomain.QuestionRequest{
		WorkspaceID: fixture.workspaceID, ConversationID: fixture.conversationID, QuestionText: text,
		Scope: conversationdomain.QuestionScope{RetrievalMode: retrievaldomain.SearchModeKeyword},
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
	return conversationdomain.Question{
		ID: fixture.questionID(ordinal), Request: request, Ordinal: ordinal,
		ContextThroughOrdinal: 0, ContextHash: contextHash, RequestHash: requestHash, CreatedAt: at,
	}
}

func publishedTurnDocuments(
	t *testing.T,
	fixture conversationRuntimeFixture,
	ordinal int64,
	status conversationdomain.AnswerPublicationStatus,
	modelRunID foundation.ID,
	assistantText string,
) (conversationdomain.PublishedResult, []byte) {
	t.Helper()
	var (
		resultType conversationdomain.AnswerResultType
		raw        []byte
		summary    conversationdomain.RetrievalSummary
	)
	switch status {
	case conversationdomain.AnswerPublicationCompleted:
		citation := agentdomain.Citation{
			ID: "citation-1", WorkspaceID: fixture.workspaceID, IndexVersionID: fixture.indexID,
			ChunkID:         conversationTurnID(fixture.idBase + int(ordinal)*10 + 20),
			SourceVersionID: conversationTurnID(fixture.idBase + int(ordinal)*10 + 21),
			SourceSpanID:    conversationTurnID(fixture.idBase + int(ordinal)*10 + 22),
		}
		raw = mustJSON(t, agentdomain.RAGAnswerResultV2{
			ResultType: agentdomain.ResultTypeRAGAnswer, SchemaID: agentdomain.RAGAnswerSchemaID,
			SchemaVersion: agentdomain.OutputSchemaVersionV2, ModelRunRef: modelRunID,
			Payload: agentdomain.RAGAnswerPayloadV2{
				RAGAnswerPayload: agentdomain.RAGAnswerPayload{
					Conclusion: assistantText,
					Assertions: []agentdomain.Assertion{{ID: "assertion-1", Text: assistantText, Kind: agentdomain.AssertionFactual, CitationIDs: []string{citation.ID}}},
					Citations:  []agentdomain.Citation{citation}, ConflictPositions: []agentdomain.ConflictPosition{},
				},
				RelatedTopics:     []agentdomain.RelatedTopic{{TopicID: conversationTurnID(fixture.idBase + int(ordinal)*10 + 23), Name: "Topic", CitationIDs: []string{citation.ID}}},
				FollowUpQuestions: []string{"What should I inspect next?"},
			},
		})
		resultType = conversationdomain.AnswerResultRAGAnswer
		indexID := fixture.indexID
		summary, _ = conversationdomain.CanonicalizeRetrievalSummary(fixture.workspaceID, conversationdomain.RetrievalSummary{
			Rewrites: []string{"approved answer"}, RequestedMode: retrievaldomain.SearchModeKeyword, EffectiveMode: retrievaldomain.SearchModeKeyword,
			Scope:          conversationdomain.RetrievalScopeSummary{SourceIDs: []foundation.ID{}, SourceVersionIDs: []foundation.ID{}, PathPrefixes: []string{}},
			IndexVersionID: &indexID, CandidateCount: 1, SelectedCount: 1, Degradations: []conversationdomain.RetrievalDegradation{},
		})
	case conversationdomain.AnswerPublicationRefused:
		raw = mustJSON(t, agentdomain.RefusalResult{
			ResultType: agentdomain.ResultTypeRefusal, SchemaID: agentdomain.RefusalSchemaID,
			SchemaVersion: agentdomain.OutputSchemaVersionV1, ModelRunRef: modelRunID,
			Payload: agentdomain.RefusalPayload{
				ReasonCode: agentdomain.RefusalNoRelevantEvidence, Summary: assistantText, RetrievalScope: "approved knowledge",
				MissingRequirements: []string{"approved evidence"}, SuggestedActions: []string{"add an approved source"},
			},
		})
		resultType = conversationdomain.AnswerResultRefusal
		summary, _ = conversationdomain.CanonicalizeRetrievalSummary(fixture.workspaceID, emptyRetrievalSummary())
	default:
		t.Fatalf("unsupported published fixture status %q", status)
	}
	result, err := conversationdomain.CanonicalizePublishedResult(resultType, raw)
	if err != nil {
		t.Fatal(err)
	}
	summaryJSON, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	return result, summaryJSON
}

func emptyRetrievalSummary() conversationdomain.RetrievalSummary {
	return conversationdomain.RetrievalSummary{
		Rewrites: []string{}, RequestedMode: retrievaldomain.SearchModeKeyword, EffectiveMode: retrievaldomain.SearchModeKeyword,
		Scope:        conversationdomain.RetrievalScopeSummary{SourceIDs: []foundation.ID{}, SourceVersionIDs: []foundation.ID{}, PathPrefixes: []string{}},
		Degradations: []conversationdomain.RetrievalDegradation{},
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

type conversationDBCounter struct {
	pool         *pgxpool.Pool
	queries      int
	queryRows    int
	transactions int
}

func (counter *conversationDBCounter) Begin(ctx context.Context) (pgx.Tx, error) {
	counter.transactions++
	return counter.pool.Begin(ctx)
}

func (counter *conversationDBCounter) Query(ctx context.Context, sql string, arguments ...any) (pgx.Rows, error) {
	counter.queries++
	return counter.pool.Query(ctx, sql, arguments...)
}

func (counter *conversationDBCounter) QueryRow(ctx context.Context, sql string, arguments ...any) pgx.Row {
	counter.queryRows++
	return counter.pool.QueryRow(ctx, sql, arguments...)
}

func (counter *conversationDBCounter) total() int {
	return counter.queries + counter.queryRows + counter.transactions
}

func (counter *conversationDBCounter) reset() {
	counter.queries = 0
	counter.queryRows = 0
	counter.transactions = 0
}

func countingConversationRepository(t *testing.T, pool *pgxpool.Pool) (*Repository, *conversationDBCounter) {
	t.Helper()
	counter := &conversationDBCounter{pool: pool}
	events, err := eventspostgres.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewRepository(counter, events)
	if err != nil {
		t.Fatal(err)
	}
	return repository, counter
}

func conversationTurnID(value int) foundation.ID {
	return foundation.ID(fmt.Sprintf("73000000-0000-4000-8000-%012d", value))
}
