# 持续演进合成笔记契约

## 1. Scope / Trigger

修改资料解析后的自动合成、`internal/organizing/**/synthesis*`、生成 Authoring 版本、笔记面试、相关 HTTP/Worker 或迁移 `00094`–`00097` 时应用。
自动合成产生可审阅候选，正式文件/Git/索引仍经真实 Approval 与 Safe Writeback；原手工 Organizing Confirm 与正式 Claim Evidence 合同保持独立。

共同约束见 [GORM/UoW](./gorm-persistence.md)、[Authoring](./authoring-contract.md)、[Organizing](./organizing-contract.md)、
[Eino StructuredRunner](./eino-structured-scheduler.md) 与 [Auth](./auth-security.md)。本契约不以确定性 Provider 测试代替真实模型语义质量或全产品发布验收。

## 2. Signatures

[Application 入口](../../../internal/organizing/application/synthesis_service.go)：

```go
func (*SynthesisService) ApplyGeneration(context.Context, SynthesisGenerationInput, SynthesisGenerationResult) (SynthesisApplyResult, error)
func (*SynthesisService) RecoverAppliedGeneration(context.Context, foundation.ID, foundation.ID) (SynthesisApplyResult, bool, error)
func (*SynthesisService) ReadPublishedSynthesisNote(context.Context, foundation.ID, foundation.ID) (domain.SynthesisNoteSnapshot, error)
```

后两个方法的 ID 顺序分别为 `workspaceID, processingID` 与 `workspaceID, noteID`。
[Authoring scoped 入口](../../../internal/authoring/application/generated.go)为 `AppendGeneratedRevisionScoped`、`RetireGeneratedPublicationScoped`、`ReservePublicationScoped`；均参与调用方的同池事务。
[面试入口](../../../internal/review/interview/application/note_preparation.go)为 `NotePreparationService.Prepare(PrepareNoteInterviewCommand)` 与 `Retry(RetryNoteInterviewCommand)`，均另接收 `context.Context` 并返回 `(NotePreparationResult, error)`。

[HTTP](../../../internal/organizing/http/synthesis_handler.go) 与 [面试 HTTP](../../../internal/review/interview/http/note_preparation.go) 共用前缀 `/api/v1/workspaces/{workspace_id}/synthesis`：

```text
GET      /notes
GET      /notes/{note_id}
GET      /notes/{note_id}/revisions
GET      /notes/{note_id}/revisions/{revision_id}
GET      /notes/{note_id}/revisions/{revision_id}/sources/{source_span_id}
GET      /processing
GET      /processing/{processing_id}
POST     /processing/{processing_id}/retry
GET|POST /notes/{note_id}/interviews
GET      /notes/{note_id}/interviews/{preparation_id}
POST     /notes/{note_id}/interviews/{preparation_id}/retry
```

- GET 要求 `READ_LOCAL`，POST 要求 `WRITE_PROPOSAL`；以 [Auth 路由表](../../../internal/auth/http/handler.go)为准。
- processing retry 必须有 `Idempotency-Key` 与 `{"expected_version":1}`（值须为正整数），返回 `202 {workspace_id, processing, replayed}`。
- 面试 create/retry 必须有 `Idempotency-Key` 及全部非 null 字段 `role, difficulty, duration_minutes, question_count, max_follow_ups`；零追问预算仍须传 `max_follow_ups:0`，返回 `202 {preparation, replayed}`。
- OpenAPI 是 [wire 事实源](../../../api/openapi/openapi.json)；Web 使用 [Synthesis](../../../web/src/api/synthesis.ts)/[Interview](../../../web/src/api/interview.ts) strict decoder、共享 transport 与 Workspace query key。Synthesis 分页默认 20、最大 100，面试 preparation 列表至多 20；Synthesis cursor 的 HMAC 绑定 Workspace、资源 kind、Note 与 limit，来源正文按需打开。
- Interview 的列表、新建、详情、答题、完成和 Path Step 更新提供显式 `/api/v2/review/...` operation。
  v1 保留 Claim 成功响应；v2 读取 Claim/NOTE 联合。两个版本的直接 Start 仍只接受 Claim，NOTE 继续由上述
  `/api/v1/workspaces/{workspace_id}/synthesis/.../interviews` preparation 创建。Path status/Memory Candidate 保留 v1。

