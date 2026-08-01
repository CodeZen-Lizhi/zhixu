# 首页公开访问与控制操作授权拆分

## Goal

让用户在任意本机浏览器中随时打开首页及普通业务工作台，不再依赖一次性 Host Controller 链接、Controller Cookie 或某个浏览器的本地状态；业务数据仍由独立业务认证层保护，宿主机目录授权、Workspace 切换/移除和 Docker 运行时重建继续由 Host Controller 凭证保护。

用户价值是把“打开笔记应用”和“修改宿主机运行边界”拆成两件事：日常浏览与使用不再被控制会话生命周期打断，高权限操作也不会因此匿名开放。

## Confirmed Facts

- Go Host Controller 已允许匿名获取 SPA 文档；`GET /dashboard` 当前阻塞在 React 启动组合，而不是静态路由：`internal/hostcontroller/http.go:76`、`internal/webassets/handler.go:42`、`web/src/app/App.tsx:57`。
- 当前 `HostControlProvider` 在 Controller Session 缺失、过期或 Controller 实例变化时进入 `session_required`，并阻止整个 `BusinessRuntime` 挂载：`web/src/app/host-control-context.tsx:181`、`web/src/app/host-control-context.tsx:199`。
- Controller Bootstrap Token 每个进程生命周期只能交换一次；Controller Session Cookie 有效期 12 小时且保存在进程内，Controller 重启会使旧 Session 失效。
- `/control/v1/state` 包含宿主机 Root Path、最近 Workspace、操作详情、状态版本和 Controller 实例，不能匿名开放：`internal/hostcontroller/types.go:53`、`internal/hostcontroller/types.go:76`。
- Host Controller 写操作已经要求 Session、精确 Host/Origin、CSRF、`If-Match` 与 `Idempotency-Key`：`internal/hostcontroller/http.go:306`、`internal/hostcontroller/http.go:325`。
- 业务代理本身不要求 Controller Session；它仅在服务端权威运行时 ready 时转发，并剥离 Controller 凭证、保留业务凭证：`internal/hostcontroller/http.go:384`、`internal/hostcontroller/http.go:554`。
- 业务 API 有独立的 Session/API Token 认证。在 `required` 模式下匿名业务请求仍返回 `401`；当前本地 Docker 配置显式使用 `ZHIXU_AUTH_MODE=disabled`。
- `localStorage` 中的 Workspace ID 只是浏览器本地提示；新浏览器没有该值，旧浏览器中的值也可能过期，不能作为运行时授权事实。

## Requirements

- R1：Controller 模式下，除 `/` 与 `/workspace` 外，现有普通业务路由由独立业务认证和运行时状态决定，不再由 Controller Session 决定。范围包括首页、知识、搜索、对话、产出、复习、记忆、时间线与普通设置等现有 `AppRoutes` 路由。
- R2：业务认证为 `required` 时，无 Controller Session 的浏览器应进入现有业务登录流程；业务认证为 `disabled` 时，本机浏览器可直接进入已就绪的业务工作台。两种模式都不得显示“控制链接已失效”作为普通业务路由门禁。
- R3：`/` 与 `/workspace` 继续作为 Host Controller 控制入口。Workspace Root 授权、Workspace 切换/移除、可用性重查和 Docker 运行时重建继续要求有效控制会话，并保留全部既有请求防护。
- R4：新增独立、只读、最小化的运行时访问投影，由 Host Controller 权威状态派生。公开字段只允许表达 `waiting|ready|unavailable`、ready 时的不透明 Active Workspace ID 以及有界轮询间隔。
- R5：公开投影不得返回 Root Path、Workspace 名称或最近列表、操作 ID/阶段/错误详情、Controller 实例/状态版本、Bootstrap/Session/CSRF 或任何业务秘密。
- R6：Controller Session 过期或 `/control/v1/session` 返回 401 只降低控制能力；只要业务运行时仍被权威投影证明 ready，普通业务页面继续由业务认证决定。Host Controller 进程不可达或 `zhixu restart` 明确撤销/重建 runtime 时允许短暂中断，但页面必须停止读取旧 Workspace 数据，并在恢复 ready 后复用仍有效的业务身份重新挂载。
- R7：公开投影是发现信息，不是授权或租约。每个业务请求仍由 Host Controller 代理重新检查 ready 状态，并由业务 API 重新检查业务身份和 Capability。
- R8：Active Workspace 的客户端唯一权威来源改为公开运行时投影。`localStorage` 只能作为可丢弃提示，不能在权威响应前挂载 Query、SSE 或业务路由；A 到 B 切换必须先清空 A 的 Query/SSE，再发布 B。
- R9：Direct 模式行为保持兼容；启动器的一次性控制链接仍能进入 `/` 或 `/workspace` 并建立控制会话，现有业务深链不得被重定向成控制页或假成功页。
- R10：更新 `2026-08-01 需求优化清单`，在代码、自动化测试和浏览器验收全部完成后勾选 TODO 1，并记录最终权限边界。

