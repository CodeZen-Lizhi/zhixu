# 实施清单

1. [x] 补齐 `ToolAuthorization` 领域模型、能力白名单、错误码和 Repository/Application 契约。
2. [x] 新增前向迁移：授权表、状态/TTL/绑定/唯一幂等约束、Workflow/Change Control 外键和消费更新触发器。
3. [x] 实现 PostgreSQL 签发、查询、撤销和原子消费；为重复同绑定与不同绑定分别保留幂等语义。
4. [x] 实现 Application 签发前的 Proposal/Approval/Target/Workflow 上下文校验和 TTL 上限。
5. [x] 补充领域、Application、PostgreSQL race 集成和安全负测；不创建文件/Git/索引副作用。
6. [x] 同步 `tool-security.md`、API/错误契约和父任务状态，执行 `make test`、真实迁移、OpenAPI、Compose 和 Review。

## 回滚

只新增前向迁移；停用授权签发入口即可阻止新副作用，历史授权按 expires_at/撤销状态自然失效。不得删除或修改已应用迁移。
