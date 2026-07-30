//go:build integration

package migration

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestM9AttachmentExportMigrationUpgradeRepeatAndCollectionOnlyDownUp(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 62); err != nil {
		t.Fatalf("migrate to 00062: %v", err)
	}

	const (
		workspaceID  = "e9300000-0000-4000-8000-000000000001"
		collectionID = "e9400000-0000-4000-8000-000000000001"
		exportID     = "e9500000-0000-4000-8000-000000000001"
	)
	now := time.Date(2026, 7, 29, 9, 0, 0, 0, time.UTC)
	queryHash := strings.Repeat("1", 64)
	insertExportHardeningWorkspace(t, ctx, pool, workspaceID, "attachment-migration-upgrade", "active", now)
	insertExportHardeningCollection(t, ctx, pool, collectionID, workspaceID, queryHash, now)
	if err := insertExportHardeningFact(ctx, pool, exportHardeningFact{
		ID: exportID, WorkspaceID: workspaceID, CollectionID: collectionID, QueryHash: queryHash,
		IdempotencyKey: "attachment-migration-upgrade", RequestHash: strings.Repeat("2", 64),
		TTLSeconds: 3600, CreatedAt: now,
	}); err != nil {
		t.Fatalf("insert pre-00063 Collection export: %v", err)
	}

	if _, err := provider.UpTo(ctx, 63); err != nil {
		t.Fatalf("00063 upgrade: %v", err)
	}
	if _, err := provider.UpTo(ctx, 63); err != nil {
		t.Fatalf("00063 repeated Up: %v", err)
	}
	assertM9AttachmentMigrationVersion(t, ctx, pool, 63)
	assertM9AttachmentMigrationShape(t, ctx, pool)
	var scopeKind string
	var rootVersion *string
	if err := pool.QueryRow(ctx, `SELECT scope_kind,attachment_root_contract_version FROM ops.export_job WHERE id=$1`, exportID).Scan(&scopeKind, &rootVersion); err != nil {
		t.Fatal(err)
	}
	if scopeKind != "COLLECTION" || rootVersion != nil {
		t.Fatalf("upgraded Collection scope=%q root=%v", scopeKind, rootVersion)
	}

	if _, err := provider.DownTo(ctx, 62); err != nil {
		t.Fatalf("00063 Collection-only Down: %v", err)
	}
	assertM9AttachmentMigrationVersion(t, ctx, pool, 62)
	assertM9AttachmentMigrationAbsent(t, ctx, pool)
	if _, err := provider.UpTo(ctx, 63); err != nil {
		t.Fatalf("00063 Up after Collection-only Down: %v", err)
	}
	assertM9AttachmentMigrationShape(t, ctx, pool)
}

