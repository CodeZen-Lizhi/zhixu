# 按功能思考强度后端实施（2026-09-16）

状态：本轮后端实现与限定验证已完成；没有改动 UI 手写文件、凭据或用户数据库。

## 最终契约

`chat.reasoning_effort_by_function` 是八个固定键的封闭 object：`file_profile`、`knowledge_organization`、`anchor_scope`、`main_note_synthesis`、`manuscript_source_review`、`knowledge_qna`、`workspace_analysis`、`note_interview`。

- 缺 key 继承现有全局 `reasoning_effort`；key 值 `""` 显式使用模型默认，不发送 wire 字段；其余值为 `low|medium|high|xhigh|max`。
- GET 总是返回 object；PUT/Test 可以省略整个 map，省略解释为空覆盖 `{}`，即所有功能继承全局（旧客户端不会保留自己无法看到的覆盖）。拒绝 null、重复/未知 key、非字符串及非法档位。
- 仅 openai-compatible 允许非空 map；disabled/Ollama 拒绝任何条目，包括值为空字符串的条目。
- 静态 YAML `chat_reasoning_effort_by_function` 和 JSON 环境变量 `ZHIXU_CHAT_REASONING_EFFORT_BY_FUNCTION` 同语义。环境 map 整体替换 YAML map，显式 `{}` 可以清除覆盖，不使用 Viper 的默认 map 合并语义。
- 连接测试与 API/Worker 的激活探测会对全局及各功能的**不同有效强度各探测一次**，同档位复用一次；无覆盖时仍仅一次，最多六次。共享原调用 deadline，遇到第一次失败立即返回；没有新增重试、扩大任务预算或自动推荐档位。

## 实现与接线

Domain 新增可信 `ReasoningFunction` 和 `ReasoningEffortOverrides`；在输入 canonicalization、GORM Save/ResolveDraft、managed overlay 均复制 map。配置仍是原不可变 revision 的一部分，不增加可变运行时侧表。

`platformmodels.ModelRuntime` 按有效强度去重创建结构化 Chat 与普通/stream RuntimeChat 两种 adapter，并由同一个 generation 管理释放。只保留已构造能力和非敏感 contract，不持有输入 map 或密钥配置；`ChatFor`/`RuntimeChatFor` 对未知 function 返回 disabled。

Worker composition root 在初始构造和 managed 重建两处接入功能：

| 功能 | 实际消费者 |
| --- | --- |
| file_profile | Capture 文件简介、标签、知识画像 |
| knowledge_organization | 关系判断、Artifact、整理生成 |
| anchor_scope | 主笔记范围和来源关联建议 |
| main_note_synthesis | 目标知识选择、融合生成和结果语义校验 |
| manuscript_source_review | 当前主笔记全文的来源支持复核 |
| knowledge_qna | RAG 结构化阶段、工具 Agent 和答案流 |
| workspace_analysis | 工作区规划/检索/校验、循环及候选答案流 |
| note_interview | 笔记访谈准备 |

所有适配器从同一个冻结 revision 选择；保留原 lease/attempt/generation 路由，不在请求时读取新 desired。调用方 SDK option 仍不能覆盖冻结强度。没有新增 Responses、工具协议或功能专属模型/密钥。

00130 添加 jsonb 非空默认 `{}` 和封闭 key/value/provider CHECK；00129 保持不变。旧行的全局档位和其他字段（包括密文/AAD身份）不变，append-only guard 保留。schema 来自应用 130 的真实隔离 PostgreSQL pg_dump，保留 00080 数据库局部权限 footer。

OpenAPI 增加 `ModelReasoningEffortOverrides`、summary 必填/写入可省略及 provider 限制；project checker、generator input manifest、生成 TS 和生成指纹已同步。

## 验证证据

