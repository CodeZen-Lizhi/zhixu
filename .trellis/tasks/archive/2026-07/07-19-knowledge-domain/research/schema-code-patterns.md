# Research: M5-05 schema and code patterns

- Query: 检查 `migrations/00001`–`00016` 与 `internal/*` 现有实现，提出 M5-05 Topic、Claim、Relation、Evidence、Conflict、Source Span、Proposal 的建议表结构、约束、Repository/UoW 复用模式和受影响文件。
- Scope: internal
- Date: 2026-07-19

## Findings

### 1. 结论摘要

- 当前最新项目迁移是 `00016_embedding_hybrid_search.sql`；下一份前向迁移编号应为 `00017`。迁移通过 `//go:embed *.sql` 自动纳入，不需要维护手工清单（`migrations/embed.go:6-9`）。
- PostgreSQL 已有 `core`、`change_control`、`workflow`、`retrieval`、`learning`、`ops`，另由 `00007` 创建 `ingestion`；架构把 Knowledge 放在 `core`（`migrations/00001_extensions.sql:4-9`，`migrations/00007_ingestion.sql:2`，`docs/architecture/database-design.md:7-18`）。
- 仓库不存在 `core.topic`、`core.claim`、`core.relation`、Evidence/Conflict 表，也不存在 `internal/knowledge/` 包。现有 `Evidence` 是 Retrieval 查询 DTO/Source Span 引用，现有 `Conflict` 多为错误分类，Workflow 的 `Claim` 是租约动作，均不是知识领域实体。
- Source Span 已完整落地，适合作为 M5-05 Evidence 的不可变引用底座；Proposal 已完整落地但当前是“文件补丁 Proposal”，不能无修改地承载 Topic/Claim/Relation 变更。
- 建议新增 `00017_knowledge_domain.sql` 和独立 `internal/knowledge/{domain,application,adapter/postgres,http,workflow}`；第一阶段优先完成数据库内同步 UoW，只有超过 3 秒、需人工等待或跨存储副作用的操作才注册 Workflow。

### 2. Files found

| 文件 | 说明 |
|---|---|
| `migrations/00001_extensions.sql` | 已有逻辑 Schema、pgvector 与 `core.schema_meta`。 |
| `migrations/00004_change_control.sql` | Proposal、不可变 Revision、Approval 基础表与触发器。 |
| `migrations/00005_change_control_hardening.sql` | Proposal 幂等键、请求哈希和 Workspace 级唯一约束。 |
| `migrations/00007_ingestion.sql` | Parse Projection、Source Span、Canonical Chunk 及跨表 Workspace/版本约束。 |
| `migrations/00009_safe_writeback.sql` | Proposal 乐观锁状态机、文件/Git Writeback 绑定。 |
| `migrations/00013_approval_writeback_dispatch.sql` | Approved Proposal 与唯一 Workflow Run 的绑定。 |
| `migrations/00014_retrieval_index_foundation.sql` | 复合 Workspace 外键、不可变 Projection、guarded Down 的成熟模式。 |
| `internal/foundation/{id,error,clock}.go` | 可复用 ID、错误分类、生产/测试 Clock。 |
| `internal/ingestion/domain/model.go` | Source Span 领域结构。 |
| `internal/ingestion/adapter/postgres/repository.go` | 显式 SQL、事务、扫描、SQLSTATE 分类模式。 |
| `internal/retrieval/domain/evidence_reference.go` | Source Version/Span 引用的领域校验。 |
| `internal/retrieval/adapter/postgres/evidence.go` | Workspace→SourceVersion→Artifact→Projection→Span 的 fail-closed 查询。 |
| `internal/changecontrol/domain/model.go` | 当前文件型 Proposal/Revision/Approval 聚合与状态机。 |
| `internal/changecontrol/adapter/postgres/repository.go` | Proposal/Revision 同事务创建、幂等重放、乐观锁。 |
| `internal/workflow/application/{executor_registry,definition_registry}.go` | Workflow Executor/Definition 的版本化注册与 Freeze 模式。 |
| `internal/httpapi/response.go` | 严格 JSON 与统一 Problem Details。 |
| `cmd/api/main.go`、`internal/app/router.go` | API Composition Root 与模块 Handler 注入点。 |
| `cmd/worker/main.go` | Worker、Workflow Executor/Definition、River Worker 注册点。 |
| `internal/platform/migration/*` | Goose/River runner、legacy baseline、Up/Down 集成门禁。 |

