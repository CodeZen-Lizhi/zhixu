# Events Store GORM 迁移设计

## 1. 目标与阶段边界

本 child 将 Server Events 的 Workspace 水位、逻辑保留边界、有界 SSE 重放和调用方事务追加实现为 staged GORM 路径，并新增不暴露 `any`/pgx/GORM 的 opaque transaction Application Port。TODO 9 真实 PostgreSQL 门禁通过前：

- 保留 `Store`、`NewStore` 和 `Appender.AppendTx(any)`；
- 新实现不接入 `cmd/**` 或任何跨模块 producer；
- 不删除 pgx 文件、不修改 migration/trigger/index、不完成或归档任务；
- 不增加运行时 selector、双读、双写或 fallback。

## 2. 数据、水位与游标事实

`ops.server_event` 由 `00020_rag_conversation_sse.sql` 建立，后续 migration 仅新增专用索引或 producer trigger。必须保持：

- `seq` 是全局 `bigserial`，单个 Workspace 只保证严格递增，允许其他 Workspace 消耗序列造成 gap；
- `CurrentWatermark` 是该 Workspace 表内全部行的 `MAX(seq)`，包含已经逻辑过期的行；
- `EarliestRetained` 与 `ListAfter` 使用数据库 `CURRENT_TIMESTAMP`，无 retained row 返回 `nil`；
- `ListAfter` 固定 `workspace_id`、`seq > cursor`、`seq ASC` 和 `1..100` Limit；
- Event 的 `OccurredAt` 来自 caller，Adapter 只规范化为 UTC 微秒；数据库时间只拥有 retention 判定；
- 读取后继续复核 Workspace、严格递增和 Domain JSON 白名单，一行损坏时整页失败而不返回部分结果。

## 3. Application transaction Port

在不破坏 17 个 legacy 生产调用点的前提下新增：

```go
type ScopedAppender interface {
    AppendScoped(context.Context, foundation.TransactionScope, domain.AppendRequest) (domain.ServerEvent, bool, error)
}

type ScopedStore interface {
    Store
    ScopedAppender
}
```

旧 pgx Store 继续实现 `Store` 和 `Appender`；新 GORM Store 实现 `Store` 和 `ScopedAppender`，不实现 legacy `Appender`。后续 owner child 分别把 `AppendTx(any)` 迁到 `AppendScoped`，Final 才删除 legacy Port。`foundation.TransactionScope` 是 Application 唯一可见的事务类型。

## 4. Staged GORM Store

新增：

```go
NewGORMStore(*platformpostgres.Pool) (*GORMStore, error)
```

- 构造器拒绝 nil Pool，并只从该 Pool 获取共享 `*gorm.DB` root；禁止自行 `gorm.Open`、`sql.Open` 或建立第二 pgx pool；
- `CurrentWatermark`、`EarliestRetained`、`ListAfter` 在共享 root 上执行只读 Raw SQL；
- `AppendScoped` 只通过 `platformpostgres.GORMTransaction(scope)` 解出 active transaction，不 commit/rollback、不 fallback 到 root、不另开事务；
- Events 没有 Store-owned standalone append 业务契约，因此本 child 不新增 `Append` 或自行使用 Unit of Work；事务生命周期始终由 owner 调用方拥有；
- 平台 scope 当前不携带可比较的 Pool identity：nil、非平台实现和失效 scope 必须 fail closed；来自另一平台 Pool 的 active scope 当前无法由 Events Adapter 静态识别，同池事实由后续 owner Composition 和 TODO 9 fixture 保证。若要求运行时拒绝 cross-pool scope，需回到 Foundation 单独增强 scope owner identity。

## 5. Read SQL 与资源生命周期

所有读取保留 PostgreSQL 专属、固定列序和参数化 Raw SQL：

| 操作 | SQL 形状 | 关键契约 |
| --- | --- | --- |
| Watermark | `COALESCE(MAX(seq),0)` + Workspace | 包含逻辑过期行，不读全局 sequence |
| Earliest | `MIN(seq)` + Workspace + `expires_at>CURRENT_TIMESTAMP` | 无 retained row 为 nil |
| List | Workspace + `seq>?` + retained + `ORDER BY seq ASC LIMIT ?` | Limit 1..100、无 OFFSET、无 N+1 |

