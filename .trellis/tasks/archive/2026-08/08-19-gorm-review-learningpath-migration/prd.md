# Review Learning Path Repository 迁移到 GORM

> **当前交付状态（2026-09-08）**：`completed`，已归档；最终代码 [cb935655](https://github.com/CodeZen-Lizhi/zhixu/commit/cb93565561498674cda1dc1230fed587fba66075) 已推送至 `origin/dev`，尚未部署。
> TODO 10 全部 30 个子任务已完成，最新实现与验证见 [Final 验收记录](../../2026-09/08-19-gorm-composition-pgx-convergence/research/final-acceptance.md)。下文 staged、待 Final 或未提交的描述保留其原阶段事实，现存 M9 历史升级限制仍有效。

## Goal

在不改变 Review/Interview 共享 Learning Path 的持久化语义和生产接线的前提下，为 `internal/review/learningpath/adapter/postgres` 新增完整的 staged GORM Repository。该实现必须保持 Answer-scoped reservation、Artifact digest/hold fence、CAS、历史保护、稳定顺序、唯一约束、数据库时间、取消、错误与日志契约；TODO 9 真实 PostgreSQL 门禁通过前不得切换生产 Composition、删除 legacy pgx 实现、完成或归档本任务。

## Background And Confirmed Facts

- `migrations/00060_review_shared_learning_path.sql:15-110` 将 Interview Path/Step 升格为 `learning.learning_path` / `learning.learning_path_step` 唯一基表，Interview 只保留可更新兼容视图；Review origin 唯一绑定不可变 Review Answer。
- `migrations/00060_review_shared_learning_path.sql:194-325` 定义 append-only command receipt、Answer-scoped creation reservation、唯一 key、状态/时间/binding shape、pending maintenance index 与 no-keepalive trigger。
- `migrations/00060_review_shared_learning_path.sql:367-514,566-657` 用数据库 trigger 保护精确 Artifact hold、Path/Step 历史不可改删、version 单步推进与允许的状态转换。
- 现有 `application.Store` 已完整表达 11 个持久化操作；Domain/Application 不暴露 pgx、GORM 或 `database/sql`，无需调整稳定 Port。
- legacy `Repository` 的固定事务顺序为 Workspace/Answer/command/reservation/path 锁、receipt replay、CAS 写入和 commit；复杂 evidence projection、maintenance CTE、advisory lock、`FOR UPDATE SKIP LOCKED` 均是已验证的 PostgreSQL 语义，不适合改写为隐式 ORM association。
- 现有 integration suite 覆盖同 key/异 key 并发、maintenance 与迟到 hold/Complete 竞态、commit response-loss replay、ABANDONED 重开和 attempt fence，但 fixture 和故障注入当前直接依赖 pgx。
- Foundation 已在共享脏工作树中提供 `Pool.GORM()`、`Pool.UnitOfWork()` 和 `GORMTransaction(scope)`；其 TODO 9 真实 PostgreSQL 门禁尚未完成，Foundation task 仍为 `in_progress`。
- 生产调用点位于 `cmd/api/main.go:1259` 与 `cmd/worker/main.go:1903`，均继续构造 legacy `NewRepository`；本 child 不修改这些文件。

## Requirements

### R1. Scope And Staged Compatibility

- 产品代码修改范围仅限 `internal/review/learningpath/adapter/postgres`；规划、证据和检查记录仅限本任务目录。
- 保留现有 `Repository`、`DB`、`NewRepository(DB)`、legacy pgx SQL 与所有生产调用点，新增 `GORMRepository` 并静态实现现有 `application.Store`。
- GORM 构造必须只从同一个 `platformpostgres.Pool` 获取共享 GORM root 和 Unit of Work；禁止独立 `gorm.Open`、第二连接池、运行时 selector、双读、双写、fallback 或半迁移 Composition。
- 不修改 Review Core、Review Interview、`cmd/**`、`go.mod`、`go.sum`、`vendor/**`、`internal/platform`、父任务公共设计或其他 GORM child。

### R2. Schema And Explicit Persistence Models

- Schema、列、约束、trigger、index 和数据库枚举只以现有 migration 为事实源；不新增/修改 migration，不调用 `AutoMigrate`、`Migrator` 或 GORM Schema API。
- 为 Path、Step、command receipt 和 creation reservation 定义 Adapter 私有显式 model/table mapping；不得使用 `gorm.Model`、隐式复数表名、soft delete、association、hook 或自动时间戳。
- nullable UUID/time/digest/final binding、JSONB snapshot/response 和 UTC 时间必须显式映射；Domain model 与 Persistence model 分离。
- 所有读 SQL 显式列出字段；所有写操作显式列出列和条件，禁止 `Save`、全字段 `Updates`、隐式 preload 或 `SELECT *`。

### R3. Creation Reservation And Artifact Visibility

- `BeginReviewCreate` 保持 Workspace lock -> exact receipt -> Review Answer lock -> reservation lock -> frozen snapshot/evidence validation -> insert/reopen CAS 的顺序。
- 相同 key/hash 的 PENDING 请求返回原 reservation；COMPLETED 请求 exact replay；异 key/hash 与绑定漂移返回既有稳定 conflict；只有 ABANDONED 且 attempt 匹配时才能以 `attempt_no+1` 重开。
- `PrepareReviewCreate` 继续以 key/hash/source digest/attempt fence 将 PENDING reservation 绑定到唯一 Artifact digest；迟到旧 attempt 不得污染新尝试。
- `CompleteReviewCreate` 必须在一个事务中写唯一 Review Path、按 `step_no,id` 稳定的 Steps、append-only receipt、reservation COMPLETED CAS，并且只删除当前 Answer/Artifact/attempt digest 的一个 ACTIVE hold；任一步失败整体回滚。
- `MaintainReservations` 继续以 `updated_at,workspace_id,review_answer_id` 有界排序和 `FOR UPDATE SKIP LOCKED` 将超时 PENDING 转为 ABANDONED，并只把同 digest ACTIVE hold 转为 ORPHANED；不物理删除恢复/审计历史。

### R4. Read, CAS, Ordering And Uniqueness

- `GetByReviewAnswer` / `Get` 只返回同 Workspace 的 `origin_type='REVIEW'` Path，并按 `step_no,id` 稳定读取完整 Step 集；任何 ID、origin、source、Artifact、version、时间或 Step 连续性异常均 fail closed。
- Path status 命令保持 command advisory lock、exact receipt、aggregate `FOR UPDATE`、expected version 与允许转换校验、Path CAS 和 receipt 原子写入。
- Step 命令保持 command advisory lock、exact receipt、Path/Steps 锁定读取、Step CAS、父 Path CAS/自动完成和 receipt 原子写入；不允许把 `PENDING` 作为更新 target。
- 保持 `(workspace_id,review_answer_id)`、`(workspace_id,idempotency_key)`、Path/Artifact final tuple、Step 顺序/identity 和 command primary key 的数据库唯一约束语义，不用应用层 upsert 掩盖冲突。
- Evidence projection 继续使用单条 set-based `unnest`/join 查询和请求序号复核；禁止 N+1、无界读取或丢失输入顺序。

### R5. Transactions, Time, Cancellation And Resources

- 所有现有多语句事务以及由多条 SQL 组成的聚合读取必须复用 Foundation `Pool.UnitOfWork()` / `UnitOfWork.Within`，并仅在 Adapter callback 内用 `GORMTransaction(scope)` 取得 scoped `*gorm.DB`；只有真正单 SQL 的只读操作可直接使用共享 GORM root。
- 禁止直接调用 `database.Transaction`、自行解包 `*sql.Tx`、使用 `SQLTransaction(scope)` 或建立第二套事务机制；公开 Port 不出现 GORM、pgx、`database/sql` 或 `any`，`internal/platform` 在本 child 中保持只读。
- 保持默认 PostgreSQL 隔离级别、锁顺序与单次 commit/rollback ownership；Artifact 创建仍通过 reservation/hold saga 分阶段闭合，不建立跨模块长事务。
- reservation、receipt 和 maintenance 的 `statement_timestamp()` 语义保持不变；Path/Step 用户命令继续使用 Application 已冻结的 `record.At`，GORM 不自动生成或覆盖时间。
- 每个数据库调用携带 caller context；取消、deadline、transaction ended、begin/commit/rollback/Rows 错误不得被吞掉，Rows 必须 Close 并检查 Err。

### R6. Error And Logging Compatibility

- 保持现有 Learning Path error kind/code/retryability：no-row 按调用语义映射 miss/NotFound/CAS conflict，`23505` 映射 idempotency conflict，`23503/23514/23502/22001/22P02` 映射 consistency，其他数据库失败保持 dependency unavailable。
- GORM 路径同时识别 `sql.ErrNoRows`、`gorm.ErrRecordNotFound` 与 pgx 基线；已有 `foundation.Error` 原样保留，context sentinel/cause 可由 `errors.Is` 复核。
- GORM logger 继续使用 Foundation 的 discard 配置；公开错误和检查记录不得包含 SQL、参数、Review score/evidence、snapshot、receipt JSON、Artifact 正文、完整 DSN、Secret 或绝对路径。

### R7. Verification And Completion Gate

- 静态阶段使用现有测试、race、vet、integration compile、API/Worker compile、module/vendor、Trellis validate、gofmt 和 `git diff --check`；不新增测试文件。
- TODO 9 可用后，只在现有 integration 文件中参数化 legacy/GORM fixture，并为每种实现使用独立 migrated disposable database；必须验证创建/读取/状态/步骤、锁/CAS/唯一约束、rollback、response-loss、ABANDONED/ORPHANED、attempt fence、context 和 SQLSTATE 等价。
- TODO 9 前只允许交付未接 Composition 的 staged 实现和静态证据；所有 Acceptance Criteria 保持未勾选，任务保持 `in_progress`，不得归档或宣称生产迁移完成。
- TODO 3 Atlas 只阻断 Final Composition/pgx 收口，不阻断本 child 的 staged 实现。

## Acceptance Criteria

- [x] staged `GORMRepository` 完整实现现有 `application.Store`，稳定 Port 与 legacy pgx 生产路径保持不变。
- [x] Review Answer frozen snapshot/evidence、reservation exact replay/reopen、Artifact digest/hold 与 attempt fence 在真实 PostgreSQL 上与 legacy 等价。
- [x] Path/Step/receipt/final reservation 的原子写入、回滚、历史保护、稳定顺序和唯一约束在真实 PostgreSQL 上等价。
- [x] Path/Step command 的 advisory lock、expected-version CAS、状态转换、父聚合推进和 exact replay 在真实 PostgreSQL 上等价。
- [x] maintenance 的数据库时间、有界 `SKIP LOCKED`、ABANDONED/ORPHANED 和迟到请求 fence 在真实 PostgreSQL 上等价。
- [x] no-row、SQLSTATE、cancel/deadline、commit response-loss、Rows/transaction 生命周期和日志脱敏通过门禁。
- [x] 局部 test/race/vet/compile、module/vendor、Trellis validate、Go/SQL Review、gofmt 与 `git diff --check` 通过并记录。
- [x] TODO 9 真实 PostgreSQL 门禁全部通过；在此之前不得完成、归档或声明生产已迁移。TODO 3 仅阻断 Final。

## Out Of Scope

- 生产 Composition 切换、删除或重命名 legacy pgx Repository、修改 API/Worker readiness/maintenance wiring。
- Review Core、Review Interview、Artifact、Domain/Application/HTTP/API/OpenAPI/前端行为或共享 transaction Port 变更。
- Schema/migration/trigger/index/数据回填、AutoMigrate、性能索引调整、缓存、双写、运行时 fallback 或第二连接池。
- 新建测试文件、实现 TODO 9 Testcontainers 工厂、修改 Foundation、依赖清单或父任务/其他 child 状态。

## Dependencies And Deferred Items

- 直接依赖 `08-19-gorm-platform-transaction-foundation` 的共享 GORM root、Unit of Work 与 opaque scope；该基础当前已静态实现但仍等待 TODO 9 运行时门禁。
- Review Core、Interview 与 Learning Path 可独立实现；第三个 child 完成后由父任务执行跨模块集成门禁，本 child 不提前修改其他 owner。
- TODO 9 前的主要剩余风险是 GORM/database/sql 对真实锁等待、array/JSONB、trigger SQLSTATE、commit response-loss、取消和连接释放的行为尚未完成成对证明。
