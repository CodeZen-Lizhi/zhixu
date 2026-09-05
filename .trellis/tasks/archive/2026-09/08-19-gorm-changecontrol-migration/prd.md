# Change Control Repository 迁移到 GORM

## Goal

为 Change Control 主 Repository 与 Approval Dispatch 增加未接生产 Composition 的 GORM sibling，保持 Proposal、Revision、Approval、Authorization、Safe Writeback、Workflow/River 与 Server Event 的现有 PostgreSQL 原子性。

## Scope

- `internal/changecontrol/adapter/postgres`：Proposal/typed Proposal、Revision/history/fence、Approval、Authorization、Safe Writeback、Workflow cancellation safety。
- `internal/changecontrol/adapter/postgres` 额外提供 owner-owned scoped Knowledge Proposal 能力，供 Graph Candidate Confirm 在调用方同一 `foundation.TransactionScope` 内创建或精确读取 immutable Revision 1；Graph 只定义 consumer Port，不复制 `change_control.*` SQL。
- `internal/changecontrol/adapter/approvaldispatchpostgres`：Approval decision、Proposal-to-Run binding、Workflow Start/Outbox、River Job 与 rejected Server Event 的跨 Schema UoW。
- 只新增 staged GORM 构造与必要的本 owner 私有 persistence helper；保留 legacy pgx API、测试基线和所有 `cmd/**` wiring。
- Schema、migration、文件/Git side effect、Tools writeback audit、Knowledge/Graph/Retrieval/Artifact 等下游 owner 不在本 child 修改范围。

## Requirements

- 主 GORM Repository 与 Approval Dispatch 均从一个 `*platformpostgres.Pool` 获取 GORM root 和 `foundation.UnitOfWork`，不得单独 `gorm.Open`、创建第二连接池或接受可错配的 root/UoW。
- 多语句写入只经 `UnitOfWork.Within`；跨 owner 事务只调用已有 `foundation.TransactionScope` Port：Events `AppendScoped`、Workflow `StartScoped`。不得把 `*gorm.DB`、`*sql.Tx`、`pgx.Tx` 或 `any` 暴露到 Domain/Application 新接口。
- scoped Knowledge Proposal 方法只解包调用方 live scope，不自行开启、提交、回滚事务，也不回退 root；新建行写入 canonical `current_revision_id=revision.id`，历史 `current_revision_id IS NULL` 仍按 immutable Revision 1 精确读取。
- 保持 Proposal/Approval/Revision、双 Authorization、Writeback Execution/Commit/Outbox、Workflow/Server Event 的锁序、数据库时间、CAS、幂等、response-loss 和失败回滚语义。
- `Approve`、`AppendRevision` 和 rejected dispatch 的 Server Event 必须与所属业务事实同一事务提交；事件依赖可选语义与 legacy 一致。
- Approval Dispatch 的 Workflow scoped runtime 必须已绑定同 Pool 的 scoped River producer 与非 no-op enqueue fence；本 child 不构造或绕过 Workflow/Model Settings 的 admission policy。
- Audit 已有 scoped Port，但当前两个 PostgreSQL Adapter 没有直接 Audit 调用；Tools Safe Writeback audit 继续留在 Application/Tools owner，禁止为了满足依赖表而制造第二套审计写入。
- 保留 migration/trigger、Workspace 条件、严格 JSON、Credential 只哈希、错误分类、context cause、取消、回滚和敏感日志策略；禁止 AutoMigrate、association、Preload、Save 和隐式时间。
- 使用现有测试文件和局部编译验证；TODO 9 交付前该 child 仅保留 staged 实现。当前仍不得切换生产实现或删除 legacy。

## Acceptance Criteria

- [x] GORM sibling 覆盖 Change Control 主 Repository 的现有 Proposal/Revision/Approval/Authorization/Writeback 端口，状态机与锁序不变。
- [x] GORM Approval Dispatch 通过同一 opaque scope 原子维护 Approval、Proposal binding、Workflow facts、River Job 和可选 rejected Event。
- [x] 双授权消费、Revision supersede fence、幂等与事件原子写有冻结基线与静态审查证据；真实 PostgreSQL 精简门禁覆盖 Proposal/Approval 幂等冲突、scoped commit/rollback，以及 Approval/Workflow/River 同事务绑定。
- [x] Domain/Application 不新增底层数据库类型，生产 Composition 仍只构造 legacy pgx，实现可独立回滚。
- [x] Change Control scoped Knowledge Proposal 能力可在调用方同一 UoW 中 create-or-exact-load typed Proposal/Revision 1，并拒绝 nil、foreign type 与 stale scope；该 owner Port 使后续 Graph GORM child 无需直接拥有 Change Control 表写入。
- [x] Go/SQL/Trellis Review、`git diff --check`、受影响包既有 test/vet 和 task 校验通过；全量 integration race、response-loss、EXPLAIN 与 cmd compile 按风险触发。

- [x] TODO 9 Testcontainers 工厂可用；本 child 未切换生产 Composition 或删除 legacy，TODO 3/Final 收口保持独立。

## 2026-09-01 测试范围调整

按父任务精简政策，Change Control 不再默认要求完整 response-loss/故障/取消/EXPLAIN 矩阵。现有领域/legacy 基线与 Go/SQL 静态审查继续约束双授权、锁序、状态机和权限；新增 Testcontainers 场景验证 GORM 主路径、幂等冲突、同 scope commit/rollback 和 Approval/Workflow/River 原子组合。

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.

## Dependencies

- 直接实现依赖：`gorm-platform-transaction-foundation`、`gorm-workflow-migration`、`gorm-events-migration`；TODO 9 fully fenced Workflow fixture 还依赖 `gorm-modelsettings-migration` 提供 scoped enqueue fence。
- `gorm-audit-migration` 是父依赖图中的应用审计前置，但当前 Change Control PostgreSQL Adapter 没有直接 Audit import；本 child 只验证不破坏现有 Tools audit 边界。
- Knowledge、Graph、Retrieval 与 Artifact 依赖本 owner 的 staged 事实/Port；本 child 不提前修改这些消费者。
- Graph Candidate Confirm 的 staged GORM 实现以前置方式依赖本 child 的 scoped Knowledge Proposal create/initial-read；本 child 不修改 Graph 代码或生产接线。
