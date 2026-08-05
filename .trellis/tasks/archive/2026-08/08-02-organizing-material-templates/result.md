# 材料确认与整理模板：交付结果

## Delivered Behavior

- 用户可用自然语言获得带命中原因与 Evidence 的材料建议，在同一整理页增删材料；建议、默认选择和手动补充都不会在显式确认前启动 Workflow。
- 整理 Draft 可恢复；确认时重新校验材料与 Evidence 版本，并原子冻结 Source/Document/Claim/Smart Collection 展开结果、模板 Revision 和 canonical input snapshot。
- 四个内置模板交付稳定输入、输出、Evidence/GAP、冲突和审批路径；专题文章先审大纲，多文档合并不静默覆盖，报告与面试复习默认保持 Artifact。
- 自定义模板采用不可变 Revision，只允许受约束的材料、章节和表达配置，不能关闭证据、冲突/GAP、审批或 Safe Writeback，也不能声明任意工具或脚本。

## Product PRD Backfill

- `docs/product/PRD.md` 的 `10.4.11 整理材料发现已交付契约`记录建议、降级、用户确认和版本复核。
- `10.6.10`、`10.11.11` 记录正式知识边界、四类内置模板和受约束自定义模板。
- `13.7.1 Workflow Input Snapshot` 记录不可变输入快照与材料判别联合；`AC-39` 记录端到端验收。

## Validation Evidence

- 子任务 PRD 的 10 条验收项全部完成。
- `go test -race -count=1 -timeout=120s ./internal/organizing/...` 通过。
- 全量前端回归 107 个测试文件、1126 个用例通过；typecheck、lint、production build 和 `git diff --check` 通过。
- 自动化覆盖建议降级、Draft 恢复、显式确认、Snapshot 漂移、四模板、Human Task、Artifact/Proposal owner binding、跨 Workspace 和响应丢失恢复。

## Review And Delivery Notes

- Review 覆盖 Snapshot 原子性、Evidence owner、模板治理、N+1/批量读取、Workflow 幂等与前端判别联合；未发现剩余 P0/P1 问题。
- 任务归档时未创建 Git commit；用户已于 2026-08-05 随后明确授权将本轮整批能力提交并推送。
