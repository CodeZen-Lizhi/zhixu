# Research: M5-05 Architecture and Test Contract

- Query: 从 `docs/architecture`、已归档 M3-M6 任务和现有测试基础设施提取 M5-05 Knowledge Domain 的模块边界、状态机、测试、性能、部署与回滚契约。
- Scope: internal
- Date: 2026-07-19

## Findings

### 1. 结论与最小交付边界

M5-05 的最小独立交付应是一个不依赖 HTTP、River、模型 SDK 或 Graph UI 的 `internal/knowledge` 深模块：

```text
internal/knowledge/domain
  Topic / Claim / EvidenceReference / Relation / Conflict
  状态机、规范化、兼容矩阵、对称去重、稳定错误

internal/knowledge/application
  同步 Command/Query service
  Evidence reachability、跨对象 Workspace 绑定、事务编排

internal/knowledge/adapter/postgres
  参数化 Repository、乐观锁、事务、数据库错误分类

migrations/00017_knowledge_domain.sql
  core.* 知识事实表、约束、索引、Down guard
```

依据：

- Knowledge Context 明确拥有 Topic、Claim、Relation、Conflict、Provenance，Graph/Health 只消费这些事实形成视图和健康能力（`docs/architecture/domain-model.md:29-40`）。
- Knowledge Module 对外接口是 ConfirmClaim、Suggest/ConfirmRelation、Open/ResolveConflict；适用条件、关系不变量、冲突状态和 Provenance 校验应隐藏在模块内部（`docs/architecture/module-architecture.md:113-129`）。
- 依赖方向要求 Presentation → Application，领域模块不能依赖 HTTP、pgx/sqlc、模型 SDK，模块之间只经公开 Interface 协作（`docs/architecture/module-architecture.md:282-300`；`.trellis/spec/backend/directory-structure.md:50-58`）。
- 当前仓库不存在 `internal/knowledge`、`internal/agent`、`internal/graph` 或 `internal/collection`；M5-05 是这些后续模块的事实边界，而不是同时创建它们。

建议将 M5-05 按以下顺序实现，但仍可作为一个任务验收：

1. Domain contract：纯 Go 类型、状态机、兼容矩阵、规范化和单元测试。
2. Persistence contract：`00017` 迁移、Repository、真实 PostgreSQL 约束/并发测试。
3. Application/read seam：Evidence 可达性、事务命令、供 M6/M7 使用的有界查询接口。

不要在本任务新增公共 HTTP 路由、OpenAPI、Workflow Definition/Executor、Graph Projection、Smart Collection、Agent Prompt/Schema、AI Eval 或 Relation Proposal UI。

### 2. 领域对象与唯一事实源

#### Topic / Claim

- Topic、Claim 是 PostgreSQL 持久事实；确认历史不能仅靠重新抽取恢复（`docs/architecture/data-architecture.md:81-95`）。
- Claim 必须至少绑定一个 Source Span 或已批准 Claim；无来源模型内容只能保持建议，不能成为 RAG 正式证据（`docs/product/PRD.md:265-269`）。
- Claim 的架构状态为 Suggested → Confirmed/Invalid；Confirmed 可进入 Disputed、Superseded、Deprecated（`docs/architecture/domain-model.md:282-294`）。
- `normalized_statement` 不能被简单用作 Workspace 内全局唯一键：产品允许不同 applicability、来源和历史 Claim 共存，重复判断本身也是业务能力。应使用显式 fingerprint/idempotency 约束，而不是把语义重复误压成数据库唯一冲突。

#### Evidence / Provenance

- Source Span 是 Ingestion 拥有的不可变位置，字段已包含 Workspace、Content Artifact、Parse Projection、行/byte 范围和 excerpt hash（`internal/ingestion/domain/model.go:155-170`）。
- Claim Source 与 Relation Evidence 必须额外保存 `source_version_id`，从多个导入路径中选择具体 Provenance（`docs/product/PRD.md:3238-3244`）。
- 现有 Retrieval `EvidenceV1` 含 Index Version、检索分数和瞬时降级，是 Search wire/result 契约（`internal/retrieval/domain/search.go:284-318`），不能被 Knowledge 持久模型直接复用。
- Knowledge 应持久化稳定 Evidence identity：至少 `workspace_id + source_version_id + source_span_id`，并通过固定查询/端口证明 Source Version 映射到 Span 所属 Parse Projection。不得复制 snippet、当前工作树路径、FTS/vector score 或 Index Version。
- 现有 Evidence Reference Service 已证明 SourceVersion→ContentArtifact→ParseProjection→Span 的完整绑定和不可变 Artifact hash/range（`internal/retrieval/application/evidence_reference.go:19-30,88-123`）；M5-05 可复用其验证思想或以小 Port 适配，但不应让 Knowledge Domain import Retrieval HTTP/result 类型。

