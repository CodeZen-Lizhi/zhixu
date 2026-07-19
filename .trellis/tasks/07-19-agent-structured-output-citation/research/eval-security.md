# M6-02 Eval 与安全门禁研究

## 1. 结论

M6-02 不能把模型返回了 JSON 或 PoC helper 测试通过视为完成。项目级门禁必须同时证明：真实 ChatModel seam 的输出经过严格 JSON、Schema、领域和 Evidence 校验；修复次数有界且失败显式暴露；引用可打开并支撑相邻结论；证据不足、冲突无法解释或引用失效时拒绝发布；Prompt Injection 不能改变工具权限；日志、Trace、错误和评测数据不泄露 Prompt、Source、Secret 或模型原始响应；每次运行能够反查实际 Model/Prompt/Schema/Retrieval 版本。

当前仓库仍没有正式 ChatModel Adapter，`internal/platform/models` 只有 Embedding 实现；现有 Workflow 持久化只保存 `input_schema_version/output_schema_version`，尚未保存 `model_adapter/model_id/model_version/prompt_version`。因此“模型版本记录”不是仅在日志里增加一个字段，而是 M6-02 必须处理的跨 Application、Workflow 和持久化契约缺口。

M2 Eino PoC 的正式结论仍是“不采用 Eino”：Structured Output helper 虽验证了严格 JSON 和一次修复，但没有接到 Eino Model/Graph 真实输出，门禁明确为 FAIL（`poc/eino/report.md:7-11,21-24`）。M6-02 应继续以项目自有 Interface + 直接 OpenAI-Compatible Adapter 为基线；不得把 PoC helper 搬入主模块后直接宣称门禁通过。

## 2. 事实来源与优先级

1. 当前产品/架构契约：`docs/architecture/*.md`。
2. 已执行的 M2 Eino PoC 任务、报告和测试：`.trellis/tasks/archive/2026-07/07-16-m2-eino-poc/**`、`poc/eino/**`。
3. 当前可执行持久化与可观测实现：`migrations/00011_river_runtime_foundation.sql`、`internal/platform/observability/**`。
4. 当前任务 `prd.md` 的 Requirements/Acceptance Criteria 仍为 TBD（`.trellis/tasks/07-19-agent-structured-output-citation/prd.md:8-11`），下列内容应转化为正式 PRD、design 与 implement 验收项后再编码。

## 3. Schema Repair 门禁

### 3.1 必须满足

| 门禁 | 仓库证据 | 建议验收 |
|---|---|---|
| 严格单 JSON 值 | PoC 使用 `json.Decoder.UseNumber`，并拒绝尾随第二个 JSON（`poc/eino/structured/structured.go:39-57`）；工具参数测试覆盖 malformed、null、unknown field、multiple values（`poc/eino/tooltrace/tooltrace_test.go:72-102`） | 空输出、非法 UTF-8、非法 JSON、多个 JSON、未知字段、缺字段、错误类型、越界数组/字符串全部明确失败，不能提取部分字段后成功 |
| Schema 后继续业务校验 | ChatModel 契约要求结构化输出或显式错误（`docs/architecture/interfaces-and-adapters.md:16-36`）；Agent 流程明确为 `Schema + Evidence Validator`（`docs/architecture/agent-rag-architecture.md:9-20`） | JSON Schema 通过但 Evidence、Relation 五分类、Applicability、Workspace 或 Provenance 不合法时仍失败；Schema 不能替代领域校验 |
| 修复有界 | M2 设计固定“最多一次修复，不允许无限重试”（`.trellis/tasks/archive/2026-07/07-16-m2-eino-poc/design.md:22-31`）；PoC 测试断言 repair 只调用一次且二次非法返回 `ErrRepairExhausted`（`poc/eino/structured/structured_test.go:26-38`） | 对每个 Node/调用记录 repair attempt；同一原始响应最多一次 repair；修复仍失败归约为稳定非成功结果，不进入循环或静默自由文本 fallback |
| 禁止假成功解析 | Agent 架构明确禁止自由文本正则解析兜底（`docs/architecture/agent-rag-architecture.md:82-95`）；Interface 也禁止不支持输入时自由文本/正则静默兜底（`docs/architecture/interfaces-and-adapters.md:90-96`） | 删除/禁用任何 regex JSON extraction、markdown fence 宽松截取或默认对象填充；失败返回稳定错误码并保留 redacted 摘要 |
| 真实 Model seam 覆盖 | M2 Structured Output 因未接 Eino Model/Graph 输出而 FAIL（`.trellis/tasks/archive/2026-07/07-16-m2-eino-poc/prd.md:19-25`，`poc/eino/report.md:21-24`） | Contract 测试必须从正式 ChatModel Adapter 返回原始响应进入解析/修复/业务验证；只测纯 helper 不算完成 |
| 可观测但不泄露 | Model metrics 已要求 `schema_repair`（`docs/architecture/observability.md:136-143`），日志仅允许稳定摘要（同文件 `:47-59`） | 记录 model/prompt/schema 版本、repair 次数、结果码、耗时和 token 摘要；不记录 invalid raw output、完整 Prompt/Source 或修复 Prompt |

