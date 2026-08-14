# 数据库开发规范

## 适用范围

适用于 PostgreSQL、pgvector、Goose 迁移、pgx 参数化查询、River 任务表以及各领域模块的 Repository/Projection 实现。仓库当前尚未配置 sqlc，Repository 沿用显式列、参数化手写 SQL；若后续引入 sqlc，必须作为独立迁移任务验证生成与兼容性。

## 已确认事实

- M4-A 已锁定 River/riverpgxv5 `v0.40.0` 与 Goose `v3.27.0`。`cmd/migrate` 通过嵌入 SQL 的 Goose Provider 执行项目迁移，再以 `Schema:"workflow"` 执行 River Up/Validate；两套 history 独立，单一 advisory lock 覆盖整个入口。
- `migrations/00001`–`00010` 的 Goose 兼容只在内存 `fs.FS` 中注入 StatementBegin/End；不得改写历史 SQL。没有 Goose history 的旧 shell-runner 数据库只有在完整 `core.schema_meta` 事实匹配时才能 baseline 接管。
- `workflow.run`、`workflow.node_run`、`workflow.outbox_event` 的 M4-A Runtime identity 字段允许完整 NULL 的 legacy tuple 或完整非 NULL 的 Runtime tuple；active legacy 行由新 Runtime Application/UoW 返回 `WORKFLOW_LEGACY_RUNTIME_UNSUPPORTED`，数据库不猜测回填。

- PostgreSQL 是领域数据、投影和运行数据的主数据库；pgvector 保存向量，PostgreSQL FTS 保存全文索引（依据 [`system-design.md`](../../../docs/architecture/system-design.md)）。
- PostgreSQL 驱动为 pgx，当前 SQL 访问采用参数化手写查询，迁移采用 Goose，River 只负责可运行 Job 的投递和 Worker 获取，不是 Workflow 业务事实源（依据 [`system-design.md`](../../../docs/architecture/system-design.md) 与 [`ai-runtime.md`](../../../docs/architecture/ai-runtime.md)）。
- 事务边界由维护不变量的领域模块控制：Proposal/Approval、Workflow Node、Review Answer、Relation Confirm 等在数据库内使用 ACID；文件和 Git 不放入数据库事务。
- Source Version、Article Revision、Proposal Revision、Workflow Definition/Run、Embedding/Index Version 等必须有版本、哈希或状态约束；重复消息不得创建重复 Node、Tool Call、Answer 或 Health Issue。
- 查询必须支持稳定排序和 cursor 分页；大集合、图谱邻居和 Collection 结果禁止无分页返回（依据 [`application-contracts.md`](../../../docs/architecture/application-contracts.md)）。

## 目标代码落点（M1 起）

- `migrations/`：Goose 前向迁移、约束、索引和必要的数据回填。
- `internal/platform/postgres/`：pgx 连接池、sqlc 生成代码入口、Repository/Projection Adapter、事务辅助函数。
- 各领域模块（如 `internal/changecontrol/`、`internal/workflow/`、`internal/review/`）：定义 Repository/Store Interface 和事务不变量；不得暴露 sqlc 类型。
- `internal/workflow/`：River Job 与 Workflow Node 的映射、租约和 Outbox 协作。
- 数据库集成测试与 Testcontainers Fixture 的实际路径由 M1 测试布局确认；本规范不虚构文件名。

## 查询模式

1. SQL 必须显式列出字段并参数化，避免生产路径使用 `SELECT *`；若采用 sqlc，生成类型不得泄漏到领域层。
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
- 用 ORM/手写 Map 绕过已定义的 Repository 查询边界，或把未来的 sqlc 生成类型泄漏到领域层。
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
- `EXPLAIN`/容量测试证明核心 Search、Collection、Graph、Workflow 查询使用预期索引；性能门槛以 [`quality.md`](../../../docs/architecture/quality.md) 和任务验收为准。
- 重复消息、重复审批、重复 Tool Call、重复 Answer 和 Health Scan 不产生重复副作用。

## 待 M1 代码验证

- sqlc、Goose、River 和 pgvector 的具体版本及配置文件位置；当前仓库没有 manifest 或 lockfile。
- 最终数据库 Schema 组织方式、字段长度、时间类型和所有索引名称。
- 连接池参数、迁移执行入口和 Testcontainers 版本。
- 中文 FTS 配置、向量维度、HNSW 参数以及 50 万数据容量结果。

## Scenario: Model Runtime Hot Activation Persistence

### 1. Scope / Trigger

- 修改 `00079_model_settings_hot_activation.sql`、Model Settings activation Repository、runtime/participant ownership、
  Workflow Claim binding 或相关恢复逻辑时，必须应用本场景，并同时读取 `model-settings-runtime.md`。

### 2. Signatures

- 持久事实为 `ops.model_settings_state`、`ops.model_settings_runtime`、
  `ops.model_settings_rollout_participant` 与 `workflow.node_attempt` 的 runtime binding。
- state phase 固定为 `idle|preparing|arming|activating|failed`；participant phase 固定为
  `preparing|prepared|armed|activated|failed|aborted|retired`。
- Repository 只通过 Start、participant transition、Commit、Acknowledge、Finalize、Fail/Recover 和 Claim command
  修改协议事实；HTTP、Controller、CLI 与 shell 不直接拼状态 SQL。

### 3. Contracts

- 所有 activation 事务锁顺序固定为 singleton state -> runtime `api,worker` -> participant `api,worker`；
  takeover 和 recovery 不得采用相反顺序。
- target/previous/rollout binding 在 live phase 内不可变；active 只能在 `arming -> activating` 一次更新为 target。
  commit 前失败先把非终态 participant 原子转 aborted，再转 failed；commit 后只允许向 target finalize。
- `arming -> activating` 由数据库 trigger 使用 `clock_timestamp()` 验证两个 role 的 serving owner、previous applied、
  armed participant 和非未来 fresh heartbeat。应用进程时间不能替代该提交不变量。
- runtime/participant 更换 instance 只允许旧 owner 按数据库时间 stale；fresh owner、future heartbeat、revision jump、
  缺 role、owner mismatch 或未 armed 的 raw SQL 都必须 fail closed。
- 新 Workflow Attempt 在 Claim 事务内按 state -> Worker runtime 锁序选择 fresh serving revision/instance 并冻结；
  exact delivery 先返回持久 binding。caller 不传 revision，激活时不得批量改写历史 Attempt。
- revision `0` 是存在于状态/Attempt binding 中但没有 revision 行的 canonical disabled；正 revision 才能从历史设置重建。
  迁移、FK helper 与 Claim 必须显式保留该语义，不能把 0 当缺失值。
- `00079` 是 forward-only mixed-binary boundary：存在 legacy live rollout 时 Up 返回 `55000`；存在受保护热激活历史时
  Down 必须 fail closed。不得修改已发布迁移来假设旧环境会重跑。

### 4. Validation & Error Matrix

| Condition | Required result |
| --- | --- |
| 双 role 未 fresh/armed、owner mismatch 或 future heartbeat 时提交 | PostgreSQL 拒绝，active 保持 previous |
| fresh runtime/participant owner 被另一个 instance 接管 | PostgreSQL 拒绝；达到 DB-time stale 窗口后才允许受限恢复 |
| live activation 改 target/previous/rollout 或 desired | CAS/trigger 拒绝，持久 binding 不变 |
| activating 后尝试回到 previous/failed | 拒绝；只允许 acknowledge target 并 finalize idle |
| 新 Claim 看见 stale/mismatch Worker runtime | 不创建新 Attempt，返回稳定 runtime unavailable/conflict |
| exact delivery 重放 | 返回原 Attempt binding，不从当前 active 重新选择 |
| legacy live rollout 升级或已有 participant 历史 Down | SQLSTATE `55000`，schema/data/version 保持可恢复 |

### 5. Good / Base / Bad Cases

- Good：两端 prepared/armed 后单次提交 active；响应丢失时相同 target 重放或恢复最终收敛，Attempt 全部绑定完整 old 或 target。
- Base：revision 0 disabled 可作为 active/applied/Attempt binding 启动和切换，但永不伪造对应 revision row。
- Bad：只用 Go mutex 保护跨进程提交、按进程时钟判断 stale、先锁 participant 再锁 state、或 SQL 手改 active 完成发布。

### 6. Tests Required

- 真实 PostgreSQL `-race -p 1` 覆盖 fresh/legacy migration、raw SQL guard、双 role commit、重复 Start、pre/post-commit
  recovery、runtime/participant stale takeover、future heartbeat 和 version/owner CAS。
- Workflow PostgreSQL 测试覆盖 exact replay、新 Attempt 的 DB-selected binding、非默认 freshness 窗口和 Claim/cutover 竞态。
- Migration 测试必须直接执行非法 INSERT/UPDATE，证明数据库约束独立于 Repository；只做 Go fake 不算协议证据。

### 7. Wrong vs Correct

```text
Wrong: Coordinator 先更新 active，再等待 API/Worker 报告 applied。
Correct: 两端 fresh/armed 后由 arming -> activating 事务单点发布；提交后只向前恢复。

Wrong: Workflow caller 把当前 revision 放进 Claim，或重放时重新读取 active。
Correct: 新 Attempt 在 PostgreSQL 事务内选择 fresh Worker binding；exact delivery 返回持久 binding。

Wrong: 用 time.Now 判断可接管 owner，或允许 raw SQL 跳过 participant 状态机。
Correct: trigger/Repository 都以数据库时间和 version/owner CAS fail closed，并由真实 PostgreSQL 负测锁定。
```

## Scenario: M7-03 Collection / Health Persistence Contract

### 1. Scope / Trigger

- 适用于 `migrations/00025`–`00029`、`internal/collection`、`internal/health`、SMART_COLLECTION Candidate scan、
  schedule delivery、affected-change outbox，以及 Collection result/preview revision 字段的任何修改。

### 2. Signatures

- Collection query：`PlanDurableScan(workspace_id, collection_id)`、`ReadDurableScanPage(binding, after, limit)`。
- Collection result/preview：同时返回 64 位十六进制 `revision_hash` 与 `scan_revision_hash`。
- Health scan：`Start(scope snapshot, detector coverage, max_items, idempotency_key)`，Scan/Issue/Workflow 状态均以
  PostgreSQL 持久行读取。
- Schedule command：Workspace + Idempotency-Key + request hash + command type + complete response snapshot。

### 3. Contracts

- SQL 标识符只能来自 `collection-query/v1` registry；值全部参数化，Workspace predicate 必须出现在每个 root。
- Collection receipt 保存历史 snapshot；replay 不读取当前 Collection。结果 cursor 绑定 Workspace、query/version/hash、
  sort、limit、read-model revision 和最后 key。
- `revision_hash` 绑定页面成员、排序和固定批量 hydration，必须包含 active Health revision；`scan_revision_hash`
  绑定 durable scan membership，只在 Query predicate/sort 引用 `health_issue_type` 时包含 Health revision。两者必须从
  同一 repeatable-read snapshot 的单次 O(1) Workspace revision-vector 查询计算，禁止为第二个 hash 再查一次数据库。
- Health Issue 以 `identity_hash` 找回、以 `fingerprint` 判断 unchanged/reopen；detector 完整 coverage 之前不得
  自动 resolve。Schedule 默认 DISABLED，due/lease 使用 DB time + `FOR UPDATE SKIP LOCKED`。
- Smart Collection scope 必须同时绑定 collection version、query hash、read-model revision 和 exact count；任何漂移
  返回 stale/invalid，不退化为 Workspace scope。

### 4. Validation & Error Matrix

| 条件 | 结果 |
|---|---|
| 未知字段、动态排序、超深/超大 AST | `COLLECTION_*` invalid，拒绝 SQL 生成 |
| receipt schema/hash/Workspace/snapshot 篡改 | consistency error，fail closed |
| scope version/query/revision/count 漂移 | stale/invalid，不创建新的 Candidate/Scan |
| Health 输出改变 hydration，但 Query 不引用 `health_issue_type` | `revision_hash` 改变，`scan_revision_hash` 保持；继续 durable scan |
| Query 引用 `health_issue_type` 后 Health revision 改变 | 两个 hash 都改变，旧 scan binding 返回 stale |
| partial/failed/cancelled detector | 保留 Issue，不执行 missing-set resolve |
| pending schedule 或重复 key 不同 payload | conflict；相同 payload exact replay |
| guarded Down 存在业务行 | SQLSTATE `55000`，不得静默删除 |

### 5. Good / Base / Bad Cases

- Good：统一 read model 先分页，再批量 hydration；同一次 revision-vector 读取派生 page/scan 两个 hash，后继
  detector page 使用 `scan_revision_hash` 重新验证 durable revision。
- Base：Tag/Review/Directory owner 或无 apply seam 的 repair 返回 capability unavailable，不写空事实。
- Bad：用 `revision_hash` 启动 Health scan、为两个 hash 执行两次 revision 查询、用 JSON AST 直接拼 SQL，或把
  River job 当 Scan/Issue 状态。

### 6. Tests Required

- 空库/重复 Up、空数据 Down→Up、业务数据 guarded Down；Workspace 复合约束和 append-only receipt trigger。
- Collection lifecycle exact replay/CAS、全字段 operator、cursor invalid/stale、统一结果 refs 和 EXPLAIN/index。
- 回归必须证明：Health Issue 写入会使 `revision_hash` 变化；不依赖 Health membership 的 `scan_revision_hash` 与
  `PlanDurableScan().ReadModelRevision` 相等且保持稳定；依赖 `health_issue_type` 的旧 binding 明确 stale；query-count
  契约仍只有一次 O(1) revision 查询。
- Health unchanged/reopen/resolve、typed target cursor、schedule claim/reclaim/missed-once/exact replay、affected-change
  outbox rollback/response-loss，以及 SMART_COLLECTION 跨页 drift fail-closed。

### 7. Wrong vs Correct

```text
Wrong: 前端把含 Health hydration 的 `revision_hash` 作为 durable scan binding，Health 写出 Issue 后扫描被自身稳定打断。
Correct: cursor/page 使用 `revision_hash`；Health/Semantic durable binding 使用服务端同 snapshot 返回的
`scan_revision_hash`，并由 PostgreSQL 在每页验证，River 只投递并可重放。
```

## M5-05 Knowledge Domain Contract

### Scope

- Migration `00017_knowledge_domain.sql` owns Topic、Claim、Claim Source、Relation、Relation Evidence、
  Conflict、Conflict Member 和 Knowledge Command Receipt。
- PostgreSQL 保存领域事实和确认历史；Graph/Collection 只保存可重建投影，不形成 Relation 第二事实源。

### Persistence Rules

- 不建通用 Evidence 表或可独立写入的 `topic_claim`；Topic–Claim 归属只使用 `BELONGS_TO` Relation。
- Claim Source/Relation Evidence 必须同时保存 `source_version_id + source_span_id`，并通过固定 JOIN/trigger
  证明 Workspace → Source Version → Content Artifact → Source Version Projection → Source Span。
- Relation NodeType 首期只允许 `TOPIC|CLAIM`；兼容矩阵在 Domain 中唯一维护，数据库函数镜像最小防线。
- `DUPLICATES|CONFLICTS_WITH` 写入前 canonicalize，数据库唯一键防止正反/并发重复。
- Confirmed Claim 至少一条 SUPPORTS Claim Source；Confirmed Relation 至少一条可达 Evidence 和有效确认。
- Claim 退出 SUGGESTED/CONFIRMED/DISPUTED 生命周期前，所有关联的 SUGGESTED/CONFIRMED Relation 必须先进入历史状态；提交时 deferred constraint 兜底。
- Topic 退出 ACTIVE 前遵循同一规则；Relation 激活时使用与生命周期 UPDATE 冲突的端点共享锁，禁止并发创建有效边绕过 deferred 检查。
- Conflict 在提交时至少两个不同 Claim；OpenConflict 与 Member 插入、Claim→DISPUTED 在同一事务。
- Applicability 是版本化 canonical JSON object；数据库验证 JSON 类型/hash 格式，语义比较由 Domain 负责。
- 可变聚合 `version=expected+1`；命令 receipt 只保存 request hash 与 aggregate binding，不缓存第二份响应事实。
- 有任一 Knowledge 业务数据时 `00017 Down` 必须返回 SQLSTATE `55000`；发布回滚保留数据并 forward fix。

### Required Tests

- 空库/重复 Up、空数据 Down→Up、有数据 guarded Down、迁移版本和旧 Down 顺序。
- Provenance 错绑/跨 Workspace、自环/非法端点组合、无 Evidence Confirm、Conflict 少成员。
- 对称正反/并发去重、CAS、同幂等键不同请求、response-loss replay 和事务半失败回滚。
- 批量 Claims/Relations/Evidence 查询使用显式列、固定上限和稳定排序，不逐 owner N+1。

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

## M5 Safe Writeback Persistence Contract

### 1. Scope / Trigger

- Trigger：已批准 Proposal 需要进入文件/Git Saga 前，必须先建立可恢复的 Durable Writeback Execution；Git Commit 后必须原子发布 Mapping 与重索引 Outbox。
- Scope：本契约只覆盖 Approval Git HEAD、Proposal 乐观锁、Execution/Mapping/Outbox 持久化，不执行文件、Git 或真实索引副作用。

### 2. Signatures