#### Relation

必须区分两套概念，避免“关系第二事实源”：

- 关系分析结果：`NEW | COMPLEMENTARY | DUPLICATE | CONFLICT | LOW_CONFIDENCE`（`docs/product/PRD.md:1158-1205`）。
- 持久 RelationType：`CITES | DERIVED_FROM | BELONGS_TO | SUPPORTS | COMPLEMENTS | DUPLICATES | CONFLICTS_WITH | PREREQUISITE_OF | VERSION_OF | IMPACTS`（`docs/architecture/domain-model.md:245-258`）。

`NEW` 和 `LOW_CONFIDENCE` 不是 Relation 行；`COMPLEMENTARY/DUPLICATE/CONFLICT` 可映射为 `COMPLEMENTS/DUPLICATES/CONFLICTS_WITH` 候选。M6-02 才拥有模型结构化输出和五分类推理，M5-05 只冻结可验证的领域输入、输出枚举和落库规则。

Relation 必须校验：

- 端点存在、同一 Workspace、生命周期允许。
- relation type 与端点类型兼容，禁止自环。
- Confirmed Relation 有 Evidence 和确认方式（`docs/architecture/domain-model.md:94-109`）。
- `DUPLICATES`、`CONFLICTS_WITH` 使用稳定 ID 排序，反向输入落为同一 canonical pair（`docs/architecture/database-design.md:278-286`）。
- 多态端点类型必须来自代码注册白名单，不能把 type 拼入 SQL 标识符（`docs/architecture/database-design.md:282-288`）。

首个切片建议只启用已真实落地且由 Knowledge 拥有的 Topic/Claim 端点，以及明确的 Claim→Topic/Claim→Claim 兼容矩阵。Source、Document、Conflict、Artifact 等 Graph 节点可在其领域对象和跨模块查询契约真实落地后扩展，不能先填空壳类型。

数据库设计同时出现 `topic_claim` 和通用 `BELONGS_TO` Relation（`docs/architecture/database-design.md:29-35`；`docs/architecture/domain-model.md:247-258`）。M5-05 必须在 design 阶段选一个唯一事实源；建议使用 Relation 表表达正式归属，若为查询性能建立 `topic_claim`，它只能是由 Relation 派生的投影，不能接受独立写入。

#### Conflict

- Conflict 是 2..N Claims 的独立聚合，普通 Claim 覆盖不能让它静默消失（`docs/architecture/domain-model.md:94-109`）。
- 状态机：Open → Investigating → ResolutionProposed → Resolved/AcceptedDivergence；Investigating 可 Deferred，Deferred 可恢复调查（`docs/architecture/domain-model.md:296-307`）。
- 相同 Claim 不同 applicability 不能直接判冲突；条件不同可进入 Accepted Divergence（`docs/architecture/domain-model.md:368-376`）。
- Conflict 解决必须保留双方来源和调查历史；下游只生成影响报告/Proposal，不自动级联重写（`docs/architecture/workflows/09-conflict-resolution.md:41-77`）。

当前文档没有定义 applicability 的字段 schema 或“相近条件”的确定性算法。安全的 M5-05 合同应是：

- applicability 使用版本化、canonical JSON/value object，生成稳定 hash；
- 完全相同条件可由领域规则判断冲突；
- 非完全相同条件不得自动断言相同或不同，交给 M6-02 分析并以 LOW_CONFIDENCE/人工确认收口；
- Accepted Divergence 必须是显式 resolution，不由 JSON 不相等自动推断。

### 3. API 与 Workflow 边界

M5-05 不需要公共 HTTP API。现有 API 规范只列 `/graph`、`/collections`、`/health` 等后续资源，没有独立 `/knowledge` 已冻结契约（`docs/architecture/api-and-events.md:16-35`）。Graph/RAG/Collection 应在后续任务通过 Knowledge Application 的小接口读取事实。

