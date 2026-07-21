//go:build integration

package migration

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestRunnerRealPostgreSQLUpRepeatDownAndGuard(t *testing.T) {
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
	var maxVersion, applied, wrongSchema int
	if err := pool.QueryRow(ctx, `SELECT max(version_id), count(*) FILTER (WHERE is_applied AND version_id > 0) FROM public.goose_db_version`).Scan(&maxVersion, &applied); err != nil {
		t.Fatal(err)
	}
	if maxVersion != 24 || applied != 24 {
		t.Fatalf("project history max=%d applied=%d", maxVersion, applied)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_tables WHERE tablename LIKE 'river_%' AND schemaname <> 'workflow'`).Scan(&wrongSchema); err != nil {
		t.Fatal(err)
	}
	if wrongSchema != 0 {
		t.Fatalf("River tables outside workflow schema=%d", wrongSchema)
	}

	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	annotated, err := NewLegacyAnnotationFS(projectmigrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, annotated, goose.WithTableName(projectMigrationTable))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Down(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	insertRuntimeIdentityFixture(t, ctx, pool)
	downEmptyKnowledgeMigration(t, ctx, provider)
	// 00016 has no cache, V2 Delivery, or Hybrid Index data in this fixture.
	if _, err := provider.Down(ctx); err != nil {
		t.Fatalf("00016 Down rejected an empty Embedding Hybrid Search schema: %v", err)
	}
	// 00015 has no Source Manifest or Delivery data in this fixture.
	if _, err := provider.Down(ctx); err != nil {
		t.Fatalf("00015 Down rejected an empty Reindex Consumer schema: %v", err)
	}
	// 00014 has no Retrieval data yet, so remove it before exercising the
	// earlier M4-A runtime-identity downgrade guard.
	if _, err := provider.Down(ctx); err != nil {
		t.Fatalf("00014 Down rejected an empty Retrieval schema: %v", err)
	}
	// 00013 has no Proposal→Run bindings yet, so it can be removed before
	// removing 00012 and testing the M4-A runtime-identity guard.
	if _, err := provider.Down(ctx); err != nil {
		t.Fatalf("00013 Down rejected an unbound Proposal schema: %v", err)
	}
	if _, err := provider.Down(ctx); err != nil {
		t.Fatalf("00012 Down rejected legacy-only runtime identity: %v", err)
	}
	if _, err := provider.Down(ctx); err == nil {
		t.Fatal("00011 Down accepted Runtime identity data")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55000" {
			t.Fatalf("guarded Down error=%v", err)
		}
	}
}

func TestRunnerRetrievalMigrationDownRejectsBusinessData(t *testing.T) {
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
	if _, err := pool.Exec(ctx, `
INSERT INTO retrieval.embedding_version(
    id, provider, adapter_name, adapter_version, model, dimensions,
    normalization, distance_metric, config_hash, created_at
) VALUES (
    '60000000-0000-4000-8000-000000000001', 'test', 'test', 'v1', 'test-model', 3,
    'l2', 'cosine', repeat('1', 64), now()
)`); err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	annotated, err := NewLegacyAnnotationFS(projectmigrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, annotated, goose.WithTableName(projectMigrationTable))
	if err != nil {
		t.Fatal(err)
	}
	downEmptyKnowledgeMigration(t, ctx, provider)
	if _, err := provider.Down(ctx); err != nil {
		t.Fatalf("00016 Down rejected empty Embedding Hybrid Search schema: %v", err)
	}
	if _, err := provider.Down(ctx); err != nil {
		t.Fatalf("00015 Down rejected empty Reindex Consumer schema: %v", err)
	}
	_, err = provider.Down(ctx)
	var pgErr *pgconn.PgError
	if err == nil || !errors.As(err, &pgErr) || pgErr.Code != "55000" {
		t.Fatalf("00014 Down with Retrieval data error=%v", err)
	}
}

func TestEmbeddingHybridMigrationCacheAndDownGuards(t *testing.T) {
	t.Run("cache schema vector constraints and immutability", func(t *testing.T) {
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
		var tableCount, triggerCount, schemaMetaCount, v2ConstraintCount int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
			WHERE table_schema='retrieval' AND table_name='embedding_cache'`).Scan(&tableCount); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger
			WHERE tgname IN ('retrieval_embedding_cache_validate_insert','retrieval_embedding_cache_reject_mutation')`).Scan(&triggerCount); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM core.schema_meta
			WHERE key='embedding_hybrid_search' AND value='m6-c'`).Scan(&schemaMetaCount); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint
			WHERE conrelid='retrieval.reindex_delivery'::regclass
			  AND conname='reindex_delivery_regression_code_check'
			  AND pg_get_constraintdef(oid) LIKE '%SNAPSHOT_STRUCTURE_V2%'`).Scan(&v2ConstraintCount); err != nil {
			t.Fatal(err)
		}
		if tableCount != 1 || triggerCount != 2 || schemaMetaCount != 1 || v2ConstraintCount != 1 {
			t.Fatalf("table=%d triggers=%d meta=%d v2_constraint=%d", tableCount, triggerCount, schemaMetaCount, v2ConstraintCount)
		}

		if _, err := pool.Exec(ctx, `
INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at) VALUES
('91000000-0000-4000-8000-000000000001','cache-a','/tmp/cache-a','/tmp/cache-a',now(),'active',now(),now()),
('91000000-0000-4000-8000-000000000002','cache-b','/tmp/cache-b','/tmp/cache-b',now(),'inactive',now(),now());
INSERT INTO retrieval.embedding_version(
    id,provider,adapter_name,adapter_version,model,dimensions,normalization,distance_metric,config_hash,created_at
) VALUES
('92000000-0000-4000-8000-000000000001','test','direct','v1','l2-model',3,'l2','cosine',repeat('1',64),now()),
('92000000-0000-4000-8000-000000000002','test','direct','v1','none-model',3,'none','euclidean',repeat('2',64),now());
INSERT INTO retrieval.embedding_cache(workspace_id,embedding_version_id,content_hash,embedding,created_at) VALUES
('91000000-0000-4000-8000-000000000001','92000000-0000-4000-8000-000000000001',repeat('a',64),'[0.6,0.8,0]'::vector,now()),
('91000000-0000-4000-8000-000000000002','92000000-0000-4000-8000-000000000001',repeat('a',64),'[0.6,0.8,0]'::vector,now()),
('91000000-0000-4000-8000-000000000001','92000000-0000-4000-8000-000000000002',repeat('b',64),'[2,0,0]'::vector,now());`); err != nil {
			t.Fatal(err)
		}
		_, err = pool.Exec(ctx, `INSERT INTO retrieval.embedding_cache(workspace_id,embedding_version_id,content_hash,embedding,created_at)
			VALUES('91000000-0000-4000-8000-000000000001','92000000-0000-4000-8000-000000000001',repeat('a',64),'[0.6,0.8,0]'::vector,now())`)
		assertPostgresCode(t, err, "23505")
		for name, vectorValue := range map[string]string{
			"wrong dimensions": "[1,0]",
			"non unit l2":      "[1,1,0]",
			"zero norm":        "[0,0,0]",
		} {
			t.Run(name, func(t *testing.T) {
				_, err := pool.Exec(ctx, `INSERT INTO retrieval.embedding_cache(workspace_id,embedding_version_id,content_hash,embedding,created_at)
					VALUES('91000000-0000-4000-8000-000000000001','92000000-0000-4000-8000-000000000001',repeat($1,64),$2::vector,now())`,
					string(name[0]), vectorValue)
				assertPostgresCode(t, err, "23514")
			})
		}
		_, err = pool.Exec(ctx, `UPDATE retrieval.embedding_cache SET embedding='[1,0,0]'::vector
			WHERE workspace_id='91000000-0000-4000-8000-000000000001' AND content_hash=repeat('a',64)`)
		assertPostgresCode(t, err, "55000")
		_, err = pool.Exec(ctx, `DELETE FROM retrieval.embedding_cache
			WHERE workspace_id='91000000-0000-4000-8000-000000000001' AND content_hash=repeat('a',64)`)
		assertPostgresCode(t, err, "55000")
		provider := migrationProvider(t, pool)
		downEmptyKnowledgeMigration(t, ctx, provider)
		_, err = provider.Down(ctx)
		assertPostgresCode(t, err, "55000")
	})

	t.Run("hybrid index blocks down", func(t *testing.T) {
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
		if _, err := pool.Exec(ctx, `
INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at)
VALUES('93000000-0000-4000-8000-000000000001','hybrid','/tmp/hybrid','/tmp/hybrid',now(),'active',now(),now());
INSERT INTO retrieval.embedding_version(
    id,provider,adapter_name,adapter_version,model,dimensions,normalization,distance_metric,config_hash,created_at
) VALUES('93000000-0000-4000-8000-000000000002','test','direct','v1','hybrid-model',3,'l2','cosine',repeat('1',64),now());
INSERT INTO retrieval.index_version(
    id,workspace_id,embedding_version_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
    source_snapshot_ref,manifest_hash,expected_chunk_count,idempotency_key,status,degraded_capabilities,version,created_at,updated_at
) VALUES(
    '93000000-0000-4000-8000-000000000003','93000000-0000-4000-8000-000000000001','93000000-0000-4000-8000-000000000002',
    'simple','v1',repeat('2',64),'{}','hybrid:test',repeat('3',64),0,'hybrid-test','building','[]',1,now(),now()
);`); err != nil {
			t.Fatal(err)
		}
		provider := migrationProvider(t, pool)
		downEmptyKnowledgeMigration(t, ctx, provider)
		_, err = provider.Down(ctx)
		assertPostgresCode(t, err, "55000")
	})

	t.Run("v2 delivery blocks down", func(t *testing.T) {
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
		if _, err := pool.Exec(ctx, `
SET session_replication_role = replica;
INSERT INTO retrieval.reindex_delivery(
    id,consumer_name,outbox_event_id,workspace_id,writeback_execution_id,status,dispatch_no,attempt_no,version,
    source_version_id,parse_projection_id,index_version_id,excluded_source_count,
    regression_code,regression_hash,regression_passed_at,manual_recovery_required,created_at,updated_at
) VALUES(
    '94000000-0000-4000-8000-000000000001','test-consumer','94000000-0000-4000-8000-000000000002',
    '94000000-0000-4000-8000-000000000003','94000000-0000-4000-8000-000000000004','processing',1,1,1,
    '94000000-0000-4000-8000-000000000005','94000000-0000-4000-8000-000000000006',
    '94000000-0000-4000-8000-000000000007',0,'SNAPSHOT_STRUCTURE_V2',repeat('a',64),now(),false,now(),now()
);
SET session_replication_role = origin;`); err != nil {
			t.Fatal(err)
		}
		provider := migrationProvider(t, pool)
		downEmptyKnowledgeMigration(t, ctx, provider)
		_, err = provider.Down(ctx)
		assertPostgresCode(t, err, "55000")
	})
}

func TestRunnerReindexConsumerRejectsPartialProcessingContractTuple(t *testing.T) {
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
	workspaceID := "71000000-0000-4000-8000-000000000001"
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at
	) VALUES($1,'partial-contract','/tmp/partial-contract','/tmp/partial-contract',now(),'test',now(),now())`, workspaceID); err != nil {
		t.Fatal(err)
	}
	base := []any{strings.Repeat("2", 64), int64(1), "goldmark", "v1", strings.Repeat("3", 64), "structure-v1", "v1"}
	for missing := range base {
		values := append([]any(nil), base...)
		values[missing] = nil
		assertPartialProcessingContractRejected(t, ctx, pool, workspaceID, 720+missing, values)
	}
	processingOnly := append([]any(nil), base...)
	processingOnly[0], processingOnly[1] = nil, nil
	assertPartialProcessingContractRejected(t, ctx, pool, workspaceID, 730, processingOnly)
}

