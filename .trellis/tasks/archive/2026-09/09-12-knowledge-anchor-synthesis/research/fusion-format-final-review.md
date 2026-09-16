# Fusion 目标、生成格式与原来源资格最终复审

2026-09-16，fusion-final-review。限定 133/134/135 三轮实施范围与 main 的 live 夹具调整，使用 trellis-check/go-review，未派发、未改生产代码或测试、未运行真实模型、服务、配置/rebind、用户数据库或提交。仅新增本报告。补源容量 P2 复用 `source-identity-review.md` 末节关闭结论，不重审。

## 结论

最终只读复审完成，未发现待修确定缺陷；已补读135最终实施报告并核对最后追加的历史重放/unknown测试及日志。main 的五类真实融合现已全部通过并完成人工核对，36.71秒、10次调用；该质量判断归 main，本 reviewer 复用其最终验收记录，不重复调用模型或执行已通过测试。

## 代码与调用链证据

- **可信生成格式。** `internal/organizing/adapter/agent/synthesis_catalog.go:82` 为 v13–v18 单独注册不可变 PromptRef；`:96` 使用与实际 Schema 相同的 `synthesisDeltaSchemaVersion` 序列化完整 Schema 至 system 消息，明确 ADD_FACT.statement 嵌套、禁止 fact 别名和放平。正文 v16/v18 保留 delta Schema v2，其他新生成版本 v1；refresh 沿原 v5/Schema v3。`:185` 的类型选择、`synthesis_binding.go:221` 的原业务 decoder 与 application 原业务分类一致，没有通过放松 decoder 修复格式。`synthesis_model_test.go:876` 实际 StructuredRunner INITIAL/REPAIR 请求验证六个版本的完整可信 Schema。
- **冻结版本精确配对。** `application/synthesis_model.go:317` 保留旧 pair，v9 semantic 仅允许 anchored/v17 或 body/v18；`:349` 只为新冻结输入选择最新版本。`workflow/synthesis_contract.go:214` 和 application 的整体验证都检查唯一目标及 Fusion Note/Anchor/ScopeVersion/AllowedSources 完整绑定。`adapter/synthesispostgres/model_steps.go:392` 从已验证 frozen input 取精确 stage 版本，`:413` 独立核 Prompt/Schema/ReducedSchema，不能把 generation v9 与 semantic v9 混作同一契约。
- **付费前目标约束与输出门禁。** `workflow/synthesis_executor.go:223` 先筛唯一目标，`:242` 限定已批准来源，`:260` 重验当前准入；完整候选和来源确定后 `:460` 冻结版本。`adapter/agent/synthesis_model.go:149` execution 在 invoke/Prepare/Provider 前调用 input.Validate；`application/synthesis_contract.go:209` 仍拒绝新笔记或其他目标的 Fusion 输出。普通 source-ready 保留新主题能力，goal 在 `:175` 要求恰好一篇新主笔记；body/refresh 原业务边界保留。
- **短标签投影。** `adapter/agent/synthesis_binding.go:171` 只在 Fusion semantic v8/v9 投影目标 N-label；来源、条目继续 S/I-label，不把 request/note/anchor/proposal UUID 交给 Provider。semantic `synthesis_semantic.go:55` 附目标 scope，NO_CHANGE 仍独立复核唯一目标范围和新增来源义务。
- **existing_sources 精确资格。** `synthesis_semantic.go:60` 仅 v9 添加该字段；`:283` 遍历目标投影的 FACT、全部 CONFLICT alternatives、GAP context/resolution，按遇到顺序去重，空集合编码为 `[]`。`synthesis_binding.go:79` 按完整 `SynthesisSourceRef` 映射本次已加载来源，`:101` 只遍历 `Revision.Items`，`:124` 按当前 item/有效 slot 追加 supplements；`application/synthesis_contract.go:114` 在 Provider 前另校验 supplement 归属和 MatchesItem。没有扫描 MachineItems、其他笔记或把 incoming=false 当资格，未加载历史也没有可投影标签。
- **资格不冒充支持。** 新 system/instruction 明确 existing/admitted 是引用资格并集，不扩大 scope，不证明 statement/applicability 或冲突成立。`synthesis_semantic.go:108` 各冲突 alternative 独立检查并追加 CONFLICT_RELATION；`:252` 对每个 check、每个来源逐项要求 SUPPORTED 和精确来源顺序。`synthesis_model_test.go:950` 已有来源引用错误十五分钟结论的反例被拒绝，重放不追加调用。原独立 semantic 和原三阶段预算未被替换。
- **旧消息和恢复。** v9 字段为 nil 时 omitempty，v8 semantic 不带 existing_sources；生成投影不增加此字段。直接对比 `/tmp/fusion-existing-baseline/synthesis_catalog.go`，135 仅新增 v9 文本与注册，原 v1–v18 generation/旧 semantic 定义未改。134 的旧 catalog 字节比较、v11/v12 READY 重放证据见实施报告；hash 按 stage 加原冻结版本，未用 Latest helper 重算历史。FAILED/unknown 账本入口未新增重试或绕过；既有失败回放测试继续属于定向包通过范围，不宣称每种历史 pair 均做过独立实库升级。
- **SQL 前向兼容。** 133→134 增加格式版本配对；134→135 仅追加 `(semantic='v9' AND fusion_bound AND generation=CASE original ... v17/v18)`，旧分支逐字保留。fusion_bound 同时要求真实 processing fusion_request、冻结 request、唯一 note、anchor、scope 和双向完整 allowed_sources JSONB 包含/等长。135 无数据回写。已用本地文本比较确认 `atlas/schema.sql` 对应函数正文与135逐字一致。
- **main live 夹具。** `adapter/agent/synthesis_live_test.go:423` 为四个目标更新案例构造完整 Fusion trigger，`:429` 使用 Latest helper；混合材料仍走 goal。`:134` 起保留单目标、纯补源不改正文、有意义处理、冲突双方来源等质量断言。诊断仅提取分类错误及枚举化阶段/原因/HTTP状态，未输出 raw cause、URL/header 或配置；安全 canary 测试位于 `:479`。本轮只核对合成夹具与安全字段，不读取真实调用产物或密钥。

