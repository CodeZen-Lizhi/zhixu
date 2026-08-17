# Workspace Analysis 合同

## 1. Scope / Trigger

- 修改 `workspace_analysis` Question mode、`workspace-analysis@1` Definition、六个 Node Executor、四个精确
  Tool、Model/Tool Operation、预算、receipt/candidate、终态发布、时间线、SSE/Audit/Metric、API/Worker
  readiness 或 Web 展示时，必须同时遵守本规范。
- 该能力是固定 RAG 之外的独立显式模式。默认 Question mode 仍是 `rag`；任何依赖、合同或 readiness
  不满足时只能对 `workspace_analysis` fail closed，禁止静默改投 RAG。
- 首期是静态六阶段、只读、硬预算流程，不是开放 ReAct 循环。正常阶段推进由 Workflow successor
  负责，不用 Node retry 模拟下一轮 Agent。
- 修改 Web Stop、Workflow control version、控制幂等键或取消烟测时，必须保留本节定义的严格有界
  CAS 恢复；不得用通用 HTTP retry 或等待候选屏障掩盖运行中版本推进竞态。

## 2. Signatures

### HTTP / Web

```text
POST /api/v1/conversations/{conversation_id}/questions
request.mode = "rag" | "workspace_analysis"   # omitted -> rag

GET /api/v1/answers/{answer_id}/analysis-timeline?workspace_id={workspace_id}
POST /api/v1/workflows/{run_id}/cancel
```

- Answer 终态类型固定为 `workspace_analysis`、`workspace_analysis_refusal`、`clarification` 或
  `workspace_analysis_termination`；`model_run_ref` 的必需/可空规则由终止原因决定。
- Timeline snapshot 是权威事实；SSE 只触发 Query 失效，Answer Draft SSE 只承载未验证的临时 Token。
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

### Database / Config

- 主要事实：`agent.workspace_analysis_run`、`agent.workspace_analysis_operation`、
  `agent.workspace_analysis_budget_reservation`、`agent.workspace_analysis_model_result`、
  `agent.workspace_analysis_candidate`、`workflow.tool_result_receipt`、
  `workflow.tool_result_receipt_failure`、`agent.workspace_analysis_publication_proof`、
  `agent.workspace_analysis_termination_proof`、`agent.workspace_analysis_worker_capability`、
  `agent.workspace_analysis_tool_refusal`。
- 配置入口固定为：

```text
ZHIXU_WORKSPACE_ANALYSIS_API_ENABLED=false
ZHIXU_WORKSPACE_ANALYSIS_WORKER_ENABLED=false
ZHIXU_WORKSPACE_ANALYSIS_CONFIG_REVISION=1
```

- 迁移 `00085`–`00091` 互相扩展函数、trigger 和列。测试某个旧版本 Down 时必须先按版本逆序
  回退全部后继迁移；不能只调用一次 `Down()` 后假设目标版本已回退。

## 3. Contracts

### Definition 与工具目录

- Definition 固定为 `workspace-analysis@1`，Node 顺序固定为：
  `inspect_workspace -> retrieve_evidence -> read_evidence -> synthesize_answer ->
  validate_citations -> review_publish`。
- 目录只能包含 `ReadGitStatus@2`、`SearchKnowledge@2`、`ReadSource@3`、`ValidateCitation@3`，均为
  `TRUSTED_WORKFLOW_ONLY`、`READ_LOCAL`、无副作用。模型不能选择 Tool 名称、版本或授权身份。
- Git 输出只能包含 branch/head/object format/clean 和 staged、unstaged、untracked、conflict 四类计数；
  禁止路径、文件名、porcelain 行、Diff 或正文。
- Search 最多五个短引用，服务端冻结前 1–3 个；ReadSource 只能读取该 same-Run frozen tuple；
  ValidateCitation 只能解析当前 immutable candidate 的完整引用集合。

### Operation、Attempt 与恢复

- logical operation key 固定包含 Analysis Run、node key、operation kind、ordinal、request hash；Attempt
  不进入 key。`first_node_attempt_id` 绑定最初外部调用，`latest_node_attempt_id` 只表示当前恢复围栏。
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

### 发布、安全与可观测性

- completed Answer 必须由 immutable candidate 投影，绑定 Synthesis Model Run，同时要求 exact Citation
  validation receipt 和独立 succeeded Review Model Run；Review `passed=false` 不能发布 completed。
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
| mode 未知 | `WORKSPACE_ANALYSIS_MODE_INVALID`，不创建 Workflow |
| `workspace_analysis` 携带 `allow_web=true` 或不支持 Scope | `WORKSPACE_ANALYSIS_SCOPE_UNSUPPORTED` |
| API/Worker flag、Definition、Tool catalog、policy/config revision 任一不一致 | `WORKSPACE_ANALYSIS_CAPABILITY_UNAVAILABLE`，仅新模式 fail closed |
| 启用前旧 API 收到带 `mode` 的请求 | 严格拒绝未知字段，不创建 Question/Workflow；省略 mode 的固定 RAG 保持可用 |
| Workspace 已存在工作区分析 Question/Answer | 读流量只能进入识别新 mode、hash 和 Answer 联合的兼容 API；不得因 Run 已排空而回退到旧 reader |
| 非 exact Tool、旧版本、模型选择 Tool、伪造 Workspace/Attempt/lease/fence | Executor 前拒绝，不建立副作用事实 |
| 调用前 deadline/budget/cancel/lease 不满足 | 不产生 Call；由数据库事实导出稳定终止或 conflict |
| 合法 frozen-v1 operation 前缀 | canonical 下一 reservation 始终可容纳；不得构造 `BUDGET_EXHAUSTED` 正例 |
| 非因果 `BUDGET_EXHAUSTED` termination proof | PostgreSQL 拒绝，不得关闭 Run/Answer |
| Call 成功但 receipt contract/binding 无效 | 原子记录 receipt failure，Operation failed，不能发布输出 |
| commit 状态不确定 | exact lookup/reconcile；未证明成功前不向后继返回输出 |
| 同 Attempt 看见 `STARTED` | reconcile/in-progress；不得提前 Unknown |
| replacement Attempt 看见旧 `STARTED` | 原子 `UNKNOWN` + `UNKNOWN_CHARGED`，禁止第二次外部调用 |
| Citation/Review 不通过 | 发布对应 refusal/failed 终态，不发布 Draft |
| 无 Call 事实的节点编排失败 | `WORKSPACE_ANALYSIS_RUNTIME_FAILED`，Answer failed，Model Run 为空 |
| terminal Run | 禁止授权新 Model/Tool Call；exact publication lookup 只可回放 |
| Stop 返回精确 `WORKFLOW_VERSION_CONFLICT` 且权威 version 前进 | 使用新 version/新幂等键重试，总 POST 不超过 5 次 |
| Stop 返回控制冲突、未知 409、网络/5xx、身份漂移、终态或 version 未推进 | 立即 fail closed，不继续 GET/POST |

