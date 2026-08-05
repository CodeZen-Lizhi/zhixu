# 主动创作工作台契约

## Scenario: Authoring Overview And Recoverable Markdown Editor

### 1. Scope / Trigger

- 修改 `web/src/api/authoring.ts`、`web/src/features/authoring`、`/authoring`、`/authoring/new`、
  Authoring 导航或发布恢复时应用。
- HTTP JSON、Active Workspace、Draft/Document/Revision/Publication identity 必须在 API/Query 边界绑定为 UI model。

### 2. Signatures

```ts
getAuthoringOverview(workspaceId, signal?)
listWorkingDrafts(workspaceId, options?, signal?)
listDocumentDrafts(workspaceId, options?, signal?)
createWorkingDraft(input)
getWorkingDraft(workspaceId, draftId, signal?)
updateWorkingDraft(input)
freezeWorkingDraft(input)
getDocumentDraft(workspaceId, documentId, signal?)
publishArticleRevision(input)
```

### 3. Contracts

- `web/src/api/authoring.ts` 是 Authoring wire 唯一 owner；字段集合、UUID、hash、time、版本、状态、href 和
  Workspace/Draft/Document/Revision/Proposal binding 全部严格运行时校验，Feature 不读取 snake_case 或自行 cast。
- Query key 必须包含 Workspace，并按 overview、Working Draft、Document 和 Publication identity 分层；
  SSE 只失效 Query，REST 响应仍是事实源。
- 编辑器保存 Working Draft，不把每次输入变成 Revision。autosave debounce、保存中、已保存、冲突和失败必须分离；
  页面刷新从服务端恢复，不写 Local Storage。
- Freeze 成功后以 mutation 响应中的最新 Revision 为准；旧 Document query 稍后返回时不得覆盖它。
  Publication 只有在 `articleRevisionId` 与当前 Revision 完全一致时才显示或禁用发布按钮。
- Markdown preview 必须经过现有 sanitizer；Monaco model 使用稳定 URI，并在 editor detach 后释放。
- `/authoring` 提供创建文章和恢复最近草稿的主入口；Organizing 未接入时显示明确 unavailable，不能指向假页面。
- 发布操作只显示 Proposal identity 和待审批状态，不宣称 Git、文件或正式知识已经完成。

### 4. Validation & Error Matrix

| Condition | Required result |
| --- | --- |
| 未知字段、非法 UUID/hash/time/version/status 或 binding 漂移 | `INVALID_RESPONSE`，不渲染部分事实 |
| autosave 409 | 保留本地输入，显示冲突并回查服务端；不显示已保存 |
| Freeze/Publish response unknown | 重试同一动作 identity 并回查，不生成第二命令 |
| 旧 Revision publication 返回 | 不显示为当前发布状态，当前 Revision 仍可发布 |
| capability unavailable | 保留只读内容与明确原因，不伪装为空或成功 |
| desktop/mobile console error、文本或 document 横向溢出 | browser gate 失败 |

### 5. Tests Required

- Decoder：exact fields、全部状态、Workspace/资源/Proposal binding、Problem/Abort、分页 cursor。
- Query/Mutation：Workspace key、stable idempotency、invalidation、autosave CAS、Freeze 最新响应优先、publication match。
- Component：overview、空态、编辑/预览、保存状态、冲突恢复、连续 Freeze、发布与返回焦点。
- Canonical：ESLint、TypeScript、Vitest、production build 和 `git diff --check`。
- Browser：真实 API/Worker/Vite，在桌面和 `390x844` 创建、恢复、Freeze、发布；无横向溢出和 console error/warning。

### 6. Wrong vs Correct

```text
Wrong: Document query 的旧 Revision 覆盖刚 Freeze 的 mutation 结果。
Correct: 最新 Freeze identity 优先，后续 query 必须与当前 Revision 对齐后才可替换。

Wrong: Publication 存在就禁用当前页面的发布按钮。
Correct: 只有 Publication.articleRevisionId 等于当前 Revision.id 才属于当前版本。
```
