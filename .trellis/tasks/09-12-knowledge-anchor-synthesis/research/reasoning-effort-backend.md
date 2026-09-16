# 思考强度配置后端实施（2026-09-16）

状态：实现与后端定向验证完成。

- HTTP `chat.reasoning_effort`：写入可省略（归一为空字符串）；读取始终显式返回字符串。允许 `""/low/medium/high/xhigh/max`，只有 openai-compatible 可非空；disabled/Ollama 拒绝非空。
- `ChatSettings.ReasoningEffort` 经 SaveDesired、immutable revision、GORM scan、managed overlay、两种 Eino runtime、连接草稿测试、generation contract 完整传递。沿用 desired/active/applied 与原热应用和冻结 Attempt 协议，不改变 Secret AAD、Audit allowlist 或模型身份。
- 静态 `ChatReasoningEffort` / `chat_reasoning_effort` / `ZHIXU_CHAT_REASONING_EFFORT`，默认空；loader disabled gate 清空低优先级值且不读取 gated 环境值。
- 00129 只加 `chat_reasoning_effort text NOT NULL DEFAULT ''` 及枚举/provider CHECK。旧 revision 逐字段相同，新列空；append-only guard保留；atlas.sum及schema已同步。
- 结构化和普通 Generate/Stream（含已有工具路径）统一冻结请求强度。空值不发送 wire 字段；显式强度或 `gpt-6`/`gpt-6-*` 使用 `max_completion_tokens`（保持调用方原数值，不加大任务预算），移除 temperature/top_p/top_logprobs/logprobs。旧模型默认请求仍为 temperature=0/max_tokens。
- 连接探测单独把推理模型预算从64调整为有界2048，给可见输出和 reasoning tokens 共同使用；不改变任务预算、超时或失败重试，不保证每种模型强度2048都足够。
- 保留普通 Runtime 既有冻结 ModelVersion wire 行为，未顺带修改模型别名逻辑。没有引入 Responses 协议；GPT-6 官方仅在 Responses 提供工具调用，本轮不承诺 GPT-6 工具工作流可用。
- OpenAPI、project checker、generated TS client已更新；六档和provider限制一致。

已得到的证据：

- 真实 PostgreSQL `TestRepositoryRevisionRolloutRuntimeAndEnqueueFence`，10.971s，保存low→新revision high→分别重读、Secret keep重加密、原rollout/runtime/fence：`/tmp/zhixu-reasoning-postgres.log`。
- 真实128→129升级，旧行JSON除新列外逐字语义一致；非法枚举、disabled非空 CHECK拒绝；旧append-only UPDATE拒绝：`/tmp/zhixu-reasoning-schema-129.log`。
- schema来自隔离最终128数据库clone应用129后pg_dump，保留00080数据库局部权限；恢复目标 `zhixu_reasoning_schema_129_restore`，日志 `/tmp/zhixu-reasoning-schema-129-restore.log`。
- OpenAPI lint/project/routes/tags PASS，258 operations：`/tmp/zhixu-reasoning-openapi.log`。
- Atlas validate/lint PASS，129 files：`/tmp/zhixu-reasoning-schema-129.log`。

最终验证：

- `GIN_MODE=release go test ./internal/modelsettings/domain ./internal/modelsettings/application ./internal/modelsettings/http ./internal/modelsettings/runtime ./internal/platform/config ./internal/platform/models` 全部PASS：`/tmp/zhixu-reasoning-unit.log`。包含四种真实本地HTTP wire场景（旧默认、显式high、GPT6默认、GPT6max），两种Runtime均相同；调用方SDK effort不能覆盖冻结配置；数值预算保持；HTTP可省略/拒绝null/连接草稿传递；Domain和Config非法/provider边界。
- `go vet ./internal/modelsettings/... ./internal/platform/config ./internal/platform/models` exit0：`/tmp/zhixu-reasoning-vet.log`。
- `env -u ZHIXU_TEST_DATABASE_URL go test -tags integration ./internal/modelsettings/runtime -run '^TestReasoningEffortPersistedRevisionReachesRuntimeConnectionProbe$' -count=1 -timeout=3m` PASS8.545s：`/tmp/zhixu-reasoning-runtime.log`。真实PG保存high/low两个revision→各自重读→生产ConnectionTester/完整Eino Runtime→精确loopback HTTP，两次请求各携带正确强度、2048探测总预算，无temperature/max_tokens；测试不推进active，Secret不出快照。这个fixture使用已允许读取的legacy loopback目标，只为控制本地HTTP；新远端draft仍要求HTTPS。
- standalone schema恢复 exit0，末尾数据库局部权限也应用成功：`/tmp/zhixu-reasoning-schema-129-restore.log`。
- scoped `git diff --check` exit0。

实现文件：`.env.example`；`internal/modelsettings/domain/settings{,_test}.go`、`http/handler{,_test}.go`、`adapter/postgres/{gorm_revision,revision_codec,repository_integration_test}.go`、`runtime/{models,models_test,reasoning_effort_integration_test}.go`；`internal/platform/config/{config,loader,chat_config_test}.go`；`internal/platform/models/{chat_protocol,chat_http,chat_factory,eino_chat,eino_runtime_chat,eino_runtime_chat_test,runtime}.go`；`atlas/migrations/00129_model_settings_reasoning_effort.sql`、`atlas.sum`、`atlas/schema.sql`；OpenAPI schema/checker/generator input manifest与generated model TS。

观察到并修正：第一次普通Runtime wire尝试写ModelID引发既有三个model-alias测试失败，已恢复原ModelVersion wire契约（不扩展无关行为）。HTTP fake manager不承担领域验证，非法枚举/provider断言放在实际Domain/Config/PG边界；null仍由HTTP拒绝。一次外部admin fixture连接失败（临时容器认证不同）后改用项目默认Testcontainers，后续PG测试全部实际执行，无skip。


文档依据（实际读取）：
- https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create ，max_completion_tokens包含可见输出与reasoning；旧max_tokens弃用。
- 主会话已验证 https://developers.openai.com/api/docs/models/gpt-6-astra 及 https://developers.openai.com/api/docs/guides/latest-model 的5档、参数限制和工具仅Responses边界。

限制：无真实Provider密钥，未调用外部模型；本地fixture仅证明参数和配置传递，不能宣称真实模型语义质量通过。未跑全仓测试、双进程完整Compose滚动激活或整站浏览器（主会话负责页面最小流程）；未提交、未迁移用户数据库。
