package observability

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	otelmetric "go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/exemplar"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
)

const otlpMetricExportInterval = 30 * time.Second

type metricLifecycle interface {
	Metrics() Metrics
	Shutdown(context.Context) error
}

type otelMetricProvider struct {
	provider  *sdkmetric.MeterProvider
	metrics   *otelMetrics
	transport *http.Transport
	closed    *atomic.Bool
	once      sync.Once
	closeErr  error
}

func newExportingMetricProvider(ctx context.Context, endpoint string, identity telemetryIdentity) (*otelMetricProvider, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	metricURL, err := buildOTLPSignalURL(endpoint, "metrics")
	if err != nil {
		return nil, ErrTelemetryExporterRequired
	}

	transport := newOTLPHTTPTransport()
	httpClient := &http.Client{
		Transport: transport,
		Timeout:   otlpExportTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	exporter, err := otlpmetrichttp.New(ctx,
		otlpmetrichttp.WithHeaders(map[string]string{}),
		otlpmetrichttp.WithCompression(otlpmetrichttp.NoCompression),
		otlpmetrichttp.WithRetry(otlpmetrichttp.RetryConfig{
			Enabled:         true,
			InitialInterval: otlpRetryInitialInterval,
			MaxInterval:     otlpRetryMaxInterval,
			MaxElapsedTime:  otlpRetryMaxElapsedTime,
		}),
		otlpmetrichttp.WithTimeout(otlpExportTimeout),
		otlpmetrichttp.WithMaxRequestSize(otlpMaxRequestSize),
		otlpmetrichttp.WithTemporalitySelector(sdkmetric.DefaultTemporalitySelector),
		otlpmetrichttp.WithAggregationSelector(sdkmetric.DefaultAggregationSelector),
		otlpmetrichttp.WithTLSClientConfig(nil),
		otlpmetrichttp.WithHTTPClient(httpClient),
		otlpmetrichttp.WithEndpointURL(metricURL),
	)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, ErrTelemetryExporterRequired
	}

	exactResource := identity.resource()
	safeExporter := &exactResourceMetricExporter{Exporter: exporter, resource: exactResource}
	reader := sdkmetric.NewPeriodicReader(safeExporter,
		sdkmetric.WithInterval(otlpMetricExportInterval),
		sdkmetric.WithTimeout(otlpExportTimeout),
	)
	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(exactResource),
		sdkmetric.WithReader(reader),
		sdkmetric.WithExemplarFilter(exemplar.AlwaysOffFilter),
		sdkmetric.WithCardinalityLimit(2000),
	)
	closed := &atomic.Bool{}
	metrics, err := newOTelMetrics(provider.Meter(telemetryInstrumentationName), closed)
	if err != nil {
		closed.Store(true)
		cleanupCtx, cancel := context.WithTimeout(context.Background(), otlpExportTimeout)
		_ = provider.Shutdown(cleanupCtx)
		cancel()
		transport.CloseIdleConnections()
		return nil, ErrTelemetryExporterRequired
	}
	runtime := &otelMetricProvider{
		provider: provider, metrics: metrics, transport: transport, closed: closed,
	}
	if err := RecordProcessPresence(context.Background(), metrics); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), otlpExportTimeout)
		_ = runtime.shutdown(cleanupCtx)
		cancel()
		return nil, ErrTelemetryExporterRequired
	}
	probeCtx, cancel := context.WithTimeout(ctx, otlpStartupProbeTimeout)
	defer cancel()
	if err := provider.ForceFlush(probeCtx); err != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), otlpExportTimeout)
		_ = runtime.shutdown(cleanupCtx)
		cleanupCancel()
		return nil, ErrTelemetryExporterRequired
	}
	return runtime, nil
}

type exactResourceMetricExporter struct {
	sdkmetric.Exporter
	resource *resource.Resource
}

func (exporter *exactResourceMetricExporter) Export(ctx context.Context, data *metricdata.ResourceMetrics) error {
	if data == nil {
		return ErrTelemetryExporterRequired
	}
	projectData := *data
	projectData.Resource = exporter.resource
	if err := exporter.Exporter.Export(ctx, &projectData); err != nil {
		return ErrTelemetryExporterRequired
	}
	return nil
}