```go
BeginWriteback(context.Context, domain.BeginWriteback) (domain.WritebackExecution, error)
CreateWritebackExecution(context.Context, domain.CreateWriteback) (domain.WritebackExecution, error)
GetWritebackExecution(context.Context, foundation.ID) (domain.WritebackExecution, error)
CheckpointWritebackExecution(context.Context, domain.CheckpointWriteback) (domain.WritebackExecution, error)
PublishWriteback(context.Context, domain.PublishWriteback) (domain.PublishWritebackResult, error)
ValidateWritebackLease(context.Context, foundation.ID, string) error
FinalizeWritebackCleanup(context.Context, foundation.ID, int64, time.Time) (domain.WritebackExecution, error)
```

数据库事实源：`change_control.writeback_execution`、`change_control.proposal_commit` 与 `workflow.outbox_event`。

### 3. Contracts

- Atomic Begin 必须按 Authorization ID 固定顺序加锁，使用数据库时间验证两份 `issued` 授权与 running Node lease，在同一事务内消费双授权、创建 `prepared/version=1` Execution 并推进 Proposal `approved → applying`。已 `consumed` 授权只允许返回已存在的 exact Execution；没有 Execution 时必须返回 `WRITEBACK_AUTHORIZATION_ALREADY_CONSUMED`。
- Legacy Create 仍用于历史兼容；新 Saga 不得通过 Create + 两次 Consume 绕过 Atomic Begin。
- `(workspace_id,idempotency_key)` 与 `(proposal_id,revision_id)` 只允许一个逻辑 Execution；同完整身份重放已有记录，不同身份返回冲突。
- Checkpoint 只允许领域状态机下一跳且 `version=old+1`；`file_prepared` 必须一次写入 temp/backup locator、byte size、mode 与原始目标/Result/backup 三个 opaque identity token；`git_prepared` 写入 Diff/Blob/Mode；`git_committed` 写入 Git Commit/Parent/Diff 并同步 Proposal `applying → applied`。
- Publish 在一个事务内写 Proposal Commit、Execution `→ verifying`、Proposal `applied → verifying` 与 Outbox；任一步失败全部回滚。
- Reindex Outbox 固定类型 `retrieval.revision.reindex_requested`，幂等键为 `reindex:<workspace>:<proposal>:<revision>:<commit>`，`schema_version` 固定整数 `1`。
- Payload 只能有：`schema_version`、`workspace_id`、`workflow_run_id`、`node_run_id`、`proposal_id`、`revision_id`、`approval_id`、`writeback_execution_id`、`target_path`、`result_hash`、`git_commit`；正文、Credential、Token、Embedding/向量维度和绝对路径禁止入库。
- 原始 Credential 长度最多 256 bytes，只在 Begin 调用栈中 SHA-256；数据库只读既有 `token_hash`，Execution/Outbox/日志/API/Workflow payload 不得包含原始值。
- `file_lock_token/file_result_lock_token/file_backup_lock_token` 是 64 位小写 SHA-256 identity 摘要，三者必须不同且写入后不可变；它们不作为授权 Secret，也不得进入公开输出。
- 锁顺序固定为 Authorization → Proposal/Revision/Approval → Workflow，必须与 Authorization Consume 一致，避免 Create/Consume 交叉死锁。
- Approval `approved_git_head` 使用 nullable expand 保留历史记录；NULL 可读但 Execution Trigger 必须拒绝 Apply。新审批的 Git Inspect/clean/HEAD 注入由 M5-04D Application/Git seam 完成。

### 4. Validation & Error Matrix

| 条件 | 稳定错误码/分类 | 数据变化 |
|---|---|---|
| Create 缺字段、路径/Hash/Git HEAD 非法 | `WRITEBACK_INVALID` / InvalidInput | 无 |
| 幂等键、Proposal Revision 或 Authorization 已绑定不同身份 | `WRITEBACK_IDENTITY_CONFLICT` / VersionConflict | 无 |
| 两份授权已被旧路径消费但不存在 exact Execution | `WRITEBACK_AUTHORIZATION_ALREADY_CONSUMED` / PermissionDenied | 无；Proposal 保持 approved |
| Node ID 被当作 Execution ID、owner 不匹配或 lease 过期 | `WRITEBACK_LEASE_LOST` / VersionConflict | 无副作用 |
| Checkpoint Expected Version 过期 | `WRITEBACK_VERSION_CONFLICT` / VersionConflict | 无 |
| 非法状态迁移 | `WRITEBACK_STATUS_CONFLICT` / VersionConflict | 无 |
| Mapping/Outbox 字段或 Payload 不匹配 | `WRITEBACK_PUBLISH_BINDING_CONFLICT` / ConsistencyViolation | 事务回滚 |
| PostgreSQL `40001` / `40P01` / 连接瞬断 | 原操作码 / RetryableFailure | 事务回滚，可按幂等记录重试 |
| Commit 已存在但 Mapping/Outbox 缺失 | 保持 `git_committed`/`publish_recovery_required` | 不恢复文件，不重复 Commit |

### 5. Good / Base / Bad Cases

- Good：相同 Begin/Publish 请求重复执行，返回同一 Execution/Mapping/Outbox，不重复消费授权、不增加版本或第二副作用。
- Base：历史 Approval 的 Git HEAD 为 NULL 时仍可查询 Proposal，但创建 Execution 被数据库拒绝，要求重新审批。
- Base：旧 Execution 没有 M5-04D identity token 仍可查询；新 `file_prepared` 必须写齐三个 token，活动恢复记录禁止 Down 删除字段。
- Bad：先通过旧 ConsumeAuthorization 消费两份授权，再用同凭据首次 Begin 并创建 Execution。
- Bad：Commit 后分别提交 Mapping、Proposal 状态与 Outbox；中途失败会形成无法证明的完成状态。
- Bad：Create 先锁 Proposal 再锁 Authorization，而 Consume 先锁 Authorization 再锁 Proposal；并发时可形成死锁环。

### 6. Tests Required

- Domain：Proposal/Writeback 状态机、完整身份、Result Hash 不可变、Publish Payload 精确字段和 schema version。
- PostgreSQL：空库全部 Up 两次、空数据 Down→Up、活动 checkpoint Down 返回 SQLSTATE `55000`；Atomic Begin 双消费/rollback/concurrent replay、consumed-without-execution 拒绝、lease 只接受 Execution ID、file identity token round-trip/不可变、checkpoint、publish/replay。
- SQL 负测：交叉 Workspace/Run/Node/Approval/Authorization 绑定、非法状态/version、Execution/Mapping/Outbox update/delete、敏感或额外 Payload 字段。
- 并发：Create Execution 与 Consume Authorization 使用真实连接并发至少 20 轮，`-race` 下无 `40P01`、超时或重复副作用。

### 7. Wrong vs Correct

```text
Wrong: 两次 Consume Authorization → Create Execution；任一步崩溃都会形成已消费但不可恢复的偏态。
Correct: Atomic Begin 同事务消费两份 issued Authorization、验证 lease、create/replay prepared Execution。

Wrong: Git Commit、Mapping、Proposal verifying 和 Outbox 分事务提交。
Correct: Commit 之后的数据库发布在一个 PostgreSQL 事务中原子完成；失败保留 Commit并从持久化检查点恢复。
```

## M4-C Approval Dispatch Contract

### 1. Scope / Trigger

- Trigger：Approved Proposal 需要原子创建唯一 Workflow Run/Node/River Job，并由 Worker 恢复或创建 Safe Writeback Execution。
- Scope：只覆盖 Approval Dispatch、Proposal→Run binding、pre-Begin Bootstrap 和持久 Runtime payload；真实进程 `kill -9`、River rescue 参数和日志/metrics/trace 扫描由 M4-D Worker Operability 契约负责。

### 2. Signatures

```go
type Dispatcher interface {
    DecideAndDispatch(context.Context, dispatch.Command) (dispatch.Result, error)
}

func (r *RuntimeRepository) StartTx(
    context.Context,
    pgx.Tx,
    application.RuntimeStartRequest,
) (application.RuntimeStartResult, error)

func (r *Repository) FindWritebackExecutionByKey(
    context.Context,
    foundation.ID,
    string,
) (domain.WritebackExecution, bool, error)
```

数据库 binding 为 `change_control.proposal.workflow_run_id uuid NULL`，引用 `workflow.run(id)`。

### 3. Contracts

- `proposal.workflow_run_id` 通过 `(workflow_run_id,workspace_id) → workflow.run(id,workspace_id)` 复合 FK 只允许 NULL→唯一同 Workspace Run，禁止 INSERT 预绑定、解绑、换绑或在绑定后移动 Run Workspace；历史 Proposal 保持 NULL。
- Approved 首次决定或历史未绑定补建必须先在事务外通过 Target Hash 与 strict Git snapshot；完整绑定 exact replay 不读取文件/Git。
- `Dispatcher` 在一个调用方持有的 pgx transaction 内保存 Approval、Proposal binding、固定 Definition/Run/Node、版本化 Workflow Outbox 与 River `InsertTx` Job；Rejected 不创建 Workflow 事实。
- 固定 Node input 只允许 `schema_version/proposal_id/revision_id/approved_change_hash`；River Args 只允许 `schema_version/node_run_id/dispatch_no`。
- Worker Claim 后使用 `safe-writeback:<node_run_id>` exact lookup；不存在才签发新的瞬时双授权并 Atomic Begin。原始 Credential 永不持久化，数据库只保存 Authorization token hash。
- 正文、相对路径和 locator/identity token 只能存在于拥有恢复事实的 Change Control Proposal/Execution 记录，不得复制到 Runtime Job、Node input、Attempt、dispatch Outbox 或错误摘要。

### 4. Validation & Error Matrix

| 条件 | 稳定错误/分类 | 结果 |
|---|---|---|
| Proposal/Revision/Workspace/Change Hash 不一致 | `APPROVAL_DISPATCH_BINDING_CONFLICT` / ConsistencyViolation | 全事务回滚 |
| 已有不同 Approval 决定 | `PROPOSAL_DECISION_CONFLICT` / VersionConflict | 不创建第二决定或 Run |
| 首次/补建安全观察缺失或与 Base/HEAD 不一致 | `APPROVAL_DISPATCH_SAFETY_CONFLICT` / VersionConflict | 不创建 Workflow |
| Runtime 已存在但 Approval 不存在 | `APPROVAL_DISPATCH_RUNTIME_ORPHANED` / ConsistencyViolation | 全事务回滚 |
| Proposal binding 与 Runtime replay 不一致 | `APPROVAL_DISPATCH_BINDING_CONFLICT` / ConsistencyViolation | 不改绑定 |
| Execution exact lookup 全绑定不一致 | `WRITEBACK_EXECUTION_BINDING_CONFLICT` / ManualRecoveryRequired | 不签发新 Credential、不开始副作用 |
| 更高 River attempt 遇到旧 delivery 未过期 lease | `WORKFLOW_LEASE_HELD` / RetryableFailure | River 保留唯一 Job，lease 到期后 reclaim |
| River/数据库暂时错误或死锁 | 对应 Retryable/Dependency 错误 | 调用方重试前先 exact lookup |

### 5. Good / Base / Bad Cases

- Good：两个并发 Approved 请求只提交一个 Approval、Run、Node、Workflow Outbox 和 River Job，另一个返回相同 binding 的 replay。
- Base：历史 Approved Proposal 的 `workflow_run_id` 为 NULL；重新通过文件/Git 安全门后补建唯一 dispatch。
- Bad：Handler 先提交 Approval，再单独调用 Workflow Start；或完整 binding replay 重新读取已被写回改变的文件/Git。

### 6. Tests Required

- PostgreSQL：并发唯一 binding、Rejected 无 Workflow、Runtime 失败全回滚、commit response-loss 后 exact replay、迁移 Up/Down/guard 和同 Workspace 约束。
- Application/HTTP：首次 201、exact replay 200、Approved 返回同一 status URL、Rejected 省略 Workflow 字段、完整 replay 不访问 FS/Git。
- Bootstrap：exact lookup、双授权、Begin response-loss、binding conflict Manual、授权 replay 无 Credential 时拒绝。
- 真实 Smoke：HTTP Approval → River Claim → Bootstrap → Safe Writeback → Workflow Complete；双 Worker只产生一个 Execution/Commit/Mapping/Reindex Outbox。
- 安全扫描：Runtime Job/Node/Attempt/dispatch Outbox/错误摘要不得含原始 Credential、正文、路径、Git 参数或 lock token；M4-D 另验真实 `kill -9` 和可观测性载荷。

### 7. Wrong vs Correct

```text
Wrong: Approval Commit → 另起事务 Start Workflow → Job Insert；中间失败后留下无法判断的半绑定。
Correct: 单一 pgx transaction 内 Approval + Proposal binding + Definition/Run/Node/Outbox + River InsertTx。

Wrong: 每次 delivery 都重签 Credential 并 Begin，或把 Credential 放进 River Args 方便恢复。
Correct: Claim 后先按 safe-writeback:<node_run_id> exact lookup；只有不存在才瞬时签发双授权并 Atomic Begin，响应丢失后再次 exact lookup。
```

## M6-B Reindex Consumer Persistence Contract

### 1. Scope / Trigger

- Trigger：Safe Writeback 已原子发布 `retrieval.revision.reindex_requested`，需要构建 committed
  Source/FTS Snapshot 并完成 Delivery、Execution 与 Proposal。
- Scope：M6-B 只实现 FTS-only Reindex；Embedding、Hybrid Search 与 Search API 不在本契约。

### 2. Signatures

```go
func NewDispatcher(
    db DispatcherDB,
    ids foundation.IDGenerator,
    jobs ReindexRiverInserter,
) (*application.Dispatcher, error)

func (r *Repository) BeginWorkspaceSnapshot(
    context.Context,
    domain.WorkspaceSnapshotCommand,
) (domain.WorkspaceSnapshotResult, error)

func (r *Repository) CompleteReindexTx(
    context.Context,
    domain.CompleteReindexCommand,
) (domain.CompleteReindexResult, error)
```

数据库入口为 `migrations/00015_reindex_consumer.sql`；River Args 固定为
`schema_version/delivery_id/dispatch_no`，Outbox Payload 固定为 Reindex v1 的 11 个字段。

### 3. Contracts

- Outbox v1 使用唯一 11 字段公共 codec；River Args 只允许 `schema_version/delivery_id/dispatch_no`。
- `published_at` 只证明 Dispatcher 与 River `InsertTx` 同事务成功；业务终态只看
  `retrieval.reindex_delivery.status`。Reindex unpublished Outbox 必须使用 event-type partial index，
  不能在大量其他事件中只依赖全局 unpublished 索引过滤。
- `reindex_delivery_attempt` append-only；Claim/Heartbeat/Checkpoint/Fail 使用数据库
  `clock_timestamp()`、完整 owner/attempt/version fence，并同时更新 heartbeat 与 lease。
- Snapshot 在 Workspace advisory lock + repeatable-read 中分页 Hash/Count 与分批 COPY；
  `index_manifest_source`/`index_manifest_chunk` 创建后不可变，超限整事务回滚。
- Processor 的确定性 Invalid/NotFound/NonRetryable 归约 failed，Version/Consistency/Manual 归约
  manual_recovery；连接、锁或 Commit 结果未知原样交同一 River dispatch 重投。
- `CompleteReindexTx` 固定锁序并在单事务切换唯一 Active、追加 Activation、完成 Delivery/
  Execution/Proposal；deferred invariant 在提交时验证 Outbox/Commit/Workflow/cleanup/Manifest/
  Regression/Activation 全绑定。
- 结构回归失败保留 Git Commit 和旧 Active，不自动反向 Commit；语义回滚必须创建新的
  Proposal/Approval。

### 4. Validation & Error Matrix

| 条件 | 稳定错误/归约 | 数据结果 |
|---|---|---|
| Outbox 缺字段、额外字段、未知版本或跨绑定 | `REINDEX_OUTBOX_CONTRACT_INVALID`，fatal/readiness=false | 不创建伪 Delivery，不设置 `published_at` |
| Commit/Path/Hash 或 committed blob 不一致 | Invalid/NotFound/NonRetryable → `failed` | 保留 Git Commit 与旧 Active |
| Delivery Fence、Version 或跨域绑定损坏 | Version/Consistency/Manual → `manual_recovery` | 阻止同 Workspace 后续自动 Reindex |
| 连接、锁或 Commit 结果未知 | 原错误返回 River transport 重投 | 不创建新的业务 retry generation |
| cleanup 或原 Workflow 尚未 succeeded | `REINDEX_COMPLETION_PREREQUISITE_PENDING` → `retry_wait` | Active/Execution/Proposal 不变 |
| Snapshot Source/Chunk 超过配置上限 | 稳定 capacity error | 整个 Snapshot 事务回滚，无部分 Manifest |
| Completion 任一步或 deferred invariant 失败 | 事务错误或稳定 binding error | Activation、Active、Delivery、Execution、Proposal 全部回滚 |

### 5. Good / Base / Bad Cases

- Good：同一 Outbox 并发派发只创建一个 Delivery/Job；响应丢失后重放既有 checkpoint，最终只产生一个 Activation。
- Base：历史 Active 没有 Source Manifest 时执行全量 eligible rebuild，不复制不完整的旧 Chunk 集合。
- Base：合法 legacy Outbox 的 Runtime tuple 全 NULL 时仍可通过 11 字段 payload 建立业务绑定。
- Bad：从漂移的工作树读取正文，或把正文、路径、Credential、Git 参数放入 River Args/日志。
- Bad：把 `published_at` 当作业务成功，或在事务外串联 Activate、Execution completed、Proposal completed。

### 6. Tests Required

- 00015 空库/重复 Up、空数据 Down→Up、有业务数据 Down `55000`。
- Dispatcher rollback/response-loss/并发，Delivery lease/fence/checkpoint，Snapshot capacity/
  incremental，Regression closure，Completion fault injection/replay。
