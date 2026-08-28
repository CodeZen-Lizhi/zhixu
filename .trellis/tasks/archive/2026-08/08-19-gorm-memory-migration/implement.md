# Memory GORM 迁移执行计划

## 0. Phase Gate

- [x] 父任务已批准“每个模块一个任务”与 TODO 9 前可开发但不可完成/切生产。
- [x] Foundation 已提供共享 `Pool.GORM()` 和显式 GORM transaction 基础。
- [x] 已盘点 Memory 七个端口、Schema/触发器、锁顺序、调用点和现有测试。
- [x] 向用户展示本 child 最终 PRD/Design/Implement 摘要后，获得新的明确开发批准。
- [x] 批准后运行 `task.py start` 并确认状态为 `in_progress`；未批准前不修改 Go 产品代码。

## 1. Adapter Skeleton And Shared Mapping

- [x] 新增 `GORMRepository` / `NewGORMRepository`，校验 nil/zero/error GORM root 与 nil/typed-nil Unit of Work，并断言 `application.Repository`。
- [x] 新增 GORM SQL 常量，与 legacy 显式列表保持一致；不改 legacy SQL 除非为了无行为变化的共享校验/映射。
- [x] 定义 Adapter 私有 JSONB/array carrier 和必要 insert record，显式列/表名/时间设置，无 `gorm.Model`、association、hook 或 soft delete。
- [x] 复用 strict receipt codec、canonical content、Memory scanner/validator 和 equality helper，确保 pgx/GORM 不产生第二套领域映射规则。

## 2. Commands And Mutation Transactions

- [x] 实现 `FindCommand`：Foundation Unit of Work + command advisory lock + locked receipt lookup + exact binding/snapshot replay。
- [x] 实现 `CreateCandidate`：锁顺序、exact replay、Interview semantic replay、workspace lock、Memory/audit/receipt 原子写入。
- [x] 实现 `Mutate`：owner-scoped `FOR UPDATE`、snapshot/expected-version 复核、DB time 校验、CAS `RETURNING`、audit/receipt 原子写入。
- [x] `FindCommand/CreateCandidate/Mutate/ExpireDue` 统一使用 `foundation.TransactionOptions{}`（默认隔离、非只读）；`Get/List/LoadEffective` 不额外建事务。
- [x] 逐项复核 callback error、begin/commit/rollback error、`sql.ErrTxDone`、no-row 与 SQLSTATE；分类先保留 foundation error，再以原始 `ctx.Err()` 恢复取消/deadline cause。

## 3. Queries And Maintenance

- [x] 实现 `Get`，保持 workspace/owner 越权隐藏和 NotFound。
- [x] 实现 `List`，保持 PostgreSQL array 单参数、tuple keyset、Limit+1 和最后项 cursor。
- [x] 实现 `LoadEffective`，保持 SQL 根过滤、DB clock、global/task scope 与稳定顺序。
- [x] 实现 `ExpireDue`，保持同一事务的 DB time、有界 `SKIP LOCKED`、逐项 CAS/audit 和整批回滚。
- [x] 检查所有 `*sql.Rows` 的 nil/error/Close/Err 和所有 Raw no-row 路径。

## 4. Static Verification

- [x] `go test -mod=vendor ./internal/memory/... -count=1 -timeout 60s`
- [x] `go test -race -mod=vendor ./internal/memory/... -count=1 -timeout 60s`
- [x] `go vet -mod=vendor ./internal/memory/... ./internal/platform/postgres`
- [x] `go test -mod=vendor -tags=integration -run '^$' ./internal/memory/adapter/postgres -count=1 -timeout 60s`
- [x] `go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker -count=1 -timeout 60s`
- [x] `go list -mod=vendor ./internal/memory/... ./cmd/api ./cmd/worker` 与 `go mod verify`。
- [x] 运行 `go mod tidy -diff`；仅命中预存 `go.sum` 漂移，未应用/覆盖。
- [x] 扫描 Adapter/Domain/Application pgx/GORM import、`AutoMigrate|Migrator`、生产 `NewRepository` wiring 和日志敏感参数。
- [x] 运行 task validate、`gofmt -d` 与 `git diff --check`。

## 5. Required Review

- [x] Go Review：公共契约、nil/context/error chain、transaction lifecycle、scanner/resource 关闭、并发锁顺序。
- [x] SQL Review：参数化、workspace/owner 隔离、CAS、advisory/row lock、append-only/audit guard、keyset/index、DB time、N+1/有界批次。
- [x] Trellis Check：PRD/Design/Implement 合规、修改范围、验证证据、TODO 9/Final 门禁和 task 状态。
- [x] 将发现与修复写入 `research/static-validation.md`；任何 P0/P1/P2 修复后重跑受影响门禁。

## 6. TODO 9 PostgreSQL Gate

- [x] 在现有 `repository_integration_test.go` 中通过 `testdb.Require` 的临时 migration/runtime Pool，从同一 Pool 取 `DB()` / `GORM()` / `UnitOfWork()` 构造 legacy 与 staged Repository；未新建测试文件或复制生命周期。
- [x] 每个既有顶层场景只使用一个 migrated Testcontainers fixture；legacy/GORM 依次从同一 Pool 执行，并以 UUID/Workspace seed 命名空间隔离生命周期、receipt/audit、effective scope、并发 Confirm 和 Interview 双层幂等等价场景。
- [x] 补齐 audit/receipt 失败回滚、append-only 拒绝、CAS/no-row/SQLSTATE、context cancel/deadline 与共享 Pool 事务可见性。
- [x] 补齐并发 `ExpireDue` 的 `SKIP LOCKED`、计数、审计唯一性和无重复处理。
- [x] 运行 `go test -race -mod=vendor -tags=integration -count=1 -p 1 -timeout 240s ./internal/memory/adapter/postgres`（2026-08-27，21.092s）。

## 7. Completion And Rollback

- [x] TODO 9 真实 PostgreSQL 证据已回填；PRD AC 已完成，child 可归档，legacy 生产接线保持不变。
- [x] 已记录 Final 切换/删除边界；本 child 未自行修改 `cmd/**`。
- [x] 回滚边界是本 child 新增 GORM Adapter/共享 helper；不回滚 Schema、不删除历史数据。
