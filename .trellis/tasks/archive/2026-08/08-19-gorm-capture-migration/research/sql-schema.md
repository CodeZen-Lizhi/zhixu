# Capture SQL、Schema 与事务研究

## Schema 事实

- `migrations/00068_capture_profile.sql` 创建 `core.capture`、`core.capture_command`、`ops.capture_outbox`、`ops.capture_attempt` 和四张 Profile 表，并定义 Workspace composite FK、唯一键、部分索引、状态/时间 CHECK、append-only trigger、Evidence closure trigger、stale trigger 与 fail-closed Down。
- `core.capture` 以 `(workspace_id,original_location)` 唯一，Source/Source Version 都受 Workspace binding；所有读写必须携带 Workspace predicate。
- `core.capture_command(workspace_id,idempotency_key)` 是 exact replay 的唯一事实；response JSON 必须严格解码并复核 request hash、command type、target/version 与 Domain 结果。
- Outbox pending 部分索引与 Claim 的 due/terminal/lease predicate、`ORDER BY available_at,id` 对齐。
- Attempt 以 `(capture_id,workflow_run_id,attempt_number)` 唯一，Capture/Source Version/Workflow 均有 Workspace composite FK。
- Profile 每 Source Version 唯一；Revision/Evidence append-only；active attempt 是部分唯一；current revision 使用 deferred closure constraint；Source/Index/Model Run binding 由 FK/trigger保护。
- Workspace Source/Version 与 Artifact 约束来自 `00002_workspace_sources.sql`、`00006_content_artifact.sql`，仍由 Workspace owner 维护。

## 事务与锁序

- Create：Workspace Source facts -> Capture -> Outbox -> receipt -> commit。
- Retry：Capture `FOR UPDATE` -> receipt replay -> Capture CAS -> Outbox -> receipt。
- Processing：Capture `FOR UPDATE` -> Attempt `FOR UPDATE` -> optional Source Version -> Capture CAS -> Attempt CAS。
- Profile Prepare：Source advisory lock -> Capture binding -> Profile/Attempt locks。
- Profile Bind：Attempt `FOR UPDATE` -> Agent Model Run `FOR UPDATE` -> Attempt CAS。
- Profile Complete：Attempt -> Settings/Index shared locks -> Source advisory lock -> Profile -> Agent Run/Calls -> Revision/Evidence -> Profile/Attempt CAS -> Agent Run CAS。
- Profile Fail：Attempt -> Profile -> optional Agent Run -> Profile/Attempt CAS。
- Profile Retry：Profile `FOR UPDATE` -> Capture `FOR UPDATE` -> Profile/Capture CAS -> Outbox -> receipt。

这些顺序来自现有实现和 trigger 互锁，GORM 迁移不得调换。事务级 advisory lock、row lock 与 owner Port 调用必须使用同一个 live scope；root GORM 调用不能参与事务锁。

## 必须保留 Raw 的路径

- Outbox CTE `FOR UPDATE SKIP LOCKED`、`clock_timestamp()` lease；
- transaction advisory lock、`FOR UPDATE/FOR SHARE`；
- `INSERT/UPDATE ... RETURNING`、CAS RowsAffected、DB time/GREATEST；
- Profile Source/Manifest/Chunk 的复杂 join 和 SQL 阶段大小限制；
- Evidence `unnest(?::text[])`、Profile batch `ANY(?::uuid[])`；
- 固定状态分支和 terminal replay 查询。

GORM 版本使用 `?` placeholder。数组必须用 `pq.Array` 或等价 `driver.Valuer` 作为一个参数；裸 slice 会被 GORM 展开。JSONB carrier在校验后返回 string，避免 pgx stdlib 把 `[]byte` 推断为 bytea。

## 错误与安全

- legacy classifier：`23505` version conflict；`23503/23514/55000` consistency violation；`40001/40P01/55P03` retryable；cancel/deadline dependency unavailable。
- GORM 路径还需识别 `sql.ErrNoRows`、`gorm.ErrRecordNotFound`、`sql.ErrTxDone`，并保留 custom `context.Cause`。
- unique receipt 冲突必须先 rollback，再从 root 读取 durable receipt；不能在 aborted transaction中回放。
- 原始正文、URL response、Profile content、receipt JSON、SQL 参数、DSN、Secret 与 stage locator不得进入错误或日志。

## TODO 9

- 对 Create/MaterializeURL/Retry/Profile 每个写点注入失败，证明跨 owner 无部分提交。
- 并发验证 exact replay、Outbox claim/reclaim、Attempt CAS、Profile/Index activation锁交错和 SQLSTATE。
- 验证真实 UUID/JSONB/array/cast、deferred FK/trigger、DB time、cancel/commit outcome、连接释放。
- 对 Capture List、Outbox Claim、Profile Source/Batch 运行目标规模 EXPLAIN，确认 Limit/index/no N+1/无无界 Sort。
