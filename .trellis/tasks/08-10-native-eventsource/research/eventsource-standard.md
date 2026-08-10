# Research: 浏览器原生 EventSource 能力、限制与当前 SSE 契约适配

- Query: 研究当前 Web 标准 EventSource 的 GET/Cookie/CORS、命名事件、首次连接与自动重连的 Last-Event-ID、HTTP 状态/响应体可见性、retry/退避、close/readyState，并对照 ZHIXU 当前 SSE 契约给出可落地方案与风险。
- Scope: mixed（WHATWG/MDN 官方标准资料 + 当前仓库实现）
- Date: 2026-08-10

## Findings

### 1. 结论

原生 `EventSource` 可以可靠接管以下通用能力：

- `GET` 长连接、`text/event-stream` MIME 校验、UTF-8/换行/注释/多行 `data` 解析；
- `open`、`message`/命名事件、`error` 和 `CONNECTING|OPEN|CLOSED` 连接状态；
- 同一个 `EventSource` 实例内，网络断开后的标准重连，以及由服务端 `retry:` 设置的基础重连时间；
- 浏览器在自动重连时，根据该实例内部的 last event ID 自动发送 `Last-Event-ID`。

它不能直接覆盖当前项目的全部强约束：

1. 新建实例的 last event ID 固定从空字符串开始；构造器没有首次 `Last-Event-ID`、自定义 Header、Method、Body 或 `AbortSignal` 选项。
2. 浏览器在把事件排入 JS 事件队列前就更新内部 last event ID，不能等待项目异步 `onEvent`/Query invalidation 成功后再推进。
3. `error` 是普通 `Event`，不暴露 HTTP status、response headers 或 Problem JSON，无法直接区分当前的 `401`、`400 cursor invalid/future`、`409 cursor expired`、`503` 与错误 MIME。
4. 标准重连初始时间是浏览器实现定义值；浏览器还可以额外等待或自行指数退避，项目无法精确保持当前 `500ms -> 15s + jitter` 策略。
5. `event:` 会按精确名称派发；没有命名事件通配监听。当前服务端把业务 `event.Type` 写成动态事件名，`onmessage` 接收不到这些帧。

因此，**在当前 wire 不变时直接把 Fetch connector 替换成 `new EventSource(...)` 会造成恢复缺口和错误语义退化**。可落地方案需要至少增加“首次游标传递”契约，并保留一个只负责错误诊断/特殊认证的最小 Fetch seam；异步游标提交则需要有界、串行的内存队列和失败回卷规则。

### 2. Web 标准能力矩阵

