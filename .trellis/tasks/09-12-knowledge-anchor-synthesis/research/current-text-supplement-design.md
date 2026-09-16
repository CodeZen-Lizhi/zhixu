# Research: 当前全文补充来源的独立复核与恢复

- Query: 人工改写后，如何只在新来源确实支持当前全文时完成纯补源，并恢复已保存的 `SYNTHESIS_MANUSCRIPT_SOURCE_REVIEW_REQUIRED`？
- Scope: internal；已有框架复用、持久 owner、查询展示与最小恢复协议；不修改生产代码、测试、SQL 或任务规划文件。
- Date: 2026-09-15
- Decision input: `prd.md:57` 与 `research/historical-supplement-recovery.md` 最后“用户已决定”段。用户已选择 AI 重核当前全文，无须再次询问。以下“提议”是实施合同，尚未实现或实测。

## Findings

### 1. 结论与最小实施边界（提议）

新增一个窄的 **当前正文来源复核 owner**，使用现有 PostgreSQL/River Workflow、配置的 ChatModel、RecordingChatModel 和 Eino StructuredRunner。它读取原处理的真实 generation/semantic 历史证明，再独立调用一次新复核 ModelRun，核验完整当前正文、段落和真实来源。成功只写外置证据及补源完成 receipt，不写 SynthesisRevision、ArticleRevision、Note version、正文文件、发布指针或旧 Items。

最低可交付路径：现有 `audit_duplicate` 的 **已存在 v2 note + ADD_FACT 精确重复 + 只增加来源**。允许同一次 generation 包含多个这样的目标，但必须完整枚举全部目标/补源义务；任何目标涉及新 note、正文/标题/条件/冲突/GAP 改动或无法证明的 operation，整个原 processing 不得标为“补源完成”。本切片不能把“只复核其中一个 target”当完整恢复。

不解除 `SynthesisMachineDelta` 的旧 guard，不在旧 `synthesis_model_step` 追加第三个 stage，不改旧 v1/v2 工作流图。采用新固定 definition `organizing.synthesis-manuscript-source-review@1`：`prepare -> review -> apply`。新 dispatcher 从精确可恢复的旧错误创建一次请求，因此未来和存量 `SOURCE_REVIEW_REQUIRED` 走同一恢复入口；不会重新运行旧 generation/semantic，也不重发 source-ready。

原 processing/workflow 的失败历史保持真实；独立 `source_review` 的成功 receipt 表示“该次纯补源已恢复完成”。HTTP/UI 同时返回原执行状态和该 owner 的当前有效结果，不把原失败日志改成成功，不把有失败历史等同于当前仍未恢复。这一方案避免碰正在由 candidate-remerge-owner 修改的通用 manuscript 文件及 00125。

### 2. 已核验的主证据与相关规范

