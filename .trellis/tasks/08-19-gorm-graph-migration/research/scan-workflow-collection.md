# Research: Graph Semantic Link Scan / Workflow / Collection GORM 事务边界

- Query: 研究 Semantic Link Scan 的 Start/Advance/Finish、Topic/Smart Collection 分页、Workflow scoped runtime、Collection scoped durable binding、取消 guard、响应丢失恢复与 TODO9 双实现验收；给出最小 staged GORM 边界及依赖缺口。
- Scope: internal
- Date: 2026-08-20

## Findings

### 1. 结论摘要

最小 staged GORM 实现应保持 Graph Application 契约不变，并在 Graph PostgreSQL Adapter 内新增下列边界：

1. 一个从单一 `*platformpostgres.Pool` 派生 GORM root 与 `foundation.UnitOfWork` 的 Scan repository，实现既有 `SemanticLinkScanStartPort`、`SemanticLinkScanStatePort` 和 executor 使用的 `GetByWorkflowRun`。
2. Start repository 注入并复用 `workflowapplication.ScopedRuntimeStarter` 与 `collectionapplication.ScopedDurableScanBindingVerifier`。Graph 拥有 Repeatable Read UoW；Workflow 与 Collection 只能消费 scope，不能提交、回滚或退回自己的 root DB。
3. 一个实现 `workflowapplication.ScopedCancellationSafetyGuard` 的 Graph GORM cancellation guard；保留现有 pgx legacy guard，staged 阶段不切换 Composition。
4. Topic planner/page 的 GORM sibling。Topic page 的 version、node page、pair hydration、exclusion 必须在同一个 Repeatable Read、Read Only UoW 中执行；这是现有实现缺少的快照边界，而不是改变分页规则。
5. Smart Collection planner/page 继续依赖既有 Application `SmartCollectionScanReader`；Collection GORM 已经拥有 durable page 的 Repeatable Read snapshot。Graph exclusion 仍只能在另一个 root snapshot 中读取，这是现有 Port 无法消除的依赖缺口，不应在本 child 擅自扩大公共接口。

Graph 不应继续暴露 `pgx.Tx` 给 Workflow，也不应在 GORM sibling 中静态调用 Collection PostgreSQL helper。opaque scope 已经是项目批准的跨 owner 事务协议（`internal/foundation/transaction.go:23-34`；`internal/platform/postgres/transaction.go:35-97`）。

### 2. Files Found

