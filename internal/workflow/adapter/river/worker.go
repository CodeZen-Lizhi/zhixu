package riveradapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/riverqueue/river"
)

// ExecutionContextProvider loads the immutable workflow facts needed to
// execute a delivered node. Implementations normally read PostgreSQL state;
// this seam keeps River transport details out of the workflow application.
type ExecutionContextProvider interface {
	LoadExecutionContext(context.Context, NodeJobArgs) (application.ExecutionContext, error)
}

// NodeWorker resolves a project executor and invokes it. It never claims,
// completes, or otherwise mutates workflow business state.
type NodeWorker struct {
	river.WorkerDefaults[NodeJobArgs]
	registry *application.ExecutorRegistry
	provider ExecutionContextProvider
}

// NewNodeWorker constructs a typed worker over the immutable executor registry.
func NewNodeWorker(registry *application.ExecutorRegistry, provider ExecutionContextProvider) (*NodeWorker, error) {
	if registry == nil || isNilExecutionContextProvider(provider) {
		return nil, jobError(foundation.ErrorDependencyUnavailable, "WORKFLOW_NODE_WORKER_DEPENDENCY_MISSING", errors.New("executor registry or context provider is nil"))
	}
	return &NodeWorker{registry: registry, provider: provider}, nil
}

func isNilExecutionContextProvider(provider ExecutionContextProvider) bool {
	if provider == nil {
		return true
	}
	value := reflect.ValueOf(provider)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// Work loads the node facts, resolves its executor, and executes it.
func (w *NodeWorker) Work(ctx context.Context, job *river.Job[NodeJobArgs]) error {
	if w == nil || w.registry == nil || w.provider == nil {
		return jobError(foundation.ErrorDependencyUnavailable, "WORKFLOW_NODE_WORKER_UNAVAILABLE", errors.New("node worker is not initialized"))
	}
	if job == nil {
		return jobError(foundation.ErrorInvalidInput, "WORKFLOW_NODE_JOB_MISSING", errors.New("job is nil"))
	}
	if err := ValidateNodeJobArgs(job.Args); err != nil {
		return err
	}
	if job.JobRow != nil && len(job.EncodedArgs) > 0 {
		if err := validateEncodedNodeJobArgs(job.EncodedArgs, job.Args); err != nil {
			return err
		}
	}
	var metadata []byte
	if job.JobRow != nil {
		metadata = job.Metadata
	}
	ctx, err := decodeTraceMetadata(ctx, metadata)
	if err != nil {
		return err
	}
	execution, err := w.provider.LoadExecutionContext(ctx, job.Args)
	if err != nil {
		return err
	}
	if execution.NodeRunID != job.Args.NodeRunID || execution.DispatchNo != job.Args.DispatchNo {
		return jobError(foundation.ErrorConsistencyViolation, "WORKFLOW_NODE_JOB_BINDING_CONFLICT", errors.New("execution context does not match job identity"))
	}
	if strings.TrimSpace(execution.NodeKind) == "" {
		return jobError(foundation.ErrorNonRetryableFailure, "WORKFLOW_NODE_KIND_MISSING", errors.New("node kind is missing from workflow facts"))
	}
	executor, err := w.registry.Resolve(execution.NodeKind, execution.InputSchemaVersion)
	if err != nil {
		return err
	}
	_, err = executor.Execute(ctx, execution)
	return err
}

func validateEncodedNodeJobArgs(encoded []byte, expected NodeJobArgs) error {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var persisted NodeJobArgs
	if err := decoder.Decode(&persisted); err != nil {
		return jobError(foundation.ErrorNonRetryableFailure, "WORKFLOW_NODE_JOB_PAYLOAD_INVALID", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("node job payload contains multiple JSON values")
		}
		return jobError(foundation.ErrorNonRetryableFailure, "WORKFLOW_NODE_JOB_PAYLOAD_INVALID", err)
	}
	if persisted != expected {
		return jobError(foundation.ErrorConsistencyViolation, "WORKFLOW_NODE_JOB_DECODE_CONFLICT", errors.New("decoded job args differ from persisted payload"))
	}
	return ValidateNodeJobArgs(persisted)
}

// Workers is a River worker bundle owned by this adapter. Callers do not need
// to import River merely to register the project's typed node worker.
type Workers struct{ inner *river.Workers }

var _ river.Worker[NodeJobArgs] = (*NodeWorker)(nil)

// NewWorkers creates an empty worker bundle.
func NewWorkers() *Workers { return &Workers{inner: river.NewWorkers()} }

func (w *Workers) riverWorkers() (*river.Workers, error) {
	if w == nil || w.inner == nil {
		return nil, jobError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RIVER_WORKERS_MISSING", errors.New("worker bundle is nil"))
	}
	return w.inner, nil
}

// AddWorkerSafely registers the typed node worker and reports duplicate-kind
// errors without panicking. Duplicate registration is a readiness failure.
func AddWorkerSafely(workers *Workers, worker *NodeWorker) error {
	inner, err := workers.riverWorkers()
	if err != nil {
		return err
	}
	if worker == nil {
		return jobError(foundation.ErrorInvalidInput, "WORKFLOW_NODE_WORKER_INVALID", errors.New("worker is nil"))
	}
	if err := river.AddWorkerSafely(inner, worker); err != nil {
		return jobError(foundation.ErrorVersionConflict, "WORKFLOW_RIVER_WORKER_DUPLICATE", err)
	}
	return nil
}

func decodeTraceMetadata(ctx context.Context, encoded []byte) (context.Context, error) {
	if len(bytes.TrimSpace(encoded)) == 0 {
		return ctx, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	var raw map[string]json.RawMessage
	if err := decoder.Decode(&raw); err != nil || raw == nil {
		if err == nil {
			err = errors.New("trace metadata must be a JSON object")
		}
		return ctx, traceMetadataError(err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = errors.New("trace metadata contains multiple JSON values")
		}
		return ctx, traceMetadataError(err)
	}
	var value json.RawMessage
	for key, candidate := range raw {
		switch {
		case key == observability.TraceParentMetadataKey:
			value = candidate
		case strings.HasPrefix(key, "river:"):
			// River owns this reserved namespace and may add recovery metadata
			// such as river:rescue_count. It is never copied into application
			// context, logs, metrics, or traces.
			continue
		default:
			return ctx, traceMetadataError(errors.New("trace metadata contains an unknown field"))
		}
	}
	if len(value) == 0 {
		return ctx, nil
	}
	var traceParent string
	if err := json.Unmarshal(value, &traceParent); err != nil {
		return ctx, traceMetadataError(err)
	}
	decoded, err := observability.DecodeTraceMetadata(ctx, map[string]string{observability.TraceParentMetadataKey: traceParent})
	if err != nil {
		return ctx, traceMetadataError(err)
	}
	return decoded, nil
}

func traceMetadataError(cause error) error {
	return foundation.NewError(foundation.ErrorNonRetryableFailure, "WORKFLOW_RIVER_TRACE_METADATA_INVALID", false, fmt.Errorf("trace metadata rejected: %w", cause))
}
