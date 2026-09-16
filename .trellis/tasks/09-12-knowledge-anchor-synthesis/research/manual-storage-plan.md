# 人工正文 v2：存储与发布最小接线计划

2026-09-15，只读调查；未改生产文件，未编写或执行 SQL。沿 `manual-content-integration.md` 与 `manual-content-review.md` 后半入口结论；不重新讨论算法、USER 编辑入口或映射策略。root 负责纯合并服务，impact-ui 负责 domain v2；下面是两者之后的持久化接线清单。名称标“建议”的是待新增接口，不代表已经存在。

## 1. v2 envelope 的落库与重读

| 现有文件/函数 | 最小实施变化 |
| --- | --- |
| `internal/organizing/adapter/postgres/synthesis_models.go`：`synthesisRevisionModel`、`synthesisRevisionColumns`、`synthesisRevisionRecord`、`synthesisRevisionModel.domain` | 增加 nullable Manuscript envelope 字段及列选择；按 renderer version 解码。domain Validate 仅做 envelope.ValidateIntegrity，Content() 做版本分派、完整性/content hash/可信子集校验；parser 映射、source 与 receipt 证明必须由 owner 写入/重读边界另外验证，不能只反序列化 JSON 或把 domain Validate 当作完整映射证明。v1 空扩展保留原路径。 |
| `internal/organizing/adapter/postgres/synthesis_store.go`：`loadSynthesisRevision`、`insertSynthesisRevisionSources` | 前者经 model.domain 取得已重验全文；后者仍只投影顶层可信 Items 的 source 集合，不从 machine_items 或人工 Markdown 链接补齐。 |
| `internal/organizing/adapter/postgres/synthesis_read.go`：`GetSynthesisRevision`、`GetSynthesisNote`、`ReadPublishedSynthesisNote`、`ListSynthesisCandidates` | 全文 reader 使用 revision/snapshot.Content()；当前 Items/历史全文分开返回，候选目录不把 machine_items 重新暴露成可包含项。P 读取必须保留已有 publication 证明。 |
| `internal/organizing/workflow/synthesis_contract.go`、`synthesis_executor.go` 的 `openInput` | 冻结/重新打开携带可校验 v2 版本，准备机器 delta 使用 L 的机器投影，普通 source/body inclusion 目录仍只使用可信 Items；模型结果不能自行携带最终人工全文。 |

SQL 必须是 **118 之后的新迁移**，不改 00095/00116/00118 历史文件：

- `00095_synthesis_notes.sql` 的 `synthesis_revision` 现在 CHECK renderer 只能 v1、item_count 为 1..128。新分支允许 v2 零可信项，并限制 envelope 大小/类型、可信 Items 与 mapping/machine_items 对应、全文 hash 与 Article/receipt 相同；旧 v1 约束原样保留。
- `organizing.validate_synthesis_revision` / `synthesis_revision_validate`：保持 note、parent、计数校验；新增 v2 envelope/merge receipt 身份校验。L 与新版本仍同一 AGENT 链，各编号加一，不需要放宽 parent。
- `organizing.verify_synthesis_closure` / deferred `synthesis_revision_verify_closure`：继续验证顶层可信来源闭包和 apply receipt；可在这里增加 v2 merge proof 闭包。当前逻辑并不要求“全部机器 delta 来源”都成为当前可信来源，不应为了 v2 把 machine_items 混进此表。
- `organizing.validate_synthesis_revision_body_reference`、`project_synthesis_revision_body_references`（00116）：继续仅遍历可信 Items，机器审计项不单独投影正文包含。完整来源关系仍需既有发布证明。
- `learning.guard_note_interview_preparation`、`guard_note_interview_question`（`00097_synthesis_note_interview.sql`）：snapshot 固定 JSON 的构造/比较要按版本显式分支；零可信项在 Go preparation 返回无可用材料，不能改为选 machine_items。

