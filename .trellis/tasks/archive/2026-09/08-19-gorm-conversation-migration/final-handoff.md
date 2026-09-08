# Conversation Final 交接

## 状态

GORM 实现与本模块 Final legacy 清理完成。13 个旧 pgx 实现文件已移除，纯 helper 保留在 `*_contract.go`，所有现有 Conversation PostgreSQL 测试入口与事务协作者均使用 GORM/scoped。`NewGORM*` 构造签名保持不变；没有旧构造 alias、双实现 selector 或 any transaction bridge。

本 owner 只修改 `internal/conversation/**` 与本任务记录；没有改 `cmd/**`、Schema、其他 owner、active pointer、commit/push/archive。主会话负责生产接线和任务最终状态。

## 接线入口

| 用途 | 入口与依赖 |
| --- | --- |
| Repository、Turn/Answer/Feedback、Execution Context、Timeline | `NewGORMRepository(sharedPool, eventsScopedAppender)` |
| RAG Question 派发 | `NewGORMQuestionDispatcher(sharedPool, workflowScopedRuntimeStarter, eventsScopedAppender, ids, clock)` |
| Workspace Analysis 派发 | `NewGORMQuestionDispatcherWithWorkspaceAnalysis(...)` 或 `NewGORMQuestionDispatcherWithWorkspaceAnalysisAndAudit(..., scopedAnalysisStarter, scopedAuditRecorder)` |
| RAG Answer 终结 | `NewGORMAnswerFinalizer(sharedPool, agentScopedModelRunStore, eventsScopedAppender, clock)` |
| Draft Stream 与 candidate draft loader | `NewGORMDraftStreamRepository(sharedPool)` |
| Workspace Analysis 终结 | `NewGORMWorkspaceAnalysisFinalizer(sharedPool, eventsScopedAppender, ids)` 或 `NewGORMWorkspaceAnalysisFinalizerWithAudit(..., scopedAuditRecorder, safeWorkerActorRef)` |
| Workflow terminal hook | `NewGORMWorkspaceAnalysisCancellationTerminalHook(eventsScopedAppender, ids)` 或 `NewGORMWorkspaceAnalysisCancellationTerminalHookWithAudit(..., scopedAuditRecorder, safeWorkerActorRef)`；实现 `OnWorkflowNodeTerminalScoped` |
| Workflow control audit hook | `NewGORMWorkspaceAnalysisCancellationAuditHook(scopedAuditRecorder)`；实现 `OnWorkflowControlScoped` |

事件使用 Events GORM Store 的 `AppendScoped`，审计使用 Audit Recorder 的 `RecordScoped`。Workflow Runtime 的 hooks 配置需选 scoped 类型。所有 `sharedPool` 必须是同一平台 Pool；不另开 GORM/pgx pool。

Workspace Analysis 模型操作是 Agent 的独立 GORM Repository：`NewGORMWorkspaceAnalysisRepository(sharedPool, workflowExecutionFence)`，区别于普通 `NewGORMRepository` 的 ModelRun Store。Run starter 使用 `NewScopedWorkspaceAnalysisRunService`，生产仍需 `NewScopedWorkspaceAnalysisCapabilityCheckedRunStarter` 的 readiness 包装，不能绕过 capability gate。

## 变更文件

- `gorm_core.go`、`gorm_models.go`、`gorm_repository.go`、`gorm_turns.go`、`gorm_feedback.go`：同池边界、模型和常规读写。
- `gorm_dispatch.go`、`gorm_finalizer.go`、`gorm_draft_stream.go`：Question/River、ModelRun/Draft/Answer 事务和草稿。
- `gorm_workspace_analysis_{audit,finalizer,terminal_hook,control_audit_hook,timeline}.go`：分析 proof、终态/审计及一致快照投影。
- 对应 13 个 `*_contract.go`、`errors.go`：保留原领域校验/编码/错误行为，清除 pgx 类型。
- 现有 `*_test.go`：原位适配 scoped ports、共享 testdb pool、提交应答丢失、计数器、lease 与读锁 fixture；未新增测试文件。

## 验证与回滚

完整命令、结果与 Go/SQL 自检见 `research/validation-2026-09-08.md`。核心提交/回滚/精确重放门禁通过；Final 清理后 unit/vet、读取实库批次（43.433s）、Draft/lease/取消实库批次（38.510s）通过。production Conversation 无非 allowlist pgx 残留。

本 owner 初次交接未执行 Workspace Analysis 完整 `FinalizeSuccess` 或 Timeline Adapter 实库场景。Final 主会话随后核对并补齐跨 owner 证据：Worker 的 `TestPublicConversationRunsThroughRiverWorkspaceAnalysis` 使用真实 GORM Finalizer，成功完成六节点、正文发布及精确重放（11.341s）；`TestWorkspaceAnalysisTimelineProjectsAuthoritativeSafeSnapshot` 通过（14.072s）。因此这两项不再是 Final 的主路径盲区；完整故障矩阵仍未执行。详见 Final 的 `worker-tests-final.md` 与 `api-validation.md`。

Schema 无变化。若需回滚，必须以 Conversation Adapter 与相应 API/Worker scoped 接线的版本边界一起 revert，并与跨 owner Final 契约协调；不要单独复活旧 any ports、混用旧构造，或修改 Atlas migration。
