# Workspace 最终规划 Review

## 已修复

- P0：明确 legacy Capture 的 `pgx.Tx` writer 不能由 Workspace child 直接替换；新增 Application scoped Port，Capture 后续迁移。
- P0：root rebind 的 history、Workspace/control CAS 与 Audit `AppendScoped` 固定为同一 UoW。
- P0：新增独立 staged GORM Runtime Composition，现有 concrete ProcessComposition 和 cmd wiring保持不动。
- P1：将原“连接级锁”纠正为 `pg_advisory_xact_lock` 事务级 advisory lock，并冻结 Registry/Control/Runtime/Git 锁序。
- P1：Workspace TODO 9 不再等待 Capture child。它只验 scoped writer 的 Source/Artifact/Version caller-owned UoW语义；Capture/Outbox/receipt/URL 的共同事务是下游 Capture child验收，不阻断本 child。

## 已接受限制

- Foundation scope 没有 Pool identity。当前只能拒绝 nil、非平台和 stale scope；active foreign-Pool scope 由同 Pool Composition避免。运行时 affinity 拒绝需要 Foundation 单独增加能力。
- `ZHIXU_TEST_DATABASE_URL` 未配置，真实 PostgreSQL 等价、并发、trigger、commit failure和 EXPLAIN仍为 TODO 9，不能由 compile-only 替代。

独立 Go/调用链、SQL/Schema、规划和最终依赖闭环 Review 均完成；未发现剩余阻止用户审批的规划问题。
