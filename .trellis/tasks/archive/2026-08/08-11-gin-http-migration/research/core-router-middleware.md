# Research: Chi 到 Gin 的核心 Router/Middleware 行为差异

- Query: 在不改变现有 HTTP、安全、日志和静态资源契约的前提下，核对 Chi v5.3.1 到 Gin v1.12.0 的 Router、Middleware、Context、ResponseWriter 和 Recovery 行为，并给出迁移配置与验证建议。
- Scope: mixed（仓库代码、Trellis 规格、Go module cache 中的 Gin/Chi v1.12.0/v5.3.1 源码及测试）
- Date: 2026-08-11

## Findings

### 1. 当前 Composition Root 与 Chi 基线

**Files found**

| 文件 | 证据 |
|---|---|
| `internal/app/router.go:100-121` | `NewRouter` 创建 `chi.NewRouter()`，全局顺序固定为 `no-store -> request ID -> trace -> request log -> panic recovery`。 |
| `internal/app/router.go:160-183` | `/api/v1` 子路由；公开路由后进入带 Auth Middleware 的 protected group；子路由 NotFound 返回 `NOT_FOUND`。 |
| `internal/app/router.go:184-197` | 根 MethodNotAllowed 返回项目 Problem；根 NotFound 只对 `strings.HasPrefix(path, "/api/")` 返回 API Problem，否则转发 `deps.Static`，无静态依赖时返回 `WEB_ASSETS_UNAVAILABLE`。 |
| `internal/app/router.go:292-328` | Trace Middleware 对 `/metrics`、`/livez`、`/readyz` 跳过 span；其他请求把 trace 和 correlation 写回标准 `request.Context()`。 |
| `internal/app/router.go:485-552` | Request ID 写入标准 Context/Header；`statusWriter` 在首次 `WriteHeader`/`Write`/`Flush` 时记录状态；日志只读取 Chi `RoutePattern`，不回退到实际 URL path。 |
| `internal/app/recovery.go:17-52` | Recovery 只在 HTTP 边界捕获 panic；精确的 `http.ErrAbortHandler` 重新 panic；日志包含稳定错误码、request ID、路由模板和有界栈，不包含 panic 值；响应已开始时不追加 Problem。 |
| `internal/app/recovery_test.go:88-146` | 锁定 response-started 204、精确 ErrAbortHandler、wrapped abort、日志脱敏和中间件顺序。 |
| `internal/app/router_test.go:705-723,790-815` | 现有测试锁定 API 404、405 Problem 和模板化日志（`/api/v1/workspaces/{workspace_id}`，不能出现 UUID/path）。 |
| `internal/webassets/handler.go:42-63` | 静态 handler 自己实现精确文件、GET extensionless SPA `index.html` fallback、路径遍历拒绝和最终 `http.NotFound`。 |
| `internal/auth/http/handler.go:96-132` | Auth 公开/受保护路由使用 Chi；认证失败直接写 Problem 后返回，成功将 Principal 放入标准 `request.Context()`。 |

Chi v5.3.1 的相关实现：`mux.go:447-490` 先按 `RawPath`（存在时）/`Path` 路由，支持的 HTTP method 之外立即调用 MethodNotAllowed；匹配成功后写入 `Request.SetPathValue` 和 `Request.Pattern`（`mux.go:476-481`）。`mux.go:402-417` 显示自定义 MethodNotAllowed 会绕过默认 `Allow` header responder。`chi.go:50-53` 的路径说明明确普通参数不匹配尾斜杠；当前代码没有安装 `middleware.RedirectSlashes`、`CleanPath` 或 `GetHead`，因此基线不是自动重定向/自动 HEAD。

### 2. Gin v1.12.0 默认值必须全部显式覆盖

Gin `New` 的字段和默认值见 module cache `github.com/gin-gonic/gin@v1.12.0/gin.go:90-227`：

