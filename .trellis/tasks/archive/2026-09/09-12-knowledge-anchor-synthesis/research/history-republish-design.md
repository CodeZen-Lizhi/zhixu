# 历史主笔记重发布：最小跨 owner 方案

2026-09-15。只读源码研究；仅新增本文，未运行测试、迁移或浏览器，未派生 agent。通用恢复导致主笔记正式识别丢失的发现直接采用本轮 quick_scan，不重复作为新发现。下文“事实”是源码可核验结论，“建议”尚未实现、未经实库验证。

## 建议结论

选 **A：独立 HistoricalRepublish owner + 新 SynthesisRevision/ArticleRevision/reservation + 原 file_patch Approval/Git 发布**。R1 是用户选中的已持久且证明完整的历史 SynthesisRevision（包括未发布旧候选），L 是当前最新候选/修订，P 是当前正式发布，F 是真实文件；复制 R1 的完整内容及冻结证据形成 R3，**R3.parent=L，restored_from=R1**。新 revision_no 连续增长，发布指针只在新 ProposalCommit 闭合后推进到 R3，绝不回指 R1。

借鉴 125 的“旧证明只读 + 新人工操作回执 + 新候选 + exact resume”，不复用其具体 remerge 权限分支或假造新的模型语义证明。保留现有 AGENT/generated owner 判据：AGENT Article 表示该受控生成 owner 写入的投影，不代表本次又调用了 AI；恢复行为明确记录 `HUMAN_HISTORICAL_REPUBLISH`，原 ModelRun/Workflow/source-event/receipt 仅为继承来源。新增独立历史复制闭包证明，禁止仅凭 AGENT、hash 或历史 model ID 放行。无需把 SYSTEM 列入可信 AI。

A 的最小性在于保留 authoring.generated_article_revision 与正常 publication_binding，所以集中阅读、115 发布事实、116 引用、117/118 传播、后续人工全文 baseline 和 Interview 均能继续使用同一种发布身份。主要新增写端的精确复制证明，而不是给所有消费者增加第二种正式发布体系。

## 已确认源码事实与落点

| 事实 | 证据 |
| --- | --- |
| GeneratedRepository 支持调用方事务内 append、retire、reserve；不自行提交。PrepareGeneratedRevision 固定创建 AGENT DRAFT，父 Article 必须同 owner、连续编号、AGENT。 | `internal/authoring/application/generated.go:58`；`internal/authoring/domain/generated.go:129` |
| SQL 同时核 Document 版本、正文 hash、AGENT、无 source_version、最新 revision、无活动 reservation/binding、同 generated parent。Synthesis 与 generated 有双向 deferred 精确身份 FK。 | `atlas/schema.sql:6428`、`:39016`、`:42308` |
| 通用 restore finalizer 从 restore_document ProposalRevision/ProposalCommit 重建事实，按 writeback 派生 Article ID，并保存 document_restore_publication，创建 SYSTEM PUBLISHED，CAS Document。不是 generated/reservation 链。 | `internal/authoring/adapter/postgres/gorm_restore_publication.go:54`；`restore_publication.go:27`；Schema `validate_document_restore_publication` / `restore_publication_is_exact_current` |
| Synthesis hash 包含新 revision 身份、parent、时间等；当前必须有 SourceEventID/WorkflowRunID/ModelRunID 和非空 Delta。v2 Content 使用原 FullContent，Items 只是映射后的可信子集。 | `internal/organizing/domain/synthesis_revision.go:20`、`:123` |
| 125 Apply 追加 BEGIN/APPLY 事实，复制旧 run/delta，但写新的 remerge provenance/hash；同事务退役未批准旧候选、append 新 Article/Synthesis/source、reserve，再调 publisher。 | `internal/organizing/adapter/postgres/synthesis_candidate_remerge_apply.go:14`；`atlas/migrations/00125_synthesis_candidate_remerge.sql:24`、`:254` |
| 125 不是通用历史复制：要求 v2、原 manuscript receipt/processing SUCCEEDED、待审或精确 needs_revision；其 source_revision_id 必须等于 parent。 | `00125:17`、`:47`；`synthesis_candidate_remerge.go:313` |
| 125 Resume 核 APPLY 的完整 revision、原 publication command、reservation/request hash/F/P/capture/receipt，再调用同一 PublishArticleRevision；不再合并或 apply。 | `internal/organizing/adapter/postgres/synthesis_candidate_remerge_resume.go:14` |
| 画像冻结沿 parent 的精确 source tuple 查 profile；值为 NULL 还会查同版本最新 profile。普通 insertSources 不显式复制 profile_revision_id。历史重发布不能原样调用后就声称快照未变。 | `atlas/migrations/00107_synthesis_profile_readiness.sql:8`；`internal/organizing/adapter/postgres/synthesis_store.go:519` |
| body_reference 从 Items 自动投影；本地延展过的引用条目只有 parent 已持有同一引用才能继承，否则必须与上游原条目完整相等。 | `atlas/migrations/00116_synthesis_body_references.sql:40`、`:85` |
| 115 按新 Synthesis revision 身份生成事件；证明允许历史 Article SUPERSEDED，要求真实 generated/binding/ProposalCommit，不能拿 Document 当前指针轮询代替历史事件。 | `atlas/migrations/00115_synthesis_publication_events.sql:6`、`:28`；`internal/organizing/adapter/postgres/synthesis_publication_events.go` |
| 117 要求 updated.revision_no > 原上游版本，按实际引用条目比较，去重是 base_revision/item/publication；不按全文 hash 去重。118 冻结新的 upstream identity/projection。 | `atlas/migrations/00117_synthesis_body_impacts.sql:35`；`00118_synthesis_body_refresh.sql:99` |
| manuscript baseline 核 latest AGENT owner、note version/current、P 的完整 publication/commit，并确认 P 是 latest 的祖先；source-review baseline 也用这一发布身份。 | `internal/organizing/adapter/postgres/synthesis_manuscript_baseline.go:164`；`synthesis_manuscript_source_review_baseline.go:148` |
| Interview 从 ReadPublishedSynthesisNote 取已证明发布快照；SnapshotFromRevision 不自行赋予发布证明，v2 保留全文和可信映射。 | `internal/organizing/adapter/postgres/synthesis_read.go:301`；`internal/organizing/domain/synthesis_revision.go:70`；`synthesis_manuscript_interview_integration_test.go:163` |

