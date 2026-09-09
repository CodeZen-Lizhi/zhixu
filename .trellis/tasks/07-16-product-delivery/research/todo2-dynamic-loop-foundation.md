# Research: TODO2 持久动态工具循环 foundation 交接

- Query: 原始 TODO2 动态工具循环如何在保留 workspace-analysis@1 历史合同的前提下，复用现有 Eino、Model/Tool operation、预算、receipt 与 Finalizer。
- Scope: internal；Eino 官方资料由主会话核对，本研究核对本仓 vendor。
- Date: 2026-09-09
- Active task: .trellis/tasks/07-16-product-delivery
- 状态: 研究交接稿；事实与建议分开。未实施代码、迁移、测试、Git 操作或部署。
- 需求依据: 同目录 todo2-dynamic-loop-prd.md 的 R1–R9 / AC-D1–D6。主会话已另写 todo2-dynamic-loop-design.md，具体冻结合同以该设计和 implementation owner 的一致约定为准。

## Findings

### 已确认的实施边界

- v1 是固定六阶段流程，其已有交付证据不等于原始动态循环已完成。不能把“仅动态生成 Search query”当成本轮完成。
- 新行为使用 workspace-analysis@2；保留 v1 Definition、policy、catalog、hash、历史读取和 replay，不原地改写。
- Eino v0.9.13 是既定生产 Runtime。继续使用 ChatModelAgent / ToolsNode，不写第二套通用 ReAct，不引入生产 Eino checkpoint。
- 模型必须可依据上一工具的安全结果选择下一工具，支持第二轮 Search、追加任意已授权 Source、跳过 Git、自主结束。四类可选能力为 Git、Search、ReadSource、ValidateCitation。
- 主会话已锁定公共 phase=decide_next、成功 Answer/Timeline schema_version=v2、git_status nullable、Run-global E1..E32。时间线按 journal 的全局 sequence 排序。
- 模型结束后仍必须走 candidate → final Citation validation → 独立 Faithfulness Review → Finalizer。模型主动执行的循环内 Citation 校验不能替代与 immutable candidate 绑定的发布校验。
- 主会话为 foundation 预留迁移 00098。00095–00097 属 TODO4；研究角色不创建迁移、不修改 organizing/authoring/interview。

### 文件与已核实硬限制

| 文件 / 位置 | 事实及用途 |
| --- | --- |
| atlas/migrations/00018_agent_runtime.sql:44 | uq_agent_model_run_node_attempt 强制每个 NodeAttempt 只有一个 ModelRun。 |
| atlas/migrations/00018_agent_runtime.sql:88 | ModelCall.call_no 范围 1–32；每个 ModelRun 的 call_no 唯一，活动 STARTED Call 也唯一。 |
| atlas/migrations/00085_workspace_analysis_persistence.sql:21 | 后续 phase guard 已允许 AGENT → AGENT；不能只看 00018 初始 phase CHECK 判断不能多轮。 |
| internal/agent/domain/workspace_analysis_operation.go:126 | v1 固定九个 logical slot：Git1、Plan1、Search1、Source1–3、Synthesis1、Validation1、Review1。 |
| internal/agent/domain/workspace_analysis_operation.go:157 | OperationKey.Validate 只接受这些槽，无法直接承载多轮工具。 |
| internal/agent/domain/workspace_analysis_persistence.go:202 | Run 校验冻结 DefinitionVersion / PolicyVersion=1。 |
| internal/agent/domain/workspace_analysis_persistence.go:243 | v1 成功统计硬要求 ModelCalls=3、ToolCalls=SourceReads+3、SourceReads=1–3。 |
| internal/agent/domain/workspace_analysis_persistence.go:768 | v1 budget 固定 nodes6/models3/tools6/sources3，费用为 nil。 |
| internal/agent/domain/workspace_analysis_policy.go | v1 deadline、预算和成功路径冻结，不能原地放宽常量来实现 v2。 |
| internal/agent/application/workspace_analysis_model_execution.go:511 | 当前模型执行校验固定 CallNo==1。 |
| internal/agent/adapter/postgres/gorm_workspace_analysis_model_finalize.go:201 | 当前授权每次 INSERT 新 ModelRun，不能直接重复调用作为动态决策持久化。 |
| internal/agent/adapter/postgres/gorm_workspace_analysis_model_finalize.go:486 | 当前完成单次 Call 时终结 ModelRun；v2 decide_next 需要不同生命周期。 |
| atlas/migrations/00085_workspace_analysis_persistence.sql:778 | workspace_analysis_model_result 对 model_run_id 唯一，现有种类仅 Planner/Review，不能容纳一个 Run 的每次 decision。 |
| internal/workflow/adapter/postgres/gorm_execution_fence.go:39 | LockWorkspaceAnalysisExecutionScoped 可复用，Workflow owner fence 本身不写死 v1。 |
| internal/workflow/adapter/postgres/gorm_runtime_claim.go:139 | replacement 前先把旧 Attempt 标为 lease_lost；UNKNOWN 归约必须使用这个真实状态链。 |

