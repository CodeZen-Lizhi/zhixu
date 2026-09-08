//go:build integration

package workflowpostgres

import (
	"context"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	riveradapter "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

func TestRuntimeNodeWorkerExecutesDeterministicDeliveryEndToEnd(t *testing.T) {
	ctx := t.Context()
	platformPool, cleanup := newGORMRuntimeTestDatabase(t, ctx)
	defer cleanup()
	pool := platformPool.DB()
	workspaceID := foundation.ID("a5000000-0000-4000-8000-000000000001")
	if _, err := pool.Exec(ctx, `INSERT INTO core.workspace(id,name,root_path,git_repository_path,git_checked_at,status,version,created_at,updated_at) VALUES($1,'runtime-worker',$2,$2,CURRENT_TIMESTAMP,'active',1,CURRENT_TIMESTAMP,CURRENT_TIMESTAMP)`, string(workspaceID), "/tmp/runtime-worker"); err != nil {
		t.Fatal(err)
	}
	repository, err := NewGORMRuntimeRepositoryWithHooks(platformPool, riveradapter.DefaultOptions(), riveradapter.NewStaticScopedEnqueueFence(), GORMRuntimeRepositoryHooks{})
	if err != nil {
		t.Fatal(err)
	}
	started, err := repository.Start(ctx, runtimeStateStartFixture(workspaceID, "runtime-worker", domain.RetryPolicy{MaxRetries: 0, BaseDelay: time.Millisecond, MaxDelay: time.Second}))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := application.NewValidationCatalog([]int{1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	executors, err := application.NewExecutorRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := executors.Register(application.CanonicalJSONHashNodeKind, 1, application.NewCanonicalJSONHashExecutor()); err != nil {
		t.Fatal(err)
	}
	if err := executors.Freeze(); err != nil {
		t.Fatal(err)
	}
	coordinator, err := application.NewRuntimeCoordinator(repository)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := riveradapter.NewRuntimeNodeWorker(executors, coordinator, "integration-worker", 3*time.Second, 500*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	workers := riveradapter.NewWorkers()
	if err := riveradapter.AddRuntimeWorkerSafely(workers, worker); err != nil {
		t.Fatal(err)
	}
	client, err := riveradapter.NewClient(pool, workers)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.Stop(stopCtx)
	}()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		var runStatus, nodeStatus string
		if err := pool.QueryRow(ctx, `SELECT r.status,n.status FROM workflow.run r JOIN workflow.node_run n ON n.run_id=r.id WHERE r.id=$1 AND n.id=$2`, string(started.Run.ID), string(started.FirstNode.ID)).Scan(&runStatus, &nodeStatus); err != nil {
			t.Fatal(err)
		}
		if runStatus == string(domain.RunStatusSucceeded) && nodeStatus == string(domain.NodeStatusSucceeded) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("runtime worker did not complete run=%s node=%s", started.Run.ID, started.FirstNode.ID)
}
