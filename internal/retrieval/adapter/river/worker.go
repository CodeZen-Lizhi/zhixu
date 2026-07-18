package river

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
	workflowriver "github.com/CodeZen-Lizhi/zhixu/internal/workflow/adapter/river"
	riverlib "github.com/riverqueue/river"
)

const (
	reindexActivationReasonCode = "REINDEX_REGRESSION_PASSED"
	workerUnavailableCode       = "REINDEX_RIVER_WORKER_UNAVAILABLE"
	workerInvalidCode           = "REINDEX_RIVER_WORKER_INVALID"
	workerJobInvalidCode        = "REINDEX_RIVER_JOB_INVALID"
	workerResultInvalidCode     = "REINDEX_PROCESSOR_RESULT_INVALID"
)

// deliveryRuntime 是 Worker 消费 Application Runtime 的最小接口；不复制任何领域结果。
type deliveryRuntime interface {
	application.DeliveryLeaseRuntime
	Claim(context.Context, application.DeliveryClaimCommand) (application.DeliveryClaimResult, error)
}

// completionRunner 是 Worker 调用原子完成用例的最小接口。
type completionRunner interface {
	Complete(context.Context, application.CompleteReindexRequest) (domain.CompleteReindexResult, error)
}

// WorkerOptions 配置 Reindex Worker 的稳定 owner、数据库租约与独立心跳周期。
type WorkerOptions struct {
	Owner             string
	LeaseDuration     time.Duration
	HeartbeatInterval time.Duration
	Metrics           observability.Metrics
	Logger            *slog.Logger
	FatalInvariants   chan<- error
}

// Worker 将一个 River transport delivery 归约到 PostgreSQL Reindex 事实源。
type Worker struct {
	riverlib.WorkerDefaults[Args]
	runtime    deliveryRuntime
	processor  application.ProcessorRunner
	completion completionRunner
	options    WorkerOptions
	observer   workerObserver
}

var (
	_ riverlib.Worker[Args] = (*Worker)(nil)
	_ deliveryRuntime       = (*application.DeliveryRuntime)(nil)
	_ completionRunner      = (*application.CompletionService)(nil)
)

// NewWorker 创建严格解码、租约心跳和原子完成的 Reindex River Worker。
func NewWorker(runtime deliveryRuntime, processor application.ProcessorRunner, completion completionRunner, options WorkerOptions) (*Worker, error) {
	options.Owner = strings.TrimSpace(options.Owner)
	if nilWorkerDependency(runtime) || nilWorkerDependency(processor) || nilWorkerDependency(completion) ||
		options.Owner == "" || len(options.Owner) > 128 || strings.ContainsAny(options.Owner, "\r\n\t/") ||
		options.LeaseDuration <= 0 || options.HeartbeatInterval <= 0 || options.HeartbeatInterval >= options.LeaseDuration ||
		options.Metrics != nil && nilWorkerDependency(options.Metrics) {
		return nil, workerError(foundation.ErrorInvalidInput, workerInvalidCode, false, errors.New("reindex worker dependencies or lease cadence are invalid"))
	}
	return &Worker{
		runtime: runtime, processor: processor, completion: completion, options: options,
		observer: workerObserver{metrics: options.Metrics, logger: options.Logger},
	}, nil
}

