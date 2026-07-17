//go:build integration

package migration

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestRunnerRealPostgreSQLUpRepeatDownAndGuard(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	runner, err := NewRunner(pool, projectmigrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}
	var maxVersion, applied, wrongSchema int
	if err := pool.QueryRow(ctx, `SELECT max(version_id), count(*) FILTER (WHERE is_applied AND version_id > 0) FROM public.goose_db_version`).Scan(&maxVersion, &applied); err != nil {
		t.Fatal(err)
	}
	if maxVersion != 14 || applied != 14 {
		t.Fatalf("project history max=%d applied=%d", maxVersion, applied)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM pg_tables WHERE tablename LIKE 'river_%' AND schemaname <> 'workflow'`).Scan(&wrongSchema); err != nil {
		t.Fatal(err)
	}
	if wrongSchema != 0 {
		t.Fatalf("River tables outside workflow schema=%d", wrongSchema)
	}

	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	annotated, err := NewLegacyAnnotationFS(projectmigrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, annotated, goose.WithTableName(projectMigrationTable))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Down(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	insertRuntimeIdentityFixture(t, ctx, pool)
	// 00014 has no Retrieval data yet, so remove it before exercising the
	// earlier M4-A runtime-identity downgrade guard.
	if _, err := provider.Down(ctx); err != nil {
		t.Fatalf("00014 Down rejected an empty Retrieval schema: %v", err)
	}
	// 00013 has no Proposal→Run bindings yet, so it can be removed before
	// removing 00012 and testing the M4-A runtime-identity guard.
	if _, err := provider.Down(ctx); err != nil {
		t.Fatalf("00013 Down rejected an unbound Proposal schema: %v", err)
	}
	if _, err := provider.Down(ctx); err != nil {
		t.Fatalf("00012 Down rejected legacy-only runtime identity: %v", err)
	}
	if _, err := provider.Down(ctx); err == nil {
		t.Fatal("00011 Down accepted Runtime identity data")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55000" {
			t.Fatalf("guarded Down error=%v", err)
		}
	}
}

func TestRunnerRetrievalMigrationDownRejectsBusinessData(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	runner, err := NewRunner(pool, projectmigrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO retrieval.embedding_version(
    id, provider, adapter_name, adapter_version, model, dimensions,
    normalization, distance_metric, config_hash, created_at
) VALUES (
    '60000000-0000-4000-8000-000000000001', 'test', 'test', 'v1', 'test-model', 3,
    'l2', 'cosine', repeat('1', 64), now()
)`); err != nil {
		t.Fatal(err)
	}
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	annotated, err := NewLegacyAnnotationFS(projectmigrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, annotated, goose.WithTableName(projectMigrationTable))
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Down(ctx)
	var pgErr *pgconn.PgError
	if err == nil || !errors.As(err, &pgErr) || pgErr.Code != "55000" {
		t.Fatalf("00014 Down with Retrieval data error=%v", err)
	}
}

func TestRunnerAdoptsLegacyShellHistory(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	applyLegacyShellMigrations(t, ctx, pool)
	runner, err := NewRunner(pool, projectmigrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Up(ctx); err != nil {
		t.Fatal(err)
	}
	var maxVersion, applied int
	if err := pool.QueryRow(ctx, `SELECT max(version_id), count(*) FILTER (WHERE is_applied AND version_id > 0) FROM public.goose_db_version`).Scan(&maxVersion, &applied); err != nil {
		t.Fatal(err)
	}
	if maxVersion != 14 || applied != 14 {
		t.Fatalf("adopted history max=%d applied=%d", maxVersion, applied)
	}
}

func TestRunnerSerializesConcurrentUp(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	runner, err := NewRunner(pool, projectmigrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	errorsCh := make(chan error, 2)
	for range 2 {
		go func() { errorsCh <- runner.Up(ctx) }()
	}
	for range 2 {
		if err := <-errorsCh; err != nil {
			t.Fatal(err)
		}
	}
}

func TestRunnerWorksWithSingleConnectionPool(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabaseWithMaxConns(t, ctx, 1)
	defer cleanup()
	runner, err := NewRunner(pool, projectmigrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := runner.Up(deadline); err != nil {
		t.Fatalf("single-connection migration failed: %v", err)
	}
}

func TestRawLegacyMigrationsFailGooseParsing(t *testing.T) {
	ctx := context.Background()
	pool, cleanup := newMigrationTestDatabase(t, ctx)
	defer cleanup()
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, projectmigrations.FS, goose.WithTableName(projectMigrationTable))
	if err != nil {
		t.Fatal(err)
	}
	_, err = provider.Up(ctx)
	var pgErr *pgconn.PgError
	if err == nil || !errors.As(err, &pgErr) || pgErr.Code != "42601" || !strings.Contains(err.Error(), "version:2") {
		t.Fatalf("raw legacy Goose error=%v", err)
	}
}

func newMigrationTestDatabase(t *testing.T, ctx context.Context) (*pgxpool.Pool, func()) {
	return newMigrationTestDatabaseWithMaxConns(t, ctx, 0)
}

func newMigrationTestDatabaseWithMaxConns(t *testing.T, ctx context.Context, maxConns int32) (*pgxpool.Pool, func()) {
	t.Helper()
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Fatal("ZHIXU_TEST_DATABASE_URL is required for migration integration tests")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("zhixu_m4a_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	config, err := pgxpool.ParseConfig(parsed.String())
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier)
		admin.Close()
		t.Fatal(err)
	}
	if maxConns > 0 {
		config.MaxConns = maxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier)
		admin.Close()
		t.Fatal(err)
	}
	return pool, func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
	}
}

func applyLegacyShellMigrations(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	names, err := fs.Glob(projectmigrations.FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	for _, name := range names {
		if !isLegacyMigration(name) {
			continue
		}
		content, err := fs.ReadFile(projectmigrations.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		up := legacyUpSection(string(content))
		if _, err := pool.Exec(ctx, up); err != nil {
			t.Fatalf("apply legacy %s: %v", name, err)
		}
	}
}

func legacyUpSection(content string) string {
	start := strings.Index(content, "-- +goose Up")
	if start < 0 {
		return ""
	}
	content = content[start+len("-- +goose Up"):]
	if end := strings.Index(content, "-- +goose Down"); end >= 0 {
		content = content[:end]
	}
	return content
}

func insertRuntimeIdentityFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(ctx, `
INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,created_at,updated_at)
VALUES('10000000-0000-4000-8000-000000000001','m4a','/tmp/m4a','/tmp/m4a',now(),'active',now(),now());
INSERT INTO workflow.definition(id,workspace_id,key,version,graph,created_at)
VALUES('20000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000001','m4a',1,'{}',now());
INSERT INTO workflow.run(id,workspace_id,definition_id,status,input,version,created_at,updated_at,idempotency_key,request_hash)
VALUES('30000000-0000-4000-8000-000000000001','10000000-0000-4000-8000-000000000001','20000000-0000-4000-8000-000000000001','pending','{}',1,now(),now(),'m4a-runtime',repeat('1',64));`)
	if err != nil {
		t.Fatal(err)
	}
}
