package observability

import (
	"context"
	"errors"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// MetricName is a stable project-owned metric identifier.
type MetricName string

const (
	// MetricProcessPresence emits the current process presence as 1. API and
	// Worker remain distinguishable through the OTLP service.name resource.
	// It is intentionally not a readiness or health signal.
	MetricProcessPresence MetricName = "runtime.process.presence"
	// MetricTelemetryRequired exposes the configured telemetry mode as 1 only
	// for required mode. It lets protected observation jobs prove the mode
	// without trusting an operator-authored manifest boolean.
	MetricTelemetryRequired      MetricName = "runtime.telemetry.required"
	MetricQueueDepth             MetricName = "river.queue.depth"
	MetricActiveWorkers          MetricName = "river.workers.active"
	MetricNodeDuration           MetricName = "workflow.node.duration_ms"
	MetricNodeResultTotal        MetricName = "workflow.node.result_total"
	MetricRetryTotal             MetricName = "workflow.retry_total"
	MetricManualRecoveryTotal    MetricName = "workflow.manual_recovery_total"
	MetricLeaseExpiryTotal       MetricName = "workflow.lease_expiry_total"
	MetricHeartbeatFailureTotal  MetricName = "workflow.heartbeat_failure_total"
	MetricDuplicateDeliveryTotal MetricName = "river.duplicate_delivery_total"
	MetricShutdownTotal          MetricName = "worker.shutdown_total"
	// MetricModelCallDuration 是 Eino Chat callback 观测到的调用耗时。
	MetricModelCallDuration MetricName = "model.chat.duration_ms"
	// MetricModelCallTotal 是 Eino Chat callback 观测到的调用结果计数。
	MetricModelCallTotal MetricName = "model.chat.result_total"
	// MetricAnswerFirstTokenDuration 是最终 Answer 流从请求开始到首个正文 token 的耗时。
	MetricAnswerFirstTokenDuration MetricName = "agent.answer.first_token.duration_ms"
	// MetricAnswerCompletionDuration 是最终 Answer 流从请求开始到终态的耗时。
	MetricAnswerCompletionDuration MetricName = "agent.answer.completion.duration_ms"
	// MetricAnswerResultTotal 是最终 Answer 流的终态计数。
	MetricAnswerResultTotal MetricName = "agent.answer.result_total"
	// MetricDraftDegradationTotal 是草稿流因持久化背压或故障降级的计数。
	MetricDraftDegradationTotal MetricName = "agent.draft.degradation_total"
	// MetricAgentIterations 是单次 Eino Agent 运行实际使用的迭代次数。
	MetricAgentIterations MetricName = "agent.runtime.iterations"
	// MetricAgentToolCalls 是单次 Eino Agent 运行实际调用的冻结工具数量。
	MetricAgentToolCalls MetricName = "agent.runtime.tool_calls"
	// MetricAgentResultTotal 是 Eino Agent 运行的终态计数。
	MetricAgentResultTotal MetricName = "agent.runtime.result_total"
	// MetricRAGOutcomeTotal 是 RAG 业务终态与运行失败的计数。
	MetricRAGOutcomeTotal MetricName = "rag.outcome_total"
	// MetricRAGGraphNodeResultTotal 是 Eino RAG Graph 固定节点的运行结果计数。
	MetricRAGGraphNodeResultTotal MetricName = "agent.rag.graph_node.result_total"
)

// MetricKind controls aggregation semantics in concrete adapters.
type MetricKind string

const (
	MetricKindCounter   MetricKind = "counter"
	MetricKindGauge     MetricKind = "gauge"
	MetricKindHistogram MetricKind = "histogram"
)

var (
	ErrUnknownMetric          = errors.New("unknown metric")
	ErrInvalidMetric          = errors.New("invalid metric")
	ErrUnboundedMetricLabel   = errors.New("unbounded metric label")
	ErrSensitiveMetricLabel   = errors.New("sensitive metric label")
	ErrMetricsUnavailable     = errors.New("metrics recorder is unavailable")
	ErrObservabilityClosed    = errors.New("observability resource is closed")
	metricLabelValuePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$`)
	errorCodeLabelPattern     = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	highCardinalityHexPattern = regexp.MustCompile(`(?i)^[0-9a-f]{16,}$`)
	uuidPattern               = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

type metricDefinition struct {
	kind     MetricKind
	allowed  map[string]struct{}
	required map[string]struct{}
}

var metricDefinitions = map[MetricName]metricDefinition{
	MetricProcessPresence:          newMetricDefinition(MetricKindGauge, nil, nil),
	MetricTelemetryRequired:        newMetricDefinition(MetricKindGauge, nil, nil),
	MetricQueueDepth:               newMetricDefinition(MetricKindGauge, []string{"queue"}, []string{"queue"}),
	MetricActiveWorkers:            newMetricDefinition(MetricKindGauge, []string{"queue"}, []string{"queue"}),
	MetricNodeDuration:             newMetricDefinition(MetricKindHistogram, []string{"node_kind", "result"}, []string{"node_kind", "result"}),
	MetricNodeResultTotal:          newMetricDefinition(MetricKindCounter, []string{"node_kind", "result", "error_code"}, []string{"node_kind", "result"}),
	MetricRetryTotal:               newMetricDefinition(MetricKindCounter, []string{"node_kind", "error_code"}, []string{"node_kind"}),
	MetricManualRecoveryTotal:      newMetricDefinition(MetricKindCounter, []string{"node_kind", "error_code"}, []string{"node_kind"}),
	MetricLeaseExpiryTotal:         newMetricDefinition(MetricKindCounter, []string{"node_kind"}, []string{"node_kind"}),
	MetricHeartbeatFailureTotal:    newMetricDefinition(MetricKindCounter, []string{"node_kind", "error_code"}, []string{"node_kind"}),
	MetricDuplicateDeliveryTotal:   newMetricDefinition(MetricKindCounter, []string{"node_kind"}, []string{"node_kind"}),
	MetricShutdownTotal:            newMetricDefinition(MetricKindCounter, []string{"shutdown_kind", "result"}, []string{"shutdown_kind", "result"}),
	MetricModelCallDuration:        newMetricDefinition(MetricKindHistogram, []string{"component", "phase", "result", "error_code"}, []string{"component", "phase", "result"}),
	MetricModelCallTotal:           newMetricDefinition(MetricKindCounter, []string{"component", "phase", "result", "error_code"}, []string{"component", "phase", "result"}),
	MetricAnswerFirstTokenDuration: newMetricDefinition(MetricKindHistogram, []string{"result", "error_code"}, []string{"result"}),
	MetricAnswerCompletionDuration: newMetricDefinition(MetricKindHistogram, []string{"result", "error_code"}, []string{"result"}),
	MetricAnswerResultTotal:        newMetricDefinition(MetricKindCounter, []string{"result", "error_code"}, []string{"result"}),
	MetricDraftDegradationTotal:    newMetricDefinition(MetricKindCounter, []string{"error_code"}, nil),
	MetricAgentIterations:          newMetricDefinition(MetricKindHistogram, []string{"result", "error_code"}, []string{"result"}),
	MetricAgentToolCalls:           newMetricDefinition(MetricKindHistogram, []string{"result", "error_code"}, []string{"result"}),
	MetricAgentResultTotal:         newMetricDefinition(MetricKindCounter, []string{"result", "error_code"}, []string{"result"}),
	MetricRAGOutcomeTotal:          newMetricDefinition(MetricKindCounter, []string{"outcome", "error_code"}, []string{"outcome"}),
	MetricRAGGraphNodeResultTotal:  newMetricDefinition(MetricKindCounter, []string{"node_kind", "result", "error_code"}, []string{"node_kind", "result"}),
}

func newMetricDefinition(kind MetricKind, allowed, required []string) metricDefinition {
	definition := metricDefinition{
		kind:     kind,
		allowed:  make(map[string]struct{}, len(allowed)),
		required: make(map[string]struct{}, len(required)),
	}
	for _, key := range allowed {
		definition.allowed[key] = struct{}{}
	}
	for _, key := range required {
		definition.required[key] = struct{}{}
	}
	return definition
}

// Labels is an immutable, bounded metric label set.
type Labels struct {
	values map[string]string
}

// NewLabels validates label keys and values before they reach an exporter.
// Correlation IDs, arbitrary paths and sensitive values are rejected.
func NewLabels(values map[string]string) (Labels, error) {
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if _, allowed := globallyAllowedMetricLabels[key]; !allowed {
			return Labels{}, ErrUnboundedMetricLabel
		}
		if redactString(key, value) == RedactedValue {
			return Labels{}, ErrSensitiveMetricLabel
		}
		if !metricLabelValuePattern.MatchString(value) || uuidPattern.MatchString(value) ||
			highCardinalityHexPattern.MatchString(value) || isLongNumericIdentifier(value) {
			return Labels{}, ErrUnboundedMetricLabel
		}
		if key == "error_code" && !errorCodeLabelPattern.MatchString(value) {
			return Labels{}, ErrUnboundedMetricLabel
		}
		if allowedValues, bounded := boundedMetricLabelValues[key]; bounded {
			if _, allowed := allowedValues[value]; !allowed {
				return Labels{}, ErrUnboundedMetricLabel
			}
		}
		cloned[key] = value
	}
	return Labels{values: cloned}, nil
}

var globallyAllowedMetricLabels = map[string]struct{}{
	"queue": {}, "node_kind": {}, "result": {}, "error_code": {}, "shutdown_kind": {}, "component": {}, "phase": {}, "outcome": {},
}

var boundedMetricLabelValues = map[string]map[string]struct{}{
	"result": {
		"success": {}, "failure": {}, "retry": {}, "manual_recovery": {}, "cancelled": {},
	},
	"shutdown_kind": {"graceful": {}, "forced": {}},
	"component":     {"eino_chat": {}},
	"phase":         {"PLAN": {}, "AGENT": {}, "ANSWER": {}, "INITIAL": {}, "REPAIR": {}, "REDUCED": {}, "REVIEW": {}},
	"outcome":       {"completed": {}, "refused": {}, "clarification_required": {}, "failure": {}},
}

func isLongNumericIdentifier(value string) bool {
	if len(value) < 13 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

// Map returns a defensive copy for adapters and tests.
func (labels Labels) Map() map[string]string {
	cloned := make(map[string]string, len(labels.values))
	for key, value := range labels.values {
		cloned[key] = value
	}
	return cloned
}

// Measurement is a single project metric update.
type Measurement struct {
	Name   MetricName
	Kind   MetricKind
	Value  float64
	Labels Labels
}

// NewMeasurement constructs and validates a metric update.
func NewMeasurement(name MetricName, kind MetricKind, value float64, labels Labels) (Measurement, error) {
	measurement := Measurement{Name: name, Kind: kind, Value: value, Labels: labels}
	if err := measurement.Validate(); err != nil {
		return Measurement{}, err
	}
	return measurement, nil
}

// Validate enforces the metric registry and its bounded label contract.
func (measurement Measurement) Validate() error {
	definition, found := metricDefinitions[measurement.Name]
	if !found {
		return ErrUnknownMetric
	}
	if measurement.Kind != definition.kind || math.IsNaN(measurement.Value) || math.IsInf(measurement.Value, 0) || measurement.Value < 0 {
		return ErrInvalidMetric
	}
	if measurement.Kind == MetricKindCounter && measurement.Value == 0 {
		return ErrInvalidMetric
	}
	for key := range measurement.Labels.values {
		if _, allowed := definition.allowed[key]; !allowed {
			return ErrUnboundedMetricLabel
		}
	}
	for key := range definition.required {
		if _, found := measurement.Labels.values[key]; !found {
			return ErrInvalidMetric
		}
	}
	return nil
}

// Metrics is the project-owned metrics recording interface.
type Metrics interface {
	Record(context.Context, Measurement) error
}

// RecordProcessPresence records one unlabelled synchronous gauge sample for
// this process. Exported resource attributes, rather than metric labels, own
// the API/Worker distinction.
func RecordProcessPresence(ctx context.Context, metrics Metrics) error {
	if metrics == nil {
		return ErrMetricsUnavailable
	}
	measurement, err := NewMeasurement(MetricProcessPresence, MetricKindGauge, 1, Labels{})
	if err != nil {
		return err
	}
	return metrics.Record(ctx, measurement)
}

// RecordTelemetryRequired records whether this process was configured with
// required telemetry. The external service.name resource owns process identity.
func RecordTelemetryRequired(ctx context.Context, metrics Metrics, required bool) error {
	if metrics == nil {
		return ErrMetricsUnavailable
	}
	value := float64(0)
	if required {
		value = 1
	}
	measurement, err := NewMeasurement(MetricTelemetryRequired, MetricKindGauge, value, Labels{})
	if err != nil {
		return err
	}
	return metrics.Record(ctx, measurement)
}

type noopMetrics struct{}

// NewNoopMetrics returns a recorder that exports nothing but still validates
// measurements so disabled telemetry cannot hide instrumentation mistakes.
func NewNoopMetrics() Metrics {
	return noopMetrics{}
}

func (noopMetrics) Record(_ context.Context, measurement Measurement) error {
	return measurement.Validate()
}

// MemoryMetrics is a concurrency-safe test and local diagnostic adapter.
type MemoryMetrics struct {
	mutex        sync.RWMutex
	measurements []Measurement
	closed       bool
}

// NewMemoryMetrics constructs an empty in-memory metrics adapter.
func NewMemoryMetrics() *MemoryMetrics {
	return &MemoryMetrics{}
}

// Record validates and stores a defensive measurement copy.
func (metrics *MemoryMetrics) Record(_ context.Context, measurement Measurement) error {
	if err := measurement.Validate(); err != nil {
		return err
	}
	metrics.mutex.Lock()
	defer metrics.mutex.Unlock()
	if metrics.closed {
		return ErrObservabilityClosed
	}
	measurement.Labels = Labels{values: measurement.Labels.Map()}
	metrics.measurements = append(metrics.measurements, measurement)
	return nil
}

// Snapshot returns measurements in recording order without exposing mutable
// label maps.
func (metrics *MemoryMetrics) Snapshot() []Measurement {
	metrics.mutex.RLock()
	defer metrics.mutex.RUnlock()
	result := make([]Measurement, len(metrics.measurements))
	for index, measurement := range metrics.measurements {
		measurement.Labels = Labels{values: measurement.Labels.Map()}
		result[index] = measurement
	}
	return result
}

func (metrics *MemoryMetrics) close() error {
	metrics.mutex.Lock()
	defer metrics.mutex.Unlock()
	metrics.closed = true
	return nil
}

// MetricNames returns the stable registry in lexical order for diagnostics and
// static contract tests.
func MetricNames() []MetricName {
	names := make([]MetricName, 0, len(metricDefinitions))
	for name := range metricDefinitions {
		names = append(names, name)
	}
	sort.Slice(names, func(left, right int) bool { return names[left] < names[right] })
	return names
}
