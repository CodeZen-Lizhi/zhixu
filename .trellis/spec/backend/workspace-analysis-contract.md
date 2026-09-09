# Workspace Analysis 合同

## 1. Scope / Trigger

- 修改 `workspace_analysis` Question mode、`workspace-analysis@1/@2` Definition、版本化 Node Executor、精确
  Tool、Decision journal、Model/Tool Operation、预算、receipt/candidate、终态发布、时间线、SSE/Audit/Metric、API/Worker
  readiness 或 Web 展示时，必须同时遵守本规范。
- 该能力是固定 RAG 之外的独立显式模式。默认 Question mode 仍是 `rag`；任何依赖、合同或 readiness
  不满足时只能对 `workspace_analysis` fail closed，禁止静默改投 RAG。
- `workspace-analysis@1` 是保留的静态六阶段历史合同；当前新请求使用 `workspace-analysis@2`，在四节点
  DAG 的 `decide_next` 内运行受限动态工具循环。模型可依据上一工具结果选择下一只读工具或结束取证。
  后续阶段仍由 Workflow successor 推进，不用 Node retry 模拟下一轮决策，不原地替换 v1 Graph/hash。
- Eino 承担通用模型/工具协议；项目继续拥有授权、预算、租约、Model/Tool 事实、journal 和恢复。
  动态循环不引入第二 Workflow Runtime，也不启用生产 Eino checkpoint。
- 修改 Web Stop、Workflow control version、控制幂等键或取消烟测时，必须保留本节定义的严格有界
  CAS 恢复；不得用通用 HTTP retry 或等待候选屏障掩盖运行中版本推进竞态。

## 2. Signatures

### HTTP / Web

```text
POST /api/v2/conversations/{conversation_id}/questions
request.mode = "rag" | "workspace_analysis"   # omitted -> rag

GET /api/v2/answers/{answer_id}
GET /api/v2/answers/{answer_id}/analysis-timeline?workspace_id={workspace_id}
GET /api/v2/conversations/{conversation_id}/turns
POST /api/v1/workflows/{run_id}/cancel
```

- `/api/v1` 保留 RAG 与历史 Workspace Analysis v1 的成功响应和精确重放；`/api/v2` 读取新旧事实。
  当前生产 starter 只新建 `workspace-analysis@2`，因此通过 v1 新建分析返回不可重试的
  `409 CONVERSATION_API_VERSION_UNSUPPORTED`，发生在 ID 分配、Runtime/Analysis starter 和任何新事实写入前。
  该边界不代表仍支持旧客户端新建分析；新建须迁移到 v2。HTTP 版本不改变 canonical request/hash/receipt。
- HTTP 兼容性按持久 Workflow Definition key/version 判断，不能依赖 result 的 `schema_version`；动态 pending
  尚无 result，refusal/termination 也可能保留 v1 envelope。GetAnswer 在 ETag/304 前检查，Timeline 在读事务内
  按 Analysis Run definition_version 检查；跨 Workspace 先返回统一 404。
- v1 Turn/list/latest 在 SQL LIMIT 前过滤动态 Definition，缺失 Answer/Definition 的损坏行仍须被 scanner 拒绝。
  Turn cursor 的 `schema_version` 为 HTTP v1=1、v2=2，跨版本返回 `CONVERSATION_CURSOR_INVALID`，需重读第一页。
- 下文运行时 v1/v2 指持久 Workflow/Analysis 版本，与 HTTP 路径版本独立。Conversation 创建、Feedback、
  Workflow control、来源链接和 SSE 保留原 `/api/v1` 路径。
- Answer 终态类型固定为 `workspace_analysis`、`workspace_analysis_refusal`、`clarification` 或
  `workspace_analysis_termination`；`model_run_ref` 的必需/可空规则由终止原因决定。
- 客户端不选择 Definition、policy 或工具版本。成功 Answer 的 `schema_version` 按已持久版本分为
  `v1` / `v2`；拒答、clarification、termination 继续使用各自原有 `v1` Schema，不能把所有 Envelope 一起改成 v2。
- v2 成功结果的 `git_status` 必需但可为 `null`，只有实际 Git receipt 才可投影 Git 聚合；v1 保持原有非空合同。
- Timeline snapshot 是权威事实；SSE 只触发 Query 失效，Answer Draft SSE 只承载未验证的临时 Token。
- Timeline `schema_version` 独立分支为 `v1` / `v2`。v2 增加 `decide_next`，按持久 journal `sequence`
  投影 Model/Tool 行（最多 27 项），工具 phase 由精确 tuple 推导；Web 保留服务端顺序，不按 phase 重新分组。
- Timeline budget 必须投影 `model_calls`、`tool_calls`、`source_reads`、`input_tokens`、`output_tokens`
  和 required-nullable `estimated_cost_microunits`；`source_reads` 直接来自持久 reservation/settlement，
  不能从前端项目数近似推导。
- Web Stop 最多发送 5 次 cancel POST、最多执行 4 次无 ETag Answer 权威 GET。每次 POST 的
  `expected_version` 必须严格递增，并生成绑定该 version 的新控制幂等键；TanStack mutation 保持
  `retry=false`。

### Application

```go
ExecutionService.ExecuteWorkspaceAnalysisTool(
    context.Context,
    toolsapplication.ExecuteWorkspaceAnalysisToolCommand,
) (toolsapplication.ToolExecutionResult, error)

WorkspaceAnalysisModelOperationRepository.AuthorizeWorkspaceAnalysisModelCall(
    context.Context,
    agentapplication.AuthorizeWorkspaceAnalysisModelCallCommand,
) (agentapplication.WorkspaceAnalysisModelAuthorizationResult, error)

WorkspaceAnalysisFinalizer.FinalizeSuccess(...)
WorkspaceAnalysisFinalizer.FinalizeTermination(...)
```

