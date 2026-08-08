# OpenTelemetry 与 Prometheus 可观测性实施计划

## 前置条件

- `08-08-viper-validator-config` 已完成或其 config/module/vendor/Compose 变更已稳定，可在当前分支安全集成。
- 开始实现前重新读取 `implement.jsonl` 注入的 specs/research，并运行 Trellis Phase 2 start；不得在本规划审批前启动。
- 记录实现起点的相关测试结果和 `git status --short`，不覆盖当前工作树中的用户改动。

## Step 1. 冻结基线与依赖

目标：在不改变行为的前提下确认现有合同和最终第三方版本。

1. 运行 observability、router、River worker、API/Worker composition 的局部基线测试。
2. 重新核对 OTel/Prometheus 最新稳定 module metadata、GoVersion 和 Apache-2.0 License。
3. 添加 `go.opentelemetry.io/otel`、`otel/sdk`、`otlptracehttp` 与 `github.com/prometheus/client_golang` 直接依赖；执行 tidy/vendor，检查未升级无关模块。
4. 先写 characterization/失败测试：mode 矩阵、真实 startup probe、`/metrics`、映射、传播、清理与并发 shutdown。

预期文件：`go.mod`、`go.sum`、`vendor/`、`internal/platform/observability/*_test.go`。

回滚点：仅依赖与红灯测试，产品行为尚未改变。

## Step 2. 实现 Prometheus Adapter

目标：用 custom Registry 替换生产 Noop/Memory Metrics，并保持项目验证为唯一入口。

1. 新增私有 Prometheus Adapter，在初始化时注册十个固定 collectors、Go collector 和 process collector。
2. 实现冻结映射、fixed label order、optional empty projection、counter/gauge/histogram 语义和毫秒转秒。
3. 使用 `Register` 返回稳定初始化错误；禁止 global registry、动态 collector 和 `MustRegister` panic。
4. 为 Telemetry 增加标准库 `MetricsHandler()` accessor，使用 custom registry 的 `promhttp.HandlerFor`。
5. 保留 `MemoryMetrics` 作为测试 spy，确保生产 InitializeTelemetry 永远返回 Prometheus-backed Metrics。

预期文件：`internal/platform/observability/metrics.go`、新增 `prometheus.go`/测试、`telemetry.go`、`internal/observability/facade.go`。

验收：十个指标 gather 精确匹配，invalid measurement 不改变 collector；三种 mode 均能 scrape。

## Step 3. 实现 OTel Trace Adapter 与启动门禁

目标：建立显式 SDK Provider、真实 OTLP/HTTP 导出、无 exporter 传播和安全生命周期。

1. 新增 OTel project Tracer/Span adapter；复用现有 input validation/redaction，错误只记录稳定 code。
2. 将 project TraceContext 与 OTel SpanContext 收敛为一个运行时事实源；保持现有 W3C 校验和只传播 `traceparent`。
3. disabled 创建无 exporter Provider；optional/required 创建 OTLP HTTP exporter + Resource + BatchSpanProcessor。
4. 包装 exporter 错误为稳定 sentinel；不设置任何 OTel 全局 Provider/ErrorHandler。
5. 通过 `telemetry.startup` span + 有界 `ForceFlush` 发起真实启动探针。
6. optional 失败逆序清理并创建无 exporter fallback；required 失败清理后返回稳定错误。
7. `Shutdown` 有界、幂等、并发安全，显式 flush 后 shutdown，折叠 raw errors。

预期文件：`internal/platform/observability/tracing.go`、新增 `otel.go`/测试、`telemetry.go`/测试、`internal/observability/facade.go`。

验收：fake Collector 收到可解析 protobuf；disabled 零网络；optional/required 语义、资源属性、Secret canary 和并发 shutdown 全覆盖。

## Step 4. 接入 API `/metrics` 与 HTTP Trace

目标：在 API 现有 listener 暴露抓取并保持业务 HTTP 合同。

1. `app.Dependencies` 注入标准库 Metrics handler，顶层注册 `GET /metrics`，保持在 `/api/v1` auth/CSRF 外。
2. request trace middleware 对 `/metrics`、`/livez`、`/readyz` 跳过 span，其他路由继续创建 `http.request`。
3. API composition 传入 `cfg.AppName+"-api"`、version/environment、Tracer 和 Metrics handler。
4. required 初始化错误继续在 listener 启动前返回；shutdown 等待 HTTP request spans 后关闭 telemetry。

预期文件：`internal/app/router.go`、`router_test.go`、`cmd/api/main.go`、相关 API tests。