**数据库证明的边界：** PG 不能凭 JSON/hash 证明 Markdown AST 映射合法。最小做法是应用 owner 对最终全文运行 domain 完整性校验及独立 parser/source/receipt 校验，再在同一事务保存不可变 merge receipt 与该确切 envelope/hash；数据库验证两者相等及来源/owner 闭包。不能宣称只加 JSON CHECK 就可独立验证映射；若要抗拥有任意表写权限者伪造整个 receipt，需额外权限/证明机制，非本轮简单 CHECK 已能保证。

## 2. 文件捕获与 merge receipt

建议新增 `internal/organizing/application/synthesis_manual_merge_store.go`（窄 port/命令）和 `internal/organizing/adapter/postgres/synthesis_manual_merge.go`（owner 实现），不把文件读取塞进 model adapter 或 Authoring。

### 捕获持久化的最小选择

首版可在不可变 capture 行存 **受限原字节 bytea + SHA256 + 字节数**，避免额外引入 Artifact staging/提交补偿链。既有设计明确允许“合并记录内不可变快照”。复用 `changecontrol/application.CurrentContentReader` 与 `adapter/localfs/reader.go:CurrentContent` 读取同一文件句柄、UTF-8/1 MiB 内字节并计算 hash；capture 的字节只接受该受信 reader 结果，不能接客户端上传的 F/hash。

capture 至少绑定 workspace、canonical target path、RootGrant/根身份或对应授权 fence 身份、capture ID、content/hash/size、时间；不存在使用独立 absence 分支，不以空 content 冒充。Reader 的 `openSafeTarget` 提供安全路径/根/普通文件检查，但它本身不是新业务入口的授权证明；合并 owner/Worker 组合仍须接现有 workspace runtime grant/READ_LOCAL fence。prepare 与 apply 重新检查授权及 F；数据库事务不能冻结文件系统。

如果 root 选择 Artifact 而非 bytea，可复用 `internal/platform/filesystem/content_store.go` 的 `CaptureBytes`/`ReadArtifactLimited`，但必须同时记录 Artifact owner 元数据、授权读取与失效处理；不要为了省一个受限 bytea 字段重新实施通用内容仓库。两种表示选一种，不并行重复保存多个“权威 F”。

### 持久 attempt / receipt 的建议边界

- 不可变 attempt：workspace/note/processing、L revision/hash/version、Authoring latest ID/hash/no 与 Document.version、P revision/article/publication/proposal_commit 证明、path/capture ID/F hash 或 absence、生成与语义 ModelRun/output/request hash、scope ID/version、merger contract、每阶段 input hash/fingerprint/conflict IDs。
- 不可变 resolution/receipt：attempt ID、阶段编号、前一阶段结果 hash、最终 content hash、envelope/mapping hash、HumanTask/decision 身份及裁决内容。干净结果可有无冲突的合并 receipt，但不代表已获发布审批。
- 用唯一键限制一次 attempt/阶段的确定性提交；重放同键不同输入拒绝。冲突 Candidate 中的 Current fallback 不得作为已解决结果保存。
- 最终 v2 revision FK/owner 闭包绑定 receipt；receipt 绑定机器结果，**不能把人工编辑伪造为新的语义 ModelRun**。冲突 attempt 允许持久但没有 SynthesisRevision、Article、reservation。
- 写 capture/attempt 后，普通进程失败可以精确恢复；apply 原子写最终 receipt（若尚未终结）、revision、Article/receipt、reservation、apply receipt。所有可修改状态与不可变输入分开；旧 attempt 留审计，新基线创建新 attempt。

### P 快照读取位置

复用 `synthesis_read.go:readSynthesisOwnerProjections` 的批量证明查询和 `loadSynthesisRevision`，在新 prepare owner 中按请求 note 集有界读取 L/P，不调用带 reconcile 副作用的整个 `GetSynthesisNote`。`authoring/gorm_generated_read.go:ReadGeneratedDocumentScoped` 核对 latest，但不会替你加载 P 全文。必须沿 P 指针加载其 v2 全文，且证明 P 属于 L 受支持的 generated/synthesis 祖先链。

## 3. Authoring reservation 双基线（不可遗漏 SQL 终态闭包）

