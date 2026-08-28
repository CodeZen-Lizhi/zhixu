# Research: Events PostgreSQL SQL/Schema

- Query: 审计 `internal/events/adapter/postgres`、`ops.server_event` 全部 Schema/Trigger/Index 与现有测试，为 staged GORM 迁移确定 SQL、事务、幂等、保留期、水位、游标、SQLSTATE 和 JSONB 不变量。
- Scope: internal
- Date: 2026-08-19

## Findings

### Files Found

- `internal/events/adapter/postgres/store.go`：legacy pgx 水位、最早保留边界、Workspace keyset 重放、严格扫描与查询错误分类。
- `internal/events/adapter/postgres/append.go`：调用方事务内追加、来源幂等 exact replay、冲突检测和 PostgreSQL SQLSTATE 分类。
- `internal/events/adapter/postgres/store_integration_test.go`：真实 PostgreSQL 的隔离、保留期、分页、追加、回滚和并发基线。
- `internal/events/http/handler.go`：水位/最早保留边界的调用顺序及保留期竞态二次校验。
- `internal/events/http/handler_postgres_integration_test.go`：真实 HTTP + PostgreSQL 的 future/expired cursor、Workspace 隔离和新事件追踪。
- `internal/events/domain/event.go`：24 小时保留期、JSON 摘要白名单、持久 Event 与公开 Envelope 不变量。
- `internal/events/domain/cursor.go`：正十进制 `Last-Event-ID`、future/expired 判定。
- `internal/events/application/ports.go`：legacy `AppendTx(context.Context, any, ...)` 事务 Port。
- `migrations/00020_rag_conversation_sse.sql`：`ops.server_event` 基表、约束、索引及 Workflow Outbox 投影 trigger。
- `migrations/00023_rag_stage_projection_index.sql`：RAG stage 专用部分索引，不服务通用重放查询。
- `migrations/00047_m8_learning_sse.sql`：Learning owner 变更到 Server Event 的同事务 trigger 投影。
- `migrations/00048_m8_memory_stable_owner.sql`、`00059_m8_interview_memory_integrity.sql`：Memory trigger 重建与 migration backfill 时显式静默 SSE。
- `migrations/00060_review_shared_learning_path.sql`：Shared Learning Path 的安全摘要投影。
- `migrations/00076_git_remote_sync.sql`：Git Remote/Sync Run 的安全摘要投影。
- `internal/platform/postgres/pool.go`、`gorm.go`、`transaction.go`：共享 pgx/database/sql/GORM Pool 与 opaque transaction scope。

### Authoritative Schema

1. `ops.server_event.seq` 是全局 `bigserial` 主键，不是 Workspace 内连续计数器；所有 Workspace 共用序列，因此单 Workspace 合法存在间隙（`migrations/00020_rag_conversation_sse.sql:240-242`）。读取契约只能要求同 Workspace 内 `seq` 严格递增，不能要求相邻。
2. `workspace_id` 必填且引用 `core.workspace`；`conversation_id`、`workflow_run_id` 可空，但非空时分别通过 `(id, workspace_id)` 复合 FK 阻止跨 Workspace 绑定（`migrations/00020_rag_conversation_sse.sql:242-244,266-271`）。GORM 追加必须继续在调用方的同一物理事务中运行，否则“先写 owner，再追加 Event”会触发 `23503` 或失去原子性。
3. 数据库限制 Event Type/Ref/Version、JSONB 对象及 16 KiB 上限、Schema Version 和固定保留期；`expires_at` 必须精确等于 `occurred_at + INTERVAL '24 hours'`（`migrations/00020_rag_conversation_sse.sql:245-272`）。ORM model tag 不能替代这些约束，也不得 `AutoMigrate`。
4. 来源幂等事实源是唯一索引 `(workspace_id, source_event_ref)`（`migrations/00020_rag_conversation_sse.sql:275-276`）。通用水位/重放使用 `(workspace_id, seq)`（`:278-279`）；物理过期扫描可用 `(expires_at, seq)`（`:285-286`），但当前生产代码没有物理清理路径。
5. `idx_ops_server_event_rag_stage_lookup(workspace_id, resource_ref, seq DESC)` 只覆盖六个 RAG stage 类型（`migrations/00023_rag_stage_projection_index.sql:3-12`），不能被当作通用 Store 查询索引。

