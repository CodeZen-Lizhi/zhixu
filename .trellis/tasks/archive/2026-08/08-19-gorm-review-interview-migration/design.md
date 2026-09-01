# Review Interview GORM 迁移设计

## 1. Boundary And Compatibility

本 child 采用“legacy pgx 保留 + staged GORM sibling + Final 统一切换”边界：

- `Repository`、`DB`、`NewRepository(DB)` 与 API/Worker wiring 原样保留；
- 新增 `GORMRepository` / `NewGORMRepository(*platformpostgres.Pool)`，实现现有 `application.Store`、`application.QuestionSource` 和 `AbandonStaleCompletions`；
- 构造器从同一个 Pool 取得 `Pool.GORM()` 和 `Pool.UnitOfWork()`，caller 不能分别传 root/UoW；
- 多语句写流程唯一事务入口为 `UnitOfWork.Within`，callback 内使用 `platformpostgres.GORMTransaction(scope)`；
- scoped `*gorm.DB` 不逃逸 callback，不自行 Begin/Commit/Rollback，不解包 `*sql.Tx`；
- Domain/Application、Review Core、Learning Path、Artifact、Memory、migration 和 `cmd/**` 不改；
- TODO 9 前不接生产、不双写、不 fallback、不删除 legacy。

该边界让 staged 实现可独立回滚，同时避免提前改变 Review 三 owner 的共享 Schema 和生产行为。

## 2. File Layout

- `gorm_core.go`：Repository 构造、ready/within、Row/Rows guard、no-row/context/SQLSTATE 分类、接口断言。
- `gorm_model.go`：JSONB carrier、nullable/scanner、Session/Question/Turn/Report/Path/Step/receipt/reservation 私有 persistence record。
- `gorm_helpers.go`：共享 receipt、reservation、hold 与固定 SQL helper；其余固定 `?` SQL 与对应 read/command/completion owner 共置，保留显式列清单、锁/CTE/CAS/RETURNING。
- `gorm_read.go`：QuestionSource Select、Get/List、Path 聚合读取及共享 scanner。
- `gorm_commands.go`：Start/Submit、Path Step/Status 与 receipt replay。
- `gorm_completion.go`：Begin/Prepare/Complete 与 stale completion maintenance。
- 现有 `repository.go`、`commands.go`、`codec.go`、`errors.go` 保持 legacy owner；仅抽取数据库无关且能静态证明无行为变化的 codec/equality/domain helper。
- TODO 9 只扩展现有 integration 文件建立 implementation fixture，不新增测试文件。

## 3. Schema And Persistence Boundary

Schema 以 `00046`、`00056`、`00057`、`00059`、`00060` 为唯一事实源：

| Owner | Relation | 关键边界 |
| --- | --- | --- |
| Interview | `learning.interview_session` | 与 `learning.review_session` 共享 shell；Workspace/session 唯一、状态/version/time 受 trigger 保护 |
| Interview | `learning.interview_question` | 稳定 `question_no`、证据/provenance JSON、回答与状态 CAS |
| Interview | `learning.interview_turn` | append-only，request/idempotency 与 question/answer 绑定 |
| Interview | `learning.interview_report` | completion terminal fact，Artifact/Revision/digest 精确绑定 |
| Interview | `learning.interview_command` | append-only receipt，key/hash/type/response 精确重放 |
| Interview | `learning.interview_completion_reservation` | snapshot version、单一 Artifact digest、PENDING/COMPLETED/ABANDONED 与 terminal Report/Path tuple |
| Shared Path | `learning.interview_learning_path` | `learning.learning_path` 的 auto-updatable INTERVIEW view，`LOCAL CHECK OPTION` 强制 origin |
| Shared Step | `learning.interview_learning_path_step` | shared step 的 INTERVIEW view，按父 Path origin 隔离 |
| Artifact | Artifact hold/revision tables | 本 child 只按既有 SQL 锁定/核验/释放 REPORT、PATH 两个 hold，不迁移 owner |