- Tool/Model 授权必须在一个数据库事务中完成 active lease/fence、deadline、cancel、logical operation、
  budget reservation 与 `STARTED` Call 的校验/写入。
- Tool 成功必须在一个事务中完成 Call CAS、canonical receipt、预算结算与 Operation 终结；模型成功同理
  持久化 exact model result 或 candidate。commit 结果不确定时不得把输出交给后继。

v2 决策持久端口沿用现有模型授权，不新增无预算的 Provider 入口：

```go
type WorkspaceAnalysisDecisionRepository interface {
    WorkspaceAnalysisModelOperationRepository
    LoadWorkspaceAnalysisJournal(context.Context, WorkspaceAnalysisJournalQuery) (WorkspaceAnalysisJournalSnapshot, error)
    FinalizeWorkspaceAnalysisDecision(context.Context, FinalizeWorkspaceAnalysisDecisionCommand) (WorkspaceAnalysisDecisionMutationResult, error)
}

func WorkspaceAnalysisAdmissionDenialFromError(error) (WorkspaceAnalysisAdmissionDenial, bool)

type ScopedWorkspaceAnalysisRunStarter interface {
    StartWorkspaceAnalysisRunScoped(context.Context, foundation.TransactionScope, WorkspaceAnalysisRunStartCommand) (domain.WorkspaceAnalysisRun, error)
}
```

- `WorkspaceAnalysisJournalQuery` 精确绑定 Workspace、Analysis Run、Workflow Run；返回 Operation、Decision、
  ModelRun/Call 的事实引用，不携带来源正文或 Provider transcript。
- `FinalizeWorkspaceAnalysisDecisionCommand` 绑定 Identity、OperationKey/ID、ExpectedCallVersion 与 ReceiptID。
  成功必须有合法 Decision 和 usage；FAILED/UNKNOWN 不得携带 Decision，UNKNOWN 无可证明 usage 时全额计费。
- `WorkspaceAnalysisAdmissionDenial` 证明已提交、尚无 Call 的 PENDING Operation；提取函数返回 struct 值和 bool，
  不能把它当作失败事务或一个可为空的指针。

### Database / Config

- 主要事实：`agent.workspace_analysis_run`、`agent.workspace_analysis_operation`、
  `agent.workspace_analysis_budget_reservation`、`agent.workspace_analysis_model_result`、
  `agent.workspace_analysis_candidate`、`workflow.tool_result_receipt`、
  `workflow.tool_result_receipt_failure`、`agent.workspace_analysis_publication_proof`、
  `agent.workspace_analysis_termination_proof`、`agent.workspace_analysis_worker_capability`、
  `agent.workspace_analysis_tool_refusal`。
- v2 追加 `agent.workspace_analysis_decision`、`agent.workspace_analysis_journal` 和
  `agent.workspace_analysis_evidence`。Decision 唯一绑定 Operation/ModelCall 与 Run-global ordinal；journal
  主键是 `(analysis_run_id, sequence)`，Evidence 主键是 `(analysis_run_id, reference_no)`，三者都只追加。
- 配置入口固定为：

```text
ZHIXU_WORKSPACE_ANALYSIS_API_ENABLED=false
ZHIXU_WORKSPACE_ANALYSIS_WORKER_ENABLED=false
ZHIXU_WORKSPACE_ANALYSIS_CONFIG_REVISION=1
```

- 迁移 `00085`–`00091` 互相扩展函数、trigger 和列。旧版本边界测试只用
  `MigrateAtlasToVersion` 构造前缀，再按顺序向前升级；不得执行逆向 DDL。
- `00098` 前向增加 v2 Run/Decision/journal/预算及 Finalizer 约束，`00099` 补 v2 Tool/receipt/Evidence 约束。
  保留 v1 函数分支与既有 trigger 路由；使用标准 Atlas 目录/checksum，不能靠临时 overlay 或关闭约束完成验收。

## 3. Contracts

### v1：保留的 Definition 与工具目录

- Definition 固定为 `workspace-analysis@1`，Node 顺序固定为：
  `inspect_workspace -> retrieve_evidence -> read_evidence -> synthesize_answer ->
  validate_citations -> review_publish`。
- 目录只能包含 `ReadGitStatus@2`、`SearchKnowledge@2`、`ReadSource@3`、`ValidateCitation@3`，均为
  `TRUSTED_WORKFLOW_ONLY`、`READ_LOCAL`、无副作用。模型不能选择 Tool 名称、版本或授权身份。
- Git 输出只能包含 branch/head/object format/clean 和 staged、unstaged、untracked、conflict 四类计数；
  禁止路径、文件名、porcelain 行、Diff 或正文。
- Search 最多五个短引用，服务端冻结前 1–3 个；ReadSource 只能读取该 same-Run frozen tuple；
  ValidateCitation 只能解析当前 immutable candidate 的完整引用集合。

### v2：四节点 DAG 与动态决策

- `RegisteredWorkspaceAnalysisDefinitionV2()` 固定 `workspace-analysis@2`，输入 Schema 仍为 1，节点输出为 2，
  kind 前缀为 `agent.workspace-analysis.v2.`，Graph/hash 与 v1 分离。依赖顺序固定为：
  `decide_next -> synthesize_answer -> validate_citations -> review_publish`。
- `decide_next` 内由 Eino 按工具结果继续调用模型。每次最多一个冻结工具调用；应用将其归一化为下表的
  Decision 并持久化，然后才执行对应工具。无工具调用的最终响应必须是严格 `finish` 决策，不能直接发布正文。
