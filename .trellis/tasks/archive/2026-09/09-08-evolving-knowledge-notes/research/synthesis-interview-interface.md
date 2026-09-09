# W4 笔记面试接口（实现 owner：synthesis_interview）

## HTTP 与服务接线

本代理负责 `internal/review/interview/http` 所有新/旧投影；公共契约代理负责 OpenAPI/Web。准备入口由独立 `interviewhttp.NewNotePreparationHandler(service)` 注册，复用现有身份、strict JSON 和 Problem Details；主会话接 composition。

- `POST /api/v1/workspaces/{workspace_id}/synthesis/notes/{note_id}/interviews`，WRITE_PROPOSAL、Idempotency-Key；202（首次与进行中 replay），准备 READY 时仍返回同一 preparation 以及 session_id。
- `GET /api/v1/workspaces/{workspace_id}/synthesis/notes/{note_id}/interviews/{preparation_id}`，READ_LOCAL；200，刷新/有界轮询恢复。
- `GET /api/v1/workspaces/{workspace_id}/synthesis/notes/{note_id}/interviews`，READ_LOCAL；200，固定最近 20 项 `{items: NotePreparation[]}`，开始时间倒序，无 body/query；找回首次响应丢失的任务。
- `POST /api/v1/workspaces/{workspace_id}/synthesis/notes/{note_id}/interviews/{preparation_id}/retry`，WRITE_PROPOSAL、Idempotency-Key；仅已知失败/capability unavailable 能进入新准备任务；UNKNOWN 禁止自动重复付费调用。返回新 preparation。

POST / retry body 无 scope/schema/题目/答案/可信 hash：

```json
{"role":"后端工程师","difficulty":"INTERMEDIATE","duration_minutes":30,"question_count":6,"max_follow_ups":3}
```

role 1–256 UTF-8 bytes；difficulty FOUNDATION|INTERMEDIATE|ADVANCED；duration_minutes 1–240；question_count 1–20 且必须能够覆盖笔记现有 FACT/CONFLICT/GAP 种类数；max_follow_ups 0–20。全部字段必填，无未知/重复字段。retry 的 body 同 POST；必须与原 preparation 的配置相同，后端精确冻结原 snapshot。

response：

```json
{"preparation":{"id":"uuid","workspace_id":"uuid","note_revision":{},"options":{"role":"后端工程师","difficulty":"INTERMEDIATE","duration_minutes":30,"question_count":6,"max_follow_ups":3},"status":"QUEUED","workflow_run_id":"uuid","session_id":null,"failure":null,"created_at":"RFC3339Nano","updated_at":"RFC3339Nano"},"replayed":false}
```

`status`: QUEUED|GENERATING|READY|FAILED|CAPABILITY_UNAVAILABLE|RECOVERY_REQUIRED；`session_id` 必需 nullable，只有 READY 非空；failure 必需 nullable，失败时 `{code:string,retryable:boolean}`。`note_revision` 复用以下精确 Ref。响应不返回 snapshot.Items、答案要点或条件追问计划。

preparation 另含必需 `options: NoteInterviewOptions`，精确保存创建时五字段，供刷新后的受控 retry 复用。

应用入口：`NewNotePreparationService(NotePreparationDependencies{Store, Snapshots, IDs, Clock})`；`Prepare(ctx, PrepareNoteInterviewCommand{WorkspaceID,NoteID,Options,IdempotencyKey})`、`Get(ctx,workspaceID,noteID,preparationID)`、`Retry(ctx,RetryNoteInterviewCommand{WorkspaceID,NoteID,PreparationID,Options,IdempotencyKey})`。Store 持有同池 scoped Workflow starter；snapshot reader 使用 SynthesisService.ReadPublishedSynthesisNote。运行 Definition 和 Executor 由 `internal/review/interview/workflow` 提供，模型在 `adapter/agent`。

## 固定运行时接口

```go
interviewpostgres.NewGORMNotePreparationRepository(pool, interviewpostgres.NotePreparationDependencies{
    IDs, Clock, ModelRuns, Fence, Bindings,
})
repository.BindWorkflowStarter(workflowapp.ScopedRuntimeStarter) error
// 同一 repository 实现 workflowapp.ScopedWorkflowTerminalHook。

interviewworkflow.NotePreparationDefinition() (workflowdomain.RegisteredDefinition, error)
interviewworkflow.RegisterNotePreparationDefinition(*workflowapp.DefinitionRegistry) error
interviewworkflow.NewNotePreparationExecutor(interviewworkflow.NotePreparationExecutorDependencies{
    Runs, Store, Model, Clock,
})

interviewagent.RegisterNoteInterviewRuntimeCatalog(*agentapp.RuntimeCatalog) error
interviewagent.NewNoteInterviewModel(interviewagent.NoteInterviewModelDependencies{
    Model, Scheduler, Catalog, ModelRuns, Store, ProfileRef, IDs, Clock, Budget,
})
interviewagent.NewUnavailableNoteInterviewModel()
```

