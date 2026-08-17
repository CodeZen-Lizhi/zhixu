# 受限 Workspace Agent 用户链路

## Goal

让用户在现有 `/chat` 中选择“工作区分析”，提交一次复合分析目标后，由系统在服务端冻结的只读能力和硬预算内完成多步执行，展示可恢复、可审计的时间线，并以经过 Evidence/Citation 校验的答案结束。涉及正式知识或 Git 变更时，首期只给出带证据的变更建议并引导用户查看现有 Proposal 工作台，不直接创建 Proposal、写文件或创建 Commit。

## User Value

- 用户不再需要在 Git 状态、知识检索、Source 阅读和 Citation 校验之间手工切换。
- 用户能看见系统正在做什么、使用了哪些受限能力、为何等待或终止，并在刷新或断线后继续查看同一事实。
- 最终结论有可打开的证据；证据不足时系统明确拒答，不用无证据文本伪装完成。
- 任何变更意图仍停在现有 Proposal/Approval/Safe Writeback 安全边界之外。

## Background And Confirmed Facts

- 路线图将本能力定义为 P1、20–30 人天，并要求独立于固定 RAG 的真实用户入口、至少两次有依赖的只读调用、硬预算和持久恢复（`docs/roadmap.md:38`）。
- 当前 Question 请求只包含问题、检索 Scope、回答深度和格式，没有执行模式；Dispatcher 总是启动固定 RAG v2（`internal/conversation/domain/question.go:75`、`internal/conversation/adapter/postgres/dispatch.go:277`）。
- 当前 Eino `AgentRuntime` 的完整 ReAct transcript 仅存在于单次进程执行中，不能作为跨 Worker 中断的恢复事实（`internal/agent/application/agent_runtime.go:63`、`internal/agent/application/agent_runtime.go:101`）。
- Workflow Run/Node/Attempt 已提供持久 DAG、lease/fence、River 重投递和原子后继节点；Model Run/Call 与 Tool Call 已提供持久调用事实和 Unknown 归约。
- 当前固定 RAG v2 只允许 `ReadSource@2`、`ValidateCitation@2`；冻结 Definition、Graph Hash 和 Tool Definition Hash 不能原地扩大（`internal/conversation/workflow/contract.go:115`）。
- `ReadGitStatus@1` 复用写入审批 Snapshot，只接受 clean attached HEAD，dirty、冲突和 detached 状态 fail closed，四类计数始终为零（`internal/tools/adapter/workspace/git_status.go:27`）。
- `SearchKnowledge@1` 可执行 Workspace 受限检索，但成功结果没有 canonical receipt replay，重投递时会稳定拒绝（`internal/tools/adapter/retrieval/executor.go:117`）。
- `workflow.tool_call` 保存服务端身份、精确工具版本、合同 hash、参数/响应 hash、脱敏摘要、Result Ref 和恢复状态，但不保存可直接重放的完整 canonical output（`migrations/00019_tool_registry_security.sql:3`）。
- 当前 `/chat` 已有 Answer Draft SSE、断线游标恢复、严格 Citation Inspector 和 Answer 发布状态；Proposal 后端创建 API 已存在，但前端通用 Proposal 工作台当前只有列表、详情、审批和 Revision 流程，没有通用预填创建表单。
- Gin、Eino、Tool Registry、Evidence/Citation、持久 Workflow、Proposal/Approval 与 Safe Writeback 均为已批准基线；Eino 或 Provider 类型不得进入领域、持久化、HTTP/OpenAPI 或前端业务合同。

## Key Decisions

