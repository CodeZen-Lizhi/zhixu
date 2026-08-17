package migration

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

const (
	projectMigrationTable      = "goose_db_version"
	migrationLockName          = "zhixu:migrate"
	migrationLockRetryInterval = 25 * time.Millisecond
)

// Runner applies the project schema first and the River schema second while a
// dedicated PostgreSQL session owns the process-wide migration advisory lock.
type Runner struct {
	pool        *pgxpool.Pool
	projectFS   fs.FS
	riverSchema string
}

// NewRunner constructs the production migration runner.
func NewRunner(pool *pgxpool.Pool, projectFS fs.FS) (*Runner, error) {
	if pool == nil {
		return nil, errors.New("migration pool is nil")
	}
	annotated, err := NewLegacyAnnotationFS(projectFS)
	if err != nil {
		return nil, err
	}
	return &Runner{pool: pool, projectFS: annotated, riverSchema: "workflow"}, nil
}

// Up applies all pending project migrations, adopts a fully verified legacy
// shell-runner database when necessary, then migrates and validates River.
func (r *Runner) Up(ctx context.Context) (retErr error) {
	if r == nil || r.pool == nil || r.projectFS == nil || r.riverSchema == "" {
		return errors.New("migration runner is not initialized")
	}
	if ctx == nil {
		return errors.New("migration context is nil")
	}
	lockConnection, err := r.openLockConnection(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration lock connection: %w", err)
	}
	defer lockConnection.Close(context.Background())
	if err := acquireMigrationAdvisoryLock(ctx, lockConnection); err != nil {
		return fmt.Errorf("acquire migration advisory lock: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, unlockErr := lockConnection.Exec(unlockCtx, "SELECT pg_advisory_unlock(hashtextextended($1, 0))", migrationLockName)
		retErr = errors.Join(retErr, unlockErr)
	}()

	sqlDB := stdlib.OpenDBFromPool(r.pool)
	defer func() { retErr = errors.Join(retErr, sqlDB.Close()) }()
	if err := adoptLegacyProjectHistory(ctx, sqlDB); err != nil {
		return err
	}
	collisionBridgeApplied, err := bridgeMigrationVersionCollision(ctx, sqlDB, r.projectFS)
	if err != nil {
		return err
	}
	providerOptions := []goose.ProviderOption{goose.WithTableName(projectMigrationTable)}
	if collisionBridgeApplied {
		// The bridge adopts 83/84 before normal migration resumes at 80.
		providerOptions = append(providerOptions, goose.WithAllowOutofOrder(true))
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, r.projectFS, providerOptions...)
	if err != nil {
		return fmt.Errorf("create project migration provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply project migrations: %w", err)
	}

	riverMigrator, err := rivermigrate.New(riverpgxv5.New(r.pool), &rivermigrate.Config{Schema: r.riverSchema})
	if err != nil {
		return fmt.Errorf("create River migrator: %w", err)
	}
	if _, err := riverMigrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return fmt.Errorf("apply River migrations: %w", err)
	}
	validation, err := riverMigrator.Validate(ctx, nil)
	if err != nil {
		return fmt.Errorf("validate River migrations: %w", err)
	}
	if validation == nil || !validation.OK {
		return fmt.Errorf("validate River migrations: %s", validationMessage(validation))
	}
	return nil
}

// acquireMigrationAdvisoryLock polls on the dedicated session so every failed
// attempt completes its PostgreSQL statement and releases its MVCC snapshot.
// A blocking pg_advisory_lock statement can otherwise deadlock with CREATE
// INDEX CONCURRENTLY running under the current lock owner.
func acquireMigrationAdvisoryLock(ctx context.Context, connection *pgx.Conn) error {
	retry := time.NewTicker(migrationLockRetryInterval)
	defer retry.Stop()
	for {
		var acquired bool
		if err := connection.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1, 0))", migrationLockName).Scan(&acquired); err != nil {
			return err
		}
		if acquired {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-retry.C:
		}
	}
}

