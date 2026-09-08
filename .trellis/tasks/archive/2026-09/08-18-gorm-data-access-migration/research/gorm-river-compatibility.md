# GORM、pgx 与 River 事务兼容性调研

## 结论

不需要自研 GORM→pgx 事务 Driver。首选方案是：

1. 保留现有 `*pgxpool.Pool` 作为唯一物理连接池；
2. 使用 pgx 官方 `stdlib.OpenDBFromPool` 生成共享该 pool 的 `*sql.DB`；
3. GORM 使用官方 PostgreSQL Driver 和该 `*sql.DB`；
4. GORM 事务中的 River 原子入队使用 River v0.40.0 官方 `riverdatabasesql` Driver（`*sql.Tx`）；
5. Worker 启动、LISTEN/NOTIFY、River migration 等保留现有 `riverpgxv5` Driver，直到独立证据支持改变。

该方案复用三个官方边界，不建立第二物理连接池，也不复制 River Job INSERT 协议。它仍需先行基础任务做真实 PostgreSQL/River Spike；未验证前不能作为完成事实。

## 已核对版本

| 组件 | 版本 | 兼容事实 |
| --- | --- | --- |
| Go | `1.25.4` | 项目 `go.mod` 当前版本 |
| pgx | `v5.10.0` | 项目当前版本；GORM PostgreSQL Driver 和 River v0.40.0 同样依赖该版本 |
| GORM | `v1.31.2` | 2026-08-19 `go mod download gorm.io/gorm@latest` 的解析结果；MIT |
| GORM PostgreSQL Driver | `v1.6.2` | 2026-08-19 latest 解析结果；要求 Go 1.25，依赖 pgx v5.10.0；MIT |
| River | `v0.40.0` | 项目当前锁定版本 |
| River database/sql Driver | `v0.40.0` | River 官方同版本子模块；MPL-2.0，与项目既有 River 许可证族一致 |

版本只表示本次调研候选。实际引入时必须锁定 `go.mod/go.sum/vendor`、运行现有兼容门禁，并重新核对最新安全/维护状态；不得用 `@latest` 直接进入生产构建。

## 连接池事实

- GORM `ConnPool` 使用 `database/sql` 风格的 `PrepareContext`、`ExecContext`、`QueryContext`、`QueryRowContext`。
- GORM PostgreSQL Driver 可通过 `postgres.Config.Conn` 接收现有 `gorm.ConnPool`，官方文档支持传入 `*sql.DB`。
- pgx v5.10.0 的 `stdlib.OpenDBFromPool(pool)` 返回使用现有 `pgxpool.Pool` 的 `*sql.DB`：
  - 自动将 `sql.DB` 的 MaxIdleConns 设为 0，避免占满 pgxpool；
  - 关闭返回的 `*sql.DB` 不会关闭底层 pgxpool；
  - `sql.DB` 与直接 pgx 调用竞争同一 pool，必须验证 MaxConns、deadline、关闭顺序和饥饿行为。
- 因此不应再用 DSN 调用 GORM Driver 创建第二个独立 pool。

## 事务事实

- GORM v1.31.2 的 `Transaction/Begin` 最终使用 `database/sql` 的 `BeginTx`，事务 ConnPool 为 `*sql.Tx`。
- River `riverdatabasesql` v0.40.0 包注释明确说明用于 Bun/GORM；Driver 泛型事务类型是 `*sql.Tx`，`UnwrapExecutor` 和 `UnwrapTx` 都直接使用该类型。
- River 当前 `riverpgxv5` 的事务类型是 `pgx.Tx`。两者不能互换，但可以同时操作同一 River Schema：
  - GORM 业务事务内通过 `riverdatabasesql.Client[*sql.Tx].InsertTx` 原子入队；
  - Worker/listener 继续通过 `riverpgxv5.Client[pgx.Tx]` 消费、监听和维护。
- `riverdatabasesql.SupportsListener()` 返回 false，不能未经验证直接替换 Worker 的 pgx Driver。

## River/事务调用点归属矩阵

