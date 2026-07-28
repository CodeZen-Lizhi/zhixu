//go:build integration

package migration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	knowledgepostgres "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/adapter/postgres"
	knowledgeapp "github.com/CodeZen-Lizhi/zhixu/internal/knowledge/application"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestTimelineImpactMigrationReplayAppendOnlyAndGuardedDown(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 37); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 37); err != nil {
		t.Fatalf("repeated migration UpTo(37) failed: %v", err)
	}
	var version int64
	if err := pool.QueryRow(ctx, `SELECT max(version_id) FROM public.goose_db_version WHERE is_applied`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 37 {
		t.Fatalf("migration version=%d want 37", version)
	}

	workspaceID := "37000000-0000-4000-8000-000000000001"
	eventID := "37000000-0000-4000-8000-000000000002"
	reportID := "37000000-0000-4000-8000-000000000003"
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at)
VALUES($1,'timeline-impact','/tmp/timeline-impact','/tmp/timeline-impact',now(),'active',now(),now())`, workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ops.knowledge_event(
id,workspace_id,event_type,aggregate_type,aggregate_id,source_event_ref,source_ref,event_version,schema_version,summary,payload,correlation,occurred_at,created_at)
VALUES($1,$2,'CONFLICT_RESOLVED','CONFLICT',$3,'conflict:resolved:1','conflict:1',1,'knowledge-event/v1','conflict resolved','{}','{}',now(),now())`, eventID, workspaceID, eventID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ops.knowledge_event(
id,workspace_id,event_type,aggregate_type,source_event_ref,source_ref,event_version,schema_version,summary,payload,correlation,occurred_at,created_at)
VALUES('37000000-0000-4000-8000-000000000004',$1,'CORRECTIVE_EVENT','CONFLICT','conflict:resolved:1','conflict:1',1,'knowledge-event/v1','','{}','{}',now(),now())`, workspaceID); !isPostgresCode(err, "23505") {
		t.Fatalf("duplicate source event error=%v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.knowledge_event SET summary='mutated' WHERE id=$1`, eventID); !isPostgresCode(err, "55000") {
		t.Fatalf("append-only update error=%v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ops.impact_report(
id,workspace_id,source_event_id,status,objects,summary,generated_at,schema_version,source_event_version,fingerprint,version,created_at)
VALUES($1,$2,$3,'READY','[]','{}',now(),'impact-report/v1',1,repeat('a',64),1,now())`, reportID, workspaceID, eventID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.impact_report SET summary='{"mutated":1}' WHERE id=$1`, reportID); !isPostgresCode(err, "55000") {
		t.Fatalf("Impact Report append-only update error=%v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM ops.impact_report WHERE id=$1`, reportID); !isPostgresCode(err, "55000") {
		t.Fatalf("Impact Report append-only delete error=%v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.timeline_projection_outbox SET summary='mutated' WHERE workspace_id=$1`, workspaceID); !isPostgresCode(err, "55000") {
		t.Fatalf("Timeline outbox binding mutation error=%v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM ops.timeline_projection_outbox WHERE workspace_id=$1`, workspaceID); !isPostgresCode(err, "55000") {
		t.Fatalf("Timeline outbox append-only delete error=%v", err)
	}

	if _, err := migrationProvider(t, pool).DownTo(ctx, 36); !isPostgresCode(err, "55000") {
		t.Fatalf("guarded 00037 Down error=%v", err)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER impact_report_append_only ON ops.impact_report`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM ops.impact_report WHERE id=$1`, reportID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER timeline_projection_outbox_guard ON ops.timeline_projection_outbox`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM ops.timeline_projection_outbox WHERE workspace_id=$1`, workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER knowledge_event_append_only ON ops.knowledge_event`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM ops.knowledge_event WHERE id=$1`, eventID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `CREATE TRIGGER knowledge_event_append_only BEFORE UPDATE OR DELETE ON ops.knowledge_event FOR EACH ROW EXECUTE FUNCTION ops.reject_append_only_mutation()`); err != nil {
		t.Fatal(err)
	}
	if _, err := migrationProvider(t, pool).DownTo(ctx, 36); err != nil {
		t.Fatalf("empty 00037 Down failed: %v", err)
	}
	var healthTriggerCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger
WHERE tgrelid='ops.health_issue'::regclass
  AND tgname='timeline_project_health_issue_source'
  AND NOT tgisinternal`).Scan(&healthTriggerCount); err != nil {
		t.Fatal(err)
	}
	if healthTriggerCount != 0 {
		t.Fatalf("health Timeline trigger survived 00037 Down: %d", healthTriggerCount)
	}
	if _, err := migrationProvider(t, pool).UpTo(ctx, 37); err != nil {
		t.Fatalf("00037 Up after Down failed: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger
WHERE tgrelid='ops.health_issue'::regclass
  AND tgname='timeline_project_health_issue_source'
  AND NOT tgisinternal`).Scan(&healthTriggerCount); err != nil {
		t.Fatal(err)
	}
	if healthTriggerCount != 1 {
		t.Fatalf("health Timeline trigger count after 00037 Up=%d want 1", healthTriggerCount)
	}
	for table, trigger := range map[string]string{
		"ops.impact_report":              "impact_report_append_only",
		"ops.timeline_projection_outbox": "timeline_projection_outbox_guard",
	} {
		var triggerCount int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger
WHERE tgrelid=$1::regclass AND tgname=$2 AND NOT tgisinternal`, table, trigger).Scan(&triggerCount); err != nil {
			t.Fatal(err)
		}
		if triggerCount != 1 {
			t.Fatalf("trigger %s on %s count=%d want 1", trigger, table, triggerCount)
		}
	}
	var conflictTopicIndexCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes
WHERE schemaname='core' AND indexname='idx_knowledge_conflict_workspace_topic'`).Scan(&conflictTopicIndexCount); err != nil {
		t.Fatal(err)
	}
	if conflictTopicIndexCount != 1 {
		t.Fatalf("Conflict topic index count=%d want 1", conflictTopicIndexCount)
	}
}

func TestTimelineImpactMigrationBackfillsAndProjectsHealthLifecycle(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 36); err != nil {
		t.Fatal(err)
	}

	const (
		workspaceID      = "37100000-0000-4000-8000-000000000001"
		topicID          = "37100000-0000-4000-8000-000000000002"
		backfillIssueID  = "37100000-0000-4000-8000-000000000003"
		lifecycleIssueID = "37100000-0000-4000-8000-000000000004"
		replayedEventID  = "37100000-0000-4000-8000-000000000005"
	)
	baseTime := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	insertTimelineHealthWorkspaceAndTopic(t, ctx, pool, workspaceID, topicID, baseTime)
	insertTimelineHealthIssue(t, ctx, pool, workspaceID, topicID, backfillIssueID, "a", "b", baseTime.Add(10*time.Minute))
	backfillResolvedAt := baseTime.Add(20 * time.Minute)
	if _, err := pool.Exec(ctx, `UPDATE ops.health_issue
SET status='RESOLVED',version=2,resolved_at=$3,last_verified_at=$3,updated_at=$3
WHERE workspace_id=$1 AND id=$2`, workspaceID, backfillIssueID, backfillResolvedAt); err != nil {
		t.Fatal(err)
	}

	if _, err := provider.UpTo(ctx, 37); err != nil {
		t.Fatal(err)
	}
	backfillDetectedRef := "health-issue.detected:" + backfillIssueID + ":v1"
	backfillResolvedRef := "health-issue.resolved:" + backfillIssueID + ":v2"
	assertHealthTimelineOutboxSource(t, ctx, pool, workspaceID, backfillIssueID, backfillDetectedRef, "HEALTH_ISSUE_DETECTED", 1, baseTime.Add(10*time.Minute))
	assertHealthTimelineOutboxSource(t, ctx, pool, workspaceID, backfillIssueID, backfillResolvedRef, "HEALTH_ISSUE_RESOLVED", 2, backfillResolvedAt)

	if _, err := pool.Exec(ctx, `SELECT ops.enqueue_timeline_projection(
$1::uuid,'HEALTH_ISSUE_DETECTED','HEALTH_ISSUE',$2::uuid,$3,'health_issue:' || $2::uuid::text,
1::bigint,'Health issue detected','{}'::jsonb,$4::timestamptz)`, workspaceID, backfillIssueID, backfillDetectedRef, baseTime.Add(10*time.Minute)); err != nil {
		t.Fatalf("exact Health backfill replay failed: %v", err)
	}
	if _, err := pool.Exec(ctx, `SELECT ops.enqueue_timeline_projection(
$1::uuid,'HEALTH_ISSUE_DETECTED','HEALTH_ISSUE',$2::uuid,$3,'health_issue:' || $2::uuid::text,
1::bigint,'drifted summary','{}'::jsonb,$4::timestamptz)`, workspaceID, backfillIssueID, backfillDetectedRef, baseTime.Add(10*time.Minute)); !isPostgresCode(err, "23514") {
		t.Fatalf("Health source binding drift error=%v", err)
	}
	var backfillSources int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.timeline_projection_outbox
WHERE workspace_id=$1 AND aggregate_type='HEALTH_ISSUE' AND aggregate_id=$2`, workspaceID, backfillIssueID).Scan(&backfillSources); err != nil {
		t.Fatal(err)
	}
	if backfillSources != 2 {
		t.Fatalf("Health backfill source count=%d want 2", backfillSources)
	}
	if _, err := provider.UpTo(ctx, 62); err != nil {
		t.Fatalf("upgrade Timeline projector schema to 00062: %v", err)
	}

	repository, err := knowledgepostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	projectTimelineSource(t, ctx, repository, knowledgeapp.TimelineProjectionProjected)
	projectTimelineSource(t, ctx, repository, knowledgeapp.TimelineProjectionProjected)
	assertHealthTimelineEvent(t, ctx, pool, workspaceID, backfillIssueID, backfillDetectedRef, "HEALTH_ISSUE_DETECTED", 1, baseTime.Add(10*time.Minute))
	assertHealthTimelineEvent(t, ctx, pool, workspaceID, backfillIssueID, backfillResolvedRef, "HEALTH_ISSUE_RESOLVED", 2, backfillResolvedAt)

	detectedAt := baseTime.Add(30 * time.Minute)
	insertTimelineHealthIssue(t, ctx, pool, workspaceID, topicID, lifecycleIssueID, "c", "d", detectedAt)
	detectedRef := "health-issue.detected:" + lifecycleIssueID + ":v1"
	assertHealthTimelineOutboxSource(t, ctx, pool, workspaceID, lifecycleIssueID, detectedRef, "HEALTH_ISSUE_DETECTED", 1, detectedAt)
	projectTimelineSource(t, ctx, repository, knowledgeapp.TimelineProjectionProjected)
	assertHealthTimelineEvent(t, ctx, pool, workspaceID, lifecycleIssueID, detectedRef, "HEALTH_ISSUE_DETECTED", 1, detectedAt)

	verifiedAt := detectedAt.Add(time.Minute)
	if _, err := pool.Exec(ctx, `UPDATE ops.health_issue
SET last_verified_at=$3
WHERE workspace_id=$1 AND id=$2`, workspaceID, lifecycleIssueID, verifiedAt); err != nil {
		t.Fatal(err)
	}
	assertHealthTimelineSourceCount(t, ctx, pool, workspaceID, lifecycleIssueID, 1)

	resolvedAt := detectedAt.Add(2 * time.Minute)
	resolvedRef := "health-issue.resolved:" + lifecycleIssueID + ":v2"
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE ops.health_issue
SET status='RESOLVED',version=2,resolved_at=$3,last_verified_at=$3,updated_at=$3
WHERE workspace_id=$1 AND id=$2`, workspaceID, lifecycleIssueID, resolvedAt); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	var inTransactionSources int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM ops.timeline_projection_outbox
WHERE workspace_id=$1 AND source_event_ref=$2`, workspaceID, resolvedRef).Scan(&inTransactionSources); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if inTransactionSources != 1 {
		_ = tx.Rollback(ctx)
		t.Fatalf("resolved source count inside domain transaction=%d want 1", inTransactionSources)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	var version int64
	if err := pool.QueryRow(ctx, `SELECT status,version FROM ops.health_issue
