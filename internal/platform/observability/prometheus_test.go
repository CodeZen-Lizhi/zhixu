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
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("metrics body does not contain %q:\n%s", expected, body)
		}
	}
	if strings.Contains(body, "workspace-secret") || strings.Contains(body, "workspace_id") || strings.Contains(body, `zhixu_river_queue_depth{queue="workflow"} 99`) {
		t.Fatalf("invalid measurement mutated exposition:\n%s", body)
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
