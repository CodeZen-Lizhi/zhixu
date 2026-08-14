# Eino Embedding 历史实施结果

> 历史记录：本文件记录的是“默认 direct、Eino 条件 Go”的上一版离线实现结论。2026-08-08 用户明确要求 Eino 成为正式生产主路径；当前完成条件以本任务最新 PRD/design/implement 为准。
>
> 2026-08-11 决策进一步删除 direct selector、回滚脚本与部署 overlay；下文关于切回 `direct` 的描述仅是历史迁移证据，不是当前运维步骤。

## 结论

| Provider | 决策 | 当前默认 | 生产启用前剩余门禁 |
|---|---|---|---|
| OpenAI-Compatible | 条件 Go，可 opt-in 灰度 | `direct` | 目标供应商真实 endpoint smoke |
| Ollama native | 条件 Go，可 opt-in 灰度 | `direct` | 目标 Ollama 版本真实 `/api/embed` smoke |

“条件 Go”表示代码、离线协议合同、race、静态检查和回滚开关已通过；不表示已经替用户选择生产流量。任何单个 Provider 失败只需把共享进程配置 `ZHIXU_EMBEDDING_IMPLEMENTATION` 切回 `direct` 并重启 API/Worker/modelctl。

## 实现边界

- Eino ext 负责构造 Provider 请求和 SDK 调用。
- 项目验证型 RoundTripper 继续拥有 status、Content-Type、响应上限、单 JSON、model/count、错误脱敏和安全 transport。
- OpenAI 路径校验完整唯一 index，并按 index 还原输入顺序；Ollama 显式发送 `truncate=false`，拒绝 SDK 隐式 Authorization。
- 每次请求使用 context-scoped capture，48 路并发与 race 测试证明共享 adapter 不串状态。
- direct/Eino 沿用相同 `EmbeddingContract` 和 `ConfigHash`，避免仅因内部实现切换产生新 Embedding Version 或强制重建索引。历史 `AdapterName=direct-http` 在这里代表稳定 wire contract，不代表实际内部 selector。
- selector 与 Chat selector 一样属于进程级 Composition 配置，不进入受管 model revision、HTTP DTO 或数据库列；managed overlay 测试证明它不会被覆盖。

## 精确依赖

- `github.com/cloudwego/eino v0.9.13`
- `github.com/cloudwego/eino-ext/components/embedding/openai v0.0.0-20260803030130-90a15623ddb6`
- `github.com/cloudwego/eino-ext/components/embedding/ollama v0.0.0-20260803030130-90a15623ddb6`

两个 ext 仍是 pseudo-version，升级 commit 必须重跑四组合合同和真实 Provider smoke。

## 验证结果

```text
go test ./internal/platform/config ./internal/platform/models ./internal/modelsettings/runtime  PASS
go test -race ./internal/platform/models                                            PASS
go vet ./internal/platform/config ./internal/platform/models ./internal/modelsettings/runtime PASS
python3 deploy/compose_runtime_contract.py                                          PASS
go mod tidy -diff                                                                   CLEAN
git diff --check                                                                    PASS
```

独立 Go review 未发现 adapter correctness、context/timeout、call-state 并发、响应上限、SSRF/redirect、错误脱敏或 Provider wire 合同问题。发现的 `go.sum` 未收敛问题已通过重新 `go mod tidy` 和 `go mod vendor` 修复。
