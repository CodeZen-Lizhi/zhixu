# Research: Phase 0 public contracts

- Query: 盘点 Workspace Analysis Answer 终态/result schema 与 ModelRun 可空性、Analysis Timeline DTO/安全摘要、稳定 Problem/termination/readiness code 的现有 owner、最小冻结文件与兼容陷阱；不得启用 runtime 或修改 persistence。
- Scope: internal
- Date: 2026-08-15

## Findings

### 1. 结论

Phase 0 应只新增“可独立严格解码、规范序列化并由 golden fixture 锁定”的 Conversation 公共合同，不接入现有 Answer validator/finalizer、HTTP router、OpenAPI、前端 decoder、API/Worker composition 或数据库约束。当前最小且边界清晰的 owner 是：

1. `internal/conversation/domain` 拥有 Workspace Analysis 的 result documents、终态矩阵、ModelRun author provenance 规则、timeline 安全投影值类型和稳定业务 code。
2. `internal/conversation/http` 在 Phase 4 才拥有 Answer/timeline 的精确 wire wrapper 与 Problem 映射；Phase 0 可以只用 domain canonical JSON fixture 冻结未来 wire，不注册 route。
3. `internal/httpapi` 继续只拥有通用 Problem envelope，不应知道 Workspace Analysis code。
4. `internal/workflow/runtime` 继续拥有 Worker 进程就绪快照；Workspace Analysis 的内部 policy/readiness 错误与用户可见 capability Problem 必须分开。

当前代码只部分冻结了 `QuestionMode`、未注册的 `workspace-analysis@1` Definition 和预算/deadline readiness policy；Answer 三个新 result type、`failed/cancelled`、nullable ModelRun、timeline DTO 和公开 code 均尚无 owner。Phase 0 若直接改现有 `Answer.Validate`、finalizer、migration 或 OpenAPI，会越过任务定义的 rollback point，并形成无法被旧数据库/前端读取的半发布状态。

### 2. Files found

| Path | Description |
| --- | --- |
| `.trellis/tasks/08-15-workspace-agent-user-flow/design.md` | 本任务 Answer 矩阵、timeline 安全投影、failure code families 与 rollout 行为的设计事实源。 |
| `.trellis/tasks/08-15-workspace-agent-user-flow/implement.md` | Phase 0 明确只冻结合同/fixture；schema/domain 集成从 Phase 1 开始，HTTP/OpenAPI 从 Phase 4 开始。 |
| `internal/conversation/domain/question.go` | 已声明 `rag/workspace_analysis` 枚举与空值归一，但 `QuestionRequest` 尚未携带 mode。 |
| `internal/conversation/workflow/workspace_analysis_contract.go` | 已声明默认关闭、未注册的六节点 Definition 和精确 Tool refs；它只应拥有 DAG 合同。 |
| `internal/agent/application/workspace_analysis_policy.go` | 已冻结预算/deadline 与内部 policy/readiness error；不是公共 Problem/Answer code owner。 |
| `internal/conversation/domain/answer.go` | 当前 Answer 状态、result type、canonical projection 和 ModelRun binding 的唯一领域 owner。 |
| `internal/conversation/domain/answer_test.go` | 当前 RAG/refusal/clarification canonical bytes/hash 和所有终态 ModelRun 必填规则的回归 owner。 |
| `internal/agent/domain/output.go` | 现有 RAG/refusal result schema、strict decoder、bounded payload 和 required ModelRunRef 的 owner。 |
| `internal/conversation/domain/clarification.go` | 现有 `conversation.clarification@v1` strict schema；Workspace Analysis clarification 应复用它。 |
| `internal/conversation/application/finalizer.go` | 当前单 ModelRun finalizer command；不支持 nullable/publication author provenance。 |
| `internal/conversation/adapter/postgres/finalizer.go` | 当前 RAG 专用原子发布实现，始终加载/写入/解引用一个 ModelRun。 |
| `internal/conversation/adapter/postgres/scans.go` | SQL nullable ModelRun 扫描后仍调用旧 Answer validator/projection。 |
| `migrations/00020_rag_conversation_sse.sql` | 当前 Answer DB publication bundle/trigger，只接受四状态、三 result type 和非空终态 ModelRun。 |
| `internal/conversation/http/handler.go` | Conversation route 与 exact response wrapper owner；当前无 analysis-timeline route。 |
| `api/openapi/openapi.json` | 当前公开 wire 唯一事实源；只描述 RAG/refusal/clarification 且 ModelRunRef 必填。 |
| `web/src/api/conversation.ts` | 前端 Conversation raw JSON 唯一 strict decoder；当前 exhaustive union 只有四状态/三 result type。 |
| `internal/knowledge/domain/timeline.go` | 可借鉴的 bounded timeline domain/value validation 模式，但不是 Analysis Timeline owner。 |
| `internal/knowledge/application/timeline.go` | 可借鉴的 read port/query result/cursor 分层模式。 |
| `internal/knowledge/http/handler.go` | 可借鉴的 domain validate -> exact wire DTO -> map 模式；不能复用 Knowledge event DTO。 |
| `internal/httpapi/response.go` | 通用 Problem envelope 与 foundation error kind -> HTTP status 映射 owner。 |
| `internal/app/router.go` | API `/readyz` 与 `/system/status` 的 capability/readiness composition owner。 |
| `internal/workflow/runtime/readiness.go` | Worker readiness v1 snapshot、优先级和 stable code owner。 |
| `internal/workflow/httphealth/handler.go` | Worker public readiness code allowlist；未知内部 code 会折叠成 generic unavailable。 |