// Work 只在业务归约已确认提交后返回 nil；未确认事务结果原样交给 River 重投。
func (worker *Worker) Work(ctx context.Context, job *riverlib.Job[Args]) (workErr error) {
	if worker != nil {
		defer func() { worker.reportFatal(workErr) }()
	}
	if worker == nil || nilWorkerDependency(worker.runtime) || nilWorkerDependency(worker.processor) || nilWorkerDependency(worker.completion) {
		return workerError(foundation.ErrorDependencyUnavailable, workerUnavailableCode, true, errors.New("reindex worker is unavailable"))
	}
	if ctx == nil || job == nil || job.JobRow == nil || job.ID < 1 || job.Attempt < 1 {
		return workerError(foundation.ErrorInvalidInput, workerJobInvalidCode, false, errors.New("River job row is missing or invalid"))
	}
	if err := ValidateArgs(job.Args); err != nil {
		return err
	}
	if err := ValidateEncodedArgs(job.EncodedArgs, job.Args); err != nil {
		return err
	}
	var err error
	ctx, err = workflowriver.DecodeTraceMetadata(ctx, job.Metadata)
	if err != nil {
		return err
	}

	deliveryKey := fmt.Sprintf("reindex-job:%d:attempt:%d", job.ID, job.Attempt)
	leaseOwner := worker.options.Owner + ":" + deliveryKey
	claim, err := worker.runtime.Claim(ctx, application.DeliveryClaimCommand{
		DeliveryID: job.Args.DeliveryID, DispatchNo: job.Args.DispatchNo,
		RiverJobID: job.ID, RiverAttempt: job.Attempt,
		DeliveryKey: deliveryKey, LeaseOwner: leaseOwner, LeaseDuration: worker.options.LeaseDuration,
	})
	if err != nil {
		return err
	}
	worker.observer.observeClaim(ctx, claim)
	if claim.Disposition == application.DeliveryClaimStale || claim.Disposition == application.DeliveryClaimCommitted {
		return nil
	}
	if claim.Disposition != application.DeliveryClaimed {
		return workerError(foundation.ErrorConsistencyViolation, workerResultInvalidCode, false, errors.New("claim returned an unknown disposition"))
	}
	lease, err := application.NewDeliveryLeaseSession(worker.runtime, claim.Fence)
	if err != nil {
		return err
	}
	ctx = observability.WithCorrelation(ctx, observability.Correlation{
		WorkspaceID: string(claim.Delivery.WorkspaceID), AttemptNo: claim.Attempt.AttemptNo,
		DispatchNo: claim.Delivery.DispatchNo, RiverJobID: job.ID,
	})

	processorContext, cancelProcessor := context.WithCancel(ctx)
	heartbeatErrors := make(chan error, 1)
	heartbeatDone := make(chan struct{})
	go worker.heartbeatLoop(processorContext, cancelProcessor, lease, heartbeatErrors, heartbeatDone)
	result, processorErr := worker.processor.Process(processorContext, application.ProcessorRequest{Lease: lease})
	cancelProcessor()
	<-heartbeatDone
	if heartbeatErr := readHeartbeatError(heartbeatErrors); heartbeatErr != nil {
		return heartbeatErr
	}
	if processorErr == nil && (result.Disposition == application.ProcessorStale || result.Disposition == application.ProcessorCommitted) {
		return nil
	}
	snapshot := lease.Snapshot()
	if snapshot.Disposition == application.DeliveryLeaseStale || snapshot.Disposition == application.DeliveryLeaseCommitted {
		return nil
	}
	if snapshot.Disposition != application.DeliveryLeaseActive {
		return workerError(foundation.ErrorConsistencyViolation, workerResultInvalidCode, false, errors.New("lease session returned an unknown disposition"))
	}
	if processorErr != nil {
		var failure *application.DeliveryFailureError
		if !errors.As(processorErr, &failure) {
			return processorErr
		}
		return worker.settleDeliveryFailure(ctx, lease, failure)
	}

	switch result.Disposition {
	case application.ProcessorReady:
		if !validReadyProcessorResult(claim, snapshot, result) {
			return workerError(foundation.ErrorConsistencyViolation, workerResultInvalidCode, false, errors.New("processor ready result is inconsistent"))
		}
	default:
		return workerError(foundation.ErrorConsistencyViolation, workerResultInvalidCode, false, errors.New("processor returned an unknown disposition"))
	}

	snapshot, err = lease.Heartbeat(ctx, worker.options.LeaseDuration)
	if err != nil {
		return err
	}
	if snapshot.Disposition == application.DeliveryLeaseStale || snapshot.Disposition == application.DeliveryLeaseCommitted {
		return nil
	}
	if snapshot.Disposition != application.DeliveryLeaseActive {
		return workerError(foundation.ErrorConsistencyViolation, workerResultInvalidCode, false, errors.New("final lease renewal returned an unknown disposition"))
	}
	completionResult, err := worker.completion.Complete(ctx, application.CompleteReindexRequest{
		WorkspaceID: result.WorkspaceID, Fence: snapshot.Fence,
		ActivationIdempotencyKey: "reindex-complete:" + string(result.OutboxEventID),
		ActivationReasonCode:     reindexActivationReasonCode,
	})
	if err != nil {
		var failure *application.DeliveryFailureError
		if errors.As(err, &failure) {
			return worker.settleDeliveryFailure(ctx, lease, failure)
		}
	}
	if err == nil {
		worker.observer.observeCompletion(ctx, completionResult)
	}
	return err
}

