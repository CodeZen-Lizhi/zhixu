# M6-02 产品与 Agent 契约研究

## 1. 结论摘要

M6-02 的产品价值不是“让模型能返回 JSON”，而是建立一个可验证、可追踪、可替换的 AI 决策边界：模型结果必须经过任务专用 Schema、Evidence、Faithfulness、冲突和拒答校验后，才能作为 Relation Assessment、回答或后续 Proposal 候选进入系统。该任务直接支撑“判断新旧知识关系”和“有来源地回答问题”两条核心价值链（`docs/product/PRD.md:97-105`, `docs/product/PRD.md:123-125`, `docs/product/PRD.md:167-178`）。

当前任务目标已经点名结构化输出、Schema Repair、五分类、引用/Faithfulness、冲突、拒答和模型版本，但 Requirements 与 Acceptance Criteria 仍为 TBD（`.trellis/tasks/07-19-agent-structured-output-citation/prd.md:3-13`）。父任务将 M6-02 定义为 M5-05 与 M6-01 之后、M6-03/M6-04 之前的 P0 Agent 内核，验收关键词是 RAG eval、schema repair、citation/refusal test（`.trellis/tasks/07-16-product-delivery/implement.md:47-51`）。因此本研究建议把 M6-02 限定为“Agent 领域/Application 契约 + 直接模型 Adapter + 校验器 + 确定性测试/评测夹具”，不提前实现 RAG HTTP/SSE、Tool 执行或 UI。

有两个实施前必须关闭的契约缺口：

1. 现有 Retrieval Evidence 可打开，但通用 Search 契约不携带“批准知识资格”；M6-02 不能仅凭当前 `SearchResult` 证明“默认只用批准知识”。
2. 文档要求保存 Model Adapter、模型、Prompt/Schema 版本、Token、延迟和错误，但当前 Workflow 只提供通用 Node output/schema/hash，Knowledge 只保存可选 `model_run_ref`；需要明确 M6-02 的持久化边界。

## 2. 权威来源与术语

### 2.1 事实优先级

本任务采用父任务已定义的事实优先级：当前任务文档高于正式 PRD，正式 PRD与已接受 ADR 高于其他架构文档，最后再以代码和实际行为验证（`.trellis/tasks/07-16-product-delivery/prd.md:9-20`）。当前 M6-02 PRD 仍是占位，因此需求语义以正式 PRD、已接受 ADR、`CONTEXT.md`、领域模型和已实现公共 seam 的交集为准。

### 2.2 统一领域语言

| 术语 | M6-02 中的精确定义 | 依据 |
|---|---|---|
| Evidence | 模型输入或输出引用的、可追溯到不可变 Source Version/Source Span 或已批准 Claim 的证据；不是模型自由生成的“理由” | `docs/product/PRD.md:265-269`, `docs/architecture/CONTEXT.md:41-43`, `docs/architecture/CONTEXT.md:79-81` |
| Citation | Answer 中指向一个确定 Evidence 身份的引用；必须可打开并能支撑相邻结论 | `docs/product/PRD.md:1554-1559`, `docs/architecture/agent-rag-architecture.md:142-154` |
| Faithfulness | Answer 的事实性主张是否受其引用证据支持；与“引用存在/可打开”是不同校验层 | `docs/product/PRD.md:3821-3836`, `docs/product/PRD.md:4604-4611` |
| Relation Assessment | NEW、COMPLEMENTARY、DUPLICATE、CONFLICT、LOW_CONFIDENCE 五分类判断；不是正式 RelationType | `docs/architecture/CONTEXT.md:67-73`, `docs/architecture/domain-model.md:249-253` |
| Relation | 已有明确类型、状态、证据和确认方式的知识边；不能由一次模型分类直接等同生成 | `docs/architecture/CONTEXT.md:63-73`, `docs/architecture/domain-model.md:105-110` |
| Applicability | Claim/Relation 成立的版本化适用条件；不同 JSON 不能自动推断冲突 | `docs/architecture/CONTEXT.md:55-57`, `docs/architecture/domain-model.md:355-362` |
| Conflict | 两个或多个 Claim 在相同或经审查为重叠的适用条件下不能同时成立的持续对象，不是普通错误提示 | `docs/architecture/CONTEXT.md:75-77`, `docs/architecture/workflows/09-conflict-resolution.md:14-20` |
| Refusal | 因证据资格、充分度、可定位性、工具权限或不可条件化冲突而拒绝发布事实回答；不是模型异常的通用替代文案 | `docs/product/PRD.md:1570-1576`, `docs/architecture/workflows/04-rag-question-answering.md:57-66` |
| Model Run | 一次可追踪模型调用，至少绑定 Adapter、模型、Prompt、输出 Schema 和 Workflow/Node；Knowledge Evidence 通过稳定引用关联它 | `docs/product/PRD.md:2612-2629`, `migrations/00017_knowledge_domain.sql:81-92` |

