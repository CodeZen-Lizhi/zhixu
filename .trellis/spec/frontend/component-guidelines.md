# 前端组件规范

> 定义组件职责、组合方式和可访问性约束。

## 适用范围

适用于 React 组件和路由级 UI。仓库当前没有生产组件，以下内容是已确认设计约束，不是已有代码描述，M1 必须用真实组件和测试验证。

## 已确认事实

- `docs/architecture/frontend-architecture.md` 要求使用 React + TypeScript，业务状态不得依赖 UI 组件库。
- Diff、Evidence、Status、Graph、Table、Workflow 和 Review 是核心产品界面。
- `docs/product/PRD.md` 要求关键操作支持键盘、焦点清晰、状态同时使用文字和视觉标识、图谱不只靠颜色区分、Diff 为屏幕阅读器提供文本说明。
- Error Boundary 仅处理渲染错误；业务错误必须在页面展示错误码、影响和下一步操作。

## 组件职责

- Route Component 负责页面组合并连接 Feature Query、Command 和 URL State。
- Feature Component 展示产品行为并使用 Feature Hook，不解码原始 API 或 SSE Payload。
- Shared Component 保持展示性和领域无关，以语义化 Props 代替完整 Transport Object。
- Domain Display Projection 在组件外生成，避免 Table、Card 和 Detail 重复解释同一业务状态。
- 优先使用小型语义组件和组合，避免用大量布尔 Props 承载无关模式。

## Props 与事件

- 本地 Props Type 与组件共置；只有存在真实公共组合边界时才导出。
- Props 表达 UI 意图，例如状态展示或 Evidence Reference，不直接暴露完整生成 DTO。
- 事件 Props 使用 `onApprove`、`onRetry` 等用户动作命名；只有基础组件才暴露 DOM 实现细节。
- 禁止修改 Props。派生值在渲染期或命名 Projection 中计算，不同步复制到 State。
- 破坏性、审批和写回操作必须表达 Pending、Success、Failure、Disabled Reason 和重复提交行为。

## 样式与视觉语义

- 当前未选择 UI 库和样式系统；M1 必须先记录并锁定方案，之后才能添加示例。
- 业务状态不得依赖组件库的状态或类型。
- Status、Risk、Graph Relation 和 Severity 不得只依赖颜色，应结合文字、形状、图标、线型或标签。
- 大表使用虚拟滚动，图谱增量渲染，大 Diff 分段加载，遵守 `docs/architecture/performance.md`。

## 可访问性

- 优先使用原生语义元素，避免无必要地用 ARIA 重建。
- 所有关键操作可用键盘完成，并有可见焦点。
- Dialog、Drawer、Menu 和错误恢复流程打开时管理焦点，关闭后恢复焦点。
- 纯图标控件必须有可访问名称；表单字段必须关联 Label 和错误说明。
- 需要用户注意的异步变化使用合适的 Live Region，但不逐 Token 播报流式内容。
- Diff 为屏幕阅读器提供文本变化说明，不只展示插入/删除颜色。
- Graph 必须提供非 Canvas 或其他键盘可访问方式，用于检查节点、关系和证据。
- Loading、Empty、Error、Degraded、Waiting for Human、Manual Recovery 状态必须可区分且可操作。

## 禁止模式

- 在 JSX 事件处理器中实现业务规则或 Workflow 状态转换。
- 架构要求服务端持久化时，只在组件内存保存 Proposal Revision 或 Approval Draft。
- 用 Error Boundary 捕获业务错误并显示通用成功或空页面。
- 不安全 HTML、可执行 Markdown 或未清理的外部内容。
- 用户必须解除前置条件时，只禁用按钮而不解释原因。
- 仅点击交互、移除焦点但不替代、仅颜色表达状态。
- 组件接收 `any`、原始 `unknown` 或未校验 SSE/API Envelope。

## 验证

M1 前执行：

```bash
rg -n 'Accessibility|键盘|颜色|焦点|屏幕阅读器|Error Boundary' docs/architecture/frontend-architecture.md docs/product/PRD.md
git diff --check
```

M1 后，Component Test 必须覆盖语义 Role、键盘、焦点、Loading/Error/Empty 以及 Diff/Evidence/Status；浏览器测试覆盖高风险审批和恢复流程。

## M1 待代码验证

M1 必须确定样式与组件库策略、测试渲染器、可访问性工具和代表性组件模式；只有 Manifest 和源码存在后才添加真实代码链接。

## M6-04 RAG 页面模式

- `/chat` 与 `/chat/:conversationId` 使用“会话栏—回答工作区—证据栏”研究台布局；移动端收敛单列，
  Citation 选择后以可聚焦 drawer 呈现，关闭后焦点返回触发按钮。
- Pending 只展示 Workflow/current_stage；Completed、Refusal、Clarification 必须穷尽分支，Clarification 不展示
  后端必然拒绝的 Feedback。
- Answer 正文按纯文本 `white-space: pre-wrap` 展示；Citation href 只使用服务端值，不执行 Markdown/HTML。
- Composer 支持 Enter 提交、Shift+Enter 换行，并显式提供 Scope、Depth、Format 与不可用能力说明。
