# 优化工作台首屏与导航流程

## Goal

在保留知序现有编辑部式品牌语言和真实状态表达的前提下，降低首次使用时连接 Workspace 的操作成本，使用户能够在首屏完成连接入口选择，并在创建或扫描后获得明确、可继续的下一步。

## Confirmed Facts

- 真实运行页中，未连接 Workspace 时，创建按钮在 `1440x900` 桌面视口约 `y=1519`，在 `390x844` 移动视口约 `y=1894`；主操作被大标题和完整系统状态矩阵推到首屏外。
- [`web/src/app/AppShell.tsx`](../../../web/src/app/AppShell.tsx) 当前把 17 个导航入口平铺为同级项目，并混用中英文标签；移动 Sheet 需要继续滚动才能到达部分入口。
- [`web/src/features/workspace/WorkspacePage.tsx`](../../../web/src/features/workspace/WorkspacePage.tsx) 创建或打开 Workspace 后只展示摘要和手动扫描入口；扫描完成后只展示结果表格，没有继续到资料或工作台的引导。
- [`web/src/features/system-status/SystemStatusPage.tsx`](../../../web/src/features/system-status/SystemStatusPage.tsx) 将 14 项运行事实平铺为四列网格，首行“所有基础依赖可用”与 RAG “已关闭”、侧栏 “离线”在首次使用时容易产生语义冲突。
- 产品 PRD 要求首次启动采用步骤式引导，展示检查结果；Workspace 创建成功后进入首页并显示初始扫描进度。参见 [`docs/product/PRD.md`](../../../docs/product/PRD.md) 第 10.1.4-10.1.5 节。

## Requirements

### R1. 保留品牌方向，收紧首次连接层级

- 保留暖灰、深墨、黄铜强调色和有辨识度的中文排版，不做全站视觉重做。
- Workspace 页面首屏优先呈现连接或打开 Workspace 的控件；桌面端主按钮必须在 `1440x900` 首屏可见，移动端在 `390x844` 下无需经过完整运行状态矩阵即可到达主按钮。
- 缩小 Workspace 页面展示型标题，修复常见桌面宽度下单字孤行的标题断行。

### R2. 让导航匹配任务而非路由数量

- 将现有路由归入清晰的任务组，统一为中文导航标签。
- 未连接 Workspace 时，仅突出 Dashboard、工作区和设置；仍保留直接 URL 的既有 gate 行为，不移除任何路由。
- 已连接 Workspace 时保留全部功能入口，学习与研究类低频入口可以折叠，桌面侧栏和移动 Sheet 使用相同分组。

### R3. 区分运行状态、同步状态和工作区状态

- 未选择 Workspace 时，侧栏不再显示容易误解为服务不可用的“离线”；明确表达尚未建立 Workspace 上下文。
- Workspace 首屏只显示紧凑、可扫读的运行摘要；完整运行诊断仍保留在 Dashboard 与设置，不删除任何后端事实。
- RAG 已关闭等可选能力必须在摘要文案中与核心服务 ready 状态区分，避免“全部可用”与“已关闭”并置造成误读。

### R4. 闭合 Workspace 设置后的下一步

- 创建或打开成功后展示当前 Workspace 和下一步说明，提供扫描资料及进入工作台的明确入口。
- 扫描完成后保留现有事实表格，并补充进入资料版本或工作台的下一步入口。
- 不改变 Workspace 创建、打开和扫描 API 的请求、权限、幂等或数据模型。

### R5. 可验证性

- 更新受影响的 React 单元测试，覆盖未连接导航、Workspace 创建后的后续入口、扫描后的后续入口以及紧凑状态摘要。
- 执行 lint、typecheck、相关 Vitest 测试、production build 和 `git diff --check`。
- 使用 Playwright CLI 对真实本地页面执行桌面 `1440x900` 与移动 `390x844` 浏览器验证，检查主操作可见性、移动导航、无横向溢出和无浏览器控制台错误。

## Out of Scope

- 不修改后端 API、数据库、认证、SSE、Workspace 文件系统行为或扫描语义。
- 不创建、扫描或删除真实用户 Workspace 作为验收步骤。
- 不重做已有数据态页面的 Graph、Review、Collection、Artifact 等业务 UI。
- 不将 Playwright CLI 验证扩展为新的后端数据 fixture 或完整 E2E 套件。

## Acceptance Criteria

- [x] `1440x900` 与 `390x844` 下，未连接 Workspace 的页面均能在主任务路径中清晰找到“创建 Workspace”或“打开已有 Workspace”，且不需要先浏览完整状态矩阵。
- [x] 未连接 Workspace 时，导航不暴露一长串无上下文的工作区功能；连接后全部现有路由仍可由分组导航访问。
- [x] Root Workspace 页不再显示错误的“详情”面包屑；同步、运行和 Workspace 状态的文案不相互矛盾。
- [x] 创建、打开和扫描成功后的页面均有下一步 CTA，且不伪造 Workflow、扫描或业务成功事实。
- [x] 受影响单测、TypeScript、lint、build 和 `git diff --check` 通过。
- [x] Playwright CLI 在真实本地服务上完成桌面和移动检查，无横向溢出和 console warning/error，并保存截图到 `output/playwright/`。

## Risks and Constraints

- 导航分组仅改变呈现与可发现性，不能改变既有 URL、路由保护或 deep link 行为。
- 系统状态摘要必须继续由 `/api/v1/system/status` 的严格解码结果驱动，不能使用静态文案替代事实。
- 现有数据库无 Workspace 数据；浏览器验收只覆盖真实空态和无副作用的 gate，不通过创建数据来伪造完整数据态。
