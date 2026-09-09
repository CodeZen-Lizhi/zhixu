# W1：持续合成核心合同

本文件的类型事实源是 `internal/organizing/domain/synthesis_*.go` 与 `internal/organizing/application/synthesis_model.go`。W1 只实现纯领域规则与调用合同；数据库、模型执行、HTTP、面试状态机及发布接线属于后续工作包。

## 核心事实

- `domain.SynthesisNote`：Workspace 内稳定 TopicKey/别名、Note ID、Authoring Document ID、候选 CurrentRevisionID、CAS Version、整理状态。**没有独立 Published 指针**。`CurrentRevisionID` 在首个非空增量前可空；Document 绑定由服务端准备。
- `domain.SynthesisRevision`：immutable 语义投影，包含 Note/Document/ArticleRevision ID、两种版本号、父 NoteRevision、Title、Items、Delta、SourceEvent/Workflow/Model ID、RendererVersion、CreatedAt；`ContentHash` 对应固定渲染的精确 Markdown，`Hash` 对应整个投影。它不保存可独立编辑正文。
- `domain.SynthesisSourceVersion`：Workspace/Source/SourceVersion/ContentArtifact/ParseProjection/content hash。`SynthesisSourceRef` 再绑定 Span/excerpt hash 与 owner 提供的 Title。没有 Claim/Index/Verified 字段；与手工 Organizing 的 MaterialRef/EvidenceRef 不互换。
- `domain.SynthesisNoteSnapshot`：已证明发布的 NoteRevision/ArticleRevision/content hash/projection hash/条目快照；不依赖 Interview 包。`application.SynthesisNoteSnapshotReader.ReadPublishedSynthesisNote(ctx, workspaceID, noteID)` 必须由 owner 先证明实际发布事实再返回。领域构造本身不伪造发布证明。

Authoring 生成入口使用 `Origin=SYNTHESIS_NOTE`、`OriginID=Note.ID`、`OriginRevisionID=SynthesisRevision.ID`、`ProjectionHash=SynthesisRevision.Hash`；AGENT ArticleRevision 与语义投影在同一个 `foundation.TransactionScope` 写入。发布相关 reservation/退役合同由 `research/generated-publication-contract.md` 独立维护。

## 条目与增量

`SynthesisItem` 是严格联合：

| Kind | 唯一非空内容 | 规则 |
| --- | --- | --- |
| FACT | Fact: SynthesisStatement | text + applicability + 至少一个真实来源 |
| CONFLICT | Conflict: SynthesisConflictContent | subject + 2–4 个带来源的备选结论；不选择胜者 |
| GAP | Gap: SynthesisGapContent | 原始 question/context/context sources 保留；resolution 只能新增有依据的结论 |

稳定 ID 来自服务端 `foundation.IDGenerator`，模型不能选择。首次接受的顺序和文字是持久事实。完全相同的语义项使用既有 ID，只追加尚未出现的证据；不同措辞是否同义由模型的受控匹配与语义验证处理，纯领域层不伪装具备自然语言蕴含能力。

五种操作：

- `ADD_FACT` / `ADD_CONFLICT` / `ADD_GAP`：只携带 `Item`，必须匹配对应 kind。新 GAP 不能预带 Resolution。
- `ADD_SUPPORT`：`TargetItemID + Sources`；FACT 不带 AlternativeIndex，CONFLICT 必须带 0 起始且未越界的 AlternativeIndex。它不修改已有文字或适用条件。
- `RESOLVE_GAP`：`TargetItemID + Resolution`；保留原问题与上下文。相同 resolution 的新增证据可补强；不同 resolution 不能覆盖旧结论。

没有删除/全文替换/路径/权限/审批/版本修改操作。未知 operation、混合联合字段、未知目标、重复 ID 的不同内容、缺少来源、跨 Workspace、未打开的引用或 hash 漂移全部拒绝，错误不包含模型正文。

纯函数入口：

