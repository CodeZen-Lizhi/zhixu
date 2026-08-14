package observability

import (
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	collectormetrics "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectortrace "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	metricspb "go.opentelemetry.io/proto/otlp/metrics/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestOTLPHTTPProviderExportsProjectMetricsAndTraces(t *testing.T) {
	receiver := newOTLPTestReceiver(t)
	server := httptest.NewServer(receiver)
	defer server.Close()

	telemetry, err := InitializeTelemetry(context.Background(), TelemetryOptions{
		Mode:     TelemetryModeRequired,
		Endpoint: server.URL + "/collector-prefix",
		Factory:  NewOTLPHTTPProviderFactory("zhixu-worker", "2026.08.10"),
	})
	if err != nil {
		t.Fatalf("InitializeTelemetry: %v", err)
	}
	if status := telemetry.Status(); !status.Exporting || status.Degraded || status.Code != TelemetryStatusExporting {
		t.Fatalf("status = %#v", status)
	}

	labels, err := NewLabels(map[string]string{"queue": "workflow"})
	if err != nil {
		t.Fatalf("NewLabels: %v", err)
	}
	measurement, err := NewMeasurement(MetricQueueDepth, MetricKindGauge, 3, labels)
	if err != nil {
		t.Fatalf("NewMeasurement: %v", err)
	}
	if err := telemetry.Metrics().Record(context.Background(), measurement); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := RecordProcessPresence(context.Background(), telemetry.Metrics()); err != nil {
		t.Fatalf("RecordProcessPresence: %v", err)
	}
	if err := RecordTelemetryRequired(context.Background(), telemetry.Metrics(), true); err != nil {
		t.Fatalf("RecordTelemetryRequired: %v", err)
	}

	parent := TraceContext{
		TraceID: "00112233445566778899aabbccddeeff",
		SpanID:  "0123456789abcdef", TraceFlags: "01",
	}
	parentContext, err := WithTraceContext(context.Background(), parent)
	if err != nil {
		t.Fatalf("WithTraceContext: %v", err)
	}
	parentContext = WithCorrelation(parentContext, Correlation{WorkspaceID: "workspace-1"})
	childContext, span, err := telemetry.Tracer().Start(parentContext, "agent.answer.stream",
		TraceAttribute{Key: "component", Value: "eino_chat"},
		TraceAttribute{Key: "token", Value: "must-not-leak"},
	)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	child, found := TraceContextFromContext(childContext)
	if !found || child.TraceID != parent.TraceID || child.SpanID == parent.SpanID {
		t.Fatalf("child trace context = %#v, found=%t", child, found)
	}
	span.RecordError("MODEL_CHAT_REJECTED")
	span.End()
	span.End()

	shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := telemetry.Shutdown(shutdownContext); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	metricsRequest, tracesRequest := receiver.snapshot(t)
	assertOTLPMetricExport(t, metricsRequest, "zhixu-worker", "2026.08.10")
	assertOTLPProcessPresenceExport(t, metricsRequest, "zhixu-worker", "2026.08.10")
	assertOTLPTelemetryRequiredExport(t, metricsRequest, "zhixu-worker", "2026.08.10")
	assertOTLPTraceExport(t, tracesRequest, parent)

	if err := telemetry.Metrics().Record(context.Background(), measurement); !errors.Is(err, ErrObservabilityClosed) {
		t.Fatalf("Record after shutdown = %v", err)
	}
	if _, _, err := telemetry.Tracer().Start(context.Background(), "closed.span"); !errors.Is(err, ErrObservabilityClosed) {
		t.Fatalf("Start after shutdown = %v", err)
	}
}