- `RedirectTrailingSlash=true`：不匹配的尾斜杠会自动重定向，GET 是 301，其他方法是 307（`gin.go:99-115,690-734,781-831`）。Chi 当前没有该行为。
- `RedirectFixedPath=false`，但必须显式设为 false，避免后续默认/模板变化导致清理双斜杠、`..` 或大小写重定向。
- `HandleMethodNotAllowed=false`。必须设为 true 才会扫描其他 method tree（`gin.go:738-760`）；开启后 Gin 自动设置 `Allow` header（`gin.go:739-753`），这与当前自定义 Chi 405 响应的 header 可能不同。
- `UseRawPath=false`、`UseEscapedPath=false`、`UnescapePathValues=true`。默认使用已解码的 `URL.Path`；Chi 在 `RawPath` 存在时使用原始编码。
- `RemoveExtraSlash=false`。保持 Chi 的精确斜杠匹配。
- `ForwardedByClientIP=true`、`RemoteIPHeaders=[X-Forwarded-For,X-Real-IP]`、trusted proxies 为 `0.0.0.0/0` 和 `::/0`（`gin.go:125-159,193-227`），这是不应继承的安全默认值。
- `ContextWithFallback=false`。Gin Context 只有在该开关为 true 时才把 `Deadline/Done/Err/Value` 委托给 `Request.Context()`（`context.go:1433-1482`）。项目应始终把 `c.Request.Context()` 传到 Application/Domain，而不是把 `*gin.Context` 当作标准 Context。

建议 Composition Root 创建单一 `gin.New()`，并在启动时集中设置：

```go
engine := gin.New()
engine.RedirectTrailingSlash = false
engine.RedirectFixedPath = false
engine.HandleMethodNotAllowed = true
engine.UseRawPath = true
engine.UseEscapedPath = false
engine.UnescapePathValues = false
engine.RemoveExtraSlash = false
engine.ContextWithFallback = false
engine.ForwardedByClientIP = false
engine.RemoteIPHeaders = nil
engine.TrustedPlatform = ""
if err := engine.SetTrustedProxies(nil); err != nil {
	return err // 初始化失败应显式终止，不静默回退
}
```

进程启动阶段还应一次性调用 `gin.SetMode(gin.ReleaseMode)`（测试用 `gin.TestMode`），因为 `mode.go:17-69` 会读取全局 `GIN_MODE`，`debug.go:32-69` 在 debug 模式输出非结构化 `[GIN-debug]` 路由注册日志。不得使用 `gin.Default()`：`gin.go:235-240` 会隐式安装 Gin Logger/Recovery，造成第二套日志和恢复语义。

### 3. 404/405 分支不能直接照搬 Gin 的默认 responder

1. **MethodNotAllowed**：开启 `HandleMethodNotAllowed` 后，Gin 只在当前 path 在其他 method tree 找到路由时进入 `NoMethod`，并预先设置 `Allow`；没有匹配 method 时进入 `NoRoute`（`gin.go:738-760`）。现有 Chi 对不支持的 method（不在 `CONNECT, DELETE, GET, HEAD, OPTIONS, PATCH, POST, PUT, QUERY, TRACE` method map）会在任意 path 直接调用全局 405（`chi/mux.go:464-471`）。因此若要完全保留 Chi，Gin 的全局前置 middleware 或 `NoRoute` 必须识别 unsupported method，并返回项目 405，而不是让未知路径变成静态 fallback/404。
2. **Allow header**：当前 `router.MethodNotAllowed` 和 `api.NotFound` 使用项目 `writeProblem`，Chi 的自定义 handler 不添加默认 `Allow`。Gin 的 `NoMethod` 应在确认基线后删除/覆盖 `Allow`，否则客户端会观察到新增 header。若产品决定采用 RFC 405 `Allow`，需单独记录为批准的契约变更并更新 OpenAPI/测试。
3. **Problem body**：`serveError` 在调用 `NoRoute`/`NoMethod` 前直接把 Gin 内部 status 设为 404/405，然后运行 handler；若 handler 未写 body，Gin 会追加纯文本默认 body（`gin.go:764-779`）。自定义 responder 必须始终使用现有 `writeProblem`，并在 handler 内 `c.Abort()`，避免默认 `404 page not found`/`405 method not allowed` 污染 Problem 格式。
4. **API 分类**：Gin 没有 Chi 的 group-scoped `NoRoute` API。单一 Engine `NoRoute` 需复刻当前分支：`/api/` 前缀返回 API `NOT_FOUND`，其余调用 `deps.Static`，nil static 返回 `WEB_ASSETS_UNAVAILABLE`（`internal/app/router.go:180-197`）。不要把 `/api`（无尾部 `/`）误分类为 API；当前代码明确只匹配 `/api/`。
5. **Auth 边界**：未知 API 不应被 Auth middleware 挑战，因为当前 protected group 只包住已注册路由；Gin 的全局 `NoRoute`/`NoMethod` 也不会自动运行 group Auth。迁移测试应确认未认证的未知 API 仍是 404，而不是 401/403。

