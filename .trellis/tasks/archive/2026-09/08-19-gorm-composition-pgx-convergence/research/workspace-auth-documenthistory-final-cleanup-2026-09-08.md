# Workspace / RootGrant / Auth / Document History Final cleanup

日期：2026-09-08。执行范围由 Final 主会话授权；本文只记录本 owner 的结果，不代替全仓 composition、发布或部署门禁。

## 范围与变更

- Workspace：删除旧 pgx Repository、Registry、Control、Git capture、rebind Audit 实现及 pgx process composition；保留 `NewGORMRepository`、`NewGORMProcessComposition` 和既有 GORM/scoped 契约。把仍被使用的扫描、ID、nullable、SourceVersion metadata 比较和 runtime 授权/状态校验留在纯 helper 中。
- RootGrant：删除 pgx authority store 与 resolver 构造入口，保留 `NewGORMAuthoritativeStore`、`NewGORMRuntimeResolver`；managed/direct 读取语义和 capability 边界不变。
- Workspace 错误分类：使用 platform `SQLState` / `ConstraintName` 及 `sql.ErrNoRows` / `gorm.ErrRecordNotFound`，移除把 GORM no-row 转造为 pgx error 的路径；保留原错误码、重试性、取消原因和事务已结束语义。
- Auth：删除 pgx Repository 及重复 SQL；把错误和 canonical scopes 解码保留到 `errors.go` / `model.go`。`NewGORMRepository(*gorm.DB)`、数据库时钟、hash-only credential、单语句 session rotation 不变。
- Document History：删除 pgx Repository 及重复 SQL；把校验和错误 helper 保留到 `model.go` / `errors.go`。`NewGORMRepository(*gorm.DB)`、Workspace/Document/path 绑定、批量映射顺序与上限不变。
- 既有测试原地适配：Workspace/Auth pgx fake 改用 stdlib SQL driver + GORM；单入口集成夹具仅保留 GORM。Document History 原双实现对照改为完整预期 fixture struct 比较，保留 50 commits、单次查询、5,000 行 fixture、隔离/异常/取消与 EXPLAIN 断言；EXPLAIN 使用实际 GORM SQL。
- 追加授权的两个 migration 测试：`workspace_root_grant_repository_integration_test.go` 和 `workspace_root_rebinding_integration_test.go` 删除 legacy factory 与 `AppendTx` fault helper，保留 GORM/scoped fault 注入和全部业务断言。

没有新增测试文件、临时测试代码、依赖、Schema、build tags 或跳过断言。既有 `legacy-direct` Workspace fixture 表示受支持的历史数据兼容性场景，仍保留其断言。

## 修改文件

- `internal/workspace/adapter/postgres/`：新增 `model.go`；修改 `control_errors.go`、`control_scan.go`、`gorm_core.go`、`gorm_git_capture.go`、`gorm_rebind_audit.go`、`runtime.go` 及既有 guard/lifecycle/list/Git capture 测试；删除 `repository.go`、`registry.go`、`control.go`、`git_capture.go`、`rebind_audit.go`。
- `internal/workspace/runtimegrant/`：修改 `gorm_composition.go`；删除 `composition.go`。
- `internal/platform/rootgrant/`：修改 `gorm_store.go`；删除 `postgres.go`。
- `internal/auth/adapter/postgres/`：新增 `errors.go`；修改 `gorm_repository.go`、`model.go`、`queries.go` 和既有 Repository 单元/集成测试；删除 `repository.go`。
- `internal/documenthistory/adapter/postgres/`：新增 `errors.go`；修改 `gorm_repository.go`、`model.go`、`queries.go` 和既有 Repository 集成测试；删除 `repository.go`。
- `internal/platform/migration/`：仅修改上述两个 Workspace 文件。

## 已通过验证

各命令均在仓库根目录运行，未执行全仓测试。

```bash
go test -mod=vendor ./internal/workspace/... ./internal/platform/rootgrant -count=1 -timeout=60s
go vet -mod=vendor ./internal/workspace/... ./internal/platform/rootgrant
go test -mod=vendor ./internal/auth/... -count=1 -timeout=60s
go vet -mod=vendor ./internal/auth/...
go test -mod=vendor ./internal/documenthistory/... -count=1 -timeout=60s
go vet -mod=vendor ./internal/documenthistory/...
```

以上均通过。Document History postgres 包没有非 integration 测试；真实数据库行为由下述既有集成用例验证。

```bash
go test -mod=vendor -tags=integration ./internal/workspace/adapter/postgres -run '^(TestRepositoryWorkspaceAndSourceVersionLifecycle|TestGORMScopedSourceWriterUsesCallerOwnedUnitOfWork)$' -count=1 -timeout=60s
```

通过，17.603s。覆盖 Workspace/Source 生命周期与 Source scoped writer 使用调用方事务；事务 participant 不独立提交。

```bash
go test -race -mod=vendor -tags=integration ./internal/auth/adapter/postgres -run '^(TestServicePostgreSQLCredentialLifecycleUsesDatabaseClockAndNeverPersistsPlaintext|TestRepositoryPostgreSQLSessionRotationIsAtomicAndConcurrentAuthenticationUpdatesLastSeen)$' -count=1 -timeout=60s
```

通过，29.338s。覆盖数据库时钟、凭据不存明文、rotation 失败原子回滚和并发 authentication。

