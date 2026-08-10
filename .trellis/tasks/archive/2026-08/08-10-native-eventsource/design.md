# 浏览器原生 EventSource 优化：技术设计

## 1. 设计目标

让浏览器原生 `EventSource` 接管 `/api/v1/events` 的 UTF-8、SSE 分帧、heartbeat comment、默认 message 分发和已建立连接后的普通网络重连，同时保留 ZHIXU 自有且无法由 Web 标准表达的业务可靠性边界：严格 Envelope、Workspace 隔离、异步 invalidation 后确认 cursor、权威恢复、认证失效和稳定 Problem 分类。

该变更是一个不可拆分的跨层交付：服务端 query/wire 扩展、前端 transport 和 OpenAPI 必须在同一版本中一起验证。拆成独立子任务会产生无法工作的中间协议，因此不建立父子任务。

## 2. 不变量

1. REST/TanStack Query 是业务事实源，SSE 只提供有序 invalidation hint。
2. 每个 Active Workspace 只有 `EventStoreProvider` 一个长连接 owner。
3. `onEvent` 的全部异步失效成功后才能提交项目 cursor；失败必须从旧 committed cursor 重放。
4. Workspace A -> B、logout 或认证失效时，先关闭旧连接/诊断/队列，再清理旧 cache；旧 generation 不能回写。
5. Cookie Session、READ_LOCAL、CSRF safe-method、Problem code、400/401/403/409/503 与 retained-window 语义不变。
6. 默认 `/api/v1/events` wire 向后兼容；原生模式必须显式 opt in。

## 3. 服务端契约

### 3.1 请求

```text
GET /api/v1/events
  ?workspace_id=<canonical UUID>          # required
  &last_event_id=<positive int64>         # optional new EventSource seed
  &event_format=message                   # optional native mode
```

- 允许的 query key 只有 `workspace_id`、`last_event_id`、`event_format`；每个 key 最多一个值，拒绝空值、重复值、未知 key 和非 canonical 值。
- replay cursor 优先级：唯一有效 `Last-Event-ID` Header > `last_event_id` query > 当前 Workspace watermark。
- Header 与 query 同时存在且不同是正常原生重连：URL seed 保持旧 committed cursor，浏览器 Header 已推进；Header 必须覆盖 query，但 query shape 仍需验证。
- `event_format` 缺失时保持 legacy；只接受显式值 `message`，不增加模糊 fallback。
- cursor 是非敏感正十进制 sequence，但可能进入访问日志；不得复用 query 传认证或业务正文。

### 3.2 响应 wire

Legacy 默认保持：

```text
id: 42
event: answer.completed
data: {"schema_version":1,"id":"42","type":"answer.completed",...}

```

原生 `event_format=message`：

```text
id: 42
data: {"schema_version":1,"id":"42","type":"answer.completed",...}

```

- 两种模式共享完全相同的 `ServerEventEnvelope`、顺序、heartbeat、retention、Flush 与 Workspace 过滤。
- message 模式省略重复的 transport `event:`，浏览器统一派发 `message`；业务类型只来自严格 Envelope `type`。
- Handler 入口即设置 `Cache-Control: no-store`，使成功流和 400/409/500/503 Problem 都不被缓存；成功继续设置 `text/event-stream`、`nosniff`、`X-Accel-Buffering: no`。
- OpenAPI 新增两个 query 参数、Header 优先级、两种兼容格式与 Cookie/GET 说明；既有 Header 参数保留。

## 4. 前端连接边界

### 4.1 保持的公共接口

`EventStoreProvider` 继续只调用 `connectServerEvents(options)`，页面和 Feature 不接触 `EventSource`、Fetch 或 wire DTO。保留：

- `ServerEventEnvelope`、`ServerEventConnectionState`、`ServerEventClientError`；
- `onEvent`、`onRecoveryRequired`、`onStateChange`、`onError`；
- `close()` 与用于测试/确定性清理的 `done` Promise；
- `eventSourceFactory`、`fetcher`、fallback scheduler 只作为 transport 测试 seam。

删除 `parseServerEventStream` 及其 export；若确认无产品调用方，删除只用于旧测试的 `getLastEventId()`。不新增 polyfill 或依赖。

### 4.2 URL 与凭据

- URL 固定包含唯一 `workspace_id` 和 `event_format=message`；存在 committed cursor 时追加 `last_event_id`。
- 使用浏览器原生 EventSource；同源 Cookie 是生产主路径，构造选项保持 credentialed 请求语义。
- 继续同步校验 Workspace UUID 和本地 cursor；坏 storage cursor 在建立网络连接前抛 `CURSOR_REJECTED`，沿用 Event Store 权威恢复。

### 4.3 双水位与有界串行队列

```text
browser EventSource
  -> MessageEvent
  -> data size / JSON / strict Envelope / lastEventId / Workspace / monotonic ID
  -> bounded FIFO queue
  -> await onEvent(event)
  -> commit connector cursor
  -> Event Store commits sessionStorage cursor
```

