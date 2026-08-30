//go:build integration

package migration

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"ariga.io/atlas/sql/migrate"
	atlaspostgres "ariga.io/atlas/sql/postgres"
	"ariga.io/atlas/sql/schema"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

// buildGooseStateTo reproduces a pre-Atlas database: migration contents 1..N
// applied directly and the Goose history table marked accordingly. It replaces
// the retired Goose runner for fixture construction.
func buildGooseStateTo(t *testing.T, ctx context.Context, pool *pgxpool.Pool, dir *migrate.MemDir, version int64) {
	t.Helper()
	versions := make([]int64, 0, version)
	for v := int64(1); v <= version; v++ {
		versions = append(versions, v)
	}
	applyAtlasFilesDirectly(t, ctx, pool, dir, versions...)
	insertGooseHistory(t, ctx, pool, versions...)
}

// applyAtlasFilesDirectly executes the statements of the given migration
// versions without recording Atlas revisions, reproducing legacy databases
// whose schema exists without Atlas history.
func applyAtlasFilesDirectly(t *testing.T, ctx context.Context, pool *pgxpool.Pool, dir *migrate.MemDir, versions ...int64) {
	t.Helper()
	files, err := atlasFilesByVersion(dir)
	if err != nil {
		t.Fatalf("index atlas migration files: %v", err)
	}
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()
	for _, version := range versions {
		file, ok := files[version]
		if !ok {
			t.Fatalf("no atlas migration file for version %d", version)
		}
		stmts, err := file.StmtDecls()
		if err != nil {
			t.Fatalf("scan %s: %v", file.Name(), err)
		}
		if atlasFileTxModeNone(file) {
			for _, stmt := range stmts {
				if _, err := db.ExecContext(ctx, stmt.Text); err != nil {
					t.Fatalf("apply %s: %v", file.Name(), err)
				}
			}
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("begin %s: %v", file.Name(), err)
		}
		for _, stmt := range stmts {
			if _, err := tx.ExecContext(ctx, stmt.Text); err != nil {
				_ = tx.Rollback()
				t.Fatalf("apply %s: %v", file.Name(), err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit %s: %v", file.Name(), err)
		}
	}
}

// insertGooseHistory records applied Goose versions directly, reproducing the
// legacy history table layout used before the Atlas runner.
func insertGooseHistory(t *testing.T, ctx context.Context, pool *pgxpool.Pool, versions ...int64) {
	t.Helper()
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS public.goose_db_version (
		id serial PRIMARY KEY,
		version_id bigint NOT NULL,
		is_applied boolean NOT NULL,
		tstamp timestamp DEFAULT now()
	)`); err != nil {
		t.Fatalf("create goose history table: %v", err)
	}
	for _, version := range versions {
		if _, err := db.ExecContext(ctx,
			"INSERT INTO public.goose_db_version (version_id, is_applied) VALUES ($1, true)", version); err != nil {
			t.Fatalf("insert goose history %d: %v", version, err)
		}
	}
}

// inspectNormalizedRealm inspects the full database and strips the tooling
// objects (Atlas revision storage and the retired Goose history table) so two
// databases can be compared on business schema alone.
func inspectNormalizedRealm(t *testing.T, ctx context.Context, dsn string) *schema.Realm {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("open %s: %v", dsn, err)
	}
	defer func() { _ = db.Close() }()
	driver, err := atlaspostgres.Open(db)
	if err != nil {
		t.Fatalf("atlas driver: %v", err)
	}
	realm, err := driver.InspectRealm(ctx, nil)
	if err != nil {
		t.Fatalf("inspect realm: %v", err)
	}
	filtered := realm.Schemas[:0]
	for _, sch := range realm.Schemas {
		if sch.Name == atlasRevisionSchema {
			continue
		}
		if sch.Name == "public" {
			tables := sch.Tables[:0]
			for _, table := range sch.Tables {
				if table.Name == projectMigrationTable {
					continue
				}
				tables = append(tables, table)
			}
			sch.Tables = tables
		}
		filtered = append(filtered, sch)
	}
	realm.Schemas = filtered
	return realm
}

// assertEquivalentRealms diffs two inspected realms and fails with the full
// change list when they diverge.
func assertEquivalentRealms(t *testing.T, ctx context.Context, referenceDSN, actualDSN string) {
	t.Helper()
	reference := inspectNormalizedRealm(t, ctx, referenceDSN)
	actual := inspectNormalizedRealm(t, ctx, actualDSN)
	db, err := sql.Open("pgx", actualDSN)
	if err != nil {
		t.Fatalf("open diff connection: %v", err)
	}
	defer func() { _ = db.Close() }()
	driver, err := atlaspostgres.Open(db)
	if err != nil {
		t.Fatalf("atlas diff driver: %v", err)
	}
	changes, err := driver.RealmDiff(reference, actual)
	if err != nil {
		t.Fatalf("realm diff: %v", err)
	}
	if len(changes) != 0 {
		for _, change := range changes {
			t.Logf("unexpected change: %+v", change)
		}
		t.Fatalf("realms diverge with %d changes", len(changes))
	}
}

// assertAtlasTerminalState verifies the shared end state of every adoption
// path: complete revision history, no Goose table, repeat-run no-op.
func assertAtlasTerminalState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, runner *AtlasRunner, wantRevisions int) {
	t.Helper()
	var revisions int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM atlas_schema_revisions.atlas_schema_revisions").Scan(&revisions); err != nil {
		t.Fatalf("count revisions: %v", err)
	}
	if revisions != wantRevisions {
		t.Fatalf("revisions=%d, want %d", revisions, wantRevisions)
	}
	var gooseTable bool
	if err := pool.QueryRow(ctx, "SELECT (to_regclass('public.goose_db_version') IS NOT NULL)").Scan(&gooseTable); err != nil {
		t.Fatalf("probe goose table: %v", err)
	}
	if gooseTable {
		t.Fatal("goose_db_version still present after atlas adoption and 00092")
	}
	if err := runner.Up(ctx); err != nil {
		t.Fatalf("repeat atlas Up must be a no-op: %v", err)
	}
}

func newAtlasRunnerForPool(t *testing.T, pool *pgxpool.Pool) *AtlasRunner {
	t.Helper()
	runner, err := NewAtlasRunner(pool, loadAtlasDirForTest(t))
	if err != nil {
		t.Fatalf("new atlas runner: %v", err)
	}
	return runner
}

func atlasTestPool(t *testing.T, ctx context.Context, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestAtlasAdoptionFullGooseHistory adopts a fully migrated Goose database.
func TestAtlasAdoptionFullGooseHistory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	adoptedDSN, adoptedCleanup := spikePostgreSQL(t, ctx)
	defer adoptedCleanup()
	adoptedPool := atlasTestPool(t, ctx, adoptedDSN)
	dir := loadAtlasDirForTest(t)
	buildGooseStateTo(t, ctx, adoptedPool, dir, legacyGooseMaxVersion)

	runner := newAtlasRunnerForPool(t, adoptedPool)
	if err := runner.Up(ctx); err != nil {
		t.Fatalf("atlas adoption Up: %v", err)
	}
	assertAtlasTerminalState(t, ctx, adoptedPool, runner, 92)

	referenceDSN, referenceCleanup := spikePostgreSQL(t, ctx)
	defer referenceCleanup()
	referencePool := atlasTestPool(t, ctx, referenceDSN)
	if err := newAtlasRunnerForPool(t, referencePool).Up(ctx); err != nil {
		t.Fatalf("atlas reference Up: %v", err)
	}
	assertEquivalentRealms(t, ctx, referenceDSN, adoptedDSN)
}

// TestAtlasAdoptionPartialGooseHistory adopts a database stopped at Goose
// version 50 and verifies the remaining migrations apply on top.
func TestAtlasAdoptionPartialGooseHistory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	dsn, cleanup := spikePostgreSQL(t, ctx)
	defer cleanup()
	pool := atlasTestPool(t, ctx, dsn)
	buildGooseStateTo(t, ctx, pool, loadAtlasDirForTest(t), 50)

	runner := newAtlasRunnerForPool(t, pool)
	if err := runner.Up(ctx); err != nil {
		t.Fatalf("atlas adoption Up: %v", err)
	}
	assertAtlasTerminalState(t, ctx, pool, runner, 92)

	referenceDSN, referenceCleanup := spikePostgreSQL(t, ctx)
	defer referenceCleanup()
	referencePool := atlasTestPool(t, ctx, referenceDSN)
	if err := newAtlasRunnerForPool(t, referencePool).Up(ctx); err != nil {
		t.Fatalf("atlas reference Up: %v", err)
	}
	assertEquivalentRealms(t, ctx, referenceDSN, dsn)
}

// TestAtlasAdoptionShellRunnerLegacy adopts a pre-Goose shell-runner database
// whose only migration proof is the core.schema_meta fact set.
func TestAtlasAdoptionShellRunnerLegacy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	dsn, cleanup := spikePostgreSQL(t, ctx)
	defer cleanup()
	pool := atlasTestPool(t, ctx, dsn)
	dir := loadAtlasDirForTest(t)
	legacyVersions := make([]int64, 0, legacyMigrationMaxVersion)
	for version := int64(1); version <= legacyMigrationMaxVersion; version++ {
		legacyVersions = append(legacyVersions, version)
	}
	applyAtlasFilesDirectly(t, ctx, pool, dir, legacyVersions...)

	runner := newAtlasRunnerForPool(t, pool)
	if err := runner.Up(ctx); err != nil {
		t.Fatalf("atlas legacy adoption Up: %v", err)
	}
	assertAtlasTerminalState(t, ctx, pool, runner, 92)
	var firstType int64
	if err := pool.QueryRow(ctx,
		"SELECT type FROM atlas_schema_revisions.atlas_schema_revisions WHERE version='00001'").Scan(&firstType); err != nil {
		t.Fatalf("read adopted revision: %v", err)
	}
	if firstType != int64(migrate.RevisionTypeExecute) {
		t.Fatalf("adopted revision type=%d, want execute (%d)", firstType, migrate.RevisionTypeExecute)
	}

	referenceDSN, referenceCleanup := spikePostgreSQL(t, ctx)
	defer referenceCleanup()
	referencePool := atlasTestPool(t, ctx, referenceDSN)
	if err := newAtlasRunnerForPool(t, referencePool).Up(ctx); err != nil {
		t.Fatalf("atlas reference Up: %v", err)
	}
	assertEquivalentRealms(t, ctx, referenceDSN, dsn)
}

// TestAtlasAdoptionEinoCollision reproduces the legacy Eino 78/79 collision:
// Goose history claims 78/79, the schema carries the legacy Eino content (now
// files 83/84), canonical 78/79 are missing, and 80-82 were never applied.
func TestAtlasAdoptionEinoCollision(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	dsn, cleanup := spikePostgreSQL(t, ctx)
	defer cleanup()
	pool := atlasTestPool(t, ctx, dsn)
	dir := loadAtlasDirForTest(t)
	buildGooseStateTo(t, ctx, pool, dir, 77)
	applyAtlasFilesDirectly(t, ctx, pool, dir, adoptedEinoFirstVersion, adoptedEinoSecondVersion)
	insertGooseHistory(t, ctx, pool, legacyCollisionFirstVersion, legacyCollisionSecondVersion)

	runner := newAtlasRunnerForPool(t, pool)
	if err := runner.Up(ctx); err != nil {
		t.Fatalf("atlas collision adoption Up: %v", err)
	}
	assertAtlasTerminalState(t, ctx, pool, runner, 92)

	referenceDSN, referenceCleanup := spikePostgreSQL(t, ctx)
	defer referenceCleanup()
	referencePool := atlasTestPool(t, ctx, referenceDSN)
	if err := newAtlasRunnerForPool(t, referencePool).Up(ctx); err != nil {
		t.Fatalf("atlas reference Up: %v", err)
	}
	assertEquivalentRealms(t, ctx, referenceDSN, dsn)
}

// TestAtlasAdoptionEinoCollisionResumesAfterAdoption proves a process crash
// after revision adoption but before the 80-82 gap is applied remains
// recoverable on the next runner invocation.
func TestAtlasAdoptionEinoCollisionResumesAfterAdoption(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	dsn, cleanup := spikePostgreSQL(t, ctx)
	defer cleanup()
	pool := atlasTestPool(t, ctx, dsn)
	dir := loadAtlasDirForTest(t)
	buildGooseStateTo(t, ctx, pool, dir, 77)
	applyAtlasFilesDirectly(t, ctx, pool, dir, adoptedEinoFirstVersion, adoptedEinoSecondVersion)
	insertGooseHistory(t, ctx, pool, legacyCollisionFirstVersion, legacyCollisionSecondVersion)

	runner := newAtlasRunnerForPool(t, pool)
	db := stdlib.OpenDBFromPool(pool)
	if err := ensureAtlasRevisionStorage(ctx, db); err != nil {
		_ = db.Close()
		t.Fatalf("create revision storage: %v", err)
	}
	allowNonLinear, err := runner.adoptLegacyHistory(ctx, db)
	if closeErr := db.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatalf("adopt collision before simulated crash: %v", err)
	}
	if !allowNonLinear {
		t.Fatal("collision adoption did not request non-linear execution")
	}

	if err := runner.Up(ctx); err != nil {
		t.Fatalf("resume atlas collision adoption: %v", err)
	}
	assertAtlasTerminalState(t, ctx, pool, runner, 92)
}

// TestAtlasAdoptionRejectsUnknownVersion refuses a database whose Goose
// history claims an Atlas-owned version, even though that file exists.
func TestAtlasAdoptionRejectsUnknownVersion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	dsn, cleanup := spikePostgreSQL(t, ctx)
	defer cleanup()
	pool := atlasTestPool(t, ctx, dsn)
	buildGooseStateTo(t, ctx, pool, loadAtlasDirForTest(t), 10)
	insertGooseHistory(t, ctx, pool, legacyGooseMaxVersion+1)

	runner := newAtlasRunnerForPool(t, pool)
	err := runner.Up(ctx)
	if err == nil || !strings.Contains(err.Error(), "MIGRATION_ADOPTION_UNKNOWN_VERSION") {
		t.Fatalf("expected MIGRATION_ADOPTION_UNKNOWN_VERSION, got %v", err)
	}
}

// TestAtlasAdoptionRejectsAmbiguousCollision refuses a collision-shaped
// database whose canonical fingerprint is partially present.
func TestAtlasAdoptionRejectsAmbiguousCollision(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	dsn, cleanup := spikePostgreSQL(t, ctx)
	defer cleanup()
	pool := atlasTestPool(t, ctx, dsn)
	dir := loadAtlasDirForTest(t)
	buildGooseStateTo(t, ctx, pool, dir, 77)
	applyAtlasFilesDirectly(t, ctx, pool, dir, adoptedEinoFirstVersion, adoptedEinoSecondVersion)
	insertGooseHistory(t, ctx, pool, legacyCollisionFirstVersion, legacyCollisionSecondVersion)
	// Partially apply canonical 78: the chat_api_style column exists without
	// its constraint, so the fingerprint reports partial and adoption must
	// refuse rather than guess.
	if _, err := pool.Exec(ctx,
		"ALTER TABLE ops.model_settings_revisions ADD COLUMN chat_api_style text"); err != nil {
		t.Fatalf("corrupt canonical 78 fingerprint: %v", err)
	}

	runner := newAtlasRunnerForPool(t, pool)
	err := runner.Up(ctx)
	if err == nil || !strings.Contains(err.Error(), "MIGRATION_VERSION_COLLISION_FINGERPRINT_AMBIGUOUS") {
		t.Fatalf("expected MIGRATION_VERSION_COLLISION_FINGERPRINT_AMBIGUOUS, got %v", err)
	}
}

// TestAtlasAdoptionEmptyGooseHistory keeps parity with the Goose runner for a
// database whose history table exists without any applied version.
func TestAtlasAdoptionEmptyGooseHistory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	dsn, cleanup := spikePostgreSQL(t, ctx)
	defer cleanup()
	pool := atlasTestPool(t, ctx, dsn)
	insertGooseHistory(t, ctx, pool)

	runner := newAtlasRunnerForPool(t, pool)
	if err := runner.Up(ctx); err != nil {
		t.Fatalf("atlas Up with empty goose history: %v", err)
	}
	assertAtlasTerminalState(t, ctx, pool, runner, 92)
}
