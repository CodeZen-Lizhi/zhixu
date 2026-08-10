# Research: 前端 SSE 迁移到浏览器原生 EventSource

- Query: 研究当前前端 SSE 实现迁移到浏览器原生 `EventSource` 的影响范围，提出最小接口、保留/删除逻辑、命名事件方案和兼容风险。
- Scope: mixed
- Date: 2026-08-10

## Findings

### 1. 结论摘要

建议采用“原生 `EventSource` 主连接 + 仅用于 fatal 握手分类的短生命周期 Fetch 诊断”方案，同时保持 `EventStoreProvider -> connectServerEvents` 的现有所有权边界。不能只在 `server-events.ts` 中把 `fetch()` 换成 `new EventSource()`；要保住当前项目契约，至少还需要两个服务端协议调整和一个客户端确认游标层：

1. 服务端增加只供原生 EventSource 首次连接使用的 URL cursor；同一请求同时出现浏览器自动发送的 `Last-Event-ID` Header 时，Header 必须优先。当前服务端只允许唯一的 `workspace_id` query，且 cursor 只读 Header（`internal/events/http/handler.go:245-268`）；当前 OpenAPI 也只声明 Header cursor（`api/openapi/openapi.json:3141-3161`）。
2. 服务端不再把动态业务 `Envelope.type` 用作 SSE 命名事件。推荐省略 `event:`，统一走默认 `message`；业务类型继续只从严格 Envelope 解码。当前服务端输出 `event: <event.Type>`（`internal/events/http/handler.go:224-242`），当前手写解析器还要求 frame event 与 Envelope type 相等（`web/src/events/server-events.ts:372-418`）。
3. 客户端必须维护独立于浏览器内部 last-event-id 的“最后已提交项目 cursor”，串行 `await onEvent` 后才推进；处理失败时关闭旧 EventSource、废弃旧 generation 的排队回调，再从已提交 cursor 重建。项目明确要求 invalidation 完成后才能推进游标（`docs/architecture/application-contracts.md:61-69`；`.trellis/spec/frontend/state-management.md:148-170`），当前实现也确实在 `await onEvent` 之后才赋值（`web/src/events/server-events.ts:646-653`）。

推荐数据流：

```text
native EventSource
  -> browser SSE/UTF-8/frame parsing
  -> MessageEvent queue（按 connector generation 串行）
  -> data 大小 + JSON + strict Envelope + lastEventId/workspace/单调性
  -> await EventStore onEvent（完成 Query invalidation）
  -> commit connector cursor + EventStore sessionStorage cursor

EventSource error
  -> readyState=CONNECTING: 浏览器原生重连，只报告 reconnecting
  -> readyState=CLOSED: Fetch 诊断 HTTP/Problem
       -> 401: 共享 Auth 失效
       -> 400/409 cursor: 权威 REST recovery，成功后无 cursor 重建
       -> retryable 5xx/network: 有界 fallback 调度后重建
       -> non-retryable/invalid response: fail closed

onEvent reject
  -> close old EventSource
  -> invalidate old generation/queue
  -> 从最后已提交项目 cursor 重建并重放
```

### 2. 当前所有权和兼容面

- `EventStoreProvider` 已是每个 Active Workspace 的唯一连接 owner（`web/src/events/event-store.tsx:39-68`）；前端规范也禁止 Feature 自行创建 EventSource/fetch stream（`.trellis/spec/frontend/state-management.md:129-146`）。迁移后不应把原生 EventSource 暴露给页面或 Feature。
- `EventStoreProvider` 把 Workspace recovery、Query invalidation、sessionStorage cursor 和 UI 连接状态都封装在 `ConnectServerEventsOptions` callbacks 后面（`web/src/events/event-store.tsx:294-326`）。保持这些 callback 语义，可以让 `event-store.tsx` 基本不感知底层迁移。
- Workspace switch 已通过 generation + effect identity 隔离迟到回调（`web/src/events/event-store.tsx:54-79`、`web/src/events/event-store.tsx:328-388`），测试覆盖单连接、先 close A 再连接 B 和旧 callback 不覆盖新状态（`web/src/events/event-store.test.tsx:118-162`）。原生实现必须复用同一 generation 规则，并把 EventSource listener、诊断 Fetch 和事件处理队列都绑定到 generation。
- Event Store 只有在全部异步失效完成后才写 sessionStorage（`web/src/events/event-store.tsx:314-320`）；对应回归测试明确断言 invalidation reject 时 cursor 不提交（`web/src/events/event-store.test.tsx:201-214`）。这个应用级确认语义不能委托给浏览器内部 last-event-id。
- 本地 cursor 损坏目前同步抛 `CURSOR_REJECTED`，Event Store 先执行权威 recovery 再无 cursor 连接（`web/src/events/server-events.ts:546-558`、`web/src/events/event-store.tsx:370-378`）；测试覆盖该路径（`web/src/events/event-store.test.tsx:707-733`）。建议保留同步输入校验，不让明显坏 cursor 先进入 EventSource 的不透明错误路径。
- 连接状态公开值是 `connecting|open|reconnecting|recovery_failed|closed`（`web/src/events/server-events.ts:73-78`）。EventSource 的 `open/error/readyState` 足以映射前三者和 `closed`；`recovery_failed` 仍由项目 recovery error 映射，不应由原生 API 推导。

