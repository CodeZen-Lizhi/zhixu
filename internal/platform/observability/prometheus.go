package observability

import (
	"context"
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const prometheusNamespace = "zhixu"

type prometheusMetricDefinition struct {
	name             string
	help             string
	histogramDivisor float64
}

var prometheusMetricDefinitions = map[MetricName]prometheusMetricDefinition{
	MetricProcessPresence:               {name: "runtime_process_presence", help: "Whether this process is present; it is not a readiness signal."},
	MetricTelemetryRequired:             {name: "runtime_telemetry_required", help: "Whether this process requires telemetry export."},
	MetricQueueDepth:                    {name: "river_queue_depth", help: "Immediately available River jobs in the configured queue."},
	MetricActiveWorkers:                 {name: "river_workers_active", help: "Runtime node workers currently executing in this process."},
	MetricNodeDuration:                  {name: "workflow_node_duration_seconds", help: "Persisted workflow node attempt duration in seconds.", histogramDivisor: 1000},
	MetricNodeResultTotal:               {name: "workflow_node_result_total", help: "Persisted workflow node attempt results."},
	MetricRetryTotal:                    {name: "workflow_retry_total", help: "Persisted workflow node retries."},
	MetricManualRecoveryTotal:           {name: "workflow_manual_recovery_total", help: "Persisted workflow node manual recovery transitions."},
	MetricLeaseExpiryTotal:              {name: "workflow_lease_expiry_total", help: "Persisted workflow node lease expirations."},
	MetricHeartbeatFailureTotal:         {name: "workflow_heartbeat_failure_total", help: "Workflow node heartbeat failures."},
	MetricDuplicateDeliveryTotal:        {name: "river_duplicate_delivery_total", help: "River deliveries identified as duplicates by the workflow claim transaction."},
	MetricShutdownTotal:                 {name: "worker_shutdown_total", help: "Worker process shutdown outcomes."},
	MetricModelCallDuration:             {name: "model_chat_duration_seconds", help: "Eino Chat callback call duration in seconds.", histogramDivisor: 1000},
	MetricModelCallTotal:                {name: "model_chat_result_total", help: "Eino Chat callback call results."},
	MetricAnswerFirstTokenDuration:      {name: "agent_answer_first_token_duration_seconds", help: "Final answer stream time to first token in seconds.", histogramDivisor: 1000},
	MetricAnswerCompletionDuration:      {name: "agent_answer_completion_duration_seconds", help: "Final answer stream completion duration in seconds.", histogramDivisor: 1000},
	MetricAnswerResultTotal:             {name: "agent_answer_result_total", help: "Final answer stream results."},
	MetricDraftDegradationTotal:         {name: "agent_draft_degradation_total", help: "Draft stream persistence degradation events."},
	MetricAgentIterations:               {name: "agent_runtime_iterations", help: "Eino Agent iterations used per run.", histogramDivisor: 1},
	MetricAgentToolCalls:                {name: "agent_runtime_tool_calls", help: "Frozen tool calls used per Eino Agent run.", histogramDivisor: 1},
	MetricAgentResultTotal:              {name: "agent_runtime_result_total", help: "Eino Agent run results."},
	MetricRAGOutcomeTotal:               {name: "rag_outcome_total", help: "RAG business and runtime outcomes."},
	MetricRAGGraphNodeResultTotal:       {name: "agent_rag_graph_node_result_total", help: "Eino RAG Graph node results."},
	MetricWorkspaceAnalysisOutcomeTotal: {name: "workspace_analysis_outcome_total", help: "Terminal outcomes for restricted workspace analysis runs."},
}

type prometheusCollector struct {
	kind             MetricKind
	labels           []string
	histogramDivisor float64
	counter          *prometheus.CounterVec
	gauge            *prometheus.GaugeVec
	histogram        *prometheus.HistogramVec
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
		collector := prometheusCollector{
			kind:             definition.kind,
			labels:           append([]string(nil), definition.labels...),
			histogramDivisor: prometheusDefinition.histogramDivisor,
		}
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
			if collector.histogramDivisor <= 0 {
				return nil, nil, ErrTelemetryMetrics
			}
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
		collector.histogram.WithLabelValues(values...).Observe(measurement.Value / collector.histogramDivisor)
	default:
		return ErrTelemetryMetrics
	}
	return nil
}
