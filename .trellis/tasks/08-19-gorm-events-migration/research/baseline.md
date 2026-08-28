# Events GORM 迁移基线

## 1. 模块范围

- Application Port：`internal/events/application/ports.go`。
- legacy PostgreSQL Store：`internal/events/adapter/postgres/store.go`、`append.go`。
- Domain 契约：`internal/events/domain/event.go`、`append.go`、`cursor.go`。
- SSE consumer：`internal/events/http/handler.go`。
- Schema 与 producer trigger：`migrations/00020_rag_conversation_sse.sql`、`00023_rag_stage_projection_index.sql`、`00047_m8_learning_sse.sql`、`00048_m8_memory_stable_owner.sql`、`00059_m8_interview_memory_integrity.sql`、`00060_review_shared_learning_path.sql`、`00076_git_remote_sync.sql`。

本 child 只允许修改 Events Application transaction Port 与 Events PostgreSQL Adapter 的 staged 文件/共享 helper。Domain、HTTP、migration、trigger、producer owner 和 `cmd/**` 不在实现范围。

## 2. 生产与测试接线

- 生产共有 7 个 `NewStore` 构造：API 4 个、Worker 3 个；部分 helper 当前只接收 `*pgxpool.Pool`，Final 不能机械替换构造器，必须传完整平台 Pool。
- 生产共有 17 个 `AppendTx` 调用，分布在 Agent、Change Control、Conversation、Export、Health、Knowledge、Tools；涉及 13 个 producer/holder 文件。
- 测试共有 41 个 legacy Store 构造，分布在 23 个文件；另有 7 个 legacy `Appender` fake。TODO 9 前均保持不变。
- SSE HTTP Handler 只依赖 read `Store`；事务追加调用方依赖 `Appender`。因此 staged GORM Store 可以独立实现 `Store + ScopedAppender`，无需同时迁移 HTTP 或 producer。

## 3. 数据与查询契约

- `ops.server_event.seq` 是跨 Workspace 共享的全局 `bigserial`；Workspace 内严格递增但允许 gap。
- `CurrentWatermark` 为 Workspace 内全部物理行的 `MAX(seq)`，不按 retention 过滤；无行返回 0。
- `EarliestRetained`/`ListAfter` 以 PostgreSQL `CURRENT_TIMESTAMP` 判定 `expires_at`；无 retained row 返回 nil。
- `ListAfter` 使用 `workspace_id`、`seq > cursor`、`ORDER BY seq ASC`、Limit `1..100`，扫描后复核 Workspace 与严格递增。
- JSON 摘要必须是 Domain 白名单对象，最大 16 KiB；可选 Conversation/Workflow ID 受复合 FK 约束。
- `OccurredAt` 是 caller time，Adapter 规范化为 UTC 微秒；`expires_at` 必须由数据库表达式保持恰好 24 小时。

## 4. 事务与幂等契约

- legacy `AppendTx(context.Context, any, AppendRequest)` 断言 `pgx.Tx`，从不拥有 commit/rollback。
- 唯一事实为 `(workspace_id,source_event_ref)`；INSERT 使用 `ON CONFLICT DO NOTHING RETURNING`，无行时同事务重读 winner。
- exact replay 必须比较 Workspace、两个可选 owner ID、type/ref/version、summary、schema/source、occurred/expiry 全部 binding；同 key 不同 binding 为 conflict。
- 并发 claim 依赖 PostgreSQL unique index，不使用 advisory lock；caller 选择更强隔离级别时允许 `40001` retry。
- 手写 Append、Workflow/Learning/Memory/Shared Path/Git trigger 都在 owner 事务内追加到同一表；迁移不得拆出独立事务。

## 5. 现有验证证据

- `store_integration_test.go` 覆盖 Workspace 隔离、全局 seq gap、expired 过滤但 watermark 保留、升序分页、连接释放、corrupt JSON 无 partial、unique source、exact replay/conflict、caller commit/rollback、nil tx 和并发 claim。
- `handler_postgres_integration_test.go` 覆盖 invalid/future/expired cursor、Workspace 隔离、fresh watermark 和新事件追踪。
- migration integration 覆盖 trigger 摘要脱敏、固定 24 小时 expiry、跨 Workspace FK 与必要字段约束。
- 以上真实 PG fixture 全部实例化 legacy `NewStore(pgxpool)`；不能证明 GORM placeholder、database/sql JSONB、opaque scope、同一平台 Pool 或 GORM 连接释放。

## 6. 阶段边界与已知盲区

- 当前 `ZHIXU_TEST_DATABASE_URL` 未配置；TODO 9 的真实 PostgreSQL、并发、事务原子性、Handler 和 EXPLAIN 均未证明。
- 当前 opaque scope 不携带可比较的 Pool identity，Events Adapter 能拒绝 nil、非平台实现和 stale scope，但不能独立识别来自另一平台 Pool 的 active scope；同池由 owner Composition/TODO 9 保证，若需运行时强制须另改 Foundation。
- 仓库没有生产物理 retention cleanup；测试 DELETE 仅用于 fixture/故障注入。新增清理会使 `MAX(seq)` watermark 回退，必须另立产品/架构任务。
- Store 没有 Conversation/Workflow“主体过滤”契约，只有 Workspace + sequence；本迁移不得新增过滤维度。
- TODO 9 前生产 7 个构造点和 17 个追加点全部保持 pgx；child 状态必须为 `in_progress`，PRD AC 不勾选，不归档。

## 7. 推荐实现形状

- `gorm_model.go`：私有 JSONB text `driver.Valuer`/Scanner 或等价单参数 carrier，不使用 `gorm.Model`/Hook/自动时间。
- `gorm_queries.go`：固定 `?` SQL、guarded Row/Rows、共享 no-row 辅助。
- `gorm_store.go`：`NewGORMStore(*platformpostgres.Pool)`、三个 read 方法和 `AppendScoped`。
- 小范围重构 legacy `store.go`/`append.go`，只抽取共享 scanner、binding、校验和错误分类；生产行为不变。
