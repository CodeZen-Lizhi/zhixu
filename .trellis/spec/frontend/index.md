# 前端开发规范

> ZHIXU 前端工作的统一入口。

## 当前状态

M1 已创建 React/Vite/TypeScript 前端骨架、Manifest、Lockfile 和可执行测试。本目录只记录稳定约束，具体行为以 `web/` 真实代码和 API 契约为准。
M6-D 已新增 `web/src/api/search.ts` 作为 Search wire 的严格 Decoder/Client 边界；Search 页面仍归 M9，
当前不得在组件中重复解析或用假页面冒充功能交付。
M7-01 已交付 `/graph` 的 Topic/Claim Global/Local/Path、严格 Graph client、会话布局、完整列表 fallback、
Evidence lazy drawer 与桌面/移动浏览器门禁；500,000 Relation/FPS 和正式认证仍归 M10。
M7-02 已在同一 `/graph` 页面增加独立 Semantic Link Candidate 面板、durable Topic scan、严格 decoder、
刷新恢复和完整决策 UX；Candidate 不进入正式 Graph canvas，正式认证仍归 M10。
M7-03 已交付 Collection/Knowledge Health strict decoder、Workspace-bound Query keys、三视图、Evidence/Decision/Scan
恢复、Vite 同源 proxy 和桌面/移动真实浏览器 smoke；cursor 仍是 opaque server state，SSE 只做 invalidation，Tag/
Review/Directory/Artifact owner 和最终容量/认证不在本任务范围。
M7 Timeline/Impact 已交付 `/timeline`、`/timeline/:eventId`、严格 `web/src/api/timeline.ts`、Workspace-bound Query、
URL filter/cursor、v1/v2 Event/Report、Artifact/Review owner binding，以及从当前报告创建正式 `downstream_update`
Proposal 的交互。批准仍只记录 decision；下游 Apply/executor 未交付，UI 不得显示可应用或已修改目标。
M9 已交付业务工作台、严格 Proposal 判别联合、Workspace-bound 详情和全站唯一 SSE Event Store；Apply Preflight 明确属于 Approval 后写回检查，Monaco DiffEditor 模型在内部 editor detach 后按 URI 安全释放。
M9 Export 已在 Collection 详情交付 `MARKDOWN|METADATA_JSON` Panel，并在 Settings 交付独立的 Workspace
`ATTACHMENTS_ZIP` Panel。`web/src/api/{exports,attachment-exports}.ts` 分别拥有严格 wire，Query 绑定 Workspace/scope，
刷新恢复、2 秒有界轮询、`export.*` SSE 定向失效、跨 Workspace Abort 和受控 Blob 下载均已验证。
AC-33 已由 Markdown、领域 Metadata JSON 与真实附件 ZIP 闭环关闭；`EVALUATION_JSON|AUDIT_JSON` 继续不公开。
M10-02 已交付严格认证 API 边界、`AuthProvider`/`AuthBoundary`、Session 恢复、CSRF 注入和登录/登出状态；
Bootstrap Token 只用于一次交换，浏览器持久化的唯一认证派生值是 CSRF Token，业务授权仍由后端裁决。
TODO 8 已将 189 个 OpenAPI operation 按 26 个领域 tag 生成 `typescript-fetch` client，并把现有
26 个生产 API 模块迁移到生成 Raw operation 与共享 Transport；Zod 严格拥有 Problem/Auth 边界，
其他模块继续由原 strict decoder 校验领域不变量。原生 EventSource 与 Answer Draft stream 是仅有的
专用协议 owner，生成/漂移/公共模型由 Makefile 与 CI 门禁锁定。
工作台导航与首页已重构为白蓝低噪声信息架构：未连接 `/dashboard` 是不发业务请求的知识脉络入口，已连接首页仅投影有界真实待办与最近捕获资料；导航归属由路由展示表统一派生，系统状态收敛至五类 Settings 的 `section=system`。
Quick Capture 已由 AppShell 单例提供文字、链接、文件和图片入口，`/inbox` 同时展示 Capture 与 Workspace Scan，
`/captures/:captureId` 分层恢复原始来源、处理状态与候选画像；严格 wire、焦点恢复和降级语义见 `capture-workbench.md`。
Authoring 已交付 `/authoring` 与 `/authoring/new`、服务端 Working Draft autosave、每次 Freeze 新 Revision、
安全 Markdown preview 和 Proposal publication 恢复；严格 wire 与 stale Revision 防护见 `authoring-workbench.md`。
Organizing 已交付 `/organizing` 的服务端 Draft 恢复、可解释材料增删/选择、四模板与受约束自定义模板、
Snapshot/Run/Artifact/Proposal 状态，以及 Topic/Merge Human Task Evidence/GAP/Diff 审阅；严格 wire 与盲批防护见
`organizing-workbench.md`。
Document History 已交付 `/authoring/documents/:documentId/history` 的 current-path 时间线、版本比较和受控恢复入口；
strict wire 保留 managed partial relations，Query/SSE 恢复绑定 Workspace/head/path/version/cursor，Proposal 创建不冒充已写回，
具体契约见 `document-history-workbench.md`。
Git Remote Sync 已交付 `/settings?section=workspace`（旧 `/settings?section=sync` 兼容归一化）、唯一 strict wire、Workspace-bound Query、write-only Secret action、
配置/测试/手动同步/重试和 Git/索引分列状态。未配置 `current_run: null`、网络重试 key、Secret 销毁、终态轮询停止及
桌面/390x844 无横向溢出门禁见 `git-sync-settings.md`。
前端已收敛为 Docker 单一业务模式：`GET /api/v1/workspaces/active` 是 Active Workspace 唯一事实源，所有业务 deep link
独立经过业务认证；宿主机目录选择和切换只由本机 `zhixu` 命令执行，浏览器不从 localStorage 恢复业务作用域。
M8-01 已交付 `/artifacts` 工作台、严格 Artifact decoder、Workspace-bound Query 与 generation 恢复；
Citation 可信性、Artifact 状态和 export/publication 绑定只来自服务端响应。
M8-02 已交付 `/review` 与 `/review/session`：Deck 管理、绑定 Workspace/Session/Deck 的仅问题 due 投影、服务端评分与
`scorer_version` 回执、同 key 重试、Workspace cache 与 `review.*` SSE 回查。due query key 必须包含 Session，避免同一
Deck 的并行会话复用另一会话的 `question_ref`。答题前绝不渲染答案要点或证据正文；key 轮换、不同 key 实例或 local
`disabled` 随机 key 的 API 重启使未提交 `question_ref` 失效时必须刷新 due，不能本地重签或复用旧题。显式/派生 key
在同 key 重启后保持有效。
Review-derived Learning Path 的客户端代码也已落在 `web/src/api/review.ts`、Review Query 与 `/review/session?answer=...`：
它只提交 Workspace/Answer/key，严格解码 `origin_type=REVIEW`、`LEARNING_PATH` Artifact 与 Workspace/Answer/Path/Step
binding，使用 `['review',workspaceId,'answers',answerId,'learning-path']` 精确缓存，并由 `learning_path.*` 事件定向失效。
后端已在数据库依赖可用时组装真实 Learning Path Service/Handler，并将 Review readiness 与两者的可用性共同绑定；
`00059/00060`、Repository 契约和 24 小时维护调用已用 fresh PostgreSQL、真实 API/Worker/Vite 及桌面/390x844
浏览器主链路动态验证。该 UI 仍须把实际依赖不可用时的 503 显式展示为可恢复错误；有业务数据的 Down guard、
Review Path reservation/hold 专门并发与 ABANDONED 重开仍需独立覆盖。
M8-03 已交付 `/interviews`、Interview-origin Learning Path 和 `/memories`：Interview/Path 的严格 decoder、逐题恢复、报告/步骤状态、
难度策略提示、从 `started_at + duration_minutes` 派生的倒计时、连续追问和 SKIPPED gap/path，以及 Memory Candidate/Confirm、
编辑、暂停/恢复/删除、到期与 cursor 恢复。认证身份不进入浏览器；Memory provenance 只从服务端响应只读展示，任何用户
命令都不能提交或修改 provenance。用户创建只能产生 USER candidate，INTERVIEW 来源仅由服务端受控命令产生。

