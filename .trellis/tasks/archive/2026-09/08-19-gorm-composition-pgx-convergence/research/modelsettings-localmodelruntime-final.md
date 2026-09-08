# Model Settings / Local Runtime Final 收口记录

日期：2026-09-08。Owner：`/root/gorm_models_final`。活跃任务：`08-19-gorm-composition-pgx-convergence`。

## 变更范围

本轮在各模块既有 GORM 门禁已通过、Final 已放行旧实现删除后执行。独占范围为 `internal/modelsettings/**`、`internal/localmodelruntime/**`；主 agent 追加授权修改现有 `internal/platform/migration/managed_ollama_integration_test.go`。未修改 `cmd/**`、其他领域文件、Schema、配置或迁移 SQL。

- 删除 Model Settings 旧 pgx `Repository`、`Bootstrap` 和 `SettingsAuditAppender(any)`，以及 Local Runtime 的 `PostgresStore`、`TxLifecycle`、`WithTx(pgx.Tx)`。GORM 导出构造签名和全部 Scoped ports 保持不变。
- 删除旧持久化文件中的重复实现，将 93 个共享纯函数迁入 codec、validation、projection 文件。只读脚本对照 `git show HEAD:<原文件>` 检查，去除空白后全部一致。
- 两个业务目录不再直接导入 `github.com/jackc`。GORM 无记录错误使用 `database/sql` / GORM 错误；Model Settings SQLSTATE 分类通过现有 `platformpostgres.SQLState`，保留原错误码与重试分类。
- 更新现有 Model Settings 测试为共享 `platformpostgres.Pool`、Scoped audit 和 UnitOfWork。实库 fixture 统一 `testdb.Require`、`FailWhenUnavailable`，Model Settings 的 `MaxConns` 为 8。
- 恢复执行 4 个原先 `t.Skip` 的旧协议用例，按当前 prepare → arm → commit → acknowledge → finalize 热激活协议构造数据，保留 revision、runtime ownership/staleness、enqueue fence 和 snapshot 业务断言。Snapshot 检查当前协议的 `ApplyRequired`，同时断言 `RestartRequired` 为 false。
- managed Ollama 现有 integration fixture 只构造同一个 Pool 的 `NewGORMStore`，删除 legacy variant。原业务断言保留；custom cancellation cause 从 GORM 条件分支改为无条件断言。测试中的 pgx 仅用于隔离 fixture、锁和数据库事实断言。

主要文件：

- 新增 `internal/localmodelruntime/lifecycle_codec.go`；删除 `lifecycle_postgres.go`；修改 `gorm_core.go`、`lifecycle_store.go`。
- 新增 Model Settings postgres 下的 `persistence.go`、`revision_codec.go`、`activation_validation.go`、`runtime_validation.go`、`rollout_validation.go`、`participant_validation.go`、`snapshot_projection.go`、`audit_codec.go`。
- 删除 Model Settings postgres 下的旧 `repository.go`、`revision.go`、`activation.go`、`runtime.go`、`rollout.go`、`participant.go`、`snapshot.go`、`audit.go`，以及 `runtime/bootstrap.go`。
- 修改 Model Settings `gorm_core.go`、`gorm_audit.go`、`application/ports.go`、`runtime/bootstrap_gorm.go` 和 3 个现有测试文件；修改 `internal/platform/migration/managed_ollama_integration_test.go`。

## 验证证据

以下命令均在仓库根目录执行。没有新增测试文件或临时测试代码；所有 Go 测试设置 `-timeout=60s`。实库测试使用 `testdb.Require` 提供的隔离数据库，实际启动 Testcontainers `pgvector/pgvector:pg16`。

### 构建与静态检查

```bash
go build -mod=vendor ./internal/modelsettings/... ./internal/localmodelruntime
go test -mod=vendor ./internal/modelsettings/... ./internal/localmodelruntime -count=1 -timeout=60s
go vet -mod=vendor ./internal/modelsettings/... ./internal/localmodelruntime
go vet -mod=vendor -tags=integration ./internal/modelsettings/adapter/postgres
go vet -mod=vendor -tags=integration ./internal/platform/migration/managed_ollama_integration_test.go
git diff --check -- internal/modelsettings internal/localmodelruntime internal/platform/migration/managed_ollama_integration_test.go
```

全部 exit 0；普通 Go 测试 7 个包通过，最长包为 runtime，耗时 2.145s。19 个新增/修改 Go 文件逐一 `gofmt -l` 无输出。未改动的 `internal/localmodelruntime/ollama_client.go` 存在基线格式差异，本轮未扩展修改。

定向搜索确认两个业务目录无旧构造器、旧 transaction ports、`github.com/jackc` import 或 `t.Skip`；无 GORM AutoMigrate / Migrator / Schema 操作。

### Model Settings 全部 11 条现有 integration

