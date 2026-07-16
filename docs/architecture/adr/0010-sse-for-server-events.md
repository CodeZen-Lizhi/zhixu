---
status: accepted
---

# 使用 SSE 推送任务状态

前端主要需要服务器向浏览器单向推送 Workflow、Proposal、Index 和 Review 状态，不需要高频双向实时协作。SSE 比 WebSocket 更简单，具备 HTTP 代理兼容、Last-Event-ID 和自动重连能力。

## Consequences

- 用户命令仍通过 REST。
- SSE 只做通知，资源状态必须通过查询接口确认。

