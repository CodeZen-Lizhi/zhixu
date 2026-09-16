# 实时模型验收的受限错误诊断

2026-09-16。本次仅修改 `internal/organizing/adapter/agent/synthesis_live_test.go` 和本报告，不修改生产适配器、模型配置、验收条件或调用预算。未调用外部模型。

两个 opt-in 验收入口现在将 `CallDiagnostics` 写入原有 artifact：每个实际到达 delegate 的调用有从 1 开始的 `Call` 序号，失败时附带 `Error`；成功记录不含 `Error`。最终业务阶段失败也在对应 `Cases[].Error` 或 `SourceReviews[].Error` 保存相同的投影。原有 `ErrorStage`、合成输入及有效模型内容保持原义。

诊断只复制项目 `foundation.Error` 的 `Code`、`Kind`、`Retryable`，以及 `models.ConnectionDiagnostic` 的固定枚举 `Stage`、100–599 范围内的 `ProviderHTTPStatus`、通过 `IsKnown()` 的 `ValidationReason`。不保留错误对象、Cause、上游消息/错误码/错误类型/请求 ID、transport 文本、endpoint、headers 或请求内容。未知 stage/reason 和无效状态码省略；无 `foundation.Error` 时只使用固定 `LIVE_UNCLASSIFIED_ERROR`，空 Kind/false Retryable 不代表已得到真实错误分类。

普通测试日志仅输出上述投影。比如 `MODEL_CHAT_RESPONSE_INVALID` + `response_validation` + `finish_reason_length` 表示响应因输出截断被拒绝；`MODEL_CHAT_PROVIDER_UNAVAILABLE` + `provider_response` + `502` 表示上游状态错误。日志 `provider_status=0` 或 artifact 中缺少该字段仅表示没有可用状态，不推断请求成功。最终语义断言失败而没有 error 时保留 `ErrorStage`，日志明确提示检查失败断言。

对未进入 delegate 的预算拒绝不伪造外部调用记录。原有五场景最多 12 次调用/10 分钟、当前全文两场景最多 4 次/5 分钟、每次最多 2048 输出 Token、单次最多 90 秒、首失败停止和生产有限结构修复均保持。追加诊断不重试、不改变返回错误或取消语义。

## 验证

以下定向检查全部退出 0：

```sh
env ZHIXU_EINO_LIVE_SYNTHESIS_ENABLED=false ZHIXU_EINO_LIVE_SOURCE_REVIEW_ENABLED=false \
  GOCACHE=/tmp/zhixu-synthesis-live-go-cache \
  go test ./internal/organizing/adapter/agent -run '^TestSynthesisLive' -count=1 -v
env GOCACHE=/tmp/zhixu-synthesis-live-go-cache go vet ./internal/organizing/adapter/agent
```

包测试通过，耗时 0.877s；两个真实模型入口明确 Skip。新增 `TestSynthesisLiveSafeDiagnostics` 完全无网络，经过实际 `synthesisLiveChat.Chat`，用带 canary 的包装错误和 ConnectionDiagnostic 检查 artifact JSON 与实际日志格式，覆盖 502 可重试错误、输出截断、未知枚举/无效状态拒绝、原始未分类错误及成功调用。断言原始 error 身份、调用数、取消状态保持，密钥样例、私有地址、Authorization、上游原文及禁止的字段名均未进入诊断输出。既有 Preflight、TransportBudget（本机 httptest）、SourceReviewPlumbing/Budget 均通过。

已执行 gofmt，并按 go-review 对该小范围检查错误投影、锁内写入、defer 产物与日志收尾、预算不变和敏感字段边界；无本次遗留问题。此结果只证明诊断安全性及原有离线约束，真实来源复核第二场景的失败原因仍需主会话用新的独占 artifact 路径重跑后判断。
