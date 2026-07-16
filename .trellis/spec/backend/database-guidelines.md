# 数据库开发规范

## 适用范围

适用于 PostgreSQL、pgvector、Goose 迁移、sqlc 查询、River 任务表以及各领域模块的 Repository/Projection 实现。当前没有数据库迁移或 Go 代码，以下内容是已确认的设计约束和 M1 实现门禁。

## 已确认事实

- PostgreSQL 是领域数据、投影和运行数据的主数据库；pgvector 保存向量，PostgreSQL FTS 保存全文索引（依据 [`technology-stack.md`](../../../docs/architecture/technology-stack.md) 第 3 节）。
- PostgreSQL 驱动为 pgx，SQL 访问采用 sqlc，迁移采用 Goose，River 只负责可运行 Job 的投递和 Worker 获取，不是 Workflow 业务事实源（依据 [`technology-stack.md`](../../../docs/architecture/technology-stack.md) 与 [`workflow-engine.md`](../../../docs/architecture/workflow-engine.md)）。
- 事务边界由维护不变量的领域模块控制：Proposal/Approval、Workflow Node、Review Answer、Relation Confirm 等在数据库内使用 ACID；文件和 Git 不放入数据库事务。
- Source Version、Article Revision、Proposal Revision、Workflow Definition/Run、Embedding/Index Version 等必须有版本、哈希或状态约束；重复消息不得创建重复 Node、Tool Call、Answer 或 Health Issue。
- 查询必须支持稳定排序和 cursor 分页；大集合、图谱邻居和 Collection 结果禁止无分页返回（依据 [`api-and-events.md`](../../../docs/architecture/api-and-events.md)）。

## 目标代码落点（M1 起）

- `migrations/`：Goose 前向迁移、约束、索引和必要的数据回填。
- `internal/platform/postgres/`：pgx 连接池、sqlc 生成代码入口、Repository/Projection Adapter、事务辅助函数。
- 各领域模块（如 `internal/changecontrol/`、`internal/workflow/`、`internal/review/`）：定义 Repository/Store Interface 和事务不变量；不得暴露 sqlc 类型。
- `internal/workflow/`：River Job 与 Workflow Node 的映射、租约和 Outbox 协作。
- 数据库集成测试与 Testcontainers Fixture 的实际路径由 M1 测试布局确认；本规范不虚构文件名。

## 查询模式

1. 通过 sqlc 生成类型安全查询；SQL 必须显式列出字段，避免生产路径使用 `SELECT *`。
2. 所有外部输入使用参数化参数，动态过滤通过受限 Query AST/白名单映射生成，禁止字符串拼接。
3. 列表查询使用稳定排序字段加稳定 ID 作为游标边界，并限制最大 `limit`；不使用无界 offset 扫描承载大列表。
4. Embedding、解析、索引和健康扫描使用批量操作；禁止逐 Chunk、逐行循环远程调用或循环查库造成 N+1。
5. 外键、状态、唯一键、乐观锁版本、幂等键和对称 Relation 规范化优先用数据库约束表达；应用层校验不能替代数据库不变量。
6. 事务只覆盖同一数据库内的状态变化和 Outbox 写入。文件原子替换、Git Commit、索引切换等跨存储步骤遵循 Change Control Saga。
7. Worker 获取任务使用安全租约和等价的并发控制（文档明确提出 `SKIP LOCKED` 或安全领取）；完成 Node 时在同一事务中保存输出、更新状态、写后继 Outbox 和 checkpoint。
8. Vector 只在同一 Index Version 中使用固定维度；变更维度必须新建列/表或 Index Version，不得混写。

## 迁移规则

- 迁移只向前执行；DDL 和数据回填拆分，大表索引采用非阻塞策略。
- Schema 变化遵循 Expand → 双读/迁移 → 切换 → 清理旧字段，避免与运行中的旧代码不兼容。
- 每次迁移记录应用版本和校验结果；重复执行必须安全失败或明确无操作。
- 逻辑 Schema（`core`、`change_control`、`workflow`、`retrieval`、`learning`、`ops`）是设计建议；是否采用独立 Schema 或统一 `public` 前缀必须在首批迁移中锁定，不能由单个模块自行决定。
- 正式 v1 不强制分区；只有 Node Run、Tool Call、Audit 等达到文档规定的容量或查询退化条件后才评估按月归档/分区。
- Embedding、FTS 等可重建投影不作为永久备份最低要求；Proposal、Approval、Confirmed Relation、Workflow、Audit、Review 等运行/决策数据必须备份。

## 命名与约束

