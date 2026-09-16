# 人工内容保留：窄范围架构复核

状态：**设计建议，未实施**。2026-09-15 只读核对两份 integration 研究与下列具体代码；未修改生产代码、00117/00118，未执行测试，不表示 PRD 完成。代码由其他 agent 同时推进，行号对应本次读取时的工作树。

## 推荐表示：v2 全稿 + 可证明的未改写条目映射

选择 versioned full manuscript，不选择“每个 item 的 manual override + raw blocks”。后者若要保留标题、任意插入位置、删除/移动、断裂标记、换行与文件尾字节，仍需完整的顺序/分块树与组装协议；对现有完整文件 Git merge 而言增加了第二个编辑模型。全稿只增加一种内容事实和一个窄的可信映射校验器，不建立通用 Markdown AST 编辑器。

建议 v2 SynthesisRevision 增加可空 `Manuscript` 扩展，包含：

- `content`：唯一最终全文，UTF-8 原字节；`ContentHash=SHA256(content)`。Markdown 标题也是这些字节的一部分，已有 Note.Title 仍是目录元数据，不能反向重写全文标题。
- `machine_items`、`machine_title`：本轮通过既有模型/来源核验的机器投影，用原 v1 renderer 得到精确 `machine_content`，保存其 hash。它是下一轮机器更新的基线，**不是本篇已发布全文的语义投影**。
- `mappings`：按全文字节偏移排序的 `{item_id,start,end,block_hash}`；仅指向通过下面规则证明未改写的完整机器块。
- `manual_changes`：新增/改写/删除/移动/歧义的范围及相关旧 item ID、基线范围、状态；范围无正文时允许删除记录。正文不再复制到 overrides。绑定捕获 hash 和 review/merge receipt；不把用户提供的标签当成来源证明。
- `merge_receipt_id/hash`：冻结三方输入、来源模型结果、owner 人工父版本、工作区采集事实、scope 及最终审阅决定的不可变证明。

**保留顶层 `Items` 的可信语义：v2 Items 只等于 mappings 对应的 machine_items 子集，按机器投影顺序排列；可为空。** 人工改写内容只在全文和 manual_changes 中，旧来源只作为历史对照。这样现有 BodyReference SQL、来源闭包、Interview 的 Items 消费不会无意把旧机器文案当作当前全文。新字段 machine_items 只能由生成/合并内部入口使用，不能放进普通候选 item/source 目录冒充已发表条目。这比让 Items 继续包含失效文本、再要求每个旧消费者记得过滤更安全。

机器 Delta 作用于旧 `machine_items`（v1 回退为 Items），得到新 machine_items；最终 Items 是映射校验结果，不要求等于机器 Delta 的全部输出。两层语义必须命名和分开校验。未改动项维持原 ID；用户删掉或改写的机器项不因下一次渲染重新出现。需要改动这类项时仍通过三方合并及审阅，不直接替换全文。

## v1 和证明边界

1. `RenderSynthesisMarkdown` **不改字节协议**；`ComputeSynthesisRevisionHash` 按 renderer/schema 分派，v1 使用冻结的旧字段结构与旧 JSON 次序，拒绝非空 v2 扩展。不能给 v1 hash 输入直接增加一个默认 null 字段。旧 request hash、receipt replay、旧 snapshot JSON 继续原路径。新 v2 hash 包含完整稿、机器投影、映射、人工状态、合并证明及已有身份字段，Hash 自身仍排除。
2. 新 `RevisionMarkdown`/等价读取函数：v1 调原 renderer；v2 返回已校验 content。ArticleRevision.content、ContentHash、ProjectionHash 绑定、Proposal.content、实际文件/导出/阅读历史均来自同一结果。禁止 reader 用 v2 Items 重建全文。
3. 映射只接受服务端已知 machine_items ID：完整开始/结束标记各唯一、不嵌套、非代码围栏/代码块/引用内，闭合范围与原 renderer 对该项输出逐字相等。校验实际 Markdown 块边界和外围上下文，不能单凭 marker 搜索或子串 hash。标记重复、伪造、移入不同章节、边界断裂、块内批注/来源链接/适用条件改写都取消该项映射；原文照存。保守地将难以界定的结构变更所覆盖章节，或整篇，降为待复核，不丢文本。
4. 字节相等只证明“该完整机器块原文仍在”，不证明新人工上下文不改变含义。人工段落声明“以上均无效”等无法靠 marker 判断。干净 merge 也需用户审阅全文差异；未审的新全文不成为已发布上游。涉及上下文的条目需明确审阅定位，无法判定时不映射；审核发布不能自动恢复改写项的 verified 状态。
5. 人工部分显示“人工内容／未验证为原来源结论”；改写项给出旧机器全文、人工文本、新机器建议和旧来源的历史对照。旧链接仍可作为普通链接出现，不显示成修改后结论的证据徽标。只读来源跳转可沿旧 revision 打开，不把历史 ref 加入当前有效来源闭包。
6. 人工段落不参与自动 body inclusion、补证/缺口统计、Interview/语义 claims。用户接受发布不等于来源核验；今后只有经现有来源采集、owner span 校验、独立语义核验生成的**新**机器项才可成为可信项，不能通过 UI 勾选让人工旧项重新冒充 exact upstream item。本轮无需新增这种“人工转可信”的功能。

