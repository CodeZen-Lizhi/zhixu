# API 与事件架构

## 1. 目标

定义 Web UI 与 Go API 的命令、查询、异步任务和实时事件契约。

## 2. 风格

- REST 用于资源查询和命令提交。
- SSE 用于单向任务状态推送。
- 长任务返回 202 + Workflow Run ID。
- 写命令要求 Idempotency-Key。
- 修改要求 Version/ETag。
- 统一 Problem Details 风格错误。

## 3. API 分组

```text
/api/v1/workspaces
/api/v1/sources
/api/v1/documents
/api/v1/search
/api/v1/conversations
/api/v1/proposals
/api/v1/approvals
/api/v1/graph
/api/v1/collections
/api/v1/health
/api/v1/artifacts
/api/v1/review
/api/v1/workflows
/api/v1/settings
/api/v1/auth
/api/v1/events
```

## 4. 命令与查询分离

Query：

- 无业务副作用。
- 可缓存。
- 分页。
- 返回 read model。

Command：

- 校验版本。
- 返回业务结果或 Workflow Run。
- 记录审计。
- 使用 Idempotency Key。

## 5. 异步命令

响应：

```json
{
  "workflow_run_id": "uuid",
  "status": "PENDING",
  "status_url": "/api/v1/workflows/uuid"
}
```

客户端不假设创建任务即成功完成。

## 6. 版本冲突

请求带：

- If-Match/Version。
- Target Revision。
- Change Hash（审批）。

冲突返回：

- current_version。
- expected_version。
- conflict_type。
- resolution_actions。

## 7. 错误模型

```json
{
  "error_code": "CONSISTENCY_CONFLICT",
  "message": "目标文件在审批后发生变化",
  "retryable": false,
  "workflow_run_id": "uuid",
  "details": {}
}
```

错误码稳定，message 可本地化。

## 8. 分页

- 默认 Cursor Pagination。
- 稳定排序字段 + ID。
- limit 有最大值。
- 图谱邻居使用 cursor。
- Collection 大结果禁止无分页。

## 9. SSE

事件：

- workflow.status.changed。
- workflow.node.changed。
- human_task.created。
- proposal.ready。
- index.activated。
- health.scan.completed。
- review.scored。

事件字段：

- id。
- type。
- occurred_at。
- workspace_id。
- resource_ref。
- payload_summary。

## 10. 断线恢复

- 客户端保存 Last-Event-ID。
- 重连时服务端补发保留窗口内事件。
- 超出窗口则客户端重新查询资源状态。
- SSE 不是事实源。

## 11. 主要契约

### Source Import

- 输入：文件/URL/文本、处理策略。
- 输出：Source ID、Workflow Run。

M5 已落地的同步摄取命令为 `POST /api/v1/source-versions/{source_version_id}/ingestion-attempts`：

- 必须携带 `Idempotency-Key` 和 `attempt_number`。
- 服务端只读取已捕获的 Content Artifact，不读取用户原始路径。
- 首次完成返回 201；同一幂等键重放返回已持久化 Attempt/Projection 摘要（200）。
- 响应中的 `chunked` 仅表示 Ingestion 投影完成，不表示 FTS、Embedding 或 `ready`。
- 超过长任务阈值的异步 Workflow 仍由 Workflow API/Worker 负责；该命令不把同步返回包装成异步成功。

### Search

- 输入：query、scope、filters、mode。
- 输出：Evidence Items、scores、index_version、degraded。

### RAG

- 输入：question、conversation、scope、web policy。
- 输出：Workflow Run；完成后 Answer。

### Approval

- 输入：proposal_revision、approved_change_hash、action。
- 输出：Approval + Apply Workflow。

### Workflow Control

`POST /api/v1/workflows/{run_id}/pause|resume|cancel` 是版本化控制命令：

- Header 必须有 `Idempotency-Key`（1..128）；Body 为 `{ "expected_version": <positive integer> }`，不接受客户端 `workspace_id`。
- 成功返回 `{workflow_run_id,status,version,status_url}`；相同命令键和请求 hash 重放返回持久结果。
- 稳定错误包括 `IDEMPOTENCY_KEY_REQUIRED`、`WORKFLOW_CONTROL_INVALID`、`WORKFLOW_RUN_NOT_FOUND`、`WORKFLOW_VERSION_CONFLICT`、`WORKFLOW_CONTROL_CONFLICT`。
- Workspace 绑定由 Run 持久化事实解析，不能由请求体覆盖。

### Graph

- global clusters。
- neighborhood。
- path。
- relation details。
- candidates。

### Review Answer

- 输入：session、card、answer、idempotency key。
- 输出：score、feedback、schedule。

## 12. 安全

- 本地模式默认只绑定 localhost；浏览器仍使用 Cookie Session，并检查 Origin/CSRF，不能把回环地址当作身份。
- 自托管模式必须使用 HTTPS 和单用户认证。
- Web UI 使用可轮换、可撤销的 Cookie Session；Cookie 必须采用 HttpOnly、SameSite，并在 HTTPS 下使用 Secure。
- 非浏览器自动化使用可撤销、限 Scope、可过期的 API Token；Token 不进入 URL Query，服务端只保存不可逆摘要或等价安全表示。
- API Token 不替代浏览器 CSRF 防护，也不得获得超出其 Scope 的能力。
- 登录身份与普通 Capability Authorization 只证明调用者可以发起操作；Apply Knowledge/Git Write 仍要求绑定 Proposal、Approval、Change Hash、Target Version 和 Expiry 的一次性 Write Authorization。
- Session/API Token 撤销不追溯改变已完成审计；撤销后所有后续请求必须失败。
- 不从 URL Query 传 Secret。
- 文件下载通过 Object ID。

认证 API 至少提供登录、登出、当前 Session、Session 轮换，以及 API Token 创建、列出元数据和撤销能力。创建 Token 时明文只返回一次；响应和日志不得再次暴露完整 Token。

## 13. API 可观测性

- request_id。
- trace_id。
- workflow_run_id。
- error_code。
- latency。
- response size。

## 14. OpenAPI

- 从契约生成 OpenAPI。
- CI 校验 Breaking Change。
- Generated Client 只在前端边缘，领域模块不依赖。

## 15. 测试

- Contract Test。
- Version Conflict。
- Idempotency。
- Pagination Stability。
- SSE Reconnect。
- Error Mapping。
- Session Rotation/Revocation。
- CSRF/Origin。
- API Token Scope/Expiry/Revocation。
- 登录授权不能绕过 Approval Write Authorization。
