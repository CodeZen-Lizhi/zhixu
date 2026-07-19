//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestStoreReadsScopedRetainedEventsInMonotonicBoundedPages(t *testing.T) {
	store, pool, ctx := newEventStoreIntegration(t)
	workspaceA := foundation.ID("7b000000-0000-4000-8000-000000000001")
	workspaceB := foundation.ID("7b000000-0000-4000-8000-000000000002")
	seedEventWorkspaces(t, ctx, pool, workspaceA, workspaceB)

	now := time.Now().UTC().Truncate(time.Microsecond)
	expiredSeq := insertServerEvent(t, ctx, pool, workspaceA, "workflow.run.expired", "7b000000-0000-4000-8000-000000000011", now.Add(-25*time.Hour), `{}`)
	otherSeq := insertServerEvent(t, ctx, pool, workspaceB, "workflow.run.started", "7b000000-0000-4000-8000-000000000012", now.Add(-time.Hour), `{}`)
	firstSeq := insertServerEvent(t, ctx, pool, workspaceA, "workflow.run.started", "7b000000-0000-4000-8000-000000000013", now.Add(-time.Hour), `{"status":"running","candidate_count":0}`)
	secondSeq := insertServerEvent(t, ctx, pool, workspaceA, "workflow.run.progress", "7b000000-0000-4000-8000-000000000014", now.Add(-30*time.Minute), `{"status":"running","candidate_count":2}`)
	if !(expiredSeq < otherSeq && otherSeq < firstSeq && firstSeq < secondSeq) {
		t.Fatalf("fixture sequences are not globally monotonic: %d %d %d %d", expiredSeq, otherSeq, firstSeq, secondSeq)
	}

	watermark, err := store.CurrentWatermark(ctx, workspaceA)
	if err != nil || watermark != secondSeq {
		t.Fatalf("current watermark=%d want=%d err=%v", watermark, secondSeq, err)
	}
	earliest, err := store.EarliestRetained(ctx, workspaceA)
	if err != nil || earliest == nil || *earliest != firstSeq {
		t.Fatalf("earliest retained=%v want=%d err=%v", earliest, firstSeq, err)
	}

	firstPage, err := store.ListAfter(ctx, workspaceA, 0, 1)
	if err != nil || len(firstPage) != 1 || firstPage[0].Seq != firstSeq {
		t.Fatalf("first page=%#v err=%v", firstPage, err)
	}
	secondPage, err := store.ListAfter(ctx, workspaceA, firstPage[0].Seq, domain.MaxReplayPageSize)
	if err != nil || len(secondPage) != 1 || secondPage[0].Seq != secondSeq {
		t.Fatalf("second page=%#v err=%v", secondPage, err)
	}
	if secondPage[0].WorkspaceID != workspaceA || secondPage[0].PayloadSummary.CandidateCount == nil || *secondPage[0].PayloadSummary.CandidateCount != 2 {
		t.Fatalf("second page lost workspace or summary binding: %#v", secondPage[0])
	}
	otherPage, err := store.ListAfter(ctx, workspaceB, 0, domain.MaxReplayPageSize)
	if err != nil || len(otherPage) != 1 || otherPage[0].Seq != otherSeq || otherPage[0].WorkspaceID != workspaceB {
		t.Fatalf("workspace B page=%#v err=%v", otherPage, err)
	}

	if acquired := pool.Stat().AcquiredConns(); acquired != 0 {
		t.Fatalf("short replay queries retained %d database connections", acquired)
	}
}

