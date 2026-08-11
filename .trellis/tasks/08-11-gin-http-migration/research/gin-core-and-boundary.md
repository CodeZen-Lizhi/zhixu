# Research: Gin 核心 Router 与 HTTP 边界摘要

- Query: 汇总 Chi v5.3.1 到 Gin v1.12.0 的核心 Router/Middleware 迁移结论，供 `design.md`、`implement.md` 直接引用。
- Scope: mixed（仓库实现、Trellis 规格、Gin/Chi 版本化源码）
- Date: 2026-08-11

## Findings

完整逐行证据、源码位置、外部引用和验证矩阵见 [`core-router-middleware.md`](./core-router-middleware.md)。本文件只保留规划阶段不能遗漏的边界。

### Files found

- `internal/app/router.go:100-197`：当前唯一 Chi Composition Root、全局 middleware 顺序、API/Auth 分组、404/405 与 static fallback。
- `internal/app/router.go:485-552`：request ID、response status tracker、低基数 route template 日志。
- `internal/app/recovery.go:17-74`、`internal/app/recovery_test.go:88-146`：安全 panic recovery、response-started、ErrAbortHandler 和脱敏栈契约。
- `internal/auth/http/handler.go:96-141`：Auth group、失败短路和标准 `request.Context()` Principal。
- `internal/webassets/handler.go:42-63`：精确静态文件、GET SPA fallback、路径拒绝和最终 404。
- `$GOMODCACHE/github.com/gin-gonic/gin@v1.12.0/{gin.go,context.go,response_writer.go,recovery.go,routergroup.go,utils.go}`：Gin v1.12.0 官方发布源码。
- `$GOMODCACHE/github.com/go-chi/chi/v5@v5.3.1/{mux.go,chi.go,tree.go,context.go}`：当前 Chi v5.3.1 路由基线。

### Required engine configuration

必须使用 `gin.New()`，不使用会隐式安装 Logger/Recovery 的 `gin.Default()`。建议显式设置：

```text
RedirectTrailingSlash=false
RedirectFixedPath=false
HandleMethodNotAllowed=true
UseRawPath=true
UseEscapedPath=false
UnescapePathValues=false
RemoveExtraSlash=false
ContextWithFallback=false
ForwardedByClientIP=false
RemoteIPHeaders=nil
TrustedPlatform=""
SetTrustedProxies(nil)
```

Gin 默认开启尾斜杠 301/307、默认信任全部 proxy、默认对路径参数解码，均与当前契约不等价。进程 bootstrap 应一次性设置 release mode，测试使用 test mode；Gin mode 是 package-global，不能在请求或并行测试中反复修改。

### Required compatibility behavior

1. `NoRoute` 复刻当前根分支：只有 `/api/` 前缀返回 API `NOT_FOUND`，其他路径调用现有 `deps.Static`；`/api` 无尾斜杠仍属于 static 分支。不要使用 Gin `StaticFS`。
2. `NoMethod` 返回项目 Problem。Gin 会新增 `Allow` header，而当前自定义 Chi responder 不会；若要求严格对等，需要删除该 header。Chi 对 unsupported method + 任意 path 返回 405，Gin 默认会对未知 path 进入 NoRoute，需额外兼容。
3. 尾斜杠、大小写、双斜杠和 `X-Forwarded-Prefix` 都不得产生 301/307/Location。
4. Gin route adapter 在调用标准 `http.Handler` 前，把 `c.Params` 写入 `Request.SetPathValue`；`gin.WrapH/WrapF` 本身不会这样做。仓库有 107 个 `chi.URLParam` 调用点，最终应迁为 `Request.PathValue`。
5. 日志读取 `c.FullPath()` 后把 Gin `:param` canonicalize 成现有 `{param}`，无匹配时保持空，绝不回退实际 URL path。
6. request ID、trace 和 Principal 都写入 `c.Request = c.Request.WithContext(ctx)`；Gin Context 不进入 Application/Domain/Repository/Workflow，也不能跨请求保存。
7. 全局 middleware 保持 `no-store -> request ID -> trace -> request log/status tracker -> project recovery`；Auth 仅挂 protected group，失败写 Problem 后必须 `c.Abort()`。
8. 不使用 Gin Recovery。它会记录 panic value/request 信息并吞掉 `http.ErrAbortHandler`，不满足现有安全契约。
9. Recovery 不能只看 `c.Writer.Written()` 或 `Status()!=200`。Gin `WriteHeader` 延迟提交，而 NoRoute/NoMethod 又预加载 404/405；必须用显式 wrapper 记录 handler 的 `WriteHeader/Write/WriteString/Flush`，同时保留 Flusher/Hijacker/Pusher/Unwrap 能力。

### Minimum verification gate

- Engine config + trusted proxy spoofing 测试。
- Runtime `Engine.Routes()` 与冻结 OpenAPI inventory 对比，并单测 health/optional metrics/static。
- 404/405/unsupported method/Allow/default body/尾斜杠/Location 表驱动测试。
- `%2F`、`%252F` 和 `Request.PathValue` 参数对等测试。
- 正常、NoRoute、NoMethod、static、Auth failure 的 middleware order/abort 测试。
- route template 脱敏、request ID/trace/principal/cancellation 传播测试。
- panic 前无输出、显式 200/204、Write/Flush、NoRoute 预加载 404、精确/wrapped ErrAbortHandler 测试。
- static exact file、SPA GET、HEAD、traversal、API bypass、nil dependency、panic 测试。

## External References

- [Gin v1.12.0 gin.go](https://github.com/gin-gonic/gin/blob/v1.12.0/gin.go)
- [Gin v1.12.0 Context](https://github.com/gin-gonic/gin/blob/v1.12.0/context.go)
- [Gin v1.12.0 ResponseWriter](https://github.com/gin-gonic/gin/blob/v1.12.0/response_writer.go)
- [Gin v1.12.0 Recovery](https://github.com/gin-gonic/gin/blob/v1.12.0/recovery.go)
- [Chi v5.3.1 mux.go](https://github.com/go-chi/chi/blob/v5.3.1/mux.go)

## Related Specs

- `.trellis/spec/backend/auth-security.md`
- `.trellis/spec/backend/error-handling.md`
- `.trellis/spec/backend/logging-guidelines.md`
- `.trellis/spec/backend/quality-guidelines.md`
- `.trellis/tasks/08-11-gin-http-migration/prd.md`

## Caveats / Not Found

- 本研究未修改产品代码、依赖或规格；`design.md`、`implement.md` 尚需把上述边界转成分批步骤和回滚点。
- 当前缺少尾斜杠、trusted proxies、路径参数编码、Router/static 组合和 Gin response-started panic 的专项基线测试，不能假定默认行为等价。
- 未发现生产正则/catch-all 路由，但必须由完整 route inventory 最终确认；快速搜索不能替代运行时对比。