WHERE workspace_id=$1 AND id=$2`, workspaceID, lifecycleIssueID).Scan(&status, &version); err != nil {
		t.Fatal(err)
	}
	if status != "OPEN" || version != 1 {
		t.Fatalf("rolled back Health issue status=%s version=%d", status, version)
	}
	assertHealthTimelineSourceCount(t, ctx, pool, workspaceID, lifecycleIssueID, 1)

	if _, err := pool.Exec(ctx, `UPDATE ops.health_issue
SET status='RESOLVED',version=2,resolved_at=$3,last_verified_at=$3,updated_at=$3
WHERE workspace_id=$1 AND id=$2`, workspaceID, lifecycleIssueID, resolvedAt); err != nil {
		t.Fatal(err)
	}
	assertHealthTimelineOutboxSource(t, ctx, pool, workspaceID, lifecycleIssueID, resolvedRef, "HEALTH_ISSUE_RESOLVED", 2, resolvedAt)
	projectTimelineSource(t, ctx, repository, knowledgeapp.TimelineProjectionProjected)
	assertHealthTimelineEvent(t, ctx, pool, workspaceID, lifecycleIssueID, resolvedRef, "HEALTH_ISSUE_RESOLVED", 2, resolvedAt)

	if _, err := pool.Exec(ctx, `UPDATE ops.timeline_projection_outbox