## 3. 用户价值与核心场景

### 3.1 用户价值

- 用户能知道新资料相对旧知识究竟是全新、互补、重复、冲突还是证据不足，而不是只得到一个相似度；每个判断展示双方证据、适用条件和不确定原因（`docs/product/PRD.md:1134-1157`, `docs/product/PRD.md:1236-1248`）。
- 用户能获得带可打开引用、冲突说明和推断标记的回答；证据不足时系统拒答，不用“看似有引用”的答案制造可靠性错觉（`docs/product/PRD.md:1506-1510`, `docs/product/PRD.md:1543-1576`）。
- 模型、Prompt、Schema 或 Retrieval 变化后，团队可以复现当时结果并运行相同评测，避免模型切换静默改变产品语义（`docs/product/PRD.md:3797-3802`, `docs/architecture/adr/0012-version-workflows-prompts-schemas.md:5-12`）。
- AI 结果仍然只是候选判断或后续动作，不获得文件、Git、数据库或正式知识写权限（`docs/product/PRD.md:271-283`, `docs/product/PRD.md:3114-3123`）。

### 3.2 M6-02 直接覆盖的场景

1. Source/Claim 分析：模型抽取 Claim，Retrieval 返回候选，Agent 产生五分类，Review 校验证据，Application 映射为候选动作。
2. RAG 核心判定：对已有 Evidence 做充分度、冲突、回答生成、引用与 Faithfulness 校验，返回 Publishable Answer 或 Refusal；Conversation/API/SSE 留给 M6-04。
3. 结构化输出失败：有限修复，仍失败则 Node 明确失败，不用自由文本解析成假成功。
4. 模型调用追踪：每个结构化结果可反查实际 Adapter、模型、Prompt、Schema、Workflow/Node 和 Retrieval 版本。

## 4. 建议冻结的产品契约

### 4.1 通用结构化结果 Envelope

架构明确要求不同任务使用具体 Schema，而不是一个巨型万能对象（`docs/architecture/agent-rag-architecture.md:67-80`）。建议只冻结一个小型公共 Envelope，每种任务再定义独立 payload：

| 字段 | 必需性 | 产品语义 |
|---|---:|---|
| `result_type` | 必填 | 任务结果判别类型，例如 `relation_assessment`、`rag_answer`、`refusal` |
| `schema_id` + `schema_version` | 必填 | 稳定 Schema 身份与版本；仅有版本号无法跨 Schema 区分 |
| `conclusion` | 按任务 | 面向用户的简洁结论；不能承担完整结构化事实 |
| `evidence_refs` | 必填数组，可空仅限明确 Refusal | 受控 Evidence 身份，不接受模型自造 URL/路径 |
| `confidence_factors` | 必填 | 可解释因素；不能只依赖模型自报概率 |
| `uncertainty_reasons` | 必填数组 | 无不确定性时返回空数组，不省略 |
| `proposed_actions` | 必填数组 | 只表示候选动作，不执行写入 |
| `tool_requests` | 必填数组 | M6-02 可定义输出形状，执行/授权归 M6-03 |
| `model_run_ref` | 必填 | 稳定关联实际模型调用与版本元数据 |

