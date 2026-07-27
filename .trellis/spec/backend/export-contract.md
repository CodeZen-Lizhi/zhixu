# Smart Collection 异步导出契约

## Scenario: M9-03 可恢复 Export Job

### 1. Scope / Trigger

- 修改 `internal/export/**`、`migrations/00036_export_hardening.sql`、Export Router/Auth/Worker/LocalFS、
  `ops.export_job`、下载 Audit、`export.*` Server Event 或 Collection Export UI/API 时，必须应用本契约。
- 正式范围仅为 Smart Collection 的 `MARKDOWN` 与 `METADATA_JSON`，输出 schema 固定为 `export/v1`。
  `EVALUATION_JSON`、`AUDIT_JSON` 没有正式内容源；附件打包、CSV/XLSX、通用字段映射和公式字段均不在
  M9-03 范围。它们不得因领域枚举或历史代码存在而出现在公开请求、UI 或交付说明中。
- PostgreSQL 是 Job、幂等、租约、冻结范围、prepared result、生命周期、清理、下载统计和 Audit 的唯一
  事实源；River 只运输 `{workspace_id, export_id}`，LocalFS 只保存受控结果文件。

### 2. Public Boundary

```text
POST /api/v1/exports
GET  /api/v1/exports/{export_id}?workspace_id=...
GET  /api/v1/workspaces/{workspace_id}/exports?collection_id=...&limit=...&cursor=...
GET  /api/v1/exports/{export_id}/download?workspace_id=...
```

- 创建请求必须有唯一 `Idempotency-Key`，并绑定 Workspace、Collection ID/version、query hash、kind、
  `export/v1`、规范化字段白名单、脱敏策略、敏感字段意图、调用主体/能力与 TTL。
- 首次创建返回 `202` 和 `Location`；相同 key 的完全相同请求返回原 Job（`200`），不同规范请求返回
  `409 EXPORT_IDEMPOTENCY_CONFLICT`。`dispatch_pending=true` 只表示本次 River 投递尚未确认，不能被客户端
  渲染为业务失败或完成。
- List 使用 `created_at DESC,id DESC` 的有界 opaque cursor；cursor 与 Workspace、Collection 和 limit 绑定。
  Get/List/Download 均按当前 Workspace 授权，不返回服务器文件路径、staging locator、租约或 request hash。

### 3. Durable Contracts

- Create 必须先读取同 `(workspace_id,idempotency_key)` 的既有 Job，再验证当前 Collection；因此首次响应丢失后，
  即使 Collection 已变化、归档或 Job 已过期，完全相同的请求仍只返回原 Job。过期 Job 不复用 key 创建新任务。
- 首次执行在同一 Collection version/query hash 的 durable scan 中冻结 `read_model_revision` 与 `exact_count`。
  单次结果最多 10,000 项，超过上限、Workspace/Collection/version/query/revision/count 漂移都必须 fail closed，
  禁止截断或退化为 Workspace 全量。
- Worker 的可恢复顺序固定为 `Claim -> durable snapshot -> render -> create-only staging -> Prepare -> atomic promote -> Complete`。
  `Prepare` 持久化 staging/final 相对路径、hash、size、revision 和 count；prepared 后任何恢复都只能验证该固定
  binding，不能重新读取 Collection 或重新 render。
- Claim、Prepare、Complete、Fail 都以 PostgreSQL 当前时间同时校验 Job version、lease owner、lease 未过期和
  `expires_at`。旧 lease owner 无权提交；Prepare 后到期必须归约为 `EXPIRED`，不能写入成功或失败终态。
- `PENDING -> RUNNING -> SUCCEEDED|FAILED|EXPIRED` 是当前写入生命周期；`CANCELLED` 仅为历史兼容读模型，
  M9-03 不提供取消入口。Get、List 与后台 sweep 都归约到期 Job；任务和 Audit 历史保留，TTL 只回收物理文件。
- 默认 `MASKED`，前端首版只发送安全字段且不暴露敏感开关。`FULL + include_sensitive` 只能由已认证
  Session/API Token 主体以 `READ_LOCAL` 发起；Secret、绝对路径和危险公式前缀不得进入 render input、文件、
  Problem、日志或事件摘要。
- 下载前必须重新验证受控路径、symlink 边界、SHA-256 和 size；成功准备返回时，在同一 PostgreSQL 事务内增加
  下载统计并追加 `export.download` append-only Audit，记录 actor、Export ID、hash、size 和
  `server_outcome=prepared_for_return`。这不声称客户端已完整接收文件。
- 过期清理幂等删除 prepared staging 与 final 路径；删除失败保留 cleanup 状态、次数和受限错误供后续 sweep 重试。
  orphan sweep 只扫描 `.knowledge/exports/.staging` 的严格命名空间，且绝不删除已有 prepared binding 的文件。

### 4. HTTP / Event Error Matrix

