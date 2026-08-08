# OpenTelemetry 与 Prometheus 可观测性

## Goal

使用成熟的 OpenTelemetry Go SDK、OTLP 和 `prometheus/client_golang` 补齐生产可观测性：API 与 Worker 能导出真实 Trace、暴露可抓取的 Prometheus Metrics，并继续遵守项目已经验证的脱敏、有限标签、W3C Trace 传播和显式 Telemetry 模式合同。

本任务替换生产 Adapter，不把 OpenTelemetry 或 Prometheus 类型扩散到 Domain/Application，也不删除项目拥有的安全与业务语义。

默认运行形态保持个人本地项目的轻量性：只启用结构化日志、`/livez`、`/readyz` 和 `/metrics`，不启动可观测后端，不向外部发送 Trace；只在复杂问题排查期间按需启用本地 Collector 和 Trace 后端。

## Background And Confirmed Facts

- `internal/observability` 是业务包使用的稳定 facade；具体实现由 `internal/platform/observability` 单一拥有，见 `docs/architecture/observability.md:40-42`。
- 当前 `ProviderFactory` 只是为真实 exporter 预留的 seam；API 与 Worker 均未注入 Factory，因此 `optional` 必然 degraded、`required` 必然启动失败，见 `internal/platform/observability/telemetry.go:34-60`、`cmd/api/main.go:688-693`、`cmd/worker/main.go:270-273`。
- 当前 Metrics registry 已固定 metric name、kind、允许/必填 labels，并拒绝未知、高基数和敏感值，见 `internal/platform/observability/metrics.go:56-66`、`:88-116`。
- 当前 API middleware 校验入站 `traceparent`、创建 `http.request` child/root span 并回写响应 `traceparent`，见 `internal/app/router.go:286-307`。
- River producer 只编码 `traceparent`；consumer 只接受该字段和 River 自有 `river:*` recovery metadata，拒绝 baggage、正文、Credential、路径和其他项目 metadata，见 `internal/workflow/adapter/river/inserter.go:151-165`、`internal/workflow/adapter/river/worker.go:166-213`。
- Worker 已有容器内独立 `:8081/livez|readyz` listener，默认不发布宿主端口；API 默认只发布到本机，见 `docs/architecture/deployment.md:62-75`。
- Docker build 使用 `-mod=vendor`，新增依赖必须同步 `go.mod`、`go.sum`、`vendor/`，见 `deploy/Dockerfile:13-16`。
- 当前默认配置为 `TelemetryModeDisabled`，`.env.example` 和 Compose 也默认 `ZHIXU_TELEMETRY_MODE=disabled`；Compose 不定义 Collector、Prometheus、Grafana、Jaeger 或 Tempo 服务，见 `internal/platform/config/config.go:340-344`、`.env.example:99-102`、`deploy/compose.yml:204-207`。
- 活跃任务 `08-08-viper-validator-config` 正在修改 `internal/platform/config`、`go.mod`、`go.sum`、`vendor/` 和 Compose；本任务实现必须在其变更稳定后开始，并基于其最终实例化 Viper loader 工作，不覆盖或回退现有改动。
- 历史架构评估已经冻结本任务选型：Trace 使用 OpenTelemetry Go SDK + OTLP，Metrics 使用 `prometheus/client_golang`；不再重新评估自研 Provider 或 OpenTelemetry Prometheus exporter 作为主方案。

## Requirements

### R1. Boundary And Ownership

- 继续由项目 facade 拥有 `Tracer`、`Span`、`Metrics`、`Measurement`、`Labels`、Correlation、脱敏和稳定错误语义。
- OpenTelemetry SDK、OTLP exporter、Prometheus collector/registry/handler 类型只能位于 `internal/platform/observability` 或 Composition Root。
- Domain、Application、Workflow contract 和持久化 schema 不得依赖具体 SDK 类型。
- 允许收窄或删除只服务于占位实现的 `ProviderFactory`/`ExternalExport` seam，但必须保持业务调用方接口清晰且测试可注入。

### R2. OpenTelemetry Trace And Propagation

- 生产 Trace 使用 OpenTelemetry Go SDK `TracerProvider` 和 OTLP/HTTP protobuf exporter；现有绝对 HTTP(S) endpoint 与 `4318` 语义保持兼容。
- 生产导出使用有界 BatchSpanProcessor；资源至少包含稳定、非敏感的 service name、service version 和 deployment environment。
- 入站 HTTP、响应头和 River 异步边界继续只传播经过校验的 W3C `traceparent`；不得新增 baggage、正文、Credential、路径或任意业务 metadata。
- `disabled` 或降级状态下仍必须生成有效 child/root SpanContext，使 API -> River 的 TraceID 连续性不依赖外部 exporter。
- 至少采集 API `http.request` span 和 River 公共消费边界 span，使异步链路不仅传播 context，而且在 Collector 中可观察父子关系。数据库、模型、Tool 和每个领域操作的自动埋点不在本任务强制范围。
- Span name、attribute key/value 和错误状态在进入 SDK 前执行项目现有校验与 fail-closed 脱敏；只记录稳定 error code，不把原始 error、endpoint、请求正文或路径写入 Trace。

