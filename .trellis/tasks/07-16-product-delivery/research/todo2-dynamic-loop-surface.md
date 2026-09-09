# Research: TODO2 动态工具循环的 API / Web 公共合同与烟测接线

- Query: 真正的 `workspace-analysis@2` 如何接入现有 `/chat`，同时保留冻结 v1 的读取、恢复与幂等重放？
- Scope: internal；Dispatcher、API / Worker capability、Definition catalog、Answer / Timeline / SSE、OpenAPI / 生成客户端、Web decoder / Timeline / Stop、deterministic provider 与浏览器烟测。
- Date: 2026-09-09
- Task: `.trellis/tasks/07-16-product-delivery`；依据 `research/todo2-dynamic-loop-prd.md`。本文件是实施前代码研究和合同建议，不是开发或测试通过证明。

## Findings

### 1. 结论与实施边界

当前页面、公开结果和读取投影都严格锁定 v1，不能只换 Worker 执行器。最小完整变更是：新建 Question 选择 Definition v2；v1 / v2 的 Definition、运行 policy、公开结果与 Timeline 独立校验；v2 时间线按持久 journal 的真实操作顺序输出；继续复用现有 mode、HTTP endpoint、Answer publication 状态、Draft SSE、Workspace SSE 失效、Query key 和 Stop。

主会话已统一 v2 公共操作 phase：新增 `decide_next`，保留现有工具 / 合成 / 校验 phase；不把模型参数、Prompt、来源正文或 Tool 私有身份放进时间线。无需新增 `item.kind` 或用户可选的 Question mode。成功 Answer v2 的 `git_status` 为 required-nullable，模型可以跳过无关 Git；来源短引用固定为 Run-global `E1`–`E32`，永久绑定具体 receipt tuple。

冻结 v1 不可原地更改：`internal/conversation/workflow/workspace_analysis_contract.go:19` 的版本 1、`:39` 的 Graph Hash `6faa6f0eee72c7e99b3e6118d29322c7d0c377b655403c81fd2337e820a18f73`，以及 `:72` 的原 Definition 必须保留。

### 2. 新建、幂等 replay、capability 与 catalog

| 文件 / 位置 | 当前行为 | v2 接线要求 |
| --- | --- | --- |
| `internal/conversation/adapter/postgres/dispatch_contract.go:52` | `buildQuestionWorkflowDispatchPlan` 按 mode 选择 Definition；`:64` 对分析永远选 v1 | 新提交选 v2，保留按精确版本构建 v1 / v2 plan 的函数；不由浏览器传 Definition / policy |
| `internal/conversation/adapter/postgres/gorm_dispatch.go:247`、`:274` | replay 也调用 `startQuestionWorkflow`，再次根据当前代码构建 plan | replay 先从已绑定 `workflow.run` / Definition 读取真实版本，再构造同版 plan；不能让历史 v1 使用新 v2 hash 重放 |
| `internal/conversation/adapter/postgres/gorm_dispatch.go:78` | mode 有效但缺 Run starter 时先返回 capability unavailable | 新建 fail closed 继续保留；明确区分历史 GET / Timeline 与新建接单 gate，已有 Run 的读取不得依赖 v2 ready |
| `internal/conversation/adapter/postgres/dispatch_contract.go:204` | 分析 idempotency 前缀为 `workspace-analysis-question:` | 不需要加入浏览器提交的新版本字段；按持久 Question / Workflow binding 解决 replay |
| `cmd/api/workspace_analysis.go:95`、`:104`、`:125` | Run starter 绑定 v1 Definition hash、工具 hash、v1 timeouts、policy=1、config revision | 组装 v2 starter 和 exact v2 capability contract；旧广告不能证明 v2 可接单 |
| `cmd/worker/workspace_analysis_capability.go:49`、`:54`、`:73` | exact Definition deep-equal、逐节点 executor 检查、工具快照、policy=1；10 秒续约 | 广告必须证明 v2 Definition / executor / tool / policy / runtime limits 完整匹配；v1 executors 另保留用于旧任务恢复 |
| `cmd/worker/main.go:1470`、`:1910`、`:2274` | schema catalog 已有 `[1,2]`；只有 v1 分析 Definition 与六 executor 注册 | 注册双版本，新增 loop executor；不要拿 RAG v2 的就绪代替分析 v2 就绪 |
| `cmd/api/main.go:1976` | API runtime validation catalog 当前只允许 schema `[1]` | 如果新 Definition input / output schema 使用 2，API 同样需要允许 2；只变 DefinitionVersion、不变 wire input version 则按实际合同处理 |
| `internal/workflow/application/definition_registry.go:143`、`:193` | registry 按 key+version 解析；Graph 禁止环 | 动态循环应位于受恢复保护的 loop executor / journal 中，不能给 Workflow DAG 添回边 |

