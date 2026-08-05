# Research: frontend existing patterns

- Query: 调研现有 Web 路由/导航/工作台、TanStack Query、严格 API decoder、Monaco、Markdown 安全渲染、响应式原语与 `/authoring` 现状；给出精确影响文件、复用模式、风险和测试目标。
- Scope: mixed
- Date: 2026-08-03
- Snapshot: 研究期间共享工作区有并行实现落盘；本文把“原有模式”和“当前在途 Authoring 文件”分开标注。

## Findings

### 结论

1. 原有前端没有 `/authoring` 路由、页面或占位组件；未知路径会被通配路由重定向到 `/dashboard`。研究期间新增了在途 `web/src/api/authoring.ts`，但尚未出现 `features/authoring/`、路由或页面。
2. Monaco 已是直接依赖且已有本地 worker、稳定 model URI、detached model 清理模式；应抽成 shared owner 后同时供 Diff Viewer 和 Authoring Editor 使用，无需新增编辑器依赖。
3. 当前 manifest 已直接包含 `react-markdown@10.1.0` 与 `remark-gfm@4.0.1`，源码尚未使用。它们可以在不使用 `dangerouslySetInnerHTML`、不启用 `rehype-raw` 的情况下完成预览；必须显式 `skipHtml` 并覆盖 `urlTransform`，因为默认协议集合宽于任务允许的 `http/https/mailto`。
4. Authoring server state 应沿用 Workspace-scoped Query family、SSE 只失效 Query、Workspace 切换清缓存；编辑缓冲是独立的组件内存状态，不能被 Query refetch 或 SSE 静默覆盖。
5. 自动保存没有现成实现。需要建立“已确认服务端版本 + 未确认本地缓冲 + 串行发送队列”三层状态，500-1000ms debounce、CAS、稳定幂等身份、409 后停止自动保存并保留本地内容。
6. 两个契约决定仍不明确：草稿恢复所需的 URL 身份，以及 Authoring 在一级导航中是替换“产出”还是加入现有“产出”分组。实现前必须冻结，否则刷新恢复或导航 spec 会漂移。

### Files Found

| 文件 | 现有职责 / 证据 |
| --- | --- |
| `web/src/routes/AppRoutes.tsx:5` | Feature 页面逐个 `lazy`；路由集中在 Shell 下组合，当前无 Authoring，通配在 `:65` 回 Dashboard。 |
| `web/src/routes/route-display.ts:39` | 导航、面包屑、route section 的唯一 registry；最长前缀匹配在 `:61-70`。当前一级为工作台/知识/产出/设置。 |
| `web/src/app/AppShell.tsx:92` | 从 registry 派生菜单；section active、未连接可见性和移动导航在 `:135-174`，Quick Capture owner 在 `:192-220`、`:277-305`。 |
| `web/src/features/business/DashboardPage.tsx:185` | 已连接工作台读取真实 Workspace/Proposal/Workflow/Source 数据；当前没有固定 Authoring 动作带。 |
| `web/src/features/artifacts/query-keys.ts:1` | 标准 Query key factory：root/list/detail 均带 Workspace 和资源身份。 |
| `web/src/features/artifacts/queries.ts:62` | Query 透传 `AbortSignal`、用 `enabled` 门禁；mutation 成功后 set/invalidate 精确 Query。 |
| `web/src/app/workspace-runtime-state.ts:3` | Workspace 切换集中 cancel/remove Query root；当前没有 `authoring`。 |
| `web/src/events/event-store.tsx:79` | 重连恢复逐 Query family refetch；`:141-153` 去重失效，`:155-252` 只由事件分类失效 Query。当前没有 Authoring。 |
| `web/src/api/captures.ts:246` | 最完整的 strict transport 模式：required/optional exact keys、UTF-8 byte bound、UUID/RFC3339/version；`:735-771` 校验媒体类型、strict JSON、成功状态和 Problem。 |
| `web/src/api/exports.ts:120` | `StrictJsonParser` 检测 trailing data、重复字段和非有限数；`:271` 导出 `strictJson`，现被多个 API client 复用。 |
| `web/src/api/artifacts.ts:232` | 可复用 Workspace/Artifact binding 和命令语义；但 `:239-246` 使用 `response.json()` 和宽松 Problem，不足以满足 Authoring duplicate-key 契约。 |
| `web/src/api/authoring.ts:1` | 研究期间出现的在途唯一 wire owner；已有 exact decode、Workspace/resource binding、expected version 和幂等输入。`:416-432` 尚未校验响应 Content-Type/精确成功状态。 |
| `web/src/features/business/MonacoDiffViewer.tsx:22` | 本地 loader/worker 初始化；`:33-43` 仅释放 detached model；`:66-79` 使用持久 model path、`automaticLayout`。 |
| `web/src/features/business/MonacoDiffViewer.test.tsx:37` | 已覆盖本地 worker/model path、卸载释放和快速同 URI 重挂载不误释放。 |
| `web/src/shared/ui.tsx:12` | 现有 Button/Badge/Card/PageHeader/Empty/Error/Unavailable、Radix Dialog/Sheet/Tabs/Tooltip；Dialog/Sheet 已处理焦点恢复。 |
| `web/src/styles.css:100` | Workbench 使用 `minmax(0, 1fr)` 和受控 content gutter；`:480-481`、`:1265-1290` 有 920/720/620 响应式门槛。 |
| `web/src/features/capture/capture.css` | Feature-local CSS 已有先例；Authoring 样式不必继续堆入全局文件。 |
| `web/src/api/system-status.ts:145` | `system/status` 根对象 exact-key；后端若增加 `authoring` capability 而前端未同步，整个响应会 `INVALID_RESPONSE`。 |
| `web/package.json:21` | Monaco、TanStack Query、`react-markdown`、`remark-gfm` 都是直接依赖；当前无需再改 manifest。 |
| `web/package-lock.json:4449` | `marked@14.0.0`、`dompurify@3.4.8` 仅是 `monaco-editor@0.56.0` 的传递依赖，不应作为应用预览的隐式 API。 |