func (provider *otelMetricProvider) Metrics() Metrics { return provider.metrics }

func (provider *otelMetricProvider) Shutdown(ctx context.Context) error {
	if provider == nil {
		return nil
	}
	provider.once.Do(func() {
		provider.closeErr = provider.shutdown(ctx)
	})
	return provider.closeErr
}

func (provider *otelMetricProvider) shutdown(ctx context.Context) error {
	if provider == nil || provider.provider == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	provider.closed.Store(true)
	result := provider.provider.Shutdown(ctx)
	if provider.transport != nil {
		provider.transport.CloseIdleConnections()
	}
	return result
}

type otelMetrics struct {
	counters   map[MetricName]otelmetric.Float64Counter
	gauges     map[MetricName]otelmetric.Float64Gauge
	histograms map[MetricName]otelmetric.Float64Histogram
	closed     *atomic.Bool
}

func newOTelMetrics(meter otelmetric.Meter, closed *atomic.Bool) (*otelMetrics, error) {
	metrics := &otelMetrics{
		counters: make(map[MetricName]otelmetric.Float64Counter), gauges: make(map[MetricName]otelmetric.Float64Gauge),
		histograms: make(map[MetricName]otelmetric.Float64Histogram), closed: closed,
	}
	for _, name := range MetricNames() {
		definition := metricDefinitions[name]
		switch definition.kind {
		case MetricKindCounter:
			instrument, err := meter.Float64Counter(string(name))
			if err != nil {
				return nil, err
			}
			metrics.counters[name] = instrument
		case MetricKindGauge:
			instrument, err := meter.Float64Gauge(string(name))
			if err != nil {
				return nil, err
			}
			metrics.gauges[name] = instrument
		case MetricKindHistogram:
			instrument, err := meter.Float64Histogram(string(name))
			if err != nil {
				return nil, err
			}
			metrics.histograms[name] = instrument
		default:
			return nil, ErrInvalidMetric
		}
	}
	return metrics, nil
}

func (metrics *otelMetrics) Record(ctx context.Context, measurement Measurement) error {
	if err := measurement.Validate(); err != nil {
		return err
	}
	if metrics == nil || metrics.closed == nil || metrics.closed.Load() {
		return ErrObservabilityClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	options := otelmetric.WithAttributes(metricAttributes(measurement.Labels)...)
	switch measurement.Kind {
	case MetricKindCounter:
		metrics.counters[measurement.Name].Add(ctx, measurement.Value, options)
	case MetricKindGauge:
		metrics.gauges[measurement.Name].Record(ctx, measurement.Value, options)
	case MetricKindHistogram:
		metrics.histograms[measurement.Name].Record(ctx, measurement.Value, options)
	default:
		return ErrInvalidMetric
	}
	return nil
}

func metricAttributes(labels Labels) []attribute.KeyValue {
	values := labels.Map()
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	attributes := make([]attribute.KeyValue, 0, len(keys))
	for _, key := range keys {
		attributes = append(attributes, attribute.String(key, values[key]))
	}
	return attributes
}

type fanoutMetrics struct {
	local    Metrics
	external Metrics
}

func newFanoutMetrics(local, external Metrics) Metrics {
	return &fanoutMetrics{local: local, external: external}
}

func (metrics *fanoutMetrics) Record(ctx context.Context, measurement Measurement) error {
	if err := measurement.Validate(); err != nil {
		return err
	}
	if metrics == nil || metrics.local == nil || metrics.external == nil {
		return ErrMetricsUnavailable
	}
	if err := metrics.external.Record(ctx, measurement); err != nil {
		if errors.Is(err, ErrObservabilityClosed) {
			return ErrObservabilityClosed
		}
		return ErrTelemetryMetrics
	}
	if err := metrics.local.Record(ctx, measurement); err != nil {
		if errors.Is(err, ErrObservabilityClosed) {
			return ErrObservabilityClosed
		}
		return ErrTelemetryMetrics
	}
	return nil
}
