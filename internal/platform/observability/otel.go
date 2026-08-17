package observability

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const (
	telemetryInstrumentationName = "github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
	otlpExportTimeout            = 2 * time.Second
	otlpStartupProbeTimeout      = 3 * time.Second
	otlpBatchTimeout             = 2 * time.Second
	otlpMaxQueueSize             = 512
	otlpMaxExportBatchSize       = 128
	otlpMaxRequestSize           = 4 << 20
	otlpRetryInitialInterval     = 25 * time.Millisecond
	otlpRetryMaxInterval         = 50 * time.Millisecond
	otlpRetryMaxElapsedTime      = 200 * time.Millisecond
)

var explicitSpanLimits = sdktrace.SpanLimits{
	AttributeValueLengthLimit:   1024,
	AttributeCountLimit:         128,
	EventCountLimit:             32,
	LinkCountLimit:              32,
	AttributePerEventCountLimit: 32,
	AttributePerLinkCountLimit:  32,
}

type traceLifecycle interface {
	Tracer() Tracer
	Shutdown(context.Context) error
}

type otelTraceProvider struct {
	provider  *sdktrace.TracerProvider
	tracer    Tracer
	transport *http.Transport
}

func newNoExportTraceProvider(identity telemetryIdentity) *otelTraceProvider {
	exactResource := identity.resource()
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.NeverSample())),
		sdktrace.WithRawSpanLimits(explicitSpanLimits),
		sdktrace.WithResource(exactResource),
	)
	return &otelTraceProvider{
		provider: provider,
		tracer:   newOTelTracer(provider.Tracer(telemetryInstrumentationName)),
	}
}

func newExportingTraceProvider(ctx context.Context, endpoint string, identity telemetryIdentity) (*otelTraceProvider, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	traceURL, err := buildOTLPTraceURL(endpoint)
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
	exporter, err := otlptracehttp.New(ctx,
		otlptracehttp.WithHeaders(map[string]string{}),
		otlptracehttp.WithCompression(otlptracehttp.NoCompression),
		otlptracehttp.WithRetry(otlptracehttp.RetryConfig{
			Enabled:         true,
			InitialInterval: otlpRetryInitialInterval,
			MaxInterval:     otlpRetryMaxInterval,
			MaxElapsedTime:  otlpRetryMaxElapsedTime,
		}),
		otlptracehttp.WithTimeout(otlpExportTimeout),
		otlptracehttp.WithMaxRequestSize(otlpMaxRequestSize),
		// A nil TLS config deliberately clears certificate settings loaded by
		// the SDK from OTEL_* environment variables. TLS is owned by httpClient.
		otlptracehttp.WithTLSClientConfig(nil),
		otlptracehttp.WithHTTPClient(httpClient),
		// Keep this last so endpoint, path and secure/insecure state all
		// override the SDK's environment-derived configuration.
		otlptracehttp.WithEndpointURL(traceURL),
	)
	if err != nil {
		transport.CloseIdleConnections()
		return nil, ErrTelemetryExporterRequired
	}

	exactResource := identity.resource()
	safeExporter := &exactResourceExporter{SpanExporter: exporter, resource: exactResource}
	processor := sdktrace.NewBatchSpanProcessor(safeExporter,
		sdktrace.WithMaxQueueSize(otlpMaxQueueSize),
		sdktrace.WithMaxExportBatchSize(otlpMaxExportBatchSize),
		sdktrace.WithBatchTimeout(otlpBatchTimeout),
		sdktrace.WithExportTimeout(otlpExportTimeout),
	)
	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
		sdktrace.WithRawSpanLimits(explicitSpanLimits),
		sdktrace.WithResource(exactResource),
		sdktrace.WithSpanProcessor(processor),
	)
	runtime := &otelTraceProvider{
		provider:  provider,
		tracer:    newOTelTracer(provider.Tracer(telemetryInstrumentationName)),
		transport: transport,
	}

	_, startup := provider.Tracer(telemetryInstrumentationName).Start(context.Background(), "telemetry.startup")
	startup.End()
	probeCtx, cancel := context.WithTimeout(ctx, otlpStartupProbeTimeout)
	defer cancel()
	if err := provider.ForceFlush(probeCtx); err != nil {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), otlpExportTimeout)
		_ = runtime.shutdown(cleanupCtx, false)
		cleanupCancel()
		return nil, ErrTelemetryExporterRequired
	}
	return runtime, nil
}

