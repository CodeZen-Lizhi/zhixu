# Foundation 静态交付与验证记录

## 当前结论

- Foundation 已交付共享 GORM 根、数据库无关的 opaque transaction scope、受控 GORM/SQL transaction unwrap，以及仅用于事务内插入的 River database/sql producer。
- 本次新增真实 PostgreSQL/River 集成验收，覆盖事务提交/回滚、SQLSTATE、read-only、statement timeout、取消 cause、同事务 River 可见性、pgx Worker 消费、scoped fence 拒绝、提交后 ACK 丢失重放、事务与 Worker 共享单池压力和关闭生命周期。
- 生产 Composition、业务 Repository、旧 `pgx.Tx`/`any` 事务接口、River Worker/listener/migrator 均未切换；本 child 仍保持 `in_progress`，不能把平台验收误报为全量 GORM 迁移完成。

## 已交付边界

1. `postgres.Open` 以现有 `pgxpool.Pool` 为唯一物理池，通过 `stdlib.OpenDBFromPool` 创建私有 `database/sql` facade，再构造共享 GORM root；`OpenMigration` 保持 pgx-only。
2. GORM 配置显式关闭自动 Ping、默认写事务、prepared statements、自动外键/关系迁移和默认 SQL 日志；未引入 `AutoMigrate`。
3. `internal/foundation` 的新增 transaction Port/Scope 不包含 GORM、`database/sql`、pgx 或 `any`。平台层只在受控 unwrap 函数中暴露当前且仍有效的 `*gorm.DB` 或 `*sql.Tx`。
4. River 新 producer 通过 `riverdatabasesql` 接收 GORM 回调中的同一个 `*sql.Tx`；现有 `riverpgxv5` Client、Worker、listener 和 migrator 保持不变。
5. `Pool.Close` 先关闭 SQL facade，再关闭底层 pgxpool，并通过 `sync.Once` 保证幂等；GORM 初始化失败会关闭 facade 与 pool。

## 依赖与供应链

| 依赖 | 固定版本 | 用途 | License |
| --- | --- | --- | --- |
| `gorm.io/gorm` | `v1.31.2` | ORM 与显式事务 | MIT |
| `gorm.io/driver/postgres` | `v1.6.2` | GORM PostgreSQL Dialector | MIT |
| `riverdatabasesql` | `v0.40.0` | GORM/SQL 事务内 River InsertTx | MPL-2.0 |
| `riverpgxv5` | `v0.40.0` | 既有 Worker/listener/migrator | MPL-2.0 |

- `go.mod`、`go.sum`、`vendor/modules.txt` 与 vendor 源已同步；上述模块及间接依赖的 License 文件存在。
- `riverdatabasesql` 与既有 River 固定为同一 `v0.40.0`，没有将不支持 listener 的 SQL driver 用作 Worker driver。
- 本次依赖解析在原有 `go.sum` 用户脏改上追加所需条目，没有回退或覆盖原改动。

## River/事务调用点矩阵

| 当前调用点 | 当前事务契约 | 后续 owner | 切换门禁 |
| --- | --- | --- | --- |
| `internal/workflow/adapter/postgres/runtime_start.go`、`runtime_state.go` | `JobInserter.InsertTx(..., any, ...)`，实际为 `pgx.Tx` | Workflow child | Repository 切换为 GORM UoW 后使用 scoped inserter；保留取消与响应丢失语义 |
| `internal/export/adapter/river/dispatcher.go` | dispatcher 自行 `Begin` 并传 `pgx.Tx` | Export child | 同时迁移 dispatcher 事务 owner 与 typed scoped inserter，禁止跨事务入队 |
| `internal/retrieval/adapter/postgres/dispatcher.go`、`internal/retrieval/adapter/river/inserter.go` | Application/transport Port 暴露 `any`，store 使用 `pgx.Tx` | Retrieval child | 同时替换 Application Port、store UoW 与 transport inserter |
| `internal/modelsettings/adapter/postgres/runtime.go` | `EnqueueFence.CheckEnqueue(..., pgx.Tx)` | Model Settings child | 提供基于 opaque scope 的 fence；禁止以 `nil` fence 绕过 rollout 闸门 |

旧接口在对应 owner 完成迁移前继续留在临时 pgx allowlist；Foundation 不跨模块替换它们。

## 已执行验证

以下命令均通过：

```text
go test -mod=mod ./internal/foundation ./internal/platform/postgres ./internal/workflow/adapter/river -count=1 -timeout 60s
go test -mod=vendor ./internal/foundation ./internal/platform/postgres ./internal/workflow/adapter/river -count=1 -timeout 60s
go test -race -mod=vendor ./internal/foundation ./internal/platform/postgres ./internal/workflow/adapter/river -count=1 -timeout 60s
go vet -mod=vendor ./internal/foundation ./internal/platform/postgres ./internal/workflow/adapter/river
go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker ./cmd/migrate ./cmd/modelctl ./cmd/workspacectl ./cmd/local-model-runtime ./cmd/runtimewait ./cmd/workspaceprobe -count=1 -timeout 60s
go list -mod=vendor ./internal/foundation ./internal/platform/postgres ./internal/workflow/adapter/river
go mod verify
git diff --check
```

真实 PostgreSQL/River（使用临时随机数据库，测试结束后 `DROP DATABASE ... WITH (FORCE)`）另行通过：

```text
ZHIXU_TEST_DATABASE_URL=<disposable PostgreSQL URL> \\
  go test -mod=vendor -tags integration ./internal/platform/postgres -count=1 -timeout 240s
ZHIXU_TEST_DATABASE_URL=<disposable PostgreSQL URL> \\
  go test -race -mod=vendor -tags integration ./internal/platform/postgres -count=1 -timeout 240s
ZHIXU_TEST_DATABASE_URL=<disposable migrated PostgreSQL URL> \\
  go test -mod=vendor -tags integration ./internal/workflow/adapter/river -run '^TestRealRiverDeterministicWorkerSmoke$' -count=1 -timeout 120s
```

