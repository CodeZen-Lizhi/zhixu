//go:build integration

package migration

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestHealthAffectedChangeMigrationSchemaAndUpRepeat(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	runner := newAtlasRunnerForPool(t, pool)
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}
	assertHealthAffectedChangeMigrationShape(t, ctx, pool)

	provider := migrationProvider(t, pool)
	if err := provider.Up(ctx); err != nil {
		t.Fatalf("00028 repeated up: %v", err)
	}
	assertHealthAffectedChangeMigrationShape(t, ctx, pool)
}

func TestHealthAffectedChangeMigrationProducersRollback(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	runner := newAtlasRunnerForPool(t, pool)
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}

	const workspaceID = "fa000000-0000-4000-8000-000000000001"
	insertSemanticLinkWorkspace(t, ctx, pool, workspaceID, "health-affected-change")
	seedHealthAffectedKnowledgeAggregates(t, ctx, pool, workspaceID)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO core.knowledge_command_receipt(
		workspace_id,idempotency_key,request_hash,command_type,aggregate_type,aggregate_id,aggregate_version,created_at)
		VALUES($1,'rollback',repeat('1',64),'topic.create','TOPIC','fa100000-0000-4000-8000-000000000001',1,clock_timestamp())`, workspaceID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	var inside int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM ops.health_affected_change_outbox WHERE workspace_id=$1`, workspaceID).Scan(&inside); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if inside != 1 {
		_ = tx.Rollback(ctx)
		t.Fatalf("transactional outbox rows=%d", inside)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var afterRollback int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.health_affected_change_outbox WHERE workspace_id=$1`, workspaceID).Scan(&afterRollback); err != nil {
		t.Fatal(err)
	}
	if afterRollback != 0 {
		t.Fatalf("rolled-back outbox rows=%d", afterRollback)
	}

	insertHealthKnowledgeReceipt(t, ctx, pool, workspaceID, "topic", "topic.create", "TOPIC", "fa110000-0000-4000-8000-000000000001", 1)
	insertHealthKnowledgeReceipt(t, ctx, pool, workspaceID, "claim", "claim.confirm", "CLAIM", "fa120000-0000-4000-8000-000000000001", 2)
	insertHealthKnowledgeReceipt(t, ctx, pool, workspaceID, "relation", "relation.confirm", "RELATION", "fa130000-0000-4000-8000-000000000001", 3)
	insertHealthKnowledgeReceipt(t, ctx, pool, workspaceID, "conflict", "conflict.open", "CONFLICT", "fa140000-0000-4000-8000-000000000001", 4)
	seedHealthAffectedRetrievalFacts(t, ctx, pool, workspaceID)

	var knowledgeEvents, indexEvents, vectorEvents, lexicalEvents int
	if err := pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE event_type='health.affected.knowledge_changed'),
		count(*) FILTER (WHERE event_type='health.affected.index_failed'),
		count(*) FILTER (WHERE event_type='health.affected.vector_degraded'),
		count(*) FILTER (WHERE event_type='health.affected.lexical_degraded')
		FROM ops.health_affected_change_outbox WHERE workspace_id=$1`, workspaceID).Scan(&knowledgeEvents, &indexEvents, &vectorEvents, &lexicalEvents); err != nil {
		t.Fatal(err)
	}
	if knowledgeEvents != 4 || indexEvents != 1 || vectorEvents != 2 || lexicalEvents != 1 {
		t.Fatalf("events knowledge=%d index=%d vector=%d lexical=%d", knowledgeEvents, indexEvents, vectorEvents, lexicalEvents)
	}
	var failedVectors, oversizedVectors int
	if err := pool.QueryRow(ctx, `SELECT
		count(*) FILTER (WHERE change_status='failed'),
		count(*) FILTER (WHERE change_status='skipped_oversized')
		FROM ops.health_affected_change_outbox
		WHERE workspace_id=$1 AND event_type='health.affected.vector_degraded'`, workspaceID).Scan(&failedVectors, &oversizedVectors); err != nil {
		t.Fatal(err)
	}
	if failedVectors != 1 || oversizedVectors != 1 {
		t.Fatalf("vector events failed=%d oversized=%d", failedVectors, oversizedVectors)
	}

	_, err = pool.Exec(ctx, `UPDATE ops.health_affected_change_outbox
		SET attempt_count=attempt_count+1,version=version+1,updated_at=clock_timestamp()
		WHERE workspace_id=$1 AND source_kind='KNOWLEDGE_COMMAND_RECEIPT'`, workspaceID)
	assertPostgresCode(t, err, "23514")
	_, err = pool.Exec(ctx, `DELETE FROM ops.health_affected_change_outbox WHERE workspace_id=$1`, workspaceID)
	assertPostgresCode(t, err, "55000")
}