证据：`internal/organizing/domain/synthesis_render.go:15` 是固定渲染；`synthesis_revision.go:18,59,71` 现在强制全文为该渲染；`synthesis_model.go:200,224` 无全文扩展。`synthesis_body_reference.go:32,54` 以 Items 完整复制/深比较证明包含。`atlas/migrations/00116_synthesis_body_references.sql:39,75` 同样从 Items 验证、投影关系。`internal/review/interview/application/note_preparation.go:273` 消费 snapshot.Items。

## 合并、owner、CAS 与发布

### 输入与状态

保存一个有界的不可变 merge attempt：机器基线 revision/hash、人工 ArticleRevision ID/hash/no 与 Document.version、最近已发布身份/hash、当前受授权目标路径及文件内容 artifact/hash（或缺失 token）、新 machine projection/model result/hash、anchor ID/scope version、git merge contract。另存最终全文 hash、人工决定、conflict IDs、preview fingerprint。幂等键包含 attempt 身份；旧键不同输入必拒绝。

正常三方：Base=旧 machine render，Current=最新完整人工稿，Proposed=新 machine render。v2 下一次也如此：Current 包含上一轮已保留的人工作品，Base 仍来自该轮保存的 machine_items，避免拿机器全文与混合全文比较时删掉批注。没有新人工编辑时 Current 取已保存的 v2 content。模型只生成机器差异，不接收全文的编辑权限。

**人工 ArticleRevision 和外部工作区文件是两个可能分叉的编辑源，不能选一个覆盖另一个。** 若两者不同且都偏离其可证明共同祖先，先用同一个 ThreeWayMerger 合并这两个人工分支（保存两阶段各自 fingerprint/冲突）；再做机器三方。必须能证明双方共同基线；不能以最新 AGENT 草稿假冒磁盘编辑祖先。无共同基线时进入现有手动冲突工作台明确裁决。未保存的浏览器编辑缓冲不在服务器能力范围，继续使用已有 dirty-buffer 提示。

干净合并只形成待审预览。冲突时保存预览和结构化冲突，**不创建可发布 SynthesisRevision/Authoring receipt/reservation**。`gitcli.parseMergeOutput` 返回的 Candidate 在冲突处包含 Current，并不表示冲突已解决。审阅提交重跑相同冻结合并，验证 fingerprint/全部 conflict IDs；最终人工裁决全文再次进行映射校验，即使它等于机器块也不能仅据 equality 接纳新来源。

### 复用 Change Control 而非绕过绑定

复用 `ProposalRevisionWorkbench` 的三方内容、diff、冲突确认、编辑器、stale 交互；为 preview/submit 注入 owner-specific transport（当前组件直接依赖通用 Proposal API）。合并前还没有合法绑定的 synthesis Proposal，所以不能直接调用现有 `PreviewProposalRevision` 假装已有 Proposal，也不能在已绑定 Proposal 上单独 `AppendProposalRevision` 后沿用旧 SynthesisRevision。

