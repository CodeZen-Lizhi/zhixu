# Research: `/api/v1/events` 对浏览器原生 EventSource 的契约适配

- Query: 核对当前 GET/Cookie/Auth、Workspace query、Last-Event-ID、Problem JSON、命名事件、proxy/cache 契约，比较 query cursor、preflight 与最小 Fetch adapter，并给出兼容方案。
- Scope: mixed（仓库实现、项目规范、WHATWG/MDN/Nginx 官方文档）
- Date: 2026-08-10

## Findings

### Files Found

| 文件 | 作用 |
| --- | --- |
| `internal/events/http/handler.go` | `/events` 路由、请求解析、游标恢复、SSE 编码、心跳和 Problem 映射。 |
| `internal/events/http/handler_test.go` | 请求严格性、400/409、fresh watermark、重放、命名事件和响应头单测。 |
| `internal/events/http/handler_postgres_integration_test.go` | 真实 PostgreSQL 的跨 Workspace 隔离、游标过期/未来与重放。 |
| `internal/events/domain/cursor.go` | 正十进制 int64 replay cursor 与 retained window 校验。 |
| `internal/events/domain/event.go` | Event/Envelope 字段、类型、引用和 16 KiB payload summary 上限。 |
| `internal/app/router.go` | `/api/v1` 认证分组和 Events 路由注册。 |
| `internal/auth/http/handler.go` | Session Cookie/Bearer 优先级、GET 安全方法、Capability 和认证 Problem。 |
| `cmd/api/main.go` | 生产 composition、Workspace gate 包装及 HTTP timeout。 |
| `cmd/api/workspace_runtime_gate.go` | 长连接不计入 Workspace root quiescence。 |
| `deploy/compose.yml` | 本地入口是 `socat` TCP 透传，不是有 HTTP buffer/cache 的反向代理。 |
| `web/vite.config.ts` | 开发环境 `/api` 同源代理。 |
| `api/openapi/openapi.json` | 当前公开契约只声明 `workspace_id` query 和 `Last-Event-ID` header。 |
| `web/src/events/server-events.ts` | 当前 Fetch stream、严格 SSE parser、Problem 解码和重连实现。 |
| `web/src/events/event-store.tsx` | 异步 Query invalidation 完成后才持久化 Workspace cursor。 |
| `.trellis/spec/frontend/state-management.md` | 单连接 owner、ack-after-invalidation、恢复和 Workspace 切换硬约束。 |

### 1. 当前后端契约

#### 路由、Cookie 与认证

- Events 通过 `GET /api/v1/events` 注册在 domain routes 中（`internal/events/http/handler.go:64-67`、`internal/app/router.go:211-240`）。认证启用时，domain routes 全部位于 `Auth.Middleware` 后（`internal/app/router.go:160-179`）。
- GET 自动要求 `READ_LOCAL` Capability（`internal/auth/http/handler.go:148-180`）。若存在 `Authorization`，Bearer 是唯一认证来源；否则读取 `zhixu_session` Cookie（`internal/auth/http/handler.go:612-650`）。因此浏览器原生路径应明确使用 Session Cookie，不能依赖给 EventSource 增加 Bearer Header。
- GET/HEAD/OPTIONS 被视为 safe method，不要求 `Origin` 或 `X-CSRF-Token`（`internal/auth/http/handler.go:628-650`）。Session Cookie 是 `HttpOnly`、`Path=/`、`SameSite=Strict`，按配置决定 `Secure`（`internal/auth/http/handler.go:25-30, 442-443`）。
- 缺失或失效凭据在进入 Events Handler 前返回 `401 AUTH_UNAUTHORIZED`、`WWW-Authenticate` 和 `Cache-Control: no-store`（`internal/auth/http/handler.go:662-681`）。Capability 不足返回 403。
- 项目没有 CORS middleware；生产 Web/API 同一 Go 进程同源提供，本地开发通过 Vite `/api` proxy 保持浏览器同源（`web/vite.config.ts:17-26`）。当前支持边界不是跨源 EventSource。

结论：同源原生 `EventSource` 可以携带 Session Cookie，GET 不触发 CSRF 要求。`withCredentials: true` 只应在未来明确支持跨源 Cookie+CORS 时使用；当前应使用相对 URL 和默认 same-origin credentials。

#### Workspace query 与重放起点