### ModelRun / decision journal：必须解决的设计点

推荐 v2 decide_next 每个 Attempt 一个 ModelRun，内部多条 AGENT ModelCall。Synthesis / Review 保留各自独立节点和 ModelRun。不删除全局 per-Attempt 唯一约束。

1. 新增 Workspace Analysis 专属 decision journal，建议表名 agent.workspace_analysis_decision。每个模型决策有一条 append-only canonical document，关联 exact operation、ModelCall、request_hash 与 parent digest。
2. decision ordinal 是 Analysis Run 级编号，replacement 后不重置；ModelCall.call_no 仍是其所属 ModelRun 内的编号。两个编号不要混用。
3. 第一次真实 Provider 调用才为当前 Attempt 创建 ModelRun；后续 decision 复用该 RUNNING ModelRun 并递增 CallNo。仅回放已有前缀时不得新建或收费。
4. 每次模型成功必须在一个 UoW 内提交 Call terminal、decision document、reservation settlement 与 operation terminal；提交确认前不得把模型选出的工具交给 Eino 执行。
5. 普通中间 decision 成功不终结当前 ModelRun。finish decision 成功后可终结该 ModelRun，再推进候选生成节点。
6. crash 可能发生在“所有已有 Calls 均成功，但 ModelRun 仍 RUNNING”。replacement 必须持新 fence 核实旧 Attempt 已失效、每条 Call/decision/settlement 闭合后，明确关闭旧 ModelRun；新 Attempt 只在需要新外部调用时建立自己的 ModelRun。
7. 建议旧 ModelRun 以一个 v2 专属、可验证的 decision checkpoint 结果终结为 SUCCEEDED，表示这一组模型调用已完整持久化，并不表示 Analysis Run 完成。这个 final_result_type、对应 proof/closure guard 和 exact lookup 尚需 foundation 实现核验，不能把没有 finish 的前缀冒充最终答案。
8. 若旧 Attempt 留有 STARTED Call，不得按完整 checkpoint 成功收口：replacement 原子 UNKNOWN + UNKNOWN_CHARGED，随后发布 RESULT_UNKNOWN；禁止重新发送外部调用。
9. Tool 失败、取消、deadline 等退出也要收口已创建的 ModelRun，不能只依赖 Eino 正常结束回调。没有模型调用时不伪造 ModelRun；已有 Call 的终态按可证明事实关闭。

journal 最小事实建议：

| 字段 / 不变量 | 用途 |
| --- | --- |
| analysis_run_id / workspace_id / workflow_run_id / definition_version | 由服务端 authority 与 FK 绑定，不进入模型输入。 |
| decision_ordinal / operation_id / model_call_id | Run 内唯一决策序号及原始执行事实。 |
| sequence / parent_digest / request_hash | 绑定唯一全局步骤、已闭合前缀和 exact request；漂移拒绝。 |
| schema_version / decision_kind / canonical_document / document_hash | 持久化已验证的“调用工具或 finish”，不用 Eino 序列化格式。 |
| exact tool ref / bounded canonical arguments | Search query 等恢复执行必要参数必须持久保存于私有安全 document；只有 hash 无法恢复待执行决策。 |
| chosen operation link | decision 已提交、tool 未开始时恢复同一个被选择操作，不跳到下一次模型调用。 |