新增窄的 synthesis owner preview/resolve 入口，复用 `gitcli.ThreeWayMerger`（可经 gitmerge adapter）和冲突/fingerprint 展示格式；fingerprint 额外涵盖上述 owner 基线。resolve 后，在既有工作流的待审恢复/应用边界提交冻结机器结果和审阅结果，原子创建 v2 SynthesisRevision + 对应 Authoring revision/receipt + apply receipt；随后走现有 reservation/Proposal/审批/Git 发布。冲突预览可以复用编辑 UI，但此时不是可执行发布提案。模型来源、ModelRunID 不因人工裁决重新伪造；单独 merge receipt 标记人工步骤。

### 严格的人工父版本入口

普通 `AppendGeneratedRevisionScoped`、`gormVerifyGeneratedParent` 和 `PrepareGeneratedRevision` 保留原约束。新增 owner 专属 merged append 命令/准备函数：核验 generated document origin、原机器 receipt、当前 latest ArticleRevision 及合法人工保存来源、冻结 merge attempt、结果全文/投影 hash，并在同一事务记 merge proof 和新 generated receipt。该入口允许经证明的 USER 父版本，不回填伪造 USER 的 generated receipt。ArticleRevisionNo=latest ArticleRevisionNo+1；SynthesisRevisionNo=上一 SynthesisRevisionNo+1，两者不能再假定一起加 1。

证据：`internal/organizing/adapter/postgres/synthesis_store.go:330` 的 appendCandidate 目前拒绝 owner latest 偏离；`internal/authoring/adapter/postgres/gorm_generated.go:14,149` 和 `internal/authoring/domain/generated.go:100,122` **两层**都限制 generated 父版本。`atlas/migrations/00095_synthesis_notes.sql:167` 还强制两条修订链各只差 1，v2 需按 merge proof 校验 Article 跨步，不能只改 Go。

### 文件基线与发布身份分别冻结

目前 `gormReservePublication:211` 取旧已发布 Article 的 hash 为 BaseVersion，`gormReconcileOne:590` 再校验这个等式。它不能直接发布“已捕获且合入候选的外部文件改动”。对有 merge proof 的预约新增双基线：previous published revision/hash 用于正式指针 CAS；captured workspace hash 用于 Proposal 的文件替换 CAS。完整正文/hash 校验仍原样严格。普通预约保留原逻辑；proof 必须包含受授权路径、内容 artifact 与关联 merge result，不接受客户端裸报一个 hash。

预览→resolve：重读 latest Article、Document.version、Synthesis current ID/hash/version、scope、文件 hash，并在事务锁内重验 owner/DB 事实；resolve→审批/发布：再次核验 owner 最新状态与文件基线。新文件变化由执行时现有文件 CAS 拒绝；新的 USER revision 即使未改磁盘，也必须使原提案 stale，不能只靠 Git 文件 hash。审批授权绑定确切 proposal revision/change hash，不能沿用旧批准。

DB 与文件不能做一个 ACID 事务；接受预览后落库到正式 write 之间可能变化，但执行边界必须再次比较并拒绝，而不是称冻结采集是原子文件锁。失败保持正式指针不变，沿现有 retirement/cancellation/recovery 规则处理，不能把仍运行的旧发布提前标成功。

rebase 建立新 attempt，读最新两个编辑分支和 scope，重新合并、分类与审阅；旧 attempt 只供审计。若只有人工/文件基线变且来源/机器基线/scope 相同，可复用冻结机器结果，不重跑 Provider；若机器目标或 scope 变则重新 prepare/核验，不能把旧 Delta 不加检查套上新基线。

## 精确接线清单与保持不变的旧契约

