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
	requestHash := strings.Repeat("2", 64)
	insertExportHardeningWorkspace(t, ctx, pool, workspaceID, "attachment-migration-upgrade", "active", now)
	insertExportHardeningCollection(t, ctx, pool, collectionID, workspaceID, queryHash, now)
	if err := insertExportHardeningFact(ctx, pool, exportHardeningFact{
		ID: exportID, WorkspaceID: workspaceID, CollectionID: collectionID, QueryHash: queryHash,
		IdempotencyKey: "attachment-migration-upgrade", RequestHash: requestHash,
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
	var scopeKind, storedRequestHash, status string
	var rootVersion *string
	var requestTTL, version int64
	if err := pool.QueryRow(ctx, `SELECT scope_kind,attachment_root_contract_version,
		request_hash,request_ttl_seconds,version,status FROM ops.export_job WHERE id=$1`, exportID).Scan(
		&scopeKind, &rootVersion, &storedRequestHash, &requestTTL, &version, &status,
	); err != nil {
		t.Fatal(err)
	}
	if scopeKind != "COLLECTION" || rootVersion != nil || storedRequestHash != requestHash ||
		requestTTL != 3600 || version != 1 || status != "PENDING" {
		t.Fatalf("upgraded Collection scope=%q root=%v request=%q ttl=%d version=%d status=%q",
			scopeKind, rootVersion, storedRequestHash, requestTTL, version, status)
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

func TestM9AttachmentExportMigrationRepairsLegacyRecordedVersion62Shape(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 62); err != nil {
		t.Fatalf("migrate to 00062: %v", err)
	}

	const (
		workspaceID      = "e9300000-0000-4000-8000-000000000020"
		collectionID     = "e9400000-0000-4000-8000-000000000020"
		pendingExportID  = "e9500000-0000-4000-8000-000000000020"
		completeExportID = "e9500000-0000-4000-8000-000000000021"
		attachmentID     = "e9500000-0000-4000-8000-000000000022"
	)
	now := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)
	queryHash := strings.Repeat("6", 64)
	insertExportHardeningWorkspace(t, ctx, pool, workspaceID, "attachment-legacy-62", "active", now)
	insertExportHardeningCollection(t, ctx, pool, collectionID, workspaceID, queryHash, now)
	for _, fact := range []exportHardeningFact{
		{ID: pendingExportID, WorkspaceID: workspaceID, CollectionID: collectionID, QueryHash: queryHash,
			IdempotencyKey: "attachment-legacy-pending", RequestHash: strings.Repeat("7", 64), TTLSeconds: 3600, CreatedAt: now},
		{ID: completeExportID, WorkspaceID: workspaceID, CollectionID: collectionID, QueryHash: queryHash,
			IdempotencyKey: "attachment-legacy-complete", RequestHash: strings.Repeat("8", 64), TTLSeconds: 3600, CreatedAt: now},
	} {
		if err := insertExportHardeningFact(ctx, pool, fact); err != nil {
			t.Fatalf("insert export %s: %v", fact.ID, err)
		}
	}
	completeM9CollectionExport(t, ctx, pool, completeExportID, now)
	downgradeM9ExportJobToLegacyRecorded62(t, ctx, pool)

	if _, err := provider.UpTo(ctx, 63); err != nil {
		t.Fatalf("upgrade production-shaped 00062: %v", err)
	}
	if _, err := provider.UpTo(ctx, 63); err != nil {
		t.Fatalf("repeat production-shaped 00062 upgrade: %v", err)
	}
	assertM9AttachmentMigrationVersion(t, ctx, pool, 63)
	assertM9AttachmentMigrationShape(t, ctx, pool)

	var requestHash, cleanupStatus, scopeKind string
	var requestTTL, version int64
	if err := pool.QueryRow(ctx, `SELECT request_hash,request_ttl_seconds,version,cleanup_status,scope_kind
		FROM ops.export_job WHERE id=$1`, pendingExportID).Scan(
		&requestHash, &requestTTL, &version, &cleanupStatus, &scopeKind,
	); err != nil {
		t.Fatal(err)
	}
	if len(requestHash) != 64 || requestTTL != 3600 || version != 1 || cleanupStatus != "NOT_REQUIRED" || scopeKind != "COLLECTION" {
		t.Fatalf("pending legacy backfill hash=%q ttl=%d version=%d cleanup=%q scope=%q",
			requestHash, requestTTL, version, cleanupStatus, scopeKind)
	}

	var status, readRevision, stagingPath string
	var exactCount int64
	var preparedAt time.Time
	if err := pool.QueryRow(ctx, `SELECT status,request_hash,read_model_revision,exact_count,prepared_at,prepared_staging_path
		FROM ops.export_job WHERE id=$1`, completeExportID).Scan(
		&status, &requestHash, &readRevision, &exactCount, &preparedAt, &stagingPath,
	); err != nil {
		t.Fatal(err)
	}
	if status != "SUCCEEDED" || len(requestHash) != 64 || readRevision != requestHash || exactCount != 0 ||
		!preparedAt.Equal(now.Add(3*time.Minute)) ||
		stagingPath != ".knowledge/exports/.staging/"+completeExportID+"-"+strings.Repeat("0", 32)+".md.stage" {
		t.Fatalf("completed legacy backfill status=%q request=%q read=%q exact=%d prepared=%s staging=%q",
			status, requestHash, readRevision, exactCount, preparedAt, stagingPath)
	}

	_, err := pool.Exec(ctx, `UPDATE ops.export_job SET version=version WHERE id=$1`, pendingExportID)
	assertPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `UPDATE ops.export_job SET version=version+1,idempotency_key=idempotency_key||'-changed' WHERE id=$1`, pendingExportID)
	assertPostgresCode(t, err, "55000")
	_, err = pool.Exec(ctx, `UPDATE ops.export_job SET version=version+1,file_hash=repeat('f',64) WHERE id=$1`, completeExportID)
	assertPostgresCode(t, err, "55000")

	if err := insertM9AttachmentExportFact(ctx, pool, m9AttachmentExportFact{
		ID: attachmentID, WorkspaceID: workspaceID, Kind: "ATTACHMENTS_ZIP",
		RequestHash: strings.Repeat("9", 64), CreatedAt: now.Add(10 * time.Minute),
	}); err != nil {
		t.Fatalf("insert attachment after legacy repair: %v", err)
	}
	claimedAt := now.Add(11 * time.Minute)
	if _, err := pool.Exec(ctx, `UPDATE ops.export_job SET status='RUNNING',version=version+1,
		attempt_count=attempt_count+1,lease_owner='legacy-repair-worker',lease_expires_at=$2,
		started_at=$3,updated_at=$3 WHERE id=$1`, attachmentID, claimedAt.Add(time.Hour), claimedAt); err != nil {
		t.Fatalf("claim attachment after legacy repair: %v", err)
	}
	preparedAt = now.Add(12 * time.Minute)
	if _, err := pool.Exec(ctx, `UPDATE ops.export_job SET version=version+1,prepared_at=$2,
		prepared_staging_path=$3,file_path=$4,file_hash=repeat('a',64),file_size=1,
		manifest_sha256=repeat('b',64),entry_count=1,total_uncompressed_bytes=1,updated_at=$2
		WHERE id=$1`, attachmentID, preparedAt,
		".knowledge/exports/.staging/"+attachmentID+"-"+strings.Repeat("c", 32)+".zip.stage",
		".knowledge/exports/"+attachmentID+".zip"); err != nil {
		t.Fatalf("prepare attachment zip after legacy repair: %v", err)
	}
}

