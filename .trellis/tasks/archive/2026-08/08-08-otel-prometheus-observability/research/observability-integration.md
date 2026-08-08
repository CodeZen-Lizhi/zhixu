# OpenTelemetry 与 Prometheus 集成研究

## 研究范围

- 日期：2026-08-08
- 目标：验证现有可观测性合同、OpenTelemetry Go SDK/OTLP 启停语义、`prometheus/client_golang` 注册与暴露方式，以及本任务的依赖和影响面。
- 结论：保留项目 facade 与安全策略，生产 Trace Adapter 使用 OpenTelemetry Go SDK + OTLP/HTTP，生产 Metrics Adapter 使用 `prometheus/client_golang` custom Registry。`disabled|optional|required` 只控制 OTLP Trace；Prometheus 始终启用。

## 仓库事实

### 当前边界

- `internal/observability/facade.go` 对外重导出项目自有 `Tracer`、`Span`、`Metrics`、`Telemetry`、安全错误和构造函数；业务包不需要直接引用第三方 SDK。
- `internal/platform/observability/telemetry.go:34-62` 的 `ProviderFactory`/`Provider` 是为外部实现预留的占位 seam。API 与 Worker 初始化没有注入 Factory，因此当前没有生产 exporter。
- `internal/platform/observability/metrics.go:56-67` 已固定十个项目 metric 的 kind、允许 labels 和必填 labels；`:89-116` 在 exporter 前拒绝敏感值、UUID、长十六进制和长数字 ID。
- `internal/platform/observability/tracing.go:15-117` 只接受 W3C `traceparent`；`TraceContext` 是项目允许跨异步边界持久传播的唯一子集。
- `internal/platform/observability/tracing.go:258-315` 在 span 写入前校验 operation/attribute key，并通过项目 redaction 清洗 attribute value。
- `internal/app/router.go:286-307` 已实现 HTTP 入站 parent、`http.request` span 和响应 `traceparent`；`/livez`、`/readyz` 与业务路由共用全局 middleware。
- `internal/workflow/adapter/river/runtime_worker.go:123-147` 在 River consumer 先解码 `traceparent`，领取成功后补齐 correlation，但当前未创建消费/执行 span。
- `cmd/worker/main.go:653-687` 先停止业务与运维 HTTP server，再记录 shutdown metric，最后关闭 telemetry。实现时需要保留业务关闭顺序，同时验证最终 metric 已写入 registry，不能因为 exporter 生命周期变化丢失 Trace flush。

### 配置、部署与并行改动

- `internal/platform/config/config.go:174-177` 已有 `AppName`、`Version`、`Environment`，可直接形成 OTel Resource，不需要新增配置项。
- `internal/platform/config/config.go:340-344`、`.env.example:99-102` 和 `deploy/compose.yml:204-207` 都以 `disabled` 为默认。当前 Compose 不定义 Collector、Prometheus、Grafana、Jaeger 或 Tempo，因此轻量默认形态与仓库现状一致。
- `internal/platform/config/config.go:767-785` 已验证 telemetry mode 和绝对 HTTP(S) endpoint；disabled 禁止 endpoint，optional/required 要求 endpoint。
- API 使用自身 HTTP listener；Worker 已在 `WorkerHealthAddr`（默认 `:8081`）提供运维 listener。`/metrics` 应复用这两个已有 listener，不新增端口。
- `deploy/Dockerfile` 使用 `-mod=vendor`，新增模块后必须同步 `go.mod`、`go.sum`、`vendor/` 和 `vendor/modules.txt`。
- 活跃任务 `08-08-viper-validator-config` 正修改 config、module/vendor 和 Compose。本任务只能在该任务稳定后集成其最终 loader，不能覆盖当前工作树中的用户改动。

## OpenTelemetry 官方行为

资料：

