package observability

import (
	"context"
	"errors"
	"testing"
)

func TestMetricsAcceptBoundedLabelsAndDefensiveSnapshots(t *testing.T) {
	labels, err := NewLabels(map[string]string{"node_kind": "safe-writeback", "result": "success"})
	if err != nil {
		t.Fatalf("NewLabels: %v", err)
	}
	measurement, err := NewMeasurement(MetricNodeDuration, MetricKindHistogram, 12.5, labels)
	if err != nil {
		t.Fatalf("NewMeasurement: %v", err)
	}
	metrics := NewMemoryMetrics()
	if err := metrics.Record(context.Background(), measurement); err != nil {
		t.Fatalf("Record: %v", err)
	}

	first := metrics.Snapshot()
	first[0].Labels.values["result"] = "tampered"
	second := metrics.Snapshot()
	if got := second[0].Labels.Map()["result"]; got != "success" {
		t.Fatalf("snapshot mutated stored labels: %q", got)
	}
}

func TestRecordProcessPresenceUsesUnlabelledGaugeAndNoopStillValidates(t *testing.T) {
	metrics := NewMemoryMetrics()
	if err := RecordProcessPresence(context.Background(), metrics); err != nil {
		t.Fatalf("RecordProcessPresence: %v", err)
	}
	snapshot := metrics.Snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("measurements=%d", len(snapshot))
	}
	measurement := snapshot[0]
	if measurement.Name != MetricProcessPresence || measurement.Kind != MetricKindGauge || measurement.Value != 1 {
		t.Fatalf("measurement=%+v", measurement)
	}
	if labels := measurement.Labels.Map(); len(labels) != 0 {
		t.Fatalf("labels=%#v, want none", labels)
	}
	if err := RecordProcessPresence(context.Background(), NewNoopMetrics()); err != nil {
		t.Fatalf("disabled telemetry did not validate process presence: %v", err)
	}
}

func TestRecordTelemetryRequiredUsesUnlabelledBinaryGauge(t *testing.T) {
	for _, test := range []struct {
		name     string
		required bool
		want     float64
	}{
		{name: "required", required: true, want: 1},
		{name: "not required", required: false, want: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			metrics := NewMemoryMetrics()
			if err := RecordTelemetryRequired(context.Background(), metrics, test.required); err != nil {
				t.Fatalf("RecordTelemetryRequired: %v", err)
			}
			snapshot := metrics.Snapshot()
			if len(snapshot) != 1 || snapshot[0].Name != MetricTelemetryRequired ||
				snapshot[0].Kind != MetricKindGauge || snapshot[0].Value != test.want || len(snapshot[0].Labels.Map()) != 0 {
				t.Fatalf("measurements=%+v", snapshot)
			}
		})
	}
}

