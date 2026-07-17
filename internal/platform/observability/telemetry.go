package observability

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
)

// TelemetryMode controls exporter initialization behavior.
type TelemetryMode string

const (
	TelemetryModeDisabled TelemetryMode = "disabled"
	TelemetryModeOptional TelemetryMode = "optional"
	TelemetryModeRequired TelemetryMode = "required"
)

const (
	TelemetryStatusDisabled            = "TELEMETRY_DISABLED"
	TelemetryStatusExporting           = "TELEMETRY_EXPORTING"
	TelemetryStatusExporterUnavailable = "TELEMETRY_EXPORTER_UNAVAILABLE"
)

var (
	ErrInvalidTelemetryMode       = errors.New("invalid telemetry mode")
	ErrTelemetryEndpointRequired  = errors.New("telemetry endpoint is required")
	ErrTelemetryEndpointForbidden = errors.New("telemetry endpoint is forbidden")
	ErrTelemetryExporterRequired  = errors.New("telemetry exporter is unavailable")
	ErrTelemetryShutdown          = errors.New("telemetry shutdown failed")
)

// Provider owns concrete metric and trace adapters and their shared resources.
// A future OpenTelemetry adapter can satisfy this interface without leaking SDK
// types into runtime packages.
type Provider interface {
	Metrics() Metrics
	Tracer() Tracer
	ExternalExport() bool
	Shutdown(context.Context) error
}

// ProviderFactory constructs a real exporter-backed Provider for an endpoint.
type ProviderFactory interface {
	Open(context.Context, string) (Provider, error)
}

// ProviderFactoryFunc adapts a function to ProviderFactory.
type ProviderFactoryFunc func(context.Context, string) (Provider, error)

// Open implements ProviderFactory.
func (factory ProviderFactoryFunc) Open(ctx context.Context, endpoint string) (Provider, error) {
	return factory(ctx, endpoint)
}

// TelemetryOptions configures explicit exporter behavior.
type TelemetryOptions struct {
	Mode     TelemetryMode
	Endpoint string
	Factory  ProviderFactory
}

// TelemetryStatus is safe for readiness and logs; it never exposes an endpoint
// or the raw exporter error.
type TelemetryStatus struct {
	Mode      TelemetryMode
	Exporting bool
	Degraded  bool
	Code      string
}

// Telemetry contains the initialized project interfaces.
type Telemetry struct {
	metrics  Metrics
	tracer   Tracer
	provider Provider
	status   TelemetryStatus
	once     sync.Once
	closeErr error
}

// InitializeTelemetry applies disabled/optional/required semantics. Optional
// failures return a degraded noop bundle; required failures return a stable
// error. No mode ever claims export succeeded without a concrete Provider.
func InitializeTelemetry(ctx context.Context, options TelemetryOptions) (*Telemetry, error) {
	endpoint := strings.TrimSpace(options.Endpoint)
	switch options.Mode {
	case TelemetryModeDisabled:
		if endpoint != "" {
			return nil, ErrTelemetryEndpointForbidden
		}
		return newNoopTelemetry(TelemetryStatus{Mode: options.Mode, Code: TelemetryStatusDisabled}), nil
	case TelemetryModeOptional, TelemetryModeRequired:
		if endpoint == "" {
			return nil, ErrTelemetryEndpointRequired
		}
	default:
		return nil, ErrInvalidTelemetryMode
	}

	provider, err := openProvider(ctx, options.Factory, endpoint)
	if err != nil {
		if options.Mode == TelemetryModeRequired {
			return nil, ErrTelemetryExporterRequired
		}
		return newNoopTelemetry(TelemetryStatus{
			Mode: options.Mode, Degraded: true, Code: TelemetryStatusExporterUnavailable,
		}), nil
	}
	return &Telemetry{
		metrics:  provider.Metrics(),
		tracer:   provider.Tracer(),
		provider: provider,
		status: TelemetryStatus{
			Mode: options.Mode, Exporting: true, Code: TelemetryStatusExporting,
		},
	}, nil
}

func openProvider(ctx context.Context, factory ProviderFactory, endpoint string) (Provider, error) {
	if isNil(factory) {
		return nil, ErrTelemetryExporterRequired
	}
	provider, err := factory.Open(ctx, endpoint)
	if err != nil || isNil(provider) {
		return nil, ErrTelemetryExporterRequired
	}
	if !provider.ExternalExport() || isNil(provider.Metrics()) || isNil(provider.Tracer()) {
		_ = provider.Shutdown(ctx)
		return nil, ErrTelemetryExporterRequired
	}
	return provider, nil
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func newNoopTelemetry(status TelemetryStatus) *Telemetry {
	return &Telemetry{metrics: NewNoopMetrics(), tracer: NewNoopTracer(), status: status}
}

// Metrics returns the initialized recorder.
func (telemetry *Telemetry) Metrics() Metrics {
	return telemetry.metrics
}

// Tracer returns the initialized tracer.
func (telemetry *Telemetry) Tracer() Tracer {
	return telemetry.tracer
}

// Status returns a secret-safe initialization snapshot.
func (telemetry *Telemetry) Status() TelemetryStatus {
	return telemetry.status
}

// Shutdown releases exporter resources exactly once and returns the same result
// to all callers.
func (telemetry *Telemetry) Shutdown(ctx context.Context) error {
	if telemetry == nil {
		return nil
	}
	telemetry.once.Do(func() {
		if telemetry.provider != nil {
			if err := telemetry.provider.Shutdown(ctx); err != nil {
				telemetry.closeErr = ErrTelemetryShutdown
			}
		}
	})
	return telemetry.closeErr
}

// MemoryProvider is an in-memory adapter for tests and local diagnostics. It
// does not claim external export.
type MemoryProvider struct {
	metrics *MemoryMetrics
	tracer  *MemoryTracer
	once    sync.Once
}

// NewMemoryProvider constructs a provider with isolated in-memory resources.
func NewMemoryProvider() *MemoryProvider {
	return &MemoryProvider{metrics: NewMemoryMetrics(), tracer: NewMemoryTracer()}
}

func (provider *MemoryProvider) Metrics() Metrics { return provider.metrics }
func (provider *MemoryProvider) Tracer() Tracer   { return provider.tracer }
func (provider *MemoryProvider) ExternalExport() bool {
	return false
}

// Shutdown closes both in-memory resources once.
func (provider *MemoryProvider) Shutdown(context.Context) error {
	provider.once.Do(func() {
		_ = provider.metrics.close()
		_ = provider.tracer.close()
	})
	return nil
}
