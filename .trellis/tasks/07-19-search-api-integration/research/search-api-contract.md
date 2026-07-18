# Research: M6-D Search API 与集成契约

## Planning Resolution

本研究完成后，主任务已补齐 PRD/Design/Implement，并裁决：沿用文档事实 `/api/v1/search`；
Cursor 纳入 M6-D，但必须以进程内随机 HMAC、规范请求、Active Index 与完整 top-100 结果指纹绑定，
结果漂移 fail closed；Evidence Span 从不可变 Content Artifact 校验并读取有界 excerpt。正式 Auth 仍归 M10，
M6-D 只声明 Workspace 数据隔离与 loopback 部署边界，不声称完成身份认证。

- Query: 研究现有 HTTP/OpenAPI/Problem Details/前端客户端边界，为 M6-D 给出可复用入口、推荐 endpoint/request/response/error/status/权限契约、影响文件、兼容性风险与验收命令。
- Scope: mixed
- Date: 2026-07-19

## Findings

### 1. 结论摘要

1. Retrieval 的领域、Application 和 PostgreSQL Search 能力已经完成；M6-D 应复用 `domain.SearchRequest/SearchResult/EvidenceV1`、`application.SearchService` 和 `postgres.SearchRepository`，不要在 Handler、OpenAPI 或前端重新实现模式降级、RRF、去重、过滤或 Evidence 绑定。
2. HTTP 边界尚不存在。推荐新增 `POST /api/v1/search`，请求体使用 `workspace_id + query + retrieval_mode + filters + limit`。这是只读 Query，不要求 `Idempotency-Key`；使用 POST 是因为 query 最长 8 KiB 且过滤数组较多，不适合 URL Query，也避免默认请求路径日志记录检索正文。
3. M6-D v1 推荐只提供有界 Top-N，不提供 cursor：领域请求当前只有 `limit`，Application 最终排序还可能依赖瞬时 Rerank，无法诚实保证跨请求的稳定游标。`limit` 默认 20、最大 100；响应不得声称 `total` 或 `next_cursor`。后续增加 cursor 时必须冻结 Index Version、规范请求指纹和最终排序配置，否则激活切换或 Rerank 漂移会产生重复/漏项。
4. Evidence “可打开”不能只返回相对路径。推荐为每个 provenance 返回服务端生成的 `citation_url`，指向新增的 `GET /api/v1/source-versions/{source_version_id}/spans/{span_id}`；该端点必须校验 SourceVersion/ParseProjection/Span/Workspace 绑定，并从不可变 Content Artifact 按 byte range 读取或返回有界 excerpt，绝不能直接打开当前 worktree 路径。
5. 现有 `Problem` 是项目自有“Problem Details 风格”结构，不是 RFC 9457：它使用 `application/json`，且没有 `type/title/status/detail/instance`。M6-D 应保持现有 `error_code/message/retryable/workflow_run_id/details` 兼容契约；RFC 9457 迁移应作为全 API 的独立版本化任务，不要只让 Search 改成 `application/problem+json`。
6. 认证/权限是关键前置缺口：ADR 已要求 Cookie Session/API Token 和 `Read Workspace` Capability，但仓库没有认证中间件、401 错误分类或 OpenAPI `securitySchemes`。M6-D 可以冻结“Search 与 citation 都要求 workspace-scoped Read Workspace”的接口契约，但若验收要求真实未授权拒绝，必须把认证基础设施明确纳入依赖任务；不能用 nil Authorizer 或 localhost 作为身份 fallback。
7. 架构要求 Generated Client 只存在于前端 API 边缘，但当前前端全部是手写 `fetch` + `unknown` runtime decoder，依赖中没有 generator。M6-D 若引入生成客户端，需要先选择并锁定 generator/runtime validator，并把生成产物 drift check 加入 CI；否则应继续沿用手写边界且明确这是临时兼容路径，不能声称“generated client 已完成”。

### 2. 可复用入口与代码模式

#### Retrieval 事实边界