- 真实 PostgreSQL/River smoke 覆盖 committed blob 与工作树漂移、四 checkpoint、Ready/
  Completion response-loss、双 Worker、单一 Activation/Active/Completion。

### 7. Wrong vs Correct

```text
Wrong: published_at 非空后直接把 Proposal/Execution 标 completed，或为每次 River redelivery 创建新 Delivery。
Correct: published_at 只表示 transport 已派发；Delivery checkpoint 与固定 Fence 驱动业务恢复和唯一完成事务。

Wrong: Commit 后从工作树重新扫描目标文件，再增量追加本次 Chunk。
Correct: 从指定 Commit 捕获受控 Blob；基于完整 Source Manifest 重建目标 Workspace Chunk 并集。

Wrong: 先 Activate，再分别提交 Delivery/Execution/Proposal；失败后依赖补偿修复半完成状态。
Correct: CompleteReindexTx 按固定锁序在单一事务中追加 Activation、切换 Active 并完成三方状态，提交时由 deferred invariant 统一验闭包。
```

## M4-D River Operability Contract

### 1. Scope / Trigger

- Trigger：API/Approval Producer 与 Worker Consumer 接入可配置 River queue、首次启动、进程停机、stuck rescue、trace metadata 和 Writeback cancel safety。
- Scope：River 只负责 transport；Workflow/Writeback PostgreSQL 事实、lease、checkpoint 和幂等约束仍由领域 Repository 控制。

### 2. Signatures

```go
riveradapter.NewClientWithOptions(*pgxpool.Pool, *riveradapter.Workers, riveradapter.Options) (*riveradapter.Client, error)
riveradapter.NewJobInserter(*riveradapter.Client) (riveradapter.JobInserter, error)
CancellationSafetyGuard.SafeToCancelWorkflowNode(context.Context, any, foundation.ID) (bool, error)
```

环境契约：`ZHIXU_WORKER_QUEUE`、`ZHIXU_WORKER_MAX_WORKERS`、`ZHIXU_WORKER_JOB_TIMEOUT`、`ZHIXU_WORKER_RESCUE_STUCK_AFTER`、`ZHIXU_WORKFLOW_LEASE`、`ZHIXU_WORKFLOW_HEARTBEAT`、`ZHIXU_WORKER_SOFT_STOP_TIMEOUT`、`ZHIXU_WORKER_HARD_STOP_TIMEOUT`。

### 3. Contracts

- API Producer 与 Worker Consumer 必须读取同一 `ZHIXU_WORKER_QUEUE`；`InsertTx` 必须显式写 `InsertOpts.Queue`，不能依赖 River `default`。
- 项目自有 Job metadata 只写合法 W3C `traceparent`。Consumer 必须允许 River 保留的 `river:*` recovery metadata（如 `river:rescue_count`）共存，但不得复制到 Application、日志、Metrics 或 Trace；其他字段 fail closed。
- `JobTimeout < RescueStuckJobsAfter`；真实 SIGKILL 后只能等待同一 Job 被 River rescue，不能手工插入第二 Job。
- Fresh database 的目标 `river_queue` 行由 River `Client.Start` 创建；普通 Worker 启动必须先启动 lifecycle，再调用
  `ResumeQueue`，两步都成功后才设置 River readiness。Resume 失败必须进入 shutting down，使用独立 hard-stop deadline
  graceful shutdown 已启动的 dispatcher/River，并关闭 health server；不得吞掉 `ErrNotFound` 或伪造 ready。
- `PauseQueue`/`ResumeQueue` 和候选进程的排空协议只保留给旧 process-replacement rollout 兼容路径或显式运维操作。
  它们不是 `00079` 后 managed Save/Apply 的正常协议：正常热应用在模型相关持久 Claim 前由 `RuntimeHost.Admit` 建立短暂
  admission fence，不持久暂停整条 River queue，也不等待已 Claim Job 或 generation lease 排空。兼容路径若要求已暂停
  queue 行，缺失行仍必须 fail closed，不能只跳过 Resume 后继续启动。
- Runtime lease/TTL fence 必须先按固定顺序取得参与判定的 Run、Node、Attempt 行锁，再调用 PostgreSQL
  `clock_timestamp()`；禁止用事务起点 `CURRENT_TIMESTAMP` 或锁前缓存时间比较 `lease_until`。锁等待跨过到期点后，
  Heartbeat/Transition 必须拒绝旧 owner，Claim 必须按到期后的当前数据库时间 reclaim。
- Writeback Execution 处于 `prepared/file_prepared/file_applied/git_prepared/git_committed/publish_recovery/compensating` 时，Cancel 只记录 request，继续 heartbeat/lease reclaim，并返回 retryable `WORKFLOW_CANCELLATION_DEFERRED`；只有安全失败、补偿、人工恢复、完成或已清理恢复证据后才允许 Run terminal cancelled。
- Cancellation guard 必须使用 Runtime 当前 pgx transaction 查询，保证 Control/Heartbeat/Transition 与 Execution checkpoint 判定原子。
- Compose PostgreSQL healthcheck 必须执行实际 `SELECT 1`；`pg_isready` 在数据库尚未创建时也可能报告 server accepting，不能作为 Migrate 前置门禁。

### 4. Validation & Error Matrix

| 条件 | 稳定错误/行为 |
|---|---|
| queue 空、非法字符、过长或 worker/timeout 非正 | `WORKFLOW_RIVER_OPTIONS_INVALID`，启动失败 |
| Producer/Consumer queue 不同 | Worker 不消费；Compose/集成测试必须阻断交付 |
| metadata 含非 `traceparent` 且非 `river:*` 字段 | `WORKFLOW_RIVER_TRACE_METADATA_INVALID` / NonRetryable |
| `river:rescue_count` 等保留字段 | 忽略并继续恢复，不向 Application 暴露 |
| Fresh database 在 `Client.Start` 前调用 `ResumeQueue` | `WORKFLOW_RIVER_QUEUE_RESUME_FAILED` / `ErrNotFound`；启动失败且不得 ready |
| `Client.Start` 成功后 `ResumeQueue` 失败 | 保留原错误，graceful shutdown 已启动 runtime 并关闭 health；不得遗留半启动进程 |
| legacy process-replacement 候选启动时目标 queue 行缺失 | Start 前 `PauseQueue` 返回 `WORKFLOW_RIVER_QUEUE_PAUSE_FAILED` / `ErrNotFound`；不得创建未暂停行或 claim |
| Cancel 遇到非安全 Writeback checkpoint | `WORKFLOW_CANCELLATION_DEFERRED` / Retryable，事务不归约终态 |
| Cancellation guard 查询/transaction 无效 | `WORKFLOW_CANCELLATION_SAFETY_UNAVAILABLE`，fail closed |
| Worker 超 hard deadline | 进程非零退出；保留 Job/lease/checkpoint 供新实例恢复 |

### 5. Good / Base / Bad Cases

- Good：子进程在 Job running 后 SIGKILL；River 写入 `river:rescue_count` 并 rescue，同一 Job 新 attempt 完成。
- Good：空库没有目标 queue 行；Worker lifecycle 启动后 River 创建该行，Resume 成功，最后才报告 ready。
- Good：正常 managed Apply 只让 `RuntimeHost.Admit` 在模型相关 Claim 前建立短 fence；已经 admitted 的 Job/lease 不被 drain，River queue 保持运行。
- Good：legacy process-replacement 候选启动先用 `PauseQueue` 验证并固化已有暂停行，再启动 lifecycle，且不执行 Resume。
- Base：没有 trace context 时 metadata 为空；有 trace 时只写 `traceparent`。
- Base：Cancel 发生在 Atomic Begin 后；旧 lease 到期，新 Worker reclaim 并恢复到 ApplyFailed/Compensated/Manual/Completed 后再 terminal cancel。
- Bad：API 使用 `default` queue，Worker 使用 `workflow` queue；Job 永久 pending。
- Bad：Consumer 把所有非 `traceparent` 字段都拒绝，导致 River rescue 自有 metadata 触发永久重试。
- Bad：为了隐藏空库 `ErrNotFound` 在 Queue adapter 中把缺失行视为 Resume 成功，导致其他控制路径的数据不一致被静默掩盖。
- Bad：把正常 hot Apply 实现为 `PauseQueue`、全队列 drain 或等待在途 Job；或 legacy 候选只跳过 Resume，允许目标行缺失时 River Start 创建未暂停队列并开始 claim。
- Bad：用 `pg_isready` 声称目标 database 已创建，Migrate 在 initdb 完成前启动并失败。

### 6. Tests Required

- Unit/race：Client options、queue 映射、traceparent、`river:*` 保留字段、lifecycle 首事件互斥、Start→Resume→readiness
  顺序、legacy Pause→Start→readiness、缺失暂停行 fail closed、Resume 失败清理、cancel safety 状态表；hot Apply 另在
  `model-settings-runtime.md` 要求验证 `RuntimeHost.Admit` 的 Claim fence 与在途 lease 不被 drain。
- PostgreSQL：cancel 后 heartbeat、lease expiry/reclaim、unsafe transition 回滚、安全 checkpoint 后 terminal cancel，至少 `-race -count=20`。
- PostgreSQL：持有 Runtime 行锁并让 Claim/Heartbeat/Transition/WaitForHuman 等待到 lease 过期，证明锁后数据库时间
  拒绝旧 owner、Human Task 不会被创建，且 Claim 只创建下一 Attempt；不得用普通 `Sleep` 后再发起请求替代该竞态回归。
- Integration：独立空库断言目标 queue 从 0 行经 lifecycle Start 变为 1 行且随后 Resume/readiness 成功；另覆盖 Approval
  双 Worker、legacy process-replacement 候选缺失 queue 行在 Start 前失败、正常 Writeback、fault smoke，并断言
  Execution/Commit/Mapping/Outbox 唯一。
- Process smoke：真实测试子进程 SIGKILL → River stuck rescue → 新 attempt completed。
- River maintenance service 在同一 Schema 内通过 leader election 单实例运行；测试只换
  queue 不能隔离 rescuer/scheduler 配置。需要自定义短 rescue 周期的进程 smoke 必须为
  每次运行创建独立临时数据库或独立 River Schema，并执行完整 Goose + River Up/Validate。
- Compose：`up --wait`、Migrate→API/Worker 顺序、API/Worker readiness、UID 10001、SIGTERM exit 0、DB 短断 503→恢复 200。

### 7. Wrong vs Correct

```text
Wrong: Producer 不设置 InsertOpts.Queue，Consumer 只监听 workflow。
Correct: API/Worker 从同一配置构造 Client，Inserter 显式写同一 queue。

Wrong: metadata 只要不是 traceparent 就拒绝，包括 River 自有 river:rescue_count。
Correct: 项目只写 traceparent；Consumer 隔离并忽略 river:*，其余字段 fail closed。

Wrong: Worker 先 ResumeQueue，再 Start River；空库靠忽略 ErrNotFound 继续启动。
Correct: 普通启动先 Start lifecycle 创建 queue 行，再 Resume 并设置 readiness；legacy 候选才在 Start 前按兼容协议验证 PauseQueue，失败完整清理并返回原错误。

Wrong: 正常 managed Apply 调用 PauseQueue、排空全队列或等待已 Claim Job 结束。
Correct: 正常热应用只在模型相关 Claim 前调用 RuntimeHost.Admit；已 admission 的 Job 和 generation lease 继续完成。

Wrong: legacy process-replacement 候选只跳过 Resume，允许 Start 在缺失行上创建未暂停队列。
Correct: legacy 候选启动先 PauseQueue 验证并固化已有队列；缺失行在 lifecycle Start 前 fail closed。

Wrong: Cancel 直接把 Run cancelled，再留下 prepared/file_applied Execution。
Correct: 同一事务调用 CancellationSafetyGuard；不安全时保持可恢复 Job/lease/checkpoint。
```

## M6-A Retrieval Index Foundation Contract

### 1. Scope / Trigger

- Trigger：Ingestion 已生成 Canonical Chunk，需要建立可重建 FTS/Vector Projection 和可回滚 Active Index。
- Scope：M6-A 只覆盖 Schema、领域/Application 契约、Lexical/Vector 批量持久化和原子激活；River Consumer、外部 Embedding、Search API 在后续任务实现。

### 2. Contracts

- `embedding_version` 绑定 Provider、Adapter/Version、Model、Dimensions、Normalization、Distance 和非敏感 Config Hash，创建后不可变。
- Begin Index 同事务写 `index_version` 与完整 `index_manifest_chunk`；Application 按 Sequence+Chunk ID 稳定排序重算 Manifest SHA-256，数据库冻结 Count、Hash 和 Canonical Chunk 版本绑定。
- Lexical Builder 只能从 Manifest `INSERT ... SELECT` Canonical Chunk 生成 `simple` tsvector；vector-only batch 不含 `SearchVector`/`LexicalStatus`，只能更新既有 lexical-ready 行。
- M6-A `simple` Tokenizer 的 `token_count` 定义为生成后 tsvector 的 lexeme 数；vector batch 只能证明计数相同，不能修改。真实模型 Tokenizer 必须通过新 Tokenizer/Index Version 演进。
- 无 Embedding 的 Index 必须显式 `degraded_capabilities=["vector"]`；有 Embedding 时 Ready 根据 `ready/skipped_oversized/failed` 实际计数推导是否 vector degraded。
- 通用 Transition 只允许 `building→ready|failed`、`active→retiring`、`retiring→archived`；任何 `→active` 必须由 Activate/RollbackActivate 在一个事务内追加 Activation 并切换状态。
- 当前 `vector` 列允许多维版本共存，但同一 Embedding/Index 固定维度。M6-A 不建立跨维度 HNSW；固定维度和容量评测后再建部分表达式索引。

### 3. Error / Concurrency Rules

- 相同 Embedding contract 或 Workspace+Index idempotency key 只有完整绑定一致时才是 replay；不同绑定返回 VersionConflict/ConsistencyViolation。
- Projection 批次全有或全无；重复批次只允许向量、Token Count、状态和失败码完全一致，不覆盖全文投影。
- Activate/Rollback 使用 Workspace advisory transaction lock、Index 行锁和单 Active 部分唯一索引；Activation Receipt 保存 `activate|rollback` 类型和切换后版本，响应丢失或后续状态变化后仍按 idempotency key 重建当次历史快照，不重复切换。
- `GetActive` 没有 Active 时返回稳定 NotFound，不回退到 Building/Failed/Retiring。
- Migration Down 在任意 Retrieval 业务数据存在时返回 SQLSTATE `55000`；空库迁移入口不能在 `CREATE EXTENSION vector` 前注册 pgvector 类型。

### 4. Tests Required

- Domain/Application `-race -count=20`：状态矩阵、Manifest Hash、Fusion canonical JSON、vector-only batch、维度/NaN/Inf/零范数、Ready degraded、Activate/Rollback。
- PostgreSQL：空库/重复 Up/Down→Up、跨 Workspace、不可变、Lexical Builder、批量 replay/conflict、首次/替换/回滚激活、并发双激活、response-loss replay。
- EXPLAIN：FTS GIN、Canonical Chunk trigram GIN、Workspace/Index/Chunk B-tree；未评测 HNSW 不计入完成证据。

### 5. Wrong vs Correct

```text
Wrong: Embedding 调用方传 PostgreSQL tsvector 并在 SaveVectorBatch 覆盖 search_vector。
Correct: Lexical Builder 是唯一全文事实源；vector-only batch 只保存向量结果。

Wrong: 先把新 Index 标 active，再单独退役旧 Index 或补 Activation。
Correct: 同一 PostgreSQL 事务内 append Activation、旧 Active→Retiring、目标→Active。
```

## M6-C Embedding And Hybrid Search Contract

### 1. Scope / Trigger

- Trigger：M6-A/B 已建立 immutable Index/Manifest/Projection 与 Reindex Delivery，需要接入真实
  Embedding、可恢复向量批处理、Active-only Keyword/Semantic/Hybrid Search 和 Evidence v1。
- Scope：本契约不包含 HTTP/OpenAPI、权限中间件、Conversation/RAG Answer、Query Rewrite、
  Document/Topic/Conflict 过滤或 50 万容量 ANN；这些分别归 M6-D、M6-02 和 M10。

### 2. Signatures

```go
type Embedder interface {
    Contract() domain.EmbeddingContract
    Embed(context.Context, application.EmbedRequest) (application.EmbedResult, error)
}

type VectorBuildStore interface {
    LoadVectorBuildPage(context.Context, domain.VectorBuildPageCommand) (domain.VectorBuildPage, error)
    CommitVectorBuildBatch(context.Context, domain.VectorBuildBatch) (domain.VectorBuildCommitResult, error)
}

type SearchStore interface {
    LoadActiveSearchIndex(context.Context, foundation.ID) (application.SearchIndex, error)
    SearchLexical(context.Context, application.LexicalSearchQuery) ([]domain.SearchCandidate, error)
    SearchVector(context.Context, application.VectorSearchQuery) ([]domain.SearchCandidate, error)
}
```

数据库入口为 `migrations/00016_embedding_hybrid_search.sql`。正式 Adapter 为
OpenAI-Compatible `/v1/embeddings` 与 Ollama `/api/embed`；Reranker 当前只冻结 Port，不提供未批准的
生产协议空壳。

### 3. Contracts

- `EmbeddingContract` 冻结 Provider、Adapter/Version、Model、Dimensions、Normalization、Distance、
  Endpoint identity、`MaxBatchSize`、`MaxInputBytes`、`MaxBatchInputBytes` 和不含 Credential 的 Config Hash。
