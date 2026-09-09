# Workspace Agent Runtime And Persistence Research

## Conclusion

现有持久 Workflow、River、Model Run/Call、Tool Call 和 SSE 足以承载首期能力，但现有 Eino `AgentRuntime` 不能直接作为可恢复的完整 Agent loop。推荐把 Agent 编排收敛为独立、静态、有限的 Workflow DAG；每个阶段是持久 Node，Eino 只执行单阶段的模型调用或最终 tool-free stream。

## Reusable Facts

- Workflow Claim/Delivery 已绑定 `dispatch_no + delivery_id + owner + attempt_no + node_version`，并以 DB-time lease/fence 隔离旧 Worker（`internal/workflow/application/runtime_contract.go:47`、`internal/workflow/adapter/postgres/runtime_state.go:412`）。
- River payload 只携带 `node_run_id` 与 `dispatch_no`，节点输入和类型从数据库加载，适合静态 Agent 节点恢复（`internal/workflow/adapter/river/args.go:21`）。
- DAG successor 在前序结果提交后由 Runtime 原子创建，适合表达 `Search -> ReadSource -> Synthesis -> Validate`（`internal/workflow/adapter/postgres/runtime_state.go:1307`）。
- Model Call 在 Provider 请求前写 `STARTED`，通过 CAS 终结；失联调用可归约为 `UNKNOWN`（`internal/agent/application/model_call_recorder.go:102`、`internal/agent/adapter/postgres/recovery.go:13`）。
- Tool Call 绑定 Workspace、Workflow Definition、Node Attempt、精确版本、合同 hash、Capability、lease/fence 和结果摘要（`migrations/00019_tool_registry_security.sql:3`）。
- `ops.server_event` 通过 `(workspace_id, source_event_ref)` 幂等追加并按序恢复，适合作为用户时间线增量（`internal/events/adapter/postgres/append.go:27`）。

## Gaps

1. Eino ReAct transcript 只存在于单进程运行，项目端口最终只得到 FinalText、Iterations、ToolCalls 和 Usage（`internal/agent/application/agent_runtime.go:63`、`internal/agent/application/agent_runtime.go:121`）。
2. `RunBudgetLedger` 是 Attempt 内存对象，不能跨多个 Workflow Node 保留授权、已用额度和稳定终止原因（`internal/agent/application/run_budget_ledger.go:76`、`internal/agent/application/run_budget_ledger.go:94`）。
3. Tool Call 不保存完整 canonical output；只有 Executor 能根据 `result_ref` 精确重载时才可恢复。Git 状态和检索结果会随时间变化，不能在重投递时重新观察。
4. `agent.model_run` 每个 Node Attempt 唯一，适合单节点模型执行，不适合作为整个多阶段 Agent 会话。
5. Answer Draft Session 是短期浏览器投影，不是可恢复候选答案或 Evidence 事实（`migrations/00084_answer_draft_stream.sql:3`）。

## Recommended Persistence Shape

### Workflow-owned checkpoints

直接使用六个 Workflow Node 作为步骤生命周期事实，不新增一套重复的 Step 状态机：

1. `inspect_workspace`
2. `retrieve_evidence`
3. `read_evidence`
4. `synthesize_answer`
5. `validate_citations`
6. `review_publish`

节点 output 只保存版本化 receipt ID/hash 和受限摘要，不保存 Provider transcript。Node/Attempt 仍拥有阶段状态，但它不足以防止 lease 回收后新 Attempt 重复发起已完成的外部观察；因此还需要下述跨 Attempt 的逻辑操作检查点。

### Workspace analysis run

新增 `agent.workspace_analysis_run`，唯一绑定 Workspace、Question、Answer、Workflow Run，保存：

- 冻结模式、Definition/Tool Catalog hash 和请求 hash；
- 节点/模型/工具/Source 读取/Token/总时限/价格快照上限；
- 已预留和已结算计数、deadline、稳定终止原因、version；
- feature/config revision，便于回放和灰度取证。

预算调用前数据库 CAS 预留，调用完成后按实际 Usage 结算。Provider/Tool Unknown 保留最大预留，禁止恢复出额外额度。

### Logical operation checkpoint

新增 `agent.workspace_analysis_operation`，唯一键为 `(analysis_run_id, node_key, operation_kind, ordinal)`，并冻结 request/input hash、状态、first/latest Attempt、reservation、Model/Tool Call、receipt/candidate/result ref/hash 和版本时间。它不复制 Node 的 pending/running/succeeded 生命周期，只回答“这个确定性逻辑调用是否已经授权、执行并持久完成”。

