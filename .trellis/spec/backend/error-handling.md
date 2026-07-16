# 后端错误处理规范

## 适用范围

适用于领域模块、Application Command/Query、HTTP/SSE 边界、Worker/Workflow、数据库、文件、Git、Parser、Model 和 Tool Adapter。当前没有可运行实现，规范中的代码落点和验证命令将在 M1 代码基线落地后校验。

## 已确认事实

- 统一错误分类为：`InvalidInput`、`NotFound`、`VersionConflict`、`PermissionDenied`、`DependencyUnavailable`、`RetryableFailure`、`NonRetryableFailure`、`ConsistencyViolation`、`ManualRecoveryRequired`（依据 [`interfaces-and-adapters.md`](../../../docs/architecture/interfaces-and-adapters.md) 第 12 节）。
- Adapter 必须把 SDK、命令行和数据库原始错误映射为领域错误；外部错误类型不得泄漏到领域层。
- API 使用稳定 `error_code`、可理解的 `message`、`retryable`、可选 `workflow_run_id` 和结构化 `details`；message 可本地化，错误码用于程序判断和日志检索（依据 [`api-and-events.md`](../../../docs/architecture/api-and-events.md) 第 7 节）。
- 预计超过 3 秒的操作必须返回异步 Workflow Run；客户端不能把创建任务的 202 当作业务已成功完成。
- 版本/一致性冲突必须包含当前版本、期望版本、冲突类型和可行解决动作；Proposal 进入 `NEEDS_REVISION` 或等价状态，不得静默覆盖用户修改。
- 不可确认权限、引用失效、Git/DB 不一致或安全检查异常时必须拒绝、隔离或进入只读/人工恢复，不得返回假成功（依据 [`security.md`](../../../docs/architecture/security.md) 第 16 节）。

## 目标代码落点（M1 起）

- 领域错误类型和错误码：shared kernel 或各领域模块的稳定接口包；最终包路径由 M1 代码确认。
- Application 层：命令/查询错误到 HTTP/SSE/Workflow 结果的统一映射。
- `cmd/api` 或 `internal/app`：Problem Details 响应、HTTP 状态码、`request_id`/`workflow_run_id` 关联。
- `internal/workflow`：Retryable/NonRetryable/Manual 分类、退避、租约和补偿状态。
- `internal/platform/*`：pgx、Git CLI、文件、解析器和模型原始错误映射。
- `internal/audit` 与 observability 边界：安全阻断、审批、写回、回滚和人工恢复的审计事件。

## 错误分类与传播

1. 在最靠近根因的 Adapter 处保留可诊断上下文并映射稳定类别；使用 Go error wrapping 保持 `errors.Is/As` 可判定，禁止直接返回厂商/命令行错误。
2. Application 层将领域错误转为契约错误；只暴露稳定错误码、用户可理解的安全信息和最小结构化详情。
3. Worker 按错误类别决定重试：超时、限流、暂时网络或锁冲突通常可重试；Schema、权限、非法状态和证据缺失不可自动重试；一致性损坏、补偿失败进入人工恢复。具体策略遵循 [`workflow-engine.md`](../../../docs/architecture/workflow-engine.md) 第 9 节。
4. Retryable 错误必须有最大次数、指数退避、抖动和必要的 `Retry-After`；未知副作用结果不得盲目重试，应先查询幂等记录或进入人工恢复。
5. 同一错误在 API、Workflow、日志、Trace 和 Audit 中共享 error code；SSE 只推送摘要，客户端重新查询资源状态，SSE 本身不是事实源。
6. 每个失败路径都要保留状态、错误类别、可重试性和最后失败节点；不要通过捕获异常后标记成功来隐藏失败。

## API 与 SSE 响应

API 错误响应至少包含：

```json
{
  "error_code": "CONSISTENCY_CONFLICT",
  "message": "目标文件在审批后发生变化",
  "retryable": false,
  "workflow_run_id": "optional-id",
  "details": {}
}
```

上面的字段和语义来自 API 架构文档；`workflow_run_id` 仅在已有异步运行实例时返回，示例中的值不是项目固定 ID。

- 输入校验失败、未找到、权限拒绝、冲突和依赖不可用必须映射到稳定 HTTP 状态；具体状态码由 OpenAPI 在 M1 锁定，不能在各 Handler 中自行发明。
- `details` 只放帮助客户端恢复的字段（如 `current_version`、`expected_version`、`conflict_type`、`resolution_actions`）；不放 Secret、完整 Prompt、内部堆栈或任意文件路径。
- SSE 事件带 `id`、`type`、`occurred_at`、`workspace_id`、`resource_ref`、`payload_summary`。断线后通过 Last-Event-ID 补发保留窗口或改为重新查询。

## 禁止模式

- `panic` 或空 `catch`/空 error 分支处理普通业务输入、外部依赖和用户可恢复错误。
- 吞掉错误、只记录日志后返回零值，或将失败响应包装成成功/空数据。
- 把完整堆栈、SQL、Authorization、模型原始响应或 Workspace 绝对路径返回给客户端。
- 让模型文本决定权限、重试或是否批准；这些判断必须在服务端 Workflow/Tool/Change Control 执行。
- 对未知副作用结果自动重复执行文件写入、Git Commit、Tool Call 或审批。
- 为了兼容旧客户端在 Handler 中增加静默 fallback；契约变化应通过版本化字段、错误码和迁移策略明确处理。

## 验证方式

### M0 当前（仅规范）

```bash
rg -n 'T(BD)|To[[:space:]]+be[[:space:]]+filled' .trellis/spec/backend
git diff --check
```

### M1 代码落地后

- 单元测试覆盖错误分类、`errors.Is/As`、Error Mapping、Change Hash/Version Conflict 和状态机非法转移。
- API Contract Test 覆盖输入校验、Problem Details 字段、稳定 error code、Idempotency、ETag/Version 冲突和 202 异步响应。
- Workflow 集成测试覆盖 Crash、租约过期、重复投递、Retryable/NonRetryable/Manual 分类、取消和补偿失败。
- 安全负测确认权限失败、路径越界、SSRF、Prompt Injection 和 Secret Store 不可用不会产生成功写入。

## 待 M1 代码验证

- 领域错误类型、错误码常量和 HTTP 状态码的实际包路径。
- 是否采用 RFC 9457 `type/title` 兼容字段；当前产品契约只锁定 `error_code/message/retryable/details`。
- 结构化错误的序列化、SSE 失败事件和多语言 message 策略。
- 全量错误码清单、客户端生成类型和 OpenAPI Breaking Gate。