func TestOTLPHTTPProviderShutdownFlushesMetricsWhileTraceExportIsBlocked(t *testing.T) {
	metricReceived := make(chan struct{})
	traceStarted := make(chan struct{})
	releaseTrace := make(chan struct{})
	var metricOnce, traceOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/metrics":
			metricOnce.Do(func() { close(metricReceived) })
			writer.Header().Set("Content-Type", "application/x-protobuf")
			_, _ = writer.Write([]byte{})
		case "/v1/traces":
			traceOnce.Do(func() { close(traceStarted) })
			<-releaseTrace
			writer.Header().Set("Content-Type", "application/x-protobuf")
			_, _ = writer.Write([]byte{})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	telemetry, err := InitializeTelemetry(context.Background(), TelemetryOptions{
		Mode: TelemetryModeRequired, Endpoint: server.URL,
		Factory: NewOTLPHTTPProviderFactory("zhixu-worker", "flush-test"),
	})
	if err != nil {
		t.Fatalf("InitializeTelemetry: %v", err)
	}
	labels, err := NewLabels(map[string]string{"queue": "workflow"})
	if err != nil {
		t.Fatalf("NewLabels: %v", err)
	}
	measurement, err := NewMeasurement(MetricQueueDepth, MetricKindGauge, 1, labels)
	if err != nil {
		t.Fatalf("NewMeasurement: %v", err)
	}
	if err := telemetry.Metrics().Record(context.Background(), measurement); err != nil {
		t.Fatalf("Record: %v", err)
	}
	_, span, err := telemetry.Tracer().Start(context.Background(), "shutdown.flush")
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	span.End()

	shutdownContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	shutdownDone := make(chan error, 1)
	go func() { shutdownDone <- telemetry.Shutdown(shutdownContext) }()
	traceObserved := false
	select {
	case <-traceStarted:
		traceObserved = true
	case <-time.After(500 * time.Millisecond):
	}
	metricObserved := false
	select {
	case <-metricReceived:
		metricObserved = true
	case <-time.After(500 * time.Millisecond):
	}
	close(releaseTrace)
	shutdownErr := <-shutdownDone
	if !traceObserved {
		t.Fatal("trace exporter did not enter the blocked request")
	}
	if !metricObserved {
		t.Fatal("metric flush was starved by the blocked trace exporter")
	}
	if shutdownErr != nil {
		t.Fatalf("Shutdown error = %v", shutdownErr)
	}
}

func TestOTLPHTTPProviderRejectsUnsafeConfigurationWithoutLeakingIt(t *testing.T) {
	tests := []struct {
		name      string
		factory   ProviderFactory
		endpoint  string
		sensitive string
	}{
		{name: "userinfo", factory: NewOTLPHTTPProviderFactory("zhixu-api", "dev"), endpoint: "https://user:collector-secret@collector.example.test:4318", sensitive: "collector-secret"},
		{name: "query", factory: NewOTLPHTTPProviderFactory("zhixu-api", "dev"), endpoint: "https://collector.example.test:4318?token=query-secret", sensitive: "query-secret"},
		{name: "fragment", factory: NewOTLPHTTPProviderFactory("zhixu-api", "dev"), endpoint: "https://collector.example.test:4318#fragment-secret", sensitive: "fragment-secret"},
		{name: "resource", factory: NewOTLPHTTPProviderFactory("zhixu-api\ninvalid", "dev"), endpoint: "https://collector.example.test:4318", sensitive: "collector.example.test"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.factory.Open(context.Background(), test.endpoint)
			if !errors.Is(err, ErrInvalidOTLPConfiguration) {
				t.Fatalf("Open error = %v", err)
			}
			if strings.Contains(err.Error(), test.sensitive) {
				t.Fatalf("error leaked configuration: %v", err)
			}
		})
	}
}

func TestSignalEndpointsAppendStandardPathsToBasePrefix(t *testing.T) {
	if !validResourceValue("service.name", "zhixu-worker") {
		t.Fatal("expected service name to be valid")
	}
	if !validResourceValue("service.version", "2026.08.10") {
		t.Fatal("expected service version to be valid")
	}
	metricsEndpoint, tracesEndpoint, err := signalEndpoints("https://collector.example.test/tenant/otlp/")
	if err != nil {
		t.Fatalf("signalEndpoints: %v", err)
	}
	if metricsEndpoint != "https://collector.example.test/tenant/otlp/v1/metrics" {
		t.Fatalf("metrics endpoint = %q", metricsEndpoint)
	}
	if tracesEndpoint != "https://collector.example.test/tenant/otlp/v1/traces" {
		t.Fatalf("traces endpoint = %q", tracesEndpoint)
	}
}

