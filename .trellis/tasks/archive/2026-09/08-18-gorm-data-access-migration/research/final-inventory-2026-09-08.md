# Research: TODO 10 Final 入口、pgx 边界与 legacy 清理盘点

- Query: 为 TODO 10 Final 提供真实入口构造、scoped 事务参与者、pgx/pgconn owner、legacy 保留边界和短时验证映射。
- Scope: internal；阅读仓库源码、任务工件、已归档 child final-handoff 和 vendor 官方源码，不执行实现、测试或 Git 操作。
- Date: 2026-09-08
- Active task: `.trellis/tasks/08-18-gorm-data-access-migration`。
- Final owner: `.trellis/tasks/08-19-gorm-composition-pgx-convergence`。
- 快照说明：主会话与七个模块 owner 正在并行修改代码；下列行号、构造和 import 数量是研究过程中读到的事实，不能充当最终完成证明。特别是 Conversation/Organizing 构造在研究期间陆续出现。

## Findings

### 1. 需要先处理的四个事实

1. 普通业务构造参数并不统一：Auth/Document History/Ingestion 接受共享 `*gorm.DB`；Memory 接受 GORM root + UoW；Git Sync 再加 CredentialSealer；其余大多数接受完整平台 Pool。不能把所有 `NewRepository(pool.DB())` 机械替换为同一种参数。
2. Health、Collection、Export 的 GORM 路径仍复用旧名字下的 SQL core。Health 还实现 pgx-shaped Tx bridge；删除所有无 `gorm_` 前缀文件会破坏正在使用的 GORM 路径。
3. 仅查 `pgxpool.Pool` / `pgx.Tx` 会漏掉大量 `PgError` / `ErrNoRows` import。已有 `internal/platform/postgres/errors.go:11` 的 `SQLState` 和 `:20` 的 `ConstraintName` 可供普通 Adapter 使用。
4. 未发现已有全仓 pgx/GORM/AutoMigrate 执行门禁。现有架构基线只是报告；多个 cmd integration 测试仍依赖环境变量并可能 `t.Skip`，仅增加 testcontainers build tag 不会使它们自动运行。

### 2. Files found

| 文件/目录 | 职责 |
| --- | --- |
| `.trellis/tasks/08-19-gorm-composition-pgx-convergence/{prd,design,implement}.md` | Final 独占 Composition、legacy 清理、allowlist 与最终验证范围 |
| `.trellis/tasks/08-18-gorm-data-access-migration/implement.md:35` | Foundation + 28 module + Final 的依赖波次；Capture/Artifact 等跨 owner 说明 |
| `.trellis/tasks/08-18-gorm-data-access-migration/design.md:80` | pgx 批准例外：平台、River、migration、Git operation、credential bootstrap、经证明的 Retrieval |
| `internal/platform/postgres/{pool,gorm,transaction,river,errors}.go` | 唯一物理池、GORM root、UoW、SQL scope unwrap、River SQL driver、错误投影 |
| `cmd/api/main.go`, `cmd/api/workspace_analysis.go` | API 全部领域入口、Workflow factory、模型/Workspace runtime、终态 Hook |
| `cmd/worker/main.go`, `cmd/worker/workspace_analysis_capability.go` | Worker executor/outbox/River、运行时与领域参与者 |
| `internal/workspace/runtimegrant/gorm_composition.go:42` | Workspace/RootGrant/Audit/lease 的完整同池 GORM 组合 |
| `internal/modelsettings/runtime/bootstrap_gorm.go:30` | API/Worker/modelctl 共用的 GORM ModelSettings/Audit/LocalRuntime bootstrap |
| `internal/export/adapter/river/architecture_test.go:14` | 现有 AST 检查，约束只能事务入队；硬编码 staged/legacy 两构造 |
| `deploy/architecture_quality_baseline.py:385` | 带注释/字符串处理的 Go import 提取；当前只有架构报告 |
| `Makefile:9`, `.github/workflows/ci.yml:64` | `make test` 是现有 CI 聚合入口 |
| `cmd/*/*composition*test.go` 与相关 `*integration_test.go` | 现有纯构造和实库链路验证；见第 8 节 |

已读取归档 handoff：Foundation、Events、Audit、Collection、Health、Artifact、Capture、Ingestion、Document History、Memory、Learning Path、Git Sync，以及 ModelSettings 的 `research/final-handoff.md`。部分 handoff 的“待归档”“当前无 GORM 依赖”“当前未接实库”等是历史描述，不能覆盖当前源码或父任务最新规则。

### 3. 进程入口矩阵

记号：`P` = 同一个 `*platformpostgres.Pool`；`D` = `P.GORM()`；`U` = `P.UnitOfWork()`；`E` = `eventspostgres.NewGORMStore(P)`；`A` = `auditpostgres.NewGORMStore(P)`。取得 D/U 的错误必须显式处理。

| 入口 | 当前已读 legacy 构造 | GORM/保留构造与参数 |
| --- | --- | --- |
| API ModelSettings | `cmd/api/main.go:221` `modelsettingsruntime.Bootstrap(ctx,database.DB(),cfg,...)` | `BootstrapGORM(ctx,P,cfg,telemetry...)`；返回 GORM Repository，同时提供 scoped enqueue fence |
| API Workspace | `cmd/api/main.go:396` `workspaceruntimegrant.NewProcessComposition` | `NewGORMProcessComposition(ctx,P,lookup,RuntimeRoleAPI)`，返回稳定 `GORMRepositoryPort`、Control、Resolver、Grant、Lease |
| API 领域 helper | `cmd/api/main.go:829` 起大量 helper 接收裸 `*pgxpool.Pool`；`workspace_analysis.go:30/80` 同样 | 改传 P 或稳定 application ports，依后文构造表接线；具体 `*workspacepostgres.Repository` / `*workflowpostgres.RuntimeRepository` 参数也要同时调整 |
| Worker Workspace | `cmd/worker/main.go:358` `NewProcessComposition`；`:1263` 还有备用 Workspace Repository 构造 | `NewGORMProcessComposition(ctx,P,lookup,RuntimeRoleWorker)`；不能遗漏 worker component 中的备用路径 |
| Worker ModelSettings | `cmd/worker/main.go:374` `Bootstrap` | `BootstrapGORM(ctx,P,cfg,telemetry...)`；保留原 HotRuntimeController/Generation 生命周期 |
| Worker 领域 helper | `cmd/worker/main.go:1257` 起，`workspace_analysis_capability.go:27/63` | P、GORM Repository 与 scoped Hook/执行围栏/队列参与者一起替换 |
| modelctl | `cmd/modelctl/main.go:74` `modelruntime.Bootstrap(executionCtx,database.DB(),cfg)` | `BootstrapGORM(executionCtx,P,cfg)`；保持 SettingsManager 与 Recovery 边界 |
| workspacectl | `cmd/workspacectl/main.go:137/144` `NewStore(database.DB())` + `NewRepository(...,WithAuditAppender)` | `A` + `workspacepostgres.NewGORMRepository(P,WithGORMScopedAuditAppender(A))` |
| workspaceprobe | `cmd/workspaceprobe/main.go:76` `workspacepostgres.NewRepository(database.DB())` | `workspacepostgres.NewGORMRepository(P)` |
| local-model-runtime | `cmd/local-model-runtime/main.go:157` `localmodelruntime.NewPostgresStore(database.DB())` | `localmodelruntime.NewGORMStore(P)`；原 Reconciler/Manager/Lifecycle interfaces 保留 |
| migrate | `cmd/migrate/main.go:58` `platformmigration.NewAtlasRunner(database.DB(),dir)` | 保留批准的 Atlas/migration native 边界；migration Pool 不提供 GORM/UoW |
| local-model-runtime-credential-init | `main.go:82` `pgx.Connect(ctx,connection)` | 批准的管理角色 provisioning；不能用普通 runtime credential 或 GORM 业务 Pool 替代 |

`GORMProcessComposition.Repository` 是 application/domain port 聚合，不是 `*workspacepostgres.Repository`。其定义见 `internal/workspace/runtimegrant/gorm_composition.go:17`；包含 RegistryStore、ControlStore、SourceWriter、GitCapture 等接口。Final 可以传该稳定 port，避免把新具体类型再次扩散到所有 helper。

### 4. Repository 与 scoped 参与者矩阵

基础构造：

| Owner | 替代构造 | 源码锚点/额外依赖 |
| --- | --- | --- |
| Auth | `NewGORMRepository(D)` | `internal/auth/adapter/postgres/gorm_repository.go:24` |
| Document History | `NewGORMRepository(D)` | `internal/documenthistory/adapter/postgres/gorm_repository.go:22` |
| Ingestion | `NewGORMRepository(D)` | `internal/ingestion/adapter/postgres/gorm_repository.go:22` |
| Memory | `NewGORMRepository(D,U)` | `internal/memory/adapter/postgres/gorm_repository.go:29` |
| Git Sync | `NewGORMRepository(D,U,sealer)` | `internal/gitsync/adapter/postgres/gorm_repository.go:29` |
| Events / Audit | `NewGORMStore(P)` | `events/.../gorm_store.go:26`、`audit/.../gorm_store.go:25`；Audit Recorder 仍由 `auditapplication.NewRecorder(A)` 构造 |
| Workspace | `NewGORMRepository(P,options...)` | `workspace/.../gorm_core.go:56`；`WithGORMRootGrantResolver`、`WithGORMScopedAuditAppender` |
| Authoring | `NewGORMRepository(P)` | `authoring/.../gorm_repository.go:26` |
| Collection | `NewGORMRepository(P)` | `collection/.../gorm_repository.go:27`；也是 scoped durable binding verifier |
| ChangeControl | `NewGORMRepository(P,E)` | `changecontrol/.../gorm_core.go:27`；同时是 scoped cancellation、KnowledgeProposal、Reindex binding/completion owner |
| Knowledge | `NewGORMRepository(P)` | `knowledge/.../gorm_core.go:27` |
| Review Core | `NewGORMRepository(P)` | `review/.../gorm_core.go:27` |
| Interview | `NewGORMRepository(P)` | `review/interview/.../gorm_core.go:25` |
| Learning Path | `NewGORMRepository(P)` | `review/learningpath/.../gorm_repository.go:32` |
| Capture | `NewGORMRepository(P,workspaceScopedWriter)` | `capture/.../gorm_core.go:28` |
| Capture Profile | `NewGORMProfileRepository(P,agentScopedFinalizer)` | `capture/.../gorm_profile_repository.go:29` |
| Artifact | `NewGORMRepository(P)` | `artifact/.../gorm_repository.go:27` |
| Export | `NewGORMRepository(P,WithGORMEventAppender(E),WithGORMAuditAppender(A))` | `export/.../gorm_repository.go:66`；API 原有两个 appender 均保留，Worker 按原使用场景注入 |
| ModelSettings | `NewGORMRepository(P,options...)` | `modelsettings/.../gorm_core.go:91`；优先使用完整 `BootstrapGORM`，不手工漏掉 sealer/Audit/LocalRuntime |
| LocalModelRuntime | `NewGORMStore(P)` | `internal/localmodelruntime/gorm_core.go:25` |
| Workflow | `NewGORMRepository(P)` | `workflow/.../gorm_core.go:48`；Runtime 是独立构造，见下表 |
| Agent | `NewGORMRepository(P)` | `agent/.../gorm_core.go:26`；提供 scoped ModelRun Store/Finalizer |
| Tools | `NewGORMRepository(P,policySnapshot,recoveryFence)` | `tools/.../gorm_core.go:48`；两个依赖分别由 Workflow GORM policy/recovery 构造 |
| Graph | `NewGORMRepository(P)` | `graph/.../gorm_core.go:38` |
| Retrieval | `NewGORMRepository(P)`、`NewGORMSearchRepository(P)` | `retrieval/.../gorm_core.go:33`、`gorm_search.go:34` |
| Conversation | `NewGORMRepository(P,E)`、`NewGORMDraftStreamRepository(P)` | 研究期间出现：`conversation/.../gorm_core.go:21`、`gorm_draft_stream.go:21`；依 owner 最终 handoff 复核 |
| Organizing | `NewGORMRepository(P)` | 研究期间出现：`organizing/.../gorm_core.go:21`；需一起接 start/fence/generation/terminal |

关键原子链，不得只改根仓储：