- 模型控制的持久 Decision 只有四字段，全部必需；未使用字段必须为 `null`，不能缺省或用空数组代替。
  Decoder 拒绝 unknown/duplicate key，JSON 上限 4096 字节。工具版本、Workspace、路径、实体 ID、
  Candidate identity、权限、Operation/Attempt、lease/fence 都由服务端重建。

| `action` | 服务端冻结 Tool | 允许的非空字段 |
| --- | --- | --- |
| `git_status` | `ReadGitStatus@3` | 无 |
| `knowledge_search` | `SearchKnowledge@3` | `query`：UTF-8、已 trim、1–1024 字节、无 NUL |
| `source_read` | `ReadSource@4` | `evidence_ref`：已由本 Run 搜索绑定的 `E1`–`E32` |
| `citation_validation` | `ValidateCitation@4` | `evidence_refs`：1–8 个唯一、已读取的本 Run 短引用 |
| `finish` | 无；进入合成阶段 | 无 |

```json
{"action":"source_read","query":null,"evidence_ref":"E7","evidence_refs":null}
```

- 四工具均为 `TRUSTED_WORKFLOW_ONLY`、`READ_LOCAL`、无副作用；v1/v2 Catalog 独立校验。模型可以选择表中
  行为，不能指定任意工具、版本、外部身份或写操作，也不能并行授权第二个工具。
- Search 每次最多五个 hit；追加 Search 为所有 hit 分配 Run-global alias，总量不超过 32，旧 alias 不重绑。
  Evidence 保留 search receipt/hash、receipt-local ref 与 immutable 来源 tuple；重复搜索同一 tuple 可以得到
  不同 alias，不能以此删除原读取事实。ReadSource 只能从对应 exact receipt 恢复，不能读取“当前最新资料”。
- Citation 仍须通过 Knowledge Eligibility。未读取、跨 Workspace、篡改 tuple/hash、正文截断或缺失合法
  Claim 的来源不能成为正式成功引用；模型不能靠一个格式正确的 `E7` 绕过这些约束。

### v2：冻结预算、deadline 与可达中止

预算来自 `WorkspaceAnalysisBudgetV2` 与领域常量，Go、SQL、Timeline 和 Web 必须一致；不能复制 v1 上限。

| 维度 | v2 冻结合同 |
| --- | --- |
| Workflow nodes / 已授权决策数 | 4 / 最多 12 |
| Model Calls / Tool Calls / Source reads / Tool 并发 | 14 / 13 / 8 / 1 |
| Run-global Evidence aliases | 最多 32 |
| 每次模型 input reservation / Run input 上限 | 65536 / 917504 tokens |
| Decision / Synthesis / Review output 上限 | 512 / `min(profile limit, 4096)` / 1024 tokens |
| Run output 上限 | `12*512 + synthesis limit + 1024`，即 7169–11264 tokens |
| Cost | 当前 `max_cost_microunits` 与公开估算为 `null`；尚无可信持久价格合同，不能记零或声称已验证费用上限 |

- 合法 v2 循环可真实耗尽决策/读取等预算。第 13 个 DECISION 只允许作为 PENDING denial 留存，不能产生
  ModelCall、Decision receipt 或 Reservation。必须先提交真实 denial 事实，再以 typed denial 驱动 Finalizer；
  禁止在事务回调中直接返回该错误而把 causal Operation 回滚。
- `PlanModelTimeout` 在 v2 表示每次决策超时，没有额外 Planner Call。`decide_next` 的总时限为
  `12*(plan timeout + max(tool timeouts) + 5s) + 15s`；后续三个节点分别是对应外部调用超时加 15s/10s/15s。
  `deadline_at = created_at + 四节点时限总和`，最多一小时，重试或 replacement 不延长。
- River Job timeout 至少为最长节点时限加 30s；lease 和 heartbeat 必须通过
  `WorkspaceAnalysisV2Deadlines.ValidateRuntimeReadiness`。deadline/取消/预算/围栏在服务端授权事务中检查，
  不依赖浏览器计时或 Eino 自己的迭代上限作为唯一限制。

### Operation、Attempt 与恢复

- logical operation key 固定包含 Analysis Run、node key、operation kind、ordinal；`request_hash` 是该槽位
  必须精确匹配的不可变绑定，不能换 hash 获得另一个槽位。Attempt 不进入 key；`first_node_attempt_id` 绑定
  最初外部调用，`latest_node_attempt_id` 只表示当前恢复围栏。
- 同 Attempt 的 `RECONCILE_EXACT_CALL` 表示同一个 Worker delivery 仍可能在执行，必须继续 reconciliation；
  不得为了快速收口而把 `STARTED` 立即改为 Unknown。
- lease 过期后，Workflow Claim 先把旧 Attempt 标为 `lease_lost` 并创建 replacement Attempt。replacement
  持有效 lease/fence 后，Model/Tool 授权事务才可把旧 `STARTED` Call/Operation/Reservation 原子归约为
  `UNKNOWN` / `UNKNOWN_CHARGED`，随后发布 `RESULT_UNKNOWN`。
- terminal Call/receipt/model result 可由 replacement Attempt exact replay，但不能复用旧 lease、重新执行
  Provider/Tool、重复扣预算或生成第二条时间线事实。
- 冻结 v1 的 canonical operation 前缀在每个预算维度都不超过 Run 上限；完整成功路径只会恰好耗尽
  Model Call、input/output Token 上限。因此 `WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED` 保留为兼容终态，
  但对合法 v1 前缀结构性不可达。测试必须用全前缀性质证明和 PostgreSQL 非因果 proof 拒绝来覆盖，
  禁止直接伪造正向 budget termination。