| 维度 | Web 标准保证 | 对当前项目的结论 |
| --- | --- | --- |
| 请求 | `EventSource(url, {withCredentials})` 只暴露 URL 与 credentials 选项；底层 Request 默认 `GET`、无 body。请求可由浏览器设置 `Accept: text/event-stream`，cache mode 为 `no-store`。 | `/api/v1/events` 已是 GET，方法适配；调用方不能设置 Bearer、自定义 Workspace Header 或首次 `Last-Event-ID`。 |
| Cookie | 默认 credentials mode 是 `same-origin`；`withCredentials: true` 才把跨源 credentials mode 设为 `include`。 | 当前同源 Session Cookie GET 可原生覆盖；CSRF Header 不适用于安全 GET。跨源仍受 CORS、Cookie `SameSite`/`Secure` 规则约束。 |
| CORS | EventSource 请求使用 CORS；`withCredentials` 只决定是否携带跨源凭据，不会替服务端生成 CORS 响应。 | 仓库未找到 `Access-Control-Allow-Origin`/`Access-Control-Allow-Credentials` 响应中间件，且 Session Cookie 是 `SameSite=Strict`。原生默认路径应限制为同源；不要把 `withCredentials: true` 当作跨源支持。 |
| 命名事件 | 无 `event:` 时派发 `message`；有 `event: foo` 时只向 `foo` 类型监听器派发。 | 当前动态事件名不能用单一 `onmessage` 捕获。建议服务端省略 `event:` 或改成固定 transport event 名，业务类型继续只由严格 Envelope 的 `type` 字段承载。 |
| 首次 Last-Event-ID | 每个新实例的内部 last event ID 初始为空，构造器无 setter/seed 参数。 | 页面刷新后从 `sessionStorage` 恢复的应用游标无法作为首次 Header 传入。必须增加 URL query seed（Header 在自动重连时优先），或该场景继续用 Fetch Adapter。 |
| 自动重连 Last-Event-ID | 同一实例重连时，若内部 last event ID 非空，浏览器设置 `Last-Event-ID` Header。 | 网络恢复可以交给浏览器，但该值表示“浏览器已解析/排队”，不是“项目异步 invalidation 已完成”。必须维护 observed 与 committed 两个水位，并保留未提交事件的有界内存队列。 |
| HTTP 错误 | network error 通常进入重连；非 200 或非 `text/event-stream` 会 fail connection；204 可用于停止重连。JS 仅收到 `error`。 | `readyState=CONNECTING` 可映射普通网络重连；`readyState=CLOSED` 只能说明 fatal/显式 close，不能分辨 401/400/409/503。Fatal 后需 Fetch 诊断或新控制端点。 |
| retry/退避 | 初始 reconnection time 为实现定义值（通常数秒）；`retry: <digits>` 改写基础毫秒值；UA 可额外等待、指数退避或等网络恢复。 | 可以接受标准策略并放宽精确时序要求，或在 `error` 时 close 后继续项目自管退避；两者不能同时宣称由浏览器完整接管。 |
| 状态 | `CONNECTING=0` 表示初连或自动重连；`OPEN=1`；`CLOSED=2` 表示 fatal 或调用了 `close()`。 | 可稳定映射到项目 `connecting/open/reconnecting/closed`；`recovery_failed` 仍是项目状态。初连与重连都为 CONNECTING，wrapper 必须记录是否曾 open。 |
| close | `close()` 中止该实例的 fetch 并同步设置 CLOSED；已关闭时 no-op；不会返回完成 Promise，也没有独立 close 事件。 | Workspace switch/logout 可调用 `close()`；当前 `done` Promise、幂等清理与 generation guard 仍由 wrapper 提供。 |

### 3. 关键标准细节

#### 3.1 GET、Cookie 与 CORS

- WHATWG 的 `EventSourceInit` 只有 `withCredentials`；Fetch Request 未显式覆盖 Method 时默认 `GET`。因此脚本无法给原生 EventSource 注入 `Authorization`、`Last-Event-ID`、CSRF 或其他 Header。
- 默认 `withCredentials=false` 对应 `credentials mode=same-origin`，所以同源 HttpOnly Session Cookie 会随请求发送；跨源需要 `{withCredentials:true}`，还必须由服务端正确完成 credentialed CORS。
- 当前前端开发入口通过 Vite `/api` proxy 保持同源（`web/vite.config.ts:19-25`）；认证 Session Cookie 为 `HttpOnly`、`SameSite=Strict`（`internal/auth/http/handler.go:392-395`）。
- 当前 Events 路由位于认证 Middleware 保护组内（`internal/app/router.go:160-179, 211-240`），GET 需要 `READ_LOCAL`（`internal/auth/http/handler.go:159-172`）。Cookie 模式适合原生 EventSource；OpenAPI 虽同时允许 API Bearer，但原生 EventSource 无法设置 Bearer Header。

结论：默认只对 `resolvedEventsURL.origin === window.location.origin` 的浏览器 Cookie/disabled-auth 场景选择原生实现；跨源、Bearer 或将来出现的自定义 Header 场景走 Fetch Adapter，除非另有经过安全评审的 CORS/认证契约。

#### 3.2 命名事件没有通配监听

标准按 `event:` 的值改变 `MessageEvent.type`；默认事件名才是 `message`。当前服务端输出：