| 参与者/调用点 | 可用替代与要求 |
| --- | --- |
| API `newAPIRuntimeRepositoryFactory`、Worker Runtime | `workflowpostgres.NewGORMRuntimeRepositoryWithHooks(P,riverOptions,scopedEnqueueFence,GORMRuntimeRepositoryHooks)`（`gorm_core.go:58`）；`riverOptions.EnqueueFence` 必须为空，单独参数传 ModelSettings GORM Repository |
| Workflow 终态/取消/控制 | `NewCompositeScopedCancellationSafetyGuard`、`NewCompositeScopedWorkflowTerminalHook`、`NewCompositeScopedWorkflowControlHook`（`internal/workflow/application/scoped_runtime.go:42/76/109`）；保持原 Hook 顺序 |
| Approval Dispatch | `NewGORMApprovalDispatchRepository(P,runtimeScopedStarter,ids,clock,E)`（`approvaldispatchpostgres/gorm_repository.go:38`） |
| Approved Relation Apply | `NewGORMApprovedRelationApplyRepository(P,ids,clock,E)`（`knowledge/.../gorm_relation_apply.go:41`） |
| Knowledge Impact | `knowledgeapplication.NewScopedImpactServiceWithAudit(repository,ids,clock,impactAudit)`；`ImpactRecorder.RecordImpactAnalysisScoped` 使用同一 Audit Recorder（`impact.go:106`、`adapter/audit/impact.go:77`） |
| Artifact Generation | `NewGORMSectionGenerationRepository(P,runtime,binding,agent,evidence,ids,clock,profile)`（`artifact/.../gorm_generation.go:40`）；binding 由 Workflow `NewGORMRuntimeBindingReader(P)` 提供 |
| Artifact Terminal | `NewGORMSectionGenerationTerminalHook(P,agentScopedStore,profile)`（`gorm_generation_terminal.go:29`） |
| Agent RAG Progress | `NewGORMRAGProgressStore(P,E)`（`agent/.../gorm_rag_progress.go:32`） |
| Agent WorkspaceAnalysis | Model operation 仓储用 `NewGORMWorkspaceAnalysisRepository(P,executionFence)`；Run persistence/readiness、Tools participant/refusal/authority 由基础 `NewGORMRepository(P)` 提供（gorm_workspace_analysis_runs.go:26、gorm_workspace_analysis_capability.go:126）；Run service 改 `NewScopedWorkspaceAnalysisRunService` / `NewScopedWorkspaceAnalysisCapabilityCheckedRunStarter` |
| Tools WorkspaceAnalysis | `NewGORMWorkspaceAnalysisRepository(P,executionFence,participant,refusalStore,authority,E,auditRecorder)`（`tools/.../gorm_core.go:70`），不能继续使用 legacy `NewRepositoryWithWorkspaceAnalysisEventsAndAudit` |
| Graph Confirm/Scan | `NewGORMCandidateConfirmRepository(P,scopedKnowledgeProposalPort,ids,clock)`；`NewGORMSemanticLinkScanRepository(P,runtime,collectionVerifier,ids,clock)` |
| Graph Worker Scan | `NewGORMSemanticLinkScanStateRepository(P)`、`NewGORMSemanticLinkTopicScanPageRepository(P)`、`NewGORMSmartCollectionScanPageRepository(P,reader)`、`NewGORMSemanticLinkDiscoveryCandidateWriter(gormRepository,ids,clock)` |
| Graph Cancellation | 原 `NewSemanticLinkScanCancellationGuard()` 改为同池 Graph `GORMRepository` 本身；它实现 `SafeToCancelWorkflowNodeScoped`（`gorm_scan_cancellation.go:18`），不需要新增同名 GORM Guard constructor |
| Health Scan/Issue | `NewGORMScanRepository(P,runtime,E,ids,clock,collectionScopedVerifier)`；`NewGORMScanStateRepository(P,E)`；`NewGORMIssueRepository(P,membership,ids,collectionScopedVerifier)`；构造当前是 `...any` 分派，不能传旧 `DurableBindingVerifier{}` |
| Health 其他 | `NewGORMScheduleRepository(P,ids)`、`NewGORMReadRepository(P)`、`NewGORMFactReader(P,membership)`、`NewGORMAffectedChangeDispatchRepository(scans,planner)`、`NewGORMScanCancellationGuard(P,E)` |
| Health membership | `healthcollection.NewMembership(collectionRepository)`；如需适配 `VerifyBindingScoped`，用 `NewScopedBindingVerifier(collectionScopedVerifier)`（`gorm_membership.go:21`） |
| Export River | `NewGORMTransactionalDispatcher(P,riverClient,modelsettingsScopedFence)`（`export/adapter/river/gorm_dispatcher.go:27`）；旧 `NewTransactionalDispatcher(rawPool,client)` 删除后同步 AST 测试 |
| Retrieval Delivery/Completion | `NewGORMDeliveryRepository(P,ids)`；`NewGORMCompletionRepository(P,changecontrolScopedCompletion)` |
| Retrieval Dispatcher | `NewGORMDispatcher(P,ids,riverOptions,fence,workflowScopedReindexOutbox,changecontrolScopedBinding)`（`gorm_dispatcher.go:31`），返回 application ScopedDispatcher；不再沿用旧 inserter 的 any Tx |
| Retrieval River inserter | `NewScopedInserter(P,client,fence)` / `NewScopedApplicationInserter(P,client,fence)`；同池 official SQL driver |
| Conversation Question | `NewGORMQuestionDispatcher(P,runtime,E,ids,clock)` 及 WithWorkspaceAnalysis / WithWorkspaceAnalysisAndAudit；分析依赖改 scoped RunStarter |
| Conversation Answer | `NewGORMAnswerFinalizer(P,agentScopedStore,E,clock)`（`gorm_finalizer.go:31`）；WorkspaceAnalysis finalizer、terminal/control Hook 在本次扫描时尚未全部形成，等 owner handoff |
| Organizing | `NewGORMStartRepository(P,runtime,binding)` + `organizingworkflow.NewScopedDispatcher(ScopedDispatcherDependencies)`；`NewGORMGenerationRepository(P,agentScopedFinalizer)`；`NewGORMFrozenMaterialFence(retrievalScopedSources,knowledgeScopedClaims)`；`NewScopedTerminalHook(gormRepository)` |

生命周期：`internal/platform/postgres/gorm.go:20` 从已有 pgxpool 调用 `stdlib.OpenDBFromPool`，不读取第二份 DSN；`pool.go:168` 的 Close 先关闭 SQL facade 再关闭物理池。先停止 Worker、executor、dispatcher、managed lease/后台控制器，再 Close Pool；GORM 当前 PrepareStmt=false，不应凭空新增 prepared-statement 生命周期。

Foundation handoff 曾写“跨平台 scope fail closed”，但当前 `transaction.go:19` 的 scope 没有 Pool identity，`GORMTransaction/SQLTransaction` 只验证具体类型、有效句柄和 active 状态。同池要求必须在组合层保证；不能声称已动态拒绝“另一个仍活跃 Pool 的 scope”。

### 5. pgx/pgconn owner 分类

第一次完整文件扫描得到 206 个非 `*_test.go` Go 文件直接 import pgx/pgconn，包含 4 个 `integration` build-tag Graph fixture 和 1 个 TestDB support 文件。该数字是清理前快照，不是当前最终剩余数。附录保留逐文件符号，便于区分真底层能力和 error-only 残留。

| 分类 | Owner / 范围 | 处理 |
| --- | --- | --- |
| 普通业务 Repository | Auth、DocumentHistory、Ingestion、Memory、Events、Audit、Workspace、Authoring、Review 3、Capture、Artifact、Export、GitSync、Collection、Health、ModelSettings、ChangeControl/ApprovalDispatch、Knowledge、Agent、Tools、Conversation、Organizing、Graph，以及 Retrieval 非 native 部分 | 全部经 GORM/scoped；不能因为“旧文件仍在”或“只用 PgError”加入 blanket allowlist |
| Runtime/Composition | `cmd/api/*.go`、`cmd/worker/*.go`、`internal/platform/rootgrant/postgres.go`、`internal/localmodelruntime/lifecycle_{postgres,store}.go`；ModelSettings bootstrap、Workspace runtimegrant 虽未直接 import pgx，仍依赖旧构造/接口 | 切完整 GORM 组合；RootGrant 不属于原生锁例外，已有 GORM 实现 |
| River 运行底层 | `internal/workflow/adapter/river/{client,migrator,queue_metrics}.go` | 保留 Worker/listener/队列观测/migration 的 native 能力 |
| River legacy producer | `workflow/adapter/river/inserter.go`、`export/adapter/river/dispatcher.go`、Retrieval legacy inserter | 改 scoped 官方 database/sql producer；不能整个 `adapter/river` 目录豁免 |
| 平台批准底层 | `platform/postgres/{pool,gorm,errors}.go`、`platform/migration/{atlasrunner,runner,rivermigrate}.go`、`platform/gitoperation/locker.go` | 平台 Pool/AfterConnect vector 注册、migration 和专用会话锁；错误解包集中在平台 |
| Retrieval 批准底层 | `native_capabilities.go`、`snapshot.go`、`source_refresh_lock.go`，以及 `repository.go:118` 的 `beginIndexNative` 及其必要 helper | 当前 native 能力为 BeginIndex COPY、Workspace Snapshot 的 COPY/临时表/会话锁、SourceRefresh 会话锁；需抽出 native owner 后精确 allowlist，不能把整个普通 repository.go 留白 |
| 管理角色 bootstrap | `cmd/local-model-runtime-credential-init/main.go` | 固定角色及 marker 验证、ALTER ROLE、0600 凭据；独立 SQL/security review |
| 测试 support | `internal/platform/testdb/fixture.go`、Graph `testfixture` 的 integration Go 文件、各 `*_test.go` | 单独测试分类；不能让这些路径成为生产包引用后门 |

错误分类收敛点：

- 只导入 pgconn 的 GORM 文件包括 Agent `gorm_rag_progress.go` / `gorm_workspace_analysis_model_operations.go`、GitSync `gorm_repository.go`、Graph `gorm_candidate_confirm.go`、Retrieval `gorm_core.go` / `gorm_search.go`、Tools `gorm_errors.go`、Workflow `gorm_reindex_outbox.go`。
- 大量普通 `errors.go` 仍解包 `*pgconn.PgError`：Agent、Artifact、Collection、Graph、Knowledge、Organizing、Review Core、Interview、Tools 等。替换时保留具体 SQLSTATE、ConstraintName 和 retryable 规则，不能统一转成 generic unavailable。
- Audit、Authoring、Events、Ingestion、Memory、ModelSettings、Capture、Workflow、Review/Workspace/LocalRuntime 的 GORM helper 仍引用 `pgx.ErrNoRows` 或将 `sql.ErrNoRows` 翻译回旧 sentinel；Final 应把内部 no-row 边界统一为标准 SQL/GORM sentinel 并核对原有领域错误。
- Collection/Graph `gorm_adapter.go` 的 pgx Row/Rows/CommandTag 是 compatibility shape，不是经证明需要原生连接的能力。Health `gorm_adapter.go` 甚至实现完整 pgx.Tx、BeginTx、Commit/Rollback bridge，不应永久 allowlist。

`transaction any` / opaque legacy ports 需要按 owner 一起删除或改成 Foundation scope：

| Owner | 当前旧边界 | 已有目标 |
| --- | --- | --- |
| Events | `application/ports.go:24` Appender.AppendTx；`adapter/postgres/append.go:28` | ScopedAppender.AppendScoped |
| Audit | `application/ports.go:16/26`、`recorder.go:46` RecordTx | ScopedAppender/ScopedReader、RecordScoped/ReadScoped |
| ModelSettings/LocalRuntime | SettingsAudit `AppendModelSettingsChangeTx(any)`；`localmodelruntime/lifecycle_store.go:52` TxLifecycle.WithTx(pgx.Tx) | ScopedSettingsAuditAppender、ScopedTxLifecycle.WithScope |
| Workflow | runtime_contract/control_hook/terminal_hook/cancellation_guard 的 any；旧 RuntimeRepository 与 inserter | scoped_runtime.go、GORMRuntimeRepositoryHooks、ScopedJobInserter/ScopedEnqueueFence |
| Agent | runtime_repository.go 的 Get/FinalizeModelRunTx(any)，WorkspaceAnalysis persistence/start/readiness | ScopedModelRunStore/Finalizer、scoped_workspace_analysis.go |
| Knowledge | ImpactAuditPort.RecordImpactAnalysisTx(any)、legacy Impact Repository | ScopedImpactAuditPort/Repository + NewScopedImpactServiceWithAudit |
| Artifact | legacy generation_terminal.go 的 OnWorkflowNodeTerminal(any) | GORMSectionGenerationTerminalHook |
| Health | legacy ScanCancellationGuard(any)、gormEventAppender.AppendTx(any) | GORM cancellation guard；内部 SQL core 也要消除旧 any bridge |
| Export | `exportTransaction.SideFactTransaction() any`，gormEventAppender/gormAuditAppender.AppendTx(any) | 保留共享 SQL core，但 SideFactTransaction 改成受控 scope，移除 Appender bridge |
| Conversation/Tools | WorkspaceAnalysis audit/control/terminal/refusal 中 RecordTx(any) | owner 新增 scoped participants；最终依 handoff |
| Organizing | FrozenFence.VerifyFrozen(any)、terminal result any、owner/transaction_fence 的 pgx cast | GORMFrozenMaterialFence、ScopedTerminalHook、GORMStartRepository |
| Retrieval | Dispatcher/JobInserter InsertTx(any) | application ScopedDispatcher + official scoped River inserter |