- `internal/retrieval/domain/search.go:14-27`：现有硬上限——query 8 KiB、返回 100、每类过滤 100、provenance 8、snippet 4 KiB、rerank text 16 KiB。
- `internal/retrieval/domain/search.go:29-58`：`keyword|semantic|hybrid`、共享 Filter 和 `SearchRequest` 已冻结；HTTP DTO 只需显式映射，不应新增第二套领域模式。
- `internal/retrieval/domain/search.go:105-151`：Span、Provenance 和候选阶段分数定义。
- `internal/retrieval/domain/search.go:242-281`：Query degradation 只允许 `vector`、`rerank`，包含稳定 code/retryable，并有规范顺序。
- `internal/retrieval/domain/search.go:284-318`：`EvidenceV1` 与 `SearchResult` 是 M6-D 直接映射的核心契约。
- `internal/retrieval/domain/search.go:320-403`：结果校验已锁定模式矩阵；Keyword 不降级，Semantic 必须完整 vector，Hybrid 可显式降级到 Keyword。
- `internal/retrieval/application/search.go:20-83`：`SearchStore`、`QueryEmbedder`、`SearchIndex` 与 `SearchService` 的注入 seam 已存在。
- `internal/retrieval/application/search.go:86-218`：Search 编排已经负责 Active-only、双路并行、Hybrid fallback、RRF/dedup/rerank；Handler 不能复制这段逻辑。
- `internal/retrieval/application/search.go:618-645`：Semantic 不可用是稳定 `RETRIEVAL_SEMANTIC_UNAVAILABLE`，Hybrid fallback 返回成功结果及 degradation。
- `internal/retrieval/adapter/postgres/search.go:25-45`：`SearchRepository` 是独立只读 Store，可直接在 API composition root 构造。
- `internal/retrieval/adapter/postgres/search.go:47-79`：仅加载 Workspace 当前 Active Index；无 Active 时稳定码为 `RETRIEVAL_ACTIVE_SEARCH_INDEX_NOT_FOUND`。
- `internal/retrieval/adapter/postgres/search.go:82-155`：Lexical/Vector 已使用参数化 SQL、固定 trigram threshold、固定 distance operator 和统一 Filter。
- `internal/retrieval/adapter/postgres/search_integration_test.go:21-263`：真实 PostgreSQL 已覆盖 Active-only、Source/SourceVersion/path/time Filter、bounded provenance、FTS/trigram index EXPLAIN。
- `internal/retrieval/adapter/postgres/search_integration_test.go:282-347`：三种 vector operator 和 exact scan EXPLAIN 已覆盖，且明确禁止把当前基线误报为 ANN。

#### HTTP、错误与路由模式

- `internal/httpapi/response.go:11-46`：共享 `Problem`、JSON 写入和 1 MiB/unknown-field/单 JSON 值解码器；新 Handler 应直接复用。
- `internal/app/router.go:33-47`：模块 Handler 由 `app.Dependencies` 注入。
- `internal/app/router.go:61-113`：Chi `/api/v1` route group、全局 NotFound/MethodNotAllowed、静态资源 fallback 已存在。
- `internal/app/router.go:199-208`：`X-Request-ID` 已由 middleware 生成或透传，Search 不需要在 body 重复返回 request ID。
- `internal/workspace/http/handler.go:18-37`：HTTP Handler 只依赖最小 Application Service interface，并通过 `Routes` 注册。
- `internal/workspace/http/handler.go:175-200`：既有错误映射为 400/404/409/403/503/500；Search 应抽取或复用统一映射，避免第五份 switch。
- `internal/foundation/error.go:5-18`：领域错误分类没有 `Unauthenticated`，只有 `PermissionDenied`；这是 401 无法准确表达的现有缺口。
- `internal/app/router_test.go:89-105`：未知 API 和方法错误已有全局 Problem contract test。

#### API composition root 与模型 Adapter

- `cmd/api/main.go:75-159`：API 进程目前只组装 Workspace/Workflow/ChangeControl/Ingestion，没有 Retrieval Handler、Search Repository、Search Service 或 Query Embedder。
- `cmd/worker/main.go:420-459`：Worker 已有 Embedding Version 注册和 Vector Builder 组装模式，可复用相同配置到 API Query Embedder，但 API 不应重复注册/改写版本事实。
- `cmd/worker/main.go:502-525`：`newConfiguredEmbedder` 已封装 OpenAI-Compatible/Ollama/disabled 构造；应提取为共享 composition helper 或等价复用，不能在 `cmd/api` 复制第三套 provider switch。
- `internal/platform/config/config.go:131-148`：Embedding 与 RRF 配置已是跨进程 Config 字段。
- `internal/platform/config/config.go:608-635`：Provider、endpoint、key/model/dimensions 的 fail-closed 校验已经存在。

