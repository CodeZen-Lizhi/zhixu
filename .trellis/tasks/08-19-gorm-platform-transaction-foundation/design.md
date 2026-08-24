# GORM 平台与事务基础设计

## 1. 目标与边界

本任务只建立后续 28 个模块共同使用的数据访问基础，不迁移业务 Repository，不修改 `cmd/**` Composition，也不删除旧 pgx 事务路径。代码可以进入仓库，但 TODO 9 真实 PostgreSQL 门禁通过前，本任务保持未完成、不得归档。

本任务负责：

- 固定 GORM、PostgreSQL Driver 与 River database/sql Driver 版本；
- 在现有 `pgxpool.Pool` 上建立唯一 `database/sql` facade 和 GORM root；
- 定义项目自有 opaque transaction scope 与 Unit of Work；
- 提供 GORM 事务内的 River database/sql 插入器；
- 固定安全配置、生命周期、错误保留策略和迁移调用点矩阵。

本任务不负责：

- 改写任何领域/application 契约或业务 SQL；
- 切换 API、Worker 或其他命令入口；
- 将 River Worker、listener、leader election 或 migration 改为 database/sql Driver；
- 改动 Schema 或调用 GORM `AutoMigrate`/`Migrator`；
- 清除全仓现存 `transaction any`/`pgx.Tx`，这些由各模块 child 完成。

## 2. 连接与生命周期

```text
postgres.Open
    |
    +-- pgxpool.Pool                 唯一物理连接池、Ping、最终 Close owner
    |       +-- riverpgxv5           Worker/listener/migration，保持现状
    |       +-- approved pgx paths   pgvector、专用会话锁等 allowlist
    |
    +-- stdlib.OpenDBFromPool
            +-- *sql.DB facade       MaxIdleConns=0，不拥有 pgxpool
                    +-- GORM root
                    +-- riverdatabasesql producer
```

- `Open` 创建 pgxpool 后立即创建共享 `*sql.DB` facade 和 GORM root；GORM 禁止通过 DSN 自建 pool。
- `OpenMigration` 继续只创建 pgxpool。Goose/River migration 已在自身作用域创建并关闭临时 `*sql.DB` facade，不需要 GORM。
- GORM 初始化关闭自动 Ping，保持调用方显式、带超时 `Pool.Ping` 的既有 readiness 语义。
- `Pool.Close` 先关闭 `*sql.DB` facade，再关闭 pgxpool。facade 的 Close 不关闭底层 pgxpool；重复 Close 不应 panic。
- pgxpool 建立后若 facade/GORM 初始化失败，构造函数关闭已创建资源，不返回半初始化实例。

## 3. GORM 配置

平台集中构造 `gorm.Config`，模块不得复制或覆盖：

| 设置 | 固定值/策略 | 原因 |
| --- | --- | --- |
| `SkipDefaultTransaction` | `true` | 写事务只能通过项目 Unit of Work 显式建立 |
| `PrepareStmt` | `false` | 保持真实 `*sql.Tx` 可受控解包，避免 prepared statement 生命周期扩散 |
| `DisableAutomaticPing` | `true` | 保持现有显式 readiness/Ping 行为 |
| `DisableForeignKeyConstraintWhenMigrating` | `true` | GORM 不拥有 Schema |
| `IgnoreRelationshipsWhenMigrating` | `true` | 防止关联推导参与任何误调用的迁移路径 |
| `AllowGlobalUpdate` | `false` | 阻止无条件全表更新 |
| `TranslateError` | `false` | 保留 `pgconn.PgError`、SQLSTATE 和约束名供模块现有错误分类 |
| `NowFunc` | UTC、微秒精度 | 与 PostgreSQL 时间精度对齐；数据库时间不变量仍使用 SQL 时间 |
| Logger | 默认 silent | 不让 SQL 参数绕过项目脱敏；后续若接 slog 必须 parameterized-only |
| Naming | PostgreSQL 标识符上限 63 | 每个 Persistence Model 仍必须显式 schema/table/column |

禁止使用 `AutoMigrate`、`Migrator`、隐式 association save、`gorm.Model` 或 Hook 承载业务不变量。

## 4. 事务契约

在 `internal/foundation` 新增无数据库依赖的契约：

```go
type TransactionScope interface { TransactionScope() }
type TransactionFunc func(context.Context, TransactionScope) error
type UnitOfWork interface {
    Within(context.Context, TransactionOptions, TransactionFunc) error
}
```

