# Workflow 持久化与人工审批基础闭环

## Goal

实现 PostgreSQL 持久化 Workflow Run/Node/Human Task 的最小可恢复闭环，为 Proposal/Approval 和安全写回提供真实执行边界。

## Requirements

- Workflow Definition/Run/Node、Human Task 和 Outbox 由 PostgreSQL 持久化。
- 节点状态迁移、租约、心跳、过期回收和幂等完成必须由应用和数据库共同约束。
- Human Task 只能提交一次；重复提交、过期版本和非法状态必须返回稳定错误。
- API 提供启动确定性工作流、查询 Run 和提交 Human Task 决定；长任务返回 202 与 Run ID。
- 本任务不执行文件/Git 写回，不绕过 Proposal → Approval → Safe Writeback seam。

## Acceptance Criteria

- [ ] Migration 包含 Definition、Run、Node Run、Human Task、Outbox 最小约束与索引。
- [ ] 单元/集成测试覆盖正常、非法状态、租约过期、重复完成和重复 Human Submit。
- [ ] API/OpenAPI 契约和统一 Problem 错误通过。
- [ ] Compose SQL smoke 证明迁移可重复执行且唯一约束生效。
- [ ] 不返回“已完成”的假异步成功结果。

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.
