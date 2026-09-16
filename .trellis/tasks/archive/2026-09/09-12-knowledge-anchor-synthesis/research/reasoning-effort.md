# 模型思考强度

用户于 2026-09-16 明确要求软件加入思考强度设置。沿用模型设置不可变 revision、保存/应用分离、API/Worker 热运行时和现有 Eino Chat 路径。

## 契约

- Chat `reasoning_effort`：空字符串表示模型默认，其他值仅 `low|medium|high|xhigh|max`。默认时不向 Provider 发送此参数；非法值拒绝，不静默降低强度。
- 新增请求字段允许省略以兼容旧客户端，读取投影明确返回当前值。数据库旧记录默认空值；旧 revision 的凭据及其他事实不变。
- 前端中文选项：模型默认、低、中、高、很高、最高；在 Chat 设置中编辑，保存与测试连接都携带相同值；active 摘要展示生效值。Embedding 无此设置。
- 此次只实现全局 Chat 强度，不自动改变不同业务任务的档位。模型默认不等于关闭思考。
- OpenAI-compatible 可配置；不支持此参数的 Provider 不伪装生效。Ollama/disabled 保持默认。
- GPT-6 Astra 官方模型名 `gpt-6-astra`，支持上述五档；Chat Completions 使用 `reasoning_effort`，不接受 temperature/top_p/top_logprobs/logprobs。处理现有固定 temperature 和输出预算参数兼容；不引入 Responses 协议。
- 官方证据：https://developers.openai.com/api/docs/models/gpt-6-astra 、https://developers.openai.com/api/docs/guides/latest-model （本次已实际打开）。

## 实施与验证分工

- 后端：domain/config、存储迁移、HTTP、managed/static runtime、结构化与通用 Chat、OpenAPI/生成客户端；定向验证实际请求和旧配置兼容。
- 前端：strict decoder/encoder、表单及生效摘要；复用现有控件和保存/应用状态机。
- 主会话：整合、真实最小页面流程、规范更新；实施结束由独立 trellis-check 检查跨层遗漏。
- 只验证本次关键行为，使用现有测试及必要临时复现；不运行全仓测试，不对用户数据库或模型配置做验收写入，不调用外部模型。

状态：实现、定向验证和独立检查完成，无已证实的待修 P1/P2（见 reasoning-effort-review.md）。后端证据见 reasoning-effort-backend.md，前端见 reasoning-effort-ui.md，实际浏览器见 reasoning-effort-browser.md。真实模型质量仍依赖用户配置，本功能的本地验收不代表外部模型语义质量通过。

用户再次追问统一强度的作用，主会话明确：之前按摘要/融合/核验分配不同档位仅是建议，本次不实现自动分档。用户设置并应用后，后续使用该模型配置的新任务统一采用所选强度；已有在途任务遵守原冻结 revision，模型默认空值由 Provider 决定。

临时浏览器、Vite和Testcontainers均已关闭；未提交、未修改用户运行数据库或模型配置。00129迁移SHA256：`1a767b25faf3a6582b5a58e4a5f1c7e517618b0096742184469f2e28d0787f25`。