### 3.2 文档冲突

`docs/architecture/agent-rag-architecture.md:82-93` 描述了 `Repair Attempt 1` 后还有 `Reduced Output Retry`，而已归档 M2 设计和可执行测试固定“最多一次 repair”（`.trellis/tasks/archive/2026-07/07-16-m2-eino-poc/design.md:26`；`poc/eino/structured/structured_test.go:26-38`）。在 M6-02 design 明确解决前，建议采用已验证且更保守的“一次修复后失败”作为实现依据；如产品确需 reduced-output 第二次模型调用，必须把它定义为独立、总次数仍有上限的策略并新增成本、超时、重放和评测门禁，不能把它隐藏在 parser 内。

## 4. Citation、Faithfulness、Conflict 与 Refusal Eval

### 4.1 确定性业务门禁

- RAG 最高层 seam 是 `Query → Retrieval → Answer → Citation/Refusal`（`docs/architecture/testing-and-evaluation.md:18-25`）。
- 引用验证必须证明引用 ID 存在、Source Span 可打开、引用文本支撑相邻结论、未批准内容未被当作正式事实；失败应有界再生成，达到上限拒绝发布（`docs/architecture/agent-rag-architecture.md:142-155`）。
- 证据不足应先扩展允许范围内的查询，仍不足则拒答；不能由模型常识补齐（同文件 `:118-140`）。
- 安全失败语义是“引用失效 → 不发布回答”（`docs/architecture/security.md:184-190`），因此引用身份、Workspace 绑定和 Span 可打开属于 100% fail-closed 不变量，不是平均分指标。
- 冲突 Claim 必须同时进入上下文，回答披露不同观点、适用条件与来源，不允许模型静默裁决（`docs/architecture/agent-rag-architecture.md:135-140`）。
- Relation Assessment 五分类为 `NEW/COMPLEMENTARY/DUPLICATE/CONFLICT/LOW_CONFIDENCE`（同文件 `:156-167`）；模型评测不能替代 Knowledge Domain 的确定性状态机、Evidence、Applicability、乐观锁和幂等测试（`docs/architecture/testing-and-evaluation.md:246-255`）。

### 4.2 Eval 数据与指标

Gold Set 至少包含 Question、Scope、Relevant Source Spans、Conflict、Expected Refusal（`docs/architecture/testing-and-evaluation.md:227-245`）；指标至少包含 Citation Precision/Coverage、Faithfulness、Conflict Disclosure、Refusal，以及 Retrieval 的 Recall@K、MRR/NDCG。Relation 五分类另算 Precision/Recall/F1，并重点监控 Conflict→Duplicate 高风险误判（同文件 `:246-251`）。

