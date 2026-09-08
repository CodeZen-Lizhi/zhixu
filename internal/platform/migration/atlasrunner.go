package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"ariga.io/atlas/sql/migrate"
	atlaspostgres "ariga.io/atlas/sql/postgres"
	atlasmigrations "github.com/CodeZen-Lizhi/zhixu/atlas"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

// atlasOperatorVersion records which executor lineage wrote a revision.
const atlasOperatorVersion = "zhixu-atlas/1"

// AtlasRunner applies the project Atlas migration directory first and the River
// schema second while the process-wide migration advisory lock is held by a
// dedicated session. It replaces the Goose-based Runner for TODO3.
type AtlasRunner struct {
	pool        *pgxpool.Pool
	dir         *migrate.MemDir
	riverSchema string
}

// NewAtlasRunner constructs the Atlas migration runner over the project pool.
func NewAtlasRunner(pool *pgxpool.Pool, dir *migrate.MemDir) (*AtlasRunner, error) {
	if pool == nil {
		return nil, errors.New("migration pool is nil")
	}
	if dir == nil {
		return nil, errors.New("atlas migration directory is nil")
	}
	return &AtlasRunner{pool: pool, dir: dir, riverSchema: "workflow"}, nil
}

// Up adopts any legacy Goose history, applies all pending project migrations,
// then migrates and validates River, all under the shared advisory lock.
func (r *AtlasRunner) Up(ctx context.Context) (retErr error) {
	if r == nil || r.pool == nil || r.dir == nil || r.riverSchema == "" {
		return errors.New("migration runner is not initialized")
	}
	if ctx == nil {
		return errors.New("migration context is nil")
	}
	lockConnection, err := openMigrationLockConnection(ctx, r.pool)
	if err != nil {
		return fmt.Errorf("acquire migration lock connection: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, lockConnection.Close(context.Background())) }()
	if err := acquireMigrationAdvisoryLock(ctx, lockConnection); err != nil {
		return fmt.Errorf("acquire migration advisory lock: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		var unlocked bool
		unlockErr := lockConnection.QueryRow(unlockCtx,
			"SELECT pg_advisory_unlock(hashtextextended($1, 0))", migrationLockName).Scan(&unlocked)
		if unlockErr == nil && !unlocked {
			unlockErr = errors.New("migration advisory lock was not held during release")
		}
		retErr = errors.Join(retErr, unlockErr)
	}()

	sqlDB := stdlib.OpenDBFromPool(r.pool)
	defer func() { retErr = errors.Join(retErr, sqlDB.Close()) }()
	if err := r.applyProjectMigrations(ctx, sqlDB); err != nil {
		return err
	}
	return migrateRiver(ctx, r.pool, r.riverSchema)
}

// applyProjectMigrations adopts legacy state and executes every pending file,
// wrapping each file in its own transaction unless the file opts out with an
// atlas:txmode none directive (required for CREATE INDEX CONCURRENTLY).
func (r *AtlasRunner) applyProjectMigrations(ctx context.Context, sqlDB *sql.DB) error {
	if err := ensureAtlasRevisionStorage(ctx, sqlDB); err != nil {
		return err
	}
	allowOutOfOrder, err := r.adoptLegacyHistory(ctx, sqlDB)
	if err != nil {
		return err
	}
	options := []migrate.ExecutorOption{migrate.WithOperatorVersion(atlasOperatorVersion)}
	if allowOutOfOrder {
		// The Eino collision adoption intentionally leaves versions 80-82 pending
		// while 83/84 are already recorded.
		options = append(options, migrate.WithExecOrder(migrate.ExecOrderNonLinear))
	}
	return applyAtlasPending(ctx, sqlDB, r.dir, 0, options)
}

// applyAtlasPending executes pending migration files whose numeric version does
// not exceed targetVersion (non-positive means unbounded). Each file runs in
// its own transaction unless it declares atlas:txmode none.
func applyAtlasPending(ctx context.Context, sqlDB *sql.DB, dir *migrate.MemDir, targetVersion int64, options []migrate.ExecutorOption) error {
	driver, err := atlaspostgres.Open(sqlDB)
	if err != nil {
		return fmt.Errorf("open atlas postgres driver: %w", err)
	}
	probe, err := migrate.NewExecutor(driver, dir, newAtlasRevisionStore(sqlDB), options...)
	if err != nil {
		return fmt.Errorf("create atlas executor: %w", err)
	}
	pending, err := probe.Pending(ctx)
	if errors.Is(err, migrate.ErrNoPendingFiles) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("compute pending atlas migrations: %w", err)
	}
	for _, file := range pending {
		if targetVersion > 0 {
			fileVersion, ok := parseMigrationFileVersion(file.Name())
			if !ok {
				return fmt.Errorf("migration file %s has no numeric version", file.Name())
			}
			if fileVersion > targetVersion {
				break
			}
		}
		if err := executeAtlasFile(ctx, sqlDB, driver, dir, file, options); err != nil {
			return err
		}
	}
	return nil
}

// executeAtlasFile runs one migration file atomically unless it declares
// atlas:txmode none, in which case statements run without a transaction so
// CREATE INDEX CONCURRENTLY stays legal and partial progress can resume.
func executeAtlasFile(ctx context.Context, sqlDB *sql.DB, driver migrate.Driver, dir *migrate.MemDir, file migrate.File, options []migrate.ExecutorOption) error {
	compatibility, err := proposalRevisionBackfillCompatibility(dir, file)
	if err != nil {
		return err
	}
	if atlasFileTxModeNone(file) {
		executor, err := migrate.NewExecutor(driver, dir, newAtlasRevisionStore(sqlDB), options...)
		if err != nil {
			return fmt.Errorf("create atlas executor for %s: %w", file.Name(), err)
		}
		if err := executor.Execute(ctx, file); err != nil {
			return fmt.Errorf("apply atlas migration %s: %w", file.Name(), err)
		}
		return nil
	}
	tx, err := sqlDB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction for atlas migration %s: %w", file.Name(), err)
	}
	defer func() { _ = tx.Rollback() }()
	if compatibility != nil {
		if err := executeProposalRevisionCompatibility(ctx, tx, compatibility); err != nil {
			return err
		}
	}
	txDriver, err := atlaspostgres.Open(tx)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("open transactional atlas driver for %s: %w", file.Name(), err)
	}
	executor, err := migrate.NewExecutor(txDriver, dir, newAtlasRevisionStore(tx), options...)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("create transactional atlas executor for %s: %w", file.Name(), err)
	}
	if err := executor.Execute(ctx, file); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("apply atlas migration %s: %w", file.Name(), err)
	}
	if compatibility != nil {
		// The same SQL verifies that 00082 restored its permanent guards before
		// either the backfill or its Atlas revision becomes visible.
		if err := executeProposalRevisionCompatibility(ctx, tx, compatibility); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit atlas migration %s: %w", file.Name(), err)
	}
	return nil
}