1. **独立模式**：在现有 `/chat` 增加 `workspace_analysis` 模式；现有固定 RAG 保持默认，旧客户端省略模式时继续走 `rag`。
2. **有限持久流程**：首期使用独立、静态、版本化的六阶段 Workflow，而不是让一个进程内 Eino ReAct 循环跨 Worker 中断续跑。
3. **独立工具合同**：首期目录只有 Git 状态、受证据约束的检索、Source 读取和 Citation 校验四类能力；使用绑定新 Workflow 的精确新版本，不修改旧工具合同或旧 RAG allowlist。
4. **dirty Git 聚合**：`ReadGitStatus` 支持 clean 与 dirty attached HEAD，只返回 branch/head 及 staged、unstaged、untracked、conflict 数量；不返回文件名、路径、Diff 或正文。detached HEAD、跨 Workspace 和不可可靠解析状态继续 fail closed。
5. **Proposal 边界**：首期只返回带证据的变更建议和进入 `/proposals` 的操作入口；不自动创建、预填、审批或执行 Proposal。
6. **权威恢复**：Workflow Node/Attempt、Model Call、Tool Call、canonical result receipt 和服务端预算事实是唯一恢复来源；Prompt、模型输出、Source 与 Tool Result 均是不可信数据。
7. **跨 Attempt 操作检查点**：Workflow Node/Attempt 继续拥有阶段生命周期；每个实际 Model/Tool 调用另以 `(Analysis Run, node key, operation kind, ordinal, request hash)` 标识逻辑操作。租约回收后的新 Attempt 只能复用或归约该操作，不能重新观察 Git、重新检索或重复消费预算。
8. **服务端选择工具**：首期不提供通用 Agent Tool Invoker。每个静态节点由服务端选择其唯一允许的精确 Tool；模型只生成有界检索计划或候选答案，不能选择工具名称、版本或参数中的授权事实。

## Requirements

### R1. 入口、模式和兼容性

- `/chat` Composer 提供“证据问答 / 工作区分析”分段选择；默认始终为证据问答，工作区分析必须由用户显式选择。
- Question 的模式进入领域校验、请求哈希、幂等回放、持久化和 OpenAPI；省略 mode 与显式 `rag` 必须继续产生部署前相同的 v1 请求 hash，`workspace_analysis` 使用带 mode 的新 hash schema。同一 Idempotency-Key 切换模式必须冲突，不能复用旧执行。
- `rag` 继续启动现有冻结 RAG Definition；`workspace_analysis` 启动独立 `workspace-analysis@1` Definition。两者可独立启用、关闭、灰度和回滚，失败时不得静默切换模式。
- 首期工作区分析只允许本地、已批准知识范围；`allow_web=true` 或其他不支持的 Scope 组合稳定拒绝，不暗中放宽或忽略。

### R2. 有依赖的只读能力闭环

- 成功链路依次完成 Workspace Git 聚合、受证据约束的检索、1–3 个 Source Span 读取、答案合成、Citation 校验和 Faithfulness Review。
- 检索结果对模型只暴露运行内短引用和有界摘要；完整 Index/Chunk/Source Version/Span tuple 由服务端私有 receipt 绑定。
- `ReadSource` 只接受前一检索 receipt 产生的短引用；`ValidateCitation` 只接受当前候选答案中、可由服务端展开的引用。模型不能自造实体 ID、Workspace、Capability、Tool Version、Attempt、lease/fence 或路径。
- 成功验收至少证明 `Search -> ReadSource` 与 `Synthesis -> ValidateCitation` 两组依赖；任一上游 receipt 不存在、漂移或跨 Run 时稳定拒绝。
- 检索最多返回五项且每项模型可见 snippet 不超过 4 KiB；服务端持久选择排序最前的 1–3 项，按序逐项读取且不替换。任一已选 Source 读取失败、漂移或不再合格时整次分析拒答/失败，不基于部分集合继续合成。
- `ReadSource` 的模型可见 excerpt 不超过 4 KiB，并返回 `truncated` 与 `content_hash`；私有 binding 只保存身份 tuple/hash，不保存第二份正文。
- Agent 不获得 Shell、任意文件系统、任意网络、动态工具注册、正文写入或任何可信写工具。

### R3. 持久执行、Receipt 和预算

- 六个逻辑阶段分别使用持久 Workflow Node；正常下一阶段由 DAG successor 产生，不用 retry 伪装 Agent 下一轮。
- Git 与检索等会随时间变化的结果必须在 Tool 成功终结时原子保存 canonical result receipt；响应丢失或 Worker 重投递只加载 receipt，不重新观察并产生不同后继输入。
- 调用前必须在固定锁序内原子校验当前 lease/fence、取消/终态、逻辑操作状态和预算，并同时建立 reservation 与 `STARTED` Model/Tool Call；调用完成时按同一锁序原子终结 Call、结算预算、保存 receipt/candidate 并完成逻辑操作。事务结果不确定时不得把输出交给后继或模型，统一进入可归约的 Unknown。
- 每个 Run 冻结最大节点数、模型调用数、工具调用数、Source 读取数、单工具超时、总时限、并发、输入/输出 Token、上下文/结果字节和可用时的价格快照/费用上限。
- 跨节点预算在调用前以数据库 CAS 预留、调用后结算；未知结果按预留上限消耗，不能因崩溃恢复出额外额度。未配置可信价格时只显示 Token，不伪造金额。
- 超限、取消、Provider/Tool Unknown、合同漂移或恢复不一致都以稳定终止原因结束，且终止后不能产生新调用。