评测数据、Case 和 Run 必须版本化；Run 固定实际 model/prompt/embedding/chunk/rerank/index/workflow 版本并引用 baseline（`docs/architecture/database-design.md:596-624`）。任何 Model、Prompt、Schema 或 Retrieval 变更都触发数据集，若高风险指标下降则保持旧默认版本（`docs/architecture/agent-rag-architecture.md:272-279`；`docs/architecture/testing-and-evaluation.md:283-292`）。E2E 使用 Deterministic Fake，AI Evaluation 使用真实模型，两类证据不能互相替代（`docs/architecture/testing-and-evaluation.md:221-225`）。

### 4.3 当前缺口

- 文档列出了指标，但没有数值阈值、baseline 选择、置信区间、允许退化幅度或最小样本数。不得凭经验填数；M6-02 PRD/design 必须定义“绝对安全不变量”和“与 baseline 比较的质量指标”两类门禁。
- 当前仓库未发现正式 AI Eval Runner 或 M6-02 Gold Set，因此现在没有可执行的 citation/faithfulness/refusal eval 命令。创建 Runner、数据集版本和 canonical 命令是任务本身的交付项，未完成前不能用单元测试宣称 AI Eval 已通过。
- Faithfulness 不能只依赖第二次模型自评。最低限度应组合：引用身份/Span/批准状态的确定性校验、结论到 evidence ref 的结构化绑定、真实模型 Gold Set 评分；裁判模型及其版本也必须进入 Evaluation Run。

## 5. Prompt Injection 与“假成功”门禁

### 5.1 信任边界

- Source 与 Tool Result 必须标记为 untrusted data，System Policy 不与 Source 拼成同等角色；工具权限由服务端 Workflow 决定，Eino/模型只可转交 Tool Request（`docs/architecture/tool-security.md:119-141`）。
- Tool 执行顺序固定为 Registry → Schema Validation → Permission Decision → Executor → Adapter → Audit（同文件 `:7-17`）；模型不能请求未注册工具，不能构造身份、Capability 或 Write Authorization。
- Tool output 必须过 Schema、限长、脱敏、HTML 清理，并且不能直接作为下一条 System Message（同文件 `:180-187`）。
- PoC 已证明 permission/unknown tool/invalid args 在真实 Handler 前拒绝（`poc/eino/tooltrace/tooltrace_test.go:41-102`），但这是 Eino PoC 证据，正式 Agent/Tool seam 仍需共享 Contract Test。

### 5.2 禁止假成功

- M2 真实 Provider 只有显式环境变量才运行；缺少凭据明确 Skip，绝不切换 Fake（`.trellis/tasks/archive/2026-07/07-16-m2-eino-poc/prd.md:11-15`；`poc/eino/README.md:12-20`）。Skip 只能说明“未运行”，不能计为发布 PASS（`poc/eino/report.md:27,38`）。
- 正式 capability 未配置时应返回 nil/不可用能力，不能构造假 Adapter；这一模式已在 Embedding factory 契约中固定（`docs/architecture/interfaces-and-adapters.md:50-55`），ChatModel 应沿用相同原则。
- Chat Model 不可用时 AI Node 等待/失败；Review Model 不可用时不自动发布高风险结果；Web Tool 不可用且本地证据不足时拒答（`docs/architecture/agent-rag-architecture.md:262-270`）。
- 未知 Provider/Handler 错误默认不可重试；写入结果未知进入 manual recovery（`poc/eino/report.md:40-45`）。不得返回空 citations、默认 confidence 或 degraded success 掩盖失败。

### 5.3 安全测试语料

Prompt Injection corpus 至少覆盖：Source 要求忽略系统规则、读取 Secret、访问 Workspace 外路径、自动批准、伪造工具名/参数/权限、Tool Result 注入 System 指令。期望结果为 risk flag/隔离/拒绝/安全审计，而不是执行或静默忽略（`docs/architecture/tool-security.md:130-141,224-247`；`docs/architecture/security.md:192-205`）。

