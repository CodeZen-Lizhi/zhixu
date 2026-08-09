# Current Gap Audit

## 已完成

- 冷色主 token、无衬线基础字体、蓝色主操作和 Settings 五分类已经落地。
- Workspace / Git 已合并到 Settings 的“工作区”；`/workspace` 使用“新建 / 使用 Workspace ID”互斥模式。
- 一级导航已收敛为“工作台 / 知识 / 创作”，创作直接进入 `/authoring`；Proposal、Workflow、Artifact 深链仍归属创作。
- `/authoring`、`/authoring/new` 和首页“快速记录 / 新建文章 / 整理成文 / 搜索知识”已接入真实路由。
- 整理成文只在 Authoring Overview 返回真实 `href` 时可点击，未使用假入口。

## 剩余缺口

### 顶栏重复菜单

`web/src/app/AppShell.tsx` 仍有 `MoreHorizontal` 菜单，重复列出工作区设置、系统状态和资料收件箱。应删除菜单和只为其服务的状态；认证 required 模式改为明确的“退出登录”按钮，移动 Sheet 中的退出动作可以保留。

### 系统状态矩阵

`web/src/features/system-status/SystemStatusPage.tsx` 仍把 14 项能力、版本和请求 ID 平铺为四列 `.status-grid`。目标结构应为：

- 正常时只显示“运行正常”和最近检查时间。
- 只列异常、关闭或降级项，并说明影响与恢复动作。
- `version`、`requestId` 和完整依赖值进入默认关闭的“技术详情”。
- Loading、请求失败、严格解码失败和 Degraded 保持不同状态。

### 首页 Inbox 重复入口

`web/src/features/business/DashboardPage.tsx` 当前可能同时出现空待办入口、空资料“前往资料收件箱”和最近捕获“查看全部”。保留一个真实次级 Inbox 入口，其余最近资料区域只负责恢复上下文。工作区设置应统一指向 `/settings?section=workspace`。

### 响应式与视觉收尾

- `.workbench__content` 仍有 `width: min(1440px, 100%)`，超宽主栏留下无意义空白。
- Dashboard 仍使用 `repeating-linear-gradient` 背景，不满足无页面背景渐变的验收。
- `721px-920px` 仍使用固定焦点列和最小 320px 资料列；`768x1024` 风险最高，应提前转单列。
- `web/src/app/controller.css` 仍有 Georgia；Controller Workspace 仍有 eyebrow / 大标题文案。
- 窄屏动作说明使用 ellipsis，最长文本需要允许换行且不能改变相邻控件尺寸。

## 实施顺序

1. 删除顶栏 `...`，补桌面退出登录与测试。
2. 系统状态改为总览、异常列表和技术详情，更新 CSS / 测试。
3. Dashboard Inbox 去重，调整 Settings 深链和中间宽度重排。
4. 移除 Dashboard gradient、内容宽度上限和 Controller Georgia。
5. 执行前端门禁与四视口真实浏览器验收。

## 浏览器矩阵

| 视口 | 主要检查 |
| --- | --- |
| `1440x900` | 主内容利用可用宽度、无多余滚动、系统状态信息层级 |
| `1024x768` | Dashboard 焦点/动作/资料重排、Topbar 稳定 |
| `768x1024` | 移动 Shell 与 Dashboard 单列切换，无横向溢出 |
| `390x844` | 2x2 常用动作、Sheet、Settings 分类、长文本与按钮尺寸 |

每个视口检查 `document.documentElement.scrollWidth <= window.innerWidth`、Console/Network、文本遮挡、焦点恢复和 Reduced Motion。