func assertPartialProcessingContractRejected(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID string, ordinal int, tuple []any) {
	t.Helper()
	_, err := pool.Exec(ctx, `INSERT INTO retrieval.index_version(
		id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
		source_snapshot_ref,manifest_hash,expected_chunk_count,source_manifest_hash,expected_source_count,
		source_parser_id,source_parser_version,source_parser_config_hash,source_chunk_strategy_version,source_schema_version,
		idempotency_key,status,degraded_capabilities,version,created_at,updated_at
	) VALUES($1,$2,'simple','v1',$3,'{}',$4,$5,0,$6,$7,$8,$9,$10,$11,$12,$13,'building','["vector"]',1,now(),now())`,
		fmt.Sprintf("72000000-0000-4000-8000-%012d", ordinal), workspaceID, strings.Repeat("1", 64),
		fmt.Sprintf("reindex-v1:partial-%d", ordinal), strings.Repeat("4", 64), tuple[0], tuple[1], tuple[2], tuple[3], tuple[4], tuple[5], tuple[6],
		fmt.Sprintf("partial-contract-%d", ordinal),
	)
	assertPostgresCode(t, err, "23514")
}

func TestRunnerReindexConsumerMigrationSchemaAndDownGuards(t *testing.T) {
	t.Run("source manifest", func(t *testing.T) {
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
		var tableCount, columnCount, constraintTriggerCount, writebackIndexCount, outboxIndexCount int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables WHERE table_schema='retrieval' AND table_name IN ('index_manifest_source','reindex_delivery','reindex_delivery_attempt')`).Scan(&tableCount); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns WHERE table_schema='retrieval' AND table_name='index_version' AND column_name IN (
			'source_manifest_hash','expected_source_count','source_parser_id','source_parser_version',
			'source_parser_config_hash','source_chunk_strategy_version','source_schema_version'
		)`).Scan(&columnCount); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger WHERE tgname IN ('reindex_delivery_verify_completion','writeback_execution_verify_reindex_completion','proposal_verify_reindex_completion') AND tgconstraint <> 0`).Scan(&constraintTriggerCount); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE schemaname='retrieval'
			AND tablename='reindex_delivery' AND indexname='idx_reindex_delivery_writeback_execution'`).Scan(&writebackIndexCount); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE schemaname='workflow'
			AND tablename='outbox_event' AND indexname='idx_workflow_outbox_reindex_unpublished'`).Scan(&outboxIndexCount); err != nil {
			t.Fatal(err)
		}
		if tableCount != 3 || columnCount != 7 || constraintTriggerCount != 3 || writebackIndexCount != 1 || outboxIndexCount != 1 {
			t.Fatalf("tables=%d columns=%d constraint_triggers=%d writeback_indexes=%d outbox_indexes=%d", tableCount, columnCount, constraintTriggerCount, writebackIndexCount, outboxIndexCount)
		}
		if _, err := pool.Exec(ctx, `
INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at)
VALUES('10000000-0000-4000-8000-000000000001','reindex','/tmp/reindex','/tmp/reindex',now(),'active',now(),now());
INSERT INTO core.source(id,workspace_id,type,logical_name,original_location,created_at)
VALUES('20000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000001','markdown','a','notes/a.md',now());
INSERT INTO retrieval.index_version(
    id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
    source_snapshot_ref,manifest_hash,expected_chunk_count,source_manifest_hash,expected_source_count,
    source_parser_id,source_parser_version,source_parser_config_hash,source_chunk_strategy_version,source_schema_version,
    idempotency_key,status,degraded_capabilities,version,created_at,updated_at
) VALUES(
    '30000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000001',
    'simple','v1',repeat('1',64),'{}','reindex-v1:event',repeat('2',64),0,repeat('3',64),1,
    'goldmark','v1',repeat('4',64),'structure-v1','v1',
    'reindex-index-1','building','["vector"]',1,now(),now()
);
INSERT INTO retrieval.index_manifest_source(
    index_version_id,workspace_id,source_id,selection_status,exclusion_code,created_at
) VALUES(
    '30000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000001',
    '20000000-0000-4000-8000-000000000001','excluded','NO_CURRENT_SUCCESSFUL_PROJECTION',now()
);`); err != nil {
			t.Fatal(err)
		}
		provider := migrationProvider(t, pool)
		downEmptyKnowledgeMigration(t, ctx, provider)
		if _, err := provider.Down(ctx); err != nil {
			t.Fatalf("00016 Down rejected V1 source manifest data: %v", err)
		}
		_, err = provider.Down(ctx)
		var pgErr *pgconn.PgError
		if err == nil || !errors.As(err, &pgErr) || pgErr.Code != "55000" {
			t.Fatalf("00015 Down with Source Manifest error=%v", err)
		}
	})

	t.Run("delivery", func(t *testing.T) {
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
		if _, err := pool.Exec(ctx, `