### 4. 尾斜杠、清理和路径参数

- 配置 `RedirectTrailingSlash=false`、`RedirectFixedPath=false`、`RemoveExtraSlash=false` 后，`/api/v1/system/status/` 应按基线返回 API 404 Problem，不能出现 `Location`；`/livez/` 应走非 API static fallback（或 `WEB_ASSETS_UNAVAILABLE`），不能出现 301/307。
- Gin 的重定向实现还会读取 `X-Forwarded-Prefix` 并拼接到 `Location`（`gin.go:781-794`），即使该 header 未经过 proxy 信任判断；关闭重定向可以消除该 spoofing surface，但仍应有回归测试确保任何 malformed/double slash/case path 不生成 Location。
- Gin 在 `UseRawPath=true` 时使用 `URL.RawPath`，配合 `UnescapePathValues=false` 可保留 Chi 的编码参数语义（Gin tests `routes_test.go:680-717` 验证 `%2F` 值保持 `%2F`；默认 unescape 测试在 `routes_test.go:680-697`）。推荐在 HTTP adapter 内将 `c.Params` 写入 `c.Request.SetPathValue(name, value)`，再调用现有标准 `http.Handler`。Gin 的 `WrapF`/`WrapH` 只传递 `c.Writer,c.Request`（`utils.go:46-57`），不会设置 `PathValue`，不能单独作为兼容桥。
- 生产代码有 107 个 `chi.URLParam` 调用点（`rg -n "chi\\.URLParam" internal --glob '*.go'`）。应按组迁移为标准 `r.PathValue`（或一个只位于 HTTP adapter 的 helper），不要把 Gin Context 传入下层。若保留 `chi.URLParam` 兼容函数，需确认最终 AC-06 的 `rg 'chi\\.'` 无命中，因而推荐直接改为标准 `PathValue`。
- Chi 支持正则参数和 `*` catch-all（`chi/chi.go:40-54`）；Gin 的 `:name`/`*name` 语法和匹配边界不同。当前路由注册搜索没有发现生产正则/catch-all 注册，但迁移前仍应从全部 `register*Routes` 函数和 `Engine.Routes()` 生成冻结 inventory，逐条核对参数名、方法和尾斜杠。

### 5. 路由模板日志与 request context

- 当前 `requestRoutePattern` 只从 Chi RouteContext 取模板，且无匹配时返回空（`internal/app/router.go:542-552`）；测试明确要求日志是 `/api/v1/workspaces/{workspace_id}`、不能含实际 UUID 或 `path`（`internal/app/router_test.go:790-815`）。
- Gin 的 `c.FullPath()` 返回匹配模板、未匹配时为空（`context.go:171-179`；tests `routes_test.go:731-771`），但模板格式是 `:workspace_id`。建议在日志边界统一 canonicalize `:name -> {name}`（catch-all 也定义稳定映射），并且 FullPath 为空时保持空，绝不回退 `Request.URL.Path`。Recovery 也读取同一低基数模板。
- 如果有代码/第三方 handler 读取 `r.Pattern`，可在 adapter 中把 canonical Gin template 写入 `c.Request.Pattern`；Chi 在匹配成功时会设置 `Request.Pattern`（`chi/mux.go:476-481`）。当前仓库搜索未发现生产代码依赖 `r.Pattern`，这属于兼容加固而非日志必需。
- Gin Context 是池化并在请求后 reset/reuse（`gin.go:661-675`；`context.go:120-145` 的 `Copy` 说明异步场景）。不得把 `*gin.Context` 存入 goroutine、Application、Domain、Repository、Workflow 或持久化对象；异步工作只复制必要的标准 context/value。
- `requestIDMiddleware`、trace middleware、Auth principal 都必须更新 `c.Request`：`c.Request = c.Request.WithContext(ctx)`。仅调用 `c.Set` 会让现有 `request.Context().Value(...)` 读取不到值；`internal/auth/http/handler.go:129-141` 的 `PrincipalFromContext` 明确依赖标准 context。Trace/log post-work 应在 `c.Next()` 后从最新的 `c.Request.Context()` 读取。
- Gin middleware 的 `return` 不会自动停止链；`Context.Next/Abort` 语义见 `context.go:185-216`。Auth 或 dependency-unavailable failure 写完 Problem 后必须 `c.Abort()` 并 return，否则后续 route handler 仍可能执行。全局 `engine.Use` 会覆盖正常路由、NoRoute、NoMethod（`gin.go:325-344`；Gin tests `middleware_test.go:45-157`），所以顺序测试要覆盖 404/405/static。

