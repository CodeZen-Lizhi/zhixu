# Agent GORM Final 交接（2026-09-08）

Agent child 已完成 staged 实现及精简政策要求的真实 PostgreSQL、局部 unit/vet、Go/SQL/Trellis 验证。
本 child 未切换 `cmd/**`、未改其他 owner/Schema、未删 legacy；状态与归档由主会话处理。

## 构造与能力分配

所有构造均返回 `(repository, error)`，使用同一个 `*platformpostgres.Pool`，每次检查构造错误。

| 构造 | 直接消费的 Application 能力 |
| --- | --- |
| `agentpostgres.NewGORMRepository(pool)` | `ModelRunRepository`、`RAGMemorySnapshotRepository`、`WorkspaceAnalysisRunLoader`、`WorkspaceAnalysisCapabilityLifecyclePort` |
| 同一 `GORMRepository` | `ScopedModelRunStore`（Finalizer + AttemptFinder）、`ScopedWorkspaceAnalysisRunPersistence`、`ScopedWorkspaceAnalysisReadiness` |
| 同一 `GORMRepository` | Tools 的 `ScopedWorkspaceAnalysisToolParticipant`、`ScopedWorkspaceAnalysisToolRefusalStore`、`ScopedWorkspaceAnalysisToolAuthorityReader` |
| `agentpostgres.NewGORMWorkspaceAnalysisRepository(pool, workflowFence)` | `WorkspaceAnalysisModelOperationRepository`、`WorkspaceAnalysisRetrievalPlanCheckpointReader`、`WorkspaceAnalysisCandidateAuthorityReader` |
| `agentpostgres.NewGORMRAGProgressStore(pool, scopedEvents)` | `RAGProgressRecorder`；一个 UoW 内 advisory lock + Events append |

`workflowFence` 来自 `workflowpostgres.NewGORMWorkspaceAnalysisExecutionFence(pool)`；`scopedEvents` 来自
`eventspostgres.NewGORMStore(pool)`。Agent core 与 Workspace Analysis model Repository 分别承担不同能力，
planner/synthesis/review/candidate authority 必须消费后者，Run/Memory/ModelRun 与 Tools participant 消费前者。

Conversation Run starter 使用 `NewScopedWorkspaceAnalysisRunService(agentCore, ids, config)`，再经
`NewScopedWorkspaceAnalysisCapabilityCheckedRunStarter(agentCore, starter, contract)` 包装。
最终只把包装后的 `ScopedWorkspaceAnalysisRunStarter` 交给 Conversation，保证 readiness → Run 同 scope。

Capture/Organizing 使用窄 `ScopedModelRunFinalizer`；Artifact/Conversation 需要按 Attempt 查询时使用
`ScopedModelRunStore`。所有 scoped 方法只参与 caller-owned transaction，不另开/提交/回滚事务。

## Final 生产接线位置

以下为本 child 审查时的定位，Final 编辑后以符号搜索为准：

- `cmd/api/main.go`：976、1074、1608 附近的 Agent legacy constructor。
- `cmd/worker/main.go`：2760、3077 附近的 Agent legacy constructor；2840 附近 RAG Progress constructor。
- `cmd/api/workspace_analysis.go`：96 附近 Run starter composition。
- `cmd/worker/workspace_analysis_capability.go`：63 附近 capability lifecycle composition。
- Capture/Artifact/Conversation/Organizing 的原 `ModelRunTxFinalizer` 和 Run/Readiness `*Tx(any)` consumer 由对应 owner 迁移，Final 确认全部采用 scoped Port。

Pool 生命周期由 API/Worker 主入口管理；Repository 没有独立 Close。先停止任务与所有 Pool 使用者，再关闭唯一 Pool。
Foundation 暂无 active Pool affinity 校验，不能依赖 Adapter 自动拒绝另一 Pool 的 live scope。

## Legacy 清理边界

Final 前保留以下 legacy 路径：`repository.go`、`calls.go`、`runs.go`、`recovery.go`、
`memory_snapshots.go`、`workspace_analysis_runs.go`、`workspace_analysis_capability.go`、
`workspace_analysis_model_operations.go`、`workspace_analysis_candidate_authority.go`、`rag_progress.go`，
以及 `errors.go` 的 legacy classifier。

这些文件混有 GORM 共用的 column constants、scanner、validation、binding/equality、canonical JSON/bytea codec
和错误码。删除时先分离并保留纯 helper（包括 `scans.go` 等），逐项核对引用，不能整文件机械删除。
Application 中 legacy `ModelRunTxFinalizer(any)`、Run Persistence/Starter Tx 与 Capability Readiness Tx 只有在
全部 consumer 和相关 fixture 收口后才能删除。

本轮 `gorm_core.go`、`gorm_rag_progress.go`、`gorm_workspace_analysis_model_operations.go` 已改用平台
`SQLState`，GORM 文件不再直接 import pgx/pgconn。legacy 路径仍保留原依赖。

## 验证与 Review

精确命令见 `research/static-validation.md`：

- Model Run/Call replay/CAS/UNKNOWN、caller transaction、Workspace Analysis Result/commit recovery：PASS，49.190s。
- 修改后的真实 SQLSTATE/caller context 与 RAG conflicting replay 回滚：PASS，46.164s。
- Tools participant/receipt/authority、Event 失败全 owner 回滚、refusal/Audit 原子回放：随 Tools child 的实库对照 PASS。
- Agent/Tools 全部局部 unit、vet、gofmt、定向 diff check、Trellis validate：PASS。
- Go/SQL/Trellis 复审无未解决 P0/P1/P2；本轮未改 SQL、Schema、锁序、状态机或测试。

完整 Memory/Capability/fault/并发/连接释放/EXPLAIN/全量 race/真实 Provider/browser 未在本轮执行。
生产完整可达性与 allowlist 由 Final 验证；child 实库通过不代表生产已经切换。

## 回滚

本轮错误依赖调整可单独撤销上述三个文件的 diff，不涉及数据回滚。Final 的 Composition 回滚必须恢复完整依赖链，
不允许混用 pgx transaction 与 GORM scope、双写、fallback 或拆分原子事务。不得回滚历史事实或 migration。