- `TransactionOptions` 只包含项目自有 isolation/read-only 语义；不暴露 `sql.TxOptions`。
- `internal/platform/postgres` 的私有 scope 实现同时持有当前 `*gorm.DB` 与 `*sql.Tx`。
- 平台 Unit of Work 用 GORM `Transaction` 建立事务，并在唯一位置校验 `Statement.ConnPool` 确为 `*sql.Tx`。
- 导出的受控 unwrap helper 只属于 infrastructure adapter 边界；Domain/Application 只能看到 `foundation.TransactionScope`。
- 未知、nil、跨平台或已退出回调的 scope 必须 fail closed。scope 不允许在回调结束后复用。
- Read Committed、Repeatable Read、Serializable 和 read-only 显式映射；未知 isolation 在 Begin 前拒绝。
- GORM `TranslateError` 保持关闭。本任务不集中改写各模块已有 SQLSTATE/约束错误语义。

## 5. River 双 Driver

- 现有 `riverpgxv5` Client 继续负责 Worker、LISTEN/NOTIFY、leader election、maintenance 与 migration。
- 新 GORM producer 使用 `riverdatabasesql.New(sharedSQLDB)` 创建 insert-only River Client；其 `InsertTx` 只接收从 opaque scope 受控取得的 `*sql.Tx`。
- 新 inserter 的公开事务参数固定为 `foundation.TransactionScope`，不能接收 `any` 或 `pgx.Tx`；旧 inserter 保持不变，供尚未迁移的模块使用。
- 新 GORM enqueue fence 同样接收 opaque scope；旧 pgx fence 不接入新路径。
- queue、unique states、scheduled time、trace metadata、结果校验和稳定错误码与旧 inserter 保持一致。
- `riverdatabasesql.SupportsListener=false`，严禁用它启动 Worker。真实提交/回滚原子性、pgx Worker 消费、轮询延迟与恢复语义留给 TODO 9。

## 6. 调用点与迁移 owner

| 调用点 | Foundation 结果 | 后续 owner |
| --- | --- | --- |
| Workflow `JobInserter` / `TypedJobInserter` | 保留旧 pgx API；新增 opaque-scope GORM inserter | Workflow |
| Workflow runtime start/state | 不改，记录迁移到新 scope | Workflow |
| Export dispatcher | 不改，禁止在其 GORM 实现继续调用旧 inserter | Export（依赖 Workflow） |
| Retrieval inserter/dispatcher | 不改，记录 COPY/临时表 pgx allowlist | Retrieval（依赖 Workflow） |
| Model Settings `CheckEnqueue` | 不改，后续实现 opaque-scope fence | Model Settings |
| River Worker/listener/migration | 保留 `riverpgxv5` | Final allowlist |
| Platform gitoperation | 专用 session advisory lock 保留 pgx | Foundation allowlist |

在首个业务 GORM Adapter 出现前，仓库不存在可准确匹配的 GORM 调用点静态扫描。本任务用互不兼容的 typed API 和上述 owner 矩阵建立边界；Final 在实际调用点完整后启用全仓 import/调用静态门禁。

## 7. 依赖与供应链

- 固定 `gorm.io/gorm v1.31.2`、`gorm.io/driver/postgres v1.6.2`、`github.com/riverqueue/river/riverdriver/riverdatabasesql v0.40.0`。
- `riverdatabasesql` 必须与现有 River `v0.40.0` 同版本；Worker 的 `riverpgxv5` 版本不变。
- 仓库使用 vendor，必须同步 `go.mod`、`go.sum`、`vendor/modules.txt` 和 vendor 源，并保留许可证。
- 当前 `go.sum` 已有用户改动；依赖更新只能在其现状上增量合并，不得覆盖或回滚。

## 8. 验证与未完成条件

当前可执行：

```bash
go test ./internal/foundation ./internal/platform/postgres ./internal/workflow/adapter/river -count=1 -timeout 60s
go vet ./internal/foundation ./internal/platform/postgres ./internal/workflow/adapter/river
go test -mod=vendor ./internal/foundation ./internal/platform/postgres ./internal/workflow/adapter/river -count=1 -timeout 60s
go mod verify
git diff --check
```

Foundation 通过带 `integration` build tag 的平台验收文件证明共享 facade Close、GORM commit/rollback、GORM+River 原子写入和 pgx Worker 消费；默认单元测试不连接数据库。真实网络层 COMMIT response loss、目标环境连接预算和各业务 owner 的错误分类仍属于 TODO 9/后续门禁，本任务在生产 Composition 接入前保持 `in_progress`。

## 9. 回滚

Foundation 不接入生产 Composition，回滚只需移除新增 GORM root/scope/inserter 和依赖。旧 `Pool.DB()`、`Pool.Begin()`、pgx River Client、Worker/listener 与所有业务 Repository 均保持原状，不需要数据回滚。
