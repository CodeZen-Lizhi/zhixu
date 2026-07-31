// Package observability 暴露项目级日志、指标、Trace 与 Telemetry 稳定入口。
//
// 实现仍由 internal/platform/observability 单一事实源持有；本包只提供兼容 facade，
// 避免业务模块依赖 platform 目录布局或复制安全脱敏规则。
package observability
