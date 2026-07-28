# 后端质量与交付规范

## 适用范围

适用于后端所有领域模块、API、Worker、数据库/文件/Git/模型 Adapter、迁移、Workflow 和安全边界。
仓库已进入 M6-D；本文件记录可执行质量门禁，但只有实际命令输出才能证明某项测试或 smoke 已通过。

## 已确认事实

- 模块必须是深模块：小 Interface、明确输入/输出/不变量/错误/性能约束，复杂度隐藏在实现中（依据 [`module-architecture.md`](../../../docs/architecture/module-architecture.md) 第 2 节）。
- 核心领域逻辑不能绑定框架；外部工具通过 Adapter，Composition Root 负责配置、构造和注入（依据 [`technology-stack.md`](../../../docs/architecture/technology-stack.md) 与 [`interfaces-and-adapters.md`](../../../docs/architecture/interfaces-and-adapters.md)）。
- 必须覆盖主路径和异常路径、审计、可观测、自动测试、AI Eval、文档和无未说明降级（依据 [`testing-and-evaluation.md`](../../../docs/architecture/testing-and-evaluation.md) 第 21 节）。
- 测试层级包括 Unit、Adapter Contract、PostgreSQL/Filesystem/Git/Workflow Integration、AI Evaluation、Playwright E2E、Docker Smoke 与恢复演练；Go 单元使用 `testing`/`httptest`，数据库集成采用 Testcontainers-Go。
- 安全门禁覆盖路径穿越、Symlink、SSRF、Prompt Injection、XSS、CSRF、SQL Injection、未授权 Tool 和 Secret Redaction。

## 目标代码落点（M1 起）

- 领域规则和状态机：各 `internal/<module>/`，通过纯函数/小 Interface 测试。
- Application/API：`internal/app/`、`cmd/api/`，通过 Handler/Contract/错误映射测试。
- Worker/Workflow：`internal/workflow/`、`cmd/worker/`，通过租约、崩溃恢复、幂等和补偿集成测试。
- Adapter：`internal/platform/*`，每个正式实现与 Fake/Fixture 共享 Contract Test。
- 数据库和迁移：`migrations/`、`internal/platform/postgres/`，通过空库升级、约束故障和 EXPLAIN 测试。
- 测试 Fixture、Gold Set、AI Eval、E2E 和部署验证路径由 M1 manifest/构建文件确定；不提前伪造目录或命令文件。

## 禁止模式

- 伪接口、硬编码假数据、静默 fallback、吞异常、假成功响应或把演示流程当作业务闭环。
- 在 Controller、页面边界、SQL 脚本中散落业务规则；共享校验、权限和状态机必须收敛到唯一领域事实源。
- 领域包依赖 HTTP、pgx/sqlc、River、Eino/模型 SDK、文件系统或具体 Git 命令。
- 无测试地修改公共 Interface、DTO、Schema、迁移、权限、错误码或幂等语义；禁止通过重命名绕过 Breaking Change 检查。
- 逐条查库/远程调用、无分页大集合、无界图谱、无版本 Embedding、非参数化 SQL、shell 拼接用户输入。
- 生产日志泄露 Secret、Prompt/Source 全文、用户回答或思维链；安全失败转成允许继续。
- 破坏性迁移、修改已应用迁移、`reset --hard`、任意 Git remote push、绕过 Proposal/Approval 的文件写入。

## 必须遵循的模式

1. 新行为先定义领域不变量和 Interface，再实现 Adapter/Handler；对外契约用 OpenAPI 和稳定错误码。
2. 所有有副作用命令使用 Idempotency-Key/作用域幂等键、Version/ETag/Change Hash，并记录 Audit 与 Trace 关联。
3. 长任务持久化为 Workflow；Node 有租约、心跳、重试分类、checkpoint、Outbox 和补偿；Human Task 等待时不占 Worker。
4. 通过参数化 SQL、Workspace Root/Symlink 检查、SSRF 逐跳校验、Tool Registry 权限和敏感信息脱敏保护边界。
5. Adapter Contract 必须验证 Success、Timeout、Retryable/NonRetryable、Idempotency、Version Conflict 和 Resource Cleanup。
6. 代码变更后按影响面运行 Unit → Contract → Integration → E2E/Smoke；模型、Prompt、Retrieval、Workflow 或 Schema 版本变更需要 AI Eval 和回归门禁。
7. Search API 的 Handler 只拥有严格 wire 解码、href 和 top-100 page slicing；Canonicalization、Active-only
   Search、RRF、Evidence binding 与降级属于 Domain/Application/Adapter，禁止复制到 HTTP。
8. API/Worker 的 Embedding 构造必须复用 Configured Embedder Factory；前端原始 Search JSON 只能通过
   `web/src/api/search.ts` 严格 Decoder 进入 Feature。

## 测试要求

每个新功能至少包含：

- 正常路径、边界条件和失败路径单元测试。
- 受影响 Adapter 的契约测试；涉及 DB/File/Git/Workflow 时补集成测试。
- 公共 API 的分页、幂等、版本冲突、错误映射和 SSE 重连测试（若适用）。
- 涉及 AI 的结构化输出、Schema Repair、Citation/Refusal、冲突披露和高风险指标回归。
- 涉及安全的负测；涉及文件/Git 的原子写、并发修改、Commit 失败、反向 Commit 和 Symlink Escape。
- 核心闭环的最小业务烟测与可追踪审计。

M6-D 已通过真实 PostgreSQL HTTP、River fault 与 Compose API smoke 一轮；以下仍是后续发布必须重复的
独立交付门禁，且不代表前端、全仓静态检查或独立审查已经完成：

- 真实 PostgreSQL HTTP integration：生产 Router/Repository、三模式/过滤、Cursor、Workspace isolation、
  Source Version/Span href、Artifact 完整性和生产 SQL `EXPLAIN (FORMAT JSON)`。
- 真实 PostgreSQL/River fault smoke：Completion 后通过 Router Search/Evidence，并在 response-loss/
  duplicate delivery 下断言唯一 Activation、Active 与 Completion。
- Compose API smoke：唯一 project + disposable Git Workspace，经公开 API 完成 Approval→Reindex→
  Hybrid-to-Keyword degraded Search→Evidence GET，成功/失败都清理 volume/临时目录。
- Web strict decoder 单测与 lint/typecheck/test/build；OpenAPI 三路径及关键 Schema drift check。
- go-review、sql-code-review 和独立只读审查。上述 gate 不能用单元 Fake、readiness 或文档描述替代。