func TestMetricsRejectHighCardinalityAndSensitiveLabels(t *testing.T) {
	tests := []struct {
		name   string
		labels map[string]string
		want   error
	}{
		{name: "correlation key", labels: map[string]string{"workflow_run_id": "run-1"}, want: ErrUnboundedMetricLabel},
		{name: "uuid value", labels: map[string]string{"queue": "123e4567-e89b-12d3-a456-426614174000"}, want: ErrUnboundedMetricLabel},
		{name: "trace id value", labels: map[string]string{"queue": "0123456789abcdef0123456789abcdef"}, want: ErrUnboundedMetricLabel},
		{name: "absolute path", labels: map[string]string{"queue": "/Users/example/workflow"}, want: ErrSensitiveMetricLabel},
		{name: "token assignment", labels: map[string]string{"queue": "token=metric-secret"}, want: ErrSensitiveMetricLabel},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewLabels(test.labels)
			if !errors.Is(err, test.want) {
				t.Fatalf("NewLabels error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestMetricsEnforcePerMetricLabelContract(t *testing.T) {
	labels, err := NewLabels(map[string]string{"queue": "workflow"})
	if err != nil {
		t.Fatalf("NewLabels: %v", err)
	}
	measurement := Measurement{Name: MetricNodeDuration, Kind: MetricKindHistogram, Value: 1, Labels: labels}
	if err := NewNoopMetrics().Record(context.Background(), measurement); !errors.Is(err, ErrUnboundedMetricLabel) {
		t.Fatalf("Record error = %v, want unbounded label", err)
	}

	missing, err := NewLabels(map[string]string{"node_kind": "safe-writeback"})
	if err != nil {
		t.Fatalf("NewLabels: %v", err)
	}
	measurement = Measurement{Name: MetricNodeDuration, Kind: MetricKindHistogram, Value: 1, Labels: missing}
	if err := measurement.Validate(); !errors.Is(err, ErrInvalidMetric) {
		t.Fatalf("Validate error = %v, want invalid metric", err)
	}
}

func TestModelMetricsAcceptOnlyBoundedCallbackLabels(t *testing.T) {
	labels, err := NewLabels(map[string]string{
		"component": "eino_chat", "phase": "REPAIR", "result": "failure", "error_code": "MODEL_CHAT_TIMEOUT",
	})
	if err != nil {
		t.Fatalf("NewLabels: %v", err)
	}
	for _, measurement := range []Measurement{
		{Name: MetricModelCallDuration, Kind: MetricKindHistogram, Value: 12.5, Labels: labels},
		{Name: MetricModelCallTotal, Kind: MetricKindCounter, Value: 1, Labels: labels},
	} {
		if err := measurement.Validate(); err != nil {
			t.Fatalf("Validate(%s): %v", measurement.Name, err)
		}
	}
	for name, values := range map[string]map[string]string{
		"unknown component": {"component": "provider-secret", "phase": "REPAIR", "result": "failure"},
		"unknown phase":     {"component": "eino_chat", "phase": "CUSTOM", "result": "failure"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewLabels(values); !errors.Is(err, ErrUnboundedMetricLabel) {
				t.Fatalf("NewLabels error = %v, want unbounded label", err)
			}
		})
	}
}

func TestAIRuntimeMetricsAcceptOnlyStableBoundedLabels(t *testing.T) {
	successLabels, err := NewLabels(map[string]string{"result": "success"})
	if err != nil {
		t.Fatalf("NewLabels success: %v", err)
	}
	failureLabels, err := NewLabels(map[string]string{"result": "failure", "error_code": "AGENT_ANSWER_STREAM_FAILED"})
	if err != nil {
		t.Fatalf("NewLabels failure: %v", err)
	}
	refusalLabels, err := NewLabels(map[string]string{"outcome": "refused"})
	if err != nil {
		t.Fatalf("NewLabels refusal: %v", err)
	}
	graphNodeLabels, err := NewLabels(map[string]string{"node_kind": "query_plan", "result": "failure", "error_code": "AGENT_RAG_GRAPH_FAILED"})
	if err != nil {
		t.Fatalf("NewLabels graph node: %v", err)
	}
	degradedLabels, err := NewLabels(map[string]string{"error_code": "DRAFT_STORE_TIMEOUT"})
	if err != nil {
		t.Fatalf("NewLabels degradation: %v", err)
	}

	for _, measurement := range []Measurement{
		{Name: MetricAnswerFirstTokenDuration, Kind: MetricKindHistogram, Value: 10, Labels: successLabels},
		{Name: MetricAnswerCompletionDuration, Kind: MetricKindHistogram, Value: 20, Labels: failureLabels},
		{Name: MetricAnswerResultTotal, Kind: MetricKindCounter, Value: 1, Labels: successLabels},
		{Name: MetricDraftDegradationTotal, Kind: MetricKindCounter, Value: 1, Labels: degradedLabels},
		{Name: MetricAgentIterations, Kind: MetricKindHistogram, Value: 2, Labels: successLabels},
		{Name: MetricAgentToolCalls, Kind: MetricKindHistogram, Value: 1, Labels: successLabels},
		{Name: MetricAgentResultTotal, Kind: MetricKindCounter, Value: 1, Labels: failureLabels},
		{Name: MetricRAGOutcomeTotal, Kind: MetricKindCounter, Value: 1, Labels: refusalLabels},
		{Name: MetricRAGGraphNodeResultTotal, Kind: MetricKindCounter, Value: 1, Labels: graphNodeLabels},
	} {
		if err := measurement.Validate(); err != nil {
			t.Fatalf("Validate(%s): %v", measurement.Name, err)
		}
	}

	for name, values := range map[string]map[string]string{
		"provider label":  {"result": "success", "provider": "openai"},
		"model label":     {"result": "success", "model": "gpt-5"},
		"unknown outcome": {"outcome": "partial"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewLabels(values); !errors.Is(err, ErrUnboundedMetricLabel) {
				t.Fatalf("NewLabels error = %v, want unbounded label", err)
			}
		})
	}
}

func TestWorkspaceAnalysisOutcomeMetricUsesOnlyFixedLowCardinalityLabels(t *testing.T) {
	labels, err := NewLabels(map[string]string{
		"mode":               "workspace_analysis",
		"definition":         "workspace-analysis-v1",
		"outcome":            "failure",
		"termination_reason": "WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED",
	})
	if err != nil {
		t.Fatalf("NewLabels: %v", err)
	}
	measurement, err := NewMeasurement(MetricWorkspaceAnalysisOutcomeTotal, MetricKindCounter, 1, labels)
	if err != nil {
		t.Fatalf("NewMeasurement: %v", err)
	}
	if err := NewNoopMetrics().Record(context.Background(), measurement); err != nil {
		t.Fatalf("Record: %v", err)
	}

	for name, values := range map[string]map[string]string{
		"workspace identity": {
			"mode": "workspace_analysis", "definition": "workspace-analysis-v1", "outcome": "failure",
			"termination_reason": "WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED", "workspace_id": "10000000-0000-4000-8000-000000000001",
		},
		"run identity": {
			"mode": "workspace_analysis", "definition": "workspace-analysis-v1", "outcome": "failure",
			"termination_reason": "WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED", "run_id": "10000000-0000-4000-8000-000000000002",
		},
		"unknown definition": {
			"mode": "workspace_analysis", "definition": "workspace-analysis-v3", "outcome": "failure",
			"termination_reason": "WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED",
		},
		"unknown reason": {
			"mode": "workspace_analysis", "definition": "workspace-analysis-v1", "outcome": "failure",
			"termination_reason": "WORKSPACE_ANALYSIS_PROVIDER_1234567890",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewLabels(values); !errors.Is(err, ErrUnboundedMetricLabel) {
				t.Fatalf("NewLabels error = %v, want %v", err, ErrUnboundedMetricLabel)
			}
		})
	}

	missing, err := NewLabels(map[string]string{"mode": "workspace_analysis", "definition": "workspace-analysis-v1", "outcome": "failure"})
	if err != nil {
		t.Fatalf("NewLabels missing: %v", err)
	}
	if err := (Measurement{Name: MetricWorkspaceAnalysisOutcomeTotal, Kind: MetricKindCounter, Value: 1, Labels: missing}).Validate(); !errors.Is(err, ErrInvalidMetric) {
		t.Fatalf("Validate missing termination reason error = %v, want %v", err, ErrInvalidMetric)
	}

	for _, test := range []struct {
		status string
		reason string
		want   string
	}{
		{status: "succeeded", reason: "COMPLETED", want: "completed"},
		{status: "refused", reason: "WORKSPACE_ANALYSIS_CITATION_INVALID", want: "refused"},
		{status: "clarification_required", reason: "WORKSPACE_ANALYSIS_CLARIFICATION_REQUIRED", want: "clarification_required"},
		{status: "failed", reason: "WORKSPACE_ANALYSIS_RESULT_UNKNOWN", want: "failure"},
		{status: "failed", reason: "WORKSPACE_ANALYSIS_RUNTIME_FAILED", want: "failure"},
		{status: "cancelled", reason: "WORKSPACE_ANALYSIS_CANCELLED", want: "cancelled"},
	} {
		measurement, err := NewWorkspaceAnalysisOutcomeMeasurement(test.status, test.reason)
		if err != nil || measurement.Labels.Map()["outcome"] != test.want {
			t.Fatalf("status=%q reason=%q measurement=%+v err=%v", test.status, test.reason, measurement, err)
		}
	}
	for _, test := range [][2]string{{"running", "COMPLETED"}, {"succeeded", "WORKSPACE_ANALYSIS_RESULT_UNKNOWN"}, {"completed", "WORKSPACE_ANALYSIS_RUNTIME_FAILED"}, {"failed", "WORKSPACE_ANALYSIS_CITATION_INVALID"}} {
		if _, err := NewWorkspaceAnalysisOutcomeMeasurement(test[0], test[1]); !errors.Is(err, ErrInvalidMetric) {
			t.Fatalf("status=%q reason=%q error=%v, want invalid metric", test[0], test[1], err)
		}
	}
}