### 3. 推荐最小接口

兼容优先：继续保留唯一入口 `connectServerEvents(options)`、现有 callback 和错误类型，让页面与 Event Store 无改动感知传输切换。

```ts
interface ConnectServerEventsOptions {
  workspaceId: string;
  lastEventId?: string;
  baseUrl?: string;
  onEvent?: (event: ServerEventEnvelope) => void | Promise<void>;
  onRecoveryRequired: (signal: ServerEventRecoverySignal) => void | Promise<void>;
  onStateChange?: (state: ServerEventConnectionState) => void;
  onError?: (error: ServerEventClientError) => void;

  // 只作为 transport/test seam；生产默认使用全局实现。
  eventSourceFactory?: EventSourceFactory;
  fetcher?: Fetcher; // 仅 fatal handshake 诊断，不再持续解析流。
}

interface ServerEventConnection {
  close(): void;
  done: Promise<void>;
}
```

依据与取舍：

- 当前实际产品消费者只有 `event-store.tsx`；`parseServerEventStream`、`getLastEventId` 和连接调优参数在产品代码中没有其他调用方，搜索结果只落在 `server-events.ts`、其测试和 barrel export（`web/src/events/index.ts:1-17`；`web/src/events/event-store.tsx:12-14`）。因此 `getLastEventId()` 可以删除，connector 内部仍持有 committed cursor。
- `done` 虽未被 Event Store 使用，但能让 close/fatal 单测确定性等待连接生命周期结束；保留它比把异步清理暴露给调用方更稳妥。当前连接接口为 `close + done + getLastEventId`（`web/src/events/server-events.ts:526-530`）。
- 当前 `fetcher/sleep/random/backoff` 都是实现/测试 seam（`web/src/events/server-events.ts:509-523`）。迁移后 `fetcher` 只服务 fatal 诊断；若产品接受浏览器原生网络重连，通用 `initialBackoffMs/maxBackoffMs/sleep/random` 应从公开 options 删除。若 retryable 5xx 和 `onEvent` 失败仍需有界 fallback，可把 delay/scheduler 留成 connector 私有依赖或仅测试 seam，不应继续冒充主网络重连策略。
- 本地 Vitest 使用 jsdom（`web/vite.config.ts:32-36`），依赖版本为 jsdom 29.1.1（`web/package.json:37-49`）；本次本机探针显示其 `window.EventSource` 为 `undefined`。单测必须注入轻量 FakeEventSource/EventTarget，不能新增 polyfill 并误把 polyfill 当生产实现。
- TypeScript 已包含 `DOM`/`DOM.Iterable` lib（`web/tsconfig.json:3-5`），原生 EventSource 类型不需要新增依赖。

### 4. 必须保留、应删除和需要改写的逻辑

#### 必须保留

