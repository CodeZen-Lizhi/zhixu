# 快速记录与文档知识画像：实施计划

## Order

1. 定义 Capture/Profile 领域状态、错误、幂等 receipt 和端口测试。
2. 增加 additive migration、PostgreSQL Repository、Outbox/Worker 与 fresh/upgrade/Down guard 测试。
3. 提取 Workspace 单条 Source 注册端口；保持 Scan 行为和 API 兼容。
4. 实现 TEXT/FILE/IMAGE Capture 与 managed Artifact create-only 写入。
5. 实现安全 URL Fetch 和 failure/retry fault tests。
6. 串接 Ingestion/Index durable workflow，覆盖模型/Embedding disabled。
7. 实现 Profile strict schema、Agent run、Revision 与 Evidence binding。
8. 更新 OpenAPI、Capability、Composition Root、API/Worker readiness 和事件失效。
9. 实现严格前端 Decoder、Quick Capture Dialog、应用快捷键、Inbox 和 Profile 详情。
10. 完成跨层、Secret/SSRF、race、浏览器与恢复检查。

## Validation

```bash
go test -race -count=1 -timeout 60s ./internal/capture/... ./internal/ingestion/... ./internal/workspace/... ./internal/retrieval/...
go vet ./internal/capture/... ./internal/ingestion/... ./internal/workspace/... ./internal/retrieval/...
go mod tidy -diff
make openapi-check
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web -- captures Inbox AppShell
npm run build --prefix web
git diff --check
```

增加 disposable PostgreSQL migration/integration、URL SSRF fault smoke、Worker restart、response-loss replay、文件/图片大小与 MIME fuzz/边界测试，以及桌面/移动真实浏览器检查。

## PRD Backfill Gate

实现、验证和 Review 通过后、归档前，将稳定交付行为回填到 `docs/product/PRD.md` 的 `6.3`、`10.2-10.4`、`11.1`、`13.2-13.3`、`14.3-14.4`、`21.2-21.3` 和 `22`；记录实际更新章节或无需更新的理由。不得提前写入未交付行为，不创建 `v2.0` PRD。

## Review

- 使用 `go-review`、`code-review-and-quality` 和 `sql-code-review`。
- 重点审查 SSRF、路径/文件名、资源上限、原始输入不可变、状态机、幂等、Profile 非正式性和浏览器缓存隔离。

## Dependency Output

归档前冻结并记录：Profile v1 Schema/API、Capture/Source 状态、外部变更重新捕获入口和批量 Evidence 读取契约。后续两个子任务只能依赖这些已验证契约。