| File | Role / evidence |
|---|---|
| `.trellis/tasks/08-19-gorm-graph-migration/prd.md:3-18` | Graph GORM 目标、跨模块事务、分页/排序、暂不切生产约束。 |
| `.trellis/spec/backend/database-guidelines.md:1356-1451` | M7-02 Scan/Candidate 的幂等、分页、DB 时间精度、错误矩阵与集成门禁。 |
| `internal/graph/application/scan.go:18-47` | 冻结 workflow/input/output/rule/scope 版本及 page/pair 上限。 |
| `internal/graph/application/scan.go:77-122` | Topic planner、page source、Smart Collection reader Application ports。 |
| `internal/graph/application/scan.go:238-270` | Start request/result、Start port 与 State port。 |
| `internal/graph/application/scan.go:296-396` | Start/Get/Advance/Finish 投影完整性校验。 |
| `internal/graph/application/scan.go:399-568` | page DTO、router、发现与 Candidate writer 调用顺序。 |
| `internal/graph/application/scan.go:571-804` | canonical request hash、进度/终态及稳定分页验证。 |
| `internal/graph/application/scan_workflow.go:17-220` | 单节点 Workflow definition 与 v1/v2 严格输入 binding。 |
| `internal/graph/adapter/postgres/scan_repository.go:26-195` | legacy pgx StartTx seam、RR Start、精确 replay、Workflow + Scan 原子写。 |
| `internal/graph/adapter/postgres/scan_repository.go:198-404` | Get/Advance/Finish、receipt recovery 与 exact replay。 |
| `internal/graph/adapter/postgres/scan_repository.go:645-679` | context/SQLSTATE 错误分类与固定 scan projection。 |
| `internal/graph/adapter/postgres/scan_cancellation.go:13-70` | legacy Workflow cancel 事务中的 Scan lock/CAS 状态归约。 |
| `internal/graph/adapter/postgres/scan_planner.go:13-55` | Topic version 与正式成员 exact count 的单查询规划。 |
| `internal/graph/adapter/postgres/topic_scan_source.go:23-109` | per-source LATERAL 100 与当前多语句 page 流程。 |
| `internal/graph/adapter/postgres/topic_scan_source.go:116-269` | batch pair hydration、keyset page、稳定排序。 |
| `internal/graph/adapter/postgres/topic_scan_source.go:272-330` | 参数化批量 exclusion，避免 N+1。 |
| `internal/graph/adapter/postgres/smart_collection_scan.go:16-125` | Collection-owned plan/page 与 Graph-owned exclusion 的组合。 |
| `internal/graph/adapter/postgres/smart_collection_scan.go:128-270` | deterministic keys、node binding 校验与批量 exclusion。 |
| `internal/graph/adapter/workflow/scan_executor.go:40-114` | page -> Candidate -> AdvancePage -> Finish 的恢复循环。 |
| `internal/graph/adapter/workflow/scan_executor.go:162-206` | retry budget、最终失败与精确 workflow binding。 |
| `internal/graph/adapter/postgres/discovery_candidate_writer.go:92-105` | Candidate fingerprint 是事实边界，Scan checkpoint 是 once-only counter 边界。 |
| `internal/workflow/application/scoped_runtime.go:11-21` | 可复用的 scoped Start port 与需实现的 scoped cancellation port。 |
| `internal/workflow/adapter/postgres/gorm_runtime_start.go:28-148` | root/scoped Start 所有权、Workflow/Outbox/River 同 scope 幂等。 |
| `internal/workflow/adapter/river/gorm_inserter.go:14-108` | River `InsertTx` 使用 opaque scope 解出的同一 `*sql.Tx`。 |
| `internal/collection/application/membership.go:10-21` | durable revision 与 scoped exact binding verifier 的语义差异。 |
| `internal/collection/adapter/postgres/gorm_repository.go:402-487` | Collection GORM plan/page snapshot 与 scoped verifier。 |
| `internal/collection/adapter/postgres/durable_scan.go:176-320` | `FOR SHARE`、definition/revision/exact-count 复核实现。 |
| `migrations/00024_semantic_link_candidates.sql:290-376` | Scan schema、唯一键、状态/时间/计数约束与 Topic partial index。 |
| `migrations/00024_semantic_link_candidates.sql:685-747` | immutable binding、version+1、单调计数、状态转换 trigger。 |
| `migrations/00025_smart_collection_health.sql:45-102` | Smart Collection hash/revision binding 与写入 trigger。 |
| `cmd/api/main.go:462-466` | legacy API cancellation composite 构造点。 |
| `cmd/api/main.go:1431-1465` | legacy API Scan repo、Topic/Smart planner 构造点。 |
| `cmd/worker/main.go:1378-1423` | legacy Worker state/page/candidate/executor 构造点。 |
| `cmd/worker/main.go:1461-1469` | legacy Worker cancellation composite 构造点。 |

### 3. 现有事务链与必须保持的不变量

#### 3.1 StartOrReplay

现有 root `StartOrReplay` 明确拥有 `pgx.RepeatableRead` transaction（`scan_repository.go:74-87`）。事务内的固定顺序是：

