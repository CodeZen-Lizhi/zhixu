# Health Repository GORM 迁移实施计划

## 实施顺序

1. [x] 盘点 Health Scan、Schedule、Issue/Read、Detector、Affected-change 与 Collection membership 的旧构造、事务调用方、锁 SQL、错误映射和已有测试入口。
2. [x] 新增共享 Health GORM 依赖、查询/RowsAffected helper 与受控 legacy SQL bridge；从共享 Pool 获取 GORM root/UoW，禁止创建第二连接池。
3. [x] 实现 Scan/Workflow/River/Event scoped 事务路径，保留 idempotency、CAS、SKIP LOCKED、lease、retry、cancel、deadline、response-loss recovery 和数据库时间语义。
4. [x] 实现 Schedule 与 affected-change dispatcher 的短事务 claim/ack/release/poison 路径；保持 Collection 条件和 workspace 锁顺序。
5. [x] 实现 Issue/Detector/Read GORM 读写及分页/历史聚合，补齐 Smart Collection scoped binding verifier；不改 Application/Domain 契约。
6. [x] 运行受影响包 `go test`、`go test -race`、`go vet`、integration compile-only、真实 Testcontainers PostgreSQL、`gofmt`、`git diff --check`、`task.py validate`；闭环 Collection fixture 与固定语句数验证。
7. [x] 做 Go/SQL/Trellis review，修复当前范围内缺陷；回填验证记录、legacy 清单、TODO 9 与回滚边界并准备归档。

## 验证命令

```bash
gofmt -w internal/health/adapter/postgres internal/health/adapter/collection
go test -mod=vendor -count=1 -timeout=60s ./internal/health/...
go test -mod=vendor -race -count=1 -timeout=60s ./internal/health/...
go vet -mod=vendor ./internal/health/...
go test -mod=vendor -run '^$' -tags='integration testcontainers' ./internal/health/adapter/postgres
go mod tidy -diff
go mod verify
go list -mod=vendor ./internal/health/...
go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker
git diff --check
make task-context-check
python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-health-migration
```

真实 PostgreSQL 门禁使用 `internal/platform/testdb` 的共享 Testcontainers 工厂执行。为遵守单条后端测试 60 秒上限，28 个顶层数据库用例按用例或小组串行执行；未创建外部 DSN、第二连接池或独立容器生命周期。

## 验证记录

- `gofmt`、`go test -mod=vendor -count=1 -timeout=60s ./internal/health/...`、`go test -mod=vendor -race -count=1 -timeout=60s ./internal/health/...`、`go vet -mod=vendor ./internal/health/...`：通过。
- `go test -mod=vendor -tags='integration testcontainers' -run '^$' ./internal/health/adapter/postgres`、`go list -mod=vendor ./internal/health/...`、`go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker`：通过。
- `go mod tidy -diff` 无输出；`go mod verify` 报告所有模块已验证；未修改模块或 vendor 文件。
- 28 个真实 PostgreSQL 顶层用例均以 `-mod=vendor -tags='integration testcontainers' -race -count=1 -timeout=60s` 通过，覆盖 GORM/Collection scoped composition、Affected-change、Fact/Topic、Issue/Detector/Read、Scan/River、Schedule、固定语句数、并发屏障、响应丢失、取消、重试耗尽和 checkpoint。最长用例 `TestHealthScanRiverRetryExhaustion` 为 48.382s。
- Collection 两个原阻断用例已通过：Graph fixture 改用合法 `workspace.status='inactive'`；Health 通过 Collection owner 的 durable binding revision 复核，不建立第二事实源。
- 固定语句数与并发屏障测试已通过窄作用域 DB/Tx instrumentation 复用同一共享 Pool；Detector page 保持 7 条固定语句，Read 查询保持 3/2/4 条固定语句，无需修改共享 `testdb`。
- `python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-health-migration`：通过；仅有大 spec 文件上下文注入警告。
- `git diff --check` 与 `make task-context-check`：通过；后者仅有上下文大小警告。活跃任务清单中指向已归档依赖的路径已同步修复。
- Go/SQL/Trellis review：通过；除既有 `RowsAffected`、typed-nil、missing-set verifier 修复外，本轮补充修复 membership cache hit 未先复核 durable revision 的一致性缺陷，并扩展既有测试验证失效缓存淘汰。未发现剩余 P0-P2 问题。
- SQL review：新适配器只使用参数化 SQL 与共享 GORM root/UoW；scope predicate 为静态注册表，未新增动态标识符、第二连接池或 Schema mutation。固定语句数、`SKIP LOCKED`、CAS/租约竞争、keyset 索引计划和 River/Event 原子性均由真实 PostgreSQL 门禁覆盖。

## Legacy 与回滚边界

- 本 child 新增的 GORM 适配器通过共享 Pool 的兼容桥调用现有 Health SQL repository；Collection 新路径通过 opaque `foundation.TransactionScope` 的 scoped verifier 复核 durable binding。生产 Composition、cmd、Schema、legacy 删除仍由 Final 负责。
- `internal/health/adapter/collection/legacy_membership.go` 明确保留旧 pgx durable binding bridge；`membership.go` 的 staged GORM 路径不再直接引用 Collection PostgreSQL adapter 或 `pgx.Tx`。
- 回滚仅需移除本 child 新增的四个 staged GORM 文件及对应 Trellis 记录；不回滚共享 GORM 基础设施、其他 owner staged Port、Schema 或 legacy 实现。

## 回滚点

- 依赖/Model/helper 阶段失败：删除本 child 新增 staged 文件即可，legacy 路径不变。
- scoped Scan/Schedule/Issue 实现阶段失败：仅 revert Health GORM Adapter 变更，不回滚数据库结构或其他 owner 的 staged Port。
- Final 尚未切换 Composition，故本 child 的 staged GORM 实现不会改变生产路径。
