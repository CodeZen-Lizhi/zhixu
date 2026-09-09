package observability

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestTelemetryMetricsHandlerExposesValidatedMeasurements(t *testing.T) {
	telemetry, err := InitializeTelemetry(context.Background(), TelemetryOptions{Mode: TelemetryModeDisabled})
	if err != nil {
		t.Fatalf("InitializeTelemetry: %v", err)
	}
	t.Cleanup(func() { _ = telemetry.Shutdown(context.Background()) })

	recordMetric(t, telemetry.Metrics(), MetricQueueDepth, MetricKindGauge, 7, map[string]string{"queue": "workflow"})
	recordMetric(t, telemetry.Metrics(), MetricNodeResultTotal, MetricKindCounter, 2, map[string]string{"node_kind": "safe-writeback", "result": "success"})
	recordMetric(t, telemetry.Metrics(), MetricNodeDuration, MetricKindHistogram, 250, map[string]string{"node_kind": "safe-writeback", "result": "success"})

	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	request.Header.Set("Accept", "application/openmetrics-text")
	response := httptest.NewRecorder()
	telemetry.MetricsHandler().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("metrics status=%d body=%s", response.Code, response.Body.String())
	}
	if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/openmetrics-text") {
		t.Fatalf("Content-Type=%q", contentType)
	}
	body := response.Body.String()
	for _, expected := range []string{
		"# TYPE zhixu_river_queue_depth gauge",
		`zhixu_river_queue_depth{queue="workflow"} 7`,
		"# TYPE zhixu_workflow_node_result counter",
		`zhixu_workflow_node_result_total{error_code="",node_kind="safe-writeback",result="success"} 2`,
		"# TYPE zhixu_workflow_node_duration_seconds histogram",
		`zhixu_workflow_node_duration_seconds_sum{node_kind="safe-writeback",result="success"} 0.25`,
		"# TYPE go_gc_duration_seconds summary",
		"# TYPE process_cpu_seconds counter",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("metrics body does not contain %q:\n%s", expected, body)
		}
	}
}