```go
ApplySynthesisDelta(workspaceID foundation.ID, current []SynthesisItem,
    delta SynthesisDelta, available []SynthesisSourceRef) (SynthesisDeltaResult, error)
RenderSynthesisMarkdown(workspaceID, noteID foundation.ID, title string,
    items []SynthesisItem) (string, error)
ComputeSynthesisRevisionHash(revision SynthesisRevision) (string, error)
SynthesisSnapshotFromRevision(revision SynthesisRevision) (SynthesisNoteSnapshot, error)
ResolveSynthesisSourceLabels(workspaceID foundation.ID, labels []string,
    catalog []SynthesisLabelledSource) ([]SynthesisSourceRef, error)
CanonicalSynthesisTopicKey(value string) (string, error)
```

`ApplySynthesisDelta` 不修改传入切片/指针；结果也是独立副本。空增量或仅已存在的证据返回 `Changed=false`。调用者仍完成 Source processing receipt，但**不得创建 ArticleRevision、SynthesisRevision 或 Proposal**。并发与响应丢失由 store 在 CAS 前读取完整幂等 receipt，不能拿旧 base 的新调用冒充 replay。

## 模型与原始来源

- Provider 只看到当次服务器映射的 `N001`（既有笔记）、`I001`（该笔记条目）、`S001`（原始片段）短标签。`SynthesisDelta` 已经是恢复真实身份后的领域值，不能把原始模型 JSON 直接 unmarshal 到它。
- `SynthesisLabelledSource` 的 S001–S256 必须连续且唯一；请求中的未知或重复标签拒绝。owner 必须先核验完整元组及 excerpt hash；未加载的历史引用不能被模型当作新来源。
- `application.SynthesisGenerationInput` 绑定 Processing/SourceEvent/Workflow/Node/Attempt/RequestHash、最多 24 个当前候选和有界原始片段；`SynthesisGenerationResult` 最多返回 8 个 `SynthesisGeneratedNote`。空 NoteID/BaseRevisionID 表示新知识点，ID 由 application 分配。既有笔记的 metadata/base 必须与输入完全一致。
- `SynthesisGenerator.GenerateSynthesis` 复用 StructuredRunner/RecordingChatModel/Eino，输出须经过严格 JSON Schema（未知/重复/缺失/尾随字段及联合形状拒绝）和标签恢复；Provider 不回显可信 ModelRunID/Hash。
- `SynthesisGenerationInput.Validate()` 统一校验当前候选绑定、来源范围、精确 excerpt bytes/hash、输入大小和标签目录；`SynthesisGenerationResult.Validate(input)` 核对 request/model/result 形状、既有 metadata/base 与新知识点重名，再调用纯 delta 验证。生成与消费两端复用这些入口，不把它们误当语义支持性检查。`SynthesisSourceExcerpt.Validate(workspaceID)` / `SynthesisSourceView.Validate(workspaceID)` 可供 source reader 与 HTTP 边界复用。
- `SynthesisSemanticValidator.ValidateSynthesisSemantics` 是必经的支持性校验：每个新事实、冲突备选、缺口解决结论及新增证据必须支持对应文字；GAP 的来源仅表明问题上下文。单纯元组存在/模型自报 Verified 不能通过本合同。W3 负责真实验证实现，W1 的纯校验不声称判断自然语言蕴含。
- `SynthesisSourceReader` 只打开原始来源；服务端 provenance 排除派生的 synthesis Document。`SynthesisSourceFence.VerifySynthesisSourcesScoped` 与生成 ArticleRevision/投影/receipt 同事务重验。历史来源不可用时 `SynthesisSourceView.Availability=STALE|UNAVAILABLE` 且没有替代正文。
- `SynthesisSourceReadySink.AppendSynthesisSourceReadyScoped` 在 ingestion chunked + passed 的原事务中追加通知；事件只含身份/hash，不含正文。`SynthesisSourceReady.ProcessingKey()` 以完整 source tuple + processor version 去重，忽略通知 ID/attempt ID。

## 大小与状态

