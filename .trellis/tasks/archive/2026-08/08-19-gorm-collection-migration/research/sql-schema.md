# Collection SQL、Schema 与 GORM binding 研究

## Schema owner

- `migrations/00025_smart_collection_health.sql:3-44`：smart_collection 列、JSONB/时间/status/version 约束、active name 与 Workspace/status keyset 索引。
- `00025:104-126`：immutable command receipt、Workspace/idempotency 主键和 aggregate FK。
- `00025:561-659`：aggregate version/query version/archive/delete 与 receipt immutable triggers。
- `migrations/00029_collection_read_model_revision.sql:5-139`：Workspace knowledge/conflict/health revision vector 与 owner triggers。
- `migrations/00026_smart_collection_health_query_indexes.sql:5-13`：health/relation membership/hydration 索引。
- `migrations/00036_export_hardening.sql`、`00063_m9_attachment_export.sql`：Export 以 `(collection_id,workspace_id)` 外键绑定 Collection，ON DELETE RESTRICT。

本迁移不新增/修改 migration，不使用 AutoMigrate。

## SQL 形状

- aggregate/receipt 写入依赖显式 lock/CAS/trigger 顺序，应使用 GORM transaction 内 Raw/Exec，不使用 Save/association。
- `unifiedItemCTE` 跨 core.topic/claim/relation、ops.health_issue、source/version/claim_source 形成统一 item read model。
- page query 的 dynamic WHERE/ORDER BY 仅来自已 canonicalize Registry/Compiler；值参数化。
- count、page、hydration、revision 必须在同一 repeatable-read snapshot；hydration 使用一个 page-sized `unnest` CTE。
- durable scan 的 member exists、page、pair、node SQL 都保持 Workspace predicate、固定排序和 bounded limits。

## GORM positional binding 风险

GORM v1.31.2 Raw/Expr 识别 `?`，不会按 pgx 语义解释 `$n`。Collection compiler 和 keyset builder存在重复 marker：

- topic_id membership 的一个 placeholder 同时用于 Topic 分支和 Claim relation EXISTS；
- text contains/prefix 在 title、summary、alias 三处分用同一 placeholder；
- keyset prefix equality 和当前分支比较可能重用 earlier placeholders。

因此必须在最终 SQL 上解析 `$<digits>`，每次出现转换成 `?` 并复制对应参数。简单 `strings.Replace` + 原 Args 会导致参数数目/顺序错误。转换器必须拒绝 `$0`、越界、残缺和未引用参数；输入只允许本包固定 SQL 与 compiler 产物。

## Array 与 JSONB carrier

- GORM 会展开裸 slice；`ANY(?::text[])`、`ANY(?::uuid[])` 和 `unnest(?::text[],?::uuid[])` 必须传单个 `driver.Valuer`，使用已 vendored/direct 的 `github.com/lib/pq.Array`。
- compiler IN 返回 `[]string`；List statuses、hydration IDs/object types、durable pair/node keys 也是 `[]string`，均在 adapter bind boundary 包装。
- text[] 出参使用 `pq.StringArray`/Scanner；不能依赖 database/sql 直接写入 `[]string`。
- JSONB query_definition/view_config/receipt 写参数不可直接传 `[]byte`，避免 pgx stdlib 推断 bytea；私有 Valuer 校验后返回 string。读取优先 `::text`/严格 scanner，再执行 Domain canonical 与 hash/binding 校验。

## 事务与 SQLSTATE

- default write UoW：Workspace FOR UPDATE -> receipt FOR UPDATE -> aggregate FOR UPDATE/CAS -> aggregate write -> receipt write -> commit。
- read UoW：Repeatable Read + Read Only；`SET LOCAL statement_timeout='1500ms'` 必须在 callback transaction 内。
- scoped durable verifier：caller-owned active scope，FOR SHARE collection definition，revision/count复核；不拥有 commit/rollback。
- 精确保留 SQLSTATE：23505(active name / idempotency)、23503/23514/23502、57014、40001/40P01/55P03/08000/08003/08006/57P01、55000。
- active foreign-Pool scope 无法在当前 Foundation contract 中识别；不是 Collection 层可独立解决的问题。

## TODO 9 数据库证据

真实 PostgreSQL 必须覆盖 positional renderer 的重复/多位 marker、pq.Array text/uuid casts、JSONB carrier、nullable/array scanner、trigger SQLSTATE、commit/rollback、timeout/cancel、revision triggers、cursor/keyset、durable drift 和连接释放；并对既有 List/Search/Result/Durable SQL 在目标规模下执行 EXPLAIN/P95 门禁。