架构文档给出的 CI 事实是：PR 运行 Unit、Lint/Static、Migration、Contract 和选定 Integration；主分支/发布运行 Full Integration、E2E、Security、Evaluation、Docker Smoke。具体 CI 配置在 M1 创建后以仓库文件为准。

## Code Review 清单

- 是否符合模块依赖方向，未把领域逻辑放进 Handler、Adapter 或 SQL？
- 是否更新 OpenAPI、DTO、数据库迁移、错误码、事件和对应契约测试？
- 是否有版本检查、幂等、超时、重试分类、补偿和未知副作用处理？
- 是否参数化 SQL、限制分页/大小/超时，避免 N+1、循环远程调用和无界结果？
- 是否有完整日志/Trace/Audit，且 Secret、Prompt、Source、用户回答脱敏？
- 是否覆盖主路径、边界、失败、安全负测、集成和最小烟测？
- 是否运行并记录受影响包测试、静态检查、构建和迁移验证？
- 是否同步更新真实文档，并明确待确认项，而不是用未经证实的版本、路径或 API？
- Cursor 是否绑定 canonical request、Active Index、完整 top-100 结果与 offset，并明确进程重启失效？
- Vector wire 是否为 `distance`；Evidence 是否只从不可变 Artifact 读取最多 4 KiB excerpt？
- 是否误把 Workspace 隔离/loopback 声称为 M10 Auth、CSRF 或 Capability 已完成？
- 是否误把 exact vector scan/小夹具 EXPLAIN 声称为 500,000 Chunk ANN/P95 已完成？

## 验证方式

### M0 当前（仅规范）

```bash
rg -n 'T(BD)|To[[:space:]]+be[[:space:]]+filled' .trellis/spec/backend
git diff --check
```

### M1 代码落地后

最小门禁为：

```bash
go test ./...
go vet ./...
```

并按影响范围补充迁移/集成、API Contract、E2E、Security、AI Eval、Docker Smoke 和恢复演练。`go.mod`、CI 和 Makefile 未创建前，不把具体 lint 工具、版本或命令参数当成既定事实。

## 当前后续门禁

- M6-D 前端、全仓静态/构建/测试、Review 与归档的最终运行结果；真实 PG HTTP/River/Compose smoke
  已完成一轮，后续发布仍需重跑。
- 统一覆盖率阈值、完整 E2E Fixture 和性能容量基准。
- License、发布门禁、SBOM 和镜像扫描配置；README 当前仍未确定许可证。

## M5-05 Knowledge Quality Gate

- `RelationAssessment` 与 `RelationType` 必须为不同类型；NEW/LOW_CONFIDENCE 不得产生 Relation 行。
- Domain 单测覆盖 Topic/Claim/Relation/Conflict 状态机、Applicability canonicalization、端点兼容矩阵、
  对称规范化、Evidence hash 和 Conflict fingerprint，关键纯函数执行 `-race -count=20`。
- Application/Repository 测试覆盖 Provenance/Confirmation fail-closed、Workspace 隔离、幂等重放、CAS、
  同事务 Confirm/OpenConflict、损坏对象 readback 和批量无 N+1。
- PostgreSQL integration 覆盖 00017 Up/Down guard、复合约束、deferred constraints、对称并发去重和
  response-loss replay；模型评测、文档或 Fake 不能替代真实数据库证据。
- 回归至少覆盖 Ingestion SourceSpan、Retrieval Search/Evidence、Change Control、Workflow；本任务不新增
  空壳 HTTP/OpenAPI，也不得把 Workspace 隔离误报为 M10 Auth 已完成。
- 提交前主 Agent 必须执行 go-review、sql-code-review、通用 review 和独立只读审查。

## Scenario: M6-03 Tool Security Quality Gate

### 1. Scope / Trigger

- 新增或修改 Tool Definition、Workflow `allowed_tools`、Tool Call 迁移、Executor、Safe Writeback audit、Web Fetch 或 Tool Runtime 配置时应用本门禁。
- Domain/Application/Adapter 依赖方向为 `adapter -> application -> domain -> capability/foundation`；Tools Domain 不得导入 Workflow、Change Control、HTTP、pgx、模型、文件系统或 Git。

### 2. Signatures

- 普通执行入口：`ExecutionService.Execute(context.Context, ExecuteToolCommand) (ToolExecutionResult, error)`。
- 持久策略入口：`WorkflowPolicyReader.ResolveToolPolicy(context.Context, TrustedExecutionIdentity) (WorkflowToolPolicy, error)`。
- 持久事实：`workflow.tool_call`，写入只能从 `REFUSED` 或 `STARTED` 开始，终态只能由 `STARTED` 通过 version CAS 产生。

### 3. Contracts

- API Contract Registry、Worker Execution Registry 与 Workflow Definition/Node contract 必须来自同一冻结 catalog；配置 enabled 但生产调用链不可达属于 P1，不得只靠 readiness 测试通过。
- strict Agent Tool Request 必须先转换为不含模型 `reason` 的持久 invocation；Node input 只允许空参数或稳定 ID tuple。Search query、Diff before/after、Prompt、正文、Credential、路径或授权字段不得进入生产 Node input。
- `ZHIXU_TOOL_RUNTIME_MODE` 默认 `disabled`；Web Fetch 还要求 `ZHIXU_WEB_FETCH_MODE=enabled` 和持久 Web Policy，任一条件缺失均不得访问网络。
- Safe Writeback v1 graph/hash 保持不变；ApplyApprovedPatch/CreateGitCommit 只能通过 trusted audit bridge 关联既有 Atomic Begin/Saga，不新增 File/Git Executor。

### 4. Validation & Error Matrix

| Condition | Required result |
|---|---|
| nil context、非法 Tool Request/Input/Output | 稳定 InvalidInput；Executor 调用数为 0 或不发布部分结果 |
| Definition/Run/Node/Attempt/lease 漂移 | `TOOL_CONTEXT_STALE`，不执行 Adapter |
| exact allowlist/Capability/Workflow binding 不满足 | 稳定拒绝并记录脱敏 `REFUSED` |
| 配置启用但 Executor、Workflow、Repository 或 Policy 缺失 | readiness fail closed，不构造 Fake |
| 成功 Call 无权威 replay receipt | 稳定 receipt unavailable，不重新执行网络或写入 |
| 副作用结果无法证明 | `UNKNOWN`/人工恢复，不自动成功或隐藏重试 |