| 文件与位置 | 已核验事实 / 对设计的约束 |
| --- | --- |
| `application/synthesis_manuscript_runtime.go:20–49` | MachineItems 只作更新基线；未可信旧项发生 source 变化即报 SOURCE_REVIEW_REQUIRED。解除 guard 不形成新证据 owner。 |
| `domain/synthesis_delta.go:63–74,150–167` | 重复 ADD_FACT 按精确语义键归并，保留旧 ID/文本/条件；`Changed=false` 与 `SourcesChanged=true` 可区分纯补源。 |
| `adapter/agent/synthesis_semantic.go:79–98,132–145,235–264` | 原审查对生成的 assertion 与各 source 给 SUPPORTED；ADD_SUPPORT 只能命中可信 Items。原审查不含人工当前全文，不能作为新全文 proof。 |
| `application/synthesis_model_execution.go:65–141` | 现有 model step 有 durable fence、原始 accepted output、业务绑定和恢复状态。 |
| `adapter/agent/synthesis_model.go:168–263,308–337` | 先查 READY；记录每次模型调用；Eino 有界执行；原始输出与 owner 结果持久化；丢响应先精确查回，不重复 Provider。 |
| `adapter/synthesispostgres/model_steps.go:487–562` | 原 owner 严格要求同 workflow **恰好两条** READY step，并核对独立 generation/semantic、真实 ModelRun/Call。直接加 stage 会破坏旧 proof。 |
| `adapter/synthesispostgres/manuscript_model_proof.go:43–63` | 已有只核验不可变历史的 `VerifySynthesisManuscriptHistoryScoped`，不冒充旧节点仍有运行许可。适合恢复读取原双模型事实；新 workflow 另需 live fence。 |
| `adapter/agent/synthesis_goal_selection_model.go:97–152` | 已有单 ModelRun consumer 模式；`InitialStructuredRequest` 的完整 ChatRequest hash 在 Claim 前固化，随后 RecordingChatModel + Eino。可按此实现薄 adapter。 |
| `application/synthesis_supplements.go:12–55`；`adapter/postgres/synthesis_supplements.go:71–118` | 旧账本挂到可信 item/slot；写入只遍历 base.Items，v2 空 Items 时不能补写。 |
| `atlas/migrations/00100_synthesis_source_supplements.sql:3–59` | 旧表 FK 到旧 apply receipt，trigger 从 revision.items 验真，追加不可变。新全文证据应独立表，不放宽旧 trigger。 |
| `domain/synthesis_manuscript.go:70–81,194–210`；`adapter/manuscript/mapper.go:18–21,41–44,84–107` | 全文/机器审计分离；人工变化使旧 Items 不可信。Goldmark 已在项目内用于字节坐标/AST 验证，但旧 Mapper 不是新证据段落分割器。 |
| `adapter/postgres/synthesis_read.go:79–85`；`http/synthesis_wire.go:191–205` | generation 只保留匹配可信 Items 的旧补源；display 单独读取 v2 全文。新证据不能混回 generation 的 Supplements。 |
| `adapter/owner/synthesis_manuscript_root.go:27–85` | Root ID 只是审计身份；读取必须真实 Resolver Resolve/Revalidate。当前文件由受控 reader 读取。 |
| `adapter/postgres/synthesis_manuscript_baseline.go:149–221` | 当前 Note/Article/Document、P 的 publication/ProposalCommit/Git/祖先链已有 owner 校验参考。不可调用其整套 `readOwners`：末尾又会运行触发 guard 的旧 MachineDelta。 |
| `adapter/synthesispostgres/processing.go:383–417`；`terminal.go:110–125` | 旧 Retry 只允许 FAILED + retryable；不支持这个 RECOVERY_REQUIRED。不能仅加 retry 按钮。 |
| `workflow/synthesis_contract.go:36–78` | 已有模型节点自动重试为 0，prepare/read 有界重试；旧 graph/version 不应改写。 |
| `application/synthesis_manuscript_review_service.go:16–35` | `SynthesisManuscriptCaller` 保存真实认证 ReadLocal + WriteProposal capabilities，零值拒绝；可复用命令授权模式，不借用已结束 HumanTask。 |
| `atlas/schema.sql:798–849,20487–20491` | 正式 schema 的 `agent.guard_model_run_mutation()`、`agent_model_run_final_result_type_check`、`agent_model_run_retrieval_binding` 都含 schema/result allowlist；新复核 ModelRun 必须由 00126 同步扩展并绑定实际 RUNNING review。 |

以上 `application/`、`domain/`、`adapter/`、`http/`、`workflow/` 路径均相对 `internal/organizing/`。

已读相关 specs：`.trellis/workflow.md`、`.trellis/spec/backend/synthesis-notes.md`（00122 trust/fence/纯补源、00124 完整 manifest）、`eino-structured-scheduler.md`、`organizing-contract.md`、`database-migration-compatibility.md`。当前 spec 的 SOURCE_REVIEW_REQUIRED guard 继续成立；新增的是该错误的正式恢复 owner，而不是宣布 guard 不再需要。

### 3. 准入和义务集合（提议）

`Prepare` 必须由 owner 从原 `processing_id + workflow_run_id` 重读冻结 input、原始 generation、独立 semantic、成功 ModelRun/Calls。复用只读历史 proof；不接受 HTTP 传来的 generation JSON、accepted boolean、item/段落/source hash。

自动初次请求的唯一键为 `(workspace_id, origin_processing_id, origin_workflow_run_id, attempt_no=1)`。准入要求原 processing 的当前 run 正是该 run，真实终态为 RECOVERY_REQUIRED 且错误码精确匹配，原 apply receipt 不存在，无未决原 ModelRun，原 generation/semantic 均真实成功。

在原冻结 base 的 MachineItems 上调用纯 `domain.ApplySynthesisDelta`，只作候选分类，不生成权威 Items。逐个检查 generation 的所有 notes：存在于冻结 input、v2 baseline、结果 `Changed=false`，并证明无 title/aliases 等其他正文变化。最低切片只支持重复 ADD_FACT；其它 operation 返回明确 `SYNTHESIS_SOURCE_REVIEW_SCOPE_UNSUPPORTED`，不丢弃。从 old/new FACT 来源差集得到全部义务 `(note, old audit statement fingerprint, exact incoming source tuple)`；至少一条，按稳定键排序。审计 item ID 只保存在内部 origin 说明，不作为当前段落 ID，也不进入外置支持记录的信任身份。

每一条义务只有一个 source tuple，因此不存在“总体通过，但某个新增 source 未通过”。新审查的语义是：该 source 在完整人工上下文和当前 scope 下，确实支持选定当前段落中的相关陈述、条件和冲突语境。旧生成的 assertion 可作标明“待验证”的定位线索，不作为可信答案；只支持旧 assertion、当前段落已否定/限制该 assertion、支持的是不相干段落，均不通过。