## 选定版本资格：不要求 R1 曾发布（原需求修正）

PRD原文是“用户可以选择一个版本作为当前发布版本，历史版本可查看或回滚”。此前本文把R1限为历史发布是不必要的收窄，现已纠正。现有生成顺序是先在同一事务落SynthesisRevision/generated Article/receipt/source闭包，再经独立Approval/Git发布；因此**选定版本完整性不依赖R1自身publication**，没有不可绕开的“R1必须曾发布”合同。

精确选择器是workspace/note/revision ID，服务端沿Article/generated身份、projection及content校验、来源元组和原生成闭包验证。普通v1/v2核原apply receipt与真实生成/语义证据；125派生版本核其BEGIN/APPLY、继承receipt与原生成链；128版本核其人工复制闭包递归至已证明原版本。不能只看一个ModelRun ID或AGENT标记，也不能要求原apply receipt包含后来派生版本的ID。曾退役的候选保有合法内容证明，不等于有发布授权；R3必须取得新的审批。

仍然必须存在的publication证明有两类：①有当前P时，其精确publication/commit用于本次替换和supersede；②R1条目包含别的主笔记时，body_reference所指上游当时的publication证明。它们不能被误用为R1自身曾发布的条件。R1没有publication时，Begin/Apply/Resume都不补造该事实；R3首次获批后才成为自己的独立发布事实。若主笔记尚无P，沿原CREATE_ONLY/absence capture分支处理，不强求P ID；目标已存在则保持既有冲突规则，不借本功能接管文件。

## A/B 比较及不采用的捷径

**A（推荐）**：专用人工命令 owner 验证选定 R1 的精确不可变 revision/generated/生成或派生闭包及来源快照，再调用现有 scoped generated writer。新增历史复制 provenance/operation，不新增模型运行。128 为 Synthesis 闭包、来源快照继承、发布双基线增加窄分支；正常 AGENT/proof 判据、publication finalizer 和事件类型不变。