operation/journal 的唯一逻辑位置不能把 request_hash 当成可分叉命名空间：同一 Run/sequence 或同一 logical slot 的请求 hash 不同应报冲突，不允许插入第二操作。operation 首次调用 Attempt 身份保持 immutable，latest Attempt 只记录恢复围栏。

### Eino 接法与恢复协议

可复用代码：

- internal/agent/adapter/eino/agent_runtime.go:115：ChatModelAgent、顺序 ToolsNode、关闭 retry/failover。
- 同文件 :242 / :260 / :318：Context 清理、ToolCall middleware、事件校验及取消后 drain。
- internal/agent/adapter/eino/runtime_model.go:134 / :170：现有 ModelCall 记录入口；但它目前是内存 Ledger 加普通 Recorder，不能直接充当持久 Analysis Run 预算事务。
- 同文件 :426 / :563 / :639：canonical request / response 与 Provider ID → 本地顺序引用。
- vendor/github.com/cloudwego/eino/adk/handler.go:139 / :193 / :254：middleware、WrapInvokableToolCall、WrapModel。WrapModel 收到 model.BaseModel[*schema.Message]，不能假定其静态类型就是 ToolCallingChatModel。
- vendor/github.com/cloudwego/eino/adk/chatmodel.go:216 / :314 / :323：state.Messages / ToolInfos、Handlers 及调用顺序。

推荐通过 Workspace Analysis 专属 Port / DTO 接 Eino ChatModelAgent。不要把持久 tool transcript 强塞到现有只支持 system/user/assistant 的 AgentMessage，改变 RAG 合同。

恢复可采用 Eino 从原始安全输入重放闭合前缀：

1. WrapModel 按 Run-global decision ordinal 查询 exact 已提交 decision，并转为本次 Eino 会话的 schema.Message；adapter 生成仅会话内的稳定关联 ID。
2. Tool wrapper 只回放该 decision 绑定的 canonical receipt。若 decision 已提交而 tool 尚未授权，则执行原选择，不再调用模型重选。
3. 第一个尚无持久 decision 的位置才允许经过持久授权调用 Provider。
4. 每次真实模型/工具结果必须先完成各自事务提交，才返回 Eino 继续。commit 状态未知时停止推进，使用 exact lookup/reconcile。
5. 不写生产 Eino checkpoint；不保存或暴露 Provider tool-call ID；不把 UUID、路径、lease/fence、Workspace 身份放进模型 transcript。
6. AfterAgent 仅在成功结束时调用，失败/取消/iteration 超限不能依赖它完成唯一持久终态处理。

单轮 Eino native tool-call bridge 也是可能接法，但实现仍必须证明同样的决策→工具→下一决策持久链，不能由手写通用模型循环替代已批准 Eino Runtime。具体 adapter API 由 runtime owner 与 foundation owner 一次对齐。

### 现有授权/恢复原子性：复用 owner，不绕开约束

