# 文档文件历史工作台契约

## Scenario: Strict Timeline, Comparison And Governed Restore

### 1. Scope / Trigger

- 修改 `web/src/api/document-history.ts`、`web/src/features/document-history`、
  `/authoring/documents/:documentId/history`、History SSE recovery 或共享 Monaco Diff Viewer 时应用。
- REST 响应、Active Workspace、Document/head/path/version/cursor、版本引用和 Proposal identity 必须在 API/Query 边界绑定后
  才能进入组件。SSE 只通知失效，REST 始终是事实源。

### 2. Signatures

```ts
listDocumentHistory(input, signal?)
compareDocumentHistory(input, signal?)
createDocumentRestorePreview(input, signal?)
createDocumentRestoreProposal(input, signal?)

documentHistoryQueryKeys.history(workspaceId, documentId, head, cursor, limit)
documentHistoryQueryKeys.compare(workspaceId, documentId, head, path, documentVersion, left, right)
resetDocumentHistoryWorkspaceQueriesForRecovery(queryClient, workspaceId)
```

- 时间线使用 `ManagedDocumentHistoryEntry | ExternalDocumentHistoryEntry | CurrentDocumentHistoryEntry` 严格判别联合。
- compare 版本只允许 Commit OID 或 `WORKTREE`；restore 目标只能是 Commit。

### 3. Contracts

- `web/src/api/document-history.ts` 唯一拥有 History wire。UUID、SHA-1/SHA-256 OID、SHA-256 hash、RFC3339、父 Commit、
  branch/path、nullable relationship、Proposal type 和状态都运行时校验；组件只消费 camelCase model。
- `MANAGED` 允许真实的部分关系。Proposal/Revision/Approval/Workflow/Writeback 分别展示；只有 Workflow 和 Writeback 都为空时
  才显示未关联。存在 subordinate relation 却没有 Proposal，或 Approval timestamp 没有 Approval 时拒绝响应。
- `EXTERNAL` 不显示伪造的 Approval/Workflow；`CURRENT_CHANGE` 固定使用 `WORKTREE`，只出现在首屏顶部且不算历史版本。
  页面分别表达“仓库有未提交改动”和“当前 Document 有未提交改动”。
- History Query key 绑定 Workspace、Document、发现/已知 HEAD、cursor 与 limit；Compare key 额外绑定 path、Document version、
  left/right。Workspace 切换取消并移除旧 cache，迟到响应不得进入新 Workspace。
- cursor stale 或 SSE recovery 先取消并移除该 Workspace 的 History query family，重置本地页码/选择/对比/恢复 Dialog，
  再取首屏。旧 cursor 不得自动重放；refetch 失败不能阻塞全站 SSE cursor 提交。
- 比较区明确展示 left/right identity、来源、时间和完整 Diff；允许 Commit 对 Commit 或 Commit 对 `WORKTREE`，相同版本不请求。
  返回的 Workspace、Document、HEAD、path、left/right 与当前页面 baseline 任一不一致即拒绝整个响应。
- restore Dialog 必须先请求服务端 preview，展示“当前 HEAD → 目标 Commit”的反向 Diff、preview hash 和 dirty 阻断。
  创建 Proposal 提交 server preview 的 expected HEAD/version/hash 与新 idempotency key；成功只显示待审批并链接 Proposal，
  不能显示文件或 Git 已恢复。
- restore 过程中页面 baseline 变化必须关闭/失效旧 preview 和成功态；409/stale 保留可解释错误，引导用户刷新首屏重新预览。
- Monaco DiffEditor 使用确定性 URI；先 detach editor model 再释放 model，错误/卸载/切换都执行同一清理。纯文本 patch 保留
  可访问 fallback，不把大 Diff 再复制进全局状态。
- 长 path/hash/Commit 使用可换行或水平滚动的专用容器；`390x844` 下时间线、版本对和 Dialog 改为单列，按钮与文字不得重叠。

### 4. Validation & Error Matrix

| Condition | Required result |
| --- | --- |
| 未知字段/枚举、非法 UUID/OID/hash/time/path/parent 数量 | `INVALID_RESPONSE`；不渲染部分时间线 |
| Managed subordinate relation 没有 Proposal | `INVALID_RESPONSE`；不猜测 owner |
| 合法 partial Workflow/Writeback mapping | 分别展示存在项；缺失项保持空 |
| History cursor stale | 清理旧页和本地选择，回到首屏；不重放旧 cursor |
| SSE recovery refetch 失败 | 页面保留可重试错误；全站事件 cursor 仍可提交 |
| Compare baseline 与当前 Workspace/head/path/version 不一致 | 拒绝响应并要求刷新 |
| dirty 或 restore preview stale | 禁用创建 Proposal；不 optimistic 显示恢复成功 |
| Workspace 切换时旧请求完成 | 丢弃/清理旧 cache，不污染新页面 |
| 长 path/hash/diff 在桌面或 `390x844` 溢出 | browser gate 失败 |

### 5. Good / Base / Bad Cases

- Good：用户分页浏览 managed/external 历史，选择两个版本比较，预览旧 Commit 后创建 restore Proposal，并跳转审批。
- Base：只有部分 managed relation 时仍展示已知事实；其他文件 dirty 时没有假 CURRENT_CHANGE，但恢复明确禁用。
- Bad：SSE 直接拼入 Commit、组件自行解释 snake_case、cursor stale 后继续下一页、Proposal 创建后显示“已恢复”，
  或 Workspace 切换仍复用旧 Diff model/query。

### 6. Tests Required

- Decoder：三类 union、partial relation、SHA-1/SHA-256、父 Commit 上限、nullable mapping、unknown/duplicate/mismatch/Problem/Abort。
- Query/SSE：Workspace/head/path/version/cursor key、首屏发现、分页、stale recovery、SSE invalidation failure 与跨 Workspace cleanup。
- Component：managed/external/current、仅 Workflow/仅 Writeback、compare identity、dirty restore、preview stale、Proposal pending/replay、
  loading/empty/failure/recovery、键盘、焦点、长文本和 reduced motion。
- Monaco：model URI 隔离、detach-before-dispose、error/unmount/switch cleanup 和文本 fallback。
- Canonical：ESLint、TypeScript、Vitest、production build、`git diff --check`；真实桌面和 `390x844` 浏览器无 overflow、
  overlap、console warning/error 或失败请求。

### 7. Wrong vs Correct

```text
Wrong: workflowRunId 或 writebackId 任一为空，就把两项都显示为“未关联”。
Correct: 两项独立展示；只有两项都为空才显示未关联。

Wrong: SSE 收到 history.changed 后把新 Commit 直接 append 到当前页。
Correct: 清理旧 cursor family 并重新读取首屏；REST 重新建立完整 baseline。

Wrong: create restore proposal 成功后展示“文件已恢复”。
Correct: 展示“恢复 Proposal 已创建”，并引导进入 Proposal 审批；正式文件状态由后续写回查询决定。
```
