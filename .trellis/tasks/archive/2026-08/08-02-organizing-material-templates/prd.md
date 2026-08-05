# 材料确认与整理模板

## Goal

让用户输入一个知识点即可获得可解释的相关材料建议，在同一整理页面补充、删除并确认材料，再通过内置或自定义模板生成可追溯结果，无需逐篇翻找 Workspace，也不绕过证据与审批。

## Dependencies

- 硬依赖 `08-02-quick-capture-profile` 已交付并冻结 Document Knowledge Profile v1、Source/Capture 状态和 Evidence 批量读取契约。
- 复用 Retrieval、Knowledge 正式查询、Smart Collection、Workflow Human Task、Artifact 和 Change Control。
- 本任务不得修改或放宽 Safe Writeback。

## Requirements

### Suggested Material Set

- 用户以自然语言、Topic 或 Smart Collection 发起整理，系统结合 Hybrid Search、Profile、别名和正式知识召回候选。
- 每项候选展示命中原因、来源状态、版本、Profile/正式知识身份、冲突提示和可打开 Evidence。
- 候选只是建议；系统不得在用户确认前启动生成，也不得静默把默认选中当作授权。
- 用户在同一整理页搜索补充、删除或重新建议 Source、Document、Claim 和 Smart Collection 结果。
- 整理草稿刷新或暂时离开后可恢复；首版不建设全局材料篮或多个材料篮。

### Workflow Input Snapshot

- 确认命令冻结实际 Source Version、Document Revision、Claim/Evidence、Template Revision 和相关检索/画像版本。
- 确认使用 expected draft version；草稿变化、材料 stale 或证据不可访问时拒绝启动并要求重新确认。
- Snapshot 不因后续 Source/Profile/Template 变化而改写，结果可反查实际输入和版本。

### Built-in Templates

- 专题知识文章：先生成大纲并等待用户确认，再分章生成带引用的 `DOCUMENT_DRAFT` Artifact；发布创建 Proposal。
- 多文档合并整理：展示重复、互补、冲突和独特内容，生成保留来源的合并草稿与 Markdown Diff；确认目标后创建 Merge Proposal，不覆盖原文。
- 知识总结报告：生成覆盖、结论、来源、冲突和缺口 Artifact，默认不成为正式知识。
- 面试复习文档：生成核心概念、问题、追问、代码示例和来源 Artifact，默认不成为正式知识。
- 普通阅读摘要继续由 Document Knowledge Profile 提供；会议纪要不进入首版。

### Custom Templates

- 用户可以克隆只读内置模板，或从系统支持的整理类型创建 Workspace 自定义模板。
- 可配置名称/说明、材料条件、必选/可选章节与顺序、目标读者、语言、语气、篇幅、代码/示例/FAQ、默认结果路径与文件名、补充指令。
- 每次保存产生不可变 Template Revision；运行冻结具体 Revision，历史结果可反查。
- 模板是受约束声明，不是任意 Prompt、脚本或 Workflow；不能修改节点、重试、权限、工具、材料确认、引用、冲突/缺口、Approval 或 Safe Writeback。
- 保存和运行前严格校验声明；不合法或越权配置明确拒绝。

### Results And UX

- 整理 Workflow、Human Task、Artifact/Proposal 和失败状态刷新后可恢复；SSE 只触发权威回查。
- 结果引用必须可打开；没有足够证据的章节显示 GAP，不能用模型常识静默补齐。
- Artifact 发布和 Merge Proposal 都必须经过现有 Proposal/Approval/Safe Writeback。

## Acceptance Criteria

- [x] 输入知识点后得到带命中原因和 Evidence 的候选，不必逐篇浏览 Workspace。
- [x] 用户可在整理页增删材料；未明确确认时没有生成 Workflow。
- [x] 刷新后恢复同一整理草稿；不存在全局材料篮入口。
- [x] 确认后 Snapshot 可反查每个材料、证据和模板版本，后续变化不改写它。
- [x] 四个内置模板都有稳定输入、输出、证据、冲突/GAP 和审批路径。
- [x] 专题文章正文生成前必须确认大纲；多文档合并不会静默覆盖原文或丢弃冲突。
- [x] 总结报告与面试复习文档默认保持 Artifact。
- [x] 自定义模板修改产生新 Revision；旧运行继续展示原 Revision。
- [x] 自定义模板不能关闭材料确认、证据、冲突/GAP、审批或 Safe Writeback，也不能调用任意工具。
- [x] Workspace 切换、response loss、刷新和失败重试不会复用另一 Workspace 的 Draft/Snapshot/Run。

## Out Of Scope

- 全局/多材料篮、实时多人共同编辑草稿。
- 通用无代码 Workflow 编辑器、脚本/MCP/数据库/Git 工具模板。
- 自动确认正式 Topic/Claim/Relation 或直接覆盖 Markdown。
- 会议纪要和任意用户自定义输出执行器。
