# Review Core GORM 迁移设计

## 1. Boundary And Compatibility

本 child 采用“legacy pgx 保留 + staged GORM 并行实现 + Final 统一切换”的边界：

- 现有 `Repository`、`DB`、`NewRepository(DB)` 和 `cmd/api` wiring 原样保留；
- 新增 `GORMRepository` / `NewGORMRepository(*platformpostgres.Pool)`，完整实现现有 `application.Repository` 与 `application.EvidenceVerifier`；
- 构造器只接受完整平台 Pool，并从同一个实例取得 `GORM()` 与 `UnitOfWork()`，禁止 caller 拼接可能错配的 root/UoW 或自行 `gorm.Open`；
- 所有多语句写操作通过 `UnitOfWork.Within` 执行，callback 内只用 `platformpostgres.GORMTransaction(scope)`；单条只读查询使用共享 GORM root；
- Domain/Application 不改动，不暴露 GORM、`database/sql`、pgx 或 `any`；
- TODO 9 前不修改 `cmd/**`、不删除 legacy、不增加 selector、fallback 或双写。

Review Core 可独立 staged，但它与 Interview 共用 `learning.review_session`。`GetSession` 必须保持 legacy 的 Workspace-scoped shell lookup，可返回任意通过 Domain validation 的 Session；只有 Start、Complete、SubmitAnswer 和 Answer replay 等 Review 命令继续强制 `session_type='REVIEW'` 且 `deck_id` 非空。不能把 Interview Repository 或 Learning Path 逻辑并入本实现，也不能把 Review 命令约束错误地下沉到通用 Get。

## 2. Owned Surface And File Layout

现有 Application 端口共有 21 个 Repository 方法与 1 个 EvidenceVerifier 方法。建议按现有职责拆分：

- `gorm_core.go`：构造、ready/within、Raw Row/Rows/Exec helper、context/no-row/SQLSTATE 分类；
- `gorm_model.go`：Deck/Card/Schedule/Session/Answer/Command 私有 persistence model、显式 `TableName`、JSONB/nullable carrier；
- `gorm_queries.go`：共享 scanner、receipt、Workspace/deck/card/session/schedule 锁、evidence 验证和 due SQL；
- `gorm_repository.go`：Deck/Card 的读写、Decision、Due 与 eligibility；
- `gorm_session.go`：Session、Answer replay 与 Answer+Schedule 原子提交；
- `gorm_invalidation.go`：有界失效 CTE；
- `gorm_deck_schedule.go`：Deck pause/resume/reset 与批量 Schedule 更新。

现有 pgx 文件继续拥有 production baseline。只有数据库无关且可证明不改变行为的 JSON codec、Domain validation、scanner/equality 才允许小范围共享抽取；不得为了去重重写已验证的 legacy 状态机。

## 3. Schema And Persistence Mapping

GORM model 仅映射已有 Schema，不拥有 DDL：

| Model | Table | 关键边界 |
| --- | --- | --- |
| `reviewDeckGORMRecord` | `learning.review_deck` | `scope jsonb`、status、daily limit、scheduler/version/time 显式映射 |
| `reviewCardGORMRecord` | `learning.review_card` | nullable claim/invalidation、answer points/evidence JSONB、fingerprint/status/version |
| `reviewScheduleGORMRecord` | `learning.review_schedule` | card PK、due/last-reviewed nullable、FSRS values、paused/version |
| `reviewSessionGORMRecord` | `learning.review_session` | nullable deck/ended time、type/status/config/key/request hash |
| `reviewAnswerGORMRecord` | `learning.review_answer` | immutable answer/score/feedback/schedule snapshot/scorer version |
| `reviewCommandGORMRecord` | `learning.review_command` | append-only key/hash/type/aggregate/version/response/time |

- 所有表名、列名、nullable、time/version 字段显式声明；不使用 `gorm.Model`、soft delete、hook、association 或自动时间；
- JSONB carrier 必须验证 JSON，并通过 `driver.Valuer` 返回 string，避免裸 `[]byte` 被 pgx stdlib 推断为 `bytea`；读取继续使用严格 JSON decode 与 Domain validation，损坏数据 fail closed；
- UUID/array 参数显式 cast；Evidence 的 `uuid[]/text[]` 使用 `pq.Array` 或等价单值 `driver.Valuer`，不能把裸 slice 交给 GORM 展开；
- `learning.review_card_evidence_selector` 是 trigger 维护的只读投影，不建立可写 model；
- 不修改历史 migration，也不调用 `AutoMigrate`/`Migrator`。