当前源准入每次通过现有 source/anchor owner：exact Source/Version/Artifact/Projection/Span/content/excerpt hash、AVAILABLE、真实关联已接受及当前 scope。无锚点分支重用“冻结 nil 且 note 锁后证明仍无锚点”。scope 改变不能默认放大；重新准备时读当前已批准 scope。新 source version 不允许替换旧 tuple，旧请求返回 STALE_SOURCE，由正常新版本处理建立新义务。

### 4. 当前全文与段落身份（提议；必须写死，不能模糊为“latest”）

新增 server-only `SynthesisManuscriptSourceReviewSnapshot`，每个 target 至少包含：

```text
workspace_id, note_id, document_id
target_kind: REVISION | LOCAL_FILE
base_revision_id, base_revision_hash, article_revision_id
note_version, document_version, published_revision_id?, publication_id?
root_grant_id, root_fingerprint, workspace_binding_version, target_path
full_content_hash, full_content_bytes   # exact UTF-8，无 NUL，不归一化换行
observed_file_exists, observed_file_hash
scope identity/version/hash + accepted source bindings
paragraph_schema = synthesis-source-review-paragraphs/v1
paragraphs: [{ordinal, start_byte, end_byte, paragraph_hash, kind}]
snapshot_hash                          # canonical envelope hash
```

全文保存在此 owner 的不可变审查快照，沿用 manuscript capture 已有“为精确恢复保存受控文件快照”的理由；不能进 Workflow output、ModelRun/Call、日志或错误消息。来源原文不另存副本，重开不可变 source artifact 后核 hash。以上身份来自 owner，不能暴露为客户端可修改的证明字段。

当前文本 resolver 的最小确定规则：

1. 当前 L 是已发布版本 P：文件存在时以受控 F 为实际当前全文。若 F 与 L 相同可用 `REVISION`，不同则用 `LOCAL_FILE` 快照，`base_revision_id=L` 仅说明基线，**不声称 L 的正文等于 F**。纯补源不会保存/发布 F 成为新 ArticleRevision。
2. 当前 L 是未发布 v2 candidate：以用户候选视图中的 `L.Content()` 为全文，但必须证明当前 F 等于生成 L 的 manuscript receipt/capture 中的 F（或已正式发布的 L 本身）。沿 `revision.manuscript_receipt_id -> receipt -> attempt.capture_id -> capture` 精确读取。F 在 L 之后又变化，则返回 `STALE_MANUSCRIPT_BASELINE`，先使用已经单独实施的 candidate remerge 形成当前完整稿，再重新复核；不能由补源流程自造一个 F/L merge 或忽略新增手改。
3. 尚无 P 的合法 v2 candidate 使用 L；原规则要求存在的已发布文件缺失、根许可失效或 Authoring owner 不一致，不能静默退回历史正文。

这保留“当前全文”的实际边界，也避免把未发布 L 丢掉。`LOCAL_FILE` 证据必须在自己的快照/当前文件补证面板呈现，不能套到页面上仍显示的旧 published revision。未发布 L 和新 F 已分叉时，补源保持未完成，复用现有 remerge 入口。

段落用已有 Goldmark 的 AST 生成服务端目录，新增独立 mapper 方法/文件，不改变旧 `Mapper.Assess`：首版选文本 Paragraph/TextBlock 的准确 UTF-8 byte range；标题、列表/引用父级、相邻内容仍在完整全文中作为上下文。段落边界/hash、有序 ordinal 和全文 hash 均由服务器重算。HTML、仅标记、代码等不具备当前支持合同的节点不变成目标。重复同文用绝对 `[start,end)` + ordinal 消歧，不能 `strings.Index(quote)` 命中第一段就通过，也不能拿浏览器 UTF-16 offset 作 Go byte offset。

模型只选择 `N001/P00001/S001/O001` 等服务器标签，不返回权威 UUID、hash 或自由坐标。为降低“只支持长段落一小句却替整段背书”的误述，首版 SUPPORTED 要求选中的完整段落陈述及明确条件均受该 source 支持；不能完整支持时保持 UNCERTAIN。支持更细句内范围需要后续明确合同，不能隐式截取。

### 5. 模型 wire、独立性和预算（提议）

新增 Prompt `synthesis-manuscript-source-review/v1`、Schema `agent.synthesis-manuscript-source-review/v1`、ResultType `synthesis_manuscript_source_review`。原 generation/semantic prompt/schema 不改。继续使用当前配置的同一 approved ChatModel/Profile 与其 ModelSettingsRevision；独立的是实际运行/节点/调用身份，不要求不同模型厂商或新增选型。