| 逻辑 | 证据与原因 |
| --- | --- |
| `ServerEventEnvelope`、payload/invalidation domain types | 它们是 Event Store/Feature 的稳定 typed hint（`web/src/events/server-events.ts:8-78`），不是浏览器 SSE parser 能覆盖的业务契约。 |
| `decodeServerEventEnvelope(unknown)` 全部严格校验 | 当前校验 schema、ID、type、RFC3339、Workspace、resource/version 和 payload（`web/src/events/server-events.ts:329-370`）；规范要求 SSE 只在唯一边界解码（`.trellis/spec/frontend/type-safety.md:20-34`、`.trellis/spec/frontend/type-safety.md:99-108`）。 |
| `ServerEventClientError` 和严格 Problem decode | 原生 `error` 不提供 HTTP/Problem；诊断 Fetch 仍需要现有 401/cursor/5xx 分类（`web/src/events/server-events.ts:80-101`、`web/src/events/server-events.ts:476-507`）。 |
| Workspace binding、ID 单调性、frame ID/Envelope ID 一致性 | 当前 connector 在交付前检查 Workspace 和 committed cursor 单调性（`web/src/events/server-events.ts:646-650`）；迁移后应比较 `MessageEvent.lastEventId === envelope.id`，再与 committed cursor 比较。 |
| 串行 `await onEvent` 后提交 cursor | 当前 `for await` 天然串行且先 await 再提交（`web/src/events/server-events.ts:646-653`）；测试要求处理失败重放同一事件（`web/src/events/server-events.test.ts:467-503`）。原生 listener 必须显式 Promise queue 才能保持等价。 |
| Event Store recovery/invalidation/sessionStorage 和 Workspace cleanup | 权威 recovery 先重置 Search/History，再回查 Workspace/各 Query family，成功后才删 cursor（`web/src/events/event-store.tsx:81-143`）；连接层不能复制这些业务规则。 |
| Cookie credentials 和共享 401 invalidation | 当前 SSE GET 使用 `credentials: "include"` 且不带 CSRF（`web/src/events/server-events.ts:596-601`；测试 `web/src/events/server-events.test.ts:248-270`）；共享认证入口清 CSRF 并通知 AuthProvider（`web/src/api/auth.ts:148-152`）。原生构造必须使用 `{ withCredentials: true }`。 |
| data 大小上限（应用层） | 当前 cap 为 32 Ki characters（`web/src/events/server-events.ts:134-142`、`web/src/events/server-events.ts:450-459`）。EventSource 无 raw frame hook，至少应在 JSON.parse 前限制 `MessageEvent.data.length`；这只能限制交付后的应用数据，不能阻止浏览器先缓冲超大 frame。 |

#### 应删除

| 逻辑 | 删除范围 |
| --- | --- |
| 手写 SSE field/frame parser | 删除 `ParsedFrame`、`parseFrame` 以及 id/event/data 行解析（`web/src/events/server-events.ts:372-418`）。 |
| `ReadableStream` reader、`TextDecoder`、CR/LF/chunk/heartbeat/EOF 处理 | 删除 `parseServerEventStream`（`web/src/events/server-events.ts:420-474`）。标准 EventSource 负责 UTF-8、换行、多行 data、comment heartbeat 和 incomplete EOF。 |
| 主连接的 Fetch stream loop | 删除持续 `fetch -> response.body -> for await` 路径（`web/src/events/server-events.ts:582-654`）；Fetch 只保留短诊断。 |
| 通用网络重连的手写 sleep/jitter/指数退避 | 浏览器原生路径接管后删除 `defaultSleep` 和主循环的 backoff（`web/src/events/server-events.ts:532-544`、`web/src/events/server-events.ts:668-677`）。retryable fatal HTTP/应用 handler 失败所需的最小 fallback 调度应独立命名，不能继续包成完整网络重连器。 |
| parser 级单测 | 现有 CRLF/chunk/heartbeat、EOF、reader cancel、invalid UTF-8 测试集中在 `web/src/events/server-events.test.ts:161-246`；这些不应在 FakeEventSource 上重测浏览器标准实现，应由真实浏览器 smoke 覆盖。 |

#### 需要改写而不是删除

- 连接测试 `web/src/events/server-events.test.ts:248-503` 要从 mock Fetch stream 改为 FakeEventSource + diagnostic Fetch；保留 401、cursor recovery、处理失败重放、close 幂等和状态契约。
- `web/src/events/index.ts:1-17` 应停止导出 `parseServerEventStream`，保留严格 decoder、connector 和 domain types。
- `event-store.tsx` 只应做必要的类型适配；其 invalidation、recovery 和 generation 逻辑不是本次优化目标（`web/src/events/event-store.tsx:54-388`）。

### 5. 命名事件处理方案

推荐方案：服务端正常业务帧省略 `event:`，客户端只监听默认 `message`；Envelope 的 `type` 继续表示业务事件类型。

