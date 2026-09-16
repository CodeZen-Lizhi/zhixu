# Semantic format v6 审查

2026-09-16。按 `go-review` 对本次 v6 格式修复做了独立只读审查；没有发现可定位的 P1/P2/P3 缺陷。

审查覆盖了 `SynthesisFrozenInput` 到 `SynthesisGenerationInput` 的冻结、校验、hash 与 equality 链路；普通、目标和正文刷新 prepare；agent 的 PromptRef/Schema/request hash 选择；以及 PostgreSQL 的 `synthesis_model_contract_matches`、`agent.model_run` 和 `synthesis_model_step` proof 触发器。

已确认的行为：

- 空 `semantic_prompt_version` 仍经 `omitempty` 保持旧冻结 JSON 的字段顺序和 hash；新 prepare 均冻结 `v6`，而已冻结输入直接回放，不会补写字段。
- v6 只在 `VALIDATE` 选择新 PromptRef，并只对验证 request hash 包装版本；生成阶段继续使用原有提示和 schema。READY、FAILED、RECOVERY_REQUIRED 的同节点记录继续通过既有 store fence 阻止再次调用。
- v6 格式提示保留独立语义复核的既有证据、锚点、目标、已发布正文和刷新约束，并额外声明严格 `checks[].index/verdict/sources[].source/verdict` 形状、空 sources 数组和完整 schema；decoder/schema 没有放宽。
- Go runtime proof 与 SQL proof 均从持久冻结输入判定版本。SQL 仅在 `VALIDATE + semantic_prompt_version=v6` 接受 v6，且要求 schema v1；生成阶段、旧/未知版本以及非字符串伪造字段都无法匹配。

复用的同版本定向证据来自 `research/semantic-format-implementation.md`：四个相关 Go 包的定向测试、普通/锚点/目标/正文刷新持久链路、失败与 unknown 不重付费、130 到 131 的历史 READY 回放、伪造冻结版本 SQL 拒绝、`go vet` 和 Atlas 校验均通过。审查未另跑全仓测试、真实模型或正式服务/数据库。

未验证：没有为每个历史 v2-v5 版本分别执行真实数据库升级回放；已有 hash/PromptRef 定向回归和 v1 前向升级证据覆盖了该兼容契约。真实 DeepSeek 五场景的语义质量验收不属于本次只读代码审查，仍由主流程负责。
