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
M7-04 只把 `knowledge_timeline` 加入 system-status strict capability decoder，并锁定未知/缺失字段 fail closed；
Timeline/Impact 页面、事件详情导航与版本比较 UI 仍未交付，不得用 system-status 展示冒充功能页面。
M9 已交付业务工作台、严格 Proposal 判别联合、Workspace-bound 详情和全站唯一 SSE Event Store；Apply Preflight 明确属于 Approval 后写回检查，Monaco DiffEditor 模型在内部 editor detach 后按 URI 安全释放。
M9-03 已在 Collection 详情交付异步 Export Panel、`web/src/api/exports.ts` 严格 decoder、Workspace/Collection-bound
Query、刷新恢复、2 秒有界轮询、`export.*` SSE 定向失效和受控 Blob 下载；正式范围仅为 `MARKDOWN` 与
`METADATA_JSON`。附件、`EVALUATION_JSON`、`AUDIT_JSON` 不在当前 UI/API 范围，不能将其或 AC-33 全量验收标为完成。
M10-02 已交付严格认证 API 边界、`AuthProvider`/`AuthBoundary`、Session 恢复、CSRF 注入和登录/登出状态；
Bootstrap Token 只用于一次交换，浏览器持久化的唯一认证派生值是 CSRF Token，业务授权仍由后端裁决。
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

事实来源：`docs/architecture/frontend-architecture.md`、`docs/architecture/api-and-events.md`、`docs/architecture/technology-stack.md`、`docs/architecture/testing-and-evaluation.md`、`docs/product/PRD.md`。

## 规范索引

| 规范 | 职责 | 当前状态/后续门禁 |
| --- | --- | --- |
| [目录结构](./directory-structure.md) | Feature 边界和依赖方向 | 实际根目录、Alias、Public Export |
| [组件规范](./component-guidelines.md) | 组合、Props、UI 状态和可访问性 | 已记录 M7-01 Graph 画布/列表、Evidence/移动 drawer，以及 M9 Monaco DiffEditor 安全卸载契约 |
| [Hook 规范](./hook-guidelines.md) | Query、Command、URL 和 SSE Hook | Query Key Factory 和 Hook Test Harness |
| [状态管理](./state-management.md) | Server、URL、Local Draft 和 Event 所有权 | 已记录 M7-03 Workspace cache、M9 唯一 SSE Owner、M9-03 Export polling/recovery，以及 M10 Auth/CSRF 状态和匿名时的 Query 清理契约 |
| [类型安全](./type-safety.md) | API/SSE 校验和 Domain UI Type | 已记录 M7-03 Collection/Health、M7-04 `knowledge_timeline` capability、M9 Business/M9-03 Export 与 M10 Auth 响应/Problem 的严格解码 |
| [Artifact 工作台契约](./artifact-workbench.md) | Artifact wire、Query、generation 恢复、GAP/export/publish UI 边界 | M8-01 decoder、组件、桌面/移动真实浏览器闭环已验证 |
| [质量规范](./quality-guidelines.md) | 测试、禁止模式和 Review Gate | 已记录 M7-01 Graph、M7-02 Candidate、M7-03 Collection/Health、M8 Review/Shared Path/Interview/Memory、M9-03 Export 与 M10 认证浏览器门禁 |

## 开发前检查清单

1. 阅读本索引和目标层相关规范。
2. 阅读当前 Trellis Task 的 PRD、Design、Implement 和引用上下文。
3. 阅读 `docs/architecture/frontend-architecture.md` 及相关 API、安全、性能和产品章节。
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
rg -n 'React|TypeScript|TanStack Query|React Router|SSE' docs/architecture/frontend-architecture.md docs/architecture/technology-stack.md
git diff --check
```

后续业务模块必须继续维护锁定安装、Lint、Type Check、Test、Build、Generated Client Drift、Accessibility 和 Browser Smoke 的 Canonical 命令，并用实际代码引用更新本索引。
