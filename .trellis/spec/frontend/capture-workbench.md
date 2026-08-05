# 快速记录工作台契约

## Scenario: Quick Capture, Inbox And Profile Recovery

### 1. Scope / Trigger

- 修改 `web/src/api/captures.ts`、`web/src/features/capture`、AppShell 快速记录入口、Inbox、Capture 详情、
  System Status decoder 或 Capture 浏览器 smoke 时应用。
- HTTP JSON、Active Workspace、路由 Capture/Source Version identity 和服务端阶段状态必须在 API/Query 边界绑定成 UI model。

### 2. Signatures

```ts
createCapture(workspaceId, input)
listCaptures(workspaceId, params?, signal?)
getCapture(workspaceId, captureId, signal?)
retryCapture(workspaceId, input)
getKnowledgeProfile(workspaceId, binding, signal?)
retryKnowledgeProfile(workspaceId, input)
```

- 全局 Dialog 由 AppShell 单例挂载；入口按钮在业务页可见，快捷键固定为 `Ctrl|Meta + Shift + K`。
- 路由为 `/inbox` 与 `/captures/:captureId`；所有 Capture Query key 必须包含 Workspace。

### 3. Contracts

- `web/src/api/captures.ts` 是 Capture/Profile wire 唯一 owner。字段集合、UUID、SHA-256、RFC3339、正整数、枚举、href、
  Workspace/Capture/Source Version/Profile/Revision/Evidence binding 全部运行时严格校验；Feature 不读 raw snake_case 或 cast。
- TEXT/URL 使用 JSON endpoint，FILE/IMAGE 使用 multipart endpoint；浏览器不手写 multipart `Content-Type`。相同用户动作及
  response-loss retry 保持 idempotency key，改变 kind、内容、Workspace、target 或 expected version 必须产生新 command identity。
- Dialog 支持文字、链接、文件、图片四个 tab；打开后焦点进入当前主要输入，Escape/关闭/成功保存后回到触发按钮或原活动元素。
  草稿只在 Dialog 内存中保存，关闭清空，不写 Local Storage。
- 成功创建只显示“已存入收件箱”，不宣称 Profile 或正式知识完成。mutation 后失效 Capture list/detail 及相关 Source list；
  409/503 保留明确冲突或能力状态，不 optimistic 推进服务端阶段。
- Inbox 的 Quick Capture 区与既有 Workspace Scan 区都只显示服务端事实；URL 未 materialize 或抓取失败仍必须显示 Capture。
  筛选绑定服务端 kind/status，空、加载、失败、降级分离表达。
- Capture 详情分别展示 immutable source、Fetch/Ingestion/Index/Profile 阶段和 Profile candidate projection。Profile 候选必须
  明确标为非正式；Source Span href 只能来自严格解码后的响应。
- `READY_DEGRADED`、`CAPABILITY_UNAVAILABLE` 和 `STALE` 不是空态。STALE/失败重试期间仍展示上一版 immutable Profile Revision；
  页面刷新只回查 REST，SSE 仅做 Query invalidation。
- `decodeSystemStatus` 必须把 OpenAPI 必填的 `capture` 字段纳入 exact-key 与状态校验；不能为接入新能力放宽根对象未知字段检查。

### 4. Validation & Error Matrix

| Condition | Required result |
| --- | --- |
| Capture/Profile/System Status 未知字段或非法绑定/枚举/hash/time | `INVALID_RESPONSE`，不渲染部分事实 |
| 无 Active Workspace | 不挂载可写 Quick Capture，不发 Workspace-bound Capture 请求 |
| mutation 409 | 显示冲突并回查服务端；不显示已保存/已重试 |
| Profile capability unavailable | 展示基础资料和已完成索引，明确没有候选画像 |
| URL fetch failed | Inbox/详情保留原 URL、失败阶段、稳定 error code 和合法重试入口 |
| Dialog 关闭或成功 | 返回原页面，焦点恢复；无残留 overlay/草稿 |
| desktop/mobile console error、文本或 document 横向溢出 | browser gate 失败 |

### 5. Good / Base / Bad Cases

- Good：任意业务页打开 Dialog，保存 TEXT 后焦点返回入口；Inbox 随后台阶段刷新，详情可打开 Source 与 Evidence。
- Base：模型 disabled 时记录显示“降级可用”，解析和 Keyword Index 完成，画像区明确 capability unavailable。
- Bad：组件自己拼 Capture 状态、把 SSE payload 当事实源、成功保存后跳离用户原上下文、把 Profile candidate 标成已确认 Topic。

### 6. Tests Required

- Decoder：字段精确性、全部状态、Workspace/资源/href 绑定、Profile Revision/Evidence、Problem/Abort、System Status capture。
- Query/Mutation：Workspace key、invalidation、stable idempotency、retry expected version、response-loss、SSE invalidation。
- Component：四类输入、文件拖放/粘贴、键盘提交、Escape/焦点恢复、loading/error/conflict/degraded/stale、Inbox/详情。
- Canonical：ESLint、TypeScript、全部 Vitest、production build 和 `git diff --check`。
- Browser：真实 API/Worker/Vite，在 `1440x900` 与 `390x844` 创建记录并打开 Inbox/详情；断言无横向 overflow、
  console 零 error/warning、按钮文字不截断且 Dialog 不遮挡关键操作。

### 7. Wrong vs Correct

```text
Wrong: POST 201 后显示“画像已生成”或把本地对象直接插入 Query cache 当最终状态。
Correct: 只确认 Capture 已持久化；随后从 REST 恢复每个独立阶段和 Profile 状态。

Wrong: 为接受后端新增 capture 能力而允许 System Status 任意未知字段。
Correct: 将 capture 加入 exact schema 并严格校验其 status，其他未知字段继续 fail closed。
```
