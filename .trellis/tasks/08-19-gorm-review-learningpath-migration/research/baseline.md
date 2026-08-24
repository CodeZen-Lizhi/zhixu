# Review Learning Path GORM 迁移基线

## Scope Snapshot

- 允许修改：`internal/review/learningpath/adapter/postgres` 与本任务目录。
- 目标 Adapter 当前只有 `repository.go`、`codec.go` 和两个 integration 文件；任务规划时这些产品/测试文件均无 git 脏改。
- 禁止修改：Review Core、Review Interview、`cmd/**`、`internal/platform`、`go.mod/go.sum/vendor`、父任务和其他 GORM child。
- 工作树存在大量其他会话修改，包括 Foundation/GORM 依赖与多个 Adapter；本任务必须在其上只做 scoped diff，不回滚、格式化、暂存或提交其他文件。

## Store And Caller Inventory

`internal/review/learningpath/application/model.go:139-152` 的稳定 `Store` 有 11 个方法：

1. `FindCreateReplay`
2. `BeginReviewCreate`
3. `PrepareReviewCreate`
4. `CompleteReviewCreate`
5. `GetByReviewAnswer`
6. `Get`
7. `FindPathStatusReplay`
8. `UpdatePathStatus`
9. `FindStepReplay`
10. `UpdateStep`
11. `MaintainReservations`

该 Port 只含 context、foundation ID、Application record/result、Domain Path/Step 和 time，不暴露具体数据库类型。无需调整 Application/Domain。

生产构造点：

- `cmd/api/main.go:1259`：构造 legacy Learning Path Repository。
- `cmd/worker/main.go:1903`：构造同一 legacy Repository 供 24h maintenance。

本 child 不改构造点；Final 才拥有生产切换。

## Legacy Transaction And SQL Inventory

| Flow | 证据 | 必须保持 |
| --- | --- | --- |
| Begin create | `repository.go:43-145` | Workspace lock、receipt、Answer lock、reservation、frozen snapshot、ABANDONED attempt CAS、DB timestamps |
| Prepare | `repository.go:149-200` | key/hash/source/attempt fence、PENDING digest CAS、completed replay |
| Complete | `repository.go:205-300` | Answer/reservation locks、Path/Steps/receipt/final CAS/exact hold release 同事务 |
| Path command | `repository.go:319-360` | advisory command lock、receipt replay、aggregate lock、transition/version CAS |
| Step command | `repository.go:368-440` | advisory lock、Path/Steps lock、Step CAS、Path CAS/auto-complete、receipt |
| Maintenance | `repository.go:444-491` | stable bounded order、`FOR UPDATE SKIP LOCKED`、ABANDONED/ORPHANED、DB time |
| Reads/codecs | `codec.go:27-458` | strict snapshot/receipt JSON、set-based evidence、stable step order、full aggregate validation |

只有 `repository.go` 与 `codec.go` 在产品代码中直接 import pgx；production adapter 内没有 GORM/database/sql。

## Schema And Trigger Inventory

`migrations/00060_review_shared_learning_path.sql` 是当前唯一 Schema 事实源：

- `:15-110`：共享 Path/Step 基表、Review/Interview origin shape、Review Answer 唯一 binding、Interview compatibility views。
- `:194-216`：`learning_path_command` 的 key/hash/type/path/expected/version/response 与 append-only trigger。
- `:221-325`：creation reservation 的 primary/unique/FK/final tuple、digest/status/time shape、maintenance index 和 no-keepalive trigger。
- `:367-514`：ACTIVE hold 必须绑定 PENDING reservation、exact digest、LEARNING_PATH Artifact、PATH role 与 PLAN stage key。
- `:566-657`：Path/Step retained history、immutable facts、version+1、允许状态转换，DELETE fail closed。

GORM model 不拥有这些规则，不运行 AutoMigrate/Migrator。

## Existing Test Evidence

`review_learning_path_integration_test.go` 已覆盖：

- `:21` 同 key/hash 与异 key 并发创建；
- `:142` maintenance 与迟到 Artifact hold/Complete 双向赢家；
- `:309` commit response-loss 后同 key及其他 key exact replay；
- `:357` ABANDONED 重开、attempt+1 与旧 attempt fence。

测试使用 `ZHIXU_TEST_DATABASE_URL` 创建独立 database 并执行正式 migration。当前 barrier/commit-loss doubles 位于同一文件 `:509-702`，实现 pgx `DB/Tx` 接口，不能直接证明 GORM/database/sql 行为。TODO 9 必须原位扩展现有文件并使用同一 `platformpostgres.Pool` 的 `DB/GORM/UoW`；不得新建测试文件或用 sleep 替代确定性竞态控制。

## Foundation And Dependency Facts

- `internal/platform/postgres/pool.go:26-34,157-163`：一个 Pool 同时持有唯一 pgxpool、database/sql facade 与 shared GORM root。
- `internal/platform/postgres/transaction.go:18-94`：Unit of Work 用 GORM transaction，Adapter 通过 active opaque scope 解出 `*gorm.DB`；scope 离开 callback 后失效。
- Foundation static validation 固定 `gorm.io/gorm v1.31.2`、PostgreSQL driver `v1.6.2`，logger discard、SkipDefaultTransaction、无 AutoMigrate；真实 PostgreSQL TODO 9 尚未完成。
- 当前 dirty `go.mod/vendor` 已包含 GORM 与 `github.com/lib/pq`；本 child 只消费既有依赖，不修改 dependency files。

## Planning Decisions

1. 新增 staged `GORMRepository`，保留 legacy `Repository/NewRepository` 和生产 wiring。
2. constructor 接收单个 `*platformpostgres.Pool`，防止 GORM root/UoW 错配或第二 pool。
3. 所有多语句写事务与 Path+Steps 聚合读取使用 Foundation UoW + opaque GORM scope；公开聚合读取使用 read-only options，写流程复用已有 write scope且不嵌套 UoW。只有单 SQL 只读 lookup 使用 root。禁止 root `Transaction`、`SQLTransaction`、`*sql.Tx` 或第二事务机制，`internal/platform` 只读。
4. Path/Step/command/reservation 使用显式私有 model；不使用 association/hook/soft delete/自动时间/Schema API。
5. Evidence 继续 set-based array query；Steps 使用有界 batch insert；Reads 保持显式列和稳定顺序。
6. 不调整稳定 Port，不修改 Core/Interview/Artifact/cmd/Foundation/migration/dependency files。
7. TODO 9 前只做 staged 实现与静态验证，AC 不勾选，任务不完成/归档。

## Remaining Runtime Risks

- database/sql/GORM 下真实 row/advisory lock 等待、SKIP LOCKED、trigger SQLSTATE 与 cancellation/commit-loss 分类尚未成对验证。
- pgx stdlib 的 uuid/text array、JSONB carrier、nullable UUID/time 与 Rows 生命周期需真实 PostgreSQL 证明。
- existing pgx-specific barrier/commit-loss doubles 需要 TODO 9 在原文件内提供 GORM 等价控制，不得以 legacy 结果替代 GORM 证据。
- Review Core/Interview/Learning Path 三 child 的跨模块 composition/integration 归父任务最终门禁。