- [OTLP Trace HTTP exporter](https://pkg.go.dev/go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp)
- [OpenTelemetry Trace SDK](https://pkg.go.dev/go.opentelemetry.io/otel/sdk/trace)
- [W3C Trace Context propagator](https://pkg.go.dev/go.opentelemetry.io/otel/propagation)

已验证结论：

1. `otlptracehttp.New` 支持 `WithEndpointURL`、`WithTimeout`、`WithHeaders` 和 `WithRetry`。本任务只使用已验证的 endpoint、超时和有界重试，不新增 Header 配置。
2. OTLP HTTP client 的 `Start` 不发网络请求。仅成功构造 exporter 不能证明 Collector 可用。
3. `sdktrace.BatchSpanProcessor.ForceFlush` 会等待队列处理，并向调用方返回 export 失败。因此可结束一个固定、安全的 `telemetry.startup` span，再用有界 `ForceFlush` 作为启动可用性探针。
4. `TracerProvider.Shutdown` 负责停止 span processors/exporter；业务关闭仍应先结束活动 span，再执行有界 `ForceFlush`/`Shutdown`，并由项目 `sync.Once` 保证底层只关闭一次。
5. SDK 可在无 exporter 的 Provider 中继续生成有效 SpanContext。disabled 与 optional fallback 可使用 parent-based sampler + 无 SpanProcessor Provider，保留 root/child 和 River 传播而不发网络。
6. OTel 默认错误处理是进程级行为。项目不应设置全局 `otel.SetErrorHandler` 或全局 Provider；在 exporter 边界用包装器把底层错误折叠为稳定 sentinel，防止 endpoint、响应正文或 transport 错误进入日志。
7. 项目现有 `traceparent` 校验比通用 propagator 边界更窄，且 River metadata 明确拒绝其他字段。因此 SDK SpanContext 应成为运行时上下文中的权威值，但编码/解码仍由项目函数完成，只投影 `traceparent`，不自动引入 `tracestate` 或 baggage。

## Prometheus 官方行为

资料：

- [Prometheus Go client](https://pkg.go.dev/github.com/prometheus/client_golang/prometheus)
- [promhttp](https://pkg.go.dev/github.com/prometheus/client_golang/prometheus/promhttp)
- [Prometheus testutil](https://pkg.go.dev/github.com/prometheus/client_golang/prometheus/testutil)

已验证结论：

1. `prometheus.NewRegistry` 提供进程内隔离 registry；使用 `Register` 可返回 duplicate/descriptor 错误，初始化阶段不应使用会 panic 的 `MustRegister`。
2. `promhttp.HandlerFor(registry, opts)` 可将指定 custom Registry 暴露为 HTTP handler，不需要读写 `DefaultRegisterer`/`DefaultGatherer`。
3. `CounterVec`、`GaugeVec`、`HistogramVec` 的 label names 在 collector 创建时固定。项目 optional label 可投影为固定空字符串，但不能动态增加 label name 或 collector。
4. Histogram 的 Prometheus 基础单位应为 seconds；现有 `workflow.node.duration_ms` 在 Adapter 边界除以 1000，项目 Measurement 合同保持不变。
5. 可显式向 custom Registry 注册 Go runtime 与 process collectors，使没有业务 metric 写入的 API `/metrics` 也能证明抓取链路工作；这些 collectors 不引入项目 correlation labels。
6. `/metrics` 是当前 registry 在抓取时的 exposition：Gauge 表示当前值，Counter/Histogram 表示本进程启动后累计。client_golang 不替代 Prometheus Server 的定期抓取、持久化和历史查询。

## 版本与许可证快照

规划时通过官方 Go module metadata 核对：

| 模块 | 候选版本 | GoVersion | License |
| --- | --- | --- | --- |
| `go.opentelemetry.io/otel` / `sdk` / `otlptracehttp` | `v1.45.0` | `1.25.0` | Apache-2.0 |
| `github.com/prometheus/client_golang` | `v1.24.1` | `1.25.0` | Apache-2.0 |

项目当前使用 Go `1.25.4`，满足候选版本声明。实施时必须重新执行 `go list -m -json`/license 核对并只添加直接需要的模块；若稳定版本发生变化，保持同一 minor 集合且记录最终选择，不顺带升级无关依赖。

## 建议的指标映射

| 项目 Metric | Prometheus Metric | 类型 | 固定 labels | 转换 |
| --- | --- | --- | --- | --- |
| `river.queue.depth` | `zhixu_river_queue_depth` | Gauge | `queue` | `Set(value)` |
| `river.workers.active` | `zhixu_river_workers_active` | Gauge | `queue` | `Set(value)` |
| `workflow.node.duration_ms` | `zhixu_workflow_node_duration_seconds` | Histogram | `node_kind,result` | `Observe(value/1000)` |
| `workflow.node.result_total` | `zhixu_workflow_node_result_total` | Counter | `node_kind,result,error_code` | `Add(value)` |
| `workflow.retry_total` | `zhixu_workflow_retry_total` | Counter | `node_kind,error_code` | `Add(value)` |
| `workflow.manual_recovery_total` | `zhixu_workflow_manual_recovery_total` | Counter | `node_kind,error_code` | `Add(value)` |
| `workflow.lease_expiry_total` | `zhixu_workflow_lease_expiry_total` | Counter | `node_kind` | `Add(value)` |
| `workflow.heartbeat_failure_total` | `zhixu_workflow_heartbeat_failure_total` | Counter | `node_kind,error_code` | `Add(value)` |
| `river.duplicate_delivery_total` | `zhixu_river_duplicate_delivery_total` | Counter | `node_kind` | `Add(value)` |
| `worker.shutdown_total` | `zhixu_worker_shutdown_total` | Counter | `shutdown_kind,result` | `Add(value)` |

`error_code` 是允许但非必填 label；对应 Vec 始终包含该维度，缺省投影为空字符串。Histogram 建议从 10ms 开始使用 2 倍指数 buckets，覆盖短任务到约 22 分钟，并保留 `+Inf`，可覆盖当前 15 分钟级 Worker job timeout。

## 影响面

预期产品代码修改集中于：

- `internal/platform/observability`: OTel Adapter、Prometheus Adapter、Telemetry 生命周期和现有合同测试。
- `internal/observability/facade.go`: 仅重导出新增的项目级 handler/accessor，避免 SDK 类型泄漏。
- `internal/app/router.go`: 注入并注册 `/metrics`，运维路径跳过 request span。
- `internal/workflow/adapter/river/runtime_observability.go`、`runtime_worker.go`: 注入项目 Tracer，创建公共 consumer/execution span。
- `cmd/api/main.go`、`cmd/worker/main.go`: 传入 resource identity、挂载 handler、保持 shutdown 顺序。
- `go.mod`、`go.sum`、`vendor/`: 新依赖及可复现 vendor。
- `docs/architecture/observability.md`、`docs/architecture/deployment.md`、`docs/architecture/technology-stack.md`: 冻结模式、端点、抓取和依赖合同。

不涉及数据库、迁移、Domain 状态机、River Job Args 或 metadata schema。

## 风险与缓解

- 启动探针假阳性：禁止把 exporter 构造视为成功，必须通过真实 protobuf 请求 + `ForceFlush`。
- 全局状态污染：禁止使用 OTel global Provider 和 Prometheus default registry；测试并行时每例使用隔离实例。
- 错误泄密：所有 exporter 错误在 Adapter 边界折叠为稳定 code，fake Collector 返回包含 Secret 的响应以做 canary 测试。
- 高基数：继续先调用 `Measurement.Validate`/项目 redaction，再进入 Vec/SDK；不增加动态 label。
- Worker 关闭丢数据：先停止领取与等待业务 span 结束，再 flush/shutdown Trace；Metrics registry 无关闭语义。
- 并行任务冲突：实现前确认 Viper 任务完成或已稳定合并，再处理 module/vendor/config composition。
- 能力边界误解：文档区分直接 `/metrics`、Prometheus 历史、Collector 接收/转发和 Jaeger/Tempo 存储/UI，避免将单个进程端点描述为完整监控平台。
