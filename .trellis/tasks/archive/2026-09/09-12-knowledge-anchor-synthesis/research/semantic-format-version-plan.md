# 融合复核输出格式：版本化兼容修复

2026-09-16。本次属于已批准的真实模型质量收口：修复独立语义复核的格式表达，不新增产品功能、协议模式、自动模型选择或思考强度设置。

## 证据

- DeepSeek 实际输出 `checks[].kind/source_verdicts`，但现有严格 Schema 和 decoder 只接受 `index/verdict/sources`；有限修复随后截断。
- `StructuredRunner` 的 Messages 不包含 Schema，repair 只发送稳定错误码；完整 Schema 只发送到 `response_format.json_schema`。不改变这个公共 Runner，不放宽解码或补默认字段。
- 官方 DeepSeek JSON Output 文档要求在提示中提供 JSON 示例，并指出输出可能截断；官方 Chat 参数列出的 JSON 模式为 `json_object`。此事实不能证明第三方网关或其 `deepseek-v4.1-flash` 别名的能力。来源：<https://api-docs.deepseek.com/guides/json_mode>、<https://api-docs.deepseek.com/api/create-chat-completion>（Context7 官方文档索引查询）。本轮不更换 response_format 或 Chat 协议。
- 单次合成对照实验保留原 API、Schema、模型默认强度和 8192 上限，在系统提示中追加精确字段/示例与可信 Schema。Redis 专项拒收 MySQL 案例通过：`{"checks":[{"index":1,"verdict":"SUPPORTED","sources":[]}]}`，输入 1141、输出 4456 token，24.05s，无额外调用。产物 `.zhixu/diagnostics/deepseek-prompt-probe-6c3b107e67.json`；临时测试源码已清理。不是生产账本/重放或完整五场景验收。

## 实施边界

- 保持既有 v1–v5 Prompt 文本、Schema 和旧 request hash 字节身份。
- 新准备的 SynthesisFrozenInput 显式冻结复核提示版本；省略该字段的历史输入继续选择原语义提示。新字段纳入冻结 hash、业务输入转换与 equality；不由运行中的全局配置临时决定。
- 新复核提示明确 `checks[].{index,verdict,sources:[{source,verdict}]}`、无来源时 `sources:[]`、禁止抄入输入 kind/source_verdicts。示例只表达格式，不预先指定语义结果。保留各既有目标、锚点、正文引用/局部刷新和证据约束。
- 新版本只改变 VALIDATE；生成提示及所有预算、阶段次数、拒绝语义保持。新复核 request hash 必须包含冻结版本，旧 READY/失败阶段不因升级重新付费调用。
- 同步 PostgreSQL `synthesis_model_contract_matches` 与 Go proof，通过前向迁移承认与冻结输入精确匹配的新复核版本；不能只在模型层改提示而绕过持久证明。不改已发布迁移，不更新历史输入或 ModelRun。
- 仅在隔离数据库验证迁移/模型证明。当前本地正式服务的目录 rebind 尚待用户确认，不能因此启动或修改其授权。

## 验收

1. 新实际语义请求包含精确 wire 形状、正确新 PromptRef，Schema v1 和原语义约束保持。
2. 旧 v1–v5 冻结输入、READY replay 与 hash 保持；旧失败/未知结果不追加外部调用。
3. 新冻结字段沿普通/锚点、目标、正文刷新 prepare→model→proof→apply 保持精确绑定；伪造版本或新旧交叉不能通过。
4. 迁移和真实 PG 模型账本/持久证明有定向成功与拒绝证据。
5. 按生产预算重新运行受影响真实融合场景并检查合成产物；失败时保留具体分类，不增加无界重试或降低判据。
