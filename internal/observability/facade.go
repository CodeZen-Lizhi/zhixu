package observability

import (
	"context"
	"io"
	"log/slog"

	platform "github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
)

const (
	// RedactedValue 是所有敏感值统一使用的替代文本。
	RedactedValue = platform.RedactedValue
	// TraceParentMetadataKey 是异步传播允许的唯一项目 Trace 字段。
	TraceParentMetadataKey = platform.TraceParentMetadataKey

	// TelemetryStatusDisabled 表示外部 OTLP Trace 导出已关闭。
	TelemetryStatusDisabled = platform.TelemetryStatusDisabled
	// TelemetryStatusExporting 表示真实外部 Provider 正在导出。
	TelemetryStatusExporting = platform.TelemetryStatusExporting
	// TelemetryStatusExporterUnavailable 表示 optional 模式已显式降级。
	TelemetryStatusExporterUnavailable = platform.TelemetryStatusExporterUnavailable

	// TelemetryModeDisabled 禁止构造 exporter。
	TelemetryModeDisabled = platform.TelemetryModeDisabled
	// TelemetryModeOptional 允许 exporter 失败后显式降级。
	TelemetryModeOptional = platform.TelemetryModeOptional
	// TelemetryModeRequired 要求 exporter 初始化成功。
	TelemetryModeRequired = platform.TelemetryModeRequired

	// MetricKindCounter 表示累计计数。
	MetricKindCounter = platform.MetricKindCounter
	// MetricKindGauge 表示瞬时绝对值。
	MetricKindGauge = platform.MetricKindGauge
	// MetricKindHistogram 表示分布观测值。
	MetricKindHistogram = platform.MetricKindHistogram

	// MetricQueueDepth 是可领取 River Job 数量。
	MetricQueueDepth = platform.MetricQueueDepth
	// MetricActiveWorkers 是当前进程活跃 Worker 数量。
	MetricActiveWorkers = platform.MetricActiveWorkers
	// MetricNodeDuration 是节点执行耗时。
	MetricNodeDuration = platform.MetricNodeDuration
	// MetricNodeResultTotal 是节点结果计数。
	MetricNodeResultTotal = platform.MetricNodeResultTotal
	// MetricRetryTotal 是重试计数。
	MetricRetryTotal = platform.MetricRetryTotal
	// MetricManualRecoveryTotal 是人工恢复计数。
	MetricManualRecoveryTotal = platform.MetricManualRecoveryTotal
	// MetricLeaseExpiryTotal 是租约过期计数。
	MetricLeaseExpiryTotal = platform.MetricLeaseExpiryTotal
	// MetricHeartbeatFailureTotal 是心跳失败计数。
	MetricHeartbeatFailureTotal = platform.MetricHeartbeatFailureTotal
	// MetricDuplicateDeliveryTotal 是重复投递计数。
	MetricDuplicateDeliveryTotal = platform.MetricDuplicateDeliveryTotal
	// MetricShutdownTotal 是 Worker 关闭结果计数。
	MetricShutdownTotal = platform.MetricShutdownTotal
)

var (
	// ErrUnknownMetric 表示指标未注册。
	ErrUnknownMetric = platform.ErrUnknownMetric
	// ErrInvalidMetric 表示指标定义或值非法。
	ErrInvalidMetric = platform.ErrInvalidMetric
	// ErrUnboundedMetricLabel 表示 label 可能产生高基数。
	ErrUnboundedMetricLabel = platform.ErrUnboundedMetricLabel
	// ErrSensitiveMetricLabel 表示 label 含敏感信息。
	ErrSensitiveMetricLabel = platform.ErrSensitiveMetricLabel
	// ErrObservabilityClosed 表示 Provider 已关闭。
	ErrObservabilityClosed = platform.ErrObservabilityClosed

	// ErrInvalidTelemetryMode 表示 Telemetry 模式非法。
	ErrInvalidTelemetryMode = platform.ErrInvalidTelemetryMode
	// ErrTelemetryEndpointRequired 表示当前模式要求 endpoint。
	ErrTelemetryEndpointRequired = platform.ErrTelemetryEndpointRequired
	// ErrTelemetryEndpointForbidden 表示 disabled 模式禁止 endpoint。
	ErrTelemetryEndpointForbidden = platform.ErrTelemetryEndpointForbidden
	// ErrTelemetryExporterRequired 表示 required exporter 不可用。
	ErrTelemetryExporterRequired = platform.ErrTelemetryExporterRequired
	// ErrTelemetryMetrics 表示 Prometheus Registry 初始化失败。
	ErrTelemetryMetrics = platform.ErrTelemetryMetrics
	// ErrTelemetryResource 表示 OTel Resource 身份非法。
	ErrTelemetryResource = platform.ErrTelemetryResource
	// ErrTelemetryShutdown 表示 Provider 关闭失败。
	ErrTelemetryShutdown = platform.ErrTelemetryShutdown

	// ErrInvalidTraceContext 表示 W3C Trace Context 非法。
	ErrInvalidTraceContext = platform.ErrInvalidTraceContext
)