### R4. 答案、证据和 Proposal 建议

- 最终事实性答案必须同时通过完整 Citation tuple 的可打开性/资格校验和现有 Faithfulness Review；校验失败、漂移或证据不足时返回稳定拒答或澄清，不发布未验证 Draft。
- 最终结果使用独立 `workspace_analysis` Answer schema，包含正文、可打开 Citation、安全 Git 聚合、预算摘要、终止原因和可选 Proposal 建议；旧 `rag_answer` schema 不变。
- 成功 Answer 的 `model_run_id` 固定绑定实际撰写候选正文的 Synthesis Model Run；Review Model Run 作为独立必需发布前置事实。拒答、澄清、失败、取消和 Unknown 都必须发布与其来源匹配的稳定 Workspace Analysis 终态，不能让 pending Answer 永久悬挂或伪造 Model Run。
- Proposal 建议只包含用户可读摘要、最终公开且已校验的 Citation ID 和由服务端固定填充的 `/proposals` 入口，不得使用运行内 `E<n>`；不携带可直接执行的 Authorization、Patch、Commit、目标路径或服务端凭据。
- `ApplyApprovedPatch`、`CreateGitCommit` 及其他写工具永远不进入目录；正式写回继续由现有 Proposal、Approval、Version Check、Safe Writeback、Commit、Reindex 和 Regression Validation 拥有。

### R5. 时间线、恢复和用户控制

- 页面明确区分排队/等待、模型生成 Token、工具请求、工具结果、安全摘要、最终答案和稳定终止原因。
- 时间线只展示稳定类型、顺序、Tool 名称/版本、运行内短引用、状态、耗时、预算摘要和脱敏错误；不得展示 Provider tool-call ID、中间思维、Prompt、原始参数、完整结果、完整正文、绝对路径或 Secret。
- 初次加载从服务端持久事实读取完整受限时间线；实时增量复用现有 Server Event SSE，最终正文 Token 复用 Answer Draft SSE。刷新、断线和页面离开后按游标恢复。
- 时间线 snapshot 是唯一权威读模型；Server Event 只负责幂等失效通知。Git、Search、ReadSource 和 Citation 校验均由服务端生成固定安全投影，终态事实与终态事件必须同事务提交或可由确定性 source-event ref 补齐。
- 运行中提供停止操作并复用 Workflow cancel 安全检查点；取消不删除已发生事实，也不把未确认结果标成成功。

### R6. 安全、运维和发布

- API、Worker、Registry、Definition、OpenAPI 和 Web 必须作为同一合同版本发布；新工具版本未完整装配时工作区分析 readiness fail closed，固定 RAG 不受影响。
- Tool Definition 新增的结果持久化字段必须使用不改变旧 canonical JSON 的零值/`omitempty` 兼容形式；实现前冻结并回归全部现存 Tool Definition hash。
- 日志、Metric、Trace、Audit、Receipt、HTTP Problem、SSE 和浏览器状态不得泄露 Secret、Cookie、完整 Prompt/正文、绝对路径、DSN、高敏 Tool 参数或私有 receipt binding。
- 新能力默认关闭；先完成前向兼容迁移和双端装配，再按 Workspace 灰度开启。回滚时先禁止新 Run、排空或稳定终止存量 Run，再回滚 Worker；已有工作区分析事实的 Workspace 必须继续由能读取新 Question/Answer 联合的 API 服务，不能仅因 Run 已排空就切回旧 API。不破坏已保存事实。

## Acceptance Criteria

