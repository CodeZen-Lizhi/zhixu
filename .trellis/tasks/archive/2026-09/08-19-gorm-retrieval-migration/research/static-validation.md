# Retrieval GORM 验证记录

日期：2026-09-08。范围：`internal/retrieval/**` 与本 child 工件。
本记录更新 2026-08-21 的 staged 静态结论：TODO 9 工厂和 Workflow/Change Control scoped owner
现已到位，完整 GORM Dispatcher/Completion 已完成，并在真实 PostgreSQL 上通过下面的局部门禁。
生产 Composition 和 legacy 删除仍由 Final 负责；本 child 不归档、不切 active pointer。

## 环境与策略

- Docker 29.4.0 / OrbStack；Testcontainers-Go 0.40.0；`pgvector/pgvector:pg16`，Ryuk 正常启用。
- 既有 `newRetrievalTestRepository` 改用 `testdb.Require`，可选 `ZHIXU_TEST_DATABASE_URL` 作为外部 admin，
  默认独立 Testcontainers；`Availability: FailWhenUnavailable`、MaxConns=8，不以 skip 替代 PASS。
- 每个 legacy/GORM 子测试独立正式迁移数据库。GORM、native、UoW、Workflow/CC/Model Settings/River
  来自该 fixture 的同一个完整 platform Pool。所列 PostgreSQL 容器均正常终止和清理。
- 使用父任务 2026-09-01 精简政策；只原位适配现有 fixture/测试场景，没有新增测试文件。
- 本轮不跑全仓构建/测试，不启动本地服务/浏览器，不修改 migration 或依赖版本。

## 实库命令与结果

所有以下命令在仓库根目录运行；耗时取 go test 包级输出，不包含外围调度等待。

1. **PASS，34.706s**，FTS 与 hybrid vector batch，两个场景的 legacy/GORM 变体均实际执行。

```sh
go test -mod=vendor -tags=integration ./internal/retrieval/adapter/postgres -run '^TestRepository(FTSOnlyBuildReadyActivateAndReplay|HybridVectorBatchReadyAndReplay)$' -count=1 -timeout=60s -v
```

2. **PASS，15.943s**，Active Search/Workspace/Source/path/time filters、bounded provenance、incremental
   Snapshot 与既有 EXPLAIN，legacy/GORM 对照。

```sh
go test -mod=vendor -tags=integration ./internal/retrieval/adapter/postgres -run '^TestSearchRepositoryActiveFiltersAndBoundedProvenance$' -count=1 -timeout=60s -v
```

3. **PASS，11.420s**，真实 River insert 后注错，Delivery、Job、published_at 全回滚；再用公开构造
   NewGORMDispatcher 成功提交并检查 queue/dispatch generation/Outbox 三方 closure。

```sh
go test -mod=vendor -tags=integration ./internal/retrieval/adapter/postgres -run '^TestDispatcherInsertFailureRollsBackDeliveryAndPublishedAt$/gorm$' -count=1 -timeout=60s -v
```

4. **PASS，15.387s**，Retry due、generation、workspace try-lock 不等待和 published_at 不变。
   之前 legacy 变体已通过；此命令复验 GORM 的时间参数修复。

```sh
go test -mod=vendor -tags=integration ./internal/retrieval/adapter/postgres -run '^TestDispatcherRetryCreatesNextGenerationWithoutChangingPublishedAt$/gorm$' -count=1 -timeout=60s -v
```

5. **PASS，37.261s**，Completion legacy/GORM 原子提交、response-loss、exact/historical replay；
   GORM owner 两次 CAS 成功后注错，Activation/Delivery/Attempt/Execution/Proposal 全回滚。

```sh
go test -mod=vendor -tags=integration ./internal/retrieval/adapter/postgres -run '^TestCompleteReindexTxAtomicallyActivatesAndReplaysAfterResponseLoss$|^TestCompleteReindexTxFaultInjectionRollsBackEveryMutationStage$/gorm_owner_rollback$' -count=1 -timeout=60s -v
```

6. **PASS，23.564s**，cleanup/Workflow 前置条件 fail closed；等待 workspace lock 后以数据库时间
   重验 lease，过期拒绝，索引和完成状态不变。

```sh
go test -mod=vendor -tags=integration ./internal/retrieval/adapter/postgres -run '^(TestCompleteReindexTxRequiresCleanupAndSucceededWorkflowWithoutMutatingIndexes|TestCompleteReindexTxRechecksLeaseAfterWorkspaceLockWait)$/gorm$' -count=1 -timeout=60s -v
```

7. **PASS，40.835s**，Regression pass/replay、五种 structural mismatch 的 legacy/GORM 对照，
   以及 GORM 严格 Processor Context。

```sh
go test -mod=vendor -tags=integration ./internal/retrieval/adapter/postgres -run '^TestRepositorySnapshotRegression(PassesAndReplaysWithoutChangingBuildingIndex|FailsClosedForStructuralMismatch)$|^TestProcessorContextLoaderReturnsStrictReadyCheckpointContext$/gorm$' -count=1 -timeout=60s -v
```

8. **PASS，12.901s**，删除 Completion fixture 重复的 replica-role Proposal 绑定后，最终重验同一
   GORM Completion commit/replay 主路径。