## 已确认基线

- React + TypeScript，使用 Vite 构建。
- TanStack Query 管理 Server State，React Router 管理路由和 URL State。
- 使用 Generated/Typed API Boundary、Domain UI Model、Feature Module 和共享 SSE Event Store。
- SSE 通过 Last-Event-ID 单向更新；API 可查询状态才是事实源。
- 支持键盘、可见焦点、非纯颜色状态和 Diff 文本说明。
- Vitest、Testing Library 和 Playwright 是测试类型；M1 已锁定 Vitest/Testing Library，浏览器烟测通过内置 Browser skill 执行。

事实来源：`docs/architecture/application-contracts.md`、`docs/architecture/system-design.md`、`docs/architecture/quality.md`、`docs/requirements.md`。

## 规范索引

| 规范 | 职责 | 当前状态/后续门禁 |
| --- | --- | --- |
| [目录结构](./directory-structure.md) | Feature 边界和依赖方向 | 实际根目录、Alias、Public Export |
| [组件规范](./component-guidelines.md) | 组合、Props、UI 状态和可访问性 | 已记录白蓝低噪声 Dashboard、Active Workspace 启动/重连边界、M7-01 Graph 与 M9 Monaco DiffEditor 安全卸载契约 |
| [Hook 规范](./hook-guidelines.md) | Query、Command、URL 和 SSE Hook | Query Key Factory 和 Hook Test Harness |
| [状态管理](./state-management.md) | Server、URL、Local Draft 和 Event 所有权 | 已记录服务端 Active Workspace 单一 Owner、Workspace 清理顺序、M9 唯一 SSE Owner、Export recovery 与 M10 Auth/CSRF 契约 |
| [类型安全](./type-safety.md) | API/SSE 校验和 Domain UI Type | 已记录 Generated Raw/Transport/Zod 边界，以及 Timeline/Impact、Collection/Health、Business/Export/Auth 的严格解码 |
| [模型设置前端契约](./model-settings.md) | strict wire、desired/active/applied、双进程热应用、Session-only 与 Secret 生命周期 | 无重启 Save-and-Apply、恢复状态和桌面/移动真实容器门禁 |
| [Artifact 工作台契约](./artifact-workbench.md) | Artifact wire、Query、generation 恢复、GAP/export/publish UI 边界 | M8-01 decoder、组件、桌面/移动真实浏览器闭环已验证 |
| [快速记录工作台契约](./capture-workbench.md) | Capture/Profile strict wire、Dialog、Inbox、详情、焦点与恢复 | 真实 API/Worker 下 TEXT Capture、降级画像及桌面/移动交互已验证 |
| [主动创作工作台契约](./authoring-workbench.md) | Authoring wire、autosave、Freeze、preview 与发布恢复 | Decoder、Query、组件、lint/typecheck/build 与真实桌面/移动浏览器门禁已验证 |
| [材料整理工作台契约](./organizing-workbench.md) | Material/Template/Snapshot/Run strict wire、服务端 Draft、Human Task Evidence/GAP/Diff 与结果恢复 | Decoder、Query、组件、lint/typecheck/test/build 及父任务桌面/移动真实浏览器门禁已验证 |
| [文档文件历史工作台契约](./document-history-workbench.md) | History strict wire、Workspace/head/path/version/cursor Query、compare、restore preview/Proposal 与 SSE recovery | Decoder、Query/SSE、组件、Monaco、lint/typecheck/test/build 及父任务桌面/移动真实浏览器门禁已验证 |
| [Git 同步设置前端契约](./git-sync-settings.md) | strict wire、Workspace binding、Secret 生命周期、配置/状态/重试、轮询恢复与响应式边界 | Decoder、Query/Mutation、组件、lint/typecheck/test/build及真实 API/Worker 桌面/390x844 分叉冲突、有界预览与重试门禁已验证 |
| [质量规范](./quality-guidelines.md) | 测试、禁止模式和 Review Gate | 已记录生成漂移/单一路径门禁，以及 Timeline/Impact、Graph、Collection/Health、Review/Shared Path/Interview/Memory、Export 与认证浏览器门禁 |