若未来新增 Knowledge 写命令，仍必须遵守：写命令使用 Idempotency-Key、修改使用 Version/ETag，长任务返回 202 + Workflow Run ID（`docs/architecture/api-and-events.md:7-14,53-65`）。这些 wire 规则不构成在 M5-05 提前设计路由的理由。

M5-05 的 Application 命令应是短事务：

- Suggest/Confirm Claim。
- Suggest/Confirm/Reject/MarkStale Relation。
- Open/Transition/Resolve Conflict。
- 查询 confirmed claims、relations、conflicts 和 Evidence refs 的有界批量接口。

关系分析、模型调用、Citation/Faithfulness、Schema Repair 和拒答属于 M6-02；Agent 架构明确由 Workflow Node 调 Agent，Agent 从 Retrieval 得 Evidence，再经 Structured Output 和 Validator（`docs/architecture/agent-rag-architecture.md:7-20,67-95`）。

M6-02 注册 Workflow Node 时必须遵守现有 Runtime 边界：服务端冻结 Definition/Executor Registry，Job Args 只携带 node identity/schema，不持久正文或模型文本；River 不是业务事实源（`docs/architecture/workflow-engine.md:7-11,62-70,100-117`）。M5-05 不应注册占位 Node 或 Definition。

现有 Change Control Proposal 是 `TargetPath + Markdown Content` 专用模型（`internal/changecontrol/domain/model.go:45-80`），不能被 M5-05 直接复用为 Relation Proposal。正式关系确认需要 Proposal/Approval 时，应由 M7-02 单独扩展 typed change-set/Relation Proposal 契约；本任务最多保存不可变 confirmation method/reference，不得声称已有 Relation Approval 闭环。

### 4. 后续依赖

```text
M5-05 Knowledge Domain
  ├─ M6-02 Agent：读取 confirmed Claim/Conflict/Evidence；输出五分类、引用校验、拒答
  ├─ M7-01 Graph：把 Relation/Conflict 投影为分页邻居/路径；Evidence 延迟加载
  ├─ M7-03 Collection/Health：查询事实，不复制 Claim/Relation
  └─ M8-02 Review：只绑定有效 Confirmed Claim

M7-02 Semantic Candidate
  依赖 M7-01 + M5-03，但还需要 typed Relation Proposal 契约，不能直接套用当前文件 Proposal
```

Graph 必须是投影：全局图分页、不全量渲染，局部默认深度 1，Evidence 延迟加载，路径不存在不能伪造（`docs/architecture/workflows/06-graph-and-semantic-links.md:34-63`）。这些查询、布局缓存、候选 fingerprint 属于 M7，不应进入 Knowledge Repository。

### 5. 持久化与迁移契约

建议沿用现有 `core` Schema，因为数据库设计把 Workspace、Source、Document、Knowledge 放在 core（`docs/architecture/database-design.md:7-18`），模块代码仍放 `internal/knowledge/adapter/postgres`。

最小表集：

- `core.topic`
- `core.claim`
- `core.claim_source`
- `core.relation`
- `core.relation_evidence`
- `core.conflict`
- `core.conflict_member`

基础数据库门禁：

- 所有表带 `workspace_id`、稳定 ID、状态 CHECK、必要的 version/时间字段。
- SourceVersion/Span Evidence 使用 FK + trigger/固定查询证明 Workspace 和 Parse Projection 绑定。
- 对称 Relation 保存 canonical endpoint pair，并有唯一约束。
- Conflict Member 至少两个不同 Claim；数据库可约束唯一成员，成员数量和状态迁移由事务服务校验。
- 所有查询显式列、参数化；多态 type 只能进入白名单分支，不能动态拼表名（`.trellis/spec/backend/database-guidelines.md:27-36`）。
- 迁移采用 Expand/forward 策略，默认不依赖破坏性 Down 发布回滚（`.trellis/spec/backend/database-guidelines.md:38-45`）。
- Down 仅在没有 Topic/Claim/Relation/Conflict 业务数据时允许；有确认历史时返回 SQLSTATE `55000`，因为 Confirmed Relation 不能从向量重建（`docs/architecture/data-architecture.md:91-94`）。