#### Evidence 打开所需的不可变内容入口

- `internal/ingestion/domain/model.go:155-188`：SourceSpan 绑定 `WorkspaceID + ContentArtifactID + ParseProjectionID + byte/line range`；CanonicalChunk 绑定 SourceSpan。
- `internal/ingestion/domain/repository.go:39-68`：ProjectionResult 已含 spans/chunks，但 Repository 没有按 SourceVersion+Span 的公开读取端口。
- `internal/ingestion/adapter/workspace/reader.go:28-65`：现有 Reader 已证明正确模式是先读可信 SourceMaterial，再安全读取不可变 Artifact；不得从 provenance relative path 读 worktree。
- `internal/platform/filesystem/content_store.go:189-266`：`ReadArtifactLimited` 会验证 managed location/hash/size、常规文件、symlink/替换和实际 hash；citation excerpt 应复用这一安全读取边界。
- `internal/workspace/application/service.go:160-223`：Workspace Scan 已把原文件捕获到不可变 Artifact，并持久化 SourceVersion/ContentArtifact 身份。

#### OpenAPI 与前端客户端

- `api/openapi/openapi.json`：当前为 OpenAPI 3.1.0，已有 16 个 paths、26 个 schemas，但没有 `/api/v1/search`、citation path 或 `securitySchemes`。
- `api/openapi/openapi.json` 的 `Problem` schema：只要求 `error_code/message/retryable`，`additionalProperties=false`；Search 错误必须使用同一 schema，避免局部漂移。
- `api/openapi/check.mjs:8-68`：当前检查只验证必要 operation/response/schema 是否存在，不做 schema-to-handler drift、breaking diff 或客户端生成 drift。
- `Makefile:40-44`、`.github/workflows/ci.yml`：CI 已执行 `openapi-check` 与 `make test`，可作为新增 drift/生成检查的入口。
- `web/src/api/system-status.ts:31-145`、`web/src/api/workspace.ts:57-167`：前端现行模式是将 network JSON 视为 `unknown`，在 API 边界手写校验并映射为 Domain UI Model。
- `web/src/api/system-status.test.ts:5-32`、`web/src/api/workspace.test.ts:24-62`：Decoder test 会拒绝未知枚举、缺失字段和数量不一致。
- `web/package.json:10-35`：没有 OpenAPI generator、generated fetch client 或 runtime validation dependency。
- `docs/architecture/api-and-events.md:207-212` 与 `docs/architecture/frontend-architecture.md:20-30`：目标架构要求 OpenAPI 生成与 Generated/Typed Client 只位于前端边缘，领域/Feature 不依赖 wire DTO。

#### 已有完整闭环 smoke

- `internal/changecontrol/application/approval_dispatch_river_smoke_integration_test.go:43`：已有 Approved Proposal → Safe Writeback → Reindex River 的真实入口测试。
- `internal/changecontrol/application/reindex_river_fault_smoke_integration_test.go:560-599`：已断言 Delivery、Execution、Proposal completed、唯一 Active、Artifact 与漂移 worktree 隔离。
- `internal/changecontrol/application/reindex_river_fault_smoke_integration_test.go:645-699`：已在同一闭环中构造真实 SearchRepository/SearchService，执行最小 Hybrid Search 并断言 Index Version、items、rerank degradation 和 provider call。
- 当前缺口是“通过真实 HTTP/OpenAPI/Compose 边界访问同一闭环”，不是底层业务闭环缺失。

### 3. 推荐 HTTP 契约

#### 3.1 Search endpoint

```http
POST /api/v1/search
Accept: application/json
Content-Type: application/json
```

理由：架构文档已把 Search 固定为顶级 API 分组；请求包含最多 8 KiB query 和多个过滤数组，POST body 比 GET query 更安全、更可维护。它仍是无副作用 Query，因此不要求 `Idempotency-Key`，也不返回 202。

推荐请求：

