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

### Search

- 输入：query、scope、filters、mode。
- 输出：Evidence Items、scores、index_version、degraded。

### RAG

- 输入：question、conversation、scope、web policy。
- 输出：Workflow Run；完成后 Answer。

### Approval

- 输入：proposal_revision、approved_change_hash、action。
- 输出：Approval + Apply Workflow。

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

- 本地模式仍检查 Origin/CSRF。
- 自托管模式需要单用户认证 Token/Session。
- 不从 URL Query 传 Secret。
- 文件下载通过 Object ID。

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

