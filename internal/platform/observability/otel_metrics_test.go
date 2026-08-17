package observability

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	collectmetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	"google.golang.org/protobuf/proto"
)

func TestOTLPMetricsExportWorkspaceOutcomeWithExactResourceAndPrometheusFanout(t *testing.T) {
	certificatePath, keyPath := writeTestClientCertificate(t)
	for key, value := range map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT":                              "http://127.0.0.1:1/environment-base",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT":                      "http://127.0.0.1:1/environment-metrics",
		"OTEL_EXPORTER_OTLP_HEADERS":                               "authorization=environment-secret",
		"OTEL_EXPORTER_OTLP_METRICS_HEADERS":                       "x-environment-secret=present",
		"OTEL_EXPORTER_OTLP_COMPRESSION":                           "gzip",
		"OTEL_EXPORTER_OTLP_METRICS_COMPRESSION":                   "gzip",
		"OTEL_EXPORTER_OTLP_TIMEOUT":                               "60000",
		"OTEL_EXPORTER_OTLP_METRICS_TIMEOUT":                       "60000",
		"OTEL_EXPORTER_OTLP_INSECURE":                              "false",
		"OTEL_EXPORTER_OTLP_METRICS_INSECURE":                      "false",
		"OTEL_EXPORTER_OTLP_CERTIFICATE":                           certificatePath,
		"OTEL_EXPORTER_OTLP_METRICS_CERTIFICATE":                   certificatePath,
		"OTEL_EXPORTER_OTLP_CLIENT_CERTIFICATE":                    certificatePath,
		"OTEL_EXPORTER_OTLP_CLIENT_KEY":                            keyPath,
		"OTEL_EXPORTER_OTLP_METRICS_CLIENT_CERTIFICATE":            certificatePath,
		"OTEL_EXPORTER_OTLP_METRICS_CLIENT_KEY":                    keyPath,
		"OTEL_METRIC_EXPORT_INTERVAL":                              "1",
		"OTEL_METRIC_EXPORT_TIMEOUT":                               "60000",
		"OTEL_EXPORTER_OTLP_METRICS_TEMPORALITY_PREFERENCE":        "delta",
		"OTEL_EXPORTER_OTLP_METRICS_DEFAULT_HISTOGRAM_AGGREGATION": "base2_exponential_bucket_histogram",
		"OTEL_METRICS_EXEMPLAR_FILTER":                             "always_on",
		"OTEL_GO_X_CARDINALITY_LIMIT":                              "1",
		"OTEL_SERVICE_NAME":                                        "environment-service",
		"OTEL_RESOURCE_ATTRIBUTES":                                 "environment.secret=must-not-export,deployment.environment=poisoned",
	} {
		t.Setenv(key, value)
	}

	var capture otlpMetricRequestCapture
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/x-protobuf")
		switch request.URL.Path {
		case "/collector/base/v1/traces":
			response.WriteHeader(http.StatusOK)
		case "/collector/base/v1/metrics":
			capture.record(t, request)
			encoded, err := proto.Marshal(&collectmetricspb.ExportMetricsServiceResponse{})
			if err != nil {
				t.Errorf("marshal OTLP metric response: %v", err)
				response.WriteHeader(http.StatusInternalServerError)
				return
			}
			response.WriteHeader(http.StatusOK)
			_, _ = response.Write(encoded)
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)

	telemetry, err := InitializeTelemetry(context.Background(), TelemetryOptions{
		Mode: TelemetryModeRequired, Endpoint: server.URL + "/collector/base/",
		ServiceName: "zhixu-worker", ServiceVersion: "v-test", Environment: "integration",
	})
	if err != nil {
		t.Fatalf("InitializeTelemetry: %v", err)
	}
	measurement, err := NewWorkspaceAnalysisOutcomeMeasurement("succeeded", "COMPLETED")
	if err != nil {
		t.Fatalf("NewWorkspaceAnalysisOutcomeMeasurement: %v", err)
	}
	if err := telemetry.Metrics().Record(context.Background(), measurement); err != nil {
		t.Fatalf("Record workspace outcome: %v", err)
	}

	local := httptest.NewRecorder()
	telemetry.MetricsHandler().ServeHTTP(local, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if local.Code != http.StatusOK || !strings.Contains(local.Body.String(),
		`zhixu_workspace_analysis_outcome_total{definition="workspace-analysis-v1",mode="workspace_analysis",outcome="completed",termination_reason="COMPLETED"} 1`) {
		t.Fatalf("local Prometheus fanout did not expose the Workspace Analysis outcome: status=%d", local.Code)
	}
	if err := telemetry.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := telemetry.Metrics().Record(context.Background(), measurement); !errors.Is(err, ErrObservabilityClosed) {
		t.Fatalf("Record after shutdown error=%v", err)
	}

	requests := capture.snapshot()
	if len(requests) != 2 {
		t.Fatalf("metric export requests=%d want startup probe plus shutdown flush", len(requests))
	}
	assertWorkspaceAnalysisOTLPMetric(t, requests)
}

