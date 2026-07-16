# M5-03 Write Authorization 签发、校验与一次性消费

## Goal

补齐 Proposal/Approval 后的服务端短期 Write Authorization：绑定 Workflow Run/Node、Proposal/Revision/Approval、Change Hash 与目标版本，支持过期、撤销、并发一次性幂等消费；不执行文件/Git/索引副作用。

## Requirements

- 授权只能由服务端 Change Control Application 在 Proposal 已批准、Revision/Approval/Change Hash 一致且目标基线仍匹配时签发。
- 授权必须绑定 `workspace_id`、已持久化的 `workflow_run_id`、`node_run_id`、`proposal_id`、`revision_id`、`approval_id`、`approved_change_hash`、`target_version`、能力集合、幂等键和过期时间；不得接受模型、Eino Context、Session 或 API Token 自行构造的授权。
- 授权凭据只保存不可逆摘要与服务端绑定字段；本任务不把凭据发送给模型，不执行文件、Git、索引或其他外部副作用。
- 授权签发必须有最短有效期上限，不能签发给已拒绝、已失效、Needs Revision、已消费或已撤销的 Proposal。
- 消费必须在单个 PostgreSQL 事务内原子校验所有不可变绑定、当前 Proposal/Approval 状态、Revision 的目标版本和有效期；同一幂等键及完全相同绑定可重放既有消费结果，不同绑定必须返回冲突。
- 消费成功只记录服务端授权事实，不代表文件写回成功。当前文件哈希检查可作为消费前快速失败，但不能替代跨文件系统原子保证；M5-04 Safe Writeback 必须在实际替换点重新执行目标版本 CAS。过期、撤销、绑定不匹配和已消费均返回稳定错误码并保留可审计状态。
- 实际 Apply Workflow 的创建、文件/Git 写回、索引和补偿留在后续 M5-04，不在本任务伪造成功路径。

## Acceptance Criteria

- [x] 领域模型、Repository 和迁移定义 Tool Authorization 的状态、绑定字段、版本和幂等不变量。
- [x] 签发单测覆盖 approved 正常路径、未批准/拒绝/Needs Revision、哈希或目标版本冲突、过期上限和缺少持久化 Run/Node。
- [x] 消费单测覆盖字段篡改、过期、撤销、重复同绑定重放、重复不同绑定冲突和并发单成功。
- [x] PostgreSQL 集成测试覆盖 workspace/run/node/proposal/approval 外键与交叉约束、唯一幂等键、乐观锁/原子消费和重复迁移。
- [x] 不新增 HTTP Token 返回或模型可见凭据；API/错误契约和安全文档同步说明服务端授权边界。
- [x] `go test -race ./...`、`go vet ./...`、真实 PostgreSQL 集成、OpenAPI/Compose 检查和 `git diff --check` 通过。

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.
