# Export GORM 静态与动态验证记录

## 契约与静态检查

- 公共 `NewGORMRepository` 只接收 `*platformpostgres.Pool` 与 `...GORMOption`；公共 GORM 契约未暴露 `*gorm.DB`、`*sql.Tx`、`pgx.Tx` 或 `any`。
- shared SQL core 不导入 pgx；pgx 仅保留在 `legacy_adapter.go`、legacy River 实现和测试中。私有 side-fact bridge 的 opaque transaction 不跨模块暴露。
- 没有 `AutoMigrate`、Migrator、隐式 Model、软删除、Schema 或历史 migration 改动。
- 生产 `cmd/**` 未引用 staged GORM Export 构造器；Composition 切换与 legacy 删除继续由 Final child 处理。
- 参数化 SQL、数组参数归一化、稳定 keyset、DB-time、CAS、锁顺序、rows close/error、SQLSTATE、context cause 和事务错误路径已按 Go/SQL review 检查。

## 本地门禁

以下命令通过：

```bash
go test -mod=vendor -race -count=1 -timeout=60s ./internal/export/... ./internal/events/... ./internal/audit/...
go test -mod=vendor ./internal/workflow/adapter/river ./internal/platform/postgres
go vet ./internal/export/... ./internal/events/... ./internal/audit/... ./internal/workflow/adapter/river ./internal/platform/postgres
go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker
go test -mod=vendor -tags=integration -run '^$' ./internal/export/adapter/postgres
go mod tidy -diff
go mod verify
git diff --check
```

## 真实 PostgreSQL 门禁

使用项目 `testdb` Testcontainers fixture，在 legacy/GORM 变体之间使用独立随机数据库。下列顶层用例均以 `-mod=vendor -tags=integration -race -count=1 -timeout=60s` 单独运行并通过：

- `TestExportRepositoryLifecycleIdempotencyAndDownloadAudit`
- `TestExportGORMRepositoryKeepsOptionalLifecycleEventSemantics`
- `TestExportGORMRepositoryCommitResponseLossDoesNotDuplicateFacts`
- `TestExportRepositoryAttachmentCapabilityLifecycleAndScopeIsolation`
- `TestExportRepositoryDatabaseTimeCAS`
- `TestExportRepositoryConcurrentCreateAndDownload`
- `TestExportRepositoryCleanupFailureCanRetry`
- `TestExportRepositoryOrphanSweepWorkspacesUsesStableCursor`
- `TestExportRepositoryPreservesContextCauseAndReusesConnection`
- `TestExportRepositoryRejectsCorruptRowsWithoutPartialResult`
- `TestExportRepositoryTargetQueryPlansUseDeclaredIndexes`
- `TestExportGORMDispatcherRollbackAndPGXWorkerConsumption`

生命周期双实现与 GORM optional Event 另以 `-race -count=3` 通过。一次外部管理库支持的附件 `count=3` 运行在第三轮因外部容器消失中断；随后使用项目自有 Testcontainers fixture 的 `count=1` 回归通过，未将该基础设施中断计为产品代码通过证据。

## 审查闭环

独立 reviewer 初审提出四项 P2：任务文档漂移、EXPLAIN 未复用完整生产 SQL、River completed-recovery 缺真实数据库覆盖、Repository post-commit replay 缺真实数据库覆盖。修复后复审未发现 P0-P2：

- EXPLAIN 直接复用六条生产 SQL 常量，覆盖 collection、attachment、expire、cleanup、prepared staging 和 recovery 目标索引。
- GORM River 用例验证 active duplicate 唯一、completed 后恢复 job，并由现有 pgx Worker 完成两次消费。
- GORM Repository 用真实 UoW 注入 commit 后响应丢失，验证 Create 精确重放以及 Download 不重复计数/Audit。
- 无 Event appender 时 Create 正常且不生成 Event，公共 options 保持强类型。

## 残余范围

- EXPLAIN 通过 `enable_seqscan=off` 证明目标索引可被规划器选择，不替代生产数据规模下的容量和时延验证。
- 单条全量 integration suite 会因每个变体创建并执行 Atlas migration 超过 60 秒预算，因此按顶层用例拆分执行；未宣称全量单命令通过。
- staged 实现尚未接入生产 Composition；该动作及 legacy 删除属于父任务 Final child。