核心限制：128 条目，64 操作，256 个独立来源，每个 statement/context 至多 32 来源，冲突 2–4 方；文字 4096 bytes，条件/上下文 2048 bytes，标题 512 bytes，TopicKey 256 bytes，别名至多 16。字符按 UTF-8 字节限制，条目字段是单行规范文本，不能携带控制字符。Markdown 上限 2 MiB，RevisionNo 上限 PostgreSQL integer。

模型输入：单 excerpt 最多 16 KiB、总 source text 最多 256 KiB；结构化输出最多 256 KiB。列表默认 20、最大 100。Provider schema 必须比领域上限相同或更严格，不允许通过截断一个不可定位的 excerpt 冒充原始完整片段。

Note 状态为 `QUEUED | GENERATING | PENDING_APPROVAL | READY | FAILED | CONFLICT | CAPABILITY_UNAVAILABLE | RECOVERY_REQUIRED`。失败后三态与 FAILED 要求稳定 Failure；其余不得携带 Failure。`RECOVERY_REQUIRED` 不可自动重试；生成中须有 Workflow ID；READY/PENDING_APPROVAL 须有当前 Revision。

Source processing 为 `PENDING | RUNNING | SUCCEEDED | NO_CHANGE | SKIPPED | FAILED | RECOVERY_REQUIRED`。该合同只定义 DTO，具体 store 方法由 W2/W3 按真实事务边界补充，不预造通用 Repository 框架。查询/重试的 DTO 位于 application；PublishedRevision 仅是查询时从 Authoring 证明后拼装的投影。

`SynthesisNotePage.Items` 使用轻量 `SynthesisNoteSummary`：Note、两类 revision summary、当前 Proposal binding 和 item/conflict/gap/open-gap 计数。列表不包含 Items/Delta/Markdown/原始 excerpt；只有单篇 `SynthesisNoteDetail` 打开完整当前或已发布语义投影。

## 渲染与验收

固定 renderer 把模型文字作为纯文本转义；FACT/CONFLICT/GAP 固定分区，已有项保持顺序，正文/条件/旧来源不重写。每项有服务端 item ID 注释用于定位；引用链接由 Workspace/Note 与完整原始元组派生，不接受模型 URL。前端应支持笔记详情的来源查询参数并按精确元组打开历史来源。

W1 的验证仅覆盖纯行为：重复/互补/冲突/缺口、第三来源补证与旧段落保持、重复/空 delta、引用或 scope 错误、未知或越界操作、快照/哈希/安全 Markdown。真实 owner、实库、Workflow、Provider 与浏览器验收由 W2–W6 完成，不能以纯逻辑测试替代。

W1 当前验证（2026-09-09）：

| 命令 | 结果 |
| --- | --- |
| `go test -mod=vendor ./internal/organizing/domain ./internal/organizing/application -timeout=60s` | PASS，两个包分别 0.628s / 0.758s |
| `go vet -mod=vendor ./internal/organizing/domain ./internal/organizing/application` | PASS |
| `go test -race -mod=vendor ./internal/organizing/domain ./internal/organizing/application -timeout=60s` | PASS，两个包分别 1.619s / 1.611s |
| `gofmt -l` 对本次 9 个 Go 文件 | PASS，无输出 |
| 逐文件 `git diff --no-index --check /dev/null <W1 owned path>` | PASS，10 个新文件无空白错误；不把 no-index 的正常差异退出码 1 当作检查失败 |

测试包括 9 组领域用例与 4 组 application 用例。Go 自检关注 strict union、深复制、未知或跨作用域输入、完整 owner 绑定和错误无正文泄漏；发现并修复了共享 ContentArtifact/ParseProjection 可以被另一 SourceVersion 重新绑定的漏洞，新增负测已通过。重解析可以追加新 Projection/Span，同时保留旧片段引用。

W1 不验证真实模型语义质量、原始 owner 存取、数据库原子性、后台任务/重试、审批写回或浏览器行为；这些仍需 W2–W6 接入并取得真实证据。没有修改手工 Organizing Service、迁移、任务状态或其他代理文件，没有提交、推送或部署。