func TestTelemetryMetricProbeControlsRequiredAndOptionalModes(t *testing.T) {
	for _, mode := range []TelemetryMode{TelemetryModeRequired, TelemetryModeOptional} {
		t.Run(string(mode), func(t *testing.T) {
			var metricRequests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if strings.HasSuffix(request.URL.Path, "/v1/metrics") {
					metricRequests.Add(1)
					http.Error(response, "collector-secret-response", http.StatusServiceUnavailable)
					return
				}
				response.Header().Set("Content-Type", "application/x-protobuf")
				response.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(server.Close)

			telemetry, err := InitializeTelemetry(context.Background(), TelemetryOptions{
				Mode: mode, Endpoint: server.URL,
				ServiceName: "zhixu-worker", ServiceVersion: "v-test", Environment: "integration",
			})
			if mode == TelemetryModeRequired {
				if telemetry != nil || !errors.Is(err, ErrTelemetryExporterRequired) ||
					strings.Contains(err.Error(), "collector-secret-response") || strings.Contains(err.Error(), server.URL) {
					t.Fatalf("required metric probe result telemetry=%v error=%v", telemetry != nil, err)
				}
			} else {
				if err != nil {
					t.Fatalf("optional InitializeTelemetry: %v", err)
				}
				defer func() { _ = telemetry.Shutdown(context.Background()) }()
				want := TelemetryStatus{Mode: TelemetryModeOptional, Degraded: true, Code: TelemetryStatusExporterUnavailable}
				if telemetry.Status() != want {
					t.Fatalf("optional status=%#v want=%#v", telemetry.Status(), want)
				}
				measurement, measurementErr := NewWorkspaceAnalysisOutcomeMeasurement("succeeded", "COMPLETED")
				if measurementErr != nil {
					t.Fatal(measurementErr)
				}
				if recordErr := telemetry.Metrics().Record(context.Background(), measurement); recordErr != nil {
					t.Fatalf("optional local Record: %v", recordErr)
				}
			}
			if requests := metricRequests.Load(); requests < 2 || requests > 20 {
				t.Fatalf("metric startup probe requests=%d want bounded retry", requests)
			}
		})
	}
}

func TestOTLPMetricExporterRefusesRedirectedPayload(t *testing.T) {
	var redirectedRequests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectedRequests.Add(1)
	}))
	t.Cleanup(target.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/v1/metrics") {
			http.Redirect(response, request, target.URL+"/stolen", http.StatusTemporaryRedirect)
			return
		}
		response.Header().Set("Content-Type", "application/x-protobuf")
		response.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(redirector.Close)

	_, err := InitializeTelemetry(context.Background(), TelemetryOptions{
		Mode: TelemetryModeRequired, Endpoint: redirector.URL,
		ServiceName: "zhixu-worker", ServiceVersion: "v-test", Environment: "integration",
	})
	if !errors.Is(err, ErrTelemetryExporterRequired) {
		t.Fatalf("InitializeTelemetry error=%v", err)
	}
	if redirectedRequests.Load() != 0 {
		t.Fatalf("redirect target received %d metric payloads", redirectedRequests.Load())
	}
}

func TestTelemetryShutdownFlushesMetricsWhileTraceExporterIsBlocked(t *testing.T) {
	var traceRequests, metricRequests atomic.Int64
	traceBlocked := make(chan struct{})
	metricFlushed := make(chan struct{})
	releaseTrace := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/x-protobuf")
		if strings.HasSuffix(request.URL.Path, "/v1/traces") {
			if traceRequests.Add(1) > 1 {
				select {
				case <-traceBlocked:
				default:
					close(traceBlocked)
				}
				<-releaseTrace
			}
			response.WriteHeader(http.StatusOK)
			return
		}
		if strings.HasSuffix(request.URL.Path, "/v1/metrics") {
			if metricRequests.Add(1) > 1 {
				select {
				case <-metricFlushed:
				default:
					close(metricFlushed)
				}
			}
			response.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(response, request)
	}))
	t.Cleanup(server.Close)

	telemetry, err := InitializeTelemetry(context.Background(), TelemetryOptions{
		Mode: TelemetryModeRequired, Endpoint: server.URL,
		ServiceName: "zhixu-worker", ServiceVersion: "v-test", Environment: "integration",
	})
	if err != nil {
		t.Fatalf("InitializeTelemetry: %v", err)
	}
	_, span, err := telemetry.Tracer().Start(context.Background(), "shutdown.concurrent")
	if err != nil {
		t.Fatal(err)
	}
	span.End()
	measurement, err := NewWorkspaceAnalysisOutcomeMeasurement("succeeded", "COMPLETED")
	if err != nil {
		t.Fatal(err)
	}
	if err := telemetry.Metrics().Record(context.Background(), measurement); err != nil {
		t.Fatal(err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- telemetry.Shutdown(shutdownCtx) }()
	select {
	case <-traceBlocked:
	case <-time.After(time.Second):
		t.Fatal("trace exporter did not block during shutdown")
	}
	select {
	case <-metricFlushed:
	case <-time.After(time.Second):
		t.Fatal("metric shutdown flush was starved by the blocked trace exporter")
	}
	close(releaseTrace)
	if err := <-shutdownDone; err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
}