### 3. Design facts that are already frozen

#### 3.1 Answer terminal matrix

设计已冻结以下语义（`design.md:83-112`）：

| Publication status | Result type | Public result author ModelRun | Required proof |
| --- | --- | --- | --- |
| `completed` | `workspace_analysis` | 必须是 Synthesis ModelRun | immutable candidate + all-valid Citation receipt + 独立 succeeded Review ModelRun |
| `refused` | `workspace_analysis_refusal` | 模型撰写时为精确 authoring ModelRun；确定性 server refusal 为 null | stable reason + bounded summary；事实性内容仍受 Citation 约束 |
| `clarification_required` | 现有 `clarification` | 必须是 Planner ModelRun | 复用现有 clarification schema |
| `failed` | `workspace_analysis_termination` | optional | stable failure/Unknown reason + sanitized summary |
| `cancelled` | `workspace_analysis_termination` | optional | cancellation fact + sanitized summary |

补充不变量：

- `WORKSPACE_ANALYSIS_RESULT_UNKNOWN` 只能是 `failed` reason，不能伪装成 success/refusal（`design.md:108,142`）。
- cancellation 使用 `WORKSPACE_ANALYSIS_CANCELLED`（`design.md:143`）。
- 每个 accepted Workspace Analysis Question 从 `pending` 精确退出一次（`design.md:108`）。
- `completed` 的 Answer ModelRun 与 result `model_run_ref` 都指向 Synthesis；Review 是另一条独立事实，不能覆盖 author provenance（`design.md:110`）。
- pre-model deterministic failure/refusal 不得制造 ModelRun ID（`design.md:108`）。

`ModelRunID` 的语义应冻结为“公共 result 的准确作者/来源”，不是“此 Run 曾执行过的最后一个 ModelRun”。因此 failed/cancelled 即使先前存在 Planner/Synthesis Call，也只有在某一受信任 ModelRun 直接产出最终公开 sanitized document 时才绑定它；纯 server terminalizer 必须为 null。Review ModelRun 永远不能成为 Answer/result author ref。

#### 3.2 Completed result schema

设计已给出 completed 的精确顶层 shape（`design.md:85-98`）：