1. `GIN_MODE=release go test ./internal/modelsettings/... ./internal/platform/config ./internal/platform/models`：全部 PASS。日志 `/tmp/zhixu-function-reasoning-unit.log`。覆盖严格 HTTP/静态输入、provider 限制、三态选择、map 输入不影响已建能力、相同档位复用及原模型设置回归。
2. `env -u ZHIXU_TEST_DATABASE_URL go test -tags integration ./internal/modelsettings/runtime -run '^TestReasoningEffortPersistedRevisionReachesRuntimeConnectionProbe$' -count=1 -timeout=3m`：PASS 9.994s。真实 Testcontainers PostgreSQL + 全 130 迁移 + SaveDesired/Audit/加密/LoadRevision + Eino adapter + 本地真实 HTTP。分别读取两代 revision，证明 file=low/max、main=high、qna 继承 medium/high、interview 显式模型默认；结构化和普通 Generate 都发出相应字段或正确省略，调用方原预算不变。不同强度的连接探测去重，保存新 revision 不改变旧请求，也不推进 active。日志 `/tmp/zhixu-function-reasoning-runtime.log`。
3. `env -u ZHIXU_TEST_DATABASE_URL go test -tags integration ./cmd/worker -run '^TestWorkerSynthesisCompositionImportsContinuouslyAndReplaysWithoutNewResults$' -count=1 -timeout=3m`：PASS 16.862s。真实 Worker/River 文件导入→简介→主笔记融合和核验；HTTP fixture 要求简介 low、融合生成和核验 high，否则实际流程失败。原连续导入及重复执行结果不变断言保留。日志 `/tmp/zhixu-function-reasoning-worker.log`。
4. `go test -race ./internal/platform/models -run '^(TestConfiguredModelRuntimeCapabilitiesAreConcurrentReadSafe|TestConfiguredModelRuntimeCompensatesChatWhenEmbeddingBuildFails|TestFunctionReasoningCapabilitiesFreezeAndShareEffectiveEffort)$' -count=1`：PASS 1.760s。覆盖并发读取按功能能力、共享有效档位、后续构造失败时每个独占 transport 恰好关闭一次。日志 `/tmp/zhixu-function-reasoning-resource-race.log`。
5. `go vet ./internal/modelsettings/... ./internal/platform/config ./internal/platform/models ./cmd/worker ./cmd/api`：exit 0。`go test ./cmd/api ./cmd/worker -run '^$'` 两个二进制包编译 PASS。日志 `/tmp/zhixu-function-reasoning-vet.log`、`/tmp/zhixu-function-reasoning-binary-compile.log`。
6. 补充 disabled 配置边界：`go test ./internal/platform/config -run '^TestFunctionReasoningOverridesYAMLEnvAndStrictShape$' -count=1` PASS 0.538s；从 YAML 启用配置切换 disabled 时，覆盖清空且不读取 gated 环境 map，日志 `/tmp/zhixu-function-reasoning-disabled-boundary.log`。
7. 真实隔离 129→130 SQL 升级：旧高强度行除新增列外逐字段 JSON 等价；新增列 `{}`；null/array/未知 key/null 值/非法档位拒绝；合法 map 保存；旧 UPDATE guard 拒绝。日志 `/tmp/zhixu-reasoning-schema-130.log`，重现脚本 `/tmp/zhixu-reasoning-schema-130.sql`。schema 恢复进独立空库成功，包含末尾权限语句：`/tmp/zhixu-reasoning-schema-130-restore.log`。隔离容器 `zhixu-anchor-schema-0914` 已恢复为原先停止状态。
8. `make atlas-migrate-hash-check atlas-migrate-validate atlas-migrate-lint`：PASS（130 文件），日志 `/tmp/zhixu-function-reasoning-atlas-check.log`。
9. `make openapi-generate-check`：lint/project/routes/tags（258 operations）/生成一致性/生成 TS typecheck 全部 PASS，日志 `/tmp/zhixu-function-reasoning-openapi-check.log`；scoped `git diff --check` exit 0。

## 文件与限制

本轮：`.env.example`；`internal/modelsettings/domain/{reasoning,settings}.go`、`http/handler{,_test}.go`、`adapter/postgres/{gorm_revision,revision_codec}.go`、`runtime/{models,models_host,reasoning_effort_integration_test}.go`；`internal/platform/config/{config,loader,chat_config_test}.go`；`internal/platform/models/runtime{,_test}.go`；`cmd/worker/{main,model_runtime_hot,synthesis_interview_components,synthesis_composition_integration_test,synthesis_composition_provider_integration_test}.go`；00130、atlas.sum、schema.sql；OpenAPI/check/generator manifest 与 generated TS。

按 go-review 自检了 map 共享、资源释放、strict decode、参数绑定、schema 约束及真实调用方；未发现待修复的本轮后端问题。新增适配器失败补偿路径通过定向 race 测试。

本地 provider fixture 只证明协议、配置和执行接线，不能证明真实模型效果或费用；本子任务未调用外部模型。尚未进行完整 API+Worker 双进程热应用端到端、八功能各一条完整业务流程或 UI 浏览器流程（主会话协调）。两代 revision 保存/回读/实际请求隔离已验证，六个其余消费者按接线检查与冻结 runtime 测试覆盖。没有全仓测试、提交、迁移用户数据库或改变既有凭据。
