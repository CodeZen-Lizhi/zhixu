//go:build integration

package riveradapter

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRealRiverDeterministicWorkerSmoke(t *testing.T) {
	databaseURL := os.Getenv("ZHIXU_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Fatal("ZHIXU_TEST_DATABASE_URL is required for River integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	catalog, err := application.NewValidationCatalog([]int{1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := application.NewExecutorRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	recorded := make(chan application.ExecutionContext, 1)
	const kind = "deterministic.integration"
	if err := registry.Register(kind, 1, recordingExecutor{recorded: recorded}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	provider := integrationContextProvider{kind: kind}
	workers := NewWorkers()
	worker, err := NewNodeWorker(registry, provider)
	if err != nil {
		t.Fatal(err)
	}
	if err := AddWorkerSafely(workers, worker); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(pool, workers)
	if err != nil {
		t.Fatal(err)
	}
	if client.Schema() != WorkflowSchema {
		t.Fatalf("client schema=%q", client.Schema())
	}
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		_ = client.Stop(stopCtx)
	}()
	args, err := NewNodeJobArgs(foundation.ID("f1000000-0000-4000-8000-000000000001"), 1)
	if err != nil {
		t.Fatal(err)
	}
	inserter, err := NewJobInserter(client)
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
	select {
	case execution := <-recorded:
		if execution.NodeRunID != args.NodeRunID || execution.DispatchNo != args.DispatchNo || execution.NodeKind != kind || string(execution.Input) != `{"value":1}` {
			t.Fatalf("execution=%#v", execution)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

type integrationContextProvider struct{ kind string }

func (p integrationContextProvider) LoadExecutionContext(_ context.Context, args NodeJobArgs) (application.ExecutionContext, error) {
	return application.ExecutionContext{NodeRunID: args.NodeRunID, DispatchNo: args.DispatchNo, NodeKind: p.kind, InputSchemaVersion: 1, Input: json.RawMessage(`{"value":1}`)}, nil
}

type recordingExecutor struct {
	recorded chan<- application.ExecutionContext
}

func (e recordingExecutor) Execute(_ context.Context, execution application.ExecutionContext) (application.ExecutionResult, error) {
	e.recorded <- execution
	return application.ExecutionResult{Output: json.RawMessage(`{"ok":true}`)}, nil
}