```text
id: <seq>
event: <event.Type>
data: <Envelope JSON>
```

见 `internal/events/http/handler.go:224-242`。`event.Type` 是满足 pattern 的开放字符串，不是浏览器端封闭枚举（`internal/events/domain/event.go:20-23, 105-126`），所以维护一份 `addEventListener(type)` 列表会遗漏将来新增事件。

推荐 wire：保留 `id:` 与 `data:`，省略 `event:`，让所有业务通知成为 `message`；Envelope `type` 继续是唯一业务事件类型并由 `decodeServerEventEnvelope` 严格校验。备选是固定 `event: server-event`，但没有额外收益。

#### 3.3 首次连接与自动重连的 Last-Event-ID 不同

WHATWG 明确规定：

- 新建 `EventSource` 的 last event ID string 初始为空；
- 只有“reestablish the connection”流程才根据内部值设置 `Last-Event-ID`；
- 构造器只接受 URL/withCredentials，没有初始游标参数。

当前项目在 `sessionStorage` 按 Workspace 保存 committed cursor，并在新 connector 创建时传入（`web/src/events/event-store.tsx:294-320, 328-371`）。服务端如果没有游标，会把连接起点直接设为当前 watermark，不补历史（`internal/events/http/handler.go:138-142`）。所以刷新后忽略已保存游标会跳过“保存游标之后、重连 watermark 之前”的事件。

可落地的 wire 扩展：

- 新增可选 query，例如 `last_event_id=<positive-decimal>`，仅作为**新实例首次连接 seed**；
- 浏览器自动重连时 URL 仍带旧 seed，同时 UA 会带更晚的 `Last-Event-ID` Header，因此服务端必须定义 **Header 优先，query 仅在 Header 缺失时生效**，不能把二者同时存在判为重复输入；
- query cursor 仍执行与 Header 完全相同的 canonical/invalid/future/expired 校验；cursor 不是 Secret，但会进入 URL/访问日志，仍不得复用该机制传凭据；
- 更新 OpenAPI、handler tests、代理/日志说明。当前 parser 强制 query 只有 `workspace_id`，必须修改（`internal/events/http/handler.go:245-268`）。

#### 3.4 浏览器游标推进早于异步业务提交

WHATWG dispatch algorithm 先把 EventSource 内部 last event ID 设置成 frame 的 `id`，然后创建 `MessageEvent`，最后才 queue task 派发给 JS。它不会等待 listener 返回的 Promise。

当前项目的明确契约却是：`onEvent` 可异步，只有所有 Query invalidation 成功后才提交 cursor；失败必须重放同一事件（`.trellis/spec/frontend/state-management.md:77-80`）。当前 Fetch loop 通过 `await options.onEvent(event)` 后再更新 `lastEventId` 实现该约束（`web/src/events/server-events.ts:645-653`）。

若希望保留浏览器自动重连，wrapper 必须维护以下不变量：

```text
observed cursor = 浏览器已派发、可能尚未处理的最大 ID
committed cursor = onEvent 成功且已持久化的最大 ID

(committed, observed] 中的每个事件
要么仍在有界、顺序的内存处理队列中，
要么在失败/溢出时通过 committed cursor 重建 EventSource 后重放。
```

要求：

- `message` listener 只做同步 strict decode、Workspace/ID 单调校验和 enqueue；实际 `onEvent` 串行执行；
- 每个成功项才更新 committed cursor/sessionStorage；
- 任一 decode/onEvent 失败时立即 `close()` 当前 generation，丢弃后续 generation callback，并从 committed query seed 重建；
- 队列设置明确上限；溢出也 close + 从 committed 重放，避免原生 EventSource 缺少 backpressure 导致无界内存；
- 页面卸载/Workspace switch 时未提交队列不得写回新 scope；新页面仍从旧 committed cursor 重放。

这是保持现有 at-least-once 应用语义且仍使用 UA 网络重连的必要适配，不是可删除的业务状态机。

#### 3.5 HTTP status/Problem 对 JS 不可见

