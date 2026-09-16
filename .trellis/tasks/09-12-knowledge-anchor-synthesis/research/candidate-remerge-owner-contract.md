# Candidate remerge owner contract

Go owner 和125已实现；此切片不改 HTTP/cmd/API/Web，main 负责接线和 atlas/schema.sql 导出。

## Public Go port

`(*postgres.SynthesisManuscriptRuntime).CandidateRemerge(caller application.SynthesisManuscriptCaller) (application.SynthesisCandidateRemergeOwner, error)`。
每个 HTTP 调用都用真实认证 capabilities 新建 caller；零值和只有 READ_LOCAL 都拒绝。全部调用还验证实际 Root，context 布尔和客户端 hash 都不是授权。

DTO/port：`internal/organizing/application/synthesis_candidate_remerge.go`。

- `Target(ctx, workspaceID, noteID foundation.ID) (SynthesisCandidateRemergeTarget, error)`
- `Begin(ctx, BeginSynthesisCandidateRemerge) (SynthesisCandidateRemergeReview, error)`
- `Read(ctx, ReadSynthesisCandidateRemerge) (SynthesisCandidateRemergeReview, error)`
- `Apply(ctx, ApplySynthesisCandidateRemerge) (SynthesisCandidateRemergeReview, error)`

Target 对应 GET `.../notes/:note_id/candidate-remerge/target`。独立 DTO `SynthesisCandidateRemergeTarget` 返回与 Begin 完全相同的十个身份/版本字段，**没有 idempotency_key**；不返回 hash、全文、receipt。逐调用认证实际 caller/Root，读取并锁定当前 note/Document/Proposal，复用 Begin 的 current R1/P1 可退役校验和原 processing SUCCEEDED/capture 校验，并检查实际文件可读。无 attempt/capture 写入、不 reconcile P1。UI 将这些字段绑定 Begin 并自建 key；Begin 再次 CAS，不把 Target 视为授权凭证。

Begin JSON：`workspace_id, note_id, expected_revision_id, expected_note_version, expected_document_id, expected_document_version, expected_publication_id, expected_proposal_id, expected_proposal_revision_id, expected_proposal_version, idempotency_key`。ID 均为 UUID、版本为正整数；key 非空、trim且≤200 bytes。只接收 expected 身份；当前 published P/Root/F1/模型来源由 owner 读取并冻结。expected Document/Proposal version 使用各自现有 owner 的读取结果，不以 note version 替代。

Read JSON：`workspace_id, note_id, idempotency_key`（Begin key）。无需先知道 attempt_id，Begin 丢响应可直接 GET 恢复。

Apply JSON：`workspace_id, note_id, attempt_id, idempotency_key, resolution?`。Apply 使用独立 key。resolution 复用 `SynthesisManuscriptResolution`：`stage, preview_fingerprint, acknowledged_ordinals, final_content`。有冲突必须精确 fingerprint、按顺序确认每个 ordinal，并提交用户完整最终正文；缺块、错序、伪指纹拒绝。Clean 不允许 resolution。

返回：`workspace_id, note_id, attempt_id, state, candidate?, preview_fingerprint, review?, result?, replayed`。

- state=`READY`：clean，`Candidate string` / JSON `candidate` 为本次 Git merge + Mapper 实际得到的完整 `Preview.Manuscript.FullContent`（含全部人工内容），不是 R1 或 machine-only。Begin、GET、同键 Begin replay 均返回同一全文。仍需显式 Apply，只生成新候选。CONFLICTS 使用 review.candidate，APPLIED 不返回 candidate。
- state=`CONFLICTS`：review 复用 `SynthesisManuscriptMergeReview`（Base/Current/Proposed/Candidate/Conflicts）；wire 请复用原人工工作台投影。内部 `RevisionMergeConflict` 无 JSON tags，Ordinal 为 Go 大写字段且字节段默认 base64，HTTP 应按既有 manuscript wire 映射。
- state=`APPLIED`：result=`revision_id, article_revision_id, publication_id?, proposal_id?, proposal_revision_id?`。不返回 capture/机器/来源/模型日志/权威 envelope。

Apply 原子提交 R2/A2/reservation 后，由原 Authoring owner 建立 P2；不写文件、不审批、不发布。若 P2 调用响应未知，GET 返回已落成 R2/A2 和已有 P2；若创建尚未完成，P2 字段可缺省，重发**同一 Apply 命令/key**恢复原 reservation。不得重新 Begin 来恢复已提交 Apply。

## 算法与持久化