func (worker *Worker) heartbeatLoop(ctx context.Context, cancel context.CancelFunc, lease *application.DeliveryLeaseSession, errorsChannel chan<- error, done chan<- struct{}) {
	defer close(done)
	ticker := time.NewTicker(worker.options.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			snapshot, err := lease.Heartbeat(ctx, worker.options.LeaseDuration)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				worker.observer.observeHeartbeatFailure(ctx, err)
				select {
				case errorsChannel <- err:
				default:
				}
				cancel()
				return
			}
			if snapshot.Disposition != application.DeliveryLeaseActive {
				cancel()
				return
			}
		}
	}
}

func (worker *Worker) settleDeliveryFailure(ctx context.Context, lease *application.DeliveryLeaseSession, failure *application.DeliveryFailureError) error {
	if failure == nil {
		return workerError(foundation.ErrorConsistencyViolation, workerResultInvalidCode, false, errors.New("processor failure is nil"))
	}
	snapshot, err := lease.Heartbeat(ctx, worker.options.LeaseDuration)
	if err != nil {
		return err
	}
	if snapshot.Disposition == application.DeliveryLeaseStale || snapshot.Disposition == application.DeliveryLeaseCommitted {
		return nil
	}
	if snapshot.Disposition != application.DeliveryLeaseActive {
		return workerError(foundation.ErrorConsistencyViolation, workerResultInvalidCode, false, errors.New("failure lease renewal returned an unknown disposition"))
	}
	snapshot, err = lease.Fail(ctx, failure.Failure, failure.RetryDelay)
	if err != nil {
		return err
	}
	if snapshot.Disposition == application.DeliveryLeaseStale || snapshot.Disposition == application.DeliveryLeaseCommitted {
		if snapshot.Disposition == application.DeliveryLeaseCommitted {
			worker.observer.observeFailure(ctx, failure.Failure)
		}
		return nil
	}
	return workerError(foundation.ErrorConsistencyViolation, workerResultInvalidCode, false, errors.New("processor failure was not reduced"))
}

func validReadyProcessorResult(claim application.DeliveryClaimResult, snapshot application.DeliveryLeaseSnapshot, result application.ProcessorResult) bool {
	return canonicalWorkerID(result.WorkspaceID) && canonicalWorkerID(result.OutboxEventID) && canonicalWorkerID(result.IndexVersionID) &&
		result.WorkspaceID == claim.Delivery.WorkspaceID && result.OutboxEventID == claim.Delivery.OutboxEventID &&
		domain.ValidateDeliveryFence(result.Fence) == nil && sameFenceIdentity(result.Fence, snapshot.Fence) &&
		result.Fence.DeliveryVersion <= snapshot.Fence.DeliveryVersion
}

func sameFenceIdentity(left, right domain.DeliveryFence) bool {
	return left.DeliveryID == right.DeliveryID && left.DispatchNo == right.DispatchNo && left.AttemptID == right.AttemptID &&
		left.AttemptNo == right.AttemptNo && left.Owner == right.Owner
}

func canonicalWorkerID(value foundation.ID) bool {
	parsed, err := foundation.ParseID(string(value))
	return err == nil && parsed == value
}

func readHeartbeatError(channel <-chan error) error {
	select {
	case err := <-channel:
		return err
	default:
		return nil
	}
}

// AddWorkerSafely 将 Reindex Worker 注册到现有 Workflow River Workers bundle。
func AddWorkerSafely(workers *workflowriver.Workers, worker *Worker) error {
	return workflowriver.AddWorkerSafely(workers, worker)
}

func (worker *Worker) reportFatal(err error) {
	if worker == nil || worker.options.FatalInvariants == nil || !fatalWorkerError(err) {
		return
	}
	select {
	case worker.options.FatalInvariants <- err:
	default:
	}
}

func fatalWorkerError(err error) bool {
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		return false
	}
	return strings.HasPrefix(classified.Code, "REINDEX_JOB_") ||
		classified.Code == "WORKFLOW_RIVER_TRACE_METADATA_INVALID" ||
		classified.Code == "REINDEX_DISPATCH_BINDING_CONFLICT" ||
		classified.Code == "REINDEX_DELIVERY_LEASE_SESSION_INVALID" ||
		classified.Code == workerResultInvalidCode
}

func nilWorkerDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func workerError(kind foundation.ErrorKind, code string, retryable bool, cause error) error {
	return foundation.NewError(kind, code, retryable, cause)
}
