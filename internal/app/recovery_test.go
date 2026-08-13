package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/CodeZen-Lizhi/zhixu/internal/httpapi"
	"github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	"github.com/gin-gonic/gin"
)

func TestRecoverPanicMiddlewareReturnsStableProblemAndSafeLog(t *testing.T) {
	var output bytes.Buffer
	router := panicTestRouter(&output, func(http.ResponseWriter, *http.Request) {
		panic("do-not-log-this-panic-value")
	})

	request := httptest.NewRequest(http.MethodGet, "/panic/sensitive-path-value", nil)
	request.Header.Set("X-Request-ID", "panic-request-id")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("Content-Type=%q", contentType)
	}
	var problem Problem
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if problem.ErrorCode != "INTERNAL_ERROR" || problem.Message != "服务处理失败" || problem.Retryable {
		t.Fatalf("problem=%+v", problem)
	}

	entries := decodeLogEntries(t, output.Bytes())
	if len(entries) != 2 {
		t.Fatalf("log entries=%d output=%s", len(entries), output.String())
	}
	panicEntry := entries[0]
	if panicEntry["error_code"] != "HTTP_PANIC_RECOVERED" || panicEntry["request_id"] != "panic-request-id" {
		t.Fatalf("panic log=%+v", panicEntry)
	}
	if panicEntry["http_route"] != "/panic/{value}" {
		t.Fatalf("http_route=%v", panicEntry["http_route"])
	}
	stack, ok := panicEntry["panic_stack"].(string)
	if !ok || stack == "" || len(stack) > panicStackByteLimit {
		t.Fatalf("panic_stack=%v", panicEntry["panic_stack"])
	}
	if strings.Contains(output.String(), "do-not-log-this-panic-value") ||
		strings.Contains(output.String(), "sensitive-path-value") ||
		strings.Contains(output.String(), "/Users/") {
		t.Fatalf("panic log leaked sensitive data: %s", output.String())
	}
	if entries[1]["status"] != float64(http.StatusInternalServerError) {
		t.Fatalf("completion log=%+v", entries[1])
	}
}

func TestNewRouterInstallsPanicRecoveryBoundary(t *testing.T) {
	var output bytes.Buffer
	router := NewRouter(Dependencies{
		Logger: observability.NewLogger("info", &output),
		MetricsHandler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			panic("metrics-handler-panic")
		}),
	})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(output.String(), `"error_code":"HTTP_PANIC_RECOVERED"`) || strings.Contains(output.String(), "metrics-handler-panic") {
		t.Fatalf("recovery log=%s", output.String())
	}
}

func TestRecoverPanicMiddlewareDoesNotReplaceStartedResponse(t *testing.T) {
	var output bytes.Buffer
	router := panicTestRouter(&output, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
		panic("after-response")
	})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/panic/value", nil))

	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
	entries := decodeLogEntries(t, output.Bytes())
	if len(entries) != 2 || entries[1]["status"] != float64(http.StatusNoContent) {
		t.Fatalf("logs=%+v", entries)
	}
}

func TestRecoverPanicMiddlewarePreservesWrittenOrFlushedResponse(t *testing.T) {
	tests := []struct {
		name  string
		write func(http.ResponseWriter)
		body  string
	}{
		{
			name: "body write",
			write: func(writer http.ResponseWriter) {
				_, _ = writer.Write([]byte("partial response"))
			},
			body: "partial response",
		},
		{
			name: "flush",
			write: func(writer http.ResponseWriter) {
				writer.(http.Flusher).Flush()
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			router := panicTestRouter(&output, func(writer http.ResponseWriter, _ *http.Request) {
				test.write(writer)
				panic("after-response-start")
			})

			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/panic/value", nil))
			if response.Code != http.StatusOK || response.Body.String() != test.body || strings.Contains(response.Body.String(), "INTERNAL_ERROR") {
				t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
			}
		})
	}
}

func TestRecoverPanicMiddlewarePreservesStartedStaticFallback(t *testing.T) {
	var output bytes.Buffer
	router := NewRouter(Dependencies{
		Logger: observability.NewLogger("info", &output),
		Static: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte("partial static response"))
			panic("after-static-response-start")
		}),
	})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/static/fallback", nil))
	if response.Code != http.StatusOK || response.Body.String() != "partial static response" || strings.Contains(response.Body.String(), "INTERNAL_ERROR") {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestRecoverPanicMiddlewareRepanicsAbortHandler(t *testing.T) {
	router := gin.New()
	router.Use(recoverPanicMiddleware(observability.NewLogger("info", io.Discard)))
	router.GET("/", httpapi.GinHandler(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	defer func() {
		recovered := recover()
		recoveredErr, ok := recovered.(error)
		if !ok || !errors.Is(recoveredErr, http.ErrAbortHandler) {
			t.Fatalf("recovered=%v", recovered)
		}
	}()
	router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}

func TestRecoverPanicMiddlewareHandlesWrappedAbortAsOrdinaryPanic(t *testing.T) {
	var output bytes.Buffer
	router := panicTestRouter(&output, func(http.ResponseWriter, *http.Request) {
		panic(fmt.Errorf("do-not-log-wrapped-abort: %w", http.ErrAbortHandler))
	})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/panic/value", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(output.String(), "do-not-log-wrapped-abort") {
		t.Fatalf("wrapped panic leaked: %s", output.String())
	}
}

func TestRecoverPanicMiddlewareHandlesUncomparablePanicValue(t *testing.T) {
	var output bytes.Buffer
	router := panicTestRouter(&output, func(http.ResponseWriter, *http.Request) {
		panic([]byte("do-not-compare-this-value"))
	})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/panic/value", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(output.String(), "do-not-compare-this-value") {
		t.Fatalf("uncomparable panic value leaked: %s", output.String())
	}
}

type nilPanicError struct{}

func (*nilPanicError) Error() string { return "typed nil panic" }

func TestRecoverPanicMiddlewareHandlesTypedNilErrorPanic(t *testing.T) {
	var typedNil *nilPanicError
	router := panicTestRouter(io.Discard, func(http.ResponseWriter, *http.Request) {
		panic(typedNil)
	})

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/panic/value", nil))

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}

func panicTestRouter(output io.Writer, handler http.HandlerFunc) http.Handler {
	router := gin.New()
	logger := observability.NewLogger("info", output)
	router.Use(requestIDMiddleware)
	router.Use(requestLogMiddleware(logger))
	router.Use(recoverPanicMiddleware(logger))
	router.GET("/panic/:value", httpapi.GinHandler(handler))
	return router
}

func decodeLogEntries(t *testing.T, raw []byte) []map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var entries []map[string]any
	for {
		entry := make(map[string]any)
		if err := decoder.Decode(&entry); err != nil {
			if errors.Is(err, io.EOF) {
				return entries
			}
			t.Fatalf("decode log: %v\n%s", err, raw)
		}
		entries = append(entries, entry)
	}
}