| Schema owner | 持久事实 |
| --- | --- |
| [00094](../../../atlas/migrations/00094_generated_authoring_revisions.sql) | `authoring.generated_document`、`generated_article_revision`、`generated_publication_retirement` |
| [00095](../../../atlas/migrations/00095_synthesis_notes.sql) | `organizing.synthesis_note`、`synthesis_topic_key`、`synthesis_revision`、`synthesis_revision_source`、`synthesis_apply_receipt` |
| [00096](../../../atlas/migrations/00096_synthesis_execution.sql) | `organizing.synthesis_processing`、`synthesis_execution`、`synthesis_model_step`、`synthesis_retry_receipt` |
| [00097](../../../atlas/migrations/00097_synthesis_note_interview.sql) | `learning.note_interview_preparation` 与 Interview/Report/Path 的 `NOTE_REVISION` 来源联合 |

## 3. Contracts

### Source-ready 与持久执行

- Ingestion 在 `chunked + passed + parse_projection_id != NULL` 的原事务追加 `workflow.outbox_event`，事件为 `ingestion.source.ready`。通知只含身份/hash；失败解析不通知，合成失败不丢失原资料或改写 Ingestion 成功事实。
- API/Worker 生产使用 `NewGORMRepositoryWithSourceReady(pool, outbox)`，旧构造器不能承担自动触发。见 [producer](../../../internal/ingestion/adapter/postgres/gorm_source_ready.go) 与 [outbox](../../../internal/workflow/adapter/postgres/gorm_source_ready_outbox.go)。升级不全量补跑历史完成资料。
- [dispatcher](../../../internal/organizing/workflow/synthesis_dispatcher.go) 在同一 scope 完成 claim、服务端 provenance gate、`StartScoped` 与 publish；依据完整 Source tuple + processor version 去重。回流排除读取真实 Authoring/Source 身份，不依赖路径前缀或模型自报。
- 同 Workspace 同时最多一个 `PENDING|RUNNING` processing；忙 Workspace 暂时排除后继续领取其他 Workspace，原事件保持 unpublished，不建立第二队列。处理状态为 `PENDING|RUNNING|SUCCEEDED|NO_CHANGE|SKIPPED|FAILED|RECOVERY_REQUIRED`。
- [固定 Definition](../../../internal/organizing/workflow/synthesis_contract.go) 为 `organizing.synthesis-note@1`：`organizing.synthesis.prepare → organizing.synthesis.generate → organizing.synthesis.validate → organizing.synthesis.apply`。节点和 permissions canonical 排序，Registry 与事务 fence 使用相同 graph hash。
- Workflow input 恰有非 null 的 `processing_id, execution_no, apply_recovery`；prepare 自动重试上限 3，generate/validate 为 0，apply 为 30。每 execution 的持久预算为 30 分钟，每 processing 最多 10 次 execution；apply 重试仅恢复候选发布，不重跑模型。
- 冻结输入只存 SourceEvent、Note metadata、Revision ID/hash 与完整 Source refs；重开不可变版本后再校验。原始 excerpt 不作为输入副本进入冻结输入、队列、事件或日志。

### 增量与来源