输入包含：当前完整全文（每个 target 原字节、仅一份）、服务端段落目录、当前 scope/goal、全部实际来源原文、精确义务标签与待验证生成线索。不存在 audit MachineItems 的可信列表。模型指令明确完整全文中的人工批注、否定、条件和未决冲突有约束力；只支持编辑前说法不能通过；不输出正文改写建议。

严格输出形状示例：

```json
{"checks":[{"obligation":"O001","source":"S001","targets":["P00003"],"verdict":"SUPPORTED","reason_code":"CURRENT_TEXT_SUPPORTED"}]}
```

checks 必须与全部义务一一对应且按冻结顺序，source 必须等于该义务 source，targets 只能选择对应 note 的目录且无重复。SUPPORTED 必须至少一个 target；UNSUPPORTED/UNCERTAIN 可无 target。reason_code 固定 allowlist（如 CURRENT_TEXT_SUPPORTED / CURRENT_TEXT_CONTRADICTS / CONDITIONS_UNSUPPORTED / NO_CURRENT_MATCH / UNCERTAIN），不接受未知/重复/缺失/尾随字段。任何一条非 SUPPORTED，则记录真实已完成模型输出但业务状态 REJECTED，无“补源完成” receipt；不走 schema repair 请求模型把合法拒绝改成通过。

采用 **一条新的独立全文复核 ModelRun**：必须不同于原 GENERATE 和 VALIDATE，拥有新 workflow/node/attempt 身份。段落目录为确定性数据，故无需再造一次定位模型。原来的两条 ModelRun 证明补源意图和来源候选；新的第三条证明当前全文支持。若未来用模型生成段落提案，则还需独立审查该提案，此切片不引入该额外模型环节。

Claim 前用 `InitialStructuredRequest` 构造完整 ChatRequest 并 hash，包含实际 prompt/schema/profile/model/消息；不能只 hash 任务 JSON。model owner 使用真实 RecordingChatModel -> StructuredRunner -> 已有 Eino scheduler。完成时 same transaction 保存 accepted bytes/output hash 与 model result，并调用 scoped ModelRun finalizer；拒绝结果仍是成功执行的 ModelRun，业务结果是 REJECTED。

proof verifier 在 transaction 中独立读取 ModelRun/Calls，验证 workspace、workflow/node/attempt、ModelSettingsRevision、精确 prompt/schema/result type、1–3 次 phase 有序成功调用、首次 request hash、最终 response bytes/hash 与 accepted output 一致，再重新 decode/bind 标签及段落。再核原两份历史 proof。纯 JSON/hash 校验或 caller 的 `accepted=true` 都不够。

预算沿用现有配置与硬上限：`StructuredCallLimit=3`（INITIAL/REPAIR/REDUCED）、默认累计 2 MiB request/response、64 Ki tokens、2 分钟；模型节点 Workflow 自动 retry=0。原 Runner `MaxStructuredInputBytes=512 KiB`，而 manuscript 上限 1 MiB，因此必须先验证 **全文+目录+来源** 完整请求可以装下；超限返回可见 INPUT_TOO_LARGE，不截断全文/人工注释/来源，不偷偷新增 map-reduce 或替代模型。每个显式执行 deadline 使用既有 30 分钟 owner budget；显式已知失败最多 10 次（复用 synthesis 限额语义），恢复未知调用不消耗新调用机会。上述预算值见 `internal/agent/application/runner.go:16–60`、`workflow/synthesis_contract.go:25–26`。

### 6. 固定工作流、状态和恢复（提议）

| 步骤 | 必须读取/写入的 owner 事实 | 重放/失败 |
| --- | --- | --- |
| dispatcher | 有界扫描精确 SOURCE_REVIEW_REQUIRED；同 UoW 建初次 review 行、固定 RuntimeStartRequest 与 River binding | `(origin processing, origin run, attempt=1)` 唯一；创建丢响应先查原 request；不会一轮失败后下一轮自动建 attempt=2 |
| prepare | 原双模型 proof、完整纯补源 manifest、当前 Note/Article/P/F/root/scope/source；冻结快照/hash/义务 | 纯读取阶段可沿用 read retry=3；快照只冻结一次。损坏/缺目标 fail closed |
| review | 当前 execution fence、冻结 scope/source/text 重验、完整 request hash；Claim + 新 ModelRun/Call，写原始输出与 verdict | Workflow retry=0；READY 精确恢复不再调用 Provider；合法拒绝终止为 REJECTED |
| apply | 当前 execution fence + 独立 model proof + 当前全部 owner/source/text recheck；原子写/复用全部 evidence 与完成 receipt | 只重试读/事务恢复。先查完整 exact receipt；已提交后漂移只影响当前有效性，不抹掉已完成历史 |