Persistence record 只用于显式 scan/carrier，不拥有 Schema，不使用 `gorm.Model`、hook、soft delete、association 或自动时间。JSONB carrier 的 `Value()` 返回校验后的 JSON string，避免 `[]byte` 被 pgx stdlib 推断为 `bytea`；UUID/text 数组以 `pq.Array` 作为一个 `driver.Valuer` 参数。nullable UUID/time 使用 pointer 或 `sql.Null*`，所有 scan 后继续执行现有 ID、状态、canonical JSON、digest、version 和 Domain validation。

Interview staged SQL 继续读写 compatibility views，不直接访问共享 Path base table。Review Learning Path owner继续按 `origin_type='REVIEW'` 访问 base table；两条实现不共享 Go Repository，也不建立跨模块 import。

## 4. Execution And Lock Matrix

所有公开方法先校验 repository、GORM root、UoW、context 和输入；单 SQL root read 使用 `database.WithContext(ctx)`，多语句写使用同一 UoW scope。锁顺序必须按操作分别冻结，不能抽成一个“通用 Interview 锁序”。

| Flow | 固定顺序 |
| --- | --- |
| Start | transaction advisory key -> exact START receipt -> insert Review shell/Interview session -> child trigger 锁 shell -> questions -> receipt |
| Submit | advisory key -> exact SUBMIT receipt -> Interview session + Review shell `FOR UPDATE` -> pending completion reservation check -> Question/Turn -> Session CAS -> receipt |
| BeginComplete | advisory key -> Session + shell `FOR UPDATE` -> exact COMPLETE receipt -> reservation `FOR UPDATE` -> freeze/reopen snapshot version -> question completeness；不读写 hold |
| PrepareComplete | advisory key -> Session + shell `FOR UPDATE` -> reservation `FOR UPDATE` -> single Artifact digest CAS -> mismatched ACTIVE hold ORPHANED -> same-digest ORPHANED hold recovery |
| Complete | advisory key -> Session + shell `FOR UPDATE` -> receipt -> reservation `FOR UPDATE` -> questions -> two holds `FOR UPDATE` -> Report/Path/Steps -> shell/session CAS -> delete exactly two holds -> receipt -> reservation COMPLETED CAS |
| Path Step | advisory key -> receipt -> INTERVIEW Path `FOR UPDATE` -> ordered Steps -> target Step CAS -> parent Path CAS -> receipt |
| Path Status | advisory key -> receipt -> INTERVIEW Path `FOR UPDATE` -> ordered Steps -> transition/all-terminal validation -> Path CAS -> receipt |
| Maintenance | reservation candidates `FOR UPDATE SKIP LOCKED` -> ABANDONED -> matching ACTIVE holds ORPHANED；不额外锁 Session |

Maintenance 的反向关系顺序是现有协议的一部分：ORPHANED hold trigger 在触碰 Session 前返回，从而与 completion 的 Session-first 顺序兼容。GORM 版必须保留单 CTE，不把候选查询和两个 UPDATE 拆成多条语句，也不新增 Session lock。

## 5. Command And Recovery Flows

### 5.1 Start

```text
UnitOfWork.Within
  -> advisory lock(workspace + idempotency key)
  -> exact START receipt replay
  -> insert learning.review_session with INTERVIEW type, NULL deck, config and request hash
  -> insert learning.interview_session; child trigger locks and validates the just-created shell
  -> insert ordered questions with explicit columns
  -> append strict receipt
  -> commit
on transaction error before known callback success:
  -> root FindStartReplay for commit-response-loss/unique race
```

Question ordering、difficulty/topic/claim evidence 和 memory provenance 与 legacy 相同；不得由 GORM association cascade 写 Questions。

### 5.2 Submit

