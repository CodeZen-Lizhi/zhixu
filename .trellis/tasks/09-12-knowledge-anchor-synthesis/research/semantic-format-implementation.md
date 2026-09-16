# Semantic format v6 实施记录

2026-09-16。实现与限定验证完成；真实外部模型质量验收由 main 继续。保留工作区已有 dirty work，无提交、push、正式服务/配置/数据库操作，无密钥读取或外部模型调用。

## 行为与兼容边界

- `SynthesisFrozenInput` / `SynthesisGenerationInput` 增加 `SemanticPromptVersion`，JSON `semantic_prompt_version,omitempty`。空值保持历史序列化顺序与字节；仅允许空或 `v6`。普通/锚点、目标、正文刷新新 prepare 冻结 v6；已有 input 直接重放，不补写。
- 冻结 hash、openInput 转换、Validate、EqualSynthesisFrozenGeneration 精确绑定版本。GENERATE 的 v1–v5 选择、Schema 与旧提示原文保持；v6 只注册 VALIDATE，仍用 semantic Schema v1。
- v6 系统消息提供完整可信 Schema、精确 `checks/index/verdict/sources/source` 字段、无来源 `sources:[]`、禁止 kind/source_verdicts、无预选 verdict 的占位格式模板。保留 legacyReview 的证据规则，并以条件语句组合目标、锚点、已发布正文与局部刷新约束。公共 StructuredRunner、预算、强度、三阶段有限修复均未改变。
- 旧请求 hash envelope 原样保留为 `synthesisLegacyModelRequestHash`；新 VALIDATE 对原 hash 再绑定冻结版本。新 GENERATE 因 frozen input hash 自然绑定版本，但保持原生成提示。旧 READY/FAILED/unknown 不借升级重新调用。
- Go 模型 runtime owner 与 apply proof 按冻结输入为 VALIDATE 选择 v6。SQL 由唯一前向 `00131_synthesis_semantic_format.sql` 同步精确规则；v6 GENERATE、错误版本、null/数字/对象均不通过。历史迁移/执行/ModelRun 不修改。
- 确认原最新迁移为 00130；atlas/schema.sql 的函数体与 00131 逐字一致，atlas.sum 已生成。新运行时测试夹具升至 131；专门旧执行测试先在 130 创建旧输入与真实账本，再升级。
- live 五场景构造器 `synthesisLiveInput` 已显式选择 v6；历史模型测试继续使用空版本。

## 精确文件清单

以下为本轮修改文件，部分文件在本轮之前已有改动；清单不意味着本轮拥有整份 git diff。

- `internal/organizing/application/synthesis_model.go`
- `internal/organizing/application/synthesis_model_execution.go`
- `internal/organizing/application/synthesis_contract.go`
- `internal/organizing/workflow/synthesis_contract.go`
- `internal/organizing/workflow/synthesis_executor.go`
- `internal/organizing/workflow/synthesis_executor_test.go`
- `internal/organizing/workflow/synthesis_goal_prepare.go`
- `internal/organizing/workflow/synthesis_goal_prepare_test.go`
- `internal/organizing/workflow/synthesis_body_refresh_prepare.go`
- `internal/organizing/adapter/agent/synthesis_catalog.go`
- `internal/organizing/adapter/agent/synthesis_model.go`
- `internal/organizing/adapter/agent/synthesis_model_test.go`
- `internal/organizing/adapter/agent/synthesis_live_test.go`
- `internal/organizing/adapter/synthesispostgres/model_steps.go`
- `internal/organizing/adapter/synthesispostgres/model_steps_test.go`
- `internal/organizing/adapter/synthesispostgres/runtime_integration_test.go`
- `internal/organizing/adapter/postgres/synthesis_goal_generation_integration_test.go`
- `internal/organizing/adapter/postgres/synthesis_body_roundtrip_integration_test.go`
- `internal/organizing/adapter/postgres/synthesis_body_refresh_integration_test.go`
- `internal/organizing/adapter/postgres/synthesis_manuscript_runtime_integration_test.go`
- `atlas/schema.sql`
- `atlas/migrations/atlas.sum`
- `atlas/migrations/00131_synthesis_semantic_format.sql`
- `.trellis/tasks/09-12-knowledge-anchor-synthesis/research/semantic-format-implementation.md` — 本记录。

## 命令与结果

所有 Go 命令使用 `GOCACHE=/tmp/zhixu-semantic-go-cache`（首次默认缓存目录被 sandbox 拒绝，切换后成功）。没有全仓测试/构建。数据库均由 testdb disposable Testcontainers 创建和清理，确定性 Provider 经过真实 Eino/ModelRun/Call/持久 proof。

```sh
go test ./internal/organizing/application ./internal/organizing/workflow ./internal/organizing/adapter/agent ./internal/organizing/adapter/synthesispostgres -run 'Test(Synthesis|GoalPrepare|GoalFrozen|GoalGenerationAnd|BodyRefresh)' -count=1
```