### Routing, Navigation, And Workbench

- 增加 `AuthoringPage` / `NewDocumentPage` 时沿用 route-level lazy import，不在 `AppRoutes` 承载创建草稿或解码逻辑（`AppRoutes.tsx:5-29,33-65`；`.trellis/spec/frontend/directory-structure.md:35-43`）。
- `/authoring` 和所有草稿编辑 deep link 必须进入 `routeDisplayRegistry`；AppShell 会自动得到面包屑、section active、桌面 dropdown 和移动 submenu（`route-display.ts:39-70`；`AppShell.tsx:92-174`）。不要在 AppShell 再手写一份 Authoring 菜单。
- 当前 Quick Capture dialog 完全由 AppShell local state 拥有（`AppShell.tsx:192-220,298-305`），Dashboard 无法直接调用。首页“快速记录”要复用同一 owner，需增加明确的 capture action bridge/context；不能再挂第二个 Dialog 或用 DOM/custom event 绕过 owner。
- `/authoring/new` 本身没有 Draft ID，但 PRD 要求刷新/重新进入恢复。创建成功后必须把 Draft identity 写入可刷新 URL，例如受严格 UUID 校验的 `?draft=<id>`，或冻结一条新的 `/authoring/drafts/:draftId` 契约。仅放 React state、navigation state 或 Browser Storage 都不满足恢复要求。
- 进入无身份的 `/authoring/new` 时创建空 Draft；React StrictMode/remount/response loss 不能重复创建。创建意图必须在组件生命周期内保留同一个 Idempotency-Key，成功后 `replace` 到带 Draft identity 的 URL。
- 当前 frontend spec 仍明确一级导航固定为“工作台 / 知识 / 产出 / 设置”（`.trellis/spec/frontend/component-guidelines.md:183-188`）。任务 PRD 只说“接入导航”（`prd.md:11`），未说明替换还是归组；若上游 IA 要求一级“创作”，需同步更新该 spec，不能只改 UI。

### TanStack Query, Autosave, And SSE

建议 Query key 层级：

```text
["authoring", workspaceId]
["authoring", workspaceId, "overview"]
["authoring", workspaceId, "working-draft", draftId]
["authoring", workspaceId, "document", documentId]
```

