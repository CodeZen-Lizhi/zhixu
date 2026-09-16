# 来源身份遗漏修复实施记录

2026-09-16。实施与限定验证已完成，可开始独立审查和 main 的真实模型验收。未操作正式服务、设置或目录绑定；未提交或 push。

- `GenerationPromptVersion`：`generation_prompt_version,omitempty`；旧空字段不增加历史 JSON 字节。新生成版本 `v7`=旧 v1、`v8`=旧 v2、`v9`=旧 v3、`v10`=旧 v4；正文刷新仍 v5，冻结 generation 字段为空。
- 所有新 prepare 同时冻结 semantic `v7`。版本选择在候选/admission 完整加载之后；原始输入形状继续决定 schema/decoder，body v10 仍 schema v2。新字段参与 input hash、转换、equality、stage hash、Go/SQL proof。
- 旧空 generation + 空或 v6 semantic 保留。新 generation 必须与原始类型精确匹配，且 semantic=v7；仅 refresh 可以空 generation + semantic=v7。老 v1–v6 提示原文、hash envelope 保持。
- 新系统提示追加明确不同 S-label 为不同来源的规则；生成必须用 ADD_SUPPORT 保留支持来源，NO_CHANGE 不得漏来源。正文刷新仅来源改变不产生正文变化的原规则保留。
- Application result.Validate 在通常 delta 校验及 ApplySynthesisDelta 之后拒绝窄范围可证明遗漏：已获准 incoming distinct IdentityKey、与该 FACT/CONFLICT slot 已加载旧依据 bytes 相同、slot 未记录或补充该身份时，实际应用结果必须在同一 slot 含此身份。仅拒绝，不插入操作，不追加重试；不取 MachineItems、手工全文或其核验结果作可信 slot。
- 前向迁移 `00132_synthesis_source_identity.sql`，Schema 同步函数，未改 00131。Atlas hash/validate 已通过。
- 普通定向 Go 4 包通过，日志 `/tmp/source-identity-unit.log`；覆盖实际 Messages/PromptRef、type/schema/hash、旧失败/unknown 无额外付费调用、来源遗漏/错误 slot/已补/未准入/不同 bytes/人工全文。
- 隔离 PG：131→132 旧 v6 READY proof apply 与新锚点实际执行通过（最初合跑中的另外两例夹具失败，见下文）；修正 fixture 后 exact-source River persistence 与 SQL version/schema 对照通过 16.556s，`/tmp/source-identity-pg-corrected.log`。目标/body/manual/refresh 合跑通过 106.680s，`/tmp/source-identity-body-pg.log`。全部数据库由 testdb disposable Testcontainers 创建清理。
- 最初 PG 合跑两处夹具不成立：整篇完全同文违反 content_artifact 去重约束，已改为整篇有不同上下文、原文片段逐字相同；普通 processing 不能伪装 goal JSON，已移除该无效正例，目标 v9 由真实 goal processing 流验证。未修改数据库约束或触发器。旧 v6 升级与锚点两例在该合跑已经通过，随后只重跑失败的两例。

Main live harness 应在最终输入类型/候选设置完成后使用 `app.SynthesisSourceIdentityGenerationVersion(app.SynthesisOriginalPromptVersion(input))` 与 `app.SynthesisSourceIdentitySemanticPromptVersion`。本代理未编辑 live harness。


## 完成的检查

