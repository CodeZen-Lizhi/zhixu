package riveradapter

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

type workerExecutor struct {
	called chan application.ExecutionContext
}

func (e workerExecutor) Execute(_ context.Context, execution application.ExecutionContext) (application.ExecutionResult, error) {
	e.called <- execution
	return application.ExecutionResult{Output: json.RawMessage(`{"ok":true}`)}, nil
}

type workerContextProvider struct{ execution application.ExecutionContext }

func (p workerContextProvider) LoadExecutionContext(context.Context, NodeJobArgs) (application.ExecutionContext, error) {
	return p.execution, nil
}

type nilWorkerContextProvider struct{}

func (*nilWorkerContextProvider) LoadExecutionContext(context.Context, NodeJobArgs) (application.ExecutionContext, error) {
	return application.ExecutionContext{}, nil
}

func TestNodeWorkerResolvesExecutorAndExecutesWithoutWritingBusinessState(t *testing.T) {
	catalog, err := application.NewValidationCatalog([]int{1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := application.NewExecutorRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	called := make(chan application.ExecutionContext, 1)
	if err := registry.Register("deterministic.test", 1, workerExecutor{called: called}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	nodeID := foundation.ID("10000000-0000-4000-8000-000000000001")
	worker, err := NewNodeWorker(registry, workerContextProvider{execution: application.ExecutionContext{
		NodeRunID: nodeID, NodeKind: "deterministic.test", InputSchemaVersion: 1, Input: json.RawMessage(`{"x":1}`), DispatchNo: 3,
	}})
	if err != nil {
		t.Fatal(err)
	}
	args, err := NewNodeJobArgs(nodeID, 3)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Work(context.Background(), &river.Job[NodeJobArgs]{Args: args}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-called:
		if got.NodeKind != "deterministic.test" || got.NodeRunID != nodeID || got.DispatchNo != 3 {
			t.Fatalf("execution context = %+v", got)
		}
	default:
		t.Fatal("executor was not called")
	}
}

func TestNodeWorkerRejectsProviderBindingMismatch(t *testing.T) {
	catalog, _ := application.NewValidationCatalog([]int{1}, nil)
	registry, _ := application.NewExecutorRegistry(catalog)
	if err := registry.Register("deterministic.test", 1, workerExecutor{called: make(chan application.ExecutionContext, 1)}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	worker, err := NewNodeWorker(registry, workerContextProvider{execution: application.ExecutionContext{NodeKind: "deterministic.test", InputSchemaVersion: 1}})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := NewNodeJobArgs(foundation.ID("10000000-0000-4000-8000-000000000001"), 1)
	if err := worker.Work(context.Background(), &river.Job[NodeJobArgs]{Args: args}); err == nil {
		t.Fatal("mismatched provider binding unexpectedly succeeded")
	}
}

func TestNewNodeWorkerRejectsTypedNilProvider(t *testing.T) {
	registry, err := application.NewExecutorRegistry(mustCatalog())
	if err != nil {
		t.Fatal(err)
	}
	var provider *nilWorkerContextProvider
	if _, err := NewNodeWorker(registry, provider); err == nil {
		t.Fatal("typed nil context provider unexpectedly succeeded")
	}
}

func TestNodeWorkerRejectsUnexpectedPersistedSecretOrPathField(t *testing.T) {
	catalog, _ := application.NewValidationCatalog([]int{1}, nil)
	registry, _ := application.NewExecutorRegistry(catalog)
	if err := registry.Register("deterministic.test", 1, workerExecutor{called: make(chan application.ExecutionContext, 1)}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	nodeID := foundation.ID("10000000-0000-4000-8000-000000000001")
	worker, err := NewNodeWorker(registry, workerContextProvider{execution: application.ExecutionContext{NodeRunID: nodeID, DispatchNo: 1, NodeKind: "deterministic.test", InputSchemaVersion: 1}})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := NewNodeJobArgs(nodeID, 1)
	job := &river.Job[NodeJobArgs]{
		Args:   args,
		JobRow: &rivertype.JobRow{EncodedArgs: []byte(`{"schema_version":1,"node_run_id":"10000000-0000-4000-8000-000000000001","dispatch_no":1,"absolute_path":"/tmp/secret","token":"credential"}`)},
	}
	if err := worker.Work(context.Background(), job); err == nil {
		t.Fatal("unexpected secret/path fields were accepted")
	}
}

func TestAddWorkerSafelyRejectsDuplicateKind(t *testing.T) {
	workers := NewWorkers()
	registry, err := application.NewExecutorRegistry(mustCatalog())
	if err != nil {
		t.Fatal(err)
	}
	worker, err := NewNodeWorker(registry, workerContextProvider{})
	if err != nil {
		t.Fatal(err)
	}
	if err := AddWorkerSafely(workers, worker); err != nil {
		t.Fatal(err)
	}
	if err := AddWorkerSafely(workers, worker); err == nil {
		t.Fatal("duplicate worker registration unexpectedly succeeded")
	}
}

func mustCatalog() application.ValidationCatalog {
	catalog, err := application.NewValidationCatalog([]int{1}, nil)
	if err != nil {
		panic(err)
	}
	return catalog
}