func assertHealthAffectedChangeMigrationShape(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var tableCount, columns, constraints, triggers, indexes, schemaMeta int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='ops' AND table_name='health_affected_change_outbox'`).Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema='ops' AND table_name='health_affected_change_outbox'
		  AND column_name IN ('schema_version','event_version','event_type','source_kind','source_key','source_hash',
			'aggregate_id','aggregate_sub_id','aggregate_version','available_at','attempt_count','last_error_code',
			'bound_scan_id','bound_workflow_run_id','published_at','poisoned_at','manual_recovery_required','version')`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint
		WHERE conrelid='ops.health_affected_change_outbox'::regclass
		  AND conname IN ('ops_health_affected_change_source_binding','ops_health_affected_change_lifecycle',
			'ops_health_affected_change_time_order','fk_ops_health_affected_change_scan',
			'fk_ops_health_affected_change_workflow')`).Scan(&constraints); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(DISTINCT trigger_name) FROM information_schema.triggers
		WHERE trigger_name IN ('health_affected_change_outbox_validate_write','health_affected_knowledge_change_enqueue',
			'health_affected_index_failure_enqueue','health_affected_vector_degradation_enqueue',
			'health_affected_lexical_degradation_enqueue')`).Scan(&triggers); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes
		WHERE schemaname='ops' AND indexname IN ('idx_ops_health_affected_change_due','idx_ops_health_affected_change_workspace')`).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.schema_meta
		WHERE key='health_affected_change_outbox' AND value='m7-03'`).Scan(&schemaMeta); err != nil {
		t.Fatal(err)
	}
	if tableCount != 1 || columns != 18 || constraints != 5 || triggers != 5 || indexes != 2 || schemaMeta != 1 {
		t.Fatalf("00028 shape table=%d columns=%d constraints=%d triggers=%d indexes=%d meta=%d", tableCount, columns, constraints, triggers, indexes, schemaMeta)
	}
}