1. 以 `(workspace_id, idempotency_key)` 查询并 `FOR UPDATE`；命中时先做完整 binding 比较并精确 replay（`scan_repository.go:122-127,353-404`）。因此旧 key 即使对应 FAILED/CANCELLED，也必须返回原 attempt；只有新 key 才能以相同 fingerprint 开新 attempt，和规范 `database-guidelines.md:1395-1396,1413` 一致。
2. 仅 Smart Collection scope 解析 Collection ID，并复核 active/version/query hash/read-model revision/exact count（`scan_repository.go:128-139`）。
3. 生成 Scan ID、编码 frozen input/definition、构造 Runtime request（`scan_repository.go:141-163`）。
4. 在同一 transaction 调 Workflow StartTx，写入真实 Run/Node/Outbox/River job（`scan_repository.go:164-167`）。
5. 从数据库取时间并插入 Scan；冲突 winner 在当前 RR snapshot 不可见时返回 retryable error，交给 root fresh-connection recovery（`scan_repository.go:168-195,321-350`）。
6. callback 成功后 root commit；callback 的 retryable failure 先 rollback 再 fresh-root 查 receipt，commit 返回错误也 fresh-root 查 receipt（`scan_repository.go:88-113`）。

表约束是最终防线：`(workspace_id,idempotency_key)` 和 `workflow_run_id` 均唯一（`migrations/00024_semantic_link_candidates.sql:338-339`），binding 在 UPDATE 中不可修改、version 必须逐一递增、计数不可减少、状态只能按允许边转换（`migrations/00024_semantic_link_candidates.sql:708-737`）。GORM sibling 必须保留这些约束驱动的 exact replay，不能用 `FirstOrCreate` 弱化 request-hash 比较。

#### 3.2 AdvancePage

Application/Workflow 的语义是先完成一页 Candidate upsert，再以 expected version 推进 checkpoint/counters（`internal/graph/adapter/workflow/scan_executor.go:70-110`）。Candidate exact-active replay仍计为 `Created`，而 Scan checkpoint 才是计数 once-only 边界（`discovery_candidate_writer.go:92-105`）。所以 response loss 或 Advance 失败后整页重跑，不会新增 fingerprint fact，并会在最终成功的 checkpoint CAS 上得到确定性计数。

legacy `AdvancePage` 先执行 `SELECT ... FOR UPDATE`，再取 DB time，再 CAS UPDATE（`scan_repository.go:226-273`），但生产 state repository 持有的是 root pool，方法没有 begin transaction。三个语句因此是不同 autocommit statement，`FOR UPDATE` 锁在 SELECT 结束后已经释放。expected-version CAS 仍防止覆盖，但当前代码并未实现注释暗示的连续持锁。

GORM sibling 应在一个普通 read-write UoW 中完成：

1. `SELECT ... WHERE id/workspace/version/status FOR UPDATE`；
2. 同 scope `SELECT CURRENT_TIMESTAMP`；
3. `updated_at = max(db_now, stored.updated_at)`；
4. 带相同 expected version/status 的 `UPDATE ... RETURNING`。

这样修复真实锁边界，同时保持 VersionConflict、单调时间、单调计数和 projection 返回语义。另一种单条 CAS UPDATE 也可正确防丢更新，但无法直接保持现有 `max(db_now, stored.updated_at)` 逻辑；最小迁移应沿用 UoW 三步实现。

#### 3.3 Finish

`Finish` 已是单条 `UPDATE ... WHERE expected version AND PENDING/RUNNING RETURNING`，并严格限制 SUCCEEDED/FAILED/CANCELLED 及 failed-only error payload（`scan_repository.go:276-318`）。GORM sibling 不需要额外 `FOR UPDATE`；保留单条 CAS 即可。

Finish 使用 Application/Workflow 传入的 UTC time。executor 保证它晚于 Scan `UpdatedAt`，最终失败使用 `context.WithoutCancel` 尝试落终态（`scan_executor.go:98-106,162-182`）。PostgreSQL `timestamptz` 只有微秒精度，Application 只容忍 `<1us` 的投影差异（`.trellis/spec/backend/database-guidelines.md:1402-1403`；`internal/graph/application/scan.go:375-396`）；GORM scanner 不能改变这一比较规则。