## 6. 日志、Trace、Audit 与脱敏门禁

| 门禁 | 证据 | 验收要点 |
|---|---|---|
| 统一 fail-closed Handler | slog JSON Handler 在写出前统一脱敏（`docs/architecture/observability.md:35-59`）；当前实现对敏感 key、凭据 URL、绝对路径、`[]byte` 和未知复合对象 fail closed（`internal/platform/observability/redaction.go:14-23,87-177,180-203`） | Agent/Model/Repair/Eval 全部注入项目 Logger，不直接 `fmt/log.Printf`；未知结构只记录 code/kind/count/hash |
| Correlation | request/workspace/workflow/node/tool/attempt/retry 等字段从 Context 自动合并（`docs/architecture/observability.md:15-33`） | Model call、repair、citation validator、refusal、tool request、eval case 都能关联到稳定 run/case/node，但高基数 ID 不进入 metric label |
| 内容最小化 | 禁止 API Key、Authorization、Credential、DSN、完整 Prompt/Source、模型原始响应和绝对路径（同文件 `:47-59`；`.trellis/spec/backend/logging-guidelines.md:35-69`） | 日志/Trace/Audit/错误/测试失败输出做 Secret、Prompt、Source、model raw output canary 扫描 |
| 版本与统计可查 | Trace 属性要求 model、prompt_version、index_version、retry_attempt（`docs/architecture/observability.md:61-87`）；Model metrics 包含 calls/tokens/latency/errors/schema_repair（同文件 `:136-143`） | 增加稳定 result/refusal/error code、repair count、citation count/valid count；不得把 evidence ID、query 或 Workspace ID 作为 metric label |
| 不伪造可观测成功 | 当前 telemetry optional 明确 degraded、required fail-fast，不得将 noop 当 exporter 成功（同文件 `:192-204,227-245`） | Model/Eval 指标写失败不改变业务结论，但不能声称已外部上报；发布记录区分内存 snapshot 与真实 exporter |

当前 `internal/platform/observability/logging_test.go:12-85` 已验证嵌套/绑定字段、错误、DSN、Bearer、正文和绝对路径脱敏。M6-02 应新增 Agent 专项 canary：invalid model JSON、repair cause、citation excerpt、Prompt Injection payload、provider error body、API Key、Authorization、DSN、绝对路径均不得出现在日志、Trace、Problem、Audit metadata 或测试失败文本中。

## 7. 超时、取消与重试门禁

- Model/Tool Interface 必须显式传递 Context、超时和可重试错误（`docs/architecture/interfaces-and-adapters.md:7-15,25-36`）；所有 Adapter Contract 覆盖 Success、Timeout、Retryable/NonRetryable、Idempotency、Version Conflict、Resource Cleanup（`docs/architecture/testing-and-evaluation.md:39-48`）。
- PoC 的可执行分类为：`context.DeadlineExceeded` → retryable，调用方 `context.Canceled` → non-retryable（`poc/eino/tooltrace/tooltrace_test.go:262-293`）；配置的 Tool timeout 必须真实中止 Handler（同文件 `:295-332`）。未知错误默认 non-retryable（`poc/eino/chatgraph/runner_test.go:50-77`）。
- Read/Search 可安全重试；Web Fetch 可在限流下重试；File/Git 只按幂等记录重试；非幂等未知结果进入人工恢复（`docs/architecture/tool-security.md:188-203`）。
- Schema/业务校验失败不应通过 Workflow 无限重试同一个响应；PoC 的 `schema rejected` 被归为 non-retryable（`poc/eino/nodeexec/nodeexec_test.go:39-44`）。Provider 429/5xx、deadline 等仅在 Adapter 能明确分类且总 attempt/backoff 有界时自动重试。

