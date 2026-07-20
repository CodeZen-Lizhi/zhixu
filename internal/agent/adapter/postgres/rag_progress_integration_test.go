//go:build integration

package postgres

import (
	"strings"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
)

func TestRAGProgressStorePersistsOrderedRedactedAndReplayableStages(t *testing.T) {
	pool, ctx := newAgentRepositoryIntegrationPool(t)
	seedAgentRuntime(t, ctx, pool)
	conversationID, questionID, answerID, modelRunID := testAgentID(20), testAgentID(21), testAgentID(22), testAgentID(23)
	startedAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `INSERT INTO agent.conversation(
		id,workspace_id,status,title,version,last_activity_at,created_at,updated_at,idempotency_key,request_hash)
		VALUES($1,$2,'open','Progress',1,$3,$3,$3,'progress',repeat('a',64))`,
		string(conversationID), string(testAgentID(1)), startedAt); err != nil {
		t.Fatal(err)
	}
	events, err := eventspostgres.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewRAGProgressStore(pool, events)
	if err != nil {
		t.Fatal(err)
	}
	base := agentapplication.RAGProgressRecord{
		WorkspaceID: testAgentID(1), WorkflowRunID: testAgentID(4), ConversationID: conversationID,
		QuestionID: questionID, AnswerID: answerID, ModelRunID: modelRunID, OccurredAt: startedAt.Add(time.Second),
	}
	base.Update = agentapplication.RAGProgressUpdate{Stage: agentapplication.RAGProgressPlanStarted}
	if err := store.RecordRAGProgress(ctx, base); err != nil {
		t.Fatal(err)
	}
	base.Update = agentapplication.RAGProgressUpdate{Stage: agentapplication.RAGProgressPlanCompleted, RewriteCount: 2}
	base.OccurredAt = startedAt.Add(3 * time.Second)
	if err := store.RecordRAGProgress(ctx, base); err != nil {
		t.Fatal(err)
	}
	base.OccurredAt = startedAt.Add(30 * time.Second)
	if err := store.RecordRAGProgress(ctx, base); err != nil {
		t.Fatalf("exact replay failed: %v", err)
	}
	var count int
	var eventTypes, summaries string
	var elapsedSeconds float64
	if err := pool.QueryRow(ctx, `SELECT count(*),string_agg(event_type,',' ORDER BY seq),string_agg(payload_summary::text,' ' ORDER BY seq),
		EXTRACT(EPOCH FROM max(occurred_at)-min(occurred_at))
		FROM ops.server_event WHERE workspace_id=$1 AND source_event_ref LIKE $2`,
		string(testAgentID(1)), "rag.progress:"+string(modelRunID)+":%").Scan(&count, &eventTypes, &summaries, &elapsedSeconds); err != nil {
		t.Fatal(err)
	}
	if count != 2 || eventTypes != "rag.plan.started,rag.plan.completed" || elapsedSeconds != 2 ||
		containsAny(summaries, "Need evidence", "/tmp/", "question_text", "assistant_text") {
		t.Fatalf("count=%d types=%q summaries=%q", count, eventTypes, summaries)
	}
}

func containsAny(value string, candidates ...string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
}