### 6. ResponseWriter 与 response-started panic

这是迁移中最容易出现的行为回归。Gin `responseWriter` 的 `WriteHeader` 只更新暂存 `status`，不提交底层 response；真正提交由 `WriteHeaderNow`/`Write`/`Flush` 完成（`response_writer.go:61-88,98-108,128-134`）。特别是 `serveError` 直接写 `writermem.status=404/405`（`gin.go:764-779`）。因此：

- 直接用 `c.Writer.Written()` 判断“响应已开始”会错误地把 `WriteHeader(204)` 视为未开始，无法通过现有 `TestRecoverPanicMiddlewareDoesNotReplaceStartedResponse`（`internal/app/recovery_test.go:88-105`）。
- 直接用 `c.Writer.Status()!=200` 也不安全：NoRoute/NoMethod 在 handler 执行前已预加载 404/405，handler 尚未写出时 panic 应仍可改写为 500。
- 推荐在最外层 request-log/status middleware 安装显式 tracking wrapper，嵌入/委托 Gin `ResponseWriter`，覆写 `WriteHeader`、`Write`、`WriteString`、`Flush`（必要时 `Hijack`）并记录“handler/adapter 明确调用过写操作”的布尔值；恢复逻辑只看该标记，日志状态读取底层 Gin status。必须保留 `Unwrap` 和 Flusher/Hijacker/Pusher 等能力，否则 SSE、下载、WebSocket/升级链路会回归。
- 该 tracker 要区分 Gin 引擎在路由结束时的隐式 `WriteHeaderNow`（无 handler 输出，不算 panic 前 started）与 handler 明确 `WriteHeader(200/204)`、body、flush、stream。测试至少覆盖：panic 前无输出 ->稳定 500 Problem；`WriteHeader(204)`/`WriteHeader(200)` 后 panic ->保留状态和空/已有 body；`Write`/`Flush` 后 panic ->不追加 Problem；NoRoute 预加载 404 后立即 panic ->改写为 500。

不要使用 `gin.Recovery`/`gin.CustomRecovery` 作为项目 recovery：Gin 默认会记录 panic value 和（debug 模式）完整 request dump（`recovery.go:58-111`），只遮蔽 Authorization，且把 `http.ErrAbortHandler` 当 broken pipe 吞掉（`recovery.go:60-86`；对应测试 `recovery_test.go:131-153`）。项目要求精确 ErrAbortHandler re-panic、wrapped abort 作为普通 panic、bounded basename-only stack 和不泄露 URI/header/body，因此应把现有 `internal/app/recovery.go:17-47` 逻辑改写为 Gin middleware，并在捕获后 `c.Abort()`。

### 7. Static fallback 与 Gin StaticFS 的边界

现有 `webassets.Handler` 在 `ServeHTTP` 内完成 nil FS、`fs.ValidPath`、`/ -> index.html`、精确文件、GET extensionless SPA fallback 和最终 404（`internal/webassets/handler.go:42-63`；测试还覆盖 traversal 和 SPA）。迁移建议：

1. 只在 Engine `NoRoute` 中调用 `deps.Static.ServeHTTP(c.Writer, c.Request)`；不要调用 `engine.StaticFS`/`RouterGroup.StaticFS`。
2. Gin StaticFS 会注册 catch-all 路由并为 GET/HEAD 处理文件，缺失文件时还会改写 NoRoute；这会改变 `Engine.Routes()` inventory、HEAD 语义和 API/static 分支，且与当前 handler 的 extensionless fallback 不完全相同。
3. NoRoute 先判断 `/api/`，API 永远不能落到静态 fallback；非 API 才调用现有 handler。需要用 spy handler 验证 API 404 不触发 static，静态 handler panic/返回 404 时 tracker/recovery 仍按上节规则工作。

