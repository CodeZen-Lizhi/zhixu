# 后端日志与审计规范

## 适用范围

适用于 API、Worker、Workflow、Model/Tool Adapter、数据库和文件/Git 操作。普通日志用于运行诊断；安全与业务决策必须写入独立、可长期保留的 Audit 流，不得把 Audit 当作普通日志的附属文本。

## 已确认事实

- 后端日志库选用 Go 标准库 `slog`，输出结构化 JSON；Trace 使用 OpenTelemetry，Metrics 使用 `prometheus/client_golang`（依据 [`system-design.md`](../../../docs/architecture/system-design.md)）。
- 级别语义为：DEBUG 开发诊断，INFO 状态变化，WARN 降级/重试/低置信度，ERROR 节点失败或依赖失败（依据 [`quality.md`](../../../docs/architecture/quality.md)）。
- 相关字段包括 `request_id`、`trace_id`、`workspace_id`、`workflow_run_id`、`node_run_id`、`proposal_id`、`tool_call_id`、`document_id`；异步边界必须保持关联（依据 [`quality.md`](../../../docs/architecture/quality.md)）。
- 禁止记录 API Key、Authorization Header、完整 Prompt/Source（默认）、无保留期限的用户回答全文；不展示模型私有思维链。
- Approval、Tool Permission、File Write、Git Commit、Rollback、Memory/Settings Change 和 Security Block 需要保留各 owner 的独立事实；现有通用 Audit 覆盖按实际生产者核对，不能把该目标写成全域已经接入。Audit 不由普通清理任务删除。

## 目标代码落点（M1 起）

M10 的当前精简交付保留已接入的 append-only Audit 生产者，完整跨领域覆盖与自动留存/归档不是本次承诺。
`cmd/audit` 提供操作者只读查询：显式 `--workspace UUID` 或 `--global`，后者仅表示 `workspace_id IS NULL`，
不表示跨所有 Workspace。limit 默认 50、最大 200，后续页同时使用 `before` 与 `before-id`；连接强制默认只读，
工具不迁移数据库、不写事件。输出只包含 ID/时间/主体类型/动作/资源类型/结果/安全错误码，省略任意 correlation、
metadata、ActorRef、ResourceRef 和幂等键；错误使用固定安全 code，不回显 DSN/原始数据库错误。
它通过操作者数据库凭据授权，不是公开 HTTP API 或产品 `AUDIT_JSON` 导出；各 owner 独立历史与通用 Audit 不互相冒充。

- `cmd/api`、`cmd/worker`：创建带统一字段和级别策略的 slog Handler，并注入应用与 Worker。
- `internal/observability/`：业务包使用的稳定 facade；不复制实现。
- `internal/platform/observability/`：slog JSON、OpenTelemetry Trace、Prometheus Metrics、字段规范和 Secret Redaction 的单一事实源。
- `internal/audit/`：append-only Audit Interface、持久化 Adapter、查询投影和脱敏边界。
- `internal/workflow/`、`internal/tools/`、`internal/changecontrol/`：写入运行摘要和业务审计事件，不自行拼接无结构日志。
- `internal/platform/models/`、`internal/platform/gitcli/`、`internal/platform/filesystem/`：记录调用摘要、耗时、错误码和版本，不记录完整敏感载荷。

## 级别与事件内容

- DEBUG：仅用于本地/显式诊断，记录节点输入摘要、解析器选择或查询计划摘要时必须脱敏并受配置控制；默认不输出完整 Prompt、Source 和模型响应。
- INFO：记录服务启动/停止、Workflow/Node 状态变化、Proposal/Approval/Commit/Index 状态变化和配置版本切换。
- WARN：记录明确降级（例如 Reranker 禁用）、重试、低置信度、索引陈旧、租约回收和需要用户关注但未失败的情况；必须带 `degraded` 或错误分类。
- ERROR：记录节点失败、依赖不可用、补偿失败、Git/DB 不一致和人工恢复入口；必须带稳定 `error_code`、可重试性和 correlation 字段。

日志事件至少包含：事件名/operation、时间戳、服务/版本、workspace 与 workflow 关联、耗时、重试次数、错误码和必要的资源 ID。字段名称在 M1 统一，不能同义多写（如同时使用 `run_id` 和 `workflow_id` 表示同一值）。

## 脱敏与审计

