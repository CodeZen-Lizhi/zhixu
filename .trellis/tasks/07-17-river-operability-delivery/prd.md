# M4-D River Operability And Delivery

## Goal

在 M4-A/B/C 已完成真实 River Runtime、Workflow 状态机和 Safe Writeback Dispatch 后，把 Worker 建设为可配置、可观测、可健康检查、可优雅停机、可通过 Docker Compose 部署和故障恢复的交付单元，并完成文档与全量质量门禁。

## Background

- M4-C 已把真实 River Client、Registry/Runtime 与 Safe Writeback Executor 接入 `cmd/worker`；当前仍只有数据库 Ping ticker，Compose 没有 Worker healthcheck，启动 Context 直接绑定 OS signal，也无法证明 River schema、Registry 或 Executor 已就绪。
- River v0.40.0 PoC 证明 `Start(ctx)` 启动后台循环后返回；`Stop` 等待任务完成，`StopAndCancel` 传播 Context cancel，`SoftStopTimeout` 必须显式配置以避免无限等待。
- API `/readyz` 不能代表 Worker ready；Worker 需要独立 health server。
- 产品最终还需要完整 M10 可观测性与安全体系；本任务只交付 Workflow Runtime 所需的 correlation、指标和 trace seam，不实现全产品审计 UI 或告警平台。

## Requirements

### R1. Worker Composition And Lifecycle

- `cmd/worker` 必须构造并启动 River Client、Definition/Executor Registry、Workflow Runtime、Safe Writeback Executor、heartbeat supervisor 和独立 health server。
- `Client.Start(ctx)` 使用独立的进程生命周期 Context 直接调用，不额外包 goroutine；该 Context 不由 `signal.NotifyContext` 在收到停机信号时自动取消。启动任一必需组件失败时进程非零退出。
- 收到 SIGTERM/SIGINT 时由唯一 lifecycle 状态机选择并只调用一次 `Stop(shutdownCtx)`：River v0.40.0 根据 `SoftStopTimeout` 自动从 soft stop 升级为取消 Worker Context，不再随后重复调用 `StopAndCancel`。`StopAndCancel` 只允许由 Runtime/Supervisor 报告“继续执行可能扩大副作用”的稳定 fatal invariant 事件触发，且仅在 lifecycle 仍为 running、尚未进入 graceful stop 时可被首次选择；测试可注入该事件。两条路径通过一次性状态转换互斥，后续 signal/fatal 事件只记录不再调用另一 API；不得同时通过取消 Start Context触发第二条停机路径。
- Worker 若忽略取消且超过进程 hard shutdown deadline，记录稳定错误后非零退出；新实例依靠 River rescue、Workflow lease 和领域 checkpoint 恢复，不声称该次停机优雅完成。
- 对已开始外部副作用的写权限 Node，进程停机或用户 Cancel 只有在领域 Execution 已持久化 cancelled/compensated/manual recovery 事实后，才能把 Workflow 归约为 terminal cancelled；否则必须保持可恢复 Job 或进入 Manual Recovery，禁止留下 `Proposal applying/Execution prepared` 而 Run 已 cancelled 的孤儿状态。
- 不再输出 `workflow_dispatcher=not_configured`，也不以 DB Ping 冒充业务运行时。

### R2. Configuration

- 使用现有配置模块和环境变量提供 queue、MaxWorkers、JobTimeout、River RescueStuckJobsAfter、Workflow lease、heartbeat interval、SoftStopTimeout、HardStopTimeout、health address 和 telemetry 开关。
- 校验 `heartbeat < lease/3`、`JobTimeout < RescueStuckJobsAfter`、`SoftStopTimeout < HardStopTimeout`、MaxWorkers/timeout 为正值；非法配置 fail-fast，不静默回退。
- `.env.example`、Compose 和部署文档提供安全默认值，不硬编码生产密钥或环境地址。

### R3. Worker Health

- Worker 暴露独立 `:8081/livez` 和 `/readyz`；默认只在容器网络内使用。
- `/livez` 仅证明进程和 health server 存活。
- `/readyz` 必须校验 DB、River migration Validate、River Client started、Definition Registry frozen、Executor Registry frozen，以及所有启用 Definition 的依赖已注入。
- 任何必需项失败返回非 2xx 和稳定无敏感信息的原因；Compose healthcheck 必须实际访问 Worker `/readyz`。

