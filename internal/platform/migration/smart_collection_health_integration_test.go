//go:build integration

package migration

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSmartCollectionHealthMigrationSchemaAndEmptyDownUp(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	runner, err := NewRunner(pool, projectmigrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}

	assertSmartCollectionHealthMigrationShape(t, ctx, pool)

	provider := migrationProvider(t, pool)
	if _, err := provider.DownTo(ctx, 24); err != nil {
		t.Fatalf("00025 empty Down failed: %v", err)
	}

	var tables, schemaMeta int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE (table_schema='learning' AND table_name IN ('smart_collection','smart_collection_command'))
		   OR (table_schema='ops' AND table_name IN (
				'health_scan','health_issue','health_issue_observation','health_issue_evidence',
				'health_issue_decision','health_scan_detector','health_scan_seen_identity','health_schedule','health_schedule_command'
		   ))`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.schema_meta
		WHERE key='smart_collection_health'`).Scan(&schemaMeta); err != nil {
		t.Fatal(err)
	}
	if tables != 0 || schemaMeta != 0 {
		t.Fatalf("00025 Down tables=%d schema_meta=%d", tables, schemaMeta)
	}

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("00025 Up after Down failed: %v", err)
	}
	assertSmartCollectionHealthMigrationShape(t, ctx, pool)
}

func TestSmartCollectionHealthMigrationGuardedDown(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	migrateSmartCollectionHealthTestDatabase(t, ctx, pool)

	insertSemanticLinkWorkspace(t, ctx, pool, "f1000000-0000-4000-8000-000000000001", "smart-collection-guard")
	insertSmartCollection(t, ctx, pool,
		"f2000000-0000-4000-8000-000000000001",
		"f1000000-0000-4000-8000-000000000001",
		"Guarded Collection",
		"guarded collection",
		`{"root":{"kind":"group","operator":"AND","clauses":[]},"sort":[]}`,
		"LIST",
		`{"density":"comfortable"}`,
		1,
		1,
	)

	_, err := migrationProvider(t, pool).DownTo(ctx, 24)
	assertPostgresCode(t, err, "55000")
}

func TestCollectionReadModelRevisionTracksWorkspaceMovesHealthAndRollback(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	migrateSmartCollectionHealthTestDatabase(t, ctx, pool)

	const (
		workspaceA = "f9100000-0000-4000-8000-000000000001"
		workspaceB = "f9100000-0000-4000-8000-000000000002"
		sourceID   = "f9200000-0000-4000-8000-000000000001"
		topicID    = "f9300000-0000-4000-8000-000000000001"
		issueID    = "f9400000-0000-4000-8000-000000000001"
	)
	insertSemanticLinkWorkspace(t, ctx, pool, workspaceA, "revision-a")
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at
	) VALUES($1,$2,$3,$3,now(),'inactive',now(),now())`, workspaceB, "revision-b", "/tmp/revision-b"); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := pool.Exec(ctx, `INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at)
		VALUES($1,$2,'text','revision source','revision-a.txt',$3)`, sourceID, workspaceA, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO core.topic(
		id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at)
		VALUES($1,$2,'Revision Topic','revision topic','','ACTIVE',1,$3,$3)`, topicID, workspaceA, now); err != nil {
		t.Fatal(err)
	}

	readRevision := func(workspaceID string) (int64, int64, int64) {
		t.Helper()
		var knowledge, conflict, health int64
		if err := pool.QueryRow(ctx, `SELECT knowledge_revision,conflict_revision,health_revision
			FROM core.workspace_read_model_revision WHERE workspace_id=$1`, workspaceID).Scan(&knowledge, &conflict, &health); err != nil {
			t.Fatal(err)
		}
		return knowledge, conflict, health
	}
	knowledgeA, _, healthA := readRevision(workspaceA)
	if knowledgeA != 2 || healthA != 0 {
		t.Fatalf("initial revision knowledge=%d health=%d", knowledgeA, healthA)
	}
	if _, err := pool.Exec(ctx, `UPDATE core.source SET workspace_id=$2 WHERE id=$1`, sourceID, workspaceB); err != nil {
		t.Fatal(err)
	}
	movedA, _, movedHealthA := readRevision(workspaceA)
	movedB, _, _ := readRevision(workspaceB)
	if movedA != knowledgeA+1 || movedB != 1 || movedHealthA != healthA {
		t.Fatalf("workspace move revisions a=%d b=%d health_a=%d", movedA, movedB, movedHealthA)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO ops.health_issue(
		id,workspace_id,type,target_type,target_id,detector_id,identity_hash,fingerprint_schema_version,
		fingerprint,detector_version,severity,evidence_summary,status,version,
		first_detected_at,last_detected_at,last_verified_at,created_at,updated_at)
		VALUES($1,$2,'ORPHAN','TOPIC',$3,'health.detector.revision',$4,'health-issue-fingerprint/v1',
		$5,'detector/v1','MEDIUM','revision fixture','OPEN',1,$6,$6,$6,$6,$6)`, issueID, workspaceA, topicID, strings.Repeat("a", 64), strings.Repeat("b", 64), now); err != nil {
		t.Fatal(err)
	}
	healthKnowledgeA, _, healthRevisionA := readRevision(workspaceA)
	if healthKnowledgeA != movedA || healthRevisionA != healthA+1 {
		t.Fatalf("health revision knowledge=%d health=%d", healthKnowledgeA, healthRevisionA)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE core.topic SET description='rolled back',version=2,updated_at=$2 WHERE id=$1`, topicID, now.Add(time.Second)); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	rolledBackKnowledge, _, rolledBackHealth := readRevision(workspaceA)
	if rolledBackKnowledge != healthKnowledgeA || rolledBackHealth != healthRevisionA {
		t.Fatalf("rollback advanced revision knowledge=%d health=%d", rolledBackKnowledge, rolledBackHealth)
	}
}