### 6. Legacy 删除与共享核心

下列“可整文件删除候选”仍以所有引用和现有测试完成迁移为前提；它们不是立即删除授权：

- `internal/export/adapter/postgres/legacy_adapter.go`：只承接 legacy DB/pgx transaction shape；共享 SQL core 在其他文件。
- `internal/review/adapter/postgres/deck_schedule.go`、`repository.go`：GORM 已有对应实现；后者含旧事务 helper，仍需检查同包 legacy 调用清零。
- `internal/review/learningpath/adapter/postgres/repository.go`：旧 DB/Repository、11 方法、pgx begin/commit/locks/classify；GORM 复用的 codec 在 `codec.go`。
- `internal/gitsync/adapter/postgres/{config,followup,auto_sync}.go`：旧 Repository methods 与 pgx helper；GORM 对应文件独立实现。
- `internal/knowledge/adapter/postgres/{claim_commands,evidence_topic,eligibility}.go`：旧 Repository SQL；GORM 对应文件有独立路径。
- `internal/workspace/adapter/postgres/git_capture.go`：旧 Tx SQL，GORM sibling 已独立。
- `internal/artifact/adapter/postgres/generation_query.go`：旧 ListSectionGenerations 与 authoritative query helper，GORM query 已独立。
- 旧 `workspace/runtimegrant/composition.go`、`modelsettings/runtime/bootstrap.go` 的构造部分在全部调用替换后退出；先确认其声明的 stable result/interface 没被其他 runtime 复用。

已确认混合文件与处理方向：

| Owner | 不能直接删除的文件/符号 | 建议 |
| --- | --- | --- |
| Auth | `repository.go:252` 起 invalid/unauthorized/notFound/unavailable；`model.go:42/71` scanners；`queries.go` 含 GORM SQL | 保留中性 errors/model/query，删除 legacy DB/Repository 和旧 SQL 常量 |
| DocumentHistory | `repository.go:76` validateCommitMappingRequest / classify；`model.go` scanners；`queries.go` GORM SQL | 提取校验和错误，删旧查询入口 |
| Ingestion | `repository.go:415` 起 chunkVersions/spanKey、scanners、JSON/nullable/UTC helpers | 先提取中性 codec，再删旧 Repository |
| Events | `store.go:15/119/130/146` columns、validateReplayQuery、scanner/scanEventRaw；`append.go:76/84/100/126` normalization/replay/error | 保留纯 helper；AppendTx、旧 query path 和 pgx imports 退出 |
| Audit | `store.go:128/135` nullableTime/auditListArguments；`scan.go`；`append.go:125` 起 JSON/lock/error helpers；`errors.go` | 同上；GORM 仍调用共享 classifyAppendFailure，不能删掉约束语义 |
| Workspace/RootGrant | `repository.go` RootGrantResolver/columns/scanners/source metadata；control_scan/control_errors；runtime.go 的 authorizeRuntime；`rootgrant/postgres.go:85` authoritativeView | 提取中性合同、scanner、授权检查；RootGrant 的旧 Row/Store/resolver 删除但 authoritativeView 保留 |
| Authoring | `repository.go:416` 起 document/draft/revision scanners 和验证；publication.go 的 columns、publication 状态/校验；restore_publication.go 的事实校验 | 保留验证/codec，与 GORM publication/restore 同步使用 |
| Memory | `repository.go:450` 起 auditActor、candidate/replay/query 校验；codec.go、errors.go | 提取纯 helper，移除旧 Tx/no-row 分支 |
| Review Core | `helpers.go` commandReceipt/scanners/validHash；session.go 的 validateReviewSessionBinding；invalidation.go 的 summary/record 校验 | codec 与证据验证保留；旧 SQL loaders 逐项去掉 |
| Interview | commands.go 的纯 Validate/Decode/transition helpers；repository.go 的 selection/list validation；codec.go 的 completion reservation/strict JSON | 保留这些不变量；不要把整个 commands.go 当旧实现删除 |
| LearningPath | `codec.go:61/73/379/460/481` decodeReviewSnapshot/validateReservation/validateResult/decodeStrict/validHash | 中性 codec 保留，里面旧 pgx query helper 单独清理 |
| Capture | repository/runtime/retry/profile_repository/profile_retry 均包含 GORM 使用的 receipt、scanners、lease/model-run binding、evidence/replay helpers | 无上述五文件可直接整删；详见附录共享声明 |
| Artifact | repository.go 的 schema/校验；codec.go 的 revision/coverage/citation selector；generation.go 的 binding/receipt/validation；generation_terminal.go 的纯 terminal decision；citation_backfill.go 的 state/validation | 依 Artifact handoff 先提取中性 helper，再删 pgx 入口 |
| GitSync | repository.go 的 lock purpose、config/run/attempt scanners、changes codec、领域错误；outbox.go validateOutboxLease；runs.go nullableID/time | 保留纯数据转换；不要保留旧 Tx helper |
| ModelSettings | repository.go 的 columns/state/scanners/errors；revision.go 的 secret/resolve/strict mapping；runtime/rollout/participant/activation/snapshot 的状态校验；audit.go 的 action/ID/actor | 跨多文件抽出 helpers；不应按文件名删除整个旧 runtime/snapshot |
| ChangeControl/ApprovalDispatch | repository.go 的 proposal/auth validators/scanners；revision_fence、revision_history；repository_writeback 的 hash/normalize/receipt；dispatch repository 的 validate/sameDecision/result/errors | 保留锁序外的纯校验与声明，驱动错误经平台投影 |
| Knowledge | scans.go 的 model/scan/validation；repository.go 的 command/aggregate constants；relation_commands/conflict_commands 的集合/幂等校验；timeline/relation_apply 的大量事实验证 | 保留中性业务校验；不要删掉 GORM 复用的 relation approval/replay 验证 |
| LocalRuntime | `lifecycle_postgres.go:751` 起 columns/scanners，1031 起 requirement/nullable/validation；`lifecycle_store.go` 稳定业务 ports | 将纯 helper 抽出，删除 PostgresStore/TxLifecycle；ScopedTxLifecycle 和普通 LifecycleStore 保留 |
| Collection | `gorm_repository.go:388/416` 创建旧 `Repository`，调用 `executeQuerySnapshot/executePlanSnapshot`；durable_scan.go 的 snapshot functions；gorm_adapter 的 pgx-shaped Rows | 先形成中性 query core/最小 rows 接口，再删旧 constructor；不能只抽几个纯 helper |
| Health | GORMScan/Schedule/Issue/Read/FactReader 持有 legacy 对象，GORM adapter 通过 UoW goroutine 暴露 pgx.Tx | 先改为中性内部 query/transaction 接口或直接 scoped UoW，再删 pgx shape；`scope_predicate.go`、`smart_collection_scope.go` 也在共享 SQL 链，不能因无 GORM 直接引用就删除 |
| Export | GORMRepository.core 是 `*Repository`；`create_list.go/lifecycle.go/cleanup.go/download.go/events.go` 是共享 SQL | 保留共享 core，删除 legacy_adapter；把 any SideFactTransaction 升级为 scoped |

附录的声明匹配是去除注释/字符串后的词法引用辅助清单，不是类型检查结果；存在局部同名变量和间接调用误差。例如 GitSync GORM auto-sync 的局部 `commit` 不是对旧 `commit(ctx,pgx.Tx)` 的依赖。Export/Health 方法委托也不能仅靠 free-function 匹配识别。

### 7. 静态门禁的最小拓展点

- `deploy/architecture_quality_baseline.py:385` 的 `go_imports` 已处理注释与字符串；`:406` 的 domain_dependencies 目前只查看 `internal/<owner>/domain`，不覆盖 application 或 Review 子 owner，更不检查 gorm/pgx。可复用提取逻辑，加独立、失败即非零的 Final gate；不要改变现有 read-only 报告语义。
- `cmd/api/main_test.go:114`、`cmd/worker/main_test.go:678` 和 `internal/export/adapter/river/architecture_test.go:14` 已有 Go AST 范式。可在既有测试中拓展有意义的 import/selector 检查，不必新增测试文件。
- `Makefile:9` 的 test 聚合、`:55` go-test、`.github/workflows/ci.yml:65` 的 make test 是接入位置；只提供手工 rg 命令不会阻止 CI 回归。
- pgx allowlist 应以文件或极窄 native 子目录为单位，记录 owner/原因/证据。不能 blanket allow `internal/platform`、`adapter/river` 或 `retrieval/adapter/postgres`。
- 分别检查：生产 pgx/pgconn import；所有 Domain/Application 层的 GORM/pgx/database/sql 具体事务类型；生产入口 legacy constructor；旧 any Tx 参数/返回；所有 Go 文件（包括测试/命令）对 `AutoMigrate`、`Migrator` 的方法调用/方法值引用。SQL varargs `...any` 不是事务 any。
- 用 AST/词法 token 排除注释、SQL 文本、检查器自身字符串；`riveradapter.NewMigrator` / Atlas Runner 不能被误判为 GORM `.Migrator()`。
- import gate 不能证明没有通过 `P.DB()` 取裸驱动。对业务 GORM 文件直接调用 Pool.DB、SQLTransaction、BeginTx 的位置增加精确审查；River/native bridge 使用专门例外。
- Export `architecture_test.go:34/47` 当前明确要求 `dispatcher.go` + `gorm_dispatcher.go` 和两个 exported constructors。删 legacy 后同步为真实最终列表，保留“不暴露非事务 Insert”的核心断言。

### 8. 现有验证与短时命令

本研究未运行任何测试或构建；以下为有源码支撑的建议，实际耗时和结果由 Final 记录。所有后端测试显式 `-timeout=60s`，不要直接运行 Makefile 中 5m / 全仓 / 浏览器聚合目标。

默认构造/lifecycle 覆盖：

- API：artifact_composition_test.go、capture_composition_test.go、export_composition_test.go、m8_interview_memory_composition_test.go、workspace_analysis_test.go、main_test.go、model_runtime_gate_test.go。
- Worker：main_test.go（共享模型实例、完整构造、bounded dispatch）、lifecycle_test.go（暂停队列、启动顺序、退出）、workspace_analysis_capability_test.go、git_sync_worker_test.go。
- CLI：modelctl/control_test.go、workspacectl/main_test.go、workspaceprobe/main_test.go、local-model-runtime/main_test.go、credential-init/main_test.go、migrate/main_test.go。
- 注意 `cmd/api/main_test.go:258`、`capture_composition_test.go:25` 等使用空 `&pgxpool.Pool{}` 成功构造；GORM 必须有 root+UoW。需要调整既有测试 seam/fixture 或由已有实库场景承担成功路径，不能只换成空 `&platformpostgres.Pool{}`，也不能跳过失败测试。

建议分组执行：

~~~sh
go build -mod=vendor ./cmd/api ./cmd/worker ./cmd/modelctl ./cmd/workspacectl ./cmd/workspaceprobe ./cmd/local-model-runtime ./cmd/local-model-runtime-credential-init ./cmd/migrate
go test -mod=vendor -count=1 -timeout=60s ./cmd/api ./cmd/worker
go test -mod=vendor -count=1 -timeout=60s ./cmd/modelctl ./cmd/workspacectl ./cmd/workspaceprobe ./cmd/local-model-runtime ./cmd/local-model-runtime-credential-init ./cmd/migrate
go test -mod=vendor -count=1 -timeout=60s -run '^TestRiverInsertionSurfaceIsTransactionOnly$' ./internal/export/adapter/river
go vet -mod=vendor ./cmd/api ./cmd/worker ./cmd/modelctl ./cmd/workspacectl ./cmd/workspaceprobe ./cmd/local-model-runtime ./cmd/local-model-runtime-credential-init ./cmd/migrate
~~~