最小建议：**保留 BaseVersion 作为文件 CAS 基线**，新增可选的已证明合并发布基线 binding，含 expected published Article ID/hash、capture/merge receipt ID/hash。普通 reservation 无该 binding 时仍沿旧 P.hash=BaseVersion 规则；有 binding 时 BaseVersion=F.hash，DB published CAS 使用保存的 P.ID/hash。这样 Proposal/Safe Writeback 继续读现有 BaseVersion，无需改其写文件协议。

| Go 接线 | 需要做的事 |
| --- | --- |
| `internal/authoring/application/model.go`：`ReservePublicationRecord`、`PublicationReservation` | optional 双基线输入/输出，仅接受 scoped owner 验证过的证明，不暴露“任意 base hash”公共编辑参数。 |
| `internal/authoring/adapter/postgres/gorm_publication.go`：`gormScanReservation`、`gormReservePublication` | 扫描/持久化新字段；锁 document/revisions、验证 P/current、目标 path 与 capture/result 同一 merge receipt；REPLACE 用 F.hash。CREATE_ONLY 仍缺失证明。 |
| 同文件 `gormValidateProposalSnapshot` | 保持 Proposal.content=Article.content、base_hash=reservation.BaseVersion；不因全文合并允许提案独立改版。 |
| 同文件 `gormReconcileOne` | 旧 P supersede 前比较保存的 P.ID/hash，而非用 F.hash；保留真实 proposal_commit、Git、result hash 与精确绑定检查。 |
| `internal/authoring/application/service.go` 的 publication 准备/完成与 `internal/authoring/application/generated.go` scoped port | 新证明通过 reservation 恢复后自然沿已有 Proposal 创建路径传递；重放 hash/幂等必须覆盖新增证明，历史无扩展请求 hash 不变。 |

**额外定位到的 SQL 必改点**（不能只改 Go 的约 211/590 行）：

1. `authoring.validate_publication_reservation_write`，最新定义 `00072_document_publication_reservation_abandonment.sql`；trigger `authoring_publication_reservation_validate_write`。新增证明字段必须不可变，插入时验证引用身份/分支，现有终态/唯一非终态约束保持。
2. `authoring.validate_article_revision_mutation`，最新定义 `00075_document_file_history.sql:413`；trigger `authoring_article_revision_validate_mutation`。P→SUPERSEDED 目前要求 `reservation.base_version=OLD.content_hash`，要改为有证明时校验保存 P 身份/hash，普通分支不动。
3. `authoring.publication_is_exact_current`，定义 `00073_authoring_publication_terminal_closure.sql:256`（当前 schema 仍此函数）；对被 replacement supersede 的旧 Article 同样比较 `replacement_reservation.base_version=article.content_hash`，需双基线分支。
4. `authoring.verify_publication_terminal_state`，最新定义 `00075_document_file_history.sql:620`；deferred triggers `authoring_binding_verify_terminal_state`、`authoring_document_verify_terminal_state`、`authoring_revision_verify_terminal_state`。旧正式版本闭包同样要使用 P 证明。
5. `authoring.validate_publication_binding_write`（00071）与 `publication_is_exact_current` 中 Proposal.base_hash=reservation.base_version 的检查应**保持**：它们验证的是 F 文件 CAS，不该全局替换为 P.hash。`organizing.verify_synthesis_receipt_publications`（00095）继续要求每份候选已绑定确切 reservation。

不修改普通 generated AGENT parent、USER/SYSTEM 拒绝规则、WorkingDraft 或 restore owner。独立 restore/Proposal 改版导致的 owner drift 仍拒绝，不自动吸收。

## 4. 普通融合 / body refresh 共用的全文提交 seam

已确认调用链：`workflow/SynthesisExecutor.Execute` 的 apply 分支 → `application/SynthesisService.ApplyGeneration` → `postgres/GORMSynthesisStore.ApplySynthesisGeneration` → `appendCandidate`。body refresh 和普通 delta 在 ApplySynthesisGeneration 内得出 items 后共用 `appendCandidate`。

