# M9-03 Smart Collection 异步导出闭环实施计划

## Ordered Work

1. 固化 Export Domain 和 `00036` schema：canonical request hash/TTL、Job version、frozen snapshot、prepared staging/final binding、cleanup facts、状态约束、索引和 guarded Down；先补领域与 migration integration tests。
2. 重构 PostgreSQL Repository：idempotency-first Create、Collection-filtered cursor List、DB-time lease Claim/Prepare/Complete/Fail、Expire/Cleanup candidates；在同事务追加 `export.*` Server Event。
3. 实现可恢复 FileStore/Service：staging create-only、Prepare 后原子提升、prepared replay、严格 snapshot binding、执行 deadline、DB-time lease+TTL CAS、hash/size 校验、bounded recovery 和 expire/orphan sweep；补 prepared-but-unpromoted 到期、崩溃窗口与双 Worker 测试。
4. 接入 append-only 下载 Audit：从 HTTP principal 传递 actor，文件验证后在下载统计事务内 `AppendTx`；补并发、跨 Workspace、权限、失败与 Secret/path/formula canary 测试。
5. 补 River Worker、API/Worker Composition、startup/periodic recovery+cleanup、readiness 和故障恢复测试；确保 River Args 仍只携带 Workspace/Export ID。
6. 完成 HTTP/Router/Auth/OpenAPI：创建、Collection-filtered 列表、详情、下载、安全 headers、稳定 Problem、严格 kind/state schema 和 OpenAPI checker 映射。
7. 新增前端 Export API/decoder/query keys/hooks 与 `CollectionExportPanel`；接入 SSE `export_job` 失效、Workspace cache 清理、2 秒轮询、response-loss 同 key、Blob 下载和全状态 UI。
8. 新增 `deploy/export-browser-smoke.sh` 与 Playwright：真实 PostgreSQL/API/Worker/River/Vite 完成成功、刷新恢复、失败、过期、重新创建、下载内容/hash、安全 canary、桌面/移动、键盘、console/overflow。
9. 执行全量质量门禁、Go/通用/SQL 主审和独立审查；修复后由同一 reviewer 复验。同步 Export 专项 spec、产品/架构/运行手册，明确 AC-33 附件仍未关闭。
10. 使用临时 index 从混合工作区精确暂存 M9 文件/语义 hunk，验证候选提交不混入 Review/Capacity/Trellis 工具链等未完成改动，再提交、push、归档任务并更新父任务。

## Test Matrix

| Layer | Required evidence |
| --- | --- |
| Domain/Application | request hash/TTL、状态字段组合、snapshot drift、10k 上限、prepared replay、lease/TTL loss、prepared staging/final cleanup、hash/size、下载 actor |
| PostgreSQL | 同 key 并发/response loss、Collection 变化后 replay、跨 Workspace、cursor identity、DB-time lease+TTL、Prepare/Complete/Fail CAS、download+Audit 原子性、append-only、cleanup retry |
| Migration | 空库 Up、重复 Up、约束/FK/index、历史非空 Down `55000`，三轮真实 PostgreSQL |
| River/Composition | DB success + dispatch failure、重复投递、Worker restart、lease reclaim、startup/periodic sweep、API/Worker wiring |
| HTTP/OpenAPI | strict body/header/query、Session/API Token/CSRF/Origin、200 replay/202 create、409/410/503、download headers、Router/OpenAPI 完整映射 |
| Frontend | strict decoder、跨 binding、unknown/duplicate/state conflict、query key/cursor、same-key retry、poll stop、SSE recovery、Workspace clear、历史 CANCELLED fixture、全部状态与下载 |
| Browser | 真实服务成功/失败/过期/刷新恢复，Markdown/JSON 内容与 hash，Secret/path/formula canary，桌面/390x844、键盘、焦点、overflow、console |

## Validation Commands

```bash
go test -race -count=1 -timeout 60s ./internal/export/... ./internal/events/... ./internal/audit/... ./internal/auth/http ./internal/app ./cmd/api ./cmd/worker
ZHIXU_TEST_DATABASE_URL="$ZHIXU_TEST_DATABASE_URL" go test -race -tags=integration -count=3 -p 1 -timeout 60s ./internal/export/adapter/postgres
ZHIXU_TEST_DATABASE_URL="$ZHIXU_TEST_DATABASE_URL" go test -race -tags=integration -count=3 -p 1 -timeout 60s -run '^(TestExport|TestM9)' ./internal/platform/migration
make openapi-check
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
go test -race -count=1 -timeout 60s ./...
go vet ./...
go mod tidy -diff
git diff --check
ZHIXU_TEST_DATABASE_URL="$ZHIXU_TEST_DATABASE_URL" bash deploy/export-browser-smoke.sh
```

