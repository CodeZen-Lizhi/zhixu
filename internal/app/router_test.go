package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
)

type fakePinger struct {
	err error
}

func (f fakePinger) Ping(context.Context) error { return f.err }

func TestRouterLivezDoesNotNeedDatabase(t *testing.T) {
	router := NewRouter(Dependencies{Version: "test", Database: fakePinger{err: errors.New("down")}})
	req := httptest.NewRequest(http.MethodGet, "/livez", nil)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("livez status = %d", res.Code)
	}
}

func TestRouterReadyzFailsWhenDatabaseUnavailable(t *testing.T) {
	router := NewRouter(Dependencies{Version: "test", Database: fakePinger{err: errors.New("down")}, PingTimeout: time.Second})
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz status = %d", res.Code)
	}
	if got := res.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q", got)
	}
}

func TestRouterReadyzReportsMissingDatabaseConfiguration(t *testing.T) {
	router := NewRouter(Dependencies{Version: "test", DatabaseConfigErr: errors.New("configuration missing")})
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != http.StatusServiceUnavailable || !strings.Contains(res.Body.String(), "database_not_configured") {
		t.Fatalf("unexpected readiness response: %d %s", res.Code, res.Body.String())
	}
}

func TestRouterSystemStatusReturnsDegradedButOK(t *testing.T) {
	router := NewRouter(Dependencies{Version: "v-test", Database: fakePinger{err: errors.New("down")}})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/status", nil)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status code = %d", res.Code)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{`"status":"degraded"`, `"version":"v-test"`, `"status":"unavailable"`, `"request_id"`} {
		if !strings.Contains(string(body), fragment) {
			t.Fatalf("body missing %s: %s", fragment, body)
		}
	}
}

func TestRouterSystemStatusReady(t *testing.T) {
	router := NewRouter(Dependencies{Version: "v-test", Database: fakePinger{}})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/status", nil)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status code = %d", res.Code)
	}
	if !strings.Contains(res.Body.String(), `"status":"ready"`) {
		t.Fatalf("body = %s", res.Body.String())
	}
}

func TestRouterUnknownAPIReturnsProblem(t *testing.T) {
	router := NewRouter(Dependencies{Version: "test"})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/unknown", nil)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != http.StatusNotFound || !strings.Contains(res.Body.String(), `"error_code":"NOT_FOUND"`) {
		t.Fatalf("unexpected response: %d %s", res.Code, res.Body.String())
	}
}

func TestRouterMethodNotAllowedReturnsProblem(t *testing.T) {
	router := NewRouter(Dependencies{Version: "test"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/system/status", nil)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != http.StatusMethodNotAllowed || !strings.Contains(res.Body.String(), `"error_code":"METHOD_NOT_ALLOWED"`) {
		t.Fatalf("unexpected response: %d %s", res.Code, res.Body.String())
	}
}

func TestRouterInvalidDatabaseConfigurationIsNotRetryable(t *testing.T) {
	router := NewRouter(Dependencies{Version: "test", DatabaseConfigErr: errors.New("database_url is invalid")})
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != http.StatusServiceUnavailable || !strings.Contains(res.Body.String(), `"retryable":false`) || !strings.Contains(res.Body.String(), "database_configuration_invalid") {
		t.Fatalf("unexpected response: %d %s", res.Code, res.Body.String())
	}
}

func TestRouterPropagatesIncomingTraceToHandlers(t *testing.T) {
	var output bytes.Buffer
	tracer := observability.NewMemoryTracer()
	router := NewRouter(Dependencies{
		Version: "test",
		Tracer:  tracer,
		Logger:  observability.NewLogger("info", &output),
	})
	req := httptest.NewRequest(http.MethodGet, "/livez", nil)
	req.Header.Set("X-Request-ID", "request-trace-test")
	req.Header.Set("traceparent", "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
	res := httptest.NewRecorder()
	router.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status=%d", res.Code)
	}
	traceParent := res.Header().Get("traceparent")
	if !strings.HasPrefix(traceParent, "00-0123456789abcdef0123456789abcdef-") || strings.Contains(traceParent, "-0123456789abcdef-") {
		t.Fatalf("traceparent=%q", traceParent)
	}
	spans := tracer.Snapshot()
	if len(spans) != 1 || spans[0].TraceContext.TraceID != "0123456789abcdef0123456789abcdef" || spans[0].Attributes["request_id"] == "" {
		t.Fatalf("spans=%+v", spans)
	}
	entry := make(map[string]any)
	if err := json.Unmarshal(output.Bytes(), &entry); err != nil {
		t.Fatalf("decode request log: %v\n%s", err, output.String())
	}
	if entry["trace_id"] != "0123456789abcdef0123456789abcdef" || entry["request_id"] != "request-trace-test" {
		t.Fatalf("request log correlation=%+v", entry)
	}
}