```json
{
  "workspace_id": "uuid",
  "query": "approved proposal",
  "retrieval_mode": "hybrid",
  "filters": {
    "source_ids": ["uuid"],
    "source_version_ids": ["uuid"],
    "path_prefixes": ["docs/api"],
    "captured_at_from": "2026-07-19T00:00:00Z",
    "captured_at_before": "2026-07-20T00:00:00Z"
  },
  "limit": 20
}
```

约束：

- `workspace_id`：required UUID；由请求显式给出，并同时作为授权 scope。
- `query`：required、trim 后非空、UTF-8、最大 8192 bytes。
- `retrieval_mode`：required，枚举 `keyword|semantic|hybrid`；不要再支持 `mode` 别名。
- `filters`：optional object；数组各最多 100，ID 去重排序；path 必须是 Workspace 内 canonical POSIX 相对前缀，时间采用 `[from,before)` UTC 语义。
- `limit`：optional，默认 20，范围 1..100。默认值只在 HTTP DTO 映射层设置，再进入领域 canonicalization。
- unknown fields、多个 JSON 值或超过 1 MiB body：400；不做兼容性 silent ignore。
- v1 不接收 `cursor`，也不返回 `total_count/next_cursor`。这是一条明确的“bounded ranked query”契约，不是无分页列表。

推荐响应：

```json
{
  "workspace_id": "uuid",
  "index_version_id": "uuid",
  "embedding_version_id": "uuid-or-null",
  "requested_mode": "hybrid",
  "effective_mode": "hybrid",
  "index_degraded_capabilities": [],
  "degradations": [
    {
      "capability": "rerank",
      "error_code": "RETRIEVAL_RERANK_UNAVAILABLE",
      "retryable": false
    }
  ],
  "items": [],
  "returned_count": 0
}
```

响应规则：

- 真实零命中必须是 `200` + `items: []` + `returned_count: 0`，不能变成 404、错误或模型答案。
- `embedding_version_id` 推荐 required-but-nullable，数组推荐 required 且空时为 `[]`，以减少 generated client 的 optional 分支。
- `index_degraded_capabilities` 是持久 Index 能力；`degradations` 是本次 Query 瞬时降级，不能合并成一个数组。
- `requested_mode/effective_mode` 必须同时返回，Hybrid→Keyword fallback 才可解释。
- 不返回 query、绝对路径、Provider endpoint、API key、DSN 或 rerank input text。

#### 3.2 Evidence item

推荐 wire shape：

```json
{
  "chunk_id": "uuid",
  "parse_projection_id": "uuid",
  "sequence": 3,
  "content_hash": "sha256",
  "heading_path": ["Retrieval", "Search"],
  "span": {
    "span_id": "uuid",
    "start_line": 10,
    "end_line": 18,
    "start_byte": 256,
    "end_byte": 640
  },
  "snippet": "...",
  "provenances": [
    {
      "source_id": "uuid",
      "source_version_id": "uuid",
      "relative_path": "docs/api/search.md",
      "captured_at": "2026-07-19T00:00:00Z",
      "citation_url": "/api/v1/source-versions/uuid/spans/uuid"
    }
  ],
  "provenance_truncated": false,
  "scores": {
    "lexical": {
      "rank": 1,
      "score": 0.71,
      "fts_score": 0.51,
      "trigram_score": 0.20
    },
    "vector": {
      "rank": 2,
      "distance": 0.18
    },
    "fusion": {
      "rank": 1,
      "score": 0.0325
    },
    "rerank": null
  },
  "rerank_model_version": null
}
```

关键语义：

- Vector 原始值在领域中是 distance 且升序，wire 字段应命名为 `distance`，不能泛化成“越大越好”的 `score`。
- `fusion` 始终存在；lexical/vector/rerank 按实际执行阶段 nullable。
- `rerank_model_version` 与 rerank 必须同时有值或同时为 null。
- 一个共享 Chunk 可以有多个 provenance；每个 provenance 都携带自己的 SourceVersion 和 `citation_url`，Span 可共享。
- 不增加 Document/Revision/Topic/Conflict 空壳字段。

#### 3.3 Citation endpoint

```http
GET /api/v1/source-versions/{source_version_id}/spans/{span_id}
```

推荐返回：`workspace_id/source_id/source_version_id/content_artifact_id/parse_projection_id/span_id/relative_path/content_hash/captured_at/start_line/end_line/start_byte/end_byte/excerpt`。

约束：