标准内部会根据 status 和 MIME 决定 reconnect 或 fail，但 `error` callback 收到的是 generic `Event`。JS 可读取 `source.readyState`，不能读取失败响应的 status、headers 或 body：

- `CONNECTING` + `error`：通常表示 UA 将自动重连；映射项目 `reconnecting`；
- `CLOSED` + `error`：fatal（非 200、错误 MIME、UA 判定重连无意义等），但无法区分原因；
- 显式 `close()` 也变成 CLOSED，所以 wrapper 必须用自身 closed flag 避免把主动关闭误判为 fatal。

当前 Fetch connector 依赖响应可见性完成：

- 401 调 `invalidateAuthSession()`（`web/src/events/server-events.ts:602-615`）；
- 409 `SSE_CURSOR_EXPIRED + action=refetch`（`:617-624`）；
- 400 invalid/future cursor（`:625-631`）；
- 其他 Problem 的 `retryable` 与 5xx 分类（`:633-637`）；
- 200 MIME/body 校验（`:639-644`）。

最小 Fetch seam 可采用“fatal 后诊断”而不是继续解析 SSE：

1. `EventSource.onerror` 且 `readyState === CLOSED` 时，等待/冻结当前处理队列并关闭实例；
2. 用 committed cursor 对相同 endpoint 发一次 credentialed Fetch，沿用现有 Problem strict decoder；
3. 若为 401/400/409，执行现有 auth/recovery 分支；若为 5xx，按项目 fatal-retry 策略重建；
4. 若 Fetch 返回 200 + 正确 MIME，立即 cancel body，不解析帧，再用 query seed 创建新的 EventSource。

该探测有一次额外连接和 TOCTOU 风险，但 endpoint 的 prepare/read 没有写副作用，且新 EventSource 从同一 committed cursor 重放，不丢事件。更干净但影响面更大的替代方案是新增非流式 cursor/auth validation endpoint。不要把 401/409 改成 200 SSE control event，这会破坏既有 HTTP、安全与 OpenAPI 语义。

#### 3.6 retry、退避与代理

- 标准 initial reconnection time 是 implementation-defined，通常为数秒；服务端 `retry: <ASCII digits>` 可设置毫秒值。
- UA 被允许在此基础上再等待、指数退避，或等待 OS 网络恢复。应用无法读取或设置 max/jitter。
- 当前服务端不发送 `retry:`，只发送事件和每 15 秒 heartbeat comment（`internal/events/http/handler.go:30-33, 172-190, 239`）。15 秒 comment 与 WHATWG authoring note 的代理 keepalive 建议一致。
- 当前项目自管 `500ms` 起、`15s` 上限、`0.8..1.2` jitter（`web/src/events/server-events.ts:568-570, 668-677`）。原生迁移后不能用单测断言同样的精确 delay。

设计必须二选一并写入验收：

- **标准优先**：网络 error 保持实例存活，由 UA 重连；服务端可发送一个固定 `retry:` 基线，但接受浏览器额外 backoff，测试行为而非精确毫秒数。
- **精确策略优先**：收到 error 后 `close()`，由 wrapper 定时新建 EventSource；这仍删除低层 SSE parser，但不能宣称“浏览器接管重连”。

建议本任务选标准优先；仅对 fatal/消费失败保留项目重建逻辑。

#### 3.7 close、readyState 与项目状态映射

推荐映射：

| 原生信号 | 项目状态 |
| --- | --- |
| 构造实例、尚未首次 open | `connecting` |
| `open` | `open` |
| `error` 且 readyState=CONNECTING | `reconnecting` |
| fatal cursor/auth 诊断期间 | `reconnecting` 或现有 recovery flow |
| 权威恢复失败 | `recovery_failed` |
| wrapper 主动 close / fatal non-retryable | `closed` |

`close()` 同步中止连接并置 CLOSED，但没有 done signal；现有 `ServerEventConnection.done` 需要由 wrapper 在主动关闭、fatal 终止或 recovery_failed 终止时 resolve。Workspace generation guard 与 cleanup 顺序继续由 Event Store 拥有。

