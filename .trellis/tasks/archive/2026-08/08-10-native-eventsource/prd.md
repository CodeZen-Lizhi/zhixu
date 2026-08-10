# 浏览器原生 EventSource 优化

## Goal

在不破坏认证、Workspace 隔离、事件严格校验和权威恢复语义的前提下，让浏览器原生 `EventSource` 成为业务 SSE 的主连接实现，删除可由 Web 标准覆盖的手写分帧与网络重连代码，只保留标准 API 无法覆盖的最小 Fetch 诊断适配器。

用户价值是降低 SSE 协议边界的缺陷与维护成本；本任务不承诺可感知的页面性能提升或新增业务功能。

## Background And Confirmed Facts

- TODO 12 的当前产品边界记录于 `docs/roadmap.md:79-83`：优先原生 EventSource，保留必要的最小 Fetch Adapter。原 `docs/product/2026-08-01-requirement-optimization-list.md` 已在本任务规划期间由工作区其他改动删除，本任务不恢复旧文件。
- 当前 `web/src/events/server-events.ts:372-474` 手工实现 UTF-8 流读取、CR/LF 归一化、heartbeat、多行 `data`、帧大小和 SSE 字段解析；`:560-687` 手工实现 Fetch 连接、Problem 解码、游标恢复与有界 jitter 退避。
- `/api/v1/events` 已是受 Cookie Session 保护的 GET；Workspace 使用 `workspace_id` query，CSRF 不适用于该安全 GET。当前浏览器请求不依赖 Authorization 或 Workspace Header。
- 服务端只从 `Last-Event-ID` Header 读取恢复游标（`internal/events/http/handler.go:245-269`）；原生 EventSource 构造器不能设置该 Header，因此必须新增不丢事件的初始游标传递方式。
- 服务端当前输出动态 `event: <envelope.type>`（`internal/events/http/handler.go:224-242`）。原生 EventSource 没有事件类型通配监听；为避免破坏潜在旧消费者，默认格式继续兼容动态命名事件，原生客户端通过显式 `event_format=message` 请求省略 `event:` 的默认 `message` 格式，真实业务类型继续来自严格 Envelope。
- EventSource 的 `error` 事件不暴露 HTTP 状态和 Problem JSON。现有 401、400 invalid/future、409 expired、503 retryable 分流只能通过失败后的最小 Fetch 握手诊断或等价服务端契约保持。
- `EventStoreProvider` 是每个 Active Workspace 的唯一连接 Owner；Feature 不得直接创建 EventSource 或 Fetch stream。
- SSE 只触发 TanStack Query 失效/回查，REST 仍是业务事实源。
- 只有 `onEvent` 的全部异步失效成功后才能提交项目 cursor；失败必须使用旧 cursor 重放同一事件。原生 EventSource 不等待异步 listener，客户端必须串行消费事件，并在处理失败时关闭当前实例、丢弃旧 generation 回调，再以最后已提交 cursor 新建实例。
- Workspace A -> B 必须先关闭 A，再清理 A cache/cursor owner；A 的迟到 callback 不得覆盖 B。
- 当前稳定规范要求网络失败使用有界 jitter 退避；浏览器原生自动重连时间由 User Agent/服务端 `retry` 字段控制，无法保证与现有 `500ms -> 15s` 指数退避完全相同。

## Requirements

- R1. 默认生产连接使用浏览器原生 EventSource；不引入第三方 SSE polyfill 或新的协议解析依赖。
- R2. 删除手写底层 SSE 行、分隔符、chunk、CRLF、heartbeat 与多行 `data` 解析；继续对 `MessageEvent.data` 做大小上限、JSON 和 `ServerEventEnvelope` 严格校验。
- R3. 服务端默认保持现有动态命名事件 wire；`event_format=message` 以 opt-in 方式输出原生客户端可统一订阅的默认 `message`，Envelope 的 `type`、`id`、Workspace 和 resource 字段仍保持严格契约。
- R4. 初始恢复 cursor 通过 EventSource URL 安全传递；浏览器自动重连同时携带更新后的 `Last-Event-ID` Header 时，服务端必须以 Header 为准，避免 URL 中的旧 cursor 覆盖新进度。
- R5. EventSource 已建立后的普通网络中断由浏览器原生重连；connector 维护 observed 与 committed 两个水位及有界串行队列。应用事件处理失败必须关闭旧 generation 并从最后已提交项目 cursor 重放，不能因浏览器内部 last-event-id 已推进而跳过事件。
- R6. 仅在原生 EventSource 无法分类的 fatal handshake/error 路径使用 Fetch：读取 HTTP 状态、严格 Problem、认证失效、cursor rejection 和 retryable 失败；Fetch Adapter 不持续读取或解析 SSE body。
- R7. 401 继续调用共享认证失效入口；400 invalid/future 和 409 expired 必须先完成 Workspace 权威回查，成功后清 cursor 并无 cursor 重连，失败保留 cursor 并进入 `recovery_failed`。
- R8. 保持每 Workspace 单连接、事件串行处理、ID 单调推进、最后已提交或已排队事件的精确重复幂等忽略、旧序号/错 Workspace/非法事件 fail closed、Workspace 切换 generation 隔离和 Query invalidation 后提交 cursor。
- R9. EventSource 与 Fetch 诊断层统一输出现有项目事件、连接状态和 `ServerEventClientError`，页面组件与 Feature 不感知底层差异。
- R10. 同步更新 OpenAPI、项目 SSE 规范与 `docs/roadmap.md` 的 TODO 状态，使协议、实现和测试只有一个事实版本。