## 已复用验证

- 133：直接核对 `/tmp/fusion-target-unit-final.log`、`fusion-target-pg-fixed.log`（26.336s）、`fusion-target-manuscript-pg.log`（35.026s）；五包 vet、Atlas 的退出0依据 `fusion-target-implementation.md`，不凭空日志推断退出码。
- 134：直接核对 `/tmp/generation-format-unit.log`、`generation-format-history.log`（旧 catalog 字节比较）、`generation-format-owner-pg.log`（38.620s）。四项 runtime PG（36.173s）、五包 vet、Atlas 结果复用 `generation-format-implementation.md`。
- 135：已直接读 `/tmp/fusion-existing-unit.log`（四包通过）、`fusion-existing-pg.log`（32.392s）、`fusion-existing-history.log`（0.200s）、`fusion-existing-atlas.log`（135 files lint 通过）。最终 `fusion-existing-evidence-implementation.md` 确认四包 integration-tag vet、Atlas hash/validate/lint 均通过；不是从空 vet 日志推断退出码。
- 135最后追加测试：直接读 `/tmp/fusion-existing-final-agent.log`（0.684s）并核对 `synthesis_model_test.go:905` 后最新代码，旧 v11/v12/v17/v18 + semantic v8 READY 重放保持两次总调用，v8消息/payload不含 existing_sources；v9 business commit unknown 和 ModelCall completion unknown 两种场景第二次调用返回 replay unsafe，总 Provider 保持2次。生产语义拒绝仍返回原错误，未为测试放宽。
- 独立静态核对 `runtime_integration_test.go:1014`：实际134库创建 v17/v8 Fusion generation/semantic READY，迁移135后 frozen JSON不变，SQL只接受原v8，继续实际 apply；总 Provider 4次，唯一目标产生第二修订。新135正常 Fusion 的冻结版本断言位于 `:233`。
- 迁移摘要：133 `f35c3c83ce36469ede47c5466ec8e88551f6f71fb7053587a32d1464f89dbbc9`；134 `3f8426a18881ede227dfa1c30d1e8514939b85c0c43c9ba29bf12bc65e9f4cbb`；135 `85d6a53428c3279d36214facf4ed88ab103a1f92ac2d1770e1a5cec4a1c610c3`。133/134 与原报告一致。

## 限制与交接

已补读 [deepseek-chat-activation-20260916.md](deepseek-chat-activation-20260916.md) 末节“五类真实融合最终通过”：Redis范围排除、混合面试单篇生成、同文不同来源补源、不同适用条件、相反观点且条件未知均通过。main 逐项核对完整合成输入、十次原始输出、条目和正文；保存的30秒/8192预算下，每例生成与独立复核各一次，无格式repair，没有增加生产超时/预算。产物标识为 `deepseek-synthesis-890bca50b5.json`，本 reviewer 未重新读取原始产物或作第二次模型质量判断。

上述真实质量证据限于指定模型的有界合成五场景，不代表任意笔记准确率、正式API/Worker已启用、用户笔记已写入或发布完成。正式启用及目录身份/rebind边界仍以 main 的交接为准，不属于本次只读复审的未完成事项。

未重复执行 Go/PG/Atlas，无全仓检查；固定 Provider 与隔离 PG 证明上述合同和持久链路，不证明真实模型语义。未验证每一历史版本的完整 READY/FAILED/unknown 实库矩阵，也未验证正式部署或用户数据升级。

本 reviewer 额外执行限定文件 `git diff --check`，退出0；没有其他新增测试执行。

Trellis 进度发送因沙箱禁止写 `~/.trellis/channels/.../source-identity-review-0916.lock` 返回 EPERM，消息未送达；采用此共享报告交接，不更改权限或服务。

复审完成：新增确定问题0，代码修复0，开放确定问题0。没有需要协调实施代理修改的生产代码。done
