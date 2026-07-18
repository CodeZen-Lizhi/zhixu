package river

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	workflowriver "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	riverlib "github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

func TestWorkerRejectsPersistedArgsAndTraceMetadataBeforeClaim(t *testing.T) {
	t.Parallel()
	runtime := &workerRuntimeFake{}
	worker := newWorkerTestFixture(t, runtime, processorRunnerFunc(func(context.Context, application.ProcessorRequest) (application.ProcessorResult, error) {
		t.Fatal("invalid transport reached processor")
		return application.ProcessorResult{}, nil
	}), completionRunnerFunc(func(context.Context, application.CompleteReindexRequest) (domain.CompleteReindexResult, error) {
		t.Fatal("invalid transport reached completion")
		return domain.CompleteReindexResult{}, nil
	}), WorkerOptions{})

	job := reindexRiverJob(t)
	job.EncodedArgs = []byte(`{"schema_version":1,"delivery_id":"10000000-0000-4000-8000-000000000001","dispatch_no":2}`)
	if err := worker.Work(context.Background(), job); workerErrorCode(err) != "REINDEX_JOB_DECODE_CONFLICT" {
		t.Fatalf("encoded mismatch error=%v", err)
	}
	job = reindexRiverJob(t)
	job.Metadata = []byte(`{"credential":"secret"}`)
	if err := worker.Work(context.Background(), job); workerErrorCode(err) != "WORKFLOW_RIVER_TRACE_METADATA_INVALID" {
		t.Fatalf("metadata error=%v", err)
	}
	if runtime.claimCalls != 0 {
		t.Fatalf("claim calls=%d", runtime.claimCalls)
	}
}

func TestWorkerTreatsStaleAndCommittedClaimAsTransportSuccess(t *testing.T) {
	t.Parallel()
	for _, disposition := range []application.DeliveryClaimDisposition{application.DeliveryClaimStale, application.DeliveryClaimCommitted} {
		disposition := disposition
		t.Run(string(disposition), func(t *testing.T) {
			t.Parallel()
			runtime := &workerRuntimeFake{claim: application.DeliveryClaimResult{Disposition: disposition}}
			processorCalls := 0
			completionCalls := 0
			worker := newWorkerTestFixture(t, runtime, processorRunnerFunc(func(context.Context, application.ProcessorRequest) (application.ProcessorResult, error) {
				processorCalls++
				return application.ProcessorResult{}, nil
			}), completionRunnerFunc(func(context.Context, application.CompleteReindexRequest) (domain.CompleteReindexResult, error) {
				completionCalls++
				return domain.CompleteReindexResult{}, nil
			}), WorkerOptions{})
			if err := worker.Work(context.Background(), reindexRiverJob(t)); err != nil {
				t.Fatal(err)
			}
			if runtime.claimCalls != 1 || processorCalls != 0 || completionCalls != 0 {
				t.Fatalf("claim=%d processor=%d completion=%d", runtime.claimCalls, processorCalls, completionCalls)
			}
		})
	}
}

func TestWorkerTreatsReadOnlyProcessorStaleAndCommittedAsTransportSuccess(t *testing.T) {
	t.Parallel()
	for _, disposition := range []application.ProcessorDisposition{application.ProcessorStale, application.ProcessorCommitted} {
		disposition := disposition
		t.Run(string(disposition), func(t *testing.T) {
			t.Parallel()
			runtime := newClaimedWorkerRuntime()
			completionCalls := 0
			worker := newWorkerTestFixture(t, runtime, processorRunnerFunc(func(context.Context, application.ProcessorRequest) (application.ProcessorResult, error) {
				return application.ProcessorResult{Disposition: disposition}, nil
			}), completionRunnerFunc(func(context.Context, application.CompleteReindexRequest) (domain.CompleteReindexResult, error) {
				completionCalls++
				return domain.CompleteReindexResult{}, nil
			}), WorkerOptions{})
			if err := worker.Work(context.Background(), reindexRiverJob(t)); err != nil {
				t.Fatal(err)
			}
			if completionCalls != 0 || runtime.failCalls != 0 {
				t.Fatalf("completion=%d fail=%d", completionCalls, runtime.failCalls)
			}
		})
	}
}

