# 后端日志与审计规范

## 适用范围

适用于 API、Worker、Workflow、Model/Tool Adapter、数据库和文件/Git 操作。普通日志用于运行诊断；安全与业务决策必须写入独立、可长期保留的 Audit 流，不得把 Audit 当作普通日志的附属文本。

## 已确认事实

- 后端日志库选用 Go 标准库 `slog`，输出结构化 JSON；Trace/Metrics 使用 OpenTelemetry（依据 [`technology-stack.md`](../../../docs/architecture/technology-stack.md) 第 2 节）。
- 级别语义为：DEBUG 开发诊断，INFO 状态变化，WARN 降级/重试/低置信度，ERROR 节点失败或依赖失败（依据 [`observability.md`](../../../docs/architecture/observability.md) 第 4 节）。
- 相关字段包括 `request_id`、`trace_id`、`workspace_id`、`workflow_run_id`、`node_run_id`、`proposal_id`、`tool_call_id`、`document_id`；异步边界必须保持关联（依据 [`observability.md`](../../../docs/architecture/observability.md) 第 3、5 节）。
- 禁止记录 API Key、Authorization Header、完整 Prompt/Source（默认）、无保留期限的用户回答全文；不展示模型私有思维链。
- Audit 独立记录 Approval、Tool Permission、File Write、Git Commit、Rollback、Memory/Settings Change 和 Security Block，并且不能由普通清理任务删除。

## 目标代码落点（M1 起）

- `cmd/api`、`cmd/worker`：创建带统一字段和级别策略的 slog Handler，并注入应用与 Worker。
- `internal/observability/`：业务包使用的稳定 facade；不复制实现。
- `internal/platform/observability/`：slog JSON、OpenTelemetry Trace/Metrics、字段规范和 Secret Redaction 的单一事实源。
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

1. Secret 在进入 logger、Trace、Model Prompt、DB Export 前统一脱敏；禁止把 Authorization、Cookie、API Key、Secret File 内容写入任何普通日志。
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

## M4-D 已验证边界

- `internal/platform/observability` 已实现 slog JSON safe Handler、Context correlation、Secret/绝对路径 fail-closed redaction、bounded Metrics registry 和 traceparent seam。
- API request middleware 在 exporter disabled 时也创建 child/root trace；生产
  `RuntimeNodeWorker` 在 Claim 前解码 metadata、Claim 后补齐 Workspace/Run/Node/
  Attempt/Dispatch/Retry/River Job correlation。
- queue/active/node result/retry/manual/lease/heartbeat/duplicate/shutdown 指标已接到真实
  Worker/PostgreSQL 事实点；持久 transition replay 不重复发射，metric 失败不改变业务结果。
- 项目自有 River metadata 只写 `traceparent`；River 保留的 `river:*` recovery 字段可共存但不进入 Application 或可观测载荷。
- `disabled/optional/required` 不伪造 exporter 成功；当前生产 Composition 未提供真实 exporter factory，optional 明确 degraded，required fail-fast。
- Worker `/livez|readyz` 只返回稳定 `status/code/version`；真实容器日志和 health response 已执行 Secret canary 扫描。

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

## 后续待验证

- 真实 OpenTelemetry exporter Adapter、采样/保留策略，以及全部安全/业务决策调用点
  对 Audit Recorder 的接入覆盖。
- 采样策略、日志保留和外部 OTel/Prometheus 接入方式。
- Audit 访问权限、长期归档策略与真实自托管数据库演练。

## M6-03 Tool Redaction Boundary

- Tool Output 与 observability 必须复用 `internal/foundation/redaction` 单一事实源；不得在 Adapter、Service 和 logger 分别维护 Secret/path 规则。
- 脱敏至少覆盖 password/passwd、Authorization/Cookie/Credential、DSN/database URL、通用 URL userinfo、Unix/Windows/file 绝对路径和嵌入文本路径；HTTP(S) URL 的普通 path 不能误判成本地文件路径。
- `workflow.tool_call` 是受限业务执行事实，不等同 M10 通用 append-only Audit；它只允许受控摘要、Hash、bytes 和稳定 ref。
- error、String/GoString、日志、Trace、readyz 和测试失败输出不得包含 raw Prompt/arguments/output、网页/Source 正文、Credential、完整 Endpoint、绝对路径或命令 stderr。
- Safe Writeback Apply/Git 逻辑 Tool Call 与同一 `writeback_execution` receipt 关联；retry/reconciliation 不新增第二条审计或改写历史 Attempt。