新迁移将成为 `00017`。迁移嵌入无需手动注册，`migrations/embed.go:6-9` 已 `//go:embed *.sql`；但现有迁移测试按“最新是 00016”硬编码逐步 Down（`internal/platform/migration/runner_integration_test.go:63-86`；`internal/platform/migration/runner_state_machine_integration_test.go:71-95`），实现时必须先插入 00017 的 empty Down 步骤并新增有业务数据时的 55000 guard 测试。

### 6. 测试合同与现有 Fixture 模式

仓库当前没有 `testdata/`、`internal/testkit/` 或 `eval/` 文件。已归档 M3-04 的统一 Fixture 目标仍未落地；现有测试使用包内 helper、`t.TempDir()`、固定 UUID/Clock 和 `ZHIXU_TEST_DATABASE_URL`。

可复用模式：

- Ingestion Repository 集成测试在现有迁移数据库上开启事务并 rollback，包内 seed Workspace/Artifact/SourceVersion（`internal/ingestion/adapter/postgres/repository_integration_test.go:190-237`）。
- Retrieval 集成测试为每个测试创建独立数据库、运行完整迁移、结束强制删除，隔离性更强（`internal/retrieval/adapter/postgres/repository_integration_test.go:520-580`）。
- Migration 集成测试要求 `ZHIXU_TEST_DATABASE_URL` 并创建独立临时数据库（`internal/platform/migration/runner_integration_test.go:682-726`）。
- 固定 Fixture Workspace 和 Fake Model 是未来 E2E/AI Eval 目标，不应在 M5-05 为尚未实现的 Agent 造假（`docs/architecture/testing-and-evaluation.md:221-225`）。

M5-05 必须覆盖：

#### Domain unit

- Topic/Claim 文本规范化、UTF-8/长度/空白和稳定 hash。
- Claim 全状态合法/非法迁移；Confirmed 必须有可达 Evidence，模型建议无来源不得 Confirmed。
- Relation type × endpoint type 兼容矩阵、禁止自环、跨 Workspace、生命周期状态。
- `DUPLICATES/CONFLICTS_WITH` 正反输入 canonicalization 和重复去重。
- Confirmed Relation 必须有 Evidence + confirmation method；Rejected/Stale/Deprecated 边界。
- 五分类 outcome 与 persisted RelationType 映射；NEW/LOW 不生成 Relation。
- Applicability canonicalization；相同条件 conflict、不同/不确定条件不自动冲突。
- Conflict 至少两个不同 Claim、成员/Workspace 绑定、完整状态机、Accepted Divergence 显式性。

#### Application

- Evidence resolver success/not-found/cross-workspace/binding damaged。
- Repository 返回损坏对象时 fail closed 为 ConsistencyViolation。
- optimistic version conflict、相同命令重放、不同绑定冲突。
- create Relation+Evidence、open Conflict+Members 必须在同一事务全有或全无。
- 批量读取接口有上限、稳定排序、无 N+1。

#### PostgreSQL integration

- 空库 Up、重复 Up、空数据 Down→Up。
- 有任一 Knowledge 业务数据时 Down SQLSTATE `55000`。
- FK/CHECK/唯一键、跨 Workspace、Span→SourceVersion→Projection binding。
- 对称 Relation 并发双插入最多一条事实；反向输入不能产生第二条。
- stale expected_version 被拒绝；状态和 version 同步递增。
- transaction rollback/response-loss replay 不留下 Relation 无 Evidence 或 Conflict 少成员。

#### Regression

- Ingestion SourceSpan/SourceVersion contract 保持通过。
- Retrieval Search/Evidence 不因新表或 join 扩展改变当前 M6-D wire；Topic/Claim/Conflict 字段在真正接线前仍不能返回空壳（`docs/architecture/retrieval-architecture.md:158-173`）。
- `go test -race` 和关键纯领域包 `-count=20`，避免 map 顺序、canonicalization 或并发唯一性不稳定。

建议验证命令：

```bash
go test -race -count=20 ./internal/knowledge/domain ./internal/knowledge/application
go test -race ./internal/knowledge/...
ZHIXU_TEST_DATABASE_URL='postgres://...' go test -race -tags=integration -p 1 \
  ./internal/knowledge/adapter/postgres ./internal/platform/migration
go test -race ./internal/ingestion/... ./internal/retrieval/domain ./internal/retrieval/application
go vet ./...
make test
go mod tidy -diff
docker compose -f deploy/compose.yml --env-file .env.example config --quiet
docker compose -f deploy/compose.yml --env-file .env.example up -d --build --wait
curl -fsS http://127.0.0.1:8080/readyz
docker compose -f deploy/compose.yml --env-file .env.example down -v
git diff --check
```