当前缺口：架构尚未给出 ChatModel 调用、repair 调用、citation regeneration 的默认 timeout、最大总 attempt、backoff/jitter 和 token/cost budget。M6-02 design 必须冻结这些策略，且“initial + repair + retry”的总调用数应可由测试精确断言；不得在 HTTP Adapter、Agent Service 和 Workflow 三层各自叠加重试。

## 8. 模型、Prompt、Schema 和 Eval 版本门禁

### 8.1 必须记录

- ChatModel 接收版本化任务、消息、工具描述和输出 Schema，调用方知道模型标识、能力、Token 限制、超时与错误分类（`docs/architecture/interfaces-and-adapters.md:16-36`）。
- Prompt 使用版本化模板，运行记录保存实际版本；Structured Output 使用 JSON Schema + 领域校验（`docs/architecture/technology-stack.md:59-69`）。
- Node Run 应记录 workflow definition、prompt template/version、model adapter/id/version、input/output schema version、index/embedding/chunk strategy 版本和 usage summary；启动时冻结，完成后不可被默认配置覆盖，缺失版本应失败而不是静默使用 latest（`docs/architecture/database-design.md:731-748`）。
- Evaluation Run 固定 dataset/baseline 和所有实际模型、Prompt、Embedding、Retrieval 版本（同文件 `:596-624`）。

### 8.2 当前实现差距

`migrations/00011_river_runtime_foundation.sql:24-49` 只给 `workflow.node_run` 增加了 idempotency、input/output schema version 和 dispatch no；仓库迁移/Workflow 代码中未发现 `prompt_version/model_adapter/model_id/model_version` 字段。M6-02 若要求“模型版本记录”可验收，必须选择一个持久、不可变且可查询的事实源，并补 migration、Repository、Domain/Application、重放和数据库约束测试；只写 slog 字段不满足历史可复现要求。

“model version”也不能只保存用户配置的 model name。至少需要区分 provider/adapter version、requested model ID、provider 返回的 resolved model/revision（若协议提供）、Prompt version 和 output schema version；Provider 未返回 revision 时应显式保存“不可得”的受控语义，而不是伪造版本。具体字段和兼容策略需在 design 锁定。

Eino 当前不是正式依赖：技术栈明确因 Streaming、Structured Output、Embedding/Retrieval/Rerank、River Node 和真实 Provider 门禁未全过而“不正式采用”（`docs/architecture/technology-stack.md:69-75`）。M6-02 不应把 Eino 类型放入 Domain/Application；如未来复验，通过稳定 ChatModel/ToolExecutor 接口替换即可。

## 9. Deterministic Fake 门禁

- 单元、Contract 和 E2E 使用手写 Deterministic Fake；AI Eval 使用真实模型（`docs/architecture/technology-stack.md:92-101`；`docs/architecture/testing-and-evaluation.md:221-225`）。Fake 不能作为生产 fallback，也不能证明真实模型质量。
- M2 Fake 是并发安全、固定行为的 ChatModel，可配置 Response、Role、Error、WaitForCancel，并记录 call count/last input 的防御性副本（`poc/eino/fake/chatmodel.go:11-38,40-72,80-110`）。正式 Fake 至少应保持这些能力，并增加按调用序列返回、严格请求匹配、Model/Prompt/Schema 版本回显、usage 固定值和 repair 调用计数。
- Fake 与正式 Adapter 必须通过同一 Contract：Success、Timeout、Retryable/NonRetryable、取消、错误包装、资源释放、结构化输出、版本回显；不能给 Fake 开专用成功分支（`docs/architecture/interfaces-and-adapters.md:328-337`）。
- 测试必须断言非法输入在模型调用前失败，M2 已有 `CallCount == 0` 证据（`poc/eino/chatgraph/runner_test.go:150-170`）；并发测试应避免共享基础模型被 request-scoped tool binding 原地修改（`poc/eino/tooltrace/tooltrace_test.go:343-388`）。

## 10. 风险与待设计决策