Base=R1 receipt 的实际 capture F0，Current=Root 实际读取 F1，Proposed=R1.Manuscript.FullContent。使用现有 GitMerge 和 Mapper，无模型/新来源。保留原 machine、历史 ineligible 及此前 review items 的不可信集合；人工正文不能新增可信知识项。后续对 R2 再次 remerge 时，Base 为 R2 自己的 remerge capture，不回退到旧 F0。

仅新增一张 append-only `organizing.synthesis_candidate_remerge_event` 表（BEGIN/APPLY）；APPLY 同时保存单阶段裁决和原子候选结果。复用 immutable manuscript capture 表，不复用已结束 HumanTask，不新 workflow，不伪造原 processing apply receipt。125 增加严格新闭包；原119两个函数原分支逐字节保留。R2 的 manuscript_receipt_id 是原 receipt 来源，必须同时具有 remerge application identity；publication baseline view 用原 receipt + 新 capture + 当前 P + R2/A2 exact proof，复用原 reservation/writeback guards。

## 生命周期与实测边界

Begin/Apply 都 CAS note/current R1、Document/latest Article、P1/current Proposal revision/version、当前 published P、Root/F1。先授权和 workspace/note 权威定位，再重放；已落成结果可在原候选不再 current 后恢复。

旧 P1 在 F1 上尝试批准会返回 `TARGET_BASE_HASH_CONFLICT`，并由原 Change Control owner 置为 `needs_revision`、version+1，但不创建 Approval。Synthesis GetNote 的原 Authoring reconciliation 还会关闭其 binding（`AUTHORING_PUBLICATION_PROPOSAL_NEEDS_REVISION`）。remerge 允许这种**未审核且无任何 workflow/dispatch/authorization/writeback/commit**的 exact needs_revision/closed binding；任何已有审核或副作用都拒绝。

为复用这一合法失败状态，必要下游 `internal/changecontrol/adapter/postgres/generated_publication.go` 仅扩展 untouched needs_revision 的 retire 检查；不会再次制造 needs_revision 状态迁移。Authoring 仅接受上述 exact CLOSED error；125 在 exact BEGIN/APPLY 证明下允许 retirement receipt 使用未再增加的 proposal version，原94所有身份/副作用断言和 ready→needs_revision 的 version+1 分支均保留。原94文件未改。

旧审批与 Apply 并发若改变 proposal version，旧预览 Apply 返回 stale；在**确认未应用**后刷新 expected identities，用新 Begin key 重建预览。P1 只在 R2/A2/reservation 同事务落成时由此路径退役；已被原 reconciliation 关闭的 P1 不重复改写。

## History / main 接线

- `domain.SynthesisRevision.Remerge` 含 `attempt_id/source_revision_id`。
- 列表 `application.SynthesisRevisionSummary.RemergeSourceRevisionID` 提供原 R1 标记。
- HTTP/history 应显示“重新合并”，继承的 ModelRunID/WorkflowRunID 是来源，不是新调用。原 processing.revision_ids/模型日志/receipt/history 保持原值。
- Runtime PG fixture 最低正式版本125；独立历史 migration fixture 不变。atlas/schema.sql 由 main 导出。
- 错误沿用 `SYNTHESIS_MANUSCRIPT_STALE`、`SYNTHESIS_MANUSCRIPT_INVALID`、`SYNTHESIS_MANUSCRIPT_HUMAN_FORBIDDEN` 和现有 synthesis/authoring consistency/version errors；不包含 raw SQL/证据。

## HTTP 必需契约回应（已落盘）

已回应 `candidate-remerge-ui-http-implementation.md` 的依赖：Go port 已有 `Target(context.Context, foundation.ID, foundation.ID) (SynthesisCandidateRemergeTarget, error)`，DTO 十个字段见上；`SynthesisCandidateRemergeReview.Candidate string`（JSON `candidate,omitempty`）为 READY 完整合并正文。HTTP 直接窄映射此字段，使用 Target 原值绑定 Begin；不猜 version，不用 R1 假预览。

## Binding-only publication recovery (owner 已实现并通过真实 PG/Git)

实际 app DTO：`ResumeSynthesisCandidateRemerge { WorkspaceID, NoteID, AttemptID foundation.ID }`。owner port：`Resume(context.Context, ResumeSynthesisCandidateRemerge) (SynthesisCandidateRemergeReview, error)`。

HTTP 使用 `POST .../candidate-remerge/:attempt_id/resume`，严格空 body，三个身份只从路由读取。不接收 Apply key、resolution、正文或 hash。要求已有 APPLY，重读持久 Publication 和原 reservation 后调用原 Authoring，返回已有 Review/APPLIED 和同一 P2；GET 保持不创建 P2。客户端从 GET 的 AttemptID 恢复即可，无需保留原 Apply 命令。实际验证结果更新 independent-review。