```json
{
  "result_type": "workspace_analysis",
  "schema_id": "conversation.workspace_analysis_answer",
  "schema_version": "v1",
  "model_run_ref": "<synthesis-model-run-uuid>",
  "payload": {
    "answer_markdown": "...",
    "citations": [],
    "git_status": {
      "branch": "...",
      "head": "...",
      "clean": true,
      "staged_count": 0,
      "unstaged_count": 0,
      "untracked_count": 0,
      "conflict_count": 0
    },
    "budget": {
      "model_calls": 0,
      "tool_calls": 0,
      "input_tokens": 0,
      "output_tokens": 0,
      "estimated_cost": null
    },
    "proposal_suggestion": null,
    "termination_reason": "COMPLETED"
  }
}
```

`proposal_suggestion.href` 固定为 `/proposals`；它只表示建议，不表示 Proposal 已创建。`estimated_cost` 在没有可信 price snapshot 时为 unavailable（`design.md:260`）。为避免 strict decoder 漂移，Phase 0 必须决定它与 `proposal_suggestion` 是 required-nullable 还是 optional；建议使用 required-nullable，确保“无值”与“旧 schema 不认识字段”可区分，Go 字段不得加 `omitempty`。

#### 3.3 Refusal and termination envelopes

设计只冻结了 result type、ModelRun policy 和语义，尚未冻结两个 schema ID、payload 字段、字段上限及 null/omitted 规则。最小建议是保持与所有现有 Answer document 相同的五字段 envelope：

```text
result_type, schema_id, schema_version, model_run_ref, payload
```

并将 `model_run_ref` 对 refusal/termination 定义为 required-nullable：JSON 必须出现该 key，值为 canonical UUID 或显式 `null`；缺失必须 strict-decode 失败。建议 schema IDs 为：

- `conversation.workspace_analysis_refusal@v1`
- `conversation.workspace_analysis_termination@v1`

建议最小 payload 只包含 `reason_code`、`summary`、`termination_reason`，必要的 validated Citation identity 才作为显式 bounded array 加入 refusal。不要复用 `agent.refusal@v1`：现有 schema 的 `model_run_ref` 必须为 UUID，且 payload 有 RAG 专用 `retrieval_scope/missing_requirements/suggested_actions`。

这些 schema ID 和 payload keys 是建议而不是已批准事实；见 Caveats。

### 4. Existing Answer ownership and required compatibility

#### 4.1 Current code pattern

- `internal/conversation/domain/answer.go:14-31` 定义 `AnswerResultType` 与 `PublishedResult`；后者的 `ModelRunID` 是非指针。
- `internal/conversation/domain/answer.go:41-58` 的持久 Answer 已使用 `*foundation.ID`，所以 storage model 本身能表达 null。
- `internal/conversation/domain/answer.go:77-87` 要求 pending 无 ModelRun/result，而所有现有 published Answer 都必须有 ModelRun、version 2、result、RAG RetrievalSummary 和 PublishedAt。
- `internal/conversation/domain/answer.go:96-104` 使用 status -> 唯一 result type，并要求 canonical result ModelRun 等于 Answer ModelRun。
- `internal/conversation/domain/answer.go:124-172` 的 projection switch 只支持 RAG/refusal/clarification，局部变量也是非指针 ModelRun。
- `internal/conversation/domain/answer.go:175-209` 只有四个 publication status，并假设 status 与 result type 一一对应。
- `internal/agent/domain/output.go:780-786,1010-1025` 的现有 RAG/refusal result 都要求非空 `foundation.ID` ModelRunRef。
- `internal/conversation/domain/clarification.go:57-75` 的 clarification 同样要求 Planner ModelRunRef。

#### 4.2 Phase 0 ownership recommendation

不要在 Phase 0 改写上面的现有行为。新增一个 Conversation-domain-only contract，独立暴露：

- 三个 result type 常量：`workspace_analysis`、`workspace_analysis_refusal`、`workspace_analysis_termination`。
- 两个追加 publication status 常量：`failed`、`cancelled`。
- `WorkspaceAnalysisModelRunPolicy`（required / required-nullable）和五行 terminal contract table。
- 三个 schema envelope/value types；completed 用 `foundation.ID`，refusal/termination 用 `*foundation.ID` 且 JSON 不使用 `omitempty`。
- strict decoder、validator、canonical encoder/hash；返回 `*foundation.ID` author ref，不迫使现有 `PublishedResult.ModelRunID` 变成 pointer。
- stable reason/code enum 与分类 table。

