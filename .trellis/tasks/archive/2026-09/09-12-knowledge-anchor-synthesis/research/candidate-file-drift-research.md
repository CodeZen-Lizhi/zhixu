# 候选已生成后的主笔记文件漂移

状态：`partial`。以下结论已按当前工作区代码与迁移核对；未启动 PostgreSQL、Git 或修改生产代码。

## 结论

不存在可直接复用的合法重合并入口。`processing=SUCCEEDED` 后的普通 `RetryProcessing` 不适用；它不能替代本场景。普通 Change Control Proposal revision 虽能在当前工作区内容上做三方合并，但它只更新 Proposal 的当前 revision，不能创建与合成候选相符的新 `SynthesisRevision`、生成 `ArticleRevision`、manuscript receipt 或 Authoring publication binding。因此不能用它把旧候选改写成“基于最新 F 的候选”。

已存在的链路会安全地拒绝或关闭陈旧候选，而非重合并：`F` 在候选生成后变化时，旧 Proposal 的 base 与实际文件不再相等；普通 Proposal revision 可以只在 `ready_for_review`/`needs_revision` 的 replace 提案上另建 revision，但原候选的 publication binding 仍精确指向旧 revision。若旧 pending candidate 被正常替换，已有 Authoring retirement 可以将旧 binding 关闭并把旧 Proposal 标记 `needs_revision`，然后由新的生成候选建立新的文章与 Proposal。这是新路径应复用的生命周期，不是现成的“remerge”命令。

## 已验证调用链

### 候选创建到待审核 Proposal

1. v2 workflow 的 apply 节点调用 `SynthesisManuscriptRuntime.ApplyManuscripts`，它只收集同一 execution 的 sealed receipt，并再次核对 HumanTask 所提交的 receipt 身份后调用 `ApplyManuscriptGeneration`。[synthesis_manuscript_runtime.go](/Users/zhenglizhi/GolandProjects/zhixu/internal/organizing/adapter/postgres/synthesis_manuscript_runtime.go:127)
2. `GORMSynthesisStore.ApplySynthesisGeneration` 在事务内再次锁定当前 note/revision，要求冻结的 note version、current revision 与 projection hash 仍相等；随后调用 `appendCandidate`。这一步是生成中的 CAS，而不是 SUCCEEDED 后的恢复入口。[synthesis_store.go](/Users/zhenglizhi/GolandProjects/zhixu/internal/organizing/adapter/postgres/synthesis_store.go:297)
3. `manuscriptCandidate` 从 sealed receipt 重建候选 revision，要求 receipt hash、processing ID、machine items 一致，并在同一事务内 `VerifyReceiptScoped`。新 revision 的 parent、revision number、workflow run、model run 和 delta 都取自冻结 generation。[synthesis_manuscript_candidate.go](/Users/zhenglizhi/GolandProjects/zhixu/internal/organizing/adapter/postgres/synthesis_manuscript_candidate.go:29)
4. `appendCandidate` 对已有 note 将旧 pending generated publication 通过 `RetireGeneratedPublicationScoped` 关闭；随后创建新的 `SynthesisRevision`、generated `ArticleRevision`，并为它保留 Authoring publication reservation。[synthesis_store.go](/Users/zhenglizhi/GolandProjects/zhixu/internal/organizing/adapter/postgres/synthesis_store.go:380) [synthesis_store.go](/Users/zhenglizhi/GolandProjects/zhixu/internal/organizing/adapter/postgres/synthesis_store.go:481)
5. Authoring 以冻结 ArticleRevision 创建 file-patch Proposal；replace 模式先读取实际文件 hash，必须等于 reservation 的 `BaseVersion`。[proposal_creator.go](/Users/zhenglizhi/GolandProjects/zhixu/internal/authoring/adapter/changecontrol/proposal_creator.go:49) Publication binding 保存精确的 `proposal_id` 和 `proposal_revision_id`。[gorm_publication.go](/Users/zhenglizhi/GolandProjects/zhixu/internal/authoring/adapter/postgres/gorm_publication.go:383)

### F 的证据与已生成候选的不可变闭包