新 Attempt 只能按同一 operation key/hash 归约：`SUCCEEDED` 复用结果，`FAILED` 复用稳定失败，`UNKNOWN`/hash mismatch fail closed；旧 Attempt 的 `STARTED` 必须先核对 exact Call 与原子 receipt/candidate 事实，不能把旧 lease 当作有效，也不能重新观察 Git/Search。

`AuthorizeOperation` 以 `workflow.run -> node_run -> node_attempt -> analysis_run -> operation -> budget reservation -> model/tool call` 的固定锁序，在一个事务中校验 DB-time lease/fence、取消/终态、deadline、hash 和预算，并同时写 reservation 与 `STARTED` Call。完成、Unknown、取消和终态使用同一锁序；Tool 成功还须在同一事务中 CAS Call、插入 receipt、settle reservation 和完成 operation。提交结果不确定时，输出不得进入模型/后继。

### Canonical tool result receipt

新增受限 `workflow.tool_result_receipt`：

- 与一个成功 Tool Call 1:1 绑定，并重复绑定 Workspace/Run/Node/Attempt；
- 保存经 Tool output schema 验证的 canonical output、output hash/bytes，以及可选 server-only binding；
- server-only binding 用于把运行内短引用展开为 Index/Chunk/Source Version/Span tuple，绝不进入模型、时间线或 HTTP；
- 仅新版本合同显式声明 `PERSIST_CANONICAL` 时允许写入，旧 Tool 定义/hash/重放策略保持不变；
- Tool Definition 使用 `result_persistence_policy,omitempty`，历史零值不进入直接 `json.Marshal` 的 canonical bytes；实现前对全部现有合同做 hash fixture；
- Tool Call 成功终结、receipt 插入、预算结算与 operation 完成在同一事务，receipt immutable，超限或 schema 不一致整笔失败。

### Candidate answer

新增不可变 `agent.workspace_analysis_candidate`，绑定 Analysis Run、Answer、逻辑 Synthesis operation、来源 Node Attempt 和 Synthesis Model Run，保存有界候选结果/hash。Citation Tool 与 Faithfulness Review 只消费该候选和前序 receipt。

现有 Answer finalizer 只支持一个终态 Model Run，不能直接承载“正文由 Synthesis 撰写、另一个 Review Model Run 决定能否发布”。Workspace Analysis 需要独立 finalizer：成功 Answer 的 `model_run_id/model_run_ref` 固定为 Synthesis Model Run；发布事务还必须核对独立成功的 Review Model Run/result 与 validation receipt。确定性拒答可不伪造 Model Run；澄清绑定 Planner Model Run；pre-model failure、取消和 Unknown 使用新的稳定 termination result/status，使 Answer 不会永久 pending。旧 RAG finalizer/约束不变。

## Rejected Options

- **一个 Eino ReAct 调用跨完整流程**：Worker 中断后没有 transcript/checkpoint，不能满足恢复。
- **用 Workflow retry 表示 Agent 下一轮**：会混淆失败重试、预算、Attempt 和用户时间线。
- **新增重复 workspace_analysis_step 生命周期表**：静态 DAG 已拥有 Node/Attempt 状态；重复阶段状态机会产生一致性风险。保留窄的 logical operation checkpoint 只是跨 Attempt 的调用幂等/归约事实，不拥有 DAG successor 或用户阶段生命周期。
- **用 Answer Draft Session 当候选事实**：有 TTL、允许 degraded/superseded，不能支撑 Citation 校验后恢复发布。

## Verification Focus

- Node result commit、successor、receipt 和 River Job 的事务边界。
- 模型/工具调用前 lease/fence + operation + budget reservation + Call `STARTED` 的原子授权，完成/Unknown 后同锁序 settlement。
- receipt 已提交但 Node completion 丢失、Worker kill、lease expiry、重复 delivery 下，新 Attempt 复用 operation/receipt 且不重复观察 Git/检索。
- Synthesis/Review 双 Model Run 的成功发布、确定性拒答、取消和 Unknown 均让 Answer 离开 pending，且 response-loss replay 精确一致。
- 旧 Worker、旧 API 和旧固定 RAG Definition 在滚动发布期间保持兼容。