Phase 1/3 再把它接入 `Answer.Validate`、projection、finalizer 和数据库。这样 Phase 0 编译时只增加可测试的 contract surface，不让现有 RAG runtime 接受数据库尚不能保存的新终态。

### 5. Analysis Timeline DTO and safe summaries

#### 5.1 Correct owner and pattern

Analysis Timeline 是 Answer-scoped 的 Workspace Analysis 运行投影，不是现有 Knowledge Timeline：

- Knowledge Timeline domain 以受控 enum、bounded query/page 和 validation 拥有值语义（`internal/knowledge/domain/timeline.go:19-40,42-162`）。
- Application 定义窄 `TimelineReader` port 与 query result（`internal/knowledge/application/timeline.go:29-60`）。
- HTTP 使用精确 response structs，并在映射前重新 validate domain（`internal/knowledge/http/handler.go:443-588`）。

应沿用分层方式，但新类型放在 Conversation namespace；不能复用 `KnowledgeEvent`、它的自由文本 `Summary/Payload` 或其分页语义。Analysis Timeline 是单个 Answer 的 bounded ordered snapshot，不是跨 owner event feed。

#### 5.2 Minimum DTO surface to freeze

建议 Phase 0 新增以下值类型和 enum，且所有 slice/字符串/计数有硬上限：

```text
AnalysisTimeline
  schema_id / schema_version
  answer_id / workspace_id / analysis_run_id
  run_status
  termination_reason (required-nullable)
  items[]
  budget
  latest_server_event_sequence

AnalysisTimelineItem
  sequence
  kind
  phase
  status
  tool_ref (required-nullable; exact name/version)
  summary (strict discriminated union)
  duration_millis (required-nullable)
  error_code (required-nullable)

AnalysisBudget
  model_calls {used,max}
  tool_calls {used,max}
  source_reads {used,max}
  input_tokens {used,max}
  output_tokens {used,max}
  estimated_cost_microunits {used,max} or required-nullable unavailable state
```

安全摘要不能是 `map[string]any`、`json.RawMessage` 或通用 string。应是 exactly-one 判别联合：

| Summary kind | Allowed fields | Explicitly forbidden |
| --- | --- | --- |
| Git | attached branch, HEAD, clean, staged/unstaged/untracked/conflict counts | repo path, porcelain lines, diff/body |
| Search | hit count, stable degradation codes | query prompt, snippet/body, private Citation tuple |
| ReadSource | `E<n>`, content hash, truncated | source path, excerpt/body, byte range/private binding |
| Citation validation | valid/invalid counts, stable reason codes | full Citation tuple, candidate body |
| Model phase | phase, status, token usage | Prompt, response/candidate text, chain of thought, provider request/response/id |

设计明确 snapshot 权威、SSE 只做 invalidation（`design.md:270-297`）。因此 timeline DTO 不应包含 SSE event payload，也不能由前端累积 event 重建。`latest_server_event_sequence` 只用于恢复游标对齐。

#### 5.3 Validation rules to freeze

- `items` 以 `sequence` 升序、唯一且连续；明确从 1 开始。
- `kind/phase/status/tool_ref/summary` 必须形成合法组合；Model item 不能带 tool ref，Tool item 必须带 exact versioned ref。
- running/waiting 项 `duration_millis=null`，terminal item 为非负整数；缺 key 与 null 不是同义。
- `error_code` 只允许 stable enum，永不携带 error message。
- budget `0 <= used <= max`，max 与 v1 policy 一致；Unknown 已预占的用量按设计 settlement 规则反映，不能显示负数或超过上限。
- `latest_server_event_sequence >= 0`，并冻结 0 表示“尚无 event”；不要 optional。
- branch、hash、short ref、degradation/reason code、items 数量和总 encoded bytes 都有常量上限。
- canonical JSON/hash fixture 锁住 exact keys、null presence、array order 和 numeric unit。

