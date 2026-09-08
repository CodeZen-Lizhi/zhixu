# Workflow GORM Final Handoff

日期：2026-09-08。范围：Workflow child、`internal/workflow/**`。

## 当前结果

Workflow 自身 staged GORM 实现与父任务要求的精简实库门禁已通过。此次发现并修正的缺口是
测试接线：历史 Runtime/Worker integration 仍然只使用 legacy Repository，不能证明 GORM Runtime
已运行。现在在三个既有文件中直接构造 GORM Runtime、真实 Model Settings scoped fence 与
database/sql River producer；没有新增测试文件、第二套 fixture 或生产 selector。

`gorm_reindex_outbox.go` 是任务开始前已有的未跟踪实现，已完整保留并完成 Go/SQL 审查；它提供
Retrieval 使用的 Workflow-owned Outbox 参与者。Retrieval child 已确认真实 dispatch 组合通过，
下文保留对方执行的准确命令与结果。
本次没有改 `cmd/**`、migration、父任务、active pointer，也没有删除 legacy、提交或归档。

## 必须使用的构造与端口

所有参数中的 Pool 必须是同一个 `*platformpostgres.Pool`。

| 用途 | 构造 / 端口 |
| --- | --- |
| 基础 Run/Node/Human、Run list、Pending Human Task | `NewGORMRepository(pool)` |
| Runtime 状态机与 caller-owned Start | `NewGORMRuntimeRepositoryWithHooks(pool, riverOptions, settings, hooks)`，实现 `RuntimeStarter`、`ScopedRuntimeStarter`、`RuntimeStatePort` 和 Human Port |
| Artifact/Organizing 的 durable binding | `NewGORMRuntimeBindingReader(pool)` → `ScopedRuntimeBindingReader` |
| Agent/Tools 的 Run → Node → Attempt 锁 | `NewGORMWorkspaceAnalysisExecutionFence(pool)` |
| Tools live policy 原始投影 | `NewGORMToolExecutionPolicySnapshot(pool)` |
| Tools stale recovery Node → Attempt `SKIP LOCKED` | `NewGORMToolCallRecoveryFence(pool)` |
| Retrieval Outbox claim/publish | `NewGORMReindexOutbox(pool)` → `retrievalapplication.ScopedReindexOutbox` |

Runtime 构造参数：

- `settings` 是 `modelsettingspostgres.GORMRepository`，通过 `CheckEnqueue(ctx, TransactionScope)`
  满足 `riveradapter.ScopedEnqueueFence`。Settings 的 Sealer 和 Audit appender 必须真实配置。
- `riverOptions.EnqueueFence` 必须为空，否则 factory 拒绝 legacy/scoped fence 混用。
- `GORMRuntimeRepositoryHooks` 接收 `ScopedCancellationSafetyGuard`、`ScopedWorkflowTerminalHook`、
  `ScopedWorkflowControlHook` 与 `ModelRuntimeFreshWithin`；使用 application 中现有三个 scoped composite
  保留注册顺序，不把 `TransactionScope` 传入 legacy `any` Hook。
- Runtime factory 从同 Pool 内建 insert-only Client 与 `NewScopedJobInserter`。Worker/listener 继续
  使用该 Pool 的 `DB()` 与 `NewClientWithOptions`；queue/schema/operability 配置必须相同。

## 实际验证

所有实库测试使用 `testdb.Require(FailWhenUnavailable)`，Docker 29.4.0 可用；没有通过环境变量
跳过测试。每条命令限定 `-timeout=60s`，没有全仓测试或全量 integration。

```text
go test -mod=vendor ./internal/workflow/... -count=1 -timeout=60s
go vet -mod=vendor ./internal/workflow/...

go test -mod=vendor -tags=integration ./internal/workflow/adapter/postgres -run '^(TestRuntimeStateClaimHeartbeatCompleteAndReplay|TestRuntimeRepositoryStartScopedUsesCallerTransactionAndReportsReplay|TestRuntimeNodeWorkerExecutesDeterministicDeliveryEndToEnd)$' -count=1 -timeout=60s

go test -mod=vendor -tags=integration ./internal/workflow/adapter/postgres -run '^(TestRuntimeRepositoryConcurrentDuplicateCreatesOneRunNodeOutboxAndJob|TestRuntimeRepositoryStartScopedUsesCallerTransactionAndReportsReplay|TestRuntimeTerminalHookFailureRollsBackDeliveryAndControl|TestRuntimeStateConcurrentClaimAndLeaseReclaimFenceOldOwner)$' -count=1 -timeout=60s

go test -race -mod=vendor -tags=integration ./internal/workflow/adapter/postgres -run '^(TestGORMRepositoryListAndOutputRunWithPostgres|TestGORMWorkspaceAnalysisExecutionFenceLocksExecutionWithPostgres|TestGORMToolExecutionPolicyAndRecoveryFences|TestRuntimeStateConcurrentClaimAndLeaseReclaimFenceOldOwner)$' -count=1 -timeout=60s

python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-workflow-migration
git diff --check
```