### Trigger And Transaction Semantics

- Workflow Outbox 的 `AFTER INSERT` trigger 在同一 PostgreSQL 事务追加 Event；它只投影稳定 ID，不复制 raw payload，并由 `occurred_at` 派生 24 小时到期时间（`migrations/00020_rag_conversation_sse.sql:514-543,576-579`）。迁移 Store 不得停用、重建或绕过 trigger。
- Learning 通用 trigger 对 INSERT/UPDATE/DELETE 生成安全投影，用 `txid_current()` 加强来源引用并 `ON CONFLICT ... DO NOTHING`；发生时间来自 owner 行或数据库 `CURRENT_TIMESTAMP`（`migrations/00047_m8_learning_sse.sql:7-79`）。它被多个 Review/Interview/Memory owner 使用（`:81-134`）。
- `00059` 的 schema-only Memory backfill 在独占锁下显式 disable/re-enable `memory_project_server_event`，说明 migration backfill 不等同于业务变更，不能补发通知（`migrations/00059_m8_interview_memory_integrity.sql:46-58`）。
- Shared Learning Path trigger 对 Review 仅投影 `answer_id`，Interview 保持空摘要；Event 与 Path 变更同事务（`migrations/00060_review_shared_learning_path.sql:112-190`）。
- Git Remote/Sync trigger 同样在 owner INSERT/UPDATE 的事务内投影受限 status/stage（`migrations/00076_git_remote_sync.sql:555-606`）。这些 trigger 生产者与手写 `AppendTx` 生产者共享同一表、序列、索引和回放路径。

### Current Read Semantics

- `CurrentWatermark` 使用 `MAX(seq)` 且只过滤 `workspace_id`，故包含已越过逻辑保留期但仍在表内的行；无行返回 0（`internal/events/adapter/postgres/store.go:40-55`）。不能改为全局 sequence last value、`ORDER BY ... LIMIT 1` 的跨租户查询或只看 retained rows。
- `EarliestRetained` 使用数据库 `CURRENT_TIMESTAMP` 和 `MIN(seq)`，只返回仍在 24 小时逻辑窗口内的该 Workspace 最早行；无 retained row 返回 `nil`（`store.go:58-73`）。不能改用 Go `time.Now()`，也不能把 `nil` 变为 0。
- `ListAfter` 是 Workspace-scoped 单列 keyset：`seq > afterSeq AND expires_at > CURRENT_TIMESTAMP ORDER BY seq ASC LIMIT`，最大页 100（`store.go:76-109`; `internal/events/domain/cursor.go:7-8`）。必须保留升序和严格 `>`，不得 OFFSET、无序 `Find` 或全局查询后内存过滤。
- 每行扫描后再次验证 Workspace 与严格递增顺序，并严格解码 JSONB 白名单；一行损坏时整页 fail closed，不返回部分重放（`store.go:93-108,130-164`; `internal/events/domain/event.go:80-102,121-147`）。
- HTTP 在有 cursor 时执行 watermark -> earliest -> list -> 再次 earliest，第二次校验用于关闭读取期间保留边界前移的竞态（`internal/events/http/handler.go:148-205`）。GORM Store 方法不能缓存应用时钟或跨调用缓存 earliest。

### Current Append And Idempotency Semantics

- `AppendTx` 先把 `OccurredAt` 规范化为 UTC 微秒，再做完整 Domain 校验；调用方不能传 seq/expires_at（`internal/events/adapter/postgres/append.go:27-45`; `internal/events/domain/append.go:23-36`）。
- INSERT 显式将 JSON 文本 cast 为 `jsonb`，以同一个规范时间生成 `occurred_at` 与 `expires_at`，用唯一来源索引 `ON CONFLICT DO NOTHING RETURNING`（`append.go:17-25,47-51`）。
- INSERT 成功仍做完整 binding 比对；冲突无返回行时重新按 `(workspace_id, source_event_ref)` 读取，并逐字段比较 Workspace、两个 optional ID、type/ref/version、结构化摘要、schema/source/time/expiry（`append.go:52-83`）。因此“相同 key”不等于 replay，只有完整事实一致才是 exact replay。
- 默认 PostgreSQL Read Committed 下，并发唯一键 claim 会等待 winner 提交，后续 SELECT 使用新 statement snapshot；现有真实 PG 测试要求两个相同请求恰好一 created/一 replayed，不同 binding 恰好一 success/一 conflict（`store_integration_test.go:168-230,239-284`）。如果外层 scope 使用 Serializable/Repeatable Read，允许 PostgreSQL 返回 `40001` 并由调用方重试，不得伪造 replay。
- Appender 从不提交/回滚 caller transaction；现有测试把 owner Conversation 和 Event 同事务提交，并证明 caller rollback 后两者不可见（`store_integration_test.go:102-166`）。

