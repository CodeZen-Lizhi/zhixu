package observability

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"unicode"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	otelmetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.37.0"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const instrumentationName = "github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"

var (
	ErrInvalidOTLPConfiguration = errors.New("invalid OTLP telemetry configuration")
	ErrOTLPExporterOpen         = errors.New("OTLP telemetry exporter initialization failed")
)

// OTLPHTTPProviderFactory constructs an OTLP/HTTP provider scoped to one
// process. It does not mutate OpenTelemetry's global providers.
type OTLPHTTPProviderFactory struct {
	serviceName    string
	serviceVersion string
}

// NewOTLPHTTPProviderFactory creates a concrete external exporter factory.
// serviceName should distinguish independently deployed API and Worker
// processes, while serviceVersion identifies the immutable release.
func NewOTLPHTTPProviderFactory(serviceName, serviceVersion string) ProviderFactory {
	return &OTLPHTTPProviderFactory{
		serviceName:    strings.TrimSpace(serviceName),
		serviceVersion: strings.TrimSpace(serviceVersion),
	}
}

// Open implements ProviderFactory. endpoint is the standard OTLP base URL;
// signal-specific paths are appended without exposing the endpoint in errors.
func (factory *OTLPHTTPProviderFactory) Open(ctx context.Context, endpoint string) (Provider, error) {
	if factory == nil || !validResourceValue("service.name", factory.serviceName) ||
		!validResourceValue("service.version", factory.serviceVersion) {
		return nil, ErrInvalidOTLPConfiguration
	}
	metricsEndpoint, tracesEndpoint, err := signalEndpoints(endpoint)
	if err != nil {
		return nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}

	metricExporter, err := otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpointURL(metricsEndpoint))
	if err != nil {
		return nil, ErrOTLPExporterOpen
	}
	traceExporter, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(tracesEndpoint))
	if err != nil {
		_ = metricExporter.Shutdown(ctx)
		return nil, ErrOTLPExporterOpen
	}

	telemetryResource := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceName(factory.serviceName),
		semconv.ServiceVersion(factory.serviceVersion),
	)
	metricProvider := metric.NewMeterProvider(
		metric.WithResource(telemetryResource),
		metric.WithReader(metric.NewPeriodicReader(metricExporter)),
	)
	traceProvider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(telemetryResource),
		sdktrace.WithBatcher(traceExporter),
	)
	closed := &atomic.Bool{}
	metrics, err := newOTLPMetrics(metricProvider.Meter(instrumentationName), closed)
	if err != nil {
		closed.Store(true)
		_ = traceProvider.Shutdown(ctx)
		_ = metricProvider.Shutdown(ctx)
		return nil, ErrOTLPExporterOpen
	}

	return &otlpProvider{
		metrics:        metrics,
		tracer:         &otlpTracer{tracer: traceProvider.Tracer(instrumentationName), closed: closed},
		metricProvider: metricProvider,
		traceProvider:  traceProvider,
		closed:         closed,
	}, nil
}