- [x] **AC1**：用户在 `/chat` 显式选择工作区分析并提交复合目标；旧客户端或默认模式继续产生与当前相同的固定 RAG 行为。
- [x] **AC2**：真实成功 fixture 完成 Git 状态、受限检索、至少一次 Source 读取、Citation 校验和最终发布，且数据库证据可证明至少两次调用使用了前序持久 receipt。
- [x] **AC3**：clean 与 dirty attached HEAD 返回正确 branch/head 和四类聚合计数；响应、日志、事件和审计均不含文件名、路径、Diff 或正文，detached/cross-workspace/不可可靠状态 fail closed。
- [x] **AC4**：目录严格只有四类批准能力；未登记工具、写工具、旧版本、动态工具、伪造 Workspace/Capability/实体 ID 或超限输入全部稳定拒绝且不触发 Executor。
- [x] **AC5**：Git/检索成功后发生响应丢失、Worker 重投递或 lease 回收时，新 Attempt 通过同一逻辑操作检查点恢复相同 canonical receipt 和后继引用；不会复用旧租约、重新检索、重新观察 Git、重复扣预算或生成分叉时间线。
- [x] **AC6**：调用授权、预算预留和 Call `STARTED` 原子发生；若继续授权会超过节点、模型、工具、Token、结果大小、总时限、并发或已配置费用上限，Run 必须以对应稳定原因终止且不产生额外调用，Unknown 不能伪装成功。冻结 v1 的 canonical operation 前缀已证明不会触发预算超额，数据库拒绝任何非因果 `BUDGET_EXHAUSTED` proof。
- [x] **AC7**：最终事实只引用当前 Workspace 内通过可打开性、正式知识资格和 Faithfulness 校验的 Evidence；成功 Answer 绑定 Synthesis Model Run 并要求独立 Review Model Run，通过不了则发布对应拒答/澄清/失败/取消/Unknown 终态，未验证 Draft 不成为 Answer 事实。
- [x] **AC8**：需要变更时只出现带证据的建议和 `/proposals` 入口；没有 Proposal create 请求、文件写入、Git Commit、Approval 或 Write Authorization 副作用。
- [x] **AC9**：时间线区分 Token、工具请求/结果、等待、完成/拒绝/失败/取消；刷新、SSE 断连和重新进入页面后顺序、状态和摘要与服务端一致。
- [x] **AC10**：工作区分析可独立关闭和回滚；现有 RAG v1/v2 Definition/Graph Hash、Tool Hash、API、历史回放、Citation 和浏览器回归测试保持不变。
- [x] **AC11**：OpenAPI、Go 单元/真实 PostgreSQL/River、race/fault、前端 reducer/component、SSE 恢复、权限/安全和桌面/移动浏览器链路均有直接证据，并通过 Go、SQL、前端和跨层 Review。

## Out Of Scope

- 任意 Shell、任意文件读写、任意网络访问、受控网页浏览或动态安装工具。
- 开放旧 `SearchKnowledge@1` 的自由查询合同，或把 `CalculateDiff`、`ReadDocument` 等正文能力加入首期目录。
- 自适应无界 ReAct 循环、动态 DAG、多 Agent 协作或持久化 Eino/Provider transcript。
- 由 Agent 自动创建/预填/审批/执行 Proposal，调用 Safe Writeback，创建 Commit、Push 或修改索引。
- 通用 Proposal 创建表单、全面改造聊天信息架构或自动合成知识笔记。
- 用本任务重写现有固定 RAG、Workflow Runtime、全局 Tool Registry 或 Proposal 页面。

## Risks And Deferred Items

- 当前通用 Tool Call 只有 hash/摘要/ref；本任务需要新增受限 canonical result receipt，但必须避免把它变成可任意持久化正文的旁路。
- 单进程 `RunBudgetLedger` 不能跨六个节点复用；需要持久预算 reservation/settlement，而不能只在恢复时汇总计数。
- 当前没有可信模型价格配置；首期始终执行 Token/调用硬预算，只有存在版本化价格快照时才显示和执行金额预算。
- 首期在 Search receipt 中一次性冻结排序最前的 1–3 个 Source Span，按序全部成功后才合成；不做替补、部分证据降级、多源自适应选择、二次检索或追问循环。
- `/proposals` 入口只用于查看现有流程，不承诺把 Agent 文本预填为新 Proposal；该闭环属于后续范围。
