# Eino 隔离 PoC

本目录是独立 Go module，用于保留 ADR-0013 的历史采用门禁和后续能力验证。ADR-0019 后主模块锁入 Eino Chat
Adapter 与短 Graph，ADR-0020 增加双 Provider Embedding Adapter；Domain、Workflow、Proposal/Approval 仍不依赖
Eino。ADR-0021 记录完整 RAG、Tool、Streaming、Checkpoint/ADK 的实际 Go/No-Go 结论。

默认质量门禁不访问网络：

```bash
go test -race ./...
go vet ./...
```

真实 OpenAI-Compatible Smoke 必须显式启用；缺少 Key 或 Model 时测试会明确 Skip，不会切换为 Fake。
先运行历史 PoC smoke：

```bash
ZHIXU_EINO_LIVE_ENABLED=true \
ZHIXU_EINO_LIVE_API_KEY='...' \
ZHIXU_EINO_LIVE_MODEL='...' \
ZHIXU_EINO_LIVE_BASE_URL='https://provider.example/v1' \
ZHIXU_EINO_LIVE_TIMEOUT='30s' \
go test -run TestOpenAICompatibleChatSmoke -v ./live
```

再从仓库根目录运行生产 Eino Adapter smoke。Provider 响应的模型名与请求别名不同时，需要显式填写
`ZHIXU_EINO_LIVE_MODEL_VERSION`：

```bash
ZHIXU_EINO_LIVE_ENABLED=true \
ZHIXU_EINO_LIVE_API_KEY='...' \
ZHIXU_EINO_LIVE_MODEL='...' \
ZHIXU_EINO_LIVE_MODEL_VERSION='...' \
ZHIXU_EINO_LIVE_BASE_URL='https://provider.example/v1' \
ZHIXU_EINO_LIVE_TIMEOUT='30s' \
go test -run TestEinoOpenAIChatModelLiveSmoke -v ./internal/platform/models
```

2026-08-07，生产 Adapter 已用 loopback Ollama `0.32.6` + `qwen3:0.6b` 连续通过两次该 smoke。推理模型可能先
消费输出预算并返回 `reasoning`/`reasoning_content`；项目只显式接收 string/null 后丢弃，其他未知字段继续拒绝。
该 PASS 只证明协议兼容，不是模型效果或生产灰度证据。

2026-08-08 的扩展复验还包括：

- `tooltrace`：实际 Eino ToolsNode schema/dispatch PASS，当前无生产 Tool-loop 消费者；
- `streaming`：实际编译后 Eino 顶层 Stream 多帧、取消、提前 Close PASS，当前无 Token API/消费者；
- `checkpoint`：实际 StatefulInterrupt/ResumeWithData、Attempt/Fence、加密、TTL/Delete 和并发 PASS，
  仅限同一进程、同一活跃 Attempt；
- 根模块 Embedding：OpenAI-Compatible/Ollama direct/Eino 四组合离线合同 PASS，真实 Provider smoke 仍是发布门禁。

历史与复验结论见 [report.md](report.md)，当前 Eino-only 生产采用范围以 ADR-0022 为准；ADR-0021 的
Checkpoint/Interrupt No-Go 继续有效。