验收：真实 scrape、无 auth、operational path 无 span、普通请求 parent/response header/OTLP 导出保持正确。

## Step 5. 接入 Worker ops `/metrics` 与 River consumer span

目标：复用 `:8081` 运维 listener，并让异步链路在 Collector 中可见。

1. 将 Worker 现有 health handler 包装为 ops mux，新增 `/metrics`，保留 `/livez`、`/readyz` 状态码与 body。
2. Worker composition 传入 `cfg.AppName+"-worker"` 资源身份及 Metrics handler。
3. `RuntimeWorkerObservability` 增加 project Tracer，Composition Root 与 Metrics 一同注入。
4. Runtime worker 在严格 metadata decode、有效 claim 和 correlation 后创建 `workflow.node.consume`；heartbeat/executor/transition 使用 traced context。
5. 以稳定 result/error code 收尾；stale delivery、事务错误、fatal invariant 和业务重试语义保持不变。
6. 保持 Worker 关闭顺序，确保 consumer spans 结束后才 flush Trace；shutdown metric 仍写入 Prometheus registry。

预期文件：`cmd/worker/main.go`/tests、`internal/workflow/adapter/river/runtime_observability.go`、`runtime_worker.go`/tests。

验收：ops 三端点共存；API producer 与 River consumer 具有同一 TraceID/正确 parent；shutdown/replay 行为不回归。

## Step 6. 文档、部署合同与质量门

目标：让代码、vendor、部署和操作语义一致且可回滚。

1. 更新 observability/deployment/technology-stack 文档：依赖、资源身份、三种模式、`/metrics` 网络边界、启动 probe、关闭顺序。
2. 文档明确默认轻量运行（logs + health + Metrics）、`/metrics` 的进程内快照/累计边界、Prometheus 历史能力，以及临时 optional Collector + Trace 后端的重启切换流程。
3. 保持 Compose 只传已有 endpoint/mode，不内置 Collector/Prometheus/Grafana/Jaeger/Tempo，不新增宿主端口。
4. 更新或补充 Trellis backend observability 稳定合同；只记录已由测试验证的事实。
5. 执行局部测试、race、vet、vendor build、Compose contract/smoke、Secret scan、`git diff --check`。
6. 按 `go-review` 做 Go 专项审查，再执行 `trellis-check` full-scope；修复范围内缺陷并重跑相关验证。

预期文件：`docs/architecture/observability.md`、`deployment.md`、`technology-stack.md`、必要的 `.trellis/spec/backend/*`。

## 测试与验证命令

优先拆成可在约 60 秒内完成的相关命令；单条超时 60 秒，累计明显超过 120 秒时记录未覆盖项而不盲目扩大。

```bash
go test -race ./internal/platform/observability -count=1 -timeout 60s
go test ./internal/observability ./internal/app ./internal/workflow/adapter/river ./cmd/api ./cmd/worker -count=1 -timeout 60s
go vet ./internal/platform/observability ./internal/observability ./internal/app ./internal/workflow/adapter/river ./cmd/api ./cmd/worker
go mod tidy -diff
go mod vendor
go build -mod=vendor ./cmd/api ./cmd/worker
go test ./internal/platform/config -run 'Compose|Telemetry' -count=1 -timeout 60s
python3 deploy/compose_runtime_contract.py
python3 deploy/compose_runtime_check.py
git diff --check
```

实现测试必须另行包含：

- fake OTLP Collector 200/拒绝/超时/Secret response；
- SDK span recorder root/child/resource/status；
- Prometheus gather + HTTP scrape + concurrent Record；
- API/Worker disabled/optional/required 启动矩阵；
- 默认 disabled 在无 Collector/Prometheus/Grafana/Trace 后端时仍启动，且零 OTLP 出站请求；
- partial-init cleanup 与 repeated/concurrent Shutdown；
- `traceparent`/River reserved metadata/高基数 labels/路径与 Secret canary。

## 完成定义

- PRD AC1-AC9 均有自动化证据或明确记录外部环境盲区。
- 生产路径不再使用 Memory/Noop Metrics；disabled 仍使用真实 Prometheus Registry。
- `optional|required` 的成功不是构造断言，而是真实 OTLP protobuf export/flush。
- SDK 类型未越过 platform/composition 边界；River/HTTP/数据库业务合同无变化。
- Go Review 和 Trellis check 无未处理的 P0/P1/P2 问题。
- module/vendor/docs/spec 同步，`git diff --check` 通过，回滚不需要数据迁移。
