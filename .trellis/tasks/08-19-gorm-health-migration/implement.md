# Health Repository GORM 迁移实施计划

## 实施顺序

1. [x] 盘点 Health Scan、Schedule、Issue/Read、Detector、Affected-change 与 Collection membership 的旧构造、事务调用方、锁 SQL、错误映射和已有测试入口。
2. [x] 新增共享 Health GORM 依赖、查询/RowsAffected helper 与受控 legacy SQL bridge；从共享 Pool 获取 GORM root/UoW，禁止创建第二连接池。
3. [x] 实现 Scan/Workflow/River/Event scoped 事务路径，保留 idempotency、CAS、SKIP LOCKED、lease、retry、cancel、deadline、response-loss recovery 和数据库时间语义。
4. [x] 实现 Schedule 与 affected-change dispatcher 的短事务 claim/ack/release/poison 路径；保持 Collection 条件和 workspace 锁顺序。
5. [x] 实现 Issue/Detector/Read GORM 读写及分页/历史聚合，补齐 Smart Collection scoped binding verifier；不改 Application/Domain 契约。
6. [x] 运行受影响包 `go test`、`go test -race`、`go vet`、integration compile-only、真实 Testcontainers PostgreSQL、`gofmt`、`git diff --check`、`task.py validate`；保留跨模块 fixture 阻断证据。
7. [x] 做 Go/SQL/Trellis review，修复当前范围内缺陷；回填验证记录、legacy 清单、TODO 9 与回滚边界，保持 child `in_progress`。

## 验证命令

```bash
gofmt -w internal/health/adapter/postgres internal/health/adapter/collection
go test -timeout=60s ./internal/health/...
go test -race -timeout=60s ./internal/health/...
go vet ./internal/health/...
go test -run '^$' -tags=integration ./internal/health/adapter/postgres
git diff --check
python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-health-migration
```

真实 PostgreSQL 门禁使用 `internal/platform/testdb` 的共享 Testcontainers 工厂执行；不配置外部 DSN 不再阻断此 child 的 factory 覆盖。仅依赖连接级 pgx tracer 的旧测试保留原状；本 child 不为其复制连接池生命周期或修改共享测试基础设施。

## 验证记录

- `gofmt -d`、`GOFLAGS=-mod=mod go test -count=1 -timeout=60s ./internal/health/...`、`GOFLAGS=-mod=mod go test -race -count=1 -timeout=60s ./internal/health/...`、`GOFLAGS=-mod=mod go vet ./internal/health/...`：通过。
- 工作树默认 vendor 模式曾报告 `vendor/modules.txt` 与 `go.mod` 不一致；按现有仓库可用方式以 `GOFLAGS=-mod=mod` 重跑上述 Go 门禁，结果通过，未修改 vendor 或依赖文件。
- `GOFLAGS=-mod=mod go test -run '^$' -tags='integration testcontainers' ./internal/health/adapter/postgres`：Testcontainers integration 编译通过。
- `GOFLAGS=-mod=mod go test -tags='integration testcontainers' -run '^(TestGORMHealthAdaptersUseOneRealPostgresPool|TestHealthScanRepositoryRuntimeReplayAdvanceFinishAndCancellation|TestHealthScheduleRepositoryClaimsOnceReclaimsSameDueAndAcknowledges|TestIssueRepositoryObservationReopenDecisionAndHistory|TestIssueRepositoryConcurrentDecisionReplaysWinner|TestIssueRepositoryResolveMissingFinalizesOnlyAutoResolvableStatusesAndCounters)$' -count=1 -p 1 -timeout 120s ./internal/health/adapter/postgres`：真实 PostgreSQL 通过（37.684s）。
- `GOFLAGS=-mod=mod go test -tags='integration testcontainers' -run '^TestAffectedChangeDispatcher' -count=1 -p 1 -timeout 120s ./internal/health/adapter/postgres`：真实 PostgreSQL 通过（42.668s），覆盖并发 claim、失败回滚重启、active-workspace defer、schema/source poison、commit response-loss 和 lexical degradation。
- `GOFLAGS=-mod=mod go test -tags='integration testcontainers' -run '^TestHealthScan' -count=1 -p 1 -timeout 120s ./internal/health/adapter/postgres`：真实 PostgreSQL 通过（55.931s），覆盖 River worker/replay、response-loss、取消、retry/exhaustion 与 checkpoint。
- `GOFLAGS=-mod=mod go test -tags='integration testcontainers' -run '^(TestFactReaderCancellationReleasesBlockedConnection|TestReadRepositoryHealthTrendAggregatesSevenUTCDaysFromTerminalScans)$' -count=1 -p 1 -timeout 120s ./internal/health/adapter/postgres`：真实 PostgreSQL 通过（15.408s）；取消/连接释放与趋势聚合均复用共享 Pool。
- `python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-health-migration`：通过；仅有大 spec 文件上下文注入警告。
- Go/SQL/Trellis review：通过；修复 GORM Exec 丢失 `RowsAffected` command tag、scoped 依赖 typed-nil，以及 GORM Issue missing-set 回查没有使用 scoped verifier 三个边界问题，未发现新的 Critical/Required 问题。
- SQL review：新适配器只使用参数化 legacy SQL 与共享 GORM root/UoW；未新增动态标识符、第二连接池或 Schema mutation，真实执行计划与锁竞争仍待 DSN 门禁。
- 单独运行 affected-change、Scan、Schedule、Issue 与 GORM composition 的 Testcontainers 门禁均通过。组合运行 `TestFactReaderEvidenceDetectorsDeduplicateAndPageByTypedKey` 与 `TestTopicScopeReaderAndMissingSetUseConfirmedMembership` 时失败：`internal/graph/testfixture.SeedFunctional` 写入 `workspace.status='test'`，违反当前迁移 `workspace_status_lifecycle` 的约束（SQLSTATE `23514`）。失败发生在 Health 测试断言前；Graph fixture 不在本 child 范围，故保留为跨 owner 阻断。
- `issue_repository_batch_integration_test.go` 与 `read_repository_integration_test.go` 中仍有依赖 pgx connection tracer 的固定语句数/并发屏障测试；共享 factory 未暴露 tracer 注入，改造需要共享 `internal/platform/testdb` 设计变更，超出本 child 范围。

## Legacy 与回滚边界

- 本 child 新增的 GORM 适配器通过包内 `pgx.Tx` 兼容桥调用现有 Health SQL repository；Collection 新路径通过 opaque `foundation.TransactionScope` 的 scoped verifier 复核 durable binding。生产 Composition、cmd、Schema、legacy 删除仍由 Final 负责。
- `internal/health/adapter/collection/legacy_membership.go` 明确保留旧 pgx durable binding bridge；`membership.go` 的 staged GORM 路径不再直接引用 Collection PostgreSQL adapter 或 `pgx.Tx`。
- 回滚仅需移除本 child 新增的四个 staged GORM 文件及对应 Trellis 记录；不回滚共享 GORM 基础设施、其他 owner staged Port、Schema 或 legacy 实现。

## 回滚点

- 依赖/Model/helper 阶段失败：删除本 child 新增 staged 文件即可，legacy 路径不变。
- scoped Scan/Schedule/Issue 实现阶段失败：仅 revert Health GORM Adapter 变更，不回滚数据库结构或其他 owner 的 staged Port。
- Final 尚未切换 Composition，故本 child 的 staged GORM 实现不会改变生产路径。