- 环境键统一使用 `ZHIXU_EMBEDDING_` 前缀，后缀为 `PROVIDER`、`BASE_URL`、`API_KEY`、`MODEL`、
  `DIMENSIONS`、`NORMALIZATION`、`DISTANCE_METRIC`、`MAX_BATCH_SIZE`、`MAX_INPUT_BYTES`、
  `MAX_BATCH_INPUT_BYTES`、`TIMEOUT`、`MAX_RESPONSE_BYTES`；默认 provider 为 `disabled`，生产 Adapter
  固定为 Eino。旧 implementation selector 不再是配置合同，恢复历史版本使用 Git 发布记录。
- `embedding_cache` 主键为 Workspace + Embedding Version + Content Hash，不保存正文；cache 与 Projection
  terminal update 同事务。cache 使用一条 `INSERT ... SELECT FROM unnest(...)`，Projection 使用一条
  `UPDATE ... FROM unnest(...)`，随后一次批量 readback exact float32 校验。
- Page Store 先读取最多 `Limit+1` 条 metadata/cache，再按累计正文上限选择 page，最后一条参数化查询
  读取被选中的 cache miss 正文；不得先把合法最大配置约 10 GiB 正文加载到 Go 内存。
- Hybrid Processor 固定顺序为 Lexical → bounded Vector batches → `SNAPSHOT_STRUCTURE_V2` → Ready。
  Regression 与 Ready 都调用 `DeriveReadyDegradedCapabilities`；skipped/failed 必须得到 `vector` degraded。
- 历史 Hybrid 已无 pending vector 时，配置 disabled/变化也允许完成 V2 Regression；仍有 pending 时返回
  `RETRIEVAL_VECTOR_EMBEDDER_VERSION_UNAVAILABLE`，要求恢复匹配版本，不按当前默认配置改写历史任务。
- Search 两路只读指定 Workspace 当前 Active Index，并复用 Source/SourceVersion/path/captured time filter。
  Distance 只能由持久 `cosine|inner_product|euclidean` 枚举选择 `<=>|<#>|<->` 固定模板。
- Lexical trigram `%` 必须在短事务内通过 `set_config(..., true)` 固定 threshold `0.3`；不得继承
  pool connection 的可变 `pg_trgm.similarity_threshold`，也不得为确定性改成失去 GIN 的无界全表计算。
- RRF v1 只融合 rank，`k` 范围 `1..500`，分母使用 `int64/float64`；相同 Chunk 只排名一次，Evidence
  返回稳定有界的多 Source provenance。nil/Retryable Rerank 显式 degraded，损坏 output fail closed。

### 4. Validation & Error Matrix

| 条件 | 稳定错误/行为 |
|---|---|
| Provider 数量/顺序/模型/维度/NaN/Inf/零范数损坏 | ConsistencyViolation，整批不提交 |
| caller cancel/deadline | Adapter error 保留 `errors.Is(context.Canceled/DeadlineExceeded)`；Processor 不持久归约永久失败 |
| 429/5xx/网络/超时 | Retryable；Hybrid query 可退化 Keyword，Semantic 返回错误 |
| Credential/Schema/维度确定性错误 | NonRetryable/Consistency，禁止伪向量或空成功 |
| cache 同 key 不同 float32 | `RETRIEVAL_EMBEDDING_CACHE_REPLAY_CONFLICT`，整个 Projection 事务回滚 |
| 单输入 oversized atomic/non-atomic | `skipped_oversized/EMBEDDING_INPUT_OVERSIZED` 或 `failed/EMBEDDING_INPUT_CONTRACT_MISMATCH` |
| 历史 Hybrid pending 且匹配 Provider 不可用 | `RETRIEVAL_VECTOR_EMBEDDER_VERSION_UNAVAILABLE` / DependencyUnavailable |
| FTS-only Keyword/Hybrid/Semantic | 正常 / Keyword+vector/rerank degraded / `RETRIEVAL_SEMANTIC_UNAVAILABLE` |
| Rerank nil/Retryable/损坏 output | RRF+degraded / RRF+degraded / fail closed |
| 00016 已有 cache、V2 Delivery 或 Hybrid Index Down | SQLSTATE `55000` |

### 5. Good / Base / Bad Cases

- Good：同 Content Hash 多 Chunk 只调用一次 Provider，cache/Projection 一次 set-based commit；响应丢失后
  page 为空并继续 V2 Regression，最终只产生一个 Active/Completion。
- Base：Embedding disabled 时新 Index 继续 V1 FTS-only；Keyword 可用、Hybrid 明确退化、Semantic 明确不可用。
- Base：Reranker 未配置，Hybrid 返回 RRF 顺序并只标记 rerank degraded，不返回假失败或假重排分数。
- Bad：用 `pgx.Batch.Queue` 循环 1000 条 INSERT/UPDATE 后称为“批量”；数据库仍执行 N 条 statement/trigger。
- Bad：先按 `MaxBatchSize*MaxInputBytes` 读取正文，再在 Application 截断；合法配置可导致 Worker OOM。
- Bad：V2 Regression 前要求 Building Index 已预写 `degraded_capabilities=["vector"]`；真实 Processor 会先失败。

### 6. Tests Required

- Domain/Application：Embedding hash/limits、RRF overflow、filter/candidate/evidence、vector cache hit/miss、
  total input bytes、degraded derivation、历史 terminal recovery、Search/Rerank 正常/边界/失败路径。
- Provider httptest：两协议 batch、状态码、非法/超大响应、redirect、timeout/cancel error chain、Secret canary。
- PostgreSQL：00016 Up/重复 Up/Down→Up/guard、Workspace cache、set-based commit/replay/conflict、V2
  Regression/Completion、Active-only Search、filter 等集、bounded provenance、三 distance operator 与 EXPLAIN。
- Fault smoke：真实 PostgreSQL/River/LocalFS/Git + HTTP Embedder，注入 Vector commit response-loss、四
  checkpoint、Ready/Completion response-loss；断言唯一 Activation/Active/Completion、最小 Hybrid Search
  命中，Provider Key/正文/DSN/路径不进入任务载荷、日志或错误。
- Gate：`go test -race`、关键包 `-count=20`、integration `-p 1`、`go vet ./...`、`make test`、
  `go mod tidy -diff`、Compose config、go-review、sql-code-review 和独立审查。

### 7. Wrong vs Correct

```text
Wrong: config disabled 后历史 Hybrid 一律失败，或用新默认模型继续旧 Index。
Correct: page 为空先 Done；仍 pending 时要求恢复精确 Embedding Version，不改写历史绑定。

Wrong: Provider cancel 包装成不含 context.Canceled 的安全错误，Processor 将 shutdown 归约为永久失败。
Correct: 安全 Error string 隐藏 cause，但 Unwrap 保留 context.Canceled/DeadlineExceeded 供 Worker 判定。

Wrong: Lexical 失败立即返回，后台 Vector goroutine 继续运行并可能泄漏资源。
Correct: cancel shared context 后等待 Vector worker 收敛，再返回 Lexical 错误。
```

## M6-D Search API And Evidence Reference Contract

### 1. Scope / Trigger

- Trigger：M6-C Active-only Search 需要通过真实 HTTP/OpenAPI 对外提供稳定分页 Evidence，并让每个
  provenance 的 Source Version/Span 可打开。
- Scope：本契约覆盖 Workspace-scoped PostgreSQL 查询、top-100 HTTP Cursor、不可变 Artifact 引用与
  API/Worker Embedder composition；不新增迁移，不修改 `00014`–`00016`。正式 Auth/Session/Token/
  CSRF/Capability 与 500,000 Chunk ANN/P95 归 M10。

### 2. Signatures

```text
POST /api/v1/search
GET /api/v1/workspaces/{workspace_id}/source-versions/{source_version_id}
GET /api/v1/workspaces/{workspace_id}/source-versions/{source_version_id}/spans/{source_span_id}
```

```go
type EvidenceReferenceStore interface {
    LoadSourceVersionReference(context.Context, foundation.ID, foundation.ID) (domain.SourceVersionReference, error)
    LoadSourceSpanReference(context.Context, foundation.ID, foundation.ID, foundation.ID) (domain.SourceSpanReference, error)
}

type EvidenceArtifactReader interface {
    ReadEvidenceArtifact(context.Context, foundation.ID, foundation.ID) (application.EvidenceArtifact, error)
}
```

Search Store 继续使用 M6-C `LoadActiveSearchIndex/SearchLexical/SearchVector`；HTTP 分页不得引入第二套
offset SQL 或持久 Search Session。API 与 Worker 都只能调用
`models.NewConfiguredEmbedder(config.Config)` 构造 Embedding Adapter。

### 3. Contracts

- Search 请求必须经过领域 Canonicalization：Workspace、trim 后非空且最大 8 KiB Query、
  `keyword|semantic|hybrid`、Source/SourceVersion/path/captured-time filter；底层查询固定请求 top-100。
- Cursor v1 使用进程内随机 32-byte HMAC-SHA256 key，绑定 canonical SearchRequest、page limit、
  Active Index Version、完整有序 SearchResult Hash 与下一 offset；它不存 DB、不跨进程有效、不授权访问。
- Source Version 查询必须以 Workspace + Source Version 参数化限制，并只返回 Source/type/logical name、
  受控相对路径、Content Hash/size/media/security/captured time；不得选择 Workspace root 或 managed locator。
- Span 查询必须通过参数化 JOIN 证明 Workspace → Source Version → Source → Content Artifact →
  `source_version_projection` → Parse Projection → Source Span 全绑定。Application 再从不可变 Artifact
  复核全文 Hash/大小、`[start_byte,end_byte)` 与 excerpt Hash，最多返回 4 KiB UTF-8 excerpt。
- Vector wire 字段固定为 `distance`；数据库只按持久 `cosine|inner_product|euclidean` 枚举选择
  `<=>|<#>|<->` 固定 SQL 模板，不接受用户 operator/sort 输入。
- `provider=disabled` 时 Factory 返回 nil Embedder：Keyword 正常，Hybrid 显式 effective Keyword +
  vector/rerank degraded，Semantic 返回 503；不得阻断 FTS-only Search 或返回假向量。
- exact vector scan 与生产 `EXPLAIN (FORMAT JSON)` 只证明 operator、过滤与查询计划正确；ANN 参数和
  P95 不得从小夹具外推。

### 4. Validation & Error Matrix

| 条件 | 稳定错误/行为 |
|---|---|
| Search JSON/UUID/Query/mode/filter/limit 非法 | `400 RETRIEVAL_SEARCH_REQUEST_INVALID` 或 `INVALID_JSON`，不执行 Store |
| Cursor 签名、版本、请求绑定或 offset 非法 | `400 RETRIEVAL_SEARCH_CURSOR_INVALID` |
| Active Index 或完整结果 Hash 变化 | `409 RETRIEVAL_SEARCH_CURSOR_STALE`，客户端从第一页重启 |
| API 进程重启后旧 Cursor | 新随机 key 校验失败，`400 RETRIEVAL_SEARCH_CURSOR_INVALID` |
| FTS-only Semantic | `503 RETRIEVAL_SEMANTIC_UNAVAILABLE` |
| Source Version/Span 不存在、跨 Workspace、错绑 | `404 RETRIEVAL_EVIDENCE_REFERENCE_NOT_FOUND`，不区分原因 |
| Artifact ID/Hash/大小/range/excerpt 不一致 | ConsistencyViolation，`RETRIEVAL_EVIDENCE_ARTIFACT_INVALID` |
| DB/Provider 暂时不可用 | 503 并保留稳定 retryable 分类；Hybrid 仅在既有规则允许时退化 Keyword |

### 5. Good / Base / Bad Cases

- Good：同一规范请求第一页返回 Cursor；第二页重算同一 top-100、验证 Active/完整 Hash 后切片，
  无重复或漏项；Evidence href 可经真实 Router 打开不可变 excerpt。
- Base：Embedding disabled 的 Active FTS-only Index 仍由 API 返回 Keyword 结果；Hybrid 明确退化，
  Semantic 明确失败，真实零命中仍是 `200 items=[]`。
- Bad：Cursor 只存 offset、跨 API 重启继续有效，或后续页直接对 SQL 使用 offset，导致 Active 切换后
  拼接不同排序。
- Bad：Span 直接读取 provenance.relative_path 的工作树文件，写回后把新正文冒充历史 Evidence。

### 6. Tests Required

- Unit/Handler：严格 JSON、默认值、过滤 canonicalization、三模式/零结果/降级、Cursor 正常/篡改/
  跨请求/stale/重启、Problem 映射和 405。
- PostgreSQL HTTP：真实 Router + Repository，Workspace 隔离、三模式/过滤、Source Version/Span href、
  404 防枚举、Artifact 完整性与 4 KiB UTF-8 截断。
- SQL plan：FTS GIN、trigram GIN、Active/Manifest B-tree、三种 exact vector operator 的生产查询
  `EXPLAIN (FORMAT JSON)`；不把结果登记为 ANN/P95 证据。
- Fault/Compose：唯一 River Completion 后经 Router Search 并打开 Evidence；disposable Git Workspace
  黑盒完成 Approval→Reindex→Hybrid-to-Keyword Search，成功/失败均清理 project volume/临时目录。
- Gate：Go race/count/integration/vet/test/tidy、OpenAPI drift、frontend lint/typecheck/test/build、Compose、
  go-review、sql-code-review 和独立审查。只有实际命令结果才能标记通过。

### 7. Wrong vs Correct

```text
Wrong: SELECT span by source_span_id，再在 Handler 比较 workspace_id；或跨 Workspace 返回不同 404 文案。
Correct: 参数化 JOIN 在数据库入口同时限制 Workspace/SourceVersion/Span，miss 统一 NotFound。

Wrong: 返回 vector.score/similarity，客户端假设值越大越相似。
Correct: 返回 vector.distance，并保留 Embedding Version/Distance Metric 语义。

Wrong: API 与 Worker 各自 switch provider、拼 Options，配置变化后查询与索引绑定漂移。
Correct: 两个 Composition Root 复用同一 Configured Embedder Factory。
```

## M6-02 Agent Citation、Eligibility And Model Run Contract

### 1. Scope / Trigger

- Trigger：Agent 使用 Retrieval Evidence 评估 Existing Claim、生成 Relation/RAG 结果或执行 Faithfulness Review。
- Scope：完整 frozen Index Citation tuple、Knowledge 正式资格、Existing Claim 服务端事实、Model Run/Call 持久化；
  不包含 M6-03 Tool 执行或 M6-04 Conversation/SSE。

### 2. Signatures

```go
LoadCitationSourceSpanReferences(context.Context, []CitationReferenceQuery) ([]CitationSourceSpanBinding, error)
OpenCitationEvidenceBatch(context.Context, []CitationReferenceQuery) ([]OpenedCitationEvidence, error)
EvaluateEvidence(context.Context, EvidenceEligibilityQuery) ([]ProvenanceEligibility, error)
ReadFormalClaims(context.Context, FormalClaimQuery) ([]FormalClaim, error)
StartModelRun(context.Context, ModelRun) (ModelRun, bool, error)
StartModelCall(context.Context, foundation.ID, ModelCall) (ModelCall, bool, error)
```

数据库事实源为 `agent.model_run`、`agent.model_call`；Knowledge Claim/Source/Conflict 仍由 `core` Schema 拥有，
Agent 不创建第二套 Claim、Eligibility 或 Conflict 表。

### 3. Contracts

- Citation Identity 固定为 `workspace_id + index_version_id + chunk_id + source_version_id + source_span_id`；
  同一批最多 500 条，只允许一个 Workspace/Index，重复 tuple 拒绝。
- PostgreSQL 用一条参数化 SQL 联合 Index Manifest、Chunk、Source Version、Projection 与 Span，并按输入 ordinality
  返回；任一 tuple 缺失使整批 fail closed。Application 对同一 Source Version 只读取/哈希一次 Artifact。
- Evidence Eligibility 用一条参数化查询返回 Confirmed Claim/Relation 与 Disputed Claim 绑定；Conflict 聚合只由
  本批 `bindings` 中去重的 Disputed Claim ID 驱动，不扫描 Workspace 全量 Conflict。
- Existing Claim 必须由 Knowledge `FormalClaimReader` 读取并核对 Workspace、正文、canonical Applicability、Sources、
  状态和版本；Existing Evidence 还必须命中 `owner_type=CLAIM && owner_id=existing_claim_id`。
- Disputed disclosure 固定包含 `claim_id/conflict_ids/canonical applicability/UTC updated_at`；模型输出必须精确复制。
- 一个 Node Attempt 最多一个 Model Run；v2 每次 `PLAN/AGENT/ANSWER/INITIAL/REPAIR/REDUCED/REVIEW` 为独立 Model Call，
  `AGENT` 可重复、`ANSWER` 单次，且保持唯一递增 `call_no`。调用前写 `STARTED`，完成 CAS；未知结果归 `UNKNOWN`，
  不得自动重放 Provider 或伪装成功。既有 v1 `PLAN -> INITIAL/REPAIR/REDUCED -> REVIEW` 继续兼容。
- Model Run 保存 generation/retrieval 基线；每条 Model Call 必须显式保存该次实际 Adapter/Model/Profile/Prompt/Schema
  的 ID/version 与 `max_output_tokens`。REVIEW 可以使用独立受信 Catalog，但不能只留下不可逆 request hash。
- 只保存版本、Hash、字节、Token、耗时、状态和稳定错误码；Prompt、Evidence、Source、raw response、Credential、
  Endpoint Secret 和绝对路径禁止入库。

### 4. Validation & Error Matrix