- [SynthesisNote](../../../internal/organizing/domain/synthesis_model.go) 拥有稳定知识点、Document 和当前候选；唯一正式发布指针是 Authoring `Document.current_published_revision_id`。`SynthesisRevision` 是不可变语义投影，精确绑定 ArticleRevision、content hash 与 projection hash。
- [受限 delta](../../../internal/organizing/domain/synthesis_delta.go) 只有 `ADD_FACT|ADD_CONFLICT|ADD_GAP|ADD_SUPPORT|RESOLVE_GAP`。服务器分配稳定 item ID，保留无关旧文字、顺序、适用条件与来源；禁止任意删除、全文替换及模型决定路径/权限/版本。
- FACT 必须有原始依据；CONFLICT 并列 2–4 个带来源的备选结论；GAP 保留问题/上下文，可没有来源，解决时只能追加有依据的结论。渲染把模型文本转义，来源链接由服务器身份/hash 生成。
- `SynthesisSourceRef` 绑定 Workspace/Source/SourceVersion/ContentArtifact/ParseProjection/content hash 及 Span/excerpt hash。它不是正式 Claim Evidence；[owner reader/fence](../../../internal/organizing/adapter/owner/synthesis_source.go) 重验 tuple，历史来源 `STALE|UNAVAILABLE` 不得携带替代正文或改指当前版本。
- [输入上限](../../../internal/organizing/application/synthesis_model.go)：24 个候选、8 个输出笔记、单 excerpt 16 KiB、总 source text 256 KiB、模型输出 256 KiB。Provider 只使用本次 `N001/I001/S001` 标签；未知、重复、未载入、跨 Workspace 或漂移引用全部拒绝。

### 两阶段模型证明

- [模型适配](../../../internal/organizing/adapter/agent/synthesis_model.go)复用 RecordingChatModel 与 Eino StructuredRunner。GENERATE 使用 prompt `synthesis-delta@v1`、schema `agent.synthesis-delta@v1`；VALIDATE 使用独立 ModelRun、prompt `synthesis-semantic-review@v1`、schema `agent.synthesis-semantic-review@v1`。
- 每个 ModelRun 只按 `INITIAL → REPAIR → REDUCED` 最多调用三次，保留请求/响应/token/时间预算及严格 JSON 校验。标签可定位不证明语义支持；独立审查必须核对新增事实、冲突、补证与缺口解决的依据。
- `{"notes":[]}` 仍经独立语义审查；无变化只保存 apply/processing receipt，终态为 `NO_CHANGE`，不创建空 SynthesisRevision、ArticleRevision 或 Proposal。
- [model step](../../../internal/organizing/adapter/synthesispostgres/model_steps.go) 的 accepted 原文用 `bytea` 保真，业务结果用 JSONB；READY 必须匹配最后一次成功 ModelCall 的 response hash、byte count、固定 phase 与完整 runtime 身份。ModelRun terminal 与 step READY 同 scope 提交。
- Application fence 重验活跃 Workflow lease、独立 GENERATE/VALIDATE READY 及精确 generation hash；语义拒绝也可保存 READY 审查结果，但不授权 apply。单个 `Accepted=true` 不能构成证明。
- 锁序为 Workflow Run/Node/Attempt → processing → execution → model step → Agent ModelRun；候选 Note 锁在 fence 之后。Workspace 复合 FK、CAS、nullable shape `IS TRUE`、不可变 receipt 与禁止 TRUNCATE 的约束不能削弱。

### Receipt 与恢复

- [执行器](../../../internal/organizing/workflow/synthesis_executor.go)的 apply 在重开资料前调用 `RecoverAppliedGeneration`。已提交的 immutable apply receipt 优先于后来变化的 source/model/CAS；按原 publication key 完成真实 Authoring Publish，恢复同一 Proposal/Revision。
- 模型完成响应丢失先读取精确 READY receipt；无法证明提交结果时进入 `RECOVERY_REQUIRED`，不得再次付费调用。即便自动 retry 为 0，Workflow failure ack 丢失仍可能 lease rescue；同 Run 中已 FAILED 的模型阶段不能因此再次调用 Provider。
- [显式 retry](../../../internal/organizing/workflow/synthesis_processing.go)先查完整幂等 receipt，再锁 CAS 和可重试状态。同 scope 调用 `LookupSynthesisApplyResultScoped`，不能持锁后借第二连接查询。
- 已提交候选但发布响应丢失时，新 execution 的 `apply_recovery=true` 由服务器派生；恢复 Run 跳过输入冻结及两阶段模型。无 receipt 时才允许对已知可重试失败启动普通新 execution，未知模型结果禁止重试。
- `execution.applied_result` 必须等于 `synthesis_apply_receipt.result`；普通执行绑定同 Run，recovery 可以恢复旧 Run 的结果。processing 成功由[终态 hook](../../../internal/organizing/adapter/synthesispostgres/terminal.go)与真实 Workflow succeeded/application proof 在同一事务闭合。

