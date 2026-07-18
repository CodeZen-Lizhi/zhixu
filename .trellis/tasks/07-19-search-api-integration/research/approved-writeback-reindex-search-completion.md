# Research: Approved Proposal 到 Active Search 与 Completion 闭环

## Planning Resolution

主任务已确认 Completion 与 Activation 继续保持同事务原子可见，M6-D 只在提交后通过 HTTP 读取该 Active。
Endpoint 沿用架构文档 `/api/v1/search`；Cursor 使用 HMAC + request/index/full-result fingerprint 的有界
top-100 重算方案；Evidence 通过 Workspace/SourceVersion/Artifact/Projection/Span 全绑定读取不可变 excerpt。
正式 Session/Token/Capability 仍由 M10 交付。

- Query: 核对 Approved Proposal → Safe Writeback → River Reindex → Active Index Search → Completion 的现有事实、可复用夹具、HTTP/Evidence/权限/分页缺口与建议验收。
- Scope: internal
- Date: 2026-07-19

## Findings

### Files Found

- `internal/changecontrol/application/approval_dispatch_river_smoke_integration_test.go`：HTTP Approval → 双 Workflow Worker → Safe Writeback → verifying/index_pending。
- `internal/changecontrol/application/reindex_river_fault_smoke_integration_test.go`：Dispatcher → 双 Reindex Worker → Capture/Ingestion/Snapshot/Vector/Regression/Ready/Completion response-loss → Active/Completed → Application Hybrid Search。
- `internal/retrieval/adapter/postgres/completion.go`：Activation、Delivery、Execution、Proposal 的单 PostgreSQL 事务完成归约。
- `internal/retrieval/domain/search.go`：M6-C 已冻结 SearchRequest、EvidenceV1、degradation 与边界上限。
- `internal/retrieval/application/search.go`：Active-only Keyword/Semantic/Hybrid、RRF、dedup、rerank/fallback。
- `internal/retrieval/adapter/postgres/search.go`：Active Index 与 Source/SourceVersion/Span/Chunk provenance 查询。
- `docs/architecture/retrieval-architecture.md`：M6-B/C 已验证闭环与 Evidence 边界。
- `docs/product/PRD.md`：Search cursor、可打开引用、SourceVersion/Span 的产品契约。

### Existing Closed-Loop Evidence

现有测试已经比任务标题描述更接近完整领域闭环：

1. 真实 HTTP Approval 入口：Handler 创建 approved decision 与唯一 Workflow dispatch（`internal/changecontrol/application/approval_dispatch_river_smoke_integration_test.go:126`-`:129`）。
2. 两个真实 Workflow River Worker 竞争执行 Safe Writeback，最终 Run succeeded，而 Proposal/Execution 保持 verifying（`:173`-`:229`）。
3. Approval exact replay 不创建第二套 Workflow/Execution/Commit/Outbox（`:236`-`:254`）。
4. 同一测试随后调用 `runReindexRiverFaultSmoke`，不是独立 seed 出另一条链（`:265`）。
5. Reindex smoke 使用 committed Git blob，故意让工作树漂移，证明捕获不读取漂移正文（`internal/changecontrol/application/reindex_river_fault_smoke_integration_test.go:60`-`:69`、`:586`-`:599`）。
6. Dispatcher 创建唯一 Delivery/River Job，两个 Reindex Worker 执行（`:156`-`:199`）。
7. 四个 checkpoint、Vector commit、Ready、Completion response-loss 都被同 dispatch 重投恢复（`:210`-`:228`）。
8. 最终断言一个 Activation、一个 Active、Delivery succeeded、Execution/Proposal completed、V2 vector/cache 和最小 Hybrid Search（`:550`-`:599`、`:645`-`:700`）。
9. River payload、Delivery/Attempt、logs 均扫描 Credential、DSN、Embedding Key、正文与路径 canary（`:457`-`:489`）。

