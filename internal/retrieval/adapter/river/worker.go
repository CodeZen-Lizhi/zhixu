package river

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"sync/atomic"
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

// CompatibleProcessorAcquirer 在持久 Claim 前进入 runtime admission gate。
type CompatibleProcessorAcquirer interface {
	Admit(context.Context) (CompatibleProcessorAdmission, error)
}

// CompatibleProcessorAdmission 允许已入场任务在 gate 关闭后继续按 Claim
// 返回的完整持久 Delivery 解析不可变 Processor graph；Release 必须幂等。
type CompatibleProcessorAdmission interface {
	Acquire(context.Context, domain.Delivery) (CompatibleProcessorLease, error)
	Release()
}

// CompatibleProcessorLease 固定一次 Reindex Work 使用的 Processor graph。
// Release 必须幂等，并且只能在 heartbeat 与 Complete/Fail settlement 结束后调用。
type CompatibleProcessorLease interface {
	Processor() application.ProcessorRunner
	Release()
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
	runtime           deliveryRuntime
	processorAcquirer CompatibleProcessorAcquirer
	completion        completionRunner
	options           WorkerOptions
	observer          workerObserver
}

var (
	_ riverlib.Worker[Args]        = (*Worker)(nil)
	_ deliveryRuntime              = (*application.DeliveryRuntime)(nil)
	_ completionRunner             = (*application.CompletionService)(nil)
	_ CompatibleProcessorAcquirer  = staticProcessorAcquirer{}
	_ CompatibleProcessorAdmission = (*staticProcessorAdmission)(nil)
	_ CompatibleProcessorLease     = (*staticProcessorLease)(nil)
)

// NewWorker 创建严格解码、租约心跳和原子完成的 Reindex River Worker。
func NewWorker(runtime deliveryRuntime, processor application.ProcessorRunner, completion completionRunner, options WorkerOptions) (*Worker, error) {
	if nilWorkerDependency(processor) {
		return nil, workerError(foundation.ErrorInvalidInput, workerInvalidCode, false, errors.New("reindex worker dependencies or lease cadence are invalid"))
	}
	return NewWorkerWithProcessorAcquirer(runtime, staticProcessorAcquirer{processor: processor}, completion, options)
}

// NewWorkerWithProcessorAcquirer 创建按持久 Delivery 取得 operation-scoped
// Processor graph 的 Reindex River Worker。
func NewWorkerWithProcessorAcquirer(runtime deliveryRuntime, processorAcquirer CompatibleProcessorAcquirer, completion completionRunner, options WorkerOptions) (*Worker, error) {
	options.Owner = strings.TrimSpace(options.Owner)
	if nilWorkerDependency(runtime) || nilWorkerDependency(processorAcquirer) || nilWorkerDependency(completion) ||
		options.Owner == "" || len(options.Owner) > 128 || strings.ContainsAny(options.Owner, "\r\n\t/") ||
		options.LeaseDuration <= 0 || options.HeartbeatInterval <= 0 || options.HeartbeatInterval >= options.LeaseDuration ||
		options.Metrics != nil && nilWorkerDependency(options.Metrics) {
		return nil, workerError(foundation.ErrorInvalidInput, workerInvalidCode, false, errors.New("reindex worker dependencies or lease cadence are invalid"))
	}
	return &Worker{
		runtime: runtime, processorAcquirer: processorAcquirer, completion: completion, options: options,
		observer: workerObserver{metrics: options.Metrics, logger: options.Logger},
	}, nil
}

// Work 只在业务归约已确认提交后返回 nil；未确认事务结果原样交给 River 重投。
func (worker *Worker) Work(ctx context.Context, job *riverlib.Job[Args]) (workErr error) {
	if worker != nil {
		defer func() { worker.reportFatal(workErr) }()
	}
	if worker == nil || nilWorkerDependency(worker.runtime) || nilWorkerDependency(worker.processorAcquirer) || nilWorkerDependency(worker.completion) {
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
	processorAdmission, err := worker.admitProcessor(ctx)
	if err != nil {
		return err
	}
	defer processorAdmission.Release()

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
	processorLease, processor, err := worker.acquireProcessor(ctx, processorAdmission, claim.Delivery)
	if err != nil {
		return err
	}
	defer processorLease.Release()
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
	result, processorErr := processor.Process(processorContext, application.ProcessorRequest{Lease: lease})
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

func (worker *Worker) admitProcessor(ctx context.Context) (CompatibleProcessorAdmission, error) {
	admission, err := worker.processorAcquirer.Admit(ctx)
	if err != nil {
		if !nilWorkerDependency(admission) {
			admission.Release()
		}
		return nil, err
	}
	if nilWorkerDependency(admission) {
		return nil, workerError(foundation.ErrorConsistencyViolation, workerResultInvalidCode, false, errors.New("processor acquirer returned a nil admission"))
	}
	return admission, nil
}

func (worker *Worker) acquireProcessor(ctx context.Context, admission CompatibleProcessorAdmission, delivery domain.Delivery) (CompatibleProcessorLease, application.ProcessorRunner, error) {
	lease, err := admission.Acquire(ctx, delivery)
	if err != nil {
		if !nilWorkerDependency(lease) {
			lease.Release()
		}
		return nil, nil, err
	}
	if nilWorkerDependency(lease) {
		return nil, nil, workerError(foundation.ErrorConsistencyViolation, workerResultInvalidCode, false, errors.New("processor acquirer returned a nil lease"))
	}
	processor := lease.Processor()
	if nilWorkerDependency(processor) {
		lease.Release()
		return nil, nil, workerError(foundation.ErrorConsistencyViolation, workerResultInvalidCode, false, errors.New("processor lease returned a nil processor"))
	}
	return lease, processor, nil
}

type staticProcessorAcquirer struct {
	processor application.ProcessorRunner
}

func (acquirer staticProcessorAcquirer) Admit(context.Context) (CompatibleProcessorAdmission, error) {
	return &staticProcessorAdmission{processor: acquirer.processor}, nil
}

type staticProcessorAdmission struct {
	processor application.ProcessorRunner
	released  atomic.Bool
}

func (admission *staticProcessorAdmission) Acquire(context.Context, domain.Delivery) (CompatibleProcessorLease, error) {
	if admission == nil || admission.released.Load() {
		return nil, workerError(foundation.ErrorDependencyUnavailable, workerUnavailableCode, true, errors.New("static processor admission is released"))
	}
	return &staticProcessorLease{processor: admission.processor}, nil
}

func (admission *staticProcessorAdmission) Release() {
	if admission != nil {
		admission.released.Store(true)
	}
}

type staticProcessorLease struct {
	processor application.ProcessorRunner
	released  atomic.Bool
}

func (lease *staticProcessorLease) Processor() application.ProcessorRunner {
	if lease == nil || lease.released.Load() {
		return nil
	}
	return lease.processor
}

func (lease *staticProcessorLease) Release() {
	if lease != nil {
		lease.released.Store(true)
	}
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
