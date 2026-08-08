package observability

import (
	"context"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const prometheusNamespace = "zhixu"

type prometheusMetricDefinition struct {
	name string
	help string
}

var prometheusMetricDefinitions = map[MetricName]prometheusMetricDefinition{
	MetricQueueDepth:             {name: "river_queue_depth", help: "Immediately available River jobs in the configured queue."},
	MetricActiveWorkers:          {name: "river_workers_active", help: "Runtime node workers currently executing in this process."},
	MetricNodeDuration:           {name: "workflow_node_duration_seconds", help: "Persisted workflow node attempt duration in seconds."},
	MetricNodeResultTotal:        {name: "workflow_node_result_total", help: "Persisted workflow node attempt results."},
	MetricRetryTotal:             {name: "workflow_retry_total", help: "Persisted workflow node retries."},
	MetricManualRecoveryTotal:    {name: "workflow_manual_recovery_total", help: "Persisted workflow node manual recovery transitions."},
	MetricLeaseExpiryTotal:       {name: "workflow_lease_expiry_total", help: "Persisted workflow node lease expirations."},
	MetricHeartbeatFailureTotal:  {name: "workflow_heartbeat_failure_total", help: "Workflow node heartbeat failures."},
	MetricDuplicateDeliveryTotal: {name: "river_duplicate_delivery_total", help: "River deliveries identified as duplicates by the workflow claim transaction."},
	MetricShutdownTotal:          {name: "worker_shutdown_total", help: "Worker process shutdown outcomes."},
}

type prometheusCollector struct {
	kind      MetricKind
	labels    []string
	counter   *prometheus.CounterVec
	gauge     *prometheus.GaugeVec
	histogram *prometheus.HistogramVec
}

type prometheusMetrics struct {
	collectors map[MetricName]prometheusCollector
}

func newPrometheusMetrics() (*prometheusMetrics, http.Handler, error) {
	registry := prometheus.NewRegistry()
	metrics := &prometheusMetrics{collectors: make(map[MetricName]prometheusCollector, len(metricDefinitions))}

	for _, name := range MetricNames() {
		definition := metricDefinitions[name]
		prometheusDefinition, found := prometheusMetricDefinitions[name]
		if !found {
			return nil, nil, ErrTelemetryMetrics
		}
		collector := prometheusCollector{kind: definition.kind, labels: append([]string(nil), definition.labels...)}
		var registered prometheus.Collector
		switch definition.kind {
		case MetricKindCounter:
			collector.counter = prometheus.NewCounterVec(prometheus.CounterOpts{
				Namespace: prometheusNamespace, Name: prometheusDefinition.name, Help: prometheusDefinition.help,
			}, collector.labels)
			registered = collector.counter
		case MetricKindGauge:
			collector.gauge = prometheus.NewGaugeVec(prometheus.GaugeOpts{
				Namespace: prometheusNamespace, Name: prometheusDefinition.name, Help: prometheusDefinition.help,
			}, collector.labels)
			registered = collector.gauge
		case MetricKindHistogram:
			collector.histogram = prometheus.NewHistogramVec(prometheus.HistogramOpts{
				Namespace: prometheusNamespace, Name: prometheusDefinition.name, Help: prometheusDefinition.help,
				Buckets: prometheus.ExponentialBuckets(0.01, 2, 18),
			}, collector.labels)
			registered = collector.histogram
		default:
			return nil, nil, ErrTelemetryMetrics
		}
		if err := registry.Register(registered); err != nil {
			return nil, nil, ErrTelemetryMetrics
		}
		metrics.collectors[name] = collector
	}
	if len(metrics.collectors) != len(prometheusMetricDefinitions) {
		return nil, nil, ErrTelemetryMetrics
	}
	for _, collector := range []prometheus.Collector{
		prometheus.NewGoCollector(),
		prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}),
	} {
		if err := registry.Register(collector); err != nil {
			return nil, nil, ErrTelemetryMetrics
		}
	}

	handler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		EnableOpenMetrics:   true,
		MaxRequestsInFlight: 5,
		ErrorHandling:       promhttp.HTTPErrorOnError,
	})
	return metrics, handler, nil
}

func (metrics *prometheusMetrics) Record(_ context.Context, measurement Measurement) error {
	if err := measurement.Validate(); err != nil {
		return err
	}
	if metrics == nil {
		return ErrTelemetryMetrics
	}
	collector, found := metrics.collectors[measurement.Name]
	if !found || collector.kind != measurement.Kind {
		return ErrTelemetryMetrics
	}
	labels := measurement.Labels.Map()
	values := make([]string, len(collector.labels))
	for index, name := range collector.labels {
		values[index] = labels[name]
	}
	switch collector.kind {
	case MetricKindCounter:
		collector.counter.WithLabelValues(values...).Add(measurement.Value)
	case MetricKindGauge:
		collector.gauge.WithLabelValues(values...).Set(measurement.Value)
	case MetricKindHistogram:
		collector.histogram.WithLabelValues(values...).Observe(measurement.Value / 1000)
	default:
		return ErrTelemetryMetrics
	}
	return nil
}