- Query key 的所有 queryFn 变量都进入 key；queryFn 透传 TanStack 的 `signal`，Workspace/Draft 为空时 `enabled: false`，不自行 retry（现有例：`artifacts/queries.ts:62-79`；全局默认 `query-client.ts:3-10`）。
- Mutation 成功后可以 `setQueryData` 精确确认 Draft/Document，再 invalidate overview；不得 optimistic 宣称 freeze/publish 成功（`artifacts/queries.ts:82-107`；`.trellis/spec/frontend/state-management.md:32-39`）。
- 自动保存必须串行：debounce 后冻结一份 `{expectedVersion,title,targetPath,body,idempotencyKey}`；同一请求未知结果时原样重试；请求在途时只合并“最新待发缓冲”；成功后以返回 version 作为下一次 expectedVersion。不能并发 PUT，也不能让较旧响应覆盖较新输入。
- 409 时暂停队列，保留本地 buffer，展示服务端版本重新加载动作；SSE/refetch 得到新 Draft 时只更新“已确认服务端快照”，dirty buffer 仍由本地 owner 保留。`ModelSettingsPanel.tsx:262-379` 已有“远端 revision 变化但保留 dirty draft”的相邻模式。
- response-loss 重试复用同一个 variables/object 和 Idempotency-Key；新内容/expected version 才是新意图。现有证据是 `QuickCaptureDialog.tsx:96-170,242-264` 与 `QuickCaptureDialog.test.tsx:102-119`，Timeline 也直接重放 mutation variables（`TimelinePage.test.tsx:294-311`）。
- 把 `authoring` 加入 `workspaceQueryRoots`，保证切 Workspace 时 cancel/remove（`workspace-runtime-state.ts:3-34`）。
- Event Store 的全量恢复增加 Authoring refetch；`authoring.*`、working draft/document/publication resource 失效 Authoring root。`proposal.applied`/finalizer 完成也应失效 Authoring overview/document。SSE payload 只作为失效信号，不能直接修改 editor buffer（`event-store.tsx:79-153,155-252`）。
- 不把 Draft/Revision/Proposal/body 写入 localStorage/sessionStorage；当前唯一业务持久化例外是 active Workspace ID（`active-workspace.ts:5-20,40-51`）。

### Strict API Decoder

- `web/src/api/authoring.ts` 保持唯一 snake_case wire owner；Feature 只接收已归一化 camelCase model，符合 `.trellis/spec/frontend/type-safety.md:26-37`。
- 复用 `strictJson`，不要复制 parser，也不要照搬 Artifact 的 `response.json()`。`strictJson` 能在 `JSON.parse` 已无法发现重复字段之后仍 fail closed（`exports.ts:120-146`）。
- 以 Capture transport 为基线：响应必须是 `application/json`，成功 status 必须属于端点契约，Problem 也 exact decode；AbortError 原样传播，网络失败才映射 retryable（`captures.ts:730-771`）。
- `authFetch` 已统一设置 `Accept`，对 JSON body 自动设置 `Content-Type` 和 CSRF（`auth.ts:244-266`），Authoring client 无需重复这部分。
- 所有响应检查 Workspace + Draft/Document/Revision/Proposal binding；列表检查重复 ID、状态不变量和数量边界。所有输入按 UTF-8 bytes 校验，不能只用 JS `length`。
- 当前在途 `authoring.ts:416-432` 已 strict parse，但未核对响应媒体类型或端点成功 status；相关 API 测试应先钉住，避免 2xx 错页/代理 HTML 被当成领域成功。
- `strictJson` 目前由 feature-named `exports.ts` 导出，通用 Problem 又由 `conversation.ts` 导出；这是所有权债务。最小交付可继续复用并用测试锁住，若抽取则应一次迁到 `api/strict-json.ts` / `api/problem.ts`，不能只为 Authoring 再复制一份。

### Monaco Editor

- 把 `configureLocalMonaco`、worker owner 和 detached model release helper 从 `MonacoDiffViewer.tsx:22-43` 抽到 `web/src/shared/monaco.ts`；Diff Viewer 与 Markdown Editor 都只调用 shared owner，避免重复覆盖 `globalThis.MonacoEnvironment`。
- Editor `path`/model URI 必须包含 Workspace + Draft，例如 `inmemory://zhixu/workspaces/{workspaceId}/authoring/working-drafts/{draftId}.md`。身份变化时切 model，不复用跨 Workspace URI。
- 使用 `keepCurrentModel` 保存 undo/selection/view state，同时在 editor dispose 后 queue microtask，仅释放仍 detached 的 model。必须保留现有快速同 URI 重挂载保护（`MonacoDiffViewer.test.tsx:68-114`）。
- 使用受控高度或 `clamp()` + `min-height`，`automaticLayout: true`，container 与 grid child 均 `min-width: 0`；不能让 loading text 或工具条改变编辑区尺寸。

### Markdown Rendering And XSS

