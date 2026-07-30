//go:build integration

package migration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestExportHardeningMigrationUpRepeatSchemaAndEmptyDownUp(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)

	if _, err := provider.UpTo(ctx, 36); err != nil {
		t.Fatalf("00036 empty Up failed: %v", err)
	}
	if _, err := provider.UpTo(ctx, 36); err != nil {
		t.Fatalf("00036 repeated Up failed: %v", err)
	}
	assertExportHardeningMigrationVersion(t, ctx, pool, 36)
	assertExportHardeningMigrationShape(t, ctx, pool)

	if _, err := provider.DownTo(ctx, 35); err != nil {
		t.Fatalf("00036 empty Down failed: %v", err)
	}
	assertExportHardeningMigrationVersion(t, ctx, pool, 35)
	assertExportHardeningMigrationAbsent(t, ctx, pool)

	if _, err := provider.UpTo(ctx, 36); err != nil {
		t.Fatalf("00036 Up after empty Down failed: %v", err)
	}
	if _, err := provider.UpTo(ctx, 36); err != nil {
		t.Fatalf("00036 repeated Up after empty Down failed: %v", err)
	}
	assertExportHardeningMigrationVersion(t, ctx, pool, 36)
	assertExportHardeningMigrationShape(t, ctx, pool)
}