### 5. Good / Base / Bad Cases

- Good：真实 River Node 从 strict Tool Request 解析稳定参数，经持久策略与精确版本校验，写入 Tool Call，再返回 `untrusted_data=true` 的受控摘要。
- Base：Tool Runtime disabled 时 API/Worker 可 ready，但 Tool capability 明确 unavailable；未配置外部 Provider 的测试明确 SKIP。
- Bad：把模型 reason、Search query、Diff 正文、Credential、路径或权限复制到 Node input/Tool Call，或用 Fake/空对象冒充未实现 Executor。

### 6. Tests Required

- 至少一条只读 Tool 必须由真实 River Workflow Node 完成 Agent ToolRequest -> Registry -> persisted policy -> Tool Call -> untrusted output；直接调用 ExecutionService 不是业务 smoke。
- `ResultReceiptLoader` 禁止网络、写入和副作用；Search replay 测试必须断言 Embedder/Search 调用数为 0。
- SSRF、command、path、output security 必须执行负测；Web Fetch 不访问真实互联网，使用受控 Resolver/Dialer/httptest 覆盖 rebinding/redirect/TLS/代理/解压后上限。
- 全量门禁包括定向 `-race -count=20`、真实 PostgreSQL/River/Filesystem/Git、M5 回归、`go test -race ./...`、`go vet ./...`、`make test`、`go mod tidy -diff`、Docker/Compose Tool smoke 和 `git diff --check`。
- M6-03 文档不得把 M6-04 Conversation/RAG API/SSE/前端或 M10 Auth/CSRF/通用 Audit/容量基线标记为完成。

### 7. Wrong vs Correct

```text
Wrong: 模型参数决定 Workspace/Capability/timeout/path，Executor 成功后才补写 STARTED，receipt 丢失时重新执行。
Correct: 服务端持久身份和冻结 catalog 决定策略，Executor 前写 STARTED；重放只读取权威 receipt，无法证明时进入 UNKNOWN。
```

## Scenario: M6-04 RAG Real Integration And Compose Smoke

### 1. Scope / Trigger

- 修改 Conversation RAG Workflow、Provider Adapter、Retrieval Evidence、Answer/SSE/Feedback 或 Compose 生产组装时，必须重跑本门禁。
- Smoke 只允许补没有公开写 API 的正式 Knowledge 资格；Conversation、Question、Answer、Workflow、Model Call、Event 和 Feedback 必须由真实产品链路创建。

### 2. Signatures

- `make rag-integration`：要求 `ZHIXU_TEST_DATABASE_URL`，运行公开 HTTP→River→RAG→SSE→Feedback 的真实 PostgreSQL 集成。
- `make compose-rag-smoke`：启动 disposable Compose stack 与 request-driven OpenAI-compatible fixture。
- Fixture 环境：`ZHIXU_RAG_FIXTURE_API_KEY` 必填；Worker 使用相同随机 `ZHIXU_CHAT_API_KEY` 发送精确 Bearer。

### 3. Contracts

- PLAN 与 REVIEW 的模型输入只在内存中增加服务端 `model_run_ref`；持久 Workflow Input 仍只保存稳定 ID/hash。`MaxStructuredInputBytes` 约束绑定后的完整模型输入，不承诺原始 JSON 可占满全部预算。
- Query Plan fixture 从请求 Schema 与输入生成结果，不依赖调用序号；只接受 PLAN v1、RAG Answer v2、Faithfulness v1 的精确三元组。
- Retrieval Search snippet 与重新打开的 Source Span excerpt 在 Agent Adapter 边界统一 `TrimSpace`；裁剪后为空必须 fail closed。
- 空 rewrite/degradation/citation 集合必须保持非 nil 空数组语义，禁止在 Adapter copy 时退化成 `null`。
- Compose 首次回答必须恰好持久化 `PLAN:SUCCEEDED,INITIAL:SUCCEEDED,REVIEW:SUCCEEDED`，Question exact replay 后 Model Call 投影不变。

### 4. Validation & Error Matrix

| Condition | Required result |
|---|---|
| Fixture Bearer 缺失、错误、大小写漂移或重复 Header | 401，不记录 Header/Token |
| Schema result type/id/version 非白名单组合 | 422，不生成近似响应 |
| Search/Open excerpt 只有空白 | Retrieval Adapter consistency error，不进入模型 |
| Answer/Refusal 空集合被扫描成 nil | Repository 回归测试失败；Service 不得返回 409 假损坏 |
| Seed 创建或改变 Conversation/Question/Answer/Workflow | Smoke 立即失败 |
| Question replay 新增 Model Call | Smoke 立即失败 |

### 5. Good / Base / Bad Cases

- Good：公开摄取→审批→重建索引，受限 harness 通过 Knowledge Repository 补资格，再经公共 Conversation API 和真实 Worker 发布带 Citation 的 Answer，SSE/Feedback 可重放。
- Base：未配置测试数据库时 `rag-integration` 明确失败；fixture 默认只允许 loopback，Compose 通过共享 network namespace 保持该边界。
- Bad：手写不经 Domain 的 Claim/Relation hash、直接 seed Answer/Workflow、按第几次调用返回模型结果、把 409/Refusal 当 smoke 成功。

### 6. Tests Required

- Agent/Conversation/Fixture focused race、Go vet、`go mod tidy -diff`、OpenAPI、前端 lint/typecheck/test/build。
- 真实 PostgreSQL `make rag-integration` 断言三次 Provider 调用、Citation/Topic/Follow-up、SSE、Feedback 与 exact replay。
- `make compose-rag-smoke` 断言生产 HTTP Adapter 的 Bearer、精确三阶段 Model Call、重放零新增、Citation 可打开、SSE 无正文/Secret、Feedback 不修改 Answer。
- 主 Agent 执行 Go/SQL/通用五轴审查，并由独立只读 reviewer 复验。

### 7. Wrong vs Correct

```text
Wrong: Compose 脚本直接 INSERT 假 hash 的正式知识，模型 fixture 按调用顺序返回，Question 重放只比较 Answer ID。
Correct: 受限 Go harness 复用 Knowledge Domain/Repository；fixture 按 Schema+输入生成；重放同时证明 Model Call 三阶段投影不变。

Wrong: 原样把 Markdown snippet/source excerpt 送入要求 canonical text 的 Agent Evidence。
Correct: Retrieval Adapter 边界裁剪首尾空白，裁剪后为空则明确失败。
```