### 3. 已存在数据库事实

#### 3.1 Source Span / Evidence 底座

`ingestion.source_span` 已包含 `workspace_id`、`content_artifact_id`、`parse_projection_id`、行/字节范围、selector、excerpt hash、parser/schema version，并以 `(parse_projection_id, span_type, start_byte, end_byte, schema_version)` 唯一（`migrations/00007_ingestion.sql:69-87`）。触发器验证：

- Span 的 Artifact 必须与 Parse Projection 一致；
- `end_byte` 不得超过 Content Artifact 字节长度；
- parser/schema version 必须与 Projection 一致；
- Workspace 必须一致（`migrations/00007_ingestion.sql:168-203`）。

Span 还被禁止 UPDATE/DELETE，并有 `(parse_projection_id,start_byte,id)` 索引（`migrations/00007_ingestion.sql:283-293`）。`canonical_chunk.source_span_id` 已是 RESTRICT FK（`migrations/00007_ingestion.sql:89-108`）。

Retrieval 侧已经实现可复用的“完整引用证明”：查询同时连接 Source Version、Source、Content Artifact、Source Version Projection、Parse Projection 和 Source Span，并再次校验 Workspace、Artifact、parser/schema version（`internal/retrieval/adapter/postgres/evidence.go:85-177`）。Application 再读取不可变 Artifact，复核 hash/size/byte range/excerpt hash，最多返回 4 KiB UTF-8 excerpt（`internal/retrieval/application/evidence_reference.go:88-165`）。

因此，M5-05 Evidence 不应复制原文或仅保存 URL；应保存 `source_version_id + source_span_id`，并复用上述完整绑定规则。架构也明确 Citation/Provenance 以这两个身份选择具体导入路径（`docs/architecture/database-design.md:182-191`）。

#### 3.2 当前 Evidence 不是知识 Evidence 聚合

- Retrieval 的 `EvidenceV1` 是有界搜索结果，字段为 Chunk、Span、Provenance 和各阶段分数（`internal/retrieval/domain/search.go:284-318`）。
- Change Control 的 `proposal_revision.evidence_summary` 只是非空文本（`migrations/00004_change_control.sql:16-34`）；HTTP 也只收 `evidence_summary` 字符串（`internal/changecontrol/http/handler.go:40-47`）。
- 当前没有可被 Claim 和 Relation 共用、带 stance/reason/applicability/confirmed_by 的持久 Evidence 表。

#### 3.3 当前 Proposal 是文件型 Proposal

现有 Proposal 表只有身份、Workspace、状态、时间；后续增加 idempotency/request hash、version、workflow_run_id（`migrations/00004_change_control.sql:4-14`，`migrations/00005_change_control_hardening.sql:2-22`，`migrations/00009_safe_writeback.sql:2-7`，`migrations/00013_approval_writeback_dispatch.sql:3-17`）。

不可变 Revision 强制 `target_path`、`base_hash`、全文 `content`、`evidence_summary`、`risk`、`rollback_plan`、`change_hash` 全部非空（`migrations/00004_change_control.sql:16-35`）。Go 聚合和创建命令也是相同文件补丁形态（`internal/changecontrol/domain/model.go:45-80`，`internal/changecontrol/application/service.go:62-107`）。Approved Proposal 会统一绑定 Safe Writeback Workflow，后者固定需要 `WRITE_KNOWLEDGE + GIT_WRITE`，并最终写文件/Git（`internal/changecontrol/workflow/contract.go:17-61`）。

