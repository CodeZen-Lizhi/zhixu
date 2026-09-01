# Review Learning Path GORM 迁移设计

## 1. Boundary And Compatibility

本 child 采用“legacy pgx 保留 + staged GORM 并行实现 + Final 统一切换”的边界：

- `Repository`、`DB`、`NewRepository(DB)` 和 `cmd/api` / `cmd/worker` 当前 wiring 原样保留；
- 新增 `GORMRepository` / `NewGORMRepository(*platformpostgres.Pool)`，完整实现现有 `application.Store`；
- 构造器从同一个 Pool 取得 `Pool.GORM()` 和 `Pool.UnitOfWork()`，不允许 caller 传入可能错配的 root/UoW；
- 事务机制唯一固定为 `UnitOfWork.Within` + callback 内 `platformpostgres.GORMTransaction(scope)`；所有多语句写事务和 Path+Steps 多语句读取均走该 opaque scope；
- 只有 receipt lookup 等真正单 SQL 的只读操作使用共享 GORM root；禁止直接 `database.Transaction`、`SQLTransaction(scope)`、解包 `*sql.Tx` 或自建 transaction helper；
- Domain/Application 不改动，不暴露 GORM、pgx、`database/sql` 或 `any`；
- `internal/platform` 只读，不为本模块补充任何专用 unwrap、scope 或事务机制；
- TODO 9 前 staged 实现不接生产、不删除 legacy、不建立 selector/fallback/双写。

该边界让本 child 可独立回滚，同时避免在 TODO 9 和 Final 前改变 Review/Worker 生产行为。

## 2. File Layout

- `gorm_repository.go`：构造校验、11 个 Store 方法、Foundation Unit of Work、context/no-row/error 分类与资源生命周期。
- `gorm_queries.go`：GORM `?` placeholder 的固定参数化 SQL、显式列清单、锁/CTE/RETURNING；复杂 PostgreSQL 语义保持 Raw/Exec。
- `gorm_model.go`：Path、Step、command、reservation 的私有显式 persistence model、table name、nullable/JSONB carrier；不包含 association/hook/soft delete。
- 现有 `repository.go` / `codec.go` 保持 legacy owner。仅在能无行为变化地复用 strict JSON、Domain validation 或 equality helper 时做最小共享抽取；不得为了去重重写已验证 pgx 状态机。
- TODO 9 阶段只修改现有 `review_learning_path_*_integration_test.go` 做 implementation factory/fixture 复用，不新增测试文件。

## 3. Schema And Persistence Models

四个显式 model 严格映射 `00060`：

| Model | Table | 关键边界 |
| --- | --- | --- |
| `learningPathGORMRecord` | `learning.learning_path` | 显式 origin/source/Artifact/status/version/time；Review source 与 Interview nullable tuple 不混用 |
| `learningPathStepGORMRecord` | `learning.learning_path_step` | 显式 step_no、evidence tuple、status/version/time；读取固定 `ORDER BY step_no,id` |
| `learningPathCommandGORMRecord` | `learning.learning_path_command` | append-only key/hash/type/expected/path version/response/DB time |
| `learningPathReservationGORMRecord` | `learning.learning_path_creation_reservation` | snapshot/digest/attempt/status、nullable prepared/final tuple/terminal times |

Model 只服务显式 insert/scan/carrier，不拥有 Schema。所有 nullable UUID/time 使用 pointer 或 `sql.Null*`，JSONB 使用私有单值 `driver.Valuer`/Scanner，`Value()` 返回受验证 JSON string，避免 `[]byte` 被 PostgreSQL driver 推断成 `bytea`。时间字段关闭 GORM 自动 create/update；数据库时间继续由 SQL `statement_timestamp()` 产生。

## 4. Transaction Flows

Repository 只保留两种执行入口：

| 操作形状 | 执行入口 |
| --- | --- |
| 单 SQL 只读 receipt lookup | `repository.database.WithContext(ctx)` 共享 GORM root |
| 任意多语句写事务 | `repository.unitOfWork.Within(ctx, foundation.TransactionOptions{}, ...)`，callback 内一次 `platformpostgres.GORMTransaction(scope)` |
| 公开 Path+Steps 聚合读取 | `repository.unitOfWork.Within(ctx, foundation.TransactionOptions{ReadOnly:true}, ...)`，同一 scope 内完成两条查询 |

任何 scoped `*gorm.DB` 都不能逃逸 callback、缓存或被异步使用；Repository 不调用 GORM root 的 `Transaction`，不接触 `*sql.Tx`，不建立第二套 begin/commit/rollback ownership。内部 `loadResult` 接受已有 scoped DB：公开 Get 由 read-only UoW 包裹，Begin/Complete/Update 等写流程复用当前 write scope，禁止 helper 嵌套开启第二个 UoW。