1. Secret 在进入 logger、Trace、Model Prompt、产品 Export 前统一脱敏；禁止把 Authorization、Cookie、API Key、Secret File 内容写入任何普通日志。操作者整库备份会包含数据库加密配置，必须受保护保存，解密主密钥另存，不把灾备当作公开产品导出。
2. Source、用户回答和模型原始响应只记录摘要、哈希、长度或稳定对象 ID；如确需调试原文，必须显式开启短期 debug 配置并有保留期限。
3. Tool Audit 记录调用者、Workflow/Node、Tool、权限、参数摘要、目标、幂等键、耗时和结果/错误；不记录完整敏感参数。
4. Audit 与普通日志分别有生命周期和访问权限；审计事件必须支持按 Proposal、Commit、Tool Call、Security Block 和 Workflow 反查。
5. 日志和 Trace 不能记录模型私有思维链；只记录结构化输出摘要、Schema/Prompt/Model 版本和引用统计。
6. Credential marker 优先于 `_id`、`_hash`、`_status` 等摘要后缀；未知 `error` 与
   `fmt.Stringer` 默认整体脱敏，调用方另记稳定 `error_code`。
7. Audit correlation/payload 使用严格 JSON、递归脱敏和 canonical 编码；读取持久记录
   发现明文 Secret 时必须 fail closed，不能在返回阶段静默遮住已发生的落库泄漏。

## 禁止模式

- `fmt.Printf`/`log.Printf` 散落在业务代码中，绕过统一结构化 Handler。
- 记录完整 HTTP Header、Cookie、Prompt、Source、用户答案、模型原始响应或数据库连接串。
- 用日志文本作为业务状态或审计事实源；需要用户可见时间线或恢复依据的内容必须持久化为领域事件/Audit。
- 在重试循环中重复写入不可幂等的 Audit 副作用，或因日志失败吞掉业务错误。
- 使用不同字段名表达同一 correlation，或在 API/Worker 之间丢失 `workflow_run_id`/`trace_id`。
- 把完整错误堆栈、SQL 参数和绝对文件路径返回客户端或复制到低权限日志流。

## 验证方式

### M0 当前（仅规范）

```bash
rg -n 'T(BD)|To[[:space:]]+be[[:space:]]+filled' .trellis/spec/backend
git diff --check
```

### M1 代码落地后

- 单元测试验证结构化字段、级别选择和 Secret Redaction；输入包括 Authorization、API Key、Prompt、Source、Cookie 和模型响应。
- Workflow/Adapter 集成测试验证 API→Workflow→Model/Tool→Writeback Trace 连续，重试不重复审计副作用。
- 日志契约测试验证 JSON 可解析、error code 可查询、关键资源可反查且 SSE/用户时间线只暴露摘要。
- 安全测试和导出测试确认日志、Audit、Trace 和导出包均不泄露 Secret 或未授权全文。

## OTel/Prometheus 已验证边界

- `internal/platform/observability` 已实现 slog JSON safe Handler、Context correlation、
  Secret/绝对路径 fail-closed redaction、显式 OTel SDK Provider/OTLP HTTP exporter、
  进程独立 Prometheus Registry 和 traceparent seam；业务包不得直接依赖 SDK 类型。
- API request middleware 在 exporter disabled 时也创建 child/root trace；生产
  `RuntimeNodeWorker` 在 Claim 前解码 metadata、Claim 后补齐 correlation，并只对非 stale
  delivery 创建 `workflow.node.consume` child span。
- queue/active/node result/retry/manual/lease/heartbeat/duplicate/shutdown 指标已接到真实
  Worker/PostgreSQL 事实点；持久 transition replay 不重复发射，metric 失败不改变业务结果。
- 项目自有 River metadata 只写 `traceparent`；River 保留的 `river:*` recovery 字段可共存但不进入 Application 或可观测载荷。
- `disabled` 零 OTLP 网络但保留 context 和进程内 Metrics；只有 API 暴露本地 `/metrics`，
  Worker health server 只暴露 `/livez|/readyz`。optional/required 只有在 Trace 与 Metrics
  两个 startup probe 都真实 export+ForceFlush 成功后才标记 exporting；任一失败时 optional
  原子清理两种 exporter 并降级，required 在 listener/ready 前 fail-fast。
- OTLP base URL 保留 base path 后分别追加 `/v1/traces` 和 `/v1/metrics`；endpoint/header/compression/TLS/
  timeout/retry、sampler、span limits 和 BSP 参数必须由项目显式覆盖环境默认。实际 payload
  Resource 只能含 service name/version/deployment environment。
