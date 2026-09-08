# Capture / Artifact Final legacy 清理

日期：2026-09-08。执行者：`gorm_health_final`。Health 收口见同目录 `health-final.md`。

## 范围与实现

- 独占修改 `internal/capture/**`、`internal/artifact/**`；额外授权仅覆盖 `internal/platform/migration/capture_repository_integration_test.go` 与 `capture_profile_repository_integration_test.go`。未修改 cmd、其他 migration fixture、Schema 或任务状态。
- Capture 删除旧 `repository.go`、`runtime.go`、`retry.go`、`profile_repository.go`、`profile_retry.go`。共享 receipt、row scanner、Profile frozen binding、完整 evidence closure、retry 校验迁到 `codec.go`、`runtime_codec.go`、`retry_codec.go`、`profile_codec.go`、`profile_retry_codec.go`。
- Artifact 删除旧 `repository.go`、`citation_backfill.go`、`generation.go`、`generation_query.go`、`generation_terminal.go`。共享纯函数迁到 `validation.go`、`citation_backfill_state.go`、`generation_codec.go`、`generation_terminal_state.go`；`codec.go` 只保留纯编解码/校验，删除全部 pgx 查询和写入 helper。
- Capture 错误分类及唯一冲突判断、Artifact 分类及 backfill 数据错误识别统一调用平台 `SQLState`，保持原 code/kind/retryable 映射。NoRows 使用 `database/sql` 与 GORM 错误；vendored pgx 的 ErrNoRows 包装 sql.ErrNoRows，无需业务导入 pgx。
- 保留全部 `NewGORM*` 构造与 scoped 端口；没有新增兼容 facade、第二连接池、业务双写或 owner SQL 副本。Capture Source 写入使用 Workspace scoped writer，Profile 同事务终结 Agent；Artifact 启动与 durable binding 读取使用 Workflow scoped 端口，Generation/Terminal 使用 Agent scoped store。终结 hook 只参与调用方 scope，不提交事务。

## 现有测试夹具迁移

- Capture 两个原 parity factory 收敛为唯一 GORM 组合，保留全部业务用例和断言。Agent/Workspace/Capture 来自同一个平台 Pool。
- Capture 现有 Profile constructor/batch validation 单测使用平台的惰性 Pool 构造，保留成功构造及缺依赖断言；fake Finalizer 改为 scoped。CAS no-row 测试改为 GORM classifier。nil Profile retry repository 对应最终 `CAPTURE_PROFILE_REPOSITORY_UNAVAILABLE` 错误合同。
- Artifact 全部现有 integration fixture 使用 `testdb.Require(FailWhenUnavailable)` 与同一平台 Pool，移除环境未配置时的旧 skip 和重复手工建库/迁移代码。
- Artifact 原 pgx isolation recorder 改为包装真实 UnitOfWork，仍断言 RepeatableRead + ReadOnly。原 commit-response-loss fake 复用现有 GORM UoW wrapper，在真实提交后注入错误；Start 恢复与 Finalize 人工恢复断言不变。
- Artifact 原 selector owner write/repair 测试与 Revision seed 改为 GORM 同事务；pgx 仅保留测试 seed/assert 的明确边界。Workflow fixture 使用正式 `NewStaticScopedEnqueueFence()`，Agent/ChangeControl 均使用 GORM。
- 未新增测试文件，未删除业务断言，未新增 skip/build tag 或用空结果绕过失败。

## Go / SQL Review

- Capture 迁移的 61 个纯 helper 与原文件逐函数原文核对全部一致；Artifact 42 个迁移 helper 仅 `citationBackfillDataError` 改为平台 SQLState，其余一致。
- Capture 83 个、Artifact 54 个 GORM raw literals 与清理前逐项一致，业务 SQL/JSON/SQLSTATE 取值保持原契约。
- 检查了直接构造调用方、共享 scope、回滚/提交所有权、取消 cause、错误分类、JSON/数组参数绑定、workspace predicate、CAS、分页/批量、不可变事实和 side-effect closure。保留既有 advisory lock、SKIP LOCKED、SAVEPOINT 与事务外 evidence verification。
- 生产 Capture/Artifact 无 pgx/pgconn import、`transaction any` 或旧 Tx port；剩余 pgx 文本仅解释底层编码行为的注释。无新增 Schema/DDL、跨 owner 写入或全量无界查询。
- 当前范围没有尚未修复的明确 Go/SQL 缺陷。