func newOTLPHTTPTransport() *http.Transport {
	return &http.Transport{
		Proxy: nil,
		DialContext: (&net.Dialer{
			Timeout:   otlpExportTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          20,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   otlpExportTimeout,
		ResponseHeaderTimeout: otlpExportTimeout,
		ExpectContinueTimeout: time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
	}
}

func buildOTLPTraceURL(endpoint string) (string, error) {
	return buildOTLPSignalURL(endpoint, "traces")
}

func buildOTLPSignalURL(endpoint, signal string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed == nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", ErrTelemetryExporterRequired
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", ErrTelemetryExporterRequired
	}
	if signal != "traces" && signal != "metrics" {
		return "", ErrTelemetryExporterRequired
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/v1/" + signal
	parsed.RawPath = ""
	return parsed.String(), nil
}

type exactResourceExporter struct {
	sdktrace.SpanExporter
	resource *resource.Resource
}

func (exporter *exactResourceExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	projectSpans := make([]sdktrace.ReadOnlySpan, len(spans))
	for index, span := range spans {
		projectSpans[index] = exactResourceSpan{ReadOnlySpan: span, resource: exporter.resource}
	}
	if err := exporter.SpanExporter.ExportSpans(ctx, projectSpans); err != nil {
		return ErrTelemetryExporterRequired
	}
	return nil
}

type exactResourceSpan struct {
	sdktrace.ReadOnlySpan
	resource *resource.Resource
}

func (span exactResourceSpan) Resource() *resource.Resource { return span.resource }

func (provider *otelTraceProvider) Tracer() Tracer { return provider.tracer }

func (provider *otelTraceProvider) Shutdown(ctx context.Context) error {
	return provider.shutdown(ctx, true)
}

func (provider *otelTraceProvider) shutdown(ctx context.Context, flush bool) error {
	if provider == nil || provider.provider == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var result error
	if flush {
		result = errors.Join(result, provider.provider.ForceFlush(ctx))
	}
	result = errors.Join(result, provider.provider.Shutdown(ctx))
	if provider.transport != nil {
		provider.transport.CloseIdleConnections()
	}
	return result
}

type otelTracer struct {
	tracer oteltrace.Tracer
}

type otelSpan struct {
	span oteltrace.Span
	once sync.Once
}

func newOTelTracer(tracer oteltrace.Tracer) Tracer {
	return &otelTracer{tracer: tracer}
}

func (tracer *otelTracer) Start(ctx context.Context, operation string, attrs ...TraceAttribute) (context.Context, Span, error) {
	if err := validateTraceInput(operation, attrs); err != nil {
		return ctx, nil, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	attributes := append(append([]TraceAttribute(nil), attrs...), correlationTraceAttributes(CorrelationFromContext(ctx))...)
	options := []oteltrace.SpanStartOption{oteltrace.WithAttributes(toOTelAttributes(attributes)...)}
	switch operation {
	case "http.request":
		options = append(options, oteltrace.WithSpanKind(oteltrace.SpanKindServer))
	case "workflow.node.consume":
		options = append(options, oteltrace.WithSpanKind(oteltrace.SpanKindConsumer))
	}
	child, span := tracer.tracer.Start(ctx, strings.TrimSpace(operation), options...)
	spanContext := span.SpanContext()
	correlation := CorrelationFromContext(child)
	correlation.TraceID = spanContext.TraceID().String()
	child = WithCorrelation(child, correlation)
	return child, &otelSpan{span: span}, nil
}

func (span *otelSpan) SetAttributes(attrs ...TraceAttribute) {
	if span == nil || span.span == nil {
		return
	}
	span.span.SetAttributes(toOTelAttributes(attrs)...)
}

func (span *otelSpan) RecordError(errorCode string) {
	if span == nil || span.span == nil {
		return
	}
	code := normalizeStableErrorCode(errorCode)
	span.span.SetAttributes(attribute.String("error.code", code))
	span.span.SetStatus(codes.Error, code)
}

func (span *otelSpan) End() {
	if span == nil || span.span == nil {
		return
	}
	span.once.Do(func() { span.span.End() })
}

func toOTelAttributes(attrs []TraceAttribute) []attribute.KeyValue {
	result := make([]attribute.KeyValue, 0, len(attrs))
	for _, attr := range attrs {
		if !traceAttributePattern.MatchString(attr.Key) {
			continue
		}
		result = append(result, attribute.String(attr.Key, redactString(attr.Key, attr.Value)))
	}
	return result
}

type telemetryIdentity struct {
	serviceName    string
	serviceVersion string
	environment    string
}

func newTelemetryIdentity(options TelemetryOptions, required bool) (telemetryIdentity, error) {
	identity := telemetryIdentity{
		serviceName:    strings.TrimSpace(options.ServiceName),
		serviceVersion: strings.TrimSpace(options.ServiceVersion),
		environment:    strings.TrimSpace(options.Environment),
	}
	if !required {
		if identity.serviceName == "" {
			identity.serviceName = "zhixu"
		}
		if identity.serviceVersion == "" {
			identity.serviceVersion = "unknown"
		}
		if identity.environment == "" {
			identity.environment = "unknown"
		}
	}
	for key, value := range map[string]string{
		"service.name": identity.serviceName, "service.version": identity.serviceVersion, "deployment.environment": identity.environment,
	} {
		if value == "" || len(value) > 128 || strings.ContainsAny(value, "\r\n\x00") || redactString(key, value) == RedactedValue {
			return telemetryIdentity{}, ErrTelemetryResource
		}
	}
	return identity, nil
}

func (identity telemetryIdentity) resource() *resource.Resource {
	return resource.NewSchemaless(
		attribute.String("service.name", identity.serviceName),
		attribute.String("service.version", identity.serviceVersion),
		attribute.String("deployment.environment", identity.environment),
	)
}