1. normal manuscript prepare 在 owner/root 校验后捕获 F；seal、candidate apply 都会重新读取 F 并要求 hash 与 capture bytes 相同，变化返回 `SYNTHESIS_MANUSCRIPT_STALE`。[synthesis_manuscript_store.go](/Users/zhenglizhi/GolandProjects/zhixu/internal/organizing/adapter/postgres/synthesis_manuscript_store.go:95) [synthesis_manuscript_store.go](/Users/zhenglizhi/GolandProjects/zhixu/internal/organizing/adapter/postgres/synthesis_manuscript_store.go:381)
2. `PreviewSynthesisManuscript` 的第二次合并正是已验证可复用的算法：以已发布正文 `P` 为 base、捕获文件 `F` 为 current、机器候选为 proposed；冲突留为可审阅结构而不是写入候选。[synthesis_manuscript_merge.go](/Users/zhenglizhi/GolandProjects/zhixu/internal/organizing/application/synthesis_manuscript_merge.go:58) [synthesis_manuscript_merge.go](/Users/zhenglizhi/GolandProjects/zhixu/internal/organizing/application/synthesis_manuscript_merge.go:119)
3. v2 `SynthesisRevision` 的 SQL trigger 要求其 manuscript、parent、编号、source event、workflow run、model run、delta 都与 exact receipt attempt 相同；closure trigger 还要求它出现在原 processing 的 apply receipt 中，且关联 exact generated article revision。[00119_synthesis_manuscript_storage.sql](/Users/zhenglizhi/GolandProjects/zhixu/atlas/migrations/00119_synthesis_manuscript_storage.sql:133) [00119_synthesis_manuscript_storage.sql](/Users/zhenglizhi/GolandProjects/zhixu/atlas/migrations/00119_synthesis_manuscript_storage.sql:161)
4. 00121 又将 reservation 的 file base、capture、receipt、已发布 parent 和 ArticleRevision 绑为不可变证明。它明确要求新候选的 ArticleRevision 是 document 的最新 revision，并且 reservation 的 `base_version` 等于 capture 的 file base。[00121_manuscript_publication_baselines.sql](/Users/zhenglizhi/GolandProjects/zhixu/atlas/migrations/00121_manuscript_publication_baselines.sql:67)

### 普通 Proposal revision 为什么不能成为答案

1. 普通 revision API 可以预览并追加 `Base / Current(F) / Proposed` 的 Git merge；追加后重读 F，防止 preview 与落库间漂移。[revision_service.go](/Users/zhenglizhi/GolandProjects/zhixu/internal/changecontrol/application/revision_service.go:153) [revision_service.go](/Users/zhenglizhi/GolandProjects/zhixu/internal/changecontrol/application/revision_service.go:230)
2. 它只允许 file-patch、replace、`ready_for_review` 或 `needs_revision` 的当前 revision。已绑定并成功发生副作用的 workflow 是不可编辑的；即使 run 是 terminal，状态机也没有制造 Synthesis/Authoring identity 的行为。[revision_service.go](/Users/zhenglizhi/GolandProjects/zhixu/internal/changecontrol/application/revision_service.go:370) [revision.go](/Users/zhenglizhi/GolandProjects/zhixu/internal/changecontrol/domain/revision.go:74)
3. Authoring publication 与 article revision 的最终闭包要求 ProposalCommit 的 revision ID 等于 binding 的 `proposal_revision_id`，且 result hash 等于 article content hash。[00121_manuscript_publication_baselines.sql](/Users/zhenglizhi/GolandProjects/zhixu/atlas/migrations/00121_manuscript_publication_baselines.sql:157) 所以追加普通 Proposal revision 会使新 revision 无 Authoring binding；沿用旧 binding 则 content/hash/revision identity 不再匹配。
4. 同一个 synthesis revision 不能再映射第二个 generated ArticleRevision：`authoring.generated_article_revision` 对 `(workspace_id, origin_kind, origin_id, origin_revision_id)` 唯一。[00094_generated_authoring_revisions.sql](/Users/zhenglizhi/GolandProjects/zhixu/atlas/migrations/00094_generated_authoring_revisions.sql:15) 因此不能把普通 merge 的正文重新包装为旧 R1 的新 ArticleRevision。

### 已有但不适用的恢复/重试

`RetryProcessing` 启动新 synthesis workflow，但它只由 processing retry store 判定可否重试；其目的是失败处理和已提交 application 的恢复。[synthesis_processing.go](/Users/zhenglizhi/GolandProjects/zhixu/internal/organizing/workflow/synthesis_processing.go:64) 当前 HTTP surface 也只有 processing retry 与 pending manuscript HumanTask 的读/裁决/Resume，没有候选已创建后的 remerge command。[synthesis_handler.go](/Users/zhenglizhi/GolandProjects/zhixu/internal/organizing/http/synthesis_handler.go:531) [synthesis_manuscript_review.go](/Users/zhenglizhi/GolandProjects/zhixu/internal/organizing/http/synthesis_manuscript_review.go:46)

## 最窄可行设计

新增一个专用的“候选 remerge” owner/命令，不修改 `SUCCEEDED` processing 的 retry 语义，也不把 client 内容、普通 Proposal revision 或旧 receipt 伪装为新候选。