### v2 journal、ModelRun 与历史重放

- journal 按授权 Operation 的顺序只追加，不是按最终完成时间或 phase 重新生成。合法链为
  `DECISION(n) -> 所选 Tool(n) -> DECISION(n+1)`；持久 `finish` 后才可进入 Candidate/独立 Citation/Review。
  每个 Tool Operation 绑定选中它的 Decision receipt，同一 ordinal 不能再接另一工具。
- 同一活跃 loop Attempt 只有一个 ModelRun，逐次创建 ModelCall 并结算；普通成功决策后 ModelRun 保持
  `RUNNING`，不能沿用 v1 单调用 Planner 的“每次成功就关闭 ModelRun”。finish、FAILED/UNKNOWN、受证明的
  replacement checkpoint 或终态事务才允许关闭。
- 全局 Decision ordinal 与 Attempt 内 `ModelCall.CallNo` 是两个编号。replacement 的新 ModelRun 从
  CallNo 1 接续下一全局 ordinal，不能重复使用旧 ModelRun，也不能把 ordinal 直接当 CallNo。
- 提交回执丢失时从 exact journal/Decision/Tool receipt 验证结果；确已发出但无法证明结果的外部调用，按
  上节 Attempt/fence 规则归约为 UNKNOWN 并按完整 reservation 结算。决策 UNKNOWN 的输入/输出计费为
  65536/512，不能写零或重新调用 Provider。
- 新 Run 与 Answer 在 caller-owned GORM scope 内原子创建；返回 Run 必须仍是精确 queued candidate。
  `created_at`、`updated_at`、`deadline_at` 用 `Time.Equal` 比较同一时刻，不能对整个含时间的 Run 使用
  `reflect.DeepEqual`。PostgreSQL 解码的 Location 差异不是漂移；真实 1µs 偏移、配置/hash/预算或状态漂移仍拒绝。
- 已有幂等请求按持久 Workflow Definition 与 Analysis Run 校验 Workspace、Conversation、Question、Answer、
  Workflow Run 和创建时刻，保留原 Definition/hash/catalog/policy/config。历史 Run 可以已 running/terminal，
  不能按新建 queued 条件校验，也不能拿当前配置重新生成预算或 deadline。
- 兼容 API 的 replay 路径不获取当代模型 generation，也不要求当前 Worker capability；static/managed Chat
  disabled 或 Worker release 后仍保留历史读取。只有新请求需要当前精确 readiness；这不授权旧二进制解释 v2 事实。

### 发布、安全与可观测性

- completed Answer 必须由 immutable candidate 投影，绑定 Synthesis Model Run，同时要求 exact Citation
  validation receipt 和独立 succeeded Review Model Run；Review `passed=false` 不能发布 completed。
- v2 Candidate 使用 `schema_version=2`；`finish` 只结束取证。没有可用 ReadSource 事实时，由
  `synthesize_answer` 引用持久 finish Decision 发布 evidence-insufficient refusal，不能把模型自行结束当成成功发布证明。
- v2 loop Citation 使用 `candidate_id=null, candidate_hash=null`；最终 Citation 必须在
  `validate_citations` 节点绑定 immutable candidate。loop receipt 不能替代独立 final receipt，也不能充当
  CitationInvalid 的拒绝 proof；失败的 final Citation 禁止开始 Review，随后在 `review_publish` 正确终止。
- v2 同一 immutable 来源 tuple 的多次读取与 alias 作为独立事实保留，公开 Citation 按来源 tuple 去重。
  成功预算满足 `model_calls = tool_calls + 2`；Git 可以未调用，此时公开字段为 `null`。调用过 Git 时仅使用
  最新的真实 Git receipt，不能制造默认 clean/零计数。
- budget/deadline/cancel/runtime failure 可能发生在已有成功决策前缀之后。终态事务先关闭 loop ModelRun
  前缀，再写 publication/termination proof、Run/Answer 与 Event/Audit，最后 force deferred constraints；
  不能在 proof 尚未写入时提前 force，也不能只关闭 Workflow 而遗留 pending Answer。
- Retrieval Planner 的 `workspace_analysis_model_result.subject_candidate_id` 与
  `subject_candidate_hash` 在 clarification 路径允许同时为 `NULL`；PostgreSQL loader 必须先扫描为 nullable
  值，再由领域校验拒绝仅一侧存在或非法绑定。不得把 nullable 列直接扫描到 `string`。
- refusal、clarification、failed、cancelled 和 Unknown 都要关闭 pending Answer。没有可证明 Model/Tool Call 的
 编排失败使用 `WORKSPACE_ANALYSIS_RUNTIME_FAILED`，且 `model_run_ref=null`；已有 Call 事实的失败继续使用
  对应 MODEL/TOOL/RESULT_UNKNOWN 原因。
- Proposal 建议只含公开 Citation ID、用户摘要和服务端固定 `/proposals` 链接；不得创建/预填 Proposal、
  写文件、Commit、Approval 或 Write Authorization。
- Event、Problem、Audit、Metric、Trace、Timeline 和安全日志不得包含 Prompt、正文、路径、请求参数、
  Provider/Tool 私有 ID、receipt body/private binding、Secret 或高基数 Workspace/Run/Answer ID 标签。

### Stop 与版本冲突恢复

- 仅 `BusinessApiError(status=409,errorCode=WORKFLOW_VERSION_CONFLICT)` 可进入下一轮恢复；
  `WORKFLOW_CONTROL_CONFLICT`、未知/非法 Problem、网络错误、5xx 和其它 409 必须立即返回。
