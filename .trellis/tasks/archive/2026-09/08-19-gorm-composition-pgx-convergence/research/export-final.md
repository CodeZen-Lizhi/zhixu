# Export Final 收口

## 2026-09-08 最终状态

七个模块核心 gate 放行后，完成 `internal/export/**` 的 legacy 清理。单一 GORM Repository 和 scoped River dispatcher 已可由主会话接入最终 Composition。

### 实现及调用边界

- 删除 `adapter/postgres/legacy_adapter.go`，移除公开 `NewRepository(DB)`、`DB`、`Option`、`WithEventAppender` 和 `WithAuditAppender`。
- 将既有显式 SQL core 改为私有 `exportRepository`，保留 DB-time、advisory lock、FOR UPDATE/SHARE、CAS、tagged scope、能力门禁、TTL、清理、下载及 keyset 语义；没有复制 SQL、改 Schema 或另建数据库连接。
- 私有 transaction 的 `Scope()` 直接返回 `foundation.TransactionScope`。生命周期 Event 和下载 Audit 直接调用 `ScopedAppender.AppendScoped`，删除 `SideFactTransaction() any` 以及 gormEventAppender/gormAuditAppender 的 `AppendTx(any)` 转接。
- Event 仍可选；配置 Event 时缺 scope 直接失败，下载 Audit 仍为必需依赖。所有 side facts 使用原 UoW 的活跃 scope，不能自行提交或回滚。
- 删除 `adapter/river/dispatcher.go` 的旧 pgx Dispatcher/构造器。唯一状态列表移入最终 GORM dispatcher，保持 active duplicate 去重、completed 后允许恢复。`NewGORMTransactionalDispatcher(pool, client, scopedFence)` 签名不变，静态 fence 仍由 cmd 根据配置选择。
- 保留 `NewGORMRepository(pool, ...GORMOption)`、`WithGORMEventAppender`、`WithGORMAuditAppender` 签名，原 pgx Worker 仍可消费官方 database/sql driver 的入队结果。
- 原位迁移旧 fixture、dispatcher 依赖缺失/commit/rollback/错误断言和 architecture constructor 列表。未增加测试文件或临时测试程序，未删业务断言、skip 或使用 build tag 隐藏失败。

### 验证命令与结果

全部通过：

```bash
go test -mod=vendor -count=1 -timeout=60s ./internal/export/...
go test -mod=vendor -tags=integration -run '^$' -timeout=60s ./internal/export/adapter/postgres
go vet -mod=vendor ./internal/collection/... ./internal/export/...
git diff --check -- internal/collection internal/export
```

真实 PostgreSQL/Testcontainers 使用 fail-on-unavailable，以下两组 race 集成验证分别为 51.648s 与 46.053s，均在每命令 60 秒上限内 PASS：

```bash
go test -mod=vendor -tags=integration -race -count=1 -timeout=60s -run '^(TestExportRepositoryLifecycleIdempotencyAndDownloadAudit|TestExportGORMRepositoryCommitResponseLossDoesNotDuplicateFacts|TestExportGORMRepositoryKeepsOptionalLifecycleEventSemantics)$' ./internal/export/adapter/postgres
go test -mod=vendor -tags=integration -race -count=1 -timeout=60s -run '^(TestExportRepositoryConcurrentCreateAndDownload|TestExportRepositoryAttachmentCapabilityLifecycleAndScopeIsolation|TestExportGORMDispatcherRollbackAndPGXWorkerConsumption)$' ./internal/export/adapter/postgres
```

覆盖生命周期和下载统计/Audit 原子性、commit 响应丢失后的精确重放、可选 Event、同 key 并发、附件 capability/scope 隔离，以及 fence 拒绝的同事务回滚、active 去重、completed 后恢复和真实 pgx Worker 两次消费。

### Go / SQL review

按 go-review 与 sql-code-review 自审，未发现剩余范围内的明确缺陷：

- 公开构造器只接 Pool、scoped collaborators；Application/Domain 未引入 GORM、database/sql 或 pgx 类型。
- scoped Event/Audit 与 Export 写入共享同一事务，保留缺依赖 fail-closed、错误链、context cause、Rows 关闭及 post-commit replay 行为。
- 现有私有 UoW transaction adapter 和单一 SQL core 保持；本次不扩展事务实现。所有 SQL 参数化，Workspace、稳定排序、有界查询、锁顺序及 DB-time fence 不变。
- Architecture 测试仅允许 NewGORMTransactionalDispatcher；旧入口和跨模块 any transaction bridge 搜索无残留。

### 集成及回滚

主会话负责 cmd 的接线、整体静态门禁和跨 owner 编译。没有 Schema、HTTP wire、历史事实、文件格式、依赖或部署变化；回滚需按最终 Composition 和 scoped Event/Audit/Workflow 调用方的依赖闭包恢复代码。未执行全仓测试、生产浏览器/容量验证、commit/push/archive 或 active pointer 修改。