func TestStoreRejectsUnsafeSummaryWithoutReturningPartialReplay(t *testing.T) {
	store, pool, ctx := newEventStoreIntegration(t)
	workspaceID := foundation.ID("7c000000-0000-4000-8000-000000000001")
	seedEventWorkspaces(t, ctx, pool, workspaceID)
	now := time.Now().UTC().Truncate(time.Microsecond)
	insertServerEvent(t, ctx, pool, workspaceID, "workflow.run.started", "7c000000-0000-4000-8000-000000000011", now.Add(-time.Minute), `{}`)
	insertServerEvent(t, ctx, pool, workspaceID, "answer.completed", "7c000000-0000-4000-8000-000000000012", now, `{"answer_text":"private answer canary"}`)

	events, err := store.ListAfter(ctx, workspaceID, 0, domain.MaxReplayPageSize)
	if len(events) != 0 || eventErrorCode(err) != domain.ErrorCodeEventCorrupt {
		t.Fatalf("unsafe replay events=%d code=%q err=%v", len(events), eventErrorCode(err), err)
	}
	if acquired := pool.Stat().AcquiredConns(); acquired != 0 {
		t.Fatalf("failed replay retained %d database connections", acquired)
	}
}

func TestServerEventSourceReferenceIsUniquePerWorkspace(t *testing.T) {
	_, pool, ctx := newEventStoreIntegration(t)
	workspaceID := foundation.ID("7d000000-0000-4000-8000-000000000001")
	seedEventWorkspaces(t, ctx, pool, workspaceID)
	now := time.Now().UTC().Truncate(time.Microsecond)
	sourceID := "7d000000-0000-4000-8000-000000000011"
	insertServerEvent(t, ctx, pool, workspaceID, "workflow.run.started", sourceID, now, `{}`)

	_, err := pool.Exec(ctx, insertEventSQL, string(workspaceID), "workflow.run.started", "workflow.run:"+sourceID, "workflow.outbox_event:"+sourceID, now, `{}`)
	var postgresError *pgconn.PgError
	if err == nil || !errors.As(err, &postgresError) || postgresError.Code != "23505" {
		t.Fatalf("duplicate source reference error=%v", err)
	}
}

