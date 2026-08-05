# 笔记工作流基础能力增强：实施计划

## 1. 父任务职责

父任务不直接承载业务实现。它负责维护总 PRD/设计、现有四个子任务与新增 Document Draft 创作切片的依赖、公共术语、跨子任务 API/事件一致性和最终端到端验收。

## 2. 实施顺序

### 2.0 Document Draft 创作切片规划门禁

- 在实现前创建独立子任务，拥有创作台、空白 Document Draft、Article Revision Draft 编辑与两条创作路径的统一结果入口；不得并入正在执行的 Quick Capture 子任务。
- 新建 `/authoring` 与 `/authoring/new`：前者展示两条创作动作和“最近草稿 / 待确认 / 已完成”，后者提供标题、目标路径、Markdown 正文、实时预览与明确发布。
- 建立带版本冲突检测的 Working Draft 自动恢复；自动保存不得逐输入创建 Article Revision，显式版本节点才冻结不可变 Article Revision Draft。
- 不引入富文本、块编辑器或 HTML/Markdown 双向转换；预览保持只读派生。
- 补齐该子任务 PRD、设计、实施计划和上下文清单。
- 新增正式 Document 创建 Application/API；不得通过 `DOCUMENT_DRAFT Artifact` 或前端路由别名伪造能力完成。
- 与 Organizing 子任务约定统一 Document Draft 结果契约，确保手写与整理生成不形成两套发布模型。

### 2.1 快速记录与文档知识画像

目标子任务：`08-02-quick-capture-profile`。

- 建立 Capture 状态机、Source owner bridge、JSON/multipart API 和 Inbox read model。
- 支持文字、URL、Markdown/TXT/PDF/HTML、图片/截图；原始输入先可靠保存。
- 接通异步 URL fetch、Ingestion、Index 和 Profile Workflow。
- 建立 Profile Revision、Source Span 引用、失败/降级/重试状态。
- 在 AppShell 增加全局 Quick Capture 入口与应用内快捷键，完善 Inbox/详情。
- 完成安全、解析、模型不可用、刷新恢复和桌面/移动验证。

回滚点：Capture/Profile capability 可整体关闭；既有 Workspace Scan 与 Source Version API 保持工作。

### 2.2 材料确认与整理模板

目标子任务：`08-02-organizing-material-templates`。

硬依赖：Profile v1 schema、Capture/Source 状态和批量 Evidence 读取契约已稳定。

- 建立 Organizing Draft、Suggested Material Set、召回理由和草稿恢复。
- 建立确认命令与 Workflow Input Snapshot，冻结材料和 Template Revision。
- 实现四个 Built-in Template 的固定 Workflow Definition。
- 实现声明式 Custom Template、clone、不可变 Revision、校验与编译器。
- 复用 Workflow Human Task、Artifact Citation/Coverage、Change Control Publication。
- 构建整理发起页、材料增删/确认、大纲审批、结果与发布路径。

回滚点：关闭 Organizing capability 不删除 Draft/Snapshot/Template Revision；基础 Search/Profile 继续可用。

### 2.3 文档文件历史与受控恢复

目标子任务：`08-02-document-file-history`。

- 建立受限 Git history/diff/blob 只读端口。
- 将 Commit 与 Article Revision、Proposal Commit 映射组合为统一历史页。
- 显示外部 Commit 和当前未提交工作树 Diff。
- 实现任意两个可读版本比较与有界分页。
- 增加 `RESTORE_DOCUMENT` Proposal，复用 Approval/Safe Writeback 生成新 Commit。
- 在文档详情提供历史、比较和恢复交互。

回滚点：History capability 可关闭；不修改现有 Commit/Revision，恢复路径只追加新 Proposal/Commit。

### 2.4 Git 远端同步

目标子任务：`08-02-git-remote-sync`。

硬依赖：外部文件变化重新捕获、Ingestion/Index 入口已由 Quick Capture/Profile 子任务提供。

- 建立通用 Secret Sealer 与 Git Token 专用存储/AAD，保持 Model Settings 兼容。
- 建立 Remote Config、SyncRun、Outbox、API 和 Settings UI。
- 扩展 Git CLI 为独立 Remote Adapter：Fetch、status/ancestry、FastForward、Push、post-check。
- 使用受控 AskPass/credential session，完成 Secret/argv/config/log 扫描。
- 实现手动同步；验证稳定后再启用批准写回完成后的可选自动触发。
- 拉取成功后接入外部变更捕获与索引；索引失败与 Git 同步状态分离展示。

回滚点：默认关闭自动同步；禁用 gitsync capability 不影响本地 Git Commit、Safe Writeback 或历史查看。

