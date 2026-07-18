package river

import (
	"context"
	"errors"
	"log/slog"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/application"
	"github.com/CodeZen-Lizhi/zhixu/internal/retrieval/domain"
)

const reindexMetricNodeKind = "retrieval_reindex_v1"

type workerObserver struct {
	metrics observability.Metrics
	logger  *slog.Logger
}

func (observer workerObserver) observeClaim(ctx context.Context, claim application.DeliveryClaimResult) {
	if claim.Replayed {
		observer.record(ctx, observability.MetricDuplicateDeliveryTotal, observability.MetricKindCounter, 1, map[string]string{"node_kind": reindexMetricNodeKind})
	}
	if claim.LeaseReclaimed {
		observer.record(ctx, observability.MetricLeaseExpiryTotal, observability.MetricKindCounter, 1, map[string]string{"node_kind": reindexMetricNodeKind})
	}
}

func (observer workerObserver) observeHeartbeatFailure(ctx context.Context, err error) {
	labels := map[string]string{"node_kind": reindexMetricNodeKind}
	if code := observerErrorCode(err); code != "" {
		labels["error_code"] = code
	}
	observer.record(ctx, observability.MetricHeartbeatFailureTotal, observability.MetricKindCounter, 1, labels)
}

func (observer workerObserver) observeFailure(ctx context.Context, failure domain.DeliveryFailure) {
	result := "failure"
	metric := observability.MetricNodeResultTotal
	switch failure.Class {
	case domain.DeliveryFailureRetryable:
		result = "retry"
		metric = observability.MetricRetryTotal
	case domain.DeliveryFailureManualRecovery:
		result = "manual_recovery"
		metric = observability.MetricManualRecoveryTotal
	}
	labels := map[string]string{"node_kind": reindexMetricNodeKind, "error_code": failure.Code}
	observer.record(ctx, observability.MetricNodeResultTotal, observability.MetricKindCounter, 1,
		map[string]string{"node_kind": reindexMetricNodeKind, "result": result, "error_code": failure.Code})
	if metric != observability.MetricNodeResultTotal {
		observer.record(ctx, metric, observability.MetricKindCounter, 1, labels)
	}
}

func (observer workerObserver) observeCompletion(ctx context.Context, result domain.CompleteReindexResult) {
	if result.Replayed || result.Attempt.EndedAt == nil || result.Attempt.StartedAt.IsZero() || result.Attempt.EndedAt.Before(result.Attempt.StartedAt) ||
		result.Delivery.Status != domain.DeliveryStatusSucceeded || result.Attempt.Status != domain.DeliveryAttemptSucceeded {
		return
	}
	labels := map[string]string{"node_kind": reindexMetricNodeKind, "result": "success"}
	observer.record(ctx, observability.MetricNodeDuration, observability.MetricKindHistogram,
		float64(result.Attempt.EndedAt.Sub(result.Attempt.StartedAt).Milliseconds()), labels)
	observer.record(ctx, observability.MetricNodeResultTotal, observability.MetricKindCounter, 1, labels)
}

func (observer workerObserver) record(ctx context.Context, name observability.MetricName, kind observability.MetricKind, value float64, rawLabels map[string]string) {
	if observer.metrics == nil {
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
		observer.logger.WarnContext(ctx, "reindex metric recording failed", "error_code", "REINDEX_METRIC_RECORD_FAILED", "metric", string(name))
	}
}

func observerErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) {
		return classified.Code
	}
	return ""
}
