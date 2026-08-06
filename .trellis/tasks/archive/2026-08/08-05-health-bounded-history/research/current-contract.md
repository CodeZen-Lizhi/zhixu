# Health 当前契约与复用点

## 已确认事实

- `internal/health/application/read.go` 已有默认 25、最大 100 的 Issue list limit 和 HMAC `IssueCursorCodec`。
- 现有 list cursor 只绑定 Workspace/过滤器/limit，history 必须使用新 schema 并增加 Issue/kind 绑定。
- `internal/health/adapter/postgres/read_repository.go` 当前详情依次读取 Issue、全部 observations/evidence、全部 decisions。
- `migrations/00025_smart_collection_health.sql` 已有：
  - `(issue_id, observed_at DESC, id ASC)` observation index。
  - `(issue_id, created_at DESC, id ASC)` decision index。
  - `(observation_id, evidence_no, id)` evidence index。
- 因此 history 采用 timestamp DESC、ID ASC，不需要 migration。
- PostgreSQL `ORDER BY id` 会优先解析同名输出列；当 `SELECT id::text` 时，未限定排序会退化为文本 Sort。生产查询必须使用表别名限定原生列。
- 混合方向 keyset 的 OR 条件本身只作为 Filter；增加语义等价的 `timestamp <= cursor` 后，深页时间边界才能进入现有索引的 `Index Cond`。
- 当前 observation 不能按 `(timestamp, UUID)` 推断。Issue 当前 fingerprint 与 `(issue_id,fingerprint)` 唯一约束才是当前快照的事实源。
- `internal/health/http/handler.go` 已有严格 query allowlist、UUID、limit、cursor 和 2 秒 timeout，可复用。
- `/health/issues/{issue_id}/decisions` 已有 POST；Chi 可在同一路径增加 GET。
- `web/src/api/health.ts` 当前严格要求 detail 的四个字段，并把 observation/decision 数组限制为 256。
- `web/src/features/health/HealthPage.tsx` 当前只展示 Issue 当前 evidence，没有展示历史数组。
- 前端已有 Radix Tabs 和多个 `useInfiniteQuery` 分页/失败恢复范例。

## 约束

- 兼容字段保留但有界的语义已经用户确认。
- 不新增索引；先由真实 PostgreSQL EXPLAIN 证明现有索引计划。
- 当前 Decision POST、CAS、幂等键和 repair unavailable 行为不改变。
- 完整历史只通过显式分页读取，不能保留隐藏的全量 repository helper。
