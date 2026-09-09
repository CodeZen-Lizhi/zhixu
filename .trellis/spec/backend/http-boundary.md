# Gin HTTP 边界规范

## 1. 适用范围与触发条件

本规范适用于 `internal/app` 的 HTTP Composition Root、`internal/httpapi` 的 Gin/标准库桥接、各
`internal/*/http` 路由注册、认证 Middleware、SSE、上传下载和 panic recovery。新增或修改公开路由、路径参数、
全局 Middleware、404/405、流式响应、OpenAPI operation 或 Gin 依赖时，必须读取并执行本规范。

Gin 类型只能出现在 Composition/HTTP adapter 边界。Domain、Application、Repository、Workflow 和持久化契约继续使用
领域类型、`context.Context` 或小 Interface；不得为了方便把 `*gin.Context` 向内传递。

## 2. 稳定签名与事实源

- 唯一生产入口是 `internal/app.NewRouter(Dependencies) *gin.Engine`，必须使用 `gin.New()`。
- HTTP 模块通过 `Routes(gin.IRouter)`、`OpenRoutes(gin.IRouter)` 或 `ProtectedRoutes(gin.IRouter)` 注册。
  响应合同需要新版本时，以显式 `RoutesV2(gin.IRouter)` 注册到 Composition Root 的 `/api/v2` 组；
  当前只有 Conversation/Answer 与 Interview/Path Step 的十个 operation 使用该入口。
- 既有标准库 Handler 通过 `httpapi.GinHandler(http.HandlerFunc)` 接入；业务 Handler 从
  `request.PathValue` 和 `request.Context()` 取路径参数、Principal、request ID 与 trace。
- 公开 method/path 的事实源是 `api/openapi/openapi.json`；`internal/app/router_inventory_test.go` 必须精确比较
  Gin runtime inventory 与 OpenAPI operation。operation 数量由该可执行 inventory 派生，不在本规范维护易漂移的
  固定数字；只有依赖存在时的 `/metrics` 是额外可选运行时路由。
- 路径参数名以 OpenAPI snake_case 为准；Gin `:workspace_id` 只在注册层出现，日志和 OpenAPI 使用
  `{workspace_id}` canonical 形式。

## 3. 必须保持的契约

1. Engine 必须显式关闭尾斜杠、固定路径和多余斜杠重定向，启用 405 检测，保留 raw/escaped path 基线，并禁用
   forwarded client IP 与 trusted proxy；不得依赖 Gin 默认值。
2. 不使用 `gin.Default()`、Gin Recovery、自动写响应的 `Bind*`、Gin SSE renderer、multipart convenience API 或
   StaticFS 改写项目已有行为。严格 JSON、Problem、SSE wire、上传限制和下载 Header 仍由项目 helper/Handler 拥有。
3. 全局 Middleware 顺序固定为 no-store、request ID、trace、request log、panic recovery；认证成功后的 Principal
   必须写回标准 `Request.Context()`，认证失败写 Problem 后必须 `Abort()`。
4. 404/405、静态 fallback 和不支持的 HTTP method 必须输出项目稳定 Problem；禁止 Gin 默认正文、隐式 redirect 或
   未批准的 `Allow` Header。
5. Gin 同一路由树分支的 wildcard 名必须一致。新增路由前先统一 OpenAPI 参数名，否则注册阶段可能 panic；
   `GinHandler` 必须同步写入同名 `Request.PathValue`。
6. 路由日志只记录 canonical route template，不记录实际 path、path value、凭据、请求正文或 panic 值。
7. recovery 必须区分 response started 状态；已写 Header/body 或 Flush 后不得追加 500 Problem。判断
   `http.ErrAbortHandler` 前必须防止对不可比较的 panic 动态值执行接口比较，且日志只保留有界、去路径的 stack。