### 4. 最小 staged GORM Start 边界

#### 4.1 构造与依赖

建议构造器只接收：

```go
NewGORMSemanticLinkScanRepository(
    pool *platformpostgres.Pool,
    runtime workflowapplication.ScopedRuntimeStarter,
    collection collectionapplication.ScopedDurableScanBindingVerifier,
    ids foundation.IDGenerator,
    clock foundation.Clock,
) (..., error)
```

构造器必须从同一个 Pool 调 `GORM()` 和 `UnitOfWork()`，不能独立注入任意 `*gorm.DB` 与 UoW。平台 Pool 本身共同拥有 pgx、database/sql 与 GORM root（`internal/platform/postgres/pool.go:26-42,157-162`）；Workflow staged constructor 也已采用单 Pool 派生模式（`internal/workflow/adapter/postgres/gorm_core.go:48-82`）。

`collection` 可以在构造期作为必需依赖，以避免 Smart scope 在运行时才发现能力缺失；若为保持 Topic-only fixture 允许 nil，则必须只允许 Topic start，Smart start 必须 fail closed 为 dependency unavailable，绝不能跳过 exact binding 校验。

#### 4.2 Scoped ports

必须复用：

- `workflowapplication.ScopedRuntimeStarter.StartScoped(ctx, scope, request)`（`internal/workflow/application/scoped_runtime.go:11-15`）。其 GORM 实现只 unwrap live scope，不启停事务（`gorm_runtime_start.go:68-80`），并在同一 scope 写 Definition、Run、Node、Outbox、River job；replay 必须与 River duplicate receipt 一致（`gorm_runtime_start.go:95-148`）。Graph 不能调用 Workflow root `Start`，否则产生嵌套独立 UoW，Scan 与 Workflow 不再原子。
- `collectionapplication.ScopedDurableScanBindingVerifier.VerifyDurableScanBindingScoped`（`internal/collection/application/membership.go:16-21`）。其 GORM 实现只 unwrap scope，明确不 commit/rollback/root fallback（`gorm_repository.go:472-487`）；共享 verifier 对 Collection `FOR SHARE`，再验证 definition、revision 与 exact member count（`durable_scan.go:186-201,230-259,314-320`）。Graph 不能调用 root `PlanDurableScan` 或 `VerifyDurableScanRevision`，前者另开 RR snapshot，后者不计算 exact count（`gorm_repository.go:402-470`）。

Graph 无需新增公共 Application Start port。需要新增的是 Adapter implementation，以及实现现有 Workflow Application scoped cancellation port。只有要解决 Smart page/exclusion 单快照问题时才需要新跨 owner page-projection port；它不属于最小 staged migration。

#### 4.3 GORM UoW 顺序

Start 的锁/调用顺序冻结为：

```text
Graph RR UoW owns scope
  -> Scan idempotency receipt FOR UPDATE
     -> exact replay: return immediately
  -> SMART only: Collection binding FOR SHARE + revision + exact count
  -> Workflow StartScoped
       -> Workflow definition/run/node/outbox locks/inserts
       -> scoped enqueue fence
       -> River InsertTx on the same sql.Tx
  -> database-owned Scan timestamp
  -> Scan INSERT ON CONFLICT(workspace,idempotency_key) DO NOTHING
  -> strict winner/replay comparison
Graph UoW commits or rolls back all facts
```

不要在 receipt 命中后再次校验 Collection；exact replay 返回的是已提交 attempt，不能因当前 Collection 漂移把历史 receipt 改成失败。也不要将 Collection `FOR SHARE` 放到 Workflow Start 之后，否则 stale binding 会留下无必要的 Workflow 写锁，增加冲突面。

### 5. Response-loss 与幂等恢复

Graph root 是本组合事务唯一 commit owner，因此只有 Graph root 能做 commit-response-loss recovery。Workflow `StartScoped` 不能也不会 recovery/commit；Workflow root `Start` 的 recovery 只适用于它自己拥有的 UoW（`gorm_runtime_start.go:28-65,68-80`）。

