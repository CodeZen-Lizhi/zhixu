# Git 同步设置前端契约

本规范锁定 Settings 中 Git Remote 配置、同步状态和恢复交互。服务端 API 是事实源；前端不推演 Git 是否安全，也不保存 Secret 或运行状态副本。

## 路由与信息架构

- Git 同步与 Workspace 状态共同位于 `/settings?section=workspace`；旧 `/settings?section=sync` 只作为兼容入口并归一化到
  `section=workspace`。桌面使用设置侧栏，移动端使用单一分类选择器。
- 一级导航固定为“工作台 / 知识 / 创作 / 设置”；Git 同步不是一级业务菜单，也不恢复右上角省略号菜单。
- 工作台“新建文章”直接进入 `/authoring/new`；Git 设置不得占用或替代创作入口。

## API 边界

- `web/src/api/git-sync.ts` 是唯一 wire decoder/client；组件和 Query 不解析 raw JSON。
- Decoder 必须 exact-check 字段、UUID、RFC3339、OID、HTTPS URL、branch、状态枚举、文件路径和有界列表。
- OpenAPI 标记为 required 的 nullable 字段必须显式存在；字段缺失或 `undefined` 不能按 `null`、空 Run 或空引用接受。
- Decoder 还必须校验跨字段状态不变量：运行中/终态与 `completed_at`、成功/失败分类、trigger 与 `retry_of_run_id`、direction 与预期/核验 OID、Git 终态与 index 状态必须能共同成立；不能只验证每个字段各自类型。
- `PENDING|FETCHING|COMPARING` 只能是 `direction=UNKNOWN` 且没有预期/核验 OID；`FAST_FORWARDING` 只能是
  `PULL`，`PUSHING` 只能是 `PUSH`，`VERIFYING` 只能是 `PULL|PUSH`。`SUCCEEDED` 必须有成对 expected/verified OID、
  direction 不能为 `UNKNOWN` 且 verified 两端收敛；只有成功的 `PULL` 可以进入独立索引 follow-up，其他状态必须为 `NOT_REQUIRED`。
- `changed_files` 最多 `500` 条；每条记录的 `path`、`old_path`、`kind` 都必须显式存在，非 Rename 的 `old_path` 必须为 `null`，不能省略。Rename 必须同时提供不同的 `old_path/path`。达到 `500` 条时只表示有界预览，不能推断远端总变更数。
- 所有配置、Run、列表和状态响应重新验证 `workspace_id`。`current_run: null` 表示没有当前运行，不得被误判为 Workspace binding 失败。
- Problem code、message 和 retryable 只来自严格 Problem decoder。网络失败与无效响应分开显示，不能静默回退为“未配置”。

## Workspace 与 Server State

- Query key 必须包含活动 Workspace ID；切换 Workspace 时取消旧请求与命令，并清理旧配置、Run、错误、Secret 和表单反馈。
- Workspace ID 改变后，在新 status 的 Workspace/config revision 完成表单 hydration 之前必须 fail closed：只显示明确的
  “正在切换 Git 同步配置”状态，不渲染旧 Workspace 或缓存中新 Workspace 的配置、Run、历史记录和任何可执行命令。
  迟到的旧 Workspace Query/Mutation 回调只能落在原 Workspace query key，并按命令绑定的 Workspace 丢弃页面副作用，
  不能更新当前 Workspace 的页面反馈或缓存投影。
- TanStack Query 持有配置、当前 Run 和历史列表。SSE 或轮询只触发重新读取，不成为第二事实源。
- 只有活动 Run 才轮询；Run 进入终态后停止。刷新页面通过状态/列表 API 恢复，不依赖内存计时器。
- 手动创建、重试、保存和删除都由调用方生成并持有一个 idempotency key；网络错误后的用户重试复用同一 key，明确重新输入 Secret 后生成新命令 key。

## Secret 生命周期

- Token 只存在于受控 password input 和当前 mutation 参数；不得写入 React Query cache、URL、日志、toast、DOM 文本、localStorage、sessionStorage 或持久草稿。
- 服务端只返回 `token_configured`。已配置表单默认选择 `keep`，Token input disabled 且为空；选择 `replace` 才允许输入新值。
- 保存或测试请求结束后，无论成功、业务失败、网络失败或取消，都立即清空 input state 和已消费的 attempt key。
- 同一个明文 Token 的 response-loss retry 不能由前端自动重放，因为浏览器已销毁明文；页面要求用户重新输入，并为新输入创建新 key。
- 删除配置和 `clear` 使用显式危险语义，不借助空字符串暗示删除。