- `GOCACHE=/tmp/zhixu-semantic-go-cache go test ./internal/organizing/application ./internal/organizing/workflow ./internal/organizing/adapter/agent ./internal/organizing/adapter/synthesispostgres -run 'Test(Synthesis|GoalPrepare|GoalFrozen|GoalGenerationAnd|BodyRefresh)' -count=1`：最终 4 包 PASS（0.599/0.559/0.708/0.499s），`/tmp/source-identity-unit-final.log`。最终版 includes exact slot gate 的预计算，未改变其判据。
- `go test -tags=integration ./internal/organizing/adapter/synthesispostgres -run 'Test(SynthesisSourceReadyRunsThroughRiver|AcceptedAnchorFusionRunsThroughRiverWithoutReplayingSourceReady|SynthesisSemanticFormatForwardMigrationKeepsLegacyProof|SynthesisSemanticFormatSQLRejectsForgedFrozenVersions)$' -count=1`：首次合跑锚点和131→132真实旧 v6 proof 通过，两处测试夹具失败如上。修复后只重跑 SourceReady 和 SQLRejects，PASS 16.556s。SourceReady 使用真实 River/Eino/ModelRun/Call/PG/proposal 链路，断言同文新文件片段记录一条精确 supplement、原 revision/hash 和发布指针不变、重放 first/second SourceReady 无新调用/重复记录。此处发布指针初始为 nil，不能当作已发布 Git 文件的补源验收。
- `go test -tags=integration ./internal/organizing/adapter/postgres -run 'TestGoalGenerationDispatchesThroughRiverAndRetriesOneCandidate$|TestSynthesisManuscriptRuntimeBodyRefresh/(admitted|ordinary|audit_duplicate)$|TestBodyRefreshGroupsPublishedAndCandidateImpacts/admitted$|TestSynthesisBodyRoundtripPublicationPropagation$' -count=1`：PASS 106.680s，`/tmp/source-identity-body-pg.log`。覆盖新 v9 目标、新 v10 已发布正文、v5/v7 正文刷新、人工改写零可信 items 与补源复核链路。
- `go vet -tags=integration` 同五个 affected application/workflow/agent/synthesispostgres/postgres 包：退出0，`/tmp/source-identity-vet.log`。
- `make atlas-migrate-hash`、`make atlas-migrate-validate`、`make atlas-migrate-lint`：退出0，lint 132 files。`atlas/schema.sql` 中本次函数与132逐字一致；00131 SHA256 仍为 `461e51c15a132d440e4b9497122d2f699e020b0ff0c21b415384279f904674ff`。定向 `git diff --check`、gofmt 已通过。

## 本轮修改文件

以下文件原先多数已有改动，本代理仅拥有本轮版本/来源遗漏改动，不代表拥有整份 diff。

- `internal/organizing/application/synthesis_model.go`
- `internal/organizing/application/synthesis_model_execution.go`
- `internal/organizing/application/synthesis_contract.go`
- `internal/organizing/application/synthesis_contract_test.go`
- `internal/organizing/workflow/synthesis_contract.go`
- `internal/organizing/workflow/synthesis_executor.go`
- `internal/organizing/workflow/synthesis_executor_test.go`
- `internal/organizing/workflow/synthesis_goal_prepare.go`
- `internal/organizing/workflow/synthesis_goal_prepare_test.go`
- `internal/organizing/workflow/synthesis_body_refresh_prepare.go`
- `internal/organizing/adapter/agent/synthesis_catalog.go`
- `internal/organizing/adapter/agent/synthesis_model.go`
- `internal/organizing/adapter/agent/synthesis_model_test.go`
- `internal/organizing/adapter/synthesispostgres/model_steps.go`
- `internal/organizing/adapter/synthesispostgres/runtime_integration_test.go`
- `internal/organizing/adapter/postgres/synthesis_goal_generation_integration_test.go`
- `internal/organizing/adapter/postgres/synthesis_body_roundtrip_integration_test.go`
- `internal/organizing/adapter/postgres/synthesis_body_refresh_integration_test.go`
- `internal/organizing/adapter/postgres/synthesis_manuscript_runtime_integration_test.go`
- `atlas/migrations/00132_synthesis_source_identity.sql`
- `atlas/migrations/atlas.sum`
- `atlas/schema.sql`
- 本报告。

## 自检与剩余范围

按 trellis-before-dev/go-review 检查现有 owner、版本序列化、hash/replay、新旧 stage/Schema、SQL 空值及配对、失败不自动重试、只读来源与 delta 应用边界。未发现待修确定问题。没有生成协议或共享 StructuredRunner 变更，没有放宽 decoder/预算/语义阈值。

真实外部模型质量、正式服务恢复和精确 desired10 激活由 main 继续；本代理未做外部调用，也未读取密钥。旧 v1–v5 回放身份由原 hash 与 targeted 模型 tests 覆盖，旧 v6 真实账本由131→132覆盖；没有为每个旧输入类型重复建立升级数据库。Schema仅本次函数逐字对齐+正式迁移验证，未做全量pg_dump空库恢复。