// atlasFileTxModeNone reports whether the file carries an `atlas:txmode none`
// file-level directive.
func atlasFileTxModeNone(file migrate.File) bool {
	type directiveFile interface {
		Directive(name string) []string
	}
	df, ok := file.(directiveFile)
	if !ok {
		return false
	}
	for _, value := range df.Directive("txmode") {
		if value == "none" {
			return true
		}
	}
	return false
}

// MigrateAtlasToVersion applies the embedded Atlas migration directory up to
// and including the given legacy numeric version; a non-positive version
// applies every pending file. It backs migration-focused integration tests
// that must stop at a historical schema state; it intentionally skips legacy
// adoption, the advisory lock, and River, matching the historical Goose
// provider test surface.
func MigrateAtlasToVersion(ctx context.Context, pool *pgxpool.Pool, version int64) error {
	if pool == nil {
		return errors.New("migration pool is nil")
	}
	dir, err := LoadAtlasDir(atlasmigrations.MigrationDir())
	if err != nil {
		return err
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() { _ = sqlDB.Close() }()
	return applyAtlasDirToVersion(ctx, sqlDB, dir, version)
}

// MigrateAtlas runs the full production migration path (adoption, pending
// project migrations, River Up/Validate) against the given pool. It backs
// integration tests that historically constructed the Goose runner directly.
func MigrateAtlas(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return errors.New("migration pool is nil")
	}
	runner, err := NewAtlasEmbeddedRunner(pool)
	if err != nil {
		return err
	}
	return runner.Up(ctx)
}

// NewAtlasEmbeddedRunner constructs the Atlas runner over the embedded
// migration directory, keeping the two-value shape of the retired Goose
// NewRunner for migration-focused integration tests.
func NewAtlasEmbeddedRunner(pool *pgxpool.Pool) (*AtlasRunner, error) {
	if pool == nil {
		return nil, errors.New("migration pool is nil")
	}
	dir, err := LoadAtlasDir(atlasmigrations.MigrationDir())
	if err != nil {
		return nil, err
	}
	return NewAtlasRunner(pool, dir)
}

// applyAtlasDirToVersion executes pending migration files whose numeric
// version does not exceed targetVersion (non-positive means unbounded).
func applyAtlasDirToVersion(ctx context.Context, sqlDB *sql.DB, dir *migrate.MemDir, targetVersion int64) error {
	if err := ensureAtlasRevisionStorage(ctx, sqlDB); err != nil {
		return err
	}
	options := []migrate.ExecutorOption{migrate.WithOperatorVersion(atlasOperatorVersion)}
	return applyAtlasPending(ctx, sqlDB, dir, targetVersion, options)
}