设计尚未给出 item 最大数量、完整 enum、schema ID、duration/null 细节；Phase 0 必须先明确这些值，不能由 Phase 4 handler 临时决定。

### 6. Stable Problem, terminal and readiness codes

#### 6.1 Existing pattern

- 通用 Problem 为 `{error_code,message,retryable,workflow_run_id?,details?}`（`internal/httpapi/response.go:17-24`）；HTTP status 由 foundation error kind 映射（`internal/httpapi/response.go:26-41`）。
- Conversation HTTP 透传 classified code，但用固定安全 message 且不给 details（`internal/conversation/http/handler.go:576-595`）。
- API `/readyz` 在 RAG enabled 且 composition 失败时返回 `DEPENDENCY_UNAVAILABLE` + stable `rag_dependencies_unavailable`，不泄露 private error（`internal/app/router.go:141-172`; `internal/app/router_test.go:415-443`）。
- Artifact 的 capability-unavailable precedent 是 HTTP 503、`retryable:false`，并把底层 retryable dependency failure 收敛为公共 capability code（`internal/artifact/http/handler_test.go:292-320`）。
- Worker readiness 有 versioned snapshot 与 deterministic precedence（`internal/workflow/runtime/readiness.go:6-23,25-59,150-180`）；HTTP health 只公开 allowlist，未知 code 折叠为 generic unavailable（`internal/workflow/httphealth/handler.go:54-91`）。
- 当前 Workspace Analysis policy 的 `AGENT_WORKSPACE_ANALYSIS_POLICY_INVALID` 与 `AGENT_WORKSPACE_ANALYSIS_RUNTIME_NOT_READY`（`internal/agent/application/workspace_analysis_policy.go:45-50`）是内部 application/composition code，不是用户终态或 Problem code。

#### 6.2 Code taxonomy to freeze

不要用一个 HTTP Problem 表示 accepted Question 之后的业务终态：

- 接受前 invalid mode/scope/capability unavailable：HTTP Problem，Question/Answer/Workflow 未启动。
- 接受后 evidence/citation/budget/receipt/Unknown/cancel：Answer 的 refusal/termination reason；查询 Answer/timeline 仍是 HTTP 200。
- Worker readiness：进程 health code，仅表达能否承接 enabled capability，不成为 Answer reason。
- 内部 policy/runtime error：日志/composition 诊断 code，可映射到一个 public capability code，但不可直接泄漏。

设计明确冻结的 exact strings 只有：

- `WORKSPACE_ANALYSIS_RESULT_UNKNOWN`
- `WORKSPACE_ANALYSIS_CANCELLED`

其余只冻结了 family（`design.md:301-308`）。建议 Phase 0 审批以下 exact candidates，并用单一 enum/table 固定 category、HTTP status/retryable 或 terminal status/result type：

| Candidate code | Category | Proposed public outcome |
| --- | --- | --- |
| `WORKSPACE_ANALYSIS_MODE_INVALID` | Problem | 400, non-retryable, not accepted |
| `WORKSPACE_ANALYSIS_SCOPE_UNSUPPORTED` | Problem | 400, non-retryable, not accepted |
| `WORKSPACE_ANALYSIS_CAPABILITY_UNAVAILABLE` | Problem | 503, non-retryable, not accepted |
| `WORKSPACE_ANALYSIS_BUDGET_EXHAUSTED` | termination | `failed/workspace_analysis_termination` |
| `WORKSPACE_ANALYSIS_RECEIPT_INVALID` | termination | normally `failed`; receipt drift/commit ambiguity must converge to RESULT_UNKNOWN where required |
| `WORKSPACE_ANALYSIS_EVIDENCE_INSUFFICIENT` | refusal | `refused/workspace_analysis_refusal` |
| `WORKSPACE_ANALYSIS_CITATION_INVALID` | refusal or termination | must choose one deterministic rule before freeze |
| `WORKSPACE_ANALYSIS_RESULT_UNKNOWN` | termination | `failed/workspace_analysis_termination` |
| `WORKSPACE_ANALYSIS_CANCELLED` | termination | `cancelled/workspace_analysis_termination` |