### 4.1 BeginReviewCreate

```text
UnitOfWork.Within(default write transaction)
  -> core.workspace FOR UPDATE
  -> exact CREATE_REVIEW_PATH receipt lookup
  -> learning.review_answer FOR UPDATE
  -> reservation FOR UPDATE
  -> COMPLETED: load path, repair missing other-key receipt only, replay
  -> same pending key/hash: return frozen reservation
  -> other pending binding: stable RESERVATION_PENDING
  -> ABANDONED same binding: reload current Review snapshot, attempt CAS +1
  -> no reservation: freeze Review snapshot and insert attempt 1
  -> commit
```

Review snapshot 查询保留 `FOR KEY SHARE`、approved Card、confirmed Claim、Claim Source、active Index/manifest/chunk/provenance 的单条 set-based projection。`unnest` 输入通过 `pq.Array` 或等价单值 carrier 绑定 `uuid[]/text[]`；按 `request_no` 复核数量、顺序、重复和完整 Citation。

### 4.2 PrepareReviewCreate

```text
UnitOfWork.Within
  -> reservation FOR UPDATE
  -> key/hash/source digest/attempt exact binding
  -> COMPLETED exact replay
  -> require PENDING
  -> empty digest: CAS UPDATE ... WHERE artifact_digest IS NULL AND attempt_no=?
  -> same digest replay / different digest conflict
  -> commit
```

`prepared_at` / `updated_at` 继续由同一 SQL statement 的 `statement_timestamp()` 生成；GORM 不提供本地时间。

### 4.3 CompleteReviewCreate

```text
UnitOfWork.Within
  -> Review Answer FOR UPDATE
  -> reservation FOR UPDATE
  -> key/hash/source digest/attempt/artifact digest fence
  -> COMPLETED exact replay
  -> insert one REVIEW Path with explicit columns
  -> batch insert 1..16 ordered Steps with explicit columns
  -> insert append-only CREATE receipt
  -> reservation COMPLETED CAS with final Path/Artifact tuple
  -> delete exactly one matching ACTIVE hold
  -> commit
```

Step 可使用显式 model 的单条 batch insert，输入顺序先由 `validateResult` 锁定为连续 `step_no`；不得逐 Step 查询、association create 或隐式 cascade。reservation CAS 或 hold `RowsAffected != 1` 返回既有 Artifact conflict，整个事务回滚，因此不存在可见 Path、receipt 或半释放 hold。

### 4.4 Path And Step Commands

两个命令均固定：transaction -> `pg_advisory_xact_lock(hashtextextended(workspace:key,0))` -> exact receipt -> Review Path/Steps `FOR UPDATE` -> Domain transition validation -> version CAS -> append-only receipt -> commit。

- Path status 只允许 ACTIVE->PAUSED/COMPLETED、PAUSED->ACTIVE；COMPLETED 前全部 Steps terminal。
- Step 只允许 PENDING->IN_PROGRESS/COMPLETED/SKIPPED 或 IN_PROGRESS->COMPLETED/SKIPPED；先 CAS Step，再 CAS 父 Path，全部 terminal 时父 Path 变 COMPLETED。
- `updated_at` 继续使用 Application `record.At.UTC()`，保持现有可测试时钟，不由 GORM `NowFunc` 替换。

### 4.5 Maintenance

保留一个参数化 CTE：按 `updated_at,workspace_id,review_answer_id` 取 `LIMIT ? FOR UPDATE SKIP LOCKED`，同 statement 将 PENDING 更新为 ABANDONED，再把精确 Answer/attempt digest ACTIVE hold 更新为 ORPHANED，返回两个 count。`orphaned > abandoned` 为 persistence invalid；commit 后返回 abandoned 数量。before 由 Application 24 小时策略传入，状态时间由 `statement_timestamp()` 产生。

## 5. Read And Replay Mapping

- Receipt 使用 `Row().Scan` 明确区分 miss；strict JSON decode 禁止未知/尾随字段，校验 key/hash/type/expected/path version 和完整 snapshot 后才设置 `Replayed=true`。
- Path 查询显式读取 14 列，随后单条读取 Steps；公开读取的两个 statement 必须在同一个 read-only `UnitOfWork.Within` opaque scope 内完成，写流程则复用已有 write scope，避免 root 多语句读取或 nested UoW。任何 no-row、origin/source shape、Artifact binding、version/time、Step 连续性/重复 ID/状态异常均返回稳定错误，不返回部分聚合。
- GORM 的 `Raw().Scan` 零行不会被用作 miss 判据；一行查询统一使用 `Row().Scan`，多行使用 `Rows()` + `defer Close()` + `rows.Err()`。
- persistence mapping 复用现有 `decodeStrict`、`validateReservation`、`validateResult` 和 Domain validators；若接口类型阻止直接复用，只抽取数据库无关部分，不复制第二套领域判断。