## Out Of Scope

- 不把 SSE 改为 WebSocket、轮询或第三方 EventSource polyfill。
- 不改变 ServerEventEnvelope 的业务字段、Query invalidation 映射或 REST 事实源职责。
- 不新增跨 Workspace 共享连接、多 Tab 连接复用或 Service Worker。
- 不迁移其他可能存在的独立流式回答协议；本任务只覆盖 `/api/v1/events` 业务事件流。
- 不以本任务为由重构无关 API client、认证页面或 Query key。

## Acceptance Criteria

- [x] AC1. 支持 EventSource 的生产浏览器通过原生 EventSource 连接 `/api/v1/events`；代码不再包含业务事件流的 `ReadableStream.getReader()`、`TextDecoder` 或手写 SSE frame parser。
- [x] AC2. 正常事件、heartbeat、chunk/换行和多行 `data` 由真实浏览器的标准实现解析；项目层拒绝超限 data、非法 JSON、未知 Envelope 字段、`MessageEvent.lastEventId` 与 Envelope ID 不一致、错 Workspace 和倒退 ID，并对最后已提交或已排队 ID 的精确重复保持幂等。底层未知 SSE field 与 UTF-8 行为以 Web 标准为准，不重新实现 raw frame parser。
- [x] AC3. 刷新后使用 Workspace-scoped 已提交 cursor 恢复；后续原生自动重连使用最新事件 ID，旧 URL cursor 不覆盖 `Last-Event-ID` Header。
- [x] AC4. Query invalidation 异步失败时不更新 sessionStorage cursor；关闭旧 EventSource 后从旧 cursor 重放同一事件，且旧 generation 的排队 callback 不再生效。
- [x] AC5. 400 invalid/future、409 expired、401、retryable 5xx、无效 Content-Type 和网络中断均得到稳定的连接状态、错误分类与恢复行为；Fetch 只在原生错误无法分类时短暂诊断并立即取消 200 stream body。
- [x] AC6. Workspace A -> B 关闭 A 的唯一连接、清理 A 作用域状态并只创建一个 B 连接；A 的事件、error、诊断请求和处理队列均不能提交到 B。
- [x] AC7. 后端 Handler/OpenAPI 测试覆盖 query 初始 cursor、Header 优先级、重复/空/非法 cursor、默认 legacy 与 opt-in message 两种事件格式、heartbeat、Problem 和 no-store；既有持久重放/超窗语义保持通过。
- [x] AC8. 前端单元测试覆盖连接状态、串行事件、处理失败重放、fatal handshake Fetch 诊断、cursor recovery、认证失效、close 幂等和 generation 隔离。
- [x] AC9. 通过前端 lint、typecheck、定向 Vitest、受影响 Go test、OpenAPI check、构建与 `git diff --check`。
- [x] AC10. 真实浏览器 smoke 覆盖正常事件、断线重连、刷新恢复、非法事件和 Workspace A/B 隔离，并确认无重复长期 SSE 连接、console 无新增错误。

## Product Decisions

- 已批准（2026-08-10）：已建立连接的普通网络中断使用浏览器原生重连语义，不再承诺客户端 `500ms -> 15s` jitter 的精确时序。connector 仍用 committed cursor、有界串行队列和失败回卷保证不丢事件；fatal HTTP/Problem 与消费失败保留最小应用层重建。
- 保持既有公共 wire 兼容：缺少 `event_format` 时继续输出动态命名事件；原生客户端显式请求 `event_format=message`。
- 接受浏览器标准 transport 行为：未知 SSE field、底层 UTF-8 替换和 raw frame buffering 不再由项目重复实现；严格 data 大小、JSON、Envelope、ID 与 Workspace 校验不放宽。

## Notes

- 本任务涉及前后端共享 SSE/OpenAPI 契约，按复杂任务处理；`design.md`、`implement.md`、实现/检查上下文清单必须在用户批准最终规划摘要后才可用于启动实现。