**B（不推荐）**：复用 restore_document 必须在创建 Proposal 前保存 selected SynthesisRevision + 精确生成/派生证明映射（历史 publication/commit 如有则另存）；现有 restore payload 的 Git/hash 不足以唯一识别 R1。最终化还必须同事务创建新的 SynthesisRevision 与 SYSTEM Article 映射、sources，并更新 note latest。之后至少要处理：Synthesis→generated 强 FK、generated parent AGENT 限制、Read/Graph/Interview 的可信读、115 证明 view、116/117 对 publication_binding 的 FK、121 双基线以及 manuscript/source-review owner。合法做法是独立且严格的 HUMAN restore publication proof 联合，而非 `OR created_by_type='SYSTEM'`，但改动明显大于 A；如果 B 最后也转为 generated Article/reservation，则实质回到 A 并多绕一层。

反例：同 hash 的另一个 revision/普通文件/Git commit 不能作为 R1；补写旧 apply_receipt.result 让其“包含 R3”违反不可变；把 R3.parent=R1 会破坏连续历史/当前 P 祖先检查；直接切回 R1 会让 117 的版本次序漏掉撤回 R2 内容造成的影响。仅修 UI 的 PublishedVerified 也不能修复后续生成和下游。

## 最小命令/API 与执行合同（建议）

在现有 `/api/v1/workspaces/{workspace_id}/synthesis/notes/{note_id}` 路由下增加 `historical-republish`（实际 base 字符串沿 handler），复用 125 的 Target/Begin/Read/Apply/Resume 形状，不复用其领域断言：

- `GET .../historical-republish/target?revision_id=R1`：服务端返回精确 R1 revision/Article/generated及生成或派生证明身份、可选历史 publication/commit、当前 L/P/Document/Note 版本与可用动作/阻塞原因。未发布旧候选只要证明完整即可选中；UI明确标“历史候选，尚未发布”，由本次R3审批发布，不要求先发布R1。
- `POST .../historical-republish`：Begin，提交 selected_revision_id、expected_selected_projection_hash（只作CAS校验，非身份选择）、expected L/P/Note/Document 身份、幂等键。服务端读 R1/Article/证明，不接受客户端正文、sources、ModelRun、target path 或可信布尔值。授权沿 `READ_LOCAL`/`WRITE_PROPOSAL` 与当前 RootGrant，不能从 body 接收用户身份。
- `GET .../historical-republish?key=...`：只读恢复 BEGIN 及当前结果。完整刷新可按 key 找回 attempt，不产生副作用。
- `POST .../:attempt_id/apply`：提交 key、预览 fingerprint、明确恢复为所选全文的确认；若有待审 L，绑定明确的 retire-L 选择及其精确 Proposal 版本。无任意 final_content 字段，保持恢复结果就是 R1。
- `POST .../:attempt_id/resume`：只恢复已 APPLY 的原 reservation/publish command。成功返回 Proposal 只标“待审核”，不能标“恢复已发布”。

Begin 冻结 R1 revision/projection/content hash、Article/generated、生成或派生闭包身份；R1历史publication/ProposalCommit/Git仅在存在时作为可选审计事实保存；另冻结 L/P、Document/Note 版本、当前路径和 RootGrant/F capture。显示 **P→R1** 和 **F→R1** 的完整差异及将丢弃的待审 L；其 fingerprint 绑定这些身份和字节。Apply 新事务按 note→document→proposal 的一致锁顺序重查，并复核真实 F。任一漂移返回稳定 409，不重绑定旧预览。

Apply 的一个 UoW：追加 APPLY 操作回执 → 必要时精确 retire L → append R3 Article → append Synthesis R3、原始 source/profile 快照及 body references → CAS note.current_revision=R3/version+1 → reserve R3。各 owner 使用同一 TransactionScope，失败全回滚。事务外继续原 PublishArticleRevision → file_patch Proposal → Approval → Safe Writeback/Git → Authoring finalizer。P 指针在候选阶段不动；R1/R2 的正文、sources、模型回执和历史 publication proof 不变（旧当前 Article 的合法 SUPERSEDED 状态照常）。

