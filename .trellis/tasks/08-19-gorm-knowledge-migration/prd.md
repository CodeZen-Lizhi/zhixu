# Knowledge Repository 迁移到 GORM

## Goal

为 Knowledge 聚合、Timeline/Impact 与 Approval-to-Relation Apply 增加未接生产 Composition 的 GORM sibling，保持 Topic、Claim、Relation、Conflict、Evidence、Timeline Projection、Impact Audit 与 Server Event 的现有 PostgreSQL 原子性。

## Scope

- `internal/knowledge/adapter/postgres`：Knowledge command/read、Evidence eligibility/topic、Timeline/Impact、Projection dispatcher、Approved Relation Apply。
- `internal/knowledge/application`：仅补充 Impact Audit 的 opaque scoped capability；保留 legacy `any` transaction Port 直到 Final。
- `internal/knowledge/adapter/audit`：让现有 ImpactRecorder 同时支持 legacy transaction 与 `foundation.TransactionScope`。
- 只新增 staged GORM 构造与必要的本 owner 私有 persistence helper；保留 legacy pgx API、测试基线和所有 `cmd/**` wiring。
- Schema/migration、Graph/Change Control/Events/Audit owner 实现、Organizing caller-owned pgx transaction、生产 selector/双写不在本 child 修改范围。

## Requirements

- 所有 GORM Repository 从一个 `*platformpostgres.Pool` 取得 GORM root 与 `foundation.UnitOfWork`，不得独立 `gorm.Open`、创建第二连接池或分别注入可错配 root/UoW。
- 多语句写入只经 `UnitOfWork.Within`；跨 owner 原子写只调用 opaque Port：Events `AppendScoped`、Audit `RecordImpactAnalysisScoped`。GORM Composition 必须使用专用 `NewScopedImpactServiceWithAudit`，不得在新接口中暴露 `*gorm.DB`、`*sql.Tx`、`pgx.Tx` 或 `any`。
- 保持 Workspace key-share、幂等 advisory lock、端点/成员稳定锁序、CAS、deferred constraint、严格 provenance、对称 Relation canonicalization、receipt/replay 与 commit-response-loss 语义。
- Approved Relation Apply 必须保持 Candidate-before-Proposal 锁序，并在同一事务维护 Approval/Proposal、Knowledge Relation/Evidence/receipt 与 optional Server Event；基线漂移只能提交 `needs_revision`，普通失败全部回滚。
- Timeline projection 保持 `FOR UPDATE SKIP LOCKED`、单 source 短事务、poison/project CAS；Impact Report 与 `IMPACT_ANALYZED` Audit 必须同一事务提交或回滚。
- 批量读取上限 500，保持 Repeatable Read + Read Only snapshot、Workspace predicate、稳定排序、数组单参数绑定、完整 hydration 与无 N+1。
- 保留 migration/trigger、显式数据库/调用方时间、错误分类、context cause、回滚和敏感日志策略；禁止 AutoMigrate、association、Preload、Save、soft delete、hooks 与隐式时间。
- 使用现有测试文件和局部编译验证；TODO 9 前不得切换生产实现、删除 legacy、勾选完成或归档。

## Acceptance Criteria

- [ ] GORM sibling 覆盖 Knowledge Repository 的 Topic/Claim/Relation/Conflict、bounded read、Evidence、Timeline/Impact 与 Projection 端口，结果和状态机与 legacy 等价。
- [ ] GORM Approved Relation Apply 通过单一 opaque scope 原子维护 Change Control、Knowledge 与 optional Events 事实，锁序、stale 分支和 replay 不变。
- [ ] Impact Report 与脱敏 Audit 在同一 scope 原子提交；GORM 新路径不再通过 `any` 传递事务。
- [ ] 对称 Relation、provenance、deferred constraint、Conflict closure、Timeline projection、幂等/response-loss 和失败回滚在真实 PostgreSQL 上与 legacy 等价。
- [ ] Domain 不新增底层数据库类型，生产 Composition 仍只构造 legacy pgx，实现可独立回滚。
- [ ] Go Review、SQL Review、Trellis Check、`git diff --check`、受影响包 test/race/vet、integration compile 与 cmd compile 通过。

- [ ] TODO 9 不可用时仅保留行为基线或未接入 Composition 的实现，不得勾选完成或归档；TODO 3 仅阻断 Final。

## Notes

- Keep `prd.md` focused on requirements, constraints, and acceptance criteria.
- Lightweight tasks can remain PRD-only.
- For complex tasks, add `design.md` for technical design and `implement.md` for execution planning before `task.py start`.

## Dependencies

- 直接实现依赖：`gorm-platform-transaction-foundation`、`gorm-changecontrol-migration`、`gorm-events-migration`、`gorm-audit-migration`。
- Relation Apply 读取 Graph Candidate 事实，但不调用 Graph GORM Repository；Graph staged migration 反向依赖 Knowledge canonical Relation owner，本 child 只保留现有参数化跨 Schema fence。
- Organizing 仍在 caller-owned pgx transaction 内构造 legacy Knowledge Repository；该调用点由 Organizing/Final 后续迁移，本 child 不提前拆事务。