1. 命令以 `workspace/note/current candidate revision/expected note version/expected pending publication/idempotency key` 定位唯一待审核候选。owner 锁定 note、generated document、old pending publication、root binding，并验证 R1 是当前 candidate、old publication/Proposal 仍是 pending/ready 且没有任何 writeback side effect。若已经发布、已关闭、版本变化或 F 再次漂移，返回 stale，不替换。
2. owner 从服务器 root 重新捕获最新 F1，复用 `PreviewSynthesisManuscript`。base 仍是 R1 capture 中证明的已发布 P；proposed 是 R1 receipt 中已验证的 machine projection；不得重新调用模型，也不得接受浏览器提交的正文。这样复用现有两阶段 P/F/机器合并和冲突表示。[synthesis_manuscript_merge.go](/Users/zhenglizhi/GolandProjects/zhixu/internal/organizing/application/synthesis_manuscript_merge.go:62)
3. 为 remerge 新增不可变 receipt/attempt 及 `rebase_of` provenance，记录 R1、原 model/generation/semantic identity、原 receipt、F1 capture 与 merge preview。它必须有自己的 SQL closure，而不能插入 `synthesis_apply_receipt` 或篡改旧 receipt。现有 00119 closure 精确要求 revision 出现在原 processing 的 apply receipt，所以直接复用 `synthesis_manuscript_receipt` 结构但不扩展 closure 不可行。
4. 无冲突时 new receipt 直接进入新 candidate；有冲突时由一个专用的 HumanTask/remerge state 持久化逐冲突 ledger，完整 receipt 后再进入 candidate。不能借用当前 HumanReview 的 processing task：该 service 明确只授权 pending/submitted task，并且生成后它要求 `waiting_for_human` 的 run/node。[synthesis_manuscript_human.go](/Users/zhenglizhi/GolandProjects/zhixu/internal/organizing/adapter/postgres/synthesis_manuscript_human.go:144)
5. candidate apply 复用 `appendCandidate` 的 Authoring retirement + append generated revision + reserve publication 顺序：先原子 retire R1 的 pending publication（旧 Proposal 进入 needs_revision，old binding closed），再创建 R2/A2/新 reservation，最后为 A2 创建新的 Proposal P2。P2 是用户对基于 F1 的候选的正常最终审阅入口；R1、旧 capture/receipt、old apply receipt、old proposal/binding 全部保留。
6. 必要迁移建议为新增 `synthesis_manuscript_remerge_attempt`、`..._receipt`、`..._review_decision`、`..._application`（或一个同等 append-only remerge aggregate）及 `synthesis_revision.rebase_of_revision_id`/receipt 外键；替换 00119/00121 的 trigger 函数以增加严格的 remerge 分支，同时保持原 v2 分支逐字段不变。`SynthesisRevision` hash 输入和读取投影需要包含 rebase identity，避免历史显示把“重新合并 F”误报为新模型知识。

不变量：F1 只在 owner 读取；每次 capture/prepare/seal/apply 都 CAS root、note/current revision、authoring document、pending publication 和 F hash；新 R2 必须继承原已验证 model/source/semantic identities并指向 R1；R2/A2/P2 的 content hash 必须相同；old R1/P1 永远不更新；无任何 remerge 路径可以写文件或发布，最终仍经新的 Change Control Proposal 与既有 writeback/deferred guard。

## 真实 PG + Git 验收阶段

1. 在现有 synthesis Git integration fixture 中建立已发布 R0/P0/F0，运行 v2 generation 到 `processing=SUCCEEDED` 且 R1/A1/P1 已创建、P1 为 `ready_for_review`。记录原 processing、model run、apply receipt、R1 receipt/capture、P1 revision 与文件 hash。
2. 用真实受控 workspace 修改 F0 为 F1，确认旧 P1 的常规 approval/writeback 被 base CAS 拒绝，且调用普通 Proposal revision 即便得到 merge preview/new Proposal revision，也无法通过 Authoring publication/synthesis closure。这一段证明缺口而非把该 API 当修复。
3. 调用新 remerge command。clean merge 时断言不新增 model run、不重试或改写 original processing；R1/old receipt/apply receipt 不变；R2/A2/P2 创建；P1 binding 为 CLOSED、P1 Proposal 为 needs_revision；R2 的 rebase provenance 指向 R1 和 F1 capture；P2 base/content 与 F1/remerged content 精确一致。
4. 在 remerge preview 后再次修改 F，提交或 sealing 必须 stale，不得 retire P1 或创建 R2；重发命令以新 idempotency key 后才可按新 F 再次预览。冲突用真实 HumanTask 测试每个 ledger receipt、刷新重放与最终 P2；无冲突仍必须检查 P2 审阅后真实 Git writeback、publication binding 和当前 published pointer。
5. 并发测试：remerge 与旧 P1 approval/writeback、两个 remerge 请求、以及 P2 approval 各自交错。可接受的结果只有一个精确成功的 owner 链；其他请求 stale/busy/replay，不得有两个 current candidate 或两个可发布的 generated bindings。

## 限制

- 未运行真实 PostgreSQL/Git，因此上述是静态调用链与约束结论；第一个实现切片应先写第 1--4 阶段的集成测试验证锁顺序和 trigger 分支。
- 工作树正由其他实施切片修改；本结论没有假设这些未提交改动已通过完整构建或端到端验收。
- 未追踪前端具体候选操作入口；当前后端 HTTP 已验证不存在 remerge endpoint，UI/API 细节应在 owner 设计落地后再定。