| 风险 | 影响 | 必须在 M6-02 处理 |
|---|---|---|
| Schema Repair 次数文档冲突 | 可能导致重复模型成本、延迟和无限重试 | design 明确总调用图；默认采用一次 repair 的已验证基线 |
| Eval 无阈值、无数据集、无 Runner | 无法客观判定 Citation/Faithfulness/Refusal 是否通过 | 定义版本化 Gold Set、baseline、高风险不变量与 canonical 命令；不猜数值 |
| 正式 ChatModel Adapter 缺失 | 无法完成真实输出到 validator 的闭环 | 建立稳定项目 Interface、直接 OpenAI-Compatible Adapter、Contract 与 deterministic fake |
| Workflow 未持久化 Model/Prompt version | 运行不可复现，默认变更可污染历史解释 | 新增不可变实际版本事实源与迁移/重放/查询测试 |
| Retry 在 Adapter/Agent/Workflow 叠加 | 调用放大、成本失控、重复 repair | 单一策略 owner，总 attempt/cost/timeout 可断言 |
| Faithfulness 仅靠模型裁判 | 自洽但无证据的回答可能误过 | 确定性 citation binding + 真实模型 eval + 裁判版本记录 |
| Prompt Injection 只做文本检测 | 变体绕过后可能获得工具或写权限 | 权限必须服务端独立判定；检测只增加 risk/audit，不能承担授权 |
| 日志记录 invalid raw output 便于调试 | Prompt、Source、Secret 和用户数据泄漏 | 只存 hash/length/code；短期 debug 也需显式开关与保留期，当前默认禁止 |
| Live smoke Skip 被当 PASS | 真实 Provider 行为未验证却进入发布 | 发布报告将 PASS/FAIL/SKIP 分开；required gate 出现 SKIP 即未完成 |

## 11. 验证命令

### 11.1 当前仓库可执行证据

```bash
# M2 PoC：只证明报告中已覆盖的边界，不会把 FAIL/SKIP 变为 PASS
(cd poc/eino && go test -race ./...)
(cd poc/eino && go vet ./...)

# 当前统一日志/脱敏实现
go test -race ./internal/platform/observability

# 主模块基础门禁
go test -race ./...
go vet ./...
git diff --check

# 版本字段现状与禁止内容静态审计
rg -n 'prompt_version|model_adapter|model_id|model_version|input_schema_version|output_schema_version' migrations internal
rg -n 'fmt\.Printf|log\.Printf|Authorization|api[_-]?key|raw_response|full_prompt' internal
```

### 11.2 M6-02 落地后新增 canonical gate

具体包路径必须由 design 按实际文件确定，不能在研究阶段猜测。canonical gate 至少组合为：

1. Domain/Application unit：严格 JSON、Schema、一次 repair、Evidence/Citation、Conflict、Refusal、五分类、版本冻结、所有失败路径。
2. 正式 ChatModel Adapter Contract：httptest Provider 覆盖成功、429/5xx/4xx、timeout/cancel、非法/超大/多 JSON、模型版本回显、错误与日志不泄密。
3. Workflow/PostgreSQL integration：Node Run 实际版本持久化、append-only/重放、repair 与 retry 次数、失败不伪成功。
4. Deterministic Fake E2E：固定 Fixture 下 Answer/Citation/Refusal 可重复，race 下无共享状态污染。
5. 真实模型 AI Eval：固定 dataset/baseline，输出 Citation Precision/Coverage、Faithfulness、Conflict Disclosure、Refusal 和五分类指标；记录 PASS/FAIL/SKIP，required gate 不接受 SKIP。
6. Security/observability canary：Prompt Injection corpus、工具权限前置拒绝、invalid model output/Prompt/Source/Secret 不进入日志、Trace、Audit、Problem 或测试错误。

在 AI Eval Runner、数据集和命令文件实际创建前，不存在可诚实执行的“真实模型 Citation/Faithfulness/Refusal Eval”命令；此缺口应作为 M6-02 P0 任务，而不是用 `go test` 替代。