func insertHealthKnowledgeReceipt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, key, commandType, aggregateType, aggregateID string, version int64) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO core.knowledge_command_receipt(
		workspace_id,idempotency_key,request_hash,command_type,aggregate_type,aggregate_id,aggregate_version,created_at)
		VALUES($1,$2,repeat('a',64),$3,$4,$5,$6,clock_timestamp())`,
		workspaceID, "health-affected-"+key, commandType, aggregateType, aggregateID, version); err != nil {
		t.Fatal(err)
	}
}

func seedHealthAffectedKnowledgeAggregates(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID string) {
	t.Helper()
	connection, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, `SET session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, restoreErr := connection.Exec(ctx, `SET session_replication_role=origin`); restoreErr != nil {
			t.Errorf("restore knowledge fixture trigger role: %v", restoreErr)
		}
	}()
	statements := []string{
		`INSERT INTO core.topic(id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at)
		 VALUES('fa100000-0000-4000-8000-000000000001',$1,'rollback topic','rollback topic','','ACTIVE',1,clock_timestamp(),clock_timestamp())`,
		`INSERT INTO core.topic(id,workspace_id,name,normalized_name,description,status,version,created_at,updated_at)
		 VALUES('fa110000-0000-4000-8000-000000000001',$1,'affected topic','affected topic','','ACTIVE',1,clock_timestamp(),clock_timestamp())`,
		`INSERT INTO core.claim(id,workspace_id,statement,normalized_statement,applicability,applicability_schema_version,
		 applicability_hash,status,confidence_score,confidence_factors,fingerprint,version,created_at,updated_at)
		 VALUES('fa120000-0000-4000-8000-000000000001',$1,'affected claim','affected claim','{}',
		 'knowledge-applicability/v1',repeat('1',64),'SUGGESTED',NULL,'{}',repeat('2',64),2,clock_timestamp(),clock_timestamp())`,
		`INSERT INTO core.relation(id,workspace_id,source_node_type,source_node_id,target_node_type,target_node_id,
		 relation_type,status,confidence_score,fingerprint,evidence_fingerprint,confirmation_method,confirmation_ref,
		 valid_from,valid_to,version,created_at,updated_at)
		 VALUES('fa130000-0000-4000-8000-000000000001',$1,'TOPIC','fa110000-0000-4000-8000-000000000001',
		 'CLAIM','fa120000-0000-4000-8000-000000000001','SUPPORTS','SUGGESTED',NULL,repeat('3',64),
		 NULL,NULL,NULL,NULL,NULL,3,clock_timestamp(),clock_timestamp())`,
		`INSERT INTO core.conflict(id,workspace_id,topic_id,status,severity,summary,applicability_assessment,
		 applicability_hash,overlap_reason,fingerprint,resolution,resolution_reference,version,created_at,updated_at,resolved_at)
		 VALUES('fa140000-0000-4000-8000-000000000001',$1,NULL,'OPEN','HIGH','affected conflict','EXACT',
		 repeat('4',64),NULL,repeat('5',64),NULL,NULL,4,clock_timestamp(),clock_timestamp(),NULL)`,
	}
	for _, statement := range statements {
		if _, err := connection.Exec(ctx, statement, workspaceID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := connection.Exec(ctx, `SET session_replication_role=origin`); err != nil {
		t.Fatal(err)
	}
}

func seedHealthAffectedRetrievalFacts(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID string) {
	t.Helper()
	const (
		embeddingID     = "fa200000-0000-4000-8000-000000000001"
		failedIndexID   = "fa210000-0000-4000-8000-000000000001"
		vectorIndexID   = "fa220000-0000-4000-8000-000000000001"
		failedChunkID   = "fa230000-0000-4000-8000-000000000001"
		oversizeChunkID = "fa240000-0000-4000-8000-000000000001"
		lexicalChunkID  = "fa240000-0000-4000-8000-000000000002"
	)
	connection, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, `SET session_replication_role=replica`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, restoreErr := connection.Exec(ctx, `SET session_replication_role=origin`); restoreErr != nil {
			t.Errorf("restore retrieval trigger role: %v", restoreErr)
		}
	}()
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO retrieval.embedding_version(id,provider,adapter_name,adapter_version,model,dimensions,normalization,distance_metric,config_hash,created_at)
		  VALUES($1,'test','test','v1','test',3,'none','cosine',repeat('1',64),clock_timestamp())`, []any{embeddingID}},
		{`INSERT INTO retrieval.index_version(id,workspace_id,embedding_version_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,
		  fusion_config,source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,version,created_at,updated_at)
		  VALUES($1,$2,$3,'simple','v1',repeat('2',64),'{}','failed-index',repeat('3',64),0,'health-failed-index','building','[]',1,clock_timestamp(),clock_timestamp())`, []any{failedIndexID, workspaceID, embeddingID}},
		{`INSERT INTO retrieval.index_version(id,workspace_id,embedding_version_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,
		  fusion_config,source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,version,created_at,updated_at)
		  VALUES($1,$2,$3,'simple','v1',repeat('4',64),'{}','vector-index',repeat('5',64),3,'health-vector-index','building','[]',1,clock_timestamp(),clock_timestamp())`, []any{vectorIndexID, workspaceID, embeddingID}},
		{`INSERT INTO ingestion.canonical_chunk(id,workspace_id,parse_projection_id,sequence,heading_path,content,content_hash,source_span_id,
		  byte_count,rune_count,parser_version,chunk_strategy_version,schema_version,atomic_oversized,status,created_at)
		  VALUES($1,$2,'fa250000-0000-4000-8000-000000000001',0,'[]','failed chunk',repeat('6',64),
		  'fa260000-0000-4000-8000-000000000001',12,12,'v1','v1','v1',false,'active',clock_timestamp())`, []any{failedChunkID, workspaceID}},
		{`INSERT INTO ingestion.canonical_chunk(id,workspace_id,parse_projection_id,sequence,heading_path,content,content_hash,source_span_id,
		  byte_count,rune_count,parser_version,chunk_strategy_version,schema_version,atomic_oversized,status,created_at)
		  VALUES($1,$2,'fa250000-0000-4000-8000-000000000002',0,'[]','oversized chunk',repeat('7',64),
		  'fa260000-0000-4000-8000-000000000002',16,16,'v1','v1','v1',true,'active',clock_timestamp())`, []any{oversizeChunkID, workspaceID}},
		{`INSERT INTO ingestion.canonical_chunk(id,workspace_id,parse_projection_id,sequence,heading_path,content,content_hash,source_span_id,
		  byte_count,rune_count,parser_version,chunk_strategy_version,schema_version,atomic_oversized,status,created_at)
		  VALUES($1,$2,'fa250000-0000-4000-8000-000000000003',0,'[]','lexical failed chunk',repeat('8',64),
		  'fa260000-0000-4000-8000-000000000003',20,20,'v1','v1','v1',false,'active',clock_timestamp())`, []any{lexicalChunkID, workspaceID}},
		{`INSERT INTO retrieval.index_manifest_chunk(index_version_id,chunk_id,workspace_id,content_hash,sequence,parser_version,chunk_strategy_version,schema_version,created_at)
		  VALUES($1,$2,$3,repeat('6',64),0,'v1','v1','v1',clock_timestamp())`, []any{vectorIndexID, failedChunkID, workspaceID}},
		{`INSERT INTO retrieval.index_manifest_chunk(index_version_id,chunk_id,workspace_id,content_hash,sequence,parser_version,chunk_strategy_version,schema_version,created_at)
		  VALUES($1,$2,$3,repeat('7',64),1,'v1','v1','v1',clock_timestamp())`, []any{vectorIndexID, oversizeChunkID, workspaceID}},
		{`INSERT INTO retrieval.index_manifest_chunk(index_version_id,chunk_id,workspace_id,content_hash,sequence,parser_version,chunk_strategy_version,schema_version,created_at)
		  VALUES($1,$2,$3,repeat('8',64),2,'v1','v1','v1',clock_timestamp())`, []any{vectorIndexID, lexicalChunkID, workspaceID}},
		{`INSERT INTO retrieval.chunk_projection(index_version_id,chunk_id,workspace_id,embedding_version_id,search_vector,embedding,
		  token_count,lexical_status,vector_status,failure_code,created_at,updated_at)
		  VALUES($1,$2,$3,$4,NULL,NULL,3,'pending','pending',NULL,clock_timestamp(),clock_timestamp())`, []any{vectorIndexID, failedChunkID, workspaceID, embeddingID}},
		{`INSERT INTO retrieval.chunk_projection(index_version_id,chunk_id,workspace_id,embedding_version_id,search_vector,embedding,
		  token_count,lexical_status,vector_status,failure_code,created_at,updated_at)
		  VALUES($1,$2,$3,$4,NULL,NULL,3,'pending','pending',NULL,clock_timestamp(),clock_timestamp())`, []any{vectorIndexID, oversizeChunkID, workspaceID, embeddingID}},
		{`INSERT INTO retrieval.chunk_projection(index_version_id,chunk_id,workspace_id,embedding_version_id,search_vector,embedding,
		  token_count,lexical_status,vector_status,failure_code,created_at,updated_at)
		  VALUES($1,$2,$3,$4,NULL,NULL,3,'pending','pending',NULL,clock_timestamp(),clock_timestamp())`, []any{vectorIndexID, lexicalChunkID, workspaceID, embeddingID}},
	}
	for _, statement := range statements {
		if _, err := connection.Exec(ctx, statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := connection.Exec(ctx, `SET session_replication_role=origin`); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, `UPDATE retrieval.index_version
		SET status='failed',failure_code='INDEX_BUILD_FAILED',failed_at=clock_timestamp(),version=2,updated_at=clock_timestamp()
		WHERE id=$1 AND workspace_id=$2`, failedIndexID, workspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, `UPDATE retrieval.chunk_projection
		SET vector_status='failed',failure_code='VECTOR_FAILED',updated_at=clock_timestamp()
		WHERE index_version_id=$1 AND chunk_id=$2`, vectorIndexID, failedChunkID); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, `UPDATE retrieval.chunk_projection
		SET vector_status='skipped_oversized',failure_code='EMBEDDING_INPUT_OVERSIZED',updated_at=clock_timestamp()
		WHERE index_version_id=$1 AND chunk_id=$2`, vectorIndexID, oversizeChunkID); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, `UPDATE retrieval.chunk_projection
		SET lexical_status='failed',failure_code='LEXICAL_BUILD_FAILED',updated_at=clock_timestamp()
		WHERE index_version_id=$1 AND chunk_id=$2`, vectorIndexID, lexicalChunkID); err != nil {
		t.Fatal(err)
	}
}