8. SSE 开始响应前必须确认最底层 `http.ResponseWriter` 实际支持 `http.Flusher`。Gin 和项目 wrapper 自身实现
   `Flush()` 不等于底层可以 flush；检查通过后仍通过最外层 writer 刷新，以保持 tracker 状态。
9. `/api/v2` 复用 v1 的认证、Origin、CSRF、Capability 和 Workspace 校验；只注册确实需要版本化的 operation。
   Handler 用值副本固定版本，不能让路由注册顺序或并发请求改变共享状态，也不能整体重写 `/api/v1` 前缀。
10. HTTP 版本只限定表示与准入，不进入领域 request hash、幂等 receipt 或来源身份。v1 不能表示的新资源在
    ETag/304、命令 replay 和新副作用前返回稳定 409；跨 Workspace 仍先返回统一 404。列表在 SQL LIMIT 前按
    冻结 Definition/来源过滤，不能先取一页再在 HTTP 丢弃条目。版本化列表的 cursor 不可跨版本复用。

## 4. 验证与错误矩阵

| 条件 | 必须结果 |
| --- | --- |
| runtime/OpenAPI method/path 缺失、额外或重复 | inventory 测试失败；不得以手工清单豁免 |
| 同一路由分支使用不同 wildcard 名 | 统一为 OpenAPI snake_case，并补 `NewRouter` 不 panic 回归 |
| 尾斜杠、大小写或固定路径可能触发 Gin redirect | 返回既有 404/405 Problem，不产生 301/307 |
| 认证或安全 Middleware 拒绝请求 | 写稳定 Problem、`Abort()`，Application 不得被调用 |
| 严格 JSON 出现未知字段、多文档、非法 Unicode 或超限 | 由 `httpapi.DecodeJSON` fail closed；不得退回 Gin binding |
| panic 发生在响应开始前 | 写一次脱敏 500 Problem 并记录有界 stack |
| panic 发生在 Header/body/Flush 后 | 只记录并中止，不追加或重写响应 |
| panic 值为 slice/map 等不可比较类型 | recovery 自身不得二次 panic，不得泄露值 |
| SSE 最底层 writer 不支持 flush | 在 200/SSE Header 前返回 `SSE_STREAMING_UNSUPPORTED` |
| `/metrics` handler 未注入 | 不注册；OpenAPI/runtime 精确比较仍通过 |
| v2 缺凭据、错误 Capability、Cookie 缺 Origin/CSRF | 同 v1 的稳定认证/权限 Problem，不进入领域写操作 |
| v1 读取或重放动态 Analysis / NOTE Interview | `409 CONVERSATION_API_VERSION_UNSUPPORTED` / `INTERVIEW_API_VERSION_UNSUPPORTED`，不新增业务事实 |
| v1/v2 Turn 或 Interview cursor 混用 | `400 CONVERSATION_CURSOR_INVALID` / `INTERVIEW_CURSOR_INVALID`，切换端点从第一页开始 |

## 5. Good / Base / Bad Cases

- Good：新增 `/workspaces/{workspace_id}/...` operation 时，同时更新 OpenAPI、Gin `:workspace_id` 注册和 Handler
  `PathValue("workspace_id")`，inventory 与边界测试一起通过。
- Base：普通 JSON Handler 继续接收 `http.ResponseWriter`/`*http.Request`，Gin 只负责路由、分组和 Middleware 编排。
- Good：动态 Analysis 与 NOTE 响应放入独立 v2 operation/schema；旧列表保持完整分页，旧事实在两个版本中保留同一身份。
- Bad：为清除 OpenAPI breaking 报告而只恢复 schema，却仍让 v1 Handler 返回新版字段或在写入后才拒绝响应。
- Bad：在 Application test 直接构造 `gin.New()`，让 Application 包依赖框架；应通过生产 `app.NewRouter` 做 HTTP smoke，
  或把纯 Application 测试留在无 HTTP 的服务接口。