func TestPrometheusAdapterMapsEveryMetricAndRejectsBeforeMutation(t *testing.T) {
	telemetry, err := InitializeTelemetry(context.Background(), TelemetryOptions{Mode: TelemetryModeDisabled})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = telemetry.Shutdown(context.Background()) })

	recordMetric(t, telemetry.Metrics(), MetricQueueDepth, MetricKindGauge, 2, map[string]string{"queue": "workflow"})
	recordMetric(t, telemetry.Metrics(), MetricQueueDepth, MetricKindGauge, 5, map[string]string{"queue": "workflow"})
	recordMetric(t, telemetry.Metrics(), MetricActiveWorkers, MetricKindGauge, 3, map[string]string{"queue": "workflow"})
	recordMetric(t, telemetry.Metrics(), MetricNodeDuration, MetricKindHistogram, 250, map[string]string{"node_kind": "hash", "result": "success"})
	recordMetric(t, telemetry.Metrics(), MetricNodeResultTotal, MetricKindCounter, 1, map[string]string{"node_kind": "hash", "result": "success"})
	recordMetric(t, telemetry.Metrics(), MetricRetryTotal, MetricKindCounter, 1, map[string]string{"node_kind": "hash"})
	recordMetric(t, telemetry.Metrics(), MetricManualRecoveryTotal, MetricKindCounter, 1, map[string]string{"node_kind": "hash", "error_code": "WRITE_CONFLICT"})
	recordMetric(t, telemetry.Metrics(), MetricLeaseExpiryTotal, MetricKindCounter, 1, map[string]string{"node_kind": "hash"})
	recordMetric(t, telemetry.Metrics(), MetricHeartbeatFailureTotal, MetricKindCounter, 1, map[string]string{"node_kind": "hash"})
	recordMetric(t, telemetry.Metrics(), MetricDuplicateDeliveryTotal, MetricKindCounter, 1, map[string]string{"node_kind": "hash"})
	recordMetric(t, telemetry.Metrics(), MetricShutdownTotal, MetricKindCounter, 1, map[string]string{"shutdown_kind": "graceful", "result": "success"})
	recordMetric(t, telemetry.Metrics(), MetricProcessPresence, MetricKindGauge, 1, nil)
	recordMetric(t, telemetry.Metrics(), MetricTelemetryRequired, MetricKindGauge, 1, nil)
	recordMetric(t, telemetry.Metrics(), MetricModelCallDuration, MetricKindHistogram, 25, map[string]string{"component": "eino_chat", "phase": "ANSWER", "result": "success"})
	recordMetric(t, telemetry.Metrics(), MetricModelCallTotal, MetricKindCounter, 1, map[string]string{"component": "eino_chat", "phase": "ANSWER", "result": "success"})
	recordMetric(t, telemetry.Metrics(), MetricAnswerFirstTokenDuration, MetricKindHistogram, 10, map[string]string{"result": "success"})
	recordMetric(t, telemetry.Metrics(), MetricAnswerCompletionDuration, MetricKindHistogram, 30, map[string]string{"result": "success"})
	recordMetric(t, telemetry.Metrics(), MetricAnswerResultTotal, MetricKindCounter, 1, map[string]string{"result": "success"})
	recordMetric(t, telemetry.Metrics(), MetricDraftDegradationTotal, MetricKindCounter, 1, map[string]string{"error_code": "DRAFT_STORE_TIMEOUT"})
	recordMetric(t, telemetry.Metrics(), MetricAgentIterations, MetricKindHistogram, 2, map[string]string{"result": "success"})
	recordMetric(t, telemetry.Metrics(), MetricAgentToolCalls, MetricKindHistogram, 1, map[string]string{"result": "success"})
	recordMetric(t, telemetry.Metrics(), MetricAgentResultTotal, MetricKindCounter, 1, map[string]string{"result": "success"})
	recordMetric(t, telemetry.Metrics(), MetricRAGOutcomeTotal, MetricKindCounter, 1, map[string]string{"outcome": "completed"})
	recordMetric(t, telemetry.Metrics(), MetricRAGGraphNodeResultTotal, MetricKindCounter, 1, map[string]string{"node_kind": "query_plan", "result": "success"})
	recordMetric(t, telemetry.Metrics(), MetricWorkspaceAnalysisOutcomeTotal, MetricKindCounter, 1, map[string]string{
		"mode": "workspace_analysis", "definition": "workspace-analysis-v1", "outcome": "failure", "termination_reason": "WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED",
	})
	v2, err := NewWorkspaceAnalysisOutcomeMeasurementForVersion(2, "succeeded", "COMPLETED")
	if err != nil {
		t.Fatal(err)
	}
	if err := telemetry.Metrics().Record(context.Background(), v2); err != nil {
		t.Fatal(err)
	}

	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			labels, labelErr := NewLabels(map[string]string{"node_kind": "hash"})
			if labelErr != nil {
				t.Errorf("NewLabels: %v", labelErr)
				return
			}
			measurement, measurementErr := NewMeasurement(MetricRetryTotal, MetricKindCounter, 1, labels)
			if measurementErr != nil {
				t.Errorf("NewMeasurement: %v", measurementErr)
				return
			}
			if recordErr := telemetry.Metrics().Record(context.Background(), measurement); recordErr != nil {
				t.Errorf("Record: %v", recordErr)
			}
		}()
	}
	wait.Wait()

	invalid := Measurement{
		Name: MetricQueueDepth, Kind: MetricKindGauge, Value: 99,
		Labels: Labels{values: map[string]string{"queue": "workflow", "workspace_id": "workspace-secret"}},
	}
	if err := telemetry.Metrics().Record(context.Background(), invalid); !errors.Is(err, ErrUnboundedMetricLabel) {
		t.Fatalf("invalid Record error=%v", err)
	}

	body := scrapeMetrics(t, telemetry.MetricsHandler())
	for _, expected := range []string{
		`zhixu_runtime_process_presence 1`,
		`zhixu_runtime_telemetry_required 1`,
		`zhixu_river_queue_depth{queue="workflow"} 5`,
		`zhixu_river_workers_active{queue="workflow"} 3`,
		`zhixu_workflow_node_duration_seconds_sum{node_kind="hash",result="success"} 0.25`,
		`zhixu_workflow_node_result_total{error_code="",node_kind="hash",result="success"} 1`,
		`zhixu_workflow_retry_total{error_code="",node_kind="hash"} 33`,
		`zhixu_workflow_manual_recovery_total{error_code="WRITE_CONFLICT",node_kind="hash"} 1`,
		`zhixu_workflow_lease_expiry_total{node_kind="hash"} 1`,
		`zhixu_workflow_heartbeat_failure_total{error_code="",node_kind="hash"} 1`,
		`zhixu_river_duplicate_delivery_total{node_kind="hash"} 1`,
		`zhixu_worker_shutdown_total{result="success",shutdown_kind="graceful"} 1`,
		`zhixu_model_chat_duration_seconds_sum{component="eino_chat",error_code="",phase="ANSWER",result="success"} 0.025`,
		`zhixu_model_chat_result_total{component="eino_chat",error_code="",phase="ANSWER",result="success"} 1`,
		`zhixu_agent_answer_first_token_duration_seconds_sum{error_code="",result="success"} 0.01`,
		`zhixu_agent_answer_completion_duration_seconds_sum{error_code="",result="success"} 0.03`,
		`zhixu_agent_answer_result_total{error_code="",result="success"} 1`,
		`zhixu_agent_draft_degradation_total{error_code="DRAFT_STORE_TIMEOUT"} 1`,
		`zhixu_agent_runtime_iterations_sum{error_code="",result="success"} 2`,
		`zhixu_agent_runtime_tool_calls_sum{error_code="",result="success"} 1`,
		`zhixu_agent_runtime_result_total{error_code="",result="success"} 1`,
		`zhixu_rag_outcome_total{error_code="",outcome="completed"} 1`,
		`zhixu_agent_rag_graph_node_result_total{error_code="",node_kind="query_plan",result="success"} 1`,
		`zhixu_workspace_analysis_outcome_total{definition="workspace-analysis-v1",mode="workspace_analysis",outcome="failure",termination_reason="WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED"} 1`,
		`zhixu_workspace_analysis_outcome_total{definition="workspace-analysis-v2",mode="workspace_analysis",outcome="completed",termination_reason="COMPLETED"} 1`,
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("metrics body does not contain %q:\n%s", expected, body)
		}
	}
	if strings.Contains(body, "workspace-secret") || strings.Contains(body, "workspace_id") || strings.Contains(body, `zhixu_river_queue_depth{queue="workflow"} 99`) {
		t.Fatalf("invalid measurement mutated exposition:\n%s", body)
	}
}