SET status='PENDING',projected_at=NULL,updated_at=GREATEST(CURRENT_TIMESTAMP,created_at),version=version+1
WHERE workspace_id=$1 AND source_event_ref=$2`, workspaceID, resolvedRef); !isPostgresCode(err, "55000") {
		t.Fatalf("terminal Timeline outbox replay error=%v", err)
	}
	var resolvedEvents int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.knowledge_event
WHERE workspace_id=$1 AND source_event_ref=$2`, workspaceID, resolvedRef).Scan(&resolvedEvents); err != nil {
		t.Fatal(err)
	}
	if resolvedEvents != 1 {
		t.Fatalf("resolved Health replay event count=%d want 1", resolvedEvents)
	}

	reopenedAt := detectedAt.Add(3 * time.Minute)
	reopenedRef := "health-issue.detected:" + lifecycleIssueID + ":v3"
	insertPreexistingHealthTimelineEvent(t, ctx, pool, replayedEventID, workspaceID, lifecycleIssueID, reopenedRef, 3, reopenedAt)
	if _, err := pool.Exec(ctx, `UPDATE ops.health_issue
SET status='REOPENED',version=3,resolved_at=NULL,last_detected_at=$3,last_verified_at=$3,updated_at=$3
WHERE workspace_id=$1 AND id=$2`, workspaceID, lifecycleIssueID, reopenedAt); err != nil {
		t.Fatal(err)
	}
	assertHealthTimelineOutboxSource(t, ctx, pool, workspaceID, lifecycleIssueID, reopenedRef, "HEALTH_ISSUE_DETECTED", 3, reopenedAt)
	if _, err := pool.Exec(ctx, `UPDATE ops.timeline_projection_outbox SET summary='drifted'
WHERE workspace_id=$1 AND source_event_ref=$2`, workspaceID, reopenedRef); !isPostgresCode(err, "55000") {
		t.Fatalf("pending Timeline outbox source mutation error=%v", err)
	}
	projectTimelineSource(t, ctx, repository, knowledgeapp.TimelineProjectionReplayed)
	assertHealthTimelineEvent(t, ctx, pool, workspaceID, lifecycleIssueID, reopenedRef, "HEALTH_ISSUE_DETECTED", 3, reopenedAt)
	if _, err := pool.Exec(ctx, `DELETE FROM ops.knowledge_event
WHERE workspace_id=$1 AND source_event_ref=$2`, workspaceID, reopenedRef); !isPostgresCode(err, "55000") {
		t.Fatalf("Health Timeline append-only delete error=%v", err)
	}
}