### 生成 Authoring、审批与历史

- [生成版本](../../../internal/authoring/domain/generated.go)固定 `AGENT` 与 `SYNTHESIS_NOTE` origin，绑定 Note/Revision/projection hash；HTTP Working Draft 仍是 USER，不伪造手工 Confirm/Claim 或用户审批。
- 未批准候选可继续累积。旧 Proposal 仅在精确 `ready_for_review`、当前 Revision/version 一致且无 Approval/dispatch/authorization/writeback/commit 事实时，才由[受控退役入口](../../../internal/changecontrol/adapter/postgres/generated_publication.go)转为 `needs_revision`；历史保留，旧审批不能授权新候选。
- [候选事务](../../../internal/organizing/adapter/postgres/synthesis_store.go)原子完成旧 publication retirement、AGENT ArticleRevision/语义投影、候选 CAS、新 reservation 与 apply receipt；事务内任一步失败全部回滚。提交后调用既有 Authoring Publish 完成 Proposal，发布响应丢失按已提交 receipt 恢复。
- 批准/Apply 已开始时返回 busy，等待真实安全终态；不撤销既有副作用。正式 Published 必须经 exact `proposal_commit` 证明，不能因为生成成功或 Proposal 已创建便推进文件/Git/索引或 Published pointer。

### NOTE_REVISION 面试

- [Prepare](../../../internal/review/interview/application/note_preparation.go)先查幂等结果，再由 `ReadPublishedSynthesisNote` 冻结真实已发布版本；待审候选不能作为新面试来源。冻结 Note/Revision/Document/ArticleRevision、两类版本号、content/projection hash、条目和原始来源，后续笔记更新不改变既有 Session。
- 固定 Workflow `synthesis-note-interview@1`，节点 `prepare_interview` / `interview.synthesis_note.prepare`，自动重试为 0。input 仅 `{preparation_id}`，output 仅 `{schema_version, preparation_id, session_id}`。
- Preparation 状态为 `QUEUED|GENERATING|READY|FAILED|CAPABILITY_UNAVAILABLE|RECOVERY_REQUIRED`。HTTP projection 隐藏 Snapshot、模型输出、Node/Attempt/ModelRun、ModelSettingsRevision、Version、IdempotencyKey、RequestHash 与 RetryOf，不能通过 SSE/Workflow output 暴露题目计划。
- 显式 Retry 只接受 failure.retryable 的 `FAILED|CAPABILITY_UNAVAILABLE`，要求相同 Options，沿用原 Snapshot 创建新 preparation；不重新冻结最新笔记。`RECOVERY_REQUIRED` 不可付费重试。
- [来源联合](../../../internal/review/interview/domain/note.go)为 `CLAIM|NOTE_REVISION`，历史空 kind 仍按 Claim。NOTE_REVISION 必须 `ClaimID=""`、`TopicID=nil`、正式 `Evidence=[]`，且 NoteSource 非空；FACT/CONFLICT 有原始来源，GAP 可无来源。Report 与 Learning Path 保留同一 NoteSource tuple，不放宽原 Claim verifier。
- HTTP v1 的 `ClaimOnly` 约束贯通 Application 与 Store，在命令 replay、评分、Completion reservation、Artifact
  创建与 Path Step 更新之前拒绝 NOTE；约束不进入 request hash/receipt。v1 列表按不可变来源在 SQL LIMIT 前过滤，
  不在 HTTP 丢弃条目。Interview cursor 的 `version` 为 v1=1、v2=2，切换端点需从第一页读取。
