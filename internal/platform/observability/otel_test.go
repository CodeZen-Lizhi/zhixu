package observability

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	collecttracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

func TestOTLPExporterUsesProjectConfigurationAndExactResource(t *testing.T) {
	certificatePath, keyPath := writeTestClientCertificate(t)
	for key, value := range map[string]string{
		"OTEL_EXPORTER_OTLP_ENDPOINT":                  "http://127.0.0.1:1/environment-base",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT":           "http://127.0.0.1:1/environment-traces",
		"OTEL_EXPORTER_OTLP_HEADERS":                   "authorization=environment-secret",
		"OTEL_EXPORTER_OTLP_TRACES_HEADERS":            "x-environment-secret=present",
		"OTEL_EXPORTER_OTLP_COMPRESSION":               "gzip",
		"OTEL_EXPORTER_OTLP_TRACES_COMPRESSION":        "gzip",
		"OTEL_EXPORTER_OTLP_TIMEOUT":                   "1",
		"OTEL_EXPORTER_OTLP_TRACES_TIMEOUT":            "1",
		"OTEL_EXPORTER_OTLP_INSECURE":                  "false",
		"OTEL_EXPORTER_OTLP_TRACES_INSECURE":           "false",
		"OTEL_EXPORTER_OTLP_CERTIFICATE":               certificatePath,
		"OTEL_EXPORTER_OTLP_TRACES_CERTIFICATE":        certificatePath,
		"OTEL_EXPORTER_OTLP_CLIENT_CERTIFICATE":        certificatePath,
		"OTEL_EXPORTER_OTLP_CLIENT_KEY":                keyPath,
		"OTEL_EXPORTER_OTLP_TRACES_CLIENT_CERTIFICATE": certificatePath,
		"OTEL_EXPORTER_OTLP_TRACES_CLIENT_KEY":         keyPath,
		"OTEL_TRACES_SAMPLER":                          "always_off",
		"OTEL_SPAN_ATTRIBUTE_COUNT_LIMIT":              "0",
		"OTEL_BSP_MAX_QUEUE_SIZE":                      "1",
		"OTEL_BSP_MAX_EXPORT_BATCH_SIZE":               "1",
		"OTEL_BSP_SCHEDULE_DELAY":                      "60000",
		"OTEL_BSP_EXPORT_TIMEOUT":                      "1",
		"OTEL_SERVICE_NAME":                            "environment-service",
		"OTEL_RESOURCE_ATTRIBUTES":                     "environment.secret=must-not-export,deployment.environment=poisoned",
	} {
		t.Setenv(key, value)
	}

	var capture otlpRequestCapture
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		capture.record(t, request)
		response.Header().Set("Content-Type", "application/x-protobuf")
		response.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	startupParent, err := WithTraceContext(context.Background(), TraceContext{
		TraceID: "0123456789abcdef0123456789abcdef", SpanID: "0123456789abcdef", TraceFlags: "01",
	})
	if err != nil {
		t.Fatal(err)
	}
	telemetry, err := InitializeTelemetry(startupParent, TelemetryOptions{
		Mode:           TelemetryModeRequired,
		Endpoint:       server.URL + "/collector/base/",
		ServiceName:    "zhixu-api",
		ServiceVersion: "v-test",
		Environment:    "integration",
	})
	if err != nil {
		t.Fatalf("InitializeTelemetry: %v", err)
	}

	ctx, span, err := telemetry.Tracer().Start(context.Background(), "http.request",
		TraceAttribute{Key: "http.method", Value: "GET"},
		TraceAttribute{Key: "credential", Value: "credential-secret"},
	)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if _, found := TraceContextFromContext(ctx); !found {
		t.Fatal("started span did not publish a valid trace context")
	}
	span.RecordError("E_REQUEST_FAILED")
	span.End()
	_, rawErrorSpan, err := telemetry.Tracer().Start(context.Background(), "raw.error")
	if err != nil {
		t.Fatalf("Start raw error: %v", err)
	}
	rawErrorSpan.RecordError("connection refused")
	rawErrorSpan.End()
	if err := telemetry.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	requests := capture.snapshot()
	if len(requests) < 2 {
		t.Fatalf("OTLP request count = %d, want startup and shutdown flush requests", len(requests))
	}
	var spans []*tracepb.Span
	for _, request := range requests {
		if request.path != "/collector/base/v1/traces" {
			t.Fatalf("OTLP path = %q, want base path plus /v1/traces", request.path)
		}
		if request.authorization != "" || request.environmentHeader != "" {
			t.Fatalf("environment OTLP headers leaked: authorization=%q x-environment-secret=%q", request.authorization, request.environmentHeader)
		}
		if request.contentEncoding != "" {
			t.Fatalf("content encoding = %q, want explicit no compression", request.contentEncoding)
		}
		for _, resourceSpans := range request.payload.ResourceSpans {
			assertExactOTLPResource(t, resourceSpans.Resource.Attributes)
			for _, scopeSpans := range resourceSpans.ScopeSpans {
				spans = append(spans, scopeSpans.Spans...)
			}
		}
	}

	byName := make(map[string]*tracepb.Span, len(spans))
	for _, recorded := range spans {
		byName[recorded.Name] = recorded
	}
	startupSpan := byName["telemetry.startup"]
	if startupSpan == nil {
		t.Fatalf("exported spans = %v, want telemetry.startup", sortedSpanNames(spans))
	}
	if len(startupSpan.ParentSpanId) != 0 {
		t.Fatalf("telemetry.startup parent span id = %x, want fixed root span", startupSpan.ParentSpanId)
	}
	requestSpan := byName["http.request"]
	if requestSpan == nil {
		t.Fatalf("exported spans = %v, want http.request", sortedSpanNames(spans))
	}
	attributes := otlpAttributes(requestSpan.Attributes)
	if attributes["http.method"] != "GET" || attributes["credential"] != RedactedValue {
		t.Fatalf("request span attributes = %#v", attributes)
	}
	if attributes["error.code"] != "E_REQUEST_FAILED" || requestSpan.Status.GetCode() != tracepb.Status_STATUS_CODE_ERROR {
		t.Fatalf("request span error attributes=%#v status=%v", attributes, requestSpan.Status.GetCode())
	}
	rawSpan := byName["raw.error"]
	if rawSpan == nil {
		t.Fatalf("exported spans = %v, want raw.error", sortedSpanNames(spans))
	}
	rawAttributes := otlpAttributes(rawSpan.Attributes)
	if rawAttributes["error.code"] != RedactedValue || rawSpan.Status.GetMessage() != RedactedValue {
		t.Fatalf("raw error escaped stable-code boundary: attributes=%#v status=%#v", rawAttributes, rawSpan.Status)
	}
}