- `parseReplayRequest` 当前要求 query map **恰好一个 key**，即唯一且 canonical 的 `workspace_id`；缺失、重复、额外参数、非 canonical UUID 都返回 `400 SSE_WORKSPACE_INVALID`（`internal/events/http/handler.go:241-258`）。
- replay cursor 当前只能来自唯一非空的 `Last-Event-ID` Header（`internal/events/http/handler.go:260-270`）。没有 Header 时，服务端读取当前 Workspace watermark，从“连接建立以后”开始，不重放旧事件（`internal/events/http/handler.go:109-116`）。
- cursor 是 canonical positive int64；前导零、零、负数、溢出无效。大于 watermark 返回 `400 SSE_CURSOR_FUTURE`；早于 retained window 返回 `409 SSE_CURSOR_EXPIRED`（`internal/events/domain/cursor.go:15-38`）。
- replay、watermark 和 earliest-retained 查询始终携带 Workspace ID；真实 PostgreSQL 测试覆盖 A/B 隔离（`internal/events/http/handler_postgres_integration_test.go:30-89`）。

结论：新建原生 EventSource 的内部 last-event-ID 初始为空，构造器又不能设置 Header；若要从 `sessionStorage` 的已确认 cursor 恢复，后端必须增加一个严格的 bootstrap query cursor，或保留 Fetch 作为连接实现。

#### 成功响应、命名事件和连接行为

- 成功前会完成 cursor 校验和首个 replay page 读取；之后才返回 200。Headers 是 `Content-Type: text/event-stream`、`Cache-Control: no-store`、`X-Content-Type-Options: nosniff`、`X-Accel-Buffering: no`（`internal/events/http/handler.go:69-104`）。
- 每条 frame 当前固定为 `id: <seq>`、`event: <business.type>`、`data: <Envelope JSON>`（`internal/events/http/handler.go:220-231`）。业务 event type 是开放的受限 token，而不是一个完整、封闭的前端枚举（`internal/events/domain/event.go:20-24, 121-147`）。
- 原生 EventSource 对带 `event:` 的记录只分发同名事件，不会同时触发 `message`。Web API 没有 wildcard listener。静态维护所有业务事件名会让新增后端事件被静默漏掉，因此不是可维护方案。
- Handler 每秒 poll，15 秒发送 comment heartbeat 并 Flush（`internal/events/http/handler.go:30-33, 165-194`）。Go Server 没有 `WriteTimeout`；30 秒 `ReadTimeout` 只约束请求读取，活动响应流不受 60 秒 keep-alive `IdleTimeout` 截断（`cmd/api/main.go:140-144, 699-706`）。
- Workspace runtime gate 明确不跟踪该长连接，Workspace runtime 撤销时由旧 API container 关闭连接（`cmd/api/workspace_runtime_gate.go:69-80`）。前端仍必须在 A -> B 时主动关闭 A。

结论：原生客户端需要一个固定 DOM event 名。为保持现有 `/v1` 命名事件消费者兼容，应提供 opt-in message 格式，而不是全局把 `event: <business.type>` 改成 `message`。

#### Problem JSON 与流启动后的错误

| 条件 | 当前 HTTP 结果 | 原生 EventSource 可见性 |
| --- | --- | --- |
| 无/失效 Session | 401 `AUTH_UNAUTHORIZED` | 只有 `error`；无 status/body |
| Capability 不足 | 403 auth Problem | 只有 `error`；无 status/body |
| Workspace/query 非法 | 400 `SSE_WORKSPACE_INVALID` | 只有 `error`；无 status/body |
| cursor 非法/未来 | 400 `SSE_CURSOR_INVALID|FUTURE` | 只有 `error`；无 status/body |
| cursor 过期 | 409 `SSE_CURSOR_EXPIRED`, `details.action=refetch` | 只有 `error`；无 status/body |
| Store/Handler 不可用 | 503 `SSE_STORE_UNAVAILABLE|SERVICE_UNAVAILABLE` | 只有 `error`；无 status/body |
| 首页成功 | 200 event stream | `open` + MessageEvent |
| 200 后 poll/write/数据错误 | TCP/HTTP body 结束 | `error`；不能再发送 HTTP Problem |