### Recommended Staged GORM Path

1. **使用 Raw SQL，不使用 CRUD builder/model hooks。** 四条 read SQL 和 append/replay lookup 都依赖 PostgreSQL casts、`CURRENT_TIMESTAMP`、aggregate、`RETURNING`、`ON CONFLICT DO NOTHING` 以及固定列顺序。项目已固定 GORM `v1.31.2`、postgres driver `v1.6.2`、pgx `v5.10.0`（`go.mod:14,42-43`）；vendor 的 `Raw` 保留参数表达式，`Row/Rows` 返回 `database/sql` scanner（`vendor/gorm.io/gorm/chainable_api.go:461-470`; `vendor/gorm.io/gorm/finisher_api.go:516-533`）。
2. **构造器只接受一个 `*platformpostgres.Pool`。** 从同一 Pool 获取 `GORM()` 与 `UnitOfWork()`，避免 read root 和 scope 来自不同数据库；平台本身用同一 pgxpool 打开 database/sql/GORM facade（`internal/platform/postgres/pool.go:26-32,57-75,157-163`）。
3. **新增 opaque scoped 追加 Port，冻结 legacy Port。** 仿照 Audit 的 `ScopedAppender.AppendScoped(... foundation.TransactionScope ...)`（`internal/audit/application/ports.go:23-40`）；GORM adapter 用 `platformpostgres.GORMTransaction(scope)` 获取仍 active 的 `*gorm.DB`，不暴露 `*gorm.DB` 给 Application（`internal/platform/postgres/transaction.go:70-97`）。生产 `AppendTx(any)` 调用方在 TODO9/Final 前继续走 pgx Store。
4. **Raw query 形状应与 legacy 等价：**
   - watermark: `SELECT COALESCE(MAX(seq),0) ... WHERE workspace_id=?::uuid`；
   - earliest: `SELECT MIN(seq) ... WHERE workspace_id=?::uuid AND expires_at>CURRENT_TIMESTAMP`；
   - list: 保留 `seq>? AND expires_at>CURRENT_TIMESTAMP ORDER BY seq ASC LIMIT ?`；
   - append: 保留 INSERT/ON CONFLICT/RETURNING 与 text casts；
   - replay lookup: `WHERE workspace_id=?::uuid AND source_event_ref=?`。
5. **GORM `?` 每次出现都消耗一个参数。** legacy append 的 `$10` 在 occurred/expires 两处复用（`append.go:21`）；GORM SQL 若写两个 `?::timestamptz`，必须传两次同一个微秒化 `OccurredAt`，否则参数错位。不要用 Go 先算 expiry 替代数据库表达式。
6. **JSONB 使用单参数 string `driver.Valuer`。** `json.Marshal(PayloadSummary)` 的 `[]byte` 经 pgx stdlib 可能按 `bytea` 绑定；沿用已 staged Audit 的 JSONB carrier，校验 `json.Valid` 后 `Value()` 返回 string（`internal/audit/adapter/postgres/gorm_model.go:10-19`），SQL 端再 `?::jsonb`。读路径继续选 `payload_summary::text` 并调用 Domain strict decoder，不能直接 Scan 到宽松 map/struct。
7. **保留手工 scanner。** SELECT 列及顺序保持 `eventSelect`，optional UUID 继续以 `::text` + nullable string 读取；不要依赖 GORM naming、zero value、association、soft delete 或 time hook。可沿用 Audit 的 guarded `Raw().Row()/Rows()` helper 模式（`internal/audit/adapter/postgres/gorm_queries.go:35-66`）。
8. **保留 no-row 与上下文 cause。** GORM/database/sql 的 `Row.Scan` 返回 `sql.ErrNoRows`，legacy 是 `pgx.ErrNoRows`；helper 应同时识别二者及 `gorm.ErrRecordNotFound`。在分类未知 Store failure 前先保留 `ctx.Err()`、`context.Canceled`、`context.DeadlineExceeded` 和 `sql.ErrTxDone`，满足取消 cause 契约（`.trellis/spec/backend/error-handling.md:29-36`）。
9. **SQLSTATE 映射不扩张语义。** 当前明确映射 `23503/23514 -> SSE_APPEND_BINDING_INVALID`、`23505 -> SSE_APPEND_CONFLICT`、`40001/40P01 -> retryable SSE_STORE_UNAVAILABLE`，其余为 retryable dependency unavailable（`append.go:99-115`; `append_test.go:12-33`）。平台设置 `TranslateError=false`，pgx stdlib 可保留 `*pgconn.PgError`（`internal/platform/postgres/gorm.go:34-50`）；GORM classifier 应继续用 `errors.As`，不要依赖错误字符串或 GORM 翻译类别。
10. **不执行 schema 操作。** staged adapter 不调用 `AutoMigrate`/`Migrator`、不创建索引、不重建 trigger、不改变 retention。Schema 仍只由 Goose migration 拥有。