func TestWorkspaceAnalysisOutcomeMeasurementKeepsDefinitionVersionBounded(t *testing.T) {
	for version, definition := range map[int64]string{1: "workspace-analysis-v1", 2: "workspace-analysis-v2"} {
		measurement, err := NewWorkspaceAnalysisOutcomeMeasurementForVersion(version, "succeeded", "COMPLETED")
		if err != nil || measurement.Validate() != nil || measurement.Labels.Map()["definition"] != definition || len(measurement.Labels.Map()) != 4 {
			t.Fatalf("version=%d measurement=%#v err=%v", version, measurement, err)
		}
	}
	for _, version := range []int64{-1, 0, 3, 9999} {
		if _, err := NewWorkspaceAnalysisOutcomeMeasurementForVersion(version, "succeeded", "COMPLETED"); !errors.Is(err, ErrInvalidMetric) {
			t.Fatalf("version=%d error=%v, want invalid metric", version, err)
		}
	}
	if _, err := NewWorkspaceAnalysisOutcomeMeasurementForVersion(2, "succeeded", "WORKSPACE_ANALYSIS_TOOL_FAILED"); !errors.Is(err, ErrInvalidMetric) {
		t.Fatalf("v2 admitted an impossible terminal pair: %v", err)
	}
	legacy, err := NewWorkspaceAnalysisOutcomeMeasurement("succeeded", "COMPLETED")
	if err != nil || legacy.Labels.Map()["definition"] != "workspace-analysis-v1" {
		t.Fatal("legacy metric definition changed")
	}
}

func TestModelMetricsAllowAgentAndAnswerPhasesOnly(t *testing.T) {
	for _, phase := range []string{"AGENT", "ANSWER"} {
		t.Run(phase, func(t *testing.T) {
			labels, err := NewLabels(map[string]string{"component": "eino_chat", "phase": phase, "result": "success"})
			if err != nil {
				t.Fatalf("NewLabels: %v", err)
			}
			if err := (Measurement{Name: MetricModelCallTotal, Kind: MetricKindCounter, Value: 1, Labels: labels}).Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
}

func TestMemoryMetricsRejectRecordAfterProviderShutdown(t *testing.T) {
	provider := NewMemoryProvider()
	labels, err := NewLabels(map[string]string{"queue": "workflow"})
	if err != nil {
		t.Fatalf("NewLabels: %v", err)
	}
	measurement, err := NewMeasurement(MetricQueueDepth, MetricKindGauge, 0, labels)
	if err != nil {
		t.Fatalf("NewMeasurement: %v", err)
	}
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := provider.Metrics().Record(context.Background(), measurement); !errors.Is(err, ErrObservabilityClosed) {
		t.Fatalf("Record after shutdown = %v, want closed", err)
	}
}
