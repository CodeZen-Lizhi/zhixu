# API 与 WorkspaceAnalysis 既有测试收口

日期：2026-09-08。实施 owner：`gorm_conversation`，承接 Conversation 子任务完成后的 Final 追加范围。

## 修改范围

- API 构造测试共 12 个既有文件：`main_test.go`、`workspace_analysis_test.go`、`artifact_composition_test.go`、`authoring_composition_test.go`、`capture_composition_test.go`、`document_history_composition_test.go`、`export_composition_test.go`、`git_sync_composition_test.go`、`m8_interview_memory_composition_test.go`、`model_runtime_embedding_test.go`、`organizing_composition_test.go`、`review_composition_test.go`。
- API 实库测试共 6 个既有文件：`artifact_integration_test.go`、`graph_integration_test.go`、`health_smart_collection_composition_integration_test.go`、`review_learning_path_integration_test.go`、`m8_interview_artifact_integration_test.go`、`timeline_impact_integration_test.go`。
- 追加两个既有 migration 文件：`workspace_analysis_timeline_integration_test.go`、`workspace_analysis_deadline_terminalization_integration_test.go`。

这 20 个文件保留原有 62 个 API 和 12 个 migration 顶层测试函数。未新增测试文件；业务断言、严格 JSON 解码、故障恢复和跨 Workspace 检查继续保留。生产 `main.go` / `workspace_analysis.go` 由主会话负责，`model_runtime_gate.go` / 对应测试与启动关闭接线由 Workflow owner 负责。

## 接线及修复

- 构造用例通过既有 `main_test.go` 内 `apiConstructorPool` 初始化平台 Pool，使用不可达 loopback DSN、`MaxConns=2`、`MinConns=0`。平台禁用自动 Ping，构造测试不需要数据库连接。Workspace Port、Capture Source Writer、Document History Repository、Workflow hooks 和 Events 均指向 GORM/scoped 接口。
- 六个 API 实库 fixture 改用 `testdb.Require(...).Pool()`，缺省走 Testcontainers；`ZHIXU_TEST_DATABASE_URL` 仍按隔离 admin DSN 处理，availability 保持 fail。业务 Repository、GORM 与 UnitOfWork 共用该平台 Pool；raw pgx 仅用于种子数据和独立 SQL 断言。
- 实测修正旧 fixture 漂移：四处 Workspace 种子的 `test` 状态改为现有 `00067` 合法的 `inactive`；Graph 的隔离 Workspace 不再直接 DELETE，由 testdb 清理整个隔离数据库。原 Graph fixture 自有的受限清理仍保留。
- Artifact Workflow 的 GET / cancel 请求带上现行 `X-Workspace-ID`；响应 fixture 显式补齐现有 `document_sources` 和 `human_task` DTO，没有放宽 strictjson 解码。
- Timeline 下游 Proposal 的严格响应类型补齐现行 `version` 和 `revision_capability`，同时检查初始版本为 1、下游 Proposal 不支持修订；保留审批为空、幂等重放、匿名 / 只读拒绝和跨 Workspace 隔离断言。首次成功响应因旧 DTO 缺少 `version` 解码失败，按真实 Handler 类型修复后定向复验通过。
- 内部 migration 测试复用主会话在 `migration_provider_test.go` 提供的 `openMigrationRuntimePool`：完成指定 Atlas 阶段后关闭迁移 Pool，再打开共享运行时 Pool；后续种子、断言和协作者使用该 Pool。
- Deadline 的三处 Finalizer 场景升级到当前运行时 Schema；纯 migration 场景继续保留明确的 `00085` / `00086` 边界。原因是当前 GORM 行映射包含 `00087` 新增的 `runtime_terminal_at`，旧 Finalizer fixture 仅升级至 `00086`。
- 原 Commit 应答丢失注入迁为可复用 `armMigrationCommitResponseLoss(t, runtime)`，仍放在既有 Deadline 测试文件。它使用官方 `stdlib.GetPoolConnector` 和 `sql.OpenDB` 借用同一物理 Pool，设置零 idle，嵌入 `*stdlib.Conn` 保留驱动能力，首次真实成功提交后返回注入错误；UnitOfWork 仍得到真实 `*sql.Tx`。cleanup 恢复 GORM root/statement、关闭 SQL facade，并确认注入确实发生。Agent/Tools receipt 用例也复用该 helper。

## 已执行检查

构造与静态检查：

```sh
go test -timeout=60s ./cmd/api
go vet ./cmd/api
go vet -tags=integration ./cmd/api
gofmt -l cmd/api/*_test.go internal/platform/migration/workspace_analysis_timeline_integration_test.go internal/platform/migration/workspace_analysis_deadline_terminalization_integration_test.go
git diff --check -- cmd/api internal/platform/migration/workspace_analysis_timeline_integration_test.go internal/platform/migration/workspace_analysis_deadline_terminalization_integration_test.go
```

以上已通过。最终生命周期接口接线完成后，API unit 再次 PASS，2.200s；最终响应 DTO 修正后 integration vet 再次 PASS。所有 20 个拥有文件的 gofmt / diff 检查通过。

API 实库使用 OrbStack、Testcontainers `pgvector/pgvector:pg16` 和真实 Atlas / River 迁移，没有 skip。全部测试命令显式 `-timeout=60s`，分组执行：

