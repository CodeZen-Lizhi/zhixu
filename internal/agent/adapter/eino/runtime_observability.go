package eino

import (
	"context"
	"errors"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
)

type runtimeMetricObserver struct {
	metrics observability.Metrics
	now     func() time.Time
}

func newRuntimeMetricObserver(metrics observability.Metrics) runtimeMetricObserver {
	return runtimeMetricObserver{metrics: metrics, now: time.Now}
}

func (observer runtimeMetricObserver) start() time.Time {
	if observer.now == nil {
		return time.Now()
	}
	return observer.now()
}

func (observer runtimeMetricObserver) observeAnswerFirstToken(ctx context.Context, startedAt time.Time) {
	observer.record(ctx, observability.MetricAnswerFirstTokenDuration, observability.MetricKindHistogram,
		metricDurationMilliseconds(startedAt, observer.start()), map[string]string{"result": "success"})
}

func (observer runtimeMetricObserver) observeAnswerCompletion(ctx context.Context, startedAt time.Time, draftError error, runErr error) {
	result, code := runtimeMetricResult(runErr)
	labels := runtimeMetricLabels(result, code)
	observer.record(ctx, observability.MetricAnswerCompletionDuration, observability.MetricKindHistogram,
		metricDurationMilliseconds(startedAt, observer.start()), labels)
	observer.record(ctx, observability.MetricAnswerResultTotal, observability.MetricKindCounter, 1, labels)
	if draftError != nil {
		degradationLabels := map[string]string{}
		if draftCode := runtimeMetricErrorCode(draftError); draftCode != "" {
			degradationLabels["error_code"] = draftCode
		}
		observer.record(ctx, observability.MetricDraftDegradationTotal, observability.MetricKindCounter, 1, degradationLabels)
	}
}

func (observer runtimeMetricObserver) observeAgent(ctx context.Context, iterations, toolCalls int, runErr error) {
	result, code := runtimeMetricResult(runErr)
	labels := runtimeMetricLabels(result, code)
	observer.record(ctx, observability.MetricAgentResultTotal, observability.MetricKindCounter, 1, labels)
	if runErr != nil {
		return
	}
	observer.record(ctx, observability.MetricAgentIterations, observability.MetricKindHistogram, float64(iterations), labels)
	observer.record(ctx, observability.MetricAgentToolCalls, observability.MetricKindHistogram, float64(toolCalls), labels)
}

func (observer runtimeMetricObserver) observeRAGGraphNode(ctx context.Context, node string, runErr error) {
	result, code := runtimeMetricResult(runErr)
	labels := runtimeMetricLabels(result, code)
	labels["node_kind"] = "eino_rag_" + node
	observer.record(ctx, observability.MetricRAGGraphNodeResultTotal, observability.MetricKindCounter, 1, labels)
}

func (observer runtimeMetricObserver) record(
	ctx context.Context,
	name observability.MetricName,
	kind observability.MetricKind,
	value float64,
	rawLabels map[string]string,
) {
	defer func() { _ = recover() }()
	if observer.metrics == nil {
		return
	}
	labels, err := observability.NewLabels(rawLabels)
	if err != nil {
		return
	}
	measurement, err := observability.NewMeasurement(name, kind, value, labels)
	if err != nil {
		return
	}
	_ = observer.metrics.Record(ctx, measurement)
}

func runtimeMetricResult(err error) (string, string) {
	if err == nil {
		return "success", ""
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled", runtimeMetricErrorCode(err)
	}
	return "failure", runtimeMetricErrorCode(err)
}

func runtimeMetricErrorCode(err error) string {
	var classified *foundation.Error
	if errors.As(err, &classified) && classified != nil {
		return classified.Code
	}
	return ""
}

func runtimeMetricLabels(result, code string) map[string]string {
	labels := map[string]string{"result": result}
	if code != "" {
		labels["error_code"] = code
	}
	return labels
}

func metricDurationMilliseconds(startedAt, endedAt time.Time) float64 {
	if startedAt.IsZero() || endedAt.Before(startedAt) {
		return 0
	}
	return float64(endedAt.Sub(startedAt)) / float64(time.Millisecond)
}
