//go:build integration

package migration

import (
	"context"
	"strings"
	"testing"
	"time"

	atlaspostgres "ariga.io/atlas/sql/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

const (
	compatibilityWorkspaceID     = "c9000000-0000-4000-8000-000000000001"
	compatibilityOtherWorkspace  = "c9000000-0000-4000-8000-000000000002"
	compatibilityProposalID      = "c9100000-0000-4000-8000-000000000001"
	compatibilityOtherProposal   = "c9100000-0000-4000-8000-000000000002"
	compatibilityOldRevision     = "c9200000-0000-4000-8000-000000000009"
	compatibilityLatestRevision  = "c9200000-0000-4000-8000-000000000001"
	compatibilityOtherRevision   = "c9200000-0000-4000-8000-000000000099"
	compatibilityLegacyGuardHash = "ce38e1300b22768b6e13237badfd7d5a8898231ed1dbc042c6b5b9871e9a069f"
	compatibilityFinalGuardHash  = "60500b092acadc6d9a10ca72a2beeb2e7366659767d4565d5348f2817ab1f8e0"
)

func TestProposalRevisionCompatibilityBackfillsLatestOwnedRevision(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabaseWithMaxConns(t, ctx, 1)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if err := provider.UpTo(ctx, 81); err != nil {
		t.Fatal(err)
	}
	seedProposalRevisionCompatibility(t, ctx, pool)
	before := proposalRevisionCompatibilityHistory(t, ctx, pool)
	if err := provider.UpTo(ctx, 82); err != nil {
		t.Fatalf("upgrade historical proposals: %v", err)
	}
	assertMigrationVersion(t, ctx, pool, 82)
	assertProposalRevisionCompatibilityGuards(t, ctx, pool, compatibilityFinalGuardHash)
	assertProposalRevisionCompatibilityHistory(t, ctx, pool, before)
	var ownedPointers int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM change_control.proposal
		WHERE (id=$1 AND current_revision_id=$2) OR (id=$3 AND current_revision_id=$4)`,
		compatibilityProposalID, compatibilityLatestRevision, compatibilityOtherProposal, compatibilityOtherRevision).Scan(&ownedPointers); err != nil {
		t.Fatal(err)
	}
	if ownedPointers != 2 {
		t.Fatalf("latest owned revision pointers=%d, want 2", ownedPointers)
	}

	if err := provider.UpTo(ctx, 92); err != nil {
		t.Fatal(err)
	}
	// A later legal migration may change the guard. Normal 00093 execution must
	// neither replace it nor require the historical 00082 function fingerprint.
	var guardBody string
	if err := pool.QueryRow(ctx, `SELECT prosrc FROM pg_proc
		WHERE oid='change_control.validate_proposal_transition()'::regprocedure`).Scan(&guardBody); err != nil {
		t.Fatal(err)
	}
	guardBody += "\n-- Later compatible transition guard.\n"
	if _, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION change_control.validate_proposal_transition()
		RETURNS trigger LANGUAGE plpgsql AS $later$`+guardBody+`$later$`); err != nil {
		t.Fatal(err)
	}
	// Match the production River baseline before comparing project-only 00093.
	if err := migrateRiver(ctx, pool, "workflow"); err != nil {
		t.Fatal(err)
	}
	beforeRealm := inspectNormalizedRealm(t, ctx, pool.Config().ConnString())
	runner := newAtlasRunnerForPool(t, pool)
	if err := runner.Up(ctx); err != nil {
		t.Fatalf("apply compatibility revision normally: %v", err)
	}
	assertAtlasTerminalState(t, ctx, pool, runner, 93)
	assertProposalRevisionCompatibilityHistory(t, ctx, pool, before)
	var afterBody string
	if err := pool.QueryRow(ctx, `SELECT prosrc FROM pg_proc
		WHERE oid='change_control.validate_proposal_transition()'::regprocedure`).Scan(&afterBody); err != nil {
		t.Fatal(err)
	}
	if afterBody != guardBody {
		t.Fatal("normal compatibility revision replaced a later guard")
	}
	afterRealm := inspectNormalizedRealm(t, ctx, pool.Config().ConnString())
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() { _ = sqlDB.Close() }()
	driver, err := atlaspostgres.Open(sqlDB)
	if err != nil {
		t.Fatal(err)
	}
	changes, err := driver.RealmDiff(beforeRealm, afterRealm)
	if err != nil || len(changes) != 0 {
		t.Fatalf("normal compatibility apply changed permanent schema: changes=%v error=%v", changes, err)
	}
}