### Retention And Physical Cleanup Caveat

- 对 `internal/`、`cmd/`、`migrations/` 全量搜索后，没有找到生产 `DELETE FROM ops.server_event WHERE expires_at ...` 或 Events cleanup Port；现有 `DELETE FROM ops.server_event` 只在测试清理/故障注入中（例如 `internal/health/adapter/postgres/integration_cleanup_test.go:51`、`internal/conversation/adapter/postgres/dispatch_integration_test.go:830`）。当前实现是**逻辑保留过滤**，不是物理 retention worker。
- 因为 watermark 当前来自表内 `MAX(seq)`，直接新增物理删除会使某 Workspace 水位回退；旧 cursor 可能从“expired”误判为“future”。除非另建持久 per-Workspace high-water fact 并重新设计 cursor 语义，否则本 staged 迁移不应发明物理清理。
- PRD 的“清理路径”应在 design/implement 中解释为“保持现有逻辑 expiry 查询”；若产品确实要求物理清理，应拆独立任务设计水位事实、批量上限、锁/并发、删除索引、恢复和发布迁移，不能作为 ORM 等价改写顺带实现。

### Existing Evidence

- Store integration 覆盖：全局 sequence 下的 Workspace 隔离/间隙、expired 过滤但 watermark 保留、升序有界分页、连接释放（`store_integration_test.go:25-68`）；损坏 JSON 不返回部分页（`:70-85`）；唯一来源索引（`:87-100`）；append exact replay/conflict/commit/rollback/nil tx（`:102-166`）；并发 exact/conflict（`:168-284`）。
- HTTP PostgreSQL integration 覆盖：invalid/future/expired cursor、Workspace A/B 隔离、Header 优先级和 fresh watermark 后的新事件（`handler_postgres_integration_test.go:30-118`）。
- Migration integration 覆盖 Workflow Outbox 摘要不泄漏 raw payload、固定 24 小时 expiry、跨 Workspace conversation FK `23503`（`internal/platform/migration/conversation_sse_integration_test.go:256-299`）；缺少 `source_event_ref` 的 `23502` 也由 DB 拒绝（`conversation_sse_invariants_integration_test.go:70-75`）。
- 以上 fixture 均实例化 legacy `NewStore(pgxpool)`；它们不能证明 GORM placeholder、database/sql JSONB、opaque scope 或 GORM 连接释放。

### TODO9 Real PostgreSQL Scenarios

使用同一个 disposable database 和同一个 `platformpostgres.Open` Pool（migration 阶段可另用 `OpenMigration`，运行阶段必须重新以 `Open` 获取 GORM root），至少验证：