```text
UnitOfWork.Within
  -> advisory lock
  -> exact SUBMIT receipt replay before mutable-state decisions
  -> Session + Review shell FOR UPDATE
  -> reject pending completion reservation
  -> validate status/deadline/version/question order/follow-up
  -> insert immutable Turn
  -> CAS answered Question
  -> optionally insert follow-up Question
  -> CAS Session/version
  -> reload ordered Questions and append strict receipt
  -> commit
on ambiguous failure -> root FindSubmitReplay
```

Receipt replay 必须早于可变状态判断；否则 response loss 后的合法重试会被新版本/终态错误遮蔽。

### 5.3 Completion Protocol

Completion 是三阶段 durable protocol，不可合并或拆成跨事务补偿：

1. `BeginComplete` 冻结当前 Session snapshot version，创建或从 ABANDONED 重开 PENDING reservation；它不设置 Artifact digest，也不读写 hold。
2. `PrepareComplete` 为相同 key/hash/manual-end/snapshot 一次性设置单一 `artifact_digest`，把不同 digest 的 ACTIVE hold 标为 ORPHANED，并在同 role 没有 ACTIVE hold 时恢复相同 digest 的 ORPHANED hold；相同 digest 精确 replay，不同 digest conflict。
3. Artifact bridge 在 child 外按该 digest 创建 REPORT/PATH 两个 Artifact/Revision 及 ACTIVE holds。
4. `Complete` 核验 reservation、Artifact tuple/digest、问题闭包和恰好两个 ACTIVE holds，在一个事务中写 Report、INTERVIEW Path、ordered Steps、Review shell/Interview terminal CAS、receipt、reservation COMPLETED，并删除恰好两个 holds。

任何 missing/duplicate/mismatched hold、Artifact binding、digest、attempt、question、CAS 或 trigger 失败都必须回滚所有 Interview 写入。Commit 返回错误后在 root 查询 `FindCompleteReplay`；只有完整 durable receipt/binding 才返回 replay，不能根据部分 Report/Path 猜测成功。

### 5.4 Path Commands

Path/Step 命令继续使用 compatibility view，让数据库 origin guard 始终生效。每个命令固定 advisory lock -> exact receipt -> Path/Steps lock/read -> Domain transition -> CAS -> append-only receipt -> commit。Step 先 CAS 自身，再 CAS 父 Path；全部 terminal 时父 Path自动完成。任何 RowsAffected/no-row 由调用点映射到既有 NotFound/StateConflict，而不是通用 dependency error。

## 6. Read, JSON And Time Contracts

- `QuestionSource.Select` 保持一个 set-based 查询，从 active index/manifest/chunk/provenance 选择 topic-scoped confirmed evidence；输入数组使用 `pq.Array`，输出继续按 claim ID 字典序以及每个 claim 内既有 evidence 顺序稳定排列，并复核数量、重复和 Workspace 绑定；不得改成 caller 输入顺序。
- `Get` 返回 Interview 快照并读取 ordered Questions/Turns；所有多行路径 `Rows()` 后 `defer Close()`，循环后检查 `rows.Err()`，任何 scan/validation 错误不返回部分聚合。
- `GetPath` 从 INTERVIEW view 读取 Path 后以 `ORDER BY step_no,id` 单条读取 Steps，复核连续序号、Workspace/path 和 Artifact/evidence binding。
- `List` 保持 `(review_session.started_at,interview_session.session_id) DESC` tuple keyset、Workspace predicate、UTC 微秒 cursor 与显式 page limit + 1，不改为 `updated_at` 或 offset。
- 单行 miss 使用 `Raw(...).Row().Scan`；禁止依赖 `Raw().Scan` 的零行行为。
- caller 提供的业务时间继续 UTC/微秒规范化；maintenance/lease/expiry 继续使用现有数据库 `clock_timestamp()` / statement time，不用 GORM `NowFunc`。
- JSONB receipt、evidence、answer/report/path payload 均走现有 strict codec：拒绝未知字段、尾随 JSON、非法 canonical/hash/binding。

## 7. SQL, Error, Context And Logging