- 每轮重新 GET 当前 Answer，并精确复核 Workspace、Conversation、Answer、Workflow Run、
  `publication_status=pending` 和可取消 Workflow status；新 version 必须大于上一轮已尝试 version。
- 任一身份漂移、终态、缺失 Answer、GET 失败、version 相等/倒退或第五次冲突都 fail closed；不得发送
  第六次 cancel。成功响应仍须绑定同一 Run、`cancel_requested=true` 且 version 大于最后一次请求。
- 浏览器诊断只允许输出有界的 HTTP status 与稳定 `error_code` 序列，不输出 Problem message/body。

## 4. Validation & Error Matrix

| 条件 | 必须结果 |
| --- | --- |
| mode 省略或显式 `rag` | 保持历史 RAG request hash、Definition 和行为 |
| HTTP v1 新建动态分析，或读取/重放持久 Definition v2 | `409 CONVERSATION_API_VERSION_UNSUPPORTED`；无新 ID、Workflow、Job、Analysis Run 或通知 |
| HTTP v1/v2 使用另一版本的 Turn cursor | `400 CONVERSATION_CURSOR_INVALID`，不调用业务查询 |
| mode 未知 | `WORKSPACE_ANALYSIS_MODE_INVALID`，不创建 Workflow |
| `workspace_analysis` 携带 `allow_web=true` 或不支持 Scope | `WORKSPACE_ANALYSIS_SCOPE_UNSUPPORTED` |
| 新请求的 API/Worker flag、Definition、Tool catalog、policy/config revision 任一不一致 | `WORKSPACE_ANALYSIS_CAPABILITY_UNAVAILABLE`，新建 fail closed；不以当前配置改绑历史请求 |
| 启用前旧 API 收到带 `mode` 的请求 | 严格拒绝未知字段，不创建 Question/Workflow；省略 mode 的固定 RAG 保持可用 |
| Workspace 已存在工作区分析 Question/Answer | 读流量只能进入识别新 mode、hash 和 Answer 联合的兼容 API；不得因 Run 已排空而回退到旧 reader |
| v1 模型选择 Tool；或 v2 选择目录外工具/错误版本、伪造 Workspace/Attempt/lease/fence | 对应 Tool Executor 前拒绝，不产生该 Tool Call |
| v2 持久 Decision 缺字段、unknown/duplicate key、非法 action/ref | `DecodeWorkspaceAnalysisDecision` 返回 `AGENT_WORKSPACE_ANALYSIS_DECISION_INVALID`；不执行后继工具 |
| v2 Source/Citation ref 未由本 Run 搜索/读取、跨 Workspace 或 tuple/hash 漂移 | 授权/证据校验拒绝，不能仅凭 alias 格式执行 |
| 调用前 deadline/budget/cancel/lease 不满足 | 不产生 Call；由数据库事实导出稳定终止或 conflict |
| 合法 frozen-v1 operation 前缀 | canonical 下一 reservation 始终可容纳；不得构造 `BUDGET_EXHAUSTED` 正例 |
| 非因果 `BUDGET_EXHAUSTED` termination proof | PostgreSQL 拒绝，不得关闭 Run/Answer |
| v2 完成 12 次取证决策/工具后尝试第 13 次 Decision | 提交 PENDING admission denial，零新 Call/Reservation；以 causal proof 发布 `WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED` |
| v2 授权前 deadline 或已持久 PENDING deadline denial | 不调用外部依赖；从相应数据库事实生成终态，不伪造已调用失败 |
| Call 成功但 receipt contract/binding 无效 | 原子记录 receipt failure，Operation failed，不能发布输出 |
| commit 状态不确定 | exact lookup/reconcile；未证明成功前不向后继返回输出 |
| 同 Attempt 看见 `STARTED` | reconcile/in-progress；不得提前 Unknown |
| replacement Attempt 看见旧 `STARTED` | 原子 `UNKNOWN` + `UNKNOWN_CHARGED`，禁止第二次外部调用 |
| Citation/Review 不通过 | 发布对应 refusal/failed 终态，不发布 Draft |
| v2 仅有 finish 或 loop Citation，缺独立 candidate-bound final Citation/Review | 拒绝成功 publication proof |
| 同一时刻不同时间 Location | 新 Run 精确比较通过；真实时间、配置、预算或状态变化返回 `AGENT_WORKSPACE_ANALYSIS_RUN_START_CONFLICT` |
| 兼容 API 下历史 replay，当前 Chat disabled / Worker capability 已释放 | 返回原持久绑定，不创建新 ID/Run/Call，也不重新获取模型 generation |
| 无 Call 事实的节点编排失败 | `WORKSPACE_ANALYSIS_RUNTIME_FAILED`，Answer failed，Model Run 为空 |
| terminal Run | 禁止授权新 Model/Tool Call；exact publication lookup 只可回放 |
| Stop 返回精确 `WORKFLOW_VERSION_CONFLICT` 且权威 version 前进 | 使用新 version/新幂等键重试，总 POST 不超过 5 次 |
| Stop 返回控制冲突、未知 409、网络/5xx、身份漂移、终态或 version 未推进 | 立即 fail closed，不继续 GET/POST |

## 5. Good / Base / Bad Cases

- Good v1：历史显式工作区分析在相同 Run 内完成 Git -> Search -> 1–3 ReadSource -> Candidate -> Validate ->
  Review -> Answer；数据库可证明 receipt 依赖、预算结算和 Synthesis/Review 双 Model Run，刷新后 Timeline 一致。
- Good v2：模型选择 Search -> Read E1 -> Search -> Read E7 -> finish，再经 Candidate -> 独立 Citation ->
  Review -> Answer；下一决策包含上一工具的受控结果，刷新保留该顺序。另一合法路径可不调用 Git，成功结果显式
  `git_status=null`，引用和 Review 门禁不减少。