## Scenario: M7-01 Graph Real Query Quality Gate

### 1. Scope / Trigger

- 修改 Graph Domain/Application/PostgreSQL/HTTP、生产 composition、fixture、前端 client/page 或公开契约时，
  必须运行本跨层门禁。
- M7-01 只交付 Topic/Claim canonical facts 的只读 Graph；门禁不得用 Fake Repository、直接 Handler 调用、
  readiness 或静态页面替代真实 PostgreSQL 和公共 HTTP。

### 2. Signatures

- 公共查询：`POST /api/v1/graph/global`、`POST /api/v1/graph/neighborhood`、
  `POST /api/v1/graph/path`、`GET /api/v1/graph/nodes`、
  `GET /api/v1/graph/nodes/{node_type}/{node_id}`、`GET /api/v1/graph/relations/{relation_id}`、
  `GET /api/v1/graph/relations/{relation_id}/evidence`。
- 真实门禁：`make graph-integration`、`make graph-smoke`、`make graph-benchmark`；三者都要求显式设置
  `ZHIXU_TEST_DATABASE_URL`，benchmark 可用 `ZHIXU_GRAPH_BENCHMARK_ARTIFACT_DIR` 指定产物目录。
- 前端门禁：`npm run lint --prefix web`、`npm run typecheck --prefix web`、
  `npm run test --prefix web`、`npm run build --prefix web`；浏览器实际访问 `/graph`。

### 3. Contracts

- `graph-integration` 必须通过生产 Router/Repository 贯穿 Global -> Local -> Path -> Evidence，并验证 cursor
  response-loss replay/stale、Workspace 防枚举和 timeout；它证明进程内公共契约，不替代真实进程 smoke。
- `graph-smoke` 必须启动真实 `cmd/api` 子进程、等待 readiness 后执行相同核心 HTTP 链路。成功删除临时状态；
  失败停止进程、尝试幂等清理 canonical fixture，并保留权限为 `0700` 的诊断目录及位置；清理失败继续保持
  非零结果。
- HTTP 响应不得包含数据库 URL、fixture/storage 绝对路径、managed storage 字段或未公开来源正文；公开
  Graph 契约要求的 Claim statement 与 Evidence reason 必须保留。API/fixture 进程日志还不得包含 Claim
  statement、Evidence reason 或 provenance 正文 canary。Smoke 失败控制输出只能额外打印用于运维恢复的
  私有 `0700` 诊断目录位置，不得打印 DSN、fixture 路径或正文；benchmark 的 summary/samples/EXPLAIN
  文件必须为 `0600`。
- 容量门禁固定 20k Active Topic/100k Confirmed IMPACTS Relation/100k Evidence 参考拓扑、单客户端一跳、
  5 次预热和 30 次采样；每个样本必须为 6 条数据库语句，p95 <= 1.5s，并保存
  Neighborhood/Path/Evidence 的索引计划。Mixed Topic/Claim 与 BELONGS_TO 正确性由
  `graph-integration`/`graph-smoke` 覆盖；claim-heavy/mixed 拓扑和 500,000 Relation/FPS 仍归 M10。
- 前端必须通过严格 Graph decoder、Workspace-scoped query key、URL round-trip、60 node/100 edge 画布上限、
  完整列表 fallback、Relation Evidence lazy load 和键盘/焦点测试；浏览器同时验证桌面 1440x900 与移动
  390x844，无横向溢出和控制台 warning/error。

### 4. Validation & Error Matrix

| Condition | Required result |
|---|---|
| 缺少测试数据库环境变量或数据库不可达 | 门禁明确非零退出，不回退 Fake/内存实现 |
| public integration 只通过 Handler/Repository 私有入口 | 不计为通过，必须经过 Router 和 HTTP wire |
| API 子进程未 ready、提前退出或请求失败 | smoke 失败、停止进程、清理 fixture、保留诊断目录 |
| 响应命中基础设施/未公开来源 canary，或日志命中 DSN、路径、Claim/Evidence/provenance canary | smoke 失败，不以“测试数据”名义豁免 |
| cursor stale/timeout 被显示为空结果 | 契约或浏览器验收失败，必须提供可操作错误状态 |
| 容量样本 SQL 数不是 6、P95 超阈值或索引缺失 | benchmark 非零退出 |
| 仅通过桌面或仅启动 dev server | 浏览器门禁未完成 |

### 5. Good / Base / Bad Cases

- Good：同一 canonical fixture 由真实 PostgreSQL 提供事实，integration 验证 wire/failure contract，真实进程
  smoke 验证 composition/清理/日志，capacity benchmark 独立验证性能和执行计划。
- Base：开发者只运行 focused 单元测试可作为中间反馈，但 T13/T14 归档前必须完成全部门禁；临时数据库未配置
  时应报告未验证，不能标记 PASS。
- Bad：用 mock fetch 截图声称真实页面完成、只检查 `/readyz` 声称 Graph smoke 完成、保留成功 smoke 临时目录，
  或把一次低延迟样本宣称为容量结论。

### 6. Tests Required

```bash
go test -race -count=1 ./...
go vet ./...
go mod tidy -diff
make test
make graph-integration
make graph-smoke
make graph-benchmark
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
python3 ./.trellis/scripts/task.py validate 07-20-graph-projection-queries
git diff --check
```

- 主 Agent 执行 Go、SQL 和通用质量审查；Graph 涉及公共 API、数据库查询计划、cursor 安全和前端，必须再由
  独立只读 reviewer 复验需求、逻辑、边界、质量、测试和真实运行结果。
- 500,000 Relation 的最终 P95/FPS、正式 Auth/Session/API Token/CSRF/Capability 仍归 M10；Workspace 隔离、
  loopback 或 HMAC cursor 不是认证。

### 7. Wrong vs Correct

```text
Wrong: integration、真实进程 smoke、容量 benchmark 三选一；页面能打开就跳过移动端、焦点和控制台检查。
Correct: 三类后端门禁分别验证 wire、composition 和容量；前端命令通过后仍在桌面/移动完成真实浏览器闭环。

Wrong: 将 20k/100k 一跳 P95 和 Workspace 隔离标记为 500k/FPS/Auth 已完成。
Correct: 只声明 M7-01 的参考容量与只读闭环，M10 边界保持显式待验收。
```