| 边界 | 延伸现有位置 | 最小变化 |
|---|---|---|
| 版本/全文/语义 | `ComputeSynthesisRevisionHash`, `SynthesisSnapshotFromRevision`, `SynthesisNoteSnapshot.Validate`; `synthesisRevisionModel.domain`, `synthesisRevisionRecord` | v2 envelope、全文 reader、映射与零可信项；冻结 v1 serializer |
| 生成和输入 | `SynthesisExecutor.prepareInput` (`internal/organizing/workflow/synthesis_executor.go:182`), `SynthesisGenerationInput.Validate`, `ApplySynthesisGeneration`, `appendCandidate` | 区分 machine 更新目标与公开可信 Items；冻结合并与 scope；冲突不走候选提交 |
| owner | `AppendGeneratedRevisionScoped` 附近新增 merged 专属入口；普通 `gormVerifyGeneratedParent` 不放宽 | proof/replay/CAS；文章编号跟实际 USER 父版本走 |
| 发布 | `gormReservePublication`, `gormValidateProposalSnapshot`, `gormReconcileOne` (`gorm_publication.go:120,433,504`) | 有证明的双基线和 latest fence；最终 Article/Proposal/文件同文校验不变 |
| 合并 UI | `PreviewProposalRevision`, `AppendProposalRevision` (`revision_service.go:155,233`) 的协议与校验方式；`ProposalRevisionWorkbench.tsx:294` | 复用组件和 merger，新增 owner transport；不能独立更新绑定 Proposal 内容 |
| 数据约束 | 后续**新迁移**延伸 `organizing.validate_synthesis_revision`, `verify_synthesis_closure`; `learning.guard_note_interview_preparation`, `guard_note_interview_question` | v2 字段/零 Items、双链编号、snapshot JSON 版本分支；机器来源审计与当前可信来源闭包分开 |
| 消费/读取 | `IncludeSynthesisPublishedItem`, `MatchesSynthesisBodyItem`, `ReadPublishedSynthesisNote`, `OpenSource`, `ListSynthesisCandidates` | 顶层 Items 仍只暴露可证明原文；v2 全文/人工差异与可信索引分别显示；历史 ref 从旧版本打开 |

`verify_synthesis_closure` 当前要求 revision 与 apply receipt/model/source 对齐（00095:296）；新合并提交必须继续满足，不能另建无 receipt 的 v2 修订。`machine_items` 的旧来源证明由冻结机器结果/原 revision 审计，不混入当前 `synthesis_revision_source`。00116 的“完整已发布 item 引用”与来源相同不等于正文包含的原则不变；117 不改。旧 SQL 文件不重写，迁移编号待当前 118 稳定后分配。

Interview snapshot 也含完整正文 hash。v2 snapshot 应携带足以验证全文和可信映射的扩展，但模型材料仅选可信 Items；不能把全稿作为已有来源事实塞入 prompt。SQL 00097:59 构造固定 snapshot，00097:175 用减去两个字段的方式构造引用，新增 envelope 时需要显式 version 分支，避免新字段泄漏到历史 note_source 比较。

## 全稿来源/锚点范围

`prepareInput:305-338` 通过 `ReadSynthesisAnchorAdmission` 冻结现有 scope/version、接受的源 span，并从 Items 收集来源。v2 可以从 full content 提取主题/来源链接**候选提示**并在现有审阅里展示，必须把提取器版本、输入 content hash 和范围偏移绑定到 attempt；不因 Markdown 链接存在就扩充 AllowedSources 或自动接纳 anchor。只有既有 owner 读取、scope admission 与用户确认流程能升级范围。最小首版无需新增抽取器：全文人工差异可见、scope 不变、范围疑点进入待复核，避免把语言识别启发式变成自动授权。

## 最小有意义验证（建议，未执行）

一个真实 PG + Git + HTTP 审阅流程，复用既有独立 synthesis smoke 的固定 Provider 与临时 workspace，不重跑117：

1. 发布含“索引/事务”片段的 v1；保存真实 USER revision，加入事务批注并改写一个管理项；在磁盘另加一段独立批注。更新索引来源后生成合并预览，确认三方显示实际字节及人工分类。审核并发布 v2 后比较 Git 文件、ArticleRevision、API 全文/历史的 exact hash；事务、两条人工分支文本逐字保留，改写项不在可信 Items/当前正文引用表，下游 Include 和 Interview 均不能将其作为原项选择。
2. 同一测试场景分支让双方改同一行，验证有真实 Git 冲突、无可发布候选/正式指针变化；裁决后再审。预览后另存 USER、候选后改文件分别使旧操作 stale；rebase 后保留最新文本，旧审批不可复用。
3. 少量纯契约检查：固定旧 v1 JSON/hash/replay 样本完全不变；重复/伪造/围栏内 marker、不变标记内改字、全项删除→零可信 Items；v2 全稿/映射任一改字都校验失败。数据库直接伪造映射/owner proof 必拒绝。此处通过才可声称对应边界已验证，编译/lint 不能替代真实流程。

## 待实现前收敛的风险与独立切面