- Bad：看到 `writer.(http.Flusher)` 成功就开始 SSE。Gin wrapper 总能满足该断言，仍需沿 `Unwrap()` 验证底层 writer。
- Bad：直接写 `recovered == http.ErrAbortHandler`。panic 值可能是不可比较类型，接口比较会让 recovery 再次 panic。

## 6. 必需测试与门禁

修改路由或共享边界后至少运行：

```bash
GIN_MODE=test go test -count=1 -timeout 60s ./internal/app ./internal/httpapi ./internal/auth/http
GIN_MODE=test go test -race -count=1 -timeout 60s ./internal/app ./internal/httpapi ./internal/auth/http
make openapi-check
OPENAPI_BASE_REVISION=<40-character-lowercase-commit-sha> make openapi-breaking-check
go vet ./...
go build ./cmd/api
go mod tidy -diff
go list -mod=vendor ./...
git diff --check
```

版本化回归由 `internal/app/router_v2_test.go` 覆盖全部十个 operation 的认证、Capability、Cookie Origin/CSRF、
Auth unavailable 与未注册路径；Conversation/Interview 的 `http/api_version_test.go` 和 PostgreSQL 版本/来源测试
覆盖严格旧响应、跨版本 cursor、LIMIT 前过滤及拒绝时零副作用。生成客户端升级还须通过 `make openapi-generate-check`。

涉及领域 HTTP、SSE、上传或下载时，追加对应 `internal/*/http` 包的普通测试和 race；涉及真实 PostgreSQL、Docker 或浏览器
流程时按任务影响面运行 Integration/Compose/E2E。环境不具备时必须记录命令、失败原因与剩余风险，不能把 compile-only
或定向测试描述为全仓通过。

`make openapi-check` 固定组合 Spectral、项目 checker 与 inventory；它不替代历史兼容性判断。修改已提交公开
wire 时必须另以显式完整 40 位小写 SHA 执行 `make openapi-breaking-check`，禁止分支名、短 SHA、自动 baseline、
ignore 或降低 severity。PR/push base 由 CI 事件提供，受保护分支的 required check 和显式 breaking bypass 属于
GitHub 外部保护配置，需由管理员核验。

静态边界检查至少包括：

```bash
rg -n 'github\.com/go-chi/chi|\bchi\.' --glob '!vendor/**' .
rg -n 'github\.com/gin-gonic/gin' internal \
  --glob '**/domain/**' --glob '**/application/**' --glob '**/adapter/**'
rg -n 'gin\.Default|\.Bind(JSON|XML|YAML|TOML|Query|Header|Uri)?\(' internal
```

预期 Chi 与低层 Gin 检查无输出；Gin 自动 binding/Default 命中必须逐项删除或给出已有契约批准证据。

## 7. Wrong vs Correct

```text
Wrong: 路由测试只断言几个核心 endpoint 能返回 2xx。
Correct: 从 OpenAPI 解析完整 method/path 集合，与 Engine.Routes 精确比较，并单独覆盖可选 metrics 和 static fallback。

Wrong: Gin Context 保存认证信息，Application 从 *gin.Context 读取。
Correct: HTTP Middleware 把 Principal 写入 Request.Context，Application 只依赖标准 context/领域接口。

Wrong: 使用 Gin 默认 redirect、Recovery 或 binding，再为响应差异增加兼容 fallback。
Correct: gin.New 显式配置，复用项目 strict decoder、Problem、tracker 和 recovery，一条生产路由路径到底。

Wrong: wrapper 实现 Flusher 就假定 SSE 可流式传输。
Correct: 沿 Unwrap 验证最底层 writer 支持 Flusher，再通过外层 writer 写入和 Flush。

Wrong: 根据 result.schema_version 判断 v1 是否可读，或返回旧字段前删掉一页中的新条目。
Correct: 按不可变 Workflow Definition/面试来源限定 HTTP 表示，在 LIMIT、ETag 和命令副作用前执行版本边界。
```
