package riveradapter

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/workflow/domain"
)

// RuntimeWorkerObservability 配置 Runtime Worker 的有界指标和 fatal invariant 上报边界。
type RuntimeWorkerObservability struct {
	Metrics         observability.Metrics
	Queue           string
	Logger          *slog.Logger
	FatalInvariants chan<- error
}

type runtimeWorkerObserver struct {
	metrics         observability.Metrics
	queue           string
	logger          *slog.Logger
	fatalInvariants chan<- error
	active          atomic.Int64
}

func newRuntimeWorkerObserver(options RuntimeWorkerObservability) runtimeWorkerObserver {
	return runtimeWorkerObserver{
		metrics: options.Metrics, queue: options.Queue, logger: options.Logger, fatalInvariants: options.FatalInvariants,
	}
}

func (observer *runtimeWorkerObserver) reportFatal(err error) {
	if observer == nil || observer.fatalInvariants == nil || !isFatalRuntimeInvariant(err) {
		return
	}
	select {
	case observer.fatalInvariants <- err:
	default:
	}
}

func isFatalRuntimeInvariant(err error) bool {
	var classified *foundation.Error
	if !errors.As(err, &classified) {
		return false
	}
	switch classified.Code {
	case "WORKFLOW_CLAIM_RESULT_INVALID", "WORKFLOW_HEARTBEAT_RESULT_INVALID", "WORKFLOW_DELIVERY_TRANSITION_RESULT_INVALID", "WORKFLOW_EXECUTION_RESULT_INVALID":
		return true
	default:
		return false
	}
}

func (observer *runtimeWorkerObserver) observeClaim(ctx context.Context, claim application.ClaimResult) {
	nodeKind := claim.ObservedNodeKind
	if nodeKind == "" {
		nodeKind = claim.Node.NodeType
	}
	if claim.DuplicateDelivery {
		observer.record(ctx, observability.MetricDuplicateDeliveryTotal, observability.MetricKindCounter, 1, map[string]string{"node_kind": nodeKind})
	}
	if claim.LeaseReclaimed {
		observer.record(ctx, observability.MetricLeaseExpiryTotal, observability.MetricKindCounter, 1, map[string]string{"node_kind": nodeKind})
	}
}

func (observer *runtimeWorkerObserver) beginExecution(ctx context.Context) {
	active := observer.active.Add(1)
	observer.record(ctx, observability.MetricActiveWorkers, observability.MetricKindGauge, float64(active), map[string]string{"queue": observer.queue})
}

func (observer *runtimeWorkerObserver) endExecution(ctx context.Context) {
	active := observer.active.Add(-1)
	observer.record(ctx, observability.MetricActiveWorkers, observability.MetricKindGauge, float64(active), map[string]string{"queue": observer.queue})
}

func (observer *runtimeWorkerObserver) observeHeartbeatFailure(ctx context.Context, nodeKind string, err error) {
	if isControlRequestedError(err) {
		return
	}
	labels := map[string]string{"node_kind": nodeKind}
	if code := stableErrorCode(err); code != "" {
		labels["error_code"] = code
	}
	observer.record(ctx, observability.MetricHeartbeatFailureTotal, observability.MetricKindCounter, 1, labels)
}

func (observer *runtimeWorkerObserver) observeTransition(ctx context.Context, nodeKind string, result application.DeliveryTransitionResult) {
	if result.Replayed || result.Attempt.EndedAt == nil || result.Attempt.StartedAt.IsZero() || result.Attempt.EndedAt.Before(result.Attempt.StartedAt) {
		return
	}
	resultLabel, ok := metricResult(result.Attempt.Status)
	if !ok {
		return
	}
	labels := map[string]string{"node_kind": nodeKind, "result": resultLabel}
	if result.Attempt.ErrorCode != "" {
		labels["error_code"] = result.Attempt.ErrorCode
	}
	observer.record(ctx, observability.MetricNodeDuration, observability.MetricKindHistogram, float64(result.Attempt.EndedAt.Sub(result.Attempt.StartedAt).Milliseconds()), map[string]string{"node_kind": nodeKind, "result": resultLabel})
	observer.record(ctx, observability.MetricNodeResultTotal, observability.MetricKindCounter, 1, labels)
	switch result.Attempt.Status {
	case domain.AttemptStatusRetryScheduled:
		observer.record(ctx, observability.MetricRetryTotal, observability.MetricKindCounter, 1, optionalErrorLabels(nodeKind, result.Attempt.ErrorCode))
	case domain.AttemptStatusManualRecovery:
		observer.record(ctx, observability.MetricManualRecoveryTotal, observability.MetricKindCounter, 1, optionalErrorLabels(nodeKind, result.Attempt.ErrorCode))
	}
}

func metricResult(status domain.AttemptStatus) (string, bool) {
	switch status {
	case domain.AttemptStatusSucceeded:
		return "success", true
	case domain.AttemptStatusRetryScheduled:
		return "retry", true
	case domain.AttemptStatusManualRecovery:
		return "manual_recovery", true
	case domain.AttemptStatusFailed, domain.AttemptStatusLeaseLost:
		return "failure", true
	case domain.AttemptStatusCancelled:
		return "cancelled", true
	default:
		return "", false
	}
}

func optionalErrorLabels(nodeKind, errorCode string) map[string]string {
	labels := map[string]string{"node_kind": nodeKind}
	if errorCode != "" {
		labels["error_code"] = errorCode
	}
	return labels
}

func stableErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}

func (observer *runtimeWorkerObserver) record(ctx context.Context, name observability.MetricName, kind observability.MetricKind, value float64, rawLabels map[string]string) {
	if observer == nil || observer.metrics == nil {
		return
	}
	labels, err := observability.NewLabels(rawLabels)
	if err == nil {
		var measurement observability.Measurement
		measurement, err = observability.NewMeasurement(name, kind, value, labels)
		if err == nil {
			err = observer.metrics.Record(ctx, measurement)
		}
	}
	if err != nil && observer.logger != nil {
		observer.logger.WarnContext(ctx, "workflow metric recording failed", "error_code", "WORKFLOW_METRIC_RECORD_FAILED", "metric", string(name))
	}
}
