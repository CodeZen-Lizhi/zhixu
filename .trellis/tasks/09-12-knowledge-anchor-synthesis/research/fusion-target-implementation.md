# 融合目标显式投影实施记录

生产代码与限定验证已完成，可交 main 审查及真实模型验收。新冻结 fusion 使用 GENERATE v11（原 anchored v2）或 v12（原 body v4），VALIDATE v8；普通入库继续 v7/v8/v9/v10 + semantic v7，refresh 保持 v5/v7，旧 prompt/message/payload/hash 分支不改变。

main live fixture 在设置 SourceEvent.Fusion、Notes 与 Anchor 后使用：
`input.GenerationPromptVersion, input.SemanticPromptVersion = app.LatestSynthesisPromptVersions(app.SynthesisOriginalPromptVersion(input), input.SourceEvent.Fusion != nil)`。

新字段仅投影 `fusion_target: "N001"`，不泄露 UUID/准入身份；semantic 还收到 target scope，NO_CHANGE 只比较这一已确认范围内应有的知识和来源。新版本调用前严格校验仅一个目标候选与 trigger 的 Note/Anchor/ScopeVersion/AllowedSources 完整一致；错 target/new-topic output 仍被原结果门禁拒绝。历史 fusion 请求不投影新字段，真实 old READY 保持原 hash。原 body schema/decoder仍按 original kind 选择。

本 follow-up 在 `application/synthesis_contract.go` 仅修改 input.Validate 顶部一行版本判断以调用新的整体验证 helper；未改共享补源容量/遗漏 gate。adapter/model.go 仅修改 catalog version 注册与 hash validator，未改 identity-preflight worker 的 model preflight 部分。其余代码为 app helper/constants、workflow frozen version/target 校验、adapter binding/semantic/prompt、Go proof与133迁移/Schema。未编辑 live harness、Spec、服务、配置、密钥。

- 四包 targeted Go PASS：`/tmp/fusion-target-unit.log`，0.849/0.256/0.658/0.208s。
- 正式133前向迁移、实际 fusion runtime、132→133旧 v7 READY proof、SQL伪造版本验证 PASS 26.336s：`/tmp/fusion-target-pg-fixed.log`。首次运行发现 JSONB 路径运算符与包含比较组合需要显式括号，133尚未成功执行时已修复，重新跑同一组全部通过；原失败日志 `/tmp/fusion-target-pg.log` 保留。
- 人工正文 admitted/audit_duplicate 两条实际 PG/River/模型证明流程 PASS 35.026s：`/tmp/fusion-target-manuscript-pg.log`。覆盖新融合在人工正文上的后续生成和零可信条目补源仍要求独立全文核对。
- 补齐旧 fusion payload/READY 重放测试后，agent/workflow/synthesispostgres 定向 Go 最终 PASS：`/tmp/fusion-target-unit-final.log`，0.287/0.186/0.059s；应用层本轮唯一契约修改已在先前四包 PASS 范围内。
- 五包 `go vet -tags=integration` 退出0：`/tmp/fusion-target-vet.log`。
- Atlas hash/validate/lint 全部退出0（133 files）；定向 diff --check/gofmt 通过。Schema中本次函数与133逐字一致。133 SHA256 `f35c3c83ce36469ede47c5466ec8e88551f6f71fb7053587a32d1464f89dbbc9`；131/132未改。


## 修改文件

- application：`synthesis_model.go`、`synthesis_model_execution.go`；`synthesis_contract.go` 仅顶部一行整体验证调用。
- workflow：`synthesis_contract.go`。
- agent adapter：`synthesis_binding.go`、`synthesis_semantic.go`、`synthesis_catalog.go`、`synthesis_model.go`、`synthesis_model_test.go`。
- synthesispostgres：`model_steps.go`、`runtime_integration_test.go`。
- postgres 现有测试夹具迁移版本升到133：`synthesis_goal_generation_integration_test.go`、`synthesis_body_roundtrip_integration_test.go`、`synthesis_body_refresh_integration_test.go`、`synthesis_manuscript_runtime_integration_test.go`。
- `atlas/migrations/00133_synthesis_fusion_target.sql`、`atlas/migrations/atlas.sum`、`atlas/schema.sql`；本报告。

## 关键证据与边界

- 实际模型 Messages 有唯一 `fusion_target` 与确认 scope，无 request/anchor/note/proposal UUID；未把身份或权限交给模型决定。
- 新 v11/v12 + v8 必须有精确冻结 Fusion 绑定；普通 source-ready 没有 Fusion 时继续能生成新主题。缺触发、错 note/anchor/scope/sources 在 Provider 前拒绝；生成新 note 的一次合法 JSON 响应被既有结果门禁拒绝，无新增 retry。
- v12 继续使用原 body schema v2，v11 使用 v1。NO_CHANGE 的复核明确只检查该目标范围内的知识与补源义务，不因范围外 MySQL 要求建立新笔记。
- 旧 v8/v10 + v7 的 fusion 生成/复核 payload 不含新字段，实际 READY 重放无新增调用；132普通 v7账本升级133后仍可 apply，Frozen JSON 不变。
- 按 go-review 自检版本和序列化、阶段 proof、Scope/target投影及失败行为，未发现待修确定问题。Go/SQL/helper 使用 frozen input，不读当前设置作隐式升级。
- 没有为每个历史类型建立独立升级实库；新 body v12 schema/投影经实际模型 adapter 测试，融合持久链路和人工正文经上述PG流程，未宣称全类型外部模型质量通过。正式部署、真实模型调用和范围更广的任务验收由main继续。