```text
id: 42
data: {"schema_version":1,"id":"42","type":"answer.completed",...}

```

理由：

- 当前业务类型是开放字符串（decoder pattern 允许任意合规类型，`web/src/events/server-events.ts:134-138`），Event Store 还按多个 prefix 分派（`web/src/events/event-store.tsx:168-219`）。维护一份 exhaustive `addEventListener(<business-type>)` registry 会成为新的协议事实源，新事件漏注册将静默不交付。
- WHATWG 规定有 `event:` 时 MessageEvent.type 会变成该精确名字；无 `event:` 时为 `message`。API 没有 wildcard named-event listener。因此不能用一个 `onmessage` 消费当前动态 `event: <type>`。
- 当前服务端 Envelope 已包含并验证业务 `type`，动态 transport event 是重复元数据（`internal/events/http/handler.go:231-240`；`web/src/events/server-events.ts:339-341`）。去掉重复字段不会削弱业务类型校验。
- 迁移后的 metadata 一致性应定义为 `MessageEvent.lastEventId === decodedEnvelope.id`；transport event type 只要求固定 `message`，不再与 Envelope.type 比较。

若必须保留命名 transport event，次选是一个固定常量（例如 `event: server-event`）并只注册该常量；不建议注册全部动态业务类型。若担心非浏览器外部消费者依赖当前动态命名，可在 URL 增加显式格式版本做短期双模式 rollout，但仓库搜索未发现产品代码外的当前消费者，增加长期双模式会抵消简化收益。

### 6. 初始 cursor 与应用确认 cursor

#### 初始 cursor

原生构造器只有 URL 和 `withCredentials`，不能传自定义 Header。WHATWG 还规定每个新 EventSource 的内部 last event ID 初始为空，只有“重建同一个 EventSource 的连接”时浏览器才设置 `Last-Event-ID`。因此 sessionStorage cursor 不能直接交给新实例。

推荐新增 `last_event_id` query：

```text
GET /api/v1/events?workspace_id=<uuid>&last_event_id=<committed-seq>
```

服务端解析规则：

1. 只接受唯一 `workspace_id` 和可选唯一、非空、规范正十进制 `last_event_id`，继续拒绝未知/repeated query。
2. `Last-Event-ID` Header 存在时优先使用 Header；query 仍需格式合法，但允许与 Header 不同。不同是正常的：EventSource URL 保存首次 committed cursor，而同一实例自动重连时 Header 已是浏览器新看到的 ID。
3. 两者都缺失时保持现有“从当前 watermark 开始”语义；当前实现无 cursor 时直接取 CurrentWatermark（`internal/events/http/handler.go:138-161`）。
4. OpenAPI 同步声明 query cursor、Header precedence 和默认 message frame；当前契约入口位于 `api/openapi/openapi.json:3141-3161`。

#### 应用确认 cursor

浏览器的 last event ID 不能当项目 committed cursor。WHATWG 解析算法在排队分发 MessageEvent 之前就更新 EventSource 内部 last event ID；项目 `onEvent` 又是异步 Query invalidation（`web/src/events/event-store.tsx:314-320`）。因此需要：

- connector 私有 `committedLastEventId`，初始值来自已校验的 session cursor；只在 `await onEvent` 成功后更新。
- 一个串行 Promise queue，保持当前 `for await` 的顺序/背压语义；EventSource callback 本身不能 `await` 浏览器读取。
- 每个 EventSource 实例有独立 generation。任一 decode/binding/handler 错误先使旧 generation 无效并 close；旧实例已排队的 message/error 不再进入 Event Store。
- decoder/binding 错误为 non-retryable，fail closed；业务 `onEvent` reject 视为 retryable consumer failure，从 `committedLastEventId` query 重建。当前测试要求同一事件重放且 cursor 不提前推进（`web/src/events/server-events.test.ts:403-441`、`web/src/events/server-events.test.ts:467-503`）。

### 7. 最小 Fetch 诊断适配器

原生 `error` 事件不包含 Response、HTTP status 或 Problem body；只凭 `readyState` 可区分“浏览器仍在重连”与“连接已 fatal closed”，无法区分 401、400、409、503 和 Content-Type 错误。当前 connector 对这些状态有不同业务行为（`web/src/events/server-events.ts:602-640`），不能把它们统一为网络重试。

建议只在原生 `error` 且 `readyState === EventSource.CLOSED` 时：

