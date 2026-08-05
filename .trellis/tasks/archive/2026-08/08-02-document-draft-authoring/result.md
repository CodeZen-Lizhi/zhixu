# Document Draft 主动创作：交付结果

## Delivered Behavior

- `/authoring` 展示真实的最近草稿、待确认发布和已完成文档；`/authoring/new` 创建服务端 Working Draft，并提供标题、目标路径、Markdown 编辑、实时安全预览与自动恢复。
- 自动保存使用 expected version CAS，只更新 Working Draft；显式保存版本或发布前 Freeze 才追加不可变 Article Revision，多标签陈旧写入稳定返回冲突。
- 首次发布使用 `CREATE_ONLY` Proposal 与不存在证明，审批和 Safe Writeback 前后都复核目标路径，竞争文件不会被覆盖。
- Proposal 创建、批准、Git Commit 和 Authoring finalizer 分阶段展示；只有 Commit 与冻结 Revision 完全一致时才推进 Document/Revision 为 Published，响应丢失重放不会重复写回。

## Product PRD Backfill

- `docs/product/PRD.md` 的 `10.5.12 主动创作已交付契约`记录 Working Draft、Freeze、CREATE_ONLY Proposal 和发布最终化。
- “主动创作工作台”记录 `/authoring`、`/authoring/new`、Markdown 单一事实源、安全预览和真实发布状态。
- `AC-38` 记录空白草稿、冲突恢复、不可变 Revision、Git 最终化和桌面/移动验收。

## Validation Evidence

- 子任务 PRD 的 11 条验收项全部完成。
- `go test -race -count=1 -timeout=120s ./internal/authoring/...` 通过。
- 全量前端回归 `npm test -- --run` 通过，共 107 个测试文件、1126 个用例；typecheck、lint 和 production build 通过。
- 真实浏览器验证新建文章、保存/冻结 Revision、刷新恢复与 `390x844` 布局；最新创作台截图保存在 `output/playwright/authoring-records-*.png` 和 `new-article-*.png`。

## Review And Delivery Notes

- Review 覆盖 Markdown XSS、路径边界、CAS、幂等发布、CREATE_ONLY 竞争、Proposal/Commit/Revision 一致性和 Workspace 隔离；未发现剩余 P0/P1 问题。
- 任务归档时未创建 Git commit；用户已于 2026-08-05 随后明确授权将本轮整批能力提交并推送。
