# TODO 9 完成后的 TODO 10 实库门禁筛选

日期：2026-08-27。

## 已确认事实

- TODO 9 工厂已由 `109d2cb4` 交付，并由归档任务
  `archive/2026-08/08-25-gorm-prerequisites/` 的实装与验证记录证明完成。
- 工厂通过 `testdb.Require` 只提供一个 `platformpostgres.Pool`；同一 Pool 可取得
  `DB()`、`GORM()` 与 `UnitOfWork()`。每个模块必须在其既有 integration 文件中用这个
  Pool 分别构造 legacy 与 GORM 路径，不能复制容器生命周期。
- 此结论只解除 TODO 9 环境前置，不解除模块自身的 GORM 等价、跨模块依赖、生产
  Composition、legacy 删除或 TODO 3 Atlas 收口门禁。

## 选择规则

仅当 child 的已记录实施、静态验证和必需的跨模块 staged 契约均已完成，且剩余工作完全是：

1. 原位接入既有 integration fixture；
2. 从同一 `platformpostgres.Pool` 构造 legacy 与 GORM；
3. 运行该 child PRD 的 PostgreSQL 等价、事务/锁/失败/性能门禁；
4. 记录 review、完成状态、Final handoff 后归档；

才进入本轮执行。生产 `cmd/**`、Schema、Atlas、selector、双写、legacy 删除均不在本轮范围。

## 执行队列

按低耦合和依赖顺序执行；单个 child 失败或发现前置缺口时只停止该 child 的归档，保留其
`in_progress`，并继续记录其他已满足筛选条件的独立 child：

1. `08-19-gorm-auth-migration`
2. `08-19-gorm-audit-migration`
3. `08-19-gorm-documenthistory-migration`
4. `08-19-gorm-memory-migration`
5. `08-19-gorm-events-migration`
6. `08-19-gorm-collection-migration`
7. `08-19-gorm-gitsync-migration`
8. `08-19-gorm-localmodelruntime-migration`
9. `08-19-gorm-review-core-migration`
10. `08-19-gorm-review-interview-migration`

`Workspace` 已在 Audit 本轮门禁通过后重新筛选；其既有多个 integration fixture 仍使用外部
DSN/手工建库，且尚未覆盖完整 GORM 对照，因此不进入本轮执行。Review Learning Path 的静态
`go mod tidy -diff` 未绿，因此不在此队列。未列入队列的 child 必须保持当前状态。

### 本轮已验证结果

| Child | 结果 | 证据与边界 |
| --- | --- | --- |
| auth | 已完成并归档 | `repository_integration_test.go` 从同一共享 Pool 构造 legacy/GORM；真实 `-race` 覆盖数据库时钟、明文不落库、撤销/认证竞态、并发轮换、keyset、失效/损坏 scope 与 CTE 回滚。生产 Composition、legacy 删除仍属 Final。 |
| audit | 已完成并归档 | `store_integration_test.go` 从同一共享 Pool 构造 legacy/GORM/UoW；真实 `-race` 覆盖 replay、12 并发、脱敏/损坏、keyset/EXPLAIN、commit/rollback、SQLSTATE 和 `transaction_id`。Workspace/Tools 跨 owner 闭环仍属其 owner 与 Final。 |
| documenthistory | 已完成并归档 | 同一 Pool 的 legacy/GORM 对照通过；5,000 行、100 path fixture 以默认 planner 实测 `idx_authoring_publication_history_git` 与 `uq_proposal_commit_git`，并证明 50 OID 单 statement。 |
| memory | 已完成并归档 | 三个既有场景在同一 Pool 上 legacy/GORM 成对通过；覆盖 receipt/audit rollback、8 并发 Confirm、`SKIP LOCKED`、CAS、错误链与共享 Pool 事务隔离。 |
| events | 保持 in_progress | 功能、事务、锁、失败和 SSE 实库门禁通过，但当前 EXPLAIN 在极小 fixture 上强制关闭顺扫；尚无目标数据量、默认 planner 的索引性能证据，不能归档。 |
| collection | 保持 in_progress | 生命周期 fixture 已完成 shared-Pool legacy/GORM 对照；query/query_contract/durable/HTTP 仍依赖外部 DSN、第二 tracer pool 或外层未提交事务，无法满足完整 PRD 门禁。 |
| gitsync | 已完成并归档 | 既有 repository integration 已改用 shared Pool；7 组 legacy/GORM 场景覆盖配置回放、Run/Attempt、lease、Outbox、Poison/result-unknown、并发与触发器，3 个 EXPLAIN 门禁通过。生产切换与 legacy 删除仍属 Final。 |
| workspace | 保持 in_progress | Audit 前置已完成但重新筛选发现 Workspace 的多个既有 fixture 仍读取 `ZHIXU_TEST_DATABASE_URL` 并手工建库/开池，且尚未覆盖完整 Source/Registry/Control/Runtime/Git/scoped-writer 门禁；不满足 fixture-only。 |
| localmodelruntime | 保持 in_progress | 唯一 fixture 位于 `internal/platform/migration`，导入 shared `testdb` 会与其 Goose runner 形成 import cycle；未满足“只差 fixture 接入”，未运行真实 legacy/GORM。 |
| review-core | 保持 in_progress | 既有 fixture 只有 legacy 手写建库，且缺少 InvalidateCards 五类 selector、200/has_more、due/invalidation EXPLAIN、取消与连接释放回归；不只是接入 shared fixture。 |
| review-interview | 保持 in_progress | 既有 fixture 仍读取 `ZHIXU_TEST_DATABASE_URL`、手工建库并创建第二 pool；同时缺少 Completion/Path/maintenance、response-loss、锁/取消/Rows/EXPLAIN 等 PRD 门禁，不满足 fixture-only。 |