- API 顶层 `/metrics` 暴露项目 collector 及 Go/process collector；`/metrics|/livez|/readyz`
  不创建 request span。Worker 没有本地 Metrics HTTP route；optional/required 模式通过 OTLP
  导出同一项目 Measurement。Metrics 只表示当前进程快照和本次启动累计值，历史由外部后端拥有。
- API/Worker 的进程作用域 Provider 分别使用 `zhixu-api`/`zhixu-worker` service name，并在 Shutdown 刷新
  Metrics/Trace。`TELEMETRY_EXPORTING` 只证明两个 exporter 的 startup probe 已成功；持续可达性必须由
  真实 `/v1/metrics`、`/v1/traces` 导出证据证明。
- Worker `/livez|readyz` 只返回稳定 `status/code/version`；真实容器日志和 health response 已执行 Secret canary 扫描。

### Scenario: Prometheus 与 OTLP Metrics 双写

#### 1. Scope / Trigger

- `cmd/api`、`cmd/worker` 初始化 `internal/platform/observability.Telemetry` 时，必须按配置同时建立
  进程内 Prometheus 与可选 OTLP Metrics/Trace 生命周期；业务包只能通过项目 `Metrics` 接口记录。

#### 2. Signatures

- 初始化入口固定为 `InitializeTelemetry(ctx, TelemetryOptions)`；Metrics 入口固定为
  `Telemetry.Metrics()`，只有 API composition 使用 `Telemetry.MetricsHandler()` 挂载 `/metrics`。
- OTLP base endpoint 分别追加 `/v1/traces` 与 `/v1/metrics`；Resource 必须且只能包含
  `service.name`、`service.version`、`deployment.environment`。
- 项目 Metrics 使用 canonical dotted 名称，例如 `workspace_analysis.outcome_total`；Prometheus
  exposition 再映射为 `zhixu_workspace_analysis_outcome_total`，不得反向污染 OTLP 名称。

#### 3. Contracts

- `disabled` 不发 OTLP 网络请求；API 本地 `/metrics` 继续可用，Worker 只提供健康路由。
- `optional|required` 的业务 Metrics 必须同时写入本地 Prometheus Registry 与 OTLP MeterProvider；
  Trace 或 Metrics 任一 startup probe 失败时，不得留下半导出状态。
- shutdown 必须各自恰好一次关闭 Trace 与 Metrics Provider；PeriodicReader shutdown 自带最终 collect/export，
  不得在前面额外 ForceFlush 造成 cumulative sample 重复。
- OTLP label 继续服从项目低基数和脱敏合同，不能加入 Workspace、Run、Answer、Tool、Receipt 或路径正文。
- Collector 的 Prometheus projection 可按标准语义从 `service.name` 增加固定 `job` 标签；演练必须将它
  与三项 Resource、业务标签一起做完整键值集合校验，不能把任意额外标签当成安全元数据。

#### 4. Validation & Error Matrix

- `disabled` 且 endpoint 非空 -> `ErrTelemetryEndpointForbidden`，零 exporter 请求。
- `optional` 且任一 signal 构造或 startup probe 失败 -> 稳定 degraded，本地 Metrics 与 non-exporting Trace 可用。
- `required` 且任一 signal 失败 -> `ErrTelemetryExporterRequired`，进程在 listener/ready 前失败。
- 重定向、环境变量偷渡、非法 TLS/endpoint、超时或未知 Metric/label -> fail closed，不记录 raw endpoint/error。
- exporting fanout 在 shutdown 后继续记录 -> `ErrObservabilityClosed`，不得静默接受或重新创建 Provider；
  `disabled`/degraded 的本地 Prometheus Registry 保持既有进程内生命周期，不据此宣称存在外部 Provider。

#### 5. Good/Base/Bad Cases

- Good: Worker required 模式向隔离 Collector 导出一个带精确四个业务标签、三项 Resource 和固定 `job`
  投影的 `workspace_analysis.outcome_total` cumulative monotonic Sum，exact replay 不增量。
- Base: disabled 模式 API 仍可读取本地 `/metrics`，Worker `/livez|/readyz` 正常且无 OTLP 请求。
- Bad: 宣称 Worker 存在 `:8081/metrics`，或 Trace exporter 成功后 Metrics exporter 失败却仍报告 exporting。

#### 6. Tests Required

- 单元与 race 测试覆盖 `/v1/metrics` 路径、exact Resource、canonical instrument、label 集、环境隔离、
  optional/required 原子语义、并发 shutdown 和关闭后拒绝。
