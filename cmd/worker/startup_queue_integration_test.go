//go:build integration

package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	workflowruntime "github.com/CodeZen-Lizhi/zhixu/internal/workflow/runtime"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

func TestStartWorkerRuntimeFreshQueueStartsBeforeResume(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a PostgreSQL admin database")
	}
	pool := newMigratedWorkerTestPool(t, baseURL)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	options := riveradapter.DefaultOptions()
	options.Queue = "startup_queue_fresh"
	workers := riveradapter.NewWorkers()
	if err := riveradapter.AddWorkerSafely(workers, &startupQueueProbeWorker{}); err != nil {
		t.Fatal(err)
	}
	client, err := riveradapter.NewClientWithOptions(pool, workers, options)
	if err != nil {
		t.Fatal(err)
	}
	var queueRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.river_queue WHERE name=$1`, options.Queue).Scan(&queueRows); err != nil {
		t.Fatal(err)
	}
	if queueRows != 0 {
		t.Fatalf("fresh queue rows=%d want=0", queueRows)
	}
	lifecycle, err := newLifecycleController(client, &startupQueueIntegrationDispatcher{})
	if err != nil {
		t.Fatal(err)
	}
	readiness := workflowruntime.NewReadiness()
	if err := startWorkerRuntime(
		ctx, true, 5*time.Second, 5*time.Second,
		lifecycle, client, readiness, startupQueueIntegrationHealth{},
	); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelShutdown()
		if err := lifecycle.Shutdown(shutdownContext, shutdownGraceful); err != nil {
			t.Errorf("shutdown worker runtime: %v", err)
		}
	})

	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.river_queue WHERE name=$1 AND paused_at IS NULL`, options.Queue).Scan(&queueRows); err != nil {
		t.Fatal(err)
	}
	if queueRows != 1 || !readiness.Snapshot().RiverStarted {
		t.Fatalf("ready queue rows=%d readiness=%#v", queueRows, readiness.Snapshot())
	}
}

func TestStartWorkerRuntimeNonTerminalRolloutRejectsMissingPausedQueue(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("ZHIXU_TEST_DATABASE_URL"))
	if baseURL == "" {
		t.Skip("set ZHIXU_TEST_DATABASE_URL to a PostgreSQL admin database")
	}
	pool := newMigratedWorkerTestPool(t, baseURL)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	options := riveradapter.DefaultOptions()
	options.Queue = "startup_queue_missing_paused"
	workers := riveradapter.NewWorkers()
	if err := riveradapter.AddWorkerSafely(workers, &startupQueueProbeWorker{}); err != nil {
		t.Fatal(err)
	}
	client, err := riveradapter.NewClientWithOptions(pool, workers, options)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := newLifecycleController(client, &startupQueueIntegrationDispatcher{})
	if err != nil {
		t.Fatal(err)
	}
	readiness := workflowruntime.NewReadiness()
	err = startWorkerRuntime(
		ctx, false, 5*time.Second, 5*time.Second,
		lifecycle, client, readiness, startupQueueIntegrationHealth{},
	)
	if !errors.Is(err, rivertype.ErrNotFound) {
		t.Fatalf("startup error=%v", err)
	}
	var queueRows int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM workflow.river_queue WHERE name=$1`, options.Queue).Scan(&queueRows); err != nil {
		t.Fatal(err)
	}
	if queueRows != 0 || readiness.Snapshot().RiverStarted || client.Started() {
		t.Fatalf("queue rows=%d readiness=%#v started=%v", queueRows, readiness.Snapshot(), client.Started())
	}
}

type startupQueueProbeArgs struct{}

func (startupQueueProbeArgs) Kind() string { return "startup_queue_probe" }

type startupQueueProbeWorker struct {
	river.WorkerDefaults[startupQueueProbeArgs]
}

func (*startupQueueProbeWorker) Work(context.Context, *river.Job[startupQueueProbeArgs]) error {
	return nil
}

type startupQueueIntegrationDispatcher struct{}

func (*startupQueueIntegrationDispatcher) Start(context.Context) error { return nil }
func (*startupQueueIntegrationDispatcher) Stop(context.Context) error  { return nil }

type startupQueueIntegrationHealth struct{}

func (startupQueueIntegrationHealth) Close() error { return nil }
