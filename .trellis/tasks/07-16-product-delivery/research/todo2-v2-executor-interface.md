# TODO2 v2 Runtime / Workflow 接口

实施 owner：dynamic_runtime。工具 catalog、工具 adapters/application/domain/postgres、Agent scoped tool participant 全部由 dynamic_tools 维护。

## 已落接口

- `eino.NewWorkspaceAnalysisLoopRuntime(model einomodel.ToolCallingChatModel, metrics observability.Metrics)` 返回 `application.WorkspaceAnalysisLoopRuntime`。每次运行建立无 checkpoint 的真实 ADK loop，模型请求及工具结果都通过项目端口授权／重放。
- `application.NewWorkspaceAnalysisDecisionRunner(WorkspaceAnalysisDecisionRunnerDependencies{Catalog, Repository, IDs, Clock})`；`Repository` 是 foundation 的 `WorkspaceAnalysisDecisionRepository`。
- `runner.BindWorkspaceAnalysisLoop(WorkspaceAnalysisDecisionRunRequest{Identity, AnalysisRunID, ModelSettingsRevision, Retrieval, ProfileRef, PromptRef})` 返回每个 decision 调用的持久化授权端口。Decision 阶段不预取最新 Search index，Retrieval 留空；实际 Search receipt 绑定历史索引。
- 已有 Synthesis / Review runner 构造器沿用，将按 `Identity.DefinitionVersion` 选择严格独立的 v2 合同。

## Workflow 接线合同（已落实现）

提供 `agentworkflow.NewWorkspaceAnalysisV2Executors(agentworkflow.WorkspaceAnalysisV2ExecutorDependencies{...})`，返回 `WorkspaceAnalysisV2Executors`，字段 `DecideNext / SynthesizeAnswer / ValidateCitations / ReviewPublish` 均实现 `workflowapplication.Executor`。

依赖字段冻结为：

| 字段 | 端口 / 来源 |
| --- | --- |
| Context | `conversationapplication.QuestionExecutionContextLoader` |
| Runs | `agentapplication.WorkspaceAnalysisRunLoader` |
| Inputs | `GetRunInput(ctx, workspaceID, workflowRunID)`，现有 Workflow repository |
| Stages | `GetSucceededNodeOutput(ctx, workspaceID, workflowRunID, nodeKey)`，同上 |
| Journal | `LoadWorkspaceAnalysisJournal(ctx, agentapplication.WorkspaceAnalysisJournalQuery)`，Agent repository |
| Catalog | `*agentapplication.RuntimeCatalog`，含新版 prompt/schema |
| Decisions | 上述 `*agentapplication.WorkspaceAnalysisDecisionRunner`（Bind 端口） |
| Runtime | 上述 `agentapplication.WorkspaceAnalysisLoopRuntime` |
| Tools | 现有 `ExecuteWorkspaceAnalysisTool`，含四个 exact v2 tuple |
| ToolOutputs | `toolsapplication.WorkspaceAnalysisDynamicToolAuthorityReader`，Tool repository |
| Evidence | `toolsapplication.WorkspaceAnalysisSynthesisEvidenceAuthorityReader`，Tool repository |
| Candidates | `agentapplication.WorkspaceAnalysisCandidateAuthorityReader`，Agent repository |
| Validation | `toolsapplication.ValidateCitationV4PublicationAuthorityReader`，Tool repository |
| Synthesis | 现有 Synthesis runner 的 `Run` 端口 |
| Review | 现有 Review runner 的 `Run` 端口 |
| Finalizer | `conversationapplication.WorkspaceAnalysisFinalizer`，已加 metrics wrapper 亦可 |
| Clock | `foundation.Clock` |

注册使用 `RegisteredWorkspaceAnalysisDefinitionV2()` 的四个 node kind / input schema version。旧六个 executor 及 schema/hash 原样保留。

新版 Agent runtime catalog 由本 owner 在 `internal/agent/adapter/workflow/catalog*.go` 注册，与 Tool catalog 无关：`WorkspaceAnalysisDecisionPromptRef()`、`WorkspaceAnalysisSynthesisPromptRefV2()`、decision schema@1、candidate schema@2。

## 闭包与终态

- Loop输出只证明持久 finish decision，不能发布成功 Answer。后续节点从 exact journal、candidate、Tool receipts 重建输入。
- Search 模型仅传 query；服务器冻结 mode，并使用 `WorkspaceAnalysisDynamicSearchLimit` 得到1–5的请求limit。Read/Citation引用为Run-global E1–E32。
- Loop内Citation candidate identity/hash均为null；最终独立Citation绑定candidate ID/hash/完整refs，不能复用loop校验。
- Finalizer lookup 的 `DefinitionVersion=2`。Git未调用时成功命令的 GitReceiptID/Hash均为空；调用过则指向latest成功Git receipt。
- 无可回答Read时，EvidenceInsufficient以finish decision的OperationID及`DECISION_RECEIPT` id/hash证明；不制造空Search。
- 第13个decision及真实budget/deadline拒绝由持久层提交PENDING operation/journal后返回typed AdmissionDenial；executor按其actual OperationID/Requested收口，不能调用Provider。

## 验证状态

2026-09-09：受影响的七个 Go 包完整 race 测试及 vet 通过：Agent application、Eino adapter、Workflow adapter、Conversation workflow、Platform observability、observability facade、rag-model-fixture。

- 真 HTTP + production Eino ADK fixture 覆盖 Git→Search→Read→Citation→finish 和两轮 Search/Read 分支；第二轮真实返回的 E17 控制下一步模型决定。
- Application runner 覆盖 v2 synthesis/review、全局引用和候选/draft 重放、同 Attempt ModelRun 复用、replacement CallNo 重置、提交确认丢失、PENDING denial ID 重用及不可用 usage 的 UNKNOWN 收口。
- 四个 Executor 的完整 Execute 测试覆盖 publication 重放、finish 闭包、nullable/latest Git、多 Search 的实际历史 index、空/全截断证据拒答、精确前序/receipt/Model/DecisionID 绑定、最终 Citation 的 candidate ID/hash/完整refs、独立 Review 拒绝、真实 typed budget proof 传递和未知模型结果不推进。
- v1 fixture 和协议保留；v1/v2 candidate schema 混用拒绝；候选流 barrier 共用稳定 stage，模型 v2 决策日志使用 `workspace_analysis_decision`。
- Outcome metric 增加固定 `workspace-analysis-v2`，旧构造函数继续产生 v1；版本化 helper 拒绝其他版本，Finalizer 只在 fresh commit 记录对应版本。

这是 runtime/protocol/Executor 单元与 adapter 合同验证。PostgreSQL/River 的完整恢复闭包、Compose/API/浏览器流程由主会话和持久化 owner 集成验收；M11 全产品测试/Eval/发布包不在本次验证范围。详情见 `todo2-v2-runtime-verification.md`。
