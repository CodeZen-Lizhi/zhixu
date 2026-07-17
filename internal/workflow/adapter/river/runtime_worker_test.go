package riveradapter

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
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

func runtimeRiverJob() *river.Job[NodeJobArgs] {
	return &river.Job[NodeJobArgs]{JobRow: &rivertype.JobRow{ID: 41, Attempt: 1}, Args: NodeJobArgs{SchemaVersion: NodeJobSchemaVersion, NodeRunID: foundation.ID("a0000000-0000-4000-8000-000000000001"), DispatchNo: 1}}
}

func claimedRuntimeResult() application.ClaimResult {
	return application.ClaimResult{Disposition: application.ClaimDispositionClaimed, Run: domain.Run{ID: foundation.ID("a0000000-0000-4000-8000-000000000002"), WorkspaceID: foundation.ID("a0000000-0000-4000-8000-000000000003"), Status: domain.RunStatusRunning}, Node: domain.NodeRun{ID: foundation.ID("a0000000-0000-4000-8000-000000000001"), RunID: foundation.ID("a0000000-0000-4000-8000-000000000002"), NodeType: application.CanonicalJSONHashNodeKind, Status: domain.NodeStatusRunning, InputSchemaVersion: 1, OutputSchemaVersion: 1, DispatchNo: 1, Version: 2, Input: json.RawMessage(`{"value":1}`)}, Attempt: domain.NodeAttempt{NodeRunID: foundation.ID("a0000000-0000-4000-8000-000000000001"), AttemptNo: 1, DispatchNo: 1, DeliveryID: "job-41-attempt-1", RiverJobID: 41, RiverJobAttempt: 1, LeaseOwner: "worker-a", Status: domain.AttemptStatusRunning}}
}

type runtimeExecutor struct {
	output        json.RawMessage
	result        application.ExecutionResult
	err           error
	waitForCancel bool
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
	claimErr         error
	heartbeat        application.HeartbeatResult
	heartbeatErr     error
	heartbeatCalls   int
	complete         application.DeliveryTransitionResult
	completeErr      error
	completeCommand  application.CompleteDeliveryCommand
	completeCalls    int
	fail             application.DeliveryTransitionResult
	failErr          error
	failCommand      application.FailDeliveryCommand
	failCalls        int
	humanWaitCommand application.HumanWaitTransition
	humanWaitCalls   int
}

func (f *runtimeWorkerFake) Claim(context.Context, application.ClaimCommand) (application.ClaimResult, error) {
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
	return f.complete, f.completeErr
}
func (f *runtimeWorkerFake) Fail(_ context.Context, command application.FailDeliveryCommand) (application.DeliveryTransitionResult, error) {
	f.failCalls++
	f.failCommand = command
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