func TestProposalRevisionCompatibilityRollsBackAndRetries(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabaseWithMaxConns(t, ctx, 1)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if err := provider.UpTo(ctx, 81); err != nil {
		t.Fatal(err)
	}
	seedProposalRevisionCompatibility(t, ctx, pool)
	before := proposalRevisionCompatibilityHistory(t, ctx, pool)
	// Fail on 00082's last statement, after both the backfill and the permanent
	// guard replacement. The published migration file is executed unchanged.
	if _, err := pool.Exec(ctx, `CREATE FUNCTION pg_temp.fail_proposal_revision_upgrade()
		RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF EXISTS (SELECT 1 FROM change_control.proposal WHERE current_revision_id IS NULL) THEN
				RAISE EXCEPTION 'test fault did not reach the completed backfill';
			END IF;
			RAISE EXCEPTION 'TEST_PROPOSAL_REVISION_BACKFILL_FAILURE' USING ERRCODE='23514';
		END $$;
		CREATE TRIGGER test_proposal_revision_upgrade_failure BEFORE INSERT ON core.schema_meta
		FOR EACH ROW WHEN (NEW.key='change_control_revision')
		EXECUTE FUNCTION pg_temp.fail_proposal_revision_upgrade()`); err != nil {
		t.Fatal(err)
	}
	err := provider.UpTo(ctx, 82)
	assertPostgresCode(t, err, "23514")
	if !strings.Contains(err.Error(), "TEST_PROPOSAL_REVISION_BACKFILL_FAILURE") {
		t.Fatalf("unexpected failure before the injected final statement: %v", err)
	}
	assertProposalRevisionCompatibilityRollback(t, ctx, pool, before)

	// Also prove the runner's postcondition can roll back an otherwise completed
	// Atlas Execute, including the revision row that Execute already wrote.
	if _, err := pool.Exec(ctx, `CREATE OR REPLACE FUNCTION pg_temp.fail_proposal_revision_upgrade()
		RETURNS trigger LANGUAGE plpgsql AS $fault$
		BEGIN
			EXECUTE 'CREATE OR REPLACE FUNCTION change_control.validate_proposal_transition()
				RETURNS trigger LANGUAGE plpgsql AS ''BEGIN RETURN NEW; END''';
			RETURN NEW;
		END $fault$`); err != nil {
		t.Fatal(err)
	}
	err = provider.UpTo(ctx, 82)
	assertPostgresCode(t, err, "55000")
	if !strings.Contains(err.Error(), "MIGRATION_PROPOSAL_REVISION_BACKFILL_FINAL_GUARD_MISMATCH") {
		t.Fatalf("unexpected final guard failure: %v", err)
	}
	assertProposalRevisionCompatibilityRollback(t, ctx, pool, before)
	if _, err := pool.Exec(ctx, `DROP TRIGGER test_proposal_revision_upgrade_failure ON core.schema_meta;
		DROP FUNCTION pg_temp.fail_proposal_revision_upgrade()`); err != nil {
		t.Fatal(err)
	}
	if err := provider.UpTo(ctx, 82); err != nil {
		t.Fatalf("retry historical upgrade after failure: %v", err)
	}
	assertMigrationVersion(t, ctx, pool, 82)
	assertProposalRevisionCompatibilityGuards(t, ctx, pool, compatibilityFinalGuardHash)
	assertProposalRevisionCompatibilityHistory(t, ctx, pool, before)
}

