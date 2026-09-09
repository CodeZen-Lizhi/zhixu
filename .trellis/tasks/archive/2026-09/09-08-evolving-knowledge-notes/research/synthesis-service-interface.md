# W2 服务与持久化接口

本文件对应 `internal/organizing/application/synthesis_store.go`，实现期间的共享接口，不代表完成验证。

`NewSynthesisService(SynthesisDependencies) (*SynthesisService,error)` 的依赖为 Store、Sources、Publications（既有 Authoring Service）、IDs、Clock。服务公开：

```go
ListCandidates(ctx, workspaceID) ([]SynthesisGenerationNote,error)
ApplyGeneration(ctx, input SynthesisGenerationInput, result SynthesisGenerationResult) (SynthesisApplyResult,error)
RecoverAppliedGeneration(ctx, workspaceID, processingID) (SynthesisApplyResult,bool,error)
ListNotes(ctx, SynthesisListQuery) (SynthesisNotePage,error)
GetNote(ctx, workspaceID, noteID) (SynthesisNoteDetail,error)
GetSynthesisRevision(ctx, workspaceID, noteID, revisionID) (domain.SynthesisRevision,error)
ListRevisions(ctx, SynthesisRevisionListQuery) (SynthesisRevisionPage,error)
OpenSource(ctx, workspaceID, noteID, revisionID, domain.SynthesisSourceRef) (SynthesisSourceView,error)
ReadPublishedSynthesisNote(ctx, workspaceID, noteID) (domain.SynthesisNoteSnapshot,error)
```

`SynthesisApplyResult` 包含 ProcessingID、RevisionIDs、Changed、Publications（持久 Authoring reservation 命令）和 Replayed。每个 publication 命令包含 NoteID、RevisionID、DocumentID、ArticleRevisionID、ContentHash 及 `synthesis-publish:<revisionID>` 幂等键。Apply 在触发当前 Authoring reconciliation 前先查 immutable receipt；进入候选 UoW 并加短 Workspace lock 后再次查 receipt，然后验证真实 semantic journal、锁笔记/源 fence、退役旧待审候选、创建 generated ArticleRevision/语义投影和 reservation、保存 receipt。提交后通过既有 Authoring Service 恢复所有 publication 命令。NO_CHANGE 只保存 receipt，零 Revision/Proposal。

`RecoverAppliedGeneration` 只依赖已提交 receipt 和既有 Authoring 发布，不重新打开来源或请求模型；runtime 在重投递时先调用它，再决定是否需要生成。Store 同时提供 root `LookupSynthesisApplyResult(ctx, workspaceID, processingID)` 与 `LookupSynthesisApplyResultScoped(ctx, scope, workspaceID, processingID)`，返回 `(SynthesisApplyResult,bool,error)`；retry 持 processing lock 时使用 scoped 版本，避免占用第二连接或读不到事务内 receipt。

Runtime 独占 Source processing、模型 input/result 和 semantic journal（00096）；W2 独占候选/application receipt（00095）。Runtime 的 GORM store 实现 `SynthesisValidatedGenerationFence.VerifyValidatedSynthesisGenerationScoped(ctx, scope, input, result) error`，W2 在写候选的同一 scope 校验真实语义模型已成功，拒绝只传 Verified 的信任标志。

`organizingowner.NewGORMSynthesisSourceReader(pool, artifacts retrievalapp.EvidenceArtifactReader)` 返回 `SynthesisSourceReader` 与 `SynthesisSourceFence` 实现，并提供 `IsGeneratedSynthesisSourceScoped(ctx, scope, domain.SynthesisSourceVersion) (bool,error)`。`artifacts` 复用 `retrievalworkspace.NewReader(workspaceRepo, files)`，从 managed Content Artifact 读取原始字节，不能用 canonical chunk 替代。`raw_bytes` 使用精确 byte span，`derived_text` 使用 parser excerpt 并校验完整 Artifact bounds/hash；每段最多 16 KiB、输入最多 256 KiB，超长派生正文在 SQL 传输前受限且明确拒绝，不截断充当证据。

消费者同 scope gate 根据服务端 generated Document 的精确路径与真实 publication commit，或 generated ArticleRevision 的完整 content hash 排除回流。历史来源只按精确 tuple 打开；已移除/回流返回 UNAVAILABLE，非当前版本返回 STALE，均不返回替代正文。snapshot reader 只接受 Authoring published pointer、AGENT ArticleRevision、exact publication binding 与真实 proposal_commit/Git/hash 一致的版本，待审候选不能被当作 published。

Store 构造：`organizingpostgres.NewGORMSynthesisStore(pool, SynthesisStoreDependencies{Authoring, Retirer, Sources, Validated})`，这些参与者全部来自同一 Pool。公开查询不读模型正文或凭据，不伪造 published pointer。

`Authoring` 使用 Authoring GORM repository；`Retirer` 使用 `authoringchangecontrol.NewGeneratedPublicationRetirer(changecontrolRepo)`；service 的 `Publications` 直接复用现有 Authoring Service，无需另建发布实现。`SynthesisProcessingPage`（Items、NextTime、NextID）位于 application，供 runtime 与公开 HTTP 共用。笔记列表只读 revision 摘要与计数，不加载 Items/Delta/Markdown；详情 current revision 必须与 Note pointer 相符，published revision 可为较旧版本，reservation 尚未完成 Proposal 创建时 Publication 为 nil。

迁移 `00095_synthesis_notes.sql` 将 Note/Revision 与 Authoring generated origin 双向绑定；语义投影、来源索引、topic namespace、publication reservation 与 apply receipt 在同事务闭合，历史来源和 aliases 不能事后追加到投影之外。当前验证证据见 `synthesis-core-verification.md`。