- 最大产品选择是人工 Article 与工作区双分支的共同祖先捕获：已有事实不足时必须明确冲突，不能默认其中一方获胜。应在实现前确认保存/发布流程提供哪条共同祖先身份；本次未追踪整个 Working Draft 历史链，不能宣称现有接口已提供。
- 最小映射器需要 Markdown 块边界识别；先检查既有 Markdown 解析能力再选用，不自建编辑器。重排/上下文语义无法由 exact bytes 证明，保守隔离会减少可自动包含项，这是已知代价。
- “全部人工改写/删除后零可信项”是全稿契约的必要情况，需要允许 v2 空 Items 并让 Interview 返回无可用项，而不是退回旧机器内容。machine_items 仍受原数量/来源上限；全文还受 Git merger 的 1 MiB 限制（`merge.go:18`），超限明确失败，不截断。
- 独立先行切面：①冻结 v1 hash 样本与纯 v2 envelope/mapping 契约；②Authoring merged-parent proof 与双发布基线接口设计。两者可在契约确定后独立实施。③工作流 apply/merge 接线依赖前两者；④复用 UI transport/全文 reader 依赖稳定 DTO。当前 body-refresh agent 持有 domain/app/workflow/执行和118，UI agent 持有摘要 HTTP/web/newread；本轮只交此文档，不并行侵入这些文件。

## 补充复核：现有编辑入口与最小磁盘基线算法（2026-09-15）

**本节收窄并替代上文把 USER/磁盘双编辑分支、merged-parent 入口与 Article 跨步作为首版必需项的建议。** 未发现现有产品可将 synthesis generated document 打开为 WorkingDraft 并保存 USER ArticleRevision；无需为本 PRD 新增这种产品功能。最小实现沿“受授权工作区文件读取 → 合并预览/裁决 → 完整稿候选 → 既有 Proposal/Approval/Git 发布机制”。这里复用的是发布机制，不是绕过绑定后继续使用旧 Proposal 身份；新全文仍必须绑定新的 Synthesis/Article/Proposal 候选。

### 可达入口的证据

| 入口 | 已核实行为与证据 | 对 generated note 的意义 |
|---|---|---|
| 创建/编辑/保存文章 | `internal/authoring/http/handler.go:80,93,241` 仅有 create/list/get/update/freeze，create 解码空请求；`application/model.go:363,369,380` 的命令均不接受 DocumentID。`application/service.go:45,171` 创建空 Draft，Freeze 的候选 DocumentID 由服务端生成 | 无“从已有 document 创建 Draft”公共入口 |
| Draft 绑定 | `adapter/postgres/gorm_repository.go:112` INSERT 不含 document_id，`:179` autosave 不改它；`:311` Freeze 只从现有 Draft.DocumentID 读 parent，否则采用新 document ID；`:349` 保存绑定。`domain/model.go:252,294` 同样只允许未绑定 Draft 新建 Document，或沿原绑定追加 | 用户不能通过标题/目标路径把 Draft 接到 generated Document；同路径不是同一 Document 身份 |
| Generated 创建 | `adapter/postgres/gorm_generated.go:18` 明确不建 WorkingDraft；`atlas/migrations/00094_generated_authoring_revisions.sql:90` 要求新 generated origin 无旧 Article、无绑定 Draft | 也不存在先建用户 Draft 再将该 Document 接为 synthesis origin 的常规绕行 |
| 编辑器恢复 | `web/src/features/authoring/AuthoringPage.tsx:67` 最近草稿链接为 `/authoring/new?draft=...`；`NewDocumentPage.tsx:168,188` 创建空 Draft 或读取指定 Draft，DocumentID 从返回 Draft 获得 | 恢复已有 Draft/重读 autosave 冲突，不是从 generated note 恢复编辑 |
| 合成笔记页面 | `web/src/features/synthesis/SynthesisNotePage.tsx:56,57,77` 只有候选查看、Proposal、文件历史入口 | 没有打开 USER 编辑器的链接 |
| Proposal 编辑 | `internal/changecontrol/adapter/postgres/gorm_revision.go:274,294,302` 写 ProposalRevision、base snapshot、lineage 和 Proposal.current_revision_id；不写 Article 或 WorkingDraft | 是真实人工修改入口，但修改的是提案，不产生 USER Article；`synthesis_read.go:215` 已把绑定 Proposal revision 偏移显示为 conflict，不能把它作为 owner 已接纳的全文 |
| 历史恢复 | `DocumentHistoryPage.tsx:131,425` 预览并创建 restore_document 提案；`internal/documenthistory/application/service.go:234` 重读历史 Git blob、拒绝 dirty/stale，再交 Change Control | 不是任意编辑器，但**会改变 Article owner 链**，见下一段 |

