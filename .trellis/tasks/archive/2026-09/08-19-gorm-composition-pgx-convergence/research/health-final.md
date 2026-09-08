# Health Final：同步 UnitOfWork 收口

记录时间：2026-09-08。修改范围限于 `internal/health/**`；Composition、任务状态和其他 owner 由 Final 主会话整合。

## 当前结果

- GORM Health 不再使用 goroutine、channel 或模拟 `pgx.Tx` 持有事务。`gormDB.Within` 在共享 `foundation.UnitOfWork.Within` 的同步回调内通过 `platform/postgres.GORMTransaction(scope)` 执行查询。
- 新增包内 `healthRow`、`healthRows`、`healthSQL`、`healthTransaction` 和 `healthTransactor`。事务值不暴露 Commit/Rollback；Workflow、Events、Collection 只能使用调用方的同一 `foundation.TransactionScope`。
- GORM 构造持有中性核心 `.core`，不引用 legacy 构造。Scan、Issue、Schedule、Read、FactReader、AffectedChange 的 SQL/codec/锁/CAS/回执规则继续共享现有实现。
- 主会话确认七模块核心门禁全部通过后，已删除 `adapter/postgres/legacy_adapter.go` 和 `adapter/collection/legacy_membership.go`。Health 生产代码的 pgx/pgconn、StartTx、AppendTx、transaction any 和 legacyHealth 扫描均零命中。

## 接线合同

- `NewGORMScanRepository(pool, scopedRuntime, scopedEvents, scopedVerifier, ids, clock)`：runtime 必须实现 Workflow `ScopedRuntimeStarter`，events 必须实现 Events `ScopedAppender`；smart collection 使用 scoped verifier。只处理 workspace/topic 时可以不传 verifier。
- `NewGORMScanStateRepository(pool, scopedEvents)`：Worker 只需状态推进时使用。
- `NewGORMIssueRepository(pool, membership, scopedVerifier, ids)`：传入 membership 时必须同时提供 scoped verifier；缺少任一端会在构造阶段失败。无 smart collection 时可以只传 pool/ids。
- verifier 支持 Collection `ScopedDurableScanBindingVerifier`，也支持 `health/adapter/collection.NewScopedBindingVerifier(collectionPort)` 生成的 Health adapter。
- `NewGORMScheduleRepository(pool, ids[, lease])`、`NewGORMReadRepository(pool)`、`NewGORMFactReader(pool[, membership])` 保留原 GORM 入口。
- `NewGORMAffectedChangeDispatchRepository(gormScans, planner)` 与 Scan 共享同一 GORM 数据库/UoW。
- `NewGORMScanCancellationGuard(pool, scopedEvents)` 只参与调用方 scope，不开启、提交或回滚新事务。

## 保留与修正的行为

- Start 保持 Repeatable Read；Scan 快照读取保持 Repeatable Read + ReadOnly。
- `withHealthTransaction` 区分 begin、业务回调和 commit 错误。只有回调成功后出现的提交错误允许原有持久化事实回查；调用方取消/超时不进入成功回查分支。
- AffectedChange poison 先正常提交持久标记，再返回 `ManualRecoveryRequired`，避免把人工恢复提示当事务失败而回滚。保留 5 秒独立 response-loss 回查。
- Query/Row/Rows 保留调用方取消和自定义 cancel cause；Rows 由原有 Close/Err 约定释放。
- Go/SQL 审查发现并修复 `IssueRepository.loadLatestObservation` 的首个 rows 在 Scan/JSON 解码提前失败时未关闭的问题。
- Issue 批量协调、missing-set 和 decision 在事务失败时继续返回零值结果；不会暴露尚未确认提交的计数或实体。
- 普通 NoRows 改用 `database/sql.ErrNoRows`，SQLState 分类统一复用 `platform/postgres.SQLState`。

## 实际验证

所有容器用例使用已有 `requireHealthIntegrationPlatform`，真实 Testcontainers PostgreSQL 和同一个平台 Pool；没有新增测试文件或临时测试代码。原有 Scan/Schedule/AffectedChange fixture 已原位切换到 GORM/scoped 依赖。提交响应丢失注入现在包装真实 UoW：真实事务成功提交后仅一次返回注入错误。

以下命令均通过，单次 Go test timeout 均为 60 秒：

```bash
go test -mod=vendor -timeout=60s ./internal/health/...
go vet -mod=vendor ./internal/health/...
git diff --check -- internal/health
```

最后一次默认 Health 包测试的 postgres 包耗时 1.468s；vet 和 diff check 无输出。

容器命令共同前缀为 `go test -mod=vendor -tags='integration testcontainers'`，共同后缀为 `-count=1 -timeout=60s ./internal/health/adapter/postgres`：

