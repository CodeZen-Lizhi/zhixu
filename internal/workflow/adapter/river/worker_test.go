package riveradapter

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
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

type workerContextProvider struct {
	execution application.ExecutionContext
	contexts  chan context.Context
}

func (p workerContextProvider) LoadExecutionContext(ctx context.Context, _ NodeJobArgs) (application.ExecutionContext, error) {
	if p.contexts != nil {
		p.contexts <- ctx
	}
	return p.execution, nil
}

type nilWorkerContextProvider struct{}

func (*nilWorkerContextProvider) LoadExecutionContext(context.Context, NodeJobArgs) (application.ExecutionContext, error) {
	return application.ExecutionContext{}, nil
}

type genericWorkerArgs struct{}

func (genericWorkerArgs) Kind() string { return "workflow_generic_worker_test" }

type genericWorker struct {
	river.WorkerDefaults[genericWorkerArgs]
}

func (*genericWorker) Work(context.Context, *river.Job[genericWorkerArgs]) error { return nil }

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

func TestNodeWorkerPropagatesTraceContextBeforeLoadingExecution(t *testing.T) {
	catalog, err := application.NewValidationCatalog([]int{1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := application.NewExecutorRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register("deterministic.test", 1, workerExecutor{called: make(chan application.ExecutionContext, 1)}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	nodeID := foundation.ID("10000000-0000-4000-8000-000000000001")
	contexts := make(chan context.Context, 1)
	worker, err := NewNodeWorker(registry, workerContextProvider{
		execution: application.ExecutionContext{NodeRunID: nodeID, NodeKind: "deterministic.test", InputSchemaVersion: 1, DispatchNo: 1},
		contexts:  contexts,
	})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := NewNodeJobArgs(nodeID, 1)
	trace, err := observability.WithTraceContext(context.Background(), observability.TraceContext{
		TraceID: "0123456789abcdef0123456789abcdef", SpanID: "0123456789abcdef", TraceFlags: "01",
	})
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := encodeTraceMetadata(trace)
	if err != nil {
		t.Fatal(err)
	}
	job := &river.Job[NodeJobArgs]{Args: args, JobRow: &rivertype.JobRow{Metadata: metadata}}
	if err := worker.Work(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	loaded := <-contexts
	got, ok := observability.TraceContextFromContext(loaded)
	if !ok || got.TraceID != "0123456789abcdef0123456789abcdef" || got.SpanID != "0123456789abcdef" {
		t.Fatalf("loaded trace = %#v, found=%t", got, ok)
	}
}

func TestNodeWorkerRejectsUnknownOrSensitiveTraceMetadataBeforeLoading(t *testing.T) {
	tests := map[string]string{
		"unknown":   `{"credential":"secret"}`,
		"sensitive": `{"traceparent":"00-0123456789abcdef0123456789abcdef-0123456789abcdef-01","path":"/private/file"}`,
		"invalid":   `{"traceparent":"not-a-trace"}`,
	}
	for name, metadata := range tests {
		t.Run(name, func(t *testing.T) {
			catalog, err := application.NewValidationCatalog([]int{1}, nil)
			if err != nil {
				t.Fatal(err)
			}
			registry, err := application.NewExecutorRegistry(catalog)
			if err != nil {
				t.Fatal(err)
			}
			if err := registry.Register("deterministic.test", 1, workerExecutor{called: make(chan application.ExecutionContext, 1)}); err != nil {
				t.Fatal(err)
			}
			if err := registry.Freeze(); err != nil {
				t.Fatal(err)
			}
			nodeID := foundation.ID("10000000-0000-4000-8000-000000000001")
			contexts := make(chan context.Context, 1)
			worker, err := NewNodeWorker(registry, workerContextProvider{
				execution: application.ExecutionContext{NodeRunID: nodeID, NodeKind: "deterministic.test", InputSchemaVersion: 1, DispatchNo: 1},
				contexts:  contexts,
			})
			if err != nil {
				t.Fatal(err)
			}
			args, _ := NewNodeJobArgs(nodeID, 1)
			err = worker.Work(context.Background(), &river.Job[NodeJobArgs]{Args: args, JobRow: &rivertype.JobRow{Metadata: []byte(metadata)}})
			var classified *foundation.Error
			if !errors.As(err, &classified) || classified.Code != "WORKFLOW_RIVER_TRACE_METADATA_INVALID" || classified.Kind != foundation.ErrorNonRetryableFailure {
				t.Fatalf("error = %v", err)
			}
			select {
			case <-contexts:
				t.Fatal("provider loaded execution before rejecting metadata")
			default:
			}
		})
	}
}

func TestNodeWorkerAllowsRiverOwnedRescueMetadataWithoutExposingIt(t *testing.T) {
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
		NodeRunID: nodeID, NodeKind: "deterministic.test", InputSchemaVersion: 1, DispatchNo: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	args, _ := NewNodeJobArgs(nodeID, 1)
	metadata := []byte(`{"river:rescue_count":1}`)
	if err := worker.Work(context.Background(), &river.Job[NodeJobArgs]{Args: args, JobRow: &rivertype.JobRow{Metadata: metadata}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-called:
	default:
		t.Fatal("executor was not called for River-owned rescue metadata")
	}
}

func TestDecodeTraceMetadataIsSharedAcrossTypedWorkers(t *testing.T) {
	ctx, err := DecodeTraceMetadata(context.Background(), []byte(`{"traceparent":"00-0123456789abcdef0123456789abcdef-0123456789abcdef-01","river:rescue_count":2}`))
	if err != nil {
		t.Fatal(err)
	}
	trace, found := observability.TraceContextFromContext(ctx)
	if !found || trace.TraceID != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("trace=%+v found=%t", trace, found)
	}
	if _, err := DecodeTraceMetadata(context.Background(), []byte(`{"credential":"secret"}`)); stableErrorCode(err) != "WORKFLOW_RIVER_TRACE_METADATA_INVALID" {
		t.Fatalf("unknown metadata error=%v", err)
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
	err = AddWorkerSafely(workers, worker)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "WORKFLOW_RIVER_WORKER_DUPLICATE" || classified.Kind != foundation.ErrorVersionConflict {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestAddWorkerSafelyRegistersArbitraryTypedWorker(t *testing.T) {
	workers := NewWorkers()
	worker := &genericWorker{}
	if err := AddWorkerSafely(workers, worker); err != nil {
		t.Fatal(err)
	}
	err := AddWorkerSafely(workers, worker)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "WORKFLOW_RIVER_WORKER_DUPLICATE" || classified.Kind != foundation.ErrorVersionConflict {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestAddWorkerSafelyPreservesNodeWorkerNilErrorCode(t *testing.T) {
	workers := NewWorkers()
	var worker *NodeWorker
	err := AddWorkerSafely(workers, worker)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "WORKFLOW_NODE_WORKER_INVALID" || classified.Kind != foundation.ErrorInvalidInput {
		t.Fatalf("error = %v", err)
	}
}

func TestAddWorkerSafelyRejectsGenericTypedNil(t *testing.T) {
	workers := NewWorkers()
	var worker *genericWorker
	err := AddWorkerSafely(workers, worker)
	var classified *foundation.Error
	if !errors.As(err, &classified) || classified.Code != "WORKFLOW_RIVER_WORKER_INVALID" || classified.Kind != foundation.ErrorInvalidInput {
		t.Fatalf("error = %v", err)
	}
}

func mustCatalog() application.ValidationCatalog {
	catalog, err := application.NewValidationCatalog([]int{1}, nil)
	if err != nil {
		panic(err)
	}
	return catalog
}
