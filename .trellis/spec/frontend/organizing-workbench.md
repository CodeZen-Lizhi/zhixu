# 材料整理工作台契约

## Scenario: Recoverable Material Selection And Reviewable Results

### 1. Scope / Trigger

- 修改 `web/src/api/organizing.ts`、`web/src/features/organizing`、`/organizing`、Template UI、
  Workflow Human Task review 或 Organizing 导航/结果入口时应用。
- HTTP JSON、Active Workspace、Draft、Material、Template Revision、Snapshot、Run、Artifact、Proposal 和 Evidence
  必须在 API/Query 边界绑定后才能进入组件。

### 2. Signatures

```ts
createOrganizingDraft(input)
getOrganizingDraft(workspaceId, draftId, signal?)
updateOrganizingDraft(input)
suggestOrganizingMaterials(input)
searchOrganizingMaterials(input)
addOrganizingMaterial(input)
removeOrganizingMaterial(input)
setOrganizingMaterialSelection(input)
confirmOrganizingDraft(input)
getOrganizingSnapshot(workspaceId, snapshotId, signal?)
listOrganizingTemplates(workspaceId, options?, signal?)
createOrganizingTemplate(input)
cloneOrganizingTemplate(input)
reviseOrganizingTemplate(input)
getOrganizingRun(workspaceId, snapshotId, signal?)
getWorkflow(workspaceId, runId, signal?)
```

- `web/src/api/organizing.ts` 唯一拥有 Organizing wire；`web/src/api/business.ts` 唯一拥有 Workflow review wire。

### 3. Contracts

- Material 使用严格判别联合；UUID、64 位小写 hash、RFC3339、版本、状态、reason、Evidence tuple/href、Template declaration
  和结果 binding 全部运行时校验。Feature 只消费 camelCase UI model，不读 snake_case 或 assertion cast。
- Query key 必须绑定 Workspace 与 Draft/Snapshot/Template/Run 完整 identity；Workspace 切换取消旧请求并移除旧 cache。
  SSE 只触发 invalidation/refetch，REST 仍是唯一事实源。
- `/organizing` 是唯一材料选择 owner：同页展示建议理由、状态、Evidence、补充搜索、增删和显式选择。
  不建立全局材料篮，不把默认选中或 Suggest 成功显示为已授权/已生成。
- 补充搜索按 Workspace、query 和单一 Material kind 组成 Query key，最多取 25 项。搜索 wire 只接受
  Source Version ID、Document/Revision ID、Claim ID 或 Smart Collection ID；不得携带 version/hash/Evidence。
  点击加入只提交该 identity，由服务端重新解析完整材料；页面不提供原始 UUID 手填入口。
- Draft 由服务端恢复；confirm 前按钮明确显示选中数量与 Template Revision，只有 confirm success 才进入 Snapshot/Run。
  409/stale 保留当前可见 Draft 并引导回查，不 optimistic 创建 Run。
- Template UI 只编辑服务端声明允许的字段；built-in 只读，自定义每次保存追加 Revision。旧 Run 始终展示冻结 Revision/hash。
- Topic review 展示大纲、Evidence 或 GAP；Merge review展示四类计数、Artifact、冲突、受控 Diff 和来源 Evidence。
  Evidence 复用 `SourceSpanViewer` 并绑定 Workspace/Source Version/Span。
- `human_task.review=null` 时任务仍可见，但批准、拒绝、目标路径和提交函数全部禁用。review decoder 拒绝未知 kind、字段、
  hash、计数、分类顺序或 identity 漂移；不能降级成普通盲批。
- 结果页区分 Artifact、Proposal、等待审批、失败和可恢复状态；创建 Proposal 不显示为文件/Git 已完成。
- Snapshot/Run 结果区必须完整展示 `templateHash`、每个 frozen material 的判别引用和全部 Evidence tuple；
  Smart Collection 还要展示 collection version、query hash、read-model revision，展开成员保留 `originCollectionId`。

### 4. Validation & Error Matrix

| Condition | Required result |
| --- | --- |
| 未知字段/枚举、非法 UUID/hash/time/version 或 union shape | `INVALID_RESPONSE`；不渲染部分卡片 |
| Material Evidence/Template/Snapshot/Run Workspace binding 漂移 | 拒绝整个响应并清晰报错 |
| 搜索结果携带冻结字段/Evidence、identity union 漂移或跨 Workspace | `INVALID_RESPONSE`；不允许加入 |
| Draft mutation 409 或 response loss | 保留服务端 Draft，原 key 重试/回查；不显示假成功 |
| Material stale | 回到可编辑 Draft 并要求重新建议/确认；不复用旧 Snapshot |
| required review 为 null、无效或 projector 503 | decision 控件和 API 提交禁用 |
| 长 path/hash/diff 在桌面或 `390x844` 溢出 | browser gate 失败 |

### 5. Good / Base / Bad Cases

- Good：输入主题后审阅理由与 Evidence，增删并显式选择材料，确认后刷新仍恢复同一 Run；大纲/合并审阅可打开来源。
- Base：没有建议时仍可手动补充；模型/向量 unavailable 时展示降级原因；报告/面试结果只进入 Artifact。
- Bad：把材料放进全局 store/localStorage、Suggest 后自动 confirm、组件自行解析 review、`review=null` 仍允许 POST decision，
  或创建 Proposal 后显示“已写入”。

### 6. Tests Required

- Decoder：四类 Material、identity-only 搜索 union、Template declaration、Draft/Snapshot/Run、全部状态、unknown/duplicate/mismatch/Problem/Abort。
- Query/Mutation：Workspace key、stable idempotency、refresh recovery、selection、confirm stale、Template Revision、Run polling/SSE invalidation。
- Workflow review：Topic Evidence/GAP XOR、Merge 固定分类与受控 Diff、Artifact binding、SourceSpanViewer、`review:null` 禁止提交。
- Component：normal/empty/loading/degraded/failure/conflict/recovery、键盘、焦点、长文本和 reduced motion。
- Canonical：ESLint、TypeScript、Vitest、production build、`git diff --check`；真实桌面和 `390x844` 浏览器无横向溢出、
  overlap、console warning/error 或失败请求。

### 7. Wrong vs Correct

```text
Wrong: Suggest mutation 成功后本地 setRun({status:'running'})。
Correct: Suggest 只更新服务端 Draft；confirm 成功并回查 Snapshot/Run 后才展示执行状态。

Wrong: review 为 null 时保留可点击“批准”，或由组件猜测 Outline/Merge 字段。
Correct: strict decoder 形成判别联合；null/invalid review 同时禁用 UI 和提交边界。

Wrong: Workspace A 的迟到 Draft/Run 响应写入 Workspace B 页面。
Correct: Query key、Abort 与 cache cleanup 全部绑定 Workspace 和资源 identity。
```