## Scenario: M7-03 Smart Collection / Knowledge Health Quality Gate

### 1. Scope / Trigger

- 修改 Collection AST/read model/cursor、Health Issue/detector/scan/schedule、migrations `00025`–`00029`、SMART_COLLECTION
  Candidate scan、OpenAPI、API/Worker composition 或 `/collections`/`/health` 时执行。

### 2. Signatures

- Collection public seams：`Validate`、`Preview`、`Results`、`Create/Update/Archive`、`PlanDurableScan`。
- Health public seams：`Summary/Issues`、`Start/GetScan`、`Decide/Repair`、`Get/PutSchedule`。
- SMART_COLLECTION scan payload 必须包含 Workspace、Collection ID/version、query hash、read-model revision 和 exact count。

### 3. Contracts

- Query AST 只接受 `collection-query/v1` 的 registry field/operator/value，最大深度 3、64 nodes、32 KiB、IN 100。
- LIST/TABLE/COMPACT_CARD 共享同一个 result page；Collection 不复制 Knowledge，Health/scan/decision 不直接写正式知识。
- Issue identity/fingerprint 语义、complete-only resolve、partial coverage、Decision CAS/idempotency、Schedule disabled-by-default
  和 affected-scope outbox 必须在 Domain + PostgreSQL 同时成立。
- API/Worker/Graph/Health 故障隔离；River 只投递。SSE 只触发 REST Query invalidation，不能成为事实源。

### 4. Validation & Error Matrix

| Failure | Required result |
|---|---|
| unknown/duplicate JSON、非法 cursor/UUID/enum/limit | stable 400 Problem，不能静默默认 |
| Workspace/resource mismatch | 404 anti-enumeration |
| Collection dependency/owner unavailable | 独立 unavailable/503，不返回假空集合 |
| partial/failed/cancelled Health scan | coverage 可见，不 resolve active Issue |
| same identity+fingerprint | 只更新 `last_verified_at` |
| fingerprint changed | 原 Issue ID + observation + `REOPENED` |

### 5. Good / Base / Bad Cases

- Good：真实 PostgreSQL/River/API/browser 贯穿 Collection → Health scan → Evidence/Decision，response-loss 可 exact replay。
- Base：Tag、Review/Directory、Artifact/Review Deck 和无真实 apply seam 的 repair 显式 unavailable。
- Bad：页面本地过滤三份结果、把 scan ID/Workflow event 当终态、或用 Workspace scope 替代 stale Collection binding。

### 6. Tests Required

- `go test -race ./...`、`go vet ./...`、`go mod tidy -diff`、`make test`、OpenAPI/migration/integration/fault/benchmark/browser/
  secret gates、前端 lint/typecheck/test/build、Trellis validate。
- 独立 backend/SQL 与 frontend/cross-layer reviewer 必须按 AC-01..AC-14 复验；P0–P2 全部关闭后才能归档。

### 7. Wrong vs Correct

```text
Wrong: 只测 handler Fake 或只启动 Vite 就声称 Collection/Health 已交付。
Correct: 以真实 PostgreSQL/Workflow/HTTP/browser 分层证据证明动态结果、持久 scan、错误隔离和移动端闭环。
```

## Scenario: M7-02 Semantic Link Cross-Layer Quality Gate

### 1. Scope / Trigger

- Candidate、typed Proposal、Approval apply、Topic scan、OpenAPI、production wiring 或 `/graph` Candidate UI 任一变化时执行。

### 2. Signatures

- 门禁：`semantic-link-integration`、`semantic-link-fault-smoke`、`semantic-link-eval`、`semantic-link-smoke`。
- 最终还必须执行全仓 Go race/vet/tidy、`make test`、迁移测试、OpenAPI、前端 lint/typecheck/test/build、
  Trellis validate、diff/secret/body scan。

### 3. Contracts

- integration 使用真实 PostgreSQL/River，证明 Candidate→独立 typed Proposal→Approval→一条 Relation。
- fault 覆盖 retry exhaustion、cancel、response-loss、stale→needs_revision 和事务回滚，不允许 Scan/Run 假一致。
- eval 固定五项指标和全部版本；Fake 结果只标记 deterministic pipeline。`ignored_unchanged` 样本必须先完整
  经过生产 discovery、规则分类和 Candidate fingerprint，再用数据集冻结的历史 fingerprint 投影抑制结果；
  禁止把 ignored 样本伪装成正式 Relation/active Proposal exclusion 而在 discovery 前删除。
- `semantic-link-smoke` 的 browser 前置步骤使用真实测试数据库；随后普通 Go race 子命令必须局部清空
  `ZHIXU_TEST_DATABASE_URL`，避免把未隔离的默认数据库测试误启用到共享质量库。真实数据库行为由 integration、
  fault 和 browser 目标分别负责。
- 正式 Graph readiness 与 Semantic Link readiness 独立，候选故障不能让七个查询端点失效。

### 4. Validation & Error Matrix

| Failure | Required result |
|---|---|
| Scan 成功但 Workflow failed，或反之 | 门禁失败并查根因，不标 flake 跳过 |
| Approval 已提交但返回错误 | exact replay 恢复同 Relation，不重复副作用 |
| Candidate dependency unavailable | 独立 503/status；Graph 仍 ready |
| eval 版本缺失或 Fake 冒充真实质量 | 门禁失败 |

### 5. Good / Base / Bad Cases

- Good：真实数据库、真实 River、公共 HTTP、浏览器和离线指标各自提供不同层证据。
- Base：Semantic/RAG unsupported 是显式 capability，不影响 Rule 信号和正式 Graph。
- Bad：只跑 mock/unit、把一次 flaky 重跑通过当关闭、或用 readiness 代替业务 smoke。

### 6. Tests Required

- 任务 `implement.md` Checkpoint C/D 全部命令；迁移空库/重复/Down-Up/guarded Down。
- 主 Agent 使用 `go-review`、`code-review-and-quality`、`sql-code-review`；公共 API/DB/Workflow/前端由独立 reviewer 复验。

### 7. Wrong vs Correct

```text
Wrong: fault smoke 偶发出现 Scan=SUCCEEDED/Run=failed，重跑一次通过后忽略。
Correct: 提高复现率、锁定持久时间精度根因、加入真实 PostgreSQL 回归，再连续运行原场景。
```

## Scenario: M9-03 Smart Collection Export Quality Gate

### 1. Scope / Trigger

