---
status: accepted
---

# 使用 PostgreSQL + River 持久化工作流而非直接引入 Temporal

项目需要暂停恢复、Human-in-the-Loop、重试和补偿，但部署目标是个人本地与轻量自托管。使用 PostgreSQL 保存 Workflow/Node/Outbox，River 负责 PostgreSQL Job 投递和 Worker 执行，可以复用现有依赖并减少队列样板代码；Temporal 的集群和运维成本暂不合理。

## Consequences

- 必须自行实现业务状态迁移、幂等、Human Node 和补偿测试。
- Workflow Module Interface 保留未来迁移可能。
