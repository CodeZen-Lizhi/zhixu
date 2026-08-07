package models_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	agentdomain "github.com/CodeZen-Lizhi/zhixu/internal/agent/domain"
	"github.com/CodeZen-Lizhi/zhixu/internal/foundation"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/models"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
)

func TestEinoCallbackTelemetryPreservesCorrelationWithoutSensitivePayloads(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, validChatResponse("chat-v1", `{"result":"response-body-secret-canary"}`))
	}))
	defer server.Close()

	tracer := observability.NewMemoryTracer()
	metrics := observability.NewMemoryMetrics()
	options := chatOptions(server.URL+"/private-endpoint-canary", server.Client())
	model, err := models.NewEinoOpenAIChatModel(options, models.NewModelTelemetry(tracer, metrics))
	if err != nil {
		t.Fatal(err)
	}
	ctx := observability.WithCorrelation(context.Background(), observability.Correlation{
		RequestID: "request-correlation", WorkspaceID: "workspace-correlation", WorkflowRunID: "workflow-correlation",
		NodeRunID: "node-correlation", AttemptNo: 2, DispatchNo: 3, RetryNo: 1,
	})
	ctx, err = observability.WithTraceContext(ctx, observability.TraceContext{
		TraceID: "0123456789abcdef0123456789abcdef", SpanID: "0123456789abcdef", TraceFlags: "01",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := validChatRequest(model.Contract().Model)
	request.Phase = agentdomain.ModelCallReview
	request.Messages[0].Content = "Authorization: Bearer auth-secret-canary Cookie=session-secret-canary"
	request.Messages[1].Content = "prompt-request-body-secret-canary"
	response, err := model.Chat(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if string(response.Content) != `{"result":"response-body-secret-canary"}` {
		t.Fatalf("response=%s", response.Content)
	}

	spans := tracer.Snapshot()
	if len(spans) != 1 {
		t.Fatalf("spans=%#v", spans)
	}
	span := spans[0]
	if span.Operation != "model.chat.generate" || span.TraceContext.TraceID != "0123456789abcdef0123456789abcdef" ||
		span.ParentSpanID != "0123456789abcdef" || span.ErrorCode != "" {
		t.Fatalf("span=%#v", span)
	}
	wantAttributes := map[string]string{
		"component": "eino_chat", "phase": "REVIEW", "status": "success",
		"request_id": "request-correlation", "workspace_id": "workspace-correlation",
		"workflow_run_id": "workflow-correlation", "node_run_id": "node-correlation",
		"attempt_no": "2", "dispatch_no": "3", "retry_no": "1",
	}
	for key, want := range wantAttributes {
		if got := span.Attributes[key]; got != want {
			t.Fatalf("span attribute %s=%q want=%q; span=%#v", key, got, want, span)
		}
	}
	assertModelCallbackMeasurements(t, metrics.Snapshot(), "REVIEW", "success", "")
	assertTelemetryOmits(t, spans, metrics.Snapshot(),
		options.APIKey, options.BaseURL, server.URL, "private-endpoint-canary", "Authorization", "auth-secret-canary",
		"Cookie", "session-secret-canary", "prompt-request-body-secret-canary", "response-body-secret-canary",
	)
}

func TestEinoCallbackTelemetryUsesStableFailureAndCancellationCodes(t *testing.T) {
	t.Parallel()
	t.Run("provider failure", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(writer, `{"error":"raw-sdk-error-secret-canary"}`)
		}))
		defer server.Close()
		tracer := observability.NewMemoryTracer()
		metrics := observability.NewMemoryMetrics()
		model, err := models.NewEinoOpenAIChatModel(chatOptions(server.URL, server.Client()), models.NewModelTelemetry(tracer, metrics))
		if err != nil {
			t.Fatal(err)
		}
		_, err = model.Chat(context.Background(), validChatRequest(model.Contract().Model))
		assertChatError(t, err, foundation.ErrorRetryableFailure, models.ErrorCodeChatRateLimited, true)
		spans := tracer.Snapshot()
		if len(spans) != 1 || spans[0].ErrorCode != models.ErrorCodeChatRateLimited || spans[0].Attributes["status"] != "failure" {
			t.Fatalf("spans=%#v", spans)
		}
		assertModelCallbackMeasurements(t, metrics.Snapshot(), "INITIAL", "failure", models.ErrorCodeChatRateLimited)
		assertTelemetryOmits(t, spans, metrics.Snapshot(), "raw-sdk-error-secret-canary")
	})

	t.Run("caller cancellation", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		defer server.Close()
		tracer := observability.NewMemoryTracer()
		metrics := observability.NewMemoryMetrics()
		model, err := models.NewEinoOpenAIChatModel(chatOptions(server.URL, server.Client()), models.NewModelTelemetry(tracer, metrics))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err = model.Chat(ctx, validChatRequest(model.Contract().Model))
		assertChatError(t, err, foundation.ErrorNonRetryableFailure, models.ErrorCodeChatCancelled, false)
		spans := tracer.Snapshot()
		if len(spans) != 1 || spans[0].ErrorCode != models.ErrorCodeChatCancelled || spans[0].Attributes["status"] != "cancelled" {
			t.Fatalf("spans=%#v", spans)
		}
		assertModelCallbackMeasurements(t, metrics.Snapshot(), "INITIAL", "cancelled", models.ErrorCodeChatCancelled)
	})
}