### R3. Prometheus Metrics

- 生产 Metrics 使用 `prometheus/client_golang`，每个 API/Worker 进程拥有独立 custom Registry；禁止依赖全局 `prometheus.DefaultRegisterer` 或 `DefaultGatherer`。
- Prometheus Metrics 与 `/metrics` 在 `disabled|optional|required` 三种模式下始终启用；Telemetry mode 只控制 OTLP Trace 导出及其启动门禁。
- 现有项目 Metric registry 继续作为 name、kind、labels 和 value 校验的唯一事实源；Prometheus Adapter 不得绕过该校验。
- Counter 使用增量 `Add`，Gauge 使用绝对值 `Set`，Histogram 使用 `Observe`；`duration_ms` 对外转换为 Prometheus `_seconds` 单位。
- 对外 metric name 使用稳定的 `zhixu_` namespace 和 Prometheus 风格下划线名称；counter 保持 `_total` 后缀。
- Optional label 在固定 Vec label 维度中使用稳定空值投影，不允许动态 label name 或运行时创建 collector。
- API 与 Worker 分别暴露 `GET /metrics`：API 使用自身 HTTP listener，Worker 使用现有 `:8081` 运维 listener。端点不进入业务鉴权/CSRF，但必须依赖现有本机或容器网络边界，不新增公网发布。
- 直接请求 `/metrics` 只返回该进程在抓取时刻的 exposition：Gauge 是当前值，Counter/Histogram 是本次进程启动以来的累计值；不保存跨抓取、跨重启的历史趋势。历史查询/图表需由外部 Prometheus Server 定期抓取，Grafana 只是可选展示层。
- `/metrics`、`/livez`、`/readyz` 不应制造自观测 Trace/Metrics 递归或高频噪声。
- `MemoryMetrics` 如仍有必要，只能作为测试 spy 存在，不得进入生产初始化或被描述为生产采集。

### R4. Telemetry Modes

- 保留 `disabled|optional|required` 配置值、`ZHIXU_TELEMETRY_MODE` 和 `OTEL_EXPORTER_OTLP_ENDPOINT`，不增加静默默认或第二套 endpoint 事实源；这三种模式的状态仅表示 OTLP Trace 导出状态。
- `optional|required` 必须要求有效 endpoint；`disabled` 必须继续拒绝 endpoint，保持配置 fail-fast。
- exporter 构造成功不能单独证明可用。启动阶段必须使用有界、真实的 OTLP export/flush 探针验证 Collector 接受 Trace。
- `optional` 启动探针失败时关闭半初始化资源，返回可传播但不导出的 SDK Trace，并报告稳定 degraded 状态；API/Worker 仍可 ready。
- `required` 启动探针失败时返回稳定、脱敏错误；API/Worker 在监听或进入 ready 前失败。
- 运行期 Collector 短暂不可用不得改变业务事务结果或触发 Worker emergency shutdown；SDK 重试/丢弃通过稳定日志或有限内部指标观察。`required` 是启动门禁，不承诺 Collector 运行期故障立即终止进程。
- `disabled` 只禁用 OTLP exporter 和网络探针；本地 SDK TraceContext 创建/传播、Prometheus 记录和 `/metrics` 暴露仍正常工作。
- `disabled` 是默认运行模式：不启动可观测外部进程，不产生 OTLP 出站网络；本地指标记录仍有有界 CPU/内存成本，`/metrics` 只在被请求时产生 HTTP 抓取流量。

### R5. Startup, Shutdown And Resource Lifecycle

- API 与 Worker 每个进程只初始化一套 Trace Provider 和一套 Prometheus Registry，所有调用方共享同一实例。
- 初始化任一步失败必须关闭已创建资源；不得泄漏 exporter goroutine、HTTP transport 或重复注册 collector。
- Shutdown 必须有界、幂等且并发安全：停止接收新工作后结束业务 spans，随后 ForceFlush/Shutdown Trace Provider；同一底层资源最多关闭一次。
- Shutdown error 只向调用方返回稳定错误，不泄露 endpoint、Header、Credential 或 exporter 原始响应。
- Prometheus Registry/collectors 不伪造需要关闭的生命周期；HTTP server 仍由 API/Worker 现有 lifecycle 所有者关闭。

### R6. Security And Cardinality

- 日志、Trace、Metrics exposition、health/status 和测试失败输出都必须通过 Secret/路径 canary 扫描。
- `workspace_id`、`request_id`、`workflow_run_id`、`node_run_id`、`proposal_id`、`river_job_id`、TraceID/SpanID 等高基数值不得成为 metric labels。
- 未知 metric、错误 kind、缺失必填 label、动态 label、UUID、长十六进制、长数字 ID、Secret、Credential URL 和绝对路径继续 fail closed。
- OTLP endpoint 和认证 Header 不得出现在 Config string、状态响应、日志或稳定错误中。
- `/metrics` 只暴露聚合指标，不暴露 Job payload、Workspace identity、用户内容、路径、堆栈或原始 error。