PRD 的通用输出使用 `evidence`、`confidence`（`docs/product/PRD.md:3101-3112`），Agent 架构使用 `evidence_refs`、`confidence_factors`（`docs/architecture/agent-rag-architecture.md:67-78`）。建议采用后者作为 canonical 字段，并将可选数值分数限制为任务 payload 的辅助字段；UI 展示高/中/低、影响因素和不确定原因，不单独展示伪精确百分比（`docs/product/PRD.md:3146-3154`）。当前 Knowledge 命令也已把 `ConfidenceScore` 与 `ConfidenceFactors` 分离（`internal/knowledge/application/commands.go:24-31`），支持该兼容策略。

### 4.2 Schema 校验与 Repair

建议冻结为最多三次模型响应：

1. 初次生成。
2. 若 JSON/Schema 失败，使用原响应和结构化错误列表做一次修复。
3. 若仍失败，使用该任务定义的 reduced schema 做一次重试。
4. 仍失败则 Node 失败，保存有界错误摘要，不解析自由文本。

该顺序与 PRD 的“首次修复、第二次缩小、达到上限失败”一致（`docs/product/PRD.md:3125-3130`），也对应架构流程的 Repair Attempt 1、Reduced Output Retry、Exhausted（`docs/architecture/agent-rag-architecture.md:82-95`）。

最低校验要求：拒绝 unknown field、重复 JSON key、多个顶层 JSON 值、错误枚举、越界字符串/数组、无效 UUID、非 canonical Schema 版本和跨 Workspace Evidence。Schema 合法后必须继续做业务校验；“JSON 合法”不等于“证据和行为合法”（`docs/architecture/agent-rag-architecture.md:84-93`, `docs/architecture/adr/0013-eino-adoption-gate.md:11-16`）。

### 4.3 Relation Assessment 五分类

| Assessment | 判定语义 | 必须输出 | 允许的后续动作 |
|---|---|---|---|
| `NEW` | 未检索到语义等价或直接相关 Claim | 检索范围、查询、未命中说明、新 Claim 证据 | `PROPOSE_NEW_CLAIM`；不落 Relation |
| `COMPLEMENTARY` | 同主题但增加条件、示例、解释或适用范围 | 双方 Evidence、相同点、差异点、Applicability 比较 | 建议 `COMPLEMENTS` Relation/补充 Proposal |
| `DUPLICATE` | 核心 Claim、条件和结论基本一致 | 双方 Evidence、独有细节、合并风险 | 建议 `DUPLICATES` Relation/合并 Proposal |
| `CONFLICT` | 相同或经审查为重叠条件下不能同时成立 | 双方 Evidence、Applicability、立场摘要、冲突原因 | 打开 Conflict；可选建议 `CONFLICTS_WITH`，不得裁决 |
| `LOW_CONFIDENCE` | 证据不足、上下文缺失、分类接近或来源质量不足 | 缺口、不确定因素、已执行检索范围 | `REQUIRE_HUMAN_REVIEW`；不落 Relation |

产品定义与处理来自 `docs/product/PRD.md:1158-1205`。现有 Domain 已把五分类确定性映射为新 Claim、候选 Relation、Conflict 或人工处理，并明确 NEW/LOW_CONFIDENCE 不产生 Relation（`internal/knowledge/domain/relation.go:315-374`）。

每个非 NEW 判断必须有“新 Claim 证据 + 候选旧 Claim 证据”两侧绑定；PRD 明确要求双方证据，Source 引用无法定位时禁止创建 Proposal（`docs/product/PRD.md:1207-1221`, `docs/product/PRD.md:1250-1262`）。