**R3 字段**：新的 ID/Article ID/revision_no/Article no/parent=L/created_at/projection hash；正文、title、renderer、Items、v2 Manuscript 从 R1 精确复制。新增可选 `historical_republish={attempt_id,selected_revision_id,selected_article_revision_id,selected_projection_hash,action:HUMAN_HISTORICAL_REPUBLISH}（选定版本的publication/commit为可选审计字段）` 并纳入 hash；当前候选的 `remerge` 必须清空，R1 自身的 remerge 历史通过 selected_revision 查，不能带来两个并行授权分支。保留 R1 的 run/event/model/Delta/可选 manuscript_receipt 引用只是满足继承 lineage，不将它们显示为 R3 本次融合变更；恢复历史行/API 要显示人工恢复来源，差异使用 P→R1。128 严格核继承字段等于选定 R1，不制造新 model/processing/apply receipt。

v1 仍复制 v1，不为了拿 v2 receipt 伪造 Manuscript；v2 精确复制包括 FullContent、Machine、Assessment、ReviewItems/IneligibleItems、可信 Items，不能把未映射人工段落提升为可信知识。当前 note.title 是不可变身份（`atlas/schema.sql:17695`），同 note 历史 title 应一致；不同则拒绝为不一致，不能借恢复修改身份。路径仍用当前合法 canonical_path，不回到旧文件路径。

## 待审核候选、人工 F 与丢响应（建议）

1. 无活动候选/发布：直接准备。已有未批准 ready_for_review L 时，预览明确展示其将被替代，Apply 调 Authoring retirement + ChangeControl scoped retirer；旧 proposal/binding 保留为 needs_revision/CLOSED。已有精确 needs_revision 的 L 需要 128 支持其历史重发布操作回执，不能冒用 125 的 APPLY 事件来走例外。已 Approval/dispatch/tool authorization/writeback/commit、RECOVERY_REQUIRED 或正在准备 reservation，返回 busy，保留原链等待现有恢复完成；不取消已授权发布。没有绑定的草稿/正在生成的 processing 也不能被静默淘汰：owner 给出阻塞/显式处理入口，执行中运行先正常终结，不能改其 proof。操作并发以 note/document CAS 排斥。
2. **F≠P 可恢复，但必须明确全文覆盖**：专用操作的语义是选定 R1，不自动把 F 合并进去。预览确认准确绑定 F，用户可取消保留 F；确认后原 Proposal 审批审阅 F→R1。reservation.base=F.hash，独立保存 P.Article/hash 供 supersede/CAS。候选产生后 F 再变，原预览/写回拒绝；重新 Begin 按最新 F 审阅，旧未批准候选只能经上述退役规则处理。不能把 125 三方合并产物仍标为“精确恢复 R1”。Git clean、文件锁/CAS 与 root identity 门禁保持，未提交 F 仍可能被现有 Git 门禁拒绝。
3. Begin/Apply 同 key 同完整请求先读不可变结果再判断旧 CAS；同 key 换 R1、版本、fingerprint、确认内容拒绝。并发不同 key 只能一个 Apply 成功，不能双退役/双候选。BEGIN 唯一 `(workspace,note,key)`，APPLY 每 attempt 至多一个，并有独立 command key/request hash。
4. Apply 已提交但 publisher 尚未调用或响应丢失：GET 返回确切 R3/reservation；Resume 只用原 `synthesis-publish:R3` 命令验证完整绑定后补建/找回原 Proposal。不能因后续 P/F 漂移而把“已成功 APPLY”说成没发生；读回历史结果与“是否还能继续新写入”分开。已有 binding/commit 后继续原 Authoring reconciliation，禁止重建 R4。
5. UI 在 URL 保留 workspace/note/selected revision、begin key/attempt 与未确认的 apply key/fingerprint；刷新只 GET，不自动批准；同 key 重放找回同一 R3/Proposal。读权限不足不能泄露结果；写权限/RootGrant 失效不允许 Resume 创建新 Proposal。
6. 选中已是当前发布的同一 **revision identity** 可返回明确 no-op（若 F 不同则仍可提出恢复文件的候选）；选择不同历史 revision 但全文 hash 相同不能认成同一个历史版本。若仅身份/来源不同，仍需新 R3 表达这次选择、经审批；须实测现有 writeback 的同字节提交路径能否形成合法 ProposalCommit，若不支持要修精确 no-content-change 发布证明，不能伪造 commit 或吞成旧 R1。

### 来源失效与当前范围变化（补充合同）