### 4. 原生 parser 与当前严格 frame parser 的行为差异

当前手写 parser 不只是实现 Web 标准，还额外实施了项目策略：

- fatal UTF-8 decoder；
- 32 KiB frame character limit；
- `id`/`event` 必填且禁止重复；
- 未知 SSE field 直接拒绝；
- frame `id/event` 必须与 JSON Envelope 相等。

见 `web/src/events/server-events.ts:141, 372-474`。WHATWG parser 则会忽略未知 field、按标准覆盖/积累 buffer，并没有项目可配置的 frame size limit。

迁移时应保留的最小校验：

- `event.data.length <= 32 KiB` 后再 `JSON.parse`；
- strict `decodeServerEventEnvelope(unknown)` 保持不变（`web/src/events/server-events.ts:329-370`）；
- `MessageEvent.lastEventId === envelope.id`、Workspace binding、observed/committed 单调性；
- JSON/decode 失败触发 close + committed replay，不把坏事件提交。

无法等价保留的部分是对 comment/未知 frame field 总长度的应用级限制，以及 fatal UTF-8 解码。服务端自身已严格生成单行、有界 Envelope；若安全评审仍要求客户端精确 frame-level fail-closed，则该场景必须继续走 Fetch parser，原生 EventSource 不能提供原始字节/行。

### 5. 可落地方案比较

#### 方案 A：协议小扩展 + 原生默认 + 最小 Fetch 诊断（推荐）

1. 保持 `connectServerEvents`、`ServerEventConnection`、Event Store snapshot/recovery 接口不变。
2. 服务端支持首次 `last_event_id` query seed，自动重连时 Header 优先；事件 frame 改为 data-only/default `message`。
3. 原生 adapter 负责 framing、连接状态和普通网络自动重连；项目层保留 strict Envelope decoder、有界串行消费队列、committed cursor、Workspace generation 和恢复策略。
4. 仅在原生 fatal CLOSED 时用 Fetch 读取 status/Problem；200 时 cancel body，SSE 帧仍由 EventSource 解析。
5. 非同源、Bearer/custom-header、缺少 EventSource 或需要精确 frame fail-closed 的场景保留 Fetch Adapter。

优点：生产同源 Cookie 主路径删除手写行级 parser，并让 UA 接管普通网络重连；保留当前业务恢复语义。缺点：需前后端/OpenAPI 一起改，增加一次 fatal 诊断请求，并新增有界消费队列。

#### 方案 B：保持 wire，仅在“无已保存游标”的首次连接用原生

一旦存在 session cursor、发生 fatal error 或需要恢复，就回落现有 Fetch stream。

优点：后端改动小。缺点：刷新后的常见路径仍是 Fetch，长期维护两套完整 stream parser/connector，净优化很低；不建议作为最终状态。

#### 方案 C：完全采用 UA cursor，把消费失败改为全量权威恢复

把浏览器 last event ID 视为 transport receipt，不再要求失败时重放同一事件；任一 `onEvent` 失败都执行完整 Workspace REST recovery。

优点：客户端最简单。缺点：直接改变 `.trellis/spec/frontend/state-management.md:77-80` 的 at-least-once 契约，恢复成本更高，且 HTTP 错误不可见问题仍存在。必须得到产品/架构明确批准，不应作为“纯优化”静默实施。

### 6. 建议验收与测试