func insertPreexistingHealthTimelineEvent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, eventID, workspaceID, issueID, sourceEventRef string, version int, occurredAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO ops.knowledge_event(
id,workspace_id,event_type,aggregate_type,aggregate_id,source_event_ref,source_ref,event_version,
schema_version,summary,payload,correlation,occurred_at,created_at)
VALUES($1,$2,'HEALTH_ISSUE_DETECTED','HEALTH_ISSUE',$3,$4,'health_issue:' || $3::uuid::text,$5,
'knowledge-event/v1','Health issue detected','{}','{}',$6,$6)`, eventID, workspaceID, issueID, sourceEventRef, version, occurredAt); err != nil {
		t.Fatal(err)
	}
}

func TestTimelineImpactMigrationBackfillsAndProjectsSourceDirectory(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 36); err != nil {
		t.Fatal(err)
	}

	const (
		workspaceID = "37200000-0000-4000-8000-000000000001"
		proposalID  = "37200000-0000-4000-8000-000000000002"
		revisionID  = "37200000-0000-4000-8000-000000000003"
		approvalID  = "37200000-0000-4000-8000-000000000004"
		commitID    = "37200000-0000-4000-8000-000000000005"
	)
	createdAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	insertTimelineSourceDirectoryFixtures(t, ctx, pool, workspaceID, proposalID, revisionID, approvalID, commitID, createdAt)
	if _, err := provider.UpTo(ctx, 37); err != nil {
		t.Fatal(err)
	}

	assertTimelineSourceBinding(t, ctx, pool, workspaceID, "proposal.created:"+proposalID+":v1", "PROPOSAL_CREATED", "PROPOSAL", proposalID, "proposal:"+proposalID, 1, map[string]string{"proposal_id": proposalID})
	assertTimelineSourceBinding(t, ctx, pool, workspaceID, "approval:"+approvalID+":v1", "APPROVAL_GRANTED", "APPROVAL", approvalID, "approval:"+approvalID, 1, map[string]string{"proposal_id": proposalID, "approval_id": approvalID})
	assertTimelineSourceBinding(t, ctx, pool, workspaceID, "proposal-commit:"+commitID+":v1", "GIT_COMMITTED", "GIT_COMMIT", commitID, "git_commit:"+strings.Repeat("c", 40), 1, map[string]string{"proposal_id": proposalID, "approval_id": approvalID, "git_commit_ref": strings.Repeat("c", 40)})
	assertTimelineSourceBinding(t, ctx, pool, workspaceID, "proposal-revision-published:"+commitID+":v1", "VERSION_PUBLISHED", "ARTICLE_REVISION", revisionID, "article_revision:"+revisionID, 1, map[string]string{"proposal_id": proposalID, "approval_id": approvalID, "git_commit_ref": strings.Repeat("c", 40)})
	if _, err := provider.UpTo(ctx, 62); err != nil {
		t.Fatalf("upgrade Timeline projector schema to 00062: %v", err)
	}

	repository, err := knowledgepostgres.NewRepository(pool)
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 4; index++ {
		projectTimelineSource(t, ctx, repository, knowledgeapp.TimelineProjectionProjected)
	}
	assertTimelineEventBinding(t, ctx, pool, workspaceID, "proposal.created:"+proposalID+":v1", "PROPOSAL_CREATED", "PROPOSAL", proposalID, "proposal:"+proposalID, 1, map[string]string{"proposal_id": proposalID})
	assertTimelineEventBinding(t, ctx, pool, workspaceID, "approval:"+approvalID+":v1", "APPROVAL_GRANTED", "APPROVAL", approvalID, "approval:"+approvalID, 1, map[string]string{"proposal_id": proposalID, "approval_id": approvalID})
	assertTimelineEventBinding(t, ctx, pool, workspaceID, "proposal-commit:"+commitID+":v1", "GIT_COMMITTED", "GIT_COMMIT", commitID, "git_commit:"+strings.Repeat("c", 40), 1, map[string]string{"proposal_id": proposalID, "approval_id": approvalID, "git_commit_ref": strings.Repeat("c", 40)})
	assertTimelineEventBinding(t, ctx, pool, workspaceID, "proposal-revision-published:"+commitID+":v1", "VERSION_PUBLISHED", "ARTICLE_REVISION", revisionID, "article_revision:"+revisionID, 1, map[string]string{"proposal_id": proposalID, "approval_id": approvalID, "git_commit_ref": strings.Repeat("c", 40)})
}

func insertTimelineSourceDirectoryFixtures(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, proposalID, revisionID, approvalID, commitID string, createdAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at)
VALUES($1,'timeline-source-directory','/tmp/timeline-source-directory','/tmp/timeline-source-directory',$2,'active',$2,$2)`, workspaceID, createdAt); err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO change_control.proposal(
id,workspace_id,status,risk_level,idempotency_key,request_hash,version,created_at,updated_at)
VALUES($1,$2,'approved','LOW','timeline-source-directory-proposal',repeat('a',64),1,$3,$3)`, proposalID, workspaceID, createdAt); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO change_control.proposal_revision(
id,proposal_id,revision_no,target_path,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,created_at)
VALUES($1,$2,1,'timeline.md',repeat('b',64),'timeline source directory','evidence','LOW','rollback',repeat('d',64),$3)`, revisionID, proposalID, createdAt); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO change_control.approval(
id,proposal_id,revision_id,change_hash,decision,approved_git_head,decided_at)
VALUES($1,$2,$3,repeat('d',64),'approved',repeat('e',40),$4)`, approvalID, proposalID, revisionID, createdAt); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO change_control.proposal_commit(
id,workspace_id,writeback_execution_id,proposal_id,revision_id,approval_id,git_commit,parent_git_commit,target_path,diff_hash,result_hash,created_at)
VALUES($1,$2,'37200000-0000-4000-8000-000000000099',$3,$4,$5,repeat('c',40),repeat('e',40),'timeline.md',repeat('f',64),repeat('0',64),$6)`, commitID, workspaceID, proposalID, revisionID, approvalID, createdAt); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func assertTimelineSourceBinding(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, sourceEventRef, eventType, aggregateType, aggregateID, sourceRef string, version int, correlation map[string]string) {
	t.Helper()
	assertTimelineBinding(t, ctx, pool, "ops.timeline_projection_outbox", workspaceID, sourceEventRef, eventType, aggregateType, aggregateID, sourceRef, version, correlation)
}