## 4. Transaction And Lock Matrix

所有写事务的第一把锁保持为 `core.workspace ... FOR UPDATE`。这既串行 Review 命令，也与 Claim/Conflict/Quarantine 触发器的失效路径形成相同 Workspace 锁边界；不得删去、换成 advisory lock 或 `SKIP LOCKED`。

| Flow | 固定顺序和原子事实 |
| --- | --- |
| CreateDeck | Workspace lock -> exact receipt -> Deck insert -> receipt -> commit/recovery lookup |
| Create/Edit Card | Workspace -> receipt -> Deck/Card `FOR UPDATE` -> Claim/Evidence `FOR SHARE` -> Card insert/CAS -> receipt |
| DecideCard | Workspace -> receipt -> Card `FOR UPDATE` -> status/version CAS -> APPROVED Schedule upsert -> receipt |
| Start/Complete Session | Workspace -> exact session/receipt -> active Deck or Session `FOR UPDATE` -> insert/CAS -> receipt |
| SubmitAnswer | Workspace -> exact Answer replay -> Session -> Card -> Evidence `FOR SHARE` -> Schedule `FOR UPDATE` -> immutable Answer insert -> Schedule CAS |
| Deck Schedule | Workspace -> receipt -> Deck `FOR UPDATE`/CAS -> set-based Schedule update -> receipt |
| InvalidateCards | Workspace -> receipt -> indexed targets `ORDER BY id LIMIT batch+1 FOR UPDATE` -> bounded Card update -> count/has_more -> receipt |

事务 callback 之外不缓存 scoped `*gorm.DB`。Repository 不直接调用 root `database.Transaction`，不自行 Begin/Commit/Rollback，不解包 `*sql.Tx`。rollback/commit ownership由 Foundation Unit of Work 唯一拥有。

### 4.1 Exact Replay And Commit Ambiguity

- 每个命令都必须在读取可变聚合前查询持久 receipt/Answer；同 key 同 binding 返回 snapshot，不重新验证当前证据或状态；同 key 异 binding 返回现有 stable conflict；
- callback 内任何 unique/CAS/trigger failure 都整体回滚；不能用 `OnConflict DO NOTHING` 把错误吞成成功；
- callback 已成功但 UoW 返回 commit error 时，按 legacy 语义从共享 root 重新查询对应 receipt/Answer；只有 exact snapshot 可证明成功，否则保留原错误分类；
- Answer insert 与 Schedule CAS 的顺序和原子性不变，任何一侧失败都不能留下部分事实。

### 4.2 Card/Schedule Trigger Boundary

数据库的 deferred guard 要求 APPROVED Card 恰有 Schedule，其他状态没有 Schedule。Approve 必须在同事务完成 Card 状态更新、Schedule upsert 和 receipt；Reject/Invalidate 只改 Card，由现有 trigger 同事务删除 Schedule。不得由 GORM association 或额外事务模拟。

### 4.3 Invalidation Boundary

保留现有五种静态 selector 查询与统一 CTE：Claim、Source Version、Claim+Version、Source Span、Claim+Span。所有 selector 以 AND 组合，使用 `review_card_evidence_selector` 索引；每次最多 200，`batch+1` 只用于 `has_more`。不得退化为 JSON 全表展开、逐 Card 更新、无界锁或 `SKIP LOCKED`。

## 5. Read And Query Semantics

- Deck/Card 列表继续 `updated_at DESC,id DESC`，严格 bounded；不引入 offset 或 N+1；
- `rankedDueCardsSQL` 整体保留参数化 Raw SQL。它拥有 UTC daily limit、approved/active/unpaused/due、confirmed Claim、高危 Conflict、Evidence/Source/ingestion quarantine fail-closed 与稳定 `due_at ASC,card_id ASC`；不得形式化为 Preload/Association；
- `CheckAnswerEligibility` 与 `ListDue` 必须共享同一 due eligibility 事实，不能只在 Application 做校验；
- 一行查询统一 `database.WithContext(ctx).Raw(...).Row().Scan`，避免 GORM `Raw().Scan` 零行不报错；多行统一从 `WithContext(ctx).Raw(...)` 取得 `Rows()`，检查 statement error/nil rows，`defer Close()` 并在末尾检查 `Rows.Err()`；Raw Row/Rows/Exec helper 都必须显式接收 caller context，不能靠 root 的 background context；
- Workspace、Deck、Card、Session、Schedule predicate 在每个 root 查询显式出现，跨 Workspace 与不存在保持现有 NotFound/Conflict 语义；
- caller time 与 DB time 不混写：record 的 `At.UTC()`、Due 的传入 `now` 和 legacy compatibility fallback 原样保留，GORM NowFunc/自动时间不得覆盖；trigger 自有 `CURRENT_TIMESTAMP` 保持不变。