GORM Start 应保留 legacy 的两类 fresh-root receipt 检查：

- callback 未成功且错误可重试：UoW 已回滚或 PostgreSQL 已使 transaction abort；用 root connection 查 `(workspace,idempotency_key)`。这覆盖并发 winner 造成的 serialization/unique race。
- callback 成功但 `Within` 返回 commit error：commit 结果未知；必须先查 receipt，不能直接开新事务重做。

fresh-root 命中后必须调用与事务内相同的 strict replay 比较：workspace、scope（含 Smart hash/revision）、fingerprint、request hash、idempotency key、total nodes 全部一致（`scan_repository.go:385-404`）。不匹配返回稳定 conflict；未找到则返回原 commit/retryable error。规范也要求未知副作用先查 durable fact，而不是盲重试（`.trellis/spec/backend/error-handling.md:29-36`）。

TODO9 必须同时断言一次提交只形成一个 Scan、一个 Workflow Run 和一个 River Job；现有并发/commit-loss 集成用例已经做了三表计数（`internal/graph/adapter/postgres/scan_repository_integration_test.go:186-290`）。

### 6. DB-time 语义

- Workflow staged Start 用 `clock_timestamp()` 为初始 Runtime facts 提供同一个 DB-owned wall-clock timestamp（`gorm_runtime_start.go:95-106,151-160`）。
- Scan Start legacy 在同一 RR transaction 中随后调用 `SELECT CURRENT_TIMESTAMP`（`scan_repository.go:168-176`；`candidate_repository.go:927-932`）。`CURRENT_TIMESTAMP` 是 transaction-start timestamp，可能早于 Workflow `clock_timestamp()`；这是现有行为，最小迁移应保留，不应悄悄换成 Go Clock。
- AdvancePage 用 DB time 并对 stored `UpdatedAt` 做单调 clamp（`scan_repository.go:251-268`）。
- cancellation 在 Workflow-owned transaction 中用 `CURRENT_TIMESTAMP` 同时写 `updated_at/completed_at`（`scan_cancellation.go:51-62`）。
- Finish 使用 executor Clock，但按 PostgreSQL 微秒精度验证。不要让 GORM 默认时间回调、`Save` 或自动字段覆盖这些显式时间。

### 7. Scoped cancellation guard

现有 guard 只实现 legacy `CancellationSafetyGuard`，把 `transaction any` 断言为 `pgx.Tx`（`scan_cancellation.go:13-35`）。GORM sibling 应实现：

```go
workflowapplication.ScopedCancellationSafetyGuard
```

它通过 `platformpostgres.GORMTransaction(scope)` 获取 live transaction，保留下列精确归约（`scan_cancellation.go:39-69`）：

- node 没有关联 Scan：`true, nil`；
- PENDING/RUNNING：`FOR UPDATE OF scan` 后 expected version/status CAS 到 CANCELLED，DB time 同时写完成时间；
- 已 CANCELLED：`true, nil`；
- 已 SUCCEEDED/FAILED：返回 non-retryable VersionConflict；
- 未知状态：ConsistencyViolation；scope 无效/过期则 fail closed。

Workflow GORM cancellation 会保留 VersionConflict，其余 guard error 统一包成 retryable `WORKFLOW_CANCELLATION_SAFETY_UNAVAILABLE`（`internal/workflow/adapter/postgres/gorm_runtime_delivery.go:671-683`）。因此 Graph guard 不要把 terminal conflict 降级成 dependency error，也不要吞掉 context cause。

锁序应保持 Workflow-owned transaction 先锁/归约 Runtime node，再由 composite guard 锁 Graph Scan；Graph guard 内只有 scan lock -> scan CAS，不能反向调用 Workflow repository。legacy guard 必须保留，直到 Final Composition 整体切换 scoped composite。

### 8. 分页、快照与容量语义

#### 8.1 Topic

