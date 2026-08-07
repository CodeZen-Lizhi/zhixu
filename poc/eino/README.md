# Eino 隔离 PoC

本目录是独立 Go module，用于保留 ADR-0013 的历史采用门禁。ADR-0019 后主模块只在
`internal/platform/models` 正式锁入 Eino Chat Adapter；Domain、Workflow、Proposal/Approval 仍不依赖 Eino。

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

历史门禁结论见 [report.md](report.md)，当前采用范围以 ADR-0019 为准。
