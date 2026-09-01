//go:build integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	agentapplication "github.com/CodeZen-Lizhi/zhixu/internal/agent/application"
	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	eventsapplication "github.com/CodeZen-Lizhi/zhixu/internal/events/application"
	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
)

func TestRAGProgressStorePersistsOrderedRedactedAndReplayableStages(t *testing.T) {
	testRAGProgressStoreIntegrationVariants(t, testRAGProgressStorePersistsOrderedRedactedAndReplayableStages)
}

func testRAGProgressStorePersistsOrderedRedactedAndReplayableStages(
	t *testing.T,
	platform *platformpostgres.Pool,
	ctx context.Context,
	store agentapplication.RAGProgressRecorder,
) {
	pool := platform.DB()
	seedAgentRuntime(t, ctx, pool)
	base, startedAt := seedRAGProgressIntegration(t, platform, ctx)
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
		string(testAgentID(1)), "rag.progress:"+string(base.ModelRunID)+":%").Scan(&count, &eventTypes, &summaries, &elapsedSeconds); err != nil {
		t.Fatal(err)
	}
	if count != 2 || eventTypes != "rag.plan.started,rag.plan.completed" || elapsedSeconds != 2 ||
		containsAny(summaries, "Need evidence", "/tmp/", "question_text", "assistant_text") {
		t.Fatalf("count=%d types=%q summaries=%q", count, eventTypes, summaries)
	}
}

func TestRAGProgressStoreConflictingReplayRollsBackIntegration(t *testing.T) {
	testRAGProgressStoreIntegrationVariants(t, func(
		t *testing.T,
		platform *platformpostgres.Pool,
		ctx context.Context,
		store agentapplication.RAGProgressRecorder,
	) {
		seedAgentRuntime(t, ctx, platform.DB())
		record, _ := seedRAGProgressIntegration(t, platform, ctx)
		record.Update = agentapplication.RAGProgressUpdate{Stage: agentapplication.RAGProgressPlanStarted}
		if err := store.RecordRAGProgress(ctx, record); err != nil {
			t.Fatal(err)
		}
		conflict := record
		conflict.AnswerID = testAgentID(24)
		if err := store.RecordRAGProgress(ctx, conflict); err == nil {
			t.Fatal("conflicting progress replay was accepted")
		}
		assertRAGProgressEventCount(t, platform, ctx, record.ModelRunID, 1)
	})
}

func TestRAGProgressStoreAppenderFailureRollsBackIntegration(t *testing.T) {
	for _, variant := range []struct {
		name string
		open func(*testing.T, *platformpostgres.Pool) agentapplication.RAGProgressRecorder
	}{
		{name: "legacy", open: openLegacyRAGProgressRollbackStoreIntegration},
		{name: "gorm", open: openGORMRAGProgressRollbackStoreIntegration},
	} {
		t.Run(variant.name, func(t *testing.T) {
			platform, ctx := newAgentPlatformIntegrationPool(t)
			seedAgentRuntime(t, ctx, platform.DB())
			record, _ := seedRAGProgressIntegration(t, platform, ctx)
			record.Update = agentapplication.RAGProgressUpdate{Stage: agentapplication.RAGProgressPlanStarted}
			if err := variant.open(t, platform).RecordRAGProgress(ctx, record); err == nil {
				t.Fatal("injected Event append failure was accepted")
			}
			assertRAGProgressEventCount(t, platform, ctx, record.ModelRunID, 0)
		})
	}
}

func TestRAGProgressStoreCommitResponseLossIntegration(t *testing.T) {
	for _, variant := range []struct {
		name string
		open func(*testing.T, *platformpostgres.Pool) (agentapplication.RAGProgressRecorder, func())
	}{
		{name: "legacy", open: openLegacyRAGProgressStoreWithCommitLossIntegration},
		{name: "gorm", open: openGORMRAGProgressStoreWithCommitLossIntegration},
	} {
		t.Run(variant.name, func(t *testing.T) {
			platform, ctx := newAgentPlatformIntegrationPool(t)
			seedAgentRuntime(t, ctx, platform.DB())
			record, _ := seedRAGProgressIntegration(t, platform, ctx)
			record.Update = agentapplication.RAGProgressUpdate{Stage: agentapplication.RAGProgressPlanStarted}
			store, armCommitLoss := variant.open(t, platform)
			armCommitLoss()
			if err := store.RecordRAGProgress(ctx, record); err == nil || agentErrorCode(err) != agentapplication.ErrorCodeRAGProgressUnknown {
				t.Fatalf("commit response loss code=%s err=%v", agentErrorCode(err), err)
			}
			assertRAGProgressEventCount(t, platform, ctx, record.ModelRunID, 1)
			if err := store.RecordRAGProgress(ctx, record); err != nil {
				t.Fatalf("commit-loss exact replay failed: %v", err)
			}
			assertRAGProgressEventCount(t, platform, ctx, record.ModelRunID, 1)
		})
	}
}

func seedRAGProgressIntegration(
	t *testing.T,
	platform *platformpostgres.Pool,
	ctx context.Context,
) (agentapplication.RAGProgressRecord, time.Time) {
	t.Helper()
	conversationID, questionID, answerID, modelRunID := testAgentID(20), testAgentID(21), testAgentID(22), testAgentID(23)
	startedAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	if _, err := platform.DB().Exec(ctx, `INSERT INTO agent.conversation(
		id,workspace_id,status,title,version,last_activity_at,created_at,updated_at,idempotency_key,request_hash)
		VALUES($1,$2,'open','Progress',1,$3,$3,$3,'progress',repeat('a',64))`,
		string(conversationID), string(testAgentID(1)), startedAt); err != nil {
		t.Fatal(err)
	}
	return agentapplication.RAGProgressRecord{
		WorkspaceID: testAgentID(1), WorkflowRunID: testAgentID(4), ConversationID: conversationID,
		QuestionID: questionID, AnswerID: answerID, ModelRunID: modelRunID, OccurredAt: startedAt.Add(time.Second),
	}, startedAt
}