SET session_replication_role = replica;
INSERT INTO retrieval.reindex_delivery(
    id,consumer_name,outbox_event_id,workspace_id,writeback_execution_id,status,
    dispatch_no,attempt_no,version,manual_recovery_required,created_at,updated_at
) VALUES(
    '40000000-0000-4000-8000-000000000001','test-consumer',
    '50000000-0000-4000-8000-000000000001','60000000-0000-4000-8000-000000000001',
    '70000000-0000-4000-8000-000000000001','pending',0,0,1,false,now(),now()
);
SET session_replication_role = origin;`); err != nil {
			t.Fatal(err)
		}
		provider := migrationProvider(t, pool)
		downEmptyKnowledgeMigration(t, ctx, provider)
		if _, err := provider.Down(ctx); err != nil {
			t.Fatalf("00016 Down rejected V1 delivery data: %v", err)
		}
		_, err = provider.Down(ctx)
		var pgErr *pgconn.PgError
		if err == nil || !errors.As(err, &pgErr) || pgErr.Code != "55000" {
			t.Fatalf("00015 Down with Delivery error=%v", err)
		}
	})

	t.Run("orphan source-bound index", func(t *testing.T) {
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
		if _, err := pool.Exec(ctx, `
INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at)
VALUES('81000000-0000-4000-8000-000000000001','orphan','/tmp/orphan','/tmp/orphan',now(),'active',now(),now());
INSERT INTO retrieval.index_version(
    id,workspace_id,tokenizer_id,tokenizer_version,tokenizer_config_hash,fusion_config,
    source_snapshot_ref,manifest_hash,expected_chunk_count,source_manifest_hash,expected_source_count,
    source_parser_id,source_parser_version,source_parser_config_hash,source_chunk_strategy_version,source_schema_version,
    idempotency_key,status,degraded_capabilities,version,created_at,updated_at
) VALUES(
    '82000000-0000-4000-8000-000000000001','81000000-0000-4000-8000-000000000001',
    'simple','v1',repeat('1',64),'{}','reindex-v1:orphan',repeat('2',64),0,repeat('3',64),1,
    'goldmark','v1',repeat('4',64),'structure-v1','v1',
    'orphan-index','building','["vector"]',1,now(),now()
);`); err != nil {
			t.Fatal(err)
		}
		provider := migrationProvider(t, pool)
		downEmptyKnowledgeMigration(t, ctx, provider)
		if _, err := provider.Down(ctx); err != nil {
			t.Fatalf("00016 Down rejected V1 source-bound index: %v", err)
		}
		_, err = provider.Down(ctx)
		assertPostgresCode(t, err, "55000")
	})
}

func TestReindexConsumerMigrationRejectsDirectProposalCompletion(t *testing.T) {
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
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at)
VALUES('83000000-0000-4000-8000-000000000001','completion','/tmp/completion','/tmp/completion',now(),'active',now(),now());
INSERT INTO change_control.proposal(id,workspace_id,status,idempotency_key,request_hash,version,created_at,updated_at)
VALUES('84000000-0000-4000-8000-000000000001','83000000-0000-4000-8000-000000000001','completed','direct-completed',repeat('a',64),1,now(),now());`); err != nil {
		t.Fatal(err)
	}
	assertPostgresCode(t, tx.Commit(ctx), "55000")
}