`Makefile` 当前只有 `test`、Go/Web/Eino/OpenAPI/Compose 等基础 target（`Makefile:3-55`），没有父计划中设想的 `verify`、`migrate-test` 或 `integration-test`。M5-05 应使用上面的显式命令；不要顺手扩建全仓 Makefile，除非任务 PRD 明确要求。

### 7. 性能合同

M5-05 不承担 M7 Graph 或 M10 50 万容量指标，但必须避免把后续任务锁进不可扩展查询：

- Repository 提供批量 Evidence/Claim/Relation 读取，不逐关系查 Evidence。
- 邻居/列表接口必须稳定排序、有 limit/cursor seam；Graph 实际 cursor/path 查询留 M7。
- 对称 pair、Workspace+status、relation source/target、conflict member、claim evidence 应建立支持后续查询的 B-tree 索引。
- 禁止 N+1 Relation Evidence 和逐节点图谱查询；架构性能规范明确要求 Relation 候选批量、Graph 一跳有界、Evidence 延迟加载（`docs/architecture/performance.md:61-73,83-90`）。
- 不在 M5-05 建图数据库、图布局缓存、全局聚类、ANN 或 50 万基线；图数据库只有 PostgreSQL 多跳成为真实瓶颈后评估（`docs/architecture/performance.md:145-166`）。

### 8. 部署、备份与回滚

- 本任务只新增 Go 包和 PostgreSQL migration，无新进程、端口、环境变量或外部服务。
- Compose 启动顺序已固定为 PostgreSQL → migrate → API/Worker readiness（`docs/architecture/deployment.md:107-114`）；部署 smoke 的重点是 `00017` 可由 `/app/zhixu-migrate` 成功执行，现有 API/Worker 仍 ready。
- 当前 Auth/Session/Token/CSRF/Capability 归 M10，在完成前 API 必须保持 loopback（`docs/architecture/deployment.md:95-105`）。归档 Workflow 文档中把认证误指向 M5-05 的文字不能作为本任务扩权依据。
- Topic/Claim 确认历史和 Confirmed Relation 必须纳入 PostgreSQL 备份；它们不能仅靠文件、向量或重新抽取完整恢复（`docs/architecture/data-architecture.md:81-95`）。
- 发布回滚使用兼容当前 Schema 的旧应用；数据库迁移默认 forward/expand-contract，不以破坏性 Down 作为普通回滚（`docs/architecture/deployment.md:206-227`）。
- 若 00017 已有业务数据，Down 必须拒绝；回滚时保留表和数据、停用新代码路径，不能删除确认历史。

### 9. 不应提前实现的范围

- M6-02：ChatModel、Prompt、Structured Output、Schema Repair、模型版本、Citation/Faithfulness、RAG refusal/conflict 展示和 Eval。
- M7-01：Graph Projection、分页邻居、路径、布局、Evidence lazy-load API/UI。
- M7-02：Semantic Candidate fingerprint、ignore/re-evaluate、Relation Proposal 与 Change Control 泛化。
- M7-03/M7-04：Collection Query AST、Health Issue、Knowledge Event、Timeline、Impact Analysis。
- M8：Artifact/Review/FSRS/Interview/Memory。
- M10：认证、容量 P95、备份/恢复产品化、正式 Audit。
- 公共 OpenAPI/前端客户端、SSE event store、Graph/Collection 页面。
- 用向量相似度或模型置信度直接创建 Confirmed Relation；架构明确禁止（`docs/architecture/domain-model.md:390-396`；`docs/architecture/data-architecture.md:260-266`）。

### 10. Files Found