- 修改 Export Domain/Application/PostgreSQL/LocalFS/River/HTTP/Auth/Audit/OpenAPI、Collection Export 前端或
  `deploy/export-browser-smoke.sh` 时，必须执行本门禁。
- 门禁只证明 Smart Collection `MARKDOWN|METADATA_JSON`。附件、`EVALUATION_JSON`、`AUDIT_JSON`、CSV/XLSX 和
  AC-33 全量验收仍在后续任务；测试通过不得扩大该产品声明。

### 2. Contracts

- 真实 PostgreSQL 是 Job/lease/prepared result/cleanup/download Audit 的事实源；River 只投递，不能用 River
  成功、readiness 或单元 Fake 代替 Create→Worker→Download 的闭环证据。
- 故障注入必须覆盖 staging 写入、Prepare、promote、Complete、租约接管、TTL 到期、文件删除和 Audit 提交。
  每个窗口都只能留下一个权威 result binding，或可解释的 `FAILED|EXPIRED|ManualRecovery`，不得重读可变
  Collection 后给出第二个成功结果。
- 安全测试必须验证 Workspace 隔离、Session/API Token/`READ_LOCAL`、默认 MASKED、Secret/绝对路径/危险公式前缀
  canary、symlink/hash/size 检查与安全下载 header。Audit 必须记录 actor 和服务端准备返回的 outcome，而不声称
  浏览器已收完字节。
- 浏览器验证真实 API/Worker/Vite 的 Collection 面板：刷新恢复、同 key response-loss、`PENDING/RUNNING` 2 秒
  有界轮询、SSE invalidation、成功下载、`FAILED/EXPIRED` 新建、桌面/390x844 键盘、无横向溢出和 console warning/error。

### 3. Required Commands

```bash
go test -race -count=1 -timeout 60s ./internal/export/... ./internal/events/... ./internal/audit/... ./internal/auth/http ./internal/app ./cmd/api ./cmd/worker
ZHIXU_TEST_DATABASE_URL="$ZHIXU_TEST_DATABASE_URL" go test -race -tags=integration -count=3 -p 1 -timeout 60s ./internal/export/adapter/postgres
ZHIXU_TEST_DATABASE_URL="$ZHIXU_TEST_DATABASE_URL" go test -race -tags=integration -count=3 -p 1 -timeout 60s -run '^(TestExport|TestM9)' ./internal/platform/migration
make openapi-check
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
ZHIXU_TEST_DATABASE_URL="$ZHIXU_TEST_DATABASE_URL" bash deploy/export-browser-smoke.sh
git diff --check
```

- Export 涉及公共 API、PostgreSQL、文件、认证、Audit、Worker 和前端，完成代码改动后必须执行 Go、SQL、通用
  review，并由独立只读 reviewer 复验需求范围、崩溃恢复、安全边界和实际门禁结果。

### 4. Wrong vs Correct

```text
Wrong: 只验证 Create 返回 202，或只用 mock fetch 截图就宣称 Export/AC-33 完成。
Correct: 从真实 PostgreSQL/API/Worker/LocalFS/Audit/浏览器证明可恢复结果与受控下载；AC-33 仍明确为部分完成。
```

## Scenario: M8 Review, Interview And Memory Quality Gate

### 1. Scope / Trigger

- 修改 `internal/review/**`、`internal/review/interview/**`、`internal/review/learningpath/**`、`internal/memory/**`、
  `internal/platform/scheduler/**`、迁移 `00035`、`00038`、`00044`–`00060`、相关 HTTP/OpenAPI/Composition、Artifact
  visibility hold、Worker expiry 或 M8 前端时应用。`00056` 保留 Interview provenance shell guard，`00057` 追加 Completion
  reservation，`00059` 强化 Memory 完整性，`00060` 建立 Review/Interview 共享 Path。
- 该门禁覆盖 Review FSRS、共享 Learning Path 与 Interview/Memory 三条事实链；它们可以读取受控的正式知识和评分结果，
  但不得复用 Answer/Schedule、创建第二套 Path owner、把 Memory 变成 Evidence，或接受浏览器可伪造的 provenance。

### 2. Signatures

- Review：`StartSession(REVIEW, deck_id)`、`SubmitAnswer(session_id, card_id, question_ref, answer, rating, idempotency_key)`、
  `CompleteSession(session_id, idempotency_key)`；Session、Deck、Card、question_ref 必须属于同一 Workspace。
- Interview：`Start`、`SubmitTurn`、`Complete`、Learning Path status/step command；Question/Turn/Report/Path 使用独立
  receipt 和 version，Interview Turn 不写 `learning.review_answer` 或 FSRS。
- Review Path：`CreateForReview(workspace_id,review_answer_id,idempotency_key)`、`GetForReviewAnswer`、
  `UpdateStatus(expected_version,status,key)`、`UpdateStep(step_id,expected_version,status,key)`；客户端不能提交 gap、Score、
  Evidence 或 Artifact binding，所有输入从冻结 Answer 与正式 Citation 读取。Step 的 `PENDING` 只允许出现在读取投影；更新
  target 只能是 `IN_PROGRESS|COMPLETED|SKIPPED`。
- Memory：响应可只读返回服务端 provenance；用户 HTTP Create/Edit/Confirm/Pause/Resume/Delete 均不接受 source/owner/
  confirmed_by，也不能修改既有 provenance。服务端 Interview Candidate 只接收已持久化的 session/path/step identity、
  Workspace 与客户端 Idempotency-Key，并固定 `INTERVIEW` provenance。

### 3. Contracts

- Review Scorer 输入只来自已验证 Card、答案和受控上下文；评分、Evidence 子集校验、Answer、Schedule、receipt 在一个
  PostgreSQL 事务中完成。`00052` 要求 Claim 离开 `CONFIRMED`（包括 `DISPUTED`）或 legacy evidence 不可验证时，Card
  与 Schedule 一起收敛到失效状态；due 查询的 legacy UUID 转换必须 fail closed。每条 Answer 必须冻结非空、无首尾空白、
  不超过 128 bytes 的 Scorer version；`00055` 将旧数据标记为 `legacy/unknown`，有 Answer 时禁止 Down 丢失该审计字段。
- due 查询必须携带活动且 Deck-bound 的 Review Session；`question_ref` 使用 API-only HMAC key 绑定
  Workspace/Session/Deck/Card fingerprint 与 Card/Schedule version。显式配置必须为至少
  32 个 canonical bytes；`required` 模式缺省时从 Bootstrap Token 域隔离派生，local `disabled` 缺省时才使用进程随机 key。
  多实例必须共享显式 key；key 轮换或 local 进程重启后未提交题目刷新 due，已持久 Answer 仍按 receipt exact replay。