上述 `cmd/*/main.go`、OpenAPI 主文件仅列变更要求，本 research 不修改；由主会话统筹与其他活跃任务的共用文件改动。

### 3. 当前公开合同的硬限制

| 层 | 真实代码 | 限制 / 影响 |
| --- | --- | --- |
| Timeline Go envelope | `internal/conversation/domain/workspace_analysis_timeline.go:189` | `schema_version` 只接受 `v1`，最多 32 项，sequence 必须 `index+1` |
| Timeline Go success | 同文件 `:220` | 要求固定六 phase 的精确成功操作数量；model=3，Source=1–3；不接受第二轮 Search |
| Timeline Go item | 同文件 `:271`、`:327` | model 只允许 retrieve / synthesize / review；工具按 phase 精确匹配，Token cap 按 phase 固定 |
| Timeline Go budget | 同文件 `:389` | maxima 必须等于 v1 policy，不是任意 `used <= max` |
| Timeline SQL / sort | `internal/conversation/adapter/postgres/gorm_workspace_analysis_timeline.go:170`、`:188` | 先按 `phaseOrder`，再按 operation ordinal 排序，最后临时重新编号；不能保留 Search → Read → Search 的真实顺序 |
| Timeline phase projection | `internal/conversation/adapter/postgres/workspace_analysis_timeline_contract.go:210` | 将 node key 一对一变成六个 phase；v2 node key 不能继续当作每个动态 operation 的 phase |
| 成功 Answer Go | `internal/conversation/domain/workspace_analysis_result.go:125`、`:143`、`:209` | `model_calls=3`、tool 4–6、Token 上限固定、Git 必填、schema=v1 |
| Answer / Timeline Web | `web/src/api/conversation.ts:153`、`:537`、`:557`、`:714`、`:780`、`:821`、`:845` | TypeScript 字面量 `modelCalls: 3`；strict decoder 重复完整 v1 约束，扩展必须双分支 |
| Source 引用 | `web/src/api/conversation.ts:758`；OpenAPI `WorkspaceAnalysisTimelineSourceSummary` | 只接受 `E1`–`E3`；不能将多轮 Search 都从 E1 重编号，否则同 Run 引用失去身份 |
| Citation 摘要 | `web/src/api/conversation.ts:769` | valid+invalid 总数 1–3；与成功 Answer 通用 Citation 数组上限 500 不同，不能只改数组上限 |
| UI | `web/src/features/rag/WorkspaceAnalysisTimeline.tsx:28`、`:69`、`:225` | phase label / icon 是 exhaustive Record；渲染按返回数组顺序，key=sequence |
| Answer 当前阶段 | `internal/conversation/application/repository.go:122`；`web/src/api/conversation.ts:352`、`:640` | `current_stage` 是固定 RAG 专属枚举；分析 v2 继续用 Timeline，维持该字段为 null，避免混进 RAG 阶段 |

### 4. 可直接交给实现者的 v2 wire 合同

#### 4.1 保留的 HTTP 入口

```text
POST /api/v1/conversations/{conversation_id}/questions
  mode: "rag" | "workspace_analysis"        # 请求不增加 definition_version

GET /api/v1/answers/{answer_id}?workspace_id=...
GET /api/v1/conversations/{conversation_id}/turns?workspace_id=...
GET /api/v1/answers/{answer_id}/analysis-timeline?workspace_id=...
POST /api/v1/workflows/{run_id}/cancel
  { "expected_version": <authoritative workflow.version> }
```

Timeline endpoint 返回同一个 schema ID 的 v1 或 v2，具体版本由 Answer 所绑定的持久 Run 决定。禁止以当前 feature flag、最新配置或客户端传参猜版本。

#### 4.2 Timeline v2：不增加一种新的 item kind