恢复是用户明确选择已持久且证明完整的历史版本内容，不能调用当前 source admission 重新筛选 R1 的 Items，不能静默丢弃现已范围外的段落，也不能将该选择解释为接受扩大锚点范围。Begin 同屏展示选定历史来源的当前失效提示、当前锚点范围与历史内容可能不一致的提示；没有执行新的语义核验就不得声称恢复内容符合当前范围或来源已重新支持。来源快照及旧 proof 原样保留，读取仍执行既有隔离/授权边界。

冻结当前锚点身份/范围版本（无锚点则冻结其缺席），Apply 重查；若在预览后变更则旧 fingerprint 失效，重新预览，而不是自动重生成或改锚点。合法恢复可以恢复过去已生成且证明完整（无论是否发布）、但现不属于自动纳入范围的全文，因为这是显式人工历史选择；其后新的增量、补源和下游候选继续遵守当时有效的范围与独立核验规则，不能继承“历史恢复”作为新来源准入许可。SQL 操作回执需绑定该范围审阅事实，保持既有 anchor 行与关系不变。实库案例补入：R1 生成后收窄范围/来源失效，恢复保持 R1 内容及范围现状；Begin 后再次改范围则 Apply 冲突，下一次新增范围外来源仍不能自动纳入。

## 128 必须包含的约束（建议）

- 新 append-only `organizing.synthesis_historical_republish_event`（BEGIN/APPLY）及 Synthesis `historical_republish_id`/provenance；限制 payload/hash/keys，workspace/note/attempt 复合 FK，拒绝 UPDATE/DELETE/TRUNCATE；APPLY 新 revision 唯一。互斥本次 remerge 与 historical-republish 分支，老数据 NULL 行继续走老规则，历史 hash 编码不变。
- BEGIN/APPLY 的直接 SQL guard 独立验证 selected R1 的不可变 revision、精确 generated Article与对应生成/派生闭包、same workspace/note/document、完整内容/Items/Manuscript/继承字段；从服务器保存事实导出，不能凭自报 JSON/hash 授权。selected 可以是尚未发布的DRAFT旧候选、PUBLISHED/SUPERSEDED历史版本，也可以是此前合法remerge/republish；退役/拒绝的旧Proposal不能复用其授权，必须新审批R3；链通过独立 selected 指针追溯，时间/已有行约束防环，不回写原 proof。
- 扩展 `validate_synthesis_revision_manuscript`、`verify_synthesis_manuscript_closure`、`verify_synthesis_closure` 的**精确历史复制分支**。检查新 parent=L 与连续编号，而复制源是 R1。deferred closure 强制 APPLY/R3/generated Article/note CAS/同一 reservation 及可选旧候选 retirement 全闭合。缺源、少映射、假 ModelRun、跨 note 同 hash 都失败。正常生成仍要求真实 apply receipt，不能全局取消。
- `freeze_synthesis_source_profile` 对此分支按 selected R1 的**完整 source tuple**复制 profile_revision_id，包括 NULL，不回退当前最新 profile；`insertHistoricalRepublishSources` 从旧行复制而非再推导来源。`validate_synthesis_source` 的 span/parser/artifact 精确核验保留。选定历史正文的不可变证据不因来源当前删除/失效自动拒绝为“无证据”；可用性继续独立提示。
- `validate_synthesis_revision_body_reference` 对此分支允许 selected R1 已有的同一引用/完整条目继承，仍要求精确上游 historical publication proof；不能因为 R3.parent=L 没该引用就要求它等同上游未延展原文。正常新 INCLUDE_ITEM 的相等约束不变。后补 source_supplement 和 126/127 current-text evidence 不拷贝成原始引用、不改旧记录；仍按所属版本/独立记录展示，不能因相同全文 hash 将旧 review 完成状态迁移到 R3。
- **Authoring 双基线须加类型化分支，不能把 APPLY ID 填进 merge_receipt_id。** 当前 `publication_merge_receipt_fk` 确实引用 manuscript_receipt（`atlas/schema.sql:39076`）。建议 reservation 新增 `historical_republish_id`，以 `(id,workspace_id)` FK 指本次 APPLY；历史恢复支路 `merge_receipt_id=NULL`，仍保存本次 F capture 和当前 P ID/hash。`PublicationMergeBaseline` 增加可选 HistoricalRepublishID，形状严格为“原 manuscript receipt”与“历史 APPLY”二选一，普通非双基线两者均空；原 FK 保留。`synthesis_publication_merge_baseline` 增加同名判别列，普通支路 NULL，历史支路 receipt_id=NULL、historical_republish_id=APPLY、capture_id=本次 F、published fields=P、generated parent=L，valid 核完整历史复制事实，v1/v2均覆盖。普通121/125支路排除 historical_republish_id，保证一个Article一行proof。同步 reservation insert/scan/domain Validate/equality、SQL publication_merge_shape/validate_publication_merge_baseline/publication_replaces_revision 中“存在双基线”的判定，以及 Go `gorm_publication_merge.go` 扫描nullable receipt和新ID；原无proof及普通发布规则不变。历史 v2的Synthesis.manuscript_receipt_id仍指R1旧receipt，不当作这次恢复授权。
- 保留 `authoring.validate_generated_revision_insert` 的 AGENT/origin/parent/latest 检查，保留 binding/reservation/ProposalCommit 和 `validate_publication_merge_baseline` 双基线检查；如退役 needs_revision 增加例外，只接受该次已证明人工操作的精确旧 Proposal 与“从未批准/执行”的全部负条件。
- 115 `synthesis_proven_publication`、event key/schema 无需放宽；R3 正常生成新的 published event，117 按新的递增 revision_no 检查实际正文依赖，118 正常冻结并生成候选。历史内容重现但内容没有实际变化，允许新发布事实而无无关正文候选；不能按 hash 抑制发布事实。
- 仅新增 00128、更新 atlas.sum 与正式 schema 导出；不得改 095/106/115/125/127 原迁移、旧 ModelRun/apply proof。已有通用 SYSTEM restore 不能靠 hash 回填历史 R1 身份；无当时精确选择记录的存量需报告不可自动修复，本切片不暗中迁移。

