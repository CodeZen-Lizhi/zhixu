# 浏览器原生 EventSource 优化：实施计划

## 0. 启动前门禁

- [x] 用户批准最新 PRD/Design/Implement 摘要。
- [x] `task.py validate` 通过，`implement.jsonl` 与 `check.jsonl` 均包含真实 spec/research。
- [x] `task.py start .trellis/tasks/08-10-native-eventsource` 将任务切换为 `in_progress`。
- [x] 实现 agent 读取注入上下文、PRD、Design、Implement，并检查相关文件当前脏改，不覆盖用户改动。

## 1. 服务端兼容扩展

- [x] 为 Events request 建立严格内部请求结构：Workspace、Header/query cursor、event format。
- [x] 接受可选唯一 `last_event_id` 与 `event_format=message`；拒绝未知、重复、空或非 canonical 值。
- [x] 实现 Header > query > watermark 的 replay 优先级，允许 Header 与旧 query seed 不同。
- [x] 保持 legacy 默认 frame；message 模式只输出 `id + data`；两种模式共享 Envelope、heartbeat、顺序和 Flush。
- [x] 在 Handler 入口设置 `Cache-Control: no-store`，保持既有成功响应安全/代理 Header。
- [x] 扩展 Handler 与 PostgreSQL integration tests：query/format matrix、Header precedence、expired/future、A/B 隔离和取消。

验证：

```bash
timeout 60s go test ./internal/events/http
```

数据库集成测试仅在已有 `ZHIXU_TEST_DATABASE_URL` 时执行定向 package；没有数据库事实时如实记录盲区，不临时修改 Schema。

## 2. OpenAPI 契约

- [x] 声明 `last_event_id` query、`event_format=message`、Header precedence、legacy 默认与 native message 格式。
- [x] 保留 Header cursor、Envelope schema、Problem 与 no-store 契约。
- [x] 扩展 `api/openapi/check.mjs`，防止 query/format/precedence 回退。

验证：

```bash
node api/openapi/check.mjs
```

## 3. 原生 EventSource transport

- [x] 保持 strict Envelope/domain error/callback 接口，新增可注入 EventSource factory。
- [x] 删除 `ParsedFrame`、`parseFrame`、`parseServerEventStream`、ReadableStream reader、TextDecoder 和相关 export。
- [x] 构造 message-mode URL，保留同步 Workspace/cursor 校验与 Cookie credential 语义。
- [x] 对 MessageEvent 执行 data cap、JSON、Envelope、lastEventId、Workspace 和 observed ID 校验；精确重复幂等忽略，倒退 ID fail closed。
- [x] 实现固定上限 FIFO 与单 drain loop；仅 `await onEvent` 成功后推进 committed cursor。
- [x] 实现 generation cleanup：消费失败/overflow 从 committed cursor 受控重建；旧 callback、queue 和诊断不能提交。
- [x] 普通 `CONNECTING` error 交给原生重连；fatal `CLOSED` 进入诊断。
- [x] 实现最小 Fetch probe：严格 Problem、401、cursor recovery、retryable fatal、200 body cancel 和 Abort。
- [x] 保留幂等 `close()` 与可等待 `done`；删除无产品消费者的旧 `getLastEventId`。

## 4. 前端回归测试

- [x] 保留所有 Envelope decoder 测试。
- [x] 删除重测浏览器 parser 的 CRLF/chunk/reader/UTF-8 单测。
- [x] 用 FakeEventSource 覆盖 URL/credentials、open/CONNECTING/CLOSED、严格 message、串行 queue、上限和 generation。
- [x] 覆盖 onEvent 失败不提交并从旧 cursor 重放；旧 generation 排队事件不生效。
- [x] 覆盖 probe 401、400 invalid/future、409 expired、503/network、invalid Content-Type、200 cancel 与 close Abort。
- [x] 运行 Event Store 回归，必要时只做接口适配，不改 invalidation ownership。

验证：

```bash
npm run test --prefix web -- src/events/server-events.test.ts src/events/event-store.test.tsx
npm run lint --prefix web
npm run typecheck --prefix web
npm run build --prefix web
```

## 5. 真实浏览器验证