type capturedOTLPMetricRequest struct {
	path              string
	authorization     string
	environmentHeader string
	contentEncoding   string
	payload           *collectmetricspb.ExportMetricsServiceRequest
}

type otlpMetricRequestCapture struct {
	mutex    sync.Mutex
	requests []capturedOTLPMetricRequest
}

func (capture *otlpMetricRequestCapture) record(t *testing.T, request *http.Request) {
	t.Helper()
	body, err := io.ReadAll(io.LimitReader(request.Body, otlpMaxRequestSize+1))
	if err != nil || len(body) > otlpMaxRequestSize {
		t.Errorf("read bounded OTLP metric body: bytes=%d error=%v", len(body), err)
		return
	}
	payload := new(collectmetricspb.ExportMetricsServiceRequest)
	if err := proto.Unmarshal(body, payload); err != nil {
		t.Errorf("unmarshal OTLP metric payload: %v", err)
		return
	}
	capture.mutex.Lock()
	defer capture.mutex.Unlock()
	capture.requests = append(capture.requests, capturedOTLPMetricRequest{
		path: request.URL.Path, authorization: request.Header.Get("Authorization"),
		environmentHeader: request.Header.Get("X-Environment-Secret"),
		contentEncoding:   request.Header.Get("Content-Encoding"), payload: payload,
	})
}

func (capture *otlpMetricRequestCapture) snapshot() []capturedOTLPMetricRequest {
	capture.mutex.Lock()
	defer capture.mutex.Unlock()
	return append([]capturedOTLPMetricRequest(nil), capture.requests...)
}

func assertWorkspaceAnalysisOTLPMetric(t *testing.T, requests []capturedOTLPMetricRequest) {
	t.Helper()
	found := false
	for _, request := range requests {
		if request.path != "/collector/base/v1/metrics" || request.authorization != "" ||
			request.environmentHeader != "" || request.contentEncoding != "" {
			t.Fatalf("unsafe OTLP metric request metadata: %#v", request)
		}
		for _, resourceMetrics := range request.payload.ResourceMetrics {
			resourceAttributes := otlpAttributes(resourceMetrics.Resource.Attributes)
			wantResource := map[string]string{
				"service.name": "zhixu-worker", "service.version": "v-test", "deployment.environment": "integration",
			}
			if len(resourceAttributes) != len(wantResource) {
				t.Fatalf("OTLP metric resource=%#v", resourceAttributes)
			}
			for key, value := range wantResource {
				if resourceAttributes[key] != value {
					t.Fatalf("OTLP metric resource[%q]=%q want=%q", key, resourceAttributes[key], value)
				}
			}
			for _, scopeMetrics := range resourceMetrics.ScopeMetrics {
				for _, metric := range scopeMetrics.Metrics {
					if metric.Name != string(MetricWorkspaceAnalysisOutcomeTotal) {
						continue
					}
					sum := metric.GetSum()
					if sum == nil || !sum.IsMonotonic ||
						sum.AggregationTemporality != metricspb.AggregationTemporality_AGGREGATION_TEMPORALITY_CUMULATIVE {
						t.Fatalf("Workspace Analysis metric aggregation=%#v", sum)
					}
					for _, point := range sum.DataPoints {
						labels := otlpAttributes(point.Attributes)
						wantLabels := map[string]string{
							"mode": "workspace_analysis", "definition": "workspace-analysis-v1",
							"outcome": "completed", "termination_reason": "COMPLETED",
						}
						if len(labels) != len(wantLabels) {
							t.Fatalf("Workspace Analysis metric labels=%#v", labels)
						}
						for key, value := range wantLabels {
							if labels[key] != value {
								t.Fatalf("Workspace Analysis metric labels=%#v", labels)
							}
						}
						if point.GetAsDouble() != 1 {
							t.Fatalf("Workspace Analysis metric value=%v", point.GetAsDouble())
						}
						found = true
					}
				}
			}
		}
	}
	if !found {
		t.Fatal("Workspace Analysis OTLP outcome metric was not exported")
	}
}