- internal/agent/adapter/postgres/gorm_workspace_analysis_model_operations.go:219 的锁顺序是 WorkflowRun → NodeRun → NodeAttempt → AnalysisRun → Operation → Reservation → ModelRun → ModelCall → DB clock。
- 同文件 :422 的 AuthorizeWorkspaceAnalysisModelCall 已区分 PENDING 首次授权、同 Attempt STARTED reconcile、replacement STARTED unknown、SUCCEEDED exact replay；:818 有授权提交响应丢失 lookup。
- internal/agent/adapter/postgres/gorm_workspace_analysis_model_finalize.go:759 提交前 SET CONSTRAINTS ALL IMMEDIATE，v2 不应跳过 deferred closure 检查。
- internal/tools/application/workspace_analysis_execution.go:30 是 prepare/contract → authorize → executeStarted；:128 / :211 的版本、CallNo、AllowedWorkflows 需要明确 v2 分支。
- internal/tools/adapter/postgres/gorm_workspace_analysis_operations.go:15 由 Tools 自有 UoW 调 Agent scoped participant，复用 Prepare / Reserve / Settle / AdvanceAttempt 协作边界。
- 同文件 :65 同 Attempt STARTED reconcile，:106 终态 exact replay，:301 / :367 授权与完成的提交响应丢失恢复。
- internal/tools/adapter/postgres/gorm_workspace_analysis_core.go:28 使用 Workflow scoped fence。

不要把 *gorm.DB 引入 Application/Domain，不从别的 owner 绕过 scoped participant 任意更新其事实。相同 Attempt 的 STARTED 不能因为重投递立即判 Unknown；只有真实 lease 失效和 replacement fenced claim 后才有 UNKNOWN 归约权。

### 工具 catalog、receipt 与 Run-global evidence

- internal/tools/adapter/catalog/workspace_analysis_snapshot.go:21 冻结 v1 四个精确工具：ReadGitStatus@2、SearchKnowledge@2、ReadSource@3、ValidateCitation@3。
- 同文件 :124 的 AllowedWorkflows 精确为 workspace-analysis@1。直接给旧工具追加 v2 会改变 DefinitionHash；必须新增工具版本。
- internal/tools/domain/result_receipt.go:42 也限制 WorkflowVersion=1。
- 建议新增 Git@3 / Search@3 / Source@4 / Citation@4，但这些版本号只是研究建议，最终可用编号由工具/合同 owner 核对锁定；不能把建议当成已注册事实。
- 既有 Git / Search / Source / Citation output/private binding 上限分别为 4KiB/1KiB、32KiB/16KiB、8KiB/4KiB、16KiB/16KiB，可优先复用，不为循环任意放大。
- internal/tools/adapter/retrieval/workspace_analysis.go:61 的 Search@2 最多五 hits，:198 服务端固定选择前 1–3；v2 必须允许模型选择所有已授权返回 hit。
- internal/tools/application/workspace_analysis_authority.go:12 假定每 Run 唯一 Search。
- internal/tools/adapter/postgres/gorm_workspace_analysis_authority.go:95 / :276 硬编码 Search1 与 Source1–3，候选/审核 evidence loader 也要支持 v2。
- v2 在同一 Run 内维护 immutable E1..E32 → exact Search receipt + Citation tuple。第二次 Search 不能把 E1 覆盖；ReadSource 的模型参数仅为短引用，服务端解析同 Run authority。32 个 alias 用尽应有明确拒绝/预算事实，不能静默覆盖或跨 Run 借用。
- 循环 Citation 可以检查已读取 refs；最终 Citation 必须绑定 immutable candidate ID/hash/完整引用集合。建议新增操作种类或 purpose 字段区分两者，避免发布 proof 误取旧校验。
- journal 的公开投影仅包含顺序、phase、工具名与安全摘要；query、snippet、canonical arguments、private receipt binding 都不进入 Timeline/Event/Audit/日志。

### SQL 前向迁移的必要检查面

以下为已发现依赖，不是已完成迁移。不能使用“if v2 then return NEW”来绕过 v1 guard；应为 v2 编写完整分支/helper，同时保留原 v1 强度。