func TestCollectionReadModelRevisionOrdersOppositeWorkspaceMoves(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	migrateSmartCollectionHealthTestDatabase(t, ctx, pool)

	const (
		workspaceA = "f9500000-0000-4000-8000-000000000001"
		workspaceB = "f9500000-0000-4000-8000-000000000002"
		sourceA    = "f9600000-0000-4000-8000-000000000001"
		sourceB    = "f9600000-0000-4000-8000-000000000002"
	)
	insertSemanticLinkWorkspace(t, ctx, pool, workspaceA, "revision-order-a")
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at
	) VALUES($1,$2,$3,$3,now(),'inactive',now(),now())`, workspaceB, "revision-order-b", "/tmp/revision-order-b"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at)
		VALUES($1,$2,'text','revision order source a','revision-order-a.txt',now()),
		      ($3,$4,'text','revision order source b','revision-order-b.txt',now())`, sourceA, workspaceA, sourceB, workspaceB); err != nil {
		t.Fatal(err)
	}

	for iteration := 0; iteration < 10; iteration++ {
		targetA, targetB := workspaceB, workspaceA
		if iteration%2 == 1 {
			targetA, targetB = workspaceA, workspaceB
		}
		start := make(chan struct{})
		errors := make(chan error, 2)
		var wait sync.WaitGroup
		wait.Add(2)
		for _, move := range []struct {
			sourceID  string
			workspace string
		}{{sourceA, targetA}, {sourceB, targetB}} {
			move := move
			go func() {
				defer wait.Done()
				<-start
				_, err := pool.Exec(ctx, `UPDATE core.source SET workspace_id=$2 WHERE id=$1`, move.sourceID, move.workspace)
				errors <- err
			}()
		}
		close(start)
		wait.Wait()
		close(errors)
		for err := range errors {
			if err != nil {
				t.Fatalf("opposite workspace move iteration %d failed: %v", iteration, err)
			}
		}
	}

	for _, workspaceID := range []string{workspaceA, workspaceB} {
		var revision int64
		if err := pool.QueryRow(ctx, `SELECT knowledge_revision FROM core.workspace_read_model_revision WHERE workspace_id=$1`, workspaceID).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		if revision != 21 {
			t.Fatalf("workspace %s revision=%d want 21", workspaceID, revision)
		}
	}
}

