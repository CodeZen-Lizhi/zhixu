# M9-03 Current State Research

## Sources

- `.trellis/tasks/07-16-product-delivery/prd.md`：正式 Export 需求与 Excel/CSV 排除项。
- `.trellis/tasks/07-16-product-delivery/design.md`：PostgreSQL Job、权限/脱敏、hash、TTL、下载审计和 River 边界。
- `.trellis/tasks/07-16-product-delivery/implement.md`：M9-03 交付范围和 M7-03 前置完成事实。
- `docs/product/PRD.md`：AC-33 同时要求 Markdown、附件和领域元数据。
- 主 agent 与三个只读研究 agent 对现有 Backend、Frontend、OpenAPI、迁移和验收缺口的审计。

## Existing Slice

- `internal/export/domain/model.go` 已定义 Workspace/Collection scope、四类 kind、字段白名单、脱敏、租约、结果和下载计数。
- `internal/export/application/service.go` 已有 Create/Get/List/Execute/Download/Recover；DB 成功而 River 失败会保留 PENDING。
- `internal/export/adapter/collection/snapshot.go` 使用 Collection durable scan，校验 version/query hash/read-model revision/exact count，10,000 项上限不截断。
- `internal/export/adapter/localfs/store.go` 已有受控路径、原子写、hash/size 读取和删除基础。
- `internal/export/adapter/postgres/repository.go` 与 `migrations/00036_export_hardening.sql` 已有 Job scope、租约、TTL、文件和下载统计基础。
- `internal/export/adapter/river/**`、`cmd/api/main.go`、`cmd/worker/main.go`、`internal/app/router.go` 和 `internal/auth/http/handler.go` 已有基础接线。
- 当前局部门禁通过：`go test -race ./internal/export/... ./internal/auth/http ./internal/app`、`node api/openapi/check.mjs`、`git diff --check`。这些结果不覆盖真实 PostgreSQL/River、OpenAPI Export 路由或 Web UI。

## Blocking Gaps

1. `Repository.Create` 在读取已有 idempotency Job 前校验当前 Collection。首次响应丢失、Collection 随后变化时，同 key retry 会错误冲突。
2. `Service.Execute` 先直接写最终 `<job-id>` 路径，再调用 `Complete`。文件写入成功但 Complete 前崩溃后，重试会重新读取可变 Collection，可能与旧文件 hash 冲突。
3. Job 没有持久化 read-model revision/exact count/prepared binding；Application 也未完整比较 Snapshot 的 Collection version。
4. `Complete`/`Fail` 只比较 lease owner，不检查数据库当前时间下租约仍有效；固定租约没有严格更短的执行 deadline。
5. `Get` 才惰性 Expire/Delete；无人读取的成功文件、staging/orphan 没有 startup/periodic sweep。
6. 下载只有 count/time，没有 actor、Export ID、hash、outcome 的 append-only Audit，也没有与统计同事务的证明。
7. `SameRequest` 未绑定规范化 TTL；同 key 的过期策略语义不精确。
8. Workspace list 缺少 `collection_id` 过滤，前端无法稳定恢复当前 Collection 的完整历史；cursor identity 也未绑定 Collection。
9. Export 状态变化未写 `ops.server_event`；前端 SSE resource union 和 Query invalidation 不认识 `export_job`。
10. OpenAPI 只有 Artifact Export，没有 M9 `/api/v1/exports`；Web 没有 Export client、query 或 UI。
11. Service/HTTP/PostgreSQL/River/00036 缺少完整测试，尚无真实 API/Worker/Vite smoke。

## Frontend Findings

- 最小入口是现有 Collection 详情中的 Export Panel，不新增路由或导航。
- 创建只发送当前 Workspace/Collection/version/query hash、格式和安全默认策略；浏览器不重建成员集合，也不传 read-model revision。
- 任务从服务端列表/详情恢复；PENDING/RUNNING 以 2 秒轮询兜底，SSE 只用于加速失效，终态停止。
- 下载必须走 `authFetch` 并解析非 2xx Problem，再生成 Blob download；直链无法正确处理 `410 EXPIRED`。
- decoder 必须拒绝未知/重复字段、非法 UUID/time/hash/enum、重复 fields、跨 binding 和状态字段冲突。

## Scope Decision

- 本任务仅交付 Smart Collection `MARKDOWN` 与 `METADATA_JSON`。
- Artifact Markdown、CSV/XLSX、Evaluation/Audit 内容、附件包和通用字段编排不纳入。
- M9-03 完成后只更新其交付状态；AC-33 的附件部分保持未完成并明确记录。

## Recommended Recovery Protocol

`Claim -> durable snapshot -> render -> create-only staging -> Prepare(staging/final binding) -> atomic promote -> Complete`。

- Prepare 前崩溃：无权威 result binding，staging 由 orphan sweep 清理。
- Prepare 后崩溃：Job 已冻结 revision/count/staging/final path/hash/size，恢复不再读 Collection；到期 cleanup 可定位并删除尚未提升的 staging。
- Promote 后 Complete 前崩溃：验证固定 final 文件后完成。
- 任何 Prepare/Complete/Fail 都要求 owner 匹配、DB-time lease 未过期且 Job 未到期；否则归约 EXPIRED，不能提交成功或失败终态。
