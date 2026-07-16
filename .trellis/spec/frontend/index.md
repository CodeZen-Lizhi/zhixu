# 前端开发规范

> ZHIXU 前端工作的统一入口。

## 当前状态

仓库当前只有产品和架构文档，没有 React 源码、前端 Manifest、Lockfile 或可执行前端测试。本目录只记录 `docs/` 已确认的设计约束，不虚构代码示例或依赖版本。M1 创建前端骨架后，必须用真实代码链接替换规划示例。

## 已确认基线

- React + TypeScript，使用 Vite 构建。
- TanStack Query 管理 Server State，React Router 管理路由和 URL State。
- 使用 Generated/Typed API Boundary、Domain UI Model、Feature Module 和共享 SSE Event Store。
- SSE 通过 Last-Event-ID 单向更新；API 可查询状态才是事实源。
- 支持键盘、可见焦点、非纯颜色状态和 Diff 文本说明。
- Vitest、Testing Library 和 Playwright 是预期测试类型；版本和确切命令尚未锁定。

事实来源：`docs/architecture/frontend-architecture.md`、`docs/architecture/api-and-events.md`、`docs/architecture/technology-stack.md`、`docs/architecture/testing-and-evaluation.md`、`docs/product/PRD.md`。

## 规范索引

| 规范 | 职责 | M1 待验证 |
| --- | --- | --- |
| [目录结构](./directory-structure.md) | Feature 边界和依赖方向 | 实际根目录、Alias、Public Export |
| [组件规范](./component-guidelines.md) | 组合、Props、UI 状态和可访问性 | UI/样式方案和代表性组件 |
| [Hook 规范](./hook-guidelines.md) | Query、Command、URL 和 SSE Hook | Query Key Factory 和 Hook Test Harness |
| [状态管理](./state-management.md) | Server、URL、Local Draft 和 Event 所有权 | Cache Default 和持久化策略 |
| [类型安全](./type-safety.md) | API/SSE 校验和 Domain UI Type | Compiler、Generator、Runtime Validator |
| [质量规范](./quality-guidelines.md) | 测试、禁止模式和 Review Gate | 确切命令、工具、Coverage 和 Budget |

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

## 项目级禁止模式

- 虚构仓库不存在的库版本、源码示例或命令。
- Feature 间内部导入，或业务状态依赖组件库类型。
- 在 Component/Hook 中强转原始 API/SSE 数据。
- Silent Error、Fake Success、无边界数据加载或仅颜色表达状态。
- Browser Storage 保存 Secret 或受保护 Workflow 事实。

## 规范验证

M1 创建可执行前端 Gate 前执行：

```bash
rg -n 'React|TypeScript|TanStack Query|React Router|SSE' docs/architecture/frontend-architecture.md docs/architecture/technology-stack.md
git diff --check
```

M1 后必须定义锁定安装、Lint、Type Check、Test、Build、Generated Client Drift、Accessibility 和 Browser Smoke 的 Canonical 命令，并用实际代码引用更新本索引。