PASS，四包 0.491/0.525/0.702/0.499s；`/tmp/semantic-format-unit-final.log`。覆盖实际 Messages/PromptRef/Schema、v1–v5 hash 分支、v6 版本隔离、旧 READY、失败/unknown 不新增调用、冻结 hash/转换/equality 与伪造版本拒绝。live opt-in 未启用。

```sh
go test -tags=integration ./internal/organizing/adapter/synthesispostgres -run 'Test(SynthesisSourceReadyRunsThroughRiver|AcceptedAnchorFusionRunsThroughRiverWithoutReplayingSourceReady|SynthesisUnknownModelCommitBlocksPaidReplay|SynthesisFailedModelDoesNotRepeatAfterLostWorkflowFailure)$' -count=1
```

PASS 36.752s；`/tmp/semantic-format-pg.log`。普通/锚点实际新 prepare→模型账本→proof→apply，精确原始响应和调用次数、失败/unknown 拒绝付费重试。

```sh
go test -tags=integration ./internal/organizing/adapter/postgres -run 'TestGoalGenerationDispatchesThroughRiverAndRetriesOneCandidate$|TestSynthesisManuscriptRuntimeBodyRefresh/(admitted|ordinary)$' -count=1
go test -tags=integration ./internal/organizing/adapter/postgres -run 'TestBodyRefreshGroupsPublishedAndCandidateImpacts/admitted$|TestSynthesisBodyRoundtripPublicationPropagation$' -count=1
```

PASS 43.878s / 39.296s；`/tmp/semantic-format-prepare-pg.log`、`/tmp/semantic-format-body-pg.log`。目标初版、普通人工正文、正文刷新与多轮发布传播真实持久链路成功。

```sh
go test -tags=integration ./internal/organizing/adapter/synthesispostgres -run '^TestSynthesisSemanticFormatForwardMigrationKeepsLegacyProof$' -count=1
go test -tags=integration ./internal/organizing/adapter/synthesispostgres -run '^TestSynthesisSemanticFormatSQLRejectsForgedFrozenVersions$' -count=1
```

PASS 9.582s / 8.954s；`/tmp/semantic-format-forward-pg.log`、`/tmp/semantic-format-forged-pg.log`。真实 130→131 升级前后冻结 JSON 不变，旧 v1 两条 READY 模型证明升级后可 apply，Provider 保持 2 次；SQL 实测空/v6 正例与 v5/v7/null/数字/对象负例，GENERATE/VALIDATE 新旧交叉拒绝。伪造输入试验仅对未冻结行事务写入后 rollback，未禁用触发器、未改已有不可变事实。

```sh
go vet -tags=integration ./internal/organizing/application ./internal/organizing/workflow ./internal/organizing/adapter/agent ./internal/organizing/adapter/synthesispostgres ./internal/organizing/adapter/postgres
make atlas-migrate-hash
make atlas-migrate-validate
```

均退出 0；vet 日志 `/tmp/semantic-format-vet.log`。23 个目标文件定向 `git diff --check` 与 Go 文件 `gofmt -l` 通过。00131 checksum：`h1:dYDCH1QMvy82d3u3Nqz9KcMVv/Maxvv+dv7vzHSt/bU=`。

## 自检与剩余限制

按 trellis-before-dev / go-review 读取规范并自检版本来源、Go/SQL 空值与白名单、模型身份、冻结转换、重放和失败路径；本轮未发现待修确定问题。没有独立 check worker。

本次证明格式契约/兼容性和确定性 Provider 的实际持久链路，不证明 DeepSeek 生产质量。main 仍需按生产预算执行五场景、检查真实产物并完成最终 review/spec/acceptance/journal。没有迁移正式数据库、重启服务或 rebind。Schema 仅同步本次函数并验证其与迁移一致；未另做全量 pg_dump/空库恢复。历史 v1 实库升级已验，v2–v5 保持旧编码/提示与定向模型回归，未为每个历史版本分别建立升级实库。

## 主会话补充的现场事实

正式 desired10 的 `chat_timeout_microseconds=30000000`，即 30 秒；main 后续真实验收将显式使用 30s，对齐实际配置。本实现不修改生产 timeout 或思考强度。上述值来自主会话现场反馈，本子会话未读取 live settings。

单次新格式实验耗时 24.05s、输出 4456 token；既有两例全文补源分别耗时 5.22s、27.19s。这些单例不能证明其他融合场景的语义质量或 30s 内稳定完成。v6 仅修复已确认的字段表达缺口，真实五场景仍需分别验收并保留具体失败分类。

## 实施交接

代码已稳定，可以开始独立审查和由 main 执行的真实合成调用。本实施 agent 不再主动修改上述 owned 文件，等待具体审查反馈。Owned 范围为上列 23 个代码/测试/Schema 文件中的本轮改动及本报告；保留其他已有改动。已执行的定向命令、结果与日志均列于上节，未将未执行的真实模型质量或全量 Schema 恢复记为通过。真实调用按主会话指定 30s 与原生产预算执行；服务恢复仍不在本 agent 操作范围。
