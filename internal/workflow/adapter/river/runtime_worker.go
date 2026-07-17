package riveradapter

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
	"github.com/riverqueue/river"
)

// RuntimeExecutionCoordinator is the project-owned delivery state machine used
// by the River transport adapter.
type RuntimeExecutionCoordinator interface {
	Claim(context.Context, application.ClaimCommand) (application.ClaimResult, error)
	Heartbeat(context.Context, application.HeartbeatCommand) (application.HeartbeatResult, error)
	Complete(context.Context, application.CompleteDeliveryCommand) (application.DeliveryTransitionResult, error)
	Fail(context.Context, application.FailDeliveryCommand) (application.DeliveryTransitionResult, error)
}

type runtimeHumanWaiter interface {
	application.RuntimeHumanStatePort
}

// RuntimeNodeWorker claims, heartbeats, executes, and atomically reduces one
// stable River delivery. River remains transport; PostgreSQL remains truth.
type RuntimeNodeWorker struct {
	river.WorkerDefaults[NodeJobArgs]
	registry          *application.ExecutorRegistry
	runtime           RuntimeExecutionCoordinator
	owner             string
	leaseDuration     time.Duration
	heartbeatInterval time.Duration
}

// NewRuntimeNodeWorker constructs the production state-machine worker.
func NewRuntimeNodeWorker(registry *application.ExecutorRegistry, runtime RuntimeExecutionCoordinator, owner string, leaseDuration, heartbeatInterval time.Duration) (*RuntimeNodeWorker, error) {
	owner = strings.TrimSpace(owner)
	if registry == nil || isNilRuntimeCoordinator(runtime) || owner == "" || len(owner) > 80 || strings.ContainsAny(owner, "\r\n\t/") || leaseDuration <= 0 || heartbeatInterval <= 0 || heartbeatInterval >= leaseDuration/3 {
		return nil, jobError(foundation.ErrorInvalidInput, "WORKFLOW_RUNTIME_WORKER_INVALID", errors.New("runtime worker dependencies or lease cadence are invalid"))
	}
	return &RuntimeNodeWorker{registry: registry, runtime: runtime, owner: owner, leaseDuration: leaseDuration, heartbeatInterval: heartbeatInterval}, nil
}