- Base：mode 省略继续运行冻结 RAG；工作区分析 flag 关闭或 Worker capability 不新鲜时只返回 capability
  unavailable，历史 Answer/receipt/timeline 仍可读。
- Bad：把 Question 自动切到 RAG、让 v1 模型选择 Tool 或让 v2 绕过冻结目录、在事件中携带正文、根据前端时钟判 deadline、
  response loss 后重新观察 Git/Search、把 runtime orchestration failure 伪装为 MODEL_FAILED、按任意 409
  重试 Stop、把 Planner nullable subject candidate 扫描到非空字符串，或伪造没有 causal denial 的 budget proof。
- Bad v2：每次 Decision 新建 ModelRun、把 replacement CallNo 当全局 ordinal、重放再次调用 Provider/读取最新来源、
  以 loop Citation 替代 final gate、按 phase 排序时间线，或用 `reflect.DeepEqual` 比较含数据库时间的整个 Run。
- Good Stop：Run 在相邻 Node Claim 间连续推进 version；Web 只对精确版本冲突逐轮重读并使用严格递增
  version，最多第五次 POST 后成功或显式失败。

## 6. Tests Required

以下按版本保留验证合同，不能仅因测试名称含 Workspace Analysis 就视为覆盖 v2。
截至 2026-09-09，v2 必要的领域/持久化与标准 PostgreSQL 回归，以及真实 analysis Compose/桌面与窄屏浏览器均已通过：不同调用次数、追加检索、预算终止、exact replay、SSE、Stop 与只读边界已有实际证据。
v1 已有的 Compose、Worker kill/reclaim/restart、OTLP 与浏览器结果保持原验证范围，不能标成 v2 PASS。
确定性 Provider/持久端口 fixture 证明调用、事务与恢复，不等于真实模型质量或完整 River Worker E2E。

- Domain/contract：Question mode/hash、Definition/Graph/Tool hashes、预算公式、Operation/Attempt identity、
  receipt/private binding、Candidate、Answer terminal matrix、Timeline strict decoder。
- HTTP 版本：[边界测试](../../../internal/conversation/http/api_version_test.go)覆盖两个 HTTP 版本与持久 Definition、
  pending/completed/refused/failed/cancelled、匹配 ETag、版本化 status URL 与 cursor；
  [隔离实库](../../../internal/conversation/adapter/postgres/api_version_integration_test.go)覆盖混合分页/latest、
  历史幂等身份与拒绝路径零 Runtime/ID/starter/数据库副作用。
- PostgreSQL：migration fresh/repeated Up 与旧版本数据前向升级、Workspace/FK/immutability、固定锁序、授权/取消/预算竞态、
  Call+receipt+settlement atomicity、response loss、stale fence、replacement Unknown、publication/termination
  proof、capability freshness、runtime failure 与 exact replay。
- v1 River/API：公开 Question 经真实 River 六节点完成；同 Idempotency-Key 终态 replay 不增加 Model/Tool Call
  或事实计数。fault 测试要把 lease loss -> replacement -> Unknown -> Answer terminal 串成一条链，不能只用
  分散单测替代该验收。一个聚合真实 River 门禁必须覆盖除 `BUDGET_EXHAUSTED` 外的 13 个可达终态，
  并逐项反查 publication/termination proof 到 exact operation、Model/Tool Call、artifact 或 receipt failure。
- v1 Budget：遍历全部允许的 synthesis output cap、1–3 个 Source read 和每个 canonical operation 前缀，证明
  reservation 不超过冻结上限；真实 PostgreSQL 必须拒绝没有 causal fixed overage 的 budget proof。
- v1 Clarification：真实 PostgreSQL 持久化 Planner result 的两个 subject candidate 列为 `NULL`，Finalizer
  必须成功发布并 exact replay 同一 Planner Model Run/proof。
- Web：严格 mode/result/timeline decoder、workspace-bound Query key、SSE 只失效、Draft 不覆盖终态、
  Stop 使用权威 Workflow version、连续两次冲突恢复、第五次成功/第五次冲突耗尽、后续控制冲突、
  非法/未知 409、GET 失败、身份/状态漂移和 version 非单调，键盘分段选择、390x844 无横向溢出、
  Proposal 仅 GET Link。
- Browser/Compose：临时 Git/DB/Evidence + deterministic provider；桌面与移动检查 console/network、Citation、
  Timeline、预算、Stop 实际动作和无 Proposal write request。发布前必须重跑固定 RAG smoke。
- v1 首期 Compatibility：使用分析模式引入前和引入后的真实二进制，在只由新 runner 完成迁移的数据库上验证四种 API/Worker
  组合。启用前固定 RAG 均可用，旧 API 严格拒绝新 mode，新 API 在没有匹配 Worker capability 时返回 capability
  unavailable 且零新事实。完整兼容门禁还必须执行 post-fact 阶段：current/current 创建并排空真实 Workspace
  Analysis Run 后，用相同 current API image 以 feature-off 配置重建 API、保持 ingress netns identity，并仅把 Worker
  artifact 回退为 legacy；历史 Answer、Turn、Timeline 及绑定 exact Analysis Run 的 SSE replay 仍可读，新 mode 以非重试 capability unavailable 拒绝，固定 RAG 和 Workspace
  全量事实投影不变。
