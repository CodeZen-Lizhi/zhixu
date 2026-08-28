# Capture 最终规划 Review

## 独立 Review 发现与修复

- P0/P1：确认 Profile 不能复用现有 `ModelRunTxFinalizer(any)`。唯一 Agent PostgreSQL 实现只接受 `pgx.Tx`，而 Foundation GORM UoW 是 `*sql.Tx`；规划改为 Capture Core先 staged，Profile closure 等待 Agent scoped Port，禁止双事务和 SQL复制。
- P1：冻结 Agent owner 的最小 `ScopedModelRunFinalizer`：`GetModelRunScoped`、`GetModelRunRecordScoped`、`FinalizeModelRunScoped`。明确 live scope、for-update、Model Call稳定排序、CAS/exact replay及不提交/回滚事务。
- P1：补齐 TODO 9 fixture 生命周期。每个子用例先用 raw pool迁移独立数据库，保存 ConnString并关闭 raw pool，再打开唯一完整平台 Pool；cleanup 先关平台 Pool再 force-drop。
- P2：补上 `ProfileBatchReader` 静态断言，避免遗漏 `GetProfiles` 的三次 bounded set query契约。
- P2：明确 GORM integration从同一平台 Pool创建 Workspace `GORMRepository`，以真实 `ScopedSourceWriter` 注入 Capture，不能用 mock替代跨 owner原子回滚证据。
- P1：补齐 `planning-review.md` 与 `baseline-validation.md`，确保 implement/check context可被 Trellis校验和注入。

## 已确认正确的规划边界

- Capture Core 的 Create/MaterializeURL由 Capture UoW拥有，并让 Workspace writer加入同一 scope。
- Outbox CTE、DB time、SKIP LOCKED、Capture -> Attempt锁序、CAS、receipt replay与 Profile既有锁序均已冻结。
- Profile数组使用 `pq.Array`，JSONB使用校验后的 string carrier；复杂查询保留参数化 Raw SQL。
- production wiring、legacy pgx、Schema和文件发布协议均保持不变；TODO 9前无 selector、双写或 fallback。

## 已接受限制

- Foundation TransactionScope没有 Pool identity。当前可拒绝 nil、非平台和 stale scope，但不能在 Adapter内识别 active foreign-Pool scope；Composition和 TODO 9 fixture必须保证同池，严格 affinity需 Foundation后续能力。
- `ZHIXU_TEST_DATABASE_URL` 未配置，真实 PostgreSQL上的 GORM绑定、锁、trigger、SQLSTATE、commit/rollback、并发与计划仍是 TODO 9，不能由 compile-only代替。
- Capture Core staged完成后，Profile前置和 TODO 9未完成时 child仍保持 `in_progress`。

独立 Go/调用链与 SQL/事务规划 Review 均完成；修复上述问题后未发现剩余阻止用户审批的 P0/P1/P2 规划缺口。