1. 确认当前 generation 仍 active，close/废弃原实例。
2. 用 Fetch 对同一 GET 做一次诊断，携带 `credentials: "include"`、`Accept: text/event-stream` 和项目 committed `Last-Event-ID` Header。
3. 非 2xx 严格 decode Problem，复用现有 401、cursor recovery、retryable 分类。
4. 若为 200，验证 Content-Type 后立即 abort/cancel body，绝不调用 `getReader()` 或解析事件；随后按 retryable handshake 重新创建 EventSource。该探针可能让服务端短暂准备一次 replay page，但从同一 committed cursor 重建不会丢事件，只会产生一次受控重复服务端工作。
5. 诊断 Fetch、recovery 和后续重建都绑定 connector generation/AbortController；close/Workspace switch 必须取消它们。

注意：WHATWG 规定非 200 或非 `text/event-stream` 会 fail connection 且不再自动重连；因此 retryable 5xx 仍需要项目 wrapper 在诊断后手动新建 EventSource。原生自动重连主要覆盖已建立连接的 EOF/网络断开，而不是所有 HTTP 失败。

### 8. 重连策略的产品决策

当前规范要求网络失败使用有界抖动退避（`.trellis/spec/frontend/state-management.md:73-80`、`.trellis/spec/frontend/state-management.md:172-180`），实现是 `500ms -> 15s` 指数退避 + 0.8..1.2 jitter（`web/src/events/server-events.ts:568-570`、`web/src/events/server-events.ts:668-677`）。

WHATWG 的 EventSource reconnection time 初始值由实现定义，浏览器还“可以”额外指数退避；服务端 `retry:` 只能设置基础毫秒值，不能保证各浏览器复现当前 jitter/max。两种可选结果：

- 推荐：接受浏览器原生网络重连语义，更新前端规范；只为 fatal 5xx/诊断失败和应用 handler reject 保留最小有界 fallback。这样才能实现 TODO 所说的“连接状态和重连由标准 API 接管”（`docs/roadmap.md:79-83`）。
- 若精确 `500ms -> 15s` 是硬契约：每次 `error` 都必须 `close()` 并由项目 timer 新建 EventSource。这样只能删除 frame parser，不能删除主重连状态机，优化收益明显下降。

### 9. 测试影响与推荐矩阵

前端单元/集成：

| 场景 | 断言 |
| --- | --- |
| 创建 | URL 含 Workspace 和可选 committed query cursor；`withCredentials=true`；初始 state connecting。 |
| open/native reconnect | `open -> open`；`error + CONNECTING -> reconnecting`，不触发 Fetch 诊断，由同一 FakeEventSource 表示原生重连。 |
| message strict boundary | data cap、JSON、Envelope、`lastEventId === envelope.id`、Workspace、ID 单调；非法值 fail closed。 |
| 串行消费 | 第二条 `onEvent` 必须等待第一条 resolve；只有成功条目推进 connector/Event Store cursor。 |
| consumer failure | close 旧实例，从旧 committed cursor 新建；同一事件可重放；旧 generation 排队事件/error 被忽略。 |
| fatal 401 | CLOSED 后 Fetch 诊断，清理 CSRF/通知 Auth，无 CSRF Header，不重连。当前共享行为测试见 `web/src/events/server-events.test.ts:248-270`。 |
| 400/409 | strict Problem -> recovery；成功删 cursor/无 cursor 重建，失败保留并报告 `RECOVERY_FAILED`。当前覆盖见 `web/src/events/server-events.test.ts:315-401`。 |
| 503/network/invalid CT | retryable fatal 受控重建；non-retryable/invalid response closed；200 probe body 立即 cancel。 |
| close/switch | close 幂等；取消 EventSource、诊断 Fetch、fallback timer、queue generation；A callback 不写 B。 |
| Event Store regression | 尽量保持现有 `event-store.test.tsx`，重点保留单连接、invalidation reject、recovery、坏 cursor、StrictMode 与 Workspace switch 用例（`web/src/events/event-store.test.tsx:118-214`、`web/src/events/event-store.test.tsx:669-733`）。 |

后端/OpenAPI：

