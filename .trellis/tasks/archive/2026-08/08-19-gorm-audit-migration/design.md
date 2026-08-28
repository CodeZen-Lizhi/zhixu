# Audit Store GORM 迁移设计

## 1. 目标与阶段边界

本 child 将 Audit 的独立追加、调用方事务追加、单条读取和有界列表实现为 staged GORM 路径，同时新增不暴露
`any`/pgx/GORM 的 opaque transaction Application Port。TODO 9 真实 PostgreSQL 门禁通过前：

- 保留 `Store`、`NewStore`、`Appender.AppendTx(any)` 与 `Recorder.RecordTx(any)`；
- 新实现不接入 `cmd/**`、Workspace、Export、Model Settings、Knowledge、Conversation 或 Tools；
- 不删除 pgx 文件、不修改 migration、不完成或归档任务；
- 不增加运行时 selector、双读、双写或 fallback。

## 2. 数据与行为基线

`ops.audit_event` 由 `00034_learning_ops_auth.sql` 建立，`00081_workspace_root_rebinding.sql` 增加
`transaction_id xid8 NOT NULL DEFAULT pg_current_xact_id()`。表和查询契约为：

- UUID 主键；nullable `workspace_id`；受控 actor/resource/idempotency 文本；`correlation`/`payload` 为 JSONB object；
- `(workspace_id,idempotency_key)` 唯一约束不能覆盖 `workspace_id IS NULL`，因此全局和 Workspace 事件都必须先取得 transaction-scoped advisory lock；
- `(workspace_id,occurred_at DESC,id DESC)` 支撑有界 keyset List；
- UPDATE/DELETE 由 append-only trigger 以 SQLSTATE `55000` 拒绝；
- `transaction_id` 不由 Adapter 传值，必须在调用方同一事务中由数据库默认值生成，才能满足 Workspace rebind 与延迟外键/事务绑定。

领域脱敏、canonical JSON、reserved payload envelope、完整 binding 比较和持久行 fail-closed 继续由现有 Domain/共享 scanner 拥有，GORM 不复制这些规则。

## 3. Application transaction Port

在不破坏 legacy 调用方的前提下新增：

```go
type ScopedAppender interface {
    AppendScoped(context.Context, foundation.TransactionScope, domain.Event) (domain.Event, bool, error)
}

type ScopedReader interface {
    GetScoped(context.Context, foundation.TransactionScope, foundation.ID) (domain.Event, error)
}

type ScopedStore interface {
    Repository
    ScopedAppender
    ScopedReader
    Get(context.Context, foundation.ID) (domain.Event, error)
    List(context.Context, domain.ListQuery) ([]domain.Event, error)
}
```

`Recorder.RecordScoped` 只断言并调用 `ScopedAppender`；`Recorder.ReadScoped` 只断言并调用 `ScopedReader`，供
跨 owner 的 immutable durable closure 验证；既有 `RecordTx(any)` 保持不变。后续模块 child 按 owner迁移到
scoped Port，Final 才删除 legacy `any` Port。GORM Store 不实现 legacy `Appender`，防止 opaque scope 被重新塞入 `any`。

## 4. Staged GORM Store

新增 `GORMStore`，从同一个平台 Pool 派生共享 `*gorm.DB` root 与 `foundation.UnitOfWork`：

```go
NewGORMStore(*platformpostgres.Pool) (*GORMStore, error)
```

- 构造器拒绝 nil Pool，并通过该 Pool 的 `GORM()`/`UnitOfWork()` 派生不可拆分的读写依赖；每个入口继续拒绝失效 root/UoW 与 nil context；
- `Append` 只能通过 `UnitOfWork.Within` 建立事务，再在 callback 内调用内部 scoped append；
- `AppendScoped` 仅通过 `platformpostgres.GORMTransaction(scope)` 使用调用方现有事务，不 commit/rollback；
- `GetScoped` 通过同一受控 unwrap 在调用方事务内读取单条不可变 Audit 事实，不 commit/rollback；
- `Get`/`List` 只读共享 GORM root；
- root 与 Store 自有 Unit of Work 由同一个 `platformpostgres.Pool` 派生，构造签名禁止调用方混配这两项依赖；TODO 9 fixture 仍需验证真实连接和事务可见性。
- 当前 Foundation scope 只携带 opaque GORM/SQL 事务和存活状态，不携带可校验的 Pool identity。因此 `AppendScoped`
  能拒绝 nil、非平台和过期 scope，但不能在运行时识别“来自另一 Pool 的仍活跃 scope”。本阶段以同一 Pool
  构造 Store 与 caller Unit of Work 作为 Composition 硬约束；若需要 Adapter 主动拒绝 foreign active scope，必须回到
  Foundation child 增加不可伪造的 owner identity 和受控匹配 API，不能由 Audit 猜测连接归属。

