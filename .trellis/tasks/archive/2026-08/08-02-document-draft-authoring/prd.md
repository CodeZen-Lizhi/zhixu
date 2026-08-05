# Document Draft 主动创作

## Goal

让用户从空白 Markdown 开始撰写文章，刷新或临时离开后仍能恢复编辑状态，并在显式冻结版本后通过既有 Proposal、Approval 与 Safe Writeback 发布为 Workspace Markdown 文档。

## Dependencies

- 不依赖 Quick Capture；不得把主动创作伪装成 Inbox Source 或 Artifact。
- 复用 `core.document`、`core.article_revision`、Change Control `file_patch` Proposal、Approval、Safe Writeback 与 Git Commit。
- 工作台任务只在本任务提供真实 `/authoring`、`/authoring/new` 与 overview 状态后接入导航和首页快捷动作。

## Requirements

### Working Draft

- 用户进入 `/authoring/new` 后可以立即获得服务端持久的空白 Working Draft，不要求先填写标题、路径或正文。
- Working Draft 以 Workspace 和 Draft 身份隔离，保存标题、目标路径和 Markdown 正文，并用单调 version 做 CAS 更新。
- 编辑页按 500-1000ms debounce 自动保存；刷新和重新进入可恢复服务端已确认版本。
- 自动保存只更新可变 Working Draft，不得为每次输入创建 `core.article_revision`。
- 多标签或陈旧版本更新返回明确冲突；页面保留本地未确认内容并允许重新加载服务端版本，不静默覆盖。

### Freeze And Document Identity

- 用户显式“保存版本”或进入发布时，系统校验标题、目标路径和非空正文，再原子创建或更新 Document Draft 与不可变 Article Revision Draft。
- 目标路径必须是 Workspace 内的相对 `.md` 路径；拒绝绝对路径、反斜杠、遍历、保留目录和已被其他 Document 占用的路径。
- 首次冻结创建 `core.document(DRAFT)` 和 `article_revision(DRAFT)`；后续冻结追加 revision_no 并绑定父 Revision，不修改历史 Revision。
- 相同幂等命令重放返回同一 Revision；内容或目标不同的同 key 请求必须冲突。

### Publish Governance

- 发布从冻结的 Article Revision 创建既有 `file_patch` Proposal；Proposal 正文、目标路径、内容哈希必须与 Revision 完全一致。
- 首次发布新路径必须使用显式 create-only Safe Writeback：批准与应用时都证明目标尚不存在，且并发出现的文件绝不被覆盖；不得用空文件哈希或预建占位文件绕过审批。
- 创建 Proposal 后写入不可变 publication binding，关联 Workspace、Document、Article Revision、Proposal 和 Proposal Revision。
- 未批准前 Document 保持 DRAFT；前端不得把“Proposal 已创建”描述成“已发布”。
- Approval 与 Safe Writeback 继续由现有 Change Control owner 执行；本任务不得直接写 Workspace 文件或 Git。
- Safe Writeback Commit 成功后，由幂等 finalizer 将绑定 Article Revision 标记 PUBLISHED，并更新 Document 的 current_published_revision_id、lifecycle_status 和 git_commit。
- base hash 漂移、dirty Workspace、审批失败或 finalizer 失败必须保持可恢复状态，不得伪造发布成功或重复 Commit。
- 可证明 Proposal 尚未创建的确定性失败必须终结为 ABANDONED 并稳定重放原错误；响应未知或暂时故障必须保留 PENDING，不能猜测外部结果。

### Authoring UI

- `/authoring` 展示“新建文章 / 整理成文”两条主动作，并按真实数据展示最近草稿、待确认和已完成；整理成文只在 Organizing 能力真实可用后启用。
- `/authoring/new` 提供标题、目标路径、Markdown 编辑器、实时预览、保存状态、版本冲突处理、显式保存版本和发布操作。
- Markdown 是单一编辑事实；不提供富文本、块编辑器或 HTML/Markdown 双向转换。
- 预览使用受控 Markdown renderer，拒绝原始 HTML、脚本和不安全链接协议；不得执行用户内容。
- 发布后进入真实 Proposal 详情或创作台状态，不显示假成功页。

### API And Recovery

- 提供 Working Draft create/get/update、freeze、publish proposal、Authoring overview 和 Document Draft detail/list API。
- 所有命令要求单个 Workspace-scoped `Idempotency-Key`；更新要求 expected version；响应严格、可刷新恢复。
- SSE 仅用于对应 Query family invalidation，REST/DB 仍是事实源；Article Revision、Proposal 和服务端状态不得写入 Browser Storage。

## Acceptance Criteria

- [x] 用户可创建空白 Working Draft，输入标题、路径和 Markdown 后自动保存，刷新页面恢复服务端版本。
- [x] 连续自动保存不会创建逐输入 Article Revision；只有显式保存版本或发布前冻结才新增不可变 Revision。
- [x] 两个标签同时编辑时，陈旧更新被 409 拒绝且本地内容不会静默丢失。
- [x] 非法/冲突路径、空标题和空正文不能冻结或发布。
- [x] 相同幂等命令不会重复创建 Draft、Document、Revision、Proposal 或 publication binding。
- [x] 发布 Proposal 的路径、正文和 hash 与冻结 Revision 一致；批准前 Document 不会变为 PUBLISHED。
- [x] Proposal 批准并完成 Safe Writeback 后，Git Commit、Article Revision 和 Document current revision 一致；响应丢失重放不会重复写回。
- [x] 新文章路径在批准后以 no-replace 方式创建并进入 Git；审批后路径被用户或其他进程占用时稳定冲突且不覆盖现有文件。
- [x] `/authoring` 使用真实 overview 状态；`/authoring/new` 在桌面和 `390x844` 下可编辑、预览、恢复和发布，无横向溢出或焦点丢失。
- [x] Markdown 预览不执行原始 HTML、脚本、事件属性或危险 URL。
- [x] 现有 Proposal 审阅、Approval、Safe Writeback 与旧业务深链保持兼容。

## Acceptance Evidence

- Go：Authoring/Change Control 单元、race、vet、真实 PostgreSQL migration/repository 与 CREATE_ONLY Git adapter 定向用例通过。
- Contract：OpenAPI checker 通过；`target_mode`、absence token、ABANDONED 稳定错误均由机器契约约束。
- Web：ESLint、TypeScript、99 个 Vitest 文件共 988 项测试和 production build 通过。
- Browser：真实 API/Vite 下完成创建、autosave/刷新恢复、Freeze、发布 Proposal 与审阅；桌面和 `390x844` 均无横向溢出，恶意 Markdown 未产生脚本、事件处理器或危险链接。
- 已知门禁边界：`internal/platform/gitcli` 全包 63 个真实仓库用例总时长超过 60 秒包级超时；本次影响的 CREATE_ONLY/no-replace/private-index/recovery 定向用例均在 60 秒内通过。

## Out Of Scope

- 富文本、块编辑器、协同光标和实时多人编辑。
- 浏览器本地长期草稿作为第二事实源。
- 自动批准、直接覆盖正式 Markdown 或绕过 Safe Writeback。
- 把 Working Draft 建模为 Source、Artifact、Proposal 或 Workflow。