func TestOTLPCollectorPreservesCrossProcessParentChildTrace(t *testing.T) {
	var capture otlpRequestCapture
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		capture.record(t, request)
		response.Header().Set("Content-Type", "application/x-protobuf")
		response.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	apiTelemetry, err := InitializeTelemetry(context.Background(), TelemetryOptions{
		Mode: TelemetryModeRequired, Endpoint: server.URL,
		ServiceName: "zhixu-api", ServiceVersion: "v-test", Environment: "integration",
	})
	if err != nil {
		t.Fatalf("Initialize API telemetry: %v", err)
	}
	t.Cleanup(func() { _ = apiTelemetry.Shutdown(context.Background()) })
	workerTelemetry, err := InitializeTelemetry(context.Background(), TelemetryOptions{
		Mode: TelemetryModeRequired, Endpoint: server.URL,
		ServiceName: "zhixu-worker", ServiceVersion: "v-test", Environment: "integration",
	})
	if err != nil {
		t.Fatalf("Initialize worker telemetry: %v", err)
	}
	t.Cleanup(func() { _ = workerTelemetry.Shutdown(context.Background()) })

	apiContext, apiSpan, err := apiTelemetry.Tracer().Start(context.Background(), "http.request")
	if err != nil {
		t.Fatalf("Start API span: %v", err)
	}
	metadata := EncodeTraceMetadata(apiContext)
	apiSpan.End()
	workerParent, err := DecodeTraceMetadata(context.Background(), metadata)
	if err != nil {
		t.Fatalf("Decode API trace metadata: %v", err)
	}
	_, consumerSpan, err := workerTelemetry.Tracer().Start(workerParent, "workflow.node.consume")
	if err != nil {
		t.Fatalf("Start consumer span: %v", err)
	}
	consumerSpan.End()

	if err := apiTelemetry.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown API telemetry: %v", err)
	}
	if err := workerTelemetry.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown worker telemetry: %v", err)
	}

	var exportedAPI, exportedConsumer *tracepb.Span
	for _, request := range capture.snapshot() {
		for _, resourceSpans := range request.payload.ResourceSpans {
			resourceAttributes := otlpAttributes(resourceSpans.Resource.Attributes)
			serviceName := resourceAttributes["service.name"]
			if resourceAttributes["service.version"] != "v-test" || resourceAttributes["deployment.environment"] != "integration" {
				t.Fatalf("resource attributes=%#v", resourceAttributes)
			}
			for _, scopeSpans := range resourceSpans.ScopeSpans {
				for _, span := range scopeSpans.Spans {
					switch span.Name {
					case "http.request":
						if serviceName != "zhixu-api" {
							t.Fatalf("API span service.name=%q", serviceName)
						}
						exportedAPI = span
					case "workflow.node.consume":
						if serviceName != "zhixu-worker" {
							t.Fatalf("consumer span service.name=%q", serviceName)
						}
						exportedConsumer = span
					}
				}
			}
		}
	}
	if exportedAPI == nil || exportedConsumer == nil {
		t.Fatalf("missing cross-process spans: api=%v consumer=%v", exportedAPI != nil, exportedConsumer != nil)
	}
	if !bytes.Equal(exportedAPI.TraceId, exportedConsumer.TraceId) {
		t.Fatalf("trace ids differ: api=%x consumer=%x", exportedAPI.TraceId, exportedConsumer.TraceId)
	}
	if !bytes.Equal(exportedConsumer.ParentSpanId, exportedAPI.SpanId) {
		t.Fatalf("consumer parent=%x want API span=%x", exportedConsumer.ParentSpanId, exportedAPI.SpanId)
	}
}