- 锁、CTE、views、array、JSONB、`RETURNING`、CAS、advisory lock、SKIP LOCKED 和 maintenance 均使用固定 Raw/Exec SQL；简单明确 insert 也必须列出字段。
- GORM SQL 使用 `?` placeholders 与显式 PostgreSQL casts；表、列、排序和状态常量只来自包内固定文本，用户输入只作为参数。
- 禁止 `Save`、implicit Updates、Preload/Association、AutoMigrate/Migrator、动态 identifier 和 root `.Transaction`。

错误分类优先级：

1. 已有 `foundation.Error` 原样返回；
2. `ctx.Err()` 与 distinct `context.Cause(ctx)`，保留 canceled/deadline sentinel 和自定义 cause；
3. `sql.ErrTxDone`、`sql.ErrNoRows`、`gorm.ErrRecordNotFound`；
4. `*pgconn.PgError` 的精确 SQLSTATE/constraint；
5. 不泄露 SQL/参数/driver 明细的 dependency unavailable。

no-row 由调用点决定 miss、NotFound、StateConflict 或 ReservationConflict。原始 PgError cause 必须可被 `errors.As` 获取。Foundation GORM logger 已禁用，本 Adapter 不新增 SQL/参数日志；回答、证据、报告、Path 内容、receipt、Artifact、DSN 和绝对路径不得进入公开错误或验证记录。

## 8. Verification Design

### Static Stage

- 运行 Interview 包 test/race/vet、integration compile-only、API/Worker compile-only、vendor list/module verify、Trellis validate、gofmt 和 `git diff --check`。
- 静态确认生产仍只调用 legacy `NewRepository`，无 selector/双写；Domain/Application、Review Core、Learning Path、migration、Foundation、go.mod/go.sum/vendor 不因本 child 改动。
- 扫描 `AutoMigrate|Migrator|Save|Preload|Association`、root `.Transaction(`、独立 `gorm.Open`、动态 SQL、底层 DB 类型泄漏和敏感日志。
- 使用 `go-review`、`sql-code-review` 与独立 `trellis-check`；修复范围内明确缺陷并写入 `research/static-validation.md`。

### TODO 9 PostgreSQL Stage

只在现有 integration 文件扩展 fixture：每个 legacy/GORM 子用例创建独立 disposable database，先用 migration pool 迁移并关闭，再由一个 `platformpostgres.Open` 实例提供 `DB()`、`GORM()` 和 `UnitOfWork()`。禁止自行 `gorm.Open` 或共享固定 ID/key 的同库双实现串跑。

GORM commit response-loss 不能复用 pgx Begin wrapper；同包测试应包装 Repository 的 `foundation.UnitOfWork`，让真实 `Within` 成功 commit 后确定性返回注入错误，再验证 root receipt recovery。并发使用 barrier/lock/notification，不靠 sleep。

必须成对覆盖：

- Question selection 的 active index、array/JSONB/provenance、顺序、坏数据 fail-closed；
- Start/Submit same-key replay、different binding conflict、follow-up、deadline/cancel、commit response-loss；
- Begin/Prepare/Complete same-attempt replay、stale attempt、两个 hold、Artifact digest、故障 rollback、maintenance 两种赢家；
- Path/Step CAS、INTERVIEW/REVIEW 双向不可见、history/origin trigger、zero FSRS writes；
- keyset List、Rows/连接释放、SQLSTATE/constraint、trigger/deferred failure；
- maintenance batch/索引与关键 Select/Get/List/Completion SQL 的 EXPLAIN、statement count 和无 N+1。

## 9. Rollback And Final Handoff

- TODO 9 前回滚只删除本 child 的 staged GORM 文件并还原必要的本包共享 helper；legacy production 与 Schema 不变。
- TODO 9 后、Final 前仍保留 pgx wiring 作为回退；Final 统一切 API/Worker Composition 并决定 legacy 删除。
- Review 三 child 的跨 owner Schema/behavior gate由父任务统一收口，本 child 不改其他 Repository。
- TODO 3 Atlas 完成前不做最终 Schema/Composition 收口。