行状态：`PENDING -> PREPARED -> RUNNING -> REVIEWED -> SUCCEEDED`。REVIEWED 只表示模型有效输出已持久化，不是补源完成。旁路终态 `REJECTED / STALE / FAILED / RECOVERY_REQUIRED` 分别是语义未证实、快照漂移、已知执行失败、调用/落盘结果未知。所有非成功结果保留错误、version、时间与原 workflow 身份；UI 显示“尚未完成补源”。成功必须全 manifest 至少一项且每项有正式 evidence，不能用零目标/零证据成功。

显式 retry/recheck 创建 successor review（新 ID、attempt_no、supersedes_id、独立 workflow），不清空旧 attempt/output/hash。只允许最新的 FAILED+retryable、或 STALE 且已有新 owner baseline；同一 snapshot 的 UNSUPPORTED/UNCERTAIN 不自动抽奖重试。已知业务拒绝在用户修改正文/来源/已批准 scope 后才能重新准备；新 source version仍走正常 source-ready，不改绑旧请求。精确相同的成功 snapshot/source/scope 可复用已有 owner-verified evidence，记录 REUSED proof 引用，不重复追加或付费调用。

RECOVERY_REQUIRED 不提供普通 retry：优先查 exact frozen output+ModelRun/Calls+结果 receipt；完整可验证时完成本次 finalize/apply；缺少已接受输出时不能从 response hash 反推内容或再次叫模型。对原 SOURCE_REVIEW_REQUIRED 的自动恢复只因为旧双模型已经完整且 **这个新复核尚未调用过**，不是放宽所有未知调用恢复。

状态投影分开：`execution_status` 是 review 持久历史；`effective_status` 为 CURRENT / STALE_TEXT / STALE_SOURCE / STALE_SCOPE / UNAVAILABLE / UNVERIFIED。只有 `execution_status=SUCCEEDED && effective_status=CURRENT && 全义务 evidence 闭合` 才能在当前页面显示“补充完成”。后续刷新、重开、源删除/新版、全文或 scope 漂移均重新计算有效性；保持旧成功记录并显示“当时通过，当前需重新核验”。一次命令重放返回原结果的历史事实，同时附带最新 effective_status。

### 7. 持久化与 SQL 闭包（提议；预留 00126，未写 SQL）

最小采用三张新增表，命名以 `synthesis_manuscript_source_review` 为前缀，不修改 00100 的旧可信补源表、不修改 00125：

1. `organizing.synthesis_manuscript_source_review`：一行一个 attempt；workspace/origin processing+run、attempt_no、supersedes_id、request hash、scheduled/current workflow、state/version、sealed snapshot+manifest payload/hash、模型 request/run/node/attempt/settings身份、原始 output/hash、result/receipt hash、failure/timestamps。冻结字段只能 NULL→一次 seal；终态 payload/身份不可变。`UNIQUE(workspace,origin_processing,origin_run,attempt_no)`，active/uncertain attempt 对同 origin 的部分唯一索引防并发重跑。幂等 dispatcher 不因终态而忘记初次请求。
2. `organizing.synthesis_manuscript_source_review_evidence`：只保存 SUPPORTED 的 append-only 关联。workspace/note/base revision、target kind、snapshot/content/scope hash、段落 ordinal/byte start/end/hash、完整 source tuple、first_review_id、proof ModelRun/output hash、created_at；无 item_id 信任入口。`UNIQUE(workspace,note,base_revision_id,target_kind,full_content_hash,scope_hash,start_byte,end_byte,paragraph_hash,source_version_id,parse_projection_id,source_span_id)`，并核对冲突行全部 artifact/source/hash。same key different tuple/hash 报 consistency conflict，不能 `ON CONFLICT DO NOTHING` 后当证明成立。
3. `organizing.synthesis_manuscript_source_review_command_receipt`：workspace+idempotency_key 唯一，操作 INITIAL/RECHECK/RECOVER、原 request/version/hash、结果 request ID/result hash。只存身份；不可变。创建与 Runtime start 在同 UoW；同 key 异参 409，同 key 同命令先返回 winner。

review 的 final result 只保存完整义务→evidence IDs/proof review IDs 的有序 manifest 与 receipt hash（不伪造旧 synthesis_apply_receipt）。新 evidence 可指向当前 REVIEWED review；DEFERRABLE constraint 在提交时要求 first_review 已 SUCCEEDED、output 全 SUPPORTED、真实 ModelRun成功、全 range/source/hash 完整绑定。全复用时本 review 可没有新 model_run，但 result 必须指向具备真实成功新复核模型的先前 evidence/proof，不能引用原 generation semantic 充数。

FK/trigger 必须覆盖：同 workspace origin processing/execution、note/document/revision、self predecessor、workflow/run/node/attempt、source/version/artifact/projection/span（复用现有 `validate_synthesis_source` 的完整元组规则）、ModelRun、evidence first_review。完整 source tuple 不靠 client hash。快照 bytes/hash、range UTF-8 边界/hash、目录 ordinal 再由 Go owner 重建 AST 验证；SQL JSON/hash 不是语义证明。