- `observedLastEventId` 表示当前原生实例已经派发的最大合法 ID；`committedLastEventId` 只在 `await onEvent` 成功后推进。ID 等于最后 committed 或已经排队的事件视为精确 transport duplicate 并幂等忽略，ID 倒退则 fail closed。
- MessageEvent listener 同步完成边界校验和 enqueue；单个 drain loop 串行调用 `onEvent`，不能并发失效 Query。
- queue 设固定上限并测试。溢出时关闭并废弃当前 generation，等待正在执行的 handler 收敛，再从 committed cursor 重建；不能无界占用内存。
- `MessageEvent.lastEventId` 必须等于 Envelope ID；data 在 `JSON.parse` 前保持现有 32 KiB 字符上限。未知字段、非法 JSON/Envelope、错 Workspace 或非单调 ID fail closed，不提交 cursor。
- 每个原生实例、queue、Fetch 诊断和 fallback timer 绑定 connector generation；generation 失效后，迟到 callback 只允许完成清理，不能提交状态/cursor。

### 4.4 状态与重连

| 原生/项目信号 | 项目状态与动作 |
| --- | --- |
| 创建新实例、未 open | `connecting` |
| `open` | `open` |
| `error` 且 `readyState=CONNECTING` | `reconnecting`；同一实例由浏览器标准重连，不启动 Fetch 诊断或项目 timer |
| `error` 且 `readyState=CLOSED` | 关闭/冻结 generation，执行最小 Fetch 诊断 |
| `onEvent` reject 或 queue overflow | 关闭 generation，保留 committed cursor，使用有界 fallback 后新建实例并重放 |
| cursor 权威恢复失败 | `RECOVERY_FAILED` / `recovery_failed`，保留 cursor，停止自动恢复 |
| 主动 close/Workspace switch/logout | 关闭 EventSource，Abort 诊断与 timer，废弃 queue，最终 `closed` |

普通已建立连接的网络退避时序由浏览器决定；不再承诺精确 `500ms -> 15s` jitter。项目的有界 fallback 只用于 fatal retryable HTTP、诊断网络失败、消费失败和 queue overflow，并在成功提交事件后复位。

## 5. 最小 Fetch 诊断适配器

只在原生 EventSource fatal CLOSED、无法从 `error` 获取 HTTP 事实时调用：

1. 原实例先关闭且 generation 停止接收新消息。
2. 对同一 URL 发 credentialed、`cache: no-store` 的 GET；Header 使用最后 committed `Last-Event-ID`。
3. 非 200 严格解码既有 Problem：
   - 401：调用共享 `invalidateAuthSession`，停止连接；
   - 400 `SSE_CURSOR_INVALID|FUTURE`、409 `SSE_CURSOR_EXPIRED + refetch`：执行既有权威 recovery，成功后清 committed cursor 并无 cursor 重建；
   - retryable/5xx：有界 fallback 后重建；
   - 其他 non-retryable：fail closed。
4. 200 必须验证 `text/event-stream`，随后直接 `response.body?.cancel()`，不得 `getReader()`、`TextDecoder` 或解析任何 frame；再重建原生实例。
5. 诊断、recovery 和 sleep 全部可 Abort，Workspace switch 必须取消。

该 probe 只读且只发生在 fatal 路径；它可能额外执行一次有界 replay read，但不会同时保留第二条长期 SSE 连接。

## 6. 兼容、安全与性能

- 默认 legacy wire 与 `Last-Event-ID` Header 均保留，因此未知仓库外消费者无需立即迁移。
- 前端仅显式请求 message mode；回滚前端到 Fetch connector 时，后端新增 query/format 是向后兼容的。
- 同源 Cookie/GET/READ_LOCAL 是已验证边界；不宣称跨源 CORS、Bearer EventSource 或 API Token 已支持。
- 接受 Web 标准对未知 SSE field、UTF-8 replacement 和 raw frame buffering 的处理；项目继续严格约束交付后的 data/Envelope，服务端继续输出有界单行 JSON。
- 一条 Workspace 连接的吞吐很低，性能目标是删除重复协议代码和避免无界 queue，不宣称显著降低延迟。

## 7. 影响文件

| 范围 | 文件 | 修改 |
| --- | --- | --- |
| Go Handler | `internal/events/http/handler.go` | query cursor、format、Header precedence、no-store、双格式编码 |
| Go tests | `internal/events/http/handler_test.go`、`handler_postgres_integration_test.go` | request/wire/replay/Workspace 契约 |
| OpenAPI | `api/openapi/openapi.json`、`api/openapi/check.mjs` | query、precedence、兼容格式门禁 |
| TS transport | `web/src/events/server-events.ts`、`index.ts` | 原生 adapter、队列、probe；删除 parser export |
| TS tests | `web/src/events/server-events.test.ts`、必要时 `event-store.test.tsx` | FakeEventSource、error/recovery/ack/generation |
| Browser | `web/e2e/eventsource.smoke.spec.ts` | 真实 Chromium parser、query seed、自动重连 Header 与 message |
| 稳定规范 | `.trellis/spec/frontend/state-management.md`、`type-safety.md`，必要的 backend/architecture SSE 文档 | 原生重连、transport/committed cursor 边界 |
| 产品清单 | `docs/roadmap.md` | 完成状态与最终边界 |

## 8. 验证与回滚

- 定向 Go、Vitest、Event Store 回归、OpenAPI、frontend lint/typecheck/build 和 `git diff --check` 是必跑门禁。
- Playwright 使用真实 Chromium EventSource，不以 jsdom/FakeEventSource 证明标准 parser 或浏览器 Header 行为；既有真实 API SSE smoke 继续作为跨层回归。
- 若原生路径出现未关闭的可靠性缺陷，回滚前端 connector 到当前 Fetch transport；后端 opt-in 参数可兼容保留。不得在同一页面并行开启 Fetch 与 EventSource 两条长期连接。
