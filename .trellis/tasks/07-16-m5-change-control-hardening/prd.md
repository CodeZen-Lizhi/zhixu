# M5 Change Control 幂等与失效状态加固

## Goal

补齐 Proposal 创建与审批幂等语义，并在目标不可用时进入 needs_revision。

## Requirements

- Proposal 创建必须要求 `Idempotency-Key`，并在 Workspace 范围内持久化唯一。
- 相同幂等键和相同请求重放时返回已有 Proposal；相同幂等键绑定不同请求时返回稳定冲突。
- 相同 Revision、Change Hash 和 Decision 的审批重试返回已有 Approval；不同 Decision 或 Hash 仍返回冲突。
- Apply preflight 发现目标不存在、不再是普通文件或路径不再安全时，将已批准 Proposal 标记为 `needs_revision`。
- 不新增文件写回、Git Commit 或假成功路径。

## Acceptance Criteria

- [x] migration 包含 Proposal 创建幂等键与请求哈希唯一/非空约束。
- [x] 单元和 PostgreSQL 集成测试覆盖相同请求重放、幂等键复用冲突、相同审批重放和不同审批冲突。
- [x] HTTP/OpenAPI 明确要求 `Idempotency-Key`，首次创建返回 201，重放返回 200。
- [x] 目标文件不可用时返回稳定冲突并持久化 `needs_revision`。

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.