这与架构中通用 Proposal 的 `type/target_refs/base_versions/change_set/evidence_refs/schema_version` 目标形态不同（`docs/architecture/database-design.md:308-342`）。因此不能用占位路径、假 hash 或假正文把知识变更塞进现表；那会绕过类型契约并错误触发文件 Safe Writeback。

### 4. 建议 `00017_knowledge_domain.sql` 表结构

以下是基于现有约束风格的建议，不是仓库已实现事实。Knowledge 按当前 Schema 决策落在 `core`，迁移写 `core.schema_meta('knowledge_domain','m5-05')`。

#### 4.1 `core.topic`

- `id uuid primary key`
- `workspace_id uuid not null references core.workspace(id) on delete restrict`
- `name text not null`
- `normalized_name text not null`
- `aliases jsonb not null default '[]'`，检查为 array
- `status text not null`
- `version bigint not null default 1 check(version>0)`
- `created_at/updated_at timestamptz not null`，检查时间顺序
- `unique(id,workspace_id)`，供跨表复合 FK
- `unique(workspace_id,normalized_name)`；若归档后允许重名，应改为 Active 状态 partial unique，不能同时保留两套语义。

#### 4.2 `core.claim`

- `id/workspace_id`
- `statement text not null`
- `normalized_statement text not null`
- `applicability jsonb not null default '{}'`，检查为 object
- `applicability_hash text not null check SHA-256`
- `status text not null`
- `confidence_factors jsonb not null default '{}'`
- `version/created_at/updated_at`
- `unique(id,workspace_id)`
- 建议 `unique(workspace_id,normalized_statement,applicability_hash)`，避免相同陈述与适用条件重复；hash 必须由 Domain 对 canonical JSON 计算，数据库只校验格式。

Claim 状态机应以 Domain 为唯一事实源，并由 DB trigger 镜像最小防线；架构候选状态为 Suggested→Confirmed/Invalid、Confirmed↔Disputed、Confirmed/Disputed→Superseded 等（`docs/architecture/domain-model.md:282-294`）。最终枚举需由 PRD 锁定。

#### 4.3 `core.topic_claim`

- `topic_id/claim_id/workspace_id`
- 复合 FK `(topic_id,workspace_id)`、`(claim_id,workspace_id)`
- `created_at`
- 主键 `(topic_id,claim_id)`

该表比把 Topic ID 放进 Claim 更符合多对多模型（`docs/architecture/domain-model.md:232-235`）。

#### 4.4 `core.evidence` + owner join tables

建议把 Evidence 做成一等不可变引用，避免 Claim/Relation 各复制一套 Source Span 元数据：

- `core.evidence(id,workspace_id,source_version_id,source_span_id,reason,applicability,evidence_hash,model_run_ref,confirmed_by,created_at)`；
- `core.claim_evidence(claim_id,evidence_id,workspace_id,stance,created_at)`，`stance` 至少 support/refute；
- `core.relation_evidence(relation_id,evidence_id,workspace_id,created_at)`。

关键约束：

- `evidence_hash` 为 canonical Evidence payload 的 SHA-256；`unique(workspace_id,evidence_hash)` 支持幂等重放。
- Evidence、join 行 append-only，UPDATE/DELETE 返回 SQLSTATE `55000`，沿用 Source Span/Revision 模式。
- 插入 Evidence 时用 trigger/Repository 查询证明 `workspace → source_version → content_artifact → source_version_projection → parse_projection → source_span` 全绑定；直接只 FK `source_span_id` 不足以证明具体 Source Version provenance。
- Claim 进入 Confirmed、Relation 进入 Confirmed 时，使用 DEFERRABLE CONSTRAINT TRIGGER 在提交时验证至少一个有效 Evidence；单行 CHECK 无法检查子表存在性。