Applicability 必须使用现有 canonical `knowledge-applicability/v1`，不能由 Agent 自由文本定义第二套条件模型（`internal/knowledge/domain/applicability.go:17-30`, `internal/knowledge/domain/applicability.go:32-67`）。冲突只允许 `EXACT` 或带人工/受控审查理由的 `REVIEWED_OVERLAP`；当前 Domain 已拒绝没有理由的 overlap 和条件哈希不一致的 exact（`internal/knowledge/domain/conflict.go:35-51`, `internal/knowledge/domain/conflict.go:82-93`, `internal/knowledge/domain/conflict.go:101-134`）。

### 4.4 Citation 与 Faithfulness

建议拆为四层，错误码和测试不得混在一起：

1. **Identity Validation**：引用的 Workspace、Chunk、Source Version、Source Span 身份存在且绑定一致。
2. **Openability Validation**：Source Span 能从不可变 Content Artifact 复核 hash/range 并返回 excerpt。
3. **Eligibility Validation**：Evidence 属于当前允许的知识范围，默认 RAG 只能使用 Ready + Approved；Source/Historical 仅在用户显式允许时使用。
4. **Semantic Support / Faithfulness**：引用文本支持相邻原子结论；未被 Evidence 支持的内容必须标为模型推断或导致重生成/拒答。

文档已明确引用 ID、可打开性、相邻结论支持和批准状态四项检查（`docs/architecture/agent-rag-architecture.md:142-154`），RAG workflow 也要求 Ready + Approved、Source Span 可打开（`docs/architecture/workflows/04-rag-question-answering.md:41-46`）。

现有 Retrieval 已提供可复用的前两层稳定 seam：

- `EvidenceV1` 冻结 Workspace/Index/Embedding/Chunk/Span/Snippet/Provenance/排名信息（`internal/retrieval/domain/search.go:284-318`）。
- `EvidenceReferenceService.GetSourceSpan` 校验数据库绑定、不可变 Artifact hash、byte range 和 excerpt（`internal/retrieval/application/evidence_reference.go:88-123`, `internal/retrieval/application/evidence_reference.go:136-165`）。
- Search HTTP 为每个 Provenance 生成 Source Version/Source Span href（`internal/retrieval/http/handler.go:174-202`, `internal/retrieval/http/handler.go:502-517`）。

Citation identity 不能只有 `chunk_id`，因为一个 canonical Chunk 可以有多个 Provenance；输出必须选择具体 `source_version_id + source_span_id`，并保留 `chunk_id` 作为检索结果绑定。现有 HTTP 结构正是“一个 Span + 多个 Provenance/href”（`internal/retrieval/http/handler.go:174-202`）。

Faithfulness 最低可验收定义建议为：Answer 拆分出的每个事实性 assertion 至少有一个通过 Eligibility 的 Citation 支持；Citation Precision 衡量被引用证据是否支持 assertion，Citation Coverage 衡量事实 assertions 是否全部有引用，Faithfulness 衡量回答是否超出全部证据。正式评测指标已要求 Citation Precision、Citation Coverage、Faithfulness、Conflict Disclosure 和 Appropriate Refusal（`docs/product/PRD.md:3821-3836`）。

### 4.5 冲突解释

当 Evidence 中存在冲突 Claim，Publishable Answer 必须：

- 明确声明存在冲突。
- 分别展示每个观点、适用条件、来源和更新时间。
- 标明 `EXACT` 或 `REVIEWED_OVERLAP` 的条件比较结果。
- 能条件化时给出“在条件 A 下…；在条件 B 下…”的答案。
- 不能条件化或证据不足时拒答，不用模型常识裁决。

以上来自 PRD 冲突规则（`docs/product/PRD.md:1561-1568`）和 Agent 架构“不允许静默裁决”（`docs/architecture/agent-rag-architecture.md:135-140`）。Conflict 调查记录还要求保存 Query、Tool Calls、新 Evidence、分析说明和模型版本，且不覆盖旧记录（`docs/architecture/workflows/09-conflict-resolution.md:41-49`）。M6-02 只生成解释/调查候选；Conflict 状态迁移与正式解决仍走 Knowledge Application/Proposal。