| `-run` 表达式 | 结果 | 覆盖 |
| --- | --- | --- |
| `^TestGORMHealthAdaptersUseOneRealPostgresPool$` | PASS，9.963s | Scan/runtime、Issue observation/decision/history、Schedule、Read、FactReader 共用实际平台连接池 |
| `^Test(GORMScanRepositoryPassesSmartBindingThroughScopedVerifier\|HealthScanRepositoryRuntimeReplayAdvanceFinishAndCancellation\|FactReaderCancellationReleasesBlockedConnection)$` | PASS，25.808s | scoped verifier、状态重放/完成/取消、Events 失败回滚、取消 cause 与连接释放/复用 |
| `^Test(HealthScanRepositoryPreventsConcurrentScopeAcrossIdempotencyKeys\|HealthScanFinishRecoversCommitResponseLossWithoutDuplicatingCompletionEvent\|HealthScheduleRepositoryClaimsOnceReclaimsSameDueAndAcknowledges)$` | PASS，29.622s | scope 并发限制、完成事件 response-loss、Schedule SKIP LOCKED/lease/receipt/recovery |
| `^TestAffectedChangeDispatcher(ConcurrentClaimCreatesOneRuntime\|RuntimeFailureRollsBackAndRestartPublishes\|DefersActiveWorkspaceScanThenBindsOwnScan)$` | PASS，22.214s | 唯一派发、跨 owner 失败回滚、active scan 退避 |
| `^TestAffectedChangeDispatcher(PersistsSchemaAndSourcePoison\|RecoversCommitResponseLoss\|PublishesLexicalDegradation)$` | PASS，31.179s | schema/source poison 持久化、提交响应丢失恢复、lexical degradation 派发 |

另逐文件对照 HEAD 与当前全部反引号字面量，Scan 31、Issue 79、Schedule 23、AffectedChange 11、Read 19、FactReader 10，均完全一致。未修改 SQL、schema、索引、字段映射或公共 API DTO。审查保留 workspace 条件、参数绑定、keyset 顺序、批量写入、CAS 和锁顺序。

## Final legacy 清理与复验

主会话正式放行后，所有剩余 Health fixture 已原位迁到 GORM/scoped 接入：Issue observation/missing-set/batch、Read history/trend、topic scope、detector dedupe、真实 River helper。保留所有原有断言和 build tags。生产只留下原有的中性 SQL/codec/业务核心；scoped Collection verifier 不再 fallback 到事务外 revalidation。

- 统一现有平台测试 helper 到 `integration_cleanup_test.go`，integration 下也能获得同一完整 Pool。
- Query/transaction 语句计数和并发 barrier 改包内中性接口，实际 SQL 仍由 GORM/UoW 执行。
- 原先藏在 fixture 外层 pgx transaction 中的 seed 先提交，再调用 GORM；清理覆盖随之持久化的 Proposal 等测试事实。
- River queue/worker 使用批准的原生连接池；Health/Workflow/Events 的业务状态与 cancellation hook 使用 GORM/scoped 接口。

清理后验证：

```bash
go test -mod=vendor -timeout=60s ./internal/health/...
go vet -mod=vendor ./internal/health/...
go test -mod=vendor -tags='integration testcontainers' -run '^$' -timeout=60s ./internal/health/adapter/postgres
git diff --check -- internal/health
```

全部通过。以下真实 PostgreSQL 命令沿用前述共同前缀/后缀：

| `-run` 表达式 | 结果 |
| --- | --- |
| `^TestIssueRepository(ReconcileDetectorPageUsesFixedStatementCount\|ConcurrentNewIdentityRollsBackLosingPage\|ReconcileDetectorPageDuplicateReopenUsesOriginalVersionCAS)$` | PASS，26.581s；1/25/100 observation 固定 7 语句、并发 loser 原子回滚、重复 reopened CAS |
| `^TestReadRepository(IssueHistoryIsBoundedStableAndUsesFixedStatements\|HealthTrendAggregatesSevenUTCDaysFromTerminalScans)$` | PASS，25.845s；完整 261/260 历史分页、固定查询数、EXPLAIN 索引和 7 日 trend |
| `^TestHealthScan(RunsThroughRealRiverAndReplays\|CancellationConvergesWithWorkflow)$` | PASS，35.864s；真实 River 成功/重放与跨 owner 取消闭合 |
| `^Test(TopicScopeReaderAndMissingSetUseConfirmedMembership\|FactReaderEvidenceDetectorsDeduplicateAndPageByTypedKey)$` | PASS，20.099s；scope/missing-set、typed keyset 和证据去重 |

未重新执行每个历史集成用例；全部 fixture 已编译，以上是当前清理版本实际跑过的代表路径。API/Worker Composition、启动关闭和全仓 allowlist 由 Final 主会话统一验证。本次未修改 schema、cmd，未部署、commit、push 或归档；回滚通过代码版本和 Composition 整体恢复，不删除持久业务事实。