| 00085 对象 / 位置 | v2 影响 |
| --- | --- |
| run:171、contract:236、budget bounds:249、lifecycle:290 | 增加版本化 Definition/policy/budget/成功统计。 |
| operation:329、ordinal:337、unique slot:362、slot:377、result_kind:390、active tool index:457 | 动态 operation 种类、序位、decision result、同 Run 唯一逻辑顺序。 |
| reservation:461 | v2 多轮额度与实际可到达耗尽，完整结算。 |
| workflow.tool_result_receipt:559、exact contract:612、failure:668 | v2 工具精确合同及 append-only failure。 |
| candidate:732、model_result:772 | candidate v2；不要破坏 v1 Planner/Review 或 model_result 的唯一性。新增 decision journal。 |
| publication_proof:838 | git_receipt_id 当前 NOT NULL；v2 跳过 Git 需要版本条件或独立 v2 proof。 |
| termination_proof:893 | 动态下一步骤、预算因果、finish 前失败与无 Call failure。 |
| guard_tool_result_receipt_insert:1164 | 工具版本、output/private binding、Search/Source 依赖。 |
| candidate/model result guard:1570 / :1677 | 新 schema 与 decision/candidate authority。 |
| selected_refs:1799 / source_prefix_matches:1865 | 唯一 Search / 固定 Source 前缀假设不适用 v2。 |
| validation_all_valid:1949 / is_operation_next:2057 | 最终 candidate 校验与动态下一步决策 proof。 |
| publication / termination guard:2144 / :2485 | v2 发布/终止事实完整性。 |
| operation mutation:3119；fence:3233；Attempt binding:3300；kind/phase/tool:3304；result:3355；settlement:3384 | 全部需要版本分支并保持不可变身份。 |
| reservation mutation:3398 / budget totals:3513 | v1 timeout、65536 input、256 plan、1024 review、费用 nil 不能套到 v2。 |
| operation closure:3598 / model_call closure:3656 / tool_call closure:3710 | 每个 Call 仍必须同事务有正确 operation/reservation；一个 ModelRun 多 Call不能漏 closure。 |
| publication closure:3848 / Run mutation:3975 / fixed success:4141 / fixed operations:4221 / Answer mutation:4266 | v2 不再以固定九槽、models3、Git 必有判定成功。 |
| deferred constraint triggers:4630–4695 | 新 journal/alias/proof 同样需要 transaction-level closure 与不可变性。 |

后续迁移也扩展了同一行为：

- 00086:43 deadline_next_slot、:189 preoperation deadline：v2 需绑定真实 next decision/tool，而不是静态 next slot。
- 00087:41 runtime cancellation guard：无运行中 Attempt 的终态 hook 也必须关闭 pending Answer。
- 00088:5 worker capability、:19 contract CHECK：目前只 v1，必须广告 v2 exact hashes，保留 v1。
- 00089：Tool refusal append-only 审计不可丢。
- 00091:96 runtime failure Answer guard、:203 failure proof：无 Call 的编排失败也必须关闭 Answer。

此外，新 ModelRun checkpoint final_result_type 必须核对 generic model_run enum/guard 的最新版本及 domain enum，不只增加 decision 表。

### Finalizer、派发与时间线

