package riveradapter

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

func TestRuntimeNodeWorkerTreatsStaleDeliveryAsSuccess(t *testing.T) {
	runtime := &runtimeWorkerFake{claim: application.ClaimResult{Disposition: application.ClaimDispositionStale}}
	worker := newRuntimeWorkerFixture(t, runtime, runtimeExecutor{output: json.RawMessage(`{"ok":true}`)})
	if err := worker.Work(context.Background(), runtimeRiverJob()); err != nil {
		t.Fatal(err)
	}
	if runtime.completeCalls != 0 || runtime.failCalls != 0 {
		t.Fatalf("complete=%d fail=%d", runtime.completeCalls, runtime.failCalls)
	}
}

func TestRuntimeNodeWorkerCompletesPersistedSuccess(t *testing.T) {
	runtime := &runtimeWorkerFake{claim: claimedRuntimeResult(), complete: application.DeliveryTransitionResult{}}
	worker := newRuntimeWorkerFixture(t, runtime, runtimeExecutor{output: json.RawMessage(`{"ok":true}`)})
	if err := worker.Work(context.Background(), runtimeRiverJob()); err != nil {
		t.Fatal(err)
	}
	if runtime.completeCalls != 1 || runtime.failCalls != 0 || runtime.completeCommand.Binding.DeliveryID != "job-41-attempt-1" || runtime.completeCommand.OutputSchemaVersion != 1 {
		t.Fatalf("runtime=%+v", runtime)
	}
}