## 5. Good / Base / Bad Cases

- Good：显式工作区分析在相同 Run 内完成 Git -> Search -> 1–3 ReadSource -> Candidate -> Validate ->
  Review -> Answer；数据库可证明 receipt 依赖、预算结算和 Synthesis/Review 双 Model Run，刷新后 Timeline 一致。
- Base：mode 省略继续运行冻结 RAG；工作区分析 flag 关闭或 Worker capability 不新鲜时只返回 capability
  unavailable，历史 Answer/receipt/timeline 仍可读。
- Bad：把 Question 自动切到 RAG、让模型选择 Tool、在事件中携带正文、根据前端时钟判 deadline、
  response loss 后重新观察 Git/Search、把 runtime orchestration failure 伪装为 MODEL_FAILED、按任意 409
  重试 Stop、把 Planner nullable subject candidate 扫描到非空字符串，或伪造 budget-exhausted 正例。
- Good Stop：Run 在相邻 Node Claim 间连续推进 version；Web 只对精确版本冲突逐轮重读并使用严格递增
  version，最多第五次 POST 后成功或显式失败。

## 6. Tests Required

- Domain/contract：Question mode/hash、Definition/Graph/Tool hashes、预算公式、Operation/Attempt identity、
  receipt/private binding、Candidate、Answer terminal matrix、Timeline strict decoder。
- PostgreSQL：migration Up/Down guard、Workspace/FK/immutability、固定锁序、授权/取消/预算竞态、
  Call+receipt+settlement atomicity、response loss、stale fence、replacement Unknown、publication/termination
  proof、capability freshness、runtime failure 与 exact replay。
- River/API：公开 Question 经真实 River 六节点完成；同 Idempotency-Key 终态 replay 不增加 Model/Tool Call
  或事实计数。fault 测试要把 lease loss -> replacement -> Unknown -> Answer terminal 串成一条链，不能只用
  分散单测替代该验收。一个聚合真实 River 门禁必须覆盖除 `BUDGET_EXHAUSTED` 外的 13 个可达终态，
  并逐项反查 publication/termination proof 到 exact operation、Model/Tool Call、artifact 或 receipt failure。
- Budget：遍历全部允许的 synthesis output cap、1–3 个 Source read 和每个 canonical operation 前缀，证明
  reservation 不超过冻结上限；真实 PostgreSQL 必须拒绝没有 causal fixed overage 的 budget proof。
- Clarification：真实 PostgreSQL 持久化 Planner result 的两个 subject candidate 列为 `NULL`，Finalizer
  必须成功发布并 exact replay 同一 Planner Model Run/proof。
- Web：严格 mode/result/timeline decoder、workspace-bound Query key、SSE 只失效、Draft 不覆盖终态、
  Stop 使用权威 Workflow version、连续两次冲突恢复、第五次成功/第五次冲突耗尽、后续控制冲突、
  非法/未知 409、GET 失败、身份/状态漂移和 version 非单调，键盘分段选择、390x844 无横向溢出、
  Proposal 仅 GET Link。
- Browser/Compose：临时 Git/DB/Evidence + deterministic provider；桌面与移动检查 console/network、Citation、
  Timeline、预算、Stop 实际动作和无 Proposal write request。发布前必须重跑固定 RAG smoke。
- Compatibility：使用真实上一版本和当前版本二进制，在只由新 runner 完成迁移的数据库上验证四种 API/Worker
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

### Wrong: 单次 Down 假设已到目标版本

```go
provider.Down(ctx)
provider.ApplyVersion(ctx, 87, false)
```

当 head 是 `00091` 时，第一次 Down 只回到 `00090`，后续对象仍可能依赖 `00087` 的列或函数。

### Correct: 严格逆序回退后继版本

```go
for _, version := range []int64{91, 90, 89, 88} {
    if _, err := provider.ApplyVersion(ctx, version, false); err != nil {
        t.Fatal(err)
    }
}
if _, err := provider.ApplyVersion(ctx, 87, false); err != nil {
    t.Fatal(err)
}
```

每一步都断言实际版本与 SQLSTATE；有持久事实的 guarded Down 必须拒绝，不能通过删表绕过。

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

稳定枚举继续保留以兼容持久合同；只有未来预算/operation policy 变更使某个合法前缀确实超额时，才新增
正向 River 场景。