`CITATION_INVALID` 和 `RECEIPT_INVALID` 的矩阵在设计中仍允许“refusal or stable failure”（`design.md:137-143`），因此不能只冻结字符串而不冻结分类规则。

#### 6.3 Rollout readiness truth table

Phase 0 应用 pure table test 冻结以下行为，不接入 root composition：

| API flag | API contract/runtime ready | Worker capability ready | Workspace Analysis submission | RAG/readiness effect |
| --- | --- | --- | --- | --- |
| off | any | any | stable capability-unavailable | API/Worker 全局 readiness 不因 WA 失败 |
| on | false | any | stable capability-unavailable | fail closed for WA only |
| on | true | false/unknown | stable capability-unavailable | fail closed for WA only |
| on | true | true | allowed to dispatch WA | RAG behavior unchanged |

API 与 Worker wiring 独立、默认关闭，只有两侧同版 Definition/Tool contracts/runtime policy ready 才接受提交（`design.md:338-342`）。不要把 WA 的 disabled 状态塞进全局 `DependenciesOK=false`，否则会错误拉掉现有 RAG。后续新增 Worker readiness code 时必须同步 `publicReadinessCode` allowlist/test，否则会静默显示 generic `WORKER_READINESS_UNAVAILABLE`。

### 7. Minimum Phase 0 files and tests

最小、可审查且不启用行为的文件集合：

| File | Minimum contents |
| --- | --- |
| `internal/conversation/domain/workspace_analysis_contract.go` | result/status/schema/reason constants；五行 terminal/ModelRun policy table；Problem/termination taxonomy；纯 rollout truth table。 |
| `internal/conversation/domain/workspace_analysis_result.go` | 三个 exact envelopes/payloads、limits、strict decode、validate、canonical bytes/hash；nullable ref 不使用 `omitempty`。 |
| `internal/conversation/domain/analysis_timeline.go` | timeline snapshot/item/budget enums、exactly-one typed safe summary、bounds/validation/canonical encoding。 |
| `internal/conversation/domain/workspace_analysis_contract_test.go` | terminal matrix、exact code string/classification、ModelRun policy、readiness truth table、legacy type/status unchanged。 |
| `internal/conversation/domain/workspace_analysis_result_test.go` | 每种 terminal path golden bytes/hash；required-null vs missing；strict unknown/duplicate/type/UTF-8/bounds；citation/proposal binding。 |
| `internal/conversation/domain/analysis_timeline_test.go` | golden snapshot/hash；order/kind/phase/status/ref/duration/error/budget/event sequence；leak canaries 与 exactly-one summary。 |

若希望 fixture 可被 Phase 4 Go/OpenAPI/Web tests 共用，再新增 `internal/conversation/domain/testdata/workspace_analysis_public_contract_v1/`，至少包含：completed、server refusal null ModelRun、model-authored refusal、clarification、failed null ModelRun、failed with author ModelRun、cancelled null ModelRun、timeline mixed/running/terminal 三类。否则可以把 canonical JSON literals 放在上述 tests 内保持最小文件数。

Phase 0 test 必须证明：

1. Existing `rag_answer@v2`、`agent.refusal@v1`、`conversation.clarification@v1` bytes/hash/required ModelRun 完全不变。
2. `model_run_ref:null` 可通过 refusal/termination decoder，而字段缺失、空字符串、invalid UUID 均失败。
3. completed/clarification 的 null ModelRun 失败。
4. public result author ref 与 terminal policy 一致；Review ModelRun 没有可写入 result envelope 的位置。
5. server summary 不接受 path、snippet/excerpt、Prompt/provider payload/private binding 等额外 key；不要只做关键词 redaction 后继续发布。
6. code table 没有重复字符串、没有未分类 code，每个 terminal code 唯一映射到 status/result type。
7. disabled/mismatched readiness 不影响 RAG，并始终返回同一 capability-unavailable family。

建议 Phase 0 局部验证：

```bash
go test ./internal/conversation/domain
go test ./internal/conversation/workflow ./internal/agent/application
git diff --check
```

