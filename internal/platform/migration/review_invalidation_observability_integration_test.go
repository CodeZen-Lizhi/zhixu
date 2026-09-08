//go:build integration

package migration

import (
	"context"
	"testing"
	"time"

	knowledgepostgres "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/adapter/postgres"
	knowledgeapp "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestReviewInvalidationObservabilityMigrationUsesHealthAndTimelineOwners(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if err := provider.UpTo(ctx, 43); err != nil {
		t.Fatalf("prepare migrations through 00043: %v", err)
	}
	seedReviewFSRSBackfillFixture(t, ctx, pool)
	if err := provider.UpTo(ctx, 49); err != nil {
		t.Fatalf("migrate through 00049: %v", err)
	}
	assertMigrationVersion(t, ctx, pool, 49)

	workspaceID := "74000000-0000-4000-8000-000000000001"
	claimID := "74000000-0000-4000-8000-000000000007"
	cardID := "74000000-0000-4000-8000-000000000009"
	var issueID, issueType, targetType, targetID, detectorID, status string
	var issueVersion int64
	var invalidatedAt time.Time
	if err := pool.QueryRow(ctx, `SELECT issue.id::text,issue.type,issue.target_type,issue.target_id::text,
		issue.detector_id,issue.status,issue.version,card.invalidated_at
		FROM ops.health_issue AS issue
		JOIN learning.review_card AS card ON card.workspace_id=issue.workspace_id AND card.claim_id=issue.target_id
		WHERE issue.workspace_id=$1 AND issue.type='REVIEW_INVALIDATED' AND card.id=$2`, workspaceID, cardID).Scan(
		&issueID, &issueType, &targetType, &targetID, &detectorID, &status, &issueVersion, &invalidatedAt,
	); err != nil {
		t.Fatal(err)
	}
	if issueType != "REVIEW_INVALIDATED" || targetType != "CLAIM" || targetID != claimID || detectorID != "health.detector.review_invalidated" || status != "OPEN" || issueVersion != 1 {
		t.Fatalf("backfilled review Health issue=%s/%s target=%s:%s detector=%s status=%s version=%d", issueID, issueType, targetType, targetID, detectorID, status, issueVersion)
	}
	assertReviewHealthTimelineSource(t, ctx, pool, workspaceID, issueID, 1, "HEALTH_ISSUE_DETECTED")

	recoveredAt := invalidatedAt.Add(time.Second)
	if _, err := pool.Exec(ctx, `UPDATE learning.review_card
		SET status='DRAFT',invalidation_reason=NULL,invalidated_at=NULL,version=version+1,updated_at=$3
		WHERE workspace_id=$1 AND id=$2`, workspaceID, cardID, recoveredAt); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status,version FROM ops.health_issue WHERE workspace_id=$1 AND id=$2`, workspaceID, issueID).Scan(&status, &issueVersion); err != nil {
		t.Fatal(err)
	}
	if status != "RESOLVED" || issueVersion != 2 {
		t.Fatalf("recovered review Health issue status=%s version=%d", status, issueVersion)
	}
	assertReviewHealthTimelineSource(t, ctx, pool, workspaceID, issueID, 2, "HEALTH_ISSUE_RESOLVED")

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE learning.review_card SET status='APPROVED',version=version+1,updated_at=$3
		WHERE workspace_id=$1 AND id=$2`, workspaceID, cardID, recoveredAt.Add(time.Second)); err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO learning.review_schedule(
			card_id,workspace_id,due_at,interval_days,stability,difficulty,last_reviewed_at,scheduler_version,paused,version
		) VALUES($1,$2,$3,0,0,0.5,NULL,'fsrs/v1',false,1)`, cardID, workspaceID, recoveredAt.Add(time.Second))
	}
	if err == nil {
		err = tx.Commit(ctx)
	} else {
		_ = tx.Rollback(ctx)
	}
	if err != nil {
		t.Fatalf("reapprove review card: %v", err)
	}
	reinvalidatedAt := recoveredAt.Add(2 * time.Second)
	if _, err := pool.Exec(ctx, `INSERT INTO ingestion.attempt(
		id,workspace_id,source_version_id,status,security_status,failure_stage,error_code,retryable,
		parser_id,parser_version,parser_config_hash,chunk_strategy_version,schema_version,
		idempotency_key,attempt_number,warnings,started_at,completed_at,version
	) VALUES(
		'74000000-0000-4000-8000-000000000012',$1,'74000000-0000-4000-8000-000000000004',
		'validating','quarantined','security','SOURCE_QUARANTINED',false,'text','v1',repeat('3',64),
		'chunk/v1','v1','review-fsrs-reinvalidated-attempt',2,'[]',$2,$2,1
	)`, workspaceID, reinvalidatedAt); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status,version FROM ops.health_issue WHERE workspace_id=$1 AND id=$2`, workspaceID, issueID).Scan(&status, &issueVersion); err != nil {
		t.Fatal(err)
	}
	if status != "REOPENED" || issueVersion != 3 {
		t.Fatalf("reinvalidated review Health issue status=%s version=%d", status, issueVersion)
	}
	assertReviewHealthTimelineSource(t, ctx, pool, workspaceID, issueID, 3, "HEALTH_ISSUE_DETECTED")

	if err := provider.UpTo(ctx, 62); err != nil {
		t.Fatalf("upgrade Timeline projector schema to 00062: %v", err)
	}
	platform := openMigrationRuntimePool(t, ctx, pool)
	defer platform.Close()
	pool = platform.DB()
	repository, err := knowledgepostgres.NewGORMRepository(platform)
	if err != nil {
		t.Fatal(err)
	}
	dispatcher, err := knowledgeapp.NewTimelineProjectionDispatcher(repository)
	if err != nil {
		t.Fatal(err)
	}
	if batch, err := dispatcher.DispatchBatch(ctx, knowledgeapp.MaxTimelineProjectionBatch); err != nil || batch.Projected < 3 {
		t.Fatalf("project review Health Timeline batch=%+v err=%v", batch, err)
	}
	var projectedCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.knowledge_event
		WHERE workspace_id=$1 AND aggregate_type='HEALTH_ISSUE' AND aggregate_id=$2
		  AND event_type IN ('HEALTH_ISSUE_DETECTED','HEALTH_ISSUE_RESOLVED')`, workspaceID, issueID).Scan(&projectedCount); err != nil {
		t.Fatal(err)
	}
	if projectedCount != 3 {
		t.Fatalf("projected review Health Timeline events=%d", projectedCount)
	}
}

func assertReviewHealthTimelineSource(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, issueID string, version int64, eventType string) {
	t.Helper()
	var actualType, aggregateType, aggregateID, status string
	var actualVersion int64
	if err := pool.QueryRow(ctx, `SELECT event_type,aggregate_type,aggregate_id::text,event_version,status
		FROM ops.timeline_projection_outbox
		WHERE workspace_id=$1 AND aggregate_id=$2 AND event_version=$3`, workspaceID, issueID, version).Scan(
		&actualType, &aggregateType, &aggregateID, &actualVersion, &status,
	); err != nil {
		t.Fatal(err)
	}
	if actualType != eventType || aggregateType != "HEALTH_ISSUE" || aggregateID != issueID || actualVersion != version || status != "PENDING" {
		t.Fatalf("Timeline source=%s/%s aggregate=%s version=%d status=%s", actualType, aggregateType, aggregateID, actualVersion, status)
	}
}