- Server：query seed 无 Header、UA Header 覆盖旧 query seed、invalid/future/expired、无 cursor 从 watermark、data-only frame、heartbeat、取消释放。
- Adapter unit：构造 URL/credentials、默认 message、strict decode、`lastEventId`/Envelope mismatch、Workspace mismatch、observed 单调、串行处理、失败从 committed replay、队列溢出、主动 close、generation 隔离。
- Error matrix：network error + CONNECTING 由 UA 重连；401/400/409/503/错误 MIME + CLOSED 经 Fetch 诊断进入原有分支；诊断 200 必须 cancel body。
- Event Store：只有 invalidation/recovery 成功后写 session cursor；Workspace A->B 先 close A 且旧队列不能写 B；recovery_failed 保留 cursor。
- 真实浏览器门禁：至少 Chromium 验证首次 query cursor、断网恢复时浏览器实际发送 `Last-Event-ID`、命名/default event、Session Cookie、401 logout、409 recovery、Workspace switch 后只有一条连接。jsdom/fake EventSource 不能证明 UA Header 和重连行为。
- 退避测试改为状态与最终恢复断言；若采用标准优先，不再断言浏览器的精确 500ms/15s/jitter。

### 7. Files Found

| 文件 | 说明 |
| --- | --- |
| `web/src/events/server-events.ts` | 当前 strict Envelope decoder、手写 frame parser、Problem decoder、Fetch/reconnect owner。关键实现 `:329-474, :482-507, :560-688`。 |
| `web/src/events/server-events.test.ts` | 当前 401、Last-Event-ID、cursor recovery、消费失败重放、Abort 语义基线。关键用例 `:248-503`。 |
| `web/src/events/event-store.tsx` | Workspace 唯一连接、权威 recovery、异步 invalidation 后 session cursor commit。关键实现 `:54-150, :294-388`。 |
| `internal/events/http/handler.go` | GET SSE handler、watermark/replay、heartbeat、frame encoding、Header-only cursor parser。关键实现 `:63-101, :110-161, :164-242, :245-285`。 |
| `internal/events/http/handler_test.go` | invalid/future/expired/retention race 与 stream 行为基线。 |
| `internal/events/domain/event.go` | 开放 event type pattern、有界 PayloadSummary、Envelope binding。关键实现 `:20-23, :47-102, :105-173`。 |
| `internal/events/domain/cursor.go` | 正十进制 cursor 与 invalid/future/expired 校验。关键实现 `:7-38`。 |
| `internal/app/router.go` | Events 位于认证保护的 `/api/v1` domain routes。关键实现 `:160-179, :211-240`。 |
| `internal/auth/http/handler.go` | Session/Bearer 认证、GET READ_LOCAL、SameSite Strict Cookie。关键实现 `:111-131, :159-180, :392-395`。 |
| `api/openapi/openapi.json` | `/api/v1/events` 只定义 Workspace query + `Last-Event-ID` Header；全局安全同时允许 Session Cookie/API Bearer。关键位置 `:3143-3173, :11379-11393`。 |
| `web/vite.config.ts` | 开发环境 `/api` 同源 proxy。关键实现 `:19-25`。 |
| `.trellis/spec/frontend/state-management.md` | 异步消费成功后提交 cursor、失败重放、恢复与有界退避的稳定契约。关键位置 `:77-80, :129-180`。 |
| `.trellis/spec/frontend/type-safety.md` | SSE strict decoder 与唯一 boundary owner 规则。关键位置 `:21-31, :107-110`。 |
| `docs/architecture/application-contracts.md` | SSE 仅 invalidation、REST 权威回查、单 Workspace owner 与 cleanup 顺序。关键位置 `:61-71, :84-99`。 |
| `docs/roadmap.md` | TODO 12 目标与“必要时保留最小 Fetch Adapter”边界。关键位置 `:79-83`。 |
| `docs/architecture/adr/0010-sse-for-server-events.md` | 已批准 SSE/Last-Event-ID/自动重连方向，SSE 非事实源。 |

### 8. External References