func writeTestClientCertificate(t *testing.T) (string, string) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "environment-only-certificate"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	certificatePath := filepath.Join(directory, "environment-cert.pem")
	keyPath := filepath.Join(directory, "environment-key.pem")
	if err := os.WriteFile(certificatePath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certificatePath, keyPath
}

type capturedOTLPRequest struct {
	path              string
	authorization     string
	environmentHeader string
	contentEncoding   string
	payload           *collecttracepb.ExportTraceServiceRequest
}

type otlpRequestCapture struct {
	mutex    sync.Mutex
	requests []capturedOTLPRequest
}

func (capture *otlpRequestCapture) record(t *testing.T, request *http.Request) {
	t.Helper()
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Errorf("read OTLP body: %v", err)
		return
	}
	payload := new(collecttracepb.ExportTraceServiceRequest)
	if err := proto.Unmarshal(body, payload); err != nil {
		t.Errorf("unmarshal OTLP payload: %v", err)
		return
	}
	capture.mutex.Lock()
	defer capture.mutex.Unlock()
	capture.requests = append(capture.requests, capturedOTLPRequest{
		path:              request.URL.Path,
		authorization:     request.Header.Get("Authorization"),
		environmentHeader: request.Header.Get("X-Environment-Secret"),
		contentEncoding:   request.Header.Get("Content-Encoding"),
		payload:           payload,
	})
}

func (capture *otlpRequestCapture) snapshot() []capturedOTLPRequest {
	capture.mutex.Lock()
	defer capture.mutex.Unlock()
	return append([]capturedOTLPRequest(nil), capture.requests...)
}

func assertExactOTLPResource(t *testing.T, attributes []*commonpb.KeyValue) {
	t.Helper()
	got := otlpAttributes(attributes)
	want := map[string]string{
		"deployment.environment": "integration",
		"service.name":           "zhixu-api",
		"service.version":        "v-test",
	}
	if len(got) != len(want) {
		t.Fatalf("OTLP resource = %#v, want exact project resource", got)
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("OTLP resource[%q] = %q, want %q (resource=%#v)", key, got[key], value, got)
		}
	}
}

func otlpAttributes(attributes []*commonpb.KeyValue) map[string]string {
	result := make(map[string]string, len(attributes))
	for _, attribute := range attributes {
		result[attribute.Key] = attribute.Value.GetStringValue()
	}
	return result
}