- Compose contract 必须锁定 Collector digest、loopback 网络、named config、只读/最小权限和无 host bind mount。
- 真实 Compose rehearsal 必须跑完整 Worker/PostgreSQL/River Workspace Analysis，执行 exact replay、
  graceful shutdown，并在 Collector 投影验证单一样本和零身份标签。

#### 7. Wrong vs Correct

```text
Wrong: 给 Worker 虚构 /metrics 路由，或用 host bind mount 注入 Collector 配置后绕过 Workspace grant。
Correct: API 保留本地 /metrics；API/Worker 经官方 OTLP Metrics SDK 双写，Collector 用 pinned image、named config 和隔离 loopback 真实验收。
```

## M10-01 Audit 与安全脱敏边界

- `internal/audit` 已实现 append-only Event、Recorder 和 PostgreSQL Adapter；业务事件
  调用方负责稳定提供 ID、发生时间和幂等键。
- 相同 Workspace+幂等键只允许精确完整 binding 重放；不同 binding 稳定冲突。
  `NULL workspace_id` 使用 advisory lock 与 `IS NOT DISTINCT FROM` 收敛并发事实。
- `ops.audit_event` UPDATE/DELETE 由迁移 trigger 以 SQLSTATE `55000` 拒绝；Adapter
  不提供改写接口。
- 单元测试覆盖递归 Secret/JWT/路径脱敏、重复 JSON key、canonical/fingerprint、
  Recorder binding 校验、Adapter 错误分类与数据库读回 fail-closed。真实 PostgreSQL
  测试通过 `-tags integration` 和 `ZHIXU_TEST_DATABASE_URL` 显式启用。
- Impact Analysis 以 Report ID 为 UUID v5 namespace、以 HTTP `Idempotency-Key` 为 name 派生
  Audit Event ID；相同请求键精确重放同一 Audit 事实，不同请求键重用同一 Report 时必须追加不同
  Audit 事件，不能复用 `ReportID` 作为 Audit 主键。
- 审计主体必须保留认证来源：Cookie Session 记为 `USER`，Bearer API Token 记为 `API_TOKEN`；
  不能把自动化 Token 操作伪装成人工用户操作。

### Scenario: Workspace Audit Advisory Lock Key

#### 1. Scope / Trigger

- `internal/audit/adapter/postgres.AppendTx` 追加带 `workspace_id` 的幂等 Audit 事件时，
  PostgreSQL 的唯一约束与 `NULL` scope 需要同一条 transaction-scoped advisory lock 路径。

#### 2. Signatures

- 锁调用固定为 `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`。
- Workspace scope 锁键固定为 `string(workspaceID) + ":" + idempotencyKey`；无 Workspace
  时锁键仅为 `idempotencyKey`。

#### 3. Contracts

- `workspaceID` 必须是 canonical fixed-width UUID；`idempotencyKey` 必须先通过
  `domain.Event.Validate` 的可打印文本校验。
- 锁键作为 PostgreSQL `text` 参数传递，分隔符必须是可打印、无歧义字符；不得使用 `NUL`。

#### 4. Validation & Error Matrix

- 锁键含 `NUL` -> PostgreSQL 拒绝 text 参数，表现为 `AUDIT_EVENT_UNAVAILABLE`；这是实现错误，
  不能靠重试掩盖。
- 同一 Workspace+幂等键且完整 binding 相同 -> 返回既有事件并标记 replay。
- 同一 Workspace+幂等键但 binding 不同 -> `AUDIT_IDEMPOTENCY_CONFLICT`。

#### 5. Good/Base/Bad Cases

- Good: `workspaceUUID + ":" + "impact:request-1"` 能正常取得锁并精确重放。
- Base: 全局事件只用 `idempotencyKey`，再用 `IS NOT DISTINCT FROM` 查询统一 `NULL` scope。
- Bad: `workspaceUUID + "\\x00" + idempotencyKey`；pgx/PostgreSQL 不接受 NUL text 参数。

#### 6. Tests Required

- 真实 PostgreSQL 集成测试必须覆盖 Workspace-scoped 首次追加、同键 replay 与单行计数，
  断言不会得到 `AUDIT_EVENT_UNAVAILABLE`。
- 同时保留全局 scope 的并发 replay、不同 binding 冲突和 append-only trigger 断言。

#### 7. Wrong vs Correct