func TestProposalRevisionCompatibilityRejectsUnknownGuardsAndUnsafeBackfill(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabaseWithMaxConns(t, ctx, 1)
	defer cleanup()
	provider := migrationProvider(t, pool)
	if err := provider.UpTo(ctx, 81); err != nil {
		t.Fatal(err)
	}
	seedProposalRevisionCompatibility(t, ctx, pool)
	before := proposalRevisionCompatibilityHistory(t, ctx, pool)
	var originalGuard string
	if err := pool.QueryRow(ctx, `SELECT pg_get_functiondef('change_control.validate_proposal_transition()'::regprocedure)`).Scan(&originalGuard); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, alter, restore string
	}{
		{"unknown transition function", `CREATE OR REPLACE FUNCTION change_control.validate_proposal_transition()
			RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END $$`, originalGuard},
		{"disabled transition trigger", `ALTER TABLE change_control.proposal DISABLE TRIGGER proposal_validate_transition`,
			`ALTER TABLE change_control.proposal ENABLE TRIGGER proposal_validate_transition`},
		{"disabled completion constraint", `ALTER TABLE change_control.proposal DISABLE TRIGGER proposal_verify_reindex_completion`,
			`ALTER TABLE change_control.proposal ENABLE TRIGGER proposal_verify_reindex_completion`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := pool.Exec(ctx, test.alter); err != nil {
				t.Fatal(err)
			}
			err := provider.UpTo(ctx, 82)
			if _, restoreErr := pool.Exec(ctx, test.restore); restoreErr != nil {
				t.Fatal(restoreErr)
			}
			assertPostgresCode(t, err, "55000")
			if !strings.Contains(err.Error(), "MIGRATION_PROPOSAL_REVISION_BACKFILL_UNEXPECTED_SCHEMA") {
				t.Fatalf("unexpected guard refusal: %v", err)
			}
			assertProposalRevisionCompatibilityRollback(t, ctx, pool, before)
		})
	}

	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() { _ = sqlDB.Close() }()
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	files, err := atlasFilesByVersion(loadAtlasDirForTest(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := executeProposalRevisionCompatibility(ctx, tx, files[93]); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE change_control.proposal ADD COLUMN current_revision_id uuid`); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, statement string
		args            []any
	}{
		{"older owned revision", `UPDATE change_control.proposal SET current_revision_id=$2 WHERE id=$1`, []any{compatibilityProposalID, compatibilityOldRevision}},
		{"foreign revision", `UPDATE change_control.proposal SET current_revision_id=$2 WHERE id=$1`, []any{compatibilityProposalID, compatibilityOtherRevision}},
		{"business version change", `UPDATE change_control.proposal SET current_revision_id=$2,version=version+1 WHERE id=$1`, []any{compatibilityProposalID, compatibilityLatestRevision}},
		{"business status change", `UPDATE change_control.proposal SET current_revision_id=$2,status='rejected' WHERE id=$1`, []any{compatibilityProposalID, compatibilityLatestRevision}},
		{"business timestamp change", `UPDATE change_control.proposal SET current_revision_id=$2,updated_at=updated_at+interval '1 second' WHERE id=$1`, []any{compatibilityProposalID, compatibilityLatestRevision}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := tx.ExecContext(ctx, `SAVEPOINT unsafe_backfill`); err != nil {
				t.Fatal(err)
			}
			_, err := tx.ExecContext(ctx, test.statement, test.args...)
			if _, rollbackErr := tx.ExecContext(ctx, `ROLLBACK TO SAVEPOINT unsafe_backfill`); rollbackErr != nil {
				t.Fatal(rollbackErr)
			}
			assertPostgresCode(t, err, "23514")
		})
	}
	if _, err := tx.ExecContext(ctx, `UPDATE change_control.proposal SET current_revision_id=$2 WHERE id=$1`,
		compatibilityProposalID, compatibilityLatestRevision); err != nil {
		t.Fatalf("exact historical backfill was refused: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertProposalRevisionCompatibilityRollback(t, ctx, pool, before)
	if err := provider.UpTo(ctx, 82); err != nil {
		t.Fatalf("upgrade after rejected unsafe backfills: %v", err)
	}
	assertProposalRevisionCompatibilityGuards(t, ctx, pool, compatibilityFinalGuardHash)
}

func seedProposalRevisionCompatibility(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	now := time.Date(2026, 9, 8, 5, 0, 0, 123000000, time.UTC)
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(
		id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at
	) VALUES($1,'M9 compatibility','/tmp/m9-compatibility','/tmp/m9-compatibility',$3,'inactive',1,$3,$3),
		($2,'M9 other','/tmp/m9-compatibility-other','/tmp/m9-compatibility-other',$3,'inactive',1,$3,$3)`,
		compatibilityWorkspaceID, compatibilityOtherWorkspace, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO change_control.proposal(
		id,workspace_id,proposal_type,risk_level,idempotency_key,request_hash,status,version,created_at,updated_at
	) VALUES($1,$2,'file_patch','LOW','m9-history',repeat('a',64),'ready_for_review',1,$5,$5),
		($3,$4,'file_patch','HIGH','m9-other-history',repeat('b',64),'ready_for_review',1,$5,$5)`,
		compatibilityProposalID, compatibilityWorkspaceID, compatibilityOtherProposal, compatibilityOtherWorkspace, now); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO change_control.proposal_revision(
		id,proposal_id,revision_no,target_path,target_mode,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,created_at
	) VALUES($1,$2,1,'docs/history.md','REPLACE',repeat('c',64),'old','evidence','LOW','restore',repeat('d',64),$6),
		($3,$2,2,'docs/history.md','REPLACE',repeat('c',64),'latest','evidence','LOW','restore',repeat('e',64),$6),
		($4,$5,99,'docs/other.md','REPLACE',repeat('f',64),'other workspace','evidence','HIGH','restore',repeat('a',64),$6)`,
		compatibilityOldRevision, compatibilityProposalID, compatibilityLatestRevision, compatibilityOtherRevision, compatibilityOtherProposal, now); err != nil {
		t.Fatal(err)
	}
}