若产品坚持文档名 `claim_source`，可以把 `claim_evidence` 命名为 `claim_source`，但领域层仍应暴露统一 `Evidence`，不能再制造第二种 Evidence 含义。

#### 4.5 `core.relation`

- `id/workspace_id`
- `source_type/source_id/target_type/target_id`
- `relation_type/status/confidence`
- `valid_from/valid_to`
- `version/created_at/updated_at`
- `unique(id,workspace_id)`
- `unique(workspace_id,source_type,source_id,target_type,target_id,relation_type)`
- CHECK：非自环、时间范围有效、type/relation/status 来自固定枚举；不能把 type 拼进 SQL。

多态端点无法用一个普通 FK 完整表达。M5-05 若只支持 Topic/Claim，应将 `source_type/target_type` 限定为这两类，并在固定分支 trigger/UoW 中校验对象存在、同 Workspace、生命周期合法。架构明确数据库负责基础类型/Workspace/唯一性，Knowledge Module 负责端点兼容、自环、证据和状态转移（`docs/architecture/database-design.md:278-288`）。

对 `DUPLICATES`、`CONFLICTS_WITH`，Domain 写入前必须按 `(type,id)` 规范化稳定顺序，DB 唯一键阻止反向重复；这是项目数据库规范的明确要求（`.trellis/spec/backend/database-guidelines.md:47-53`）。

#### 4.6 `core.conflict` + `core.conflict_member`

- `conflict(id,workspace_id,topic_id nullable,status,severity,summary,resolution nullable,version,created_at,updated_at)`；
- `conflict_member(conflict_id,claim_id,workspace_id,applicability,position_summary,created_at)`；
- 复合 FK 保证 Conflict、Topic、Claim 同 Workspace；主键 `(conflict_id,claim_id)`。
- Conflict 状态/版本由乐观锁推进；Resolved/Accepted Divergence 要求非空 resolution。
- 使用 deferred constraint trigger 保证一个可提交 Conflict 至少有两个成员；CHECK 不能统计关联行。
- 不允许通过删除 Claim/Member 静默消失，全部 FK `ON DELETE RESTRICT`，符合“Conflict 不能通过覆盖 Claim 静默消失”的不变量（`docs/architecture/domain-model.md:94-109`）。

#### 4.7 Proposal 兼容建议

不要在 `00017` 中破坏现有文件 Proposal。若 M5-05 必须同时支持 Knowledge Proposal，采用 Expand 方式：

1. 给 `change_control.proposal` 增加 `proposal_type text not null default 'file_patch'`；历史行自动保持现语义。
2. 给 `proposal_revision` 增加 nullable/defaulted `target_refs jsonb`、`base_versions jsonb`、`change_set jsonb`、`evidence_refs jsonb`、`schema_version text`。
3. 将现有文件列的 NOT NULL/CHECK 改成按 `proposal_type='file_patch'` 条件约束；知识 Proposal 禁止出现假 `target_path/base_hash/content`。
4. Approval/Revision 唯一绑定和 change hash 可复用；但 Approval Dispatch 必须按 Proposal Type 路由。`file_patch` 继续 Safe Writeback；`knowledge_change` 进入 Knowledge Apply UoW/Workflow，不能要求 Git Write。
5. 先保持现有 Go Repository 和 HTTP wire 对 `file_patch` 完全兼容，再新增知识专用命令/响应；不要直接改变现有 `/workspaces/{id}/proposals` 的必填字段。

这是典型 Expand→切换→清理；规范禁止直接破坏运行中旧代码（`.trellis/spec/backend/database-guidelines.md:38-45`）。是否把 Proposal 泛化纳入本任务必须由 PRD 明确，否则建议 `00017` 只建 Knowledge 表与 Proposal 引用 seam，不改当前 Safe Writeback 流。