- SQL 表名、列名和索引名采用小写 snake_case；同一业务对象沿用 `workspace_id`、`version`、`status`、`created_at` 等统一语义。最终命名以 M1 首个迁移和 sqlc 配置为准。
- 外键列以关联对象 `_id` 结尾；时间字段明确时区策略；JSONB 仅用于版本化契约或确实动态的结构，不为所有 JSONB 建无差别索引。
- 索引名必须表达表、关键列和用途；核心 FTS 使用 GIN，向量先按文档压测选择 HNSW 参数，Relation 使用 source/target/relation_type 组合索引。
- 对称关系（DUPLICATES、CONFLICTS_WITH）在写入前规范化稳定 ID 顺序，并用唯一约束阻止反向重复。
- 可变 Aggregate 使用 `version` 乐观锁；Proposal 使用 `base_versions`/Change Hash；Node、Tool、Review Answer 使用作用域内幂等键。

## 禁止模式

- 在业务代码里拼接 SQL、表名或排序字段；不得把用户输入作为 SQL 标识符。
- 用 ORM/手写 Map 代替已确定的 sqlc 查询边界，或把 sqlc 生成类型泄漏到领域层。
- 在 HTTP Handler 中开启跨模块事务，或把文件/Git 网络调用放进数据库事务长期占用连接。
- 通过删除历史记录、覆盖 Revision 或静默修改状态“修复”冲突；正式知识变更必须经过 Proposal/Approval/Safe Writeback。
- 无约束地 `SELECT *`、无分页大结果、循环查库、逐条远程 Embedding 或无界 JSONB 索引。
- 混用不同维度或版本的 Embedding；将向量相似度直接写成 Confirmed Relation。
- 修改已发布迁移、使用破坏性回滚命令或把本机 PostgreSQL 版本当成项目锁定事实。

## 验证方式

### M0 当前（仅规范）

```bash
rg -n 'T(BD)|To[[:space:]]+be[[:space:]]+filled' .trellis/spec/backend
git diff --check
```

### M1 代码与迁移落地后

- Goose 从空数据库执行全部迁移，再次执行不会产生未处理错误。
- PostgreSQL/Testcontainers 测试覆盖外键、唯一键、乐观锁、幂等、Outbox、租约、FTS、pgvector 和分页稳定性。
- `EXPLAIN`/容量测试证明核心 Search、Collection、Graph、Workflow 查询使用预期索引；性能门槛以 [`performance.md`](../../../docs/architecture/performance.md) 和任务验收为准。
- 重复消息、重复审批、重复 Tool Call、重复 Answer 和 Health Scan 不产生重复副作用。

## 待 M1 代码验证

- sqlc、Goose、River 和 pgvector 的具体版本及配置文件位置；当前仓库没有 manifest 或 lockfile。
- 最终数据库 Schema 组织方式、字段长度、时间类型和所有索引名称。
- 连接池参数、迁移执行入口和 Testcontainers 版本。
- 中文 FTS 配置、向量维度、HNSW 参数以及 50 万数据容量结果。

## M5 Ingestion Projection Contract

### 1. Scope / Trigger

- Trigger：新增 `ingestion.attempt`、`parse_projection`、`source_version_projection`、`source_span` 和 `canonical_chunk` 迁移及 Repository。

### 2. Signatures

- `Repository.SaveProjection(ctx, ProjectionWrite) (ProjectionResult, error)` 必须在一个 PostgreSQL 事务内完成 Projection、Provenance、Span 和目标 Chunk Strategy。
- `Repository.GetProjection(ctx, projectionID, chunkStrategyVersion, schemaVersion)` 只返回请求的 Chunk 策略。

### 3. Contracts

- Parse Projection 唯一键：`content_artifact_id + parser_id + parser_version + parser_config_hash + schema_version`。
- Canonical Chunk 唯一键：`parse_projection_id + chunk_strategy_version + schema_version + sequence`。
- Span/Chunk 的 parser/schema 版本必须与 Parse Projection 一致；Workspace、Artifact、Projection、Span 归属由触发器交叉校验。
- Projection/Span/Chunk 只能 INSERT；Attempt 只允许显式状态转移和 `version=old+1`。

### 4. Validation & Error Matrix

- `23505` → `INGESTION_RECORD_CONFLICT`/版本冲突。
- `23503`、`23514`、版本交叉不一致 → 一致性错误，不自动重试。
- `40001`、`40P01` → 保留 `RetryableFailure`，Attempt 的 `retryable` 必须为 true。
- Source Version 与 Artifact 哈希/大小不一致 → 读取前拒绝，不创建成功投影。

### 5. Good / Base / Bad Cases

- Good：两个 Source Version 通过 `source_version_projection` 共享同一 Parse Projection，并保留各自 Provenance。
- Base：同 Projection 的新 Chunk Strategy 追加新 Chunk 集，旧 Strategy 不被更新或删除。
- Bad：以 `parse_projection_id` 单独查询 Chunk，或把不同策略的 Chunk 混在一次响应中。

### 6. Tests Required

- 空库执行全部 Up 两次；PostgreSQL race 集成测试。
- 并发/重复 `SaveProjection`、策略隔离、版本交叉约束、不可变更新/删除拒绝。
- Query 使用显式列和参数绑定；事务失败后无半批次 Span/Chunk。

### 7. Wrong vs Correct

