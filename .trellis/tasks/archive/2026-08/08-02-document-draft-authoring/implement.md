# Document Draft 主动创作：实施计划

## Order

1. 定义 Working Draft、Freeze、Publication Binding 状态、路径和幂等请求哈希测试。
2. 增加 additive migration、PostgreSQL Repository、CAS/append-only/Down guard 集成测试。
3. 实现 create/get/update/list/freeze Application，原子创建 Document 与 Article Revision。
4. 扩展 Change Control `CREATE_ONLY` target mode、路径锁、no-replace 文件发布与受控 Git add，并保持 `REPLACE` 兼容。
5. 实现 Change Control publication bridge 与 `proposal_commit` 驱动的幂等 finalizer/recovery。
6. 更新 HTTP、OpenAPI、Capability、Composition Root 和路由测试。
7. 实现严格前端 API、Query、`/authoring` overview 与 `/authoring/new` 编辑器。
8. 实现安全 Markdown preview、autosave debounce、冲突恢复和 Monaco model 生命周期。
9. 串接 Proposal detail、Approval、Safe Writeback 与发布状态恢复。
10. 完成跨层、并发、response-loss、XSS、浏览器和移动端检查。

## Validation

```bash
go test -race -count=1 -timeout 60s ./internal/authoring/... ./internal/changecontrol/...
go vet ./internal/authoring/... ./internal/changecontrol/...
go mod tidy -diff
make openapi-check
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web -- authoring AppRoutes ProposalsPage
npm run build --prefix web
git diff --check
```

增加 disposable PostgreSQL migration/concurrency、freeze/publish response-loss、Safe Writeback finalizer recovery 与真实 Git smoke；浏览器覆盖桌面和 `390x844` 的创建、恢复、冲突、预览、发布和返回焦点。

## PRD Backfill Gate

实现、验证和 Review 通过后、归档前，将稳定交付行为回填到 `docs/product/PRD.md` 的 `6.2-6.4`、`10.5-10.6`、`11.1`、`13.3`、`13.7`、`21.3` 和 `22`；不得提前写入未交付行为，不创建 `v2.0` PRD。

已完成：主动创作入口、Working Draft/Revision、CREATE_ONLY 发布治理、数据事实、工作台和 AC-38 已回填到原
`docs/product/PRD.md` 的 `6.2`、`6.4`、`10.5.12`、`11.1`、`13.3`、`21.3` 与 `22`；`10.6`、`13.7`
无需新增实现现状，因为本能力不拥有知识抽取或 Workflow Run。

## Review

- 使用 `go-review`、`code-review-and-quality` 和 `sql-code-review`。
- 重点检查路径边界、CAS、逐输入 Revision、幂等、发布双写、Workspace 隔离、XSS、浏览器状态和 Safe Writeback owner。

## Dependency Output

归档前冻结并记录 Document Draft、Working Draft、Article Revision freeze、publication binding 和 Authoring overview 契约，供 Organizing 与 Workbench 只通过公开边界集成。