### 5. Repository / UoW 复用模式

建议接口由 `internal/knowledge/domain` 定义，pgx 类型留在 Adapter。最小命令/UoW：

- `UpsertTopic`：Workspace + normalized name 幂等创建/读取；不同 payload 命中同 key 返回 VersionConflict。
- `SuggestClaim`：canonical statement/applicability hash 幂等创建 Suggested Claim。
- `ConfirmClaim`：按 Workspace→Claim 固定锁序，写 Evidence/ClaimEvidence，校验至少一个有效 provenance，`status/version` CAS 更新；同事务提交。
- `SuggestRelation`：规范化端点与对称关系，保存 Suggested Relation。
- `ConfirmRelation`：锁端点→Relation，写 Evidence/RelationEvidence，校验端点/类型/证据，CAS Confirmed。
- `OpenConflict`：规范化并锁 Claim ID 顺序，同事务写 Conflict + 至少两个 Member。
- `ResolveConflict`：锁 Conflict→Members→Claims，记录 resolution/version；若需要正式知识变更，只创建/绑定 Proposal，不在 HTTP Handler 中跨模块开事务。

可直接复用的代码模式：

- 小 `DB` interface + `NewRepository` nil 依赖拒绝：`internal/ingestion/adapter/postgres/repository.go:17-33`。
- `INSERT ... ON CONFLICT DO NOTHING RETURNING` 后读取持久行、比较完整绑定判定 replay/conflict：`internal/changecontrol/adapter/postgres/repository.go:36-78`。
- 显式列、参数化 SQL、扫描后 `foundation.ParseID`；禁止 `SELECT *`。
- SQLSTATE：`23505`→VersionConflict，`23503/23514/55000`→ConsistencyViolation，`40001/40P01/55P03`→Retryable；现有参考在 `internal/ingestion/adapter/postgres/repository.go:591-608` 和 `internal/retrieval/adapter/postgres/errors.go:11-29`。
- 可变 Aggregate 使用 `version=expected+1` 且检查 `RowsAffected()==1`；Proposal 参考 `internal/changecontrol/adapter/postgres/repository.go:151-159`。
- 跨 Workspace/跨版本 Evidence 查询 fail closed：`internal/retrieval/adapter/postgres/evidence.go:85-177`。

### 6. Foundation、HTTP、Workflow 与测试复用

#### Foundation / Adapter

- `foundation.ID/ParseID/UUIDGenerator` 可直接使用（`internal/foundation/id.go:12-60`）。
- `foundation.ErrorKind/Error` 是 HTTP、Workflow、Adapter 共同错误包络（`internal/foundation/error.go:5-47`）。
- `SystemClock/FixedClock` 可用于应用和确定性测试（`internal/foundation/clock.go:6-21`）。
- `httpapi.DecodeJSON` 已有 1 MiB 上限、严格未知字段、单 JSON 值和 Unicode 校验；`WriteProblem/StatusForErrorKind` 可复用（`internal/httpapi/response.go:15-80`）。
- `platform/postgres.Pool` 只在 Composition Root 暴露 pgx，领域 Repository 继续依赖自身接口（`internal/platform/postgres/pool.go:23-47`）。

#### HTTP / Composition

- 新 Handler 应像现有模块只依赖 Application 小接口并通过 `Routes` 注册（`internal/retrieval/http/handler.go:27-58`）。
- `internal/app.Dependencies` 目前没有 Knowledge 字段；Router 只注册 Workspace/Workflow/ChangeControl/Ingestion/Retrieval（`internal/app/router.go:34-49,81-99`）。
- `cmd/api/main.go` 需要创建 Knowledge Repository/Service/Handler 并注入 Router；现有构造模式见 `cmd/api/main.go:81-171`。
- 新/变更 API 必须同步 `api/openapi/openapi.json` 和 `api/openapi/check.mjs`；检查脚本硬编码 required operations/schema（`api/openapi/check.mjs:8-33,58-110`）。

