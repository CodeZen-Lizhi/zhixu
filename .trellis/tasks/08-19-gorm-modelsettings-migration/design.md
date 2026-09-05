# Model Settings GORM 迁移设计

## 1. 目标与阶段边界

Model Settings Repository 是 Revision、Audit、双进程热激活、Runtime、Participant、Workflow admission 与 Managed Ollama preparation 的原子边界，不是普通 CRUD。迁移采用 sibling staging：新增 `GORMRepository` 和 GORM Bootstrap，legacy `Repository`/`Bootstrap`/pgx 端口及生产接线保持不变。

TODO 9 真实 PostgreSQL 门禁通过前：

- 不改 `cmd/api`、`cmd/worker`、`cmd/modelctl`、migration 或现有 integration fixture；生产继续使用 legacy `Bootstrap(database.DB())` 和 `riveradapter.EnqueueFence`。
- 不双写、不加 selector/fallback，不将 `pgx.Tx`、`*sql.Tx`、`*gorm.DB` 或 `any` 暴露到 Application/Domain。
- 新路径只从一个完整 `*platformpostgres.Pool` 取得 GORM root 与 Unit of Work；Audit、Local Runtime 和 Model Settings 必须由同一 Pool 构造。
- Foundation scope 当前没有 Pool identity，能够拒绝 nil、异构和失活 scope，但不能识别另一 Pool 的活跃 scope；同池是 Composition/TODO 9 硬约束，不能伪报为运行时已验证。

## 2. Schema 与安全事实

Schema 唯一事实源为：

- `00064_model_settings.sql`：append-only `ops.model_settings_revisions`、singleton `ops.model_settings_state`、`ops.model_settings_runtime` 与 trigger。
- `00078_model_settings_chat_api_style.sql`：Chat API style 前向扩展。
- `00079_model_settings_hot_activation.sql`：双角色 Participant、activation phase、runtime mutation gate、Workflow claim fence 与 trigger。
- `00080_managed_ollama_runtime.sql`：Managed Ollama runtime/hold/operation、security-barrier view、SECURITY DEFINER commands 与最小权限。
- `00065`/`00066`：Workflow、Retrieval、Agent 对 revision/runtime provenance 的数据库 guard。

禁止 AutoMigrate/Migrator、GORM association/preload/save、hook、soft delete、隐式时间戳或修改既有 migration。Revision credential 只持久化 key ID、nonce、ciphertext；Keep 必须按新 revision/target AAD 解密后重新加密。错误、日志、Audit、String/GoString 与公共 DTO 不得包含 endpoint、明文、nonce、ciphertext、DSN 或 instance ID。

## 3. Scoped 跨 owner 契约

### 3.1 Settings Audit

Application 新增：

```go
type ScopedSettingsAuditAppender interface {
    AppendModelSettingsChangeScoped(context.Context, foundation.TransactionScope, ModelSettingsChange) error
}
```

`GORMSettingsAuditAppender` 接收 `auditapplication.ScopedAppender`，在 caller scope 内构造同一份 redacted Audit event 并调用 `AppendScoped`。`SaveDesired` 的 revision insert、state CAS、persisted readback 和 Audit append 由同一 UoW 提交；Audit 失败必须整体回滚。legacy `SettingsAuditAppender(any)` 保留到 Final。

### 3.2 Local Model Runtime

GORM Repository 只接收 `localmodelruntime.ScopedTxLifecycle`。在 activation UoW callback 内调用 `WithScope(scope)`，再执行 seed/read/complete。该 TxStore 不拥有 commit/rollback，也不得 fallback 到 root。Start 的 state preparing 与 preparation seed、Fail/Finalize/Recover 的 state 终态与 operation/hold 收尾必须同事务。

### 3.3 Workflow enqueue fence

`GORMRepository` 实现 `riveradapter.ScopedEnqueueFence`：

```go
CheckEnqueue(context.Context, foundation.TransactionScope) error
```

它只 unwrap caller scope，在该事务对 singleton state `FOR SHARE`；`idle|failed|preparing` 允许，其余 activation phase 阻断。它不拥有事务，不允许 no-op。Workflow 的 scoped River inserter随后使用同一 scope 内的 `*sql.Tx` 插入 job。

## 4. GORM Repository 结构

建议文件与职责：

- `gorm_core.go`：`GORMRepository`、独立 GORM options、Pool/UoW、readiness、Raw Row/Rows guard、no-row/context/SQLSTATE 分类、within helper。
- `gorm_revision.go`：SaveDesired、ResolveDraft、LoadRevision 与 scoped Audit。
- `gorm_rollout.go`：legacy leased rollout 状态机。
- `gorm_runtime.go`：Runtime 注册/心跳/availability/phase CAS 与 scoped enqueue fence。
- `gorm_participant.go`：Participant 注册/心跳/transition CAS。
- `gorm_activation.go`：hot activation 全流程与 scoped Local Runtime。
- `gorm_snapshot.go`：单一 RepeatableRead+ReadOnly snapshot。
- `gorm_audit.go`：redacted scoped Audit adapter。
- `runtime/bootstrap_gorm.go`：独立 staged Bootstrap；现有 `bootstrap.go` 不改。

构造器接收完整 Pool：

```go
NewGORMRepository(*platformpostgres.Pool, ...GORMOption) (*GORMRepository, error)
WithGORMSecretSealer(application.SecretSealer) GORMOption
WithGORMScopedSettingsAuditAppender(application.ScopedSettingsAuditAppender) GORMOption
WithGORMScopedLocalModelLifecycle(localmodelruntime.ScopedTxLifecycle) GORMOption
```

