# TODO2 v2 持久接口

实施 owner：dynamic_foundation；本文件记录已落代码的接口，不作为完成或测试证据。

## 冻结合同

- `domain/workspace_analysis_v2_policy.go` 是唯一预算来源：12 个决策、14 个模型调用、13 个工具调用、8 次来源读取、32 个 Run-global Evidence 引用、工具并发 1。每模型输入预留 65536 Token；决策输出 512、候选 4096、独立复核 1024。候选上限可以按 Profile 缩小，其他上限不能由请求扩大。
- `RegisteredWorkspaceAnalysisDefinitionV2()`：`decide_next → synthesize_answer → validate_citations → review_publish`，保留最小 input schema 1，node output 2。Kind 前缀 `agent.workspace-analysis.v2.`。v1 原始 Graph Hash 不变。
- 工具 exact tuples：ReadGitStatus@3、SearchKnowledge@3、ReadSource@4、ValidateCitation@4；旧 tuple 继续只服务 v1。

## 调用端口

`domain.WorkspaceAnalysisDecision{Action string, Query *string, EvidenceRef *string, EvidenceRefs []string}` 仅允许 `git_status|knowledge_search|source_read|citation_validation|finish`。四个 JSON 字段都必须存在，不接受重复键或未知键：Git/finish 的三个参数均为 null；Search 只接受去首尾空白后 1–1024 字节的 Query；Read 只接受 E1–E32 的 EvidenceRef；Citation 只接受 1–8 个不重复且已读取的 EvidenceRefs。未使用的字段必须 null。它不保存 reasoning、Provider ToolCall ID 或可信身份。`Canonical` 与 `DecodeWorkspaceAnalysisDecision` 给出 exact 私有决策文档。

`application.WorkspaceAnalysisDecisionRepository` 继承现有 `WorkspaceAnalysisModelOperationRepository`，由 `*postgres.GORMWorkspaceAnalysisRepository` 实现，继续调用 `AuthorizeWorkspaceAnalysisModelCall`；DECISION 只能属于 v2 的 decide_next。每个 Attempt 的一个 ModelRun 可以包含多个 AGENT ModelCall。候选使用 v2 schema，独立 Review 复用已有 schema，均通过各自模型节点执行；v1 保留原版本分支。

新增：

```text
LoadWorkspaceAnalysisJournal(ctx, {WorkspaceID, AnalysisRunID, WorkflowRunID})
  -> {Run, Entries[{Sequence, Operation, DecisionID?, ModelRun?, ModelCall?, Decision?}]}

FinalizeWorkspaceAnalysisDecision(ctx, {
  Identity, OperationKey, OperationID, ExpectedCallVersion, ReceiptID,
  Status, Decision?, Usage, LatencyMillis, ErrorCode
}) -> {Run, Call, Operation, Decision?, Replayed}
```

授权返回 CREATED 才能调用 Provider；RECONCILE 不允许再调用，REUSE 读取 exact receipt。UNKNOWN 通过同一 Finalize 结算完整预留，不带输出与 usage。持久 commit 不可证明时不把结果交给下一个步骤。

`WorkspaceAnalysisAdmissionDenial` 只在真实 PENDING 操作已经提交、尚未分配 Call/Reservation 时返回。预算或截止时间拒绝重放时必须复用该 OperationID。每次决策都为候选和独立 Review 保留两个模型调用及对应输入、输出 Token 额度。

## 序列与存储

- `agent.workspace_analysis_journal(workspace_id, analysis_run_id, sequence, operation_id, decision_id, created_at)`；PK `(analysis_run_id,sequence)`，operation_id 唯一。每个 PENDING Operation 原子追加一行；公开 Timeline 直接按 sequence，不能按 phase 排序。decision_id 只在被决策选择的 loop Tool 行非空，指向同 Run 上一个决策 receipt。
- `agent.workspace_analysis_decision(id,workspace_id,analysis_run_id,operation_id,node_attempt_id,model_run_id,model_call_id,ordinal,document,document_hash,document_bytes,created_at)`：每个 ModelCall 一个不可变 canonical 结果，复用同一个 ModelRun，不放宽旧 model_result 的唯一约束。
- `WorkspaceAnalysisOperationDecision = DECISION`，`WorkspaceAnalysisOperationResultDecision = DECISION_RECEIPT`。decide_next 的 Operation Ordinal 为决策序号：决策与其选择的 Tool 共用序号。第 13 个 DECISION 仅可保留为预算拒绝的 PENDING 操作，不得获得 ModelCall。候选、Citation、Review 的 Ordinal 继续为 1。
- Run 沿用原表及结构，以 definition_version=2、policy_version=2 选择严格分支。v1 不接受 decide_next 或新的结果文档。
- Run-global Evidence 映射在 Search 成功事务内追加，绑定 exact Search receipt/local ref；已分配 E<n> 永不改绑，来源正文仍只在受控 Tool receipt。
- 决策序号是 Run-global，ModelCall.CallNo 是 Attempt-local。替换 Attempt 可以关闭全部调用均已成功结算的旧 ModelRun 前缀，新 ModelRun 从 CallNo 1 接续下一个全局决策；未完成调用必须按 UNKNOWN 结算，不能按成功 checkpoint 关闭。
- loop 内 Citation 可选；最终候选仍须执行独立、绑定 candidate ID/hash 的 Citation 校验，不以 loop 内已校验替代发布门禁。

## 所有权

foundation：Agent domain、Model journal/授权/预算、Run start、候选持久化与 00098 整合。
dynamic_runtime：Eino/Workflow executor、DecisionRunner、application synthesis/review runner v2 适配、Workflow catalog 与测试 Provider。
dynamic_tools：Tools domain/application/postgres/具体 executor、Agent 同 scope Tool participant/authority/records/refusals、00099。
dynamic_contract：Conversation 公共 DTO/validator/Timeline 读取；Finalizer、终态 hook、成功与终止 proof、Run/Answer/deadline SQL。SQL 片段由 foundation 检查并纳入 00098。
主会话：Dispatcher、readiness、main、checksum/schema 与部署；公共 UI owner 独占 OpenAPI/生成目录。