SSE Handler 自己写出的 400/409/503 目前没有统一设置 `Cache-Control: no-store`；只有成功流和 auth failures 明确设置。迁移时应在 Handler 入口先设置 no-store，使所有 Events 响应一致；Fetch probe 也必须使用 `cache: "no-store"`。

### 2. 浏览器标准行为与项目硬约束的冲突

WHATWG HTML 的关键行为：

- 构造器只有 URL 和 `withCredentials`；没有 method/header 选项。初始 last event ID string 是空值。
- 浏览器在重新建立**同一个 EventSource 对象**的连接时，才把内部 last event ID 写入 `Last-Event-ID` Header。
- 解析一条记录时，浏览器先更新 EventSource 内部 last event ID，再排队分发 MessageEvent。也就是说它早于项目的异步 `onEvent` 完成。
- 非 200 或非 `text/event-stream` 会 fail connection；公开接口只有 `open/message/error/readyState`，`error` 事件不暴露 Response status 或 Problem body。
- EventSource 请求 cache mode 由标准设为 `no-store`；同源默认 credentials mode 会发送同源 Cookie。

项目现有硬约束：

- 所有 Query invalidation/权威回查成功后才能提交 cursor；失败必须重放同一事件（`.trellis/spec/frontend/state-management.md:148-165`）。
- `event-store.tsx` 实际在 `await invalidateEventQueries(event)` 后才写 `sessionStorage`（`web/src/events/event-store.tsx:314-320`）。
- 当前 connector 也在 `await onEvent` 后才更新内存 cursor（`web/src/events/server-events.ts:646-653`），并使用可控的 500 ms 到 15 s 指数退避+jitter（`web/src/events/server-events.ts:568-570, 668-677`）。

因此“纯原生自动重连”存在不可消除的窗口：浏览器已经把 ID N 作为下次 Header，而 N 的 Query invalidation 仍可能失败。若此时连接中断，浏览器会从 N 以后恢复，违反“失败重放 N”。浏览器的 reconnection time 也是实现定义值，无法证明等价于现有有界 jitter 策略。

这不是只增加 query cursor 就能修复的问题。除非产品明确放宽 ack-after-invalidation 和确定性重连契约，否则项目必须保留最小的应用层 reconnect owner。

### 3. 方案比较

| 方案 | 覆盖 | 关键缺口 | 结论 |
| --- | --- | --- | --- |
| A. 后端不改，直接 `new EventSource` | Cookie GET、浏览器 parser、heartbeat、同对象重连 | 无初始 Header；命名事件无 wildcard；Problem 不可见；内部 cursor 过早推进 | 不可用 |
| B. 增加 query cursor + 固定 message，完全交给浏览器重连 | 解决初始恢复和事件分发 | 401/400/409 不可分类；ack 时序和现有 jitter 仍不满足 | 不可单独采用 |
| C. 新增 preflight endpoint + 原生自动重连 | 初次连接前可读 Problem | 重连时仍可能出现状态变化；ack 时序仍不满足；新增 API 面 | 不能单独采用 |
| D. 原生 EventSource parser + 应用控制重连 + 最小 Fetch probe | 删除自研 UTF-8/SSE frame parser；保留严格 Envelope、Problem、ack、Workspace 与 backoff | 不能删除全部重连代码；错误时多一次短 GET | **推荐** |
| E. 保持当前 Fetch stream | 完整行为基线 | 没有 TODO 12 的维护收益 | 作为 rollout/rollback 路径 |

#### Preflight 的具体取舍

- 独立 `GET /api/v1/events/preflight` 可以返回 204/Problem，不建立长流；但它新增 OpenAPI、Handler、测试和 TOCTOU 边界，且仍需与真正 stream 的 `prepare` 逻辑严格复用。
- 复用现有 Events GET 做 probe：只在 EventSource `error` 后发 Fetch；非 200 解码现有 Problem，200 校验 Content-Type 后立即 cancel body。它会多做一次有界 replay read，并短暂建立/取消一个流，但不增加 API surface，且只发生在错误恢复路径。
- MVP 推荐复用现有 GET probe。只有监控证明错误探针造成明显数据库/连接噪声时，再引入共享 `prepare` 的专用 preflight endpoint。

### 4. 推荐后端兼容契约

建议保持默认行为完全兼容，并让原生客户端显式 opt in：

```text
GET /api/v1/events
  ?workspace_id=<canonical UUID>          # required, exactly one
  &last_event_id=<canonical positive i64> # optional bootstrap cursor
  &event_format=message                   # optional native mode
```