### 4.6 Refusal

建议定义稳定 refusal reason code，而不是只有自然语言：

| Code | 条件 |
|---|---|
| `NO_RELEVANT_EVIDENCE` | 无相关 Evidence |
| `UNAPPROVED_EVIDENCE_ONLY` | 只有未批准草稿/候选 |
| `CITATION_UNRESOLVABLE` | Source Span 不存在、损坏或无法打开 |
| `EVIDENCE_INSUFFICIENT` | 有证据但不能覆盖问题关键子项 |
| `EXTERNAL_FACT_NOT_AUTHORIZED` | 问题要求实时外部事实但未授权 Web Tool |
| `CONFLICT_NOT_CONDITIONABLE` | 高严重度冲突无法形成有条件回答 |
| `VALIDATION_EXHAUSTED` | 生成后 Schema/Citation/Faithfulness 修复耗尽 |

前五类直接来自产品拒答条件（`docs/product/PRD.md:1570-1576`）与 RAG workflow（`docs/architecture/workflows/04-rag-question-answering.md:57-66`）；`VALIDATION_EXHAUSTED` 用于区分“证据不足”和“模型结果始终不合法”。Refusal 仍应包含检索范围、缺失项、允许的下一步操作和模型运行引用，但不能伪造答案正文。

### 4.7 模型、Prompt、Schema 与 Retrieval 版本

每次 Model Run 至少记录：

- `model_run_id`、`workflow_run_id`、`node_run_id`。
- `adapter_name`、`adapter_version`、`model_id`。
- `prompt_template_id`、`prompt_template_version`。
- `output_schema_id`、`output_schema_version`。
- `retrieval_index_version_id`、可选 `embedding_version_id`、可选 `rerank_model_version`。
- 输入/输出 Token、延迟、结束状态和稳定错误类型。
- Repair attempt 序号与最终是否发布。

PRD 对模型记录的最小字段要求见 `docs/product/PRD.md:2612-2629`；模型切换必须记录且所有 Adapter 使用同一 Review/评测标准（`docs/product/PRD.md:3132-3137`）。Workflow、Prompt 和 Schema 必须有稳定 ID/版本，运行中任务继续使用启动版本（`docs/architecture/adr/0012-version-workflows-prompts-schemas.md:5-12`）。Retrieval 已能返回 Index/Embedding/Rerank 版本（`internal/retrieval/domain/search.go:284-318`），Knowledge Claim/Relation Evidence 已预留 `ModelRunRef`（`internal/knowledge/application/commands.go:34-40`, `internal/knowledge/application/commands.go:58-74`）。

主模块不正式采用 Eino。PoC 关键门禁失败后，正式实现应使用项目自有 Port + 直接 OpenAI-Compatible/Ollama Adapter；不能把 Eino 类型放入公共契约（`.trellis/tasks/archive/2026-07/07-16-m2-eino-poc/prd.md:9-15`, `.trellis/tasks/archive/2026-07/07-16-m2-eino-poc/prd.md:19-29`）。已接受 ADR 也要求框架输出继续经过项目 Schema/Evidence/Permission 校验（`docs/architecture/adr/0013-eino-adoption-gate.md:9-16`）。

## 5. 现有 Retrieval / Knowledge seam 影响

### 5.1 可直接复用