| 条件 | 结果 |
|---|---|
| 空批次、超过 500、重复或跨 Workspace/Index | `INVALID_INPUT` / Citation invalid |
| Index/Chunk/Source Version/Span 任一绑定缺失或乱序 | 整批 Evidence reference fail closed |
| Artifact Hash/Byte Range/Excerpt 不一致 | Evidence consistency violation |
| Suggested/Rejected/Deprecated/无正式绑定 | Ineligible，不得发布为正式事实 |
| Existing Claim 正文/Applicability/Source/Owner 漂移 | 模型调用前 Relation invalid |
| Conflict disclosure 遗漏、增加、条件或时间漂移 | Structured result invalid |
| 同 Node Attempt 第二个 Model Run、call_no gap 或 active predecessor | 数据库/Repository consistency violation |
| Provider 结果未知或 crash 后仍为 STARTED | 恢复为 `UNKNOWN`，进入显式恢复路径 |
| 有 Model Run/Call 时执行 Down | SQLSTATE `55000` |

### 5. Good / Base / Bad Cases

- Good：500 个 Citation 单 SQL 验证；同一 Source Version 多个 Span 只读一次 Artifact；Disputed Claim 返回完整
  Conflict disclosure，REVIEW Call 与 generation calls 使用同一 Model Run 的连续 call_no。
- Base：Chat disabled 时 API/Worker 不注册可执行 Agent capability；既有 Retrieval/Knowledge 功能继续可用。
- Bad：把 Active Index 当 Approved Evidence、按 Citation 循环查库/读 Artifact、信任调用方 Existing Claim、让模型
  自造 Conflict/更新时间、把 REVIEW 绕过 RecordingChatModel、保存完整 Prompt 或 raw response。

### 6. Tests Required

- Strict JSON：unknown、duplicate、trailing、invalid UTF-8、required `null`、类型/枚举/大小边界。
- Retrieval PG：多元素/500 条、乱序 ordinality、任一 tuple 篡改整批失败；Application Reader 计数证明 Artifact 去重。
- Knowledge PG：500 条单批、Workspace 隔离、多 owner、Disputed Conflict；SQL shape 锁定 Conflict 只由 requested
  disputed Claim 驱动。
- Workflow PG：真实 Retrieval + Knowledge production adapters 覆盖 Existing/Disputed disclosure，并反查 Model Run/Call。
- REVIEW PG：generation 后从连续 call_no 记录 `phase=REVIEW`，断言 Hash/Token/latency/status 且 canary 不落库。
- Version PG：INITIAL 与不同 REVIEW refs 均可由 `GetModelRun` 直接反查；replay 时任一 call ref 漂移都返回冲突。
- Migration：空库/重复 Up、CAS/FK/Workspace/call sequence/crash→UNKNOWN、空数据 Down→Up、有数据 guarded Down。
- 全量门禁：Agent count/race、`go test -race ./...`、真实 PG integration、vet、Make、Eval、Docker/Compose smoke。

### 7. Wrong vs Correct

```text
Wrong: workspace + chunk 足以作为 Citation；逐条打开后再检查 Source/Span。
Correct: 一次查询证明 workspace + index + chunk + source_version + source_span，任一缺失整批失败。

Wrong: WHERE conflict_member.workspace_id=$1 后聚合整个 Workspace，再筛本批 Claim。
Correct: 先从本批 bindings 提取 DISTINCT Disputed Claim ID，再按 workspace + claim_id 聚合 Conflict。

Wrong: Workflow 输入或模型输出决定 Existing Claim/Conflict；REVIEW 直接调用裸 ChatModel。
Correct: Knowledge Application 提供服务端事实；所有 generation/REVIEW 调用经 RecordingChatModel 连续持久化。
```

## M6-03 Tool Call Persistence Contract

- `workflow.tool_call` 是 Tool 执行唯一持久事实；只保存版本化身份、请求/响应 Hash、字节数、受控摘要和稳定 result/side-effect ref，禁止 raw Prompt、arguments/output、正文、Credential、URL Secret、绝对路径和 stderr。
- `(node_attempt_id, call_no)`、活动 STARTED、Workspace 幂等键和复合 FK 由数据库约束；应用不得只靠先查后写。
- INSERT 必须由 Trigger 复核 running Run/Node/Attempt、attempt_no=fence、相同 owner/lease 和数据库时间未过期；终态只允许 STARTED→SUCCEEDED/FAILED/UNKNOWN 且 immutable binding 不变。
- `RecordRefused` 不占有副作用幂等键；Schema/Policy/Capability 失败不得创建 STARTED 或调用 Executor。
- stale recovery 使用 Node→Attempt→Tool Call 锁序、`SKIP LOCKED` 和锁后数据库时间重检；与 Heartbeat 并发时不能误收回新 lease。
- `writeback_execution` trusted Call 不由通用 recovery 归约 UNKNOWN。历史 Call 保留首次 STARTED Attempt；新 Attempt 在读取历史 receipt 前必须验证 Definition version/hash、Node key/run、Attempt/fence/owner/lease、Workflow binding 与 exact Capability。
- 迁移 `00019_tool_registry_security.sql` 只前向新增；有 Tool Call 数据时 Down 返回 SQLSTATE `55000`。

Required real-PG tests：跨 Workspace/Run/Node/Attempt、RecordRefused、STARTED/CAS/replay/conflict、commit response-loss、两 Worker recovery、Heartbeat race、trusted write 新 Attempt 资格、guarded Down 和稳定 Timeline。

## M6-04 Conversation And SSE Persistence Contract

### 1. Scope / Trigger

- Trigger：新增或修改 Conversation、Question、Answer、Answer Feedback、RAG v2/Clarification Model Run，或浏览器 SSE 重放投影。
- Scope：`migrations/00020_rag_conversation_sse.sql`、`internal/conversation`、Agent PLAN/RAG v2 增量契约和
  `ops.server_event`；Workflow retry/failure 仍由 `workflow.*` 拥有，正式 Auth/CSRF/Audit 仍归 M10。

### 2. Signatures

```go
CanonicalizeQuestionRequest(QuestionRequest) (QuestionRequest, error)
EncodeQuestionScope(QuestionRequest) (json.RawMessage, error)
ComputeQuestionRequestHash(QuestionRequest) (string, error)
ComputeContextHash([]PublishedTurn) (string, int64, int, error)
CanonicalizePublishedResult(AnswerResultType, json.RawMessage) (PublishedResult, error)
CanonicalizeFeedbackRequest(FeedbackRequest, Answer) (FeedbackRequest, error)
ComputeFeedbackRequestHash(FeedbackRequest, Answer) (string, error)
ValidateSearchModeOutcome(SearchMode, SearchMode, bool, []SearchDegradation) error
```

数据库事实源固定为：

```text
agent.conversation
agent.question
agent.answer
agent.answer_feedback
ops.server_event
```

`agent.model_call.phase` 增量接受 `PLAN`；同一 RAG Model Run 的调用序列允许
`PLAN(1) -> INITIAL(2) -> REPAIR(3) -> REDUCED(4) -> REVIEW(>=2)`，没有 PLAN 的历史序列继续有效。
`agent.model_run.final_result_type` 增量接受 `clarification`，既有结果类型保持可读。

### 3. Contracts

- Conversation 用 `(workspace_id,id)`、version CAS 和 `last_activity_at,id` 稳定排序；Question append-only，
  `ordinal`、canonical scope/options、request/context hash 和幂等绑定创建后不可变。
- Question Scope 与 retrieval summary 分别复用唯一字段定义编码为 canonical JSON，均不得超过 16 KiB；
  Domain 必须在进入 Repository 前拒绝超限，不能让领域有效请求在数据库 CHECK 才失败。
- Question 正文与历史只存在 Conversation 事实源。Workflow input、Model Call、Server Event 和日志不得复制正文；
  执行上下文最多 8 个已发布 Turn、合计 32 KiB，并由稳定 context hash 绑定。
- RAG Executor 通过窄 `QuestionExecutionContextLoader` 读取当前 Question、Answer slot 和冻结历史；实现最多执行
  一次 Question/Answer/Run 联合查询与一次有界历史查询。调用前先拒绝非法或复用的身份与非 canonical hash，
  读回后必须校验 Workspace/Conversation/Question/Answer/Run/ordinal 完整绑定并重算历史 hash；持久字段彼此相同
  但与实际历史不一致时仍按 consistency failure 拒绝，不能只信任 Workflow input 或 Question 行中的 hash。
- RAG Workflow output 只允许 `schema_version/answer_id/publication_status/result_type/model_run_id/result_hash`
  六字段 canonical receipt；发布状态到结果类型的映射由 Conversation Domain 唯一维护。receipt 不保存 Answer、
  retrieval summary、Prompt、Evidence、Provider 或 Tool 数据，消费端必须回查 Answer 事实源。
- Answer 在接收 Question 时预分配为 `pending/version=1`，只允许一次 CAS 发布到
  `completed|refused|clarification_required/version=2`。终态必须绑定同 Workspace 的 Question、Workflow Run、
  terminal Model Run、canonical result bytes/hash 和不可变 retrieval summary；JSONB readback 必须先重建 canonical
  document，再进入领域校验/API，不能把 PostgreSQL 的键顺序作为哈希输入。
- Retrieval summary 的 requested/effective mode 与 degradation 必须复用 Retrieval Domain 的唯一模式矩阵：
  Semantic 不得退化，Keyword 不得携带 query degradation，Hybrid→Keyword 必须显式 Vector degradation。
- Answer `pending` 只表示尚未发布，不能替代 Workflow 活动状态。`00021_conversation_active_workflow.sql` 删除初始
  pending 部分唯一索引；Question UoW 先锁 Conversation、按 Run 状态拒绝非终态 Workflow，Answer INSERT Trigger
  复用同一 Conversation 锁并只拒绝 `succeeded|failed|cancelled` 之外的既有 Run。failed/cancelled 的历史 pending
  slot 不进入上下文，也不阻止下一 Question。
- Conversation 归档只拒绝新 Question；既有幂等键的 exact replay/冲突判定必须先执行。Answer Trigger 使用稳定
  constraint 名区分 active Workflow 与 archived Conversation，Adapter 将数据库兜底映射回同一稳定错误码。
- Question exact replay 可与 Worker 推进 Run 状态并发。重放前只读取不可变 Answer ID；必须在 Workflow `StartTx`
  replay 锁定并返回最新 Run 后重新读取 Answer/Workflow 投影，再比较 status/version/updated_at。不得把锁前旧快照与
  锁后新状态的正常差异归类为持久绑定损坏。
- Feedback append-only 且按 canonical request hash 幂等。只允许已发布 Answer/Refusal；Clarification 和 pending
  不可反馈。Citation 类反馈必须绑定 RAG v2 Answer 中真实 Citation，其他类型不得携带 Citation。
- `ops.server_event.seq` 是浏览器重放游标，按 Workspace 单调读取，逻辑保留 24 小时。它是通知投影，不是
  Conversation/Workflow/Audit 事实源；事件消费者必须回查权威资源。
- Workflow Outbox 投影只复制 Workspace、Run、event type、source event ID 和受限资源摘要；不得复制
  `workflow.outbox_event.payload`、Question/Answer 正文、Evidence、Tool 输出、Credential 或绝对路径。
- `00020` 只做前向 additive 变更；五张新表任一有数据、存在 PLAN Call 或 Clarification Model Run 时，Down
  返回 SQLSTATE `55000`。发布回滚保留数据并 forward fix。
- SQL JSON 字段必填判断使用 `IS DISTINCT FROM`，避免缺失字段产生 NULL 后绕过 PL/pgSQL `IF`；Model Call
  额外验证前驱 phase，禁止 `PLAN -> REPAIR/REDUCED/REVIEW` 跳过 INITIAL。

### 4. Validation & Error Matrix

| Condition | Required result |
|---|---|
| Question 非法 UTF-8、NUL、超过 8 KiB，或 scope/options 非 canonical | `CONVERSATION_QUESTION_INVALID`，零持久副作用 |
| canonical Scope 或 retrieval summary 超过 16 KiB | 领域 InvalidInput，不进入 SQL |
| 同 Workspace/Conversation 幂等键同 hash | exact replay，不增加 ordinal/version |
| 同幂等键不同 hash，或已有非终态 Answer Workflow | Version/Idempotency conflict，不创建第二条活动 Workflow |
| Archived Conversation 上的新 key / 既有 key | 新 key 拒绝；既有 key 继续 exact replay 或稳定 Idempotency conflict |
| Answer 跨 Workspace/Question/Workflow/Model Run 或非法 CAS | FK/CHECK/trigger/Repository consistency failure |
| RAG v2/Refusal/Clarification result 非 canonical，或与发布状态、Model Run/hash 不匹配 | `ANSWER_INVALID`，不发布 |
| Semantic→Keyword、Keyword degradation 或 PLAN→REPAIR 等非法模式/阶段序列 | 领域或 SQL consistency failure |
| Clarification/pending 收到 Feedback，或 Citation 绑定不闭合 | `ANSWER_FEEDBACK_INVALID`，Answer/Knowledge 不变 |
| Server Event 非法、未来或过期序号 | 稳定 `SSE_*` Problem；不得回放其他 Workspace 或全量历史 |
| `00020` 含业务数据，或 `00021` Down 会恢复冲突的 pending 唯一索引 | SQLSTATE `55000` |

### 5. Good / Base / Bad Cases

- Good：Question、pending Answer、Workflow/Outbox/River Job 和首个摘要事件在一个事务提交；最终 Answer 与
  Model Run 在一个事务发布；响应丢失后按相同 hash 返回既有绑定。
- Base：旧 RAG v1、无 PLAN 的 Model Call 序列和既有 Relation/Tool Workflow 继续可用；未启用 RAG 时不注册
  Fake executor，Conversation 只读数据仍可保留。
- Bad：Handler 先写 Question 再单独启动 Workflow；把 Workflow failed/cancelled 复制成 Answer 终态；直接把
  Outbox raw payload 作为 SSE data；用 Clarification 冒充 Refusal 或允许其进入 Feedback。

### 6. Tests Required

- Domain：Question/options/scope/context/cursor、Query Plan、RAG v2、Clarification、Answer 发布、retrieval summary、
  Feedback 的正常/边界/失败路径；固定 canonical hash 样例，覆盖 invalid UTF-8、NUL、大小、重复和 unknown field。
- Agent：v1 decoder 继续拒绝 v2-only 字段；PLAN 为 call_no 1、随后 INITIAL 为 2；Clarification 为 additive
  Model Run 结果类型，不改变旧三阶段/REVIEW 行为；真实 PG 拒绝 PLAN 后跳入 REPAIR。
- Migration real PostgreSQL：空库 Up、重复 Up、空数据 Down/Up、Workspace 复合 FK、单非终态 Answer Workflow、
  failed/cancelled 后下一 pending slot、`00021` guarded Down、CAS、
  append-only、缺失 Answer schema 字段、Feedback eligibility、24 小时 event expiry、安全 Outbox 投影和有数据 guarded Down。
- Repository/HTTP/SSE 后续门禁必须补并发 exact replay、response-loss、稳定 cursor、批量上下文/Turn 查询、
  future/expired Last-Event-ID、heartbeat/cancel 和正文 canary 扫描；单元 Fake 不替代真实 PostgreSQL 证据。

### 7. Wrong vs Correct

```text
Wrong: Question commit -> 另起事务启动 Workflow；SSE payload 保存正文以便客户端直接渲染。
Correct: 同一 PostgreSQL UoW 原子创建 Question/Answer/Workflow/Job/Event；SSE 仅通知并触发权威查询。

Wrong: Answer 表复制 Workflow failed/retry 状态，或 completed 后再单独完成 Model Run。
Correct: Answer 只保存 pending/三个发布终态；运行状态从 Workflow 投影，Answer 与 Model Run 原子终结。
```

## M6-04 RAG Execution And Atomic Publication Contract

### 1. Scope / Trigger

- Trigger：RAG Workflow 需要在 PLAN 后才知道真实 Retrieval tuple，并将 Answer/Refusal/Clarification 与 Model Run、Conversation activity、Server Event 原子发布。
- Scope：`00022_agent_rag_deferred_retrieval.sql`、Agent Model Run/Call、Conversation Answer finalizer、RAG progress event；不包含 HTTP、Worker composition 或前端页面。

### 2. Signatures

```go
type AnswerFinalizer interface {
    Lookup(context.Context, AnswerPublicationLookup) (workflow.OutputReceipt, bool, error)
    Finalize(context.Context, FinalizeAnswerCommand) (workflow.OutputReceipt, bool, error)
}

type RAGProgressRecorder interface {
    RecordRAGProgress(context.Context, RAGProgressRecord) error
}
```

### 3. Contracts

- RAG `RUNNING` Model Run 可暂时没有 Retrieval；只有同一次 `RUNNING -> terminal` 更新允许首次绑定完整 tuple。已有非空 tuple 永远不可变。
- `SUCCEEDED/rag_answer` 必须绑定 Retrieval；Clarification、零 Provider 的确定性 Refusal、FAILED/UNKNOWN 可保持未绑定。
- Finalizer 在一个 PostgreSQL 事务中锁 Conversation、Answer、Model Run，依次终结 Run、发布 Answer、更新 Conversation、追加 terminal event。
- Workflow 在任何 Provider 调用前执行 terminal receipt Lookup；命中后零 Provider、零 Context load 返回稳定 receipt。
- PLAN/retrieval/validation 的 started/completed 使用真实注入时钟写 Server Event；payload 只含 ID、阶段和计数。
- Progress exact replay 先按 Workspace/source ref advisory lock，恢复首次 `occurred_at`，再由 Event Store 校验完整业务 binding。