因此 M6-D 不需要重建领域闭环；应把同一事实穿过真实 Search HTTP/OpenAPI/Evidence 打开边界，并补足可复用 testkit 与 Compose black-box smoke。

### Critical Ordering Clarification

`CompleteReindexTx` 在一个数据库事务内完成：

1. Activate target Index（`internal/retrieval/adapter/postgres/completion.go:111`-`:123`）。
2. Delivery/Attempt succeeded（`:124`-`:140`）。
3. Writeback Execution completed（`:142`-`:149`）。
4. Proposal completed（`:151`-`:159`）。
5. commit 后才对其他事务可见（`:160`）。

所以不能在外部可见状态下严格按“Active Index Search，然后再 Completion”做时间序列验收；Active 与 Completion 是同一原子提交。正确闭环证明应是：

```text
Approved → Safe Writeback → Reindex gates →
atomic(Activation + Delivery/Execution/Proposal completed) →
Search API reads exactly that Active Index
```

若任务文案要求 Search 必须发生在 Proposal completed 之前，会直接破坏现有原子完成不变量，应在 PRD 规划阶段修正，不应修改 Completion 事务。

### Evidence Contract: Existing Facts And Gap

M6-C `EvidenceV1` 已包含：Workspace、IndexVersion、EmbeddingVersion、Chunk、ParseProjection、Sequence、ContentHash、HeadingPath、Span 行/byte 范围、Snippet、多个 Source/SourceVersion provenance、阶段分数与 rerank model version（`internal/retrieval/domain/search.go:284`-`:306`）。

但“可打开引用”尚未完成：

- `EvidenceProvenance` 只有 `source_id`、`source_version_id`、`relative_path`、`captured_at`（`:114`-`:120`），没有稳定 API locator。
- 现有 OpenAPI 只有 `POST /source-versions/{id}/ingestion-attempts`，没有读取 SourceVersion、Span 或 immutable artifact 的 GET endpoint。
- `internal/retrieval/http` 不存在；`cmd/api`/router 没有 Retrieval Handler。

建议 HTTP DTO 为每个 provenance 增加由 Handler 生成的 `open_url`，Domain 不保存 URL：

```text
/api/v1/workspaces/{workspace_id}/source-versions/{source_version_id}/spans/{span_id}
```

打开 endpoint 必须重新验证：Workspace、SourceVersion、SourceVersionProjection、ParseProjection、Span、ContentArtifact 全绑定；从 immutable managed artifact 读取 `[start_byte,end_byte)`，并返回 1-based line range、content hash、relative path、captured_at。不得直接打开工作树相对路径，也不得只凭 `span_id` 越过 Workspace/SourceVersion 权限边界。

共享 Chunk 有多个 provenance 时，每个 SourceVersion 都应得到自己的 `open_url`；Span 可以相同，因为多个 SourceVersion 可合法共享同一 Parse Projection/Content Artifact。

### Search HTTP Boundary Recommendation

当前架构文档写 `/api/v1/search`，但领域请求天然绑定 Workspace。为减少 body 覆盖与权限歧义，建议优先：

```text
POST /api/v1/workspaces/{workspace_id}/search
```

Request body 只包含 `query`、`mode`、`filters`、`cursor?`、`limit`；不再接受第二个 `workspace_id`。Handler 只做：strict JSON decode、路径 ID → Domain request、authorizer、cursor codec、Application 调用、DTO/Problem 映射；不得复制 RRF/filter/degradation 规则。

错误映射可沿用现有 Handler 模式：

- invalid request/cursor → 400。
- workspace/active index/evidence not found → 404。
- permission denied → 403。
- cursor 与新 Active Index/request hash 冲突、Search consistency damage → 409。
- semantic unavailable / retryable provider failure → 503；Hybrid 的允许降级仍是 200 + explicit degradations。

### Permission Gap

仓库目前没有 Session/API Token/principal middleware 或 Search authorizer。现有 Handler 只会把下层 `PermissionDenied` 映射为 403，但没有入口生成调用者授权上下文。OpenAPI 也没有 `securitySchemes`。