## 开发前检查清单

1. 阅读本索引和目标层相关规范。
2. 阅读当前 Trellis Task 的 PRD、Design、Implement 和引用上下文。
3. 阅读 `docs/architecture/application-contracts.md` 及相关 API、安全、性能和产品章节。
4. 绘制 API/SSE → Boundary Decoder → Domain UI → Feature → Component 完整数据流。
5. 新建前先搜索已有 Feature、Projection、Query Key、Decoder、Component 或 Utility。
6. 确认 Server、URL、Draft 和 Event State 的权威 Owner。
7. 定义 Normal、Empty、Loading、Degraded、Failure、Conflict、Reconnect 和 Recovery 行为。
8. 实现前规划键盘、焦点、非纯颜色状态和 Screen Reader 行为。
9. 只使用项目 Manifest 和 Lockfile 证明的依赖版本。

## Canonical 边界

```text
Routes/Pages -> Feature Modules -> Domain UI Models
                              -> Query/Command Clients -> Typed API Client
                              -> SSE Event Store
Shared UI ----> Feature Modules
```

Feature 内部实现私有；Generated Wire Type 留在 API 边缘；SSE 只用于 Refetch/Invalidation，不是第二事实源。

Search 当前边界固定为：

```text
unknown HTTP JSON -> web/src/api/search.ts strict decoder -> SearchResponse -> future M9 feature/component
```

Decoder 必须保留 Workspace、Cursor、Index/Embedding Version、requested/effective mode、degradation、
Evidence href 与 vector `distance`；不得把 distance 重命名为 similarity 或静默丢弃未知 capability。

## 项目级禁止模式

- 虚构仓库不存在的库版本、源码示例或命令。
- Feature 间内部导入，或业务状态依赖组件库类型。
- 在 Component/Hook 中强转原始 API/SSE 数据。
- Silent Error、Fake Success、无边界数据加载或仅颜色表达状态。
- Browser Storage 保存 Secret 或受保护 Workflow 事实。

## M1 真实实现入口

- 前端入口与路由：[`web/src/main.tsx`](../../../web/src/main.tsx)、[`web/src/app`](../../../web/src/app)、[`web/src/routes`](../../../web/src/routes)。
- API 边界与状态页面：[`web/src/api`](../../../web/src/api)、[`web/src/features/system-status`](../../../web/src/features/system-status)。
- M1 Canonical Gate：`npm ci --prefix web`、`npm run lint --prefix web`、`npm run typecheck --prefix web`、`npm run test --prefix web`、`npm run build --prefix web`。

## 规范验证

持续执行：

```bash
rg -n 'React|TypeScript|TanStack Query|React Router|SSE' docs/architecture/application-contracts.md docs/architecture/system-design.md
git diff --check
```

后续业务模块必须继续维护锁定安装、Lint、Type Check、Test、Build、Generated Client Drift、Accessibility 和 Browser Smoke 的 Canonical 命令，并用实际代码引用更新本索引。
