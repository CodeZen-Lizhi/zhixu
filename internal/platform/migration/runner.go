package migration

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// projectMigrationTable is the retired Goose history table. The Atlas
	// runner reads it once during legacy adoption and migration 00092 drops it.
	projectMigrationTable = "goose_db_version"
	migrationLockName     = "zhixu:migrate"
	// legacyGooseMaxVersion is the last migration ever owned by Goose. Atlas
	// owns 00092 and all later versions, so legacy history must never adopt them.
	legacyGooseMaxVersion = 91
	// legacyMigrationMaxVersion is the last migration covered by the legacy
	// shell-runner core.schema_meta adoption proof.
	legacyMigrationMaxVersion  = 10
	migrationLockRetryInterval = 25 * time.Millisecond
)

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

// openMigrationLockConnection creates a dedicated session outside the
// application pool for the process-wide migration advisory lock.
func openMigrationLockConnection(ctx context.Context, pool *pgxpool.Pool) (*pgx.Conn, error) {
	config := pool.Config()
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

func legacyMetaKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}
