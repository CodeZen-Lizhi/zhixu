# 快速记录与文档知识画像：交付结果

## Delivered Behavior

- AppShell 在已连接业务页提供 Quick Capture 按钮与 `Ctrl|Meta + Shift + K`，支持文字、链接、文件和图片四类输入；关闭或保存后恢复原焦点。
- Capture 先保存不可变输入、Source/Content Artifact/Source Version（URL 在抓取成功后追加 Version）与 durable outbox，再由 Worker 执行安全抓取、解析、基础索引和候选画像。
- Fetch、Ingestion、Index 与 Profile 独立恢复状态；Embedding、OCR/视觉或 Profile 能力不可用时保留已完成事实并显示明确降级。
- `/inbox` 展示 Quick Capture 与 Workspace Scan 两类真实来源；`/captures/:captureId` 展示不可变来源、各处理阶段和带 Evidence 的候选画像。
- Profile v1 Revision/Evidence append-only；重试、失败和 stale 不删除上一版；候选不会写入正式 Topic、Claim、Relation 或 Proposal。
- OpenAPI、API/Worker composition、认证能力和 System Status 均已纳入 Capture；前端 System Status decoder 对新增 `capture` 必填字段继续严格校验。

## Product PRD Backfill

已将稳定交付行为回填到唯一总 PRD `docs/product/PRD.md`：

- `6.3` Inbox、`10.2.10` Quick Capture、`10.3.8` 解析边界、`10.4.10` 自动索引与画像降级。
- `11.1` 摄取前半段、`13.2` Capture、`13.3` Document Knowledge Profile。
- `14.3` Capture/Profile API、`14.4` Search 边界。
- `21.2` Inbox/详情、`21.3` Document 上下文、`22` 的 `AC-37`。

## Validation Evidence

以下门禁均通过：

- Go：受影响包测试与 race、`go test -count=1 -timeout 60s ./...`、`go vet ./...`、`go mod tidy -diff`。
- PostgreSQL：`00068_capture_profile.sql` 空库 Up、Down-Up、guarded Down，以及 Capture/Profile Repository、response-loss replay 和旧 Revision 保留集成测试。
- HTTP/OpenAPI：Capture strict JSON/multipart、认证能力、API/Worker composition 与 `node api/openapi/check.mjs`。
- Web：ESLint、TypeScript、production build、全量 92 files / 941 tests；System Status 修复后相关 24 tests 复验通过。
- Repository：`git diff --check` 通过；后端/前端新规范无 `TBD` 或待填占位符。

真实浏览器烟测使用 PostgreSQL + API + Worker + Vite：

- TEXT Capture `43930237-85e3-477d-b2aa-fd5da0170cc3`，Source Version `8f6d3e6c-578f-42be-9910-6905e6a6d005`。
- POST 返回 `201`；outbox claim 1，River 完成 1；Inbox/详情恢复为 `READY_DEGRADED`，解析与基础索引完成，Profile 明确为 capability unavailable。
- `1440x900` 与 `390x844` 均无 document 横向溢出；快捷键、四类 tab、保存/显式关闭焦点恢复均通过。
- Console 为 0 error / 0 warning；网络记录为预期的 React abort 后成功 GET、Capture POST `201`、Profile GET `200`。
- 截图保存在 `output/playwright/capture-smoke-1785660784400/`：`dashboard-desktop.png`、`quick-capture-dialog-desktop.png`、`capture-inbox-desktop.png`、`capture-detail-mobile.png`、`quick-capture-dialog-mobile.png`。
- 浏览器闭环实际创建路径为 TEXT；URL/FILE/IMAGE 的安全、解析、保存、降级与幂等路径由自动化测试覆盖，未声称三者已做浏览器逐项创建。

## Dependency Output

### Profile v1 Schema And API

- Schema：`document-knowledge-profile/v1`。
- 字段：`summary`；`topics {label, aliases, source_span_ids}`；`terms {label, aliases, source_span_ids}`；`knowledge_points {text, source_span_ids}`；`examples {text, source_span_ids}`。
- 读取：`GET /api/v1/workspaces/{workspace_id}/source-versions/{source_version_id}/knowledge-profile`。
- 重试：同路径 `POST .../retry`，绑定 expected version 与 idempotency identity。
- 状态：`PENDING|RUNNING|READY|FAILED|CAPABILITY_UNAVAILABLE|STALE`；重试、失败或 stale 继续返回旧 current Revision/Evidence。