type otlpTestReceiver struct {
	testing *testing.T
	mutex   sync.Mutex
	metrics []*collectormetrics.ExportMetricsServiceRequest
	traces  []*collectortrace.ExportTraceServiceRequest
}

func newOTLPTestReceiver(t *testing.T) *otlpTestReceiver {
	t.Helper()
	return &otlpTestReceiver{testing: t}
}

func (receiver *otlpTestReceiver) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	body, err := readOTLPRequestBody(request)
	if err != nil {
		http.Error(writer, "invalid OTLP body", http.StatusBadRequest)
		return
	}
	if request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/x-protobuf" {
		http.Error(writer, "invalid OTLP request", http.StatusBadRequest)
		return
	}

	receiver.mutex.Lock()
	defer receiver.mutex.Unlock()
	writer.Header().Set("Content-Type", "application/x-protobuf")
	switch request.URL.Path {
	case "/collector-prefix/v1/metrics":
		message := &collectormetrics.ExportMetricsServiceRequest{}
		if err := proto.Unmarshal(body, message); err != nil {
			http.Error(writer, "invalid metrics protobuf", http.StatusBadRequest)
			return
		}
		receiver.metrics = append(receiver.metrics, message)
		response, _ := proto.Marshal(&collectormetrics.ExportMetricsServiceResponse{})
		_, _ = writer.Write(response)
	case "/collector-prefix/v1/traces":
		message := &collectortrace.ExportTraceServiceRequest{}
		if err := proto.Unmarshal(body, message); err != nil {
			http.Error(writer, "invalid traces protobuf", http.StatusBadRequest)
			return
		}
		receiver.traces = append(receiver.traces, message)
		response, _ := proto.Marshal(&collectortrace.ExportTraceServiceResponse{})
		_, _ = writer.Write(response)
	default:
		http.Error(writer, "unknown OTLP path", http.StatusNotFound)
	}
}

func readOTLPRequestBody(request *http.Request) ([]byte, error) {
	reader := io.Reader(request.Body)
	if request.Header.Get("Content-Encoding") == "gzip" {
		compressed, err := gzip.NewReader(request.Body)
		if err != nil {
			return nil, err
		}
		defer compressed.Close()
		reader = compressed
	}
	return io.ReadAll(io.LimitReader(reader, 4<<20))
}

func (receiver *otlpTestReceiver) snapshot(t *testing.T) (*collectormetrics.ExportMetricsServiceRequest, *collectortrace.ExportTraceServiceRequest) {
	t.Helper()
	receiver.mutex.Lock()
	defer receiver.mutex.Unlock()
	if len(receiver.metrics) == 0 || len(receiver.traces) == 0 {
		t.Fatalf("received metrics=%d traces=%d", len(receiver.metrics), len(receiver.traces))
	}
	return receiver.metrics[len(receiver.metrics)-1], receiver.traces[len(receiver.traces)-1]
}

