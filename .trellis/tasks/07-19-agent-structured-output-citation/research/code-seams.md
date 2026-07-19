# M6-02 代码 Seam 与影响面研究

## 1. 当前可复用事实

- 正式主模块未采用 Eino；`poc/eino/report.md` 已冻结直接 OpenAI-Compatible Adapter 路线。
- `internal/platform/models` 当前只有 OpenAI-Compatible/Ollama Embedding Adapter，没有 ChatModel、Chat 配置或 Factory。
- Retrieval 已提供有界 Search 和不可变 Source Version/Source Span 打开能力；Agent 不应复制 FTS、向量、RRF、Rerank 或文件读取。
- Knowledge 已提供 Relation Assessment 映射、Confirmed Claim/Relation/Conflict 批量查询和正式写命令；M6-02 不得直接写 Knowledge 表。
- Workflow Node Attempt 是当前真实执行 attempt 身份，适合作为一次 Agent Model Run 的稳定父身份。

## 2. 必须补齐的 Seam

### ChatModel

在 `internal/agent/application` 定义项目自有 `ChatModel`、`ChatRequest`、`ChatResponse`、usage 和稳定错误；
正式实现放在 `internal/platform/models`，沿用现有 Embedding HTTP Adapter 的 TLS、redirect、响应大小、
timeout/cancel、错误分类和 Secret 不泄漏模式。Ollama 可通过其 OpenAI-Compatible endpoint 接入，本期不再维护
第二套 native chat 协议。

### Evidence Eligibility

Search 的 Active Index 只证明可检索，不证明 Approved。扩展 Knowledge Repository/Application：按最多 500 个
`ProvenanceRef` 单批返回资格与正式 Claim/Relation/Conflict 绑定。Confirmed Claim/Relation 可发布；Disputed Claim
可进入上下文但必须标记 Conflict；Suggested/Rejected/历史无正式绑定默认不合格。Agent Adapter 只调用该 seam，
不直接查 `core.claim_source` 或 `core.relation_evidence`。

### Model Run

新增 `agent.model_run` 与 `agent.model_call`：Model Run 绑定 Workspace、Workflow Run、Node Run、Node Attempt、
Adapter/Model、Prompt、Schema、Retrieval 版本和最终状态；Model Call 记录 INITIAL/REPAIR/REDUCED/REVIEW 每次调用的
request/response hash、Token、耗时、错误和状态，不保存完整 Prompt、Evidence 或原始响应。Knowledge 的
`model_run_ref` 使用稳定 Model Run ID。一个 Node Attempt 只允许一个 Model Run，Call No 在 Run 内唯一。

该专用事实源优于把多次调用压入单个 `node_run` 字段；Node/Timeline 可通过 FK 查询 Model Run，M10 再建立成本和
审计投影。Migration 必须 guarded Down，已有 Agent 数据禁止破坏性降级。

## 3. 建议模块

```text
internal/agent/domain/                # 任务 Schema、Citation、Assessment、Answer、Refusal、Model Run
internal/agent/application/           # Prompt/Schema Catalog、Structured Runner、Relation/RAG/Review 编排
internal/agent/adapter/postgres/      # Model Run/Call Repository
internal/agent/adapter/retrieval/     # Retrieval Search/Evidence seam 适配
internal/agent/adapter/knowledge/     # Knowledge eligibility/read/write seam 适配
internal/platform/models/             # OpenAI-Compatible Chat Adapter + Factory
eval/agent/                           # 版本化 fixture、metrics、offline runner
migrations/00018_agent_runtime.sql    # agent schema 与持久化约束
```

依赖方向：`agent/domain <- agent/application <- agent/adapter|platform/models|composition root`。Domain 不导入
Retrieval、Knowledge、Workflow、pgx、HTTP 或 Provider 类型。

## 4. 关键实现决策

- Strict Decoder 拒绝非法 UTF-8、重复 key、unknown field、多个 JSON 值、错误类型/枚举和越界字段。
- Structured Runner 总模型响应数最多 3：INITIAL、REPAIR、REDUCED；不做 regex/markdown fence 提取。
- HTTP Adapter 不自动重试；Workflow 只重试明确 429/5xx/deadline 等 provider transient failure，Schema/业务拒绝不重试同一响应。
- Citation 依次校验 identity、openability、eligibility、semantic support；前 3 层确定性 fail closed，semantic support 由任务专用 Review Schema + 离线/真实评测共同约束。
- Review Model 未配置时显式使用已冻结的 default model profile；配置快照写入 Model Run，不做运行中静默 fallback。
- Tool Request 只作为结构化输出，本任务不执行任何工具。

## 5. 兼容性与风险

- Knowledge Repository 增加批量 eligibility 查询是公共 seam 扩展，所有 fake 与 PostgreSQL 实现必须同步。
- 新 Migration 只 Expand，不改 `00001`–`00017`；旧 API/Worker 在未注册 Agent Node 时继续运行。
- Chat Config 采用 `provider=disabled|openai-compatible`；disabled 返回 nil capability，AI Node 明确不可用。
- Model Run 写入必须在调用前创建，调用完成后 CAS 结束；进程崩溃留下 RUNNING/UNKNOWN 可被恢复查询发现，不能伪成功。
- 当前没有 Article/Document Approval read model，Approved Eligibility 暂以正式 Knowledge Evidence 为安全最小集；后续可扩展 Port，不改变 Agent 契约。

## 6. 建议验证

- Domain/Application：strict JSON、三阶段 repair、citation/refusal/conflict、五分类、版本冻结、调用预算。
- Chat Adapter Contract：成功、429/5xx/4xx、timeout/cancel、redirect、超大/非法响应、模型回显、Secret canary。
- PostgreSQL：Model Run/Call 状态机、重放、crash/unknown、Workspace/Node Attempt FK、guarded Down。
- Knowledge Eligibility：500 Provenance 单批、Confirmed/Disputed/Suggested、跨 Workspace、无 N+1。
- Eval：固定 fixture 输出 Citation Precision/Coverage、Faithfulness、Conflict Disclosure、Refusal、五分类矩阵。
