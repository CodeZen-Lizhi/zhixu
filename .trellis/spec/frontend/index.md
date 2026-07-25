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
M9 已交付业务工作台、严格 Proposal 判别联合、Workspace-bound 详情和全站唯一 SSE Event Store；Apply Preflight 明确属于 Approval 后写回检查，Monaco DiffEditor 模型在内部 editor detach 后按 URI 安全释放。
M10-02 已交付严格认证 API 边界、`AuthProvider`/`AuthBoundary`、Session 恢复、CSRF 注入和登录/登出状态；
Bootstrap Token 只用于一次交换，浏览器持久化的唯一认证派生值是 CSRF Token，业务授权仍由后端裁决。

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
| [状态管理](./state-management.md) | Server、URL、Local Draft 和 Event 所有权 | 已记录 M7-03 Workspace cache、M9 唯一 SSE Owner，以及 M10 Auth/CSRF 状态和匿名时的 Query 清理契约 |
| [类型安全](./type-safety.md) | API/SSE 校验和 Domain UI Type | 已记录 M7-03 Collection/Health strict decoder、M9 Business strict decoder，以及 M10 Auth 响应/Problem 的严格解码 |
| [质量规范](./quality-guidelines.md) | 测试、禁止模式和 Review Gate | 已记录 M7-01 Graph、M7-02 Candidate、M7-03 Collection/Health 与 M10 认证浏览器门禁 |

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
