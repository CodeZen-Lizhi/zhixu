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
- `internal/platform/observability/`：slog JSON、OpenTelemetry Trace/Metrics、字段规范和 Secret Redaction；实际目录需在 M1 骨架确认。
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

## 后续待验证

- 真实 OpenTelemetry exporter Adapter、采样/保留策略和 Audit Repository。
- 采样策略、日志保留和外部 OTel/Prometheus 接入方式。
- Audit 表字段、不可变约束、访问权限和归档策略；当前仅有架构约束，没有迁移。