```text
Wrong: 用 NUL 拼接 workspace 与幂等键，再把失败分类为可重试 Audit 依赖故障。
Correct: 用 canonical UUID + ':' + 已验证幂等键构造可打印锁键，并用真实 PG replay 回归测试锁定。
```

## 可选运营与扩展范围

以下未执行验证与未实现扩展均不阻塞当前开发交付；只在实际运维或明确新需求中选择，不登记为本轮测试欠项。

- 生产采样/保留策略、外部 Collector/Prometheus/Trace 后端部署，以及全部安全/业务决策
  调用点对 Audit Recorder 的接入覆盖。
- 灰度环境的外部 OTel/Prometheus 查询、告警和稳定发布观察证据的受控归档方式。
- Audit 访问权限、长期归档策略与真实自托管数据库演练。

## Scenario: Workspace Analysis 终态指标

### 1. Scope / Trigger

- `workspace-analysis@1` 或 `workspace-analysis@2` 的 Answer/Analysis Run 终态成功提交后，记录一个低基数结果计数；
  临时节点状态、重放和提交前状态不得产生指标。

### 2. Signatures

- 业务指标名固定为 `workspace_analysis.outcome_total`，Prometheus 名固定为
  `zhixu_workspace_analysis_outcome_total`。
- 新调用使用 `NewWorkspaceAnalysisOutcomeMeasurementForVersion(definitionVersion, status, terminationReason)`，
  版本来自持久 Run 的绑定；旧 `NewWorkspaceAnalysisOutcomeMeasurement(status, terminationReason)` 保持 v1 语义。

### 3. Contracts

- 标签必须且只能包含 `mode=workspace_analysis`、`definition=workspace-analysis-v1|workspace-analysis-v2`、
  `outcome` 和 `termination_reason`。
- Definition 只接受版本 1 或 2，并精确映射对应标签；不得按当前默认版本重标历史 Run。
- `outcome` 只允许 `completed|refused|clarification_required|failure|cancelled`；
  `termination_reason` 只允许领域冻结的 13 个终态原因。
- Workspace、Workflow、Answer、Tool、Operation、Receipt 等身份不得成为标签。

### 4. Validation & Error Matrix

- active 状态、未知状态或 status/reason 组合不匹配 -> `ErrInvalidMetric`。
- 缺少固定标签、未知 label/value、UUID/长哈希/长数字身份 -> fail closed。
- Metrics exporter 失败 -> 不回滚已提交业务事务，但必须保留稳定诊断错误码。

### 5. Good/Base/Bad Cases

- Good: `succeeded + COMPLETED` 映射为 `completed`，在首次终态提交后计数一次。
- Base: Worker 重放已存在 publication，只读验证而不重复计数。
- Bad: 把 `workspace_id` 或 `result_hash` 放入标签，或在 transaction commit 前记录。

### 6. Tests Required

- 单元测试覆盖全部合法 status/reason 矩阵和非法组合。
- Memory/Prometheus 测试断言 exact label set、exposition 名称和 UUID/hash 泄漏拒绝。
- Finalizer 测试断言首次提交计数、持久重放不重复，以及 exporter 错误不改变终态结果。

### 7. Wrong vs Correct

```text
Wrong: 在节点完成或 publication replay 时，用 workspace_id/result_hash 作为 label 记录结果。
Correct: 只在首次持久终态提交成功后，使用固定 definition/mode 和枚举 outcome/reason 计数。
```

## M6-03 Tool Redaction Boundary

- Tool Output 与 observability 必须复用 `internal/foundation/redaction` 单一事实源；不得在 Adapter、Service 和 logger 分别维护 Secret/path 规则。
- 脱敏至少覆盖 password/passwd、Authorization/Cookie/Credential、DSN/database URL、通用 URL userinfo、Unix/Windows/file 绝对路径和嵌入文本路径；HTTP(S) URL 的普通 path 不能误判成本地文件路径。
- `workflow.tool_call` 是受限业务执行事实，不等同 M10 通用 append-only Audit；它只允许受控摘要、Hash、bytes 和稳定 ref。
- error、String/GoString、日志、Trace、readyz 和测试失败输出不得包含 raw Prompt/arguments/output、网页/Source 正文、Credential、完整 Endpoint、绝对路径或命令 stderr。
- Safe Writeback Apply/Git 逻辑 Tool Call 与同一 `writeback_execution` receipt 关联；retry/reconciliation 不新增第二条审计或改写历史 Attempt。