## 5. SQL 与锁顺序

Audit 使用 PostgreSQL 专属事务原语和显式 JSON 映射，所有路径保留参数化 Raw/Exec，不改写为普通 ORM CRUD：

| 操作 | GORM API | 保留原因 |
| --- | --- | --- |
| advisory lock | `Exec` | transaction-scoped lock 与全局 `NULL` 幂等 |
| idempotency lookup / Get | `Raw(...).Rows/Row` | `IS NOT DISTINCT FROM`、重复行检测、显式 cast |
| append | `Raw(...).Row` | `ON CONFLICT ... DO NOTHING RETURNING` 与无行重读 |
| List | `Raw(...).Rows` | Workspace 精确 scope、tuple keyset、固定排序和 Limit |

`Append`/`AppendScoped` 固定顺序：

1. `event.Redacted()` 并编码 reserved JSON envelope；
2. 生成既有 lock key：全局为 idempotency key，Workspace 为 `<canonical UUID>:<key>`；
3. `pg_advisory_xact_lock(hashtextextended(...))`；
4. `IS NOT DISTINCT FROM` 精确查询并拒绝第二行；
5. 无既有事实时执行单条 `INSERT ... DO NOTHING RETURNING`；
6. insert 无行时再次查询，只有完整 binding 相同才返回 replay；
7. 仅 `Append` 所拥有的 UoW 决定 commit/rollback。

GORM SQL 使用 `?` 占位符并保留 `?::uuid`、`?::jsonb`、`?::timestamptz`。任何动态值都不得进入 SQL 文本。

## 6. JSON、nullable 与 scanner

- 新增私有 `auditJSONB` database/sql carrier；`Value` 返回 canonical JSON string，避免 `[]byte` 被 pgx stdlib 当作 `bytea`；
- `Scan` 只接受 string/`[]byte` 且验证 JSON；读取仍选择 `correlation::text,payload::text` 并复用 `scanEvent`；
- Workspace、可选文本与 cursor ID 使用 nil/string；cursor time 使用 nil 或 UTC 微秒 `time.Time`；
- 共享 no-row helper 同时识别 pgx、`database/sql` 与 GORM sentinel；Raw Row/Rows 必须检查构造错误、nil、Close 和迭代错误；
- 不定义 `gorm.Model`，不映射/写入 `transaction_id`，不使用 Hook 或自动时间。

## 7. 错误、安全与性能

- 保留 foundation error、`23503`/`23514` consistency、`23505` idempotency conflict、`40001`/`40P01` retryable 语义；
- GORM no-row 在 Get 映射 `AUDIT_EVENT_NOT_FOUND`，在幂等 lookup 只表示 not found；
- cancellation/deadline/`sql.ErrTxDone` 保持现有 Audit 的稳定 dependency code/retryability，同时保留内部 cause，TODO 9 对比两条路径；
- 未知 driver 错误只暴露安全类型摘要，不包含 SQL、参数、JSON、DSN、Credential 或绝对路径；GORM logger 继续由平台设置为 discard；
- Append 每次为常数条 SQL，List 严格 `1..200` 且稳定 keyset，无关联预加载、N+1 或无界物化。

## 8. TODO 9 与回滚

TODO 9 在同一 `platformpostgres.Pool` 上构造 `DB()`、`GORM()` 和 `UnitOfWork()`，原位扩展现有 integration test，比较 legacy/GORM 的：

- 全局/Workspace 首写、精确 replay、binding conflict、12 并发单行和预置重复行 fail-closed；
- JSONB 脱敏读写、腐坏 envelope、Get/List、同微秒 keyset、context cancel/deadline；
- UoW commit/rollback、过期 scope拒绝、Workspace rebind `transaction_id` 同事务、`00089` 延迟外键；
- Store 与 caller UoW 必须来自同一 `platformpostgres.Pool`；当前 Foundation 不能识别 active foreign scope，
  因而不得把该场景登记为已被 Adapter fail closed；
- append-only `55000` 与 SQLSTATE 分类、连接释放及查询上界。

回滚只删除 staged GORM/scoped Port 并还原本 child 的共享 helper调整；生产仍使用 legacy pgx，不涉及 Schema 或数据回滚。