func assertTimelineEventBinding(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, sourceEventRef, eventType, aggregateType, aggregateID, sourceRef string, version int, correlation map[string]string) {
	t.Helper()
	assertTimelineBinding(t, ctx, pool, "ops.knowledge_event", workspaceID, sourceEventRef, eventType, aggregateType, aggregateID, sourceRef, version, correlation)
}

func assertTimelineBinding(t *testing.T, ctx context.Context, pool *pgxpool.Pool, table, workspaceID, sourceEventRef, eventType, aggregateType, aggregateID, sourceRef string, version int, correlation map[string]string) {
	t.Helper()
	var actualEventType, actualAggregateType, actualAggregateID, actualSourceRef string
	var actualVersion int
	if err := pool.QueryRow(ctx, `SELECT event_type,aggregate_type,aggregate_id::text,source_ref,event_version
FROM `+table+` WHERE workspace_id=$1 AND source_event_ref=$2`, workspaceID, sourceEventRef).Scan(
		&actualEventType, &actualAggregateType, &actualAggregateID, &actualSourceRef, &actualVersion,
	); err != nil {
		t.Fatal(err)
	}
	if actualEventType != eventType || actualAggregateType != aggregateType || actualAggregateID != aggregateID || actualSourceRef != sourceRef || actualVersion != version {
		t.Fatalf("Timeline binding table=%s source=%s event=%s aggregate=%s/%s source_ref=%s version=%d", table, sourceEventRef, actualEventType, actualAggregateType, actualAggregateID, actualSourceRef, actualVersion)
	}
	for key, value := range correlation {
		var actual *string
		if err := pool.QueryRow(ctx, `SELECT correlation->>$3 FROM `+table+` WHERE workspace_id=$1 AND source_event_ref=$2`, workspaceID, sourceEventRef, key).Scan(&actual); err != nil {
			t.Fatal(err)
		}
		if actual == nil || *actual != value {
			t.Fatalf("Timeline correlation table=%s source=%s key=%s actual=%v want=%s", table, sourceEventRef, key, actual, value)
		}
	}
}