- Retrieval `SearchService.Search`：提供有界 Active Index Search、显式 requested/effective mode 与降级；Agent 不应复制 FTS/vector/RRF/rerank（`internal/retrieval/application/search.go:65-113`, `internal/retrieval/domain/search.go:308-355`）。
- Retrieval `EvidenceReferenceService`：提供引用存在性、跨 Workspace、Artifact hash 和 excerpt 复核；Agent 不应直接读路径或文件（`internal/retrieval/application/evidence_reference.go:19-31`, `internal/retrieval/application/evidence_reference.go:50-61`）。
- Knowledge `MapAssessment`：五分类到领域候选动作的唯一事实源；Agent 不应复制 NEW/LOW/Conflict 落库规则（`internal/knowledge/domain/relation.go:326-374`）。
- Knowledge Application 命令：Relation Evidence 已要求 Provenance、Reason、Applicability 和 ModelRunRef；Conflict 已要求 ApplicabilityAssessment 与 ReviewedOverlapReason（`internal/knowledge/application/commands.go:58-74`, `internal/knowledge/application/commands.go:95-112`）。

### 5.2 必须补齐的 seam

1. **Approved Evidence Eligibility**：当前 `SearchFilter` 只有 Source、Source Version、路径和时间过滤，没有 Document/Revision/Claim 批准状态（`internal/retrieval/domain/search.go:41-58`）；`EvidenceV1` 也没有 eligibility 状态（`internal/retrieval/domain/search.go:284-306`）。而 RAG 默认必须只使用批准知识（`docs/product/PRD.md:1528-1541`, `docs/architecture/workflows/04-rag-question-answering.md:41-46`）。M6-02 需要一个 fail-closed 的 Eligibility Port，或由 Retrieval 扩展稳定 RAG scope；不能假设 Active Index 等于 Approved Knowledge。
2. **Citation 唯一身份**：EvidenceV1 一个 Chunk 可携带多个 Provenance，因此 Agent Citation 必须选择 Source Version + Span，不能只引用 Chunk。
3. **Model Run Persistence**：`workflow.node_run` 可保存通用 output，Runtime 另有 output schema/hash（`migrations/00003_workflow.sql:35-56`, `migrations/00012_workflow_runtime_state_machine.sql:91-108`），Knowledge 表只有可选字符串 `model_run_ref`（`migrations/00017_knowledge_domain.sql:81-92`, `migrations/00017_knowledge_domain.sql:172-186`）。需要明确 Model Run 是新增 append-only 实体，还是先嵌入版本化 Node Output；若只嵌入 Node Output，必须说明 Token/延迟/错误如何满足 PRD 查询与审计要求。
4. **Knowledge 写入编排**：M6-02 只能产出 `AssessmentAction` 或 Knowledge Command 输入，正式 Relation/Conflict/Claim 仍由 Knowledge Application 做 Provenance、Confirmation、CAS、幂等与事务校验，不能由 Model Adapter 直接调用 Repository。

## 6. 验收映射

### 6.1 正式 AC 映射

| AC | M6-02 责任 | 验收证据 |
|---|---|---|
| AC-05 引用 | 复用 Retrieval 可打开 Evidence，并增加 Citation 绑定/语义校验 | Markdown/Source Span 可打开；损坏引用 fail closed（`docs/product/PRD.md:4453-4458`） |
| AC-10 关系分析 | 五分类 Schema、双方 Evidence、Applicability、LOW/Conflict 安全映射 | 五类固定样本；每类 Precision/Recall/F1；Conflict→Duplicate 单独统计（`docs/product/PRD.md:4462-4462`, `docs/product/PRD.md:3838-3854`） |
| AC-16 RAG | Answer/Refusal 结构、Citation、Faithfulness、冲突说明和推断标记 | 引用、冲突、拒答确定性测试与固定 eval（`docs/product/PRD.md:4468-4468`, `docs/product/PRD.md:3958-3966`） |
| AC-28 Observability | 保存实际 Model/Prompt/Schema/Retrieval 版本与模型运行摘要 | 任一结果可反查版本；Token/耗时/错误有记录（`docs/product/PRD.md:4480-4480`, `docs/product/PRD.md:2669-2674`） |
| AC-29 Evaluation | 提供 RAG/Relation 固定 gold set、runner 和版本化结果契约 | 指标与基线可比较；高风险下降阻止默认切换（`docs/product/PRD.md:4481-4481`, `docs/product/PRD.md:3906-3918`） |
| AC-36 Conflict | M6-02 负责冲突检测/解释，不负责完整调查与解决 UI | 冲突不静默合并；条件差异可表达（`docs/product/PRD.md:4488-4488`, `docs/architecture/workflows/09-conflict-resolution.md:85-90`） |