#### Workflow

- 当前 Workflow 已有 `WRITE_KNOWLEDGE` permission（`internal/workflow/domain/definition.go:5-15`）。
- Executor/Definition 必须在 Freeze 前注册，且按 kind + schema version 解析（`internal/workflow/application/executor_registry.go:100-170`，`internal/workflow/application/definition_registry.go:41-106`）。
- Worker 当前只注册 Safe Writeback Executor/Definition，然后 Freeze（`cmd/worker/main.go:302-324`）。若 M5-05 新增长任务，必须在这些 Freeze 之前注册，并将依赖加入 `workerComponents/readiness`；同步查询/单事务命令无需强行走 Workflow。

#### Testkit

- 仓库没有项目自有共享 `testkit/testutil/dbtest` 包；集成测试各自在 `_test.go` 内创建数据库/fixture，统一依赖 `ZHIXU_TEST_DATABASE_URL`。例如迁移 fixture 在 `internal/platform/migration/runner_integration_test.go:682-690`，Retrieval HTTP fixture 在 `internal/retrieval/http/handler_postgres_integration_test.go:800-804`。
- 因此可复用的是测试“模式”而非可导入包：Domain table tests、Application fake Repository、HTTP fake Service、PostgreSQL integration、migration empty/repeat Up/guarded Down。不要导入 vendor 内部的 River testutil。
- 若 M5-05 出现第三套以上重复的隔离数据库创建逻辑，可另立任务抽共享 testkit；本任务不应顺手扩大范围。

### 7. 受影响文件清单

建议新增：

- `migrations/00017_knowledge_domain.sql`
- `internal/knowledge/domain/{model,repository,errors}.go`
- `internal/knowledge/application/service.go`
- `internal/knowledge/adapter/postgres/repository.go`
- `internal/knowledge/http/handler.go`
- 对应 `*_test.go` 与 PostgreSQL integration tests
- 只有确需异步时才新增 `internal/knowledge/workflow/*`

建议修改：

- `internal/app/router.go`：Knowledge Handler 注入/路由。
- `cmd/api/main.go`：Repository/Application/HTTP Composition。
- `api/openapi/openapi.json`、`api/openapi/check.mjs`：新增 wire contract。
- `cmd/worker/main.go`：仅在新增 Knowledge Workflow 时注册 Executor/Definition/Worker。
- `internal/platform/migration/runner_integration_test.go`：最新版本计数和 Down 顺序。
- `internal/platform/migration/runner_state_machine_integration_test.go`：Down 顺序。
- 若泛化 Proposal：`migrations/00004/00005/00009/00013` **不得改历史文件**；只能在 `00017`/后续迁移扩展，并修改 `internal/changecontrol/{domain,application,adapter/postgres,http,workflow}` 及 OpenAPI/测试。

### 8. 迁移编号与兼容性风险