建议独立 `WorkspaceAnalysisTimelineV2` / `WorkspaceAnalysisTimelineItemV2`，v1 structs 与 validators 保留。共同字段类型如下；最大数量 / Token / 时间等数值由主设计冻结的 v2 policy 常量给出，不能沿用 v1 的 3 / 6 / 3，也不能在三个层各定一个值。

```go
// 新增 phase，仅用于真实模型决策；工具 operation 继续使用既有语义 phase。
const WorkspaceAnalysisPhaseDecideNext WorkspaceAnalysisPhase = "decide_next"

type WorkspaceAnalysisTimelineItemV2 struct {
    Sequence   int                                  `json:"sequence"`
    Kind       WorkspaceAnalysisTimelineItemKind    `json:"kind"`       // node | model | tool
    Phase      WorkspaceAnalysisPhase               `json:"phase"`      // 新增 decide_next
    Status     WorkspaceAnalysisTimelineItemStatus  `json:"status"`
    ToolRef    *WorkspaceAnalysisTimelineToolRef     `json:"tool_ref"`
    DurationMS *int64                                `json:"duration_ms"`
    ErrorCode  *string                               `json:"error_code"`
    Summary    *WorkspaceAnalysisTimelineSummaryV2   `json:"summary"`
}
```

- envelope 保留 `schema_id, schema_version, workspace_id, answer_id, analysis_run_id, run_status, termination_reason, items, budget, latest_server_event_sequence`；v2 version=`"v2"`。
- `sequence` 必须来自持久 journal 的稳定公开序位：同一个 logical operation 从 started → succeeded 只更新同一项，replacement Attempt 不追加重复项；下一步只追加更大序位。数组按该序位排列。
- 若内部 journal 的 sequence 包含公开层不展示的事件，公开连续序位必须有确定性映射；不要直接拿含间隙的内部 event sequence 再声称是 `1..N`。若不输出 node 占位项，v2 的 item.kind 仍可复用现有 union，动态 loop 自身无需额外公开 node 行。
- 不需要另增 `step_type`；phase 已表达用途、kind 已表达模型 / 工具。可直接用 sequence 在 UI 显示“第 N 步”。若产品需要显示“第 N 轮模型决定”，可后续增加服务端 `decision_no`，本轮最小合同不必引入。
- `kind=model, phase=decide_next` 的成功摘要继续用 `kind=model_usage`，展示本次 Token 使用；紧接着的具体 tool 行展示模型选出的工具，或紧接 synthesize 行表明模型已选择结束取证。不要为了展示决策而公开模型自由文本 / reasoning。
- 工具 phase 由已授权 operation 的 tool contract 导出：ReadGitStatus → inspect_workspace；SearchKnowledge → retrieve_evidence；ReadSource → read_evidence；ValidateCitation → validate_citations。
- `decide_next` 的 `tool_ref` 必须 null；成功 Tool 必须匹配精确 name/version 和对应摘要 kind。工具目录如需新版本，v2 allowlist 精确追加新 tuple，不应接受任意正整数版本。
- 保留 required-nullable 的 `tool_ref,duration_ms,error_code,summary`。active 项不带虚假的完成时间 / 摘要；失败 / Unknown 不带成功结果；既有 status 与公开终止码足够覆盖，不需要新增终态。
- Source summary 保留 `{evidence_ref,content_hash,truncated}`，短引用扩为 Run-global `E1`–`E32`，跨 Search 追加并永久保持 receipt tuple 绑定。公开 regex 为 `^E(?:[1-9]|[12][0-9]|3[0-2])$`；Citation 摘要计数同步上限 32。32 是命名空间上限，不自动等于 Source 调用预算上限。
- budget 字段保留 `model_calls,tool_calls,source_reads,input_tokens,output_tokens,estimated_cost_microunits`（最后一项 required-nullable）。全部从服务端 ledger 得出，max 绑定 v2 policy，不能由浏览器项目数推算。
- v2 成功校验根据动态 journal / publication proof 验证真实闭环，不再统计“某 phase 必须恰好一个”。仍必须证明 candidate、Citation validation 和独立 Review 完成、预算与成功 operation 总量一致。
- item 上限需从 policy 推导，至少容纳所有允许的 decision、tool、synthesis、validation、review 及公开 node 项；不能为了通过旧 32 上限而悄悄截断历史。

