# 工作台 UX 优化设计

## Objective

将 Workspace 首次使用页从展示型首页调整为任务优先的连接引导，同时保留当前编辑部式视觉基调和真实运行状态边界。

## Boundaries

| Area | Change | Contract impact |
| --- | --- | --- |
| App shell | 分组导航、未连接状态、面包屑修正 | 仅呈现；URL 和路由不变 |
| Workspace page | 首屏表单前置、紧凑状态摘要、扫描后的下一步 CTA | 复用既有 API hook 与 local storage owner |
| System status | 新增紧凑展示模式 | 复用严格 decoder 和同一 Query；完整模式保留 |
| CSS | 局部 token 与响应式布局调整 | 不引入新依赖 |
| Tests | 单测和 Playwright CLI 验收 | 不创建真实 Workspace 数据 |

## Information Architecture

### Navigation

导航从单一 17 项列表调整为任务分组：

1. 工作台：Dashboard。
2. 资料与知识：Inbox、资料、检索、图谱、集合、健康、时间线。
3. 审阅与产出：提案、流程、产物。
4. 学习与研究：复习、记忆、访谈、对话，默认可折叠。
5. 系统：工作区、设置。

未连接 Workspace 时只呈现工作台和系统组，避免用户反复进入没有上下文的 gate 页面。任何既有 direct URL 仍由原页面的 gate 处理，保持 deep link 兼容。

## Workspace Flow

```text
未连接
  -> 创建或打开 Workspace
  -> 已连接摘要
  -> 扫描资料 或 进入工作台
  -> 扫描结果
  -> 查看资料版本 或 进入工作台
```

- 首屏由短标题、输入表单和紧凑运行摘要组成；完整运行诊断留在 Dashboard/Settings。
- 创建和打开仍只更新现有 Workspace owner；不自动触发扫描，避免将现有用户显式操作变成隐式副作用。
- 扫描完成后，事实表格不变，只增加无副作用的导航入口。

## Status Semantics

- `Workspace` 未选择、SSE 同步未连接、API/数据库运行状态是三个不同维度。
- 当未选择 Workspace 时，侧栏文案使用“等待连接 Workspace”，而不使用“离线”。
- 紧凑模式显示服务总体状态和最多四项关键事实；RAG disabled 明确描述为可选能力已关闭。
- 全量模式保留 14 项诊断网格，供 Dashboard 和 Settings 使用。

## Responsive Design

- 桌面 `1440x900`：主表单按钮在视口内，导航分组不将低频入口挤入首屏。
- 移动 `390x844`：品牌栏、上下文栏和 Workspace 主操作不重叠；Sheet 使用同一分组，且无横向溢出。
- 所有按钮保持现有 `44px` 最小触控尺寸和可见 focus ring。

## Compatibility and Rollback

- 不改变 API endpoint、Query key、storage key、路由或服务端状态机。
- 回滚只需恢复 AppShell、WorkspacePage、SystemStatusPage、相应样式与测试改动；不会留下数据或迁移。
