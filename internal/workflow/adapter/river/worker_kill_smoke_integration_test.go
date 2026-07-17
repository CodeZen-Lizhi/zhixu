//go:build integration

package riveradapter

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	platformmigration "github.com/CodeZen-Lizhi/zhixu/internal/platform/migration"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	projectmigrations "github.com/CodeZen-Lizhi/zhixu/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const riverKillHelperEnvironment = "ZHIXU_RIVER_KILL_HELPER"

func TestRiverSIGKILLRescueSmoke(t *testing.T) {
	if os.Getenv(riverKillHelperEnvironment) == "1" {
		runRiverKillHelper(t)
		return
	}
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("ZHIXU_TEST_DATABASE_URL is required for River integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool, isolatedDatabaseURL, cleanup := newRiverKillSmokeDatabase(t, ctx, databaseURL)
	defer cleanup()

	queue := "kill_smoke_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	options := killSmokeOptions(queue)
	insertClient, err := NewClientWithOptions(pool, nil, options)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := NewJobInserter(insertClient)
	if err != nil {
		t.Fatal(err)
	}
	nodeRunID, err := foundation.NewUUIDGenerator(nil).New()
	if err != nil {
		t.Fatal(err)
	}
	args, err := NewNodeJobArgs(nodeRunID, 1)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := inserter.InsertTx(ctx, tx, args, InsertOptions{})
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM workflow.river_job WHERE id=$1`, receipt.JobID)
	}()

	marker := t.TempDir() + "/started"
	command := exec.Command(os.Args[0], "-test.run=^TestRiverSIGKILLRescueSmoke$")
	command.Env = append(os.Environ(),
		riverKillHelperEnvironment+"=1",
		"ZHIXU_TEST_DATABASE_URL="+isolatedDatabaseURL,
		"ZHIXU_RIVER_KILL_QUEUE="+queue,
		"ZHIXU_RIVER_KILL_MARKER="+marker,
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waitForFile(t, ctx, marker)
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("SIGKILL helper exited successfully")
	}

	var state string
	if err := pool.QueryRow(ctx, `SELECT state FROM workflow.river_job WHERE id=$1`, receipt.JobID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "running" {
		t.Fatalf("job state after SIGKILL=%q", state)
	}
	time.Sleep(3 * time.Second)

	recovered := make(chan application.ExecutionContext, 1)
	recoveryClient := newKillSmokeClient(t, pool, options, recordingExecutor{recorded: recovered})
	if err := recoveryClient.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopContext, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = recoveryClient.Stop(stopContext)
	}()
	monitor := time.NewTicker(5 * time.Second)
	defer monitor.Stop()
	for {
		select {
		case execution := <-recovered:
			if execution.NodeRunID != nodeRunID {
				t.Fatalf("recovered node=%s want=%s", execution.NodeRunID, nodeRunID)
			}
			goto recovered
		case <-monitor.C:
			var attempt int
			var attemptedAt time.Time
			if err := pool.QueryRow(ctx, `SELECT state,attempt,attempted_at FROM workflow.river_job WHERE id=$1`, receipt.JobID).Scan(&state, &attempt, &attemptedAt); err == nil {
				t.Logf("waiting for rescue: state=%s attempt=%d age=%s", state, attempt, time.Since(attemptedAt).Round(time.Second))
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}

recovered:
	deadline := time.NewTicker(100 * time.Millisecond)
	defer deadline.Stop()
	for {
		var attempts int
		if err := pool.QueryRow(ctx, `SELECT state,attempt FROM workflow.river_job WHERE id=$1`, receipt.JobID).Scan(&state, &attempts); err != nil {
			t.Fatal(err)
		}
		if state == "completed" {
			if attempts < 2 {
				t.Fatalf("rescued job attempt=%d", attempts)
			}
			break
		}
		select {
		case <-deadline.C:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
}

func newRiverKillSmokeDatabase(t *testing.T, ctx context.Context, baseURL string) (*pgxpool.Pool, string, func()) {
	t.Helper()
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, baseURL)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("zhixu_river_kill_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{name}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	isolatedURL := parsed.String()
	pool, err := pgxpool.New(ctx, isolatedURL)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	runner, err := platformmigration.NewRunner(pool, projectmigrations.FS)
	if err != nil {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	if err := runner.Up(ctx); err != nil {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
		t.Fatal(err)
	}
	return pool, isolatedURL, func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP DATABASE "+identifier+" WITH (FORCE)")
		admin.Close()
	}
}

func runRiverKillHelper(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	queue := os.Getenv("ZHIXU_RIVER_KILL_QUEUE")
	marker := os.Getenv("ZHIXU_RIVER_KILL_MARKER")
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	options := killSmokeOptions(queue)
	client := newKillSmokeClient(t, pool, options, blockingKillExecutor{marker: marker})
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	select {}
}

func newKillSmokeClient(t *testing.T, pool *pgxpool.Pool, options Options, executor application.Executor) *Client {
	t.Helper()
	catalog, err := application.NewValidationCatalog([]int{1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := application.NewExecutorRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	const kind = "river.kill.smoke"
	if err := registry.Register(kind, 1, executor); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	worker, err := NewNodeWorker(registry, integrationContextProvider{kind: kind})
	if err != nil {
		t.Fatal(err)
	}
	workers := NewWorkers()
	if err := AddWorkerSafely(workers, worker); err != nil {
		t.Fatal(err)
	}
	client, err := NewClientWithOptions(pool, workers, options)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

type blockingKillExecutor struct{ marker string }

func (executor blockingKillExecutor) Execute(context.Context, application.ExecutionContext) (application.ExecutionResult, error) {
	if err := os.WriteFile(executor.marker, []byte("started"), 0o600); err != nil {
		return application.ExecutionResult{}, err
	}
	select {}
}

func waitForFile(t *testing.T, ctx context.Context, path string) {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal(fmt.Errorf("wait for SIGKILL helper marker: %w", ctx.Err()))
		}
	}
}

func killSmokeOptions(queue string) Options {
	return Options{
		Queue: queue, MaxWorkers: 1, JobTimeout: time.Second,
		RescueStuckJobsAfter: 2 * time.Second, SoftStopTimeout: time.Second,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

var _ application.Executor = blockingKillExecutor{}