func TestEinoCallbackTelemetryIsRequestScopedUnderConcurrency(t *testing.T) {
	t.Parallel()
	serverErrors := make(chan error, 32)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			serverErrors <- err
			return
		}
		marker, err := markerFromChatRequest(body)
		if err != nil {
			serverErrors <- err
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, validChatResponse("chat-v1", fmt.Sprintf(`{"marker":%q}`, marker)))
	}))
	defer server.Close()

	tracer := observability.NewMemoryTracer()
	metrics := observability.NewMemoryMetrics()
	model, err := models.NewEinoOpenAIChatModel(chatOptions(server.URL, server.Client()), models.NewModelTelemetry(tracer, metrics))
	if err != nil {
		t.Fatal(err)
	}
	phases := []agentdomain.ModelCallPhase{agentdomain.ModelCallInitial, agentdomain.ModelCallRepair, agentdomain.ModelCallReduced}
	wantPhase := make(map[string]string, 32)
	var wait sync.WaitGroup
	callErrors := make(chan error, 32)
	for index := range 32 {
		marker := fmt.Sprintf("callback-%02d", index)
		phase := phases[index%len(phases)]
		wantPhase[marker] = string(phase)
		wait.Add(1)
		go func() {
			defer wait.Done()
			ctx := observability.WithCorrelation(context.Background(), observability.Correlation{RequestID: marker})
			request := validChatRequest(model.Contract().Model)
			request.Phase = phase
			request.Messages[1].Content = marker
			response, err := model.Chat(ctx, request)
			if err != nil {
				callErrors <- err
				return
			}
			if string(response.Content) != fmt.Sprintf(`{"marker":%q}`, marker) {
				callErrors <- fmt.Errorf("marker=%s response=%s", marker, response.Content)
			}
		}()
	}
	wait.Wait()
	close(callErrors)
	close(serverErrors)
	for err := range callErrors {
		t.Error(err)
	}
	for err := range serverErrors {
		t.Error(err)
	}

	spans := tracer.Snapshot()
	if len(spans) != 32 {
		t.Fatalf("spans=%d", len(spans))
	}
	seen := make(map[string]struct{}, 32)
	for _, span := range spans {
		requestID := span.Attributes["request_id"]
		if _, duplicate := seen[requestID]; duplicate {
			t.Fatalf("duplicate request correlation %q", requestID)
		}
		seen[requestID] = struct{}{}
		if span.Attributes["phase"] != wantPhase[requestID] || span.Attributes["status"] != "success" {
			t.Fatalf("request=%q span=%#v", requestID, span)
		}
	}
	measurements := metrics.Snapshot()
	if len(measurements) != 64 {
		t.Fatalf("measurements=%d", len(measurements))
	}
	for _, measurement := range measurements {
		if _, found := measurement.Labels.Map()["request_id"]; found {
			t.Fatalf("correlation leaked into metric labels: %#v", measurement)
		}
	}
}