func TestReindexConsumerMigrationEnforcesLeaseRetryAndAppendOnlyAttempts(t *testing.T) {
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
	deliveryID := "85000000-0000-4000-8000-000000000001"
	attemptID := "86000000-0000-4000-8000-000000000001"
	insertSyntheticReindexDelivery(t, ctx, pool, deliveryID)
	if _, err := pool.Exec(ctx, `INSERT INTO retrieval.reindex_delivery_attempt(
        id,delivery_id,attempt_no,dispatch_no,river_job_id,river_attempt,delivery_key,
        lease_owner,lease_until,status,started_at,heartbeat_at
    ) VALUES($1,$2,1,1,1,1,'delivery-1','worker-a',CURRENT_TIMESTAMP + INTERVAL '5 minutes','processing',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, attemptID, deliveryID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE retrieval.reindex_delivery SET status='processing',attempt_no=1,current_attempt_id=$2,version=2,updated_at=CURRENT_TIMESTAMP WHERE id=$1`, deliveryID, attemptID); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE retrieval.reindex_delivery_attempt SET lease_until=lease_until + INTERVAL '1 minute' WHERE id=$1`, attemptID)
	assertPostgresCode(t, err, "23514")
	_, err = pool.Exec(ctx, `UPDATE retrieval.reindex_delivery_attempt SET status='lease_lost',failure_class='retryable',error_kind='version_conflict',error_code='LEASE_LOST',error_summary='lease lost',ended_at=CURRENT_TIMESTAMP WHERE id=$1`, attemptID)
	assertPostgresCode(t, err, "23514")
	_, err = pool.Exec(ctx, `UPDATE retrieval.reindex_delivery_attempt SET status='retry_wait',failure_class='non_retryable',error_kind='invalid_input',error_code='BAD',error_summary='bad',ended_at=CURRENT_TIMESTAMP WHERE id=$1`, attemptID)
	assertPostgresCode(t, err, "23514")
	if _, err := pool.Exec(ctx, `UPDATE retrieval.reindex_delivery_attempt SET status='retry_wait',failure_class='retryable',error_kind='dependency_unavailable',error_code='RETRY',error_summary='retry',ended_at=CURRENT_TIMESTAMP WHERE id=$1`, attemptID); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE retrieval.reindex_delivery_attempt SET heartbeat_at=CURRENT_TIMESTAMP WHERE id=$1`, attemptID)
	assertPostgresCode(t, err, "55000")
	if _, err := pool.Exec(ctx, `UPDATE retrieval.reindex_delivery SET status='retry_wait',next_attempt_at=CURRENT_TIMESTAMP + INTERVAL '1 minute',failure_class='retryable',error_kind='dependency_unavailable',error_code='RETRY',error_summary='retry',version=3,updated_at=CURRENT_TIMESTAMP WHERE id=$1`, deliveryID); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE retrieval.reindex_delivery SET status='dispatched',next_attempt_at=NULL,failure_class=NULL,error_kind=NULL,error_code=NULL,error_summary=NULL,version=4,updated_at=CURRENT_TIMESTAMP WHERE id=$1`, deliveryID)
	assertPostgresCode(t, err, "23514")
	if _, err := pool.Exec(ctx, `UPDATE retrieval.reindex_delivery SET status='dispatched',dispatch_no=2,next_attempt_at=NULL,failure_class=NULL,error_kind=NULL,error_code=NULL,error_summary=NULL,version=4,updated_at=CURRENT_TIMESTAMP WHERE id=$1`, deliveryID); err != nil {
		t.Fatal(err)
	}

	secondDelivery := "85000000-0000-4000-8000-000000000002"
	secondAttempt := "86000000-0000-4000-8000-000000000002"
	insertSyntheticReindexDelivery(t, ctx, pool, secondDelivery)
	if _, err := pool.Exec(ctx, `INSERT INTO retrieval.reindex_delivery_attempt(
        id,delivery_id,attempt_no,dispatch_no,river_job_id,river_attempt,delivery_key,
        lease_owner,lease_until,status,started_at,heartbeat_at
    ) VALUES($1,$2,1,1,2,1,'delivery-2','worker-b',CURRENT_TIMESTAMP + INTERVAL '5 minutes','processing',CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, secondAttempt, secondDelivery); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE retrieval.reindex_delivery SET status='processing',attempt_no=1,current_attempt_id=$2,version=2,updated_at=CURRENT_TIMESTAMP WHERE id=$1`, secondDelivery, secondAttempt); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE retrieval.reindex_delivery_attempt SET status='succeeded',ended_at=CURRENT_TIMESTAMP WHERE id=$1`, secondAttempt)
	assertPostgresCode(t, err, "23514")

	thirdDelivery := "85000000-0000-4000-8000-000000000003"
	insertSyntheticReindexDelivery(t, ctx, pool, thirdDelivery)
	_, err = pool.Exec(ctx, `INSERT INTO retrieval.reindex_delivery_attempt(
        id,delivery_id,attempt_no,dispatch_no,river_job_id,river_attempt,delivery_key,
        lease_owner,lease_until,status,started_at,heartbeat_at
    ) VALUES('86000000-0000-4000-8000-000000000003',$1,1,1,3,1,'delivery-3','worker-c',CURRENT_TIMESTAMP - INTERVAL '1 second','processing',CURRENT_TIMESTAMP - INTERVAL '2 seconds',CURRENT_TIMESTAMP - INTERVAL '2 seconds')`, thirdDelivery)
	assertPostgresCode(t, err, "23514")
}