## Risk Gates

- 不在读取当前 Collection 后才查幂等 Job；exact replay 必须先于当前态校验。
- 不用固定最终路径直接承接未绑定结果；prepared 前文件只能位于可清理 staging。
- 不在 prepared 后重新读取 Collection 或重新 Render；恢复只验证持久化 path/hash/size。
- 不让调用方时间决定租约有效性，也不让 Worker deadline 等于或超过 lease。
- 不允许 Prepare 后到期的 Job Complete/Fail；必须由同一 DB-time CAS 归约 EXPIRED，并清理 persisted staging/final binding。
- 不把惰性 Get 删除包装成后台 TTL；必须有周期 sweep、删除失败事实和 orphan 测试。
- 不以 download count 代替 Audit，也不声称浏览器已完整接收响应。
- 不让 UI 自行重建 Collection 成员、持久化 cursor、生成 response-loss 新 key 或显示虚构进度。
- 不修改/暂存当前混合工作区内与 M9 无关的 Review、Capacity、Observability、Trellis 基础设施改动。

## Rollback Points

- Schema/Repository gate 未通过：停在迁移和单元测试，不接 API/Worker。
- prepared/lease fault tests 未通过：不启用真实 River Worker 和下载。
- HTTP/OpenAPI/前端 decoder 漂移：保持 UI 入口不可见，不宣称能力交付。
- 浏览器或安全 canary 未通过：不提交；保留 Job/Audit，使用 forward fix，不执行破坏性 Down。

## Completion Evidence

> 下方前四项描述的是 storage、cleanup 和 supported-kind 复审修复前的候选基线；
> 它们不能作为这些修复已通过真实 PostgreSQL 和浏览器门禁的证据。

### 2026-07-27 Candidate Verification

- 创建与重放、冻结快照、prepared-result 恢复、DB-time lease/TTL CAS、Workspace/权限/脱敏、过期/cleanup、下载统计与
  append-only Audit 由 `internal/export` unit、真实 PostgreSQL integration 与 `deploy/export-browser-smoke.sh` 覆盖；
  HTTP smoke 另断言 Collection 变化后的原 body/key 精确重放返回 `200/replayed=true`，不同 TTL 返回稳定 `409`。
- `go test -race -count=1 -timeout 60s ./...`、`go vet ./...`、`go mod tidy -diff`、
  `node api/openapi/check.mjs` 通过；Export 定向 race 及新增 River `Client.Insert` 委托/错误分类回归通过。
- 真实 PostgreSQL：`go test -race -tags=integration -count=3 -p 1 -timeout 60s ./internal/export/adapter/postgres` 与
  `-run '^(TestExport|TestM9)' ./internal/platform/migration` 均通过；包含 orphan workspace cursor 的 `DISTINCT/ORDER BY`
  PostgreSQL 回归。
- 前端：lint、typecheck、build 通过；Vitest `59 files / 684 tests` 通过；真实 API/Worker/River/Vite 浏览器 smoke
  在桌面与 `390x844` 覆盖创建、刷新恢复、成功下载、失败/过期新建、SSE、键盘、overflow 与 console。
- 主审、SQL/Go/通用检查及独立只读复审均未发现当前范围 P0-P3；独立 PostgreSQL 复验确认 orphan sweep cursor SQL 有效。
- 交付范围仅为 Smart Collection `MARKDOWN|METADATA_JSON`。附件、`EVALUATION_JSON`、`AUDIT_JSON`、CSV/XLSX、通用字段
  映射仍 deferred，AC-33 维持部分完成。提交 hash、push 分支与归档记录在最终提交后补充。

### 2026-07-27 Post-review Regression Evidence

- 已修复：LocalFS final 提升改为 create-only 原子 link；prepared cleanup 在 unlink
  前复核 hash/size；领域、OpenAPI、前端和 migration 的正式 kind 收敛为
  `MARKDOWN|METADATA_JSON`。相应 LocalFS、application、migration shape 回归测试已补充。
- 当前候选副本通过：`go test -race -count=1 -timeout 60s ./...`、`go vet ./...`、
  `go mod tidy -diff`、`make openapi-check`、`git diff --check`，以及
  `npm run lint/typecheck/test/build --prefix web`（59 files / 684 tests）。
- 修复后真实 PostgreSQL 三轮导出集成与 `TestExport|TestM9` 迁移集成都已通过；
  `deploy/export-browser-smoke.sh` 也已通过真实 API、Worker、River、Vite 的桌面与 `390x844` 验收。