```sh
go test -mod=vendor -tags=integration ./internal/retrieval/adapter/postgres -run '^TestCompleteReindexTxAtomicallyActivatesAndReplaysAfterResponseLoss$/gorm$' -count=1 -timeout=60s -v
```

## 局部门禁

- `go test -mod=vendor ./internal/retrieval/... -count=1 -timeout=60s`：PASS，8 个包。
- `go test -race -mod=vendor ./internal/retrieval/... -count=1 -timeout=60s`：PASS，8 个包。
- `go vet -mod=vendor -tags=integration ./internal/retrieval/...`：PASS，包括当前 integration fixture。
- `python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-retrieval-migration`：PASS；仅有既有大规格/研究文档注入截断 warning。
- `git diff --check`：PASS；所有本次 Go 文件 gofmt -l 无输出。
- GORM 文件禁用模式扫描：无 pgx Tx/Row/Rows/Pool、gorm.Open、AutoMigrate/Migrator、手工 Begin/Commit/Rollback。
  `pgconn` 仅用于 SQLSTATE/cause 分类，native pgx 在三个显式 capability 中。

## Go / SQL / Trellis 自检

没有递归分派子代理；本 implementer 按已读取的 go-review、sql-code-review、trellis-check 自检。

1. **已修复 SQLSTATE 42804**：GORM 把 legacy 复用的 `$8` 展开为多个独立参数后，
   `CASE ... THEN ? ELSE NULL END` 将时间参数推断为 text。Delivery.Fail 的 completed_at 参数改为
   显式 timestamptz；Retry 真库路径已复验。其他 Index transition CASE 有已知 timestamp 列作为 ELSE，
   不存在相同 unknown/NULL 推断。
2. **已修复 shared native 查询歧义**：Snapshot incremental SELECT 在 join core.source 后仍使用
   未限定的 workspace_id/created_at。现所有投影列限定 source_manifest；Search legacy/GORM 完整场景通过。
   未改变 session lock、RR、COPY 或 release 生命周期。
3. **已修复 fixture 契约漂移**：Workspace 的旧 test/active seed 改为合法 inactive；Authorization 前补齐
   Proposal Workflow binding 和 proposal_revision_dispatch，使用正常约束，无 trigger 放宽或授权 bypass。
   Completion 不再重复通过 replica role 更新 Proposal；原有 Workflow completion/故障构造仍是 fixture。
4. **修正过窄 EXPLAIN 断言**：PG16 小 fixture 实际使用 uq_retrieval_source_manifest_version，以 index_version_id
   Index Cond 有界访问，并保留 Workspace 过滤。断言接受该索引及原 workspace/index/source 索引；
   未改变生产 Search SQL，未将小 fixture 当作容量结果。
5. Dispatcher/Completion 只有 Retrieval UoW 提交；owner 操作解包同一 scope，不嵌套事务。
   First Outbox -> workspace -> binding -> Delivery -> River -> publish；Retry 持 Delivery row 后只 try-lock。
   Completion workspace -> Proposal -> Execution -> Delivery -> Attempt -> sorted Index，最终 mutation 重验 lease。
6. mutation 复用 Domain state/fact validators、显式 version CAS 和错误码。Completion 返回值只有 commit 成功
   才可见，response-loss 依赖 durable replay；没有跨 owner mutation SQL、任意动态 identifier 或逐行 ORM 写入。
7. 新构造检查 nil/typed-nil，拒绝 legacy enqueue fence；context 贯穿 SQL；Rows Close/Err、参数化 UUID/
   JSONB/array/vector、Workspace predicate、稳定排序、上限和有界 Index 锁保持既有约束。
8. Final 要保留共享 validators/SQL/scanners 与 native helper，不能整文件删除 legacy；清单见 final-handoff.md。
   当前未发现尚未修复的范围内 P0/P1/P2 问题。

## 未覆盖与交接限制

- 没有执行 500k/384 维/HNSW+IVFFlat/30 samples/P95/recall，完整 SQLSTATE、connection/session failure、
  全量逐阶段 fault、HTTP/真实 Worker 进程烟测。这些不是本次 PASS 的含义。
- Search 既有 connection-specific trigram threshold 断言仍构造 legacy connection repository；GORM 的
  transaction-local set_config 有生产路径执行及静态证据，本次没有新增恶意 session 注入矩阵。
- Source Refresh session lease 生命周期本次未修改，未重复实库竞争/取消矩阵。Snapshot 流程经真实
  COPY/temp/session 路径执行，但没有重跑 unlock 故障和容量矩阵。
- 既有 capacity_benchmark_integration_test.go 仍以 Workspace status=test seed/cleanup marker；未来运行
  显式容量 gate 前需与当前 Workspace 约束一起适配，此文件未在本次改动和执行范围。
- Foundation scope 不验证 active foreign Pool identity；必须由 Final 从同一个完整 platform Pool 构造
  Retrieval、Workflow、CC、Model Settings 和 River。不能用本次同池 fixture 推断任意跨池 scope 都安全。
- 生产 API/Worker 切线、完整 owner composition 校验、legacy 删除、全仓 allowlist 与发布由 Final 负责。