## 3. 父任务集成验收

- 快速记录一篇 Java AI 资料，观察 Source → Ingestion → Index → Profile 全状态。
- 从 `/authoring/new` 创建 Markdown 文章，验证 Working Draft 自动恢复、版本冲突、显式 Article Revision Draft、发布 Proposal 与 Safe Writeback；自动保存期间不产生逐输入 Revision。
- 输入一个知识点，系统给出带原因的 Suggested Material Set；用户修改并确认后冻结 Snapshot。
- 使用专题知识文章模板确认大纲、生成带引用草稿、发布 Proposal、审批并 Safe Writeback。
- 在文件历史中看到新 Commit 与 Proposal/Approval/Workflow 关联，并通过恢复 Proposal 产生新 Commit。
- 配置 HTTPS Remote，手动 Push；模拟远端单向领先完成 Fast-forward 与重新索引。
- 模拟 dirty 和 diverged，证明同步停止且没有 Merge/Rebase/Force Push/Reset/Checkout。
- 证明 URL/OCR/Profile/自动同步任一失败不会让原始资料、本地 Commit 或其他能力出现假成功/假失败。

## 4. 数据与契约门禁

- 每个子任务使用独立 additive migration，fresh/upgrade/repeat/guarded Down 均验证。
- 所有新 API 更新 OpenAPI，并用严格 Decoder 覆盖未知字段、错误枚举、Workspace 漂移和 response-loss。
- 所有命令使用 Idempotency-Key、expected version/request hash；重试不创建重复 Source、Document、Article Revision、Snapshot、Proposal 或 SyncRun。
- SSE 仅做 Query invalidation；刷新后从 REST/PostgreSQL 恢复。
- Profile/Template/Snapshot/SyncRun 的版本和模型/工作流配置均可反查。

## 5. 分层验证

每个子任务至少执行受影响范围的短时门禁：

```bash
go test -race -count=1 -timeout 60s ./internal/<affected>/...
go vet ./internal/<affected>/...
go mod tidy -diff
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web -- <affected tests>
npm run build --prefix web
make openapi-check
git diff --check
```

涉及 PostgreSQL、Git、副作用恢复和 Secret 的子任务增加 disposable PostgreSQL integration、真实临时 Git Remote、fault smoke、Secret scan 和恢复演练。不得用 skip 结果代替验收。

父任务最终执行仓库现有 canonical gate 中与全部子任务相关的组合，并进行桌面 `1440x900`、移动 `390x844` 浏览器主链路验证；检查 Console、Network、横向溢出、键盘焦点和冲突状态。

## 6. PRD 回填门禁

每个子任务在实现、验证和 Review 通过后、归档前执行一次产品文档回填：

1. 对照子任务 PRD、最终 Diff 和验收证据，列出真正交付的用户行为、限制、状态与失败语义。
2. 只更新 `docs/product/PRD.md` 中对应的功能、工作流、数据契约、页面和 `v1.0` 验收矩阵；不整篇复制专项 PRD，不回填技术实现细节。
3. 未实现、被取消或验证失败的条目保持在专项任务中，不得写成总 PRD 的既成事实。
4. 在子任务完成记录中列明“已更新章节”或“无需更新及理由”，供父任务最终复核。
5. 保持总 PRD 的产品版本为 `v1.0`；本专项不得创建 `v2.0`、`PRD-2.0` 或同义副本。文档修订信息与产品大版本分开管理。

父任务最终集成时复核全部子任务回填映射、总 PRD 与实际行为的一致性，并确认不存在第二份产品级 PRD 或未交付声明。

## 7. Review Gate

- 每个子任务实现后使用对应 Trellis check agent，并按改动类型执行 `go-review`、`code-review-and-quality`；涉及 SQL 时追加 `sql-code-review`。
- Secret、URL Fetch、Git Remote、文件恢复属于高风险范围，必须检查权限、输入边界、凭据泄露、并发、结果未知和回滚。
- 父任务最终 Review 检查全部子任务之间没有第二事实源、重复写入路径、版本漂移或错误状态合并。

## 8. 启动规则

- 最终规划审批后，不启动父任务；先启动 `08-02-quick-capture-profile`。
- 一个子任务通过实现、检查、规范更新、提交和归档后，再启动依赖它的子任务。
- 每个子任务归档前必须通过第 6 节 PRD 回填门禁；尚未交付时不得提前修改总 PRD 来宣称能力已经存在。
- File History 可与 Organizing 在依赖满足后独立排期，但同一工作区并发开发时不得同时修改 Git CLI Runner 公共契约。
- 任一子任务需要改变本 PRD 的产品范围时，先回到父任务更新规划并重新获得审批。