不能笼统声称“非 AGENT Article 不可达”：`internal/authoring/adapter/postgres/gorm_restore_publication.go:43,101,122` 在 restore Git 成功后创建 **SYSTEM** ArticleRevision，parent 是原已发布 revision，revision_no 是 latest+1，并移动 published pointer。它不是 USER 保存路径，也不获得 synthesis generated receipt。00094 的 closure 只对 AGENT 插入要求 generated receipt（`:179`）。因此首版看到 SYSTEM/USER/latest 不匹配仍按现有 owner drift 拒绝，不应把历史恢复后的磁盘正文偷偷重新归属给旧 synthesis projection。上述恢复路径是静态确认，未进行运行测试。

搜索本次范围内所有生产 `authoring.working_draft` 插入与 document_id 更新，只有上表 Create/Freeze 两处；`generated_revision_integration_test.go:204` 的 USER freeze 用的是另建 `user.md` Document，不是 generated Document 上可达的 USER 编辑用例。因此上文建议的“先保存 generated note 的真实 USER revision”测试前置不成立，首版验收应改为真实工作区文件编辑。直接改 DB 构造 USER parent 只能测试拒绝，不能证明产品功能可达。

### 必须区分的三个基线

- **L（latest）**：当前 synthesis revision 与 Authoring latest AGENT revision，负责机器增量、generated parent 和 DB CAS，可能尚未发布。
- **P（published）**：Document.current_published_revision_id 对应的已证明发布全文，负责解释当前工作区文件相对哪份实际交付正文发生了变化。不能从 candidate.PublicationID 为空推断没有 P。
- **F（file）**：受授权路径当前文件的精确字节及 hash，负责人工输入和最终文件 CAS；它不是 ArticleRevision，也不自动成为可信来源。

`ReadGeneratedDocumentScoped` (`gorm_generated_read.go:15,34,38`) 返回 latest 和 **latest 自己的** publication；它的 Document 携带 published pointer，但不会顺便加载 P 的正文。已有 `GetSynthesisNote` (`synthesis_read.go:239,274`) 分别读 CurrentRevision、PublishedRevision；`readSynthesisOwnerProjections:168` 用 generated receipt + published binding + proposal_commit/Git/result hash 证明 P。这是可复用的读取事实，合并 prepare 需要同样的有界 owner 快照接口，而不是在事务内调用会先触发 reconcile 的整个页面 GetNote。

**错误例子：** P=`A`，未发布 L=`A+B`，磁盘 F=`A`，新机器稿 N=`A+B+C`。若直接 Merge(Base=L, Current=F, Proposed=N)，B 被解释成人工删除；它可能干净消失或导致无谓冲突。正确外层 Base=P，F 没有 B 因为 B 从未交付磁盘，不是人删了 B。

### 可实施的最小算法