## 状态与操作

- 未配置：展示 URL、branch、默认关闭的 auto-sync、replace Token 与禁用的“立即同步”。
- 已配置：展示规范化 HTTPS URL、branch、auto-sync、只写 Token 控件和移除入口；不显示掩码 Token。
- 运行状态同时使用文字和视觉标识，至少区分等待、Fetch、Compare、拉取、推送、核验、成功、冲突、失败、过期和人工恢复。
- `direction=UNKNOWN` 固定显示“尚未判断”；只有 `direction=NONE` 可以显示“无变更”，不能从缺失 OID 或活动状态推断为 NONE。
- Git 状态与知识索引状态分列。索引失败提供独立重试语义，不能把 Git 成功改成失败。
- dirty、detached、diverged、stale、offline、authentication 和 result unknown 显示稳定错误码与可执行恢复入口；不声称自动解决冲突。
- 文件变化达到 `500` 条时显示“显示前 500 个文件变化，可能还有更多”；该提示不能改成“共 500 个”。
- `REF_DRIFT` 同时涵盖当前 attached branch 与配置不一致、以及同步期间 ref 变化；文案不能只描述后一种情况。
- 活动 Run 禁用第二次“立即同步”；可重试终态显示“重新同步”，不可恢复状态只展示说明。

## 响应式与可访问性

- `1440x900` 与 `390x844` 下 `document.scrollWidth === document.clientWidth`，长 Remote URL、Run ID、错误码和 OID 不制造页面横向滚动。
- 输入、按钮、状态徽标和分类选择器使用稳定尺寸；短状态徽标不拆字换行，长错误文本允许自然换行。
- 桌面配置字段可双列，移动端降为单列；同步操作在移动端占满可用宽度，事实列表改为纵向。
- 表单使用真实 label、fieldset/radiogroup、status/alert；加载、空、错误、未配置、活动和终态都有可读文本。
- 图标按钮保留可访问名称，状态不只依赖颜色；键盘可完成分类切换、表单、保存、测试、同步和重试。

## 浏览器门禁

- 使用真实 API 与一次性 QA Workspace 验证未配置、保存、手动入队、Worker 终态和重新同步入口。
- 构造真实 `direction=UNKNOWN` 活动 Run，在桌面与 `390x844` 都确认显示“尚未判断”且不出现“无变更”。
- 预热 Workspace A 的 Git status/runs 后切换到 Workspace B，并延迟 B 的绑定响应；切换期间只能显示 fail-closed
  状态，A 的 Remote、Run、历史与命令均不得出现，B 完成绑定和表单 hydration 后才能恢复操作面。
- 至少制造一次 dirty、ref drift 或 diverged 冲突，在桌面和移动端检查双方 OID、稳定错误码、文件预览上限提示和重试入口；一般网络失败不能替代冲突态验收。
- 验证 Token 不出现在 DOM 文本、localStorage、API 响应和服务日志。
- 桌面检查工作台直接创作入口、无省略号菜单、系统状态折叠/展开和 Git 设置。
- 移动检查主导航 Sheet、设置分类选择、Git 长 URL/Run 状态、系统技术详情和新建文章实际创建/Freeze。
- 每个关键页面测量 document/body width 与可见 overflow，并检查 console error/warning；截图必须作为当前任务证据保留。
  终端 Playwright 保存到 `output/playwright/`，内置浏览器则保留任务内截图输出，并在结果中记录精确视口、宽度与重叠测量。

## 禁止模式

- 不用 optional chaining 比较缺失 `current_run` 与 Workspace ID 并误报 binding error。
- 不把 Secret 放入 Query data、optimistic update、Storage、URL 或错误信息。
- 不因配置 API 失败把旧 Workspace 配置继续显示为当前配置。
- 不把 `UNKNOWN` 文案折叠为“无变更”，也不在 Workspace 切换 effect 完成前短暂渲染旧配置、Run 或命令。
- 不以持续轮询替代终态判断，不在活动 Run 外保持高频请求。
- 不在页面加入自动 Merge/Rebase/Force/Reset/Checkout 控件或成功文案。