## 6. Error, Context And Security

GORM 错误分类顺序：

1. 已有 `foundation.Error` 原样返回；
2. `ctx.Err()` 与不同的 `context.Cause(ctx)` 共同保留，使 canceled/deadline 的 `errors.Is` 成立；canceled 优先于 deadline；
3. `sql.ErrNoRows` / `gorm.ErrRecordNotFound` 由调用点映射成对应 NotFound、Replay miss 或 CAS conflict，`sql.ErrTxDone` 映射 dependency unavailable；
4. `*pgconn.PgError` 保留 cause 并沿用 legacy SQLSTATE/ConstraintName：23505 idempotency/Card conflict，23503/23514/23502 consistency，40001/40P01/55P03/08000/08003/08006/57P01 retryable，57014 dependency timeout；
5. 未知错误以稳定、安全的 dependency error 返回，不在 message 中拼 SQL、参数或 driver 文本。

公开方法拒绝 nil context、nil/typed-invalid Pool/root/UoW。Foundation GORM logger 已禁用 SQL 参数输出；本 Adapter 不新增日志。用户 Answer、Score/Feedback、Evidence、receipt response、DSN 和绝对路径不得进入错误、日志或验证记录。

## 7. Verification Design

### Static Stage

- 运行 Review Core package test/race/vet、integration compile-only、API compile-only、go list/module verify、Trellis validate、gofmt 与 `git diff --check`；
- 静态扫描 production 仍调用 legacy `NewRepository`，无 selector/双写；Domain/Application 无 GORM/pgx 泄漏；无 `AutoMigrate/Migrator`、独立 pool、root transaction、动态用户 SQL 或敏感日志；
- 使用 `go-review` 与 `sql-code-review` 核对接口完整性、scope 生命周期、锁序、CAS、receipt/recovery、Row/Rows、JSON/array、SQLSTATE、触发器、分页/批量与回滚；
- 当前 staged 阶段不改测试文件；现有 PG integration 未运行只能记录盲区，不能勾选 PRD AC。

### TODO 9 PostgreSQL Stage

只原位扩展 `repository_integration_test.go`：每个 implementation subtest 使用独立 migrated disposable database；迁移完成后使用普通 `platformpostgres.Open` 得到同一个 runtime Pool，legacy 取 `DB()`，GORM 取 `GORM()/UnitOfWork()`。不得让两个实现共享固定 ID/key 数据。

成对复跑现有场景：

- Answer/Schedule/receipt 原子提交、same-key replay、different-key concurrent conflict、stale schedule/card CAS、commit response-loss；GORM response-loss 在同包现有 integration 文件中以 UoW wrapper 实现：先让真实 `Within` 完整提交，再向 Repository 返回受控错误，随后断言 root exact lookup 恢复，不尝试包装 opaque Pool 的 Begin；
- Review/Interview Session shell guard、Deck/Card/Session Workspace binding、Card edit-reapprove ABA；
- pause/resume/reset 与 Submit 并发；Approval 与 quarantine/high-conflict 串行；
- due daily limit/UTC、malformed legacy Evidence fail-closed、refuting/superseded Claim；
- bounded invalidation 的五种 selector、200/has_more、Schedule/Health/Timeline/SSE trigger 原子投影；
- cancel/deadline/sql.ErrTxDone、真实 SQLSTATE、JSONB/array binding、Rows/connection release；
- due 与 invalidation 关键索引/EXPLAIN，无 N+1 或 statement 数增长。

## 8. Rollback And Deferred Integration

- TODO 9 前回滚只删除本 child staged GORM 文件并还原本包内必要共享 helper；legacy production 与 Schema 不变；
- TODO 9 后、Final 前仍保留 legacy wiring；Final 统一切 `cmd/api` Composition 并登记 legacy 删除清单；
- Review Core、Interview、Learning Path 三个 child 都通过后，由父任务执行共享 Session/Answer/Path 跨模块门禁；
- TODO 3 Atlas 只阻断 Final，不阻断 staged 实现；本 child 不修改 migration 或父任务 AC。
