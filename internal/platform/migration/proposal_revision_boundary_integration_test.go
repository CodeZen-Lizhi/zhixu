//go:build integration

package migration_test

import (
	"context"
	"io/fs"
	"os"
	"slices"
	"strings"
	"testing"

	atlasmigrations "github.com/CodeZen-Lizhi/zhixu/atlas"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func proposalRevisionBoundaryDatabase(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testdb.Require(t, testdb.Config{
		ExternalAdminURL: strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL")),
		Availability:     testdb.FailWhenUnavailable,
		MaxConns:         4,
		Migrate: func(ctx context.Context, pool *pgxpool.Pool) error {
			return platformmigration.MigrateAtlasToVersion(ctx, pool, 81)
		},
	}).Pool().DB()
	if _, err := pool.Exec(t.Context(), `
INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at)
VALUES('94000000-0000-4000-8000-000000000001','Proposal upgrade','/tmp/proposal-upgrade','/tmp/proposal-upgrade',now(),'inactive',1,now(),now());
INSERT INTO change_control.proposal(
    id,workspace_id,proposal_type,risk_level,idempotency_key,request_hash,status,version,created_at,updated_at
) VALUES
    ('94000000-0000-4000-8000-000000000002','94000000-0000-4000-8000-000000000001','file_patch','LOW','upgrade-a',repeat('a',64),'ready_for_review',1,now(),now()),
    ('94000000-0000-4000-8000-000000000003','94000000-0000-4000-8000-000000000001','file_patch','LOW','upgrade-b',repeat('a',64),'ready_for_review',1,now(),now());
INSERT INTO change_control.proposal_revision(
    id,proposal_id,revision_no,target_path,base_hash,content,evidence_summary,risk,rollback_plan,change_hash,created_at
) VALUES
    ('94000000-0000-4000-8000-000000000011','94000000-0000-4000-8000-000000000002',1,'docs/upgrade.md',repeat('b',64),'old','evidence','low','restore',repeat('c',64),now()),
    ('94000000-0000-4000-8000-000000000012','94000000-0000-4000-8000-000000000002',2,'docs/upgrade.md',repeat('b',64),'new','evidence','low','restore',repeat('d',64),now()),
    ('94000000-0000-4000-8000-000000000013','94000000-0000-4000-8000-000000000003',1,'docs/other.md',repeat('b',64),'other','evidence','low','restore',repeat('e',64),now());
UPDATE change_control.proposal SET status='rejected',version=version+1,updated_at=updated_at+interval '1 second'
WHERE id='94000000-0000-4000-8000-000000000003';`); err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestProposalRevisionCompatibilityTransactionIsolation(t *testing.T) {
	ctx := t.Context()
	pool := proposalRevisionBoundaryDatabase(t)
	compatibility, err := fs.ReadFile(atlasmigrations.MigrationDir(), "00093_proposal_revision_backfill_compatibility.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, string(compatibility)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE change_control.proposal ADD COLUMN current_revision_id uuid`); err != nil {
		t.Fatal(err)
	}
	savepoint, err := tx.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, insertErr := savepoint.Exec(ctx, `INSERT INTO change_control.proposal(
    id,workspace_id,proposal_type,risk_level,idempotency_key,request_hash,status,version,created_at,updated_at
) VALUES('94000000-0000-4000-8000-000000000004','94000000-0000-4000-8000-000000000001',
         'file_patch','LOW','ordinary-insert',repeat('f',64),'ready_for_review',1,now(),now())`)
	if err := savepoint.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	assertPostgresCode(t, insertErr, "23514")

	// Real concurrent sessions cannot read/lock either table while the bridge
	// owns its transaction; they must not observe the temporary function.
	for _, statement := range []string{
		`SELECT id FROM change_control.proposal FOR UPDATE`,
		`SELECT id FROM change_control.proposal_revision FOR UPDATE`,
	} {
		other, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := other.Exec(ctx, `SET LOCAL lock_timeout='200ms'`); err != nil {
			_ = other.Rollback(context.Background())
			t.Fatal(err)
		}
		_, err = other.Exec(ctx, statement)
		_ = other.Rollback(context.Background())
		assertPostgresCode(t, err, "55P03")
	}
	if _, err := tx.Exec(ctx, `UPDATE change_control.proposal
SET current_revision_id='94000000-0000-4000-8000-000000000012'
WHERE id='94000000-0000-4000-8000-000000000002'`); err != nil {
		t.Fatalf("exact historical pointer backfill: %v", err)
	}
	// Inject a committed temporary guard only in this disposable database. The
	// runner rollback tests prove production cannot commit this state; the
	// transaction binding must still reject a later exact NULL-to-latest update.
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `UPDATE change_control.proposal
SET current_revision_id='94000000-0000-4000-8000-000000000013'
WHERE id='94000000-0000-4000-8000-000000000003'`)
	assertPostgresCode(t, err, "23514")
}

func TestProposalRevisionCompatibilityCatalogParity(t *testing.T) {
	ctx := t.Context()
	upgraded := proposalRevisionBoundaryDatabase(t)
	var historicalFacts string
	if err := upgraded.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(p) ORDER BY id)::text FROM change_control.proposal p`).Scan(&historicalFacts); err != nil {
		t.Fatal(err)
	}
	if err := platformmigration.MigrateAtlas(ctx, upgraded); err != nil {
		t.Fatalf("upgrade historical database: %v", err)
	}
	var currentFacts string
	if err := upgraded.QueryRow(ctx, `SELECT jsonb_agg(to_jsonb(p)-'current_revision_id' ORDER BY id)::text FROM change_control.proposal p`).Scan(&currentFacts); err != nil {
		t.Fatal(err)
	}
	if currentFacts != historicalFacts {
		t.Fatal("upgrade changed historical Proposal fields, including status and version")
	}
	_, err := upgraded.Exec(ctx, `UPDATE change_control.proposal SET version=version
WHERE id='94000000-0000-4000-8000-000000000002'`)
	assertPostgresCode(t, err, "23514")
	// A normal transaction still defers completion validation until commit.
	tx, err := upgraded.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `INSERT INTO change_control.proposal(
    id,workspace_id,proposal_type,risk_level,idempotency_key,request_hash,status,version,created_at,updated_at
) VALUES('94000000-0000-4000-8000-000000000004','94000000-0000-4000-8000-000000000001',
         'file_patch','LOW','invalid-completion',repeat('f',64),'completed',1,now(),now())`); err != nil {
		t.Fatalf("completion constraint no longer deferred: %v", err)
	}
	assertPostgresCode(t, tx.Commit(ctx), "55000")

	fresh := testdb.Require(t, testdb.Config{
		ExternalAdminURL: strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL")),
		Availability:     testdb.FailWhenUnavailable,
	}).Pool().DB()
	baselineSQL, err := os.ReadFile("../../../atlas/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	baseline := testdb.Require(t, testdb.Config{
		// Use the same disposable instance so 00080's cluster role exists.
		ExternalAdminURL: fresh.Config().ConnString(),
		Migrate: func(ctx context.Context, pool *pgxpool.Pool) error {
			_, err := pool.Exec(ctx, string(baselineSQL))
			return err
		},
	}).Pool().DB()
	fingerprintSQL, err := os.ReadFile("../../../deploy/atlas-schema-fingerprint.sql")
	if err != nil {
		t.Fatal(err)
	}
	readFingerprint := func(database *pgxpool.Pool) []string {
		rows, err := database.Query(ctx, string(fingerprintSQL))
		if err != nil {
			t.Fatal(err)
		}
		facts, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(facts)
		return facts
	}
	want := readFingerprint(baseline)
	for name, database := range map[string]*pgxpool.Pool{"fresh": fresh, "upgraded": upgraded} {
		got := readFingerprint(database)
		if !slices.Equal(got, want) {
			var differences []string
			for _, fact := range got {
				if _, found := slices.BinarySearch(want, fact); !found {
					differences = append(differences, "extra: "+fact)
				}
			}
			for _, fact := range want {
				if _, found := slices.BinarySearch(got, fact); !found {
					differences = append(differences, "missing: "+fact)
				}
			}
			t.Fatalf("%s differs from atlas/schema.sql: %v", name, differences)
		}
		t.Logf("%s matches atlas/schema.sql: %d catalog facts", name, len(got))
	}
}