推荐语义：

1. query 仍拒绝未知 key、重复 key、空值和非 canonical 值。
2. replay source 优先级固定为：有效且唯一的 `Last-Event-ID` Header > `last_event_id` query > fresh watermark。
3. Header 和 query 同时存在且值不同是原生同对象重连的正常状态：URL 中 bootstrap cursor 不变，浏览器 Header 前进；必须由 Header 覆盖，不能报冲突。
4. 即使 Header 存在，也验证 query shape，避免 malformed URL 被静默接受；只是不使用 query cursor 作为 replay 起点。
5. 默认格式继续输出 `event: <business.type>`，保护现有 Fetch/curl/外部命名事件消费者。
6. `event_format=message` 输出 `id:` + `data:`，省略 `event:`，让浏览器统一分发 `message`；业务类型只从严格 Envelope `type` 读取。
7. 原生客户端比较 `MessageEvent.lastEventId === Envelope.id`，继续校验 Workspace、ID 单调和 Envelope shape。
8. `Cache-Control: no-store` 在进入 Handler 时即设置，覆盖 200、400、409、500、503；保留 `X-Accel-Buffering: no`、`nosniff`、15 秒 heartbeat 和 Flush。

推荐前端/探针交互（供跨层设计使用）：

1. 新 EventSource URL 带最后**已确认**的 `last_event_id` 和 `event_format=message`。
2. `message` 进入串行处理队列；Envelope decode、Workspace/ID 校验和 Query invalidation 全部成功后，才更新 ack cursor。
3. 任一消息处理失败立即 `close()`；用旧 ack cursor 重连，确保该消息重放。
4. `error` 时先 `close()`，阻止浏览器使用可能超前的内部 cursor 自动重连；等待已排队处理收敛，再沿用当前有界 backoff+jitter 创建新对象。
5. 需要区分 401/400/409/5xx 时，Fetch probe 同一 URL并以 ack cursor 设置 `Last-Event-ID`；非 200 严格解码现有 Problem，200 立即取消 body。
6. 401 继续调用 `invalidateAuthSession`；400 invalid/future 与 409 expired 继续走现有权威 Workspace recovery；5xx/network 继续 retryable；业务页面不感知 transport。

这个方案让浏览器接管 UTF-8 解码、CR/LF、comment、multi-line data 和 DOM MessageEvent 分发；项目只保留无法由 Web 标准表达的 ack/recovery/backoff/Problem adapter。它不会把内部 EventSource cursor 当成项目 cursor。

### 5. Proxy、Cache 与部署结论

- 官方 Compose 的 `proxy` 是 `socat TCP-LISTEN -> TCP`（`deploy/compose.yml:141-157`），不解析 HTTP，因此不会缓存或按 HTTP body buffering；成功流的 Flush 可透传。
- Vite 开发代理已有真实浏览器 smoke 使用，但仓库未找到针对“原生 EventSource 首帧延迟/自动重连 Header”的专门断言。实现后应补真实浏览器测试。
- 自托管反向代理仍需显式禁用 `/api/v1/events` response buffering/cache，并把 idle/read timeout 设为大于 15 秒 heartbeat。`X-Accel-Buffering: no` 对 Nginx 有效，但 Nginx 官方文档说明可被 `proxy_ignore_headers` 禁用，不能只靠响应头推断所有代理都正确。
- 不应增加 `Connection: keep-alive`：HTTP/2 不允许该 hop-by-hop header，Go/代理会自行管理连接。

### 6. 兼容、测试与回滚风险

#### 必测契约

- Backend unit：query 缺失/重复/未知、bootstrap cursor 非 canonical、Header 优先于不同 query cursor、fresh watermark、message/legacy 两种 frame。
- Backend auth/router：Session Cookie GET 无 Origin/CSRF 成功；缺 Cookie 401；低 Scope 403；所有 Events errors 为 no-store。
- Backend integration：真实 PostgreSQL query cursor replay、expired/future、A/B Workspace 隔离、Header-over-query reconnect。
- Frontend unit：MessageEvent metadata/envelope mismatch、串行 invalidation、失败不 ack、probe 的 401/400/409/503、Abort、backoff、Workspace switch。
- Browser：同源 HttpOnly Cookie；刷新后 bootstrap cursor；断网/进程重启；409 recovery；logout 401；A -> B 旧连接关闭；Vite 与 Compose proxy 下首帧不被缓冲；同一 Workspace 仅一条活动 SSE。
- OpenAPI：新增 query 参数与兼容模式，但保留 `Last-Event-ID` Header 和默认 legacy frame 的描述。

