# BGE-M3 接入与实际配置

2026-09-16。用户明确指定向量模型切换为 SiliconFlow `BAAI/bge-m3`；凭据只经隐藏输入传入内存和既有加密模型设置 API，未写入源码或本报告。

## 最新运行状态

同日用户明确授权更新本地服务并启用 BGE，现已完成：正式 launcher 构建启动、实际 Schema 99→130、停写备份验证、容器出口修复、真实生产 Probe（418 ms）及 activation 均成功。当前 desired/active/API/Worker applied 均为 **8**；BGE 为1024维，Chat 保持disabled。旧 revision7 的 Chat草稿保存在历史中。完整证据见 `bge-local-activation-20260916.md`。下方“尚未部署”的记录是此次授权之前的历史状态。

## 已定位并修复的兼容问题

现有 Eino embedding 构造器始终传入 `Dimensions`。真实生产 smoke 返回 `MODEL_EMBEDDING_REJECTED`；单条合成输入的相同请求经直接 HTTP 核对，带 `dimensions:1024` 返回 400/code20015，省略该字段返回200，模型身份 `BAAI/bge-m3`，实际宽度1024。

[SiliconFlow 官方 Embeddings 文档](https://docs.siliconflow.cn/docs/api/embeddings-post) 明确 dimensions 请求字段仅支持 Qwen/Qwen3 系列。修复在既有 Eino 构造点对精确 `BAAI/bge-m3` / `Pro/BAAI/bge-m3` 模型省略可选 SDK Dimensions；其余模型保持原行为。预期输出宽度仍在 EmbeddingContract 中，响应维度、模型、索引、有限值、归一化和 SDK/wire 交叉检查均保留。没有新增 Provider、重试、依赖、降级、模型路由或维度转换。

## 已验证

- 两个精确 BGE 模型实际 wire 都省略 dimensions；1024宽度通过、1023宽度按一致性错误拒绝。既有其他模型 wire 仍指定维度。定向 `go test ./internal/platform/models -run '^(TestEinoBGEEmbeddingUsesFixedWidthContract|TestOpenAICompatibleEmbedderBatchIndexAndFloatContract)$' -count=1` 退出0，包0.677s。
- 修复后真实生产 `TestEinoOpenAIEmbeddingLiveSmoke` 退出0，测试0.49s/包3.710s。两条合成输入均经真实 Eino adapter 调用并返回1024维有限向量；未读取个人笔记、未发起重建索引。
- 通过运行实例的既有 bootstrap Session 和受CSRF保护的模型设置API，CAS 保存 desired revision **6→7**。新 embedding为 `BAAI/bge-m3`、1024、l2、cosine，密钥已配置；随后独立GET逐字段断言并确认 Chat草稿完全保留。保存操作临时Session正常撤销。
- 当前 active revision **2**，Chat/Embedding均disabled；保存后 `apply_required=true`。运行中旧程序尚无BGE兼容修复，本次未部署/迁移实际用户数据库/激活。

## 相关聊天验收

用户提供的本地聊天网关可连接，但真实 `TestSynthesisLiveSourceReviewQuality` 在首个合成场景第一次调用失败，消耗1/4次允许调用，未执行后续融合套件。直接最小 Chat 请求返回503，说明找到候选提供商但无法为同步请求构建本地执行计划。不能将其记为语义质量通过，也不能据此判断模型整理质量。失败artifact为 `/tmp/zhixu-source-review-live-0916.json`，不含凭据。网关恢复后可重新使用未存在的新artifact路径继续既有有界验收。

授权前继续验收时再次核对同一本地Chat网关，最小请求仍返回相同503；未重复运行会立即失败的完整质量套件。当时尚未获更新授权。用户随后说明该网关使用Responses；对应最小请求返回502 / `Upstream access forbidden`，仍未产生语义质量结果。本地服务更新和BGE激活已在后续明确授权后完成，见顶部最新状态。