## 可直接分工的文件范围（建议）

| owner | 最小实施文件 |
| --- | --- |
| Organizing 命令与存储 | 新 `internal/organizing/application/synthesis_historical_republish.go`；新 `adapter/postgres/synthesis_historical_republish*.go`（target/begin/apply/read/resume）；复用 runtime 的 root/capture/publisher 依赖；修改 `domain/synthesis_model.go`、`domain/synthesis_revision.go`、`adapter/postgres/synthesis_models.go` 保存/编码 provenance；独立 source-copy helper。 |
| Authoring/ChangeControl | 复用 `application/generated.go`、`domain/generated.go`、`adapter/postgres/gorm_generated*.go` 的 scoped append/retire，以及 `gorm_publication_merge.go`/原 publish finalizer。修改 PublicationMergeBaseline/PublicationReservation 对应 application/domain 契约、`gorm_publication.go`/reservation model与扫描、`gorm_publication_merge.go`，接入类型化 historical_republish_id 与 nullable manuscript receipt；SQL retirement 例外由128增加。**不改**通用 `gorm_restore_publication.go` 的 SYSTEM语义；不另做 Git owner。 |
| 数据库 | 新 `atlas/migrations/00128_synthesis_historical_republish.sql`、`atlas/migrations/atlas.sum`、`atlas/schema.sql`；约束点见上节，含历史 source/profile/body_reference 与 v1/v2 publication merge proof。 |
| HTTP/组合根 | 新 `internal/organizing/http/synthesis_historical_republish.go`；修改 `synthesis_handler.go`、`synthesis_wire.go`；`cmd/api/synthesis_manuscript.go`/`synthesis_components.go` 接 owner；auth 路由能力覆盖。 |
| UI/合同 | `web/src/features/synthesis/SynthesisNotePage.tsx` 历史按钮与人工恢复来源标识；新窄 HistoricalRepublishWorkbench；`queries.ts`、新 `web/src/api/synthesis-historical-republish.ts`；OpenAPI 权威输入及 `api/openapi/openapi.json`/generated client 按已有生成流程同步。复用差异/Proposal跳转及 URL 恢复模式。 |
| 下游读与验证 | `synthesis_read.go`/`synthesis_wire.go`/Web decoder 增加恢复 provenance 展示，保留 PublishedVerified 条件。`synthesis_manuscript_baseline.go`、`synthesis_manuscript_source_review_baseline.go`、`synthesis_publication_events.go`、body impacts/refresh/Interview 原逻辑优先不改；用下节实库案例证明兼容，出现契约不兼容再作窄修。 |

