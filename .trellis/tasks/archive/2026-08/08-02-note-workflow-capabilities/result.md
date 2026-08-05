# 笔记工作流基础能力增强：交付结果

## Delivered Capability Slices

- Quick Capture/Profile：文字、URL、文件和图片进入 Inbox，保留不可变来源；解析、全文索引、向量、OCR/视觉和候选画像按独立阶段恢复与降级。
- Document Draft/Authoring：从空白 Markdown Working Draft 主动创作，自动保存但不制造逐输入 Revision，发布走 Proposal、Approval 与 Safe Writeback。
- Organizing Material/Templates：可解释材料建议、同页增删、显式确认、不可变输入快照、四类内置模板和受约束自定义模板。
- Document History/Restore：当前路径的 managed/external/current-change 时间线、任意版本比较和追加新 Commit/Revision 的 Proposal 化恢复。
- Git Remote Sync：单 HTTPS Remote、加密只写 Token、Fetch/Fast-forward/non-force Push、post-check、自动同步与独立索引 follow-up。

## Cross-Cutting Product Outcome

- 一级导航使用“创作”，工作台固定提供“快速记录 / 新建文章 / 整理成文 / 搜索知识”；Quick Capture、Document Draft 和 Artifact 保持不同身份与发布路径。
- 资料建议在用户确认前不会启动生成；正式知识、Proposal、Git Commit、索引和远端同步状态保持各自事实边界。
- `docs/product/PRD.md` 仍是唯一产品级 `v1.0` 总 PRD，并已回填 `AC-37` 至 `AC-41` 及各能力对应章节；五个子任务结果文档均记录具体映射。

## Validation Evidence

- 父任务 36 条验收项及五个子任务共 48 条验收项全部完成。
- Go race：Authoring、Document History、Organizing、Capture/Profile、Git Sync 及其关键 Change Control/API/Worker 组合范围通过；迁移与 Repository 集成测试覆盖幂等、漂移、恢复和 Workspace owner。
- Web：2026-08-05 收尾复验以最终稳定工作树重新运行，typecheck、lint、production build、全量 107 个测试文件 / 1136 个用例通过；OpenAPI check 与 `git diff --check` 通过。Build 仅保留既有 chunk-size warning。
- 浏览器：真实 PostgreSQL、API、Worker 和 Vite 下完成 Quick Capture、工作台、知识菜单、创作台、新建文章、系统状态和 Git 设置/同步；四个目标视口无横向溢出，Console 0 error / 0 warning。

## Review And Delivery Notes

- Review 覆盖五个能力的幂等回放、不可变事实、Secret、Workspace 隔离、Git 安全门、跨层最终化、严格前端解码、焦点与响应式；已修复所有当前任务范围内明确问题。Git CLI 在本专项开始前已有的 Windows 交叉编译限制单独记录在 Git Sync 结果中，未通过删除 Unix 安全打开保护来掩盖。
- Git Sync 归档后的五项最终修复也已纳入父任务交付事实：strict decoder 跨字段与 required nullable 字段不变量、`UNKNOWN` 文案、Workspace 切换 fail-closed 门禁、Git `-z` 严格解析，以及共享 AES-GCM primitive 与 Git 独立 AAD purpose/schema。真实 API/Worker/Vite 在 `1440x900` 与 `390x844` 复验通过，Workspace A 数据未在 B 切换期间渲染，两个视口无横向溢出或重叠，Console 0 error / 0 warning。
- 最终 Trellis Check 发现的 `changed_files[].old_path` 必填 nullable 校验和 Rename score 符号接受问题均已修复，并在修复后重跑 Web 全量门禁与 Git CLI 全包 race；当前范围没有未处理发现。
- 五个交付子任务与本父任务最初均以 `--no-commit` 归档；Git Sync 子任务归档后又完成独立收尾复验并修正交付记录。用户已于 2026-08-05 随后明确授权将整批工作区改动提交并推送。