### 6.2 建议写入当前任务 PRD 的可执行 Acceptance Criteria

- [ ] Relation Assessment、RAG Answer、Refusal 至少三个任务 Schema 独立版本化，严格 decoder 拒绝 unknown/duplicate/trailing data。
- [ ] Schema Repair 固定“初次 + 修复 + reduced retry”上限；耗尽后稳定失败且不解析自由文本。
- [ ] 五分类固定样本全部覆盖；NEW/LOW_CONFIDENCE 不产生 Relation，CONFLICT 不被映射为 DUPLICATE/覆盖。
- [ ] 每个非 NEW Assessment 有双方 Evidence、Applicability、理由、置信因素和不确定原因；引用不可定位时不生成 Knowledge Command。
- [ ] Citation Identity/Openability/Eligibility/Semantic Support 四层测试覆盖正常、跨 Workspace、损坏 Artifact、未批准、无关证据和多 Provenance。
- [ ] Answer 中每个事实 assertion 有支持引用或显式 `model_inference`；引用不支撑时重生成/拒答，不发布已校验状态。
- [ ] 冲突回答分别展示观点、条件、来源和更新时间；无法条件化时返回稳定 Refusal。
- [ ] Model Run 可反查 Adapter、模型、Prompt/Schema、Workflow/Node、Index/Embedding/Rerank 版本、attempt、Token、延迟和错误；不保存 API Key/Secret/不必要全文。
- [ ] 使用确定性 Fake Model 完成单元/Contract 测试；真实模型只用于显式 AI Eval，不因缺少凭据走假成功。
- [ ] RAG eval 至少产出 Citation Precision/Coverage、Faithfulness、Conflict Disclosure、Appropriate Refusal；Relation eval 产出五类 Precision/Recall/F1、高风险误判和 Evidence Support。

## 7. 明确 Out of Scope

- RAG Conversation、HTTP API、SSE Streaming、反馈写入和页面展示：归 M6-04（`.trellis/tasks/07-16-product-delivery/implement.md:51-51`）。
- Tool Registry 执行、Capability、SSRF、命令/输出安全和审批授权：归 M6-03；M6-02 只允许输出 Tool Request（`.trellis/tasks/07-16-product-delivery/implement.md:50-50`, `docs/architecture/agent-rag-architecture.md:235-249`）。
- 模型直接创建/确认 Claim、Relation、Conflict，或直接写文件/Git/数据库：违反 Proposal/Approval 和 Knowledge Application 边界（`docs/product/PRD.md:271-283`, `docs/product/PRD.md:3114-3123`）。
- Artifact、Review、Interview、Memory 的业务实现；它们是 M8 消费 M6-02 的下游（`.trellis/tasks/07-16-product-delivery/implement.md:56-58`）。
- Graph、Semantic Link、Health、Timeline 与 Conflict 完整调查/解决 UI；归 M7/M9。
- 多 Agent Swarm、自主创建 Agent、模型训练/微调；正式 v1 明确排除（`docs/product/PRD.md:4661-4682`）。
- 正式采用 Eino 或把 Eino Graph 作为 Workflow/Domain 事实源；PoC 结论是不采用，保留直接 Adapter。
- 未授权网页事实补充；M6-02 可返回 Tool Request/Refusal，但 Web Fetch 执行与安全归 M6-03/M6-04。
- 为了 M6-02 顺手建设完整 M10 日志、成本、Audit UI；但满足 Model Run 可追踪所需的最小持久化不能被“留到 M10”而完全省略。

## 8. 文档冲突、缺失与待确认项

### 8.1 已可直接裁决的冲突