// openLockConnection creates a dedicated session outside the application pool.
// The advisory lock must not consume one of the pool's only connections while
// Goose and River acquire connections for their own work.
func (r *Runner) openLockConnection(ctx context.Context) (*pgx.Conn, error) {
	config := r.pool.Config()
	if config == nil || config.ConnConfig == nil {
		return nil, errors.New("migration pool connection config is nil")
	}
	connConfig := config.ConnConfig.Copy()
	if config.BeforeConnect != nil {
		if err := config.BeforeConnect(ctx, connConfig); err != nil {
			return nil, fmt.Errorf("prepare migration lock connection: %w", err)
		}
	}
	connection, err := pgx.ConnectConfig(ctx, connConfig)
	if err != nil {
		return nil, err
	}
	if config.AfterConnect != nil {
		if err := config.AfterConnect(ctx, connection); err != nil {
			_ = connection.Close(context.Background())
			return nil, fmt.Errorf("initialize migration lock connection: %w", err)
		}
	}
	return connection, nil
}

func adoptLegacyProjectHistory(ctx context.Context, db *sql.DB) error {
	var historyExists bool
	if err := db.QueryRowContext(ctx, "SELECT to_regclass('public."+projectMigrationTable+"') IS NOT NULL").Scan(&historyExists); err != nil {
		return fmt.Errorf("inspect project migration history: %w", err)
	}
	if historyExists {
		var applied int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+projectMigrationTable+" WHERE is_applied AND version_id > 0").Scan(&applied); err != nil {
			return fmt.Errorf("inspect applied project migrations: %w", err)
		}
		if applied > 0 {
			return nil
		}
	}

	var legacyMetaExists bool
	if err := db.QueryRowContext(ctx, "SELECT to_regclass('core.schema_meta') IS NOT NULL").Scan(&legacyMetaExists); err != nil {
		return fmt.Errorf("inspect legacy schema metadata: %w", err)
	}
	if !legacyMetaExists {
		return nil
	}
	expected := map[string]string{
		"foundation":           "m1",
		"workspace_sources":    "m3",
		"workflow":             "m4",
		"change_control":       "m5.1",
		"content_artifact":     "m5-ingestion",
		"ingestion_projection": "m5-parser",
		"write_authorization":  "m5-03",
		"safe_writeback":       "m5-04d",
		"safe_writeback_saga":  "m5-04d",
	}
	rows, err := db.QueryContext(ctx, "SELECT key, value FROM core.schema_meta WHERE key = ANY($1)", legacyMetaKeys(expected))
	if err != nil {
		return fmt.Errorf("read legacy schema metadata: %w", err)
	}
	defer rows.Close()
	found := make(map[string]string, len(expected))
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return fmt.Errorf("scan legacy schema metadata: %w", err)
		}
		found[key] = value
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate legacy schema metadata: %w", err)
	}
	for key, value := range expected {
		if found[key] != value {
			return fmt.Errorf("WORKFLOW_LEGACY_MIGRATION_MISMATCH: schema metadata %s=%q, want %q", key, found[key], value)
		}
	}
	if _, err := goose.EnsureDBVersionContext(ctx, db); err != nil {
		return fmt.Errorf("create project migration history: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin legacy migration history adoption: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for version := 1; version <= legacyMigrationMaxVersion; version++ {
		if _, err := tx.ExecContext(ctx, "INSERT INTO "+projectMigrationTable+" (version_id, is_applied, tstamp) SELECT $1, true, now() WHERE NOT EXISTS (SELECT 1 FROM "+projectMigrationTable+" WHERE version_id = $1 AND is_applied)", version); err != nil {
			return fmt.Errorf("adopt legacy project migration %d: %w", version, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit legacy migration history adoption: %w", err)
	}
	return nil
}

func legacyMetaKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

func validationMessage(result *rivermigrate.ValidateResult) string {
	if result == nil {
		return "empty validation result"
	}
	if len(result.Messages) == 0 {
		return "validation failed without details"
	}
	return strings.Join(result.Messages, "; ")
}