#### 风险与控制

- **公开 wire 兼容**：直接删除业务 `event:` 会破坏命名事件消费者。用 opt-in `event_format=message` 隔离。
- **游标越过失败事件**：允许原生对象自动重连会违反项目 ack 约束。error/message failure 时关闭对象，用持久 ack 创建新对象。
- **错误失真**：只看 `onerror` 会把 auth/cursor/server failure 混为网络错误。保留严格 Fetch probe。
- **帧上限变化**：当前前端 parser 有 32 KiB frame 上限和 fatal UTF-8；原生 API 不暴露原始 frame size。服务端 payload summary 已有 16 KiB文档上限和字段上限（`internal/events/domain/event.go:80-102`），实现时还应在 encoded frame 上加/锁定上限测试，再删除客户端 frame guard。
- **跨源误用**：当前无 CORS，Cookie 为 SameSite Strict。Transport 应默认相对同源 URL；不得以 `withCredentials` 宣称跨源已支持。
- **probe 负载**：错误路径会多一次有界 store read。先用指标/测试验证；若成为热点，再迁移到共享校验 preflight。
- **回滚**：保留 transport factory/显式 rollout 开关，使前端能回到当前 Fetch connector；后端新增 query/message mode 是向后兼容的，可暂留。不要在同一页面同时开 EventSource 与 Fetch 两条连接。

### External References

- [WHATWG HTML: Server-sent events](https://html.spec.whatwg.org/multipage/server-sent-events.html)：构造器、请求 cache mode、reconnect、Last-Event-ID、解析/分发时序、非 200 fail connection。
- [WHATWG Fetch: credentials mode](https://fetch.spec.whatwg.org/#concept-request-credentials-mode)：默认 same-origin credentials 会包含同源 Cookie。
- [MDN: EventSource constructor](https://developer.mozilla.org/en-US/docs/Web/API/EventSource/EventSource)：公开构造参数只有 URL 与 `withCredentials`。
- [MDN: Using server-sent events](https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events/Using_server-sent_events)：默认 `message` 与命名事件监听方式。
- [Nginx proxy buffering](https://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_buffering)：默认 buffering、`X-Accel-Buffering` 与 `proxy_ignore_headers` 行为。

### Related Specs

- `docs/roadmap.md:79-83`：TODO 12 要求先验证 Cookie/GET、Last-Event-ID、Workspace、auth、cursor 和 proxy，并允许最小 Fetch adapter。
- `docs/architecture/application-contracts.md:61-71`：SSE 不是事实源，invalidation 完成后推进 cursor，Workspace 单 owner。
- `.trellis/spec/backend/auth-security.md:18-40`：Cookie、safe method、Bearer 优先、Capability 与 auth failure matrix。
- `.trellis/spec/backend/error-handling.md:27-56`：稳定 Problem code 与 SSE recovery。
- `.trellis/spec/frontend/state-management.md:129-182`：统一 SSE owner、ack-after-invalidation、recovery_failed、Workspace switch。
- `.trellis/spec/frontend/type-safety.md:99-108`：raw SSE 唯一 decoder owner、成功处理后才推进 Last-Event-ID。

## Caveats / Not Found

- 当前任务 `prd.md` 仍为 TBD；本研究把现有架构文档和 Trellis specs 当作不可放宽的已批准约束。若产品愿意接受 at-most-once invalidation 窗口或浏览器实现定义的重连节奏，需在 design review 中显式修改契约，不能隐式发生。
- 仓库内未发现跨源 CORS 配置、原生 EventSource 的状态码/Problem 浏览器测试，或自托管 Nginx/Caddy 的正式配置；这些不能标记为已验证。
- 未发现仓库外 `/api/v1/events` 消费者清单。正因无法证明没有外部命名事件消费者，推荐保留默认 legacy frame。
- 原生 EventSource 在不同浏览器对网络错误的退避细节由实现决定；WHATWG 允许额外 backoff。不能用单一浏览器结果证明确定性等价。