type (
	// Correlation 是日志与 Trace 共享的关联字段。
	Correlation = platform.Correlation
	// Logger 是应用层依赖的窄结构化日志接口。
	Logger = platform.Logger
	// Labels 是经过有界校验的 Metrics labels。
	Labels = platform.Labels
	// Measurement 是单次项目指标记录。
	Measurement = platform.Measurement
	// MetricKind 描述指标聚合语义。
	MetricKind = platform.MetricKind
	// MetricName 是项目注册的稳定指标名。
	MetricName = platform.MetricName
	// Metrics 是项目自有指标记录接口。
	Metrics = platform.Metrics
	// MemoryMetrics 是测试用内存 Adapter。
	MemoryMetrics = platform.MemoryMetrics
	// TelemetryOptions 定义显式 exporter 初始化配置。
	TelemetryOptions = platform.TelemetryOptions
	// TelemetryStatus 是不暴露 endpoint 的安全状态。
	TelemetryStatus = platform.TelemetryStatus
	// TelemetryMode 定义 disabled/optional/required 语义。
	TelemetryMode = platform.TelemetryMode
	// Telemetry 持有初始化后的项目接口。
	Telemetry = platform.Telemetry
	// TraceContext 是项目允许持久传播的 W3C Trace 子集。
	TraceContext = platform.TraceContext
	// TraceAttribute 是已清洗的 Span 属性。
	TraceAttribute = platform.TraceAttribute
	// Span 是项目自有 Trace Span 接口。
	Span = platform.Span
	// SpanSnapshot 是已结束内存 Span 的不可变快照。
	SpanSnapshot = platform.SpanSnapshot
	// Tracer 是项目自有 Trace 创建接口。
	Tracer = platform.Tracer
	// MemoryTracer 是测试用内存 Adapter。
	MemoryTracer = platform.MemoryTracer
)

// NewLogger 创建统一 JSON slog logger，并在写出前执行 fail-closed 脱敏。
func NewLogger(level string, output io.Writer) *slog.Logger { return platform.NewLogger(level, output) }

// WithCorrelation 合并并写入 Context correlation。
func WithCorrelation(ctx context.Context, next Correlation) context.Context {
	return platform.WithCorrelation(ctx, next)
}

// CorrelationFromContext 读取当前 correlation。
func CorrelationFromContext(ctx context.Context) Correlation {
	return platform.CorrelationFromContext(ctx)
}

// NewLabels 校验并创建有界 Metrics labels。
func NewLabels(values map[string]string) (Labels, error) { return platform.NewLabels(values) }

// NewMeasurement 创建并校验单次指标记录。
func NewMeasurement(name MetricName, kind MetricKind, value float64, labels Labels) (Measurement, error) {
	return platform.NewMeasurement(name, kind, value, labels)
}

// MetricNames 返回稳定指标注册表。
func MetricNames() []MetricName { return platform.MetricNames() }

// NewNoopMetrics 创建仍执行合同校验的 noop Metrics。
func NewNoopMetrics() Metrics { return platform.NewNoopMetrics() }

// NewMemoryMetrics 创建隔离的内存 Metrics Adapter。
func NewMemoryMetrics() *MemoryMetrics { return platform.NewMemoryMetrics() }

// InitializeTelemetry 按 disabled/optional/required 语义初始化 Telemetry。
func InitializeTelemetry(ctx context.Context, options TelemetryOptions) (*Telemetry, error) {
	return platform.InitializeTelemetry(ctx, options)
}

// WithTraceContext 校验并写入 Trace Context。
func WithTraceContext(ctx context.Context, trace TraceContext) (context.Context, error) {
	return platform.WithTraceContext(ctx, trace)
}

// TraceContextFromContext 读取有效 Trace Context。
func TraceContextFromContext(ctx context.Context) (TraceContext, bool) {
	return platform.TraceContextFromContext(ctx)
}

// EncodeTraceMetadata 返回异步边界允许的唯一 Trace metadata。
func EncodeTraceMetadata(ctx context.Context) map[string]string {
	return platform.EncodeTraceMetadata(ctx)
}

// DecodeTraceMetadata 严格解码异步 Trace metadata。
func DecodeTraceMetadata(ctx context.Context, metadata map[string]string) (context.Context, error) {
	return platform.DecodeTraceMetadata(ctx, metadata)
}

// NewNoopTracer 创建保留传播能力的 noop Tracer。
func NewNoopTracer() Tracer { return platform.NewNoopTracer() }

// NewMemoryTracer 创建隔离的内存 Tracer Adapter。
func NewMemoryTracer() *MemoryTracer { return platform.NewMemoryTracer() }

// RedactString 对文本执行统一 fail-closed 脱敏。
func RedactString(key, value string) string { return platform.RedactString(key, value) }

// RedactValue 对日志或 Trace 值执行递归 fail-closed 脱敏。
func RedactValue(key string, value any) any { return platform.RedactValue(key, value) }

// IsSensitiveKey 判断字段名是否属于凭据或受限正文边界。
func IsSensitiveKey(key string) bool { return platform.IsSensitiveKey(key) }