示例顺序（数字仅说明顺序，不是运行 policy）：

```text
1 model / decide_next       → 2 tool / inspect_workspace
3 model / decide_next       → 4 tool / retrieve_evidence
5 model / decide_next       → 6 tool / read_evidence
7 model / decide_next       → 8 tool / retrieve_evidence
9 model / decide_next       → 10 tool / read_evidence
11 model / decide_next      → 12 model / synthesize_answer
13 tool / validate_citations → 14 model / review_publish
```

这条序列不能按 phase 分组重排；刷新、SSE 重放、终态回查后序位保持不变。

#### 4.3 成功 Answer v2：复用载荷，分支放宽有界预算

建议继续使用 `result_type="workspace_analysis"`、`schema_id="conversation.workspace_analysis_answer"`，新增 `schema_version="v2"`。保留 payload 字段 `answer_markdown,citations,git_status,budget,proposal_suggestion,termination_reason`。

```go
type WorkspaceAnalysisBudgetSummaryV2 struct {
    ModelCalls              int    `json:"model_calls"` // 有界实际值，不是 const 3
    ToolCalls               int    `json:"tool_calls"`
    InputTokens             int64  `json:"input_tokens"`
    OutputTokens            int64  `json:"output_tokens"`
    EstimatedCostMicrounits *int64 `json:"estimated_cost_microunits"`
}

type WorkspaceAnalysisAnswerPayloadV2 struct {
    AnswerMarkdown     string                               `json:"answer_markdown"`
    Citations          []agentdomain.Citation               `json:"citations"`
    GitStatus          *WorkspaceAnalysisGitStatus           `json:"git_status"` // 无 omitempty；未调用时 null
    Budget             WorkspaceAnalysisBudgetSummaryV2     `json:"budget"`
    ProposalSuggestion *WorkspaceAnalysisProposalSuggestion `json:"proposal_suggestion"`
    TerminationReason  string                               `json:"termination_reason"` // COMPLETED
}
```

- Citation identity / href 和 `model_run_ref` 仍来自已发布的 server-owned 事实，不能以 planner / decision Run 替代 candidate authoring Run。
- 建议保留 refusal / termination / clarification 的既有 schema v1：它们是公开结果形状版本，不必跟着 Workflow DefinitionVersion 机械加号；只有字段语义真的改变才新增结果分支。
- Git 语义已由主会话确认：v2 的 `git_status` 必须存在，但未调用 Git 时为 null；模型可以跳过无关 Git。Go 使用 pointer 且无 `omitempty`，TS 使用 `WorkspaceAnalysisGitStatus | null`；不得把 Git 设为必选工具重新固定流程，也不得生成假的默认值。成功仍要求真正读取的 Evidence、有效 Citation 和独立发布审核。
- Answer 顶层 publication status/result type 不变，历史 `workspace_analysis@1` 必须继续发旧 result schema、旧 hash，不能读取时改写成 v2。

#### 4.4 OpenAPI / 生成客户端具体分支形式

为减少已有引用迁移，保留现有 `WorkspaceAnalysisTimeline` 与 `WorkspaceAnalysisAnswerResult` schema 作为冻结 v1；添加两个新的 v2 schema 和共用 union wrapper：

```json
{
  "WorkspaceAnalysisTimelineResponse": {
    "oneOf": [
      {"$ref": "#/components/schemas/WorkspaceAnalysisTimeline"},
      {"$ref": "#/components/schemas/WorkspaceAnalysisTimelineV2"}
    ]
  },
  "WorkspaceAnalysisPublishedAnswerResult": {
    "oneOf": [
      {"$ref": "#/components/schemas/WorkspaceAnalysisAnswerResult"},
      {"$ref": "#/components/schemas/WorkspaceAnalysisAnswerResultV2"}
    ]
  }
}
```