func TestExportHardeningMigrationConstraintsForeignKeyAndGuardedDown(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 36); err != nil {
		t.Fatal(err)
	}

	const (
		workspaceID       = "e9000000-0000-4000-8000-000000000001"
		otherWorkspaceID  = "e9000000-0000-4000-8000-000000000002"
		collectionID      = "e9100000-0000-4000-8000-000000000001"
		otherCollectionID = "e9100000-0000-4000-8000-000000000002"
		exportID          = "e9200000-0000-4000-8000-000000000001"
	)
	now := time.Date(2026, 7, 26, 9, 0, 0, 0, time.UTC)
	insertExportHardeningWorkspace(t, ctx, pool, workspaceID, "export-hardening", "active", now)
	insertExportHardeningWorkspace(t, ctx, pool, otherWorkspaceID, "export-hardening-other", "archived", now)
	insertExportHardeningCollection(t, ctx, pool, collectionID, workspaceID, strings.Repeat("1", 64), now)
	insertExportHardeningCollection(t, ctx, pool, otherCollectionID, otherWorkspaceID, strings.Repeat("2", 64), now)

	err := insertExportHardeningFact(ctx, pool, exportHardeningFact{
		ID:             "e9200000-0000-4000-8000-000000000002",
		WorkspaceID:    workspaceID,
		CollectionID:   collectionID,
		QueryHash:      strings.Repeat("1", 64),
		IdempotencyKey: "export-invalid-request-hash",
		RequestHash:    "not-a-sha256",
		TTLSeconds:     86400,
		CreatedAt:      now,
	})
	assertPostgresCode(t, err, "23514")

	err = insertExportHardeningFact(ctx, pool, exportHardeningFact{
		ID:             "e9200000-0000-4000-8000-000000000003",
		WorkspaceID:    workspaceID,
		CollectionID:   collectionID,
		QueryHash:      strings.Repeat("1", 64),
		IdempotencyKey: "export-invalid-ttl",
		RequestHash:    strings.Repeat("3", 64),
		TTLSeconds:     604801,
		CreatedAt:      now,
	})
	assertPostgresCode(t, err, "23514")

	err = insertExportHardeningFact(ctx, pool, exportHardeningFact{
		ID:             "e9200000-0000-4000-8000-000000000005",
		WorkspaceID:    workspaceID,
		CollectionID:   collectionID,
		QueryHash:      strings.Repeat("1", 64),
		IdempotencyKey: "export-invalid-ttl-lower-bound",
		RequestHash:    strings.Repeat("3", 64),
		TTLSeconds:     0,
		ExpiresAt:      now.Add(time.Hour),
		CreatedAt:      now,
	})
	assertPostgresCode(t, err, "23514")

	err = insertExportHardeningFact(ctx, pool, exportHardeningFact{
		ID:             "e9200000-0000-4000-8000-000000000004",
		WorkspaceID:    workspaceID,
		CollectionID:   otherCollectionID,
		QueryHash:      strings.Repeat("2", 64),
		IdempotencyKey: "export-cross-workspace",
		RequestHash:    strings.Repeat("4", 64),
		TTLSeconds:     86400,
		CreatedAt:      now,
	})
	assertPostgresCode(t, err, "23503")

	err = insertExportHardeningFact(ctx, pool, exportHardeningFact{
		ID:              "e9200000-0000-4000-8000-000000000006",
		WorkspaceID:     workspaceID,
		CollectionID:    collectionID,
		QueryHash:       strings.Repeat("1", 64),
		IdempotencyKey:  "export-full-without-sensitive",
		RequestHash:     strings.Repeat("6", 64),
		TTLSeconds:      86400,
		RedactionPolicy: "FULL",
		CreatedAt:       now,
	})
	assertPostgresCode(t, err, "23514")

	err = insertExportHardeningFact(ctx, pool, exportHardeningFact{
		ID:               "e9200000-0000-4000-8000-000000000007",
		WorkspaceID:      workspaceID,
		CollectionID:     collectionID,
		QueryHash:        strings.Repeat("1", 64),
		IdempotencyKey:   "export-sensitive-without-full",
		RequestHash:      strings.Repeat("7", 64),
		TTLSeconds:       86400,
		RedactionPolicy:  "MASKED",
		IncludeSensitive: true,
		CreatedAt:        now,
	})
	assertPostgresCode(t, err, "23514")

	if err := insertExportHardeningFact(ctx, pool, exportHardeningFact{
		ID:             exportID,
		WorkspaceID:    workspaceID,
		CollectionID:   collectionID,
		QueryHash:      strings.Repeat("1", 64),
		IdempotencyKey: "export-history",
		RequestHash:    strings.Repeat("5", 64),
		TTLSeconds:     86400,
		CreatedAt:      now,
	}); err != nil {
		t.Fatalf("insert valid Export fact: %v", err)
	}
	var requestHash, cleanupStatus string
	var requestTTL, version int64
	var cleanupAttempts int
	if err := pool.QueryRow(ctx, `SELECT request_hash,request_ttl_seconds,version,
		cleanup_status,cleanup_attempt_count
		FROM ops.export_job WHERE id=$1`, exportID).Scan(
		&requestHash, &requestTTL, &version, &cleanupStatus, &cleanupAttempts,
	); err != nil {
		t.Fatal(err)
	}
	if requestHash != strings.Repeat("5", 64) || requestTTL != 86400 || version != 1 ||
		cleanupStatus != "NOT_REQUIRED" || cleanupAttempts != 0 {
		t.Fatalf("Export request/cleanup defaults hash=%q ttl=%d version=%d cleanup=%s attempts=%d",
			requestHash, requestTTL, version, cleanupStatus, cleanupAttempts)
	}
	claimedAt := now.Add(time.Minute)
	leaseExpiresAt := claimedAt.Add(time.Hour)
	if _, err := pool.Exec(ctx, `UPDATE ops.export_job SET
		status='RUNNING',version=version+1,attempt_count=attempt_count+1,
		lease_owner='migration-worker',lease_expires_at=$2,started_at=$3,updated_at=$3
		WHERE id=$1`, exportID, leaseExpiresAt, claimedAt); err != nil {
		t.Fatalf("claim valid Export fact: %v", err)
	}

	stagingPath := ".knowledge/exports/.staging/" + exportID + "-" + strings.Repeat("7", 32) + ".md.stage"
	finalPath := ".knowledge/exports/" + exportID + ".md"
	for _, testCase := range []struct {
		name      string
		statement string
		arguments []any
	}{
		{
			name:      "download count requires timestamp",
			statement: `UPDATE ops.export_job SET version=version+1,download_count=1 WHERE id=$1`,
			arguments: []any{exportID},
		},
		{
			name:      "download timestamp requires count",
			statement: `UPDATE ops.export_job SET version=version+1,last_downloaded_at=$2 WHERE id=$1`,
			arguments: []any{exportID, now},
		},
		{
			name:      "snapshot revision requires complete prepared binding",
			statement: `UPDATE ops.export_job SET version=version+1,read_model_revision=repeat('6',64) WHERE id=$1`,
			arguments: []any{exportID},
		},
		{
			name:      "snapshot count requires complete prepared binding",
			statement: `UPDATE ops.export_job SET version=version+1,exact_count=1 WHERE id=$1`,
			arguments: []any{exportID},
		},
		{
			name:      "staging path requires complete prepared binding",
			statement: `UPDATE ops.export_job SET version=version+1,prepared_staging_path=$2 WHERE id=$1`,
			arguments: []any{exportID, stagingPath},
		},
		{
			name: "pending status rejects prepared result",
			statement: `UPDATE ops.export_job SET
				status='PENDING',version=version+1,lease_owner=NULL,lease_expires_at=NULL,
				read_model_revision=repeat('6',64),exact_count=1,prepared_at=$2,
				prepared_staging_path=$3,file_path=$4,file_hash=repeat('8',64),file_size=1
				WHERE id=$1`,
			arguments: []any{exportID, now, stagingPath, finalPath},
		},
		{
			name: "pending status rejects completion timestamp",
			statement: `UPDATE ops.export_job SET status='PENDING',version=version+1,
				lease_owner=NULL,lease_expires_at=NULL,completed_at=$2 WHERE id=$1`,
			arguments: []any{exportID, now},
		},
		{
			name:      "running status requires start timestamp",
			statement: `UPDATE ops.export_job SET version=version+1,started_at=NULL WHERE id=$1`,
			arguments: []any{exportID},
		},
		{
			name: "succeeded status requires complete prepared result",
			statement: `UPDATE ops.export_job SET status='SUCCEEDED',version=version+1,
				completed_at=$2,lease_owner=NULL,lease_expires_at=NULL,
				file_path=$3,file_hash=repeat('8',64),file_size=1 WHERE id=$1`,
			arguments: []any{exportID, now, finalPath},
		},
		{
			name: "failed status requires complete error",
			statement: `UPDATE ops.export_job SET status='FAILED',version=version+1,
				completed_at=$2,lease_owner=NULL,lease_expires_at=NULL,error_code='EXPORT_FAILED'
				WHERE id=$1`,
			arguments: []any{exportID, now},
		},
		{
			name:      "active status rejects pending cleanup",
			statement: `UPDATE ops.export_job SET version=version+1,cleanup_status='PENDING' WHERE id=$1`,
			arguments: []any{exportID},
		},
		{
			name: "expired status requires cleanup",
			statement: `UPDATE ops.export_job SET status='EXPIRED',version=version+1,
				completed_at=$2,lease_owner=NULL,lease_expires_at=NULL
				WHERE id=$1`,
			arguments: []any{exportID, now},
		},
		{
			name: "expired status rejects failure facts",
			statement: `UPDATE ops.export_job SET status='EXPIRED',version=version+1,
				completed_at=$2,lease_owner=NULL,lease_expires_at=NULL,
				cleanup_status='PENDING',error_code='EXPORT_FAILED',error_message='failed'
				WHERE id=$1`,
			arguments: []any{exportID, now},
		},
		{
			name: "pending cleanup rejects attempt facts",
			statement: `UPDATE ops.export_job SET status='EXPIRED',version=version+1,
				completed_at=$2,lease_owner=NULL,lease_expires_at=NULL,
				cleanup_status='PENDING',cleanup_attempt_count=1 WHERE id=$1`,
			arguments: []any{exportID, now},
		},
		{
			name: "failed cleanup requires error",
			statement: `UPDATE ops.export_job SET status='EXPIRED',version=version+1,
				completed_at=$2,lease_owner=NULL,lease_expires_at=NULL,
				cleanup_status='FAILED',cleanup_attempt_count=1,cleanup_updated_at=$2
				WHERE id=$1`,
			arguments: []any{exportID, now},
		},
		{
			name: "successful cleanup requires deletion timestamp",
			statement: `UPDATE ops.export_job SET status='EXPIRED',version=version+1,
				completed_at=$2,lease_owner=NULL,lease_expires_at=NULL,
				cleanup_status='SUCCEEDED',cleanup_attempt_count=1,cleanup_updated_at=$2
				WHERE id=$1`,
			arguments: []any{exportID, now},
		},
		{
			name: "cleanup error belongs only to failed cleanup",
			statement: `UPDATE ops.export_job SET version=version+1,
				cleanup_error='unexpected cleanup error' WHERE id=$1`,
			arguments: []any{exportID},
		},
		{
			name:      "file deletion belongs only to successful cleanup",
			statement: `UPDATE ops.export_job SET version=version+1,file_deleted_at=$2 WHERE id=$1`,
			arguments: []any{exportID, now},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, testCase.statement, testCase.arguments...)
			assertPostgresCode(t, err, "23514")
		})
	}

	_, err = pool.Exec(ctx, `UPDATE ops.export_job
		SET version=version+1,request_hash=repeat('9',64) WHERE id=$1`, exportID)
	assertPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `UPDATE ops.export_job SET version=version+2 WHERE id=$1`, exportID)
	assertPostgresCode(t, err, "55000")

	_, err = provider.DownTo(ctx, 35)
	assertPostgresCode(t, err, "55000")
	assertExportHardeningMigrationVersion(t, ctx, pool, 36)
	assertExportHardeningMigrationShape(t, ctx, pool)
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.export_job WHERE id=$1`, exportID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("00036 guarded Down preserved Export rows=%d want=1", rows)
	}
}

func assertExportHardeningMigrationShape(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	for _, column := range []struct {
		name     string
		dataType string
		nullable bool
	}{
		{name: "collection_id", dataType: "uuid", nullable: true},
		{name: "collection_version", dataType: "bigint", nullable: true},
		{name: "query_hash", dataType: "text", nullable: true},
		{name: "redaction_policy", dataType: "text"},
		{name: "include_sensitive", dataType: "boolean"},
		{name: "permission_scope", dataType: "text"},
		{name: "requested_by", dataType: "text"},
		{name: "file_size", dataType: "bigint"},
		{name: "error_message", dataType: "text", nullable: true},
		{name: "attempt_count", dataType: "integer"},
		{name: "lease_owner", dataType: "text", nullable: true},
		{name: "lease_expires_at", dataType: "timestamp with time zone", nullable: true},
		{name: "started_at", dataType: "timestamp with time zone", nullable: true},
		{name: "download_count", dataType: "integer"},
		{name: "last_downloaded_at", dataType: "timestamp with time zone", nullable: true},
		{name: "expires_at", dataType: "timestamp with time zone"},
		{name: "request_hash", dataType: "text"},
		{name: "request_ttl_seconds", dataType: "bigint"},
		{name: "version", dataType: "bigint"},
		{name: "read_model_revision", dataType: "text", nullable: true},
		{name: "exact_count", dataType: "bigint", nullable: true},
		{name: "prepared_at", dataType: "timestamp with time zone", nullable: true},
		{name: "prepared_staging_path", dataType: "text", nullable: true},
		{name: "cleanup_status", dataType: "text"},
		{name: "cleanup_attempt_count", dataType: "integer"},
		{name: "cleanup_error", dataType: "text", nullable: true},
		{name: "cleanup_updated_at", dataType: "timestamp with time zone", nullable: true},
		{name: "file_deleted_at", dataType: "timestamp with time zone", nullable: true},
	} {
		assertExportHardeningColumn(t, ctx, pool, column.name, column.dataType, column.nullable)
	}

	for _, constraint := range []struct {
		name      string
		kind      string
		fragments []string
	}{
		{name: "ops_export_job_redaction_policy", kind: "c"},
		{name: "ops_export_job_sensitive_policy", kind: "c", fragments: []string{"redaction_policy", "FULL", "include_sensitive"}},
		{name: "ops_export_job_request_hash", kind: "c", fragments: []string{"request_hash", "[0-9a-f]{64}"}},
		{name: "ops_export_job_request_ttl", kind: "c", fragments: []string{"request_ttl_seconds", "604800", "expires_at", "make_interval"}},
		{name: "ops_export_job_version", kind: "c", fragments: []string{"version > 0"}},
		{name: "ops_export_job_scope_binding", kind: "c", fragments: []string{"collection_id IS NOT NULL", "collection_version > 0", "query_hash"}},
		{name: "ops_export_job_collection_workspace", kind: "f", fragments: []string{"FOREIGN KEY (collection_id, workspace_id)", "REFERENCES learning.smart_collection(id, workspace_id) ON DELETE RESTRICT"}},
		{name: "ops_export_job_counters", kind: "c", fragments: []string{"attempt_count", "download_count", "cleanup_attempt_count", "exact_count", "10000"}},
		{name: "ops_export_job_prepared_binding", kind: "c", fragments: []string{"read_model_revision", "exact_count", "prepared_staging_path", "file_path", "file_hash", "file_size"}},
		{name: "ops_export_job_prepared_path_binding", kind: "c", fragments: []string{"prepared_staging_path", "knowledge/exports", "id)::text", "md\\.stage", "json\\.stage"}},
		{name: "ops_export_job_lifecycle_binding", kind: "c", fragments: []string{"PENDING", "RUNNING", "SUCCEEDED", "FAILED", "EXPIRED", "CANCELLED", "lease_owner", "error_message"}},
		{name: "ops_export_job_cleanup_binding", kind: "c", fragments: []string{"NOT_REQUIRED", "PENDING", "FAILED", "SUCCEEDED", "cleanup_attempt_count", "file_deleted_at = cleanup_updated_at"}},
		{name: "ops_export_job_download_binding", kind: "c", fragments: []string{"download_count", "last_downloaded_at", "SUCCEEDED", "EXPIRED"}},
		{name: "ops_export_job_time_order", kind: "c", fragments: []string{"updated_at >= created_at", "prepared_at", "lease_expires_at", "cleanup_updated_at", "file_deleted_at"}},
		{name: "ops_export_job_text_bounds", kind: "c", fragments: []string{"octet_length(idempotency_key)", "octet_length(error_message)", "octet_length(cleanup_error)"}},
	} {
		var kind, definition string
		var validated bool
		if err := pool.QueryRow(ctx, `SELECT contype::text,convalidated,pg_get_constraintdef(oid,false)
			FROM pg_constraint
			WHERE conrelid='ops.export_job'::regclass
			  AND conname=$1`, constraint.name).Scan(&kind, &validated, &definition); err != nil {
			t.Fatalf("read 00036 constraint %s: %v", constraint.name, err)
		}
		definition = strings.Join(strings.Fields(definition), " ")
		if kind != constraint.kind || !validated {
			t.Fatalf("00036 constraint %s kind=%s validated=%v definition=%q", constraint.name, kind, validated, definition)
		}
		for _, fragment := range constraint.fragments {
			if !strings.Contains(definition, fragment) {
				t.Fatalf("00036 constraint %s definition=%q missing %q", constraint.name, definition, fragment)
			}
		}
	}

	for _, index := range []struct {
		name               string
		unique             bool
		shape              string
		predicateFragments []string
	}{
		{name: "idx_ops_export_pending_recovery", shape: "(status, lease_expires_at, created_at, id)", predicateFragments: []string{"PENDING", "RUNNING"}},
		{name: "idx_ops_export_collection", shape: "(workspace_id, collection_id, created_at DESC, id DESC)"},
		{name: "idx_ops_export_expiry_candidate", shape: "(expires_at, id)", predicateFragments: []string{"PENDING", "RUNNING", "SUCCEEDED", "FAILED"}},
		{name: "idx_ops_export_cleanup_candidate", shape: "(cleanup_status, cleanup_updated_at, updated_at, id)", predicateFragments: []string{"EXPIRED", "file_deleted_at IS NULL"}},
		{name: "uq_ops_export_prepared_staging", unique: true, shape: "(workspace_id, prepared_staging_path)", predicateFragments: []string{"prepared_staging_path IS NOT NULL"}},
	} {
		var valid, ready, unique bool
		var definition, predicate string
		if err := pool.QueryRow(ctx, `SELECT index_row.indisvalid,index_row.indisready,index_row.indisunique,
			pg_get_indexdef(index_row.indexrelid),COALESCE(pg_get_expr(index_row.indpred,index_row.indrelid),'')
			FROM pg_index index_row
			WHERE index_row.indexrelid=$1::regclass`, "ops."+index.name).Scan(&valid, &ready, &unique, &definition, &predicate); err != nil {
			t.Fatalf("read 00036 index %s: %v", index.name, err)
		}
		if !valid || !ready || unique != index.unique || !strings.Contains(definition, index.shape) {
			t.Fatalf("00036 index %s valid=%v ready=%v unique=%v definition=%q", index.name, valid, ready, unique, definition)
		}
		for _, fragment := range index.predicateFragments {
			if !strings.Contains(predicate, fragment) {
				t.Fatalf("00036 index %s predicate=%q missing %q", index.name, predicate, fragment)
			}
		}
	}

	var triggerEnabled, functionDefinition string
	if err := pool.QueryRow(ctx, `SELECT export_trigger.tgenabled::text,pg_get_functiondef(export_function.oid)
		FROM pg_trigger export_trigger
		JOIN pg_proc export_function ON export_function.oid=export_trigger.tgfoid
		WHERE export_trigger.tgrelid='ops.export_job'::regclass
		  AND export_trigger.tgname='trg_ops_export_job_update'
		  AND NOT export_trigger.tgisinternal`).Scan(&triggerEnabled, &functionDefinition); err != nil {
		t.Fatalf("read 00036 update trigger: %v", err)
	}
	if triggerEnabled != "O" || !strings.Contains(functionDefinition, "export job version must advance by one") ||
		!strings.Contains(functionDefinition, "export request binding is immutable") ||
		!strings.Contains(functionDefinition, "export prepared result binding is immutable") {
		t.Fatalf("00036 update trigger enabled=%q function=%q", triggerEnabled, functionDefinition)
	}
}

func assertExportHardeningMigrationAbsent(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var columns, constraints, indexes, triggers, functions, baseConstraint int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema='ops' AND table_name='export_job'
		  AND column_name = ANY($1)`, []string{
		"collection_id", "collection_version", "query_hash", "request_hash", "request_ttl_seconds",
		"redaction_policy", "include_sensitive", "permission_scope", "requested_by", "file_size",
		"error_message", "attempt_count", "lease_owner", "lease_expires_at", "started_at",
		"download_count", "last_downloaded_at",
		"version", "read_model_revision", "exact_count", "prepared_at", "prepared_staging_path",
		"cleanup_status", "cleanup_attempt_count", "cleanup_error", "cleanup_updated_at", "file_deleted_at",
	}).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint
		WHERE conrelid='ops.export_job'::regclass
		  AND conname = ANY($1)`, []string{
		"ops_export_job_redaction_policy", "ops_export_job_sensitive_policy", "ops_export_job_request_hash",
		"ops_export_job_request_ttl", "ops_export_job_version", "ops_export_job_scope_binding",
		"ops_export_job_collection_workspace", "ops_export_job_counters", "ops_export_job_prepared_binding",
		"ops_export_job_prepared_path_binding", "ops_export_job_lifecycle_binding", "ops_export_job_cleanup_binding",
		"ops_export_job_download_binding", "ops_export_job_time_order", "ops_export_job_text_bounds",
	}).Scan(&constraints); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes
		WHERE schemaname='ops' AND indexname = ANY($1)`, []string{
		"idx_ops_export_pending_recovery", "idx_ops_export_collection", "idx_ops_export_expiry_candidate",
		"idx_ops_export_cleanup_candidate", "uq_ops_export_prepared_staging",
	}).Scan(&indexes); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger
		WHERE tgrelid='ops.export_job'::regclass AND tgname='trg_ops_export_job_update' AND NOT tgisinternal`).Scan(&triggers); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_proc
		WHERE pronamespace='ops'::regnamespace AND proname='enforce_export_job_update'`).Scan(&functions); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint
		WHERE conrelid='ops.export_job'::regclass AND conname='ops_export_job_success_binding' AND convalidated`).Scan(&baseConstraint); err != nil {
		t.Fatal(err)
	}
	if columns != 0 || constraints != 0 || indexes != 0 || triggers != 0 || functions != 0 || baseConstraint != 1 {
		t.Fatalf("00036 Down columns=%d constraints=%d indexes=%d triggers=%d functions=%d base_constraint=%d",
			columns, constraints, indexes, triggers, functions, baseConstraint)
	}
	var expiresNullable string
	if err := pool.QueryRow(ctx, `SELECT is_nullable FROM information_schema.columns
		WHERE table_schema='ops' AND table_name='export_job' AND column_name='expires_at'`).Scan(&expiresNullable); err != nil {
		t.Fatal(err)
	}
	if expiresNullable != "YES" {
		t.Fatalf("00036 Down expires_at nullable=%s want=YES", expiresNullable)
	}
}

func assertExportHardeningColumn(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name, dataType string, nullable bool) {
	t.Helper()
	var gotType, gotNullable string
	if err := pool.QueryRow(ctx, `SELECT data_type,is_nullable FROM information_schema.columns
		WHERE table_schema='ops' AND table_name='export_job' AND column_name=$1`, name).Scan(&gotType, &gotNullable); err != nil {
		t.Fatalf("read 00036 column %s: %v", name, err)
	}
	wantNullable := "NO"
	if nullable {
		wantNullable = "YES"
	}
	if gotType != dataType || gotNullable != wantNullable {
		t.Fatalf("00036 column %s type=%s nullable=%s want type=%s nullable=%s", name, gotType, gotNullable, dataType, wantNullable)
	}
}

func assertExportHardeningMigrationVersion(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want int64) {
	t.Helper()
	version, err := migrationProvider(t, pool).GetDBVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != want {
		t.Fatalf("migration version=%d want=%d", version, want)
	}
}

func insertExportHardeningWorkspace(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, label, status string, at time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at
	) VALUES($1,$2,$3,$3,$4,$5,$4,$4)`, workspaceID, label, "/tmp/"+label, at, status); err != nil {
		t.Fatal(err)
	}
}