func isNilRuntimeCoordinator(runtime RuntimeExecutionCoordinator) bool {
	if runtime == nil {
		return true
	}
	value := reflect.ValueOf(runtime)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// Work treats stale deliveries as benign, and returns a River error only when
// the Workflow result transaction is not known to have committed.
func (w *RuntimeNodeWorker) Work(ctx context.Context, job *river.Job[NodeJobArgs]) error {
	if w == nil || w.registry == nil || w.runtime == nil {
		return jobError(foundation.ErrorDependencyUnavailable, "WORKFLOW_RUNTIME_WORKER_UNAVAILABLE", errors.New("runtime worker is not initialized"))
	}
	if job == nil || job.JobRow == nil {
		return jobError(foundation.ErrorInvalidInput, "WORKFLOW_NODE_JOB_MISSING", errors.New("River job row is nil"))
	}
	if err := ValidateNodeJobArgs(job.Args); err != nil {
		return err
	}
	if len(job.EncodedArgs) > 0 {
		if err := validateEncodedNodeJobArgs(job.EncodedArgs, job.Args); err != nil {
			return err
		}
	}
	deliveryID := fmt.Sprintf("job-%d-attempt-%d", job.ID, job.Attempt)
	deliveryOwner := w.owner + ":" + deliveryID
	claim, err := w.runtime.Claim(ctx, application.ClaimCommand{NodeRunID: job.Args.NodeRunID, DispatchNo: job.Args.DispatchNo, DeliveryID: deliveryID, RiverJobID: job.ID, RiverJobAttempt: job.Attempt, LeaseOwner: deliveryOwner, LeaseDuration: w.leaseDuration})
	if err != nil {
		return err
	}
	if claim.Disposition == application.ClaimDispositionStale {
		return nil
	}
	executor, err := w.registry.Resolve(claim.Node.NodeType, claim.Node.InputSchemaVersion)
	if err != nil {
		_, transitionErr := w.runtime.Fail(ctx, application.FailDeliveryCommand{Binding: deliveryBinding(claim, deliveryID), Failure: domain.FailureInput{Err: err}})
		return transitionErr
	}
	executionCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var nodeVersion atomic.Int64
	nodeVersion.Store(claim.Node.Version)
	heartbeatErrors := make(chan error, 1)
	heartbeatDone := make(chan struct{})
	go w.heartbeatLoop(executionCtx, cancel, claim, &nodeVersion, heartbeatErrors, heartbeatDone)
	result, executionErr := executor.Execute(executionCtx, application.ExecutionContext{WorkspaceID: claim.Run.WorkspaceID, RunID: claim.Run.ID, NodeRunID: claim.Node.ID, NodeKind: claim.Node.NodeType, InputSchemaVersion: claim.Node.InputSchemaVersion, AttemptNo: claim.Attempt.AttemptNo, DispatchNo: claim.Node.DispatchNo, RetryNo: claim.Node.RetryNo, LeaseOwner: claim.Attempt.LeaseOwner, Input: claim.Node.Input})
	cancel()
	<-heartbeatDone
	controlRequested := false
	select {
	case heartbeatErr := <-heartbeatErrors:
		if heartbeatErr != nil {
			if isLeaseLostError(heartbeatErr) {
				return nil
			}
			if !isControlRequestedError(heartbeatErr) {
				return heartbeatErr
			}
			controlRequested = true
		}
	default:
	}
	binding := deliveryBinding(claim, deliveryID)
	binding.Fence.NodeVersion = nodeVersion.Load()
	if controlRequested && (executionErr == nil || errors.Is(executionErr, context.Canceled)) {
		_, err = w.runtime.Fail(ctx, application.FailDeliveryCommand{Binding: binding, Failure: domain.FailureInput{Err: foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_CONTROL_CHECKPOINT", false, errors.New("workflow control checkpoint requested"))}})
		return err
	}
	if executionErr != nil {
		_, err = w.runtime.Fail(ctx, application.FailDeliveryCommand{Binding: binding, Failure: domain.FailureInput{Err: executionErr, CancellationProven: errors.Is(executionErr, context.Canceled) && ctx.Err() != nil}})
		return err
	}
	if controlRequested {
		_, err = w.runtime.Fail(ctx, application.FailDeliveryCommand{Binding: binding, Failure: domain.FailureInput{Err: foundation.NewError(foundation.ErrorVersionConflict, "WORKFLOW_CONTROL_CHECKPOINT", false, errors.New("workflow control checkpoint requested"))}})
		return err
	}
	if result.HumanWait != nil {
		if len(result.Output) != 0 {
			return jobError(foundation.ErrorInvalidInput, "WORKFLOW_EXECUTION_RESULT_INVALID", errors.New("executor returned output and human wait together"))
		}
		humanWaiter, ok := w.runtime.(runtimeHumanWaiter)
		if !ok {
			return jobError(foundation.ErrorDependencyUnavailable, "WORKFLOW_HUMAN_RUNTIME_UNAVAILABLE", errors.New("runtime does not support human task outcomes"))
		}
		humanCoordinator, coordinatorErr := application.NewRuntimeHumanCoordinator(humanWaiter)
		if coordinatorErr != nil {
			return coordinatorErr
		}
		_, err = humanCoordinator.WaitForHuman(ctx, application.HumanWaitTransition{TaskID: result.HumanWait.TaskID, RunID: claim.Run.ID, NodeRunID: claim.Node.ID, Fence: domain.LeaseFence{Owner: claim.Attempt.LeaseOwner, AttemptNo: claim.Attempt.AttemptNo, NodeVersion: nodeVersion.Load()}, ExpectedInputSchema: result.HumanWait.ExpectedInputSchema, TargetVersion: result.HumanWait.TargetVersion, ExpiresIn: result.HumanWait.ExpiresIn})
		return err
	}
	_, err = w.runtime.Complete(ctx, application.CompleteDeliveryCommand{Binding: binding, Output: result.Output, OutputSchemaVersion: claim.Node.OutputSchemaVersion})
	return err
}

func isLeaseLostError(err error) bool {
	var classified *foundation.Error
	return errors.As(err, &classified) && classified.Code == "WORKFLOW_LEASE_LOST"
}

func isControlRequestedError(err error) bool {
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		return false
	}
	return classified.Code == "WORKFLOW_PAUSE_REQUESTED" || classified.Code == "WORKFLOW_CANCEL_REQUESTED"
}

func (w *RuntimeNodeWorker) heartbeatLoop(ctx context.Context, cancel context.CancelFunc, claim application.ClaimResult, nodeVersion *atomic.Int64, errorsCh chan<- error, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(w.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			result, err := w.runtime.Heartbeat(ctx, application.HeartbeatCommand{NodeRunID: claim.Node.ID, Fence: domain.LeaseFence{Owner: claim.Attempt.LeaseOwner, AttemptNo: claim.Attempt.AttemptNo, NodeVersion: nodeVersion.Load()}, LeaseDuration: w.leaseDuration})
			if err != nil {
				select {
				case errorsCh <- err:
				default:
				}
				cancel()
				return
			}
			nodeVersion.Store(result.Node.Version)
		}
	}
}

func deliveryBinding(claim application.ClaimResult, deliveryID string) application.DeliveryBinding {
	return application.DeliveryBinding{NodeRunID: claim.Node.ID, DispatchNo: claim.Node.DispatchNo, DeliveryID: deliveryID, Fence: domain.LeaseFence{Owner: claim.Attempt.LeaseOwner, AttemptNo: claim.Attempt.AttemptNo, NodeVersion: claim.Node.Version}}
}

// AddRuntimeWorkerSafely registers the state-machine worker without panic.
func AddRuntimeWorkerSafely(workers *Workers, worker *RuntimeNodeWorker) error {
	inner, err := workers.riverWorkers()
	if err != nil {
		return err
	}
	if worker == nil {
		return jobError(foundation.ErrorInvalidInput, "WORKFLOW_RUNTIME_WORKER_INVALID", errors.New("runtime worker is nil"))
	}
	if err := river.AddWorkerSafely(inner, worker); err != nil {
		return jobError(foundation.ErrorVersionConflict, "WORKFLOW_RIVER_WORKER_DUPLICATE", err)
	}
	return nil
}

var _ river.Worker[NodeJobArgs] = (*RuntimeNodeWorker)(nil)
