# Token Streaming 产品门禁

> 历史记录：该 No-Go 基于当时没有逐 Token 产品入口。2026-08-08 用户已明确要求正式采用 Eino Streaming；当前执行合同以本任务最新 PRD/design/implement 为准。

## 结论

**No-Go：当前不新增生产 Token Streaming。** Eino v0.9.13 顶层真实流式能力已通过隔离 PoC，
但仓库没有逐 Token 产品入口或消费者；框架能力 PASS 不等于产品采用 Go。

## Eino 框架能力实测

- `poc/eino/streaming/eino_stream_test.go` 使用
  `compose.NewChain` → `compose.StreamableLambda` → `Compile` → 编译产物 `Stream`，直接消费
  `schema.StreamReader`；不是把项目自定义 `Reader` 或 `Invoke` 单帧包装成流。
- 多帧用例逐帧收到 `eino`、`-`、`stream` 后才到 EOF，证明顶层 `Stream` 保留真实帧边界。
- 提前 `Close` 顶层 reader 后，底层 `schema.Pipe` writer 的 `Send` 返回 closed，producer 在超时前退出，
  证明 Close 能沿编译流传播并释放持续发送的 producer。
- 取消用例证明调用 `Stream` 的 context 取消可到达 `StreamableLambda` producer；reader 仍由消费者显式关闭。
- `cd poc/eino && go test -race -count=20 ./streaming` 通过。该证据只覆盖 Eino compose/schema 的离线生命周期，
  不覆盖真实 Provider、HTTP 首 Token 延迟、客户端背压或草稿发布语义。

## 仓库证据

- `docs/architecture/workflows/04-rag-question-answering.md` 和 `docs/architecture/frontend-architecture.md` 明确当前 SSE 只发送持久阶段与终态摘要，不发送逐 Token 草稿。
- `web/src/events/server-events.ts` 是唯一 SSE owner，事件只触发定向 query invalidation；最终 Answer 从 REST/数据库投影恢复。
- Conversation/RAG API 没有 Token stream route，前端也没有草稿与最终 Answer 的双状态解码合同。
- 上述真实 Eino 顶层流 PoC 证明框架机制可用；没有生产消费者时接入仍只会新增不受产品授权的协议。

## 为什么不能复用现有 SSE

现有 SSE 需要 24 小时窗口、Last-Event-ID 重放和刷新恢复，传输的是脱敏阶段事实。Token 流是易失草稿，需要处理断连、取消、背压、Reader Close、部分内容和最终发布之间的关系。二者生命周期和事实语义不同，不能把同一个 endpoint 改造成混合协议。

## 重开条件

产品 PRD明确需要实时草稿体验，并批准新的 Token API、未完成草稿显示规则、断线策略、最终 Answer 覆盖规则、内容安全和回滚 ADR 后，再用 Eino 从模型到 HTTP 端到端实现并度量首 Token 延迟。