func TestSmartCollectionHealthMigrationCollectionConstraints(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	migrateSmartCollectionHealthTestDatabase(t, ctx, pool)

	const workspaceID = "f3000000-0000-4000-8000-000000000001"
	insertSemanticLinkWorkspace(t, ctx, pool, workspaceID, "smart-collection-constraints")

	_, err := pool.Exec(ctx, `INSERT INTO learning.smart_collection(
		id,workspace_id,name,normalized_name,description,
		query_schema_version,query_version,query_definition,query_hash,
		view_type,view_config,status,version,created_at,updated_at
	) VALUES(
		'f3100000-0000-4000-8000-000000000001',$1,'bad query','bad query','',
		'collection-query/v1',1,'[]'::jsonb,repeat('1',64),
		'LIST','{}'::jsonb,'ACTIVE',1,now(),now()
	)`, workspaceID)
	assertPostgresCode(t, err, "23514")

	_, err = pool.Exec(ctx, `INSERT INTO learning.smart_collection(
		id,workspace_id,name,normalized_name,description,
		query_schema_version,query_version,query_definition,query_hash,
		view_type,view_config,status,version,created_at,updated_at
	) VALUES(
		'f3200000-0000-4000-8000-000000000001',$1,'bad view','bad view','',
		'collection-query/v1',1,'{}'::jsonb,repeat('2',64),
		'GRID','{}'::jsonb,'ACTIVE',1,now(),now()
	)`, workspaceID)
	assertPostgresCode(t, err, "23514")

	insertSmartCollection(t, ctx, pool,
		"f3300000-0000-4000-8000-000000000001",
		workspaceID,
		"Collection A",
		"collection a",
		`{"root":{"kind":"group","operator":"AND","clauses":[]},"sort":[]}`,
		"LIST",
		`{"density":"comfortable"}`,
		1,
		1,
	)

	_, err = pool.Exec(ctx, `INSERT INTO learning.smart_collection_command(
		workspace_id,idempotency_key,request_hash,command_type,
		collection_id,collection_version,receipt,created_at
	) VALUES(
		$1,'collection-create-invalid-version',repeat('2',64),'CREATE',
		$2,2,'{"collection_id":"f3300000-0000-4000-8000-000000000001","version":2}'::jsonb,now()
	)`, workspaceID, "f3300000-0000-4000-8000-000000000001")
	assertPostgresCode(t, err, "23514")
	if _, err := pool.Exec(ctx, `INSERT INTO learning.smart_collection_command(
		workspace_id,idempotency_key,request_hash,command_type,
		collection_id,collection_version,receipt,created_at
	) VALUES(
		$1,'collection-create',repeat('2',64),'CREATE',
		$2,1,'{"collection_id":"f3300000-0000-4000-8000-000000000001","status":"ACTIVE","version":1}'::jsonb,now()
	)`, workspaceID, "f3300000-0000-4000-8000-000000000001"); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE learning.smart_collection_command
		SET receipt='{}'::jsonb
		WHERE workspace_id=$1 AND idempotency_key='collection-create'`, workspaceID)
	assertPostgresCode(t, err, "55000")

	_, err = pool.Exec(ctx, `INSERT INTO learning.smart_collection(
		id,workspace_id,name,normalized_name,description,
		query_schema_version,query_version,query_definition,query_hash,
		view_type,view_config,status,version,created_at,updated_at
	) VALUES(
		'f3400000-0000-4000-8000-000000000001',$1,'Collection A Copy','collection a','',
		'collection-query/v1',1,'{}'::jsonb,repeat('3',64),
		'TABLE','{}'::jsonb,'ACTIVE',1,now(),now()
	)`, workspaceID)
	assertPostgresCode(t, err, "23505")

	if _, err := pool.Exec(ctx, `UPDATE learning.smart_collection
		SET status='ARCHIVED',version=2,updated_at=now()
		WHERE id=$1`, "f3300000-0000-4000-8000-000000000001"); err != nil {
		t.Fatal(err)
	}
	var receiptStatus string
	if err := pool.QueryRow(ctx, `SELECT receipt->>'status'
		FROM learning.smart_collection_command
		WHERE workspace_id=$1 AND idempotency_key='collection-create'`, workspaceID).Scan(&receiptStatus); err != nil {
		t.Fatal(err)
	}
	if receiptStatus != "ACTIVE" {
		t.Fatalf("create receipt status = %q", receiptStatus)
	}
	_, err = pool.Exec(ctx, `DELETE FROM learning.smart_collection WHERE id=$1`, "f3300000-0000-4000-8000-000000000001")
	assertPostgresCode(t, err, "55000")

	insertSmartCollection(t, ctx, pool,
		"f3500000-0000-4000-8000-000000000001",
		workspaceID,
		"Collection A Replacement",
		"collection a",
		`{"root":{"kind":"group","operator":"AND","clauses":[{"kind":"predicate","field":"object_type","operator":"EQ","value":"CLAIM"}]},"sort":[]}`,
		"TABLE",
		`{"columns":["title"]}`,
		1,
		1,
	)

	_, err = pool.Exec(ctx, `UPDATE learning.smart_collection
		SET description='mutated archived row',version=3,updated_at=now()
		WHERE id=$1`, "f3300000-0000-4000-8000-000000000001")
	assertPostgresCode(t, err, "55000")

	_, err = pool.Exec(ctx, `UPDATE learning.smart_collection
		SET query_definition='{"root":{"kind":"group","operator":"OR","clauses":[]}}'::jsonb,
		    query_hash=repeat('4',64),
		    version=2,
		    updated_at=now()
		WHERE id=$1`, "f3500000-0000-4000-8000-000000000001")
	assertPostgresCode(t, err, "23514")

	_, err = pool.Exec(ctx, `UPDATE learning.smart_collection
		SET query_version=2,version=2,updated_at=now()
		WHERE id=$1`, "f3500000-0000-4000-8000-000000000001")
	assertPostgresCode(t, err, "23514")

	_, err = pool.Exec(ctx, `UPDATE learning.smart_collection
		SET description='stale version',version=3,updated_at=now()
		WHERE id=$1`, "f3500000-0000-4000-8000-000000000001")
	assertPostgresCode(t, err, "23514")

	if _, err := pool.Exec(ctx, `UPDATE learning.smart_collection
		SET query_definition='{"root":{"kind":"group","operator":"OR","clauses":[{"kind":"predicate","field":"status","operator":"EQ","value":"ACTIVE"}]}}'::jsonb,
		    query_hash=repeat('5',64),
		    query_version=2,
		    version=2,
		    updated_at=now()
		WHERE id=$1`, "f3500000-0000-4000-8000-000000000001"); err != nil {
		t.Fatal(err)
	}
}

func TestSmartCollectionHealthMigrationAllowsFrozenScanToFailAfterCollectionChanges(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	migrateSmartCollectionHealthTestDatabase(t, ctx, pool)

	const (
		workspaceID  = "f3600000-0000-4000-8000-000000000001"
		collectionID = "f3700000-0000-4000-8000-000000000001"
		failedScanID = "f3800000-0000-4000-8000-000000000001"
		cancelScanID = "f3900000-0000-4000-8000-000000000001"
	)
	insertSemanticLinkWorkspace(t, ctx, pool, workspaceID, "smart-collection-frozen-scan")
	insertSmartCollection(t, ctx, pool,
		collectionID,
		workspaceID,
		"Frozen Scan Scope",
		"frozen scan scope",
		`{"root":{"kind":"group","operator":"AND","clauses":[{"kind":"predicate","field":"object_type","operator":"EQ","value":"CLAIM"}]},"sort":[]}`,
		"LIST",
		`{"density":"compact"}`,
		1,
		1,
	)
	insertSemanticLinkWorkflowRun(t, ctx, pool, workspaceID,
		"f3a00000-0000-4000-8000-000000000001",
		"f3b00000-0000-4000-8000-000000000001",
		"smart-collection-frozen-failed")
	insertSemanticLinkWorkflowRun(t, ctx, pool, workspaceID,
		"f3c00000-0000-4000-8000-000000000001",
		"f3d00000-0000-4000-8000-000000000001",
		"smart-collection-frozen-cancelled")

	insertScan := func(scanID, runID, key string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `INSERT INTO graph.semantic_link_scan(
			id,workspace_id,scope_type,scope_ref,scope_version,scope_schema_version,
			scope_hash,read_model_revision,fingerprint,idempotency_key,request_hash,
			workflow_run_id,status,total_nodes,version,created_at,updated_at
		) VALUES(
			$1,$2,'SMART_COLLECTION',$3,1,'semantic-link-smart-collection-scope/v1',
			repeat('1',64),repeat('2',64),repeat('3',64),$4,repeat('4',64),
			$5,'PENDING',2,1,now(),now()
		)`, scanID, workspaceID, collectionID, key, runID); err != nil {
			t.Fatal(err)
		}
	}
	insertScan(failedScanID, "f3b00000-0000-4000-8000-000000000001", "frozen-failed")
	insertScan(cancelScanID, "f3d00000-0000-4000-8000-000000000001", "frozen-cancelled")

	_, err := pool.Exec(ctx, `UPDATE graph.semantic_link_scan
		SET scope_hash=repeat('5',64),version=2,updated_at=now()
		WHERE id=$1`, failedScanID)
	assertPostgresCode(t, err, "23514")

	if _, err := pool.Exec(ctx, `UPDATE learning.smart_collection
		SET status='ARCHIVED',version=2,updated_at=now()
		WHERE id=$1`, collectionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE graph.semantic_link_scan
		SET status='FAILED',last_error='{"stage":"collection","code":"COLLECTION_VERSION_CONFLICT","retryable":false}'::jsonb,
			completed_at=now(),version=2,updated_at=now()
		WHERE id=$1`, failedScanID); err != nil {
		t.Fatalf("frozen scan failed transition after Collection archive: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE graph.semantic_link_scan
		SET status='CANCELLED',completed_at=now(),version=2,updated_at=now()
		WHERE id=$1`, cancelScanID); err != nil {
		t.Fatalf("frozen scan cancelled transition after Collection archive: %v", err)
	}

	var failedStatus, cancelledStatus string
	if err := pool.QueryRow(ctx, `SELECT
		(SELECT status FROM graph.semantic_link_scan WHERE id=$1),
		(SELECT status FROM graph.semantic_link_scan WHERE id=$2)`, failedScanID, cancelScanID).Scan(&failedStatus, &cancelledStatus); err != nil {
		t.Fatal(err)
	}
	if failedStatus != "FAILED" || cancelledStatus != "CANCELLED" {
		t.Fatalf("failed status=%q cancelled status=%q", failedStatus, cancelledStatus)
	}
}

func TestSmartCollectionHealthMigrationScanIssueHistoryAndScheduleConstraints(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	migrateSmartCollectionHealthTestDatabase(t, ctx, pool)

	const (
		workspaceID   = "f4000000-0000-4000-8000-000000000001"
		topicID       = "f4100000-0000-4000-8000-000000000001"
		collectionID  = "f4200000-0000-4000-8000-000000000001"
		definitionID  = "f4300000-0000-4000-8000-000000000001"
		runID         = "f4400000-0000-4000-8000-000000000001"
		proposalID    = "f4500000-0000-4000-8000-000000000001"
		scanID        = "f4600000-0000-4000-8000-000000000001"
		issueID       = "f4700000-0000-4000-8000-000000000001"
		observationID = "f4800000-0000-4000-8000-000000000001"
		decisionID    = "f4900000-0000-4000-8000-000000000001"
		scheduleID    = "f4a00000-0000-4000-8000-000000000001"
	)

	insertSemanticLinkWorkspace(t, ctx, pool, workspaceID, "smart-health-runtime")
	insertSmartCollection(t, ctx, pool,
		collectionID,
		workspaceID,
		"Health Scope",
		"health scope",
		`{"root":{"kind":"group","operator":"AND","clauses":[{"kind":"predicate","field":"object_type","operator":"EQ","value":"TOPIC"}]},"sort":[]}`,
		"LIST",
		`{"density":"compact"}`,
		1,
		1,
	)
	insertHealthTopic(t, ctx, pool, workspaceID, topicID, "health topic")
	insertSemanticLinkWorkflowRun(t, ctx, pool, workspaceID, definitionID, runID, "health-scan")
	insertKnowledgeChangeProposal(t, ctx, pool, workspaceID, proposalID, "health-repair-proposal")

	_, err := pool.Exec(ctx, `INSERT INTO ops.health_scan(
		id,workspace_id,scope_type,scope_ref,scope_version,scope_schema_version,scope_hash,scope_read_model_revision,scope_exact_count,
		fingerprint,idempotency_key,request_hash,workflow_run_id,max_items,status,version,created_at,updated_at
	) VALUES(
		'f4b00000-0000-4000-8000-000000000001',$1,'SMART_COLLECTION',$2,1,'health-scope/v1',NULL,repeat('3',64),0,
		repeat('1',64),'scan-missing-hash',repeat('2',64),$3,100,'PENDING',1,now(),now()
	)`, workspaceID, collectionID, runID)
	assertPostgresCode(t, err, "23514")
	_, err = pool.Exec(ctx, `INSERT INTO ops.health_scan(
		id,workspace_id,scope_type,scope_ref,scope_version,scope_schema_version,scope_hash,scope_read_model_revision,scope_exact_count,
		fingerprint,idempotency_key,request_hash,workflow_run_id,max_items,status,version,created_at,updated_at
	) VALUES(
		'f4b00000-0000-4000-8000-000000000001',$1,'SMART_COLLECTION',$2,2,'health-scope/v1',repeat('1',64),repeat('3',64),0,
		repeat('1',64),'scan-stale-version',repeat('2',64),$3,100,'PENDING',1,now(),now()
	)`, workspaceID, collectionID, runID)
	assertPostgresCode(t, err, "23514")
	_, err = pool.Exec(ctx, `INSERT INTO ops.health_scan(
		id,workspace_id,scope_type,scope_ref,scope_version,scope_schema_version,scope_hash,scope_read_model_revision,scope_exact_count,
		fingerprint,idempotency_key,request_hash,workflow_run_id,max_items,status,version,created_at,updated_at
	) VALUES(
		'f4b00000-0000-4000-8000-000000000001',$1,'SMART_COLLECTION',$2,1,'health-scope/v1',repeat('2',64),repeat('3',64),0,
		repeat('1',64),'scan-stale-hash',repeat('2',64),$3,100,'PENDING',1,now(),now()
	)`, workspaceID, collectionID, runID)
	assertPostgresCode(t, err, "23514")
	_, err = pool.Exec(ctx, `INSERT INTO ops.health_scan(
		id,workspace_id,scope_type,scope_ref,scope_version,scope_schema_version,scope_hash,scope_read_model_revision,scope_exact_count,
		fingerprint,idempotency_key,request_hash,workflow_run_id,max_items,status,version,created_at,updated_at
	) VALUES(
		'f4b00000-0000-4000-8000-000000000001',$1,'SMART_COLLECTION',$2,1,'health-scope/v1',repeat('1',64),NULL,0,
		repeat('1',64),'scan-missing-revision',repeat('2',64),$3,100,'PENDING',1,now(),now()
	)`, workspaceID, collectionID, runID)
	assertPostgresCode(t, err, "23514")
	_, err = pool.Exec(ctx, `INSERT INTO ops.health_scan(
		id,workspace_id,scope_type,scope_ref,scope_version,scope_schema_version,scope_hash,scope_read_model_revision,scope_exact_count,
		fingerprint,idempotency_key,request_hash,workflow_run_id,max_items,status,version,created_at,updated_at
	) VALUES(
		'f4b00000-0000-4000-8000-000000000001',$1,'SMART_COLLECTION',$2,1,'health-scope/v1',repeat('1',64),repeat('3',64),NULL,
		repeat('1',64),'scan-missing-exact-count',repeat('2',64),$3,100,'PENDING',1,now(),now()
	)`, workspaceID, collectionID, runID)
	assertPostgresCode(t, err, "23514")
	_, err = pool.Exec(ctx, `INSERT INTO ops.health_scan(
		id,workspace_id,scope_type,scope_ref,scope_version,scope_schema_version,scope_hash,scope_read_model_revision,scope_exact_count,
		fingerprint,idempotency_key,request_hash,workflow_run_id,max_items,status,version,created_at,updated_at
	) VALUES(
		'f4b00000-0000-4000-8000-000000000001',$1,'WORKSPACE',$1,1,'health-scope/workspace/v1',NULL,NULL,0,
		repeat('1',64),'scan-workspace-exact-count',repeat('2',64),$2,100,'PENDING',1,now(),now()
	)`, workspaceID, runID)
	assertPostgresCode(t, err, "23514")

	if _, err := pool.Exec(ctx, `INSERT INTO ops.health_scan(
		id,workspace_id,scope_type,scope_ref,scope_version,scope_schema_version,scope_hash,scope_read_model_revision,scope_exact_count,
		fingerprint,idempotency_key,request_hash,workflow_run_id,max_items,status,checkpoint,
		processed_count,created_count,reopened_count,resolved_count,unchanged_count,failed_count,
		version,created_at,updated_at
	) VALUES(
		$1,$2,'SMART_COLLECTION',$3,1,'health-scope/v1',repeat('1',64),repeat('3',64),0,
		repeat('4',64),'scan-runtime',repeat('5',64),$4,100,'PENDING','{}'::jsonb,
		0,0,0,0,0,0,1,now(),now()
	)`, scanID, workspaceID, collectionID, runID); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE ops.health_scan
		SET scope_read_model_revision=repeat('9',64),version=2,updated_at=now()
		WHERE id=$1`, scanID)
	assertPostgresCode(t, err, "23514")
	_, err = pool.Exec(ctx, `UPDATE ops.health_scan
		SET scope_exact_count=1,version=2,updated_at=now()
		WHERE id=$1`, scanID)
	assertPostgresCode(t, err, "23514")
	if _, err := pool.Exec(ctx, `UPDATE ops.health_scan
		SET status='RUNNING',checkpoint='{"page":1}'::jsonb,
		    processed_count=2,created_count=1,version=2,updated_at=now()
		WHERE id=$1`, scanID); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE ops.health_scan
		SET processed_count=1,version=3,updated_at=now()
		WHERE id=$1`, scanID)
	assertPostgresCode(t, err, "23514")
	_, err = pool.Exec(ctx, `UPDATE ops.health_scan
		SET status='PENDING',version=3,updated_at=now()
		WHERE id=$1`, scanID)
	assertPostgresCode(t, err, "23514")

	if _, err := pool.Exec(ctx, `INSERT INTO ops.health_scan_detector(
		scan_id,workspace_id,detector_id,detector_version,status,checkpoint,
		processed_count,created_count,reopened_count,resolved_count,unchanged_count,failed_count
	) VALUES(
		$1,$2,'review.invalidated','v1','PENDING','{}'::jsonb,
		0,0,0,0,0,0
	)`, scanID, workspaceID); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE ops.health_scan_detector
		SET status='UNAVAILABLE'
		WHERE scan_id=$1 AND detector_id='review.invalidated'`, scanID)
	assertPostgresCode(t, err, "23514")
	if _, err := pool.Exec(ctx, `UPDATE ops.health_scan_detector
		SET status='UNAVAILABLE',unavailable_reason='review owner not implemented',
		    completed_at=now()
		WHERE scan_id=$1 AND detector_id='review.invalidated'`, scanID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ops.health_scan_detector(
		scan_id,workspace_id,detector_id,detector_version,status,checkpoint,
		processed_count,created_count,reopened_count,resolved_count,unchanged_count,failed_count
	) VALUES(
		$1,$2,'missing.source','v1','PENDING','{}'::jsonb,
		0,0,0,0,0,0
	)`, scanID, workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.health_scan_detector
		SET status='RUNNING',started_at=now()
		WHERE scan_id=$1 AND detector_id='missing.source'`, scanID); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE ops.health_scan_detector
		SET status='PARTIAL',completed_at=now()
		WHERE scan_id=$1 AND detector_id='missing.source'`, scanID)
	assertPostgresCode(t, err, "23514")
	if _, err := pool.Exec(ctx, `UPDATE ops.health_scan_detector
		SET status='PARTIAL',failure_summary='{"stage":"detector","code":"PAGE_TIMEOUT","retryable":true}'::jsonb,
		    failed_count=1,completed_at=now()
		WHERE scan_id=$1 AND detector_id='missing.source'`, scanID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO ops.health_scan_detector(
		scan_id,workspace_id,detector_id,detector_version,status,checkpoint,
		processed_count,created_count,reopened_count,resolved_count,unchanged_count,failed_count
	) VALUES(
		$1,$2,'orphan','v1','PENDING','{}'::jsonb,
		0,0,0,0,0,0
	)`, scanID, workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.health_scan_detector
		SET status='RUNNING',started_at=now()
		WHERE scan_id=$1 AND detector_id='orphan'`, scanID); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE ops.health_scan_detector
		SET status='SUCCEEDED',failure_summary='{"stage":"detector","code":"UNEXPECTED"}'::jsonb,
		    completed_at=now()
		WHERE scan_id=$1 AND detector_id='orphan'`, scanID)
	assertPostgresCode(t, err, "23514")
	if _, err := pool.Exec(ctx, `UPDATE ops.health_scan_detector
		SET status='SUCCEEDED',completed_at=now()
		WHERE scan_id=$1 AND detector_id='orphan'`, scanID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.health_scan_detector
		SET resolved_count=1
		WHERE scan_id=$1 AND detector_id='orphan'`, scanID); err != nil {
		t.Fatal(err)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO ops.health_scan_seen_identity(
		scan_id,workspace_id,detector_id,identity_hash,created_at
	) VALUES(
		$1,$2,'review.invalidated',repeat('6',64),now()
	)`, scanID, workspaceID); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO ops.health_scan_seen_identity(
		scan_id,workspace_id,detector_id,identity_hash,created_at
	) VALUES(
		$1,$2,'review.invalidated',repeat('6',64),now()
	)`, scanID, workspaceID)
	assertPostgresCode(t, err, "23505")

	if _, err := pool.Exec(ctx, `INSERT INTO ops.health_issue(
		id,workspace_id,type,target_type,target_id,detector_id,identity_hash,
		fingerprint_schema_version,fingerprint,detector_version,severity,evidence_summary,
		status,version,first_detected_at,last_detected_at,last_verified_at,created_at,updated_at
	) VALUES(
		$1,$2,'MISSING_SOURCE','TOPIC',$3,'detector.missing_source',repeat('7',64),
		'health-fingerprint/v1',repeat('8',64),'v1','HIGH','missing source evidence',
		'OPEN',1,now(),now(),now(),now(),now()
	)`, issueID, workspaceID, topicID); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO ops.health_issue(
		id,workspace_id,type,target_type,target_id,detector_id,identity_hash,
		fingerprint_schema_version,fingerprint,detector_version,severity,evidence_summary,
		status,version,first_detected_at,last_detected_at,last_verified_at,created_at,updated_at
	) VALUES(
		'f4c00000-0000-4000-8000-000000000001',$1,'MISSING_SOURCE','TOPIC',$2,'detector.missing_source',repeat('7',64),
		'health-fingerprint/v1',repeat('9',64),'v1','HIGH','duplicate identity',
		'OPEN',1,now(),now(),now(),now(),now()
	)`, workspaceID, topicID)
	assertPostgresCode(t, err, "23505")
	_, err = pool.Exec(ctx, `INSERT INTO ops.health_issue(
		id,workspace_id,type,target_type,target_id,detector_id,identity_hash,
		fingerprint_schema_version,fingerprint,detector_version,severity,evidence_summary,
		status,version,first_detected_at,last_detected_at,last_verified_at,created_at,updated_at
	) VALUES(
		'f4d00000-0000-4000-8000-000000000001',$1,'BROKEN_REFERENCE','TOPIC',$2,'detector.broken_reference',repeat('a',64),
		'health-fingerprint/v1',repeat('8',64),'v1','LOW','duplicate active fingerprint',
		'OPEN',1,now(),now(),now(),now(),now()
	)`, workspaceID, topicID)
	assertPostgresCode(t, err, "23505")
	const deferredIssueID = "f4d00000-0000-4000-8000-000000000002"
	if _, err := pool.Exec(ctx, `INSERT INTO ops.health_issue(
		id,workspace_id,type,target_type,target_id,detector_id,identity_hash,
		fingerprint_schema_version,fingerprint,detector_version,severity,evidence_summary,
		status,version,first_detected_at,last_detected_at,last_verified_at,created_at,updated_at
	) VALUES(
		$1,$2,'STALE','TOPIC',$3,'detector.stale',repeat('0',64),
		'health-fingerprint/v1',repeat('1',64),'v1','MEDIUM','stale topic evidence',
		'OPEN',1,now(),now(),now(),now(),now()
	)`, deferredIssueID, workspaceID, topicID); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE ops.health_issue
		SET status='DEFERRED',status_reason='check later',deferred_until=now() - interval '1 second',
		    version=2,updated_at=now()
		WHERE id=$1`, deferredIssueID)
	assertPostgresCode(t, err, "23514")
	_, err = pool.Exec(ctx, `UPDATE ops.health_issue
		SET status='DEFERRED',status_reason='check later',deferred_until=now(),
		    version=2,updated_at=now()
		WHERE id=$1`, deferredIssueID)
	assertPostgresCode(t, err, "23514")
	if _, err := pool.Exec(ctx, `UPDATE ops.health_issue
		SET status='DEFERRED',status_reason='check later',deferred_until=now() + interval '1 hour',
		    version=2,updated_at=now()
		WHERE id=$1`, deferredIssueID); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO ops.health_issue_decision(
		id,workspace_id,issue_id,issue_version,idempotency_key,request_hash,
		action,reason,defer_until,created_at
	) VALUES(
		'f4d10000-0000-4000-8000-000000000001',$1,$2,2,'health-defer-past',repeat('1',64),
		'DEFER','check later',now() - interval '1 second',now()
	)`, workspaceID, deferredIssueID)
	assertPostgresCode(t, err, "23514")
	_, err = pool.Exec(ctx, `INSERT INTO ops.health_issue_decision(
		id,workspace_id,issue_id,issue_version,idempotency_key,request_hash,
		action,reason,defer_until,created_at
	) VALUES(
		'f4d10000-0000-4000-8000-000000000002',$1,$2,2,'health-defer-now',repeat('2',64),
		'DEFER','check later',now(),now()
	)`, workspaceID, deferredIssueID)
	assertPostgresCode(t, err, "23514")
	if _, err := pool.Exec(ctx, `INSERT INTO ops.health_issue_decision(
		id,workspace_id,issue_id,issue_version,idempotency_key,request_hash,
		action,reason,defer_until,created_at
	) VALUES(
		'f4d10000-0000-4000-8000-000000000003',$1,$2,2,'health-defer-future',repeat('3',64),
		'DEFER','check later',now() + interval '1 hour',now()
	)`, workspaceID, deferredIssueID); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE ops.health_issue
		SET status='PROPOSAL_CREATED',repair_proposal_id=$2,version=1,updated_at=now()
		WHERE id=$1`, issueID, proposalID)
	assertPostgresCode(t, err, "23514")
	_, err = pool.Exec(ctx, `UPDATE ops.health_issue
		SET status='PROPOSAL_CREATED',repair_proposal_id=$2,
		    repair_option_code='health.repair.verify-provenance',
		    repair_binding_object_versions='[{"Ref":{"Type":"TOPIC","ID":"f4500000-0000-4000-8000-000000000001"},"Version":1}]'::jsonb,
		    repair_proposal_created_at=now(),
		    version=2,last_detected_at=now(),last_verified_at=now(),updated_at=now()
		WHERE id=$1`, issueID, proposalID)
	assertPostgresCode(t, err, "23514")
	if _, err := pool.Exec(ctx, `UPDATE ops.health_issue
			SET status='PROPOSAL_CREATED',repair_proposal_id=$2,
			    repair_option_code='health.repair.verify-provenance',
			    repair_binding_fingerprint=fingerprint,
			    repair_binding_object_versions='[{"Ref":{"Type":"TOPIC","ID":"f4500000-0000-4000-8000-000000000001"},"Version":1}]'::jsonb,
			    repair_proposal_created_at=now(),
			    version=2,last_detected_at=now(),last_verified_at=now(),updated_at=now()
		WHERE id=$1`, issueID, proposalID); err != nil {
		t.Fatal(err)
	}

	_, err = pool.Exec(ctx, `INSERT INTO ops.health_issue_observation(
		id,workspace_id,issue_id,issue_version,scan_id,detector_version,
		fingerprint_schema_version,fingerprint,evidence_fingerprint,target_versions,
		severity,observed_at
	) VALUES(
		'f4e00000-0000-4000-8000-000000000001',$1,$2,1,$3,'v1',
		'health-fingerprint/v1',repeat('b',64),repeat('c',64),'[1]'::jsonb,
		'HIGH',now()
	)`, workspaceID, issueID, scanID)
	assertPostgresCode(t, err, "23514")
	if _, err := pool.Exec(ctx, `INSERT INTO ops.health_issue_observation(
		id,workspace_id,issue_id,issue_version,scan_id,detector_version,
		fingerprint_schema_version,fingerprint,evidence_fingerprint,target_versions,
		severity,observed_at
	) VALUES(
		$1,$2,$3,2,$4,'v1',
		'health-fingerprint/v1',repeat('b',64),repeat('c',64),'[1]'::jsonb,
		'HIGH',now()
	)`, observationID, workspaceID, issueID, scanID); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE ops.health_issue_observation
		SET severity='LOW'
		WHERE id=$1`, observationID)
	assertPostgresCode(t, err, "55000")

	_, err = pool.Exec(ctx, `INSERT INTO ops.health_issue_decision(
		id,workspace_id,issue_id,issue_version,proposal_id,idempotency_key,request_hash,
		action,reason,defer_until,created_at
	) VALUES(
		'f4f00000-0000-4000-8000-000000000001',$1,$2,1,$3,'health-decision-stale',repeat('d',64),
		'CREATE_REPAIR_PROPOSAL',NULL,NULL,now()
	)`, workspaceID, issueID, proposalID)
	assertPostgresCode(t, err, "23514")
	if _, err := pool.Exec(ctx, `INSERT INTO ops.health_issue_decision(
			id,workspace_id,issue_id,issue_version,proposal_id,repair_option_code,idempotency_key,request_hash,
			action,reason,defer_until,created_at
		) VALUES(
			$1,$2,$3,2,$4,'health.repair.verify-provenance','health-decision',repeat('e',64),
		'CREATE_REPAIR_PROPOSAL',NULL,NULL,now()
	)`, decisionID, workspaceID, issueID, proposalID); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE ops.health_issue_decision
		SET idempotency_key='mutated'
		WHERE id=$1`, decisionID)
	assertPostgresCode(t, err, "55000")

	_, err = pool.Exec(ctx, `INSERT INTO ops.health_schedule(
		id,workspace_id,scope_type,scope_ref,scope_version,scope_schema_version,scope_hash,
		cadence,cron_expression,timezone,max_items,next_run_at,version,created_at,updated_at
	) VALUES(
		'f5000000-0000-4000-8000-000000000001',$1,'SMART_COLLECTION',$2,1,'health-scope/v1',repeat('1',64),
		'CRON','0 * * * *','Asia/Shanghai',0,now(),1,now(),now()
	)`, workspaceID, collectionID)
	assertPostgresCode(t, err, "23514")
	_, err = pool.Exec(ctx, `INSERT INTO ops.health_schedule(
		id,workspace_id,scope_type,scope_ref,scope_version,scope_schema_version,scope_hash,
		cadence,cron_expression,timezone,max_items,next_run_at,version,created_at,updated_at
	) VALUES(
		'f5100000-0000-4000-8000-000000000001',$1,'SMART_COLLECTION',$2,1,'health-scope/v1',NULL,
		'CRON','0 * * * *','Asia/Shanghai',100,now(),1,now(),now()
	)`, workspaceID, collectionID)
	assertPostgresCode(t, err, "23514")
	if _, err := pool.Exec(ctx, `INSERT INTO ops.health_schedule(
		id,workspace_id,scope_type,scope_ref,scope_version,scope_schema_version,scope_hash,
		cadence,cron_expression,timezone,max_items,next_run_at,version,created_at,updated_at
	) VALUES(
		$1,$2,'SMART_COLLECTION',$3,1,'health-scope/v1',repeat('1',64),
		'CRON','0 * * * *','Asia/Shanghai',100,now(),1,now(),now()
	)`, scheduleID, workspaceID, collectionID); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE ops.health_scan_seen_identity
		SET identity_hash=repeat('1',64)
		WHERE scan_id=$1 AND detector_id='review.invalidated' AND identity_hash=repeat('6',64)`, scanID)
	assertPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `UPDATE ops.health_schedule
		SET last_run_at=now(),version=1,updated_at=now()
		WHERE id=$1`, scheduleID)
	assertPostgresCode(t, err, "23514")
	if _, err := pool.Exec(ctx, `UPDATE learning.smart_collection
		SET status='ARCHIVED',version=2,updated_at=now()
		WHERE id=$1`, collectionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.health_schedule
		SET last_run_at=now(),next_run_at=now() + interval '1 hour',version=2,updated_at=now()
		WHERE id=$1`, scheduleID); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE ops.health_scan
		SET status='SUCCEEDED',last_error='{"stage":"detector","code":"UNEXPECTED"}'::jsonb,
		    version=3,updated_at=now(),completed_at=now()
		WHERE id=$1`, scanID)
	assertPostgresCode(t, err, "23514")
	if _, err := pool.Exec(ctx, `UPDATE ops.health_scan
		SET status='PARTIAL',last_error='{"stage":"detector","code":"PAGE_TIMEOUT","retryable":true}'::jsonb,
		    failed_count=1,version=3,updated_at=now(),completed_at=now()
		WHERE id=$1`, scanID); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE ops.health_scan_detector
		SET resolved_count=2
		WHERE scan_id=$1 AND detector_id='orphan'`, scanID)
	assertPostgresCode(t, err, "55000")
}

func assertSmartCollectionHealthMigrationShape(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var tables, triggers, schemaMeta, revisionTables, revisionTriggers int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE (table_schema='learning' AND table_name IN ('smart_collection','smart_collection_command'))
		   OR (table_schema='ops' AND table_name IN (
				'health_scan','health_issue','health_issue_observation','health_issue_evidence',
				'health_issue_decision','health_scan_detector','health_scan_seen_identity','health_schedule','health_schedule_command'
		   ))`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger
		WHERE tgname IN (
			'smart_collection_validate_write',
			'smart_collection_reject_delete',
			'smart_collection_command_validate_insert',
			'smart_collection_command_reject_update_delete',
			'health_issue_validate_write',
			'health_issue_observation_validate_insert',
			'health_issue_decision_validate_insert',
			'health_issue_evidence_validate_insert',
			'health_issue_observation_reject_update_delete',
			'health_issue_evidence_reject_update_delete',
			'health_issue_decision_reject_update_delete',
			'health_scan_validate_write',
			'health_scan_detector_validate_write',
			'health_scan_seen_identity_reject_update_delete',
				'health_schedule_validate_write',
			'health_schedule_command_reject_update_delete'
		)`).Scan(&triggers); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.schema_meta
		WHERE key='smart_collection_health' AND value='m7-03'`).Scan(&schemaMeta); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='core' AND table_name='workspace_read_model_revision'`).Scan(&revisionTables); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger
		WHERE tgname IN (
			'collection_revision_topic_change','collection_revision_topic_alias_change',
			'collection_revision_claim_change','collection_revision_claim_source_change',
			'collection_revision_source_change','collection_revision_source_version_change',
			'collection_revision_relation_change',
			'collection_revision_conflict_change','collection_revision_conflict_member_change',
			'collection_revision_health_issue_change'
		)`).Scan(&revisionTriggers); err != nil {
		t.Fatal(err)
	}
	if tables != 11 || triggers != 16 || schemaMeta != 1 || revisionTables != 1 || revisionTriggers != 10 {
		t.Fatalf("shape tables=%d triggers=%d schema_meta=%d revision_tables=%d revision_triggers=%d", tables, triggers, schemaMeta, revisionTables, revisionTriggers)
	}
}