## 验证结果

全部以下局部检查通过：

```bash
go test -mod=vendor -timeout=60s ./internal/capture/...
go vet -mod=vendor ./internal/capture/...
go test -mod=vendor -timeout=60s ./internal/artifact/...
go vet -mod=vendor ./internal/artifact/...
go test -mod=vendor -tags='integration testcontainers' -run '^$' -timeout=60s ./internal/artifact/adapter/postgres
git diff --check -- internal/artifact internal/capture internal/platform/migration/capture_repository_integration_test.go internal/platform/migration/capture_profile_repository_integration_test.go
```

Artifact 真实 PostgreSQL，四组全部通过；每条命令明确 60 秒上限：

```bash
go test -mod=vendor -tags='integration testcontainers' -run '^TestSectionGenerationPostgreSQL(ReadSnapshotRecoversAuthoritativeCurrentOutline|RecoversStartAndFinalizeCommitResponseLoss)$' -count=1 -timeout=60s ./internal/artifact/adapter/postgres
# PASS 14.153s

go test -mod=vendor -tags='integration testcontainers' -run '^Test(ArtifactRevisionWritesCitationSelectorsInOwnerTransaction|GORMCitationBackfillPostgreSQLSkipsLockedMarkerAndResumesSavepointFailure)$' -count=1 -timeout=60s ./internal/artifact/adapter/postgres
# PASS 28.794s

go test -mod=vendor -tags='integration testcontainers' -run '^Test(SectionGenerationTerminalHookClosesWorkflowOutcomesAtomically|SectionGenerationTerminalHookErrorRollsBackWorkflowAndGeneration)$' -count=1 -timeout=60s ./internal/artifact/adapter/postgres
# PASS 17.878s

go test -mod=vendor -tags='integration testcontainers' -run '^Test(ArtifactCommandsConcurrentExportAndPublishKeepOneDurableBinding|GORMRepositoryPostgreSQLDocumentSourceProjectionFailsClosed)$' -count=1 -timeout=60s ./internal/artifact/adapter/postgres
# PASS 21.877s
```

## Capture 实库与跨包编译

Capture 使用两个既有 migration_test 文件的完整 fixture 运行代表用例，均为 testdb.Require 的真实 PostgreSQL；没有临时测试代码或自建 admin。

```bash
go test -mod=vendor -tags='integration testcontainers' -run '^TestCaptureRepository(ConcurrentCreateExactReplayAndAtomicFacts|MaterializeURLAtomicRollbackAndStageAdvance)$' -count=1 -timeout=60s internal/platform/migration/capture_repository_integration_test.go internal/platform/migration/capture_profile_repository_integration_test.go
# PASS 14.286s：并发 exact replay、Source/Capture/outbox 原子性、URL materialization rollback/stage advance

go test -mod=vendor -tags='integration testcontainers' -run '^Test(ReadyProfileGeneratorPersistsEvidenceAndReplaysWithoutCallingModelAgain|CaptureProfileRepositoryPreservesContextCauseAndReusesConnection)$' -count=1 -timeout=60s internal/platform/migration/capture_repository_integration_test.go internal/platform/migration/capture_profile_repository_integration_test.go
# PASS 14.280s：Profile revision/evidence/model finalization、精确重放、caller cause 与连接释放

go test -mod=vendor -tags='integration testcontainers' -run '^$' -timeout=60s ./internal/platform/migration
# PASS 0.656s：其他 owner 的旧 fixture 构造清理后，完整 migration integration 测试包恢复可编译
```

本次代表实库与局部 unit/vet 均完成，没有未解决的 Capture/Artifact 编译阻断。其余既有 integration 用例完成编译，未重复执行完整套件；全仓命令接线与门禁由主代理负责。

## 回滚与外部边界

本轮仅提供本地代码与验证证据，没有 commit/push、Schema 迁移发布或真实部署。回滚需恢复业务 Adapter 与其 scoped 构造调用方的同一依赖闭包；数据库版本不变。