- 服务端通过 SourceVersion 推导 Workspace，不能接受 body/query 中第二个 `workspace_id` 覆盖。
- 必须证明 SourceVersion 绑定该 ParseProjection、Span 绑定同一 ContentArtifact/ParseProjection，并且引用仍属于允许读取的 Workspace。
- `excerpt` 从不可变 Content Artifact 的验证后 bytes 按 span 范围读取；返回前校验 byte range、UTF-8 和 excerpt hash。不能从当前 worktree relative path 读取，因为 writeback 后 worktree 可漂移，而现有 smoke 已证明 Artifact 与 worktree 可以不同。
- 返回受控 relative path 仅用于显示；客户端不得把它拼成本地绝对路径。
- excerpt 大小应有独立上限；Evidence 已有 4 KiB snippet，可优先保持同一上限，若需要上下文窗口必须显式版本化。

### 4. 推荐错误与 HTTP 状态

沿用现有项目 `Problem`：

```json
{
  "error_code": "RETRIEVAL_SEARCH_REQUEST_INVALID",
  "message": "请求未完成：RETRIEVAL_SEARCH_REQUEST_INVALID",
  "retryable": false,
  "details": {}
}
```

| 条件 | 建议状态 | 稳定码/说明 |
|---|---:|---|
| JSON、UUID、query/mode/filter/limit 非法 | 400 | `INVALID_JSON` / `RETRIEVAL_SEARCH_REQUEST_INVALID` |
| 未认证 | 401 | 仅在认证基础设施落地后使用；当前 `foundation.ErrorKind` 无法表达 |
| 已认证但无 Workspace Read、citation 安全边界拒绝 | 403 | `PermissionDenied`；不得返回空 items 隐藏权限失败 |
| Workspace 不存在 | 404 | Workspace 稳定 NotFound code |
| Active Search Index 不存在 | 404（兼容当前分类） | `RETRIEVAL_ACTIVE_SEARCH_INDEX_NOT_FOUND`；若产品要表达 `index_pending/index_stale`，应先新增明确 read model，再统一评审是否改为 409，不能只在 Handler 猜状态 |
| SourceVersion/Span 不存在或不绑定 | 404 | 不泄露跨 Workspace 对象是否存在；可用统一 citation not found code |
| Semantic 在 FTS-only/无 Query Embedder 时不可用 | 503 + retryable=false（兼容当前 Application） | `RETRIEVAL_SEMANTIC_UNAVAILABLE`；Hybrid 同条件是 200 degraded，不是错误 |
| Hybrid query embedding 可重试故障 | 200 degraded | `effective_mode=keyword`，vector/rerank degradation 保真 |
| Lexical/Vector DB 依赖失败、非可降级 provider 失败 | 503 | 保留稳定 code/retryable；可重试时按规范补 `Retry-After` |
| Index/Embedding/Fusion/Evidence 绑定损坏 | 409 | `ConsistencyViolation`，fail closed，不返回部分 items |
| 未分类服务错误 | 500 | `INTERNAL_ERROR`，不泄露 cause/SQL/path/key |
| 不支持方法 | 405 | 复用全局 router Problem |

兼容性建议：不要在 M6-D 单独把 `Content-Type` 切换为 `application/problem+json`。如果未来采用 RFC 9457，应为所有 API 统一增加 `type/title/status/detail/instance`，并设计 `error_code/retryable/details` 作为扩展成员及客户端迁移策略。

### 5. 权限契约

推荐稳定语义：

- `POST /api/v1/search`：要求调用者拥有目标 Workspace 的 `Read Workspace` Capability。
- Citation GET：要求同一 Capability，并再次按 SourceVersion 推导 Workspace；Search 成功不能作为后续 citation 请求的授权凭据。
- 浏览器：未来使用 Cookie Session；读请求不要求 CSRF token，但仍应执行 Origin/Site 策略。自动化：使用限 Scope/可过期/可撤销 API Token，Token 不进入 URL。
- 权限判定必须位于服务端统一 middleware/Authorizer seam；Handler 不读取自定义 header 自行判断，前端也不做最终授权决策。
- 失败必须是 401/403 Problem；不能返回 200 空数组，不能泄露其他 Workspace 的 SourceVersion/Span 是否存在。

