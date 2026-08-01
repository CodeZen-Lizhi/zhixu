# 已验证的当前边界与方案比较

## 当前数据流

```text
Browser /dashboard
  -> HostControlProvider
  -> GET /control/v1/session 或 POST /control/v1/sessions
  -> GET /control/v1/state
  -> effective Workspace ID
  -> BusinessRuntime
  -> 独立 AuthProvider / AuthBoundary
  -> DashboardPage 与业务 API
```

- `HostControlledRuntime` 在 Controller Session 缺失时直接渲染“控制链接已失效”，业务认证和路由均不会挂载：`web/src/app/App.tsx:57`。
- `HostControlProvider` 把 Controller 401 或实例变化统一转为 `session_required`，并清空 effective Workspace：`web/src/app/host-control-context.tsx:181`、`web/src/app/host-control-context.tsx:199`。
- 当前 Bootstrap Fragment 只交换 Controller Session；完整 Controller State 仍要求 Cookie：`web/src/api/controller.ts:319`、`web/src/api/controller.ts:341`。
- `/control/v1/state` 包含 Workspace Root Path、最近 Workspace 和操作详情，因此不能直接匿名开放：`internal/hostcontroller/types.go:53`、`internal/hostcontroller/types.go:76`。
- Host Controller 所有写操作继续经过 Session、Host、Origin、CSRF、If-Match 与 Idempotency-Key：`internal/hostcontroller/http.go:306`、`internal/hostcontroller/http.go:325`。
- 业务代理本身不要求 Controller Session，只在权威运行时 ready 时转发，并剥离 Controller 凭证：`internal/hostcontroller/http.go:384`。
- Dashboard 只需要一个有效 Workspace ID 才会查询业务数据；Workspace 为空时已有不发业务请求的 Entry 状态：`web/src/features/business/DashboardPage.tsx:185`、`web/src/features/business/DashboardPage.tsx:219`。
- Controller Session TTL 为 12 小时且会随 Controller 进程重启清空：`cmd/hostcontroller/main.go:102`。
- 当前本地 Docker 环境使用业务 `ZHIXU_AUTH_MODE=disabled`；代码同时支持 required 模式下的独立业务登录。

## 方案比较

### A. 直接绕过前端 Controller 门禁

拒绝。新浏览器没有 authoritative Active Workspace ID，只能依赖空或陈旧 `localStorage`；切换、回滚和 Controller 重启时还会挂载错误业务作用域。

### B. 为所有本机浏览器自动签发完整 Controller Session

拒绝。任意本机进程都可获取可写 Session 和 CSRF 后伪造 Origin，等价于取消 Workspace Root 与 Docker 控制面的调用者认证。

### C. 匿名开放现有 `/control/v1/state`

拒绝。该响应包含完整宿主机 Root Path、最近 Workspace、目标名称及操作详情，违反最小公开面和现有路径不泄漏测试。

### D. 新增最小、脱敏、只读 Runtime Projection

推荐。Host Controller 继续是 Active Workspace 与 runtime readiness 的唯一事实源，但匿名端点只返回业务路由恢复所需的最小字段，例如 runtime/process 状态、active Workspace ID、有限 operation 状态和 poll interval；不返回 Root Path、最近 Workspace、Session、CSRF 或控制命令版本信息。完整 `/control/v1/state` 和所有写操作保持原认证。

前端以公开 Runtime Projection 恢复业务作用域；拥有 Controller Session 时再加载完整控制状态。Controller Session 失效只降级控制能力，不再卸载仍由公开权威状态证明 ready 的业务首页。

## 待产品决定

- 无 Controller Session 时，是只允许 `/dashboard`，还是允许整个业务工作台由其独立业务认证层裁决。前者公开面更小，但首页中的资料、待办和导航链接会在点击后重新撞上 Controller 门禁；后者边界更一致，但本地 `auth_mode=disabled` 时会让全部业务路由对本机浏览器直接可达。