优先真实同池/队列验证（每条独立运行）：

~~~sh
go test -mod=vendor -tags='integration testcontainers' -count=1 -timeout=60s -run '^TestRealGORMUnitOfWorkCommitRollbackAndScopeLifetime$' ./internal/platform/postgres
go test -mod=vendor -tags='integration testcontainers' -count=1 -timeout=60s -run '^TestRealGORMRiverInsertCommitsWithBusinessWriteAndWorkerConsumes$' ./internal/platform/postgres
go test -mod=vendor -tags='integration testcontainers' -count=1 -timeout=60s -run '^TestRealGORMRiverRollbackRemovesBusinessWriteAndJob$' ./internal/platform/postgres
go test -mod=vendor -tags='integration testcontainers' -count=1 -timeout=60s -run '^TestRealGORMPoolUsesSingleFacadeAndClosesIdempotently$' ./internal/platform/postgres
~~~

已有 cmd 实库场景：

| 包 | 测试名 |
| --- | --- |
| cmd/api | TestAPIHealthSmartCollectionCompositionStartsAndRejectsStaleBinding |
| cmd/api | TestArtifactGenerationCompositionCancellationPostgreSQLIntegration |
| cmd/api | TestReviewLearningPathProductionCompositionCreatesDraftPostgreSQL |
| cmd/api | TestInterviewProductionServiceCreatesVerifiedDraftsPostgreSQL |
| cmd/api | TestTimelineImpactDownstreamProposalAPICompositionIntegration |
| cmd/worker | TestWorkerCaptureCompositionRegistersExecutorDefinitionAndOutbox |
| cmd/worker | TestWorkerExportCompositionRegistersWorkerAndMaintenanceService |
| cmd/worker | TestGitSyncWorkerProductionCompositionAndEmptyDispatch |
| cmd/worker | TestWorkerHealthAffectedChangeCompositionConsumesTypedOutboxExactlyOnce |
| cmd/worker | TestWorkerHealthSmartCollectionCompositionExecutesDetectorAndFailsClosedOnDrift |
| cmd/worker | TestWorkerToolCompositionSeparatesContractsExecutorsAndTrustedAudit |
| cmd/worker | TestWorkerChatCompositionUsesEinoSchedulersAndRegistersRelationAndRAGTogether |
| cmd/worker | TestStartWorkerRuntimeFreshQueueStartsBeforeResume |
| cmd/local-model-runtime-credential-init | TestValidateRuntimeRoleAgainstPostgres |

按表选择单个测试，命令形状为 `go test -mod=vendor -tags='integration testcontainers' -count=1 -timeout=60s -run '^具体测试名$' ./具体包`。不要一次将整个表拼成长测试命令。

Fixture 盲区：API Health `health_smart_collection_composition_integration_test.go:252/254`、Artifact `artifact_integration_test.go:529/531`；Worker Capture `:26/28`、Export `:16/18`、GitSync `:26/28`、Tool `:37/39` 都在未设置 `ZHIXU_TEST_DATABASE_URL` 时跳过。Foundation `transaction_integration_test.go:35` 已接 `testdb.Require`。Final 应原位接入共享 fixture 或配置隔离 admin DB 并确认没有 SKIP；不得把没有实际运行的 PASS 当作实库证明。

Credential bootstrap 的已读安全事实：`main.go:20/22` 固定角色/marker；`:133` validateRuntimeRole；`:105` ALTER ROLE 字符串；`:332` parseRuntimePassword 只接受 64 位小写 hex（可有单个换行），新密码由随机 32 bytes hex 编码；凭据文件经 chmod/chown。研究未执行独立 SQL/security review或真实角色 provisioning；Final 仍需检查完整角色权限查询和失败恢复。

### 9. External references / versions

本次未访问外网；使用仓库 vendored 官方源码核对接口：

- `go.mod:3/16/23-25/45-46`：Go 1.25.4、pgx v5.10.0、River/riverdatabasesql/riverpgxv5 v0.40.0、GORM v1.31.2、GORM PostgreSQL driver v1.6.2。
- `vendor/github.com/jackc/pgx/v5/stdlib/sql.go:237/240`：OpenDBFromPool 设置 MaxIdleConns(0)。
- `vendor/github.com/riverqueue/river/riverdriver/riverdatabasesql/river_database_sql_driver.go:90`：SupportsListener=false。
- `vendor/github.com/riverqueue/river/client.go:1857`：官方 InsertTx 泛型事务入口。
- `internal/platform/postgres/river.go:11`：平台只返回基于已有 SQL facade 的 official driver。

### 10. Related specs

- `.trellis/workflow.md`：研究必须落盘；Final 与模块 owner 的职责。
- `.trellis/spec/backend/index.md`、`directory-structure.md`：Composition Root 和领域/基础设施边界。
- `.trellis/spec/backend/database-guidelines.md`：查询/事务/Atlas/TestDB，M4-C Approval Dispatch scoped 同池构造（约 :511）。
- `.trellis/spec/backend/model-settings-runtime.md`：scoped ModelSettings/LocalRuntime/Audit、Pool identity 盲区。
- `.trellis/spec/guides/index.md`：跨层与代码复用核对规则。
- 父任务 design.md:80 的 pgx allowlist 与 implement.md 的 owner map 优先于早期模块“pgx 全保留”的历史说明。

## Caveats / Not Found

- 未执行测试、构建、数据库操作、Git 操作、任务状态修改或代码编辑。本文件不是质量门禁 PASS 记录。
- 没有发现一个已经存在并强制执行全仓 GORM/pgx/AutoMigrate 边界的门禁；现有 Foundation handoff 中的“未来 Final 启用 allowlist”不代表已实现。
- 研究期间出现的新 Conversation/Organizing 文件未完成全量语义审查，其他进行中 owner 的最终签名、测试和删除清单以各自交接为准。
- 直接 import 清单不捕获所有通过接口/推导类型接触 pgx 的路径；必须再核对旧 constructor、`P.DB()`、legacy ports 和实际调用链。
- 共享 helper 词法匹配仅供定位，不能替代编译、Go/SQL review 和最终实库回归；尤其 Export、Health、Collection 通过方法/对象复用核心，不能按“没有 GORM 直接函数引用”删除。
- 本地代码交付不等于外部部署、发布审批或长期压力验证；本研究不执行这些外部动作。

## Appendix A: 第一次扫描的全部直接 pgx import 文件

口径：internal/ 与 cmd/ 下非 *_test.go 文件；显示实际引用的 pgx/pgconn/pgxpool/stdlib 符号。此表固定保留清理前快照，不能用其数量宣称当前剩余数。