- `/analysis-timeline` 200 schema 改为 TimelineResponse；Answer result 的对应分支指向 PublishedAnswerResult。两个分支分别以 `schema_version` const v1/v2 区分，不能只以 `result_type` discriminator 区分。
- v2 Item / Budget / SourceSummary / CitationSummary / ModelSummary 采用新命名 schema；复用未改变的 Git、ToolRef、ProposalSuggestion、Counter 等组件。`WorkspaceAnalysisAnswerResultV2.payload.git_status` 仍列入 required，schema 使用 `oneOf: [{$ref: "#/components/schemas/WorkspaceAnalysisGitStatus"}, {type: "null"}]`。保持 `additionalProperties:false` 与 required-nullable。
- `api/openapi/openapi.json` 为唯一合同来源；`api/openapi/generate-client.mjs` 生成 `web/src/api/generated/`。不得手工改生成文件。
- 同步核对 `api/openapi/check.mjs` 中可能受影响的固定断言、`generator-input-manifest.json`、`generator-warning-baseline.json`；是否需要更新 manifest / baseline 应由实际生成的 audited normalizer 结果决定，不能预先放宽 warning。
- 网页仍从 `TimelineApi.getWorkspaceAnalysisTimelineRaw` 获取 raw response，经过 `web/src/api/conversation.ts` 的 dual strict decoder 进入 Domain UI；不要用 generator 的 `.value()` 反序列化替代边界验证。

### 5. SSE、Draft、页面和 Stop 复用边界

- `internal/conversation/http/handler.go:386` 已有 Timeline GET，Service 在 `internal/conversation/application/workspace_analysis_timeline.go:38` 再校验 scope 和完整 Domain；这条链都要接受 v2，而不是 HTTP Handler 直接透传未经校验 JSON。
- `web/src/events/server-events.ts:134` 的 event type 是有界 token，不是六 phase enum；`:230` 的 payload_summary keys 严格白名单。沿用 `workspace_analysis.*`，以 answer/conversation/workflow IDs 做 invalidation；不必加新的 SSE payload 字段。
- `web/src/features/rag/event-recovery.ts:55` 已把 `workspace_analysis.*` 转成 Answer-scoped timeline invalidation。`queries.ts:87` 在 queued/running 时 5 秒有界轮询；断线 recovery 复读 Workspace 的权威 Query。保留这一层，v2 provider/tool 完成必须产生服务端通知，不能只依赖最后完成事件。
- Timeline UI `WorkspaceAnalysisTimeline.tsx:225` 本就按数组顺序渲染。为 `decide_next` 加 label/icon，显示序号并对 model 行明确“模型决策”，重复 Search/Read 会自然显示多次；不要按 phase 聚合成固定六行。
- 当前 UI 会展示 model/tool/source/input/output 预算，但 `estimated_cost_microunits` 尚未渲染（`:218`）。费用不是新秘密；有配置时可在同一预算区显示，有值才显示，且不得误标币种。
- `RagPage.tsx:195` 的成功答案直接读取 `gitStatus`；v2 的 nullable Git 合同必须同步到该处，null 时不渲染 Git 面板，否则即使解码成功也会 render crash。
- `Answer.current_stage` 保持 null；不要把 decide_next 写入 RAG current_stage，否则 `web/src/api/conversation.ts:640` 会拒绝整个 Answer。
- Draft 仍属于临时流，只有正式 Answer 决定 completed/refused/failed/cancelled。复用现有 `web/src/events/answer-draft-stream.ts` 和 `features/rag/answer-draft.ts`；动态 decision / tool 的 JSON 不进入 Draft。
- Stop：`web/src/features/rag/commands.ts:59` 固定最多 5 次 POST，只对精确 `409 WORKFLOW_VERSION_CONFLICT` 重新无 ETag GET 同绑定 Answer，要求 version 严格前进；不需要因 v2 循环新增取消 API，也不能放宽为通用 retry。
- `WorkspaceAnalysisTimeline.tsx:187` 使用 Answer.workflow.version 发 cancel。动态 loop 的服务端执行必须在每次授权 / 循环边界检查取消；前端 Stop 成功不是后端终态完成证明。

### 6. 确定性 Provider 与真实浏览器门禁

当前测试 Provider 未实现分析 v2 的真实工具选择：`cmd/rag-model-fixture/main.go:516` 是 v1 retrieval planner；`:765` 的 native tool 分支只识别现有 RAG 的 ReadSource exchange；`:403` 和 `:1156` 的 candidate stream 要求 v1 单次 search / 1–3 evidence。不能拿这些 v1 完成记录证明动态循环。

