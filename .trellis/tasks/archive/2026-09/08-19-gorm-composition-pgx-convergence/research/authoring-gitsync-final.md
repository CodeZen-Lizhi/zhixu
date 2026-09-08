# Authoring / GitSync Final 测试夹具收敛

日期：2026-09-08。范围：主线 Final 分工中的现有测试适配；本分工未修改生产代码、迁移 SQL、依赖或 Schema，也未新增测试文件。

## 修改文件与行为

- `internal/authoring/adapter/postgres/repository_integration_test.go`
- `internal/authoring/adapter/postgres/publication_integration_test.go`
- `internal/authoring/adapter/postgres/hardening_integration_test.go`
- `internal/gitsync/adapter/postgres/repository_integration_test.go`
- `internal/platform/migration/m8_interview_history_hardening_integration_test.go`

Authoring 删除 legacy/GORM 双实现选择器，所有 repository 场景只构造 `NewGORMRepository(platform)`。跨 Authoring / ChangeControl 的发布、提交、恢复夹具统一传递同一个 `*platformpostgres.Pool`；底层 pgx 仅用于原有种子 SQL、状态断言和故障注入。原有 CAS、workspace 隔离、发布闭包、精确重放、SQL 次数和取消断言保留。SQL counter 的 legacy nil 跳过路径删除，类型不符立即失败；取消原因断言改为无条件执行。

GitSync 删除 legacy 构造器、全局 variant ID 切换与 legacy 数据收尾逻辑。单套 GORM fixture 从同一个平台 Pool 取得 GORM 和 UnitOfWork，继续使用既有 credential sealer。七个现有场景的业务断言保留，fixture ID 固定且不再在场景间切换。

M8 现有文件迁至 `migration_test` 包，避免测试辅助包 `testdb` 导入 migration 造成循环。原有测试通过 `testdb.Require` 的自定义迁移回调先到 49，再显式调用 `MigrateAtlasToVersion(..., 50)`；重复 up、版本与历史约束断言保持。Memory 和 Interview 改用最终 GORM 构造器，Memory 的 GORM / UnitOfWork 均来自同一个平台 Pool。复用既有外部测试包的 `assertPostgresCode`。

所有新收敛的 fixture 显式要求 `testdb.FailWhenUnavailable`。测试上下文使用 `t.Context()`；原有 Authoring 60 秒子超时保留。针对历史错误数据的迁移用例保留，其名字中的 Legacy 表示历史数据而非旧 repository 路径。

## 验证命令与结果

普通测试与静态检查通过：

```bash
go test -mod=vendor ./internal/authoring/... ./internal/gitsync/... -count=1 -timeout=60s
go vet -mod=vendor ./internal/authoring/... ./internal/gitsync/...
go test -mod=vendor -tags=integration ./internal/authoring/adapter/postgres ./internal/gitsync/adapter/postgres -run '^$' -count=1 -timeout=60s
```

GitSync 代表用例真实 PostgreSQL 运行通过，52.961 秒：

```bash
go test -mod=vendor -tags=integration ./internal/gitsync/adapter/postgres \
  -run '^(TestRepositoryConfigReplayFencingAndActiveRunUniqueness|TestRepositoryAutoSyncCandidatesRespectCompletionTimeAndRunReplay|TestRepositoryPersistsAttemptsOutboxAndIndependentIndexFailure)$' \
  -count=1 -timeout=60s
```

覆盖配置精确重放与绑定、active run 并发唯一性、auto candidate 完成时间和配置 revision、Run / Attempt / Outbox、独立索引失败与重试，并执行场景内既有索引 EXPLAIN 断言。

Authoring 第一组真实 PostgreSQL 运行通过，14.223 秒：

```bash
go test -mod=vendor -tags=integration ./internal/authoring/adapter/postgres \
  -run '^(TestRepositoryPostgreSQLWorkingDraftCASReplayFreezeAndWorkspaceScope|TestRepositoryPostgreSQLRestorePublicationAppendsRevisionAndReplays)$' \
  -count=1 -timeout=60s
```