### 4. Validation & Error Matrix

| Condition | Required result |
|---|---|
| RAG Answer terminal 没有 Retrieval | Domain/DB consistency failure |
| 非 RAG Run 使用空 Retrieval，或 RUNNING 状态单独补 tuple | SQLSTATE `23514/55000` |
| Answer/Run CAS、Workspace/Attempt/result hash 任一漂移 | Finalize conflict/corrupt，零部分提交 |
| Finalizer commit response loss | `CONVERSATION_ANSWER_FINALIZE_UNKNOWN`；重放回查 Answer 返回 exact receipt |
| Provider 后 progress/finalizer 失败 | Manual Recovery，禁止 Workflow 自动重试造成重复 Provider |
| 同 stage 重放携带新时钟 | 恢复首次真实 `occurred_at` 后 exact replay |

### 5. Good / Base / Bad Cases

- Good：PLAN→检索→验证事件按真实时间持久化，最终 Answer 与 Model Run/Event 同事务提交；commit 响应丢失后零 Provider 重放。
- Base：旧 Relation/Tool Model Run 继续在创建时绑定 Retrieval；既有非空 tuple、Call phase 和 FK 行为不变。
- Bad：用 sentinel Index 创建 Run；先单独 `FinalizeModelRun` 再更新 Answer；progress event 使用合成百分比或 `run_started_at + 微秒` 伪造阶段时间。

### 6. Tests Required

- Domain/Agent：空/完整 Retrieval 生命周期、PLAN=call 1、同一 Recorder 连续 generation/review、Reduced Refusal、Provider failure/UNKNOWN。
- Progress PG：真实时间、started/completed 顺序、不同时间重放、payload canary 脱敏。
- Finalizer PG：三终态、同/异 proposal 并发、event failure 回滚、commit response-loss、retention 后 replay、跨 Workspace/Attempt、pending+terminal split 检测。
- Migration PG：Up/repeat Up/Down、guarded Down、零调用 Refusal、有调用但无成功 Call 的 Refusal 拒绝。

### 7. Wrong vs Correct

```text
Wrong: 创建 Model Run 时写假 Index UUID，或 Provider 成功后把 finalizer transient error交给 Workflow自动重试。
Correct: RAG RUNNING 延迟绑定真实 Retrieval；post-provider 持久化失败进入 Manual Recovery，避免重复 Provider。

Wrong: 每次进度重放重新生成 occurred_at，导致 Event exact binding 冲突。
Correct: 首次记录真实时钟；重放在 advisory lock 内恢复既有 occurred_at，并校验其余 binding。
```

## M6-04 RAG Stage Query Projection Contract

- Answer/Turn Query 的 `current_stage` 只从同 Workspace、同 Answer `resource_ref` 与 `answer_id` 绑定的
  `ops.server_event` 最新阶段事件恢复；不在 Answer 表复制第二套执行状态。
- 对外只允许 `plan.started|plan.completed|retrieval.started|retrieval.completed|validation.started|validation.completed`；
  无阶段返回 NULL，终态 Answer 可保留最后一次 `validation.completed`。
- 列表必须在单条有界 Turn 查询内投影阶段，禁止逐 Answer 回查。`00023_rag_stage_projection_index.sql` 使用
  `(workspace_id, resource_ref, seq DESC)` 的 RAG 事件部分索引支撑最新事件读取。
- Answer ETag 必须包含当前阶段；阶段事件不会修改 Answer/Workflow version，若 ETag 只含两者会错误返回 304。
- 真实 PostgreSQL 测试必须覆盖最新阶段、无阶段、跨 Workspace 隔离、终态保留、索引执行计划和迁移 Down/Up。

## M6-04 Model Binding And Published Empty-Collection Contract

- 历史 v1 PLAN 和 Faithfulness REVIEW 的 Provider 输入必须包含服务端分配的 `model_run_ref`，模型响应必须精确回显；该字段只加入内存 Chat Request，不得复制进持久 Workflow Input。正式 v2 PLAN 使用无身份 Provider wire，不包含也不回显 `model_run_ref`；项目严格解码后由 Compose 注入冻结的可信 ModelRunRef。
- PLAN 调用方不得在原始 Workflow Input 提供保留字段 `model_run_ref`；输入必须是 JSON object，v1 内存绑定后或 v2 无身份投影后的完整输入均受 `MaxStructuredInputBytes` 限制。
- `RAGRetrievalSummary.Rewrites/Degradations` 与 published `Citations` 的空集合是显式 `[]`，不是 `null`。Repository/Adapter 防御性复制必须使用非 nil 空 slice 作为起点。
- Search snippet 与 Source Span excerpt 进入 Agent Evidence 前在 Retrieval Adapter 边界规范化首尾空白；空白-only 结果属于一致性失败。
- 真实 PostgreSQL/Compose 回归必须覆盖 Refusal 可查询、Markdown Evidence 可打开、v2 `PLAN -> AGENT* -> ANSWER -> INITIAL/REPAIR/REDUCED -> REVIEW` Model Call 以及 exact replay 零新增调用；历史 v1 回放另测其兼容序列。

## Scenario: M7-01 Graph Canonical Read Projection

### 1. Scope / Trigger

- 修改 `internal/graph` 的 PostgreSQL 查询、cursor result window、Relation Evidence、Graph fixture 或查询索引时，
  必须应用本契约。
- Graph 只投影 Knowledge canonical facts，不拥有写模型：首版端点仅允许 `TOPIC|CLAIM`，不得新增 Graph 表、
  Relation 双写、隐式状态修复或读路径副作用。

### 2. Signatures

```go
type QueryPort interface {
    GlobalWindow(context.Context, graphdomain.GlobalRequest) (GlobalResultWindow, error)
    SearchNodes(context.Context, graphdomain.NodeSearchRequest) (graphdomain.NodeSearchResult, error)
    NeighborhoodWindow(context.Context, graphdomain.NeighborhoodRequest) (graphdomain.Neighborhood, error)
    FindPath(context.Context, graphdomain.PathRequest) (graphdomain.PathResult, error)
    NodeDetail(context.Context, foundation.ID, knowledge.NodeRef) (graphdomain.GraphNode, error)
    RelationDetail(context.Context, foundation.ID, foundation.ID) (graphdomain.RelationDetail, error)
    RelationEvidenceWindow(context.Context, foundation.ID, foundation.ID) (RelationEvidenceResultWindow, error)
}
```

- 功能夹具：`SeedFunctional(context.Context, *pgxpool.Pool) (Fixture, error)` 与
  `Cleanup(context.Context, *pgxpool.Pool, foundation.ID) error`。
- 容量夹具：`SeedCapacity(context.Context, *pgxpool.Pool, string) (CapacityFixture, error)` 与
  `CleanupCapacity(context.Context, *pgxpool.Pool, foundation.ID) error`。

### 3. Contracts

- 所有 SQL 显式列、参数化并绑定 Workspace；Node Type、Relation Type/Status 和 traversal 只能来自领域枚举
  白名单。默认正式图只读取 Confirmed Relation，STALE 只能由显式过滤请求，不能伪装成正式边。
- Repository 拥有单个 `REPEATABLE READ READ ONLY` 查询快照并设置事务内 `statement_timeout`；Path 的 frontier
  扩展与最终 hydration 必须在同一快照完成。取消或超时不能泄漏事务，也不能返回未经证明的 partial path。
- Global、depth-1 Neighborhood 和 Relation Evidence 先生成最多 500 项的有界结果窗口，再由 Application
  生成 opaque HMAC cursor。Adapter 不生成 cursor；depth 2/3 只返回有界完整层快照。
- Result-window cursor 的 fingerprint 必须覆盖稳定排序项及 `truncated/reason` 窗口元数据；任一元数据变化都必须
  返回 `GRAPH_CURSOR_STALE`，不能让客户端沿用旧页语义。
- Neighborhood 按完整 frontier 批量查询并批量 hydrate Topic/Claim；Evidence 只做 count/fingerprint，正文和
  每条 reason/applicability 只在独立 Evidence page 返回。禁止逐节点查库和逐边加载 Evidence。
- 功能与容量 fixture 必须写入真实 canonical 表：功能 fixture 使用 Knowledge canonicalization/validation，
  容量 fixture 复用冻结领域枚举、canonical hash 和数据库约束并校验精确计数。清理只有在 Workspace ID、
  固定测试 name、root/git path、`status=test` 和 version marker 全部匹配时才允许执行；缺行视为幂等成功，
  marker 不匹配必须中止且不删除。事务局部的 replica role 只允许测试清理，不得进入生产 session。
- 容量 fixture 由 canonical bounded UTF-8 seed 确定性生成 20,000 个 Active Topic、100,000 条 Confirmed
  IMPACTS Relation 和 100,000 条 Evidence；批量写入后执行 `ANALYZE` 并校验精确计数与热中心 degree=499。

### 4. Validation & Error Matrix

| Condition | Required result |
|---|---|
| Node/Relation 不存在或跨 Workspace | 相同 Not Found 语义，不暴露对象是否存在 |
| cursor 被篡改、跨查询/Workspace 使用或结果变化 | invalid/stale 明确失败，不静默换页 |
| context 或 PostgreSQL statement timeout/cancel | 稳定 timeout/cancel Problem；事务释放且无 partial path |
| 结果窗口、node/edge/frontier 预算超限 | 显式 `truncated/reason` 或请求错误，不执行无界查询 |
| fixture seed 输出失败或 commit 结果未知 | 按精确 marker 尝试清理；清理失败必须合并报告，不能伪装成功 |
| cleanup marker 任一字段不匹配 | 清理失败并保留 Workspace，不按 ID 盲删 |
| 容量计数、6-statement 不变量、P95 或 EXPLAIN 索引门禁失败 | benchmark 非零退出；已经原子写出的产物保留，不能把旧产物冒充本次结果 |

### 5. Good / Base / Bad Cases

- Good：从 canonical Topic/Claim/Relation/Evidence 生成 Workspace-scoped 有界窗口，一跳容量请求无论 degree
  大小都固定执行 6 条数据库语句，Evidence 只在详情展开后分页读取。
- Base：小 fixture 的 PostgreSQL planner 可合理选择 Seq Scan；只有 20k/100k 容量门禁要求指定 source/target/
  owner 索引出现且 Relation/Evidence Seq Scan 为 0。
- Bad：新增 Graph 双写表、逐节点展开、把 Evidence reason 压到 Relation 单值、解析 HMAC cursor 做 SQL 条件，
  或仅凭 Workspace UUID 删除测试/用户数据。

### 6. Tests Required

- Domain/Application/Repository：端点闭包、对称 canonical identity、稳定顺序、cursor replay/stale、预算、
  timeout/cancel、read-only snapshot、Workspace 隔离和 Evidence lazy page。
- `make graph-integration`：真实 PostgreSQL + 生产 Router 的 Global -> Local -> Path -> Evidence，并覆盖
  response-loss、跨 Workspace、stale 与 timeout。
- `make graph-smoke`：已提交 fixture + 真实 API 进程 + 公共 HTTP；成功清理、失败保留 `0700` 诊断目录。
  响应扫描数据库 URL、绝对路径、managed storage 与未公开来源正文；日志额外扫描 Claim/Evidence/provenance
  正文 canary，不得误删公开 Graph 响应要求的 Claim statement 或 Evidence reason。
- `make graph-benchmark`：20k Active Topic/100k Confirmed IMPACTS Relation/100k Evidence 参考拓扑，
  5 次预热 + 30 次采样，单次固定 6 SQL，
  p95 <= 1.5s；保存 Neighborhood/Path/Evidence 的 `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)` 和 `0600` 产物。
- SQL 改动完成后执行 `sql-code-review`；500,000 Relation 最终 P95/FPS 与正式 Auth/Capability 仍由 M10
  验收，Workspace 参数和 HMAC cursor 均不是认证。

### 7. Wrong vs Correct

```text
Wrong: Graph 写一份自己的 Relation，并对 frontier 中每个节点、每条边分别查询邻居和 Evidence。
Correct: Knowledge 保持唯一事实源；Graph 在只读快照中批量展开 frontier、批量 hydrate，并按需分页 Evidence。

Wrong: cleanup 只接收 Workspace UUID 后级联删除，benchmark 只报告一次最快耗时。
Correct: cleanup 先锁行并核对全部测试 marker；benchmark 固定 seed、5 次预热、30 次样本、6 SQL 和索引计划。
```

## Scenario: M7-02 Semantic Link Candidate And Durable Scan

### 1. Scope / Trigger

- 修改 Candidate/Decision/Evidence/Scan、typed Relation Proposal、Approval apply、Topic scan SQL 或迁移 `00024`
  时，必须应用本契约。
- Candidate 是 Graph 拥有的待审阅事实，不是 `core.relation`；只有 `knowledge_change` Proposal 获批并通过
  Knowledge apply 后，才允许创建一条 canonical Confirmed Relation。

### 2. Signatures

```go
type SemanticLinkScanStatePort interface {
    Get(context.Context, foundation.ID, foundation.ID) (graphdomain.SemanticLinkScan, error)
    AdvancePage(context.Context, graphdomain.SemanticLinkScanProgress) (graphdomain.SemanticLinkScan, error)
    Finish(context.Context, graphdomain.SemanticLinkScanTerminal) (graphdomain.SemanticLinkScan, error)
}

type ApprovedKnowledgeChangeApplier interface {
    ApplyApprovedKnowledgeChange(context.Context, changecontrol.ApprovedKnowledgeChange) error
}
```

- 数据库事实：`graph.semantic_link_candidate`、`semantic_link_candidate_evidence`、
  `semantic_link_candidate_decision`、`semantic_link_command_receipt`、`semantic_link_scan`；Change Control 以
  additive `proposal_type=knowledge_change` 保存 typed revision，Knowledge 继续拥有 `core.relation`。
- 公共入口：`GET /api/v1/graph/candidates`、Candidate detail/decision、
  `POST /api/v1/graph/candidate-scans`、Scan detail，以及既有 Proposal Approval。

### 3. Contracts

- Candidate fingerprint 绑定 Workspace、canonical 端点及版本、Relation Type、排序 Evidence semantic hash 和
  实际 Rule/Model/Index/Embedding/Rerank/Prompt/Schema 版本；并发重复只能形成一个当前 fingerprint 记录。
- Evidence 行 ID 是 Candidate-owned identity；同一 Claim Source 可支撑多个 Candidate，但每个 Candidate 的
  Evidence 必须重新生成独立 UUID，不能直接复用 `claim_source.id` 作为全局主键。
- Ignore/False Positive/Defer/Resume/Confirm 决策 append-only；当前状态用 expected version CAS 更新。
  同一 Candidate 最多绑定一个独立 Relation Proposal，批量确认不得合并成一个大 Proposal。
- Approval apply 在同一数据库事务内重新校验 Candidate fingerprint、端点版本、Evidence 可达性和已有
  canonical Relation；stale 进入 `needs_revision`，事务失败不留下半条 Relation/Proposal 状态。
- Topic scan 通过 Workflow/River 持久化 checkpoint。`(workspace_id,idempotency_key)` 唯一保证精确重放；
  fingerprint 仅用于查询，不得唯一化 FAILED/CANCELLED attempt，新 key 可以启动同 fingerprint 的新 attempt。
- Topic Claim pair SQL 必须使用每个 source 的 `CROSS JOIN LATERAL ... ORDER BY claim.id LIMIT 100`；部分索引
  `idx_knowledge_relation_semantic_scan_topic_claim` 与 CLAIM→TOPIC/BELONGS_TO/CONFIRMED 谓词完全一致。
- Candidate query 的 `CLAIM` scope 精确匹配 source/target；`TOPIC` scope 还可匹配直接 Topic 端点或两端 Claim
  都通过正式 CLAIM→TOPIC/BELONGS_TO/CONFIRMED Relation 归属该 Topic 的 pair。membership 使用两个参数化
  `EXISTS`，不能用可能复制分页行的普通 JOIN，也不能退化成 Workspace 全量或 discovery-method 猜测。
- PostgreSQL `timestamptz` 只有微秒精度。Application 校验持久化终态时间时允许小于 1 微秒差异；禁止把
  纳秒级严格相等失败解释为业务失败，否则会出现 Scan 已成功而 Workflow Run 被误记 failed。

### 4. Validation & Error Matrix

| Condition | Required result |
|---|---|
| Candidate/Scan 不存在或跨 Workspace | 相同 Not Found，不暴露资源是否存在 |
| 同幂等键不同请求、过期 expected version、非法状态迁移 | 稳定 conflict/invalid，不覆盖历史 Decision |
| Candidate 端点/Evidence 在 Approval 前漂移 | Proposal `needs_revision`，不创建 Relation |
| Knowledge apply 或 Evidence 写入失败 | 整个事务回滚，Candidate 不伪装成正式确认 |
| FAILED/CANCELLED scan 使用旧 key 重放 | 返回原 attempt；新 key 创建同 fingerprint 新 attempt |
| Provider/Repository/Scan dependency 缺失 | Semantic Link 独立 unavailable；七个正式 Graph 查询保持 ready |
| Topic scope 下只有一端 Claim 属于该 Topic，或 pair 属于其他 Topic | 从结果排除；不能因 Scan 成功而放宽到 Workspace 全量 |
| 持久时间与命令时间仅相差 PostgreSQL 子微秒精度 | 视为同一终态；差异达到 1 微秒仍判 projection mismatch |
| Pair SQL 未命中部分索引、出现 relation Seq Scan 或内层 Limit 未按 source 执行 | EXPLAIN 集成门禁失败 |