func TestStoreAppendTxCreatesExactReplayRejectsConflictAndLeavesCommitToCaller(t *testing.T) {
	store, pool, ctx := newEventStoreIntegration(t)
	workspaceID := foundation.ID("7d100000-0000-4000-8000-000000000001")
	conversationID := foundation.ID("7d100000-0000-4000-8000-000000000002")
	seedEventWorkspaces(t, ctx, pool, workspaceID)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	insertEventConversation(t, ctx, tx, workspaceID, conversationID, "append-committed")
	request := domain.AppendRequest{
		WorkspaceID: workspaceID, ConversationID: &conversationID,
		Type: "conversation.created", ResourceRef: "conversation:" + string(conversationID), ResourceVersion: 1,
		PayloadSummary: domain.PayloadSummary{Status: "open"}, SchemaVersion: 1,
		SourceEventRef: "conversation.created:" + string(conversationID) + ":v1",
		OccurredAt:     time.Date(2026, 7, 19, 8, 9, 10, 123456789, time.UTC),
	}
	created, replayed, err := store.AppendTx(ctx, tx, request)
	if err != nil || replayed || created.Seq <= 0 || created.ConversationID == nil || *created.ConversationID != conversationID || created.OccurredAt.Nanosecond() != 123456000 {
		t.Fatalf("created event=%#v replayed=%t err=%v", created, replayed, err)
	}
	replayedEvent, replayed, err := store.AppendTx(ctx, tx, request)
	if err != nil || !replayed || replayedEvent.Seq != created.Seq {
		t.Fatalf("replayed event=%#v replayed=%t err=%v", replayedEvent, replayed, err)
	}
	conflict := request
	conflict.ResourceVersion = 2
	if _, _, err := store.AppendTx(ctx, tx, conflict); eventErrorCode(err) != domain.ErrorCodeAppendConflict {
		t.Fatalf("append conflict code=%q err=%v", eventErrorCode(err), err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListAfter(ctx, workspaceID, 0, domain.MaxReplayPageSize)
	if err != nil || len(page) != 1 || page[0].Seq != created.Seq {
		t.Fatalf("committed page=%#v err=%v", page, err)
	}

	rolledBackConversationID := foundation.ID("7d100000-0000-4000-8000-000000000003")
	rollbackTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	insertEventConversation(t, ctx, rollbackTx, workspaceID, rolledBackConversationID, "append-rolled-back")
	rollbackRequest := request
	rollbackRequest.ConversationID = &rolledBackConversationID
	rollbackRequest.ResourceRef = "conversation:" + string(rolledBackConversationID)
	rollbackRequest.SourceEventRef = "conversation.created:" + string(rolledBackConversationID) + ":v1"
	if _, replayed, err := store.AppendTx(ctx, rollbackTx, rollbackRequest); err != nil || replayed {
		_ = rollbackTx.Rollback(ctx)
		t.Fatalf("append before caller rollback replayed=%t err=%v", replayed, err)
	}
	if err := rollbackTx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	page, err = store.ListAfter(ctx, workspaceID, created.Seq, domain.MaxReplayPageSize)
	if err != nil || len(page) != 0 {
		t.Fatalf("caller rollback left events=%#v err=%v", page, err)
	}
	if _, _, err := store.AppendTx(ctx, nil, request); eventErrorCode(err) != domain.ErrorCodeAppendTransactionUnavailable {
		t.Fatalf("nil transaction code=%q err=%v", eventErrorCode(err), err)
	}
}

func TestStoreAppendTxSerializesConcurrentSourceReferenceClaims(t *testing.T) {
	store, pool, ctx := newEventStoreIntegration(t)
	workspaceID := foundation.ID("7d200000-0000-4000-8000-000000000001")
	conversationID := foundation.ID("7d200000-0000-4000-8000-000000000002")
	seedEventWorkspaces(t, ctx, pool, workspaceID)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	insertEventConversation(t, ctx, tx, workspaceID, conversationID, "append-concurrent")
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	request := domain.AppendRequest{
		WorkspaceID: workspaceID, ConversationID: &conversationID,
		Type: "conversation.created", ResourceRef: "conversation:" + string(conversationID), ResourceVersion: 1,
		PayloadSummary: domain.PayloadSummary{Status: "open"}, SchemaVersion: 1,
		SourceEventRef: "conversation.created:" + string(conversationID) + ":concurrent-v1", OccurredAt: now,
	}

	exact := runConcurrentAppends(t, ctx, pool, store, request, request)
	var createdCount, replayedCount int
	var exactSequence int64
	for _, result := range exact {
		if result.err != nil {
			t.Fatalf("concurrent exact append error=%v", result.err)
		}
		if exactSequence != 0 && result.event.Seq != exactSequence {
			t.Fatalf("concurrent exact sequences differ: %d and %d", exactSequence, result.event.Seq)
		}
		exactSequence = result.event.Seq
		if result.replayed {
			replayedCount++
		} else {
			createdCount++
		}
	}
	if createdCount != 1 || replayedCount != 1 {
		t.Fatalf("concurrent exact created=%d replayed=%d", createdCount, replayedCount)
	}

	first := request
	first.Type = "conversation.updated"
	first.SourceEventRef = "conversation.updated:" + string(conversationID) + ":concurrent-v2"
	first.OccurredAt = now.Add(time.Second)
	second := first
	second.ResourceVersion = 2
	conflicting := runConcurrentAppends(t, ctx, pool, store, first, second)
	var successCount, conflictCount int
	for _, result := range conflicting {
		switch eventErrorCode(result.err) {
		case "":
			successCount++
		case domain.ErrorCodeAppendConflict:
			conflictCount++
		default:
			t.Fatalf("concurrent conflict unexpected code=%q err=%v", eventErrorCode(result.err), result.err)
		}
	}
	if successCount != 1 || conflictCount != 1 {
		t.Fatalf("concurrent conflict success=%d conflict=%d", successCount, conflictCount)
	}
}

type concurrentAppendResult struct {
	event    domain.ServerEvent
	replayed bool
	err      error
}

func runConcurrentAppends(t *testing.T, parent context.Context, pool *pgxpool.Pool, store *Store, requests ...domain.AppendRequest) []concurrentAppendResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()
	start := make(chan struct{})
	results := make(chan concurrentAppendResult, len(requests))
	transactions := make([]pgx.Tx, len(requests))
	for index := range requests {
		tx, err := pool.Begin(ctx)
		if err != nil {
			for _, opened := range transactions[:index] {
				_ = opened.Rollback(ctx)
			}
			t.Fatal(err)
		}
		transactions[index] = tx
	}
	for index, request := range requests {
		request := request
		tx := transactions[index]
		go func() {
			<-start
			event, replayed, err := store.AppendTx(ctx, tx, request)
			if err != nil {
				_ = tx.Rollback(ctx)
				results <- concurrentAppendResult{err: err}
				return
			}
			if err := tx.Commit(ctx); err != nil {
				results <- concurrentAppendResult{err: err}
				return
			}
			results <- concurrentAppendResult{event: event, replayed: replayed}
		}()
	}
	close(start)
	collected := make([]concurrentAppendResult, 0, len(requests))
	for range requests {
		select {
		case result := <-results:
			collected = append(collected, result)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	return collected
}

const insertEventSQL = `
	INSERT INTO ops.server_event(
		workspace_id,event_type,resource_ref,resource_version,payload_summary,schema_version,
		source_event_ref,occurred_at,expires_at
	) VALUES($1,$2,$3,1,$6::jsonb,1,$4,$5::timestamptz,$5::timestamptz + INTERVAL '24 hours')
	RETURNING seq`

func insertServerEvent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID foundation.ID, eventType, sourceID string, occurredAt time.Time, summary string) int64 {
	t.Helper()
	var sequence int64
	if err := pool.QueryRow(ctx, insertEventSQL, string(workspaceID), eventType, "workflow.run:"+sourceID, "workflow.outbox_event:"+sourceID, occurredAt, summary).Scan(&sequence); err != nil {
		t.Fatal(err)
	}
	return sequence
}

func seedEventWorkspaces(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceIDs ...foundation.ID) {
	t.Helper()
	for index, workspaceID := range workspaceIDs {
		_, err := pool.Exec(ctx, `INSERT INTO core.workspace(
			id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
		) VALUES($1,$2,$3,$3,now(),'test',1,now(),now())`, string(workspaceID), fmt.Sprintf("events-%d", index), "/tmp/events-"+string(workspaceID))
		if err != nil {
			t.Fatal(err)
		}
	}
}

func insertEventConversation(t *testing.T, ctx context.Context, tx pgx.Tx, workspaceID, conversationID foundation.ID, idempotencyKey string) {
	t.Helper()
	_, err := tx.Exec(ctx, `INSERT INTO agent.conversation(
		id,workspace_id,status,title,version,last_activity_at,created_at,updated_at,archived_at,idempotency_key,request_hash
	) VALUES($1,$2,'open',NULL,1,now(),now(),now(),NULL,$3,repeat('a',64))`, string(conversationID), string(workspaceID), idempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
}

func newEventStoreIntegration(t *testing.T) (*Store, *pgxpool.Pool, context.Context) {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL for Server Event integration tests")
	}
	ctx := context.Background()
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("zhixu_events_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	databaseURL := parsed.String()
	migrationPool, err := platformpostgres.OpenMigration(ctx, databaseURL, 2, 0)
	if err == nil {
		var runner *platformmigration.Runner
		runner, err = platformmigration.NewRunner(migrationPool.DB(), projectmigrations.FS)
		if err == nil {
			err = runner.Up(ctx)
		}
		migrationPool.Close()
	}
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	config.MaxConns = 2
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
	})
	store, err := NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	return store, pool, ctx
}

func eventErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}