```bash
go test -mod=vendor -tags=integration ./internal/documenthistory/adapter/postgres -run '^TestRepositoryReadsDocumentAndBatchMapsManagedAndExternalCommits$' -count=1 -timeout=60s
```

通过，10.154s。完整 Document/mapping fixture 比较、50 commit 批量单查询、5,000 行 fixture 和真实 GORM EXPLAIN 均通过。

```bash
go test -mod=vendor -tags=integration internal/platform/migration/workspace_root_grant_repository_integration_test.go -run '^TestWorkspaceRootGrant(AuthoritativeStoresMatchManagedAndDirect|RepositorySerializesBeginAndTakesOverExpiredLease|RepositorySwitchAndRollback|RepositoryRuntimeFences)$' -count=1 -timeout=60s
```

通过，39.813s。四个真实 PostgreSQL 用例覆盖 managed/direct authority、取消 cause、并发 Begin/过期 lease takeover、switch/rollback、runtime binding/version/freshness fences。

```bash
go test -mod=vendor -tags=integration internal/platform/migration/workspace_root_grant_repository_integration_test.go internal/platform/migration/workspace_root_rebinding_integration_test.go internal/platform/migration/managed_ollama_integration_test.go -run '^TestWorkspaceRootRebinding(IsAuditedIdempotentAndFailClosed|SerializesReverseFingerprints)$' -count=1 -timeout=60s
```

通过，23.051s。两个真实 PostgreSQL 用例覆盖 rebind/history/Audit 同事务同时间、Audit 追加后注入失败回滚、缺 Audit 的 deferred commit failure、失败零结果、response-loss 幂等、mutation gate、runtime 失效、Git checkpoint 拒绝、历史只追加以及反向 fingerprint 并发冲突。`managed_ollama_integration_test.go` 是提供既有 `assertPostgresCode` helper 所需的原文件，未修改该文件，也未执行其测试。

真实 PostgreSQL 用例均使用既有 `testdb.Require`，配置 `FailWhenUnavailable`；没有模拟 PostgreSQL 成功结果。

```bash
rg -n 'jackc/pgx|\bpgx\.|pgxpool\.|pgconn\.|NewRepository|NewPostgresAuthoritativeStore|NewRuntimeResolver|NewProcessComposition|TransactionWriter|WithAuditAppender|AppendTx' internal/workspace internal/platform/rootgrant internal/auth internal/documenthistory -g '*.go' -g '!**/*_test.go'
```

无匹配，退出码 1。四个 owner 的生产 Go 文件已无上述 pgx/legacy 路径。测试中 pgx 仅用于共享平台 fixture 的准备、数据库状态读取和原生约束验证。

```bash
git diff --check -- internal/workspace internal/platform/rootgrant internal/auth internal/documenthistory internal/platform/migration/workspace_root_grant_repository_integration_test.go internal/platform/migration/workspace_root_rebinding_integration_test.go
gofmt -l internal/workspace internal/platform/rootgrant internal/auth internal/documenthistory internal/platform/migration/workspace_root_grant_repository_integration_test.go internal/platform/migration/workspace_root_rebinding_integration_test.go
```

均通过，无输出。

## Go / SQL Review

按已读取的 `go-review` 和 `sql-code-review` skill 进行人工差异审查，包含新增 helper、被删除实现的仍用纯逻辑、构造入口、测试 oracle 与事务注入边界。

- Go：保留导出 GORM 构造签名；生产依赖由同一 platform Pool 组合；没有新增 pool/连接所有权；`ScopedSourceWriter` / Audit scoped participant 不拥有事务提交；现有错误链、取消 cause 和连接释放约束保持。
- SQL：保留参数化、Workspace/Document/path 过滤、稳定 keyset/ordinality 顺序、批量上限及单语句 rotation；没有新增 N+1、循环数据库读取、无界列表、动态未转义标识符或迁移。Workspace advisory/row lock 顺序、数据库时钟、rebind history/Audit deferred 约束保持。
- 测试：删除的仅是旧实现入口与对照重复；原有失败、隔离、幂等、并发和 pool release 断言仍执行。原双实现比较替换为完整 fixture 预期，防止移除 legacy oracle 后覆盖变弱。
- 本范围未发现待修缺陷。

## 剩余门禁与交接

使用 `./internal/platform/migration` package 路径执行上述两组定向测试，曾在编译阶段被其他并行 owner 尚未完成的清理阻断：Agent `repository.go` 的 domain import；Capture 的 `TransactionWriter` / `ModelRunTxFinalizer`；ChangeControl / Knowledge 的 `eventsapplication.Appender`；ChangeControl 的 `CancellationSafetyGuard`。因此改用**原有文件集**执行同一测试及全部断言，结果如上。主会话需在其他 owner 收口后重跑完整 migration package 的编译/定向门禁，不能把这里的 file-set 通过等同于 package 已通过。

范围外的 Workspace/Auth 旧构造调用（cmd 与 ChangeControl/Capture 等）已通知主会话；其适配由对应 owner 负责。全仓 allowlist、组合根、API/Worker smoke 和发布审批由主会话统一完成。

没有验证 runtime 对任意 foreign-Pool scope 的归属拒绝；现有 Foundation 契约并不提供该保证。本次依赖同一 Pool 组合和 caller-owned scoped 事务，不能扩写为独立 affinity 校验已通过。

未执行 commit、push、发布或部署。Schema 与依赖无变化；如需回滚，应在 Final 主会话中按 owner 撤销本次清理 diff，并同步恢复其调用方，避免单独恢复旧入口造成混用。