- 当前仓库只有单一 blind ingress，没有按 Workspace 选择 legacy/current API 的网关。存在新事实时受支持的回滚固定为
  保留 current compatible API version、只回退 Worker artifact；API 可为应用 feature-off 配置而从相同 current image 重建，
  因此该门禁不证明连接无中断。不得宣称 legacy API 可读取新 Question hash、Answer union 或 Timeline。
  未来若引入双 API 路由，必须以 `question.mode='workspace_analysis'` 与其 bound `workspace_analysis_run` 双 marker
  做数据库权威判定，缺失、分裂或查询失败都 fail closed。
- deterministic candidate 屏障必须按每轮随机 generation 绑定 `armed -> entered -> release`。`completed` 浏览器
  路径只有在候选流写入同 generation 的 `entered` 后才能 release；`cancelled/refused` 可能在候选调用前合法终止，
  只要求浏览器已经观察到 pending/Stop 前置并完成对应终态，不得等待不可达的 `entered`，但 release 仍须精确匹配
  当前 `armed` generation，禁止复用静态 latch 或旧 token。
- 最终门禁：`go test ./...`、受影响包 `go test -race`、`go vet ./...`、API/Worker build、前端
  test/lint/typecheck/build、`make openapi-check`、`git diff --check`，并执行 Go/SQL/前端/跨层独立 review。

### v2 必须保留的断言与入口

- [Decision/policy](../../../internal/agent/domain/workspace_analysis_v2_test.go)：严格四字段联合、未知/重复字段、
  预算与 timeout 派生、v1/v2 Run 版本配对。冻结的 v1 Hash/预算不得被 v2 常量覆盖。
- [决策恢复实库](../../../internal/agent/adapter/postgres/workspace_analysis_dynamic_integration_test.go)：提交回执丢失、
  exact receipt、旧 fence、成功前缀 replacement、同 Attempt 不误关 ModelRun、新 CallNo 1 接续全局 ordinal、
  第 13 个 Decision 的真实 PENDING denial 和零新 Call/Reservation。
- [发布实库](../../../internal/agent/adapter/postgres/workspace_analysis_dynamic_publication_integration_test.go)、
  [终态实库](../../../internal/agent/adapter/postgres/workspace_analysis_dynamic_finalizer_integration_test.go) 与
  [拒绝/去重实库](../../../internal/agent/adapter/postgres/workspace_analysis_dynamic_rejection_integration_test.go)：
  无 Git 成功、finish 无证据拒答、独立 final Citation、Review、引用/模型/工具失败、UNKNOWN、budget/deadline、
  同 tuple 多 alias 公开去重与 Finalizer exact replay。不得只覆盖 happy path。
- [Runtime hook 实库](../../../internal/agent/adapter/postgres/workspace_analysis_dynamic_terminal_hook_integration_test.go)：
  成功前缀取消、PENDING denial 后取消、真实 Runtime failure、Run/Node/Attempt/Answer/proof 原子闭合和重放。
  有前缀的 failure 测试不能证明 `node_attempt=0` 且尚无 Attempt 行的独立分支已覆盖。
- [Tools 实库](../../../internal/agent/adapter/postgres/workspace_analysis_dynamic_tools_integration_test.go)：
  Search/Read/loop Citation/final Citation 的精确授权、32 aliases/8 reads 上限、跨 Workspace/租约/取消、
  已完成工具不重复执行和 v1 parity。
- [时间语义单测](../../../internal/agent/application/workspace_analysis_v2_runs_test.go)、
  [v2 scoped starter 实库](../../../internal/agent/adapter/postgres/workspace_analysis_dynamic_runs_integration_test.go) 与
  [历史 capability replay](../../../internal/agent/adapter/postgres/workspace_analysis_model_operations_integration_test.go)：
  同一时刻不同 Location 成功；1µs/config/catalog/status 漂移拒绝；Answer + Run 回滚、running 后历史重放、
  错误 Answer 绑定拒绝、Worker release 后 replay 不新增记录。
- [Eino loop](../../../internal/agent/adapter/eino/workspace_analysis_loop_test.go)、
  [Workflow executor](../../../internal/agent/adapter/workflow/workspace_analysis_v2_executor_test.go) 与
  [API 接线](../../../cmd/api/workspace_analysis_hot_test.go)：不同模型决策导致不同顺序，后续输入含受控结果，
  追加检索/读取、自主 finish、typed denial、未知结果、static/managed Chat disabled 的历史 replay。
- [Web decoder](../../../web/src/api/conversation.test.ts) 与
  [浏览器入口](../../../web/e2e/workspace-analysis.smoke.spec.ts)：v1/v2 分支、nullable Git、动态顺序、真实预算、
  刷新恢复、Stop 与 Draft/终态隔离。完整 v2 HTTP/Compose 使用
  [生产烟测入口](../../../deploy/compose-workspace-analysis-v2-smoke-functions.sh)；入口存在不等于已执行通过。

## 7. Wrong vs Correct

### Wrong: 同 Attempt 立即归约正在执行的 Call

```text
STARTED + same Attempt delivery -> UNKNOWN
```

这可能把仍在 Provider/Tool 中执行的调用提前关闭，并让后继把一个未确定结果当作已归约事实。

### Correct: replacement Attempt 拥有 Unknown 归约权

```text
same Attempt     -> RECONCILE_EXACT_CALL / in progress
lease expires    -> old Attempt lease_lost
replacement      -> lock + fence -> Call/Operation/Reservation UNKNOWN atomically
finalizer        -> RESULT_UNKNOWN
```

### Wrong: 通过历史 Down 回退 Workspace Analysis schema

```text
应用旧版 Down SQL -> 删除 00088–00091 对象 -> 启动旧 Worker
```

Atlas 迁移是 forward-only，历史 Down 已删除；逆向 DDL 会破坏后继对象与已持久化事实，且不会产生可信的 Atlas revision 状态。

