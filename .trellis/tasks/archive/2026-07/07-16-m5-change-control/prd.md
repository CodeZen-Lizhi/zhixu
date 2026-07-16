# Proposal Approval 与安全写回基础闭环

## Goal

实现 Proposal/Revision/Approval/Change Hash 和文件版本校验基础边界，确保无审批不写回。

## Requirements

- Proposal 和 Proposal Revision 必须记录目标路径、基线哈希、变更内容、证据摘要、风险和回滚计划。
- Change Hash 由服务端对规范化变更内容计算，Approval 必须绑定 Revision 与 Change Hash。
- 未审批、已拒绝、已过期或基线哈希变化的 Proposal 不得进入 Apply。
- 本任务实现数据库状态和安全校验；正式文件原子写回/Git Commit 只在所有前置校验通过后由独立 Safe Writeback seam 接入。
- 错误使用稳定 `error_code`，不得返回内部 SQL、绝对路径或 Secret。

## Acceptance Criteria

- [ ] Migration 包含 Proposal、Revision、Approval 的不可变/唯一/状态约束与索引。
- [ ] 单元/集成测试覆盖无审批拒绝、Change Hash 不匹配、重复审批、基线冲突和正常审批。
- [ ] Repository/Application 不泄漏 pgx 类型，事务内保证 Proposal Revision + Approval 一致性。
- [ ] 不修改未通过审批的 Workspace 文件；Apply 前置校验失败明确返回且不产生成功状态。

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.