## Acceptance Criteria

- [x] AC1：全新浏览器配置直接访问 `http://127.0.0.1:8080/dashboard`；业务认证 disabled 时看到当前已连接首页，required 时看到业务登录，两种情况都不需要 Controller 凭证。
- [x] AC2：两个互不共享 Cookie/localStorage 的浏览器上下文可同时打开 `/dashboard` 及任一普通业务深链；每个上下文独立执行业务认证，不消费 Controller Bootstrap Token。
- [x] AC3：分别验证三种场景：仅 Controller Session 失效时仍 ready 的业务树不卸载；Host Controller 进程短暂不可达时先进入不可用、恢复后复用业务身份；`zhixu restart` 撤销并重建 runtime 后恢复 durable Active Workspace。三种场景下 `/workspace` 都只接受当前有效控制会话。
- [x] AC4：没有 Active Workspace 时显示可连接目录的等待状态；切换、回滚、恢复失败或业务运行时不可用时显示脱敏不可用状态，不渲染旧 Workspace 事实。
- [x] AC5：匿名请求所有 Host Controller 写接口仍稳定返回 4xx，且不会创建操作、改变 Root Grant、切换 Workspace 或触发 Docker 重建。
- [x] AC6：匿名运行时投影仅返回约定字段，响应 `Cache-Control: no-store`、限制精确 Host、不允许跨源读取；错误响应不包含宿主路径、内部错误文本或操作详情。
- [x] AC7：伪造或陈旧的 `zhixu.active-workspace-id` 不能让业务 Provider、Query 或 SSE 在权威 ready 响应前挂载。
- [x] AC8：Workspace A 切换到 B 时，A 的请求被取消、Query 缓存移除、SSE 关闭；B 只在权威 ready 后挂载，同一页面不得短暂显示 A 的业务数据。
- [x] AC9：`/`、`/workspace` 和所有控制命令保持现有 Session/Origin/CSRF/ETag/幂等兼容；Controller Token 不进入业务请求、日志或存储。
- [x] AC10：相关 Go 测试、前端 lint/typecheck/test/build 通过；Playwright 覆盖桌面与移动、两个隔离浏览器、required/disabled 业务认证、Session 失效、Controller 进程重启及 launcher restart；确定性 focused tests 覆盖 Workspace 切换顺序。
- [x] AC11：`docs/product/2026-08-01-requirement-optimization-list.md` 的 TODO 1 被勾选，并与最终实现及安全边界一致。

验收采用真实 Docker 与受控 fixture 组合：多浏览器、disabled、控制 Session 缺失、Controller/launcher 重启使用真实运行服务；required 使用 Playwright 响应 fixture；Workspace A -> B 使用确定性生命周期测试验证清理与挂载顺序，避免改变用户当前工作区。

## In Scope

- Controller 模式下普通业务路由与 Host Controller 会话的解耦。
- 最小、脱敏、只读运行时访问投影及严格前端解码。
- Active Workspace 权威所有权、轮询、切换清理和 Query/SSE 生命周期调整。
- `/`、`/workspace` 控制路由隔离及原有控制能力兼容。
- 后端、前端、浏览器回归测试和相关安全/前端规范更新。
- 8 月 1 日需求优化清单 TODO 1 的完成记录。

## Out of Scope

- 取消或弱化 Workspace Root、Docker、控制状态写操作的认证与 CSRF 防护。
- 将 Host Controller 或业务 API 暴露到非 loopback 网络地址。
- 引入多人账号、云端登录、SSO、远程访问或新的角色权限模型。
- 重设计首页视觉、导航、设置内容或知识业务功能。
- 改变业务认证 `required|disabled` 的产品含义；本任务只让它成为普通业务路由的独立认证边界。

## Key Product Decision

- 2026-08-01 用户选择方案 B：无 Controller 凭证时，整个普通业务工作台由独立业务认证层决定；只有 `/workspace`、目录授权/切换和 Docker 等 Host Controller 能力继续要求控制凭证。
