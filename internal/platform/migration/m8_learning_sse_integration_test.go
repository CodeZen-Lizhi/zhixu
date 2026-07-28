//go:build integration

package migration

import (
	"context"
	"strings"
	"testing"
	"time"

	eventspostgres "github.com/CodeZen-Lizhi/zhixu/internal/events/adapter/postgres"
	eventsdomain "github.com/CodeZen-Lizhi/zhixu/internal/events/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestM8LearningSSEMigrationProjectsMinimalEventsAndSupportsDownUp(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 47); err != nil {
		t.Fatalf("migrate through 00047: %v", err)
	}
	assertMigrationVersion(t, ctx, pool, 47)

	workspaceID := "77000000-0000-4000-8000-000000000001"
	deckID := "77000000-0000-4000-8000-000000000002"
	memoryID := "77000000-0000-4000-8000-000000000003"
	ownerID := "77000000-0000-4000-8000-000000000004"
	sessionID := "77000000-0000-4000-8000-000000000005"
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
	) VALUES($1,'m8-learning-sse','/tmp/m8-learning-sse','/tmp/m8-learning-sse',$2,'test',1,$2,$2)`, workspaceID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.review_deck(
		id,workspace_id,name,scope,status,daily_limit,scheduler_version,version,created_at,updated_at
	) VALUES($1,$2,'M8 SSE deck','{}','ACTIVE',20,'fsrs/v1',1,$3,$3)`, deckID, workspaceID, now); err != nil {
		t.Fatal(err)
	}
	startedAt := now.Add(time.Second)
	if _, err := pool.Exec(ctx, `INSERT INTO learning.review_session(
		id,workspace_id,deck_id,session_type,status,config,idempotency_key,request_hash,started_at
	) VALUES($1,$2,$3,'REVIEW','ACTIVE','{}','m8-sse-session',repeat('a',64),$4)`, sessionID, workspaceID, deckID, startedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO learning.memory(
		id,workspace_id,owner_principal_kind,owner_principal_id,memory_type,content,source_type,source_ref,
		status,version,created_at,updated_at
	) VALUES($1,$2,'SESSION',$3,'PREFERENCE','{"private":"must-not-enter-sse"}','USER','user:m8-sse','CANDIDATE',1,$4,$4)`,
		memoryID, workspaceID, ownerID, now); err != nil {
		t.Fatal(err)
	}
	confirmedAt := now.Add(2 * time.Second)
	if _, err := pool.Exec(ctx, `UPDATE learning.memory SET
		status='ACTIVE',confirmed_at=$3,confirmed_by_principal_kind='SESSION',confirmed_by_principal_id=$4,
		version=2,updated_at=$3 WHERE workspace_id=$1 AND id=$2`, workspaceID, memoryID, confirmedAt, ownerID); err != nil {
		t.Fatal(err)
	}
	endedAt := now.Add(3 * time.Second)
	if _, err := pool.Exec(ctx, `UPDATE learning.review_session SET status='COMPLETED',ended_at=$3
		WHERE workspace_id=$1 AND id=$2`, workspaceID, sessionID, endedAt); err != nil {
		t.Fatal(err)
	}

	assertM8LearningEventTriggers(t, ctx, pool)
	store, err := eventspostgres.NewStore(pool)
	if err != nil {
		t.Fatal(err)
	}
	parsedWorkspace, err := foundation.ParseID(workspaceID)
	if err != nil {
		t.Fatal(err)
	}
	events, err := store.ListAfter(ctx, parsedWorkspace, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 5 {
		t.Fatalf("learning event count=%d events=%+v", len(events), events)
	}
	wantTypes := []string{"review.deck.created", "review.session.created", "memory.created", "memory.updated", "review.session.updated"}
	wantRefs := []string{"review_deck:" + deckID, "review_session:" + sessionID, "memory:" + memoryID, "memory:" + memoryID, "review_session:" + sessionID}
	wantVersions := []int64{1, 1, 1, 2, 2}
	for index, event := range events {
		if err := event.Validate(); err != nil {
			t.Fatalf("event %d invalid: %v", index, err)
		}
		if event.Type != wantTypes[index] || event.ResourceRef != wantRefs[index] || event.ResourceVersion != wantVersions[index] {
			t.Fatalf("event %d=%+v", index, event)
		}
		if event.PayloadSummary != (eventsdomain.PayloadSummary{}) || strings.Contains(event.SourceEventRef, "must-not-enter-sse") {
			t.Fatalf("event %d leaked private content: %+v", index, event)
		}
	}
	if !events[1].OccurredAt.Equal(startedAt) || !events[4].OccurredAt.Equal(endedAt) {
		t.Fatalf("review session event times created=%s updated=%s want=%s/%s", events[1].OccurredAt, events[4].OccurredAt, startedAt, endedAt)
	}

	if _, err := provider.DownTo(ctx, 46); err != nil {
		t.Fatalf("00047 down: %v", err)
	}
	assertMigrationVersion(t, ctx, pool, 46)
	pausedAt := confirmedAt.Add(time.Second)
	if _, err := pool.Exec(ctx, `UPDATE learning.memory SET status='PAUSED',version=3,updated_at=$3
		WHERE workspace_id=$1 AND id=$2`, workspaceID, memoryID, pausedAt); err != nil {
		t.Fatal(err)
	}
	if count := m8LearningEventCount(t, ctx, pool, workspaceID); count != 5 {
		t.Fatalf("00047 down still projected events: %d", count)
	}

	if _, err := provider.UpTo(ctx, 47); err != nil {
		t.Fatalf("00047 re-up: %v", err)
	}
	resumedAt := pausedAt.Add(time.Second)
	if _, err := pool.Exec(ctx, `UPDATE learning.memory SET status='ACTIVE',version=4,updated_at=$3
		WHERE workspace_id=$1 AND id=$2`, workspaceID, memoryID, resumedAt); err != nil {
		t.Fatal(err)
	}
	if count := m8LearningEventCount(t, ctx, pool, workspaceID); count != 6 {
		t.Fatalf("00047 re-up event count=%d", count)
	}
}

func assertM8LearningEventTriggers(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger
		WHERE NOT tgisinternal AND tgname = ANY($1::text[])`, []string{
		"review_deck_project_server_event", "review_card_project_server_event", "review_schedule_project_server_event",
		"review_session_project_server_event", "review_answer_project_server_event", "memory_project_server_event",
		"interview_session_project_server_event", "interview_turn_project_server_event", "interview_report_project_server_event",
		"interview_learning_path_project_server_event", "interview_learning_path_step_project_server_event",
	}).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 11 {
		t.Fatalf("learning SSE trigger count=%d", count)
	}
}

func m8LearningEventCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.server_event WHERE workspace_id=$1`, workspaceID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