当前阻塞事实：`docs/architecture/adr/0014-single-user-authentication.md:9-18` 已接受上述方向，但代码/OpenAPI 尚无实现。M6-D 规划必须二选一并写入 PRD：

1. **真实权限验收**：把 auth/session/token/authorizer 基础设施作为显式前置或子任务；Search 只在其后开始。
2. **仅冻结权限契约**：M6-D 当前只实现本地未认证 read API，但必须把安全缺口列为未完成验收，不能声称权限已交付。

不推荐“可选 Authorizer，nil 时允许”，这会形成生产静默 fallback。

### 6. OpenAPI 与客户端生成建议

#### OpenAPI

- 新增 operationIds：`searchKnowledge`、`getSourceVersionSpan`。
- 新增 schemas：`SearchRequest`、`SearchFilter`、`SearchResponse`、`EvidenceItem`、`EvidenceSpan`、`EvidenceProvenance`、`SearchScores`、`StageScore`、`VectorStageScore`、`SearchDegradation`、`CitationResponse`。
- 每个 object 使用 `additionalProperties:false`；枚举和上限与领域常量一致；nullable 使用 OpenAPI 3.1 JSON Schema 语义。
- 记录 200/400/401/403/404/409/500/503/405；401 只有在 auth 实现存在时才进入实际响应矩阵。
- `api/openapi/check.mjs` 至少检查新 operation、所有响应、schema required/nullability、枚举与 max limit。仅检查 path 存在不足以证明无漂移。
- 如果加入 OpenAPI breaking/diff 工具，必须锁版本并在 CI 对基线执行；当前仓库没有这种工具，不能把现有 `check.mjs` 称为 breaking gate。

#### 前端 generated client

- 目标层次：Generated Wire Client → Search API decoder/projection → Domain UI Model → Feature/Component。
- Generated 文件只读、可重现，并放在 `web/src/api/generated/` 或最终锁定的同等 API 边缘目录；Feature 不直接消费 generated DTO。
- 继续对 network payload 做 runtime validation；OpenAPI 生成的 TypeScript 只能提供编译期类型，不能替代不可信 JSON 校验。
- generator/runtime validator 尚未选型。应在 M6-D design 中记录工具、锁定版本、命令、输出目录、是否提交 generated files 和 drift check；在这之前不要手写一套“generated”文件名冒充生成。
- 若本任务不引入 generator，新增 `web/src/api/search.ts` 时可沿用现有 `unknown` decoder 模式，但必须在风险中标明与目标架构的差距，并避免复制 OpenAPI DTO 到 Feature 内。

### 7. 影响文件

#### 必需后端

- `internal/retrieval/http/handler.go`（新增）：Search/citation HTTP DTO、解析、Application 调用、响应映射。
- `internal/retrieval/http/handler_test.go`（新增）：JSON/UUID/mode/filter/limit、零结果、degraded、错误矩阵、citation binding。
- `internal/retrieval/application/search.go`：仅当需要补 HTTP 所需的 read/citation port 或可观测 seam；不要把 HTTP DTO 放入此包。
- `internal/retrieval/application/*evidence*.go`（可能新增）：可打开引用的 read use case。
- `internal/retrieval/adapter/postgres/search.go` 或独立 evidence read adapter：SourceVersion/Projection/Span/ContentArtifact 绑定查询。
- `internal/ingestion/domain/repository.go` / `internal/workspace/domain`：仅当选择由 Ingestion/Workspace owner 暴露 citation 读取 port；不要让 Handler 直接写跨 schema SQL。
- `internal/app/router.go`、`internal/app/router_test.go`：注入并注册 Retrieval Handler。
- `cmd/api/main.go`、`cmd/api/main_test.go`：构造 SearchRepository/SearchService/Query Embedder/citation dependencies；提取复用 provider factory。
- `internal/httpapi/response.go`：建议抽取共享 classified error→status 映射，避免各 Handler 继续复制 switch；若超出 M6-D 最小范围，可先保持行为一致并列技术债。

#### API/前端/CI