### 5. Good / Base / Bad Cases

- Good：用户确认 Candidate 后创建独立 typed Proposal；Approval 在重新校验后幂等写一条 Relation，重复响应
  丢失重放不新增 Proposal、Relation 或 Evidence。
- Good：Topic scope 同时返回直接 Topic Candidate 与双方均为正式成员的 Claim pair，并保持稳定无重复分页。
- Base：Semantic/RAG signal 未配置时明确标记 unsupported；确定性 Rule signal 仍执行，不能把未运行能力标成
  success 或空结果。
- Bad：Candidate 直接写 `core.relation`、共享 `claim_source.id` 作为多个 Candidate Evidence 主键、给 scan
  fingerprint 加唯一约束、先展开 source×全部 target 再 top-100，或严格比较纳秒时间。

### 6. Tests Required

- Domain/Application：fingerprint、状态机、typed Proposal hash、Approval stale/rollback/replay、终态时间精度。
- 真实 PostgreSQL：Candidate 并发/分页/Decision、每 Candidate Evidence 唯一 ID、FAILED/CANCELLED restart、
  Topic 直接端点/双边 membership/单边排除、205 Claim 三页 pair 数 `10000/5440/10`、跨页边界和同一生产
  SQL 的 EXPLAIN。
- `ZHIXU_TEST_DATABASE_URL='postgres://...' make semantic-link-integration`、
  `ZHIXU_TEST_DATABASE_URL='postgres://...' make semantic-link-fault-smoke`、`make semantic-link-eval`、
  `ZHIXU_TEST_DATABASE_URL='postgres://...' make semantic-link-smoke`、迁移空库/重复/Down-Up/guarded Down、OpenAPI
  与全仓 race/vet/tidy。
- SQL 改动必须追加 `sql-code-review`；公共 API、Workflow、Proposal/Approval 和前端跨层变更必须有独立只读复验。

### 7. Wrong vs Correct

```text
Wrong: Scan Finish 已提交后，因为 PostgreSQL 丢失纳秒精度而返回 consistency error，Worker 再把 Run 标为 failed。
Correct: 终态身份、版本、状态严格比较；持久时间按 PostgreSQL 微秒精度比较，避免把已提交成功误判失败。

Wrong: 用 row_number() 生成全部 source×target 后筛 rank<=100。
Correct: 对每个 source 使用 LATERAL 内层 LIMIT 100，并以部分索引和 EXPLAIN 证明执行量有界。

Wrong: Topic scan 成功后用 Workspace 全量 Candidate 填充面板，或 JOIN membership 导致候选重复。
Correct: Topic scope 对两端 Claim 分别使用 EXISTS 验证正式 membership；Claim scope 仍精确匹配端点。
```

## Scenario: M9 Workspace Business List Read Models

### 1. Scope / Trigger

- 修改 Source Version、Proposal、Workflow 的 Workspace 列表接口、筛选、keyset cursor 或 SQL 投影时应用。
- 这些列表是领域事实的只读投影，不新增 Dashboard/Inbox 状态表，也不推进 Proposal、Workflow、Ingestion 或 Index 状态机。

### 2. Signatures

```text
GET /api/v1/workspaces/{workspace_id}/source-versions
GET /api/v1/workspaces/{workspace_id}/proposals
GET /api/v1/workspaces/{workspace_id}/workflows
```

- Repository 输入必须包含 `WorkspaceID`、白名单筛选、`CursorTime + CursorID` 和 `Limit=1..100`。
- 排序分别固定为 `captured_at DESC,id DESC` 或 `updated_at DESC,id DESC`，查询使用 `limit + 1` 判断下一页。

### 3. Contracts

- Cursor 是最大 2048 字节的 opaque base64url JSON，必须绑定 `version=1`、资源 `kind`、Workspace、canonical filters、最后时间和 ID；缺失、未知字段、跨资源、跨 Workspace 或跨筛选复用全部拒绝。
- SQL 只使用显式列和参数绑定；动态列名只来自代码内固定白名单，不接受请求值作为标识符。
- `change_control.proposal.risk_level` 是审批、响应和筛选的唯一等级事实，只允许精确
  `CRITICAL|HIGH|MEDIUM|LOW`；`proposal_revision.risk` 是独立自由文本，任何读路径、API 或审批不得从它派生等级。
- 新 Proposal 必须显式等级并使用包含 `risk_level` 的 v2 request hash。v1 只允许已持久历史精确重放；等价
  v2 可以重放历史 v1，v1 不可反向重放新建 v2。Semantic Candidate Proposal 固定 `HIGH`，其 v1/v2 重放都必须核对持久等级。
- `core.source_version.workspace_id` 只镜像所属 `core.source.workspace_id`：新生产写入显式携带已持久 Source 的
  Workspace，兼容 trigger 只为旧 writer 补空值并拒绝错值；最终由 `(source_id,workspace_id)` 与
  `(workspace_id,content_artifact_id)` 两条复合外键以及不可变 trigger 防止漂移。
- M9 hardening 必须拆为 `00030` Expand、`00031` non-transactional concurrent indexes、`00032` exact backfill、
  `00033` Contract。Contract 后 `risk_level` 与 Source Version `workspace_id` 均为无默认 `NOT NULL`。
- Source Version 的 Index Status 只来自 Workspace 当前 `retrieval.index_version.status='active'`：`included` 绑定具体 Source Version，`excluded` 绑定 Source，不能按最新 manifest 时间猜测当前状态。
- Proposal 摘要读取最新 Revision；Workflow 摘要读取持久 Run 与 pending Human Task，不逐项二次查库。
- Workspace ID/cursor 只限定查询范围，不是认证凭据；正式 Auth/Capability 仍由 M10 执行。

### 4. Validation & Error Matrix

| Condition | Required result |
|---|---|
| 未知/重复 query、limit 越界、非法枚举 | 400，Repository 调用次数为 0 |
| cursor 超长、损坏、缺 kind/version 或 kind 不匹配 | 400，不降级为第一页 |
| cursor Workspace/filter 不匹配 | 400，不执行 SQL |
| Source 被 active manifest 排除 | 对该 Source 投影 `excluded`；不得因 `source_version_id IS NULL` 丢失 |
| 较新的 building/failed manifest 存在 | 仍只显示 active index 选择状态 |
| 无 active index/manifest | Index Status 为空，不伪造 included/excluded |
| Proposal 创建缺少/填充小写或空白等级 | 400/Domain invalid，不从 Revision risk 猜测 |
| 历史 Candidate 的 Revision risk 为 `MEDIUM` | 精确回填并保持 Proposal `risk_level=HIGH` |
| Source Version 显式 Workspace 与 Source/Artifact 不一致 | 数据库拒绝，事务无半写 |

### 5. Good / Base / Bad Cases

- Good：同一时间戳多行按 ID 稳定翻页；Proposal/Workflow/Source cursor 不能互换；Active Index 的 included/excluded 与真实 manifest 一致。
- Base：空列表返回 `items: []` 且没有 cursor；无 Active Index 时 Source 仍可显示安全、解析和 Workflow 状态。
- Bad：共享 `{workspace,at,id,filter}` cursor 导致跨资源错页；按 manifest `created_at DESC` 取“最新”选择；用 offset 或无界 total 扫描。

### 6. Tests Required

- HTTP：正常/空、未知与重复参数、全部枚举、limit 边界、cursor 超长/损坏/旧格式/跨 kind/Workspace/filter。
- PostgreSQL：Workspace 隔离、同时间戳稳定顺序、Proposal 最新 Revision、Workflow waiting-human、Source included/excluded/无 Active Index/building 比 active 更新；v1/v2 Proposal replay、Candidate HIGH、Source Version 历史回填/旧写兼容/错绑拒绝和复合 FK。
- 迁移：空库 Up、重复 Up、空数据 Down→Up、业务数据 guarded Down、脏数据回填原子失败、并发索引 valid/ready。
- 对三条生产 SQL执行 `EXPLAIN (ANALYZE, BUFFERS)`；Source Version 根路径必须命中
  `idx_source_version_workspace_captured_id`，latest Attempt 必须命中 `idx_ingestion_attempt_source_started_id`，
  两条访问路径都不得出现 Seq Scan 或全量 Sort。未配置 `ZHIXU_TEST_DATABASE_URL` 时必须标为未验证。

### 7. Wrong vs Correct

```text
Wrong: 三类列表共用无 kind/version cursor，或把 Workspace cursor 当成身份授权。
Correct: 每个 cursor 绑定 version/kind/workspace/filters；后端授权边界独立存在。

Wrong: index_manifest_source 按 source_version_id 连接 excluded 行，或选最新 created_at manifest。
Correct: 先锁定 Workspace active index，再按 source_id 连接；included 额外匹配 source_version_id，excluded 保持 source 级语义。
```

## Scenario: M9 Export Persistence And Recovery

### 1. Scope / Trigger

- 修改 `ops.export_job`、`00036_export_hardening.sql`、`00063_m9_attachment_export.sql`、Export Repository、
  Job cursor、capability gate、生命周期事件、下载 Audit、Worker recovery 或 cleanup 时应用。
- 该表保存 `COLLECTION` 与 `WORKSPACE_ATTACHMENTS` 两种 tagged Job；`EVALUATION_JSON|AUDIT_JSON`
  没有持久内容源，不能靠枚举、空 payload 或迁移列把它们标为已交付。

### 2. Signatures

- `COLLECTION = MARKDOWN|METADATA_JSON + collection_id/version/query_hash + export/v1`。
- `WORKSPACE_ATTACHMENTS = ATTACHMENTS_ZIP + attachment-export/v1 + workspace-attachments/v1 + RAW_USER_OWNED`，
  Collection binding 必须全部为 `NULL`。
- 请求 identity 为 `(workspace_id,idempotency_key,request_hash)`；列表 identity 额外绑定 scope、owner filter 与 limit。
- attachment capability gate 持久化 contract version，默认关闭；Create/Claim 必须在事务内读取 gate。

### 3. Contracts

- `ops.export_job` 是 request hash/TTL、tagged scope、状态/version、lease、冻结 source/result、prepared staging/final
  binding、hash/size、过期/cleanup 与下载统计的唯一事实源。River 只表示 transport，文件系统只保存受 Job binding 约束的结果。
- Create 必须在同一事务中对 `(workspace_id,idempotency_key)` 取 advisory lock，先 exact replay，再验证当前
  Collection 或 attachment gate。request hash 覆盖规范化请求；`expires_at` 只在首次插入时按数据库时间计算，replay 不延长 TTL。
- List 使用 `(created_at DESC,id DESC)` keyset，并把 Workspace、scope/owner filter 与 limit 编入 opaque cursor identity。
  所有根查询参数化并带 Workspace predicate；Get/List 的数据库时间归约 `PENDING|RUNNING|SUCCEEDED|FAILED` 到
  `EXPIRED`，不能仅依赖前端刷新。
- Claim/Prepare/Complete/Fail 的更新必须同时比较 version、lease owner、数据库当前时间的 lease 与 `expires_at`。
  Prepared binding 对 Collection 写入 revision/count，对 attachment 写入 manifest digest/entry count/total bytes；两者都冻结
  staging/final 相对路径、SHA-256 和 size。后续状态转换只接受完全相同 binding，且不得重读 prepared attachment source。
- `EXPIRED` Job 的 cleanup 使用持久 status/attempt/error/file_deleted_at 重试；Job/hash/download/Audit 历史不删除。
  orphan 查询只能确认受控 staging 命名空间中没有 prepared binding 的文件，不能扫描或删除普通 Workspace 文件。
- 成功下载在一个 PostgreSQL transaction 中 CAS 增加 `download_count/last_downloaded_at`，并以当前 actor 追加
  `ops.audit_event(action=export.download)`。Audit metadata 只含 Export ID、hash、size 与服务端已准备返回的 outcome；
  不记录正文、Secret、绝对路径或“客户端已经收完”。
- 每次 create/claim/prepare/complete/fail/expire/cleanup 的 `ops.server_event` 与 Job 转移同事务追加；
  `source_event_ref=export.<stage>:<id>:v<version>` 保证事务重试不会形成第二事件。
- 迁移仅前向扩展；存在 Export Job 时 Down 必须以 SQLSTATE `55000` 拒绝。发布回滚保留 Job、Audit 和文件，采用
  forward fix，不能删除事实恢复旧 schema。

### 4. Validation & Error Matrix

| Condition | Required result |
|---|---|
| scope/kind/schema/policy 或 Collection/attachment binding 交叉 | `CHECK ... IS TRUE` 拒绝，包括 `NULL/UNKNOWN` 绕过 |
| gate 关闭或 contract version 不匹配 | 不创建、不 Claim attachment Job；Collection 路径不受影响 |
| 同 key 同 request hash | exact replay 同一 Job；TTL 不延长 |
| 同 key不同 request hash | 稳定 idempotency conflict；不插入第二行 |
| lease owner/version/数据库时间任一不匹配 | CAS 不更新；旧 Worker 不得提交终态 |
| 下载统计或 Audit 任一步失败 | 同一 transaction 整体回滚 |
| 存在 attachment Job 时 Down | SQLSTATE `55000`，保留所有事实 |

### 5. Good / Base / Bad Cases

- Good：兼容版本先迁移并保持 gate 关闭，全部 API/Worker 就绪后原子启用；附件 Job 使用独立 scope 查询与索引。
- Base：历史 Collection 行前向回填为 `COLLECTION`，既有 ID、状态、hash、TTL 和 Audit 不变。
- Bad：用 nullable 字段堆叠但不写互斥 `CHECK`，或让 `CHECK` 的 `NULL/UNKNOWN` 被 PostgreSQL 当作通过。

### 6. Tests Required

- 空库/重复 Up、现有 `00036` 数据升级、空数据 Down→Up、有 attachment Job 的 guarded Down；真实 INSERT 负测必须覆盖
  scope、prepared/path 约束的 `NULL/UNKNOWN` 绕过。
- 真实 PostgreSQL 覆盖两 scope 的同 key 并发/response-loss、gate 开关、跨 Workspace、cursor、数据库时间 lease/TTL、
  双 Worker fence、Prepare/Complete 崩溃窗口、过期/cleanup retry 与下载并发。
- 生产 SQL 使用显式列、参数绑定和对应索引；解释计划必须验证 scope list、expiry/recovery、cleanup 和
  prepared staging lookup 不退化为无界扫描。

### 7. Wrong vs Correct

```text
Wrong: 先检查当前 Collection/附件目录或 gate，再读取同 key Job；响应丢失后当前态变化会制造冲突或第二个任务。
Correct: advisory lock 下先 exact replay；只有不存在既有 Job 时才验证当前 scope 与 gate 并插入。

Wrong: Complete 只校验进程 owner，下载统计与 Audit 分两次提交。
Correct: 所有 lifecycle fence 使用 PostgreSQL 当前时间；统计 CAS 和 export.download Audit 在同一事务提交。

Wrong: CHECK 依赖 SQL 三值逻辑，允许必填 binding 为 NULL 时表达式变成 UNKNOWN。
Correct: tagged scope 与 prepared/path 约束使用显式 NOT NULL，并把完整布尔表达式写成 IS TRUE。
```

## Scenario: M8 Review, Interview And Memory Persistence Contract

### 1. Scope / Trigger

- 修改 `learning.review_*`、`learning.interview_*`、`learning.learning_path*`、`learning.memory*`、`agent.rag_memory_snapshot`、
  Agent Model Run Memory tuple、Artifact visibility hold、迁移 `00035`、`00038`、`00044`–`00061`、Review/Interview/Memory
  PostgreSQL Repository 或相关 Worker maintenance 时应用。
  `00056_m8_interview_provenance_shell_guard.sql` 保留 provenance shell guard，Completion reservation 只能由
  `00057_m8_interview_completion_reservation.sql` 追加；`00059` 强化 Interview Memory 完整性，`00060` 将 Path 升格为共享基表并保留
  `learning.interview_learning_path*` 可更新兼容视图。
- 该场景锁定 Review/Interview 共享 Path、学习状态、Memory 生命周期和最小可见性边界；Health/Timeline 与兼容视图只能消费或转发
  基表事实，不能成为 Review、Interview 或 Path 的第二事实源。

### 2. Signatures

- Review receipt identity：`(workspace_id,idempotency_key,request_hash,command_type)`；Answer 写入同时绑定 Session、Deck、
  Card、question_ref、Score snapshot、冻结 Scheduler version 和产生评分的 Scorer version。
- `question_ref` 由 API-only HMAC key 签发并绑定 Workspace/Session/Deck/Card fingerprint 与 Card/Schedule version。显式 key 至少 32
  canonical bytes；`required` 缺省时从 Bootstrap Token 域隔离派生，local `disabled` 缺省时才生成进程 key。多实例必须共享
  显式 key；key 轮换或 local 重启后刷新未提交题，已持久 Answer 的 receipt/response snapshot 仍是 exact replay 唯一依据。
- Interview receipt identity：Start/Turn/Complete/Path status/step 独立保存 Workspace、session/path identity、request hash、
  version 和完整 response snapshot；不得写入 `learning.review_answer` 或 `learning.review_schedule`。
- Shared Path identity：`learning.learning_path.origin_type` 只能为 `INTERVIEW|REVIEW`。Interview 必须绑定 session+report 且
  `review_answer_id IS NULL`；Review 必须唯一绑定同 Workspace 的不可变 Answer，且 Interview source fields 均为 NULL。