| 入口 | 可复用能力 | v2 必补 |
| --- | --- | --- |
| `cmd/rag-model-fixture/main.go` / `main_test.go` | strict request、模型/工具响应、candidate stream、独立 review、generation barrier | 精确识别 v2 tool catalog / transcript / output schema；按当前 request 中的受控前序结果选择 next tool / finish，不按全局请求计数编排；至少两种不同顺序 / 次数 fixture |
| `deploy/compose-workspace-analysis-smoke.sh` | 设置分析 smoke 模式后进入 `compose-rag-smoke.sh` | 更新为真正运行新 Dispatcher + v2 loop 的入口，同时保留明确 v1 历史读取回归 |
| `deploy/compose-rag-smoke.sh:692`、`:851`、`:1677` | 临时 Workspace / DB / Git / Evidence、真实 API / Worker、capability、幂等 replay、SSE、Playwright | 去除 v1 hard-code，以 v2 Definition / policy 过滤广告，核对真实 journal 顺序、第二轮检索和预算、replay 零新增事实 |
| `web/e2e/workspace-analysis.smoke.spec.ts:361`、`:370` | 页面提交 / 取消 / 引用 / 桌面 / 390x844 / 刷新 / 无私密标记 | 当前硬断言 `3/3`、`4/6`，替换为 fixture 对应的真实有界数；直接断言工具 DOM 顺序不是仅“已完成” |
| `deploy/compose-workspace-analysis-worker-restart-smoke.sh` | 真进程中断 / replacement / Unknown 证据 | 若复用必须按 v2 journal / decision step 更新；v1 synthesis 中断不等于动态循环中断已验证 |

最少必要验证（不是 M11 全产品矩阵）：

1. Go Domain + HTTP：v1/v2 Answer / Timeline strict decode；decision phase、重复 tool、Run-global refs、顺序/身份/预算不一致拒绝；Response scoped IDs 与 canonical version；历史 v1 hash 未变。
2. Dispatcher + capability 的真实 PostgreSQL：新建 v2；v1 已有 key replay 仍旧版；v2 same key replay 不增 operation / ModelCall / ToolCall / ledger；缺失/过期/错版本广告零新增事实；feature-off 历史 GET / Timeline 可读。
3. 生产 API / River / fixture：一种 Search→Read→Search→Read、另一种不同调用次序；后轮 request 确实包含上一轮受控结果，未知/写工具在执行前拒绝；成功证明有效 Citation+Review，budget exhaustion 与 cancel 为真实可达终态。后端 recovery 故障矩阵由 foundation research / 实施计划提供。
4. Web unit：`api/conversation.test.ts` 双版本与 corrupt variants；`RagPage.test.tsx`/Timeline 真实顺序和预算；`commands.test.tsx` 保留完整 Stop CAS；Query/SSE recovery 和 Draft 终态优先。不要仅把 fixture 旧数字全改大。
5. 真实浏览器：从 `/chat` 选择工作区分析并提交；先看 pending/decide_next/tool 行；完成后断言两次 Search 的真实相对顺序和 Citation；pending 期间刷新恢复；实际 Stop 终止；桌面与 390x844 无横向溢出、console/network 无未解释错误；Proposal 只读链接。页面权威时间线与 DB journal 对照。
6. 受影响 RAG 回归、Go/Web 必需门禁及 OpenAPI generate/check。M11 的全产品 E2E/AI Eval/发布包仍暂缓，不将 deterministic provider 结果称为真实模型质量评测。

### 7. Files found / 实现交接表

