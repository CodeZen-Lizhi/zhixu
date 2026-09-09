# W2 Generated Authoring / Publication 合同

## 接口

类型已落于 `internal/authoring/domain/generated.go` 与 `internal/authoring/application/generated.go`。
实现由 `authoringpostgres.GORMRepository` 提供；与 Synthesis Repository、Change Control Repository 使用同一个 Pool。

```go
AppendGeneratedRevisionScoped(context.Context, foundation.TransactionScope,
    authoringapp.GeneratedRevisionRecord) (authoringapp.GeneratedRevisionResult, error)
RetireGeneratedPublicationScoped(context.Context, foundation.TransactionScope,
    authoringapp.RetireGeneratedPublicationRecord, authoringapp.GeneratedProposalRetirer,
) (authoringapp.RetireGeneratedPublicationResult, error)
ReservePublicationScoped(context.Context, foundation.TransactionScope,
    authoringapp.ReservePublicationRecord) (authoringapp.PublicationPreparation, error)
```

`GeneratedRevisionRecord.Request` 为 `authoringdomain.GeneratedRevisionRequest`：

- `WorkspaceID, DocumentID, ArticleRevisionID`：调用方预分配的精确身份。
- `OriginKind = GeneratedOriginSynthesisNote`（`SYNTHESIS_NOTE`）、`OriginID = Note.ID`、`OriginRevisionID = SynthesisRevision.ID`。
- `ProjectionHash = SynthesisRevision.Hash`，由 W1 `ComputeSynthesisRevisionHash` 生成。
- `ExpectedDocumentVersion`：首次 0；后续是读取的实际 Document.Version，包含发布最终化引起的递增。
- `ParentRevisionID`：首次空；后续必须是此 Document 当前最新且同 origin、状态为 DRAFT/PUBLISHED 的 AGENT ArticleRevision；归档版本不能继续自动追加。
- `RevisionNo`：首次 1；后续精确 parent.RevisionNo + 1。
- `Title, TargetPath, Content`：已规范化标题、安全相对 Markdown 路径与 `RenderSynthesisMarkdown` 的精确 LF 字节。不会再重写正文。

Record 另有 `IdempotencyKey, RequestHash, CreatedAt`。RequestHash 必须由
`authoringdomain.ComputeGeneratedRevisionRequestHash(Request)` 得到。生成的 Document/ArticleRevision
身份是请求的一部分；重试保留同一组身份与语义投影，`CreatedAt` 重放返回原时间。
返回值包含初次写入时的 `Document`、`Revision` 快照与 `Replayed`；后续 Published 不改变旧命令重放结果。

`RetireGeneratedPublicationRecord.Request` 为 `authoringdomain.GeneratedPublicationRetirementRequest`：
`WorkspaceID, DocumentID, ArticleRevisionID, PublicationID, OriginKind, OriginID, OriginRevisionID,
ProjectionHash, ExpectedDocumentVersion, ExpectedProposalVersion`。Record 另有
`IdempotencyKey, RequestHash, RetiredAt`；hash 使用
`ComputeGeneratedPublicationRetirementRequestHash`。PublicationID 唯一决定旧 immutable Proposal/Revision binding。
作者 owner 核对真实 generated origin、当前 Document/ArticleRevision 与 Publication/Reservation 后，
通过 `GeneratedProposalRetirer` 让 Change Control 锁定精确 Proposal/Revision。
Change Control 仅允许 `ready_for_review`、精确当前 Revision/version、且无 Approval/dispatch/authorization/
writeback/commit 事实的候选进入 `needs_revision`。Authoring 复用旧规则关闭 Binding并保存不可变退役 receipt。
批准已先取得锁时返回 `AUTHORING_GENERATED_PUBLICATION_BUSY`，不取消或撤销任何外部副作用。

## Synthesis UoW 接线

在同一个 caller-owned scope 内按以下顺序执行：

1. 锁 Note 并检查其 CAS；需要更新旧待审候选时调用 scoped retirement。
2. 调用 `AppendGeneratedRevisionScoped`；写 SynthesisRevision/当前候选投影。
3. 调用 `ReservePublicationScoped` 固化新候选发布窗口。`PublishBinding`/RequestHash 继续使用现有
   `ComputePublishRequestHash(WorkspaceID, DocumentID, ArticleRevisionID)`。
4. 一起提交。任何一步失败，退役、生成版本、投影与 reservation 一起回滚。
5. 事务外以同一 publish key 调用现有 `Service.PublishArticleRevision`，恢复 reservation 并创建/绑定 Proposal。
   公共 HTTP Working Draft 命令仍固定 USER，生成链不创建或修改 Working Draft。

旧 reservation 未闭合时追加返回 busy，调用方先恢复其原 publish 命令；不能制造第二个发布窗口。
已批准/执行中的旧候选等待既有 Apply/Reconcile 终态；已发布后基于实际新 Document.Version 创建后续候选。
正式 Published pointer 仍只能由 exact `proposal_commit` 最终化。

## Schema 对接

`00094_generated_authoring_revisions.sql` 新增 `authoring.generated_document`、
`authoring.generated_article_revision` 和 `authoring.generated_publication_retirement`。
前两者分别保存不可变 Document origin 和 generated revision/command receipt。
generated revision 表提供复合唯一键：

```text
(article_revision_id, workspace_id, document_id, origin_id, origin_revision_id, projection_hash)
```

SynthesisRevision 可用其 ArticleRevision/Workspace/Document/Note/自身ID/Hash 加复合 FK，证明投影与生成正文匹配。
如需要在 DB 层强制两向完整闭合，Synthesis 迁移可再添加 deferred FK 从 generated revision 的 origin revision/hash
指向 SynthesisRevision；此 W2 迁移不猜后续 owner 表名，也不依赖未落地的 organizing Schema。

生成 ArticleRevision 固定 `created_by_type=AGENT`、`source_version_id=NULL`、`status=DRAFT`、`git_commit=NULL`。
不能把已有 USER Document 认领为 generated origin；来源/请求/hash/parent 字段 append-only。
源处理回流排除应由 Authoring generated Document/Revision 身份推导，不能依赖路径或模型输出。

默认路径由 `DefaultGeneratedTargetPath(Note.ID)` 生成 `synthesis-<note uuid>.md`，在已存在的 Workspace 根下；
不会创建父目录。其他安全相对路径仍由既有 ProposalCreator/Safe Writeback 检查真实父目录与基线。

## 错误

- 非法或非规范输入：`AUTHORING_GENERATED_REVISION_INVALID`。
- Origin、Document CAS、当前 parent 或用户版本改变：`AUTHORING_GENERATED_ORIGIN_CONFLICT`。
- pending reservation、无法退役的审批/执行状态：`AUTHORING_GENERATED_PUBLICATION_BUSY`。
- 同 key 不同请求：既有 `AUTHORING_IDEMPOTENCY_CONFLICT`。
- Workspace/identity 不匹配：既有 Authoring not-found。
- caller cancellation/deadline：保留现有 context cause 与 retryable 分类。

本文件是 W2 接口约定，验证结果另写 evidence；接口存在不代表业务闭环已完成。