- `docs/architecture/domain-model.md` — Knowledge 聚合、状态机、关系类型、命令/事件和边缘规则。
- `docs/architecture/module-architecture.md` — Knowledge/Graph/Workflow/Agent 模块职责与禁止依赖。
- `docs/architecture/database-design.md` — Topic/Claim/Relation/Evidence/Conflict 表意图、多态引用和对称去重。
- `docs/architecture/testing-and-evaluation.md` — 测试金字塔、Relation/RAG/Graph Eval 与现有 M3-M6 集成门禁。
- `docs/architecture/deployment.md` — Compose/migrate/readiness、loopback、安全升级与回滚。
- `docs/architecture/performance.md` — Batch、N+1、Graph 有界查询和扩容触发。
- `docs/architecture/workflows/06-graph-and-semantic-links.md` — M7 Graph 投影和候选边界。
- `docs/architecture/workflows/09-conflict-resolution.md` — Conflict 调查、条件化解决和下游边界。
- `docs/product/PRD.md` — 五分类、正式关系/证据字段、Graph 节点和最终 AC。
- `.trellis/tasks/archive/2026-07/07-16-m3-workspace/*` — 首个 domain/application/adapter/HTTP 垂直切片和验证方式。
- `.trellis/tasks/archive/2026-07/07-16-m4-workflow/*` — Workflow 事实源、异步/副作用边界。
- `.trellis/tasks/archive/2026-07/07-17-m5-ingestion-parser/*` — SourceSpan/Chunk/Evidence 上游契约。
- `.trellis/tasks/archive/2026-07/07-19-search-api-integration/*` — M6-D Evidence wire、真实 PG/River/Compose 测试和明确 Out of Scope。
- `internal/ingestion/domain/model.go` — SourceSpan/CanonicalChunk 当前 owner 类型。
- `internal/retrieval/domain/search.go` — Search EvidenceV1 当前契约，不能作为 Knowledge 持久类型。
- `internal/retrieval/application/evidence_reference.go` — 可打开 Evidence 完整绑定验证模式。
- `internal/platform/migration/runner_integration_test.go` — 独立数据库迁移 fixture 和硬编码 Down 顺序。
- `Makefile` — 当前实际质量与 Compose targets。

### 11. External References / Versions

本次未使用外部网页资料；版本事实来自仓库 manifest：

- Go `1.25.4`（`go.mod:3`）。
- pgx `v5.10.0`（`go.mod:7`）。
- pgvector-go `v0.4.0`（`go.mod:8-9`）。
- Goose `v3.27.0`（`go.mod:10`）。
- River/riverpgxv5 `v0.40.0`（`go.mod:11-13`）。

M5-05 不需要新增第三方依赖。

### 12. Related Specs

- `.trellis/spec/backend/index.md` — 后端开发前检查、模块/Workflow/写入边界和质量门禁。
- `.trellis/spec/backend/directory-structure.md` — `internal/knowledge` 目标位置、依赖方向和禁止模式。
- `.trellis/spec/backend/database-guidelines.md` — 参数化 SQL、事务、约束、迁移和 Relation Confirm 数据库责任。
- `.trellis/spec/backend/error-handling.md` — Foundation Error 与稳定错误分类。
- `.trellis/spec/backend/quality-guidelines.md` — unit/integration/race/Review/部署 smoke 门禁。
- `.trellis/spec/guides/cross-layer-thinking-guide.md` — 跨 Domain/Application/DB/API 数据流检查。

## Caveats / Not Found

- 当前任务 `prd.md` 的 Requirements 和 Acceptance Criteria 仍为 TBD；在进入实现前必须把上述边界、状态机和门禁转写为可勾选 AC，并补齐 `design.md`、`implement.md`。
- Applicability 缺少正式字段 schema、规范化版本和“相近条件”算法；不关闭该问题就不能安全实现自动 conflict classification。
- 文档同时存在 `topic_claim` 与 `BELONGS_TO` Relation 两种表达；必须明确一个写入事实源，避免双写漂移。
- 当前 Change Control 只支持文件 TargetPath Proposal，没有 typed Relation Proposal；Relation 的用户确认闭环不能在 M5-05 宣称完成。
- 没有统一 `testdata/`、`internal/testkit/`、`eval/`；本任务可先使用包内 deterministic fixture，但不要把它包装成已完成 M3-04/M11 Gold Set。
- Backend spec 部分段落仍保留“当前没有 Go 代码/manifest”的早期描述，与实际仓库不一致；实现应以当前代码、go.mod 和归档任务为事实，后续通过 spec 更新单独修正。
- 未运行测试或 Compose；本文提供的是基于当前仓库事实的实施验证合同，不是本轮运行结果。
