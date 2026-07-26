# M8-01 Artifact 产物闭环实施计划

## Ordered Work

1. 冻结 Artifact 迁移与 Domain-to-DB codec，补 artifact/revision/command/export 的 schema、约束与 migration integration tests。
2. 新增 Application ports 和 PostgreSQL repository：create/get/list、CAS transition、receipt replay、证据批量验证与导出结果写入。
3. 将当前纯 Service 接到持久化 command/query service；补 outline/section/draft/export/publish 的单元与真实 PostgreSQL 事务测试。
4. 扩展 Change Control typed proposal，使 `PUBLISH_ARTIFACT` 绑定不可变 Artifact Revision；保持 Proposal/Approval/Safe Writeback 单一路径。
5. 新增严格 Artifact HTTP handler、路由、Capability、Composition Root、OpenAPI/contract checker；补 HTTP/router/composition 测试。
6. 新增 Artifact API decoder、query hooks 与 `/artifacts/:id` 工作台；覆盖空、加载、冲突、GAP、导出与 Proposal 状态。
7. 执行真实 PostgreSQL/API/Worker/浏览器业务烟测，质量检查、主审、独立复审、规范同步、提交和归档。

## Validation Plan

```bash
go test -race -count=1 ./internal/artifact/...
ZHIXU_TEST_DATABASE_URL="$ZHIXU_TEST_DATABASE_URL" go test -race -tags=integration -count=3 -p 1 -timeout 60s ./internal/artifact/adapter/postgres
ZHIXU_TEST_DATABASE_URL="$ZHIXU_TEST_DATABASE_URL" go test -race -tags=integration -count=3 -p 1 -timeout 60s -run '^TestArtifact' ./internal/platform/migration
go test -count=1 ./cmd/api ./internal/app ./internal/auth/http
make openapi-check
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
go test -race -count=1 -timeout 60s ./...
go vet ./...
git diff --check
deploy/artifact-browser-smoke.sh
```

## Risk Gates

- 不把现有 `ops.export_job` 误用于 Artifact；它绑定 Collection scope。
- 不让客户端输入 `verified`、excerpt/hash 或正式知识资格成为事实。
- 不在 HTTP Handler 开启跨模块事务，也不让 Publish 绕过 Change Control。
- 不修改现有未提交的 M8-02/M9/M10 文件；最终仅暂存 M8-01 归属文件。

## Completion Evidence

2026-07-26 已逐项核对 Acceptance Criteria：

1. `TestArtifactPublicHTTPPostgreSQLIntegration` 覆盖创建、非空大纲、审批及审批前章节拒绝。
2. 同一 HTTP PostgreSQL 集成覆盖两章 `COVERED + GAP`、跨 Workspace、伪造和不具正式资格 Citation 拒绝；真实 Worker 与浏览器 smoke 覆盖受控生成。
3. `TestRepositoryPostgreSQLWorkspaceCASReceiptsAndImmutableBindings` 与领域测试覆盖不可变 Revision、CAS 冲突和 receipt 精确重放。
4. `TestArtifactGenerationSucceedsThroughPublicHTTPAndRiverWorker` 断言默认 RAG 隔离；HTTP 集成和 LocalFS 测试覆盖 Document 隔离、导出回读、权限与 SHA-256。
5. Artifact 并发 PostgreSQL 测试断言 Publish 只产生一个 Proposal/binding，response-loss 精确恢复；HTTP 集成断言 Proposal 冻结当前 Revision 且 Document 数不变。
6. OpenAPI checker、HTTP/Auth/Router/Composition、前端严格 decoder、迁移三轮集成和真实浏览器 smoke 全部通过。
7. `deploy/artifact-browser-smoke.sh` 在一次性 PostgreSQL 和 Git workspace 上启动真实 API/Worker/Vite，完成桌面与移动端完整业务流。

最终质量门禁：

- `go test -race -count=1 -timeout 60s ./...`、`go vet ./...`、`go mod tidy -diff`：通过。
- `go test -race -count=1 -timeout 60s ./internal/artifact/...`：通过。
- Artifact PostgreSQL 25 条 integration tests 按三组执行，每组保持 `-race -count=3 -p 1 -timeout 60s`：12 条 33.411s、7 条 23.309s、6 条 17.391s，全部通过。整包三轮仅因每例独立建库迁移在 61.7s 触发包级超时，拆组未降低单例轮次或覆盖。
- `go test -race -tags=integration -count=3 -p 1 -timeout 60s -run '^TestArtifact' ./internal/platform/migration`：通过，31.501s。
- `make openapi-check`：通过。
- `npm run lint --prefix web`、`npm run typecheck --prefix web`、`npm run test --prefix web`、`npm run build --prefix web`：通过，Vitest 56 files / 662 tests。
- `ZHIXU_TEST_DATABASE_URL="$ZHIXU_TEST_DATABASE_URL" bash deploy/artifact-browser-smoke.sh`：通过；覆盖一次性数据库、Evidence 资格、真实 API/Worker/Vite、桌面/移动、console 与 overflow 检查。
- 主 agent 使用 Go、通用和 SQL 专项 review；独立只读 reviewer 第 1 轮未发现代码问题，主审修复 1 项 Artifact spec 漂移后，同一 reviewer 复验通过且无剩余分歧。
- 临时 index 共 101 个 M8-01 文件，`git diff --cached --check` 与越界路径/符号扫描通过；未包含 M8-02 Review、M9 通用 Export、M10 Auth/Capacity/Scheduler/Audit 改动，主工作区真实 index 保持为空。