func proposalRevisionCompatibilityHistory(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	var snapshot string
	if err := pool.QueryRow(ctx, `SELECT jsonb_build_object(
		'proposals',(SELECT jsonb_agg(to_jsonb(p)-'current_revision_id' ORDER BY p.id) FROM change_control.proposal p),
		'revisions',(SELECT jsonb_agg(to_jsonb(r) ORDER BY r.id) FROM change_control.proposal_revision r)
	)::text`).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func assertProposalRevisionCompatibilityHistory(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want string) {
	t.Helper()
	if got := proposalRevisionCompatibilityHistory(t, ctx, pool); got != want {
		t.Fatal("historical proposal fields or revision facts changed")
	}
}

func assertProposalRevisionCompatibilityGuards(t *testing.T, ctx context.Context, pool *pgxpool.Pool, wantHash string) {
	t.Helper()
	var guardHash string
	var markerAbsent, completionEnabled bool
	if err := pool.QueryRow(ctx, `SELECT encode(sha256(convert_to(prosrc,'UTF8')),'hex'),
		to_regclass('pg_temp.zhixu_proposal_revision_backfill') IS NULL,
		EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid='change_control.proposal'::regclass
			AND tgname='proposal_verify_reindex_completion' AND tgenabled='O' AND tgdeferrable AND tginitdeferred)
		FROM pg_proc WHERE oid='change_control.validate_proposal_transition()'::regprocedure`).Scan(
		&guardHash, &markerAbsent, &completionEnabled); err != nil {
		t.Fatal(err)
	}
	if guardHash != wantHash || !markerAbsent || !completionEnabled {
		t.Fatalf("guard=%s marker_absent=%t deferred_completion_enabled=%t", guardHash, markerAbsent, completionEnabled)
	}
}

func assertProposalRevisionCompatibilityRollback(t *testing.T, ctx context.Context, pool *pgxpool.Pool, before string) {
	t.Helper()
	assertMigrationVersion(t, ctx, pool, 81)
	assertProposalRevisionCompatibilityHistory(t, ctx, pool, before)
	assertProposalRevisionCompatibilityGuards(t, ctx, pool, compatibilityLegacyGuardHash)
	var leftover bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_attribute
		WHERE attrelid='change_control.proposal'::regclass AND attname='current_revision_id' AND NOT attisdropped)
		OR to_regclass('change_control.proposal_revision_base_snapshot') IS NOT NULL
		OR EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='change_control.proposal_revision'::regclass
			AND conname='uq_proposal_revision_proposal_id_id')
		OR EXISTS(SELECT 1 FROM core.schema_meta WHERE key='change_control_revision')`).Scan(&leftover); err != nil {
		t.Fatal(err)
	}
	if leftover {
		t.Fatal("failed historical upgrade left schema or metadata changes")
	}
}