| 场景 | 结果 |
| --- | --- |
| Artifact 完整 HTTP 发布链路与生成取消 | PASS，25.747s |
| Health Smart Collection 创建、stale binding、取消 | PASS，个案 6.08s；所在早期三项批次因当时 Artifact fixture 漂移失败，后者已修复复验 |
| Graph HTTP、Interview verified drafts / rollback、Review learning path | PASS，19.024s |
| Timeline Public、API Token Audit | PASS，16.147s |
| Timeline Session Audit，含 missing_origin / missing_csrf / untrusted_origin | PASS，个案 11.12s；所在两项批次因当时 Downstream Proposal DTO 漂移失败，后者已修复复验 |
| Timeline Downstream Proposal | PASS，个案 9.35s、命令 11.394s；真实创建、审批为空、幂等重放、详情、权限与跨 Workspace 隔离断言通过 |

本 owner 的 10 个 API 实库顶层用例均已真实通过。早期失败批次保留原因和单项结果，未将失败批次记作整体成功，也未为复验已通过用例重复执行矩阵。

```sh
GIN_MODE=release go test -timeout=60s -tags=integration ./cmd/api -run '^(TestArtifactPublicHTTPPostgreSQLIntegration|TestArtifactGenerationCompositionCancellationPostgreSQLIntegration)$' -count=1 -v
go test -timeout=60s -tags=integration ./cmd/api -run '^(TestArtifactPublicHTTPPostgreSQLIntegration|TestArtifactGenerationCompositionCancellationPostgreSQLIntegration|TestAPIHealthSmartCollectionCompositionStartsAndRejectsStaleBinding)$' -count=1 -v
GIN_MODE=release go test -timeout=60s -tags=integration ./cmd/api -run '^(TestGraphPublicHTTPIntegration|TestReviewLearningPathProductionCompositionCreatesDraftPostgreSQL|TestInterviewProductionServiceCreatesVerifiedDraftsPostgreSQL)$' -count=1 -v
GIN_MODE=release go test -timeout=60s -tags=integration ./cmd/api -run '^(TestTimelineImpactPublicHTTPIntegration|TestTimelineImpactAuthenticatedAPITokenAuditIntegration)$' -count=1 -v
GIN_MODE=release go test -timeout=60s -tags=integration ./cmd/api -run '^(TestTimelineImpactDownstreamProposalAPICompositionIntegration|TestTimelineImpactAuthenticatedSessionAuditIntegration)$' -count=1 -v
GIN_MODE=release go test -timeout=60s -tags=integration ./cmd/api -run '^TestTimelineImpactDownstreamProposalAPICompositionIntegration$' -count=1 -v
```

内部 migration 由主会话本轮临时 PostgreSQL 的 admin 环境提供，每个 fixture 自建 / 清理隔离数据库；DSN 不写入仓库或输出。两个命令串行，避免 `00080` 集群级 `ALTER ROLE` 在不同数据库并发迁移时冲突：

```sh
go test -timeout=60s -tags=integration ./internal/platform/migration -run '^TestWorkspaceAnalysisTimelineProjectsAuthoritativeSafeSnapshot$' -count=1 -v
go test -timeout=60s -tags=integration ./internal/platform/migration -run '^TestWorkspaceAnalysisPreoperationDeadlineFinalizerInitialGitCommitsBundleAndReplays$' -count=1 -v
```

- Timeline PASS，个案 13.78s、命令 14.072s：权威 publication、13 项稳定顺序、预算与安全摘要、事件水位、敏感字段排除及跨 Workspace 不可见。补齐 Conversation earlier handoff 的 Timeline 实库盲区。
- Deadline PASS，个案 14.88s、命令 18.199s：真实 Commit 应答丢失后恢复、proof / event 各一份、Draft ABORTED、Answer / Run failed、重复调用 exact replay、ID 仅分配一次。
- Deadline 首次运行因旧 fixture 缺少 `runtime_terminal_at` 返回 42703，尚未提交；修复前置 Schema 后完整原断言及注入检查通过。
- Agent/Tools owner 另行报告其 `TestWorkspaceAnalysisReceiptRepositoryAtomicCompletionReplayAndCommitRecovery` 已在专用 PostgreSQL 以同一 helper 通过；完整结果由其验收记录负责。

## Review 与交付边界

按 `go-review` 和 SQL review 进行人工 diff / 调用边界自检，检查 nil Port、单 Pool、事务参与者、驱动能力、资源清理、版本化 Schema 前置、Workspace 隔离、严格响应与断言保留。已修复实测 fixture 漂移，没有把编译失败或 compile-only 写成实库通过。

本范围未更改生产 Schema、迁移 SQL、公共 HTTP 契约或业务状态机。内部 migration 的其余 10 个顶层用例本 owner 未全量执行；聚合编译 / vet 和其他 owner 的实库证据由主会话汇总。主会话随后确认 Worker 已执行 `TestPublicConversationRunsThroughRiverWorkspaceAnalysis`（11.341s），通过真实 GORM `FinalizeSuccess` 完成六节点与正文发布；参见 `worker-tests-final.md`，不再沿用早期“无成功链路覆盖”的盘点结论。未 commit / push / 部署。