- `api/openapi/openapi.json`、`api/openapi/check.mjs`。
- `web/package.json`、`web/package-lock.json`：仅在引入并锁定 generator/runtime validator 时修改。
- `web/src/api/generated/**`（可能新增）、`web/src/api/search.ts`、对应 tests。
- `web/src/features/search/**`、`web/src/routes/AppRoutes.tsx`：只有 active task 明确包含 Search UI 时纳入；父任务 M6-D 核心验收是 API/Integration，不应自动扩大成完整 RAG/Search UX。
- `.github/workflows/ci.yml`、`Makefile`：generated drift、OpenAPI contract、integration/compose API smoke target。
- `deploy/compose.yml`、`.env.example`：API 进程要执行 Semantic/Hybrid 时必须获得与 Worker 一致的 Embedding provider 配置；不要只给 Worker 配置后让 API 静默降级。
- 建议新增 `deploy/smoke/search-api.sh` 或项目最终约定的等价脚本：启动 Compose、准备 fixture、调用真实 HTTP Search/citation、检查降级与零结果，再清理。

#### 文档

- `docs/architecture/api-and-events.md`：冻结 Search request/response/status/分页与权限。
- `docs/architecture/retrieval-architecture.md`：补 HTTP Evidence/citation 打开方式与 API 进程 Query Embedder。
- `docs/architecture/testing-and-evaluation.md`：补 API/Compose smoke 和真实 HTTP 闭环。
- `README.md`：补可执行 Search API smoke 命令。

### 8. 兼容性与风险

1. **Endpoint 漂移**：架构已写 `/api/v1/search`。改为 `/workspaces/{id}/search` 会制造文档/API 双事实源；若确需嵌套路由，必须先修改设计并说明迁移。
2. **Problem 伪兼容**：局部采用 RFC 9457 media type/字段会让现有前端错误 decoder 和所有旧 operation 不一致。
3. **Cursor 假稳定**：Active Index 可原子切换，Rerank 可瞬时变化；未冻结 Index/请求/最终排序就发 cursor 会漏项或重复。v1 Top-N 是更诚实的边界。
4. **Vector score 误读**：当前值是 distance，若 wire 叫 `score`，前端可能按降序展示错误。
5. **空值与数组漂移**：Go `omitempty` 会把 nil/[] 混淆，generated client 也会产生大量 optional 分支。公开 DTO 应明确 required-nullable 与 required-empty-array 策略。
6. **Citation 读错事实源**：打开 worktree relative path 会在用户编辑或 Safe Writeback 后展示不同内容；必须读不可变 Content Artifact。
7. **共享 Chunk provenance**：一个 Chunk 可绑定多个 SourceVersion，不能只返回第一个路径；`provenance_truncated` 必须保留。
8. **API/Worker Provider 配置不一致**：Worker 构建 Hybrid Index，但 API 未配置相同 Embedder 时，Semantic 503、Hybrid 降级；这是可观察能力差异，必须在 startup/status/测试中明确。
9. **Auth 范围膨胀**：真正 401/403 需要超出 Search Handler 的身份基础设施。PRD 若不拆分，会把 M6-D 从检索集成扩大成全站认证交付。
10. **“OpenAPI 无漂移”证据不足**：现有 check 只看 path/schema 是否存在；需要 handler contract test、schema assertions 和可选 generated drift 才能支撑验收。
11. **Compose 假 smoke**：`compose config`、image build 或 `/readyz` 不能证明 Search；必须真实调用 Search/citation 并检查对应 PostgreSQL/Index 事实。
12. **性能结论过度外推**：现有 EXPLAIN 只证明 operator/index/filter；不能宣称 50 万 Chunk P95 或 ANN 参数已达标。

### 9. 建议验收命令

#### 快速单元/契约

```bash
go test ./internal/retrieval/domain ./internal/retrieval/application ./internal/retrieval/http ./internal/app ./cmd/api
go test ./internal/retrieval/adapter/postgres
go vet ./cmd/... ./internal/...
node api/openapi/check.mjs
npm run lint --prefix web
npm run typecheck --prefix web
npm run test --prefix web
npm run build --prefix web
```

`internal/retrieval/http` 在实现前不存在；命令应在该包新增后启用。

#### PostgreSQL Search/EXPLAIN

```bash
go test -tags=integration -run 'TestSearchRepository(ActiveFiltersAndBoundedProvenance|LoadsLegacyFTSOnlyWithoutInventingFusion|DistanceOperatorsAndExactExplain)$' ./internal/retrieval/adapter/postgres
```

#### Approved Proposal → Active Index → Completion → Search 闭环