```bash
go test -mod=vendor -tags=integration ./internal/modelsettings/adapter/postgres -run '^TestGORMRepositorySaveDesiredRollbackAndScopedEnqueueFence$' -count=1 -timeout=60s -v
go test -mod=vendor -tags=integration ./internal/modelsettings/adapter/postgres -run '^TestRepository(RevisionRolloutRuntimeAndEnqueueFence|CommitActivationRequiresArmedRuntimes|EnqueueFenceSerializesArming|RuntimeOwnershipAndStaleness)$' -count=1 -timeout=60s
go test -mod=vendor -tags=integration ./internal/modelsettings/adapter/postgres -run '^TestRepository(HotActivationProtocolAndRecovery|RuntimeTakeoverRequiresDatabaseTimeStaleness|ParticipantTakeoverRebuildsWhileArming)$' -count=1 -timeout=60s
go test -mod=vendor -tags=integration ./internal/modelsettings/adapter/postgres -run '^TestRepository(ConcurrentDesiredSaveUsesExpectedRevision|WrongKeyAndCiphertextSubstitutionFailClosed|AuditFailureRollsBackRevisionStateAndAudit)$' -count=1 -timeout=60s
```

依次 PASS：7.284s、42.490s、34.142s、27.376s；没有 skip。覆盖 Audit / River / Local Runtime preparation 原子提交与回滚、失效 scope、enqueue 与 arming 锁竞争、热激活恢复、数据库时间租约、参与者接管、并发版本保存、AEAD 错密钥/密文替换拒绝，以及审计失败回滚。

### Local Runtime 4 个指定实库场景

完整 migration 测试包当时被其他 owner 尚未迁完的符号阻塞，因此补充直接运行授权范围内的现有测试文件：

```bash
go test -mod=vendor -tags=integration ./internal/platform/migration/managed_ollama_integration_test.go -run '^TestManagedOllama(LifecycleStoreCAS|ScopedActivationTransaction|ReadDemandCancellationAndConnectionRelease|RuntimeRoleLeastPrivilege)$' -count=1 -timeout=60s -v
```

PASS，25.668s。实际执行 CAS、Scoped transaction commit/rollback、失效 scope 拒绝、取消原因/连接归还、runtime role 最小权限。日志确认 4 个测试均启动并清理隔离 Testcontainers 数据库；无 skip。

这条单文件命令仅证明对应场景通过，不能替代完整 `internal/platform/migration` 测试包的编译与集成门禁。

## Go / SQL Review

已按 `go-review` 与 `sql-code-review` 进行人工 diff 和直接调用链检查。

- Root repository 与 Local Runtime、Audit、River 使用同一平台 Pool；借用 Scoped transaction 不自行 commit/rollback，不通过新 Pool 或嵌套事务逃逸原子边界。
- 已验收 GORM 核心 SQL、查询数量、行锁顺序、数据库时间 lease/CAS、operation/hold 原子关系没有因删除旧实现而改动。共享纯函数迁移无语义变化。
- SQL 参数化、并发 revision CAS、错误分类、取消传播、资源关闭和脱敏边界保留；格式化安全和 AEAD/audit 实库断言通过。
- 现有 scoped enqueue 测试使用实际 UnitOfWork；取消测试继续要求 `context.Canceled`、custom cause 和归还连接，未以 skip、build tag 或删除断言掩盖问题。
- 本轮范围内未发现需要继续修复的缺陷。未执行 race 或全仓测试；已明确记录全包编译阻塞。

## 交接与待复验

以下两条完整 package 命令在该次执行时 exit 1，失败发生于编译阶段，未进入数据库测试：

```bash
go test -mod=vendor -tags=integration ./internal/platform/migration -run '^TestManagedOllama(LifecycleStoreCAS|ScopedActivationTransaction|ReadDemandCancellationAndConnectionRelease|RuntimeRoleLeastPrivilege)$' -count=1 -timeout=60s
go vet -mod=vendor -tags=integration ./internal/modelsettings/adapter/postgres ./internal/platform/migration
```

观察到的跨 owner 在途编译残留：

- `internal/changecontrol/adapter/postgres/repository.go`、`internal/knowledge/adapter/postgres/relation_apply.go`：`eventsapplication.Appender`。
- `internal/changecontrol/adapter/postgres/repository_writeback.go`：`workflowapplication.CancellationSafetyGuard`。
- `internal/capture/adapter/postgres/profile_repository.go`：`agentapp.ModelRunTxFinalizer`。
- `internal/capture/adapter/postgres/repository.go`：`workspacepostgres.TransactionWriter`。
- 最后定向搜索仍在 `cmd/worker/main.go:375` 发现 `modelsettingsruntime.BootstrapResult`，需由主 agent 改成保留的 `GORMBootstrapResult`。

上述事项已发消息交给主 agent；没有越过 owner 边界修改。交接时主 agent 回复 CC legacy 已清理且产品 build PASS，并接手 Worker 类型修正及其他 owner 收口后的完整 migration package 复验。此处失败记录保留为当次执行证据，不代表这些残留在整合后仍然存在。

已知边界：opaque `TransactionScope` 不携带可校验的 Pool identity，同 Pool 由 Composition 保证，不能声称能在运行时拒绝所有活跃 foreign-Pool scope。credential-init 的独立 pgx / security allowlist 由主 agent 负责，本轮未改变或验证其外部行为。

本轮无 Schema 变更、提交、push 或部署。若回滚 Final cutover，需要连同调用方 Composition 与移除的旧接口一起回滚，避免新旧构造契约混用。