- 所有持久参与方使用同一个 Pool。`ModelRuns` 是 Agent scoped ModelRun store；`Fence` 是 Workflow scoped ToolExecutionPolicySnapshot；`Bindings` 是 Workflow scoped RuntimeBindingReader。
- Definition key `synthesis-note-interview`、version `1`，唯一节点 `prepare_interview` / `interview.synthesis_note.prepare`；`RetryPolicy{}` 禁止自动重复付费调用。Executor registry 先 Freeze，再 Freeze definition registry。
- Workflow input 只有 `preparation_id`；output 只有 `schema_version`、`preparation_id`、`session_id`。题目、答案、完整模型输出均不进入 Workflow output。
- Prompt `interview.synthesis-note-plan` version `"1"`；Schema `agent.synthesis-note-interview-plan` version `"1"`。使用真实 Eino StructuredPhaseScheduler、RecordingChatModel 与有界 INITIAL/REPAIR/REDUCED 调用链。
- `READY`、唯一 Session、初始 Questions、Start receipt、ModelRun SUCCEEDED 在同一事务提交；提交响应丢失只按完整绑定读回，不再调用 Provider。已记录调用却缺失可靠结果时进入 `RECOVERY_REQUIRED`，禁止重新付费。
- Interview 的 Artifact CommandService 必须同时接既有正式 Citation verifier 和 `artifactauthoring.NewVerifier` 的 Documents verifier；NOTE 报告使用 ArticleRevision DocumentSource。

## 既有 Interview 投影扩展

旧 Claim/Topic 请求与评分不变；普通 `/interviews` Start 不允许传 note_revision 绕过后台准备。

`config.scope` 新增可选 `note_revision: NoteRevisionRef`，与 claim_ids/topic_ids 互斥。

`NoteRevisionRef` 全部必填：workspace_id、note_id、revision_id、document_id、article_revision_id（UUID）；revision_no、article_revision_no（positive int）；content_hash、projection_hash（64 lowercase hex）；title（1–512 bytes）。

`Question` 新增 `source_kind: CLAIM|NOTE_REVISION`（服务端新响应总是提供）。claim_id 在 NOTE_REVISION 时为 null，topic_id 不提供。新 `note_item` 可选：`{revision:NoteRevisionRef,item_id:uuid,item_kind:FACT|CONFLICT|GAP}`。此字段只描述来源身份；**Question 响应绝不暴露 answer_points、follow_up_plan 或原始 source refs**。

`Score`、`Finding`、`PathStep` 新增可选 `note_source: NoteQuestionSource`；Finding/PathStep 提供 source_kind；NOTE_REVISION 的 Claim/Evidence 分支为空（claim_id null，evidence []，PathStep source_version_id/source_span_id/evidence_hash 均 null）。

`NoteQuestionSource`：`{revision:NoteRevisionRef,item_id:uuid,item_kind:FACT|CONFLICT|GAP,sources:SynthesisSourceRef[]}`。sources 使用现有 synthesis 的原始 tuple，GAP 可为 []。每个 ref 用笔记 owner 的 `OpenSource(workspace,note,revision,ref)` / `/synthesis/...` 来源 API 按需打开；不可把它转成 formal Citation。

Report 新增可选 `note_sources: NoteQuestionSource[]`（1–20，只有 note interview），按基础题目的 item ID 去重、保留首次出现顺序；Finding、Score、Step 来源精确复制冻结题目。冲突的不同条件可以引用同一个原始 span，来源数组保留该重复关系，展示层可去重。Report 计数与分数沿用原实现：每个 question_no 链取最后一题，不把连续追问计作额外基础题。

Turn.scorer_version 继续 `interview-deterministic/v2`，UI 必须称为确定性规则评分，模型只负责出题及条件追问计划。未解决 GAP 接受明确的“不足以判断／需要补充资料”，固定词规则不代表模型评分。

来源 UI 可跳 `/authoring/notes/{note_id}?revision_id={revision_id}`，传原始 tuple 按需打开；后续笔记更新不改已开始面试，原始来源失效显示 owner 的 STALE/UNAVAILABLE。

## 错误

既有 400/404/409/503 Problem Details 复用；新增稳定 code：INTERVIEW_NOTE_PREPARATION_INVALID、INTERVIEW_NOTE_PREPARATION_NOT_FOUND、INTERVIEW_NOTE_PREPARATION_CONFLICT、INTERVIEW_NOTE_PLAN_INVALID、INTERVIEW_NOTE_PLAN_UNAVAILABLE、INTERVIEW_NOTE_RECOVERY_REQUIRED。模型底层预算/Schema/持久化 code 原样保留为 failure.code。

## SQL

只写 `00097_synthesis_note_interview.sql`，扩展严格 NOTE_REVISION 分支及 preparation 持久事实；不改旧 v1 hash、不删历史数据、不放宽 Claim Citation。已与 synthesis_model 协调共享 Agent result/schema 常量和 no-index 精确 allowlist。

NOTE 的报告与路径采用现有 `artifact-revision/v2` 文档来源，数据库 binding guard 要求唯一来源与 preparation 冻结的 ArticleRevision 完全一致，且没有混入 Citation；Claim/Review 保留原 `artifact-revision/v1` 限制。此扩展在实库组合与 race 中验证，具体证据见 `synthesis-interview-verification.md`。