已进一步核验正式 `atlas/schema.sql`：00126还必须扩展 `agent_model_run_final_result_type_check` 和 `agent_model_run_retrieval_binding`，以及 `agent.guard_model_run_mutation()` 的 INSERT schema allowlist；新schema分支必须要求精确 RUNNING source_review claim、同 workspace/run/node/attempt/settings、固定v1 prompt/schema、无retrieval/memory绑定。不能只扩展Go枚举而留下数据库拒绝，也不能为新schema放开全部无检索ModelRun。

同一个 apply transaction 的锁序沿现有 owner 习惯：live workflow fence/processing → review → 按 note ID 的 Note/Document → source/scope；复用 scoped ports，禁止嵌套另一个 UoW。最后重读 F hash并重验根许可，再写证据/result。文件与 DB 不能形成跨资源事务：这是对观察时刻的复核，write 后若文件立即被外部修改，后续查询必须检测到 STALE_TEXT，不宣称永久有效。没有文件写操作需要 rollback。

数据库应拒绝 terminal/replay payload 改写、跨 workspace FK、结果缺义务/缺 evidence、UNSUPPORTED evidence、无真实新 ModelRun 的 first proof、段落/source重绑定；源/正文变化只改变读投影，不 UPDATE/DELETE 证据。迁移需前向保留旧恢复行，Atlas checksum/schema由主集成者统一更新。候选 00126 若被其他已获授权 owner 占用则顺延，不能覆盖。

### 8. Application/API/DTO 和可观察闭环（提议）

server ports（参数只示关键绑定，具名 command/result 放新文件）：

```go
type SynthesisManuscriptSourceReviewStore interface {
    EnsureInitialScoped(context.Context, foundation.TransactionScope, SourceReviewOrigin) (SourceReview, bool, error)
    Freeze(context.Context, workflowapp.ExecutionContext, foundation.ID) (SourceReview, error)
    ReadModelInput(context.Context, foundation.ID, foundation.ID) (SourceReviewInput, error)
    Claim(context.Context, workflowapp.ExecutionContext, ClaimSourceReview) (SourceReview, error)
    CompleteModel(context.Context, workflowapp.ExecutionContext, CompleteSourceReviewModel) (SourceReview, error)
    Apply(context.Context, workflowapp.ExecutionContext, foundation.ID) (SourceReviewResult, error)
    Get(context.Context, foundation.ID, foundation.ID) (SourceReviewView, error)
    Recheck(context.Context, RecheckSourceReview) (SourceReview, bool, error)
    Recover(context.Context, RecoverSourceReview) (SourceReviewView, error)
}
```

Application 的纯模型 input/receipt 不包含 Eino/GORM 类型；上面 typed ExecutionContext 接线按现有 workflow adapter模式放置，不把运行许可藏到 context。实际实现可把以上 workflow methods放 workflow port，纯 Application service只依赖与之相容的项目 command。

HTTP 路径（统一 workspace 路由；必要最小集合）：

```text
POST /api/v1/workspaces/{workspace_id}/synthesis/processing/{processing_id}/source-reviews
  {expected_processing_version, origin_workflow_run_id, idempotency_key}
  -> 202 {review, replayed}；只启动精确错误恢复，和自动 EnsureInitial 汇合
GET  /api/v1/workspaces/{workspace_id}/synthesis/processing/{processing_id}/source-reviews
  -> {origin_processing_id, items, next_cursor}；完整最新恢复事实
GET  /api/v1/workspaces/{workspace_id}/synthesis/source-reviews/{review_id}
  -> SourceReviewView（见下）；不调用模型
POST /api/v1/workspaces/{workspace_id}/synthesis/source-reviews/{review_id}/recheck
  {expected_version, idempotency_key} -> 202 {review, replayed}
POST /api/v1/workspaces/{workspace_id}/synthesis/source-reviews/{review_id}/recover
  {expected_version, idempotency_key} -> 200 {review, replayed}；只恢复持久证明
GET  /api/v1/workspaces/{workspace_id}/synthesis/notes/{note_id}/source-review-evidence
  ?revision_id=...&cursor=...&limit=...
GET  /api/v1/workspaces/{workspace_id}/synthesis/notes/{note_id}/source-review-evidence/{evidence_id}/source
  -> {evidence, availability, text, snapshot_text?}
```