- 当前最合适的 owner 是小型 `MarkdownPreview`：`<ReactMarkdown skipHtml remarkPlugins={[remarkGfm]} urlTransform={controlledUrl}>`。不要使用 `rehype-raw`，不要使用 `dangerouslySetInnerHTML`。
- `react-markdown` 默认由 AST 生成 React elements，原始 HTML 默认不执行；显式 `skipHtml` 把任务的“拒绝原始 HTML”写成可测试配置。
- 必须覆盖 `urlTransform`：官方默认还允许 `irc/ircs/xmpp` 和相对 URL，宽于当前任务。建议 `href` 仅允许完整 `http:`, `https:`, `mailto:`；`src` 仅允许 `http:`/`https:`，或 MVP 直接禁用图片。fragment/相对 URL 是否允许需要产品契约明确，不能继承库默认。
- `remark-gfm` 会增加 autolink/table/task-list，但 URL 最终仍须经过同一 transform。外链若新窗口打开，组件必须固定 `rel="noreferrer noopener"`；更简单的是保持同窗口。
- `marked`/`dompurify` 是 Monaco 的传递依赖，不是应用直接依赖，也没有源码使用；不要导入它们或依赖其传递版本。当前 React tree pipeline 不需要额外 sanitizer。

### Responsive And UI Primitives

- 页面复用 `PageHeader`、`Button`、`Badge`、`EmptyState`、`ErrorState`、`UnavailableState` 和 Radix `Tabs`（`shared/ui.tsx:12-31`）。保存中/已保存/冲突/待恢复必须有文字与 live region，不能只靠颜色。
- Desktop 可用 `grid-template-columns: minmax(0, 1fr) minmax(0, 1fr)` 展示编辑/预览；390px 建议切换为 Editor/Preview Tabs，而不是同时压缩两列。切 Tab 不能卸载并丢失 Monaco model/focus。
- 新建 `features/authoring/authoring.css`，沿用现有 feature-local CSS 先例；Dashboard 快捷动作仍在现有全局 Dashboard 样式 owner 中调整。
- 浏览器验证沿用现有 smoke helper：桌面 1440x900、移动 390x844，断言 document/workbench/editor scrollWidth、无 console/pageerror、键盘焦点、关闭菜单/Dialog 后焦点恢复。

### Exact Frontend Impact

新增或当前在途：

- `web/src/api/authoring.ts`（已在途）、`web/src/api/authoring.test.ts`
- `web/src/features/authoring/query-keys.ts`
- `web/src/features/authoring/queries.ts`、`queries.test.tsx`
- `web/src/features/authoring/AuthoringPage.tsx`、`AuthoringPage.test.tsx`
- `web/src/features/authoring/NewDocumentPage.tsx`、`NewDocumentPage.test.tsx`
- `web/src/features/authoring/MarkdownEditor.tsx`、`MarkdownEditor.test.tsx`
- `web/src/features/authoring/MarkdownPreview.tsx`、`MarkdownPreview.test.tsx`
- `web/src/features/authoring/authoring.css`
- `web/src/shared/monaco.ts`、`web/src/shared/monaco.test.ts`
- `web/e2e/authoring.smoke.spec.ts`

修改：

- `web/src/routes/AppRoutes.tsx`、`AppRoutes.test.tsx`
- `web/src/routes/route-display.ts`
- `web/src/app/AppShell.tsx`、`AppShell.test.tsx`
- `web/src/features/business/DashboardPage.tsx`、`DashboardPage.test.tsx`
- `web/src/app/workspace-runtime-state.ts`、`workspace-runtime-state.test.ts`
- `web/src/events/event-store.tsx`、`event-store.test.tsx`
- `web/src/features/business/MonacoDiffViewer.tsx`、`MonacoDiffViewer.test.tsx`
- `web/src/styles.css`（只放 Dashboard action-band 现有 owner 的样式）
- `web/src/api/system-status.ts`、`system-status.test.ts`，以及 `features/system-status/SystemStatusPage.tsx` / test：仅当后端 Capability contract 增加必填 `authoring` 字段时，但按实施计划第 6 步和现有 capability 模式，这一修改高度可能。

无需修改或仅回归：

- `web/package.json`、`web/package-lock.json` 当前已直接锁定 `react-markdown` / `remark-gfm`，无需再加 Markdown 依赖。
- `web/src/features/business/ProposalsPage.tsx` 现有 `file_patch` detail 可复用；除非新 publication metadata 要展示，否则只跑回归测试，不扩展 Proposal UI。

### Test Targets

