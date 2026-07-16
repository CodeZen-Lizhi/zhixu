# M5-04A Writeback Persistence

## Goal

为 Safe Writeback 建立可恢复、可审计、不可伪造的领域与 PostgreSQL 事实源：Approval Git HEAD、Proposal 乐观锁、Writeback Execution、Proposal Commit Mapping 和 Reindex Outbox；本任务不执行文件/Git/索引副作用。

## Requirements

- Approval 新增服务端 Git HEAD 绑定；历史 NULL 可读但不可 Apply。
- Proposal 增加 `version`，数据库只允许受控状态转移并要求 `version=old+1`。
- Writeback Execution 绑定 Workspace/Run/Node/Proposal/Revision/Approval、两份 Authorization、Target/Hash/Git HEAD、幂等键和恢复状态；正文、Credential、绝对路径不得入库。
- `(workspace,idempotency_key)` 和 `(proposal,revision)` 保证单逻辑执行；完整绑定相同才可重放。
- Commit Mapping 唯一关联 Execution、Proposal Revision 和 Git Commit。
- Publish 必须在一个事务内写 Mapping、Execution=verifying、Proposal=verifying 和 `retrieval.revision.reindex_requested` Outbox。
- 索引事件只保存稳定引用和版本请求，不保存正文或猜测 Embedding 维度。
- 状态机、交叉绑定、不可变字段和删除限制由领域规则与数据库约束双重保护。

## Acceptance Criteria

- [x] `migrations/00009_safe_writeback.sql` 可从空库执行并重复执行，历史数据兼容。
- [x] Domain 定义 Execution/Mapping/Publish/状态机和稳定错误，包含正常/非法转移测试。
- [x] Repository 实现 create/replay、checkpoint、failure/manual recovery、publish/replay 和查询。
- [x] PostgreSQL 测试覆盖 Approval Git HEAD、Proposal version、外键/交叉 Trigger、幂等冲突、非法状态、Mapping+Outbox 原子性和不可变/删除拒绝。
- [x] 不新增文件/Git/索引副作用或假完成路径；Proposal 最多进入 verifying。
- [x] `go test -race ./internal/changecontrol/...`、`go vet ./...`、真实 PostgreSQL 和 `git diff --check` 通过。

## Out of Scope

- LocalFS CAS、Git CLI 写操作、Application Saga 和真实 Retrieval Consumer；由 M5-04B/C/D 与 M6 实现。