| 条件 | 必须结果 |
|---|---|
| 非法 JSON、重复/未知字段、UUID/hash/enum/TTL/cursor/Idempotency-Key 非法 | `400 EXPORT_REQUEST_INVALID` 或 `INVALID_JSON`，不创建 Job |
| 跨 Workspace、Job 或当前 Active Collection 不存在 | `404 EXPORT_NOT_FOUND`，不帮助枚举资源 |
| 同 key 不同 canonical request，或当前 Collection binding 漂移 | `409 EXPORT_IDEMPOTENCY_CONFLICT` |
| 下载未完成 | `409 EXPORT_RESULT_NOT_READY` |
| 下载或读取时已到期 | `410 EXPORT_EXPIRED`，并保留 cleanup 事实 |
| 授权、能力或敏感策略不满足 | `403 EXPORT_PERMISSION_DENIED` |
| 文件/hash/size/持久绑定无法证明一致 | `500 EXPORT_RESULT_INCONSISTENT`，不返回部分结果或增加下载统计 |
| PostgreSQL、River、文件或 Audit 依赖不可用 | `503 EXPORT_DEPENDENCY_UNAVAILABLE`，保留可恢复 Job |

- `export.created|claimed|prepared|completed|failed|expired|cleanup_*` 是 Server Event 摘要；
  `resource_ref=export_job:<id>`、`resource_version=job.version`。SSE 仅定向失效 Export Query，客户端必须回查
  Job/List 事实，不能从事件 payload 推导终态。
- 成功下载必须使用对应 kind 的固定 Content-Type、受控 attachment 文件名、Content-Length、
  `Cache-Control: private, no-store` 和 `X-Content-Type-Options: nosniff`。

### 5. Frontend Recovery Boundary

- `web/src/api/exports.ts` 是唯一 wire owner。它从 `unknown` 严格解码 Job/Page/Problem/下载响应，校验
  Workspace/Collection/version/query hash、UUID、RFC3339、hash、枚举、字段去重与状态字段组合；任一漂移
  都拒绝整个响应。
- Query key 至少绑定 `collection-exports + workspaceId + collectionId + cursor`；Workspace 切换取消并清除旧
  Export cache。cursor 只在内存 Query state 中保存，不写 URL 或 Browser Storage。
- `PENDING`、`RUNNING` 以 2 秒有界轮询恢复，`export.*` 事件立即失效同一 Workspace 的 Export Query；终态停止
  轮询。创建响应丢失重试复用同一 variables 与 Idempotency-Key；只有用户显式新建，或 `FAILED/EXPIRED` 后重试，
  才生成新 key。
- 下载必须经 `authFetch` 获取 Blob 并校验响应头、长度和绑定；不得用直链/新标签页绕过 Problem、401 或 410 处理。

### 6. Tests Required

- Domain/Application：canonical request/TTL、Job 状态字段、字段白名单、snapshot drift、10,000 上限、prepared replay、
  lease/TTL loss、hash/size、cleanup 和下载 actor。
- PostgreSQL：同 key 并发/response-loss、Collection 变化后的 replay、跨 Workspace、Collection-bound cursor、
  DB-time Claim/Prepare/Complete/Fail、download+Audit 原子性、append-only、cleanup retry 和 guarded Down。
- HTTP/OpenAPI：严格 body/header/query、`202/200/409/410/503`、下载安全 header、Router/OpenAPI 映射和
  `MARKDOWN|METADATA_JSON` 公开枚举。
- Frontend/Browser：strict decoder、同 key 重试、2 秒轮询停止、SSE recovery、Workspace cache 清理、全部状态、
  Blob 下载、桌面/390x844 键盘、无横向溢出和无 console warning/error。

```bash
go test -race -count=1 -timeout 60s ./internal/export/... ./internal/events/... ./internal/audit/... ./internal/auth/http ./internal/app ./cmd/api ./cmd/worker
ZHIXU_TEST_DATABASE_URL="$ZHIXU_TEST_DATABASE_URL" go test -race -tags=integration -count=3 -p 1 -timeout 60s ./internal/export/adapter/postgres
make openapi-check
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
ZHIXU_TEST_DATABASE_URL="$ZHIXU_TEST_DATABASE_URL" bash deploy/export-browser-smoke.sh
```

### 7. Wrong vs Correct

```text
Wrong: 文件写到最终路径后再次读取可变 Collection，或 River Job 被当作 Export 终态。
Correct: PostgreSQL prepared binding 冻结 revision/count/path/hash/size；恢复只验证同一 binding，River 只负责投递。

Wrong: 过期后删除 Job/Audit，或下载成功只增加计数而不记录 actor。
Correct: TTL 只删除受控物理文件；历史 Job/Audit 保留，统计和 export.download 在同一事务追加。

Wrong: 宣称 AC-33 已关闭，因为能导出 Collection Markdown。
Correct: M9-03 仅交付 Collection MARKDOWN/METADATA_JSON；附件、EVALUATION_JSON、AUDIT_JSON 继续 deferred，AC-33 仅部分完成。
```