- Workflow unit 与 vet：PASS。
- 最终 task validate、三个修改文件的 gofmt 与 `git diff --check`：PASS。
- 第一组 Testcontainers：PASS，26.233s。真实 StartScoped、Claim/Heartbeat/terminal replay、pgx Worker。
- 第二组 Testcontainers：PASS，29.807s。包含更新后的 Settings 锁持有/释放断言、Run/Node/Outbox/Job
  四类事实整体回滚、并发唯一启动、并发 Claim/reclaim、Terminal Hook 失败回滚 Delivery/Control。
- 第三组定向 `-race`：PASS，49.706s。Repository JSON/分页/隔离/资源释放、Execution Fence 锁序、
  Tools `FOR SHARE`/`SKIP LOCKED`/context/SQLSTATE 与并发 lease。
- 两次早期编译因 Organizing 代理正在落地代码而失败；对方补齐方法/接口后，上述相同 Workflow
  实库命令已经通过。它们不是已忽略的失败。

Retrieval child 在同一 platform Pool、pgvector PostgreSQL 16 Testcontainers 中执行并报告 PASS：

```text
go test -mod=vendor -tags=integration ./internal/retrieval/adapter/postgres -run '^TestDispatcherInsertFailureRollsBackDeliveryAndPublishedAt$/gorm$' -count=1 -timeout=60s -v
```

包耗时 11.420s，子测试 8.12s。fixture 使用 `NewGORMReindexOutbox`、Change Control GORM verifier、
Model Settings GORM fence 与真实 River scoped producer；在真实 Job insert 后注入错误，Delivery、
River Job、`published_at` 全部回滚，再经公开 `NewGORMDispatcher` 成功派发并验证相同 queue/generation
的三方提交。此命令由 Retrieval child 执行，Workflow 没有重复运行。早期 fixture 缺少 migration 00082
要求的 revision dispatch binding，Retrieval 已沿用现有 Change Control fixture 补齐，未修改 trigger。

## Review

按 `go-review` 与 `sql-code-review` 做人工静态审查：

- Correctness：PASS，修复真实测试接线缺口，运行前后比较 Run/Node/Attempt 与队列事实。
- Readability：PASS，原位复用现有 fixture，唯一 helper 只组合已批准的真实 Adapter。
- Architecture：PASS，caller-owned scope 不管理事务，业务与 River 使用同一 SQL transaction，
  scoped Hook 没有回落到 legacy `pgx.Tx`。
- Security：PASS，Workflow/Workspace binding、参数化 SQL、稳定错误、无敏感输出；Reindex Outbox
  只允许固定事件类型，Publish 保持未发布 CAS。
- Performance/concurrency：PASS，未增加生产查询或循环，Outbox 保持 FIFO/有界 `SKIP LOCKED`，
  Settings scope 锁与 lease 并发有真实证据。没有声称完成额外容量或全量执行计划门禁。

当前范围未发现剩余 P0/P1/P2；不是独立 reviewer 的结论，最终整合仍由主会话检查。

## Final 必须保留 / 清理的内容

1. 先按各 owner child 的 handoff 接好 Artifact、Conversation、Change Control、Health、Graph、
   Organizing scoped Start/Hook。消费者未完成前不能以空 Hook 代替。
2. 删除 `repository.go`、`runtime_start.go`、`runtime_state.go`、`runtime_output.go` 的 legacy
   数据访问前，保留/迁出 GORM 仍复用的纯 helper 和显式列：
   - `scanHuman`、`jsonEqual`、`humanColumns`；
   - `validateRuntimeStartRequest`、`validateRuntimeNodeReplay`、`objectBytes`；
   - `runtimeRunColumns`、`runtimeNodeColumns`、`runtimeAttemptColumns`、`scanClaimDefinition`、
     `scanRuntimeRun/Node/Attempt`、`validateDecisionSchema`；
   - `modelRuntimeOwnershipLost`、`nullableInt64`、`nullableFoundationID`、`sameModelRuntimeOwner`；
   - `validRuntimeOutputID`。
   纯 scanner 可使用私有 `Scan(...any) error` 接口，不能保留 `pgx.Row` 作为普通 Repository 依赖。
3. `gorm_core.go` 的 `pgx.ErrNoRows` 仅为 legacy 共享兼容；去掉 legacy 后收敛到
   `sql.ErrNoRows`/`gorm.ErrRecordNotFound`。`pgconn.PgError` 只作为已批准的 SQLSTATE 分类。
4. River 的 `client.go`/`migrator.go`/`queue_metrics.go` 保留 worker/listener/migration 底层 pgx
   allowlist。`inserter.go` 中 shared `InsertOptions`、`JobReceipt`、`buildInsertOptions`、错误 helper
   仍被 scoped producer 使用；不要整文件删除。legacy `JobInserter(any)`/`EnqueueFence(pgx.Tx)`
   与其 producer 在所有 owner 切换后再清理。
5. 本轮没有执行 GORM commit response-loss 注入、SIGKILL、Human/retry/join 全矩阵、完整跨 owner
   端到端或容量 EXPLAIN，遵守父任务精简政策。真实 Provider/发布与线上容量仍归相应发布门禁。
6. Foundation scope 仍不携带 Pool affinity；必须靠单一 Pool Composition 保证，不宣称已验证
   active foreign-Pool scope 拒绝。无 Schema 修改；回滚只涉及应用接线与本轮 fixture/工件。