- internal/conversation/adapter/postgres/gorm_workspace_analysis_finalizer.go:138 / :168：现有成功 UoW 已包含 fence → authority → candidate/Git/validation/review → Answer slot/draft → proof → Draft PUBLISHED → Run/Answer/Conversation → Events/Audit，优先复用原子边界。
- 同文件 :401 / :530 必取 review / Git authority，v2 必须允许没有 Git 并验证有 Git 时的 exact receipt；:1476 有 WithoutCancel + 短超时的 publication lookup，不可移除。
- workspace_analysis_finalizer_contract.go:74 限制 review_publish，termination authority 与 budget 仍静态；v2 decide_next 的 failure/clarification/预算终止都须显式支持。
- gorm_workspace_analysis_terminal_hook.go:73 / :191 / :530 分别涉及终态、runtime failure、operations locking，不能让 v2 pending Answer 漏收口。
- internal/agent/adapter/eino/workspace_analysis_candidate_stream.go 与 application/workspace_analysis_synthesis_runner.go 可复用真实候选流，修改 evidence authority / candidate schema。
- internal/conversation/adapter/postgres/dispatch_contract.go:52 / :64 当前直接选 RegisteredWorkspaceAnalysisDefinition。
- gorm_dispatch.go:230 / :247 的 replay 再走 startQuestionWorkflow：升级后必须按已经绑定的 Workflow/AnalysisRun 恢复旧 Definition，不能拿新默认 v2 重建老幂等请求。
- gorm_dispatch.go:355 的 AnalysisRun starter 与 Question/Answer/Workflow/River 同事务，继续保持。
- internal/agent/application/workspace_analysis_runs.go:92 / :151 目前 new Run 固定 v1，并检查 replay frozen equality。
- capability/composition 入口：internal/agent/application/workspace_analysis_capability.go:37、cmd/api/workspace_analysis.go:79、cmd/worker/workspace_analysis_capability.go:49 / :73、cmd/worker/main.go:2274 / :3041 / :3409。
- Worker 保留 v1 executor/replay，API 新 dispatch v2；能力广告必须精确匹配 Definition/hash/catalog/policy/config，flag-off 历史读取仍可用。
- internal/conversation/domain/workspace_analysis_timeline.go:189 / :211 / :220 限 v1、models3 和固定 phase counts。
- internal/conversation/adapter/postgres/gorm_workspace_analysis_timeline.go:49 SQL、:170 loader、:199 node mapping 都按静态 phase；v2 必须按持久 sequence，不用时间戳或 phaseOrder 推算。
- Timeline RepeatableRead read-only（:114）、安全摘要、SSE 只失效、草稿与正式 Answer 分离均保留。

### 可立即执行的 owner / Port 拆分

foundation 是唯一 domain / shared migration owner；runtime 不复制另一组 DTO 或同名 SQL migration。以下接口名为沟通建议，最终 Go 签名由已启动实现代理直接对齐：

| Owner | 交付边界 |
| --- | --- |
| dynamic_foundation | v2 Definition/policy、operation/decision/sequence/alias domain、00098、v2 store/guard、Run/Call/decision lifecycle、scoped budget/receipt participants。 |
| dynamic_runtime | WorkspaceAnalysisDynamicRuntime、Eino WrapModel/Tool middleware、精确重放前缀、真实新调用的持久授权/完成、循环退出和 candidate runner 接线。 |
| tools owner（由主会话分配） | 新版本 catalog、已有安全 executor 复用、Run-global alias、Search/Read/loop Citation authority、receipt schema。 |
| conversation / public owner | success/termination Finalizer、Proof、hooks、schema_version v2、nullable Git、sequence Timeline、API/Web/Stop。 |
| integration / 主会话 | API/Worker dispatch/readiness/composition，故障测试与 Docker 验证；统筹迁移和生成文件。 |

最小项目 Port 语义：

- LoadWorkspaceAnalysisLoopState：返回冻结 Run/版本/策略、已提交 decision/receipt 安全前缀、当前 next sequence、alias authority；完整身份保留服务端。
- AuthorizeWorkspaceAnalysisDecision：接收 ExecutionIdentity、logical key、expected parent digest、canonical request hash 与预算需求；返回 CREATED / REPLAY / RECONCILE / terminal。只有 CREATED 可调用 Provider。
- CompleteWorkspaceAnalysisDecision：同事务完成 ModelCall、canonical decision、reservation、operation；finish 可同时关闭当前 ModelRun。提交不确定必须 exact lookup。
- ExecuteWorkspaceAnalysisTool：沿用现有 Tools owner entry，扩展 v2 decision/sequence/alias authority，不由 runtime 越权执行工具。
- LookupWorkspaceAnalysisDecision / LookupWorkspaceAnalysisToolReceipt：以完整不可变绑定恢复提交未知结果，不用“最新一条”猜测。
- CloseWorkspaceAnalysisDecisionAttempt：有 active replacement fence 时关闭已证明完整的旧 ModelRun 前缀，或把真实 STARTED 归约 Unknown。具体 checkpoint result/proof 是待实现核验项。