- `00049_review_invalidation_observability.sql` 只将已发生的 Card invalidation 投影到既有 `REVIEW_INVALIDATED` Health
  Issue 和既有 Timeline outbox；它既不拥有 Card/Schedule 生命周期，也不实现完整 Review Health/Impact 或下游影响分析。
- Review lifecycle invalidation 每次最多 200 张 Card，以单条批量 UPDATE 写入并只返回 count/has_more 摘要；
  `has_more=true` 时用新 Idempotency-Key 继续，禁止无界锁、N+1 或完整 Card receipt。
- `00058` 使用 transition table 将一条 Card 语句的 invalidation Health 更新收敛为一次集合化 batch helper 调用；
  不得按 Card 或 Claim 循环扫描 INVALIDATED Card。
- Source Version/Span 失效只能通过 `review_card_evidence_selector` 的 Workspace-bound btree seek 取得下一批；
  多个 selector 必须按 Card 级 AND 组合，禁止误收紧为同一 Evidence item；Source+Claim 必须使用带 `claim_id` 的
  索引与静态计划，不能先扫描高扇出 Source 再过滤稀疏 Claim；也禁止在持有 Workspace 写锁时展开所有 APPROVED Card
  的 evidence JSON。
- `00056` 要求 root Interview Question 的 `interview-evidence/v2` 恰有八个字段，并通过安全 UUID、Claim Source、active Index、
  Source Manifest、canonical Chunk/Manifest Chunk 和 provenance 绑定校验；follow-up 必须原样冻结 parent Claim/Evidence。Interview
  child 与 Review Answer 写入先 `FOR UPDATE` 锁同一 `review_session` parent，parent 改型再反查 child，保证双向竞态最多一方提交。
  已有 Interview/Question 或 Review Answer 时，Down 必须以 SQLSTATE `55000` 拒绝移除这些 guard。
- Interview Completion 使用 Begin/Prepare/Complete reservation，不建立跨模块长事务。Begin 在 Session 行锁下冻结 snapshot，
  PENDING reservation 阻止 Submit；Prepare 冻结完整 Artifact digest。Artifact 创建事务写入 PLAN receipt 与带 digest 的 hidden
  hold，数据库 fence 以 `FOR UPDATE` 核对 reservation=PENDING、digest、Artifact type/role 和精确 stage key。Complete 最终
  事务再次核对并原子写 Report/Path/receipt，只 release 对应两个 hold。Worker 以 24 小时、有界批次将超时 reservation 转为
  ABANDONED、ACTIVE hold 转为继续隐藏的 ORPHANED；本范围不物理删除恢复/审计资产。
- `00059` 要求 INTERVIEW Candidate 以结构化 session/path/step FK 绑定真实 Interview-origin Path，stable owner 对同一步骤最多一条；
  Memory Audit 与 aggregate owner/status/version 一致且每个 version 唯一，非法生命周期动作和无进展 keepalive 在数据库边界拒绝。
- `00060` 将 Path/Step 升格为 `learning.learning_path(_step)`，Interview relation 只作为可更新兼容视图；Review origin 唯一绑定
  不可变 Review Answer，Path/Step 历史字段不可改删，只能通过 version CAS 推进允许状态。Review 创建使用 Answer-scoped
  reservation、`LEARNING_PATH_CREATE` hidden hold、digest 与 PLAN receipt fence。
- Review Path 的生产 Composition 在数据库依赖可用时必须注入真实 Repository、Artifact bridge、Service 与 Handler；System Status
  只有在 Review 和 Learning Path Handler 均可用时才能报告 `review: ready`。Worker 必须在启动和周期路径调用
  Application-owned 的 24 小时有界维护策略，不能由入口复制 TTL/批次规则。当前实现已完成这些接线，并统一 `00060` 与
  Repository 的 snapshot/digest、Path/Step、command/receipt、completed Artifact tuple；Artifact digest 必须冻结 source
  snapshot、完整规范化 Draft（Scope、正文、Citation）与 renderer 元数据，并与 ABANDONED 重开、Prepare、Complete 一起绑定
  `attempt_no`，确保代码版本变化、旧 attempt 的延迟请求和 ORPHANED hold 不能污染新尝试。静态门禁通过不等于
  真实 PostgreSQL 事务、并发和 API/Worker 端到端已验证。
  已持久化且非空的 legacy v1 digest 只有在由同一冻结 snapshot 精确重算匹配时才能恢复原 attempt；空 digest 和
  ABANDONED 重开不得降级生成 v1。
- Memory effective context 只返回 ACTIVE、confirmed、unexpired、Workspace/owner/task-scope 匹配项，显式标记为个人
  上下文，绝不充当 Citation/Evidence。当前唯一生产消费者是 Interview memory loader；通用 Agent/Conversation/RAG 没有接线。
  用户 HTTP 固定 `USER/user:manual`，不能伪造 AGENT/INTERVIEW source。
- Interview Candidate 使用双层幂等：`learning.memory_command` 先将 Workspace + 客户端 key 绑定完整 Candidate 请求，
  再以 stable owner + `INTERVIEW` source_ref 收敛同一步骤的语义 identity。相同 key 换步骤/内容必须在副作用前冲突；
  不同 key 的等价步骤请求复用同一 Candidate，只新增各自 receipt，不新增 Candidate/Audit。公开 `replayed=true/200`
  同时包含 exact key replay 与该语义复用，OpenAPI 和 UI 不得只解释为同 key 重放。

### 4. Validation & Error Matrix

