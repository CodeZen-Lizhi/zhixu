# Worker 既有测试最终接线记录

日期：2026-09-08。范围为 `cmd/worker` 的既有 `*_test.go`；生产 `main.go` 和 `workspace_analysis_capability.go` 由主会话持有。本轮未新增测试文件、Schema 或临时测试代码。

## 修改范围与接口

- 原位适配 15 个集成测试文件：`tool_composition`、`tool_execution`、`rag_conversation`、`rag_compose_fixture`、`workspace_analysis_conversation`、`workspace_analysis_terminal_matrix`、`artifact_generation_success`、`capture_composition`、`export_composition`、`git_sync_composition`、`health_affected_composition`、`health_smart_collection_composition`、`semantic_scan_composition`、`startup_queue`、`timeline_projection_process`。
- `newMigratedWorkerTestPool` 改为 `testdb.Require(...).Pool()`，使用一个平台 Pool 和 Atlas 的完整迁移。Timeline 子进程 fixture 同样由 `testdb.Require` 管理，DSN 仅通过 `DatabaseURL()` 传给测试子进程。
- 普通隔离数据库场景未配置 `ZHIXU_TEST_DATABASE_URL` 时使用 Testcontainers，不再直接 skip。外部 Compose Worker 和外部 RAG fixture 保留既有 opt-in 条件及精确目标库，改为 `platformpostgres.Open` 初始化共享 GORM 视图，未对外部库进行建库或迁移。
- 业务 Repository 统一使用最终 GORM 构造。原 SQL 种子、持久化断言和 River Worker driver 从同一平台 Pool 的 `DB()` 获取底层 pgx 视图。
- Workflow Runtime 改为 `NewGORMRuntimeRepositoryWithHooks`，测试明确选择静态 scoped enqueue 策略。事务插入由 Runtime 的平台 UoW 和官方 River SQL driver 完成。
- RAG 接入 GORM Events、Conversation、Agent、Memory、Knowledge、Workspace、Retrieval、Tools、Progress、DraftStream、AnswerFinalizer 和 QuestionDispatcher。
- Workspace Analysis 使用完整 `workerToolRepository` 聚合、真实 scoped Workflow fence、Agent 模型操作 Repository、Events/Audit；RunStarter 及模拟过期的 wrapper 改为 `ScopedWorkspaceAnalysisRunStarter`。候选最终化前取消 wrapper 仍调用真实 HTTP Cancel 后委托 GORM 模型操作持久化。
- Artifact 复用最终生产 Generation/Terminal 构造。Capture 注入 Workspace scoped writer；Git operation lock 和 River startup 继续使用批准的 pgx driver 边界。
- 普通非 integration 测试未发现零值 pgxpool fixture，因此未新增未使用的 lazy Pool helper。

## 实库发现与处理

保留所有原业务断言，未通过修改预期、跳过用例或降低产品权限绕过失败。

1. Tool/Chat fixture 断言 11 个 Tool 和六节点 Workspace Analysis 已注册，但没有显式打开默认关闭的能力开关。补齐 `WorkspaceAnalysisWorkerEnabled=true` 和同池真实 Audit 输入，生产 default-off 保持不变。
2. Artifact fixture 原来向启用的模型执行器传 nil scheduler，触发 `AGENT_STRUCTURED_RUNNER_MISSING`。改用项目既定的 Eino StructuredPhaseScheduler。
3. Artifact fixture 的 Workflow GET 缺少现有 HTTP 契约要求的 `X-Workspace-ID`。在其 GET helper 中携带既有 fixture Workspace ID，保留 status URL 和响应绑定断言。
4. Health 的两条种子使用 `status='test'`，被 Atlas `00067_workspace_root_grant.sql` 的 `workspace_status_lifecycle` 拒绝。改用 `active`，同样修复 Timeline fixture 中唯一同类种子；未修改约束或迁移。
5. 首轮编译报告的其他 owner legacy 符号及 Worker `NewMigrator(database)` 问题已交主会话处理；未越权修改其生产文件。

## 已执行门禁

普通测试和编译：

```bash
go test -mod=vendor ./cmd/worker -count=1 -timeout=60s
go test -mod=vendor -tags=integration,testcontainers ./cmd/worker -run '^$' -count=1 -timeout=60s
go vet -mod=vendor ./cmd/worker
go vet -mod=vendor -tags=integration,testcontainers ./cmd/worker
git diff --check -- 'cmd/worker/*_test.go'
```