预算不能以 ADK iteration 作为第二套真相：steps/model/tool/source/token/deadline/已配置费用均由同一持久 Run ledger 硬限制；为 candidate、最终 validation、review 预留额度。缺失 usage 按 reservation 结算；reasoning-inclusive 超单次用量只可使用未预占余额，不能侵占下游预留。具体 v2 数字尚未在本研究中锁定，应由 foundation 按主设计统一定义；公共 validator 不应自行发明另一套数字。

### 必要验证矩阵（待实现后执行）

- 两种确定性 Provider fixture 产生不同真实执行顺序，如 Search→Read→Search→Read→Validate→finish 与 Git→Search→Read→finish；明确 fixture 不代表真实 Provider 质量评测。
- 下一 decision 输入包含上一步安全结果；第二次 Search 不覆盖 E1；可读取非旧固定前 3 个的授权 hit；可无 Git 成功。
- decision commit response loss 不再调用该模型；tool commit loss 不重执行；decision 已提交/tool 尚未授权与 tool 已提交/next decision 未开始两处 crash 可恢复。
- 同 Attempt STARTED 只 reconcile；真实 lease expiry→replacement→UNKNOWN_CHARGED→Answer 终态，外部调用不重发。
- 两 Worker 同 sequence 仅一组 decision/Call/reservation；request hash / parent digest / Workspace / Attempt / receipt binding 漂移拒绝。
- 取消覆盖模型、工具、候选流和发布前边界；迟到结果不发布，reserved 清零，draft aborted。
- 动态 steps/model/tool/source/token/deadline 与配置费用预算可以真实到达 exhaustion，持久 causal proof，不伪造 v1 本来不可到达的预算路径。
- Citation / Review 失败不发布；最终 validation 匹配 immutable candidate refs/hash；无 Call runtime failure 关闭 Answer。
- SQL immutability / mutation / truncate / closure 负测、前向升级和 v1 replay；Timeline global sequence、刷新无重复与私有内容不泄漏。
- 受影响 Go / PostgreSQL / River / persistence-check / OpenAPI / Web / 必要 Docker smoke；M11 暂缓不能免除本次变更的必要验证。

## Related Specs

- .trellis/workflow.md：任务/研究工作流；本次沿用已授权 active task。
- .trellis/spec/backend/workspace-analysis-contract.md：现有 v1 的精确授权、恢复、发布、预算、Stop 与兼容边界。其“首期静态六阶段/模型不能选 Tool”描述是 v1 冻结事实，本轮 v2 的显式授权与 PRD 扩展该范围。
- .trellis/spec/backend/eino-runtime-adoption-gates.md：Eino 唯一 Runtime、无生产 checkpoint、Provider ID 隔离、真实多帧流、下游额度预留与持久发布。
- .trellis/spec/backend/gorm-persistence.md：单池 UoW、scoped participant、锁序、提交响应丢失、禁止 Application/Domain 数据库类型、forward-only migration。

## External References

- Eino version: v0.9.13，以本仓 vendor / go.mod 为准。
- https://www.cloudwego.io/docs/eino/core_modules/eino_adk/agent_implementation/chat_model
- https://www.cloudwego.io/docs/eino/quick_start/chapter_05_middleware
- 上述官方页面由主会话核验；本研究的 middleware 签名证据来自已读 vendor，不声称本研究重新在线查证。

## Caveats / Not Found

- 这是研究阶段已核实代码的交接稿，行号为读取时位置；并行实现可能使行号变化。
- 主设计的最终数字、Go 类型/方法拼写、工具新版本可用编号、checkpoint result/proof 的完整 SQL 形状尚未由本研究验证；由 implementation owner 在既定范围内收敛，不是要求用户额外批准。
- 不要只扩列而忘记 00086–00091 后续 guards，不要通过删除全局 ModelRun per-Attempt 唯一约束或 v2 宽泛 early return 绕开安全一致性。
- 本研究没有运行测试、迁移、构建、Docker 或 Git，没有修改产品实现，不构成 TODO2 已完成或部署成功证据。