因此 M6-D 可以验证的最小安全边界只有：

- Workspace ID 来自 path，不可被 body 覆盖。
- Repository 必须继续 Active-only + Workspace-scoped。
- Evidence open endpoint 必须验证 Workspace/SourceVersion/Span 全绑定。

如果 Acceptance Criteria 中“权限”指真实用户身份/Token scope，当前任务必须明确扩 scope 到 Auth，或者新增 fail-closed `SearchAuthorizer` Port 并由后续 Auth composition 实现；不能用 localhost、Workspace UUID 或“单用户应用”冒充已完成权限控制。建议 PRD 把本任务表述为“workspace authorization seam + deny/allow contract tests”，真实 Session/Token 仍归 Auth milestone。

### Pagination Gap And Safe Cursor Shape

当前 Search 是 bounded top-K：`limit` 为 `1..100`，Application 直接截断 items（`internal/retrieval/domain/search.go:15`-`:22`、`internal/retrieval/application/search.go:472`-`:476`）。没有 cursor、`has_more`、next cursor，也没有稳定翻页输入。

产品 PRD 明确 Search cursor 可选且公共 API 不使用 offset/page（`docs/product/PRD.md:3483`、`:3503`-`:3512`）。M6-D 不能只加一个 HTTP `cursor` 字段而不改变 Application 语义。

建议 opaque cursor v1 至少绑定：

- schema version；
- workspace ID；
- immutable index version ID；
- canonical query/mode/filter hash；
- 最后一个结果的 final rank tuple + chunk ID；
- 可选 expiry/checksum，防止客户端篡改。

翻页时必须锁定 cursor 中的 Index Version；若 Workspace 当前 Active 已切换，返回稳定 cursor conflict 并提示从第一页重启，不能静默把两代 Index 拼在同一页。Hybrid/Rerank 的最终顺序还需要确定性保证；仓库当前没有生产 Reranker，因此可先验证 nil-reranker 的 RRF 顺序，但未来 nondeterministic reranker 不应使用无持久 query snapshot 的 cursor。

如果 M6-D 不准备扩展 Application/Store 支持 after tuple，PRD 应明确 Search v1 是 bounded top-K、`next_cursor=null`，并把真实 cursor 移出本任务；否则当前任务的“分页边界”仍未完成。

### Recommended End-To-End Assertions

在现有 `TestApprovalDispatchRealRiverSafeWritebackSmoke` 基础上新增 HTTP 层断言，或用共享 fixture 新建 M6-D integration：

1. Approval `201`，replay `200`，唯一 Workflow/Execution/Commit/Outbox。
2. Reindex 全 fault matrix 后只有一个 Delivery terminal、一个 Activation、一个 Active。
3. Proposal/Execution completed 与 Activation 在同一提交后可见。
4. Search HTTP 返回的 `index_version_id` 等于 Delivery 的 target/Activation target。
5. Keyword/Hybrid mode、有效 limit、零命中、非法 mode/path/time、limit 0/101、未知 Workspace、无 Active、semantic unavailable 的 HTTP status/Problem 正确。
6. Source/SourceVersion/path/time filters 在 HTTP decode 后仍与 Repository 两路等集；body 不可覆盖 path Workspace。
7. Evidence 每个 provenance URL 可打开，返回 byte/line range 与 content hash 一致；工作树 drift 不影响打开结果。
8. 若实现 cursor：第一页/第二页无重复无遗漏、同 cursor replay 稳定、请求 hash 不同拒绝、Active 切换后旧 cursor 冲突。
9. 敏感正文、DSN、API Key、host path 不进入 Problem、日志、River payload、cursor。
10. OpenAPI schema 与真实 Handler response 做 round-trip/contract test，不只检查 path 存在。

### Reusable Testkit Recommendation

建议将现有 smoke 的“环境搭建”和“fault policy”分开：