| 现有调用点 | 当前底层类型 | 迁移 owner | 目标边界与禁止项 |
| --- | --- | --- | --- |
| `internal/workflow/adapter/river/inserter.go`：`JobInserter`、`EnqueueFence`、`TypedJobInserter`、`insertValidatedJobTx` | `any` 最终断言 `pgx.Tx`，调用 River pgx InsertTx | Foundation 定义 Port；Workflow 迁移实现 | 事务参数改为项目 opaque scope；平台 adapter 唯一 unwrap 到 `*sql.Tx`；旧 `pgx.Tx` inserter 不得被 GORM 调用 |
| `internal/workflow/adapter/postgres/runtime_start.go`、`runtime_state.go` | `pgx.Tx` Begin + `JobInserter.InsertTx` | Workflow | 业务写入和 River insert 共用同一 scope；禁止第二套 enqueue 路径 |
| `internal/export/adapter/river/dispatcher.go` | `*pgxpool.Pool`、`pgx.Tx`、Workflow typed inserter | Export + Foundation Port | Dispatcher 只依赖 Workflow transaction/job Port；不得直接持有 pool 或断言 pgx.Tx |
| `internal/retrieval/adapter/river/inserter.go`、`internal/retrieval/adapter/postgres/dispatcher.go` | `any`/`pgx.Tx`、Workflow typed inserter | Retrieval + Foundation Port | Reindex job insert 使用同一 GORM scope；COPY/临时表等专用 pgx 保留在独立 allowlist |
| `internal/modelsettings/adapter/postgres/runtime.go`：`CheckEnqueue` | `pgx.Tx` | Model Settings + Foundation Port | Enqueue fence 只接 opaque scope；锁/数据库时间语义不变 |
| `internal/workflow/adapter/river/worker.go`、`migrator.go` 及 Export/Retrieval Worker | `riverpgxv5` | Foundation/Final allowlist | Worker、listener、migration 暂保留 pgx；不得被 database/sql Driver 直接替换 |

矩阵必须作为 Foundation 退出条件的一部分：逐项记录新旧接口、调用方迁移 child、编译门禁和回滚点；静态检查禁止 GORM 事务路径继续调用旧 `pgx.Tx` inserter。

## 必须验证的 Spike

1. 用 `stdlib.OpenDBFromPool` + GORM 开启事务，在同一 `*sql.Tx` 中写业务行并调用 `riverdatabasesql.InsertTx`；提交后两者同时可见，回滚后两者同时不存在。
2. 相同唯一 Job 重放、scheduled job、metadata/traceparent 和 schema 配置与当前 `riverpgxv5` 结果一致。
3. 由现有 `riverpgxv5` Worker 消费 `riverdatabasesql` 插入的 Job；通知不可用时仍在受控轮询窗口内领取，不丢 Job、不重复副作用。
4. response-loss、serialization failure、deadlock、context cancellation、commit error 和 process kill 保持现有错误分类与恢复语义。
5. GORM 事务到 `*sql.Tx` 的获取只允许位于平台 transaction adapter，并由所选 GORM 版本的契约测试锁定；Domain/Application/模块 Repository 不做类型断言。
6. 同一物理 pgxpool 同时承载 GORM/database/sql、River pgx Worker、迁移/锁 allowlist 时，无连接饥饿、双重关闭或 readiness 漂移。

## 不采用方案

| 方案 | 不采用原因 |
| --- | --- |
| 自研 GORM `ConnPool` 包装 `pgx.Tx` | GORM 接口返回 `*sql.Rows/*sql.Row`，pgx 原生行类型不可直接满足；会复制 database/sql Driver 工作，维护和兼容成本高 |
| 为 GORM 按 DSN 创建第二物理连接池 | 会与 River/迁移 pool 竞争连接和配置，破坏“单一连接生命周期”目标；pgx 官方已有共享 pool 适配 |
| 在 GORM 事务提交后再非事务插入 River Job | 破坏现有原子性，制造丢 Job/幽灵 Job 窗口；除非另立 Outbox 架构决策，不得采用 |
| 手写 River 表 INSERT | 复制成熟框架的 schema、unique、metadata 和升级协议，违反 ADR-0019 |
| 全面将 Worker 改为 `riverdatabasesql` | v0.40.0 不支持 Listener；本任务没有证据证明替换 pgx Worker 有收益或行为等价 |

## 来源

- GORM 官方文档：`gorm.Config`、PostgreSQL existing connection、Transaction、Generic Interface（通过 Context7 于 2026-08-18/19 检索）。
- GORM v1.31.2 源码：`interfaces.go:33-68`、`finisher_api.go:637-727`。
- GORM PostgreSQL Driver v1.6.2 源码：`postgres.go:27-35,91-122` 和模块 `go.mod`。
- pgx v5.10.0 vendor 源码：`stdlib/sql.go:210-241`。
- River v0.40.0 当前 vendor 源码：`riverpgxv5/river_pgx_v5_driver.go`。
- River database/sql Driver v0.40.0 源码：`river_database_sql_driver.go:1-106,1047-1089` 和模块 `go.mod`。
- `docs/architecture/adr/0019-mature-framework-first.md`：成熟方案优先和自研例外门禁。