已核对 `http/synthesis_handler.go:78–108`，以上 `/api/v1/workspaces/{workspace_id}/synthesis` 是实际现有外部前缀。不新建另一套 HTTP auth/分页/Problem；幂等键按现有manuscript命令的body合同处理，若同时提供 `Idempotency-Key` header必须唯一且与body相同。POST 必须真实 ReadLocal+WriteProposal capabilities与工作区目标校验，GET 读权限；不绑定 HumanTask，不要求用户人为确认每个 AI段落。400严格解码、404跨scope、409版本/幂等/不可恢复、503依赖错误，稳定 error code + retryable语义沿用现有规范。

`SourceReviewView` 必含 `id/workspace_id/origin_processing_id/origin_workflow_run_id/attempt_no/version/status/effective_status/retryable/failure/workflow_run_id/created_at/completed_at/targets/obligation_count/supported_count/receipt_hash?`；targets 给确切 `note_id/base_revision_id/target_kind/full_content_hash`、paragraph excerpt/range/hash、当前证据 IDs及每义务 verdict。`completed` 不允许客户端根据 workflow成功或 supported_count>0推导；用 owner完整闭包状态。模型输入、旧 audit Items、原始模型输出、root path/凭据不暴露。

主笔记页面增加独立“当前正文补充来源”区：展示目标 snapshot/段落摘录、有效性、来源打开。paragraph标签/range可定位所选精确全文；LOCAL_FILE用自己的当前文件快照查看器，不能跳旧 `synthesis-item-*` 锚点。原冻结来源/历史来源继续各自现有语义；reviewRequired全局标志不因一个段落有支持被清空。

源打开复用 `OpenSynthesisSource` 的完整 tuple和 `availability/text/snapshot_text` 行为。源变旧/删除保留验证过的历史快照，标记当前无效；隔离/损坏不显示字节。GET证据默认按请求的确切 revision/content匹配，历史proof可显式展示为“当时通过”，不可自动迁移到内容相同的新 revision。

前端 query keys带 workspace/note/review/revision/hash；POST丢响应保留同key并GET恢复，不能发新key重复模型；PENDING/PREPARED/RUNNING/REVIEWED轮询，终态停止；source/text失效重新 GET会更新effective_status。列表/最新 processing增加可选source_review恢复摘要或通过独立查询组合，原status保留在执行详情；只有完整CURRENT proof才显示“补充完成”，其它明确显示原因/恢复入口。

### 9. 文件边界与派发顺序（提议）

**source-review 实施者专属新文件**（均以本主题前缀，减少并发覆盖）：

- `internal/organizing/application/synthesis_manuscript_source_review.go`、`..._validation.go`、`..._service.go`：snapshot/义务/receipt/status、server服务。
- `internal/organizing/adapter/manuscript/synthesis_manuscript_source_review_paragraphs.go`：独立段落AST目录；旧 Mapper语义不变。
- `internal/organizing/adapter/agent/synthesis_manuscript_source_review_model.go`、`..._catalog.go`、`..._wire.go`：现有runtime薄consumer、完整request proof和strict输出。
- `internal/organizing/adapter/postgres/synthesis_manuscript_source_review_store.go`、`..._baseline.go`、`..._proof.go`、`..._read.go`：现有owner/scoped ports组合、新账本、段落/原文投影。
- 如需借用 synthesispostgres 内部 historical recovery reader，只在其包新增 `synthesis_manuscript_source_review_origin.go`，复用已有history verifier；不编辑其 `model_steps.go/processing.go`。
- `internal/organizing/workflow/synthesis_manuscript_source_review.go`、`..._dispatcher.go`；`cmd/worker/synthesis_manuscript_source_review_components.go`：独立definition/executor/有界恢复dispatcher。
- `atlas/migrations/00126_synthesis_manuscript_source_review.sql`：后续实施才创建。当前研究只预留编号。

**主集成者顺序合入的不可避免共享接线**：`internal/agent/domain/output.go`与`runtime.go`的新schema/result allowlist；worker静态与热模型catalog/definition/executor/terminal hook/周期dispatch组合；API composition/routes/OpenAPI/生成客户端；`http/synthesis_wire.go` processing恢复摘要；web API/query、NoteContent旁的新Evidence组件。已核验存在的agent.model_run SQL结果/schema allowlist由00126扩展，不修改已发布迁移。`atlas.sum`、schema导出由主集成者统一生成。

**明确不争抢** candidate-remerge-owner 的 `application/synthesis_manuscript*.go` 既有通用文件、domain general files、postgres general manuscript files、00125与formal125 fixture。上述源复核新文件可平行写，公共worker/HTTP/web接线由主agent串行收口。需要导出只读helper时先由该文件当前owner交付，不能两个agent同改。

建议交付顺序：A. 新contract+段落+model wire/consumer+00126 owner闭包；B. 固定workflow与存量/未来统一dispatcher；C. 主agent接HTTP/web及静态/热组合；D. 独立Go/SQL审查与一次完整source_review浏览器流程。不要把 A 的单测成功报告为 C/D完成。

