# Fusion 原证据资格修复

已新增 `SynthesisFusionExistingSemanticPromptVersion = v9`。新 Fusion prepare 选择 GENERATE v17/v18 + semantic v9；v9 仅接受 anchored/body 原类型、唯一目标及完整 admission tuple。旧 v11/v12/v17/v18 + semantic v8 保持合法。普通 semantic v7、refresh 和全部 GENERATE 文本/Schema 不变。未修改预算、重试、模型强度、正式服务、配置或用户资料，未提交。

v9 semantic payload 新增 `existing_sources`，按当前目标 `Revision.Items` 的实际 source refs 和有效 slot supplements 投影，有序去重，再以完整 ref 与本次 loaded Sources 精确相交。实现复用现有 Provider note 投影，其输入边界已验证 supplement 身份与 slot；不会从 MachineItems、其他候选、未加载历史或 incoming=false 推断资格。空集合明确序列化为 `[]`；旧 v8 不输出该字段，新旧 generation 均不输出。

可信 system 和阶段指令解释 existing_sources 与 scope.allowed_sources 的引用资格并集，但不自动证明语义。每个引用仍须独立证明完整 statement/applicability；冲突双方和 CONFLICT_RELATION 仍独立核验，目标范围不变。catalog、启动 snapshot、Application 输入校验、frozen 校验、fusion_target 投影及 Go/SQL proof 同步。正式前向迁移为 `00135_synthesis_fusion_existing_evidence.sql`，schema 函数与迁移一致。

## 修改文件

- `internal/organizing/application/synthesis_model_execution.go`、`synthesis_model.go`：v9 常量、精确 pair、Latest 路由和 Fusion 版本判断。
- `internal/organizing/workflow/synthesis_contract.go`：冻结目标校验识别 v9。
- `internal/organizing/adapter/agent/synthesis_binding.go`、`synthesis_semantic.go`：v9 目标和现有证据短标签投影。
- `internal/organizing/adapter/agent/synthesis_catalog.go`、`synthesis_model.go`：独立 v9 可信规则和注册验证。
- `internal/organizing/adapter/synthesispostgres/model_steps.go`：v9 runtime proof。
- 既有 `agent/synthesis_model_test.go`、`synthesispostgres/runtime_integration_test.go`：定向契约/回放/升级验证；runtime 默认隔离库升级到135。
- `atlas/migrations/00135_synthesis_fusion_existing_evidence.sql`、`atlas/migrations/atlas.sum`、`atlas/schema.sql`。

## 验证证据

- 四个受影响 Go 包通过：agent/application/workflow/synthesispostgres，日志 `/tmp/fusion-existing-unit.log`。后续新增旧v17/v18回放和v9 unknown用例，再定向运行 Fusion/历史Fusion 测试通过，`/tmp/fusion-existing-final-agent.log`。
- 实际 StructuredRunner 消息包含 `existing_sources:[S001]`、`allowed_sources:[S002]` 和可信资格规则；生成消息无新字段。确定性 Provider 返回原来源 UNSUPPORTED 时仍被拒绝，重复读取无新增调用。业务完成未知、模型call持久化未知均 fail closed，再调用不访问 Provider。投影验证精确 tuple 差异、未加载历史、MachineItems、非匹配 slot 和仅 incoming=false 的来源不进入集合，有效 FACT supplement 进入并去重。
- 旧v11/v12/v17/v18 + semantic8 READY 均可零额外调用重放，v8实际消息和payload不含 existing_sources。新v9错generator、缺trigger、错note/anchor/scope/精确来源，在 Provider 前拒绝。
- 以实施前 `/tmp/fusion-existing-baseline/synthesis_catalog.go` 为基线逐项比较所有已注册 GENERATE v1–v5/v7–v18、VALIDATE v1–v8 的 PromptDefinition 与 Schema 字节，完全一致；`/tmp/fusion-existing-history.log`。临时对比测试已移出源码至 `/tmp/fusion-existing-baseline-comparison.go`。
- 隔离 PostgreSQL/River 三项通过（32.392秒），`/tmp/fusion-existing-pg.log`：`TestAcceptedAnchorFusionRunsThroughRiverWithoutReplayingSourceReady`（新v17/v9真实proof/apply）；`TestSynthesisFusionExistingForwardMigrationKeepsSemantic8Proof`（134真实v17/v8模型账本，READY重复读取，135升级，冻结JSON字节不变，SQL仅接受原v8，继续apply；总调用保持4次，含初始source-ready）；`TestSynthesisSemanticFormatSQLRejectsForgedFrozenVersions`（非Fusion及错版本/Schema拒绝）。
- 四包 `go vet -tags=integration` 通过，`/tmp/fusion-existing-vet.log`。gofmt、限定范围 `git diff --check` 通过。Go/SQL自检未发现待修确定问题。
- `make atlas-migrate-hash-check atlas-migrate-validate atlas-migrate-lint` 通过，135 files，`/tmp/fusion-existing-atlas.log`。135函数体与 schema 一致。
- 134 SHA256 保持 `3f8426a18881ede227dfa1c30d1e8514939b85c0c43c9ba29bf12bc65e9f4cbb`，与实施前基线相同。

初次 Go 执行受默认本机缓存写权限限制，切换 `GOCACHE=/tmp/zhixu-go-cache` 后验证通过。一次新增测试起初错误期待 semantic拒绝不返回error，已按既有 `SYNTHESIS_SEMANTIC_REJECTED` 契约修正，产品代码未为测试放宽。

本次没有调用外部模型。上述确定性语义拒绝只证明程序不会因existing标签绕过模型复核；真实五例质量与独立审查由主会话继续，不将其记为本实现验证已完成。