| 分工 | 文件 | 一句话职责 |
| --- | --- | --- |
| 公共领域合同 | `internal/conversation/domain/workspace_analysis_result.go`、`workspace_analysis_timeline.go`、`answer.go` 及 tests | 结果 / Timeline v1 保留、新增 v2、统一读取分派与完整性校验 |
| HTTP / 读取 | `internal/conversation/application/workspace_analysis_timeline.go`、`internal/conversation/http/handler.go` 及 tests | 新旧 typed response、scope 复核 |
| Timeline persistence | `internal/conversation/adapter/postgres/gorm_workspace_analysis_timeline.go`、`workspace_analysis_timeline_contract.go` | 根据 Run 版本加载；v2 从 journal 序位投影安全摘要 |
| 派发 | `internal/conversation/adapter/postgres/gorm_dispatch.go`、`dispatch_contract.go` 及 tests | 新 v2 / 旧 replay 版本绑定 |
| Definitions / readiness | `internal/conversation/workflow/workspace_analysis_contract.go`；新增 v2 合同文件；`cmd/api/workspace_analysis.go`、`cmd/worker/workspace_analysis_capability.go` | 冻结 v1、v2 组合与 capability；main.go 改动交主会话 |
| Schema / generation | `api/openapi/openapi.json`、`check.mjs`、`GENERATOR.md`、生成 manifest / baseline（按实际变更）；`web/src/api/generated/` | 公共 oneOf 分支、合同门禁与统一生成；不得手改 generated |
| Web boundary | `web/src/api/conversation.ts`、`conversation.test.ts` | raw→dual strict decode→UI model；预算/source refs/version contract |
| Web 展示 | `web/src/features/rag/WorkspaceAnalysisTimeline.tsx`、`RagPage.tsx`、`RagPage.test.tsx`、`rag.css`（如有布局需要） | decide_next、真实顺序、可变预算、nullable Git |
| Web recovery/control | `web/src/features/rag/queries.ts`、`event-recovery.ts`、`commands.ts` 及对应 tests；`web/src/events/*` | 保留单事实源、工作区隔离、bounded polling / Stop 与 Draft 终态优先 |
| 测试模型 | `cmd/rag-model-fixture/main.go`、`main_test.go` | v2 exact tool / transcript 响应分支 |
| 烟测 | `deploy/compose-rag-smoke.sh`、`deploy/compose-workspace-analysis-smoke.sh`、`web/e2e/workspace-analysis.smoke.spec.ts` | 真实 /chat v2 与 DB/SSE/浏览器证据；保留 v1 阅读与普通 RAG 回归 |

### 8. Related specs / versions / references

- 已读 `.trellis/workflow.md` 和 `trellis-start`。原生 hook 提供本任务路径；research role 不读取 implement/check manifests，不运行 git 操作，也不创建任务。
- `.trellis/spec/backend/workspace-analysis-contract.md`：本次版本扩展的直接规范；静态 v1 的限制属于历史合同，主会话需追加 v2 而不是抹掉旧约束。
- `.trellis/spec/backend/http-boundary.md`：Gin 单路由 / strict decoder / SSE flush / route inventory。
- `.trellis/spec/frontend/type-safety.md`、`state-management.md`、`quality-guidelines.md`：generated raw boundary、strict decoder、SSE 只 invalidation、Query/Workspace owner 与真实浏览器门禁。
- `.trellis/spec/backend/index.md`、`.trellis/spec/frontend/index.md`、`.trellis/spec/guides/index.md`：当前模块 / 工作流与跨层规范入口。
- `api/openapi/GENERATOR.md`：仓库权威生成说明；OpenAPI 3.1、generator CLI wrapper 2.40.1、generator 7.24.0、typescript-fetch、Zod 4.4.3；禁止浮动工具/手工生成文件改动。
- `web/package.json`：React 19.2.7、TanStack Query 5.101.2、TypeScript 5.9.3、Vitest 4.1.10、Playwright 1.61.1；本研究只复用本仓库现有接口，没有依赖未经版本核对的外部 API 建议。
- External references: 本题以当前仓库合同和对应锁定源码为事实源；未开展外部搜索。Eino 的具体 tool-loop SDK / 持久化设计由并行 foundation 研究负责，不在本文件重复推断。

## Caveats / Not Found

- 本轮只研究并写本 research 文件，未修改产品代码、OpenAPI、cmd/main、TODO4 文件，未运行产品测试或部署；以上验证均为待执行门禁。
- v2 的 budget 数字、journal Schema / operation identity 与 receipt tuple 绑定实现，须由主设计与 foundation 研究最终冻结；此文件不能替代这些持久化决策。Git required-nullable、公开 source refs `E1`–`E32` 已由主会话明确，不再是待选项。
- 当前 `workspace-analysis@1` 的完整 smoke 通过只能证明固定六阶段。将文档写成“v2 完成”前必须有新版公开链路和不同模型决策顺序的证据。
- 任何共用 main/OpenAPI/generated 文件改动需协调单 owner；主会话已指定一个 UI/API owner 统一负责 TODO2+TODO4 的 OpenAPI 与生成目录。TODO4 的 organizing/authoring/interview 本轮 research 不写、不覆盖。