覆盖工作草稿 CAS / replay / freeze、workspace 隔离、SQL 次数、ordinal 批量读取、snapshot，以及跨 ChangeControl 的恢复发布和并发恢复重放。

Authoring 第二组真实 PostgreSQL 运行通过，49.660 秒：

```bash
go test -mod=vendor -tags=integration ./internal/authoring/adapter/postgres \
  -run '^(TestRepositoryPostgreSQLListsWorkingAndDocumentDraftsByWorkspaceKeyset|TestGORMRepositoryPostgreSQLCommitFailureAndResponseLoss|TestRepositoryPostgreSQLPublicationFinalizerPublishesAndMarksRecovery)$' \
  -count=1 -timeout=60s
```

覆盖 keyset 分页、SQL 次数、阻塞查询取消原因和连接池复用、提交失败回滚、提交后响应丢失精确重放，以及发布 finalizer / recovery。

M8 以现有文件集运行两个真实 PostgreSQL 用例，通过，27.750 秒：

```bash
go test -mod=vendor -tags=integration \
  internal/platform/migration/m8_interview_history_hardening_integration_test.go \
  internal/platform/migration/managed_ollama_integration_test.go \
  -run '^TestM8InterviewHistoryHardening(MigrationSupportsRepeatedUp|RejectsMutationAndPreservesExactReplay)$' \
  -count=1 -timeout=60s
```

第二个文件仅提供既有共享 assertion，本命令未运行 Managed Ollama 用例。覆盖版本 49→50、重复 up、Memory / Interview command 和 turn 不可变、question 合法状态转移与终态不可变、原始历史 receipt 的精确 replay。

五个修改文件的 `gofmt -l` 无输出；范围内 `git diff --check` 通过。以下扫描无匹配：

```bash
rg -n 'NewRepository|legacy-pgx|withGitSyncRepositories|runAuthoringIntegrationVariants|authoringIntegrationVariant|setGitSyncVariant|finishGitSyncLegacyPath|variant\b' \
  internal/authoring internal/gitsync -g '*_test.go'
```

## Go / SQL Review

按已加载的 `go-review`、`sql-code-review` 对五文件 diff 静态自审，并以上述已有集成场景验证行为。未发现当前适配范围内需要生产修复的缺陷。

- 构造器错误显式失败，不以 skip 或 fallback 掩盖不可用数据库；同一 fixture 的数据库连接、GORM 与事务边界来源一致。
- 跨 owner fixture 的 repository 均使用最终构造器；没有新增裸 pgx repository、事务桥接实现或第二连接池。
- 现有 fixture SQL 保持参数绑定和 workspace 约束；新增版本查询只读取 Atlas revision 元数据。没有新增业务 SQL、迁移、N+1 或无限列表。
- CAS、并发唯一性、取消与连接归还、回滚 / 精确重放、immutable-history 和固定 SQL 次数断言保留；删除的仅为双实现过渡分支。
- GitSync 不再修改全局 variant IDs；Authoring 计数器仍使用 atomic，移除了能静默跳过 GORM 断言的 nil 分支。

## 限制与主线后续

此前运行以下整包编译命令失败：

```bash
go test -mod=vendor -tags=integration ./internal/platform/migration -run '^$' -count=1 -timeout=60s
```

当时的错误位于其他分工文件：`workspace_analysis_capability_integration_test.go` 的 Agent legacy repository 类型；`workspace_analysis_deadline_terminalization_integration_test.go` 的 Tools / Conversation legacy 类型和 WorkspaceAnalysisFinalizer；`workspace_analysis_authorization_repository_integration_test.go` 的 Tools legacy 构造器；`m9_business_contract_hardening_integration_test.go` 的 ChangeControl legacy 构造器。已通知主线；本分工未修改这些文件，也未在文件集通过后宣称 migration 整包通过。

当前只执行了上述代表场景，未扩展到全仓测试、整套 integration 或当前模块的 race。主线应在其他夹具 owner 完成后关闭 migration 整包编译缺口，并按 Final 门禁汇总其余验证。未提交、推送或发布；回滚范围是上述五个测试文件中本分工的夹具适配差异。