```sql
-- Wrong: WHERE parse_projection_id = $1
-- Correct: WHERE parse_projection_id = $1
--         AND chunk_strategy_version = $2
--         AND schema_version = $3
```

## M5 Write Authorization Contract

### 1. Scope / Trigger

- Trigger：Proposal/Revision 已获 Approval 后，需要为 `ApplyApprovedPatch` 或 `CreateGitCommit` 签发服务端短期写权限。
- Scope：本契约只记录授权事实、过期/撤销和一次性消费；文件/Git/索引副作用由 M5-04 Safe Writeback Saga 执行。

### 2. Signatures

```go
CreateAuthorization(context.Context, domain.ToolAuthorization) (domain.AuthorizationIssueResult, error)
GetAuthorization(context.Context, foundation.ID, string, string) (domain.ToolAuthorization, error)
ConsumeAuthorization(context.Context, domain.AuthorizationConsume) (domain.AuthorizationConsumeResult, error)
RevokeAuthorization(context.Context, foundation.ID, time.Time) error
```

数据库表为 `change_control.tool_authorization`。原始 Credential 只在首次签发返回，数据库只保存 SHA-256 `token_hash`。

### 3. Contracts

- 授权绑定 Workspace、Workflow Run/Node、Proposal、Revision、Approval、Tool、Capability、Scope、Change Hash、Target Version、幂等键和最大 5 分钟 TTL。
- `workspace_id + idempotency_key` 和 `token_hash` 均唯一；同幂等键只有完整身份和 TTL 一致时才是重放。
- PostgreSQL `CURRENT_TIMESTAMP` 是签发、过期、消费和撤销的可信时钟；Application Clock 不得覆盖数据库生命周期判断。
- `GetAuthorization` 会在行锁事务内把已到期 `issued` 持久化为 `expired`，然后才允许 Application 进入目标快速检查。
- 消费先使用 `domain.ValidateAuthorizationConsumeBinding` 校验完整绑定；Repository 在 `SELECT ... FOR UPDATE` 后复用同一领域规则并校验当前 Approval/Workflow 状态。
- 当前文件哈希只作快速失败检查且不得在消费路径修改 Proposal；文件不进入数据库事务，Safe Writeback 必须在原子替换点最终 CAS。
- 消费成功只代表授权事实，不代表文件/Git/索引写回成功；跨存储失败进入后续 Saga/补偿。

### 4. Validation & Error Matrix

| 条件 | 错误码 | 状态变化 |
|---|---|---|
| 凭据或任一不可变绑定不匹配 | `WRITE_AUTHORIZATION_BINDING_CONFLICT` | 无 |
| Proposal/Approval/Revision 不再有效 | `WRITE_AUTHORIZATION_APPROVAL_REQUIRED` | 无 |
| Workflow Run/Node 不可运行或跨 Workspace | `WRITE_AUTHORIZATION_WORKFLOW_CONTEXT_INVALID` | 无 |
| DB 时间达到 `expires_at` | `WRITE_AUTHORIZATION_EXPIRED` | `issued → expired` |
| 已撤销 | `WRITE_AUTHORIZATION_REVOKED` | 无 |
| 同幂等键绑定或 TTL 不同 | `WRITE_AUTHORIZATION_IDEMPOTENCY_CONFLICT` | 无 |
| 当前文件基线变化 | `TARGET_BASE_HASH_CONFLICT` | 消费路径不修改 Proposal；M5-04 CAS 再次判定 |

### 5. Good / Base / Bad Cases

- Good：相同完整绑定并发消费，只有一次 `issued → consumed`，另一次返回同一 consumed 记录且 `replayed=true`。
- Base：已 consumed 的同绑定请求在 Proposal 后续进入 completed 后仍可重放，不重新读取目标或执行副作用。
- Bad：仅凭 Workspace + Idempotency Key 读取授权后就读取调用方指定目标、标记 Needs Revision，最后才校验 Credential。
- Bad：Application 时间生成 `issued_at`、数据库时间生成 `consumed_at`，导致合法授权因时钟偏差违反时间约束。

### 6. Tests Required

- Domain/Application：完整绑定字段篡改、错误 Credential、Hash 大小写、过期/终态在目标读取前短路、TokenHash 出口清空。
- PostgreSQL：空库 Up 两次、外键/触发器、终态直插拒绝、时间顺序、TTL 上限、不同 TTL 幂等冲突、撤销/过期、并发单消费和 terminal replay。
- Safe Writeback（M5-04）：消费前快速检查通过后并发修改目标，最终写入点 CAS 必须拒绝且文件/Git/索引无副作用。

### 7. Wrong vs Correct

```text
Wrong: Application Clock 写 issued_at，DB Clock 写 consumed_at。
Correct: PostgreSQL CURRENT_TIMESTAMP 统一授权生命周期时间。

Wrong: 先 MarkNeedsRevision，再校验 TokenHash/Proposal/Scope 完整绑定。
Correct: 完整绑定与终态先校验；消费快速失败路径不修改 Proposal；写入点由 M5-04 CAS。
```