集成断言包括：

- GORM commit/rollback、过期 scope fail-closed、重复键 `23505`、read-only `25006`；
- transaction 内 `context.WithCancelCause` 和 active `pg_sleep` 取消，错误链同时保留 `context.Canceled` 与自定义 cause；
- 同一 GORM transaction 写入业务探针并通过 `riverdatabasesql` 插入 Job，未提交时 pgx 连接不可见，提交后由既有 `riverpgxv5` Worker 消费并完成；
- 入队 fence 拒绝和 callback 错误均回滚业务探针与 Job；
- `SET LOCAL statement_timeout` 触发真实 PostgreSQL SQLSTATE `57014`，错误分类不会被 GORM 泛化；
- 真实 PostgreSQL commit 已落地后，确定性注入 ACK 丢失，随后按 River 唯一键 exact replay 返回同一 Job，不重复插入；
- 共享 `pgxpool` + `database/sql` facade 的 bounded concurrent transactions 与 pgx River Worker 同时运行无池饥饿，`MaxIdleConns=0`，重复 `Pool.Close` 幂等且关闭后拒绝新 GORM/UoW/River facade。

静态检查确认：

- 新增 foundation 公共契约不包含具体数据库类型或 `any`。
- 新增平台/River 路径不调用 `AutoMigrate` 或 GORM Migrator。
- `riverdatabasesql` 只用于 scoped producer，现有 Worker/migrator 仍只构造 `riverpgxv5`。
- 新增 `internal/platform/postgres/transaction_integration_test.go`，仅使用 `integration` build tag，不改变默认单元测试或生产 Composition。

## Review 结果

- Go review：发现 scoped fence 的 typed-nil 判断只覆盖 pointer；已统一覆盖 Go 中所有可为 nil 的 Kind，并将集成清理/Worker 停止失败升级为测试失败。2026-08-25 提交前复核又发现 `database/sql` 自动回滚可能让 Commit 返回 `sql.ErrTxDone`，原实现会丢失 caller cancellation/deadline 与自定义 cause；已补齐错误链、增加单元回归并提交为 `d611e7ec`。
- SQL/DB review：生产路径无新增手写 SQL、migration 或 schema 变化；集成 fixture 仅使用常量 SQL 与参数绑定完成验收；同一个 `*sql.Tx` 静态传入 `riverdatabasesql.InsertTx`，未发现注入、动态 SQL、N+1 或跨事务代码路径。
- 剩余 P2 均属于 TODO 9 的运行时证据缺口，不以静态检查冒充完成。

## 2026-08-25 提交前独立快照复核

- Foundation commit `78b63d0c` 的精确快照中，`internal/foundation`、`internal/platform/postgres`、`internal/workflow/adapter/river` 的 test、race、vet、integration compile-only、`go list` 和 `go mod verify` 均通过；`git diff 78b63d0c^ 78b63d0c --check` 通过。
- `go mod tidy -diff` 只提示补充 GORM sqlite 测试依赖的 checksum；本次未改写共享 `go.sum`，`go mod verify` 与 vendor 模式编译仍通过。
- 当前本地 `dev` 的纯 Git tip（不包含未跟踪文件）无法编译 `cmd/api`、`cmd/worker`：较早提交的 Artifact/Workflow GORM 文件引用了尚未提交的 `ScopedRuntimeStarter`、`ScopedWorkflowTerminalHook`、`ScopedModelRunStore` 及 Workflow GORM helper。完整脏工作树会遮蔽该问题，因此 push 前必须先由对应 owner 提交这些依赖并在新的纯 Git tip 复跑命令编译。
- 上述提交序列问题不在 Foundation commit 的文件范围内，未通过修改其他模块规避；在纯 Git tip 恢复可编译前，不应把当前本地 `dev` push 视为通过发布门禁。

## TODO 9 未关闭门禁

真实 PostgreSQL 环境需要至少证明：

1. 已覆盖：GORM 业务写入与 River Job 的真实共同提交/回滚，以及入队 fence 拒绝。
2. 已覆盖：SQL driver 插入的 Job 被既有 pgx Worker 消费；River listener 仍由 pgx client 保持，未将不支持 listener 的 SQL driver 用作 Worker。
3. 已覆盖：主动取消、自定义 cause、statement timeout `57014`、重复键 `23505`、read-only `25006`；deadlock、serialization failure 和生产错误分类矩阵仍由各业务 child/最终 TODO 9 统一验收。
4. 已覆盖：SQL facade 与 pgxpool 共享、关闭顺序、幂等 Close 和 `MaxIdleConns=0`。
5. 已覆盖：bounded concurrent GORM transactions 与 Worker 消费未出现池饥饿；生产连接预算和更高并发容量仍需目标环境门禁。
6. Foundation 已证明 scoped fence 在拒绝时不会执行 River insert；Model Settings 的真实 rollout fence 和生产 Composition 仍由对应 child 负责。
7. 当前测试使用“真实 commit 后再注入 ACK 丢失”的确定性包装器，证明已提交事实与唯一键重放；尚未声称它覆盖真实网络层 COMMIT response loss。若需要网络级丢包/断链证明，必须在专用数据库代理或目标部署环境授权后补跑，不能用 fake/mock 替代。

因此 Foundation 是“已实现且平台级真实 PG/River 基线已验证，但尚未接入生产 Composition、仍受 TODO 9/各模块 owner 门禁约束”的基础能力。