func insertExportHardeningCollection(t *testing.T, ctx context.Context, pool *pgxpool.Pool, collectionID, workspaceID, queryHash string, at time.Time) {
	t.Helper()
	if _, err := pool.Exec(ctx, `INSERT INTO learning.smart_collection(
		id,workspace_id,name,normalized_name,description,query_schema_version,query_version,
		query_definition,query_hash,view_type,view_config,status,version,created_at,updated_at
		) VALUES($1,$2,$1::uuid::text,$1::uuid::text,'','collection-query/v1',1,
		'{"root":{"kind":"group","operator":"AND","clauses":[]},"sort":[]}'::jsonb,
		$3,'LIST','{}'::jsonb,'ACTIVE',1,$4,$4)`, collectionID, workspaceID, queryHash, at); err != nil {
		t.Fatal(err)
	}
}

type exportHardeningFact struct {
	ID               string
	WorkspaceID      string
	CollectionID     string
	QueryHash        string
	IdempotencyKey   string
	RequestHash      string
	TTLSeconds       int64
	RedactionPolicy  string
	IncludeSensitive bool
	ExpiresAt        time.Time
	CreatedAt        time.Time
}

func insertExportHardeningFact(ctx context.Context, pool *pgxpool.Pool, fact exportHardeningFact) error {
	redactionPolicy := fact.RedactionPolicy
	if redactionPolicy == "" {
		redactionPolicy = "MASKED"
	}
	expiresAt := fact.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = fact.CreatedAt.Add(time.Duration(fact.TTLSeconds) * time.Second)
	}
	_, err := pool.Exec(ctx, `INSERT INTO ops.export_job(
		id,workspace_id,kind,schema_version,query_definition,fields,status,
		idempotency_key,request_hash,request_ttl_seconds,version,expires_at,created_at,updated_at,
		collection_id,collection_version,query_hash,redaction_policy,include_sensitive,
		permission_scope,requested_by
	) VALUES(
		$1,$2,'MARKDOWN','export/v1',
		jsonb_build_object('collection_id',$3::uuid,'collection_version',1,'query_hash',$4::text),
		'["id","title"]'::jsonb,'PENDING',$5,$6,$7,1,$8,$9,$9,
		$3::uuid,1,$4::text,$10,$11,'READ_LOCAL','USER:migration-test'
	)`, fact.ID, fact.WorkspaceID, fact.CollectionID, fact.QueryHash, fact.IdempotencyKey,
		fact.RequestHash, fact.TTLSeconds, expiresAt, fact.CreatedAt,
		redactionPolicy, fact.IncludeSensitive)
	return err
}