func insertSyntheticReindexDelivery(t *testing.T, ctx context.Context, pool *pgxpool.Pool, deliveryID string) {
	t.Helper()
	connection, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Release()
	if _, err := connection.Exec(ctx, `SET session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = connection.Exec(context.Background(), `SET session_replication_role = origin`) }()
	if _, err := connection.Exec(ctx, `INSERT INTO retrieval.reindex_delivery(
    id,consumer_name,outbox_event_id,workspace_id,writeback_execution_id,status,
    dispatch_no,attempt_no,version,manual_recovery_required,created_at,updated_at
) VALUES($1,'test-consumer',$2,$3,$4,'dispatched',1,0,1,false,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, deliveryID, randomFixtureUUID(deliveryID, '5'), randomFixtureUUID(deliveryID, '6'), randomFixtureUUID(deliveryID, '7')); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, `SET session_replication_role = origin`); err != nil {
		t.Fatal(err)
	}
}

func randomFixtureUUID(value string, replacement byte) string {
	result := []byte(value)
	result[0] = replacement
	return string(result)
}

func assertPostgresCode(t *testing.T, err error, code string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if err == nil || !errors.As(err, &pgErr) || pgErr.Code != code {
		t.Fatalf("postgres error=%v want code=%s", err, code)
	}
}

