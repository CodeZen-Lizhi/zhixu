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
	ErrObservabilityClosed    = errors.New("observability resource is closed")
	metricLabelValuePattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$`)
	stableErrorCodePattern    = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	highCardinalityHexPattern = regexp.MustCompile(`(?i)^[0-9a-f]{16,}$`)
	uuidPattern               = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[1-5][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

type metricDefinition struct {
	kind     MetricKind
	labels   []string
	allowed  map[string]struct{}
	required map[string]struct{}
}

var metricDefinitions = map[MetricName]metricDefinition{
	MetricQueueDepth:             newMetricDefinition(MetricKindGauge, []string{"queue"}, []string{"queue"}),
	MetricActiveWorkers:          newMetricDefinition(MetricKindGauge, []string{"queue"}, []string{"queue"}),
	MetricNodeDuration:           newMetricDefinition(MetricKindHistogram, []string{"node_kind", "result"}, []string{"node_kind", "result"}),
	MetricNodeResultTotal:        newMetricDefinition(MetricKindCounter, []string{"node_kind", "result", "error_code"}, []string{"node_kind", "result"}),
	MetricRetryTotal:             newMetricDefinition(MetricKindCounter, []string{"node_kind", "error_code"}, []string{"node_kind"}),
	MetricManualRecoveryTotal:    newMetricDefinition(MetricKindCounter, []string{"node_kind", "error_code"}, []string{"node_kind"}),
	MetricLeaseExpiryTotal:       newMetricDefinition(MetricKindCounter, []string{"node_kind"}, []string{"node_kind"}),
	MetricHeartbeatFailureTotal:  newMetricDefinition(MetricKindCounter, []string{"node_kind", "error_code"}, []string{"node_kind"}),
	MetricDuplicateDeliveryTotal: newMetricDefinition(MetricKindCounter, []string{"node_kind"}, []string{"node_kind"}),
	MetricShutdownTotal:          newMetricDefinition(MetricKindCounter, []string{"shutdown_kind", "result"}, []string{"shutdown_kind", "result"}),
}

func newMetricDefinition(kind MetricKind, allowed, required []string) metricDefinition {
	definition := metricDefinition{
		kind:     kind,
		labels:   append([]string(nil), allowed...),
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
		if key == "error_code" && !stableErrorCodePattern.MatchString(value) {
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
	"queue": {}, "node_kind": {}, "result": {}, "error_code": {}, "shutdown_kind": {},
}

var boundedMetricLabelValues = map[string]map[string]struct{}{
	"result": {
		"success": {}, "failure": {}, "retry": {}, "manual_recovery": {}, "cancelled": {},
	},
	"shutdown_kind": {"graceful": {}, "forced": {}},
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

type noopMetrics struct{}

// NewNoopMetrics returns a recorder that exports nothing but still validates
// measurements so disabled telemetry cannot hide instrumentation mistakes.
func NewNoopMetrics() Metrics {
	return noopMetrics{}
}

func (noopMetrics) Record(_ context.Context, measurement Measurement) error {
	return measurement.Validate()
}

// MemoryMetrics is a concurrency-safe test spy.
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