### Capture And Source States

- kind：`TEXT|URL|FILE|IMAGE`。
- 聚合状态：`RECEIVED|SOURCE_SAVED|FETCHING|PROCESSING|READY|READY_DEGRADED|FETCH_FAILED|PROCESSING_FAILED`；Fetch/Ingestion/Index/Profile 另有独立阶段状态。
- URL 在任何网络请求前创建 Source；其余 kind 在命令完成时已有 Source Version。
- Capture/Profile 均不创建正式知识或 Proposal；Quick Capture 结果身份始终是 Inbox Source，不是 Document Draft 或 Artifact。

### External Change Recapture Contract

- 共享入口为 `retrievalapplication.SourceRefresher.Refresh(SourceRefreshRequest)`。
- 输入冻结 `RequestID`、`WorkspaceID`、`SourceID`、`SourceVersionID`、`AttemptNumber`；输出 `IngestionAttemptID`、`ParseProjectionID`、`IndexVersionID`、`ActivationID`、`VectorDegraded`。
- 本任务未提供公开的外部变更 HTTP recapture endpoint。后续 Git Sync 必须先创建新的不可变 Source Version，再调用共享 refresher；禁止修改已有 Source Version。

### Bulk Evidence Read Contract

- Profile loader：`ProfileGenerationRepository.LoadSource(ProfileLookup, parseProjectionID, indexVersionID)`。
- 返回 `ProfileSourceSnapshot`，最多 500 chunks，每 chunk 最多 128 KiB。
- Chunk 字节上限在 PostgreSQL 投影阶段执行，超限正文不会先被完整读取到 Worker 内存，也不会进入 Provider 输入。
- Evidence 必须与同一 Workspace/Source Version 绑定，并严格引用已验证 Source Span；正文来源为 `raw_bytes` 或 `derived_text`。
- Organizing 可以依赖 Profile view/evidence API 和共享 loader 契约，不得跨边界直接查询 Profile 内部表。

## Review And Delivery Notes

- Go、SQL、严格 decoder、跨 Workspace 绑定、幂等 receipt/outbox、Profile append-only 与浏览器恢复均已按任务风险复核。
- 浏览器发现并修复了 System Status `capture` 必填字段未进入前端 strict decoder 的真实集成缺陷。
- 浏览器还发现新库首次启动时 River queue 尚未创建却先执行 Resume；现已改为 idle/failed 时 lifecycle Start 创建 queue 后再 Resume、最后置 readiness，失败时完整清理。managed rollout 非终态则在 Start 前强制 Pause 并验证持久队列存在，缺失时 fail closed，避免创建未暂停队列后抢占任务。
- 最终审查把 Profile 单 Chunk 128 KiB 上限前移到 SQL 投影，超限值返回稳定 context-invalid，避免仓储先物化超限正文再由生成器拒绝。
- 最终审查补齐 managed content 的崩溃恢复边界：首次目录链逐级同步父目录、stage/final 按冻结 size 有界校验，
  并由幂等命令稳定派生 materialized stage locator；未知提交实际未落库后的重试会复用并清理同一暂存。
- 最终审查补齐 Capture refresh checkpoint 的 Index 状态证据；`building` Index 不能再被投影成 Capture `index_status=READY`。
- 最终审查将 URL 暂存 identity 绑定 proposed Source Version，避免重复 delivery 共享可清理 stage；同时补齐 staging 目录层级和
  复用 stage 的 fsync，以及 staging 目录/文件符号链接 fail-closed 回归测试。
- 最终全量 Web 回归另发现 9 个 System Status 组件测试仍使用不含 `capture` 的旧 fixture；已补齐真实必填字段，未放宽 decoder，全量 941 tests 通过。
- 本任务归档时按项目规则未创建 Git commit；用户已于 2026-08-05 随后明确授权将本轮整批能力提交并推送。