Topic planner 的单条聚合查询读取 Active Topic version，并只统计正式 CONFIRMED/DISPUTED Claim membership（`scan_planner.go:26-55`）。page 规则必须原样保持：

- page size 最大 100、每 source target 最大 100（`internal/graph/application/scan.go:43-47`）；
- source 使用 `(claim.id > cursor) ORDER BY claim.id LIMIT limit+1` 的稳定 keyset page（`topic_scan_source.go:221-269`）；
- pair 使用每个 source 的 `CROSS JOIN LATERAL ... ORDER BY ... LIMIT`（`topic_scan_source.go:23-47`），与 partial index 谓词完全一致（`migrations/00024_semantic_link_candidates.sql:368-373`）；
- missing target 一次批量 hydrate，数量必须精确匹配（`topic_scan_source.go:116-218`）；
- exclusions 通过参数化 `unnest(... WITH ORDINALITY)` 一次查询 Formal Relation / Active Proposal，不得逐 pair 查询（`topic_scan_source.go:272-330`）。

当前 `LoadPage` 依次用 root DB 查询 Topic version、source nodes、pairs/hydration、exclusions，没有显式 transaction（`topic_scan_source.go:57-109`）。scope 可以在这些语句之间漂移。GORM sibling 应把整页放进 Repeatable Read + Read Only UoW；这会兑现 frozen page 语义而不改变 SQL、cursor、排序和限制。

GORM/database/sql 参数载体需遵循仓库 staged 模式：数组输入不能继续依赖 pgx slice codec，应使用已采用的 `pq.Array`/等价稳定 carrier；数组聚合输出需使用 database/sql 可扫描类型。复杂 CTE/LATERAL/JSON exclusion 保留参数化 Raw SQL，GORM 不应重写查询形状。

#### 8.2 Smart Collection

Planner 继续调用 `SmartCollectionScanReader.PlanDurableScan`，并完整验证 returned binding（`smart_collection_scan.go:16-50`）。Collection GORM 已在一个 RR/read-only snapshot 中冻结 definition/revision/exact count，也在一个 RR/read-only snapshot 中读取 durable page（`gorm_repository.go:402-447,489-490`）。

Smart page 禁止 opaque cursor，使用 `Checkpoint.LastNode` 与 frozen `TotalNodes`（`smart_collection_scan.go:68-91`），验证 returned nodes/pairs 与 exact version 后再做 Graph exclusion（`smart_collection_scan.go:95-125,149-203`）。这些分页语义必须保持。

当前 Collection page transaction 在 `ReadDurableScanPage` 返回时已经结束，Graph exclusion 随后从自己的 root DB 查询（`smart_collection_scan.go:85-112`）。因此 membership/node projection 与 relation/proposal exclusion 不是同一 snapshot。现有 Application port 不传出 scope，Graph child 无法在不改变跨 owner contract 的前提下修复。最小 migration 应保留行为并记录风险；若产品要求整页完全一致，需要后续设计 scoped combined projection port，或将 exclusion 投影移入 Collection-owned page query，不能用嵌套 UoW 假装原子。

### 9. Production 构造边界

本 child 只新增 staged 实现，不改以下 Composition：

- API：legacy cancellation composite 在 `cmd/api/main.go:462-466`；Scan Start/State、Topic planner、Collection service 与 Smart planner 在 `cmd/api/main.go:1431-1465`。
- Worker：Scan state、Topic/Smart page、Candidate writer、page/workflow executor 在 `cmd/worker/main.go:1378-1423`；legacy cancellation composite 在 `cmd/worker/main.go:1461-1469`。

Final 切换时必须由 Composition 创建一个 `platformpostgres.Pool`，并用它构造 Graph GORM Scan、Collection GORM repository、Workflow GORM runtime/River inserter及所有 scoped hooks。`TransactionScope` 只验证 concrete type/live lifetime，不携带 Pool identity（`internal/platform/postgres/transaction.go:89-97`），所以“相同进程但不同 Pool/数据库”无法在 adapter 运行时可靠识别；同 Pool 是 Composition/TODO9 必须证明的不变量。