func assertRAGProgressEventCount(
	t *testing.T,
	platform *platformpostgres.Pool,
	ctx context.Context,
	modelRunID foundation.ID,
	want int,
) {
	t.Helper()
	var count int
	if err := platform.DB().QueryRow(ctx, `SELECT count(*) FROM ops.server_event
		WHERE workspace_id=$1 AND source_event_ref LIKE $2`, string(testAgentID(1)), "rag.progress:"+string(modelRunID)+":%").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("RAG progress events=%d, want %d", count, want)
	}
}

type rollbackRAGProgressAppender struct {
	inner eventsapplication.Appender
}

func (appender rollbackRAGProgressAppender) AppendTx(
	ctx context.Context,
	transaction any,
	request eventsdomain.AppendRequest,
) (eventsdomain.ServerEvent, bool, error) {
	event, replayed, err := appender.inner.AppendTx(ctx, transaction, request)
	if err != nil {
		return event, replayed, err
	}
	return event, replayed, errors.New("injected legacy RAG progress Event append failure")
}

type rollbackScopedRAGProgressAppender struct {
	inner eventsapplication.ScopedAppender
}

func (appender rollbackScopedRAGProgressAppender) AppendScoped(
	ctx context.Context,
	scope foundation.TransactionScope,
	request eventsdomain.AppendRequest,
) (eventsdomain.ServerEvent, bool, error) {
	event, replayed, err := appender.inner.AppendScoped(ctx, scope, request)
	if err != nil {
		return event, replayed, err
	}
	return event, replayed, errors.New("injected GORM RAG progress Event append failure")
}

func openLegacyRAGProgressRollbackStoreIntegration(t *testing.T, platform *platformpostgres.Pool) agentapplication.RAGProgressRecorder {
	t.Helper()
	events, err := eventspostgres.NewStore(platform.DB())
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewRAGProgressStore(platform.DB(), rollbackRAGProgressAppender{inner: events})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func openGORMRAGProgressRollbackStoreIntegration(t *testing.T, platform *platformpostgres.Pool) agentapplication.RAGProgressRecorder {
	t.Helper()
	events, err := eventspostgres.NewGORMStore(platform)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewGORMRAGProgressStore(platform, rollbackScopedRAGProgressAppender{inner: events})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func openLegacyRAGProgressStoreWithCommitLossIntegration(
	t *testing.T,
	platform *platformpostgres.Pool,
) (agentapplication.RAGProgressRecorder, func()) {
	t.Helper()
	database := &workspaceAnalysisModelCommitLossDB{DB: platform.DB()}
	events, err := eventspostgres.NewStore(platform.DB())
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewRAGProgressStore(database, events)
	if err != nil {
		t.Fatal(err)
	}
	return store, func() { database.injectNext.Store(true) }
}

func openGORMRAGProgressStoreWithCommitLossIntegration(
	t *testing.T,
	platform *platformpostgres.Pool,
) (agentapplication.RAGProgressRecorder, func()) {
	t.Helper()
	events, err := eventspostgres.NewGORMStore(platform)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewGORMRAGProgressStore(platform, events)
	if err != nil {
		t.Fatal(err)
	}
	loss := &workspaceAnalysisModelPostCommitErrorUnitOfWork{
		inner: store.unitOfWork,
		err:   errors.New("injected GORM RAG progress commit response loss"),
	}
	store.unitOfWork = loss
	return store, func() {
		loss.lost.Store(false)
		loss.armed.Store(true)
	}
}

type ragProgressStoreIntegrationVariant struct {
	name string
	open func(*testing.T, *platformpostgres.Pool) agentapplication.RAGProgressRecorder
}

func ragProgressStoreIntegrationVariants() []ragProgressStoreIntegrationVariant {
	return []ragProgressStoreIntegrationVariant{
		{name: "legacy", open: func(t *testing.T, platform *platformpostgres.Pool) agentapplication.RAGProgressRecorder {
			t.Helper()
			events, err := eventspostgres.NewStore(platform.DB())
			if err != nil {
				t.Fatal(err)
			}
			store, err := NewRAGProgressStore(platform.DB(), events)
			if err != nil {
				t.Fatal(err)
			}
			return store
		}},
		{name: "gorm", open: func(t *testing.T, platform *platformpostgres.Pool) agentapplication.RAGProgressRecorder {
			t.Helper()
			events, err := eventspostgres.NewGORMStore(platform)
			if err != nil {
				t.Fatal(err)
			}
			store, err := NewGORMRAGProgressStore(platform, events)
			if err != nil {
				t.Fatal(err)
			}
			return store
		}},
	}
}

func testRAGProgressStoreIntegrationVariants(
	t *testing.T,
	test func(*testing.T, *platformpostgres.Pool, context.Context, agentapplication.RAGProgressRecorder),
) {
	t.Helper()
	for _, variant := range ragProgressStoreIntegrationVariants() {
		t.Run(variant.name, func(t *testing.T) {
			platform, ctx := newAgentPlatformIntegrationPool(t)
			test(t, platform, ctx, variant.open(t, platform))
		})
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
