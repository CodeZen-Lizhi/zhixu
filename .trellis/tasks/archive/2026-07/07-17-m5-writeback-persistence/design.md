# M5-04A 技术设计

## Boundaries

- Domain 拥有 Writeback 状态机、不可变绑定和 Repository Interface。
- PostgreSQL Adapter 拥有 SQL、事务、交叉 Trigger、唯一键和可信数据库时间。
- Approval Git HEAD 在审批时由后续 Application/Git Reader 注入；本任务先冻结字段和持久化契约。
- Publish 事务跨 `change_control` 与现有 `workflow.outbox_event`，由 Change Control Repository 维护该 Safe Writeback 聚合的不变量；事件消费仍由 Workflow/Retrieval 边界负责。

## States

```text
prepared → file_applied → git_committed → verifying
prepared → needs_revision | apply_failed
file_applied → compensating_file → compensated | manual_recovery_required
git_committed → publish_recovery_required | verifying
```

`completed/verify_failed/rolled_back` 由 M6/后续回归任务推进，不在本任务实现假完成。

## Tables

- 扩展 `change_control.proposal(version)`、`change_control.approval(approved_git_head)`。
- 新增 `change_control.writeback_execution`。
- 新增 `change_control.proposal_commit`。
- Reindex 请求复用 `workflow.outbox_event`，事件类型和幂等键固定。

## Transaction Contracts

- Create：锁 Proposal/Approval/Authorization/Workflow，验证完整绑定后插入 prepared Execution；冲突时按完整 identity 判定 replay。
- Checkpoint：只允许状态机下一跳、`version+1`，不可变绑定不变。
- Publish：锁 git_committed Execution 与 Proposal；插 Mapping、Outbox，更新 Execution/Proposal 为 verifying，全部成功才提交。
- Recovery：Mapping 或 Outbox 已存在且绑定一致时返回 replay；不一致返回 ConsistencyViolation。

## Compatibility

- Approval Git HEAD 使用 nullable expand；新写回拒绝 NULL。
- Proposal version 默认 1；现有更新路径需同步改为 `version+1`。
- 前向迁移不修改已发布迁移，不删除历史记录。