func TestRuntimeNodeWorkerUsesDeliveryScopedLeaseOwner(t *testing.T) {
	runtime := &runtimeWorkerFake{claim: claimedRuntimeResult()}
	worker := newRuntimeWorkerFixture(t, runtime, runtimeExecutor{output: json.RawMessage(`{"ok":true}`)})
	first := runtimeRiverJob()
	if err := worker.Work(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	firstOwner := runtime.claimCommand.LeaseOwner
	second := runtimeRiverJob()
	second.Attempt = 2
	if err := worker.Work(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	secondOwner := runtime.claimCommand.LeaseOwner
	if firstOwner == "" || secondOwner == "" || firstOwner == secondOwner || !strings.Contains(firstOwner, "job-41-attempt-1") || !strings.Contains(secondOwner, "job-41-attempt-2") {
		t.Fatalf("first owner=%q second owner=%q", firstOwner, secondOwner)
	}
}

func TestRuntimeNodeWorkerUsesPersistedModelSettingsRevisionAndFrozenRuntimeOwner(t *testing.T) {
	revision := int64(7)
	instanceID := foundation.ID("a0000000-0000-4000-8000-000000000077")
	claim := claimedRuntimeResult()
	claim.Attempt.ModelSettingsRevision = cloneOptionalInt64(&revision)
	claim.Attempt.ModelRuntimeInstanceID = cloneOptionalID(&instanceID)
	runtime := &runtimeWorkerFake{claim: claim}
	executor := &capturingRuntimeExecutor{output: json.RawMessage(`{"ok":true}`)}
	worker, err := NewRuntimeNodeWorker(runtimeExecutorRegistry(t, executor), runtime, "worker-a", time.Minute, time.Second, RuntimeWorkerOptions{ModelRuntimeInstanceID: &instanceID})
	if err != nil {
		t.Fatal(err)
	}
	revision = 8
	instanceID = foundation.ID("a0000000-0000-4000-8000-000000000078")
	if err := worker.Work(context.Background(), runtimeRiverJob()); err != nil {
		t.Fatal(err)
	}
	if runtime.claimCommand.ModelRuntimeInstanceID == nil || *runtime.claimCommand.ModelRuntimeInstanceID != foundation.ID("a0000000-0000-4000-8000-000000000077") {
		t.Fatalf("claim binding instance=%v", runtime.claimCommand.ModelRuntimeInstanceID)
	}
	if executor.execution.ModelSettingsRevision == nil || *executor.execution.ModelSettingsRevision != 7 {
		t.Fatalf("execution revision=%v", executor.execution.ModelSettingsRevision)
	}
}

func TestRuntimeNodeWorkerUsesNullModelRuntimeBindingForStaticMode(t *testing.T) {
	runtime := &runtimeWorkerFake{claim: claimedRuntimeResult()}
	worker := newRuntimeWorkerFixture(t, runtime, runtimeExecutor{output: json.RawMessage(`{"ok":true}`)})
	if err := worker.Work(context.Background(), runtimeRiverJob()); err != nil {
		t.Fatal(err)
	}
	if runtime.claimCommand.ModelRuntimeInstanceID != nil {
		t.Fatalf("static claim binding instance=%v", runtime.claimCommand.ModelRuntimeInstanceID)
	}
}

func TestRuntimeNodeWorkerAcquiresAttemptExecutorLeaseAndReleasesAfterComplete(t *testing.T) {
	revision := int64(9)
	instanceID := foundation.ID("a0000000-0000-4000-8000-000000000099")
	claim := claimedRuntimeResult()
	claim.Attempt.ModelSettingsRevision = &revision
	claim.Attempt.ModelRuntimeInstanceID = &instanceID
	events := &runtimeLeaseEvents{}
	executor := &runtimeLeaseExecutor{output: json.RawMessage(`{"leased":true}`), onExecute: func() { events.add("execute") }}
	registry := runtimeExecutorRegistry(t, executor)
	lease := &runtimeExecutorLeaseFake{registry: registry, onRelease: func() { events.add("release") }}
	acquirer := &runtimeExecutorAcquirerFake{lease: lease}
	runtime := &runtimeWorkerFake{claim: claim, onComplete: func() {
		events.add("complete")
		if got := lease.releaseCount(); got != 0 {
			t.Errorf("lease released before completion: %d", got)
		}
	}}
	worker, err := NewRuntimeNodeWorker(nil, runtime, "worker-a", time.Minute, time.Second, RuntimeWorkerOptions{RuntimeExecutorAcquirer: acquirer})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Work(context.Background(), runtimeRiverJob()); err != nil {
		t.Fatal(err)
	}
	if calls := acquirer.callCount(); calls != 1 {
		t.Fatalf("acquire calls=%d", calls)
	}
	acquired := acquirer.attempt()
	if acquired.ModelSettingsRevision == nil || *acquired.ModelSettingsRevision != revision || acquired.ModelRuntimeInstanceID == nil || *acquired.ModelRuntimeInstanceID != instanceID {
		t.Fatalf("acquired attempt binding=%+v", acquired)
	}
	if got := lease.releaseCount(); got != 1 {
		t.Fatalf("lease releases=%d", got)
	}
	if got := events.snapshot(); len(got) != 3 || got[0] != "execute" || got[1] != "complete" || got[2] != "release" {
		t.Fatalf("lifecycle=%v", got)
	}
}

func TestRuntimeNodeWorkerHoldsAdmissionAcrossClaimAndAttemptAcquire(t *testing.T) {
	revision := int64(0)
	instanceID := foundation.ID("a0000000-0000-4000-8000-000000000100")
	claim := claimedRuntimeResult()
	claim.Attempt.ModelSettingsRevision = &revision
	claim.Attempt.ModelRuntimeInstanceID = &instanceID
	lease := &runtimeExecutorLeaseFake{registry: runtimeExecutorRegistry(t, runtimeExecutor{})}
	admission := &runtimeExecutorAdmissionFake{lease: lease}
	acquirer := &runtimeExecutorAdmittingFake{admission: admission}
	runtime := &runtimeWorkerFake{claim: claim, onClaim: func() {
		if acquirer.admitCallCount() != 1 || admission.acquireCallCount() != 0 {
			t.Errorf("admission lifecycle at Claim = admit:%d acquire:%d", acquirer.admitCallCount(), admission.acquireCallCount())
		}
	}}
	worker, err := NewRuntimeNodeWorker(nil, runtime, "worker-a", time.Minute, time.Second, RuntimeWorkerOptions{RuntimeExecutorAcquirer: acquirer})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Work(context.Background(), runtimeRiverJob()); err != nil {
		t.Fatal(err)
	}
	if acquirer.directCallCount() != 0 || admission.acquireCallCount() != 1 || admission.releaseCount() != 1 {
		t.Fatalf("admission lifecycle = direct:%d acquire:%d release:%d", acquirer.directCallCount(), admission.acquireCallCount(), admission.releaseCount())
	}
}

func TestRuntimeNodeWorkerDoesNotAcquireForStaleOrClaimError(t *testing.T) {
	acquirer := &runtimeExecutorAcquirerFake{lease: &runtimeExecutorLeaseFake{registry: runtimeExecutorRegistry(t, runtimeExecutor{})}}
	staleRuntime := &runtimeWorkerFake{claim: application.ClaimResult{Disposition: application.ClaimDispositionStale}}
	staleWorker, err := NewRuntimeNodeWorker(nil, staleRuntime, "worker-a", time.Minute, time.Second, RuntimeWorkerOptions{RuntimeExecutorAcquirer: acquirer})
	if err != nil {
		t.Fatal(err)
	}
	if err := staleWorker.Work(context.Background(), runtimeRiverJob()); err != nil {
		t.Fatal(err)
	}
	if got := acquirer.callCount(); got != 0 {
		t.Fatalf("stale acquire calls=%d", got)
	}

	claimErr := errors.New("claim unavailable")
	errorRuntime := &runtimeWorkerFake{claimErr: claimErr}
	errorWorker, err := NewRuntimeNodeWorker(nil, errorRuntime, "worker-a", time.Minute, time.Second, RuntimeWorkerOptions{RuntimeExecutorAcquirer: acquirer})
	if err != nil {
		t.Fatal(err)
	}
	if err := errorWorker.Work(context.Background(), runtimeRiverJob()); !errors.Is(err, claimErr) {
		t.Fatalf("claim error=%v", err)
	}
	if got := acquirer.callCount(); got != 0 {
		t.Fatalf("claim-error acquire calls=%d", got)
	}
}

func TestRuntimeNodeWorkerReleasesLeaseAfterExecutorResolutionFailure(t *testing.T) {
	claim := claimedRuntimeResult()
	registry, err := runtimeContractOnlyRegistry()
	if err != nil {
		t.Fatal(err)
	}
	lease := &runtimeExecutorLeaseFake{registry: registry}
	runtime := &runtimeWorkerFake{claim: claim, onFail: func() {
		if got := lease.releaseCount(); got != 0 {
			t.Errorf("lease released before failure settlement: %d", got)
		}
	}}
	worker, err := NewRuntimeNodeWorker(nil, runtime, "worker-a", time.Minute, time.Second, RuntimeWorkerOptions{RuntimeExecutorAcquirer: &runtimeExecutorAcquirerFake{lease: lease}})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Work(context.Background(), runtimeRiverJob()); err != nil {
		t.Fatal(err)
	}
	if runtime.failCalls != 1 || runtime.completeCalls != 0 {
		t.Fatalf("settlement fail=%d complete=%d", runtime.failCalls, runtime.completeCalls)
	}
	if got := lease.releaseCount(); got != 1 {
		t.Fatalf("lease releases=%d", got)
	}
}

func TestRuntimeNodeWorkerSettlesAcquireFailureWithoutExecuting(t *testing.T) {
	claim := claimedRuntimeResult()
	acquireErr := errors.New("runtime switching")
	executor := &runtimeLeaseExecutor{output: json.RawMessage(`{"must_not_run":true}`)}
	acquirer := &runtimeExecutorAcquirerFake{lease: &runtimeExecutorLeaseFake{registry: runtimeExecutorRegistry(t, executor)}, err: acquireErr}
	runtime := &runtimeWorkerFake{claim: claim}
	worker, err := NewRuntimeNodeWorker(nil, runtime, "worker-a", time.Minute, time.Second, RuntimeWorkerOptions{RuntimeExecutorAcquirer: acquirer})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Work(context.Background(), runtimeRiverJob()); err != nil {
		t.Fatal(err)
	}
	if runtime.completeCalls != 0 || runtime.failCalls != 1 || !errors.Is(runtime.failCommand.Failure.Err, acquireErr) || executor.executions() != 0 {
		t.Fatalf("runtime complete=%d fail=%d executions=%d", runtime.completeCalls, runtime.failCalls, executor.executions())
	}
}

func TestRuntimeNodeWorkerRejectsPersistedArgsWithUnknownFields(t *testing.T) {
	runtime := &runtimeWorkerFake{claim: claimedRuntimeResult()}
	worker := newRuntimeWorkerFixture(t, runtime, runtimeExecutor{})
	job := runtimeRiverJob()
	job.EncodedArgs = []byte(`{"schema_version":1,"node_run_id":"a0000000-0000-4000-8000-000000000001","dispatch_no":1,"credential":"secret"}`)
	if err := worker.Work(context.Background(), job); err == nil {
		t.Fatal("runtime worker accepted persisted args with an unknown secret field")
	}
	if runtime.claimCalls != 0 {
		t.Fatalf("claim calls=%d", runtime.claimCalls)
	}
}

func TestRuntimeNodeWorkerRejectsInvalidTraceMetadataBeforeClaim(t *testing.T) {
	runtime := &runtimeWorkerFake{claim: claimedRuntimeResult()}
	worker := newRuntimeWorkerFixture(t, runtime, runtimeExecutor{})
	job := runtimeRiverJob()
	job.Metadata = []byte(`{"traceparent":"invalid"}`)
	if err := worker.Work(context.Background(), job); err == nil {
		t.Fatal("runtime worker accepted invalid trace metadata")
	}
	if runtime.claimCalls != 0 {
		t.Fatalf("claim calls=%d", runtime.claimCalls)
	}
}

func TestRuntimeNodeWorkerPropagatesTraceAndClaimedCorrelation(t *testing.T) {
	claim := claimedRuntimeResult()
	claim.Attempt.AttemptNo = 3
	claim.Node.DispatchNo = 7
	claim.Node.RetryNo = 2
	runtime := &runtimeWorkerFake{claim: claim}
	executor := &capturingRuntimeExecutor{output: json.RawMessage(`{"ok":true}`)}
	worker := newRuntimeWorkerFixture(t, runtime, executor)
	job := runtimeRiverJob()
	job.Metadata = []byte(`{"traceparent":"00-0123456789abcdef0123456789abcdef-0123456789abcdef-01","river:rescue_count":1}`)
	if err := worker.Work(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	claimTrace, found := observability.TraceContextFromContext(runtime.claimContext)
	if !found || claimTrace.TraceID != "0123456789abcdef0123456789abcdef" {
		t.Fatalf("claim trace=%+v found=%t", claimTrace, found)
	}
	executionTrace, found := observability.TraceContextFromContext(executor.ctx)
	if !found || executionTrace.TraceID != claimTrace.TraceID || executionTrace.SpanID == claimTrace.SpanID {
		t.Fatalf("execution trace=%+v found=%t", executionTrace, found)
	}
	correlation := observability.CorrelationFromContext(executor.ctx)
	if correlation.WorkspaceID != string(claim.Run.WorkspaceID) ||
		correlation.WorkflowRunID != string(claim.Run.ID) ||
		correlation.NodeRunID != string(claim.Node.ID) ||
		correlation.AttemptNo != 3 || correlation.DispatchNo != 7 ||
		correlation.RetryNo != 2 || correlation.RiverJobID != job.ID {
		t.Fatalf("correlation=%+v", correlation)
	}
	if executor.execution.WorkspaceID != claim.Run.WorkspaceID || executor.execution.DefinitionID != claim.Definition.ID ||
		executor.execution.DefinitionVersion != claim.Definition.Version || executor.execution.DefinitionHash != claim.Definition.GraphHash ||
		executor.execution.RunID != claim.Run.ID || executor.execution.NodeKey != claim.Node.NodeKey || executor.execution.NodeRunID != claim.Node.ID ||
		executor.execution.NodeAttemptID != claim.Attempt.ID || executor.execution.NodeVersion != claim.Node.Version {
		t.Fatalf("execution identity=%+v claim=%+v", executor.execution, claim)
	}
}

func TestRuntimeNodeWorkerCreatesConsumerSpanOnlyAfterSuccessfulClaim(t *testing.T) {
	parent := "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"
	tracer := observability.NewMemoryTracer()
	runtime := &runtimeWorkerFake{claim: claimedRuntimeResult()}
	executor := &capturingRuntimeExecutor{output: json.RawMessage(`{"ok":true}`)}
	worker, err := NewRuntimeNodeWorkerWithObservability(
		runtimeExecutorRegistry(t, executor), runtime, "worker-a", time.Minute, time.Second,
		RuntimeWorkerObservability{Tracer: tracer},
	)
	if err != nil {
		t.Fatal(err)
	}
	job := runtimeRiverJob()
	job.Metadata = []byte(`{"traceparent":"` + parent + `"}`)
	if err := worker.Work(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	spans := tracer.Snapshot()
	if len(spans) != 1 {
		t.Fatalf("consumer spans=%+v", spans)
	}
	span := spans[0]
	if span.Operation != "workflow.node.consume" || span.TraceContext.TraceID != "0123456789abcdef0123456789abcdef" || span.ParentSpanID != "0123456789abcdef" {
		t.Fatalf("consumer span=%+v", span)
	}
	if span.Attributes["node_kind"] != application.CanonicalJSONHashNodeKind || span.Attributes["result"] != "success" {
		t.Fatalf("consumer attributes=%+v", span.Attributes)
	}
	executionTrace, found := observability.TraceContextFromContext(executor.ctx)
	if !found || executionTrace.SpanID != span.TraceContext.SpanID {
		t.Fatalf("executor trace=%+v found=%t span=%+v", executionTrace, found, span.TraceContext)
	}

	staleTracer := observability.NewMemoryTracer()
	staleWorker, err := NewRuntimeNodeWorkerWithObservability(
		runtimeExecutorRegistry(t, runtimeExecutor{}),
		&runtimeWorkerFake{claim: application.ClaimResult{Disposition: application.ClaimDispositionStale}},
		"worker-a", time.Minute, time.Second, RuntimeWorkerObservability{Tracer: staleTracer},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := staleWorker.Work(context.Background(), runtimeRiverJob()); err != nil {
		t.Fatal(err)
	}
	if spans := staleTracer.Snapshot(); len(spans) != 0 {
		t.Fatalf("stale delivery created spans: %+v", spans)
	}
}

func TestRuntimeNodeWorkerConsumerSpanUsesCommittedFailureResult(t *testing.T) {
	tracer := observability.NewMemoryTracer()
	runtime := &runtimeWorkerFake{
		claim: claimedRuntimeResult(),
		fail: application.DeliveryTransitionResult{Attempt: domain.NodeAttempt{
			Status: domain.AttemptStatusRetryScheduled, ErrorCode: "DEPENDENCY_BUSY",
		}},
	}
	worker, err := NewRuntimeNodeWorkerWithObservability(
		runtimeExecutorRegistry(t, runtimeExecutor{err: foundation.NewError(foundation.ErrorRetryableFailure, "DEPENDENCY_BUSY", true, errors.New("busy"))}),
		runtime, "worker-a", time.Minute, time.Second, RuntimeWorkerObservability{Tracer: tracer},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Work(context.Background(), runtimeRiverJob()); err != nil {
		t.Fatal(err)
	}
	spans := tracer.Snapshot()
	if len(spans) != 1 || spans[0].Attributes["result"] != "retry" || spans[0].ErrorCode != "DEPENDENCY_BUSY" {
		t.Fatalf("consumer spans=%+v", spans)
	}
}

func TestRuntimeNodeWorkerPersistsExecutorFailure(t *testing.T) {
	runtime := &runtimeWorkerFake{claim: claimedRuntimeResult()}
	worker := newRuntimeWorkerFixture(t, runtime, runtimeExecutor{err: foundation.NewError(foundation.ErrorRetryableFailure, "DEPENDENCY_BUSY", true, errors.New("busy"))})
	if err := worker.Work(context.Background(), runtimeRiverJob()); err != nil {
		t.Fatal(err)
	}
	if runtime.failCalls != 1 || runtime.completeCalls != 0 || runtime.failCommand.Failure.Err == nil {
		t.Fatalf("runtime=%+v", runtime)
	}
}

func TestRuntimeNodeWorkerPersistsHumanWaitOutcome(t *testing.T) {
	runtime := &runtimeWorkerFake{claim: claimedRuntimeResult()}
	executionResult := application.ExecutionResult{HumanWait: &application.HumanWaitResult{TaskID: foundation.ID("a0000000-0000-4000-8000-000000000009"), ExpectedInputSchema: json.RawMessage(`{"type":"object"}`), TargetVersion: 1, ExpiresIn: time.Minute}}
	worker := newRuntimeWorkerFixture(t, runtime, runtimeExecutor{result: executionResult})
	if err := worker.Work(context.Background(), runtimeRiverJob()); err != nil {
		t.Fatal(err)
	}
	if runtime.humanWaitCalls != 1 || runtime.completeCalls != 0 || runtime.failCalls != 0 || runtime.humanWaitCommand.NodeRunID != runtime.claim.Node.ID {
		t.Fatalf("runtime=%+v", runtime)
	}
}

func TestRuntimeNodeWorkerCancelsExecutorAfterHeartbeatLoss(t *testing.T) {
	runtime := &runtimeWorkerFake{claim: claimedRuntimeResult(), heartbeatErr: foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_LEASE_LOST", false, errors.New("lost"))}
	executor := runtimeExecutor{waitForCancel: true}
	registry := runtimeExecutorRegistry(t, executor)
	worker, err := NewRuntimeNodeWorker(registry, runtime, "worker-a", 60*time.Millisecond, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Work(context.Background(), runtimeRiverJob()); err != nil {
		t.Fatal(err)
	}
	if runtime.heartbeatCalls < 1 || runtime.completeCalls != 0 || runtime.failCalls != 0 {
		t.Fatalf("runtime=%+v", runtime)
	}
}

func TestRuntimeNodeWorkerReducesControlCancellationAsCheckpoint(t *testing.T) {
	runtime := &runtimeWorkerFake{claim: claimedRuntimeResult(), heartbeatErr: foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_PAUSE_REQUESTED", false, errors.New("pause"))}
	worker, err := NewRuntimeNodeWorker(runtimeExecutorRegistry(t, runtimeExecutor{waitForCancel: true}), runtime, "worker-a", 60*time.Millisecond, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Work(context.Background(), runtimeRiverJob()); err != nil {
		t.Fatal(err)
	}
	var classified *foundation.Error
	if runtime.failCalls != 1 || !errors.As(runtime.failCommand.Failure.Err, &classified) || classified.Code != "WORKFLOW_CONTROL_CHECKPOINT" {
		t.Fatalf("runtime=%+v failure=%v", runtime, runtime.failCommand.Failure.Err)
	}
}

func TestRuntimeNodeWorkerReturnsTransportErrorAfterHeartbeatDependencyFailure(t *testing.T) {
	runtime := &runtimeWorkerFake{claim: claimedRuntimeResult(), heartbeatErr: foundation.NewError(foundation.ErrorDependencyUnavailable, "WORKFLOW_HEARTBEAT_DATABASE_UNAVAILABLE", true, errors.New("database unavailable"))}
	worker, err := NewRuntimeNodeWorker(runtimeExecutorRegistry(t, runtimeExecutor{waitForCancel: true}), runtime, "worker-a", 60*time.Millisecond, 10*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Work(context.Background(), runtimeRiverJob()); err == nil {
		t.Fatal("heartbeat dependency failure was swallowed")
	}
	if runtime.completeCalls != 0 || runtime.failCalls != 0 {
		t.Fatalf("runtime=%+v", runtime)
	}
}

func TestNewRuntimeNodeWorkerRejectsInvalidCadenceAndTypedNil(t *testing.T) {
	registry := runtimeExecutorRegistry(t, runtimeExecutor{})
	var typedNil *runtimeWorkerFake
	if _, err := NewRuntimeNodeWorker(registry, typedNil, "worker", time.Minute, time.Second); err == nil {
		t.Fatal("typed nil runtime accepted")
	}
	if _, err := NewRuntimeNodeWorker(registry, &runtimeWorkerFake{}, "worker", time.Minute, 20*time.Second); err == nil {
		t.Fatal("heartbeat at lease/3 accepted")
	}
	negativeRevision := int64(-1)
	instanceID := foundation.ID("a0000000-0000-4000-8000-000000000077")
	if _, err := NewRuntimeNodeWorker(registry, &runtimeWorkerFake{}, "worker", time.Minute, time.Second, RuntimeWorkerOptions{ModelSettingsRevision: &negativeRevision, ModelRuntimeInstanceID: &instanceID}); err == nil {
		t.Fatal("negative model settings revision accepted")
	}
	zeroRevision := int64(0)
	if _, err := NewRuntimeNodeWorker(registry, &runtimeWorkerFake{}, "worker", time.Minute, time.Second, RuntimeWorkerOptions{ModelSettingsRevision: &zeroRevision}); err == nil {
		t.Fatal("managed revision without runtime instance accepted")
	}
}

func TestRuntimeNodeWorkerEmitsCommittedMetricsWithoutReplayDuplication(t *testing.T) {
	started := time.Now().Add(-25 * time.Millisecond).UTC()
	ended := started.Add(20 * time.Millisecond)
	claim := claimedRuntimeResult()
	claim.ObservedNodeKind = claim.Node.NodeType
	claim.DuplicateDelivery = true
	claim.LeaseReclaimed = true
	runtime := &runtimeWorkerFake{
		claim: claim,
		complete: application.DeliveryTransitionResult{Attempt: domain.NodeAttempt{
			Status: domain.AttemptStatusSucceeded, StartedAt: started, EndedAt: &ended,
		}},
	}
	metrics := observability.NewMemoryMetrics()
	worker, err := NewRuntimeNodeWorkerWithObservability(runtimeExecutorRegistry(t, runtimeExecutor{output: json.RawMessage(`{"ok":true}`)}), runtime, "worker-a", time.Minute, time.Second, RuntimeWorkerObservability{Metrics: metrics, Queue: "workflow"})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Work(context.Background(), runtimeRiverJob()); err != nil {
		t.Fatal(err)
	}
	counts := map[observability.MetricName]int{}
	for _, measurement := range metrics.Snapshot() {
		counts[measurement.Name]++
	}
	if counts[observability.MetricDuplicateDeliveryTotal] != 1 || counts[observability.MetricLeaseExpiryTotal] != 1 || counts[observability.MetricActiveWorkers] != 2 || counts[observability.MetricNodeDuration] != 1 || counts[observability.MetricNodeResultTotal] != 1 {
		t.Fatalf("metric counts=%v", counts)
	}

	runtime.complete.Replayed = true
	before := len(metrics.Snapshot())
	if err := worker.Work(context.Background(), runtimeRiverJob()); err != nil {
		t.Fatal(err)
	}
	for _, measurement := range metrics.Snapshot()[before:] {
		if measurement.Name == observability.MetricNodeDuration || measurement.Name == observability.MetricNodeResultTotal {
			t.Fatalf("replay emitted terminal metric: %+v", measurement)
		}
	}
}

func TestRuntimeNodeWorkerReportsOnlyAllowlistedFatalInvariant(t *testing.T) {
	fatal := make(chan error, 1)
	runtime := &runtimeWorkerFake{
		claim:        claimedRuntimeResult(),
		heartbeatErr: foundation.NewError(foundation.ErrorConsistencyViolation, "WORKFLOW_HEARTBEAT_RESULT_INVALID", false, errors.New("invalid result")),
	}
	metrics := observability.NewMemoryMetrics()
	worker, err := NewRuntimeNodeWorkerWithObservability(runtimeExecutorRegistry(t, runtimeExecutor{waitForCancel: true}), runtime, "worker-a", 60*time.Millisecond, 10*time.Millisecond, RuntimeWorkerObservability{Metrics: metrics, Queue: "workflow", FatalInvariants: fatal})
	if err != nil {
		t.Fatal(err)
	}
	if err := worker.Work(context.Background(), runtimeRiverJob()); err == nil {
		t.Fatal("fatal heartbeat invariant was swallowed")
	}
	select {
	case reported := <-fatal:
		if stableErrorCode(reported) != "WORKFLOW_HEARTBEAT_RESULT_INVALID" {
			t.Fatalf("fatal=%v", reported)
		}
	default:
		t.Fatal("fatal invariant was not reported")
	}
	foundHeartbeat := false
	for _, measurement := range metrics.Snapshot() {
		if measurement.Name == observability.MetricHeartbeatFailureTotal {
			foundHeartbeat = true
		}
	}
	if !foundHeartbeat {
		t.Fatal("heartbeat failure metric missing")
	}

	nonFatal := make(chan error, 1)
	observer := newRuntimeWorkerObserver(RuntimeWorkerObservability{FatalInvariants: nonFatal})
	observer.reportFatal(foundation.NewError(foundation.ErrorManualRecoveryRequired, "WRITEBACK_EXECUTION_BINDING_CONFLICT", false, errors.New("manual")))
	select {
	case err := <-nonFatal:
		t.Fatalf("manual recovery escalated to fatal: %v", err)
	default:
	}
}

func newRuntimeWorkerFixture(t *testing.T, runtime RuntimeExecutionCoordinator, executor application.Executor) *RuntimeNodeWorker {
	t.Helper()
	worker, err := NewRuntimeNodeWorker(runtimeExecutorRegistry(t, executor), runtime, "worker-a", time.Minute, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	return worker
}

func runtimeExecutorRegistry(t *testing.T, executor application.Executor) *application.ExecutorRegistry {
	t.Helper()
	catalog, err := application.NewValidationCatalog([]int{1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := application.NewExecutorRegistry(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(application.CanonicalJSONHashNodeKind, 1, executor); err != nil {
		t.Fatal(err)
	}
	if err := registry.Freeze(); err != nil {
		t.Fatal(err)
	}
	return registry
}

func runtimeContractOnlyRegistry() (*application.ExecutorRegistry, error) {
	catalog, err := application.NewValidationCatalog([]int{1}, nil)
	if err != nil {
		return nil, err
	}
	registry, err := application.NewExecutorRegistry(catalog)
	if err != nil {
		return nil, err
	}
	if err := registry.RegisterContract(application.CanonicalJSONHashNodeKind, 1); err != nil {
		return nil, err
	}
	if err := registry.Freeze(); err != nil {
		return nil, err
	}
	return registry, nil
}

func runtimeRiverJob() *river.Job[NodeJobArgs] {
	return &river.Job[NodeJobArgs]{JobRow: &rivertype.JobRow{ID: 41, Attempt: 1}, Args: NodeJobArgs{SchemaVersion: NodeJobSchemaVersion, NodeRunID: foundation.ID("a0000000-0000-4000-8000-000000000001"), DispatchNo: 1}}
}

func claimedRuntimeResult() application.ClaimResult {
	graph := domain.CanonicalGraph{Nodes: []domain.NodeDefinition{{Key: "hash", Kind: application.CanonicalJSONHashNodeKind, InputSchemaVersion: 1, OutputSchemaVersion: 1}}}
	graphJSON, _ := json.Marshal(graph)
	graphHash, _ := application.ComputeCanonicalGraphHash(graph)
	return application.ClaimResult{
		Disposition: application.ClaimDispositionClaimed,
		Definition:  domain.Definition{ID: foundation.ID("a0000000-0000-4000-8000-000000000004"), WorkspaceID: foundation.ID("a0000000-0000-4000-8000-000000000003"), Key: "test-workflow", Version: 1, Graph: graphJSON, GraphHash: graphHash},
		Run:         domain.Run{ID: foundation.ID("a0000000-0000-4000-8000-000000000002"), WorkspaceID: foundation.ID("a0000000-0000-4000-8000-000000000003"), DefinitionID: foundation.ID("a0000000-0000-4000-8000-000000000004"), Status: domain.RunStatusRunning},
		Node:        domain.NodeRun{ID: foundation.ID("a0000000-0000-4000-8000-000000000001"), RunID: foundation.ID("a0000000-0000-4000-8000-000000000002"), NodeKey: "hash", NodeType: application.CanonicalJSONHashNodeKind, Status: domain.NodeStatusRunning, InputSchemaVersion: 1, OutputSchemaVersion: 1, DispatchNo: 1, Version: 2, Input: json.RawMessage(`{"value":1}`)},
		Attempt:     domain.NodeAttempt{ID: foundation.ID("a0000000-0000-4000-8000-000000000005"), NodeRunID: foundation.ID("a0000000-0000-4000-8000-000000000001"), AttemptNo: 1, DispatchNo: 1, DeliveryID: "job-41-attempt-1", RiverJobID: 41, RiverJobAttempt: 1, LeaseOwner: "worker-a", Status: domain.AttemptStatusRunning},
	}
}

type runtimeExecutor struct {
	output        json.RawMessage
	result        application.ExecutionResult
	err           error
	waitForCancel bool
}

type runtimeLeaseExecutor struct {
	mu sync.Mutex

	output      json.RawMessage
	onExecute   func()
	executionsN int
}

func (executor *runtimeLeaseExecutor) Execute(context.Context, application.ExecutionContext) (application.ExecutionResult, error) {
	executor.mu.Lock()
	executor.executionsN++
	onExecute := executor.onExecute
	output := append(json.RawMessage(nil), executor.output...)
	executor.mu.Unlock()
	if onExecute != nil {
		onExecute()
	}
	return application.ExecutionResult{Output: output}, nil
}

func (executor *runtimeLeaseExecutor) executions() int {
	executor.mu.Lock()
	defer executor.mu.Unlock()
	return executor.executionsN
}

type capturingRuntimeExecutor struct {
	ctx       context.Context
	execution application.ExecutionContext
	output    json.RawMessage
}

func (e *capturingRuntimeExecutor) Execute(ctx context.Context, execution application.ExecutionContext) (application.ExecutionResult, error) {
	e.ctx = ctx
	e.execution = execution
	return application.ExecutionResult{Output: e.output}, nil
}

func (e runtimeExecutor) Execute(ctx context.Context, _ application.ExecutionContext) (application.ExecutionResult, error) {
	if e.waitForCancel {
		<-ctx.Done()
		return application.ExecutionResult{}, ctx.Err()
	}
	if e.result.HumanWait != nil {
		return e.result, e.err
	}
	return application.ExecutionResult{Output: e.output}, e.err
}

type runtimeWorkerFake struct {
	claim            application.ClaimResult
	claimContext     context.Context
	claimCommand     application.ClaimCommand
	claimCalls       int
	claimErr         error
	onClaim          func()
	heartbeat        application.HeartbeatResult
	heartbeatErr     error
	heartbeatCalls   int
	complete         application.DeliveryTransitionResult
	completeErr      error
	completeCommand  application.CompleteDeliveryCommand
	completeCalls    int
	onComplete       func()
	fail             application.DeliveryTransitionResult
	failErr          error
	failCommand      application.FailDeliveryCommand
	failCalls        int
	onFail           func()
	humanWaitCommand application.HumanWaitTransition
	humanWaitCalls   int
}

func (f *runtimeWorkerFake) Claim(ctx context.Context, command application.ClaimCommand) (application.ClaimResult, error) {
	f.claimCalls++
	f.claimContext = ctx
	f.claimCommand = command
	if f.claim.Disposition == application.ClaimDispositionClaimed {
		f.claim.Attempt.LeaseOwner = command.LeaseOwner
		f.claim.Node.LeaseOwner = command.LeaseOwner
	}
	if f.onClaim != nil {
		f.onClaim()
	}
	return f.claim, f.claimErr
}
func (f *runtimeWorkerFake) Heartbeat(_ context.Context, command application.HeartbeatCommand) (application.HeartbeatResult, error) {
	f.heartbeatCalls++
	if f.heartbeat.Node.ID == "" {
		f.heartbeat = application.HeartbeatResult{Node: domain.NodeRun{ID: command.NodeRunID, Version: command.Fence.NodeVersion + 1, Status: domain.NodeStatusRunning}, Attempt: domain.NodeAttempt{NodeRunID: command.NodeRunID, AttemptNo: command.Fence.AttemptNo, LeaseOwner: command.Fence.Owner, Status: domain.AttemptStatusRunning}}
	}
	return f.heartbeat, f.heartbeatErr
}
func (f *runtimeWorkerFake) Complete(_ context.Context, command application.CompleteDeliveryCommand) (application.DeliveryTransitionResult, error) {
	f.completeCalls++
	f.completeCommand = command
	if f.onComplete != nil {
		f.onComplete()
	}
	return f.complete, f.completeErr
}
func (f *runtimeWorkerFake) Fail(_ context.Context, command application.FailDeliveryCommand) (application.DeliveryTransitionResult, error) {
	f.failCalls++
	f.failCommand = command
	if f.onFail != nil {
		f.onFail()
	}
	return f.fail, f.failErr
}
func (f *runtimeWorkerFake) WaitForHuman(_ context.Context, command application.HumanWaitTransition) (application.HumanTransitionResult, error) {
	f.humanWaitCalls++
	f.humanWaitCommand = command
	return application.HumanTransitionResult{}, nil
}
func (f *runtimeWorkerFake) SubmitHuman(context.Context, application.HumanDecisionTransition) (application.HumanTransitionResult, error) {
	return application.HumanTransitionResult{}, nil
}

var _ application.Executor = runtimeExecutor{}
var _ RuntimeExecutionCoordinator = (*runtimeWorkerFake)(nil)

type runtimeExecutorAcquirerFake struct {
	mu sync.Mutex

	lease       RuntimeExecutorLease
	err         error
	calls       int
	lastAttempt domain.NodeAttempt
}

type runtimeExecutorAdmittingFake struct {
	mu sync.Mutex

	admission   RuntimeExecutorAdmission
	admitCalls  int
	directCalls int
}

func (acquirer *runtimeExecutorAdmittingFake) Admit(context.Context) (RuntimeExecutorAdmission, error) {
	acquirer.mu.Lock()
	defer acquirer.mu.Unlock()
	acquirer.admitCalls++
	return acquirer.admission, nil
}

func (acquirer *runtimeExecutorAdmittingFake) AcquireAttempt(context.Context, domain.NodeAttempt) (RuntimeExecutorLease, error) {
	acquirer.mu.Lock()
	defer acquirer.mu.Unlock()
	acquirer.directCalls++
	return nil, errors.New("direct attempt acquisition bypassed admission")
}

func (acquirer *runtimeExecutorAdmittingFake) admitCallCount() int {
	acquirer.mu.Lock()
	defer acquirer.mu.Unlock()
	return acquirer.admitCalls
}

func (acquirer *runtimeExecutorAdmittingFake) directCallCount() int {
	acquirer.mu.Lock()
	defer acquirer.mu.Unlock()
	return acquirer.directCalls
}

type runtimeExecutorAdmissionFake struct {
	mu sync.Mutex

	lease        RuntimeExecutorLease
	acquireCalls int
	releaseCalls int
	released     bool
}

func (admission *runtimeExecutorAdmissionFake) AcquireAttempt(context.Context, domain.NodeAttempt) (RuntimeExecutorLease, error) {
	admission.mu.Lock()
	defer admission.mu.Unlock()
	admission.acquireCalls++
	if admission.released {
		return nil, errors.New("attempt acquired after admission release")
	}
	return admission.lease, nil
}

func (admission *runtimeExecutorAdmissionFake) Release() {
	admission.mu.Lock()
	defer admission.mu.Unlock()
	if admission.released {
		return
	}
	admission.released = true
	admission.releaseCalls++
}

func (admission *runtimeExecutorAdmissionFake) acquireCallCount() int {
	admission.mu.Lock()
	defer admission.mu.Unlock()
	return admission.acquireCalls
}

func (admission *runtimeExecutorAdmissionFake) releaseCount() int {
	admission.mu.Lock()
	defer admission.mu.Unlock()
	return admission.releaseCalls
}

func (acquirer *runtimeExecutorAcquirerFake) AcquireAttempt(_ context.Context, attempt domain.NodeAttempt) (RuntimeExecutorLease, error) {
	acquirer.mu.Lock()
	defer acquirer.mu.Unlock()
	acquirer.calls++
	acquirer.lastAttempt = attempt
	acquirer.lastAttempt.ModelSettingsRevision = cloneOptionalInt64(attempt.ModelSettingsRevision)
	acquirer.lastAttempt.ModelRuntimeInstanceID = cloneOptionalID(attempt.ModelRuntimeInstanceID)
	return acquirer.lease, acquirer.err
}

func (acquirer *runtimeExecutorAcquirerFake) callCount() int {
	acquirer.mu.Lock()
	defer acquirer.mu.Unlock()
	return acquirer.calls
}

func (acquirer *runtimeExecutorAcquirerFake) attempt() domain.NodeAttempt {
	acquirer.mu.Lock()
	defer acquirer.mu.Unlock()
	attempt := acquirer.lastAttempt
	attempt.ModelSettingsRevision = cloneOptionalInt64(attempt.ModelSettingsRevision)
	attempt.ModelRuntimeInstanceID = cloneOptionalID(attempt.ModelRuntimeInstanceID)
	return attempt
}

type runtimeExecutorLeaseFake struct {
	mu sync.Mutex

	registry     *application.ExecutorRegistry
	onRelease    func()
	releaseCalls int
}

func (lease *runtimeExecutorLeaseFake) Executors() *application.ExecutorRegistry {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	return lease.registry
}

func (lease *runtimeExecutorLeaseFake) Release() {
	lease.mu.Lock()
	lease.releaseCalls++
	first := lease.releaseCalls == 1
	onRelease := lease.onRelease
	lease.mu.Unlock()
	if first && onRelease != nil {
		onRelease()
	}
}

func (lease *runtimeExecutorLeaseFake) releaseCount() int {
	lease.mu.Lock()
	defer lease.mu.Unlock()
	return lease.releaseCalls
}

type runtimeLeaseEvents struct {
	mu     sync.Mutex
	events []string
}

func (events *runtimeLeaseEvents) add(event string) {
	events.mu.Lock()
	defer events.mu.Unlock()
	events.events = append(events.events, event)
}

func (events *runtimeLeaseEvents) snapshot() []string {
	events.mu.Lock()
	defer events.mu.Unlock()
	return append([]string(nil), events.events...)
}

var _ application.Executor = (*runtimeLeaseExecutor)(nil)
var _ RuntimeExecutorAcquirer = (*runtimeExecutorAcquirerFake)(nil)
var _ RuntimeExecutorLease = (*runtimeExecutorLeaseFake)(nil)