```bash
go test -tags=integration -run '^TestApprovalDispatchRealRiverSafeWritebackSmoke$' ./internal/changecontrol/application
```

该测试当前已经到 Application Search；M6-D 应扩展或新增 HTTP 级 smoke，确保同一 fixture 通过 Router/OpenAPI DTO 返回。

#### OpenAPI/客户端 drift

```bash
make openapi-check
# 若引入 generator：运行锁定的 generate 命令，然后断言 generated tree 无 diff。
```

#### Compose/API smoke

```bash
make compose-up
curl -fsS http://127.0.0.1:8080/readyz
# 运行新增的 fixture/setup 与 POST /api/v1/search、citation GET smoke。
make compose-down
```

现有 Compose 没有 Search fixture/setup 命令；在脚本落地前，不能把 readiness curl 当作 Search API smoke。

#### 全量门禁

```bash
go test -race ./...
go vet ./...
make test
make compose-check
make docker-build
```

关键并发/恢复包按父任务要求补 `-count=20`；integration/Compose 需要 Docker 可用。

## External References

- RFC 9457, “Problem Details for HTTP APIs”, 2023-07: https://www.rfc-editor.org/rfc/rfc9457.html 。标准定义 `application/problem+json` 以及 `type/status/title/detail/instance`；本项目当前 envelope 不是该标准的完整实现。
- OpenAPI Specification 3.1.0: https://spec.openapis.org/oas/v3.1.0.html 。当前仓库 `openapi.json` 使用 3.1.0；Security Requirement Object 可表达多种认证方案，JSON Schema 3.1 语义可表达 nullable。
- 项目锁定版本：Go 1.25.4、Chi 5.3.1、pgx 5.10.0、pgvector-go 0.4.0（`go.mod:3-12`）；Node >=24.18.0、TypeScript 5.9.3、Vite 8.1.5、Vitest 4.1.10（`web/package.json:6-35`）。

## Related Specs

- `.trellis/spec/backend/index.md`：后端预开发与质量检查入口。
- `.trellis/spec/backend/directory-structure.md`：Presentation 只调用 Application；composition root 负责注入。
- `.trellis/spec/backend/error-handling.md:7-54`：稳定分类、Problem 字段、HTTP 映射、Retry-After 与敏感信息边界。
- `.trellis/spec/backend/quality-guidelines.md:24-65`：OpenAPI、分页、错误映射、SQL、性能、安全、集成/smoke 门禁。
- `.trellis/spec/frontend/index.md`：前端预开发与质量检查入口。
- `.trellis/spec/frontend/type-safety.md:16-53`：Generated Wire Type 只在 API 边缘，Network 输入仍按 unknown 校验，Generated file 只读可复现。
- `.trellis/spec/frontend/quality-guidelines.md:16-55`：Typed API、Cursor/边界、Degraded/Error 状态与 generated drift。
- `.trellis/spec/guides/cross-layer-thinking-guide.md`：公共契约需要沿 Backend → OpenAPI → Client → Feature → Tests 全链路核对。
- `.trellis/tasks/07-17-retrieval-indexing-search/prd.md:59-78`：M6 R4/R5 Search/Evidence/安全/性能事实。
- `.trellis/tasks/07-17-retrieval-indexing-search/design.md:102-124`：Search Flow、Evidence v1、兼容与 rollback。

## Caveats / Not Found

- 当前子任务 `prd.md` 仍为 TBD，且没有 `design.md/implement.md`；本文建议必须回填并由用户评审后才能作为实现契约。
- 未找到 `internal/retrieval/http`、Search OpenAPI path、citation read endpoint、API 进程 Query Embedder、认证 middleware/Authorizer、OpenAPI security scheme、generated client 或 breaking-change tool。
- 未找到真实 Cursor 实现或可持久恢复的 Search result snapshot；因此不建议在 M6-D v1 宣称 cursor pagination。
- 未找到通用生产 Reranker 协议；现有 nil Reranker 正确返回 RRF + rerank degradation。
- 未找到独立 Search UI 实现；是否包含 `/search` 页面需要由子任务 PRD 明确，不能从 M6-D 名称自动推导。
- 外部标准只用于判断协议兼容性；没有为本任务选择新的 generator/runtime validator 依赖或版本。