1. **版本断言会失败**：迁移集成测试两处硬编码 `maxVersion/applied == 16`（`internal/platform/migration/runner_integration_test.go:39-45,622-628`），新增 `00017` 后必须改为 17。
2. **Down 顺序整体后移**：migration tests 有 24 次一步 `Down(ctx)` 调用；现有用例假定栈顶是 00016，例如 `runner_state_machine_integration_test.go:71-96`。新增 00017 后所有按次数定位 00016/00014/00012 的测试都需先处理 00017。
3. **legacy baseline 不应扩大**：兼容层只对 00001–00010 注入 Goose statement boundaries，`legacyMigrationMaxVersion=10`（`internal/platform/migration/legacyfs.go:16-23`）；旧 shell-runner baseline 只接管 1..10（`internal/platform/migration/runner.go:148-195`）。00017 必须是原生 Goose 合法 SQL，不能加入 legacy expected map。
4. **guarded Down**：若 Knowledge 有业务数据，00017 Down 应以 `55000` 拒绝；模式见 Retrieval migration（`migrations/00014_retrieval_index_foundation.sql:654-675`）。空表 Down 必须成功，且测试先移除 00017 才能继续验证旧迁移 guard。
5. **跨 Schema provenance**：`source_version` 的 Workspace 通过 `core.source` 间接获得，而 Span 可被多个 Source Version 共享；单 FK 不足以证明 Evidence 的具体导入路径。必须复用完整 join/trigger，不可只保存 span_id。
6. **Proposal wire/dispatch 不兼容**：现有客户端和 OpenAPI要求文件字段；现有 approved 流总是 Safe Writeback。直接改成通用 Knowledge Proposal 会破坏 HTTP、Repository scan、Approval Dispatch、Git/writeback/reindex smoke。
7. **术语碰撞**：Workflow `Claim` 表示 lease acquisition（如 `internal/workflow/adapter/postgres/runtime_state.go:29-30`），Retrieval `EvidenceV1` 表示查询结果。新包必须始终使用包限定语义和明确错误码，避免把运行时 Claim 或搜索 Evidence 当知识实体。
8. **JSONB canonicalization**：applicability/change_set/evidence refs 如果参与唯一性或 hash，必须由 Domain canonicalize；PostgreSQL JSONB 等价不等于业务字段顺序/缺省语义已经锁定。
9. **权限与跨模块事务**：Knowledge UoW 控制 Claim/Relation/Conflict；HTTP 不得同时开启 Change Control/Workflow 事务。需要 Proposal 时通过明确 Application seam/Outbox 协作，符合事务归属（`docs/architecture/module-architecture.md:364-375`）。

### 9. External references / versions

- Go `1.25.4`、pgx `v5.10.0`、Goose `v3.27.0`、River `v0.40.0`、pgvector-go `v0.4.0` 已由 `go.mod:1-15` 锁定。
- 本研究未使用外部网页；版本事实仅来自仓库 manifest。

### 10. Related specs

- `.trellis/spec/backend/database-guidelines.md:27-63`：参数化 SQL、复合约束、Relation 规范化、乐观锁、Expand 迁移、禁止修改历史迁移。
- `.trellis/spec/backend/directory-structure.md:15-58`：目标 `internal/knowledge` 模块、依赖方向、Composition Root 与 Shared Kernel 边界。
- `.trellis/spec/guides/cross-layer-thinking-guide.md`：DB→Repository→Application→HTTP/Workflow 的完整数据流检查。
- `docs/architecture/CONTEXT.md:41-75`：Source Span、Topic、Claim、Relation、Relation Evidence、Conflict、Proposal 的统一术语。
- `docs/architecture/domain-model.md:94-125,245-305`：Knowledge/Proposal 不变量、Relation 类型、Claim/Conflict 状态机。
- `docs/architecture/database-design.md:232-342`：建议字段、多态 Relation 约束与通用 Proposal 目标形态。

## Caveats / Not Found

- 当前任务 `prd.md` 的 Requirements 和 Acceptance Criteria 仍为 TBD，无法从任务文件确认 M5-05 的最终状态枚举、Relation Node Type 范围、Evidence 是否必须一等实体、Knowledge Proposal 是否本期实现；上述表结构需在 PRD 锁定后再定稿（`.trellis/tasks/07-19-knowledge-domain/prd.md:5-11`）。
- 未连接运行中的 PostgreSQL；“已存在”结论来自迁移、Go 源码和测试，不代表某个本地数据库一定已执行到 00016。
- 未找到 `internal/knowledge`、Knowledge HTTP 路由、Knowledge Workflow、Topic/Claim/Relation/Conflict 数据表或项目级共享 testkit。
- `.trellis/spec/backend/directory-structure.md` 部分“当前没有 Go 源码”的描述已落后于仓库现状；本研究只采用其中仍有效的模块边界，不把该陈述当事实。