func TestM9AttachmentExportMigrationRejectsInvalidLegacyRecordedVersion62Rows(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate string
	}{
		{
			name: "missing collection scope",
			mutate: `UPDATE ops.export_job SET collection_id=NULL,collection_version=NULL,
				query_hash=NULL,query_definition='{}'::jsonb`,
		},
		{
			name:   "partial result binding",
			mutate: `UPDATE ops.export_job SET file_path='.knowledge/exports/e9500000-0000-4000-8000-000000000030.md'`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			pool, cleanup := newMigrationTestDatabase(t, ctx)
			defer cleanup()
			provider := migrationProvider(t, pool)
			if _, err := provider.UpTo(ctx, 62); err != nil {
				t.Fatalf("migrate to 00062: %v", err)
			}
			const (
				workspaceID  = "e9300000-0000-4000-8000-000000000030"
				collectionID = "e9400000-0000-4000-8000-000000000030"
				exportID     = "e9500000-0000-4000-8000-000000000030"
			)
			now := time.Date(2026, 7, 30, 10, 0, 0, 0, time.UTC)
			queryHash := strings.Repeat("d", 64)
			insertExportHardeningWorkspace(t, ctx, pool, workspaceID, "attachment-invalid-legacy", "active", now)
			insertExportHardeningCollection(t, ctx, pool, collectionID, workspaceID, queryHash, now)
			if err := insertExportHardeningFact(ctx, pool, exportHardeningFact{
				ID: exportID, WorkspaceID: workspaceID, CollectionID: collectionID, QueryHash: queryHash,
				IdempotencyKey: "attachment-invalid-legacy", RequestHash: strings.Repeat("e", 64),
				TTLSeconds: 3600, CreatedAt: now,
			}); err != nil {
				t.Fatal(err)
			}
			downgradeM9ExportJobToLegacyRecorded62(t, ctx, pool)
			if _, err := pool.Exec(ctx, test.mutate); err != nil {
				t.Fatalf("mutate legacy fixture: %v", err)
			}
			var before string
			if err := pool.QueryRow(ctx, `SELECT to_jsonb(export_job)::text FROM ops.export_job WHERE id=$1`, exportID).Scan(&before); err != nil {
				t.Fatalf("snapshot invalid legacy fixture: %v", err)
			}

			_, err := provider.UpTo(ctx, 63)
			var postgresError *pgconn.PgError
			if err == nil || !errors.As(err, &postgresError) || postgresError.Code != "55000" {
				t.Fatalf("invalid legacy migration error=%v", err)
			}
			assertM9AttachmentMigrationVersion(t, ctx, pool, 62)
			assertM9LegacyRepairRolledBack(t, ctx, pool, exportID, before)
		})
	}
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