| 条件 | 必须结果 |
|---|---|
| Review Session 为 INTERVIEW、无 Deck，或 Card/question_ref 不绑定该 Session | stable invalid/conflict；不评分、不写 Answer/Schedule |
| key 轮换、不同 key 实例或 local 随机 key 重启后提交旧 `question_ref` | fail closed 并要求刷新 due；已落库 Answer 的同 key 重试仍 exact replay |
| Scorer/Evidence 不可用、同 key 不同请求或并发重试 | fail closed 或 exact replay；绝不第二次推进 FSRS |
| Scorer version 为空、带首尾空白、超过 128 bytes，或有 Answer 时回滚 `00055` | CHECK 或 SQLSTATE `55000` 拒绝；不得写入或删除评分实现身份 |
| Claim DISPUTED、legacy evidence 畸形或来源不可用 | Card INVALIDATED、Schedule 不存在；due 查询继续返回安全页 |
| Interview evidence 为 scalar/多余字段/非法 UUID/漂移 tuple，或 shell/child 并发改型 | CHECK `23514` 或并发一方失败；不落 Question/Answer、不形成跨类型历史 |
| Completion reservation PENDING、snapshot/digest/Artifact/stage key 漂移 | Submit 或 Complete stable conflict；不得给任意 hold 事后贴 digest、不得公开 Draft |
| Completion 超过 24 小时 | Worker 有界转 ABANDONED/ORPHANED；晚到 Artifact Create 被数据库 fence 拒绝 |
| Interview Candidate 同 key 换步骤/内容 | 写入前稳定 conflict；不得创建 Candidate、Audit 或第二 provenance |
| Interview Candidate 不同 key 请求同一 provenance/内容 | 返回同一 Memory ID 且 `replayed=true`；每个 key 有 receipt，只有一条 Candidate/Audit |
| 用户 Memory 命令传 source/owner/confirmed_by，或 Candidate 未确认/暂停/到期 | 请求拒绝，或 effective context 返回空；不泄漏为 Citation |
| INTERVIEW Memory 的结构化 provenance、source_ref、Path origin 或 Audit aggregate/version 漂移 | CHECK/FK/trigger fail closed；不生成悬空 Candidate 或伪造 Audit |
| Review Path Handler 的 service 未装配 | 返回依赖不可用，不把路由存在或前端页面存在解释为业务成功 |
| Review Path Repository 与 `00060` 的列、command enum、expected_version、snapshot/digest 或 Artifact tuple 不一致 | 真实 PostgreSQL 门禁失败；不得通过 mock/静态 build 宣称持久化闭环 |
| 通用 Agent/RAG 尝试读取 Memory，但没有显式 loader/composition | 该能力保持未交付；不得从 Interview 接线外推为全局上下文能力 |

### 5. Good / Base / Bad Cases

- Good：Review 与 Interview 各自保存 receipt 和状态机；Memory 由受控 writer 生成 Candidate、用户 Confirm 后才进入
  Interview context；共享 Path 的 origin/source binding 明确，且只有真实 Service/Repository/maintenance 全部接线后才暴露
  Review Path capability；`00049` 只作为可观察兼容投影。
- Base：Scorer、Artifact 或 Memory 依赖不可用时返回明确 Problem，原有事实不前进，页面通过 REST/SSE 重新读取。
- Bad：把 Interview Turn 送入 Review Answer、因 due SQL 的旧 JSON cast 失败整页 500、浏览器指定 INTERVIEW provenance，
  把 Interview-only Memory loader 宣称为通用 Agent 注入、以 nil-service 路由/前端代码冒充 Review Path 已交付，或把
  Health/Timeline 投影宣称为完整 Impact。

### 6. Tests Required

- Domain/Application：Review-only Session binding、FSRS exact replay/CAS、`DISPUTED`/legacy quarantine、Interview turn
  顺序/complete replay、共享 Path origin/source/actionable gap/transition、Memory provenance 和 Interview-scoped effective filter。
- PostgreSQL：Answer/Score/Schedule 同事务、`00049` 投影与 `00052` guarded migration、安全 UUID due、Interview Artifact
  hidden hold/release/failure replay、`00053` visibility migration、`00054` Interview provenance unique index、Memory
  双层幂等的 exact-key/semantic replay、并发每-key receipt、单 Candidate/Audit、expiry/Workspace 隔离；`00055` 覆盖
  Up/repeated Up、legacy `legacy/unknown` 回填、Scorer version 约束、空数据 Down→Up 与有 Answer guarded Down；同时覆盖
  `00035` legacy receipt/Answer hardening、`00038` COMPLETE_SESSION receipt；`00056` 覆盖 malformed evidence、root/follow-up
  provenance、Interview/Review shell 双向两连接竞态与有业务数据 guarded Down；`00057` 覆盖 legacy hold→ORPHANED、NULL digest
  仅凭匹配 PLAN receipt 安全归一化、role/artifact mutation、reservation exact replay/异 key 并发、maintenance 与 late Artifact
  Create 竞态、24h ABANDONED→ORPHANED 及 forward/guarded Down。
- `00059/00060`：结构化 Interview Memory provenance/FK/每步唯一 Candidate、Audit aggregate/version/action matrix、completion/path
  reservation no-keepalive、共享基表/兼容视图、origin shape、Review Answer/Artifact 唯一 binding、Path/Step retained history、
  Review command/reservation/hold、真实 Repository SQL 列/枚举/必填字段与 guarded Down。
- HTTP/OpenAPI/Browser：Capability/CSRF/Origin、严格 decoder、脱敏 due、Interview/Path 恢复、用户 source 字段拒绝、
  Review Answer→Path 创建/恢复/状态与步骤命令、`review.*`/`learning_path.*`/`interview.*`/`memory.*` SSE invalidation；
  Composition 测试必须证明 Handler 持有真实 Service，Worker/maintenance 调用可达。最终门禁前还需独立 Go、SQL、通用审查和
  真实 PG/API/Worker/Vite 证据。

### 7. Wrong vs Correct

```text
Wrong: 先创建可见 Interview Artifact，或在 Complete 时给任意 NULL hold 事后贴当前 digest。
Correct: Artifact PLAN/hold 创建事务绑定 reservation digest；数据库 fence 串行化 timeout maintenance，Complete 只 release 精确 hold。

Wrong: 因 00049 已生成 Health/Timeline 行，就认为 Review Health/Impact 已完成。
Correct: 把它记录为既有 owner 的最小兼容投影；完整 downstream impact 继续由专门能力拥有。

Wrong: 丢弃浏览器 key，只用 step-derived key；或把 `replayed=true` 一律描述为同 key 精确重放。
Correct: 客户端 key 绑定完整请求，INTERVIEW provenance 绑定语义 Candidate；两种复用都返回既有 ID 并被契约明确区分。

Wrong: 看到 Review Path 的 Handler、Web 页面和 Store 文件存在，就把 API 与 24h maintenance 标成已交付。
Correct: 先证明 `00060` 与 Repository SQL 一致、Composition 注入真实 Service/Artifact bridge、maintenance 有生产调用，再登记运行能力。
```