func TestWorkerHeartbeatsThroughSharedSessionAndCompletesWithLatestFence(t *testing.T) {
	t.Parallel()
	runtime := newClaimedWorkerRuntime()
	heartbeatObserved := make(chan struct{})
	runtime.heartbeatObserved = heartbeatObserved
	var processorContext context.Context
	processor := processorRunnerFunc(func(ctx context.Context, request application.ProcessorRequest) (application.ProcessorResult, error) {
		processorContext = ctx
		select {
		case <-heartbeatObserved:
		case <-time.After(time.Second):
			t.Fatal("heartbeat was not observed")
		}
		snapshot := request.Lease.Snapshot()
		return application.ProcessorResult{
			Disposition:    application.ProcessorReady,
			WorkspaceID:    runtime.claim.Delivery.WorkspaceID,
			OutboxEventID:  runtime.claim.Delivery.OutboxEventID,
			IndexVersionID: "10000000-0000-4000-8000-000000000009",
			Fence:          snapshot.Fence,
		}, nil
	})
	var completionRequest application.CompleteReindexRequest
	completion := completionRunnerFunc(func(_ context.Context, request application.CompleteReindexRequest) (domain.CompleteReindexResult, error) {
		completionRequest = request
		return domain.CompleteReindexResult{}, nil
	})
	worker := newWorkerTestFixture(t, runtime, processor, completion, WorkerOptions{HeartbeatInterval: time.Millisecond, LeaseDuration: time.Minute})
	job := reindexRiverJob(t)
	job.Metadata = []byte(`{"traceparent":"00-0123456789abcdef0123456789abcdef-0123456789abcdef-01","river:rescue_count":1}`)
	if err := worker.Work(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if runtime.heartbeatCalls < 2 {
		t.Fatalf("heartbeat calls=%d; expected loop heartbeat plus final renewal", runtime.heartbeatCalls)
	}
	if completionRequest.WorkspaceID != runtime.claim.Delivery.WorkspaceID ||
		completionRequest.Fence.DeliveryVersion <= 2 ||
		completionRequest.ActivationIdempotencyKey != "reindex-complete:"+string(runtime.claim.Delivery.OutboxEventID) ||
		completionRequest.ActivationReasonCode != reindexActivationReasonCode {
		t.Fatalf("completion request=%+v", completionRequest)
	}
	trace, found := observability.TraceContextFromContext(processorContext)
	if !found || trace.TraceID != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("trace=%+v found=%t", trace, found)
	}
	correlation := observability.CorrelationFromContext(processorContext)
	if correlation.WorkspaceID != string(runtime.claim.Delivery.WorkspaceID) || correlation.AttemptNo != runtime.claim.Attempt.AttemptNo ||
		correlation.DispatchNo != runtime.claim.Delivery.DispatchNo || correlation.RiverJobID != job.ID {
		t.Fatalf("correlation=%+v", correlation)
	}
}

func TestWorkerReturnsUnknownProcessorAndCompletionOutcomesToRiver(t *testing.T) {
	t.Parallel()
	processorFailure := errors.New("processor transaction outcome unknown")
	runtime := newClaimedWorkerRuntime()
	worker := newWorkerTestFixture(t, runtime, processorRunnerFunc(func(context.Context, application.ProcessorRequest) (application.ProcessorResult, error) {
		return application.ProcessorResult{}, processorFailure
	}), completionRunnerFunc(func(context.Context, application.CompleteReindexRequest) (domain.CompleteReindexResult, error) {
		t.Fatal("processor failure reached completion")
		return domain.CompleteReindexResult{}, nil
	}), WorkerOptions{})
	if err := worker.Work(context.Background(), reindexRiverJob(t)); !errors.Is(err, processorFailure) {
		t.Fatalf("processor error=%v", err)
	}
	if runtime.failCalls != 0 {
		t.Fatalf("unknown processor result was reduced: fail calls=%d", runtime.failCalls)
	}

	completionFailure := errors.New("completion commit outcome unknown")
	runtime = newClaimedWorkerRuntime()
	worker = newWorkerTestFixture(t, runtime, readyProcessor(runtime), completionRunnerFunc(func(context.Context, application.CompleteReindexRequest) (domain.CompleteReindexResult, error) {
		return domain.CompleteReindexResult{}, completionFailure
	}), WorkerOptions{})
	if err := worker.Work(context.Background(), reindexRiverJob(t)); !errors.Is(err, completionFailure) {
		t.Fatalf("completion error=%v", err)
	}
	if runtime.failCalls != 0 {
		t.Fatalf("unknown completion result was reduced: fail calls=%d", runtime.failCalls)
	}
}