全部 PASS；普通测试为 0.731 秒，全部 integration fixture 编译为 0.936 秒。实际数据库检查均显式设置 60 秒超时，并分组运行：

```bash
go test -mod=vendor -tags=integration,testcontainers ./cmd/worker -run '^TestWorker(ToolCompositionSeparatesContractsExecutorsAndTrustedAudit|ChatCompositionUsesEinoSchedulersAndRegistersRelationAndRAGTogether)$' -count=1 -timeout=60s
go test -mod=vendor -tags=integration,testcontainers ./cmd/worker -run '^TestPersistedWorkflowRiverToolRequestExecutesRefusesAndReplays$' -count=1 -timeout=60s
go test -mod=vendor -tags=integration,testcontainers ./cmd/worker -run '^TestPublicConversationRunsThroughRiverRAGAndFeedback$' -count=1 -timeout=60s
go test -mod=vendor -tags=integration,testcontainers ./cmd/worker -run '^TestPublicConversationRunsThroughRiverWorkspaceAnalysis$' -count=1 -timeout=60s
go test -mod=vendor -tags=integration,testcontainers ./cmd/worker -run '^TestPublicConversationWorkspaceAnalysisReceiptLossLeaseReclaimReusesGitReceipt$' -count=1 -timeout=60s
go test -mod=vendor -tags=integration,testcontainers ./cmd/worker -run '^TestArtifactGenerationSucceedsThroughPublicHTTPAndRiverWorker$' -count=1 -timeout=60s
go test -mod=vendor -tags=integration,testcontainers ./cmd/worker -run '^TestPublicConversationWorkspaceAnalysisReachableTerminalMatrixThroughRiver$/(cancelled|cancelled_after_synthesis_provider_before_candidate_finalization)$' -count=1 -timeout=60s
go test -mod=vendor -tags=integration,testcontainers ./cmd/worker -run '^TestPublicConversationWorkspaceAnalysisReachableTerminalMatrixThroughRiver$/(deadline_exceeded|runtime_failed)$' -count=1 -timeout=60s
go test -mod=vendor -tags=integration,testcontainers ./cmd/worker -run '^TestWorker(CaptureCompositionRegistersExecutorDefinitionAndOutbox|HealthAffectedChangeCompositionConsumesTypedOutboxExactlyOnce|HealthSmartCollectionCompositionExecutesDetectorAndFailsClosedOnDrift)$' -count=1 -timeout=60s
go test -mod=vendor -tags=integration,testcontainers ./cmd/worker -run '^TestStartWorkerRuntime(FreshQueueStartsBeforeResume|NonTerminalRolloutRejectsMissingPausedQueue)$' -count=1 -timeout=60s
```

全部最终 PASS，对应耗时依次为 21.099、14.745、14.932、11.341、30.052、10.649、20.171、20.017、22.479、17.562 秒。Tool/Chat、Artifact、Health 的初始失败及根因见上文，未将失败批次当成通过。

## Go / SQL Review

按 `go-review` 和 `sql-code-review` 对本轮 diff、直接构造、事务参与者和 fixture 生命周期进行了人工静态审查，并结合上述实库结果复核：所有业务写入持有同池 UoW/scope，真实 Events/Audit 参与事务；静态 enqueue 策略显式选择；model/Tool replay 与取消故障 seam 保留；Worker 停止先于 fixture 关闭；测试 SQL 参数和 Workspace 绑定保持原合同。移除手工建库/迁移/清理后，隔离与清理由 Testcontainers 工厂拥有。未发现尚未修复的本轮范围缺陷。

## 验证边界

没有运行全仓测试或完整 Worker integration/race 矩阵。Timeline 独立进程测试包含 `go build -race`、启动/重启和原 4 分钟测试上下文，本轮仅完成 fixture 适配及编译；其他未选择的矩阵分支及独立 GitSync/Export/SemanticScan 测试未逐一执行，其生产构造已由完整 Tool/Chat Worker composition 覆盖。外部 Compose 两条 opt-in 场景未运行，也未执行浏览器 smoke、部署、commit/push 或实际发布回滚。