func insertTimelineHealthWorkspaceAndTopic(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, topicID string, createdAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at)
VALUES($1,'timeline-health','/tmp/timeline-health','/tmp/timeline-health',$2,'active',$2,$2)`, workspaceID, createdAt); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO core.topic(
id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at)
VALUES($1,$2,'Timeline Health','timeline health','','ACTIVE',1,$3,$3)`, topicID, workspaceID, createdAt); err != nil {
		t.Fatal(err)
	}
}

func insertTimelineHealthIssue(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, topicID, issueID, identityRune, fingerprintRune string, detectedAt time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO ops.health_issue(
id,workspace_id,type,target_type,target_id,detector_id,identity_hash,fingerprint_schema_version,
fingerprint,detector_version,severity,evidence_summary,status,version,
first_detected_at,last_detected_at,last_verified_at,created_at,updated_at)
VALUES($1,$2,'ORPHAN','TOPIC',$3,'health.detector.timeline',$4,'health-fingerprint/v1',
$5,'detector/v1','MEDIUM','timeline health fixture','OPEN',1,$6,$6,$6,$6,$6)`,
		issueID, workspaceID, topicID, strings.Repeat(identityRune, 64), strings.Repeat(fingerprintRune, 64), detectedAt); err != nil {
		t.Fatal(err)
	}
}

func assertHealthTimelineOutboxSource(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, issueID, sourceEventRef, eventType string, eventVersion int, occurredAt time.Time) {
	t.Helper()
	var actualEventType, aggregateType, aggregateID, sourceRef, summary, correlation string
	var actualVersion int
	var actualOccurredAt time.Time
	if err := pool.QueryRow(ctx, `SELECT event_type,aggregate_type,aggregate_id::text,source_ref,event_version,
summary,correlation::text,occurred_at
FROM ops.timeline_projection_outbox
WHERE workspace_id=$1 AND source_event_ref=$2`, workspaceID, sourceEventRef).Scan(
		&actualEventType, &aggregateType, &aggregateID, &sourceRef, &actualVersion, &summary, &correlation, &actualOccurredAt,
	); err != nil {
		t.Fatal(err)
	}
	wantSummary := "Health issue detected"
	if eventType == "HEALTH_ISSUE_RESOLVED" {
		wantSummary = "Health issue resolved"
	}
	if actualEventType != eventType || aggregateType != "HEALTH_ISSUE" || aggregateID != issueID ||
		sourceRef != "health_issue:"+issueID || actualVersion != eventVersion || summary != wantSummary ||
		correlation != "{}" || !actualOccurredAt.Equal(occurredAt) {
		t.Fatalf("Health outbox binding type=%s aggregate=%s/%s source=%s version=%d summary=%q correlation=%s occurred_at=%s",
			actualEventType, aggregateType, aggregateID, sourceRef, actualVersion, summary, correlation, actualOccurredAt)
	}
}

func assertHealthTimelineEvent(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, issueID, sourceEventRef, eventType string, eventVersion int, occurredAt time.Time) {
	t.Helper()
	var actualEventType, aggregateType, aggregateID, sourceRef, summary, correlation string
	var actualVersion int
	var actualOccurredAt time.Time
	if err := pool.QueryRow(ctx, `SELECT event_type,aggregate_type,aggregate_id::text,source_ref,event_version,
summary,correlation::text,occurred_at
FROM ops.knowledge_event
WHERE workspace_id=$1 AND source_event_ref=$2`, workspaceID, sourceEventRef).Scan(
		&actualEventType, &aggregateType, &aggregateID, &sourceRef, &actualVersion, &summary, &correlation, &actualOccurredAt,
	); err != nil {
		t.Fatal(err)
	}
	wantSummary := "Health issue detected"
	if eventType == "HEALTH_ISSUE_RESOLVED" {
		wantSummary = "Health issue resolved"
	}
	if actualEventType != eventType || aggregateType != "HEALTH_ISSUE" || aggregateID != issueID ||
		sourceRef != "health_issue:"+issueID || actualVersion != eventVersion || summary != wantSummary ||
		correlation != "{}" || !actualOccurredAt.Equal(occurredAt) {
		t.Fatalf("Health event binding type=%s aggregate=%s/%s source=%s version=%d summary=%q correlation=%s occurred_at=%s",
			actualEventType, aggregateType, aggregateID, sourceRef, actualVersion, summary, correlation, actualOccurredAt)
	}
}

func assertHealthTimelineSourceCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, issueID string, want int) {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.timeline_projection_outbox
WHERE workspace_id=$1 AND aggregate_type='HEALTH_ISSUE' AND aggregate_id=$2`, workspaceID, issueID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != want {
		t.Fatalf("Health Timeline source count=%d want %d", count, want)
	}
}

func projectTimelineSource(t *testing.T, ctx context.Context, repository *knowledgepostgres.Repository, want knowledgeapp.TimelineProjectionOutcome) {
	t.Helper()
	result, found, err := repository.ProjectNext(ctx)
	if err != nil || !found || result.Outcome != want {
		t.Fatalf("Timeline projection found=%t outcome=%s want=%s err=%v", found, result.Outcome, want, err)
	}
}

func isPostgresCode(err error, code string) bool {
	var pgErr *pgconn.PgError
	return err != nil && errors.As(err, &pgErr) && pgErr.Code == code
}