### 10. TODO9 legacy/GORM 场景矩阵

TODO9 应参数化现有 fixture，对 legacy 与 GORM 两种实现运行相同场景，每个变体使用独立迁移数据库/隔离数据和一个平台 Pool；不得让两种实现共享残留 receipt。

| Area | Existing evidence | GORM variant must prove |
|---|---|---|
| Start/replay/state | `scan_repository_integration_test.go:29-184` | 首次 Start、exact replay、同 key 不同请求 conflict、跨 Workspace concealment、Advance CAS/counters、Success/Failed/Cancelled；旧 key replay 与新 key same-fingerprint restart。 |
| Concurrent Start + commit loss | `scan_repository_integration_test.go:186-290` | 多 caller 只产生一个 Scan/Run/Job；commit response loss fresh-root recovery 返回同 receipt。 |
| Topic plan | `scan_repository_integration_test.go:292-329` | 只统计 Active Topic 下正式 Confirmed/Disputed membership，version/count 不漂移。 |
| Topic page/candidate | `scan_repository_integration_test.go:331-391` | deterministic page、Candidate fingerprint replay、checkpoint counters。 |
| 205-node bounded pairs + EXPLAIN | `scan_repository_integration_test.go:393-467` | 三页 pair 数 `10000/5440/10`，per-source inner LIMIT 与 partial index plan 不变。 |
| Smart durable restart/drift | `smart_collection_scan_integration_test.go:25-194` | structured checkpoint 跨页；node version frozen；Collection/read-model/member drift fail closed。 |
| Smart start atomic recheck | `smart_collection_scan_integration_test.go:196-289` | real Workflow start/replay/commit-loss；plan-to-start drift 不留下 Scan/Run/Job。 |
| Real River completion response loss | `scan_river_integration_test.go:167-216` | delivery retry返回同成功输出，不重复 checkpoint/counters。 |
| Cancellation convergence | `scan_river_integration_test.go:218-277` | scoped composite 同 transaction 使 Workflow 与 Scan 都收敛；terminal conflict 分类不变。 |
| Retry/final failure | `scan_river_integration_test.go:279-337` | retryable page fault 恢复；预算耗尽后 FAILED/error/counters 正确。 |
| Exclusion endpoint identity | `topic_scan_exclusion_integration_test.go:20` | type + ID 精确匹配，数组参数化与 ordinality 稳定。 |
| Executor contract | `internal/graph/adapter/workflow/scan_executor_test.go:19-126` | 多页、empty、Smart structured checkpoint、retry/final failure、binding drift 行为不变。 |
| Completion precision | `internal/graph/application/discovery_scan_test.go:199-258` | `<1us` 可接受，`>=1us` projection mismatch。 |
| Composition registration | `cmd/worker/semantic_scan_composition_integration_test.go:16` | Topic v1 与 Smart v2 executor 都注册；Final 才切 production implementation。 |

还应在 TODO9 加入或扩展现有 fixture 的事务原子断言：

1. Collection verifier error 后 Scan/Workflow/River 全部为零；
2. Workflow/River staged insert error 后 Scan 也为零；
3. Scan insert/trigger error 后 Workflow/Outbox/River 全部回滚；
4. live foreign-Pool scope 的运行时无法可靠拒绝，因此用 Composition 构造测试证明所有 scoped dependency 接收同一 Pool；
5. `AdvancePage` 在两个并发 expected-version caller 下只有一个成功，另一个稳定 VersionConflict；
6. cancellation 与 success/failure race 只允许一个合法终态，禁止 Workflow CANCELLED 而 Scan SUCCEEDED/FAILED 被覆盖；
7. Topic page 的多语句 snapshot 在并发 membership 更新下保持一页内部一致；
8. Smart page/exclusion 跨 snapshot 的现有局限单独记录，不把无法由当前 port 保证的原子性写成通过项。