1. legacy/GORM 对同一 Schema 的 watermark、earliest、ListAfter 结果完全一致；包含 Workspace 交错 seq、过期/临界/retained 行、空 Workspace、页大小 1/100 和 seq gap。
2. 在 `UnitOfWork.Within` scope 中先写 owner Conversation/Workflow，再 `AppendScoped`；commit 后两者可见，callback error/cancel/constraint failure 后两者均回滚。
3. 同 key exact replay、同 key不同任一 binding conflict；微秒化 occurred/expiry、optional UUID、JSONB 空对象/全白名单字段 round-trip 完全一致。
4. 两个独立 GORM transaction 并发 exact claim 和 conflicting claim，断言一 created/一 replayed 或一 success/一 stable conflict，无超时、死锁或双写；Serializable 下若出现 `40001` 必须分类 retryable。
5. 缺失 Workspace、跨 Workspace Conversation/Workflow 验证真实 `23503 -> SSE_APPEND_BINDING_INVALID`；合成/真实 `23514`、`23505`、`40001`、`40P01` 保持分类；错误响应和日志不得带 SQL、DSN、JSON payload 或绝对路径。
6. caller cancel、deadline、已结束 scope 均保留 `errors.Is` cause，transaction rollback，`sql.DB`/pgx pool acquired connection 回到零。
7. handler 级保留期竞态继续执行 watermark -> earliest -> list -> earliest；临界时间由 PostgreSQL 控制，不能由应用 Clock 模拟为通过。
8. trigger 生产者（至少 Workflow Outbox 与一个 Learning/Git owner）产生的 Event 能由 GORM Store 重放，摘要不含正文/Secret，且 owner rollback 时 Event 同步回滚。
9. 对 watermark、earliest、ListAfter 做 `EXPLAIN (ANALYZE, BUFFERS)`；确认使用现有索引且大批过期行下仍有界。若性能证据要求新索引，单独 migration 评审，不能由 GORM 自动创建。
10. 明确记录“无生产物理清理路径”；不得用测试 cleanup SQL 冒充 retention worker 验收。

## External References

- 本研究不依赖外部二手资料。库版本以仓库 `go.mod:14,42-43` 为准；Raw/Row/Rows 行为以 vendored GORM v1.31.2 源码为一手证据（`vendor/gorm.io/gorm/chainable_api.go:461-470`; `vendor/gorm.io/gorm/finisher_api.go:516-533,783-795`）。
- PostgreSQL 行为的项目事实由真实 migration 与 integration tests 所有；上线前仍需 TODO9 在目标 PostgreSQL 版本上重跑，当前仓库未固定可在本机读取到的服务端版本证据。

## Related Specs

- `.trellis/spec/backend/database-guidelines.md:1058-1175`：Conversation/SSE persistence、24 小时 cursor、同 UoW、真实 PG 门禁与禁止正文进入 Event。
- `.trellis/spec/backend/directory-structure.md:51-66`：Composition Root 所有权及 API Conversation/Event 必须共享同一个 Pool/Store。
- `.trellis/spec/backend/error-handling.md:27-36,38-56`：稳定错误、retry、取消 cause 与 SSE 摘要。
- `.trellis/spec/backend/quality-guidelines.md:53-62,445-486`：跨层 SSE、真实 PostgreSQL 与 HTTP/Compose 证据不能由单层测试替代。
- `docs/architecture/application-contracts.md:76-92`：SSE 仅为有序失效通知、Last-Event-ID 恢复、Workspace owner 与敏感字段禁入。

## Caveats / Not Found

- `ZHIXU_TEST_DATABASE_URL` 当前未配置，无法在本研究中运行真实 PostgreSQL、确认服务端版本、执行计划或 GORM transaction/concurrency 验证；所有 TODO9 项仍是未证明状态。
- 当前 Events task 的 `prd.md`、`design.md`、`implement.md` 和 context manifest 已齐，`task.json` 仍为 `planning`；实现前仍需用户审批最终方案并由 main session 正式 activation。
- 没有发现 `ops.server_event` 后续 ALTER TABLE；后续 migration 只增加/替换 producer trigger 和一个专用查询索引。没有发现数据库级 append-only trigger 禁止直接 UPDATE/DELETE `ops.server_event`，因此不可把“Event 表不可变”误报为 DB 强制事实。
- 没有发现生产物理 retention cleanup、独立高水位表或每 Workspace sequence。若 PRD 将“清理”解释为物理删除，当前需求与既有 cursor 设计存在未解决冲突。
- GORM staged Store 在 TODO9 前不得替换 `cmd/api`、`cmd/worker` 或任何 legacy `AppendTx(any)` caller；production wiring 属于 Final gate。
