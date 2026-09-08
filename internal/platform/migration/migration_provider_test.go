//go:build integration

package migration

import (
	"context"
	"strconv"
	"testing"

	platformpostgres "github.com/CodeZen-Lizhi/zhixu/internal/platform/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// migrationTestProvider gives migration integration tests a small Atlas-only
// surface for applying a prefix or the complete migration directory.
type migrationTestProvider struct {
	pool *pgxpool.Pool
}

func migrationProvider(t *testing.T, pool *pgxpool.Pool) *migrationTestProvider {
	t.Helper()
	if pool == nil {
		t.Fatal("migration test pool is nil")
	}
	return &migrationTestProvider{pool: pool}
}

// openMigrationRuntimePool switches the migrated fixture to the application's
// shared pool. Callers defer Close after the database cleanup and use DB for
// subsequent seeds/assertions; the migration-only pool is no longer usable.
func openMigrationRuntimePool(t *testing.T, ctx context.Context, pool *pgxpool.Pool) *platformpostgres.Pool {
	t.Helper()
	if pool == nil {
		t.Fatal("migration test pool is nil")
	}
	config := pool.Config()
	pool.Close()
	runtime, err := platformpostgres.Open(ctx, config.ConnString(), config.MaxConns, config.MinConns)
	if err != nil {
		t.Fatalf("open migrated application pool: %v", err)
	}
	return runtime
}

// UpTo applies pending migrations up to and including version.
func (p *migrationTestProvider) UpTo(ctx context.Context, version int64) error {
	return MigrateAtlasToVersion(ctx, p.pool, version)
}

// Up applies every pending migration.
func (p *migrationTestProvider) Up(ctx context.Context) error {
	return MigrateAtlasToVersion(ctx, p.pool, 0)
}

// GetDBVersion returns the highest applied migration version.
func (p *migrationTestProvider) GetDBVersion(ctx context.Context) (int64, error) {
	var version string
	err := p.pool.QueryRow(ctx,
		"SELECT COALESCE(max(version), '0') FROM atlas_schema_revisions.atlas_schema_revisions").Scan(&version)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(version, 10, 64)
}

// assertMigrationVersion asserts the highest applied Atlas revision equals the
// given version.
func assertMigrationVersion(t *testing.T, ctx context.Context, pool *pgxpool.Pool, version int64) {
	t.Helper()
	got, err := migrationProvider(t, pool).GetDBVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != version {
		t.Fatalf("migration version=%d want=%d", got, version)
	}
}