func TestWorkerSettlesTypedProcessorFailureAndReturnsTransportSuccess(t *testing.T) {
	t.Parallel()
	runtime := newClaimedWorkerRuntime()
	metrics := observability.NewMemoryMetrics()
	failure := domain.DeliveryFailure{
		Class: domain.DeliveryFailureRetryable, ErrorKind: foundation.ErrorRetryableFailure,
		Code: "REINDEX_INGESTION_TEMPORARY", Summary: "temporary ingestion failure",
	}
	processorFailure := application.NewProcessorFailure(failure, 3*time.Second, errors.New("internal parser failure"))
	worker := newWorkerTestFixture(t, runtime, processorRunnerFunc(func(context.Context, application.ProcessorRequest) (application.ProcessorResult, error) {
		return application.ProcessorResult{}, processorFailure
	}), completionRunnerFunc(func(context.Context, application.CompleteReindexRequest) (domain.CompleteReindexResult, error) {
		t.Fatal("settled failure reached completion")
		return domain.CompleteReindexResult{}, nil
	}), WorkerOptions{Metrics: metrics})
	if err := worker.Work(context.Background(), reindexRiverJob(t)); err != nil {
		t.Fatal(err)
	}
	if runtime.failCalls != 1 || runtime.failure.Failure != failure || runtime.failure.RetryDelay != 3*time.Second {
		t.Fatalf("fail calls=%d command=%+v", runtime.failCalls, runtime.failure)
	}
	metricCounts := map[observability.MetricName]int{}
	for _, measurement := range metrics.Snapshot() {
		metricCounts[measurement.Name]++
	}
	if metricCounts[observability.MetricNodeResultTotal] != 1 || metricCounts[observability.MetricRetryTotal] != 1 {
		t.Fatalf("metric counts=%v", metricCounts)
	}
}

func TestWorkerSettlesTypedCompletionPrerequisiteAndReturnsTransportSuccess(t *testing.T) {
	t.Parallel()
	runtime := newClaimedWorkerRuntime()
	failure := domain.DeliveryFailure{
		Class: domain.DeliveryFailureRetryable, ErrorKind: foundation.ErrorRetryableFailure,
		Code: "REINDEX_COMPLETION_PREREQUISITE_PENDING", Summary: "reindex completion prerequisite is pending",
	}
	completionFailure := application.NewDeliveryFailure(failure, 5*time.Second, errors.New("workflow node is still running"))
	worker := newWorkerTestFixture(t, runtime, readyProcessor(runtime), completionRunnerFunc(func(context.Context, application.CompleteReindexRequest) (domain.CompleteReindexResult, error) {
		return domain.CompleteReindexResult{}, completionFailure
	}), WorkerOptions{})
	if err := worker.Work(context.Background(), reindexRiverJob(t)); err != nil {
		t.Fatal(err)
	}
	if runtime.failCalls != 1 || runtime.failure.Failure != failure || runtime.failure.RetryDelay != 5*time.Second {
		t.Fatalf("fail calls=%d command=%+v", runtime.failCalls, runtime.failure)
	}
}

func TestWorkerStopsAfterHeartbeatLosesLease(t *testing.T) {
	t.Parallel()
	runtime := newClaimedWorkerRuntime()
	runtime.heartbeatDisposition = application.DeliveryMutationStale
	processorCancelled := make(chan struct{})
	worker := newWorkerTestFixture(t, runtime, processorRunnerFunc(func(ctx context.Context, _ application.ProcessorRequest) (application.ProcessorResult, error) {
		<-ctx.Done()
		close(processorCancelled)
		return application.ProcessorResult{}, ctx.Err()
	}), completionRunnerFunc(func(context.Context, application.CompleteReindexRequest) (domain.CompleteReindexResult, error) {
		t.Fatal("stale lease reached completion")
		return domain.CompleteReindexResult{}, nil
	}), WorkerOptions{HeartbeatInterval: time.Millisecond, LeaseDuration: time.Minute})
	if err := worker.Work(context.Background(), reindexRiverJob(t)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-processorCancelled:
	default:
		t.Fatal("processor was not cancelled after stale heartbeat")
	}
}