func migrationProvider(t *testing.T, pool *pgxpool.Pool) *goose.Provider {
	t.Helper()
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	annotated, err := NewLegacyAnnotationFS(projectmigrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, annotated, goose.WithTableName(projectMigrationTable))
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func downEmptyKnowledgeMigration(t *testing.T, ctx context.Context, provider *goose.Provider) {
	t.Helper()
	if _, err := provider.DownTo(ctx, 16); err != nil {
		t.Fatalf("00017 Down rejected empty Knowledge Domain schema: %v", err)
	}
}

func TestRunnerAdoptsLegacyShellHistory(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	applyLegacyShellMigrations(t, ctx, pool)
	runner, err := NewRunner(pool, projectmigrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}
	var maxVersion, applied int
	if err := pool.QueryRow(ctx, `SELECT max(version_id), count(*) FILTER (WHERE is_applied AND version_id > 0) FROM public.goose_db_version`).Scan(&maxVersion, &applied); err != nil {
		t.Fatal(err)
	}
	if maxVersion != 24 || applied != 24 {
		t.Fatalf("adopted history max=%d applied=%d", maxVersion, applied)
	}
}

func TestRunnerSerializesConcurrentUp(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	runner, err := NewRunner(pool, projectmigrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	errorsCh := make(chan error, 2)
	for range 2 {
		go func() { errorsCh <- runner.Up(ctx) }()
	}
	for range 2 {
		if err := <-errorsCh; err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunnerWorksWithSingleConnectionPool(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabaseWithMaxConns(t, ctx, 1)
	defer cleanup()
	runner, err := NewRunner(pool, projectmigrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := runner.Up(deadline); err != nil {
		t.Fatalf("single-connection migration failed: %v", err)
	}
}

func TestRawLegacyMigrationsFailGooseParsing(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, projectmigrations.FS, goose.WithTableName(projectMigrationTable))
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Up(ctx)
	var pgErr *pgconn.PgError
	if err == nil || !errors.As(err, &pgErr) || pgErr.Code != "42601" || !strings.Contains(err.Error(), "version:2") {
		t.Fatalf("raw legacy Goose error=%v", err)
	}
}

func newMigrationTestDatabase(t *testing.T, ctx context.Context) (*pgxpool.Pool, func()) {
	return newMigrationTestDatabaseWithMaxConns(t, ctx, 0)
}

func newMigrationTestDatabaseWithMaxConns(t *testing.T, ctx context.Context, maxConns int32) (*pgxpool.Pool, func()) {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Fatal("ZHIXU_TEST_DATABASE_URL is required for migration integration tests")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("zhixu_m4a_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	config, err := pgxpool.ParseConfig(parsed.String())
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier)
		admin.Close()
		t.Fatal(err)
	}
	if maxConns > 0 {
		config.MaxConns = maxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier)
		admin.Close()
		t.Fatal(err)
	}
	return pool, func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
	}
}

func applyLegacyShellMigrations(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	names, err := fs.Glob(projectmigrations.FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	for _, name := range names {
		if !isLegacyMigration(name) {
			continue
		}
		content, err := fs.ReadFile(projectmigrations.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		up := legacyUpSection(string(content))
		if _, err := pool.Exec(ctx, up); err != nil {
			t.Fatalf("apply legacy %s: %v", name, err)
		}
	}
}

func legacyUpSection(content string) string {
	start := strings.Index(content, "-- +goose Up")
	if start < 0 {
		return ""
	}
	content = content[start+len("-- +goose Up"):]
	if end := strings.Index(content, "-- +goose Down"); end >= 0 {
		content = content[:end]
	}
	return content
}

func insertRuntimeIdentityFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(ctx, `
INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at)
VALUES('10000000-0000-4000-8000-000000000001','m4a','/tmp/m4a','/tmp/m4a',now(),'active',now(),now());
INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
VALUES('20000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000001','m4a',1,'{}',now());
INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at,idempotency_key,request_hash)
VALUES('30000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001','pending','{}',1,now(),now(),'m4a-runtime',repeat('1',64));`)
	if err != nil {
		t.Fatal(err)
	}
}