- 题目计划必须覆盖快照中存在的 FACT/CONFLICT/GAP 类别。[模型完成事务](../../../internal/review/interview/adapter/postgres/note_generation.go)原子保存 ModelRun terminal、accepted 原文和 Session；响应丢失按精确 SessionID/ModelRunID/raw output 恢复，不重复 Provider 调用。
- 每题最多 3 个预生成条件追问：`LOW_COVERAGE|LOW_CORRECTNESS|LOW_BOUNDARIES`，对应分数 `<0.65` 时选取。当前 [scorer](../../../internal/review/interview/application/deterministic_scorer.go) 是 `interview-deterministic/v2`，比对冻结答案要点；不能宣称每轮实时模型追问或模型评分。

## 4. Validation & Error Matrix

| 条件 | 必须结果 |
| --- | --- |
| 客户端 JSON 非法/缺失/重复/未知字段、UUID 或请求联合非法 | 请求边界拒绝；不调用模型、不创建候选，保留 owner 的 invalid code |
| Source tuple 非法或来源已漂移 | `SYNTHESIS_SOURCE_INVALID` / `SYNTHESIS_SOURCE_STALE`；执行输入漂移可投影 `SYNTHESIS_INPUT_STALE`，不替换历史正文 |
| 模型缺失、输入越界或执行预算耗尽 | `SYNTHESIS_MODEL_CAPABILITY_UNAVAILABLE` / `SYNTHESIS_MODEL_INPUT_TOO_LARGE` / `SYNTHESIS_EXECUTION_BUDGET_EXHAUSTED`；失败可见，保留原资料 |
| 模型 JSON/标签/联合校验失败或独立语义拒绝 | 保留 StructuredRunner 原始错误；业务拒绝使用 `SYNTHESIS_MODEL_OUTPUT_INVALID` / `SYNTHESIS_SEMANTIC_REJECTED`，零候选/Proposal |
| 模型提交未知或重放不安全 | `SYNTHESIS_MODEL_FINALIZATION_UNKNOWN` / `SYNTHESIS_MODEL_REPLAY_UNSAFE`；`RECOVERY_REQUIRED`，零重复付费 |
| 伪造 Workflow input/terminal 或不安全 retry | `SYNTHESIS_EXECUTION_INVALID` / `SYNTHESIS_RETRY_UNSAFE`；不能伪造成功或重开未知结果 |
| 生成 origin/CAS 漂移或旧发布已进入审批执行 | `AUTHORING_GENERATED_ORIGIN_CONFLICT` / `AUTHORING_GENERATED_PUBLICATION_BUSY`；保留现有版本和审批事实 |
| 同 key 不同 request、跨 Workspace 或资源不匹配 | owner 幂等冲突或 not-found；不能返回其他 Workspace 的结果 |
| 面试选项、计划或 retry 配置不合法 | `INTERVIEW_NOTE_PREPARATION_INVALID` / `INTERVIEW_NOTE_PLAN_INVALID` / `INTERVIEW_NOTE_PREPARATION_CONFLICT` |
| 面试模型不可用或完成结果未知 | `INTERVIEW_NOTE_PLAN_UNAVAILABLE` / `INTERVIEW_NOTE_RECOVERY_REQUIRED`；未知结果不再调用 Provider |
| 通过 HTTP v1 读取或操作 NOTE Interview / Path Step | `409 INTERVIEW_API_VERSION_UNSUPPORTED`、不可重试；无 replay 查询、评分、reservation、Artifact 或写入 |
| Interview cursor 跨 HTTP 版本使用 | `400 INTERVIEW_CURSOR_INVALID`，不执行列表查询 |

## 5. Good / Base / Bad Cases

- Good：连续导入重复、互补和冲突资料，自动产生累计候选；稳定旧 item/来源保留，只有真实审批后正式发布。
- Base：第三篇无新知识或证据，两个独立 ModelRun 完成生成与审查，记录 NO_CHANGE；同一事件重投递不新增模型结果/版本/Proposal。
- Good：发布已提交但响应丢失，来源随后不可用且模型已禁用；显式 recovery 恢复原 Proposal/Revision，ModelRun 数不增加。
- Bad：把派生笔记当新原始来源、把有效标签当语义证明、复用旧 Approval、跳过 NO_CHANGE 审查，或把面试重试转成最新笔记的新计划。

## 6. Tests Required

