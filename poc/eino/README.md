# Eino 隔离 PoC

本目录是独立 Go module，用于执行 ADR-0013 的采用门禁。正式 Domain、Workflow、Proposal/Approval 和主 `go.mod` 不依赖 Eino。

默认质量门禁不访问网络：

```bash
go test -race ./...
go vet ./...
```

真实 OpenAI-Compatible Smoke 必须显式启用；缺少 Key 或 Model 时测试会明确 Skip，不会切换为 Fake：

```bash
ZHIXU_EINO_LIVE_ENABLED=true \
ZHIXU_EINO_LIVE_API_KEY='...' \
ZHIXU_EINO_LIVE_MODEL='...' \
ZHIXU_EINO_LIVE_BASE_URL='https://provider.example/v1' \
ZHIXU_EINO_LIVE_TIMEOUT='30s' \
go test -run TestOpenAICompatibleChatSmoke -v ./live
```

门禁结论见 [report.md](report.md)。
