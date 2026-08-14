package models

import (
	platformobservability "github.com/CodeZen-Lizhi/zhixu/internal/platform/observability"
)

// ModelTelemetry 保存模型 Adapter 使用的项目 Tracer 与 Metrics seam。
// 它不携带 Endpoint、Credential、请求正文或 Provider SDK 类型。
type ModelTelemetry struct {
	tracer  platformobservability.Tracer
	metrics platformobservability.Metrics
}

// NewModelTelemetry 创建模型 Adapter 的可观测性依赖；nil 依赖会显式降级为 noop。
func NewModelTelemetry(tracer platformobservability.Tracer, metrics platformobservability.Metrics) ModelTelemetry {
	if tracer == nil {
		tracer = platformobservability.NewNoopTracer()
	}
	if metrics == nil {
		metrics = platformobservability.NewNoopMetrics()
	}
	return ModelTelemetry{tracer: tracer, metrics: metrics}
}

func resolveModelTelemetry(options []ModelTelemetry) ModelTelemetry {
	if len(options) == 0 {
		return NewModelTelemetry(nil, nil)
	}
	selected := options[len(options)-1]
	return NewModelTelemetry(selected.tracer, selected.metrics)
}

// String 返回不展开具体 exporter 或内部状态的安全摘要。
func (ModelTelemetry) String() string { return "ModelTelemetry{configured}" }

// GoString 避免 `%#v` 展开具体 telemetry Adapter。
func (telemetry ModelTelemetry) GoString() string { return telemetry.String() }