func TestM9AttachmentExportMigrationGuardsEnabledCapabilityWithoutHistory(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if _, err := provider.UpTo(ctx, 63); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE ops.export_capability SET enabled=true,updated_at=clock_timestamp()
		WHERE capability_key='workspace-attachments'`); err != nil {
		t.Fatal(err)
	}

	_, err := provider.DownTo(ctx, 62)
	var postgresError *pgconn.PgError
	if err == nil || !errors.As(err, &postgresError) || postgresError.Code != "55000" {
		t.Fatalf("00063 enabled capability guarded Down error=%v", err)
	}
	assertM9AttachmentMigrationVersion(t, ctx, pool, 63)
	var enabled bool
	if err := pool.QueryRow(ctx, `SELECT enabled FROM ops.export_capability
		WHERE capability_key='workspace-attachments'`).Scan(&enabled); err != nil {
		t.Fatal(err)
	}
	if !enabled {
		t.Fatal("guarded Down did not preserve enabled attachment export capability")
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

func completeM9CollectionExport(t *testing.T, ctx context.Context, pool *pgxpool.Pool, exportID string, createdAt time.Time) {
	t.Helper()
	claimedAt := createdAt.Add(time.Minute)
	if _, err := pool.Exec(ctx, `UPDATE ops.export_job SET
		status='RUNNING',version=version+1,attempt_count=attempt_count+1,
		lease_owner='legacy-shape-worker',lease_expires_at=$2,started_at=$3,updated_at=$3
		WHERE id=$1`, exportID, claimedAt.Add(time.Hour), claimedAt); err != nil {
		t.Fatalf("claim completed legacy fixture: %v", err)
	}
	preparedAt := createdAt.Add(2 * time.Minute)
	if _, err := pool.Exec(ctx, `UPDATE ops.export_job SET
		version=version+1,read_model_revision=repeat('f',64),exact_count=1,prepared_at=$2,
		prepared_staging_path=$3,file_path=$4,file_hash=repeat('a',64),file_size=1,updated_at=$2
		WHERE id=$1`, exportID, preparedAt,
		".knowledge/exports/.staging/"+exportID+"-"+strings.Repeat("b", 32)+".md.stage",
		".knowledge/exports/"+exportID+".md"); err != nil {
		t.Fatalf("prepare completed legacy fixture: %v", err)
	}
	completedAt := createdAt.Add(3 * time.Minute)
	if _, err := pool.Exec(ctx, `UPDATE ops.export_job SET
		status='SUCCEEDED',version=version+1,lease_owner=NULL,lease_expires_at=NULL,
		completed_at=$2,updated_at=$2 WHERE id=$1`, exportID, completedAt); err != nil {
		t.Fatalf("complete legacy fixture: %v", err)
	}
}

func downgradeM9ExportJobToLegacyRecorded62(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	statements := []string{
		`DROP TRIGGER IF EXISTS trg_ops_export_job_update ON ops.export_job`,
		`DROP FUNCTION IF EXISTS ops.enforce_export_job_update()`,
		`DROP INDEX IF EXISTS ops.uq_ops_export_prepared_staging`,
		`DROP INDEX IF EXISTS ops.idx_ops_export_cleanup_candidate`,
		`DROP INDEX IF EXISTS ops.idx_ops_export_expiry_candidate`,
		`ALTER TABLE ops.export_job
			DROP CONSTRAINT IF EXISTS ops_export_job_supported_kind,
			DROP CONSTRAINT IF EXISTS ops_export_job_sensitive_policy,
			DROP CONSTRAINT IF EXISTS ops_export_job_scope_binding,
			DROP CONSTRAINT IF EXISTS ops_export_job_request_hash,
			DROP CONSTRAINT IF EXISTS ops_export_job_request_ttl,
			DROP CONSTRAINT IF EXISTS ops_export_job_version,
			DROP CONSTRAINT IF EXISTS ops_export_job_counters,
			DROP CONSTRAINT IF EXISTS ops_export_job_prepared_binding,
			DROP CONSTRAINT IF EXISTS ops_export_job_prepared_path_binding,
			DROP CONSTRAINT IF EXISTS ops_export_job_lifecycle_binding,
			DROP CONSTRAINT IF EXISTS ops_export_job_cleanup_binding,
			DROP CONSTRAINT IF EXISTS ops_export_job_download_binding,
			DROP CONSTRAINT IF EXISTS ops_export_job_time_order,
			DROP CONSTRAINT IF EXISTS ops_export_job_text_bounds`,
		`ALTER TABLE ops.export_job
			DROP COLUMN request_hash,
			DROP COLUMN request_ttl_seconds,
			DROP COLUMN version,
			DROP COLUMN read_model_revision,
			DROP COLUMN exact_count,
			DROP COLUMN prepared_at,
			DROP COLUMN prepared_staging_path,
			DROP COLUMN cleanup_status,
			DROP COLUMN cleanup_attempt_count,
			DROP COLUMN cleanup_error,
			DROP COLUMN cleanup_updated_at,
			DROP COLUMN file_deleted_at`,
		`ALTER TABLE ops.export_job
			ADD CONSTRAINT ops_export_job_attempt_count CHECK (attempt_count>=0),
			ADD CONSTRAINT ops_export_job_download_count CHECK (download_count>=0),
			ADD CONSTRAINT ops_export_job_file_size CHECK (file_size>=0),
			ADD CONSTRAINT ops_export_job_lease_binding CHECK (
				(status='RUNNING' AND lease_owner IS NOT NULL AND lease_expires_at IS NOT NULL)
				OR (status<>'RUNNING' AND lease_owner IS NULL AND lease_expires_at IS NULL)
			),
			ADD CONSTRAINT ops_export_job_path_safe CHECK (
				file_path IS NULL OR file_path ~ '^\.knowledge/exports/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\.(md|json)$'
			),
			ADD CONSTRAINT ops_export_job_query_hash CHECK (query_hash IS NULL OR query_hash ~ '^[0-9a-f]{64}$'),
			ADD CONSTRAINT ops_export_job_scope_binding CHECK (
				(collection_id IS NULL AND collection_version IS NULL AND query_hash IS NULL)
				OR (collection_id IS NOT NULL AND collection_version IS NOT NULL AND collection_version>0
					AND query_hash IS NOT NULL AND query_hash ~ '^[0-9a-f]{64}$')
			),
			ADD CONSTRAINT ops_export_job_sensitive_policy CHECK (NOT include_sensitive OR redaction_policy='FULL'),
			ADD CONSTRAINT ops_export_job_success_binding CHECK (
				(status='SUCCEEDED' AND file_path IS NOT NULL AND file_hash IS NOT NULL) OR status<>'SUCCEEDED'
			),
			ADD CONSTRAINT ops_export_job_time_order CHECK (
				updated_at>=created_at AND expires_at>created_at
				AND (started_at IS NULL OR started_at>=created_at)
				AND (completed_at IS NULL OR completed_at>=created_at)
				AND (last_downloaded_at IS NULL OR last_downloaded_at>=created_at)
				AND (lease_expires_at IS NULL OR lease_expires_at>=created_at)
			),
			ADD CONSTRAINT ops_export_job_text_bounds CHECK (
				btrim(idempotency_key)<>'' AND octet_length(idempotency_key)<=128
				AND btrim(permission_scope)<>'' AND octet_length(permission_scope)<=128
				AND btrim(requested_by)<>'' AND octet_length(requested_by)<=256
				AND (error_code IS NULL OR octet_length(error_code)<=128)
				AND (error_message IS NULL OR octet_length(error_message)<=512)
				AND (lease_owner IS NULL OR (btrim(lease_owner)<>'' AND octet_length(lease_owner)<=128))
			)`,
	}
	for _, statement := range statements {
		if _, err := pool.Exec(ctx, statement); err != nil {
			t.Fatalf("create legacy recorded-62 shape: %v\n%s", err, statement)
		}
	}
}

func assertM9AttachmentMigrationShape(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	for _, column := range []string{
		"request_hash", "request_ttl_seconds", "version", "read_model_revision", "exact_count",
		"prepared_at", "prepared_staging_path", "cleanup_status", "cleanup_attempt_count",
		"cleanup_error", "cleanup_updated_at", "file_deleted_at", "scope_kind",
		"attachment_root_contract_version", "manifest_sha256", "entry_count", "total_uncompressed_bytes",
	} {
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
		"ops_export_job_request_hash":          {"request_hash", "^[0-9a-f]{64}$"},
		"ops_export_job_request_ttl":           {"request_ttl_seconds", "604800"},
		"ops_export_job_version":               {"version > 0"},
		"ops_export_job_scope_binding":         {"WORKSPACE_ATTACHMENTS", "attachment_root_contract_version", "fields = '[]'"},
		"ops_export_job_counters":              {"WORKSPACE_ATTACHMENTS", "file_size <= 1073741824"},
		"ops_export_job_prepared_binding":      {"manifest_sha256", "entry_count", "total_uncompressed_bytes"},
		"ops_export_job_prepared_path_binding": {"zip", "zip\\.stage"},
		"ops_export_job_lifecycle_binding":     {"SUCCEEDED", "prepared_at IS NOT NULL"},
		"ops_export_job_cleanup_binding":       {"cleanup_status", "file_deleted_at"},
		"ops_export_job_download_binding":      {"download_count", "last_downloaded_at"},
		"ops_export_job_time_order":            {"prepared_at", "cleanup_updated_at", "file_deleted_at"},
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
	var legacyKindConstraint, legacyPathConstraint, capabilityRows, attachmentIndexes, triggerCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint
		WHERE conrelid='ops.export_job'::regclass AND conname='export_job_kind_check'`).Scan(&legacyKindConstraint); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM ops.export_capability
		WHERE capability_key='workspace-attachments' AND contract_version='workspace-attachments/v1' AND NOT enabled`).Scan(&capabilityRows); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_constraint
		WHERE conrelid='ops.export_job'::regclass AND conname='ops_export_job_path_safe'`).Scan(&legacyPathConstraint); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE schemaname='ops'
		AND indexname IN ('idx_ops_export_pending_recovery','idx_ops_export_collection',
			'idx_ops_export_expiry_candidate','idx_ops_export_cleanup_candidate',
			'uq_ops_export_prepared_staging','idx_ops_export_attachment')`).Scan(&attachmentIndexes); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_trigger
		WHERE tgrelid='ops.export_job'::regclass AND tgname='trg_ops_export_job_update' AND NOT tgisinternal`).Scan(&triggerCount); err != nil {
		t.Fatal(err)
	}
	if legacyKindConstraint != 0 || legacyPathConstraint != 0 || capabilityRows != 1 || attachmentIndexes != 6 || triggerCount != 1 {
		t.Fatalf("00063 shape legacy_kind=%d legacy_path=%d capability=%d indexes=%d trigger=%d",
			legacyKindConstraint, legacyPathConstraint, capabilityRows, attachmentIndexes, triggerCount)
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

func assertM9LegacyRepairRolledBack(t *testing.T, ctx context.Context, pool *pgxpool.Pool, exportID, before string) {
	t.Helper()
	for _, column := range []string{
		"migration_00063_needs_export_hardening", "request_hash", "request_ttl_seconds", "version",
		"read_model_revision", "exact_count", "prepared_at", "prepared_staging_path", "cleanup_status",
		"cleanup_attempt_count", "cleanup_error", "cleanup_updated_at", "file_deleted_at",
	} {
		var count int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.columns
			WHERE table_schema='ops' AND table_name='export_job' AND column_name=$1`, column).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("failed 00063 left column %s behind", column)
		}
	}
	var after string
	if err := pool.QueryRow(ctx, `SELECT to_jsonb(export_job)::text FROM ops.export_job WHERE id=$1`, exportID).Scan(&after); err != nil {
		t.Fatalf("read invalid legacy fixture after rollback: %v", err)
	}
	if after != before {
		t.Fatalf("failed 00063 rewrote legacy fixture\nbefore=%s\nafter=%s", before, after)
	}
}