### Correct: 兼容应用回退或新增 fix-forward

```text
兼容当前 schema 的旧应用 -> 停止新提交 -> 回退 Worker
不兼容的 schema -> 新增 Atlas 前向迁移修复，或从升级前备份恢复
```

测试旧版本边界时只在 disposable 数据库上用 `MigrateAtlasToVersion` 构造迁移前缀，再向前升级；不得执行 Down。

每一步都断言实际版本与 SQLSTATE；测试前缀必须只向前升级，有持久事实时不得通过删表伪造回滚。

### Wrong: 按状态码无限或通用重试 Stop

```text
409 -> retry
409 -> retry
... without authority refresh or bound
```

这会把 terminal/control/idempotency 冲突误当成 stale version，并可能跨 Workspace/Run 使用过期投影。

### Correct: 有界权威 CAS 恢复

```text
cancel(vN)
  -> exact WORKFLOW_VERSION_CONFLICT
  -> GET bound pending Answer
  -> require vN+K > vN
  -> cancel(vN+K) with version-bound idempotency key
  -> stop after at most 5 POSTs; every other error fails closed
```

`cancelled/refused` 浏览器路径仍不得等待可能永远不会出现的 candidate `entered`；只有 completed 路径
把同 generation 的 `entered` 作为 release 前置。

### Wrong: 为覆盖保留枚举伪造预算耗尽

```text
valid frozen-v1 prefix -> manually build BUDGET_EXHAUSTED proof -> mark test passed
```

这绕过了 operation catalog、reservation 和数据库因果约束，不能证明生产可达行为。

### Correct: 证明不可达并拒绝伪造 proof

```text
every canonical prefix <= frozen budget
non-causal BUDGET_EXHAUSTED proof -> PostgreSQL rejection
```

上述不可达性质只属于冻结 policy v1。当前 policy v2 已允许继续选择工具，必须验证真实 admission denial，
不能把 v1 的性质推广到 v2：

```text
v2: 12 个真实 DECISION -> 各自工具成功
next DECISION(13) -> committed PENDING denial, no Call/Reservation
finalizer -> close prefix -> causal budget proof + failed Run/Answer -> force constraints
```

### Wrong: 按时间结构体内部表示判断持久化漂移

```go
reflect.DeepEqual(persistedRun, queuedRun)
```

PostgreSQL 解码的时间可能采用不同 Location；同一时刻会因此被误判并返回 Run start conflict。

### Correct: 逐字段校验，时间比较同一时刻

```go
sameCreatedAt := persistedRun.CreatedAt.Equal(queuedRun.CreatedAt)
sameQueuedRun := sameWorkspaceAnalysisQueuedRun(persistedRun, queuedRun)
```

使用共用比较器同时校验全部不可变绑定、预算与 queued 状态；只有时间采用语义相等。禁止以截断、容差或
只比较 ID 掩盖真实漂移。历史 replay 则读取原 Run，不用当前 config 重建一个 queued 候选进行比较。

### 稳定事实来源

- Definition 与 Node kind/hash：[v1](../../../internal/conversation/workflow/workspace_analysis_contract.go)、
  [v2](../../../internal/conversation/workflow/workspace_analysis_v2_contract.go)；Eino 内层与项目恢复边界见
  [Eino Runtime 规范](./eino-runtime-adoption-gates.md) 和 [v2 loop](../../../internal/agent/adapter/eino/workspace_analysis_loop.go)。
- Decision 与预算：[决策合同](../../../internal/agent/domain/workspace_analysis_v2_decision.go)、
  [policy/deadline](../../../internal/agent/domain/workspace_analysis_v2_policy.go)、
  [Run starter/budget](../../../internal/agent/application/workspace_analysis_v2_runs.go)、
  [journal/denial 端口](../../../internal/agent/application/workspace_analysis_v2_persistence.go)。
- 精确工具与 Evidence：[v2 catalog](../../../internal/tools/adapter/catalog/workspace_analysis_dynamic.go)、
  [动态工具授权](../../../internal/tools/adapter/postgres/gorm_workspace_analysis_dynamic_authority.go)、
  [Citation v4](../../../internal/tools/adapter/retrieval/validate_citation_v4.go)。
- 持久化与发布：[Decision 授权](../../../internal/agent/adapter/postgres/gorm_workspace_analysis_decision_authorization.go)、
  [Decision 结算](../../../internal/agent/adapter/postgres/gorm_workspace_analysis_decisions.go)、
  [v2 Finalizer](../../../internal/conversation/adapter/postgres/gorm_workspace_analysis_finalizer_v2.go)、
  [00098](../../../atlas/migrations/00098_workspace_analysis_dynamic.sql)、
  [00099](../../../atlas/migrations/00099_workspace_analysis_dynamic_tools.sql)；事务边界遵守 [GORM 规范](./gorm-persistence.md)。
- Dispatch 与恢复：[版本化派发](../../../internal/conversation/adapter/postgres/dispatch_contract.go)、
  [API starter](../../../cmd/api/workspace_analysis.go)、
  [只读 replay](../../../internal/agent/application/workspace_analysis_replay.go)、
  [Run 比较器](../../../internal/agent/application/workspace_analysis_runs.go)。
- 公共投影：[v2 Answer](../../../internal/conversation/domain/workspace_analysis_result_v2.go)、
  [v2 Timeline](../../../internal/conversation/domain/workspace_analysis_timeline_v2.go)、
  [OpenAPI](../../../api/openapi/openapi.json)、[Web decoder](../../../web/src/api/conversation.ts)、
  [Timeline 展示](../../../web/src/features/rag/WorkspaceAnalysisTimeline.tsx)。
