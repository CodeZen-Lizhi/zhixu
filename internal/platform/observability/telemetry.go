package observability

import (
	"context"
	"errors"
	"net/http"
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
	TelemetryStatusDisabled = "TELEMETRY_DISABLED"
	// TelemetryStatusExporting means an external-export-capable Provider was
	// initialized. Collector reachability is proven by actual export evidence.
	TelemetryStatusExporting           = "TELEMETRY_EXPORTING"
	TelemetryStatusExporterUnavailable = "TELEMETRY_EXPORTER_UNAVAILABLE"
)

var (
	ErrInvalidTelemetryMode       = errors.New("invalid telemetry mode")
	ErrTelemetryEndpointRequired  = errors.New("telemetry endpoint is required")
	ErrTelemetryEndpointForbidden = errors.New("telemetry endpoint is forbidden")
	ErrTelemetryExporterRequired  = errors.New("telemetry exporter is unavailable")
	ErrTelemetryMetrics           = errors.New("telemetry metrics initialization failed")
	ErrTelemetryResource          = errors.New("telemetry resource identity is invalid")
	ErrTelemetryShutdown          = errors.New("telemetry shutdown failed")
)

// TelemetryOptions configures explicit exporter behavior.
type TelemetryOptions struct {
	Mode           TelemetryMode
	Endpoint       string
	ServiceName    string
	ServiceVersion string
	Environment    string
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
	metrics        Metrics
	metricsHandler http.Handler
	tracer         Tracer
	provider       traceLifecycle
	status         TelemetryStatus
	once           sync.Once
	closeErr       error
}

// InitializeTelemetry applies disabled/optional/required semantics. Optional
// failures retain Prometheus metrics and a non-exporting SDK provider; required
// failures return a stable error. No mode claims export succeeded without a
// concrete exporter-backed provider.
func InitializeTelemetry(ctx context.Context, options TelemetryOptions) (*Telemetry, error) {
	metrics, metricsHandler, err := newPrometheusMetrics()
	if err != nil {
		return nil, ErrTelemetryMetrics
	}
	endpoint := strings.TrimSpace(options.Endpoint)
	switch options.Mode {
	case TelemetryModeDisabled:
		if endpoint != "" {
			return nil, ErrTelemetryEndpointForbidden
		}
		identity, identityErr := newTelemetryIdentity(options, false)
		if identityErr != nil {
			return nil, identityErr
		}
		return newLocalTelemetry(metrics, metricsHandler, newNoExportTraceProvider(identity), TelemetryStatus{
			Mode: options.Mode, Code: TelemetryStatusDisabled,
		}), nil
	case TelemetryModeOptional, TelemetryModeRequired:
		if endpoint == "" {
			return nil, ErrTelemetryEndpointRequired
		}
	default:
		return nil, ErrInvalidTelemetryMode
	}

	identity, identityErr := newTelemetryIdentity(options, true)
	if identityErr != nil {
		return nil, identityErr
	}
	provider, err := newExportingTraceProvider(ctx, endpoint, identity)
	if err != nil {
		if options.Mode == TelemetryModeRequired {
			return nil, ErrTelemetryExporterRequired
		}
		return newLocalTelemetry(metrics, metricsHandler, newNoExportTraceProvider(identity), TelemetryStatus{
			Mode: options.Mode, Degraded: true, Code: TelemetryStatusExporterUnavailable,
		}), nil
	}
	return &Telemetry{
		metrics: metrics, metricsHandler: metricsHandler,
		tracer: provider.Tracer(), provider: provider,
		status: TelemetryStatus{
			Mode: options.Mode, Exporting: true, Code: TelemetryStatusExporting,
		},
	}, nil
}

func newLocalTelemetry(metrics Metrics, metricsHandler http.Handler, provider traceLifecycle, status TelemetryStatus) *Telemetry {
	return &Telemetry{metrics: metrics, metricsHandler: metricsHandler, tracer: provider.Tracer(), provider: provider, status: status}
}

// Metrics returns the initialized recorder.
func (telemetry *Telemetry) Metrics() Metrics {
	return telemetry.metrics
}

// MetricsHandler returns the process-local Prometheus exposition handler.
func (telemetry *Telemetry) MetricsHandler() http.Handler {
	return telemetry.metricsHandler
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

// MemoryProvider groups the in-memory adapters used by callback and facade
// tests without claiming external export capability.
type MemoryProvider struct {
	metrics *MemoryMetrics
	tracer  *MemoryTracer
	once    sync.Once
}

// NewMemoryProvider constructs isolated in-memory metric and trace adapters.
func NewMemoryProvider() *MemoryProvider {
	return &MemoryProvider{metrics: NewMemoryMetrics(), tracer: NewMemoryTracer()}
}

func (provider *MemoryProvider) Metrics() Metrics { return provider.metrics }
func (provider *MemoryProvider) Tracer() Tracer   { return provider.tracer }

// Shutdown closes both in-memory resources exactly once.
func (provider *MemoryProvider) Shutdown(context.Context) error {
	if provider == nil {
		return nil
	}
	provider.once.Do(func() {
		_ = provider.metrics.close()
		_ = provider.tracer.close()
	})
	return nil
}
