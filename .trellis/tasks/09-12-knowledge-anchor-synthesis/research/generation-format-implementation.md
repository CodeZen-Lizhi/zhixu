# 生成 wire 格式修复

新增不可变 GENERATE v13–v18：普通、anchor、goal、body、Fusion、Fusion body，分别继承原 v7/v8/v9/v10/v11/v12 指令。可信 system 消息加入完整精确 JSON Schema 和明确 ADD_FACT.statement 嵌套格式，禁止 fact 别名和放平。Schema v1/v2 原定义及 decoder 不变；semantic 仍为普通 v7/Fusion v8，refresh 保留 v5/v7。

LatestSynthesisPromptVersions 仅在准备新冻结输入时选择新版本；旧冻结输入继续使用原版本。catalog、启动快照校验、Go model proof 和正式前向迁移134同步，133不修改（SHA256 `f35c3c83ce36469ede47c5466ec8e88551f6f71fb7053587a32d1464f89dbbc9` 与交接一致）。原绑定、hash envelope、owner和workflow通过既有 helper 取得精确版本，无需另改算法。未更改共享 runner、预算、重试、思考强度、模型服务/配置、live harness 或用户资料；保留先前 Fusion/capacity 改动，未提交。

## 验证

- 四个定向 Go 包通过：agent/application/workflow/synthesispostgres，`/tmp/generation-format-unit.log`。六个新版本均经真实 StructuredRunner 首答错误 fact→repair 合法 statement，逐次断言完整 trusted Schema；历史 v11/v12 生成及 semantic READY 重放无新增调用。冻结输入测试拒绝不匹配输入种类的 v13–v18 组合。
- 旧 catalog 临时基线对比通过，`/tmp/generation-format-history.log`。以实施前备份 `/tmp/generation-format-baseline/synthesis_catalog_2.go` 注册旧 catalog，与新 catalog 逐一比较所有旧 GENERATE/VALIDATE PromptDefinition 和 Schema 字节：完全一致。因此旧消息模板及相同输入请求的身份不变。临时测试已移至 `/tmp/generation-format-baseline-comparison.go`，不在源码保留重复 catalog。
- 五包 `go vet -tags=integration` 退出0：`/tmp/generation-format-vet.log`；定向 gofmt/diff --check 通过。
- 真实隔离 PostgreSQL/River goal 生成与 body 发布传播通过（38.620s），`/tmp/generation-format-owner-pg.log`。首次在134校验和生成前启动，fixture迁移初始化失败；校验和就绪后重跑通过。
- 独立 reviewer 核对精确版本路由、注册、Schema、冻结、Fusion 约束和 proof 白名单，无待修确定问题。
- runtime 隔离 PostgreSQL proof 四项通过（36.173s）：`TestSynthesisSourceReadyRunsThroughRiver`、`TestAcceptedAnchorFusionRunsThroughRiverWithoutReplayingSourceReady`、`TestSynthesisSemanticFormatForwardMigrationKeepsLegacyProof`、`TestSynthesisSemanticFormatSQLRejectsForgedFrozenVersions`。覆盖新普通v13/Fusion v17、132→134旧READY及SQL proof兼容、普通v13/v14/v16合法组合与伪造v13–v18组合拒绝；goal v15由上面的真实owner流程覆盖。
- `make atlas-migrate-hash-check`、`atlas-migrate-validate`、`atlas-migrate-lint` 全部通过（134 files）。134与schema函数逐字一致；134 SHA256 `3f8426a18881ede227dfa1c30d1e8514939b85c0c43c9ba29bf12bc65e9f4cbb`。

## 修改范围

- `application/synthesis_model_execution.go`：六个版本常量。
- `application/synthesis_model.go`：精确类型映射、兼容旧 pair、新冻结 helper。
- `adapter/agent/synthesis_catalog.go`：新可信完整 Schema/wire 提示及 body schema 映射。
- `adapter/agent/synthesis_model.go`：新版本启动 catalog 验证。
- `adapter/synthesispostgres/model_steps.go`：新版本 Go 证明与精确 schema 配对。
- 定向 agent/workflow/runtime/owner 测试与 owner fixture 最新迁移134；`atlas/migrations/00134_synthesis_generation_format.sql`、`atlas/migrations/atlas.sum`、`atlas/schema.sql`。

旧 v11/v12 的模型重放已实测；旧 SQL 分支保留并审查，升级实库选用原 v7 READY，并未另造每种历史类型的数据库升级矩阵。

真实外部模型的生成质量由主会话验收；本次未调用外部模型，不把确定性 Provider 的格式恢复测试等同于真实模型质量保证。