建议在应用层新增“已验证机器候选 + merge receipt 引用”的内部 prepared payload，按 note 分组放入 `SynthesisApplyRecord`，不要新增可以直接传 raw content 的公共 Apply API。root 的纯合并服务输出经 domain 完整性和 owner parser/source/receipt 验证后，由该 payload 接入 `appendCandidate` 的 revision/content 构造处（当前约 431–452 行）：

- 原来的固定 `RenderSynthesisMarkdown(items)` 改为无 merge proof 走 v1；有 proof 从重验过的 v2 envelope 取 content/可信 Items。机器 delta/audit items 单独保留，不覆盖 model proof。
- `AppendGeneratedRevisionScoped` 已接受 Content/ProjectionHash，不需要新 merged-parent API；新 parent 仍 L.Article，后续 ContentHash/returned receipt 校验保持。
- 进入 `appendCandidate` **之前**，apply 事务核对 merge receipt、锁并重验 L/latest/Document/P/scope/旧 Proposal 状态及 F hash；保留原来源 fence、BodyRefresh publication fence、候选 CAS、旧 pending publication retirement。
- store 开头的 bindingHash 目前只编码 `{Input,Generation}`；新 prepared receipt 引用必须纳入 v2 幂等绑定，并在 deferred 闭包核对，不能靠独立参数改变 content 而复用旧 apply receipt。旧 v1 哈希结构保留。
- 保持已提交 receipt 的 recovery 在重新读取 F/来源之前；已完成的同键重放恢复原结果，未完成 stale attempt 才拒绝并重建。
- `appendCandidate` 末尾立即 `ReservePublicationScoped`，所以 unresolved conflict 不能在这里返回一个伪候选。冲突必须先停在独立 merge 阶段。

## 5. 最小版本化 HumanWait / 冲突工作台入口

建议新增 synthesis **definition v2**：`prepare → generate → validate → merge_review → apply`。保留 v1 四节点完整注册和旧 run 恢复；新普通融合/body refresh dispatch 选择 v2，模型协议/schema 若未改变不顺带升级。

- `internal/organizing/workflow/synthesis_contract.go:SynthesisRegisteredDefinitions` 注册两张图；`synthesis_executor.go:loadExecution` 当前只认单一 DefinitionVersion，必须显式允许并按版本 dispatch。`adapter/synthesispostgres/store.go` 的 runtime binding 校验也有同一硬编码，不能只加图。
- `merge_review` 持久化 attempt/预览；冲突返回 `workflow/application.HumanWaitResult`，TargetVersion 绑定 attempt 版本，ExpectedInputSchema 是只含 receipt/attempt/fingerprint 等身份的版本化裁决 schema。全部裁决正文在 owner resolve 入口有界校验并持久化。
- `workflow/adapter/postgres/gorm_runtime_human.go:SubmitHuman`/`submitHuman` 和 `application/runtime_human.go:RuntimeHumanCoordinator.SubmitHuman` 已明确提交后节点 SUCCEEDED、激活后继。**后继 apply** 必须重新读取并验证 resolution receipt 和当前基线；通用 HumanTask 提交不能自身授予正文写入权。建议 resolve 先幂等保存 owner resolution，再提交 HumanTask，提交中断可重试恢复；不需要向通用 SubmitHuman 添加合并副作用 hook。
- 两阶段合并发生“先裁决 N，才发现外层冲突”时，不要完成 HumanTask 后试图重入同一个节点：owner preview/resolve 可先处理 stage 1、持久化阶段 receipt 并展示 stage 2；只有所有阶段完成才提交同一个等待任务。stage fingerprint/全部 conflict IDs 各自绑定，不能复用第一阶段确认。多 note 请求也要全体完成后才放行原子 apply。
- `adapter/synthesispostgres/processing.go:PrepareSynthesisApplication` 与 `model_steps.go:VerifyValidatedSynthesisGenerationScoped` 已绑定现有 `synthesis.apply` 节点；保留该后继 kind 以减少改动，新增 merge 节点只接新 owner store。新图仍须同步数据库中的 workflow/model binding 版本门禁，不能在冻结旧 graph 上插节点。
- 复用 `web/src/features/business/ProposalRevisionWorkbench.tsx`（不是 features/changecontrol 路径）的 reducer、三方 diff、冲突确认、stale 展示；当前 333/426 行直接调 `previewProposalRevision`/`appendProposalRevision`，抽窄 preview/submit transport 与 owner-neutral result callback。新增 synthesis owner HTTP preview/resolve（扩展现有 `internal/organizing/http/synthesis_handler.go` 的 owner 路由）及 API DTO，不调用普通 Proposal append。
- resolve 之后只是 v2 待审候选，正式发布仍原 Proposal/Approval；HumanWait 裁决不是发布 approval。新权限/路由只复用已有 READ_LOCAL/相应审阅能力，不凭 task ID 绕 workspace/actor 校验。