| 层级 | 必测行为 |
| --- | --- |
| API unit | duplicate/escaped duplicate JSON key、unknown/missing field、UUID/RFC3339/version/byte bound、Workspace/资源 binding、Content-Type、精确成功 status、strict Problem、Abort/network、全部命令单一 Idempotency-Key。 |
| Query unit | key 包含 Workspace + Draft/Document；success set/invalidate；workspace switch cancel/remove；SSE reconnect/authoring/proposal-finalizer 仅失效正确 root。 |
| Create flow | 无身份 `/authoring/new` 只创建一次；StrictMode/response-loss 重放同 key；成功 replace 到带 Draft identity URL；刷新 GET 同一 Draft。 |
| Autosave | 500-1000ms debounce、快速输入 coalesce、单飞串行、成功推进 version、未知结果复用原 variables/key、409 保留本地 buffer 并停止队列、SSE/refetch 不覆盖 dirty buffer。 |
| Freeze/publish | 空标题/路径/body 或非法 path 不提交；只有显式 freeze 产生 Revision；publish 使用已冻结身份；Proposal 创建后文案不宣称 PUBLISHED；成功进入真实 `/proposals/:id`。 |
| Monaco | 本地 worker、Workspace/Draft URI、undo/focus 稳定、detached dispose、同 URI 快速重挂载不 dispose attached model。 |
| Markdown security | `<script>`、raw HTML、事件属性、`javascript:`、`vbscript:`、`data:`、大小写/编码/空白变体、恶意 image/link、GFM autolink；全部不产生可执行节点或危险 URL。 |
| Component/routes | overview loading/empty/error/organizing-disabled/pending/recovery/completed；导航 desktop/mobile/active/breadcrumb；未连接 Workspace 不发业务请求；Quick Capture 仍只有一个 owner。 |
| Browser | 真实 API 下桌面与 390x844 创建、编辑、预览、刷新恢复、冲突、freeze、publish、返回焦点；无横向溢出、console error、pageerror 或 Monaco worker/CDN 请求。 |

### External References

- [react-markdown official README](https://github.com/remarkjs/react-markdown): v10 默认不使用 `dangerouslySetInnerHTML`，支持 `skipHtml`、`urlTransform`、`allowedElements` 和自定义 components；默认 URL 协议比本任务更宽。
- [remark-gfm official README](https://github.com/remarkjs/remark-gfm): GFM autolink、table、task list、strikethrough 插件；本地锁定 4.0.1。
- [@monaco-editor/react official README](https://github.com/suren-atoyan/monaco-react): Vite 本地 worker 配置、model `path` 身份、`keepCurrentModel` 与 view state 行为。
- [Monaco Editor official repository](https://github.com/microsoft/monaco-editor): model URI 影响 provider；`model.dispose()` 释放 URI，editor/model 都需要明确清理。
- [TanStack Query v5 query keys](https://tanstack.com/query/v5/docs/framework/react/guides/query-keys), [query cancellation](https://tanstack.com/query/v5/docs/framework/react/guides/query-cancellation), [mutation invalidation](https://tanstack.com/query/v5/docs/framework/react/guides/invalidations-from-mutations): key 包含 queryFn 变量、消费 AbortSignal、成功后精确失效。

### Related Specs

- `.trellis/tasks/08-02-document-draft-authoring/prd.md:17-21,40-52,54-66`
- `.trellis/tasks/08-02-document-draft-authoring/design.md:47-57,81-103`
- `.trellis/spec/frontend/directory-structure.md:20-43`
- `.trellis/spec/frontend/type-safety.md:20-37`
- `.trellis/spec/frontend/state-management.md:20-55`
- `.trellis/spec/frontend/component-guidelines.md:16-55,181-200`
- `.trellis/spec/frontend/quality-guidelines.md:18-48`
- `.trellis/spec/frontend/artifact-workbench.md:28-47`

## Caveats / Not Found

- 共享工作区在研究期间变化：`web/src/api/authoring.ts` 与 Markdown direct dependencies 是当前快照中的在途内容，不应被误认为任务开始前已有模式；实现者需在合并前重新读取它们。
- 没有找到现成 autosave、debounce queue、route blocker 或 Authoring editor lifecycle；这些是本任务新增行为，必须靠定向测试建立契约。
- 没有找到现有 Markdown renderer、sanitizer 或 `dangerouslySetInnerHTML` 用法；当前 direct dependencies 尚未被源码消费。
- PRD 没有冻结 Draft identity 的 URL 形状，也没有明确 Authoring 与现有“产出”一级导航的关系。这两点会实质改变路由、恢复和 AppShell 测试，不能静默猜测。
- 后端是否把 `authoring` 加入 `/api/v1/system/status` 最终 schema 尚未从已实现 OpenAPI 得到确认；若增加，前端 exact decoder、设置页和认证初始化必须同一变更同步。
- 未运行构建/测试；本工作是只读研究，唯一写入是本研究文件。