- query cursor 正常/空/重复/非法/未来/过期；Header-only 兼容；query + Header 不同且 Header 优先。
- 默认 `message` frame 只含 `id + data`，Envelope.type 不变；heartbeat 仍为 comment。
- 保留 `Cache-Control: no-store`、`X-Accel-Buffering: no` 和 15 秒 heartbeat；当前实现已具备这些代理友好设置（`internal/events/http/handler.go:89-101`、`internal/events/http/handler.go:172-190`）。
- OpenAPI check 覆盖新增 query 和 frame 描述；现有 endpoint 只有 Header cursor（`api/openapi/openapi.json:3141-3161`）。

真实浏览器：

- FakeEventSource 不验证标准解析。Playwright/真实 API 必须覆盖 heartbeat、chunk/CRLF、多行 data、初次 URL cursor、断线后的浏览器 Header、刷新恢复、401/cursor fatal 诊断、Workspace A/B 和“无重复长期连接”。
- 现有 Export smoke 已等待真实 `/api/v1/events` `text/event-stream` response（`web/e2e/collection-export.smoke.spec.ts:83-89`），可作为正常连接回归，但不足以证明断线/Last-Event-ID/诊断分支。

### 10. 兼容风险

| 风险 | 等级 | 缓解 |
| --- | --- | --- |
| 浏览器内部 last-event-id 早于异步 invalidation 推进，失败后跳过事件 | 高 | 独立 committed cursor、串行 queue、失败时 close + generation 废弃 + query cursor 重建。 |
| 新 EventSource 无法设置初始 `Last-Event-ID` Header | 高 | 新增 URL cursor；自动重连 Header 优先。 |
| 当前动态 named events 无 wildcard listener | 高 | 改默认 `message`；业务类型只由 Envelope 拥有。 |
| `error` 不暴露 status/Problem | 高 | 仅 CLOSED 时短 Fetch 诊断；CONNECTING 保持原生重连。 |
| 规范要求精确 bounded jitter，与 UA 策略不等价 | 高/待决策 | 用户批准改为原生语义并更新 spec；否则保留完整 timer，降低优化目标。 |
| EventSource 对 unknown SSE field 的标准行为是忽略，当前 parser 是拒绝 | 中 | 明确接受 transport 层标准行为；继续严格 JSON/Envelope。若必须拒绝 unknown frame field，则不能删除自研 parser。 |
| EventSource 不暴露 raw bytes/frame，无法在浏览器缓冲前执行 32 KiB cap 或 fatal UTF-8 检查 | 中 | 交付后先限制 `event.data.length` 再 JSON.parse；服务端继续单行有界 Envelope。把底层 byte-level 防护差异列入验收。 |
| fatal 诊断 200 会短暂建立第二次服务端 stream | 中 | 原 EventSource 先 close；诊断只读 headers 后立即 cancel；generation 保证只有一个长期连接。 |
| 401/cursor 错误多一次诊断往返 | 低 | 这是保持现有精确错误语义所需代价；正常/网络重连路径不触发。 |
| `last_event_id` 出现在 URL/代理日志 | 低 | 当前 cursor 是正十进制 sequence，不含 Secret；仍禁止把 Workspace 外业务数据放入 URL。 |
| 跨域 Cookie/CORS | 中 | 构造使用 `withCredentials:true`；默认开发模式经 Vite `/api` 同源代理（`web/vite.config.ts:19-25`）。仓库未发现通用 CORS middleware，跨域 `VITE_API_BASE_URL` 必须单独真实浏览器验证。 |

## Files Found

- `web/src/events/server-events.ts` — SSE Envelope decoder、手写 frame parser、Fetch connector、cursor/recovery/backoff。
- `web/src/events/event-store.tsx` — 全站单连接 owner、Query invalidation、session cursor、Workspace recovery/generation。
- `web/src/events/server-events.test.ts` — decoder/parser/connector 的完整行为测试。
- `web/src/events/event-store.test.tsx` — Workspace 单连接、cursor 提交、recovery、旧 callback 隔离测试。
- `web/src/events/index.ts` — 公开导出，当前仍暴露手写 parser。
- `internal/events/http/handler.go` — SSE GET、Header cursor、动态 named event、heartbeat/proxy headers。
- `internal/events/http/handler_test.go` — Handler request/frame/error 单测。
- `internal/events/http/handler_postgres_integration_test.go` — 持久 replay/expired cursor 集成测试。
- `api/openapi/openapi.json` — `/api/v1/events` 目前仅声明 Workspace query + Header cursor。
- `.trellis/spec/frontend/state-management.md` — SSE 单 owner、确认 cursor、recovery、bounded jitter 契约。
- `.trellis/spec/frontend/type-safety.md` — SSE strict decoder 的唯一边界所有权。
- `.trellis/spec/frontend/quality-guidelines.md` — reconnect/Workspace/browser 测试门禁和成熟标准优先规则。
- `docs/architecture/application-contracts.md` — SSE invalidation -> REST -> cursor commit 的上层架构契约。
- `docs/roadmap.md` — TODO 12 的仓库内可定位需求记录。