不把本功能强塞进 125 candidate remerge：后续**新 AI 增量产生的普通候选**仍可用125。历史恢复候选因 F 漂移需重新按历史恢复流程审阅；UI 不应给此类候选暴露目前要求原 receipt/capture 的125入口。若以后允许“恢复后再合并人工内容”，应明确成为新编辑语义，本次不附带。

## 交付必须覆盖的最小实库案例（尚未执行）

1. **主流程 v1/v2**：实际生成并发布 R1、R2，选 R1→Begin/Apply R3。另以同等完整证明的R1未发布旧候选（含其旧Proposal已退役/拒绝）、当前P为其他版本及L为最新候选执行同一路径：不得因R1缺publication拒绝，不复活旧Proposal授权；R3本次审批后首次产生自己的发布事件。审批前 P/文件仍 R2，R3.parent=R2，新 ID/版本/hash、正文等于 R1；真实 Approval/Git/finalizer 后当前 GET/列表/完整刷新/历史/Graph/ReadPublishedSynthesisNote/新 Interview 都读 R3。旧 Interview 继续 R1/R2 快照。v2 含人工全文、未映射段落和冲突，全文逐字相同且可信 Items 不扩大。
2. **来源与包含继承**：R1有历史 profile=NULL、后来同 source 产生 profile；另一来源 profile 已有新修订；R2 移除该 source/body_reference。R3恢复时仍精确复制 R1 的 NULL/旧profile、原span及已在R1延展的上游引用。删除源文件后正文和已保存历史片段仍读、可用性提示保留；R1/R2/source/receipt 表快照不变，后补源/current review 不冒成 R3 新核验。
3. **丢响应与竞争合并一组**：BEGIN已提交丢响应→reload原key；APPLY提交后publisher未调；Proposal已建但响应丢失；Git完成finalizer前重启。逐步 Read/Resume并发重放，每次仅一R3/reservation/Proposal/commit/event且无额外ModelRun。完成后L/P已前进，旧key仍返回原身份。同key换选择/fingerprint、不同key同时Apply，以及跨workspace同hash均拒绝或单winner。
4. **待审与人工F**：有未批准 L，显式替代后旧proposal=needs_revision/binding=CLOSED且无历史proof改写；已有approval/dispatch/recovery拒绝。F≠P时展示F→R1并明确确认，隔离Git提交F后经双基线成功；Apply前F漂移、审批后写前F漂移、P/Document/root漂移均不覆盖。被拒绝的请求不能留下半个R3。历史恢复候选F再漂移经新Begin/退役重建成功。
5. **恢复后的新增来源/人工全文**：R3发布后新增相关source真实走 processing→generation/独立semantic→manuscript→approval得到R4；无关段落保持。再手改F，当前全文source-review/后续增量以P=R3或R4、L=真实最新、F=真实全文校验；旧R1/R2 review不得因hash相同显示为当前完成。R4候选继续走125重新合并验证一次。
6. **实际下游撤回传播**：B引用A.R2被改动的条目，A恢复R1成为A.R3，115新事件触发B的117观察和118局部候选，审核前B不动，审核后B→C继续传播；未引用章节、共享来源、同内容引用不产生无关候选。重复扫描及A/B相互引用有界，无重写旧body_reference。A.R1→R2→R3内容重现仍按身份生成新事件，不能漏掉R2撤回影响。
7. **迁移与SQL负例合并**：正式127带v1/v2/125/126/127成功历史升级128，再跑1/2；空库128/schema导出恢复可用且旧行/旧hash保持。直接SQL尝试缺原revision/generated/生成或派生闭包证明、缺当前P发布证明或引用的上游发布证明、换same-hash identity、少source/profile错/NULL填新、篡改manuscript/trust mapping、假operation/模型proof、半事务缺reservation、伪retirement或双proof、直接P指针/旧proof更新，均由约束拒绝。另包含不同历史revision同全文hash的真实审批/Git证明：不能仅测HTTP返回。

验收以真实持久身份/全文/后续读取及调用链为准；本文提供实施设计，不声称上述未执行案例通过。