### R4. Observability

- 结构化日志统一关联 `workspace_id/workflow_run_id/node_run_id/attempt_no/dispatch_no/retry_no/river_job_id/proposal_id` 中适用字段。
- 指标至少包含 queue depth、active workers、node duration、success/failure/retry/manual recovery、lease expiry、heartbeat failure、duplicate delivery、graceful/forced shutdown。
- Trace 保持 API Approval → River Job → Workflow Node → Safe Writeback Saga 的上下文连续性；River metadata 仅保存脱敏 trace propagation，不保存正文或 Credential。
- OpenTelemetry 启用/禁用必须是显式配置；Exporter 不可用时 readiness 是否失败由配置的 required/optional 模式决定，不能伪造已上报。

### R5. Compose And Docker

- Dockerfile 构建 API、Worker 和 Migrate 三个二进制，runtime 继续非 root。
- Compose migrate service 使用项目 `zhixu-migrate`，完成项目 Goose Up → River Up → Validate；API/Worker 依赖 migrate 成功。
- Worker 使用独立 healthcheck；`docker compose up --wait` 必须等待 PostgreSQL、migrate、API 和 Worker 均 ready。
- 容器 SIGTERM、Worker kill -9、数据库短暂断连和重启均不得重复领域副作用或丢失恢复证据。

### R6. Security And Operations

- 日志、metrics、trace、health、River metadata 不得包含 Credential、正文、绝对路径、Git stderr、lock token 或数据库密码。
- Health server 不提供控制能力、Job payload 或堆栈；部署默认不发布到宿主公网端口。
- 迁移必须单实例运行；生产 Down 非默认操作，回滚优先旧应用兼容新 Schema。
- 提供 Worker 停机、卡住 Job、lease 过期、migration Validate 失败和 Safe Writeback manual recovery 的 runbook。

### R7. Documentation And Quality Gate

- 同步 README、`.env.example`、Workflow、Observability、Deployment、Recovery、Testing、数据库和 ADR 文档。
- 所有门禁必须验证真实 River/Worker 行为，不能只做 config parse 或容器启动。

## Acceptance Criteria

- [ ] Worker Composition 启动真实 River Client/Registry/Runtime，启动失败非零退出，日志不再包含 `workflow_dispatcher=not_configured`。
- [ ] 配置正常、边界和非法组合测试通过，非法值不会静默使用默认值。
- [ ] `/livez` 与 `/readyz` 语义分离；DB、River migration、Registry 或依赖失败会使 ready=false。
- [ ] Compose Worker healthcheck 实际访问容器内 `:8081/readyz`，`up --wait` 同时证明 API 和 Worker ready。
- [ ] 正常 Stop 等待安全 checkpoint并由 SoftStopTimeout 自动取消；紧急 StopAndCancel 是互斥路径。忽略取消超过 hard deadline 时进程非零退出并可由 lease/checkpoint 恢复。
- [ ] Safe Writeback 在 Atomic Begin 或任一副作用 checkpoint 后收到 Cancel/forced cancel 时，不会形成 terminal Workflow + 非终态 Execution 的孤儿组合；必须恢复完成、补偿或明确进入 Manual Recovery。
- [ ] 两 Worker、duplicate、kill -9、数据库短断和重启 smoke 不重复 Execution、Commit、Mapping 或 Outbox。
- [ ] 日志/metrics/trace correlation 完整，Secret 扫描无泄漏；Telemetry disabled/optional/required 三种模式行为明确。
- [ ] Docker 镜像仍以非 root 运行，包含 API/Worker/Migrate，Compose migration 顺序和失败退出正确。
- [ ] Runbook 覆盖 migration、shutdown、stuck job、lease、manual recovery 和回滚。
- [ ] `go test -race ./...`、关键包 `-count=20`、`go vet ./...`、`make test`、OpenAPI、Compose、Docker build/up/readiness、go-review、sql-code-review、Trellis full-scope check 通过。

## Out Of Scope

- 完整 Audit 产品、告警平台、Grafana Dashboard 和长期日志存储。
- M6 Retrieval consumer、Agent/RAG/Tool Registry 和前端 Workflow Center。
- Kubernetes、Redis、Kafka、Temporal 和多区域部署。
