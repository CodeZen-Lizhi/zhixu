# Git 远端同步交付收尾复验

## Goal

修正 Git 远端同步专项过早归档造成的交付记录漂移，并对归档后最后一轮修复重新完成自动化检查、真实浏览器验收与最终 Review，使“功能完成”和“交付证据完整”重新一致。

## Background

- 原 Git 同步任务的 `result.md` 于 2026-08-05 10:44:59 写入、`task.json` 于 10:45:13 完成归档，但 `web/src/features/settings/GitRemoteSettingsPanel.tsx` 在 10:49:20 仍有后续修复，因此原归档结论早于最终代码状态。
- 归档后的最终修复包括：Git Sync 前端 strict decoder 跨字段不变量、`UNKNOWN` 方向文案、缓存 Workspace 切换 fail-closed 门禁、Git `-z` 变更解析严格性，以及 Model Settings/Git Sync 共用 AES-GCM primitive 与显式 Git AAD purpose/schema。
- Git Sync 子任务和“笔记工作流基础能力增强”父任务原记录 `107` 个测试文件 / `1128` 个用例；收尾 Review 新增一条 decoder 负测后，最终稳定工作树实测为 `107` 个测试文件 / `1136` 个用例。
- 最后两项 UI 修复已有单元测试，但原归档后的真实浏览器验收尚未覆盖 `UNKNOWN` 展示和缓存 Workspace 切换门禁。
- 最终 Trellis Check 额外发现两个严格边界残余：嵌套 `changed_files[].old_path` 缺失会被当作 `null`，Rename score 会接受带符号数字；两项均已修复并补入既有回归用例。
- 工作区当前有大量未提交改动；本任务必须保留用户已有改动，不以清理工作区为手段完成验收。

## Requirements

### R1. 修正交付文档

- 更新 Git Sync 子任务与父任务结果文档，使其覆盖归档后五项修复、最终实际测试数量和最后一轮验证结果。
- 检查 Git Sync 前后端专项规范；仅在稳定契约缺失时补充，不重复记录一次性实现细节。
- 所有完成结论必须对应本任务重新取得的证据，不沿用已经失真的旧数字。

### R2. 真实浏览器复验最后 UI 行为

- 使用一次性 PostgreSQL、真实 API、Worker 和 Vite 运行 Git 同步设置页，不以组件测试或静态截图替代运行态验收。
- 构造 `direction=UNKNOWN` 的真实运行投影，确认界面显示“尚未判断”，不得显示“无变更”。
- 预热 Workspace A 的 Git Sync 缓存后切换到 Workspace B，确认 A 的配置、运行记录和可执行命令不会短暂暴露给 B；B 的绑定响应完成前应显示 fail-closed 切换状态。
- 在桌面 `1440x900` 与移动端 `390x844` 验证关键状态、无横向溢出、无内容重叠，并检查 Console 没有本任务引入的 error/warning。
- 验收完成后停止本任务启动的服务并清理一次性数据库、仓库和临时目录，不影响用户已有服务。

### R3. 重新执行质量门禁与最终 Review

- 运行 Git Sync、Model Settings crypto 和共享 Secret Store 的相关 Go race 测试，并重新运行受影响 Go 静态门禁。
- 运行 Web lint、typecheck、全量 Vitest 和 production build；记录准确文件数、用例数与既有 warning。
- 运行 OpenAPI 检查、`go mod tidy -diff` 和 `git diff --check`。
- 由 Trellis Check 对最终工作树重新审查，重点覆盖 strict decoder 不变量、Workspace 隔离、Git `-z` parser、Secret primitive/AAD 绑定和测试证据；明确记录是否仍有发现。

### R4. 诚实收尾

- 只有在 R1-R3 全部通过后才能重新归档本任务，并在结果文档中说明验证范围、剩余盲区及回滚边界。
- Git stage、commit、push 不属于本次默认授权；未经用户另行明确授权，不创建提交，也不把未提交工作区描述为已完成版本交付。

## Acceptance Criteria

- [x] AC-1：两份归档结果文档不再保留 `1128` 的过期测试数字，并列出归档后五项最终修复及本次复验结果。
- [x] AC-2：相关 Git Sync 前后端规范与当前稳定行为一致，无缺失契约或互相矛盾的完成声明。
- [x] AC-3：真实浏览器中 `UNKNOWN` 运行显示“尚未判断”，桌面和 `390x844` 均有可复核证据。
- [x] AC-4：真实浏览器中从已缓存 Workspace A 切换到 Workspace B 时，A 的 Git 配置、运行数据和命令不会渲染在 B 的作用域内。
- [x] AC-5：两个视口均无横向溢出或内容重叠，Console 没有本任务引入的 error/warning。
- [x] AC-6：Web lint、typecheck、全量测试、build，相关 Go race/vet，OpenAPI、tidy diff 和 diff check 全部通过；结果记录准确测试数量与 warning。
- [x] AC-7：最终 Trellis Check 无未处理的当前范围缺陷；如发现缺陷，修复后重新执行受影响验收。
- [x] AC-8：一次性 QA 资源已清理，用户已有进程和无关工作区改动未被修改或删除。
- [x] AC-9：本任务最初按 no-commit 方式复验；用户随后明确授权提交与推送，因此转入标准工作提交、任务归档和会话记录流程。

## Out Of Scope

- 新增或改变 Git Remote Sync 产品功能、API、数据库 Schema 或状态机语义。
- 为既有 Unix-only 安全打开逻辑补充 Windows 支持。
- 重跑“笔记工作流基础能力增强”其余五个能力的完整浏览器矩阵。
- 清理、回滚、暂存或提交与本任务无关的工作区改动。

## Key Decisions And Risks

- 本任务是轻量交付复验，使用 PRD-only 规划；若复验发现需要修改产品行为、共享契约或数据库，再回到规划阶段补充 Design/Implement。
- 浏览器环境必须使用隔离资源和独立端口；当前工作区已有用户进程时不得复用、终止或覆盖。
- 大量未提交文件会放大误改风险；文档修改和缺陷修复必须限定到 Git Sync/Secret Store 及本任务归档记录。