- Review Path command identity：`learning_path_command` 以 `(workspace_id,idempotency_key)` 绑定 request hash、
  `CREATE_REVIEW_PATH|PATH_STATUS|PATH_STEP`、expected/path version 与完整 response；创建 reservation 以
  `(workspace_id,review_answer_id)` 唯一绑定 `source_snapshot/source_snapshot_digest/artifact_digest`、Path/Artifact tuple 与
  `PENDING|COMPLETED|ABANDONED`。
- Memory command identity：用户命令以 stable USER owner + Workspace + Idempotency-Key 绑定；`source_type/source_ref` 只由
  服务端构造并可在响应中只读投影，任何用户命令都不能提交或修改；effective query 必须包含 Workspace、owner、可选
  task scope 与数据库当前时间。
- Interview Candidate identity：外层 `(workspace_id,idempotency_key)` 绑定完整 CREATE_CANDIDATE request hash；内层
  `(workspace_id,stable owner,source_type='INTERVIEW',source_ref)` 由 `00054` partial unique index 绑定一条语义 Candidate。
- Conversation RAG snapshot identity：`(workspace_id,node_attempt_id)` 唯一；`PREPARING` claimant 绑定 Workflow/Node/Attempt、
  stable owner 与 `task_scope_id=conversation_id`，`READY` 双向绑定一个 Model Run 和完整
  `(snapshot_id,schema_version,digest,item_count,byte_count)` 审计 tuple，正文不持久化。

### 3. Contracts

- Review Answer、Score、FSRS Schedule 与 receipt 用一个 PostgreSQL transaction/CAS 写入。Review Session 必须为 REVIEW
  并绑定 Deck；Repository 读取和写入都验证 Card/Deck/Session/Workspace，不允许 HTTP 或 Application 只校验其中一层。
- `00055` 将既有 Answer 的未知评分实现显式回填为 `legacy/unknown`；新 `scorer_version` 必须去除首尾空白后非空且不超过
  128 bytes。Answer 是不可变审计事实，存在任何 Answer 时 Down 必须以 SQLSTATE `55000` 拒绝删除该字段。
- `00052` 的 Claim lifecycle trigger 对任何从 CONFIRMED 离开的状态失效 APPROVED Card；legacy evidence quarantine 使用
  `try_review_evidence_uuid`，所有 due SQL 都不得直接将可变 JSON 文本 cast 为 UUID。失效 Card 不得保留 ACTIVE Schedule。
- `00049` 的 `REVIEW_INVALIDATED` Health Issue 与 Timeline outbox 是由 `learning.review_card` 派生的最小兼容投影；
  不能据此 resolve/recreate Card，也不能把该表当成完整 downstream impact 数据源。
- Claim/Source 生命周期失效命令每次最多处理 200 张 Card，使用单条 `UPDATE ... FROM ... RETURNING` 驱动既有触发器，
  receipt 只保存 `invalidated_count/has_more` 有界摘要。`has_more=true` 时调用方必须用新 Idempotency-Key 继续，禁止
  无界锁定全部匹配 Card、逐卡 load/update 或把完整 Card 列表写入 receipt。
- `00058` 用 `review_card_evidence_selector` 保存由 APPROVED Card evidence 派生的只读 selector identity 与 Card Claim，
  并分别以 `(workspace_id,selector_kind,selector_id,card_id)` 和
  `(workspace_id,selector_kind,selector_id,claim_id,card_id)` 索引支持 Source-only 与 Source+Claim 批次；APPROVED Claim
  路径使用 `(workspace_id,claim_id,id)` partial index，Health 指纹重算使用对应的 INVALIDATED partial covering index。投影由
  evidence trigger 重建，Card JSON 仍是唯一证据事实源；多个 selector 按 Card 级 AND 组合，不要求同一 Evidence item
  同时匹配；失效热查询不得重新对 Workspace 全量 Card 执行 `jsonb_array_elements`。
- `00058` 将 Review invalidation Health 投影从逐 Card trigger 改为 transition-table statement trigger；每条 Card 语句
  只能调用一次集合化 batch helper，由一组 SQL 同时聚合全部受影响 Workspace/Claim，不能按 Card 或 Claim 循环调用
  scalar 聚合。Schedule/SSE 仍按 Card 逐行产生同事务事实，不能因投影优化而跳过。
- `00059` 将 INTERVIEW Memory 的 opaque `source_ref` 投影为结构化 session/path/step columns，在迁移时先拒绝 malformed
  历史引用，再以复合 FK 验证真实 Path chain；部分唯一索引保证 stable owner 对同一步骤最多一个 Candidate。来源列与 canonical
  `source_ref` 不可变，初始 Candidate 必须固定 USER owner、GOAL、session scope、无 expiry，并等于 Path Step 的 title/rationale。
- `00059` 还要求 `(workspace_id,memory_id,memory_version)` 只有一条 Audit；Audit owner、to_status、version 与当前 aggregate 一致，
  action 必须匹配生命周期转换。Interview completion reservation 或 Review Path creation reservation 的 replay 不能只更新
  `updated_at` 延长 24 小时维护窗口。
- `00060` 的 `learning.learning_path(_step)` 是两个 origin 的唯一基表；旧 Interview relation 只是单表可更新视图。Path/Step 的
  source、Evidence、Artifact binding 与 created_at 是 retained history，不可编辑或删除；只允许 version+1 的既定状态转换。
  Review origin 由唯一 Review Answer 和 `review-gap/v1` 服务端快照驱动，客户端不得提交 gap、Score 或 Evidence。
- `00056` 在数据库边界验证 `interview-evidence/v2` 的精确八字段 JSON、受限 UUID、Claim Source、active Index、Source/Chunk
  Manifest 与 canonical Chunk；follow-up 只能复制 parent Claim/Evidence。Interview Session 与 Review Answer child 写入必须锁同一
  `review_session` parent，parent 改型 trigger 反查 child，封闭两个提交方向；已有 Interview/Question 或 Review Answer 时 Down
  以 SQLSTATE `55000` 拒绝移除 provenance/shell guard。
- Interview Completion 使用 Begin/Prepare/Complete reservation，而不跨 Artifact/Interview 模块共享物理 transaction。
  Begin 冻结 Session snapshot；PENDING reservation 阻止后续 Submit。Prepare 先持久化 digest；随后的 Artifact PLAN receipt 与
  hidden hold 在一个 Artifact 事务写入，数据库 fence 锁 reservation，并只接受 PENDING、匹配 digest/type/role/stage key 的绑定。
  Complete 的最终事务精确核对 reservation/digest/Artifact，
  写入 Report/Path/receipt 并只 release 对应 hold。失败 Artifact 不可由普通 Get/List 读取，保留 stable identity 供 exact replay。
  Worker 在 24h 有界维护中将超时 reservation 转为 ABANDONED、其 hold 转为 ORPHANED；ORPHANED 继续隐藏。本范围不物理删除
  恢复或审计资产。
- Review Path 创建同样使用 Answer-scoped reservation、digest、`LEARNING_PATH_CREATE/PATH` hidden hold 和精确 PLAN receipt fence；
  Complete 必须在一个事务中写共享 Path/Step/receipt、完成 reservation 并只 release 自己的 hold。Repository SQL 必须使用
  `00060` 的真实列名、command type 和非空 receipt 字段；在真实 PostgreSQL 契约验证前不得宣称该链路可运行。
- Memory 表约束 Candidate insert、Confirm→ACTIVE、暂停/到期/删除与 append-only audit；读取 effective context 时在 SQL 根查询
  同时过滤 `status='ACTIVE'`、confirmation、expiry、Workspace、stable owner 和 task scope，禁止先全量读取再进程过滤。
- `00061` 只对 `workflow.node_run.node_type='agent.rag-answer'` 的 Conversation RAG 强制完整 Memory tuple；不能仅凭
  `model_run.output_schema_id='agent.rag-answer'` 判定，因为其他 Agent 节点可以复用该输出 schema。唯一 claimant 才能调用
  `LoadEffective` 一次；`READY` snapshot、Model Run insert 与双向 binding 必须同事务先于 Provider，`PREPARING|FAILED` 或
  `READY` 缺失 Model Run 均 fail closed 且不得重新加载。
- Model Run scanner 当前读取 32 列。新增持久列时必须同步 `modelRunSelect`、insert args、replay equality，以及 recovery 的
  手写 `UPDATE ... RETURNING` 列表；只修改公共 scanner 而漏掉 recovery 会让真实 stale-run 扫描在运行时失败。
- Interview Candidate Repository 固定先锁客户端 command key，再锁 provenance。相同 key 的不同 hash 在取得 provenance 锁前
  冲突；不同 key 的等价 provenance 从原 CREATE_CANDIDATE receipt 恢复初始 Candidate snapshot，并为新 key 写绑定既有
  Memory ID 的 receipt。并发只能产生一条 Candidate 和一条 `CANDIDATE_CREATED` Audit；USER provenance 不参与该唯一化。

### 4. Validation & Error Matrix

| 条件 | 必须结果 |
|---|---|
| Review 同 key 不同 hash、CAS 过期或并发 Answer | stable conflict 或 exact replay；无第二 Answer/Schedule |
| Review `question_ref` 的 key 轮换、不同 key 实例、local 随机 key 重启、签名无效或 Session/Card/Workspace 不匹配 | 刷新 Due Question 或 stable conflict；不写 Answer/Schedule，已落库 Answer 仍按 receipt exact replay |
| Claim DISPUTED 或 legacy Card evidence 不是合法 `review-evidence/v1` | 同事务失效 Card/删除 Schedule；due 安全返回 |
| Claim + Source Version/Span 组合失效 | 所有 selector 按 Card 级 AND；Source+Claim 直接走含 `claim_id` 的索引，Version+Span 不要求同一 Evidence item |
| Health/Timeline projection 写入失败 | 包含 Card 生命周期变化的事务回滚；不得留下只有 Card 或只有投影的一半事实 |
| Interview evidence malformed/tuple 漂移，或 parent/child 并发改型 | `23514` 或并发一方失败；不得留下非法 provenance 或跨类型 Session 历史 |
| PENDING Completion reservation、snapshot/digest/Artifact 不匹配或 final receipt 已存在 | Submit 拒绝、stable conflict 或 exact replay；不 release 其他 hold、不公开 Draft |
| 超过 24h 的 PENDING reservation | Worker 有界维护转 ABANDONED；关联 hold 转 ORPHANED 并继续被普通 Artifact 查询拒绝 |
| Interview Candidate 相同 key 绑定不同 request hash | provenance 副作用前 idempotency conflict；不产生第二 Candidate/Audit |
| Interview Candidate 不同 key 绑定相同 provenance/内容 | 复用初始 Candidate snapshot 和 Memory ID；每个 key 持久化独立 receipt |
| Memory source/owner 试图变更、Candidate 未确认、PAUSED/EXPIRED/DELETED | 触发器/Repository 拒绝变更或 effective query 排除 |
| 同一 Node Attempt 已有 `PREPARING|READY|FAILED` snapshot | 非 claimant 不加载 Memory、不调用 Provider；返回稳定 manual recovery/conflict |
| Conversation RAG Model Run 缺 Memory tuple，或非 Conversation 节点绑定 snapshot | CHECK/trigger `23514` 或 consistency failure；不得落部分审计事实 |
| `00061` 存在 snapshot/绑定 Model Run 时 Down | SQLSTATE `55000`，迁移版本与双向审计事实保持不变 |
| Interview Memory 的结构化 session/path/step 与 source_ref 或真实 Path chain 不一致 | `23514`；不允许靠猜测回填或留下悬空 Candidate |
| Memory Audit version/owner/status 与 aggregate 不一致，或 action 不符合状态转换 | `23514`；aggregate 写入与唯一 Audit 必须一起成功或一起回滚 |
| Shared Path origin/source shape、Review Answer/Artifact tuple 或 version transition 不一致 | CHECK/FK/trigger 拒绝；不得跨 origin 改型、编辑历史证据或重新打开终态 Path |
| Review Path command 名称、列名或必填 receipt 字段与 `00060` 不一致 | Repository 集成失败；在修正并用真实 PostgreSQL 证明前不得注册为 production capability |

### 5. Good / Base / Bad Cases

- Good：所有 learning root query 使用显式列、参数化 Workspace predicate、bounded keyset/limit；Card/Schedule、共享 Path、Interview
  facts 和 Memory 各有单一 owner，兼容视图/投影只可转发、重建或观察；Complete 仅 release 精确 digest/Artifact 对应 hold；
  Conversation RAG 在 Provider 前持久化无正文 snapshot digest 与 Model Run binding。
- Base：旧 Card 无法验证时被 quarantine；超时 Interview reservation 的 Artifact 留在 ORPHANED hidden hold；过期 Memory
  保留审计历史但不进入上下文。
- Bad：`(evidence->>'claim_id')::uuid` 直接 cast、先生成公开 Artifact 再写 Interview、为 Review 再建一套 Path 表、用 Health Issue
  驱动 Schedule，或把 source_type/source_ref 暴露为浏览器命令字段。

### 6. Tests Required

- 迁移：空库/重复 Up、业务数据 guarded Down、`00035` legacy receipt/Answer hardening、`00038` COMPLETE_SESSION receipt
  forward/guarded Down、legacy APPROVED Card quarantine、DISPUTED invalidation、`00049` projection 的 fingerprint/reopen/resolve、
  `00053` hold、`00054` INTERVIEW partial uniqueness/Down、`00055` legacy Scorer version 回填/约束/空数据 Down→Up/有 Answer
  guarded Down、`00056` malformed provenance/parent-copy/shell 双向竞态/业务数据 guarded Down、`00057` legacy ORPHANED 回填、
  PLAN receipt + digest fence、NULL digest 安全归一化、role/artifact mutation、late Create 与 maintenance 竞态、24h
  ABANDONED→ORPHANED/guarded Down；`00059` 覆盖 malformed legacy source_ref、结构化 FK/每步唯一 Candidate、Audit aggregate/version/
  lifecycle、completion keepalive 与 Session immutable binding；`00060` 覆盖共享基表/兼容视图、origin shape、Review Answer 唯一绑定、
  command/reservation/hold、Path/Step retained history、keepalive、Interview Memory provenance 重绑定与有数据 guarded Down。另验证
  malformed JSON 不导致 due 查询失败。
- Repository integration：Review Answer transaction/replay/CAS/Deck binding、question_ref key-change refresh/Answer exact replay，
  Review invalidation projection 回填/清理、Claim/Version/Span 全组合 Card 级 AND、201/200 续批、新 key 继续与原 key replay、
  200 张跨 Claim Card 的单次 set-based Health/Timeline/Schedule/SSE 原子投影，
  Interview Begin/Prepare/Complete snapshot/digest/hold/release/failure replay 和 24h bounded maintenance，Memory owner/source
  immutability、effective filter、expiry、双层幂等 exact/semantic replay、同 key 异请求冲突、并发每-key receipt/单 Candidate/Audit
  和跨 Workspace 拒绝；Review Answer→Path 的 frozen gap/citation、唯一 reservation、exact replay/CAS、Artifact hold/release、
  command type/expected version/response 字段及 24h maintenance。
- `00061` / Agent PostgreSQL：Up/repeated Up、all-null/all-present tuple、Conversation node-type fence、唯一并发 claimant、
  `PREPARING -> READY|FAILED`、Model Run 双向原子 binding、stale recovery scanner、业务数据 guarded Down 和 clean Down -> Up。
- SQL review：所有 JSON 访问安全、无字符串拼接；检查 due/expiry/path 查询索引、无 N+1/逐条写入，以及 visibility release
  与 completion receipt 的失败顺序。

### 7. Wrong vs Correct

```text
Wrong: 直接把 legacy evidence JSON 文本 cast 为 UUID，或只把 due 查询加 WHERE 过滤。
Correct: 使用受限安全转换并迁移 quarantine；Card、Schedule 和查询投影收敛到同一失效事实。

Wrong: Source+Claim 先扫描高扇出 Source 再过滤 Claim，或让 Version+Span 只能匹配同一 Evidence item；statement trigger 再逐 Claim 调 scalar 聚合。
Correct: selector 投影 Claim 并为 Source-only/Source+Claim 提供独立有序索引；Version 用同 Card EXISTS；每条 Card 语句只调用一次 set-based Health helper。

Wrong: 用一个跨模块数据库事务假装 Artifact/Interview 原子性，或在最终提交时给未知来源 hold 补 digest。
Correct: 用 reservation + PLAN/hold digest fence + exact release 表达可见性原子性；超时由 Worker 有界转 ABANDONED/ORPHANED。

Wrong: 用 step-derived key 替换客户端 Idempotency-Key，或只靠 unique violation 事后收敛 Candidate。
Correct: 先锁并校验客户端 key 的完整请求，再锁 INTERVIEW provenance；语义复用写新 receipt 并返回原 Candidate snapshot。

Wrong: 为 Review 另建 Path 表，或让 Repository 自定义 `00060` 不接受的列名、command type 和 receipt 形状。
Correct: 两个 origin 共用 `learning.learning_path(_step)`；Interview 通过兼容视图，Review Repository 严格服从基表和 command/reservation 契约。

Wrong: 只按 `output_schema_id='agent.rag-answer'` 强制 Memory，或给所有 Agent Model Run 全局注入 loader。
Correct: 用持久 NodeRun 的 `node_type='agent.rag-answer'` 界定 Conversation RAG；只有该 composition 持有 loader/snapshot 依赖。

Wrong: 给 Model Run 增列后只更新公共 SELECT/scanner，遗漏 recovery 的手写 `RETURNING`。
Correct: scanner 列序、所有 SELECT/RETURNING、insert args 和 replay equality 在同一改动中更新并用真实 PostgreSQL recovery 覆盖。
```