### R7. Compatibility, Dependency And Deployment

- 先等待/整合 `08-08-viper-validator-config` 的最终 loader 与 manifest，保持现有配置优先级、disabled lookup gate 和 API/non-API profile 行为。
- 不改变现有 HTTP 路由、Problem Details、health response、River Job Args、River metadata JSON shape 或数据库 schema。
- Compose 继续仅配置外部 `OTEL_EXPORTER_OTLP_ENDPOINT`；本任务不强制内置 Collector、Prometheus、Grafana、Jaeger 或 Tempo 服务。
- 部署/可观测文档必须明确两种运行形态：默认 `disabled` 的 logs + health + Metrics，以及复杂排障时临时启动 Collector、设置 `optional|required` + endpoint 并重启 API/Worker 后获得 Trace。Collector 只负责接收/转发；图形化完整调用路径还需 Jaeger、Tempo 等 Trace 后端。
- 新依赖必须锁定与 Go `1.25.4` 兼容的稳定版本，核对 License，并同步 vendor；不得顺带升级无关依赖。
- 回滚单位为 observability Adapter、Composition wiring、`/metrics` routes 和新增依赖；无需数据库迁移或业务数据补偿。

### R8. Verification

- 使用 OTel SDK test exporter/span recorder 验证 root/child、入站 parent、River consumer parent、资源属性、错误状态、脱敏和 disabled propagation。
- 使用 `httptest` 假 OTLP/HTTP Collector 接收并解析真实 protobuf，覆盖成功、拒绝、超时和关闭前 flush；测试不得只断言 Factory 被调用。
- 使用 Prometheus `testutil` 或真实 HTTP scrape 验证 counter/gauge/histogram 映射、单位、labels、API/Worker `/metrics`、并发记录和拒绝路径。
- 覆盖三种模式的 API/Worker 启动矩阵、optional degraded、required fail-fast、部分初始化清理和 concurrent/repeated Shutdown。
- 保留并扩展现有 `traceparent`、River reserved metadata、Secret canary、bounded label 和业务 replay 不重复发射测试。
- 验证 `go test -race` 相关包、`go vet`、vendor build、Compose contract/smoke、`git diff --check`，并执行 Go 专项 Review 与 Trellis full-scope check。

## Acceptance Criteria

- [ ] AC1: API/Worker 在 `optional|required` 成功路径通过 OTLP/HTTP 向测试 Collector 发送可解析 Trace；资源包含正确 process service identity，Collector 中 API 与 River consumer span 保持同一 TraceID 和父子关系。
- [ ] AC2: 默认 `disabled` 不构造 OTLP exporter、不访问 endpoint、不要求任何外部可观测服务，但结构化日志、API/Worker health、API root/child context、River `traceparent` 传播和 API/Worker `/metrics` 仍有效。
- [ ] AC3: `optional` Collector 不可用时进程可启动并明确 degraded；`required` 在监听/ready 前稳定失败，所有错误和日志不泄露 endpoint 或测试 Secret。
- [ ] AC4: API 与 Worker 的 `GET /metrics` 返回 Prometheus exposition；现有 queue/worker/workflow/retry/recovery/shutdown 指标按正确 counter/gauge/histogram 语义和单位采集，文档明确其为进程内当前/启动后累计状态而非持久化历史。
- [ ] AC5: 未知、高基数、敏感或不完整 labels 在到达 Prometheus collector 前被拒绝，抓取结果中不存在 correlation ID、TraceID、Workspace ID、Credential、正文或绝对路径。
- [ ] AC6: API/Worker 重复或并发 Shutdown 只关闭并 flush Trace Provider 一次；启动失败和 optional fallback 不泄漏半初始化资源。
- [ ] AC7: API HTTP 行为、health 契约、River Job Args/metadata、业务事务结果和既有 Metrics replay 语义保持兼容。
- [ ] AC8: 新依赖、`go.mod`、`go.sum`、`vendor/`、Compose/部署/可观测性文档一致，vendor Docker build 可复现。
- [ ] AC9: 相关单元、HTTP、OTLP collection、race、vet、Compose contract/smoke、Secret scan、Go Review 和 Trellis check 全部通过；未执行的外部 Collector/部署验证明确记录盲区。

## Out Of Scope

- 部署或配置完整的 OpenTelemetry Collector、Prometheus Server、Grafana、Jaeger、Tempo、告警规则和 Dashboard。
- 在应用内持久化 Metrics 历史、提供趋势查询/图表，或为 Trace 内建存储和 UI。
- 将 logs 或 Audit 改为 OTLP 导出，或改变 append-only Audit 数据模型。
- 全量接入 `otelhttp`、pgx、River、模型、Tool 等所有自动 instrumentation；本任务只保证 API 与公共 River consumer 的最小可观察异步链路。
- 增加 baggage、tracestate 持久化、自定义业务 metadata 传播或跨 Workspace metric labels。
- 修改业务领域状态机、数据库 schema、认证授权或对公网发布新的运维端口。
