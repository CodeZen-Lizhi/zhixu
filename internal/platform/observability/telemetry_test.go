package observability

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestInitializeTelemetryModes(t *testing.T) {
	t.Run("disabled never opens exporter", func(t *testing.T) {
		opened := 0
		telemetry, err := InitializeTelemetry(context.Background(), TelemetryOptions{
			Mode: TelemetryModeDisabled,
			Factory: ProviderFactoryFunc(func(context.Context, string) (Provider, error) {
				opened++
				return NewMemoryProvider(), nil
			}),
		})
		if err != nil {
			t.Fatalf("InitializeTelemetry: %v", err)
		}
		if opened != 0 || telemetry.Status() != (TelemetryStatus{Mode: TelemetryModeDisabled, Code: TelemetryStatusDisabled}) {
			t.Fatalf("opened=%d status=%#v", opened, telemetry.Status())
		}
	})

	t.Run("optional failure is explicit degraded noop", func(t *testing.T) {
		telemetry, err := InitializeTelemetry(context.Background(), TelemetryOptions{
			Mode: TelemetryModeOptional, Endpoint: "https://collector.example.test:4318",
			Factory: ProviderFactoryFunc(func(context.Context, string) (Provider, error) {
				return nil, errors.New("dial collector with token=exporter-secret")
			}),
		})
		if err != nil {
			t.Fatalf("InitializeTelemetry: %v", err)
		}
		want := TelemetryStatus{Mode: TelemetryModeOptional, Degraded: true, Code: TelemetryStatusExporterUnavailable}
		if telemetry.Status() != want {
			t.Fatalf("status = %#v, want %#v", telemetry.Status(), want)
		}
	})

	t.Run("required failure is stable", func(t *testing.T) {
		_, err := InitializeTelemetry(context.Background(), TelemetryOptions{
			Mode: TelemetryModeRequired, Endpoint: "https://user:exporter-secret@collector.example.test:4318",
			Factory: ProviderFactoryFunc(func(context.Context, string) (Provider, error) {
				return nil, errors.New("dial https://user:exporter-secret@collector.example.test:4318")
			}),
		})
		if !errors.Is(err, ErrTelemetryExporterRequired) {
			t.Fatalf("error = %v, want exporter required", err)
		}
		if stringsContainsAny(err.Error(), "exporter-secret", "collector.example.test") {
			t.Fatalf("required error leaked endpoint: %v", err)
		}
	})

	t.Run("optional success records real provider status", func(t *testing.T) {
		provider := &countingProvider{metrics: NewNoopMetrics(), tracer: NewNoopTracer(), external: true}
		telemetry, err := InitializeTelemetry(context.Background(), TelemetryOptions{
			Mode: TelemetryModeOptional, Endpoint: "http://collector:4318",
			Factory: ProviderFactoryFunc(func(context.Context, string) (Provider, error) {
				return provider, nil
			}),
		})
		if err != nil {
			t.Fatalf("InitializeTelemetry: %v", err)
		}
		want := TelemetryStatus{Mode: TelemetryModeOptional, Exporting: true, Code: TelemetryStatusExporting}
		if telemetry.Status() != want {
			t.Fatalf("status = %#v, want %#v", telemetry.Status(), want)
		}
	})

	t.Run("memory provider cannot masquerade as external export", func(t *testing.T) {
		provider := NewMemoryProvider()
		telemetry, err := InitializeTelemetry(context.Background(), TelemetryOptions{
			Mode: TelemetryModeOptional, Endpoint: "http://collector:4318",
			Factory: ProviderFactoryFunc(func(context.Context, string) (Provider, error) {
				return provider, nil
			}),
		})
		if err != nil {
			t.Fatalf("InitializeTelemetry: %v", err)
		}
		if status := telemetry.Status(); !status.Degraded || status.Exporting || status.Code != TelemetryStatusExporterUnavailable {
			t.Fatalf("status = %#v", status)
		}
		labels, labelErr := NewLabels(map[string]string{"queue": "workflow"})
		if labelErr != nil {
			t.Fatalf("NewLabels: %v", labelErr)
		}
		measurement, measurementErr := NewMeasurement(MetricQueueDepth, MetricKindGauge, 0, labels)
		if measurementErr != nil {
			t.Fatalf("NewMeasurement: %v", measurementErr)
		}
		if recordErr := provider.Metrics().Record(context.Background(), measurement); !errors.Is(recordErr, ErrObservabilityClosed) {
			t.Fatalf("rejected provider was not closed: %v", recordErr)
		}
	})
}