func TestM9AttachmentExportMigrationConstraintsAndGuardedDown(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 63); err != nil {
		t.Fatal(err)
	}

	const (
		workspaceID  = "e9300000-0000-4000-8000-000000000002"
		collectionID = "e9400000-0000-4000-8000-000000000002"
		exportID     = "e9500000-0000-4000-8000-000000000002"
	)
	now := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	queryHash := strings.Repeat("3", 64)
	insertExportHardeningWorkspace(t, ctx, pool, workspaceID, "attachment-migration-guard", "active", now)
	insertExportHardeningCollection(t, ctx, pool, collectionID, workspaceID, queryHash, now)
	assertM9AttachmentNullBindingsRejected(t, ctx, pool, workspaceID, collectionID, now)

	err := insertM9AttachmentExportFact(ctx, pool, m9AttachmentExportFact{
		ID: "e9500000-0000-4000-8000-000000000003", WorkspaceID: workspaceID,
		Kind: "MARKDOWN", CollectionID: collectionID, RequestHash: strings.Repeat("4", 64), CreatedAt: now,
	})
	assertPostgresCode(t, err, "23514")

	if err := insertM9AttachmentExportFact(ctx, pool, m9AttachmentExportFact{
		ID: exportID, WorkspaceID: workspaceID, Kind: "ATTACHMENTS_ZIP",
		RequestHash: strings.Repeat("5", 64), CreatedAt: now,
	}); err != nil {
		t.Fatalf("insert valid attachment export: %v", err)
	}
	var enabled bool
	if err := pool.QueryRow(ctx, `SELECT enabled FROM ops.export_capability
		WHERE capability_key='workspace-attachments' AND contract_version='workspace-attachments/v1'`).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("attachment export capability was not disabled by default")
	}

	_, err = provider.DownTo(ctx, 62)
	var postgresError *pgconn.PgError
	if err == nil || !errors.As(err, &postgresError) || postgresError.Code != "55000" {
		t.Fatalf("00063 guarded Down error=%v", err)
	}
	assertM9AttachmentMigrationVersion(t, ctx, pool, 63)
	var rows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.export_job WHERE id=$1 AND scope_kind='WORKSPACE_ATTACHMENTS'`, exportID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Fatalf("guarded Down preserved attachment rows=%d want=1", rows)
	}
}

func assertM9AttachmentNullBindingsRejected(t *testing.T, ctx context.Context, pool *pgxpool.Pool, workspaceID, collectionID string, now time.Time) {
	t.Helper()
	for _, test := range []struct {
		name       string
		exportID   string
		scopeKind  string
		kind       string
		queryHash  *string
		root       *string
		prepared   bool
		manifest   *string
		fileHash   *string
		filePath   *string
		constraint string
	}{
		{name: "collection query hash", exportID: "e9500000-0000-4000-8000-000000000010", scopeKind: "COLLECTION", kind: "MARKDOWN", root: nil, constraint: "ops_export_job_scope_binding"},
		{name: "attachment root contract", exportID: "e9500000-0000-4000-8000-000000000011", scopeKind: "WORKSPACE_ATTACHMENTS", kind: "ATTACHMENTS_ZIP", root: nil, constraint: "ops_export_job_scope_binding"},
		{name: "prepared attachment manifest", exportID: "e9500000-0000-4000-8000-000000000012", scopeKind: "WORKSPACE_ATTACHMENTS", kind: "ATTACHMENTS_ZIP", root: m9StringPointer("workspace-attachments/v1"), prepared: true, manifest: nil, fileHash: m9StringPointer(strings.Repeat("a", 64)), filePath: m9StringPointer(".knowledge/exports/e9500000-0000-4000-8000-000000000012.zip"), constraint: "ops_export_job_prepared_binding"},
		{name: "prepared attachment file hash", exportID: "e9500000-0000-4000-8000-000000000013", scopeKind: "WORKSPACE_ATTACHMENTS", kind: "ATTACHMENTS_ZIP", root: m9StringPointer("workspace-attachments/v1"), prepared: true, manifest: m9StringPointer(strings.Repeat("b", 64)), fileHash: nil, filePath: m9StringPointer(".knowledge/exports/e9500000-0000-4000-8000-000000000013.zip"), constraint: "ops_export_job_prepared_binding"},
		{name: "prepared attachment file path", exportID: "e9500000-0000-4000-8000-000000000014", scopeKind: "WORKSPACE_ATTACHMENTS", kind: "ATTACHMENTS_ZIP", root: m9StringPointer("workspace-attachments/v1"), prepared: true, manifest: m9StringPointer(strings.Repeat("c", 64)), fileHash: m9StringPointer(strings.Repeat("d", 64)), filePath: nil, constraint: "ops_export_job_prepared_binding"},
	} {
		t.Run(test.name, func(t *testing.T) {
			queryHash := test.queryHash
			if test.scopeKind == "COLLECTION" && test.name != "collection query hash" {
				queryHash = m9StringPointer(strings.Repeat("3", 64))
			}
			var collection any
			var collectionVersion any
			var fields string
			var schemaVersion string
			var redaction string
			if test.scopeKind == "COLLECTION" {
				collection, collectionVersion, fields = collectionID, int64(1), `["title"]`
				schemaVersion, redaction = "export/v1", "MASKED"
			} else {
				fields, schemaVersion, redaction = `[]`, "attachment-export/v1", "RAW_USER_OWNED"
			}
			var preparedAt any
			var stagingPath any
			var entryCount any
			var totalBytes any
			var startedAt any
			var leaseOwner any
			var leaseExpiresAt any
			status := "PENDING"
			if test.prepared {
				status = "RUNNING"
				preparedAt = now
				stagingPath = ".knowledge/exports/.staging/" + test.exportID + "-" + strings.Repeat("e", 32) + ".zip.stage"
				entryCount, totalBytes = int64(1), int64(1)
				startedAt, leaseOwner, leaseExpiresAt = now, "m9-null-worker", now.Add(5*time.Minute)
			}
			_, err := pool.Exec(ctx, `INSERT INTO ops.export_job(
				id,workspace_id,scope_kind,kind,schema_version,query_definition,fields,status,
				idempotency_key,request_hash,request_ttl_seconds,version,expires_at,created_at,updated_at,
				collection_id,collection_version,query_hash,attachment_root_contract_version,
				redaction_policy,include_sensitive,permission_scope,requested_by,
				started_at,lease_owner,lease_expires_at,
				prepared_at,prepared_staging_path,file_path,file_hash,file_size,manifest_sha256,entry_count,total_uncompressed_bytes
			) VALUES(
				$1::uuid,$2::uuid,$3,$4,$5,jsonb_build_object('scope_kind',$3::text),$6::jsonb,$20,
				'm9-null-'||$1::text,repeat('f',64),3600,1,$7::timestamptz+interval '1 hour',$7,$7,
				$8::uuid,$9,$10,$11,$12,false,'READ_LOCAL','USER:migration-test',
				$21,$22,$23,
				$13,$14,$15,$16,CASE WHEN $13::timestamptz IS NULL THEN 0 ELSE 1 END,$17,$18,$19
			)`, test.exportID, workspaceID, test.scopeKind, test.kind, schemaVersion, fields, now,
				collection, collectionVersion, queryHash, test.root, redaction,
				preparedAt, stagingPath, test.filePath, test.fileHash, test.manifest, entryCount, totalBytes,
				status, startedAt, leaseOwner, leaseExpiresAt)
			assertM9PostgresConstraint(t, err, test.constraint)
		})
	}
}

func assertM9PostgresConstraint(t *testing.T, err error, constraint string) {
	t.Helper()
	var postgresError *pgconn.PgError
	if err == nil || !errors.As(err, &postgresError) || postgresError.Code != "23514" || postgresError.ConstraintName != constraint {
		t.Fatalf("PostgreSQL constraint error=%v code=%q constraint=%q want=%q", err, postgresErrorCode(postgresError), postgresErrorConstraint(postgresError), constraint)
	}
}

func postgresErrorCode(err *pgconn.PgError) string {
	if err == nil {
		return ""
	}
	return err.Code
}

func postgresErrorConstraint(err *pgconn.PgError) string {
	if err == nil {
		return ""
	}
	return err.ConstraintName
}

func m9StringPointer(value string) *string { return &value }

type m9AttachmentExportFact struct {
	ID           string
	WorkspaceID  string
	Kind         string
	CollectionID string
	RequestHash  string
	CreatedAt    time.Time
}

func insertM9AttachmentExportFact(ctx context.Context, pool *pgxpool.Pool, fact m9AttachmentExportFact) error {
	_, err := pool.Exec(ctx, `INSERT INTO ops.export_job(
		id,workspace_id,scope_kind,kind,schema_version,query_definition,fields,status,
		idempotency_key,request_hash,request_ttl_seconds,version,expires_at,created_at,updated_at,
		collection_id,collection_version,query_hash,attachment_root_contract_version,
		redaction_policy,include_sensitive,permission_scope,requested_by
	) VALUES(
		$1::uuid,$2::uuid,'WORKSPACE_ATTACHMENTS',$3::text,
		CASE WHEN $3='ATTACHMENTS_ZIP' THEN 'attachment-export/v1' ELSE 'export/v1' END,
		jsonb_build_object('scope_kind','WORKSPACE_ATTACHMENTS','attachment_root_contract_version','workspace-attachments/v1'),
		'[]'::jsonb,'PENDING','attachment-migration-'||$1::text,$5::text,3600,1,$6::timestamptz+interval '1 hour',$6::timestamptz,$6::timestamptz,
		NULLIF($4::text,'')::uuid,CASE WHEN $4::text='' THEN NULL ELSE 1 END,
		CASE WHEN $4::text='' THEN NULL ELSE repeat('3',64) END,'workspace-attachments/v1',
		'RAW_USER_OWNED',false,'READ_LOCAL','USER:migration-test'
	)`, fact.ID, fact.WorkspaceID, fact.Kind, fact.CollectionID, fact.RequestHash, fact.CreatedAt)
	return err
}

func assertM9AttachmentMigrationShape(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	for _, column := range []string{"scope_kind", "attachment_root_contract_version", "manifest_sha256", "entry_count", "total_uncompressed_bytes"} {
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
			WHERE table_schema='ops' AND table_name='export_job' AND column_name=$1`, column).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("00063 column %s count=%d", column, count)
		}
	}
	for name, fragments := range map[string][]string{
		"ops_export_job_supported_kind":        {"ATTACHMENTS_ZIP"},
		"ops_export_job_scope_binding":         {"WORKSPACE_ATTACHMENTS", "attachment_root_contract_version", "fields = '[]'"},
		"ops_export_job_counters":              {"WORKSPACE_ATTACHMENTS", "file_size <= 1073741824"},
		"ops_export_job_prepared_binding":      {"manifest_sha256", "entry_count", "total_uncompressed_bytes"},
		"ops_export_job_prepared_path_binding": {"zip", "zip\\.stage"},
	} {
		var definition string
		if err := pool.QueryRow(ctx, `SELECT pg_get_constraintdef(oid) FROM pg_constraint
			WHERE conrelid='ops.export_job'::regclass AND conname=$1 AND convalidated`, name).Scan(&definition); err != nil {
			t.Fatalf("read 00063 constraint %s: %v", name, err)
		}
		for _, fragment := range fragments {
			if !strings.Contains(definition, fragment) {
				t.Fatalf("00063 constraint %s definition=%q missing %q", name, definition, fragment)
			}
		}
	}
	var legacyKindConstraint, capabilityRows, attachmentIndexes int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint
		WHERE conrelid='ops.export_job'::regclass AND conname='export_job_kind_check'`).Scan(&legacyKindConstraint); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.export_capability
		WHERE capability_key='workspace-attachments' AND contract_version='workspace-attachments/v1' AND NOT enabled`).Scan(&capabilityRows); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes
		WHERE schemaname='ops' AND indexname='idx_ops_export_attachment'`).Scan(&attachmentIndexes); err != nil {
		t.Fatal(err)
	}
	if legacyKindConstraint != 0 || capabilityRows != 1 || attachmentIndexes != 1 {
		t.Fatalf("00063 shape legacy_kind=%d capability=%d attachment_index=%d", legacyKindConstraint, capabilityRows, attachmentIndexes)
	}
}

func assertM9AttachmentMigrationAbsent(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	var columns, capabilityTables, attachmentIndexes, legacyKinds int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema='ops' AND table_name='export_job'
		  AND column_name IN ('scope_kind','attachment_root_contract_version','manifest_sha256','entry_count','total_uncompressed_bytes')`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
		WHERE table_schema='ops' AND table_name='export_capability'`).Scan(&capabilityTables); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes
		WHERE schemaname='ops' AND indexname='idx_ops_export_attachment'`).Scan(&attachmentIndexes); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint
		WHERE conrelid='ops.export_job'::regclass AND conname='export_job_kind_check'`).Scan(&legacyKinds); err != nil {
		t.Fatal(err)
	}
	if columns != 0 || capabilityTables != 0 || attachmentIndexes != 0 || legacyKinds != 1 {
		t.Fatalf("00063 Down columns=%d capability=%d index=%d legacy_kind=%d", columns, capabilityTables, attachmentIndexes, legacyKinds)
	}
}

func assertM9AttachmentMigrationVersion(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want int64) {
	t.Helper()
	version, err := migrationProvider(t, pool).GetDBVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != want {
		t.Fatalf("migration version=%d want=%d", version, want)
	}
}