func assertOTLPMetricExport(t *testing.T, request *collectormetrics.ExportMetricsServiceRequest, serviceName, serviceVersion string) {
	t.Helper()
	found := false
	for _, resourceMetrics := range request.GetResourceMetrics() {
		attrs := otlpAttributes(resourceMetrics.GetResource().GetAttributes())
		if attrs["service.name"] != serviceName || attrs["service.version"] != serviceVersion {
			continue
		}
		for _, scope := range resourceMetrics.GetScopeMetrics() {
			for _, exportedMetric := range scope.GetMetrics() {
				if exportedMetric.GetName() == string(MetricQueueDepth) && metricHasQueueValue(exportedMetric, "workflow", 3) {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("project metric or bounded labels were not exported")
	}
}

func assertOTLPProcessPresenceExport(t *testing.T, request *collectormetrics.ExportMetricsServiceRequest, serviceName, serviceVersion string) {
	t.Helper()
	for _, resourceMetrics := range request.GetResourceMetrics() {
		attrs := otlpAttributes(resourceMetrics.GetResource().GetAttributes())
		if attrs["service.name"] != serviceName || attrs["service.version"] != serviceVersion {
			continue
		}
		for _, scope := range resourceMetrics.GetScopeMetrics() {
			for _, exportedMetric := range scope.GetMetrics() {
				if exportedMetric.GetName() != string(MetricProcessPresence) {
					continue
				}
				for _, point := range exportedMetric.GetGauge().GetDataPoints() {
					if len(point.GetAttributes()) == 0 && point.GetAsDouble() == 1 {
						return
					}
				}
			}
		}
	}
	t.Fatal("process presence gauge was not exported with the process resource")
}

func assertOTLPTelemetryRequiredExport(t *testing.T, request *collectormetrics.ExportMetricsServiceRequest, serviceName, serviceVersion string) {
	t.Helper()
	for _, resourceMetrics := range request.GetResourceMetrics() {
		attrs := otlpAttributes(resourceMetrics.GetResource().GetAttributes())
		if attrs["service.name"] != serviceName || attrs["service.version"] != serviceVersion {
			continue
		}
		for _, scope := range resourceMetrics.GetScopeMetrics() {
			for _, exportedMetric := range scope.GetMetrics() {
				if exportedMetric.GetName() != string(MetricTelemetryRequired) {
					continue
				}
				for _, point := range exportedMetric.GetGauge().GetDataPoints() {
					if len(point.GetAttributes()) == 0 && point.GetAsDouble() == 1 {
						return
					}
				}
			}
		}
	}
	t.Fatal("telemetry required gauge was not exported with the process resource")
}

func metricHasQueueValue(metric *metricspb.Metric, queue string, value float64) bool {
	for _, point := range metric.GetGauge().GetDataPoints() {
		if otlpAttributes(point.GetAttributes())["queue"] == queue && point.GetAsDouble() == value {
			return true
		}
	}
	return false
}

func assertOTLPTraceExport(t *testing.T, request *collectortrace.ExportTraceServiceRequest, parent TraceContext) {
	t.Helper()
	for _, resourceSpans := range request.GetResourceSpans() {
		attrs := otlpAttributes(resourceSpans.GetResource().GetAttributes())
		if attrs["service.name"] != "zhixu-worker" || attrs["service.version"] != "2026.08.10" {
			continue
		}
		for _, scope := range resourceSpans.GetScopeSpans() {
			for _, exportedSpan := range scope.GetSpans() {
				if exportedSpan.GetName() != "agent.answer.stream" {
					continue
				}
				if string(exportedSpan.GetTraceId()) != string(mustDecodeHex(t, parent.TraceID)) ||
					string(exportedSpan.GetParentSpanId()) != string(mustDecodeHex(t, parent.SpanID)) {
					t.Fatalf("trace parent was not preserved: %#v", exportedSpan)
				}
				spanAttrs := otlpAttributes(exportedSpan.GetAttributes())
				if spanAttrs["component"] != "eino_chat" || spanAttrs["workspace_id"] != "workspace-1" ||
					spanAttrs["token"] != RedactedValue || spanAttrs["error_code"] != "MODEL_CHAT_REJECTED" {
					t.Fatalf("span attributes = %#v", spanAttrs)
				}
				if exportedSpan.GetStatus().GetCode() != tracepb.Status_STATUS_CODE_ERROR {
					t.Fatalf("span status = %#v", exportedSpan.GetStatus())
				}
				return
			}
		}
	}
	t.Fatal("project span was not exported")
}

func otlpAttributes(values []*commonpb.KeyValue) map[string]string {
	result := make(map[string]string, len(values))
	for _, value := range values {
		result[value.GetKey()] = value.GetValue().GetStringValue()
	}
	return result
}

func mustDecodeHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded := make([]byte, len(value)/2)
	for index := range decoded {
		part := value[index*2 : index*2+2]
		parsed, err := strconv.ParseUint(part, 16, 8)
		if err != nil {
			t.Fatalf("decode hex %q: %v", value, err)
		}
		decoded[index] = byte(parsed)
	}
	return decoded
}