### 8. Files explicitly deferred beyond Phase 0

以下 owner 必须等对应阶段一次性同步，Phase 0 不应修改：

- `internal/conversation/domain/answer.go` 和 `answer_test.go` 的真实 terminal integration（Phase 1/3）。
- `internal/conversation/application/finalizer.go`、`internal/conversation/adapter/postgres/finalizer.go`、`scans.go`（Phase 3 dedicated finalizer）。
- `migrations/*` publication status/result/model_ref constraints 与 forward migration（Phase 1）。
- `internal/conversation/http/handler.go` route/wire DTO、`api/openapi/openapi.json`、OpenAPI route parity tests（Phase 4）。
- `web/src/api/conversation.ts` 与前端 strict decoder/tests（Phase 5，必须基于已提交 OpenAPI/fixture）。
- `internal/app/router.go`、`internal/workflow/runtime/readiness.go`、`internal/workflow/httphealth/handler.go`、`cmd/api`、`cmd/worker` 的真实 composition（Phase 3/4/6 rollout）。

### 9. Compatibility traps

1. **Status -> result type is no longer globally one-to-one.** `completed` 既可能是 legacy `rag_answer` 也可能是 `workspace_analysis`；`refused` 同理。当前 `ResultTypeForPublicationStatus`（`answer.go:198-209`）不能继续单独决定类型，必须同时看 immutable Question mode。
2. **Current published validation requires a ModelRun and RAG RetrievalSummary.** `answer.go:84-105` 会拒绝 deterministic refusal/failure/cancel；不能为了过测制造 fake UUID 或 fake RAG summary。
3. **Do not widen legacy refusal.** 修改 `agent.RefusalResult.ModelRunRef` 为 pointer 会改变现有 canonical JSON/decoder/OpenAPI/前端语义。Workspace Analysis 必须使用独立 result schema。
4. **SQL is stricter than Go storage shape.** `agent.answer.model_run_id` column nullable，但 migration bundle/trigger 对每个现有 terminal 都要求非空并解引用 result ref（`00020...sql:115-188,390-456`）。Phase 0 contract 不能被当前 repository round-trip。
5. **Old finalizer is intrinsically single-ModelRun.** command、lookup、publication、receipt、event 全部要求或解引用一个 ModelRun（`postgres/finalizer.go:76-86,130-199,254-323,554-591`）。必须保留 RAG finalizer，并在 Phase 3 新建 WA finalizer。
6. **Null must not become omitted.** Go pointer 配 `omitempty` 会把 required-nullable 字段变成缺失；TS exact decoder 与 OpenAPI required array 会漂移。字段需要无 `omitempty` 的 pointer。
7. **Public Answer currently omits top-level ModelRun ID.** `answerResponse`（`handler.go:139-155`）只返回 raw result；因此 result envelope 的 `model_run_ref` 是公开 provenance 的唯一位置，null/UUID 必须精确。
8. **OpenAPI/router parity prevents early endpoint publication.** 项目以 OpenAPI method/path 为 wire 事实源，增加 timeline path 却不注册 handler（或反向）都会破坏 parity；应在 Phase 4 同步提交。
9. **Frontend decoder is exhaustive.** `conversation.ts:423-474` 会拒绝 `failed/cancelled`、新 result type 和不同 retrieval summary。Backend 一旦能返回新历史 turn，旧 Web 即使 composer feature flag 关闭也可能崩；OpenAPI/HTTP/Web union 必须同一 contract release。
10. **Do not overload RAG current_stage/retrieval_summary.** Web 只接受固定 RAG stages 和 RAG-specific summary（`conversation.ts:399-448`）。WA timeline 是独立 authoritative snapshot；Answer summary 需要 mode-aware strict union，不能塞 fake RAG values。
11. **SSE is not truth.** 不能从 `workspace_analysis.*` events 累积 timeline；事件丢失/重复后必须 REST reload（`design.md:295-297`; `docs/architecture/application-contracts.md:61-77`）。
12. **Generic summary is a data leak boundary.** 关键词 redaction 不能证明安全；typed server projection 应从 receipt/database facts 选择 allowlisted fields。尤其禁止路径、porcelain、query/prompt、snippet/excerpt、Citation private tuple、provider body/ID。
13. **ModelRun null is semantic, not loading failure.** null 只表示没有 authoring ModelRun。Repository 未加载、模型状态 Unknown、Call 未确定提交都不能被同一个 null silently 表示；Unknown 仍需要明确 terminal reason。
14. **ModelRun domain has no CANCELLED status.** 当前 status 只有 RUNNING/SUCCEEDED/REFUSED/FAILED/UNKNOWN（`internal/agent/domain/runtime.go:22-31`）。不要为 Answer cancellation 伪造 ModelRun CANCELLED；Answer 可以 cancelled 且 ref null。
15. **Readiness namespaces differ.** `AGENT_WORKSPACE_ANALYSIS_RUNTIME_NOT_READY`、`WORKER_*` health code 与 `WORKSPACE_ANALYSIS_CAPABILITY_UNAVAILABLE` 是不同边界；必须显式映射，不能直接把内部 error detail 暴露到 Problem/timeline。

