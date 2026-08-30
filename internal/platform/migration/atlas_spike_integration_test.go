//go:build integration

package migration

import (
	"context"
	"testing"
	"time"

	"ariga.io/atlas/sql/migrate"
	atlasmigrations "github.com/CodeZen-Lizhi/zhixu/atlas"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
)

func spikePostgreSQL(t *testing.T, ctx context.Context) (string, func()) {
	t.Helper()
	container, err := postgrescontainer.Run(
		ctx,
		"pgvector/pgvector:pg16",
		postgrescontainer.WithDatabase("zhixu"),
		postgrescontainer.WithUsername("postgres"),
		postgrescontainer.WithPassword("zhixu-test"),
		postgrescontainer.BasicWaitStrategies(),
	)
	if err != nil {
		t.Skipf("docker unavailable for spike: %v", err)
	}
	dsn, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("container DSN: %v", err)
	}
	return dsn, func() {
		terminateCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := container.Terminate(terminateCtx); err != nil {
			t.Logf("terminate container: %v", err)
		}
	}
}

// loadAtlasDirForTest loads the committed embedded Atlas migration directory,
// which is the exact artifact the production runner executes.
func loadAtlasDirForTest(t *testing.T) *migrate.MemDir {
	t.Helper()
	dir, err := LoadAtlasDir(atlasmigrations.MigrationDir())
	if err != nil {
		t.Fatalf("load embedded atlas migrations: %v", err)
	}
	return dir
}

// TestAtlasSpikeFullApplyFreshDatabase proves the converted migration directory
// applies end-to-end on a disposable PostgreSQL 16 + pgvector database through
// the in-process Atlas runner, and that a repeat run is a no-op.
func TestAtlasSpikeFullApplyFreshDatabase(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	dsn, cleanup := spikePostgreSQL(t, ctx)
	defer cleanup()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	runner, err := NewAtlasRunner(pool, loadAtlasDirForTest(t))
	if err != nil {
		t.Fatalf("new atlas runner: %v", err)
	}
	if err := runner.Up(ctx); err != nil {
		t.Fatalf("atlas Up: %v", err)
	}

	var revisions int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM atlas_schema_revisions.atlas_schema_revisions").Scan(&revisions); err != nil {
		t.Fatalf("count revisions: %v", err)
	}
	if revisions != 92 {
		t.Fatalf("revisions=%d, want 92", revisions)
	}
	for _, object := range []string{
		"core.workspace",
		"core.schema_meta",
		"agent.model_call",
		"workflow.run",
		"change_control.idx_proposal_workspace_updated_id",
		"learning.idx_learning_review_answer_workspace_created",
	} {
		var exists bool
		if err := pool.QueryRow(ctx, "SELECT (to_regclass($1) IS NOT NULL)", object).Scan(&exists); err != nil {
			t.Fatalf("probe %s: %v", object, err)
		}
		if !exists {
			t.Fatalf("expected object %s missing after atlas apply", object)
		}
	}
	var extension bool
	if err := pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname='vector')").Scan(&extension); err != nil {
		t.Fatalf("probe vector extension: %v", err)
	}
	if !extension {
		t.Fatal("vector extension missing after atlas apply")
	}
	var riverJob bool
	if err := pool.QueryRow(ctx, "SELECT (to_regclass('workflow.river_job') IS NOT NULL)").Scan(&riverJob); err != nil {
		t.Fatalf("probe river_job: %v", err)
	}
	if !riverJob {
		t.Fatal("river_job missing: River migrate did not run after atlas apply")
	}

	if err := runner.Up(ctx); err != nil {
		t.Fatalf("repeat atlas Up must be a no-op: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM atlas_schema_revisions.atlas_schema_revisions").Scan(&revisions); err != nil {
		t.Fatalf("recount revisions: %v", err)
	}
	if revisions != 92 {
		t.Fatalf("revisions after repeat=%d, want 92", revisions)
	}

	assertAtlasTxModeNoneResume(t, ctx, pool)
}

// assertAtlasTxModeNoneResume proves that a non-transactional file records its
// applied statement hashes and resumes after the failed statement on retry.
func assertAtlasTxModeNoneResume(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	file := migrate.NewLocalFile("00093_txmode_resume_probe.sql", []byte(`-- atlas:txmode none

CREATE TABLE public.atlas_txmode_resume_probe (id integer PRIMARY KEY);
INSERT INTO public.atlas_txmode_resume_probe (id) VALUES (1);
INSERT INTO public.atlas_txmode_resume_probe (id)
SELECT 2 FROM public.atlas_txmode_resume_prerequisite;
CREATE INDEX CONCURRENTLY atlas_txmode_resume_probe_id_idx
    ON public.atlas_txmode_resume_probe (id);
`))
	dir := &migrate.MemDir{}
	if err := dir.CopyFiles([]migrate.File{file}); err != nil {
		t.Fatalf("create txmode resume migration directory: %v", err)
	}
	sqlDB := stdlib.OpenDBFromPool(pool)
	defer func() { _ = sqlDB.Close() }()
	options := []migrate.ExecutorOption{migrate.WithOperatorVersion(atlasOperatorVersion)}
	if err := applyAtlasPending(ctx, sqlDB, dir, 0, options); err == nil {
		t.Fatal("first txmode none attempt unexpectedly succeeded")
	}

	var applied, total, partialHashes int
	if err := pool.QueryRow(ctx, `SELECT applied,total,COALESCE(jsonb_array_length(partial_hashes), 0)
		FROM atlas_schema_revisions.atlas_schema_revisions WHERE version='00093'`).Scan(
		&applied, &total, &partialHashes,
	); err != nil {
		t.Fatalf("read partial txmode revision: %v", err)
	}
	if applied != 2 || total != 4 || partialHashes != 2 {
		t.Fatalf("partial txmode revision applied=%d total=%d hashes=%d, want 2/4/2", applied, total, partialHashes)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE public.atlas_txmode_resume_prerequisite (marker boolean NOT NULL);
		INSERT INTO public.atlas_txmode_resume_prerequisite (marker) VALUES (true)`); err != nil {
		t.Fatalf("create txmode resume prerequisite: %v", err)
	}
	if err := applyAtlasPending(ctx, sqlDB, dir, 0, options); err != nil {
		t.Fatalf("resume txmode none migration: %v", err)
	}

	var rows int
	var errorText *string
	if err := pool.QueryRow(ctx, `SELECT applied,total,COALESCE(jsonb_array_length(partial_hashes), 0),error
		FROM atlas_schema_revisions.atlas_schema_revisions WHERE version='00093'`).Scan(
		&applied, &total, &partialHashes, &errorText,
	); err != nil {
		t.Fatalf("read completed txmode revision: %v", err)
	}
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM public.atlas_txmode_resume_probe").Scan(&rows); err != nil {
		t.Fatalf("count txmode resume rows: %v", err)
	}
	if applied != 4 || total != 4 || partialHashes != 0 || errorText != nil || rows != 2 {
		t.Fatalf("completed txmode revision applied=%d total=%d hashes=%d error=%v rows=%d, want 4/4/0/nil/2",
			applied, total, partialHashes, errorText, rows)
	}
}