- [WHATWG HTML: EventSource interface](https://html.spec.whatwg.org/multipage/server-sent-events.html#the-eventsource-interface) — 构造器、withCredentials、初始 reconnection time、初始空 last event ID、close/readyState。
- [WHATWG HTML: EventSource processing model](https://html.spec.whatwg.org/multipage/server-sent-events.html#processing-model) — response status/MIME、network reconnect、fatal error、Last-Event-ID Header、UA 可追加 backoff。
- [WHATWG HTML: Last-Event-ID header](https://html.spec.whatwg.org/multipage/server-sent-events.html#last-event-id) — Header 只用于 EventSource reestablish connection。
- [WHATWG HTML: interpreting an event stream](https://html.spec.whatwg.org/multipage/server-sent-events.html#interpreting-an-event-stream) — `event/data/id/retry`、未知 field、内部 ID 更新与异步事件派发顺序。
- [WHATWG Fetch: Request defaults](https://fetch.spec.whatwg.org/#requests) — Request 默认 GET、空 Header list、null body。
- [WHATWG HTML: potential-CORS request](https://html.spec.whatwg.org/multipage/urls-and-fetching.html#create-a-potential-cors-request) — anonymous 为 same-origin credentials，use-credentials 为 include。
- [MDN: EventSource constructor](https://developer.mozilla.org/en-US/docs/Web/API/EventSource/EventSource) — 构造器只有 URL 与 withCredentials。
- [MDN: EventSource error event](https://developer.mozilla.org/en-US/docs/Web/API/EventSource/error_event) — error 类型为 generic Event。
- [MDN: EventSource readyState](https://developer.mozilla.org/en-US/docs/Web/API/EventSource/readyState) — CONNECTING/OPEN/CLOSED 三态。
- [MDN: EventSource close](https://developer.mozilla.org/en-US/docs/Web/API/EventSource/close) — close 行为与幂等语义。
- [MDN: Using server-sent events](https://developer.mozilla.org/en-US/docs/Web/API/Server-sent_events/Using_server-sent_events) — 命名事件、comments、multi-line data、id、retry、跨源 credentials 与连接限制。

### 9. Related Specs

- `.trellis/spec/frontend/state-management.md`：M6-04/M9 cursor commit、recovery、Workspace owner 的强约束。
- `.trellis/spec/frontend/type-safety.md`：SSE raw input 只能在 Events 边界 strict decode。
- `.trellis/spec/frontend/quality-guidelines.md`：SSE reconnect、Workspace A/B cleanup、真实浏览器验证要求。
- `.trellis/spec/guides/cross-layer-thinking-guide.md`：本任务涉及 Browser API -> HTTP wire -> Go handler -> Event Store 四层契约。
- `.trellis/spec/guides/code-reuse-thinking-guide.md` 与 `docs/architecture/adr/0019-mature-framework-first.md`：标准 API 覆盖成熟能力时优先使用；缺口必须明确记录最小自研边界。

## Caveats / Not Found

- 当前工作树正在进行架构文档重组，`docs/product/2026-08-01-requirement-optimization-list.md` 与旧 `docs/architecture/api-and-events.md` 在工作树中显示为删除；本研究使用现存 `docs/roadmap.md`、新 `application-contracts.md` 与代码/OpenAPI 作为当前事实，没有恢复或修改这些用户变更。
- 搜索 `internal/`、`cmd/`、`web/` 未找到设置 `Access-Control-Allow-Origin`/`Access-Control-Allow-Credentials` 的通用 CORS 中间件。`AuthAllowedOrigins` 是 unsafe 请求的 Origin/CSRF allowlist，不等价于 CORS 响应配置。
- 未运行真实浏览器网络实验；UA Header、status-fatal 与 backoff 结论来自 2026-07/08 可访问的 WHATWG Living Standard 与 MDN。实施阶段仍应通过真实 Chromium network trace 验证部署代理行为。
- 标准没有应用可配置的 frame byte/character 上限，也不提供原始流字节。若客户端精确 32 KiB frame 限制与 fatal UTF-8 是不可放宽的安全要求，原生 EventSource 无法达到等价覆盖。
- HTTP/1.x 下浏览器通常对同一 origin 的 EventSource 连接数有限；项目已经要求每个 Active Workspace 单 owner，但多个 Tab 仍可能各有一条连接。HTTP/2 可缓解，是否需要 SharedWorker 属于独立范围。