### 8. Trusted proxies / client IP

仓库搜索显示当前 API Router 没有使用 Gin `ClientIP` 或依赖 X-Forwarded-For 的业务逻辑；因此迁移不应凭空打开 proxy trust。Gin 官方注释明确默认信任所有 proxy，`SetTrustedProxies(nil)` 才关闭（`gin.go:443-453`）。推荐：

- `ForwardedByClientIP=false`、`RemoteIPHeaders=nil`、`TrustedPlatform=""`、`SetTrustedProxies(nil)` 全部显式设置；不要只设置其中一项。
- 若未来限流/审计需要代理后的真实 IP，应由部署配置提供精确 CIDR 白名单，启动时校验并记录；仅在可信上游网段内解析 XFF。新增配置前补充 spoofing/多跳 XFF 测试，不能依赖 Gin 默认。
- 当前重定向关闭后，`X-Forwarded-Prefix` 不会影响响应 Location；仍应测试该 header 不改变 404/405/static 输出，以防未来打开 redirect 时重新引入信任边界问题。

### 9. 推荐的 middleware / adapter 顺序

Gin 的全局链应按现有 Chi 外层语义注册，且每个需要前后处理的 middleware 都调用 `c.Next()`：

```text
no-store
  -> request ID (更新 c.Request.Context + X-Request-ID)
  -> trace/correlation (更新 c.Request.Context + traceparent)
  -> request log + explicit response-start tracker
  -> project panic recovery (safe log, response-start aware)
  -> route dispatch / NoRoute / NoMethod
  -> protected group Auth (仅 protected routes，失败 Abort)
  -> standard net/http handler adapter (PathValue bridge)
```

保留 `cmd/api/main.go` 中现有进程级 workspace/producer gates 的外层顺序；Gin Engine 作为唯一 API Router 插入原有 composition 点，不要在 Engine 外再挂一套 Chi。Adapter 负责把 `c.Params` 映射到 `Request.SetPathValue`、必要时设置 `Request.Pattern`，然后调用原有 `http.Handler`；Application/Domain 继续只收标准 `context.Context` 和 DTO。

## Recommended Verification

以下测试应在实现阶段落地，先跑核心包再扩展到全仓：

1. **Engine config**：断言所有上节字段、`SetTrustedProxies(nil)` 和 `gin.New`（无 Logger/Recovery）；进程模式只在 bootstrap 设置，测试并行时不修改全局模式。
2. **Route inventory**：以 `engine.Routes()` 归一化 method/path，与冻结的 OpenAPI 182 operations 对比；单独核对 `/livez`、`/readyz`、可选 `/metrics`、NoRoute static，不因 `StaticFS`/隐式 HEAD 增加路由。
3. **404/405 matrix**：已知 path 错 method、未知 API、非 API static、`/api`（无尾 `/`）、unsupported method、HEAD/OPTIONS；断言 status、Problem JSON、Content-Type、body、Allow 和无意的 Gin default text。
4. **Path matrix**：尾斜杠、双斜杠、大小写、`%2F`/`%252F`、空参数、UUID；断言无 Location、参数值与 Chi 基线相同、`Request.PathValue` 可用。
5. **Middleware order/abort**：用事件 slice 验证正常、NoRoute、NoMethod、static 的进入/退出顺序；Auth 失败后下游 handler 不执行，Principal 只在标准 request context 可见。
6. **Logging/correlation**：动态 UUID、Cookie、Authorization、request body 不进日志；`http_route` 使用 brace 模板，NoRoute/NoMethod route template 为空；request ID/traceparent 在 handler、span、completion log 一致。
7. **Recovery matrix**：无输出 panic、显式 204/200 WriteHeader 后 panic、Write/Flush 后 panic、NoRoute 预加载 404 后 panic、精确/包装 ErrAbortHandler；验证 stable Problem、re-panic 和 bounded safe stack。
8. **Static**：exact asset、`/`、GET extensionless SPA、HEAD、traversal、API bypass、nil static、static handler panic；保持现有 `webassets` tests 的 status/header/body。
9. **Proxy**：不同 `RemoteAddr` + 伪造 XFF/X-Real-IP/Forwarded-Prefix；关闭 trust 时 ClientIP/响应不受 header 影响，未来显式 CIDR 时再验证可信链。
10. **Focused commands**：实现后先执行 `go test ./internal/app ./internal/auth/http ./internal/webassets ./internal/httpapi`、`go vet` 受影响包和 `git diff --check`；再按 PRD AC-07 扩展全仓/race/OpenAPI/Compose 门禁。