- [x] 新增定向 Playwright smoke，使用 Vite 变换后的真实 transport 模块与 Chromium 原生 EventSource。
- [x] 验证 message/heartbeat/multi-line data、初始 query cursor、EOF/网络重连后的 `Last-Event-ID` Header、Cookie 和 close。
- [x] 验证 fatal probe、刷新 committed cursor、非法事件以及 Workspace A/B generation 隔离。
- [x] 确认只有一条长期连接、console/pageerror 无新增问题。
- [ ] 复用至少一条既有真实 API SSE smoke 或进行等价实际页面 smoke，证明 Go Handler、代理和浏览器可连通。
  当前未执行：现有 `127.0.0.1:8080` Compose 运行的是旧构建，当前 Vite 无 API proxy；本轮未读取或改写其认证/业务数据，
  也未在缺少 `ZHIXU_TEST_DATABASE_URL` 的情况下把 integration 编译或 Node SSE fixture 冒充真实当前 API smoke。

执行具体环境命令前先读取现有 smoke 脚本/环境要求；不伪造缺失数据库或 fixture。若完整 Compose smoke 超过约 120 秒或缺少外部依赖，停止并记录未覆盖范围。

## 6. 规范与清单同步

- [x] 更新 frontend SSE state/type 规范：普通网络由原生重连，committed cursor/queue/fatal recovery 仍由项目拥有。
- [x] 更新必要架构说明，避免继续把手写 frame parser/Fetch stream 写成当前实现。
- [x] 将 `docs/roadmap.md` TODO 12 标记完成并记录兼容模式、最小 Fetch probe 和验证事实。
- [x] 不恢复已由其他工作区改动删除的旧需求文件。

## 7. Review 与最终门禁

- [x] `trellis-check` 检查 PRD/Design/实现一致性、lint/typecheck/test/build/OpenAPI 与跨层数据流。
- [x] 使用 `go-review` 审查 Handler、HTTP/认证/Problem/兼容/取消语义。
- [x] 使用 `code-review-and-quality` 审查 TS queue、generation、race、资源释放、测试与维护性。
- [x] 无 SQL/迁移/Repository 改动；若实际 diff 进入这些范围，追加 `sql-code-review` 和相应数据库验证。
- [x] 修复 review 发现的范围内问题并重跑受影响门禁。

最终命令：

```bash
git diff --check
git status --short
```

## 8. 回滚点

- Checkpoint A：仅后端 opt-in query/message 扩展，可独立保持 legacy 默认兼容。
- Checkpoint B：前端切到原生 transport；若可靠性门禁失败，恢复旧 Fetch connector，不删除后端兼容扩展。
- Checkpoint C：规范/TODO 只在实现、测试和浏览器证据全部成立后标记完成。

## 9. Review 证据（2026-08-10）

- PASS：`go test -count=1 ./internal/events/http`、`go test -count=1 -race ./internal/events/http`、
  `go vet ./internal/events/http`。
- PASS：`go test -count=1 -tags=integration ./internal/events/http -run '^$'`，证明 integration test 可编译；
  当前没有 `ZHIXU_TEST_DATABASE_URL`，未把未运行的 PostgreSQL integration 声明为通过。
- PASS：`node api/openapi/check.mjs`、`jq empty api/openapi/openapi.json`、`task.py validate`。
- PASS：EventSource/Event Store 定向 Vitest 2 files / 69 tests；前端 lint、typecheck、production build 通过。
- PASS：Chromium `eventsource.smoke.spec.ts` 3/3，通过原生 parser、分块 CRLF、EOF Header、页面 reload 后的 scoped
  cursor 恢复、Cookie、fatal probe、非法事件和 Workspace A/B 隔离验证。
- PASS：全工作区 `git diff --check`；旧 `getReader`、`TextDecoder`、手写 SSE parser 与相关 export/dependency 扫描无残留。
- 非本任务基线：全量 `npm run test --prefix web` 在未改动的
  `src/features/graph/feedback.test.tsx` 失败，测试期待“Graph 服务暂不可用”，`HEAD` 实现为“图谱服务暂不可用”；
  本任务未越界修改 Graph。
- 未覆盖：当前构建的 Go API + 代理 + 浏览器实际页面 smoke，原因见第 5 节保留的未勾选项。
