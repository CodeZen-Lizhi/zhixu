# Eino Embedding 正式生产替换实施计划

## 已完成基础

1. 历史实现曾增加进程级 `EmbeddingImplementation` selector；按 2026-08-11 决策已从生产配置/部署面删除，未写入 revision DTO/数据库。
2. 已精确 pin OpenAI/Ollama embedding ext 并更新 vendor。
3. 已实现并发安全的 Eino OpenAI-Compatible/Ollama adapter 和 bounded validating RoundTripper；当前合同套件覆盖两种
   Eino Provider，旧 direct 对照只保留为历史迁移证据。
4. 已通过离线协议、race、vet、依赖一致性、`git diff --check` 和 Go review。

## 当前状态与剩余门禁

1. 已完成 Config、`.env.example`、Compose、managed runtime 和测试默认改为 Eino；构造失败 fail closed。
2. 已完成离线 OpenAI-Compatible/Ollama wire contract、并发/race、超时/取消和错误脱敏验证；2026-08-11 真实外部 HTTPS OpenAI-Compatible Embedding 与本机 Ollama endpoint smoke 均已通过。
3. 在测试/灰度环境继续观察延迟、错误率、维度/一致性错误等 Eino 指标；稳定观察是独立质量证据。
4. 历史 `make compose-rag-rollback-smoke` 曾完成 Eino→direct→Eino 演练，未改变 EmbeddingContract、ConfigHash、Active Index 或任务恢复合同；脚本/overlay 已删除，当前恢复依赖 Git 历史。
5. 删除 direct adapter、factory branch、selector 和双路径测试；保留 Eino Adapter 合同、历史 v1 持久回放和网络 transport `direct`/`host-relay`。