### 10. 最小有效验证与可复用夹具（提议；未执行）

复用 `adapter/postgres/synthesis_manuscript_runtime_integration_test.go:50–56` 的正式迁移+真实PG/River/Git/root/owner夹具；`audit_duplicate`在325–334构造精确旧FACT，688–697已断言真实RECOVERY_REQUIRED、4次原Provider、v2空可信Items、零新revision。保留这份legacy guard断言，在新 `synthesis_manuscript_source_review_runtime_integration_test.go` 从同样持久错误继续跑新definition；新增formal126夹具/helper，不能把另一owner的formal125全局替换。

最小行为矩阵：

1. 第五次真实Recorded固定Provider收到完整人工全文、当前scope和准确新原文，回SUPPORTED；得到一份证据+完成receipt；L/Article revision数量、Note version、P、正文F bytes/hash和可信Items都与补源前完全一致；GET/刷新/打开段落与source仍一致。
2. 新原文只支持旧结论，当前全文否定它；或人工条件限制它。分别REJECTED/UNCERTAIN，零补源证据/完成receipt；不能仅靠fixture返回SUPPORTED就声称语义质量已验证。固定Provider断言请求/合同与排除规则，真实模型质量另行验收。
3. 两段同文、中文/emoji、CRLF、嵌套列表/引用：选定ordinal对应准确UTF-8 `[start,end)`/hash；未知label、越界、重排、遗漏一义务、跨note target拒绝；多target少一条不完成。
4. 同request并发/丢响应、同source重处理：winner唯一，重复不新增新ModelRun/evidence；同key异参409；旧workflow迟到不能覆盖新attempt。Reuse proof仍独立验原ModelRun且检查当前有效性。
5. review前与apply前各注入全文/F、SourceVersion/span、scope、root、Document/Note version变化：无当前有效证据，不自动改绑/重跑；成功后同样变化保留历史成功但GET显示STALE。
6. cancelled/expired lease、ModelRun未成功、原semantic伪造、response hash/bytes漂移、model claim未知：不可完成；READY/output精确落盘后的丢响应可恢复，Provider次数不增加。现有 `adapter/agent/synthesis_model_test.go:167–232,238–291` 和 `adapter/synthesispostgres/runtime_recovery_integration_test.go:102`有对应替身/故障模式可复用。
7. 真实新workflow启动、Worker重开、热模型重建仍注册consumer；存量SOURCE_REVIEW_REQUIRED初次恢复一次，周期dispatcher不对REJECTED/未知自动再叫模型；输入大于完整Runner上限为可见失败，无截断通过。
8. Web最小流程：原“需恢复”processing → AI复核 → 精确段落来源打开 → 刷新仍补充完成 → 修改全文/来源后刷新变待核验。LOCAL_FILE与旧published正文不混标；REJECTED页面没有“已补充”。

按变更运行受影响Go定向测试/vet、独立实库migration/closure、OpenAPI一致性、Web类型与定向测试；实库范围使用正式126目录，历史119/124/125专用用例仍保留原目的。无必要全仓测试扩张。

### 11. 外部参考 / 版本

- 已核验仓库 `go.mod:7,30`：`github.com/cloudwego/eino v0.9.13`、`github.com/yuin/goldmark v1.8.4`。复用依据来自上述实际consumer和mapper代码，不依赖新版本API或新框架选型。
- 官方源码目标：[Eino v0.9.13](https://github.com/cloudwego/eino/tree/v0.9.13)、[Goldmark v1.8.4](https://github.com/yuin/goldmark/tree/v1.8.4)。本次外网打开未完成而中止，未据此声称核验任何额外API；实现若需要当前项目未使用的AST方法，应读vendor/对应版本官方源，不猜方法签名。

## Caveats / Not Found

- 此文是可执行设计与边界建议，未编辑生产/测试/SQL、未跑新增行为测试、未证明真实外部模型判定质量。现有 `audit_duplicate` 只证明旧guard与原调用链，不证明新恢复已存在。
- role隔离规则禁止读取 implement.jsonl/check.jsonl，因此已直接读用户/主agent指定的 PRD、design、implement相关切片及历史research，未读这两份manifest。
- 当前代码没有本设计的新owner/DTO/表。现有模型step不能直接拿来塞第三stage，旧supplement不能挂空可信Items；这两处必须实体隔离，不能靠schema boolean补丁。
- 首版明确处理纯ADD_FACT audit duplicate。其它operation、混合正文变化、1MiB全文超过当前512KiB完整模型input，以及未发布L与新F再次分叉，均必须保持可见未完成并走相应已有修订/重合并流程；不能扩大补源成功语义来掩盖这些边界。
- 00125与通用owner文件正在由另一实施者编辑；行号为本次静态读取时的位置。00126只是本设计建议预留，正式实施前由主agent确认未被占用。