func TestInitializeTelemetryValidatesModeEndpointContract(t *testing.T) {
	tests := []struct {
		name    string
		options TelemetryOptions
		want    error
	}{
		{name: "unknown", options: TelemetryOptions{Mode: "best-effort"}, want: ErrInvalidTelemetryMode},
		{name: "disabled endpoint", options: TelemetryOptions{Mode: TelemetryModeDisabled, Endpoint: "http://collector:4318"}, want: ErrTelemetryEndpointForbidden},
		{name: "optional endpoint", options: TelemetryOptions{Mode: TelemetryModeOptional}, want: ErrTelemetryEndpointRequired},
		{name: "required endpoint", options: TelemetryOptions{Mode: TelemetryModeRequired}, want: ErrTelemetryEndpointRequired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := InitializeTelemetry(context.Background(), test.options)
			if !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestTelemetryShutdownClosesProviderExactlyOnce(t *testing.T) {
	provider := &countingProvider{metrics: NewNoopMetrics(), tracer: NewNoopTracer()}
	provider.external = true
	telemetry, err := InitializeTelemetry(context.Background(), TelemetryOptions{
		Mode: TelemetryModeRequired, Endpoint: "http://collector:4318",
		Factory: ProviderFactoryFunc(func(context.Context, string) (Provider, error) {
			return provider, nil
		}),
	})
	if err != nil {
		t.Fatalf("InitializeTelemetry: %v", err)
	}
	for range 3 {
		if err := telemetry.Shutdown(context.Background()); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	}
	if provider.shutdowns != 1 {
		t.Fatalf("shutdowns = %d, want 1", provider.shutdowns)
	}
}

func TestTelemetryShutdownReturnsStableSecretSafeError(t *testing.T) {
	provider := &countingProvider{
		metrics: NewNoopMetrics(), tracer: NewNoopTracer(), external: true,
		shutdownErr: errors.New("close https://user:shutdown-secret@collector.example.test"),
	}
	telemetry, err := InitializeTelemetry(context.Background(), TelemetryOptions{
		Mode: TelemetryModeRequired, Endpoint: "http://collector:4318",
		Factory: ProviderFactoryFunc(func(context.Context, string) (Provider, error) {
			return provider, nil
		}),
	})
	if err != nil {
		t.Fatalf("InitializeTelemetry: %v", err)
	}
	err = telemetry.Shutdown(context.Background())
	if !errors.Is(err, ErrTelemetryShutdown) || stringsContainsAny(err.Error(), "shutdown-secret", "collector.example.test") {
		t.Fatalf("Shutdown error = %v", err)
	}
}

type countingProvider struct {
	metrics     Metrics
	tracer      Tracer
	shutdowns   int
	external    bool
	shutdownErr error
}

func (provider *countingProvider) Metrics() Metrics { return provider.metrics }
func (provider *countingProvider) Tracer() Tracer   { return provider.tracer }
func (provider *countingProvider) ExternalExport() bool {
	return provider.external
}
func (provider *countingProvider) Shutdown(context.Context) error {
	provider.shutdowns++
	return provider.shutdownErr
}

func stringsContainsAny(value string, substrings ...string) bool {
	for _, substring := range substrings {
		if strings.Contains(value, substring) {
			return true
		}
	}
	return false
}
