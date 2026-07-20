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
