# Health 详情与历史有界化

## Goal

消除 Health Issue 详情随 observation/evidence/decision 历史无限增长的查询、内存和响应体风险，同时保留 `/api/v1` 兼容字段，并提供可验证、稳定、Workspace-scoped 的完整历史分页能力。

本任务依赖 `08-05-architecture-quality-baseline` 完成并保存实施前指标。

## Requirements

### 有界详情

- `GET /api/v1/health/issues/{issue_id}` 继续返回 `issue`、`latest_observation`、`observations` 和 `decisions`。
- `latest_observation` 是非空的当前快照，必须与 `issue.fingerprint` 精确绑定；其 evidence、object versions、detector version 和 severity 必须与当前 Issue 对齐。
- `observations`/`decisions` 只返回各自第一页，默认 25、最大 100；详情固定使用默认 25，不允许调用方把详情重新变成大响应。
- 响应增加每类历史独立的 `next_cursor` 与 `has_more`，明确旧数组已分页。
- 当前快照不从历史页首项推断；同时间戳按 UUID 排序时，`latest_observation` 可以不是 `observations[0]`。

### 历史端点

- 新增：
  - `GET /api/v1/health/issues/{issue_id}/observations?workspace_id=&limit=&cursor=`
  - `GET /api/v1/health/issues/{issue_id}/decisions?workspace_id=&limit=&cursor=`
- 默认 limit 25、最大 100；拒绝重复、未知或非法 query 参数。
- Observation 按 `observed_at DESC, id ASC`，Decision 按 `created_at DESC, id ASC` 稳定 keyset 分页，以匹配 `00025` 现有索引。
- opaque HMAC cursor 必须绑定 schema、Workspace、Issue、history kind、limit 和位置；篡改或跨绑定复用返回稳定 400。
- Observation evidence 只对当前页执行一次 batch hydration，不读取 sentinel 或后续页。

### 前端与契约

- OpenAPI、Go wire、TypeScript runtime decoder 同步升级，继续拒绝未知字段和绑定漂移。
- Health Evidence Dialog 保留当前 Issue evidence 与 decision 操作，并增加 observation/decision 历史视图和显式“加载更多”。
- 前端使用现有 TanStack `useInfiniteQuery` 模式；Query Key 必须绑定 Workspace、Issue 和 history kind，支持取消、加载更多失败重试和关闭详情后的清理。
- 当前 Web 不再把详情数组解释为完整审计历史。

### 兼容与安全

- 兼容策略已由用户确认：旧字段保留但有界，完整历史必须走 cursor。
- 跨 Workspace 与不存在 Issue 继续统一 404，避免资源枚举。
- Problem、认证、权限、2 秒 Health timeout、状态机、Decision 幂等/CAS 和 repair 语义保持不变。
- 不新增或修改 migration；先用 EXPLAIN 证明现有索引足够。

## Acceptance Criteria

- [x] AC-01：详情最多返回 25 条 observation 和 25 条 decision，并正确给出各自 `has_more`/`next_cursor`。
- [x] AC-02：历史端点 limit 范围为 1-100；同时间戳记录无重复、无遗漏地稳定翻页。
- [x] AC-03：cursor 篡改、跨 Workspace、跨 Issue、跨 observation/decision、跨 limit 复用均返回 400。
- [x] AC-04：详情 SQL statement 数与历史总量无关；observation 单页使用一次 history query 和一次 evidence batch query，无 N+1、无 sentinel evidence。
- [x] AC-05：261 条 observation/260 条 decision 的 integration fixture 仍只返回请求页，并在既有 2 秒 application timeout 量级内完成。
- [x] AC-06：非空 `latest_observation` 按当前 Issue fingerprint 精确读取，Issue evidence/object versions 与其对齐，并允许它独立于历史页首项排序。
- [x] AC-07：OpenAPI check、Go application/HTTP/PostgreSQL 定向测试、真实 PostgreSQL integration、前端 API/query/page tests 通过。
- [x] AC-08：Web 能分别浏览和加载更多 observation/decision；加载更多失败可重试，关闭 Dialog 后清理当前 Issue 详情与两类历史缓存。
- [x] AC-09：代表性 fixture 的 EXPLAIN 使用既有 observation/decision/evidence 索引；未新增 migration 或索引。
- [x] AC-10：Workspace 隔离、Decision 幂等/CAS、repair unavailable 和现有 Health 扫描定向测试无回归。

## Out Of Scope

- 修改 Health Issue/Scan/Decision 状态机、检测器、repair proposal 能力或 SSE 事件。
- 新增 migration、物化历史投影、缓存或归档策略。
- 删除旧详情数组或发布新的 API major version。
- 统一全仓 cursor、HTTP decoder 或前端 API decoder。
- 父任务步骤 2-6 的路由恢复、CI 重构、复用和大文件拆分。

## Key Decisions

- 复用现有 `IssueCursorCodec` 密钥与签名机制，增加独立 history schema/document，保持 issue-list cursor wire 不变。
- 同时间戳按 ID 正序，匹配现有 `(issue_id, timestamp DESC, id ASC)` 索引，不新增数据库结构。
- 详情第一页和独立历史端点消费同一 application/repository 分页逻辑，避免第二个历史事实源。
- 当前 observation 通过 Issue 当前 fingerprint 的唯一约束精确定位，不能用时间戳与随机 UUID 推断“当前”。
- 详情一次有界加载两类历史，完整历史只在用户显式“加载更多”时读取。

## Risks

- 严格前端 decoder 要求后端、OpenAPI 和 Web 同步发布；`latest_observation` 从历史上的可选字段收紧为当前非空契约，必须在同一子任务完成。
- Cursor 扩展若复用旧 schema 会破坏列表兼容，因此 history 使用新 schema 且测试旧 cursor 不变。
- 详情仍加载两类第一页，固定 statement 数高于只读当前 Issue；上界和一致性比减少一次查询更重要。