## Code Patterns

- Strict unknown boundary：`decodeServerEventEnvelope(value: unknown)`（`web/src/events/server-events.ts:329-370`）。
- 当前串行确认模式：`for await -> await onEvent -> lastEventId = event.id`（`web/src/events/server-events.ts:646-653`）。
- 当前 Workspace generation guard：`isActiveConnection(generation)`（`web/src/events/event-store.tsx:144-145`、`web/src/events/event-store.tsx:294-325`）。
- 当前权威 recovery 后删 cursor：`performRecovery()` 最后一项才 `sessionStorage.removeItem`（`web/src/events/event-store.tsx:81-128`）。
- 当前服务端无 cursor 从 watermark 开始、带 cursor replay：`resolveReplayStart`（`internal/events/http/handler.go:138-161`）。
- 当前服务端 frame：`id + dynamic event + data`（`internal/events/http/handler.go:224-242`）。

## External References

- WHATWG HTML Living Standard, Server-sent events（访问日期 2026-08-10）：https://html.spec.whatwg.org/multipage/server-sent-events.html
  - 构造器只有 URL 与 `withCredentials`；新实例 last event ID 初始为空。
  - `CONNECTING/OPEN/CLOSED`、自动重连和 `Last-Event-ID` Header 的处理模型。
  - 非 200/非 `text/event-stream` 会 fail connection；`error` 本身几乎不提供诊断信息。
  - 标准 parser 负责 UTF-8、CR/LF、comments、data/event/id/retry；内部 last event ID 在 MessageEvent 入队前更新。
  - 动态 `event:` 会改变 MessageEvent.type；省略时默认 `message`。

## Related Specs

- `.trellis/spec/frontend/state-management.md:129-195` — M9 Unified Workspace SSE Event Store。
- `.trellis/spec/frontend/type-safety.md:20-37`、`:99-108` — SSE unknown/strict decoder ownership。
- `.trellis/spec/frontend/quality-guidelines.md:17-34`、`:50-83` — SSE reconnect、Workspace、成熟标准优先与测试门禁。
- `.trellis/spec/guides/cross-layer-thinking-guide.md:19-50`、`:74-122` — 跨层格式/错误契约和单一 event decoder owner。
- `docs/architecture/application-contracts.md:61-71` — SSE 与恢复的 canonical data flow。
- `docs/architecture/adr/0010-sse-for-server-events.md:5-13` — 采用 SSE、REST 保持事实源的架构决策。

## Caveats / Not Found

- 用户提到的 `2026-08-01-requirement-optimization-list.md` 以及任务 PRD 引用的 `docs/product/2026-08-01-requirement-optimization-list.md` 在当前工作区均未找到；可定位的等价 TODO 12 位于 `docs/roadmap.md:79-83`。规划文件中的来源路径需要主会话纠正，不能把不存在的文件标为 confirmed fact。
- 仓库未找到明确的最低浏览器版本矩阵。TypeScript target 为 ES2023 且包含 DOM lib（`web/tsconfig.json:3-5`），但发布前仍需用项目实际支持的桌面/移动浏览器完成 smoke。
- `last_event_id` query 名、默认 `message` 的一次性 wire 切换还是带版本参数的 rollout，属于尚未落地的设计选择；本研究推荐最小的一次性切换，外部消费者存在时再加短期兼容模式。
- 精确 `500ms -> 15s` jitter 是否可改成 User Agent 原生重连仍是阻断设计定稿的产品决策；不批准会显著缩小 TODO 12 的删除范围。
- 原生 EventSource 无法提供 raw frame/HTTP Response hook；unknown SSE field、invalid UTF-8 byte 和 pre-buffer frame cap 与当前手写 parser 不可能完全行为等价。验收必须明确“Web 标准行为优先，严格 Envelope 不变”，否则该优化在技术上不可完整实现。