## 30 个 Child 审计

| Child | 结论 | 实证与原因 |
| --- | --- | --- |
| platform-transaction-foundation | 保留 | 共享 Pool/UoW/River staged 基础已存在，但其生产连接预算、网络层 commit response-loss 与业务 owner 错误矩阵仍未闭合；不是仅接业务 fixture 的模块 child。 |
| auth | 已执行并归档 | `repository_integration_test.go` 已接入 shared Pool，legacy/GORM 实库对照及 Go/SQL/Trellis review 通过。 |
| documenthistory | 已执行并归档 | 既有 integration fixture 已接入 shared Pool；批量 OID、默认 planner EXPLAIN 与 legacy/GORM 对照通过。 |
| ingestion | 保留 | `go mod tidy -diff` 仍是未完成静态项；在该项获得可复核结论前不能说“只差 fixture”。 |
| memory | 已执行并归档 | 既有 `repository_integration_test.go` 已接入 shared Pool；生命周期、锁、失败与事务隔离的 legacy/GORM 对照通过。 |
| events | 已部分执行，保持 in_progress | Store/HTTP fixture 已接入 shared Pool，功能、事务、锁、失败和 SSE 通过；EXPLAIN 仍是小 fixture + `enable_seqscan=off`，缺目标数据量/默认 planner 证据。 |
| audit | 已执行并归档 | Store integration 已接入 shared Pool；UoW、append-only、并发、keyset/EXPLAIN 与 SQLSTATE 对照通过。 |
| workspace | 保留 | Audit scoped append 实库前置已满足；重新审计仍发现多个外部 DSN/手工建库 fixture 与大范围真实门禁缺口，不是仅接入 shared fixture。 |
| workflow | 保留 | TODO 9 之外还欠 Model Settings scoped enqueue fence 与多个 owner 的 scoped Start/Hook 原子性。 |
| collection | 已部分执行，保持 in_progress | 生命周期 fixture 已接入 shared Pool并通过；query/query_contract/durable/HTTP 仍有外部 DSN、第二 tracer pool 和外层事务依赖。 |
| localmodelruntime | 保留 | 唯一 migration fixture 与 shared `testdb` 存在 import cycle，不能仅改 fixture 完成。 |
| review-core | 保留 | 既有 fixture 只有 legacy 手写建库，且缺少 InvalidateCards 五类 selector、200/has_more、due/invalidation EXPLAIN、取消与连接释放回归；不只是接入 shared fixture。 |
| review-interview | 保留 | 既有 fixture 是外部 DSN + 手工建库/第二 pool，且多项 PRD 回归尚未存在；不满足本轮 fixture-only 条件。 |
| review-learningpath | 保留 | `go mod tidy -diff` 已记录非绿，故静态门禁尚未满足。 |
| authoring | 保留 | `go mod tidy -diff` 仍未绿，不能归为只差 fixture。 |
| capture | 保留 | Profile GORM Repository 和 Agent scoped Model Run closure 尚未实施；Capture Core 不等于整个 child 完成。 |
| export | 保留 | 依赖 Workflow、Events、Audit 的实库闭环；Workflow 尚有 owner scoped 前置。 |
| gitsync | 已执行并归档 | 既有 integration fixture 已接入 shared Pool；并发、lease、触发器、回放、失败收敛和 EXPLAIN 对照通过。 |
| health | 保留 | 依赖 Collection、Workflow、Events 的实库闭环；其中 Workflow 尚有未满足前置。 |
| changecontrol | 保留 | 依赖 Workflow、Events、Audit；Workflow scoped 事务/River owner 前置未闭合。 |
| modelsettings | 保留 | 虽已有 staged GORM Bootstrap 与 scoped owner 设计，但 Model Settings、Audit、Local Runtime、Workflow fence 的真实同池原子链及生产/跨 owner 门禁尚未运行；不满足 fixture-only。 |
| knowledge | 保留 | 依赖 Change Control、Events、Audit 的同事务 Impact/Audit 组合，不能单独用 fixture 声明完成。 |
| graph | 保留 | 除真实门禁外仍欠 Final handoff 记录，且依赖 Collection、Knowledge、Change Control、Workflow。 |
| retrieval | 保留 | Workflow/Change Control concrete scoped collaborator 与 Completion 代码仍为明确阻断，且有 fault/capacity 前置。 |
| agent | 保留 | Workspace Analysis GORM Repository、scoped fence translator 与多个 Store-owned UoW 路径尚未实施。 |
| tools | 保留 | 仍欠 Agent/Workflow/Events/Audit 的完整同 Pool 依赖链与 Final handoff；不是独立 fixture-only。 |
| conversation | 保留 | 仍为 `planning`，尚无完整开发工件与 staged 实现。 |
| organizing | 保留 | 仍为 `planning`，尚无完整开发工件与 staged 实现。 |
| composition-pgx-convergence | 保留 | 仍为 `planning`；明确依赖全部模块、TODO 9 和 TODO 3，且唯一可改 `cmd/**`。 |
| artifact | 已完成，不重开 | 已归档的 `08-19-gorm-artifact-migration` 有独立实库、review 和 Final handoff；本轮不重复执行。 |

## 每个执行项的完成判定

执行项必须在其既有 integration 测试中实际通过：该 child PRD 列出的 legacy/GORM 行为对照、事务提交/回滚、锁或并发、失败/取消/SQLSTATE、资源释放以及要求的 EXPLAIN 或有界 statement 证据。随后逐项执行 Go、SQL、Trellis review，更新 PRD、Implement、evidence、`task.json` 和 Final handoff，再归档。任何失败、缺少既有 fixture 或新增跨模块前置都会立即撤出队列并保持 `in_progress`。