```text
internal/integrationtest/m6fixture/
  database.go          # disposable migrated DB
  git_workspace.go     # committed temp workspace + drift helper
  approval.go          # proposal/approval seed and IDs
  reindex.go           # production component composition + stable facts

internal/changecontrol/application/
  approval_dispatch_river_smoke_integration_test.go  # fault scenario owner

internal/retrieval/http/
  handler_integration_test.go                         # HTTP/Search/Evidence owner
```

testkit 只输出 IDs、URLs、fact snapshots 和 cleanup；不把私有 SQL、Credential 或正文复制到生产 package。Fault wrappers 仍留在原 smoke test，避免普通 API integration 默认跑七轮 response-loss。

### Impact Files

- `.trellis/tasks/07-19-search-api-integration/prd.md`：先补齐权限定义、cursor 是否 in-scope、原子 Completion 顺序与验收。
- 建议新增 `.trellis/tasks/07-19-search-api-integration/design.md`、`implement.md`：当前复杂任务缺失技术设计和执行计划。
- `internal/retrieval/http/**`：Search Handler、Evidence open Handler、DTO、cursor codec、error mapping 与 tests。
- `internal/retrieval/domain/search.go`：仅当真实 cursor/after tuple 进入领域契约时修改；`open_url` 不应进入 Domain。
- `internal/retrieval/application/search.go`：cursor/after tuple 或固定 index search 需要 Application 支持。
- `internal/retrieval/adapter/postgres/search.go`：分页 tuple、固定 Index Version 与 Evidence artifact read query。
- `internal/app/router.go`、`cmd/api/main.go`：Retrieval composition。
- `internal/platform/models/**` 或建议的共享 composition package：API/Worker 共用 Query Embedder factory。
- `api/openapi/openapi.json`、`api/openapi/check.mjs`：Search、Evidence、cursor、Problem、security seam。
- `internal/changecontrol/application/approval_dispatch_river_smoke_integration_test.go`、`reindex_river_fault_smoke_integration_test.go`：复用 fixture 并增加 HTTP response binding，保留 fault coverage。
- 建议新增 `internal/integrationtest/**`：共享 PostgreSQL/Git/M6 fixture。
- `Makefile`、`deploy/compose.yml`、`.env.example`、`README.md` 与架构测试/部署文档。

### Related Specs

- `.trellis/spec/backend/database-guidelines.md`：Active-only Search、稳定 cursor、参数化 filter、Completion 原子不变量。
- `.trellis/spec/backend/error-handling.md`：Problem Details、PermissionDenied、DependencyUnavailable 与 retryable mapping。
- `.trellis/spec/backend/logging-guidelines.md`：query/snippet/credential/DSN 不进入日志与 metrics。
- `.trellis/spec/backend/directory-structure.md`：HTTP/Application/Adapter/Composition Root 依赖方向。
- `.trellis/spec/guides/cross-layer-thinking-guide.md`：Evidence round-trip、API/DB 边界与共享 decoder/contract。
- `docs/architecture/api-and-events.md`：Search 输入输出、cursor、Problem 与安全目标。
- `docs/architecture/retrieval-architecture.md`：Evidence、Active-only、Hybrid 与 Completion 契约。
- `.trellis/tasks/07-17-retrieval-indexing-search/{prd.md,design.md,implement.md}`：父任务的 M6-D 验收和 stop gate。

## Caveats / Not Found

- 当前子任务 `prd.md` 的 Requirements 和 Acceptance Criteria 仍是 TBD，且没有 `design.md` / `implement.md`；权限与 cursor 的真实范围尚未决策。
- 未发现认证 principal/session/token middleware，因此不能把真实权限验收写成已具备事实。
- 未发现 Evidence 打开 endpoint 或 immutable artifact read API。
- 未发现 Search cursor 实现；当前只有 bounded top-K。
- 现有 closed-loop Search 是 Application 直接调用，不是 `cmd/api` 真实 HTTP/Compose 边界。
