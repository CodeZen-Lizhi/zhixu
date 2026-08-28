# Audit GORM 迁移基线

日期：2026-08-19

## Owner 与调用点

- Persistence owner：`internal/audit/adapter/postgres`。
- Application transaction owner：`internal/audit/application` 的 Recorder/Appender Port。
- 生产构造仍由 API、Worker、Workspace Control 等入口调用 `NewStore/NewRepository`；本 child 不修改 `cmd/**`。
- legacy `AppendTx(any)` 的直接消费者包括 Workspace rebind、Export download、Model Settings，以及 Knowledge/Conversation/Tools 的审计适配；这些 owner 未迁移前不能接 staged Store。

## Schema 与 SQL

- `00034_learning_ops_auth.sql`：`ops.audit_event`、Workspace/time/id 索引、append-only trigger。
- `00081_workspace_root_rebinding.sql`：`transaction_id DEFAULT pg_current_xact_id()`，要求 Workspace binding history 与 Audit 共享事务。
- `00089_workspace_analysis_tool_refusal_audit.sql`：refusal 到 Audit 的 deferrable FK，要求同事务事实完整。
- 现有写路径：redact/encode -> advisory lock -> exact lookup -> insert returning -> no-row re-read -> binding validation -> commit。
- 全局 `workspace_id=NULL` 不受普通 unique 约束去重，transaction advisory lock 不可删除。
- List 以 Workspace `IS NOT DISTINCT FROM` 和 `(occurred_at,id)` 倒序 keyset 有界读取，最大 200。

## 错误与安全

- no-row -> NotFound；23503/23514 -> consistency invalid；23505 -> idempotency conflict；40001/40P01 -> retryable unavailable。
- 未知驱动错误不暴露原始字符串；Domain 对 correlation/metadata 递归脱敏、canonical JSON 与 reserved envelope fail closed。
- GORM 路径必须以单参数 string carrier 绑定 JSONB，禁止日志记录 JSON、SQL 参数、DSN、Credential 或绝对路径。

## 现有验证与盲区

- 单测覆盖 List scope/cursor、JSON envelope、持久明文 fail-closed 和 SQLSTATE 脱敏分类。
- integration 覆盖精确 replay/conflict、12 并发全局单行、Workspace replay、预置重复行、持久脱敏与 append-only trigger。
- 当前 integration fixture 只构造 pgx pool/legacy Store；`ZHIXU_TEST_DATABASE_URL` 未配置时只能编译，不能证明 GORM JSONB、opaque scope、事务默认 `transaction_id`、取消或连接释放。
- TODO 9 必须从同一个 `platformpostgres.Pool` 获取 `DB()`、`GORM()`、`UnitOfWork()` 后比较 legacy/GORM；生产切换归 Final。
- Foundation `TransactionScope` 当前没有 Pool identity。Audit 可拒绝 nil、非平台和过期 scope，但不能识别另一 Pool
  的仍活跃 scope；同池归属目前必须由 fixture/Composition 保证，若需运行时硬拒绝则由 Foundation child 扩展契约。