func sortedSpanNames(spans []*tracepb.Span) []string {
	names := make([]string, 0, len(spans))
	for _, span := range spans {
		names = append(names, span.Name)
	}
	sort.Strings(names)
	return names
}

func TestRequiredOTLPProbeUsesBoundedRetryWithoutLeakingCollectorResponse(t *testing.T) {
	var mutex sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		mutex.Lock()
		requests++
		mutex.Unlock()
		http.Error(response, "collector-secret-response", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	startedAt := time.Now()
	_, err := InitializeTelemetry(context.Background(), TelemetryOptions{
		Mode:           TelemetryModeRequired,
		Endpoint:       server.URL,
		ServiceName:    "zhixu-worker",
		ServiceVersion: "v-test",
		Environment:    "integration",
	})
	elapsed := time.Since(startedAt)
	if !errors.Is(err, ErrTelemetryExporterRequired) {
		t.Fatalf("InitializeTelemetry error = %v, want stable exporter error", err)
	}
	if strings.Contains(err.Error(), "collector-secret-response") || strings.Contains(err.Error(), server.URL) {
		t.Fatalf("InitializeTelemetry leaked collector details: %v", err)
	}
	mutex.Lock()
	defer mutex.Unlock()
	if requests < 2 || requests > 10 {
		t.Fatalf("startup probe requests = %d, want bounded retries", requests)
	}
	if elapsed > time.Second {
		t.Fatalf("startup probe elapsed = %s, want bounded retry under one second", elapsed)
	}
}

func TestRequiredOTLPProbeHonorsCallerCancellation(t *testing.T) {
	requestStarted := make(chan struct{})
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if requests.Add(1) == 1 {
			close(requestStarted)
			select {
			case <-request.Context().Done():
			case <-time.After(1500 * time.Millisecond):
			}
			return
		}
		response.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-requestStarted:
		case <-time.After(5 * time.Second):
		}
		cancel()
	}()
	startedAt := time.Now()
	telemetry, err := InitializeTelemetry(ctx, TelemetryOptions{
		Mode: TelemetryModeRequired, Endpoint: server.URL,
		ServiceName: "zhixu-api", ServiceVersion: "v-test", Environment: "integration",
	})
	elapsed := time.Since(startedAt)
	if telemetry != nil {
		_ = telemetry.Shutdown(context.Background())
	}
	if !errors.Is(err, ErrTelemetryExporterRequired) {
		t.Fatalf("InitializeTelemetry error=%v, want cancellation to fail the required probe", err)
	}
	if elapsed > time.Second {
		t.Fatalf("canceled startup probe elapsed=%s, want caller cancellation to stop the probe", elapsed)
	}
}

func TestOTLPExporterRefusesRedirectedTracePayload(t *testing.T) {
	var redirectedRequests atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectedRequests.Add(1)
	}))
	t.Cleanup(target.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, target.URL+"/stolen", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)

	_, err := InitializeTelemetry(context.Background(), TelemetryOptions{
		Mode: TelemetryModeRequired, Endpoint: redirector.URL,
		ServiceName: "zhixu-api", ServiceVersion: "v-test", Environment: "integration",
	})
	if !errors.Is(err, ErrTelemetryExporterRequired) {
		t.Fatalf("InitializeTelemetry error=%v", err)
	}
	if redirectedRequests.Load() != 0 {
		t.Fatalf("redirect target received %d trace payloads", redirectedRequests.Load())
	}
}

func TestOTLPExporterProbeTimeoutIsBounded(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_TIMEOUT", "60000")
	t.Setenv("OTEL_EXPORTER_OTLP_TRACES_TIMEOUT", "60000")
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		time.Sleep(3 * time.Second)
	}))
	t.Cleanup(server.Close)

	startedAt := time.Now()
	_, err := InitializeTelemetry(context.Background(), TelemetryOptions{
		Mode: TelemetryModeRequired, Endpoint: server.URL,
		ServiceName: "zhixu-api", ServiceVersion: "v-test", Environment: "integration",
	})
	elapsed := time.Since(startedAt)
	if !errors.Is(err, ErrTelemetryExporterRequired) {
		t.Fatalf("InitializeTelemetry error=%v", err)
	}
	if elapsed > 4*time.Second {
		t.Fatalf("startup timeout elapsed=%s, want bounded by project timeout", elapsed)
	}
}