func TestPrometheusMetricDefinitionsCoverStableMetricRegistry(t *testing.T) {
	if len(prometheusMetricDefinitions) != len(metricDefinitions) {
		t.Fatalf("Prometheus definitions=%d, stable metric definitions=%d", len(prometheusMetricDefinitions), len(metricDefinitions))
	}
	for _, name := range MetricNames() {
		definition, found := prometheusMetricDefinitions[name]
		if !found {
			t.Fatalf("missing Prometheus definition for %q", name)
		}
		if definition.name == "" || definition.help == "" {
			t.Fatalf("incomplete Prometheus definition for %q: %#v", name, definition)
		}
		if metricDefinitions[name].kind == MetricKindHistogram && definition.histogramDivisor <= 0 {
			t.Fatalf("missing histogram divisor for %q", name)
		}
	}
	for name := range prometheusMetricDefinitions {
		if _, found := metricDefinitions[name]; !found {
			t.Fatalf("unknown Prometheus metric definition %q", name)
		}
	}
}

func scrapeMetrics(t *testing.T, handler http.Handler) string {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("metrics status=%d body=%s", response.Code, response.Body.String())
	}
	return response.Body.String()
}

func recordMetric(t *testing.T, metrics Metrics, name MetricName, kind MetricKind, value float64, rawLabels map[string]string) {
	t.Helper()
	labels, err := NewLabels(rawLabels)
	if err != nil {
		t.Fatalf("NewLabels(%s): %v", name, err)
	}
	measurement, err := NewMeasurement(name, kind, value, labels)
	if err != nil {
		t.Fatalf("NewMeasurement(%s): %v", name, err)
	}
	if err := metrics.Record(context.Background(), measurement); err != nil {
		t.Fatalf("Record(%s): %v", name, err)
	}
}