func validResourceValue(key, value string) bool {
	if value == "" || len(value) > 128 || strings.TrimSpace(value) != value || redactString(key, value) == RedactedValue {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func signalEndpoints(endpoint string) (string, string, error) {
	endpoint = strings.TrimSpace(endpoint)
	parsed, err := url.Parse(endpoint)
	if err != nil || endpoint == "" || parsed.Scheme == "" || parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", "", ErrInvalidOTLPConfiguration
	}
	metricsURL := parsed.JoinPath("v1", "metrics")
	tracesURL := parsed.JoinPath("v1", "traces")
	return metricsURL.String(), tracesURL.String(), nil
}

type otlpProvider struct {
	metrics        *otlpMetrics
	tracer         *otlpTracer
	metricProvider *metric.MeterProvider
	traceProvider  *sdktrace.TracerProvider
	closed         *atomic.Bool
	once           sync.Once
	shutdownErr    error
}

func (provider *otlpProvider) Metrics() Metrics { return provider.metrics }
func (provider *otlpProvider) Tracer() Tracer   { return provider.tracer }
func (provider *otlpProvider) ExternalExport() bool {
	return true
}

func (provider *otlpProvider) Shutdown(ctx context.Context) error {
	if provider == nil {
		return nil
	}
	provider.once.Do(func() {
		provider.closed.Store(true)
		if ctx == nil {
			ctx = context.Background()
		}
		var traceErr, metricErr error
		var group sync.WaitGroup
		group.Add(2)
		go func() {
			defer group.Done()
			traceErr = provider.traceProvider.Shutdown(ctx)
		}()
		go func() {
			defer group.Done()
			metricErr = provider.metricProvider.Shutdown(ctx)
		}()
		group.Wait()
		if traceErr != nil || metricErr != nil {
			provider.shutdownErr = errors.Join(traceErr, metricErr)
		}
	})
	return provider.shutdownErr
}

type otlpMetrics struct {
	counters   map[MetricName]otelmetric.Float64Counter
	gauges     map[MetricName]otelmetric.Float64Gauge
	histograms map[MetricName]otelmetric.Float64Histogram
	closed     *atomic.Bool
}

func newOTLPMetrics(meter otelmetric.Meter, closed *atomic.Bool) (*otlpMetrics, error) {
	metrics := &otlpMetrics{
		counters:   make(map[MetricName]otelmetric.Float64Counter),
		gauges:     make(map[MetricName]otelmetric.Float64Gauge),
		histograms: make(map[MetricName]otelmetric.Float64Histogram),
		closed:     closed,
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

func (metrics *otlpMetrics) Record(ctx context.Context, measurement Measurement) error {
	if err := measurement.Validate(); err != nil {
		return err
	}
	if metrics == nil || metrics.closed.Load() {
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
	attrs := make([]attribute.KeyValue, 0, len(keys))
	for _, key := range keys {
		attrs = append(attrs, attribute.String(key, values[key]))
	}
	return attrs
}

type otlpTracer struct {
	tracer oteltrace.Tracer
	closed *atomic.Bool
}

func (tracer *otlpTracer) Start(ctx context.Context, operation string, attrs ...TraceAttribute) (context.Context, Span, error) {
	if err := validateTraceInput(operation, attrs); err != nil {
		return ctx, nil, err
	}
	if tracer == nil || tracer.closed.Load() {
		return ctx, nil, ErrObservabilityClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	parentContext := ctx
	if parent, found := TraceContextFromContext(ctx); found {
		remoteParent, err := newOTelSpanContext(parent, true)
		if err != nil {
			return ctx, nil, err
		}
		parentContext = oteltrace.ContextWithRemoteSpanContext(ctx, remoteParent)
	}
	spanAttrs := append(append([]TraceAttribute(nil), attrs...), correlationTraceAttributes(CorrelationFromContext(ctx))...)
	startedContext, startedSpan := tracer.tracer.Start(
		parentContext,
		strings.TrimSpace(operation),
		oteltrace.WithAttributes(traceAttributes(spanAttrs)...),
	)
	spanContext := startedSpan.SpanContext()
	projectContext := TraceContext{
		TraceID:    spanContext.TraceID().String(),
		SpanID:     spanContext.SpanID().String(),
		TraceFlags: fmt.Sprintf("%02x", byte(spanContext.TraceFlags())),
	}
	childContext, err := WithTraceContext(startedContext, projectContext)
	if err != nil {
		startedSpan.End()
		return ctx, nil, ErrInvalidTraceContext
	}
	return childContext, &otlpSpan{span: startedSpan, closed: tracer.closed}, nil
}

func newOTelSpanContext(project TraceContext, remote bool) (oteltrace.SpanContext, error) {
	traceID, err := oteltrace.TraceIDFromHex(project.TraceID)
	if err != nil {
		return oteltrace.SpanContext{}, ErrInvalidTraceContext
	}
	spanID, err := oteltrace.SpanIDFromHex(project.SpanID)
	if err != nil {
		return oteltrace.SpanContext{}, ErrInvalidTraceContext
	}
	flags, err := strconv.ParseUint(project.TraceFlags, 16, 8)
	if err != nil {
		return oteltrace.SpanContext{}, ErrInvalidTraceContext
	}
	spanContext := oteltrace.NewSpanContext(oteltrace.SpanContextConfig{
		TraceID: traceID, SpanID: spanID, TraceFlags: oteltrace.TraceFlags(flags), Remote: remote,
	})
	if !spanContext.IsValid() {
		return oteltrace.SpanContext{}, ErrInvalidTraceContext
	}
	return spanContext, nil
}

func traceAttributes(attrs []TraceAttribute) []attribute.KeyValue {
	result := make([]attribute.KeyValue, 0, len(attrs))
	for _, attr := range attrs {
		if !traceAttributePattern.MatchString(attr.Key) {
			continue
		}
		result = append(result, attribute.String(attr.Key, redactString(attr.Key, attr.Value)))
	}
	return result
}

type otlpSpan struct {
	span   oteltrace.Span
	closed *atomic.Bool
	once   sync.Once
}

func (span *otlpSpan) SetAttributes(attrs ...TraceAttribute) {
	if span == nil || span.closed.Load() {
		return
	}
	span.span.SetAttributes(traceAttributes(attrs)...)
}

func (span *otlpSpan) RecordError(errorCode string) {
	if span == nil || span.closed.Load() {
		return
	}
	safeCode := redactString("error_code", strings.TrimSpace(errorCode))
	span.span.SetAttributes(attribute.String("error_code", safeCode))
	span.span.SetStatus(codes.Error, safeCode)
}

func (span *otlpSpan) End() {
	if span == nil {
		return
	}
	span.once.Do(func() { span.span.End() })
}
