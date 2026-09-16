# 同文不同来源：补源遗漏修复

2026-09-16。已批准PRD第40/56条要求：内容完全重复的新来源只追加补充来源记录，正文/版本/发布指针不变。本文属于该需求的质量修复，不新增产品功能。

## 失败证据与对照

- v6真实五类诊断 `.zhixu/diagnostics/deepseek-synthesis-c9ff087e06.json` 的duplicate：S001旧来源、S002独立同文来源，生成空notes，NO_CHANGE判SUPPORTED。绑定合法，但遗漏新增来源，验收失败。
- 两次直接合成对照 `.zhixu/diagnostics/deepseek-source-identity-45be4ca958.json`：只在可信提示明确“不同S-label代表不同来源身份，同文不等于已记录”；生成得到 `ADD_SUPPORT target=I001 alternative=null sources=[S002]`，domain apply为正文Changed=false/SourcesChanged=true；对刻意构造的空notes，复核判UNSUPPORTED。总8.93s，生成输出306token、复核380token，无修复调用。
- 对照保持Eino Chat、模型默认强度、8192输出与90秒诊断上限。不是正式模型账本、部署或30秒配置稳定性证据；临时Go源码已删除，私有runner/source保留方便审计。

## 实施边界

1. 保留原v1–v6提示和所有历史冻结/request hash/replay身份。新增GenerationPromptVersion（omitempty）；新普通/锚点/目标/正文引用分别冻结生成v7/v8/v9/v10，旧生成版本v1/v2/v3/v4继续登记。正文刷新生成v5不变。
2. 新semantic v7保留v6精确格式，独立检查新增支持来源是否遗漏。新生成版本必须与输入业务类型及semantic v7精确匹配；候选/目标/刷新已确定后才冻结版本。
3. 生成和Schema选择分离：正文引用仍是delta Schema v2，刷新仍是v3，其余v1；验证Schema始终v1。阶段请求hash独立绑定冻结版本，旧输入空字段保持原分支。Go owner及前向迁移132精确同步；不改131及之前迁移。
4. 在业务生成结果校验增加仅新版本启用的确定性遗漏拒绝：只针对非refresh的当前可信FACT/CONFLICT槽位，已纳入范围的新来源与该槽位已加载原引用逐字相同、来源身份不同且未在原引用或有效supplement中记录时，生成结果必须把该来源加到该槽位。只拒绝遗漏，不代生成ADD_SUPPORT、不跳过独立语义复核、不借旧machine items证明人工全文。正文刷新仅来源/引用变化仍NO_CHANGE。
5. 不修改生产timeout、思考强度、预算、共享StructuredRunner或严格decoder，不追加自动语义重试。生成提示本轮只增加来源身份义务；完整生成Schema仍通过既有response_format传递，不能宣称已经消除statement放平等格式错误。

## 验证

- 实际Messages、PromptRef/Schema、字段冻结/转换/哈希/版本类型拒绝、历史READY/FAILED/unknown无重复付费。
- 同文不同来源漏补拒绝；正确ADD_SUPPORT通过并保留原文；已记录来源不重复、不同条件/证据不足不强制补、范围外允许空结果、错误槽位不算完成、正文刷新例外保持。
- 隔离PG实际新模型账本/补源落库及重读、正文版本不变；131→132旧账本继续有效，伪造/交叉版本拒绝。
- 实现后使用真实合成流程重测；30秒已保存配置和90秒诊断明确区分。失败保留分类/原始合成产物，不放宽断言换取通过。

## 外部依赖边界

正式API/Worker仍因Root物理身份变化而停止；用户未确认原目录重绑。本修复只用隔离环境，不启动旧服务或修改正式DB。主会话完成独立审查、规范/验收记录及真实质量检查；实现代理只负责列明代码/迁移与定向验证。

主会话并行文件所有权：`synthesis_live_test.go` 由main维护；已补充质量断言失败的固定ErrorStage/Error分类，避免合法模型输出但业务断言失败时artifact看起来成功。新字段接入待实现代理给出选版本helper。该离线TestSynthesisLive*通过3.198s，未调用外部模型。