SecretSealer 与 scoped Audit 是必需依赖；Local Runtime 只在 managed 模式注入。每个 option 必须拒绝 nil/typed-nil、重复配置和无效依赖，便于 TODO 9 注入 Audit 故障实现验证原子回滚。`BootstrapGORM` 使用同池真实实现，测试 fixture 可替换 scoped appender/lifecycle，但不得绕过 scope。GORM Repository 实现现有六个 Application Store interface；业务校验、scanner、secret AAD 和状态机规则优先复用 legacy 包内 helper，新增 helper 只处理 GORM transaction/Row/Rows 形状。

## 5. 事务、锁序与数据库时间

所有状态写入使用 `Pool.UnitOfWork().Within`；callback 内只用 `platformpostgres.GORMTransaction(scope)`。`ResolveDraft`/`LoadRevision` 是 root read；`Snapshot` 必须使用 `RepeatableRead + ReadOnly` 的同一 UoW。

热激活固定锁序：

```text
ops.model_settings_state FOR UPDATE
  -> ops.model_settings_runtime WHERE role IN ('api','worker') ORDER BY role FOR UPDATE
  -> ops.model_settings_rollout_participant WHERE role IN ('api','worker') ORDER BY role FOR UPDATE
```

单角色路径保持 State -> Runtime(role) -> Participant(role)。不得把 Participant 提前或省略排序。所有 lease、freshness、heartbeat、recovery 与 ordering 继续用 `clock_timestamp()`；不使用 Go/GORM NowFunc。

所有 `UPDATE ... RETURNING`、`IS NOT DISTINCT FROM`、`FOR UPDATE/FOR SHARE`、interval、nullable UUID、trigger-sensitive SQL 使用参数化 Raw/Exec 与显式 casts。无行必须同时识别 `sql.ErrNoRows`、legacy `pgx.ErrNoRows` 和必要的 `gorm.ErrRecordNotFound`，并映射到原方法的稳定 conflict/not-found/corrupt code，不能统一归为 unavailable。

## 6. 错误、取消与资源

- GORM 平台保持 `TranslateError=false`，SQLSTATE 通过 `*pgconn.PgError` 分类；保留原 cause 供 `errors.As`。
- caller cancel 优先于 deadline；同时保留 `ctx.Err()` 与不同的 `context.Cause(ctx)`，使 `errors.Is` 成立。
- callback 成功后的 commit error 不重放写操作、不返回未确认结果；按既有方法语义返回稳定 dependency/manual-recovery 类错误。
- Raw `Row()` 前检查 statement error/nil row；多行查询始终 `Close` 并检查 `Rows.Err`。未知数据库错误只暴露稳定码和安全类型信息。
- `isNoLocalPreparation` 扩展识别 `sql.ErrNoRows`，仅在 legacy 已允许缺失的 Fail/Finalize/Recover 分支忽略；其他缺失保持 fail closed。

## 7. Staged Bootstrap 与生产边界

新增 `BootstrapGORM(ctx, pool, cfg, telemetry...) (GORMBootstrapResult, error)`：

1. 创建 sealer 或既有 unavailable sealer。
2. 从同一 Pool 创建 Audit `GORMStore`。
3. managed 模式从同一 Pool 创建 Local Runtime `GORMStore`。
4. 构造 `GORMRepository`、Service、SettingsManager、TestLifecycle 和 LoadedSettings。

结果持有 `*GORMRepository` 与 GORM LocalModelStore。该入口仅用于 staged 编译/TODO 9，不替换现有 `BootstrapResult.Repository *Repository`，不进入 legacy `[]riveradapter.EnqueueFence`，不修改 `cmd/**`。

## 8. 回滚与 Final handoff

TODO 9 前回滚仅删除本 child 新增的 GORM/scoped/Bootstrap 文件；legacy pgx、Schema 和生产进程不变。TODO 9 后 Final 负责同时切换 API/Worker/modelctl Composition、Workflow scoped inserter/fence、删除 legacy `any`/pgx allowlist；禁止模块 child 单独提前切线。

## 9. TODO 9 真实 PostgreSQL 门禁

本节保留为完整 hard-contract 目录；执行顺序和验收阻断范围按父任务 `research/lean-test-policy-2026-09-01.md` 收敛，仅对直接改动触发的风险项要求代表性专项，不恢复无差别全矩阵。

TODO 9 到位后只修改现有 integration 文件并原位参数化 legacy/GORM implementation，不新增 fixture 或测试文件。每个实现用独立已迁移数据库；迁移完成后用 `platformpostgres.Open` 得到完整 Pool，legacy 用 `Pool.DB()`，GORM/Audit/Local Runtime/UoW 全部使用同一 Pool。

必须验证：

- SaveDesired replace/keep/clear、AAD/密文替换、expected revision CAS、Audit 失败整体回滚和 redaction。
- Snapshot RepeatableRead+ReadOnly、并发写下无混合快照、DB-time freshness。
- Start/replay/renew/advance/commit/ack/finalize/recover 全流程，active 只在 Commit 推进。
- State -> Runtime -> Participant 并发锁序、角色 takeover、lease/CAS、无死锁。
- Local preparation seed/complete/hold release 与 activation 同 commit/rollback；缺失 operation 分支与 legacy 一致。
- scoped enqueue fence + River insert 同事务回滚/提交，并由 pgx Worker 消费。
- 真实 casts/JSON/nullable/time、trigger SQLSTATE、cancel/deadline、stale scope、commit response loss、连接释放和目标查询计划。

“未设置 `ZHIXU_TEST_DATABASE_URL` 时只允许 integration compile；不得勾选 PRD AC、完成或归档”是旧基线条件。2026-09-01 已由 `testdb` Testcontainers 实测主路径；当前按父任务精简政策的主路径与直接风险触发项判定 child 验收，生产切线仍由 Final 负责。