### 11. Error、日志与 scanner 要求

- 延续现有 context/SQLSTATE 分类：cancel non-retryable 且 Cause 可 `errors.Is`，deadline/retryable SQLSTATE 可重试，23505 为 version conflict，FK/check violation 为 consistency（`scan_repository.go:645-670`；`.trellis/spec/backend/error-handling.md:27-36`）。GORM classifier 必须 unwrap driver error，不能只看 GORM message。
- 所有 Raw SQL 保持参数化；Topic arrays/JSON 不得字符串拼接。禁止 N+1 和无界列表（`.trellis/spec/backend/quality-guidelines.md:25-42`）。
- scanner 必须继续验证 ID、scope/status/checkpoint/error JSON 与 domain invariant；不能依赖 GORM `Save` 或零值推断。固定 projection 在 `scan_repository.go:673-679`。
- retry/response-loss 路径不重复写不可幂等 Audit；日志只记录稳定 error code、Workspace/Run/Scan correlation 和摘要，不输出 SQL 参数或 binding 原文（`.trellis/spec/backend/logging-guidelines.md:24-52`）。

## External References

本研究只核对仓库内已冻结实现与规范，没有依赖外部文档。当前依赖版本为 `gorm.io/gorm v1.31.2`、`gorm.io/driver/postgres v1.6.2`、`github.com/riverqueue/river v0.40.0`、`github.com/jackc/pgx/v5 v5.10.0`（`go.mod:14,22-25,42-43`）。复杂 SQL 与 River transaction 行为以本仓库 staged adapters 和 TODO9 真实 PostgreSQL 验收为准。

## Related Specs

- `.trellis/spec/backend/database-guidelines.md:1356-1451`：M7-02 Semantic Link Candidate And Durable Scan。
- `.trellis/spec/backend/error-handling.md:27-36`：adapter 分类、retry 与未知副作用 receipt recovery。
- `.trellis/spec/backend/quality-guidelines.md:25-42`：参数化、分页、Workflow、Adapter contract。
- `.trellis/spec/backend/logging-guidelines.md:24-52`：状态事件、敏感信息与 retry audit 约束。
- `.trellis/tasks/08-19-gorm-graph-migration/prd.md:7-21`：只迁 Graph PostgreSQL Adapter、保持行为、TODO9 前不切 production。

## Caveats / Not Found

1. 当前环境未提供 `ZHIXU_TEST_DATABASE_URL`，本 researcher 也不修改代码/测试；无法在本 child 证明真实 PostgreSQL 上 Graph + Collection + Workflow + River 的跨 owner commit/rollback、serialization、commit response loss 与 cancellation race。它们必须留给 TODO9。
2. `foundation.TransactionScope` 没有 Pool identity。nil、过期或非 platform scope 可 fail closed，但另一个 active platform Pool 的 scope 无法被可靠识别；必须由 Final Composition 与 TODO9 固定同一 Pool。
3. Smart Collection durable page 与 Graph exclusion 在现有 port 下无法共享 snapshot。解决它需要新跨 owner contract 或查询归属调整，超出最小 staged Graph migration。
4. legacy `AdvancePage` 的 `FOR UPDATE` 不在显式 transaction 内；GORM staged 应用 UoW修复该锁生命周期，但 TODO9 必须证明行为兼容。
5. legacy Topic page 多语句没有统一 snapshot；GORM staged 应使用 RR/read-only UoW，TODO9 需用并发漂移场景证明整页一致。
6. Scan checkpoint recovery 依赖 Candidate GORM writer 保留 fingerprint exact replay 的计数语义。若 Candidate writer 是另一个 Graph 子任务，本 Scan child 只能静态冻结契约，不能单独完成端到端证明。
7. Graph scoped cancellation guard 只是 production scoped composite 的一个成员。Final 还依赖 Change Control、Health 等 scoped hooks 全部就绪，Graph child 不能独立切换 API/Worker Workflow runtime。
