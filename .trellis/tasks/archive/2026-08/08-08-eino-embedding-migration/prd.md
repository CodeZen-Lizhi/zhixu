# Eino Embedding 正式生产替换

## Goal

使用 Eino 官方 OpenAI-Compatible 与 Ollama Embedding components 作为生产唯一实现；完整保留项目已有 Embedding 领域合同和外部 HTTP 安全边界。需要恢复旧实现时使用 Git 历史或兼容发布制品。

## Requirements

- 生产不再提供 `EmbeddingImplementation` 或 `eino|direct` selector；`.env.example`、Compose、managed runtime 和正式文档只表达 Eino。Eino 构造/调用失败 fail closed，不提供静默 fallback。
- Embedding 实现选择不进入 revision DTO、EmbeddingContract、ConfigHash 或数据库身份；恢复历史实现走 Git 发布记录，不在运行时切换。
- OpenAI-Compatible Eino 路径必须保留响应 `data.index` 重排、缺失/重复/越界拒绝和响应 model 一致性。
- Ollama Eino 路径必须显式发送 `truncate=false`，保留响应 model、数量和顺序校验。
- 两条路径继续执行请求批量/字节限制、响应 Content-Type/大小/单 JSON 值、维度/有限值/归一化、timeout/cancel/network/status 分类和脱敏。
- ext module 固定到 commit `90a15623ddb66465aea01fbe8c63ecc9d267acc1` 对应 pseudo-version，并验证其较旧 core 依赖不会破坏 root core v0.9.13。
- Eino 初始化或调用失败必须按现有稳定错误返回，不在同一请求内自动调用 direct。
- 旧 direct adapter、factory 分支和双实现 selector 按 2026-08-11 决策删除；稳定观察是独立发布质量证据，不是删除前置条件。

## Acceptance Criteria

- [x] 两个 Eino provider 均实现项目 `application.Embedder`，并满足既有领域 EmbeddingContract。
- [x] 共享 Eino Adapter contract suite 对 OpenAI-Compatible/Ollama 两种 Provider 组合全部通过；历史 direct 对照仅作为迁移证据。
- [x] OpenAI index/model、Ollama truncate/model、响应体上限、超时和错误映射有明确回归测试。
- [x] 精确依赖、vendor、race、vet、`git diff --check` 和 Go review 通过。
- [x] Config default、`.env.example`、Compose、managed runtime 和测试默认均切为 Eino，非法值/Eino 构造失败 fail closed。
- [x] 真实 Ollama endpoint smoke 通过，并记录 model、dimensions、批量、超时和脱敏结果。
- [x] 目标外部 HTTPS OpenAI-Compatible Embedding endpoint 真实 smoke 通过；2026-08-11 当前生产 Eino Adapter 已在脱敏 live gate 中完成两个输入、model、数量、dimensions 和有限值校验。
- [x] 历史 direct rollback 演练通过，切换不改变 EmbeddingContract/ConfigHash/Active Index 身份；当前部署不再提供该切换。
- [ ] 真实 Collector 灰度指标与稳定观察仍待完成，作为 Eino 发布质量证据，不影响旧 direct 删除决定。

## Notes

- Eino 的公开 `EmbedStrings` 只返回 vectors，不能单独证明项目所需 model/index wire contract；薄协议校验 wrapper 是必要边界，不是重复实现 SDK 或向量算法。