| 文件 | owner 分类 | 驱动符号 |
| --- | --- | --- |
| `internal/agent/adapter/postgres/calls.go` | 普通 Adapter（仅错误分类） | `ErrNoRows` |
| `internal/agent/adapter/postgres/errors.go` | 普通 Adapter（仅 pgconn） | `PgError` |
| `internal/agent/adapter/postgres/gorm_core.go` | 普通 Adapter（仅错误分类） | `ErrNoRows`, `PgError` |
| `internal/agent/adapter/postgres/gorm_rag_progress.go` | 普通 Adapter（仅 pgconn） | `PgError` |
| `internal/agent/adapter/postgres/gorm_workspace_analysis_model_operations.go` | 普通 Adapter（仅 pgconn） | `PgError` |
| `internal/agent/adapter/postgres/memory_snapshots.go` | 普通 Adapter（仅错误分类） | `ErrNoRows` |
| `internal/agent/adapter/postgres/rag_progress.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx` |
| `internal/agent/adapter/postgres/repository.go` | 普通 Adapter/legacy | `CommandTag`, `ErrNoRows`, `Row`, `Rows`, `Tx` |
| `internal/agent/adapter/postgres/runs.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx` |
| `internal/agent/adapter/postgres/scans.go` | 普通 Adapter/legacy | `Row`, `Rows` |
| `internal/agent/adapter/postgres/workspace_analysis_candidate_authority.go` | 普通 Adapter（仅错误分类） | `ErrNoRows` |
| `internal/agent/adapter/postgres/workspace_analysis_capability.go` | 普通 Adapter（仅错误分类） | `ErrNoRows` |
| `internal/agent/adapter/postgres/workspace_analysis_model_operations.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx` |
| `internal/agent/adapter/postgres/workspace_analysis_runs.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx` |
| `internal/artifact/adapter/postgres/citation_backfill.go` | 普通 Adapter/legacy | `ErrNoRows`, `PgError`, `Tx` |
| `internal/artifact/adapter/postgres/codec.go` | 普通 Adapter/legacy | `ErrNoRows`, `Row`, `Tx` |
| `internal/artifact/adapter/postgres/errors.go` | 普通 Adapter（仅 pgconn） | `PgError` |
| `internal/artifact/adapter/postgres/generation.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx` |
| `internal/artifact/adapter/postgres/generation_query.go` | 普通 Adapter/legacy | `ErrNoRows`, `ReadOnly`, `RepeatableRead`, `Rows`, `TxOptions` |
| `internal/artifact/adapter/postgres/generation_terminal.go` | 普通 Adapter/legacy | `Tx` |
| `internal/artifact/adapter/postgres/repository.go` | 普通 Adapter/legacy | `CommandTag`, `ErrNoRows`, `ReadOnly`, `RepeatableRead`, `Row`, `Rows`, `Tx`, `TxOptions` |
| `internal/audit/adapter/postgres/append.go` | 普通 Adapter/legacy | `PgError`, `Tx` |
| `internal/audit/adapter/postgres/errors.go` | 普通 Adapter（仅错误分类） | `ErrNoRows`, `PgError` |
| `internal/audit/adapter/postgres/store.go` | 普通 Adapter/legacy | `Row`, `Rows`, `Tx` |
| `internal/auth/adapter/postgres/repository.go` | 普通 Adapter/legacy | `CommandTag`, `ErrNoRows`, `PgError`, `Row`, `Rows` |
| `internal/authoring/adapter/postgres/errors.go` | 普通 Adapter（仅错误分类） | `ErrNoRows`, `PgError` |
| `internal/authoring/adapter/postgres/publication.go` | 普通 Adapter/legacy | `ErrNoRows`, `Row`, `Tx`, `TxOptions` |
| `internal/authoring/adapter/postgres/read.go` | 普通 Adapter/legacy | `ErrNoRows`, `ReadOnly`, `RepeatableRead`, `TxOptions` |
| `internal/authoring/adapter/postgres/repository.go` | 普通 Adapter/legacy | `ErrNoRows`, `Pool`, `Row`, `Tx`, `TxOptions` |
| `internal/authoring/adapter/postgres/restore_publication.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx`, `TxOptions` |
| `internal/capture/adapter/postgres/gorm_core.go` | 普通 Adapter（仅错误分类） | `ErrNoRows`, `PgError` |
| `internal/capture/adapter/postgres/profile_repository.go` | 普通 Adapter/legacy | `ErrNoRows`, `Pool`, `Row`, `Rows`, `Tx`, `TxOptions` |
| `internal/capture/adapter/postgres/profile_retry.go` | 普通 Adapter/legacy | `ErrNoRows`, `Row`, `Tx`, `TxOptions` |
| `internal/capture/adapter/postgres/repository.go` | 普通 Adapter/legacy | `ErrNoRows`, `PgError`, `Pool`, `Row`, `Tx`, `TxOptions` |
| `internal/capture/adapter/postgres/retry.go` | 普通 Adapter/legacy | `Row`, `Tx`, `TxOptions` |
| `internal/capture/adapter/postgres/runtime.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx`, `TxOptions` |
| `internal/changecontrol/adapter/approvaldispatchpostgres/repository.go` | 普通 Adapter/legacy | `ErrNoRows`, `PgError`, `Tx` |
| `internal/changecontrol/adapter/postgres/repository.go` | 普通 Adapter/legacy | `ErrNoRows`, `PgError`, `Row`, `Rows`, `Tx` |
| `internal/changecontrol/adapter/postgres/repository_writeback.go` | 普通 Adapter/legacy | `ErrNoRows`, `PgError`, `Row`, `Tx` |
| `internal/changecontrol/adapter/postgres/revision.go` | 普通 Adapter（仅错误分类） | `ErrNoRows` |
| `internal/changecontrol/adapter/postgres/revision_fence.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx` |
| `internal/changecontrol/adapter/postgres/revision_history.go` | 普通 Adapter（仅错误分类） | `ErrNoRows` |
| `internal/collection/adapter/postgres/durable_scan.go` | 普通 Adapter/legacy | `ErrNoRows`, `ReadOnly`, `RepeatableRead`, `Row`, `Rows`, `Tx`, `TxOptions` |
| `internal/collection/adapter/postgres/errors.go` | 普通 Adapter（仅 pgconn） | `PgError` |
| `internal/collection/adapter/postgres/gorm_adapter.go` | 普通 Adapter/legacy | `CommandTag`, `Conn`, `ErrNoRows`, `FieldDescription`, `Row`, `Rows` |
| `internal/collection/adapter/postgres/query.go` | 普通 Adapter/legacy | `ErrNoRows`, `ReadOnly`, `RepeatableRead`, `Rows`, `TxOptions` |
| `internal/collection/adapter/postgres/repository.go` | 普通 Adapter/legacy | `CommandTag`, `ErrNoRows`, `ReadOnly`, `RepeatableRead`, `Row`, `Rows`, `Tx`, `TxOptions` |
| `internal/conversation/adapter/postgres/conversations.go` | 普通 Adapter/legacy | `ErrNoRows`, `Rows` |
| `internal/conversation/adapter/postgres/dispatch.go` | 普通 Adapter/legacy | `ErrNoRows`, `PgError`, `Tx` |
| `internal/conversation/adapter/postgres/draft_stream.go` | 普通 Adapter/legacy | `ErrNoRows`, `Row`, `Tx` |
| `internal/conversation/adapter/postgres/errors.go` | 普通 Adapter（仅错误分类） | `ErrNoRows`, `PgError` |
| `internal/conversation/adapter/postgres/execution_context.go` | 普通 Adapter（仅错误分类） | `ErrNoRows` |
| `internal/conversation/adapter/postgres/feedback.go` | 普通 Adapter（仅错误分类） | `ErrNoRows` |
| `internal/conversation/adapter/postgres/finalizer.go` | 普通 Adapter/legacy | `ErrNoRows`, `Row`, `Tx` |
| `internal/conversation/adapter/postgres/repository.go` | 普通 Adapter/legacy | `ErrNoRows`, `Row`, `Rows`, `Tx` |
| `internal/conversation/adapter/postgres/turns.go` | 普通 Adapter/legacy | `ErrNoRows`, `Rows` |
| `internal/conversation/adapter/postgres/workspace_analysis_control_audit_hook.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx` |
| `internal/conversation/adapter/postgres/workspace_analysis_finalizer.go` | 普通 Adapter/legacy | `ErrNoRows`, `Row`, `Tx` |
| `internal/conversation/adapter/postgres/workspace_analysis_terminal_hook.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx` |
| `internal/conversation/adapter/postgres/workspace_analysis_timeline.go` | 普通 Adapter/legacy | `ErrNoRows`, `Row`, `Tx` |
| `internal/documenthistory/adapter/postgres/repository.go` | 普通 Adapter/legacy | `ErrNoRows`, `Row`, `Rows` |
| `internal/events/adapter/postgres/append.go` | 普通 Adapter/legacy | `ErrNoRows`, `PgError`, `Tx` |
| `internal/events/adapter/postgres/gorm_store.go` | 普通 Adapter（仅错误分类） | `ErrNoRows`, `PgError` |
| `internal/events/adapter/postgres/store.go` | 普通 Adapter/legacy | `Row`, `Rows` |
| `internal/export/adapter/postgres/legacy_adapter.go` | 普通 Adapter/legacy | `CommandTag`, `ErrTxClosed`, `Row`, `Rows`, `Tx` |
| `internal/export/adapter/river/dispatcher.go` | legacy River producer | `Pool`, `Tx` |
| `internal/gitsync/adapter/postgres/config.go` | 普通 Adapter/legacy | `ErrNoRows`, `Row`, `Tx`, `TxOptions` |
| `internal/gitsync/adapter/postgres/followup.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx`, `TxOptions` |
| `internal/gitsync/adapter/postgres/gorm_repository.go` | 普通 Adapter（仅 pgconn） | `PgError` |
| `internal/gitsync/adapter/postgres/outbox.go` | 普通 Adapter/legacy | `ErrNoRows`, `TxOptions` |
| `internal/gitsync/adapter/postgres/repository.go` | 普通 Adapter/legacy | `CommandTag`, `PgError`, `Row`, `Rows`, `Tx`, `TxOptions` |
| `internal/gitsync/adapter/postgres/runs.go` | 普通 Adapter/legacy | `ErrNoRows`, `Row`, `TxOptions` |
| `internal/graph/adapter/postgres/candidate_confirm.go` | 普通 Adapter/legacy | `ErrNoRows`, `PgError`, `Tx` |
| `internal/graph/adapter/postgres/candidate_repository.go` | 普通 Adapter/legacy | `ErrNoRows`, `PgError`, `TxOptions` |
| `internal/graph/adapter/postgres/detail.go` | 普通 Adapter（仅错误分类） | `ErrNoRows` |
| `internal/graph/adapter/postgres/errors.go` | 普通 Adapter（仅 pgconn） | `PgError` |
| `internal/graph/adapter/postgres/gorm_adapter.go` | 普通 Adapter/legacy | `CommandTag`, `Conn`, `ErrNoRows`, `FieldDescription`, `NewCommandTag`, `Row`, `Rows` |
| `internal/graph/adapter/postgres/gorm_candidate_confirm.go` | 普通 Adapter（仅 pgconn） | `PgError` |
| `internal/graph/adapter/postgres/gorm_core.go` | 普通 Adapter（仅错误分类） | `ErrNoRows` |
| `internal/graph/adapter/postgres/repository.go` | 普通 Adapter/legacy | `CommandTag`, `ReadOnly`, `RepeatableRead`, `Row`, `Rows`, `Tx`, `TxOptions` |
| `internal/graph/adapter/postgres/scan_cancellation.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx` |
| `internal/graph/adapter/postgres/scan_planner.go` | 普通 Adapter（仅错误分类） | `ErrNoRows` |
| `internal/graph/adapter/postgres/scan_repository.go` | 普通 Adapter/legacy | `ErrNoRows`, `ErrTxClosed`, `PgError`, `RepeatableRead`, `Tx`, `TxOptions` |
| `internal/graph/adapter/postgres/topic_scan_source.go` | 普通 Adapter（仅错误分类） | `ErrNoRows` |
| `internal/graph/testfixture/capacity_integration.go` | 测试 support（独立分类） | `Pool`, `Tx`, `TxOptions`；build=integration |
| `internal/graph/testfixture/cmd/graphfixture/main.go` | 测试 support（独立分类） | `New`；build=integration |
| `internal/graph/testfixture/cmd/m8learningfixture/main.go` | 测试 support（独立分类） | `BeginFunc`, `Connect`, `New`, `Pool`, `Tx`；build=integration |
| `internal/graph/testfixture/fixture_integration.go` | 测试 support（独立分类） | `ErrNoRows`, `Pool`, `Tx`, `TxOptions`；build=integration |
| `internal/health/adapter/collection/legacy_membership.go` | 普通 Adapter/legacy | `Tx` |
| `internal/health/adapter/postgres/affected_change_dispatcher.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx` |
| `internal/health/adapter/postgres/detector_reader.go` | 普通 Adapter/legacy | `Rows` |
| `internal/health/adapter/postgres/gorm_adapter.go` | 普通 Adapter/legacy | `Batch`, `BatchResults`, `CommandTag`, `Conn`, `CopyFromSource`, `ErrNoRows`, `ErrTxClosed`, `FieldDescription`, `Identifier`, `LargeObjects`, `NewCommandTag`, `ReadCommitted`, `ReadOnly`, `ReadUncommitted`, `RepeatableRead`, `Row`, `Rows`, `Serializable`, `StatementDescription`, `Tx`, `TxOptions` |
| `internal/health/adapter/postgres/gorm_repository.go` | 普通 Adapter/legacy | `Tx` |
| `internal/health/adapter/postgres/issue_repository.go` | 普通 Adapter/legacy | `ErrNoRows`, `PgError`, `Row`, `Rows`, `Tx` |
| `internal/health/adapter/postgres/read_repository.go` | 普通 Adapter/legacy | `ErrNoRows`, `Row`, `Rows` |
| `internal/health/adapter/postgres/scan_repository.go` | 普通 Adapter/legacy | `CommandTag`, `ErrNoRows`, `PgError`, `ReadOnly`, `RepeatableRead`, `Row`, `Rows`, `Tx`, `TxOptions` |
| `internal/health/adapter/postgres/schedule_repository.go` | 普通 Adapter/legacy | `ErrNoRows`, `Row`, `Tx` |
| `internal/ingestion/adapter/postgres/gorm_repository.go` | 普通 Adapter（仅错误分类） | `ErrNoRows`, `PgError` |
| `internal/ingestion/adapter/postgres/repository.go` | 普通 Adapter/legacy | `ErrNoRows`, `PgError`, `Row`, `Rows`, `Tx` |
| `internal/knowledge/adapter/postgres/claim_commands.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx` |
| `internal/knowledge/adapter/postgres/conflict_commands.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx` |
| `internal/knowledge/adapter/postgres/errors.go` | 普通 Adapter（仅 pgconn） | `PgError` |
| `internal/knowledge/adapter/postgres/read.go` | 普通 Adapter/legacy | `ReadOnly`, `RepeatableRead`, `Tx`, `TxOptions` |
| `internal/knowledge/adapter/postgres/relation_apply.go` | 普通 Adapter/legacy | `ErrNoRows`, `PgError`, `Tx` |
| `internal/knowledge/adapter/postgres/relation_commands.go` | 普通 Adapter/legacy | `ErrNoRows`, `Rows`, `Tx` |
| `internal/knowledge/adapter/postgres/repository.go` | 普通 Adapter/legacy | `ErrNoRows`, `Row`, `Rows`, `Tx` |
| `internal/knowledge/adapter/postgres/scans.go` | 普通 Adapter/legacy | `Rows` |
| `internal/knowledge/adapter/postgres/timeline.go` | 普通 Adapter/legacy | `ErrNoRows`, `Row`, `Tx` |
| `internal/knowledge/adapter/postgres/timeline_projection.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx` |
| `internal/localmodelruntime/gorm_core.go` | Runtime/Composition | `ErrNoRows` |
| `internal/localmodelruntime/lifecycle_postgres.go` | Runtime/Composition | `ErrNoRows`, `ReadOnly`, `RepeatableRead`, `Row`, `Rows`, `Tx`, `TxOptions` |
| `internal/localmodelruntime/lifecycle_store.go` | Runtime/Composition | `Tx` |
| `internal/memory/adapter/postgres/codec.go` | 普通 Adapter/legacy | `ErrNoRows`, `Row` |
| `internal/memory/adapter/postgres/errors.go` | 普通 Adapter（仅错误分类） | `ErrNoRows`, `PgError` |
| `internal/memory/adapter/postgres/gorm_repository.go` | 普通 Adapter（仅错误分类） | `ErrNoRows`, `PgError` |
| `internal/memory/adapter/postgres/repository.go` | 普通 Adapter/legacy | `ErrNoRows`, `Row`, `Rows`, `Tx` |
| `internal/modelsettings/adapter/postgres/activation.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx` |
| `internal/modelsettings/adapter/postgres/audit.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx` |
| `internal/modelsettings/adapter/postgres/gorm_core.go` | 普通 Adapter（仅错误分类） | `ErrNoRows`, `PgError` |
| `internal/modelsettings/adapter/postgres/participant.go` | 普通 Adapter（仅错误分类） | `ErrNoRows` |
| `internal/modelsettings/adapter/postgres/repository.go` | 普通 Adapter/legacy | `ErrNoRows`, `PgError`, `Row`, `Tx`, `TxOptions` |
| `internal/modelsettings/adapter/postgres/revision.go` | 普通 Adapter/legacy | `ErrNoRows`, `Row` |
| `internal/modelsettings/adapter/postgres/rollout.go` | 普通 Adapter/legacy | `Tx` |
| `internal/modelsettings/adapter/postgres/runtime.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx` |
| `internal/modelsettings/adapter/postgres/snapshot.go` | 普通 Adapter/legacy | `ErrNoRows`, `ReadOnly`, `RepeatableRead`, `Tx`, `TxOptions` |
| `internal/organizing/adapter/owner/transaction_fence.go` | 普通 Adapter/legacy | `ErrNoRows`, `Rows`, `Tx` |
| `internal/organizing/adapter/postgres/codec.go` | 普通 Adapter/legacy | `Row`, `Rows` |
| `internal/organizing/adapter/postgres/errors.go` | 普通 Adapter（仅 pgconn） | `PgError` |
| `internal/organizing/adapter/postgres/generation.go` | 普通 Adapter/legacy | `ErrNoRows`, `TxOptions` |
| `internal/organizing/adapter/postgres/repository.go` | 普通 Adapter/legacy | `CommandTag`, `ErrNoRows`, `PgError`, `ReadOnly`, `RepeatableRead`, `Row`, `Rows`, `Serializable`, `Tx`, `TxOptions` |
| `internal/organizing/adapter/postgres/runtime.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx`, `TxOptions` |
| `internal/organizing/adapter/postgres/template.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx`, `TxOptions` |
| `internal/organizing/workflow/terminal.go` | 普通 Adapter/legacy | `ErrNoRows`, `PgError`, `Tx` |
| `internal/platform/gitoperation/locker.go` | 批准平台底层 | `Conn`, `ConnConfig`, `ConnectConfig`, `Pool` |
| `internal/platform/migration/atlasrunner.go` | 批准平台底层 | `OpenDBFromPool`, `Pool` |
| `internal/platform/migration/rivermigrate.go` | 批准平台底层 | `Pool` |
| `internal/platform/migration/runner.go` | 批准平台底层 | `Conn`, `ConnectConfig`, `Pool` |
| `internal/platform/postgres/errors.go` | 批准平台底层 | `PgError` |
| `internal/platform/postgres/gorm.go` | 批准平台底层 | `OpenDBFromPool`, `Pool` |
| `internal/platform/postgres/pool.go` | 批准平台底层 | `Config`, `Conn`, `NewWithConfig`, `ParseConfig`, `Pool`, `Row`, `Rows`, `Tx` |
| `internal/platform/rootgrant/postgres.go` | Runtime/Composition | `Row` |
| `internal/platform/testdb/fixture.go` | 测试 support（独立分类） | `Identifier`, `NewWithConfig`, `ParseConfig`, `Pool` |
| `internal/retrieval/adapter/postgres/activation_tx.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx` |
| `internal/retrieval/adapter/postgres/completion.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx` |
| `internal/retrieval/adapter/postgres/delivery_runtime.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx` |
| `internal/retrieval/adapter/postgres/dispatcher.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx` |
| `internal/retrieval/adapter/postgres/errors.go` | 普通 Adapter（仅错误分类） | `ErrNoRows`, `PgError` |
| `internal/retrieval/adapter/postgres/evidence.go` | 普通 Adapter（仅错误分类） | `ErrNoRows` |
| `internal/retrieval/adapter/postgres/gorm_core.go` | 普通 Adapter（仅 pgconn） | `PgError` |
| `internal/retrieval/adapter/postgres/gorm_search.go` | 普通 Adapter（仅 pgconn） | `PgError` |
| `internal/retrieval/adapter/postgres/native_capabilities.go` | Retrieval native 候选 | `Pool` |
| `internal/retrieval/adapter/postgres/processor_context.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx` |
| `internal/retrieval/adapter/postgres/regression.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx` |
| `internal/retrieval/adapter/postgres/repository.go` | 混合：ordinary + native COPY | `CopyFromSlice`, `ErrNoRows`, `Identifier`, `Row`, `Tx` |
| `internal/retrieval/adapter/postgres/scans.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx` |
| `internal/retrieval/adapter/postgres/search.go` | 普通 Adapter/legacy | `ErrNoRows`, `Row`, `Rows`, `Tx` |
| `internal/retrieval/adapter/postgres/snapshot.go` | Retrieval native 候选 | `Conn`, `CopyFromSlice`, `ErrNoRows`, `Identifier`, `RepeatableRead`, `Rows`, `Tx`, `TxOptions` |
| `internal/retrieval/adapter/postgres/source_refresh_lock.go` | Retrieval native 候选 | `Conn` |
| `internal/retrieval/adapter/postgres/vector_build.go` | 普通 Adapter/legacy | `Tx` |
| `internal/review/adapter/postgres/errors.go` | 普通 Adapter（仅 pgconn） | `PgError` |
| `internal/review/adapter/postgres/gorm_core.go` | 普通 Adapter（仅错误分类） | `ErrNoRows` |
| `internal/review/adapter/postgres/helpers.go` | 普通 Adapter/legacy | `ErrNoRows`, `Row`, `Rows`, `Tx` |
| `internal/review/adapter/postgres/repository.go` | 普通 Adapter/legacy | `CommandTag`, `ErrNoRows`, `Row`, `Rows`, `Tx` |
| `internal/review/adapter/postgres/session.go` | 普通 Adapter（仅错误分类） | `ErrNoRows` |
| `internal/review/interview/adapter/postgres/codec.go` | 普通 Adapter/legacy | `ErrNoRows`, `Row`, `Rows`, `Tx` |
| `internal/review/interview/adapter/postgres/commands.go` | 普通 Adapter/legacy | `Tx` |
| `internal/review/interview/adapter/postgres/errors.go` | 普通 Adapter（仅 pgconn） | `PgError` |
| `internal/review/interview/adapter/postgres/repository.go` | 普通 Adapter/legacy | `Row`, `Rows`, `Tx` |
| `internal/review/learningpath/adapter/postgres/codec.go` | 普通 Adapter/legacy | `ErrNoRows`, `Row`, `Rows`, `Tx` |
| `internal/review/learningpath/adapter/postgres/gorm_repository.go` | 普通 Adapter（仅错误分类） | `ErrNoRows`, `PgError` |
| `internal/review/learningpath/adapter/postgres/repository.go` | 普通 Adapter/legacy | `ErrNoRows`, `PgError`, `Row`, `Rows`, `Tx` |
| `internal/tools/adapter/postgres/calls.go` | 普通 Adapter/legacy | `Rows`, `Tx` |
| `internal/tools/adapter/postgres/errors.go` | 普通 Adapter（仅 pgconn） | `PgError` |
| `internal/tools/adapter/postgres/gorm_errors.go` | 普通 Adapter（仅 pgconn） | `PgError` |
| `internal/tools/adapter/postgres/policy.go` | 普通 Adapter/legacy | `Tx` |
| `internal/tools/adapter/postgres/repository.go` | 普通 Adapter/legacy | `Tx` |
| `internal/tools/adapter/postgres/result_receipt_failures.go` | 普通 Adapter/legacy | `Tx` |
| `internal/tools/adapter/postgres/result_receipts.go` | 普通 Adapter/legacy | `Tx` |
| `internal/tools/adapter/postgres/scans.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx` |
| `internal/tools/adapter/postgres/workspace_analysis_authority.go` | 普通 Adapter/legacy | `Tx` |
| `internal/tools/adapter/postgres/workspace_analysis_events.go` | 普通 Adapter/legacy | `Tx` |
| `internal/tools/adapter/postgres/workspace_analysis_operations.go` | 普通 Adapter/legacy | `Tx` |
| `internal/tools/adapter/postgres/workspace_analysis_refusals.go` | 普通 Adapter/legacy | `Tx` |
| `internal/workflow/adapter/postgres/gorm_core.go` | 普通 Adapter（仅错误分类） | `ErrNoRows`, `PgError` |
| `internal/workflow/adapter/postgres/gorm_reindex_outbox.go` | 普通 Adapter（仅 pgconn） | `PgError` |
| `internal/workflow/adapter/postgres/repository.go` | 普通 Adapter/legacy | `ErrNoRows`, `PgError`, `Row`, `Tx` |
| `internal/workflow/adapter/postgres/runtime_output.go` | 普通 Adapter（仅错误分类） | `ErrNoRows` |
| `internal/workflow/adapter/postgres/runtime_start.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx` |
| `internal/workflow/adapter/postgres/runtime_state.go` | 普通 Adapter/legacy | `ErrNoRows`, `Row`, `Tx` |
| `internal/workflow/adapter/river/client.go` | River Worker/listener/观测/migration | `Pool` |
| `internal/workflow/adapter/river/inserter.go` | legacy River producer | `Tx` |
| `internal/workflow/adapter/river/migrator.go` | River Worker/listener/观测/migration | `Pool`, `Tx` |
| `internal/workflow/adapter/river/queue_metrics.go` | River Worker/listener/观测/migration | `Row` |
| `internal/workspace/adapter/postgres/control.go` | 普通 Adapter/legacy | `ErrNoRows`, `Row`, `Tx` |
| `internal/workspace/adapter/postgres/control_errors.go` | 普通 Adapter（仅错误分类） | `ErrNoRows`, `PgError` |
| `internal/workspace/adapter/postgres/git_capture.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx` |
| `internal/workspace/adapter/postgres/gorm_core.go` | 普通 Adapter（仅错误分类） | `ErrNoRows` |
| `internal/workspace/adapter/postgres/rebind_audit.go` | 普通 Adapter/legacy | `Tx` |
| `internal/workspace/adapter/postgres/registry.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx` |
| `internal/workspace/adapter/postgres/repository.go` | 普通 Adapter/legacy | `ErrNoRows`, `PgError`, `Row`, `Rows`, `Tx` |
| `internal/workspace/adapter/postgres/runtime.go` | 普通 Adapter/legacy | `ErrNoRows`, `Tx` |
| `cmd/api/main.go` | Runtime/Composition | `Pool` |
| `cmd/api/workspace_analysis.go` | Runtime/Composition | `Pool` |
| `cmd/local-model-runtime-credential-init/main.go` | 批准 credential bootstrap | `Connect`, `Row` |
| `cmd/worker/main.go` | Runtime/Composition | `Pool` |
| `cmd/worker/workspace_analysis_capability.go` | Runtime/Composition | `Pool` |