func TestEinoCallbackTelemetryFailureDoesNotChangeChatResult(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(writer, validChatResponse("chat-v1", `{"result":"ok"}`))
	}))
	defer server.Close()
	provider := observability.NewMemoryProvider()
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	model, err := models.NewEinoOpenAIChatModel(
		chatOptions(server.URL, server.Client()),
		models.NewModelTelemetry(provider.Tracer(), provider.Metrics()),
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err := model.Chat(context.Background(), validChatRequest(model.Contract().Model))
	if err != nil || string(response.Content) != `{"result":"ok"}` {
		t.Fatalf("response=%#v err=%v", response, err)
	}

	panickingSpan := &panickingModelSpan{}
	panickingModel, err := models.NewEinoOpenAIChatModel(
		chatOptions(server.URL, server.Client()),
		models.NewModelTelemetry(panickingModelTracer{span: panickingSpan}, panickingModelMetrics{}),
	)
	if err != nil {
		t.Fatal(err)
	}
	response, err = panickingModel.Chat(context.Background(), validChatRequest(panickingModel.Contract().Model))
	if err != nil || string(response.Content) != `{"result":"ok"}` {
		t.Fatalf("panicking telemetry changed response=%#v err=%v", response, err)
	}
	if !panickingSpan.endAttempted.Load() {
		t.Fatal("span End was not attempted after SetAttributes panic")
	}
}

type panickingModelTracer struct {
	span *panickingModelSpan
}

func (tracer panickingModelTracer) Start(ctx context.Context, _ string, _ ...observability.TraceAttribute) (context.Context, observability.Span, error) {
	return ctx, tracer.span, nil
}

type panickingModelSpan struct {
	endAttempted atomic.Bool
}

func (*panickingModelSpan) SetAttributes(...observability.TraceAttribute) {
	panic("telemetry span failed")
}
func (*panickingModelSpan) RecordError(string) { panic("telemetry span failed") }
func (span *panickingModelSpan) End() {
	span.endAttempted.Store(true)
	panic("telemetry span failed")
}

type panickingModelMetrics struct{}

func (panickingModelMetrics) Record(context.Context, observability.Measurement) error {
	panic("telemetry metrics failed")
}

func assertModelCallbackMeasurements(t *testing.T, measurements []observability.Measurement, phase, result, errorCode string) {
	t.Helper()
	if len(measurements) != 2 {
		t.Fatalf("measurements=%#v", measurements)
	}
	seen := make(map[observability.MetricName]bool, 2)
	for _, measurement := range measurements {
		labels := measurement.Labels.Map()
		if labels["component"] != "eino_chat" || labels["phase"] != phase || labels["result"] != result || labels["error_code"] != errorCode {
			t.Fatalf("measurement=%#v labels=%#v", measurement, labels)
		}
		if measurement.Name == observability.MetricModelCallTotal && measurement.Value != 1 {
			t.Fatalf("total measurement=%#v", measurement)
		}
		if measurement.Name == observability.MetricModelCallDuration && measurement.Value < 0 {
			t.Fatalf("duration measurement=%#v", measurement)
		}
		seen[measurement.Name] = true
	}
	if !seen[observability.MetricModelCallDuration] || !seen[observability.MetricModelCallTotal] {
		t.Fatalf("metric names=%#v", seen)
	}
}

func assertTelemetryOmits(t *testing.T, spans []observability.SpanSnapshot, measurements []observability.Measurement, forbidden ...string) {
	t.Helper()
	formatted := fmt.Sprintf("%#v %#v", spans, measurements)
	for _, value := range forbidden {
		if value != "" && strings.Contains(formatted, value) {
			t.Fatalf("telemetry leaked %q: %s", value, formatted)
		}
	}
}

func markerFromChatRequest(body []byte) (string, error) {
	var request chatWireRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return "", err
	}
	if len(request.Messages) != 2 || !strings.HasPrefix(request.Messages[1].Content, "callback-") {
		return "", fmt.Errorf("request marker is missing")
	}
	return request.Messages[1].Content, nil
}