GORM 路径使用 guarded `Raw(...).Row()`/`Rows()`：先检查 statement error 和 nil handle；Rows 必须 `defer Close()`、循环 Scan、末尾检查 `rows.Err()`。共享 scanner 继续按显式列序读取 nullable UUID/text/time 与 `payload_summary::text`，legacy/GORM 只在 placeholder 和 scanner carrier 层不同。

## 6. Append 与 exact replay

`AppendScoped` 固定步骤：

1. 把 caller `OccurredAt` 规范化为 UTC 微秒并执行既有 Domain 校验；
2. 将安全摘要编码为 JSON，使用单参数 text `driver.Valuer` 绑定 `?::jsonb`；
3. 在 caller transaction 执行 `INSERT ... ON CONFLICT (workspace_id,source_event_ref) DO NOTHING RETURNING`；
4. INSERT 成功后仍执行完整 persisted binding 对比；
5. 无返回行时按 `(workspace_id,source_event_ref)` 在同一事务重读 winner；
6. 仅当 Workspace、两个 optional ID、type/ref/version、summary、schema/source、occurred/expiry 全部一致时返回 replay；否则返回稳定 conflict；
7. 由 caller 决定 commit/rollback。

GORM `?` 每次出现都会消耗一个参数；SQL 中 `occurred_at` 与 `expires_at` 两次使用同一规范时间时必须传两次该值，不得用 Go 预算 expiry 改变数据库表达式。并发幂等继续依赖唯一索引，不新增 advisory lock。

## 7. JSON、nullable、错误与安全

- 新增私有 JSONB carrier；`Value()` 验证 JSON 后返回 string，避免 `[]byte` 被 pgx stdlib 推断为 `bytea`；
- optional UUID 继续以 nullable text 读取并经 Domain ID parser 校验；不使用 GORM zero value、association、soft delete、Hook 或自动时间；
- no-row helper 同时识别 `pgx.ErrNoRows`、`sql.ErrNoRows` 与 `gorm.ErrRecordNotFound`；
- 保留 `23503/23514 -> SSE_APPEND_BINDING_INVALID`、`23505 -> SSE_APPEND_CONFLICT`、`40001/40P01 -> retryable SSE_STORE_UNAVAILABLE`，不扩大 SQLSTATE 范围；
- cancel/deadline/`sql.ErrTxDone` 保留内部 cause 和稳定安全 code；未知错误不得暴露 SQL、参数、JSON、DSN、正文、Secret 或绝对路径；
- 不调用 `AutoMigrate`/`Migrator`，Schema、约束、索引和 trigger 只由 Goose migration 拥有。

## 8. Retention 与物理清理边界

仓库当前没有生产 `ops.server_event` 物理清理 Port/Worker；24 小时策略仅由 read SQL 的 `expires_at > CURRENT_TIMESTAMP` 实现。现有 watermark 来自表内 `MAX(seq)`，直接删除过期行会使 Workspace 水位回退，并可能把旧 cursor 从 expired 误判为 future。

因此本迁移明确不新增 DELETE。若后续产品要求物理清理，必须另立任务设计持久 per-Workspace high-water fact、批量上限、锁与并发、执行计划、恢复和发布迁移，不能由 ORM 等价迁移顺带实现。

## 9. TODO 9、发布与回滚

TODO 9 在同一 disposable database 和同一 `platformpostgres.Pool` 上构造 legacy DB、GORM root 与 Unit of Work，原位扩展现有 integration tests，验证：

- Workspace 交错 seq、gap、expired/retained 边界、空 Workspace、页大小 1/100 和 corrupt row 无 partial；
- owner 行与 `AppendScoped` 同事务 commit/rollback，nil/非平台实现/stale scope、cancel/deadline 和连接释放；
- JSONB、optional UUID、UTC 微秒、24 小时 expiry、exact replay、binding conflict 和 response-loss 后新事务 replay；
- 两个独立事务的 exact/conflicting claim、真实 FK/CHECK/SQLSTATE 与 Serializable retry；
- GORM Store 驱动真实 SSE Handler，trigger producer 事件可重放且摘要不泄漏；
- watermark/earliest/list 的目标索引和有界执行计划。

TODO 9 前生产始终使用 legacy pgx，回滚只删除 staged GORM/scoped Port 并还原本 child 的共享 helper，Schema 与运行行为不变。TODO 9 后仍由 Final 统一切 Composition 和删除 legacy Port。
