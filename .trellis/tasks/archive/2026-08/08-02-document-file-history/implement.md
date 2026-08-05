# 文档文件历史与受控恢复：实施计划

## Order

1. 定义 History managed/external/current union、cursor、compare 与 restore preview 契约。
2. 实现受限 Git HistoryReader 及临时仓库单测：普通、external、dirty、detached、SHA-1/SHA-256、超限。
3. 实现 PostgreSQL 批量 mapping query，避免逐 Commit N+1。
4. 实现 History Application/HTTP、HMAC cursor、OpenAPI 和 capability。
5. 新增 `RESTORE_DOCUMENT` typed Proposal、preview/hash/version 绑定和 Safe Writeback 集成。
6. 覆盖 response-loss、stale、dirty、外部 Commit 与恢复后 reindex。
7. 实现 strict 前端 API、Document History/Compare/Restore UI。
8. 完成桌面/移动、键盘、长 Diff 和错误状态验证。

## Validation

```bash
go test -race -count=1 -timeout 60s ./internal/documenthistory/... ./internal/changecontrol/...
go test -race -count=1 -timeout 120s ./internal/platform/gitcli/...
go vet ./internal/documenthistory/... ./internal/platform/gitcli/... ./internal/changecontrol/...
go mod tidy -diff
make openapi-check
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web -- document-history MonacoDiffViewer proposals
npm run build --prefix web
git diff --check
```

增加 disposable PostgreSQL mapping/restore integration、真实临时 Git 仓库和 fault smoke；验证没有 reset/checkout/history rewrite 命令路径。

## PRD Backfill Gate

实现、验证和 Review 通过后、归档前，将稳定交付行为回填到 `docs/product/PRD.md` 的 `10.5`、`10.9`、`11.8`、`13.3`、`15.5`、`21.3` 和 `22`；记录实际更新章节或无需更新的理由。不得提前写入未交付行为，不创建 `v2.0` PRD。

## Review

- 使用 `go-review`、`code-review-and-quality`、`sql-code-review`。
- 重点检查 pathspec、Commit/ref 验证、输出上限、N+1、cursor stale、TOCTOU、外部 Commit 语义和恢复写回唯一性。

## Rollback

关闭 History capability 和新恢复命令即可回退 UI/API；不删除 Commit、Revision、Proposal 或映射。已创建恢复 Proposal 按现有生命周期保留。

## Delivered

- 新增 current branch/current canonical path 的有界 Git HistoryReader、HMAC cursor、managed/external/current-change 投影、版本比较和完整输出上限。
- 新增 PostgreSQL 批量 Commit mapping、`restore_document` typed Proposal、Safe Writeback/Authoring restore publication closure 与 guarded migration `00075`。
- 新增 strict HTTP/OpenAPI/Auth/Composition 和 `/authoring/documents/:documentId/history` 工作台、SSE recovery、Monaco Diff 与恢复 Proposal 交互。
- Review 修复了 Git graft/root/path TOCTOU、partial relation UI、SSE cursor 提交，以及 `FILE_PREPARED` 崩溃恢复绕过 Authoring owner preflight 的问题。

## Validation Result

- Go：受影响 Document History/Git/Change Control/Authoring/API/Worker tests、race、vet 和 `go mod tidy -diff` 通过。
- PostgreSQL：`00075` fresh/repeat/guarded Down、Authoring 函数精确恢复、mapping、restore Proposal/publication 并发重放和新增 partial index 通过。
- Contract/Web：`make openapi-check`、ESLint、TypeScript、Document History/Event Store/Monaco Vitest、production build 和 `git diff --check` 通过。
- 安全静态检查未发现 reset、checkout、rebase、push、fetch 或 pull 的 Document History 命令路径；SQL 使用参数化查询，mapping 为批量读取。
- 未启动本地服务或执行真实桌面/`390x844` 浏览器 smoke；按项目规则该操作需要用户明确要求。响应式 CSS、组件状态和 production build 已覆盖，视觉实机仍是剩余验证盲区。

## Review Result

- `go-review`：Required 项已修复并复验；`FILE_PREPARED` 重试现在会重新校验 Proposal/Execution、owner 和 lease，owner drift 转 `NEEDS_REVISION` 且不调用 Commit CAS。
- `code-review-and-quality`：partial managed relationships 独立展示；Workspace/head/path/version/cursor、SSE recovery 与 Monaco 生命周期未发现剩余 Required 问题。
- `sql-code-review`：迁移 additive、参数化、Workspace/Document/path 约束、批量映射和 guarded Down 符合约束；补充 publication Git lookup partial index。

## PRD Backfill Result

- 已回填 `docs/product/PRD.md` 的 `10.5`、`10.9`、`11.8`、`13.3`、`15.5`、`21.3` 和 `22`，新增 AC-40 与最终演示步骤 13。
- 稳定契约已写入 `.trellis/spec/backend/document-history-contract.md` 和 `.trellis/spec/frontend/document-history-workbench.md`，并加入两侧规范索引。