## Appendix B: 已完成模块文件中的 GORM 直接词法依赖

下面只列发现共享声明的文件。列表来自去除注释与字符串后的声明/标识符匹配；未列出的文件也可能经共享核心间接调用，不能据此删除。符号后的数字是该声明在原文件的扫描行号。局部同名变量可能形成少数误报，应沿调用确认。此附录不包含本轮七个进行中 owner 的实现清单。

| 文件 | 需要保留或核验的声明 |
| --- | --- |
| `internal/artifact/adapter/postgres/generation.go` | `sectionGenerationColumns:28`, `validateFinalizationLookup:491`, `validateFinalizeSectionCommand:505`, `proposalCitationInputs:655`, `sectionFromVerifiedProposal:673`, `validateCompletedSectionProposal:702`, `validateProposalRuntime:730`, `completeSectionGeneration:783`, `outputReceiptFromCompletion:864`, `validateGenerationReceipt:873`, `revisionSectionByKey:883`, `nilGenerationDependency:913`, `sectionGenerationWorkflowKey:926`, `sameStartedWorkflow:931`, `sameGenerationStartRequest:939`, `generationMatchesInput:945`, `validateGenerationContextQuery:951`, `scanSectionGeneration:1047`, `sameGenerationJSON:1107`, `decodeGenerationJSON:1115`, `generationCapabilityError:1146`, `generationInputError:1153`, `generationContextError:1160`, `generationOutputError:1167`, `generationEvidenceVerificationError:1181`, `generationFinalizationUnknown:1200` |
| `internal/artifact/adapter/postgres/generation_terminal.go` | `resolveSectionGenerationTerminal:125`, `validateTerminalModelRunRecord:177`, `terminalSectionGeneration:199`, `mapGenerationTerminalDependencyError:255` |
| `internal/artifact/adapter/postgres/citation_backfill.go` | `citationBackfillState:17`, `citationBackfillRevision:35`, `optionalFoundationID:120`, `validateCitationBackfillState:128`, `citationBackfillDataError:301`, `shouldPersistCitationBackfillFailure:386` |
| `internal/artifact/adapter/postgres/repository.go` | `validateExternalReservationBinding:292`, `validateCreateRecord:434`, `validateTransitionRecord:456`, `validateBinding:496`, `validID:503`, `artifactSchemaVersion:18`, `artifactRevisionSchemaVersion:20`, `artifactReceiptSchemaVersion:21` |
| `internal/artifact/adapter/postgres/codec.go` | `revisionSelect:26`, `persistedRevision:78`, `scanState:94`, `scanRevision:132`, `decodePersistedRevision:141`, `coverageFromSections:185`, `decodeJSON:212`, `marshalJSON:228`, `revisionCitationSelector:258`, `revisionCitationSelectors:303`, `citationSelectorIdentity:373`, `markdownFromSections:377`, `commandReceipt:392`, `commandResult:455` |
| `internal/artifact/adapter/postgres/errors.go` | `requestInvalid:13`, `notFound:17`, `idempotencyConflict:21`, `versionConflict:25`, `unavailable:29`, `inconsistent:33`, `classify:49` |
| `internal/audit/adapter/postgres/store.go` | `nullableTime:128`, `auditListArguments:135` |
| `internal/audit/adapter/postgres/scan.go` | `auditSelect:13`, `scanner:18`, `scanEventRaw:28`, `corruptEvent:133` |
| `internal/audit/adapter/postgres/append.go` | `encodeAuditJSON:125`, `classifyAppendFailure:147`, `optionalWorkspaceValue:169`, `optionalText:176`, `auditLockScope:183` |
| `internal/audit/adapter/postgres/errors.go` | `domainErrorInvalid:15`, `domainErrorUnavailable:19`, `appendConflict:23`, `classifyStoreError:27`, `auditNoRows:54` |
| `internal/auth/adapter/postgres/queries.go` | `authSessionTable:3`, `authAPITokenTable:5`, `authCheckSQL:6`, `apiTokenListProjection:40`, `gormCreateSessionSQL:45`, `gormRotateSessionSQL:51`, `gormAuthenticateSessionSQL:57`, `gormCreateAPITokenSQL:68`, `gormAuthenticateAPITokenSQL:73` |
| `internal/auth/adapter/postgres/model.go` | `scanSession:42`, `scanAPIToken:71` |
| `internal/auth/adapter/postgres/repository.go` | `invalid:252`, `unauthorized:256`, `notFound:260`, `unavailable:264` |
| `internal/authoring/adapter/postgres/restore_publication.go` | `restorePublicationFacts:158`, `validateExistingRestoreRevision:258`, `validateRestorePublicationRecord:268`, `restoreArticleRevisionID:277` |
| `internal/authoring/adapter/postgres/publication.go` | `reservationColumns:720`, `publicationColumns:786`, `validateStoredReservation:873`, `validateAbandonedReservation:941`, `sameReservationCommand:951`, `reservationMatchesBinding:957`, `validateReservePublicationRecord:964`, `validateCompletePublicationRecord:975`, `validateAbandonPublicationRecord:984`, `validGitCommit:993`, `publicationCommitMismatch:18`, `publicationStateMismatch:20`, `publicationRejected:21`, `publicationNeedsRevision:22`, `publicationCancelled:23` |
| `internal/authoring/adapter/postgres/read.go` | `validateArticleRevisionBatchQuery:176` |
| `internal/authoring/adapter/postgres/repository.go` | `scanDocument:416`, `draftColumns:445`, `scanDraft:447`, `revisionColumns:461`, `scanRevision:465`, `commandReceipt:483`, `validateCreateRecord:621`, `validateUpdateRecord:633`, `validateFreezeRecord:645`, `validHash:668`, `validID:680` |
| `internal/authoring/adapter/postgres/errors.go` | `gormNoRows:59`, `classifyGORM:63`, `notFound:103`, `idempotencyConflict:107`, `inconsistent:111`, `versionConflict:115`, `publicationConflict:119` |
| `internal/capture/adapter/postgres/runtime.go` | `captureReturning:19`, `attemptReturning:24`, `scanAttempt:638`, `scanProfile:675`, `validateLease:702`, `validErrorCode:710` |
| `internal/capture/adapter/postgres/profile_retry.go` | `encodeProfileRetryReceipt:233`, `decodeProfileRetryReceipt:258`, `restoreProfileRetryView:353`, `validProfileRetryBinding:405`, `validProfileRetryRecord:411`, `profileRetryCommandType:19` |
| `internal/capture/adapter/postgres/retry.go` | `queuedRetry:95`, `encodeRetryReceipt:164`, `decodeRetryReceipt:176`, `validateRetryRecord:193` |
| `internal/capture/adapter/postgres/repository.go` | `captureSelect:179`, `encodeReceipt:257`, `idempotencyConflict:312`, `decodeReceipt:317`, `restoreCapture:347`, `scanCapture:362`, `validateCreateRecord:406`, `isUniqueViolation:451`, `validID:456`, `validHash:461`, `invalid:473`, `inconsistent:477` |
| `internal/capture/adapter/postgres/profile_repository.go` | `boundedProfileChunkContent:163`, `profileAttemptRecord:765`, `profileModelRunBindingDecision:806`, `scanProfileAttempt:961`, `replayPreparedAttempt:1026`, `validateRunAttemptBinding:1052`, `modelRunMatchesAttempt:1059`, `validateSuccessfulRun:1067`, `validateRevisionAttemptBinding:1092`, `validateCompleteProfile:1103`, `validateFailProfile:1134`, `scanProfileRevision:1228`, `decodeHeadingPath:1301`, `validProfileLookup:1338`, `validProfileContract:1342`, `validPrepareProfile:1349`, `nullableProfileRevision:1365`, `profileContentEvidence:1372`, `evidenceSpanIDs:1392`, `sameFoundationIDs:1401`, `sameProfileRevision:1413`, `exactFailedProfileReplay:1423`, `profileTerminalAttemptReplay:1431`, `samePersistedTime:1436`, `nilProfilePort:1452`, `profileQueryInvalidCode:27`, `profileContextInvalidCode:29`, `profileAttemptConflictCode:30`, `profileVersionConflictCode:31`, `profilePersistenceFailedCode:32`, `profileFinalizationFailedCode:33`, `profileAttemptRunning:788`, `profileAttemptSucceeded:790`, `profileAttemptFailed:791`, `profileAttemptCapabilityUnavailable:792`, `profileAttemptReturning:793` |
| `internal/changecontrol/adapter/approvaldispatchpostgres/repository.go` | `approvalDispatchNo:23`, `validateApprovalDispatchCommand:300`, `validateApprovedDispatchSafety:308`, `sameApprovalDecision:316`, `dispatchResult:327`, `jsonEqualDispatch:331`, `isNilDispatchDependency:336`, `classifyDispatch:360` |
| `internal/changecontrol/adapter/postgres/repository_writeback.go` | `hashWritebackCredential:363`, `writebackColumns:605`, `proposalCommitColumns:612`, `scanWritebackExecution:614`, `scanProposalCommit:650`, `proposalStatusForCheckpoint:812`, `executionFromCreate:825`, `normalizeCreateWriteback:838`, `normalizeCheckpointWriteback:847`, `normalizePublishWriteback:909`, `sameProposalCommitIdentity:918`, `sameJSON:925`, `nullableString:935`, `nullableInt64:942`, `nullableUint32:949`, `writebackDomainError:963`, `classifyWriteback:982` |
| `internal/changecontrol/adapter/postgres/revision_fence.go` | `revisionFenceAuthorization:13`, `revisionFenceApproval:18`, `revisionFenceDispatch:22`, `revisionFenceExecution:26`, `revisionSupersedeFacts:33`, `validateRevisionSupersedeFacts:194`, `revisionNotEditable:297`, `revisionWorkflowActive:301`, `revisionAuthorizationConflict:309` |
| `internal/changecontrol/adapter/postgres/revision_history.go` | `bindRevisionHistoryDecision:179`, `revisionHistoryBindingError:204` |
| `internal/changecontrol/adapter/postgres/revision.go` | `revisionCommandError:319` |
| `internal/changecontrol/adapter/postgres/repository.go` | `downstreamImpactConflict:719`, `proposalRequestHashReplayMatches:755`, `buildProposalListQuery:909`, `scanAuthorization:1271`, `classifyAuthorization:1329`, `scanProposal:1354`, `decodeStrictJSON:1551`, `sameOptionalString:1579`, `isNilChangeControlDependency:1586`, `classify:1599` |
| `internal/collection/adapter/postgres/query.go` | `defaultCollectionStatementTimeout:20`, `queryExecution:171`, `configureCollectionStatementTimeout:399` |
| `internal/collection/adapter/postgres/durable_scan.go` | `planDurableScanSnapshot:44`, `validDurableScanRequest:120`, `readDurableScanPageSnapshot:128`, `verifyDurableScanBindingSnapshot:186`, `validateDurableScanRevision:230`, `loadDurableScanPlan:244`, `validDurableScanBinding:280` |
| `internal/collection/adapter/postgres/repository.go` | `Repository:50`, `collectionSelect:80`, `canonicalCollectionStatuses:346`, `collectionListRevision:362`, `collectionListHash:376`, `commandReceipt:381`, `commandResult:389`, `loadCommand:396`, `lockWorkspace:430`, `loadCollection:442`, `scanCollection:463`, `collectionColumnsForInsert:533`, `queryDefinition:538`, `viewConfig:542`, `commandReceiptJSON:559`, `validateCreateRecord:662`, `validateUpdateRecord:674`, `validateArchiveRecord:689`, `validID:717` |
| `internal/collection/adapter/postgres/errors.go` | `requestInvalid:13`, `notFound:16`, `idempotencyConflict:19`, `versionConflict:22`, `archivedImmutable:25`, `unavailable:28`, `inconsistent:31`, `classify:35` |
| `internal/documenthistory/adapter/postgres/queries.go` | `gormGetDocumentSQL:15`, `gormCommitMappingSQL:67` |
| `internal/documenthistory/adapter/postgres/model.go` | `scanDocument:25`, `scanCommitMapping:61` |
| `internal/documenthistory/adapter/postgres/repository.go` | `validateCommitMappingRequest:76`, `documentQueryError:95`, `dependencyUnavailable:102`, `invalid:106`, `classify:110` |
| `internal/events/adapter/postgres/store.go` | `eventSelect:15`, `validateReplayQuery:119`, `scanner:130`, `scanEventRaw:146`, `corruptEvent:198` |
| `internal/events/adapter/postgres/append.go` | `normalizeAppendRequest:76`, `sameAppendBinding:84`, `optionalIDValue:100`, `appendConflict:126` |
| `internal/export/adapter/postgres/repository.go` | `exportRow:22`, `exportRows:26`, `exportTransaction:33`, `Repository:79`, `newRepository:85`, `isNilDependency:358`, `invalid:397`, `unavailable:401` |
| `internal/gitsync/adapter/postgres/outbox.go` | `validateOutboxLease:168` |
| `internal/gitsync/adapter/postgres/repository.go` | `workspaceLockPurpose:60`, `scanConfig:62`, `scanRun:89`, `scanAttempt:141`, `marshalChanges:178`, `invalid:229`, `unavailable:233`, `corrupt:237`, `notFound:241`, `versionConflict:245`, `leaseLost:249`, `validID:253`, `validText:258`, `nilInterface:262` |
| `internal/gitsync/adapter/postgres/runs.go` | `nullableTime:418`, `nullableID:425` |
| `internal/health/adapter/postgres/schedule_repository.go` | `ScheduleRepository:24`, `NewScheduleRepository:34` |
| `internal/health/adapter/postgres/issue_repository.go` | `IssueDB:23`, `IssueRepository:30`, `NewIssueRepository:40`, `newSmartCollectionIssueRepository:56` |
| `internal/health/adapter/postgres/affected_change_dispatcher.go` | `AffectedChangeDispatchRepository:17`, `NewAffectedChangeDispatchRepository:26` |
| `internal/health/adapter/postgres/read_repository.go` | `ReadDB:16`, `ReadRepository:22` |
| `internal/health/adapter/postgres/detector_reader.go` | `DB:74`, `FactReader:79`, `NewFactReader:85` |
| `internal/health/adapter/postgres/scan_repository.go` | `ScanRuntimeStarter:27`, `SmartCollectionBindingVerifier:32`, `scanDB:36`, `ScanRepository:45`, `NewScanRepository:58`, `NewScanStateRepository:73`, `ScanCancellationGuard:389`, `NewScanCancellationGuard:396` |
| `internal/ingestion/adapter/postgres/repository.go` | `chunkVersions:415`, `spanKey:422`, `rowScanner:432`, `scanAttempt:434`, `scanProjection:478`, `scanSpan:502`, `scanChunk:532`, `optionalID:560`, `sameOptionalID:567`, `marshalWarnings:574`, `marshalObject:581`, `marshalStrings:588`, `utcPointer:594`, `invalid:600` |
| `internal/knowledge/adapter/postgres/relation_apply.go` | `candidateFingerprintSchemaV1:23`, `relationApprovalBinding:121`, `relationApplyLockTarget:132`, `validateRelationApprovalInput:175`, `approvedRelationProposal:536`, `validateApprovedRelationProposalBinding:610`, `validateRelationApplyBinding:745`, `relationApplyEndpoint:772`, `validateRelationApplyReplayFact:956`, `relationApplyNeedsRevision:1023`, `relationApplyBaselineError:1032`, `baselineChanged:1041`, `relationApplyNodeKey:1043`, `relationApplyLifecycleActive:1047`, `latestRelationApplyTime:1054`, `relationVersionBeforeConfirm:1064`, `validateReusableSuggestedRelation:1071`, `relationApplyInvalid:1096`, `relationApplyNotFound:1100`, `relationApplyApprovalRequired:1104`, `relationApplyConsistency:1108`, `relationApplyUnavailable:1112`, `relationApplyClassify:1116` |
| `internal/knowledge/adapter/postgres/timeline_projection.go` | `timelineProjectionColumns:14`, `timelineProjectionSource:20`, `shouldPoisonTimelineProjection:131`, `decodeTimelineCorrelation:160` |
| `internal/knowledge/adapter/postgres/timeline.go` | `timelineEventColumns:19`, `impactReportSelect:630`, `scanTimelineEvent:649`, `scanImpactReport:684`, `scanImpactReportRow:701`, `sameTimelineEvent:764`, `decodeTimelineEventExtensions:770`, `decodeImpactOwnerBinding:814`, `sameImpactReport:845`, `validateImpactReportIntegrity:849`, `dedupeImpactObjects:872`, `nullableText:894`, `timelineValidID:901`, `timelineUnavailable:914`, `impactUnavailable:918`, `timelineCorrupt:922`, `validImpactAnalysisVersion:926` |
| `internal/knowledge/adapter/postgres/conflict_commands.go` | `equivalentOpenConflict:314`, `memberClaimIDs:334`, `optionalIDValue:342` |
| `internal/knowledge/adapter/postgres/scans.go` | `scanTopic:63`, `scanClaim:80`, `scanClaimSource:107`, `claimSelect:142`, `scanRelation:176`, `scanRelationEvidence:205`, `relationSelect:254`, `storedConflict:291`, `scanConflict:297`, `conflictSelect:324`, `scanConflictMember:330`, `validateStoredConflict:398`, `storedApplicability:416` |
| `internal/knowledge/adapter/postgres/read.go` | `validateClaimStatuses:194`, `validateRelationStatuses:206`, `validateConflictStatuses:218`, `invalidQuery:230` |
| `internal/knowledge/adapter/postgres/relation_commands.go` | `groupNodeRefs:371`, `activeNodeLifecycle:400`, `equivalentSuggestedRelation:517`, `validateDistinctEvidence:524`, `matchesSuggestedEvidenceSet:535`, `sortedEvidence:559`, `confirmationValues:565`, `nullableEvidenceFingerprint:572` |
| `internal/knowledge/adapter/postgres/repository.go` | `commandReceipt:53`, `equivalentSuggestedClaim:347`, `idsAsStrings:368`, `statusStrings:376`, `timePointer:384`, `pointerString:391`, `commandCreateTopic:16`, `commandSuggestClaim:18`, `commandConfirmClaim:19`, `commandTransitionClaim:20`, `commandSuggestRelation:21`, `commandConfirmRelation:22`, `commandTransitionRelation:23`, `commandOpenConflict:24`, `commandTransitionConflict:25`, `aggregateTopic:28`, `aggregateClaim:30`, `aggregateRelation:31`, `aggregateConflict:32` |
| `internal/knowledge/adapter/postgres/errors.go` | `classify:24`, `notFound:54`, `versionConflict:58`, `idempotencyConflict:62`, `consistency:66`, `errorCodeDatabaseUnavailable:13`, `errorCodeStorageConsistency:16`, `errorCodeWorkspaceNotFound:17`, `errorCodeTopicNotFound:18`, `errorCodeClaimNotFound:19`, `errorCodeRelationNotFound:20`, `errorCodeConflictNotFound:21` |
| `internal/localmodelruntime/lifecycle_store.go` | `LifecycleStore:14`, `TxStore:31`, `TestPreparationStore:41`, `ScopedTxLifecycle:64` |
| `internal/localmodelruntime/lifecycle_postgres.go` | `runtimeColumns:751`, `runtimeSelect:752`, `holdColumns:753`, `operationColumns:754`, `scanRuntime:756`, `scanManagerLease:790`, `scanHold:804`, `scanOperation:843`, `scanPullAttempt:847`, `unionRequirements:1031`, `requirementFromRuntime:1039`, `conflict:1051`, `intervalArg:1052`, `nullableID:1053`, `nullableInt64:1059`, `nullableText:1065`, `nullableErrorCode:1071`, `marshalModelRefs:1078`, `marshalResolvedModels:1086`, `validateManagerClaim:1094`, `validateManagerHeartbeat:1100`, `validateDemandCAS:1106`, `validateOperationClaim:1115`, `validatePullAttempt:1128`, `validateOperationExpirySweep:1135`, `validateActiveRecovery:1142`, `validateActiveRecoveryCheck:1149`, `validateActivationPreparation:1156`, `validateTestPreparation:1169`, `validateTestProbeClaim:1184`, `validateTestProbeCompletion:1190`, `equalOptionalInt64:1214`, `validateOperationProgress:1220`, `validateOperationTerminal:1229`, `validateHoldAcquire:1238`, `validateHoldRenew:1250`, `validateRuntimeCommand:1256` |
| `internal/memory/adapter/postgres/repository.go` | `auditActor:450`, `sameInterviewCandidate:535`, `commandMatches:556`, `validateBinding:561`, `validateCandidateRecord:578`, `validateMutationRecord:587`, `validateScope:602`, `validateListQuery:609`, `validateEffectiveQuery:627`, `stringsFromTypes:657`, `stringsFromStatuses:669`, `jsonMemoryContent:681` |
| `internal/memory/adapter/postgres/codec.go` | `memoryColumns:28`, `scanner:34`, `scanMemory:36`, `persistedCommand:81`, `scanPersistedCommand:92`, `commandResult:129`, `encodeMemorySnapshot:160`, `optionalIDValue:190`, `optionalTimeValue:197`, `optionalPrincipalKind:204`, `optionalPrincipalID:211`, `sameMemory:218`, `validID:224` |
| `internal/memory/adapter/postgres/errors.go` | `invalid:13`, `notFound:17`, `idempotencyConflict:21`, `versionConflict:25`, `unavailable:29`, `inconsistent:33` |
| `internal/modelsettings/adapter/postgres/audit.go` | `validModelSettingsChange:122`, `modelSettingsAuditID:141`, `modelSettingsAuditActor:150`, `modelSettingsAuditResourceType:22` |
| `internal/modelsettings/adapter/postgres/runtime.go` | `defaultRuntimeTakeoverStaleAfter:13`, `runtimeAvailabilityRestoreAuthorized:210`, `authorizeRuntimeRegistration:302`, `validRuntimeRegistration:319`, `sameOptionalID:331`, `validOptionalID:338`, `nullableID:340` |
| `internal/modelsettings/adapter/postgres/revision.go` | `revisionColumns:16`, `persistedRevision:26`, `scanRevision:35`, `resolvePersisted:261`, `resolveEnvelope:280`, `resolveDraftSecret:306`, `validateSecretTargets:352`, `canonicalActor:362`, `nullString:366`, `nullBytes:373`, `foundationRevisionMissing:380` |
| `internal/modelsettings/adapter/postgres/rollout.go` | `sameRollout:281`, `activeRolloutPhase:285`, `validID:294`, `validLease:299`, `canonicalErrorCode:303` |
| `internal/modelsettings/adapter/postgres/participant.go` | `authorizeParticipantRegistration:234`, `authorizeParticipantTransition:249`, `validParticipantIdentity:278`, `nullableErrorCode:282` |
| `internal/modelsettings/adapter/postgres/activation.go` | `localPreparationLeaseDuration:20`, `sameActivation:600`, `validFreshWithin:604`, `deterministicLifecycleID:692` |
| `internal/modelsettings/adapter/postgres/snapshot.go` | `defaultSnapshotStaleAfter:14`, `runtimeReady:200`, `chatCapability:204`, `embeddingCapability:214` |
| `internal/modelsettings/adapter/postgres/repository.go` | `stateColumns:132`, `stateRecord:135`, `runtimeColumns:215`, `scanRuntime:217`, `participantColumns:246`, `scanParticipant:249`, `nilInterface:307`, `invalid:340`, `unavailable:344`, `corrupt:348`, `revisionConflict:352`, `rolloutInProgress:356`, `rolloutConflict:360`, `leaseExpired:364`, `runtimeConflict:368`, `runtimeNotPrepared:372`, `activationConflict:376`, `activationLeaseExpired:380`, `participantConflict:384`, `runtimeOwnershipLost:388`, `runtimeNotReady:392`, `enqueuePaused:396` |
| `internal/review/adapter/postgres/session.go` | `validateReviewSessionBinding:310` |
| `internal/review/adapter/postgres/invalidation.go` | `validInvalidationSummary:205`, `validateInvalidationRecord:209`, `optionalID:237` |
| `internal/review/adapter/postgres/helpers.go` | `commandReceipt:24`, `encodeJSON:32`, `validateCommandBinding:40`, `scanCard:252`, `scanDeck:329`, `scanCardAndSchedule:343`, `validHash:377` |
| `internal/review/adapter/postgres/errors.go` | `classify:13`, `invalid:46`, `notFound:47`, `conflict:48`, `persistenceInvalid:50` |
| `internal/review/interview/adapter/postgres/commands.go` | `validateStartRecord:837`, `validateSubmitRecord:855`, `validateBeginCompleteRecord:866`, `validatePrepareCompleteRecord:874`, `matchCompletionReservation:883`, `validateCompleteRecord:907`, `validatePathStepRecord:928`, `validatePathStatusRecord:935`, `decodeStartResult:942`, `decodeSubmitResult:961`, `decodeCompleteResult:979`, `decodePathStepResult:1001`, `decodePathStatusResult:1015`, `nullableID:1026`, `nullableTime:1033`, `hashBytes:1040`, `cloneQuestion:1045`, `idFromQuestion:1055`, `sameIDPointer:1063`, `validStepTransition:1070`, `validPathTransition:1084` |
| `internal/review/interview/adapter/postgres/repository.go` | `validateSelection:292`, `validateSessionListQuery:311`, `stringsFromIDs:321`, `min:334`, `advisoryKey:341` |
| `internal/review/interview/adapter/postgres/codec.go` | `completionReservationSelect:30`, `validateCompletionReservation:415`, `utcTimePointer:451`, `decodeJSON:459`, `encodeJSON:475`, `canonicalJSON:485`, `validHash:502`, `receipt:514` |
| `internal/review/interview/adapter/postgres/errors.go` | `classify:14`, `persistenceInvalid:47` |
| `internal/review/learningpath/adapter/postgres/codec.go` | `decodeReviewSnapshot:61`, `validateReservation:73`, `validateResult:379`, `decodeStrict:460`, `validHash:481` |
| `internal/workspace/adapter/postgres/runtime.go` | `authorizeRuntime:195`, `authorizeRuntimeTransition:254`, `runtimeHeartbeatFresh:350` |
| `internal/workspace/adapter/postgres/control.go` | `lockedSwitch:15`, `equalOptionalIDs:857`, `validateControlSnapshot:864` |
| `internal/workspace/adapter/postgres/registry.go` | `sameOptionalID:455` |
| `internal/workspace/adapter/postgres/control_scan.go` | `controlStateColumns:12`, `switchOperationColumns:17`, `workspaceRuntimeColumns:22`, `mutationGate:25`, `scanControlState:33`, `scanSwitchOperation:72`, `scanWorkspaceRuntime:120`, `scanMutationGate:149`, `nullableFoundationID:178` |
| `internal/workspace/adapter/postgres/rebind_audit.go` | `workspaceRootRebindAuditAction:15`, `workspaceRootRebindAuditResourceType:17`, `workspaceRootRebindReason:18` |
| `internal/workspace/adapter/postgres/repository.go` | `RootGrantResolver:29`, `workspaceColumns:71`, `nilRepositoryDependency:93`, `parseMaterialID:435`, `sameSourceVersionMetadata:617`, `scanContentArtifact:628`, `scanWorkspace:649`, `nullableText:683`, `nullableTime:690`, `scanSource:697`, `scanSourceVersion:716`, `classify:742` |
| `internal/workspace/adapter/postgres/control_errors.go` | `classifyControl:12`, `registryNotFound:52`, `switchNotFound:56`, `migrationRequired:60`, `identityConflict:64`, `rebindConflict:68`, `activeRemoveConflict:72`, `workspaceVersionConflict:76`, `switchInProgress:80`, `switchIdempotencyConflict:84`, `controlStateConflict:88`, `switchPhaseConflict:92`, `switchLeaseHeld:96`, `switchLeaseExpired:100`, `runtimeConflict:104`, `runtimeNotPrepared:108`, `runtimeMutationConflict:112`, `controlCorrupt:116`, `controlUnavailable:120` |
