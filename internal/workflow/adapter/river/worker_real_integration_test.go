//go:build integration

package riveradapter

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/testdb"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
)

func TestRealRiverDeterministicWorkerSmoke(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	fixture := testdb.Require(t, testdb.Config{Availability: testdb.FailWhenUnavailable, MaxConns: 8})
	pool := fixture.Pool().DB()
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
	inserter, err := NewScopedJobInserter(fixture.Pool(), client, NewStaticScopedEnqueueFence())
	if err != nil {
		t.Fatal(err)
	}
	unitOfWork, err := fixture.Pool().UnitOfWork()
	if err != nil {
		t.Fatal(err)
	}
	var receipt JobReceipt
	err = unitOfWork.Within(ctx, foundation.TransactionOptions{}, func(callbackCtx context.Context, scope foundation.TransactionScope) error {
		var insertErr error
		receipt, insertErr = inserter.InsertTx(callbackCtx, scope, args, InsertOptions{})
		return insertErr
	})
	if err != nil {
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