## 6. SQL And GORM Boundary

- 常规明确 insert/batch insert 可使用 GORM model + `Table`/`Create`；锁、CTE、array、`RETURNING`、advisory lock、CAS 和 maintenance 使用 `Raw`/`Exec`。
- GORM SQL 使用 `?` placeholders，PostgreSQL cast 保留为 `?::uuid[]` / `?::text[]`；所有值参数化，表/列/排序字符串均为包内常量。
- 禁止 `Save`、implicit Updates、association/preload、Hook、自动时间、default transaction、prepared statement cache 和 ORM 生成 DDL。
- constructor/公开方法校验 nil/typed-invalid Pool、GORM root、UoW 与 nil context；事务 callback 之外不缓存 scoped `*gorm.DB`，不调用 root `Transaction` 或任何 `*sql.Tx` unwrap。

## 7. Error, Context And Logging

GORM 分类优先级：

1. 已有 `foundation.Error` 原样返回；
2. 原始 `ctx.Err()` / `context.Cause(ctx)`，保留 canceled/deadline sentinel；
3. `sql.ErrTxDone`、`sql.ErrNoRows`、`gorm.ErrRecordNotFound`；
4. `*pgconn.PgError` 精确 SQLSTATE；
5. 安全的 dependency unavailable，不拼接 SQL、参数或 driver 原始明细到公开 message。

稳定映射保持 legacy 行为：`23505` -> idempotency conflict；`23503/23514/23502/22001/22P02` -> consistency；caller cancel -> nonretryable dependency failure；deadline/其他 DB failure -> retryable dependency unavailable。具体 no-row 由调用点决定 miss、NotFound 或 CAS conflict。

Foundation GORM logger 已为 discard；本 Adapter 不新增 SQL/参数日志。Review score/evidence、snapshot、receipt response、Artifact content、DSN 和绝对路径不进入错误文本或验证记录。

## 8. Verification Design

### Static Stage

- 运行 Learning Path 包 test/race/vet、integration compile-only、API/Worker compile-only、go list/module verify、Trellis validate、gofmt diff 和 `git diff --check`。
- 静态扫描 staged 构造未接 `cmd/**`，legacy `NewRepository` 未删，Domain/Application 无 GORM/pgx 泄漏，无 `AutoMigrate/Migrator`、第二 pool、动态 SQL 或日志泄密。
- 使用 `go-review` 与 `sql-code-review` 检查事务生命周期、error/context、Rows、锁顺序、CAS、唯一约束、DB time、batch/array/JSONB 和 rollback。

### TODO 9 PostgreSQL Stage

只原位扩展现有 integration 文件：临时 migration pool 完成迁移并关闭后，用同一 disposable database 调用普通 `platformpostgres.Open`，由一个 runtime Pool 同时提供 `DB()`、`GORM()`、`UnitOfWork()`。legacy/GORM 每个子用例使用独立 database，避免固定 ID/key 污染。

现有 pgx barrier/commit-loss doubles 不能直接证明 database/sql 行为；TODO 9 中在同一现有文件为 GORM 使用 transaction-visible lock barrier 与受控 commit-loss wrapper/callback，保持不靠 sleep 的确定性。必须成对覆盖：

- 同 key/异 key create、PENDING/completed replay、ABANDONED reopen、old-attempt rejection；
- maintenance-vs-hold/Complete 两种赢家、ORPHANED 继续隐藏、exact hold release；
- Path/Step status CAS、receipt replay、rollback/unique/trigger/history guard；
- commit response-loss 后 exact lookup、cancel/deadline/SQLSTATE/Rows/连接释放；
- evidence array/JSONB、stable step order、set-based query 和无 N+1。

## 9. Rollback And Deferred Integration

- TODO 9 前回滚只删除本 child staged GORM 文件并还原本 child 内必要的共享 helper；legacy production 和 Schema 不变。
- TODO 9 后、Final 前仍保留 legacy wiring 作为回退；Final 统一切 API/Worker Composition 并决定何时删除 legacy。
- Review 三个 child 的跨模块门禁归父任务；本 child 不修改 Review Core、Interview 或 cmd composition。
- TODO 3 Atlas 完成前不做最终 Schema/Composition 收口。