func TestWorkerRegistersInSharedWorkflowBundleAndReportsFatalTransportInvariant(t *testing.T) {
	t.Parallel()
	fatal := make(chan error, 1)
	worker := newWorkerTestFixture(t, newClaimedWorkerRuntime(), readyProcessor(newClaimedWorkerRuntime()), completionRunnerFunc(func(context.Context, application.CompleteReindexRequest) (domain.CompleteReindexResult, error) {
		return domain.CompleteReindexResult{}, nil
	}), WorkerOptions{FatalInvariants: fatal})
	workers := workflowriver.NewWorkers()
	if err := AddWorkerSafely(workers, worker); err != nil {
		t.Fatal(err)
	}
	if err := AddWorkerSafely(workers, worker); workerErrorCode(err) != "WORKFLOW_RIVER_WORKER_DUPLICATE" {
		t.Fatalf("duplicate registration error=%v", err)
	}
	job := reindexRiverJob(t)
	job.EncodedArgs = []byte(`{"schema_version":1,"delivery_id":"10000000-0000-4000-8000-000000000001","dispatch_no":1,"path":"secret"}`)
	if err := worker.Work(context.Background(), job); err == nil {
		t.Fatal("corrupt persisted payload was accepted")
	}
	select {
	case err := <-fatal:
		if workerErrorCode(err) != "REINDEX_JOB_PAYLOAD_INVALID" {
			t.Fatalf("fatal=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("fatal transport invariant was not reported")
	}
}

func newWorkerTestFixture(t *testing.T, runtime deliveryRuntime, processor application.ProcessorRunner, completion completionRunner, options WorkerOptions) *Worker {
	t.Helper()
	if options.Owner == "" {
		options.Owner = "worker:test"
	}
	if options.LeaseDuration == 0 {
		options.LeaseDuration = time.Minute
	}
	if options.HeartbeatInterval == 0 {
		options.HeartbeatInterval = 10 * time.Millisecond
	}
	worker, err := NewWorker(runtime, processor, completion, options)
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func readyProcessor(runtime *workerRuntimeFake) application.ProcessorRunner {
	return processorRunnerFunc(func(_ context.Context, request application.ProcessorRequest) (application.ProcessorResult, error) {
		snapshot := request.Lease.Snapshot()
		return application.ProcessorResult{
			Disposition:    application.ProcessorReady,
			WorkspaceID:    runtime.claim.Delivery.WorkspaceID,
			OutboxEventID:  runtime.claim.Delivery.OutboxEventID,
			IndexVersionID: "10000000-0000-4000-8000-000000000009",
			Fence:          snapshot.Fence,
		}, nil
	})
}

func reindexRiverJob(t *testing.T) *riverlib.Job[Args] {
	t.Helper()
	args, err := NewArgs("10000000-0000-4000-8000-000000000001", 1)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	return &riverlib.Job[Args]{
		JobRow: &rivertype.JobRow{ID: 41, Attempt: 1, EncodedArgs: encoded},
		Args:   args,
	}
}

func newClaimedWorkerRuntime() *workerRuntimeFake {
	now := time.Now().UTC()
	attemptID := foundation.ID("10000000-0000-4000-8000-000000000005")
	delivery := domain.Delivery{
		ID: "10000000-0000-4000-8000-000000000001", ConsumerName: "retrieval-reindex-v1",
		OutboxEventID: "10000000-0000-4000-8000-000000000002", WorkspaceID: "10000000-0000-4000-8000-000000000003",
		WritebackExecutionID: "10000000-0000-4000-8000-000000000004", Status: domain.DeliveryStatusProcessing,
		DispatchNo: 1, AttemptNo: 1, CurrentAttemptID: &attemptID, Version: 2,
		CreatedAt: now.Add(-time.Minute), UpdatedAt: now,
	}
	attempt := domain.DeliveryAttempt{
		ID: attemptID, DeliveryID: delivery.ID, AttemptNo: 1, DispatchNo: 1, RiverJobID: 41, RiverAttempt: 1,
		DeliveryKey: "reindex-job:41:attempt:1", LeaseOwner: "worker:test:reindex-job:41:attempt:1",
		LeaseUntil: now.Add(time.Minute), Status: domain.DeliveryAttemptProcessing,
		StartedAt: now.Add(-time.Second), HeartbeatAt: now,
	}
	fence := domain.DeliveryFence{
		DeliveryID: delivery.ID, DispatchNo: delivery.DispatchNo, AttemptID: attempt.ID,
		AttemptNo: attempt.AttemptNo, Owner: attempt.LeaseOwner, DeliveryVersion: delivery.Version,
	}
	return &workerRuntimeFake{claim: application.DeliveryClaimResult{
		Disposition: application.DeliveryClaimed, Delivery: delivery, Attempt: attempt, Fence: fence,
	}, heartbeatDisposition: application.DeliveryMutationApplied}
}

type processorRunnerFunc func(context.Context, application.ProcessorRequest) (application.ProcessorResult, error)

func (function processorRunnerFunc) Process(ctx context.Context, request application.ProcessorRequest) (application.ProcessorResult, error) {
	return function(ctx, request)
}

type completionRunnerFunc func(context.Context, application.CompleteReindexRequest) (domain.CompleteReindexResult, error)

func (function completionRunnerFunc) Complete(ctx context.Context, request application.CompleteReindexRequest) (domain.CompleteReindexResult, error) {
	return function(ctx, request)
}

type workerRuntimeFake struct {
	mu                   sync.Mutex
	claim                application.DeliveryClaimResult
	claimCalls           int
	heartbeatCalls       int
	heartbeatDisposition application.DeliveryMutationDisposition
	heartbeatErr         error
	heartbeatObserved    chan struct{}
	heartbeatOnce        sync.Once
	failCalls            int
	failure              application.DeliveryFailureCommand
	failErr              error
}

func (runtime *workerRuntimeFake) Claim(_ context.Context, command application.DeliveryClaimCommand) (application.DeliveryClaimResult, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.claimCalls++
	if runtime.claim.Disposition == application.DeliveryClaimed {
		runtime.claim.Attempt.RiverJobID = command.RiverJobID
		runtime.claim.Attempt.RiverAttempt = command.RiverAttempt
		runtime.claim.Attempt.DeliveryKey = command.DeliveryKey
		runtime.claim.Attempt.LeaseOwner = command.LeaseOwner
		runtime.claim.Fence.Owner = command.LeaseOwner
	}
	return runtime.claim, nil
}

func (runtime *workerRuntimeFake) Heartbeat(_ context.Context, command application.DeliveryHeartbeatCommand) (application.DeliveryMutationResult, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.heartbeatCalls++
	if runtime.heartbeatObserved != nil {
		runtime.heartbeatOnce.Do(func() { close(runtime.heartbeatObserved) })
	}
	if runtime.heartbeatErr != nil {
		return application.DeliveryMutationResult{}, runtime.heartbeatErr
	}
	if runtime.heartbeatDisposition == application.DeliveryMutationStale {
		return application.DeliveryMutationResult{Disposition: application.DeliveryMutationStale}, nil
	}
	runtime.claim.Fence = command.Fence
	runtime.claim.Fence.DeliveryVersion++
	return application.DeliveryMutationResult{Disposition: application.DeliveryMutationApplied, Fence: runtime.claim.Fence}, nil
}

func (runtime *workerRuntimeFake) Checkpoint(_ context.Context, command application.DeliveryCheckpointCommand) (application.DeliveryMutationResult, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.claim.Fence = command.Fence
	runtime.claim.Fence.DeliveryVersion++
	return application.DeliveryMutationResult{Disposition: application.DeliveryMutationApplied, Fence: runtime.claim.Fence}, nil
}

func (runtime *workerRuntimeFake) Fail(_ context.Context, command application.DeliveryFailureCommand) (application.DeliveryMutationResult, error) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.failCalls++
	runtime.failure = command
	if runtime.failErr != nil {
		return application.DeliveryMutationResult{}, runtime.failErr
	}
	return application.DeliveryMutationResult{Disposition: application.DeliveryMutationCommitted}, nil
}

func workerErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}
