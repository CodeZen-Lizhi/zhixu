# 文档文件历史与受控恢复：交付结果

## Delivered Behavior

- Document 可查看当前分支与 canonical path 的稳定分页时间线，区分知序 Safe Writeback、外部 Commit 和当前未提交改动。
- Managed Commit 只展示真实存在的 Revision、Proposal、Approval、Workflow 和 Writeback 关系；缺失关系保持为空，不为外部 Commit 补造审批事实。
- 支持 Commit 对 Commit、Commit 对当前工作树的有界 Diff；路径、Commit、Workspace 越界和超限输出整体拒绝。
- 恢复先冻结服务端 Preview 与影响范围并创建 `restore_document` Proposal；批准后追加新 Commit 和 Article Revision，不执行 reset、checkout 或历史重写，dirty/stale 状态 fail closed。

## Product PRD Backfill

- `docs/product/PRD.md` 的 `10.5.13 文档文件历史已交付契约`记录时间线、分页、关系映射、外部变更和比较边界。
- `10.9.7 受控恢复已交付契约`记录 Preview 绑定、幂等 Proposal、Safe Writeback 与 append-only Revision 最终化。
- “文档文件历史工作台”与 `AC-40` 记录桌面/移动交互、恢复文案、Workspace 切换和验收路径。

## Validation Evidence

- 子任务 PRD 的 9 条验收项全部完成。
- `go test -race -count=1 -timeout=120s ./internal/documenthistory/...` 通过。
- 全量前端回归 107 个测试文件、1126 个用例通过；typecheck、lint、production build 和 `git diff --check` 通过。
- 自动化覆盖 managed/external/current-change 联合、游标漂移、dirty 阻断、任意版本比较、响应丢失重放和 Monaco model 释放顺序。

## Review And Delivery Notes

- Review 覆盖 Git/数据库映射真实性、分页基线、输出上限、路径与 Workspace owner、恢复不重写历史和前端严格解码；未发现剩余 P0/P1 问题。
- 任务归档时未创建 Git commit；用户已于 2026-08-05 随后明确授权将本轮整批能力提交并推送。