按变更选择已有断言点；复用仍有效的验证结果，不为文档收口重复运行完整业务门禁。

| 稳定测试入口 | 必要断言 |
| --- | --- |
| [领域 delta](../../../internal/organizing/domain/synthesis_test.go) / [模型 wire](../../../internal/organizing/adapter/agent/synthesis_wire_test.go) | 重复/补证/冲突/GAP、未变正文与来源、strict union、未知标签、边界与 NO_CHANGE |
| [Source-ready](../../../internal/ingestion/adapter/postgres) 的 `TestGORMSourceReadyTransactions` | 原事务双写/回滚、重投递、提交响应丢失、Workspace 隔离与精确 tuple |
| [候选实库](../../../internal/organizing/adapter/postgres/synthesis_integration_test.go) / [真实发布](../../../internal/organizing/adapter/postgres/synthesis_publication_integration_test.go) | AGENT/投影原子性、先 receipt 后 fence、旧候选退役与审批竞争、真实 Git/commit proof、历史保留 |
| [Runtime 实库](../../../internal/organizing/adapter/synthesispostgres/runtime_integration_test.go) | Schema/nullable/FK、真实 River、忙 Workspace 不阻塞其他来源、语义拒绝零发布、缺模型显式 retry、未知结果零重付 |
| [恢复实库](../../../internal/organizing/adapter/synthesispostgres/runtime_recovery_integration_test.go) | 伪造 input/terminal 拒绝、Workflow failure ack 丢失不重复模型、`apply_recovery` 仍恢复同一 Proposal/Revision |
| [生产组合](../../../cmd/worker/synthesis_composition_integration_test.go) | 真实 Capture 录入两篇及重复第三篇、生产 Ingestion/Worker/Eino/River 接线、6 个 ModelCall、两版候选和 NO_CHANGE、原命令 replay |
| [面试实库](../../../internal/organizing/adapter/postgres/synthesis_note_interview_integration_test.go) / [Session](../../../internal/review/interview/application/note_session_test.go) / [projection](../../../internal/review/interview/http/note_projection_test.go) | 实际发布版本冻结、三类题目、条件追问、确定性 scorer、Report/Path 原始来源、响应丢失恢复与隐藏内部字段 |
| [HTTP](../../../internal/organizing/http/synthesis_handler_test.go) / [Worker readiness](../../../cmd/worker/synthesis_interview_components_test.go) | 严格请求、Workspace/cursor/capability、缺模型仍有真实 executor 与可见状态；OpenAPI/生成客户端及受影响 Web 门禁同步 |
| [Interview 版本](../../../internal/review/interview/http/api_version_test.go) / [来源分页实库](../../../internal/review/interview/adapter/postgres/source_constraint_integration_test.go) | v1 旧字段、v2 NOTE/Claim、拒绝前零副作用、跨版本同一幂等结果、混合记录满页与 Workspace/cursor 隔离 |

真实 River fixture 必须同时安装/验证 Atlas 与官方 River Schema；只运行历史 Atlas helper 不代表队列表已就绪。必要 Go race/vet、持久化门禁及浏览器证据各按实际覆盖记录，确定性 Provider 只能证明运行与合同。

## 7. Wrong vs Correct

| Wrong | Correct |
| --- | --- |
| `Accepted=true` 或 Source label 合法便直接 apply | 精确 GENERATE/VALIDATE 独立 READY + 活跃 lease + 同事务 owner fence |
| apply 重试先读当前资料，再寻找旧结果 | 先恢复 immutable receipt；已提交结果不因后来来源漂移失效 |
| `MaxRetries=0` 足以保证不重复付费 | 持久 step 拒绝同 Run 的 FAILED/未知重放，并测试 failure ack 丢失后的 lease rescue |
| 追加候选时复用旧 Proposal Approval 或改写已执行内容 | 仅安全退役未审批候选，新 Revision 重新审批；正在 Apply 的版本等待真实终态 |
| 从待审候选启动面试，或 retry 读取最新正文 | Prepare 证明已发布版本，Retry 保持原 Snapshot/Options，报告保留原始 tuple |