## External References

- Gin v1.12.0 module cache（已核对的发布源码，`go 1.25.0`）：`$GOMODCACHE/github.com/gin-gonic/gin@v1.12.0/go.mod`、`gin.go`、`context.go`、`response_writer.go`、`recovery.go`、`routergroup.go`、`utils.go` 及对应 `routes_test.go`/`middleware_test.go`/`recovery_test.go`。
- Upstream tag mirrors（用于实现时复核，不替代本地版本）：[Gin v1.12.0 gin.go](https://github.com/gin-gonic/gin/blob/v1.12.0/gin.go)、[Context](https://github.com/gin-gonic/gin/blob/v1.12.0/context.go)、[ResponseWriter](https://github.com/gin-gonic/gin/blob/v1.12.0/response_writer.go)、[Recovery](https://github.com/gin-gonic/gin/blob/v1.12.0/recovery.go)。
- Chi v5.3.1 module cache：`$GOMODCACHE/github.com/go-chi/chi/v5@v5.3.1/mux.go`、`chi.go`、`tree.go`、`context.go` 及 `mux_test.go`；upstream tag [Chi v5.3.1 mux.go](https://github.com/go-chi/chi/blob/v5.3.1/mux.go)。
- Gin 官方 API 文档/注释重点：`Engine.SetTrustedProxies(nil)` 关闭默认全量 proxy trust（`gin.go:443-453`）；`Engine.ContextWithFallback` 控制 Context 委托（`context.go:1433-1482`）。

## Related Specs

- `.trellis/spec/backend/auth-security.md`：Session、Bearer、CSRF/Origin、Capability 与失败闭环；Gin Auth adapter 不得改变这些规则。
- `.trellis/spec/backend/error-handling.md`：Problem Details、稳定 error code/retryable 和 HTTP 边界错误映射。
- `.trellis/spec/backend/logging-guidelines.md`：slog JSON、低基数路由、脱敏、Trace/Request ID 关联。
- `.trellis/spec/backend/quality-guidelines.md`：跨层回归、SSE/文件传输、race/vet 和安全质量门禁。
- `.trellis/spec/backend/index.md:99-120`：开发前读取任务 PRD/design/implement、领域层不得依赖 HTTP 框架的边界。
- `.trellis/tasks/08-11-gin-http-migration/prd.md`：R1/R2/R4/R5 及 AC-01/02/03/05/06/07 是本研究建议的验收来源。

## Caveats / Not Found

- 本研究只读；未修改 `internal/`、`go.mod`、OpenAPI 或任何 spec。当前任务 `design.md`、`implement.md` 尚未创建，本文建议需要在规划阶段转化为明确步骤和可回滚点。
- 当前仓库没有发现专门覆盖尾斜杠重定向、`RedirectFixedPath`、静态 fallback 与 Router 组合、trusted proxy spoofing、`Request.PathValue` 编码或 Gin response-started panic 的现成回归测试；这些是迁移前必须新增的行为基线，不应视为已验证。
- 当前代码直接调用 `chi.URLParam` 107 处；“只在 adapter 做 PathValue bridge”仍要求逐组将 handler 读取改为 `Request.PathValue`，否则 AC-06 的 Chi 清理无法关闭。生产路由中的正则/catch-all 注册未在快速搜索中发现，但需以完整 route inventory 最终确认。
- Gin 的 package-global mode (`GIN_MODE`/`gin.SetMode`) 会影响所有 Engine 和并行测试；不能在请求级或每个测试随意切换。若现有测试依赖 debug 输出，迁移时要把测试 logger 改为显式注入，而不是恢复 `gin.Default()`。
- 由于当前 Router 没有使用 ClientIP，本文没有提出具体可信代理 CIDR；部署拓扑改变时需单独做安全评审和配置决策。