func migrateSmartCollectionHealthTestDatabase(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	runner, err := NewRunner(pool, projectmigrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}
}

func insertSmartCollection(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	collectionID string,
	workspaceID string,
	name string,
	normalizedName string,
	queryDefinition string,
	viewType string,
	viewConfig string,
	queryVersion int,
	version int,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO learning.smart_collection(
		id,workspace_id,name,normalized_name,description,
		query_schema_version,query_version,query_definition,query_hash,
		view_type,view_config,status,version,created_at,updated_at
	) VALUES(
		$1,$2,$3,$4,'',
		'collection-query/v1',$5,$6::jsonb,repeat('1',64),
		$7,$8::jsonb,'ACTIVE',$9,now(),now()
	)`, collectionID, workspaceID, name, normalizedName, queryVersion, queryDefinition, viewType, viewConfig, version); err != nil {
		t.Fatal(err)
	}
}

func insertHealthTopic(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, topicID, normalizedName string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO core.topic(
		id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at
	) VALUES($1,$2,$3,$3,'','ACTIVE',1,now(),now())`, topicID, workspaceID, normalizedName); err != nil {
		t.Fatal(err)
	}
}

func insertKnowledgeChangeProposal(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, proposalID, key string) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO change_control.proposal(
		id,workspace_id,proposal_type,risk_level,status,idempotency_key,request_hash,version,created_at,updated_at
	) VALUES($1,$2,'knowledge_change','HIGH','ready_for_review',$3,repeat('9',64),1,now(),now())`,
		proposalID, workspaceID, key); err != nil {
		t.Fatal(err)
	}
}