## 可分配的实施文件批次与验收

1. **存储契约**：`synthesis_models.go`、新 synthesis manual merge owner/port、新迁移的 revision/envelope/capture/receipt 闭包，v2 reader 与 Interview snapshot 分支。使用已通知的 Manuscript *SynthesisManuscript（JSON omitempty）、SynthesisRendererVersionV2=synthesis-markdown/v2、SynthesisRevisionSchemaV2=synthesis-revision/v2 及 Content()；不复制映射算法，不把无参 Validate 扩成 owner 校验，v1 常量与黄金 hash 不变。
2. **双基线发布**：Authoring `application/model.go`、`gorm_publication.go` 及上述四个 SQL guard/closure，新 reservation proof verifier；普通 generated append 不放宽。
3. **共用 apply / runtime**：`synthesis_service.go`、`synthesis_store.go`、`workflow/synthesis_contract.go`/`synthesis_executor.go`、`synthesispostgres/store.go`/`processing.go`；依赖 root 纯合并服务与前两批。
4. **审阅 transport**：上述 Workbench、synthesis HTTP/OpenAPI/API client、worker/API composition。复用 HumanWait 基础设施，默认不改通用 Workflow 表或写回算法。

最小真实验收：P 已发布、L 未发布、F 含人工文本时合并发布 v2；对比 F 新文件、Article、API 历史全文 exact hash，重读映射/可信来源一致；同处冲突无 revision/reservation，裁决后再 apply；resolve 后 F/P/L/scope 任一变化拒绝旧结果；F≠P 发布后旧 P 正常 supersede、所有 SQL deferred closure 成功。另保留 v1 固定 hash/replay、零可信项和非法 envelope 拒绝。以上为实施验收计划，**本次未运行、不记录 PASS**。

## Runtime 接线补充（root 代码核对，尚未实现）

`adapter/synthesispostgres/model_steps.go:VerifyValidatedSynthesisGenerationScoped` 目前要求 execution 行已有 `ApplyNodeRunID/ApplyNodeAttemptID` 且不是 ApplyRecovery，再以 `synthesis.apply` 调用 bindLive。因此新增 `merge_review` 不能直接复用此整个 fence 当作准备阶段的模型证明：它尚无 live apply reservation。后续应复用不可变 generation/semantic journal 的精确验证部分，并分别核对当前 merge_review、HumanWait 裁决或 apply 的执行许可。

119 Prepared 保存的 generation/model/source 身份是历史事实；不能要求旧 merge_review attempt 在用户裁决后仍 RUNNING。生产 Proof/Receipt API 需要显式、类型化的当前执行或人工裁决许可，不能从客户端或普通 context 值凭空推导。初版119只提供必需的依赖端口和明确 fixture，不据此认定跨节点恢复已经实现。

## 121 实施校正：publication_is_exact_current 的真实范围

实施方重新核实00073：旧P `base_version=旧content_hash` 的检查位于迁移一次性 DO 历史校验块，并非当前 `publication_is_exact_current` 函数本体。121 保留该函数对真实 Proposal/commit/F 的检查，仅前向替换00075的 supersede mutation guard与terminal closure，并增加独立不可变merge baseline guard。上文列出的“第四个函数必改”是调查阶段误归属，不应据此无意义重写该函数。121以候选Article的不可变receipt/capture/P投影推导发布基线，不增加客户端任意base hash输入。
