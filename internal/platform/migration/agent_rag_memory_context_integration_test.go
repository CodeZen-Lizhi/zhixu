//go:build integration

package migration

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAgentRAGMemoryContextMigrationLifecycle(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if err := provider.UpTo(ctx, 60); err != nil {
		t.Fatalf("00060 up: %v", err)
	}
	fixture := seedRAGMemoryContextFixture(t, ctx, pool)
	legacy := fixture.newAttempt(t, ctx, pool, "001")
	const legacyModelRunID = "61000000-0000-4000-8000-000000000301"
	if err := insertDeferredRetrievalModelRun(ctx, pool, fixture, legacy, legacyModelRunID, "agent.rag-answer", nil); err != nil {
		t.Fatalf("insert pre-00061 conversation rag model run: %v", err)
	}
	if err := provider.UpTo(ctx, 61); err != nil {
		t.Fatalf("00061 up: %v", err)
	}
	assertLegacyRAGMemoryTupleRemainsNull(t, ctx, pool, legacyModelRunID)
	if err := provider.UpTo(ctx, 61); err != nil {
		t.Fatalf("00061 repeated up: %v", err)
	}
	assertLegacyRAGMemoryTupleRemainsNull(t, ctx, pool, legacyModelRunID)
	if err := provider.UpTo(ctx, 61); err != nil {
		t.Fatalf("00061 repeated up: %v", err)
	}
	assertMigrationVersion(t, ctx, pool, 61)
	assertRAGMemoryContextMigrationShape(t, ctx, pool)

	partial := fixture.newAttempt(t, ctx, pool, "002")
	partialSnapshotID := "61000000-0000-4000-8000-000000000102"
	insertRAGMemorySnapshot(t, ctx, pool, fixture, partial, partialSnapshotID, "61000000-0000-4000-8000-000000000202")
	if err := insertRAGMemoryModelRun(ctx, pool, fixture, partial, "61000000-0000-4000-8000-000000000302", partialSnapshotID, "agent.rag-answer", nil, false); err == nil {
		t.Fatal("partial memory tuple was accepted")
	} else {
		assertPostgresCode(t, err, "23514")
	}

	missing := fixture.newAttempt(t, ctx, pool, "003")
	if err := insertRAGMemoryModelRun(ctx, pool, fixture, missing, "61000000-0000-4000-8000-000000000303", "", "agent.rag-answer", nil, false); err == nil {
		t.Fatal("rag model run without memory snapshot was accepted")
	} else {
		assertPostgresCode(t, err, "23514")
	}

	nonRAG := fixture.newAttempt(t, ctx, pool, "004")
	nonRAGSnapshotID := "61000000-0000-4000-8000-000000000104"
	insertRAGMemorySnapshot(t, ctx, pool, fixture, nonRAG, nonRAGSnapshotID, "61000000-0000-4000-8000-000000000204")
	if err := insertRAGMemoryModelRun(ctx, pool, fixture, nonRAG, "61000000-0000-4000-8000-000000000304", nonRAGSnapshotID, "agent.relation-assessment", &fixture.indexID, true); err == nil {
		t.Fatal("non-rag model run bound a memory snapshot")
	} else {
		assertPostgresCode(t, err, "23514")
	}

	legal := fixture.newAttempt(t, ctx, pool, "005")
	legalSnapshotID := "61000000-0000-4000-8000-000000000105"
	legalRunID := "61000000-0000-4000-8000-000000000305"
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	insertRAGMemorySnapshot(t, ctx, tx, fixture, legal, legalSnapshotID, "61000000-0000-4000-8000-000000000205")
	if err := insertRAGMemoryModelRun(ctx, tx, fixture, legal, legalRunID, legalSnapshotID, "agent.rag-answer", nil, true); err != nil {
		t.Fatalf("insert preparing rag model run: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE agent.rag_memory_snapshot SET
		status='READY',context_schema_version='agent-rag-memory-context/v1',context_digest=repeat('a',64),
		context_item_count=1,context_bytes=128,model_run_id=$2,updated_at=now()
		WHERE id=$1`, legalSnapshotID, legalRunID); err != nil {
		t.Fatalf("prepare to ready snapshot: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var status, boundRunID, boundSnapshotID string
	if err := pool.QueryRow(ctx, `SELECT snapshot.status,snapshot.model_run_id::text,run.memory_snapshot_id::text
		FROM agent.rag_memory_snapshot snapshot
		JOIN agent.model_run run ON run.id=snapshot.model_run_id
		WHERE snapshot.id=$1`, legalSnapshotID).Scan(&status, &boundRunID, &boundSnapshotID); err != nil {
		t.Fatal(err)
	}
	if status != "READY" || boundRunID != legalRunID || boundSnapshotID != legalSnapshotID {
		t.Fatalf("legal binding status=%s run=%s snapshot=%s", status, boundRunID, boundSnapshotID)
	}

	if err := provider.UpTo(ctx, 61); err != nil {
		t.Fatalf("00061 final repeated up: %v", err)
	}
	assertMigrationVersion(t, ctx, pool, 61)
	assertRAGMemoryContextMigrationShape(t, ctx, pool)
}

type ragMemoryContextFixture struct {
	workspaceID  string
	workflowID   string
	runID        string
	conversation string
	indexID      string
}

type ragMemoryContextAttempt struct {
	nodeID    string
	attemptID string
}

func seedRAGMemoryContextFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) ragMemoryContextFixture {
	t.Helper()
	fixture := ragMemoryContextFixture{
		workspaceID:  "61000000-0000-4000-8000-000000000001",
		workflowID:   "61000000-0000-4000-8000-000000000011",
		runID:        "61000000-0000-4000-8000-000000000021",
		conversation: "61000000-0000-4000-8000-000000000031",
		indexID:      "61000000-0000-4000-8000-000000000041",
	}
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
	) VALUES($1,'rag-memory-context','/tmp/rag-memory-context','/tmp/rag-memory-context',now(),'active',1,now(),now())`, fixture.workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
		VALUES($1,$2,'rag-memory-context',1,'{"nodes":[]}',now())`, fixture.workflowID, fixture.workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at)
		VALUES($1,$2,$3,'running','{}',1,now(),now())`, fixture.runID, fixture.workspaceID, fixture.workflowID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO agent.conversation(
		id,workspace_id,status,title,version,last_activity_at,created_at,updated_at,idempotency_key,request_hash
	) VALUES($1,$2,'open','RAG memory context',1,now(),now(),now(),'rag-memory-context',repeat('b',64))`, fixture.conversation, fixture.workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO retrieval.index_version(
		id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
		source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,
		version,created_at,updated_at
	) VALUES($1,$2,'simple','v1',repeat('1',64),'{}','rag-memory-context:index',repeat('2',64),0,
		'rag-memory-context-index','building','["vector"]',1,now(),now())`, fixture.indexID, fixture.workspaceID); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func (fixture ragMemoryContextFixture) newAttempt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, suffix string) ragMemoryContextAttempt {
	t.Helper()
	attempt := ragMemoryContextAttempt{
		nodeID:    "61000000-0000-4000-8000-000000000" + suffix,
		attemptID: "61000000-0000-4000-8000-000000001" + suffix,
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_run(
		id,run_id,node_key,node_type,status,input,lease_owner,lease_until,version,created_at,updated_at
	) VALUES($1,$2,$3,'agent.rag-answer','running','{}','rag-memory-worker',now()+interval '5 minutes',1,now(),now())`,
		attempt.nodeID, fixture.runID, "rag-memory-"+suffix); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO workflow.node_attempt(
		id,node_run_id,attempt_no,dispatch_no,retry_no,delivery_id,lease_owner,lease_until,status,started_at
	) VALUES($1,$2,1,1,0,$3,'rag-memory-worker',now()+interval '5 minutes','running',now())`,
		attempt.attemptID, attempt.nodeID, "rag-memory-delivery-"+suffix); err != nil {
		t.Fatal(err)
	}
	return attempt
}

type ragMemoryContextExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func insertRAGMemorySnapshot(t *testing.T, ctx context.Context, executor ragMemoryContextExecutor, fixture ragMemoryContextFixture, attempt ragMemoryContextAttempt, snapshotID, claimantID string) {
	t.Helper()
	if _, err := executor.Exec(ctx, `INSERT INTO agent.rag_memory_snapshot(
		id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,claimant_id,owner_kind,owner_id,task_scope_id,
		status,created_at,updated_at
	) VALUES($1,$2,$3,$4,$5,$6,'USER','00000000-0000-5000-8000-000000000001',$7,'PREPARING',now(),now())`,
		snapshotID, fixture.workspaceID, fixture.runID, attempt.nodeID, attempt.attemptID, claimantID, fixture.conversation); err != nil {
		t.Fatal(err)
	}
}

func insertRAGMemoryModelRun(ctx context.Context, executor ragMemoryContextExecutor, fixture ragMemoryContextFixture, attempt ragMemoryContextAttempt, modelRunID, snapshotID, outputSchema string, indexID *string, completeTuple bool) error {
	var memorySnapshotID, schemaVersion, digest *string
	var itemCount, contextBytes *int
	if snapshotID != "" {
		memorySnapshotID = &snapshotID
	}
	if completeTuple {
		schema := "agent-rag-memory-context/v1"
		digestValue := strings.Repeat("a", 64)
		count, bytes := 1, 128
		schemaVersion, digest, itemCount, contextBytes = &schema, &digestValue, &count, &bytes
	}
	_, err := executor.Exec(ctx, `INSERT INTO agent.model_run(
		id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,
		adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,
		prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,
		reduced_schema_id,reduced_schema_version,retrieval_index_version_id,
		memory_snapshot_id,memory_context_schema_version,memory_context_digest,memory_context_item_count,memory_context_bytes,
		status,version,started_at,updated_at
	) VALUES($1,$2,$3,$4,$5,'openai-compatible','v1','model-test','2026-07-28','default','v1',
		'rag-memory-context','v1',$6,'v1','agent.refusal','v1',$7,$8,$9,$10,$11,$12,'RUNNING',1,now(),now())`,
		modelRunID, fixture.workspaceID, fixture.runID, attempt.nodeID, attempt.attemptID, outputSchema, indexID,
		memorySnapshotID, schemaVersion, digest, itemCount, contextBytes)
	return err
}

func insertDeferredRetrievalModelRun(ctx context.Context, executor ragMemoryContextExecutor, fixture ragMemoryContextFixture, attempt ragMemoryContextAttempt, modelRunID, outputSchema string, indexID *string) error {
	_, err := executor.Exec(ctx, `INSERT INTO agent.model_run(
		id,workspace_id,workflow_run_id,node_run_id,node_attempt_id,
		adapter_name,adapter_version,model_id,model_version,profile_id,profile_version,
		prompt_template_id,prompt_template_version,output_schema_id,output_schema_version,
		reduced_schema_id,reduced_schema_version,retrieval_index_version_id,status,version,started_at,updated_at
	) VALUES($1,$2,$3,$4,$5,'openai-compatible','v1','model-test','2026-07-28','default','v1',
		'rag-memory-context','v1',$6,'v1','agent.refusal','v1',$7,'RUNNING',1,now(),now())`,
		modelRunID, fixture.workspaceID, fixture.runID, attempt.nodeID, attempt.attemptID, outputSchema, indexID)
	return err
}

func assertRAGMemoryContextMigrationShape(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var tables, columns, triggers, constraints, meta int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='agent' AND table_name='rag_memory_snapshot'`).Scan(&tables); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema='agent' AND table_name='model_run' AND column_name IN (
			'memory_snapshot_id','memory_context_schema_version','memory_context_digest','memory_context_item_count','memory_context_bytes'
		)`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger
		WHERE tgname IN ('agent_rag_memory_snapshot_guard_mutation','agent_model_run_guard_mutation')`).Scan(&triggers); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint
		WHERE conname IN ('agent_rag_memory_snapshot_lifecycle','agent_model_run_memory_context_binding',
			'fk_agent_model_run_memory_snapshot','fk_agent_rag_memory_snapshot_model_run')`).Scan(&constraints); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.schema_meta WHERE key='agent_rag_memory_context' AND value='m8'`).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	if tables != 1 || columns != 5 || triggers != 2 || constraints != 4 || meta != 1 {
		t.Fatalf("tables=%d columns=%d triggers=%d constraints=%d meta=%d", tables, columns, triggers, constraints, meta)
	}
}

func assertLegacyRAGMemoryTupleRemainsNull(t *testing.T, ctx context.Context, pool *pgxpool.Pool, modelRunID string) {
	t.Helper()
	var snapshotNull, schemaNull, digestNull, itemCountNull, bytesNull bool
	if err := pool.QueryRow(ctx, `SELECT memory_snapshot_id IS NULL,memory_context_schema_version IS NULL,
		memory_context_digest IS NULL,memory_context_item_count IS NULL,memory_context_bytes IS NULL
		FROM agent.model_run WHERE id=$1`, modelRunID).Scan(
		&snapshotNull, &schemaNull, &digestNull, &itemCountNull, &bytesNull,
	); err != nil {
		t.Fatalf("read legacy rag model run: %v", err)
	}
	if !snapshotNull || !schemaNull || !digestNull || !itemCountNull || !bytesNull {
		t.Fatalf("legacy rag memory tuple nulls snapshot=%t schema=%t digest=%t count=%t bytes=%t",
			snapshotNull, schemaNull, digestNull, itemCountNull, bytesNull)
	}
}

var _ ragMemoryContextExecutor = (*pgxpool.Pool)(nil)
var _ ragMemoryContextExecutor = (pgx.Tx)(nil)