### 10. External references

未使用外部资料。本研究只依赖仓库内设计、Trellis specs、OpenAPI 事实源和当前代码；该问题是项目专属合同盘点，不需要第三方 API/version 信息。

### 11. Related specs

- `.trellis/spec/backend/http-boundary.md:11-20`：strict JSON、OpenAPI/runtime exact route parity、Problem 边界。
- `.trellis/spec/backend/error-handling.md:11-64`：跨 HTTP/Workflow/log/audit 的 stable error code 与敏感信息限制。
- `.trellis/spec/backend/error-handling.md:435-485`：required-nullable DTO 不能因 `omitempty` 漂移，必须有 exact JSON tests。
- `.trellis/spec/backend/timeline-impact.md`：Timeline 是 owner event 投影、SSE 只 invalidation 的边界；Analysis Timeline 只借鉴原则，不复用业务 DTO。
- `.trellis/spec/frontend/type-safety.md:90-145,360-405`：`web/src/api/conversation.ts` 是唯一 wire owner，strict discriminated union 必须与 backend/OpenAPI 同步。
- `.trellis/spec/guides/cross-layer-thinking-guide.md`：共享 enum/DTO/错误码需要沿完整数据流检查 persistence、HTTP、frontend 与 recovery。
- `docs/architecture/application-contracts.md:3,11-20,36-41,61-77,90-97`：OpenAPI 唯一 wire 事实源、Conversation/RAG 语义、SSE 非事实源与前端 wire ownership。

## Caveats / Not Found

1. `workspace_analysis_refusal` 和 `workspace_analysis_termination` 的 exact schema IDs、payload keys、byte/count limits、Citation 是否允许以及 `termination_reason`/`reason_code` 的字段关系未在 PRD/design 中给出，尚不能视为冻结事实。
2. 除 `WORKSPACE_ANALYSIS_RESULT_UNKNOWN` 与 `WORKSPACE_ANALYSIS_CANCELLED` 外，design 只列 code families；本文件表中的 exact names/status/retryability 是建议，需要主会话/用户批准。
3. Citation invalid 与 receipt invalid 何时发布 refusal、何时发布 failed 仍是歧义；必须用 deterministic decision table 消除“refusal or stable failure”。
4. Timeline 的 schema ID/version、run/item enum、最大 item 数、sequence 起点、duration/error nullability、budget cost wire unit、latest event sequence 的零值语义尚未定义。
5. Design 对 completed `estimated_cost` 使用名称，而 budget persistence 使用 `estimated_cost_microunits`；公开结果/timeline 应统一精确字段和单位，避免浮点货币。
6. 当前共享工作区中已有 Phase 0 相关代码正在出现（QuestionMode、Definition、budget/deadline policy），但尚未接入 runtime；本研究未执行 git 操作，无法也不应判断这些变更的提交归属。
7. 本研究为只读 inventory；未运行产品测试，也未修改任何产品/spec/OpenAPI 文件。