1. **冻结 DB 身份。** 读取 L、Authoring latest/document version、P 的确切身份/全文/hash/发布证明、当前 proposal/reservation 状态、机器输入/source/scope。要求 L 的 Article 就是 latest、AGENT 与同一 generated receipt 相符；P 若存在必须有可证明的 synthesis publication，且在 L 的受支持 generated/synthesis 祖先链中。未知 USER/SYSTEM、Proposal 被独立改版、进行中的发布/恢复冲突，沿现有 conflict/retirement 门禁处理，不能当作新人工分支吸收。
2. **捕获 F。** 复用 `internal/changecontrol/adapter/localfs/reader.go:104` 的 `CurrentContent`：安全 Workspace/路径、普通文件、UTF-8/限长、同一字节快照算 hash。将原字节保存为内容 Artifact 或合并记录内的不可变快照，绑定 Workspace/path/hash/捕获身份；不创建 WorkingDraft 或 USER Article。
3. **先形成候选分支 N。** 新机器 delta 仍作用在 L 的机器投影得到 Mnew。v1 的 L 纯机器，可直接取 Render(Mnew)。v2 若 L 含人工全文，先用同一个 merger `Base=Machine(L), Current=Full(L), Proposed=Render(Mnew)`，让 L 已有人工内容保留到 N；冲突停在审阅，裁决成 N 后才能继续。此阶段的 Base 是机器投影，**Current 不是磁盘 F**。没有实际机器变化时 N=Full(L)，不要重建丢掉人工部分。
4. **再接磁盘分支。** P 存在时用 `Merge(Base=Full(P), Current=F, Proposed=N)`；P 的 v2 全文包含上次已发布人工内容，所以不得用 Render(P.Items) 代替。若 L=P，可直接在 `Base=Full(P), Current=F, Proposed=N` 执行；F=Full(P) 时直接得到 N。若前一未发布 v2 候选已捕获部分 F 编辑，而磁盘又继续修改，仍允许这个保守 P 基线产生真实待裁决冲突，不静默选一侧；首版不额外实现“找最佳文件 merge base”的历史系统。
5. **首次发布特例。** P 不存在且目标路径确实缺失时，保留原 CREATE_ONLY/absence token，N 为待审候选。P 不存在但已有文件是路径碰撞；P 存在而文件缺失是外部删除/漂移。均不把缺失当空正文做三方并重新创建，不认领无祖先的现存文件。
6. **冻结结果与再次核验。** 保存各阶段输入 hash、contract/fingerprint、冲突及裁决、最终全文 hash，映射/可信来源校验由完整稿契约负责。干净/已裁决也只得到待审候选。apply 时重读/锁定 L、latest、Document.version、P pointer、scope 和相关 Proposal 状态，重读 F hash；新 Article parent 仍为 L.Article，不是 P，也不是 F。先按原规则退役旧未批准候选，再原子记录 v2 projection/generated Article/receipt 和应用结果。
7. **发布与 stale。** 后续仍用原 Proposal→Approval→Safe Writeback→Git→finalizer；预约绑定旧正式 P 与已捕获 F 两个事实，文件替换比较 F.hash，正式指针比较 P 身份/hash。审阅后 L/P/scope/F 任一变化旧结果失效；重新读取产生新 attempt、重新合并和审阅。模型输入未变可复用机器结果，但不能复用旧合并 fingerprint/审批。文件级 CAS 与 DB owner CAS 都保留。

最小必要的新增 provenance 是 **“磁盘采集 + 机器/发布双基线 + 合并裁决证明”**，不是“人工 Article 父版本合法化”。人工审阅文本必须成为新 v2 Article 的同一全文；不能通过只修改 Proposal 内容绕过 generated projection/content hash。现有 Proposal 的 `resolveRevisionBase` (`revision_service.go:403`) 已要求持久 base snapshot 与 source proposal base hash 一致，缺快照且当前不同就拒绝；应复用这一原则与 UI，而非用 L 临时冒充文件共同祖先。

### 因此首版无需修改的契约，以及仍需修改的边界

**无需新增或放宽：** WorkingDraft create/update/freeze、generated note 打开编辑功能、USER 分支采集/双人工分支合并、`PrepareGeneratedRevision` 的 AGENT parent 限制、`gormVerifyGeneratedParent`、00094 的 generated-parent FK/receipt 链、普通 `AppendGeneratedRevisionScoped` 的 parent/replay 形状。未知 owner drift 继续拒绝。L/new 都沿一条 generated 链追加时，Synthesis/Article 各加一的 00095 parent 编号检查也不需要为人工跨步修改；不用新增 merged-parent 专属 append API。上文第一个独立切面中的 v1/v2 完整稿契约仍有效，第二切面应缩为“文件采集证明与发布双基线”，移除 USER owner 迁移。

**仍需接线：** v2 全稿与可信映射/读取；P 的受证明内容读取和 F 持久快照；合并 preview/resolve 到新 owner 候选的接线；对有证明文件编辑的 reservation 双基线及 finalizer 相应校验（旧 `gorm_publication.go:211,590` 的单一等式不能直接覆盖 F≠P）；对应新迁移而非改旧迁移。Proposal/Approval/Git 引擎与正文一致性要求保持，旧绑定内容变更仍必须走新候选，不称“现有任意 Proposal 编辑已经满足”。

本节只读验证入口和祖先选择；未修改118/core/UI、未测试、未实现完整稿表示，也未宣称整个 PRD 完成。