1. **“关系类型”与 Relation Assessment 混用**：PRD 10.6 标题使用“关系类型”（`docs/product/PRD.md:1158-1205`），但 CONTEXT、领域模型和现有代码明确五分类不是 RelationType。采用 `Relation Assessment`；正式 RelationType 只由 Domain 映射生成。
2. **通用输出字段名不一致**：PRD 用 `evidence/confidence`，架构用 `evidence_refs/confidence_factors`。采用可绑定的 `evidence_refs` 和可解释 `confidence_factors`；标量 score 仅为可选辅助。
3. **Eino 采用状态过时**：父设计仍描述 Eino Adapter/Direct Adapter 二选一（`.trellis/tasks/07-16-product-delivery/design.md:133-154`），但 M2 PoC 已决定主模块不采用。M6-02 应直接实现项目自有 Port + OpenAI-Compatible/Ollama Adapter，不再把 Eino 当本期候选。
4. **“Active Index”等同“Approved Knowledge”不可成立**：M6-01 是 Source/Chunk 通用检索，且明确 Document/Article Revision 字段当时 out of scope（`.trellis/tasks/archive/2026-07/07-17-retrieval-indexing-search/prd.md:59-70`）。M6-02 必须新增批准资格校验 seam，不能静默假设。

### 8.2 必须在 design.md 冻结的待确认项

1. **Model Run 持久化位置**：新增专用 append-only 表/Repository，还是版本化 Node Output + 后续 Ops 投影？前者更满足 PRD 查询，后者改动小但容易让审计与 Knowledge `model_run_ref` 变成弱引用。
2. **批准 Evidence 的权威判断**：由 Retrieval 扩展 `EvidenceEligibility`，还是由 Agent Application 调 Knowledge/Document read model 二次校验？必须支持批量、同 Workspace、无 N+1、fail closed。
3. **Faithfulness Validator 实现**：确定性 lexical/claim-bound 规则、独立 Review Model，还是两者组合；无论选择哪种，都必须有固定 eval 和阈值版本。文档只给指标，没有冻结判定算法/阈值（`docs/product/PRD.md:3821-3836`）。
4. **Citation/Faithfulness 失败后的重试顺序**：Agent 架构写“返回生成节点重新生成”（`docs/architecture/agent-rag-architecture.md:151-154`），RAG workflow 图则从 Review Fail 返回 Retrieval（`docs/architecture/workflows/04-rag-question-answering.md:27-31`）。建议按错误类型路由：结构/支持失败先重生成一次；Evidence 无效/不足再检索；总预算耗尽拒答。
5. **Repair 预算是否固定为三次响应**：现有文档语义支持该解释，但未明确配置项和跨模型 fallback。M6-02 PRD 应冻结上限，防止成本和延迟无界。
6. **模型 fallback 规则**：PRD允许兼容 Adapter，但要求模型切换记录（`docs/product/PRD.md:3132-3137`）；必须决定哪些错误可 fallback、是否同一 Node 内切换、UI/结果如何显式标记，禁止静默切换。
7. **推断标记粒度**：按整段 Answer 还是按 assertion；为了 Citation Coverage 与 Faithfulness 可测，建议按 assertion。
8. **Eval 阈值**：正式指标已定义，但门禁阈值只说由评测 ADR 决定（`docs/product/PRD.md:3906-3911`）。M6-02 可先建立 baseline 与非零高风险门禁，最终阈值在 M11-02 冻结。

### 8.3 当前任务文档缺失

当前 `prd.md` 的 Requirements 与 Acceptance Criteria 仍为 TBD，且复杂任务按模板应在开发前补 `design.md` 与 `implement.md`（`.trellis/tasks/07-19-agent-structured-output-citation/prd.md:7-19`）。实施前至少应把本研究第 4、6、7、8 节收敛进任务 PRD/design/implement，尤其先关闭 Approved Evidence 与 Model Run persistence 两个阻塞项。
