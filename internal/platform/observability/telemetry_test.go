package observability

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestInitializeTelemetryModesUseRealExporterProbe(t *testing.T) {
	t.Run("disabled keeps local context and never dials endpoint", func(t *testing.T) {
		var requests atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
		t.Cleanup(server.Close)
		t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", server.URL)

		telemetry, err := InitializeTelemetry(context.Background(), TelemetryOptions{Mode: TelemetryModeDisabled})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = telemetry.Shutdown(context.Background()) }()
		if telemetry.Status() != (TelemetryStatus{Mode: TelemetryModeDisabled, Code: TelemetryStatusDisabled}) {
			t.Fatalf("status=%#v", telemetry.Status())
		}
		ctx, span, err := telemetry.Tracer().Start(context.Background(), "local.root")
		if err != nil {
			t.Fatal(err)
		}
		span.End()
		if trace, found := TraceContextFromContext(ctx); !found || trace.TraceID == "" {
			t.Fatalf("disabled context=%+v found=%t", trace, found)
		}
		if requests.Load() != 0 {
			t.Fatalf("disabled exporter dialed %d times", requests.Load())
		}
		if telemetry.MetricsHandler() == nil {
			t.Fatal("disabled telemetry has no metrics handler")
		}
	})

	t.Run("optional probe failure degrades but preserves context", func(t *testing.T) {
		var requests atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			http.Error(response, "collector unavailable", http.StatusServiceUnavailable)
		}))
		t.Cleanup(server.Close)
		telemetry, err := InitializeTelemetry(context.Background(), TelemetryOptions{
			Mode: TelemetryModeOptional, Endpoint: server.URL,
			ServiceName: "zhixu-api", ServiceVersion: "v-test", Environment: "integration",
		})
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = telemetry.Shutdown(context.Background()) }()
		want := TelemetryStatus{Mode: TelemetryModeOptional, Degraded: true, Code: TelemetryStatusExporterUnavailable}
		if telemetry.Status() != want {
			t.Fatalf("status=%#v want=%#v", telemetry.Status(), want)
		}
		ctx, span, err := telemetry.Tracer().Start(context.Background(), "degraded.root")
		if err != nil {
			t.Fatal(err)
		}
		span.End()
		if _, found := TraceContextFromContext(ctx); !found {
			t.Fatal("optional fallback did not preserve context")
		}
		if requests.Load() < 2 {
			t.Fatalf("optional probe did not attempt bounded retry: %d", requests.Load())
		}
	})

	t.Run("optional probe success exports", func(t *testing.T) {
		var requests atomic.Int64
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			requests.Add(1)
			response.WriteHeader(http.StatusOK)
		}))
		t.Cleanup(server.Close)
		telemetry, err := InitializeTelemetry(context.Background(), TelemetryOptions{
			Mode: TelemetryModeOptional, Endpoint: server.URL,
			ServiceName: "zhixu-api", ServiceVersion: "v-test", Environment: "integration",
		})
		if err != nil {
			t.Fatal(err)
		}
		if telemetry.Status() != (TelemetryStatus{Mode: TelemetryModeOptional, Exporting: true, Code: TelemetryStatusExporting}) {
			t.Fatalf("status=%#v", telemetry.Status())
		}
		ctx, span, err := telemetry.Tracer().Start(context.Background(), "optional.root")
		if err != nil {
			t.Fatal(err)
		}
		span.End()
		if err := telemetry.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
		if requests.Load() < 2 {
			t.Fatalf("startup/business export requests=%d", requests.Load())
		}
		if _, found := TraceContextFromContext(ctx); !found {
			t.Fatal("optional success lost context")
		}
	})
}

func TestInitializeTelemetryValidatesModeEndpointAndResourceContract(t *testing.T) {
	tests := []struct {
		name    string
		options TelemetryOptions
		want    error
	}{
		{name: "unknown mode", options: TelemetryOptions{Mode: "best-effort"}, want: ErrInvalidTelemetryMode},
		{name: "disabled endpoint", options: TelemetryOptions{Mode: TelemetryModeDisabled, Endpoint: "http://collector:4318"}, want: ErrTelemetryEndpointForbidden},
		{name: "optional endpoint", options: TelemetryOptions{Mode: TelemetryModeOptional}, want: ErrTelemetryEndpointRequired},
		{name: "required endpoint", options: TelemetryOptions{Mode: TelemetryModeRequired}, want: ErrTelemetryEndpointRequired},
		{name: "required identity", options: TelemetryOptions{Mode: TelemetryModeRequired, Endpoint: "http://collector:4318", ServiceName: ""}, want: ErrTelemetryResource},
		{name: "invalid endpoint", options: TelemetryOptions{Mode: TelemetryModeRequired, Endpoint: "ftp://collector:4318", ServiceName: "api", ServiceVersion: "v", Environment: "test"}, want: ErrTelemetryExporterRequired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := InitializeTelemetry(context.Background(), test.options)
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
		})
	}
}

func TestTelemetryShutdownIsConcurrentAndSecretSafe(t *testing.T) {
	var mutex sync.Mutex
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		mutex.Lock()
		requests++
		mutex.Unlock()
		response.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	telemetry, err := InitializeTelemetry(context.Background(), TelemetryOptions{
		Mode: TelemetryModeRequired, Endpoint: server.URL,
		ServiceName: "zhixu-api", ServiceVersion: "v-test", Environment: "integration",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, span, err := telemetry.Tracer().Start(context.Background(), "shutdown.root")
	if err != nil {
		t.Fatal(err)
	}
	span.End()

	var wait sync.WaitGroup
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if err := telemetry.Shutdown(context.Background()); err != nil {
				t.Errorf("Shutdown: %v", err)
			}
		}()
	}
	wait.Wait()
	mutex.Lock()
	defer mutex.Unlock()
	if requests != 4 {
		t.Fatalf("export requests=%d want one startup and shutdown export for each signal", requests)
	}
}

func TestTelemetryShutdownReturnsStableSecretSafeError(t *testing.T) {
	var requests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) <= 2 {
			response.WriteHeader(http.StatusOK)
			return
		}
		http.Error(response, "shutdown-secret-response", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	telemetry, err := InitializeTelemetry(context.Background(), TelemetryOptions{
		Mode: TelemetryModeRequired, Endpoint: server.URL,
		ServiceName: "zhixu-api", ServiceVersion: "v-test", Environment: "integration",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, span, err := telemetry.Tracer().Start(context.Background(), "shutdown.error")
	if err != nil {
		t.Fatal(err)
	}
	span.End()
	err = telemetry.Shutdown(context.Background())
	if !errors.Is(err, ErrTelemetryShutdown) || strings.Contains(err.Error(), "shutdown-secret-response") || strings.Contains(err.Error(), server.URL) {
		t.Fatalf("Shutdown error=%v", err)
	}
}
