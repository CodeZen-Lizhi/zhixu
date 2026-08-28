# Memory GORM 静态验证记录

## 范围

本次只新增 staged `internal/memory/adapter/postgres` GORM 路径，并将
`scanPersistedCommand` 抽为 pgx/GORM 共用 scanner。生产 Composition 保持 legacy
`NewRepository`：API `cmd/api/main.go:1166`、Worker `cmd/worker/main.go:1557`。
未修改 migration、`cmd/**` 或新增测试文件。

## 实现核对

- `GORMRepository` 覆盖 application Repository 的七个方法，构造器拒绝 nil/零值/已有错误的 GORM root 与 typed-nil Unit of Work。
- `FindCommand`、`CreateCandidate`、`Mutate`、`ExpireDue` 经 Foundation `UnitOfWork.Within`，统一使用 `foundation.TransactionOptions{}`；平台层只在 adapter 内受控解包 GORM transaction。
- Command advisory lock -> receipt `FOR UPDATE` -> Interview provenance lock -> semantic replay 的顺序与 legacy 一致；workspace/owner 条件、CAS version、audit/receipt 同事务写入均保留。
- Raw SQL 使用固定列清单和参数绑定；List 的 `text[]` 使用 `pq.Array` 单参数，JSONB 使用私有 `driver.Valuer` carrier；没有 ORM association、`gorm.Model`、soft delete、`AutoMigrate` 或 `Migrator`。
- `Raw().Row().Scan`/`Rows()` 路径显式处理 no-row、nil rows、`Close`、`rows.Err()`；共享 domain scanner 继续执行 canonical JSON、时间和领域校验。
- context cancellation/deadline 优先于 `sql.ErrTxDone`；已有 foundation error 保留错误链；SQLSTATE 只按既定精确集合分类（含 `08000/08003/08006`，未扩大为整个 08 类）。

## 验证命令

以下命令在 2026-08-19 通过：

```text
go test -mod=vendor ./internal/memory/... -count=1 -timeout 60s
go test -race -mod=vendor ./internal/memory/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/memory/... ./internal/platform/postgres
go test -mod=vendor -tags=integration -run '^$' ./internal/memory/adapter/postgres -count=1 -timeout 60s
go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker -count=1 -timeout 60s
go list -mod=vendor ./internal/memory/... ./cmd/api ./cmd/worker
go mod verify
python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-memory-migration
gofmt -d internal/memory/adapter/postgres/codec.go internal/memory/adapter/postgres/repository.go internal/memory/adapter/postgres/gorm_model.go internal/memory/adapter/postgres/gorm_queries.go internal/memory/adapter/postgres/gorm_repository.go
git diff --check
```

`go mod tidy -diff` 返回非零，但输出仅为本任务开始前已存在的全仓 `go.sum` 漂移（另含现有 SQLite checksum）；未应用 tidy 输出，也未覆盖用户改动。`go mod verify` 仍通过。

## Review 结果

- Go Review：未发现 P0/P1。静态确认公共 application/domain 不泄漏 GORM/pgx；事务生命周期、错误链、nil/context、锁顺序和 rows 资源路径通过。
- SQL Review：未发现 P0/P1/P2。参数化、workspace/owner 隔离、CAS、append-only/audit guard、keyset、DB clock 和有界 `SKIP LOCKED` 批次通过。
- Trellis Check：任务上下文、实现范围、生产接线和验证命令一致；`task.json.status` 保持 `in_progress`，TODO9/Final 未勾选。

审查保留一个 staged 设计注意项：构造器同时接收 GORM root 和 Unit of Work，接口无法静态证明二者来自同一 `platformpostgres.Pool`。Final 接线必须从同一 Pool 派生两者；本 child 未扩大平台契约来隐藏该问题。`Rows.Close` 的关闭错误沿用 legacy 忽略，需在真实数据库门禁观察连接/游标释放。

## 2026-08-27 TODO 9 真实 PostgreSQL 回归

`repository_integration_test.go` 已改为只通过 `testdb.Require` 取得隔离的
`platformpostgres.Pool`。每个既有顶层契约场景只申请一次 migrated Testcontainers
fixture；其中 `legacy-pgx` 和 `gorm` 子场景依次从同一个 Pool 构造 legacy pgx Repository、
GORM root 与 Unit of Work，并用分支 UUID/Workspace 命名空间隔离 seed。测试文件不再解析
DSN、创建数据库、执行迁移或管理容器/Pool 生命周期。

通过的实库门禁：

- 三个既有顶层契约场景均只使用一个 shared Pool，并在其中按 legacy/GORM 成对执行：生命周期、exact receipt replay、Workspace/owner/task scope、Interview 双层幂等与并发精确一次。
- `List` 以非空 type/status PostgreSQL array 参数验证 keyset `Limit+1`；no-row、取消和 deadline 保留稳定 Memory 错误链。
- audit 和 receipt 的 append-only trigger 分别返回 SQLSTATE `55000`；锁住 audit 或 receipt 写表直到 deadline 时，Candidate 聚合、audit 与 receipt 都回滚到初始版本/计数。
- 直接 stale Mutation record 返回 `MEMORY_VERSION_CONFLICT`；并发 `ExpireDue(2)` 总计仅处理四条到期记录，每条恰有一条 `EXPIRED` audit，重复 sweep 为零。
- 同一 `platformpostgres.Pool` 派生的 GORM UoW 未提交 Workspace 写入对 pgx `DB()` 不可见、提交后可见，证明两个 facade 的事务隔离保持正确。

真实命令与结果：

```text
go test -race -mod=vendor -tags=integration -count=1 -p 1 -timeout 240s ./internal/memory/adapter/postgres
# PASS, 21.092s

go test -mod=vendor ./internal/memory/... -count=1 -timeout 60s
go test -race -mod=vendor ./internal/memory/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/memory/... ./internal/platform/postgres
python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-memory-migration
git diff --check
# all PASS
```

首次全量迁移 fixture 运行发现旧 `core.workspace.status='test'` 已不符合当前
`workspace_status_lifecycle`（只允许 `active|inactive`），且存在单活跃 Workspace 约束。
Memory fixture 因此改为 `inactive`。将 legacy/GORM 放入同一 fixture 后，首次实库回归还
暴露静态 Workspace name/root path 触发 `workspace_root_path_key`；seed 和共享-Pool 可见性
探针均改为追加分支 UUID，重跑通过。这是测试数据命名空间与当前迁移契约的必要对齐，不改变
Schema 或生产行为。

## Review 结果（2026-08-27）

- Go Review：通过。复核了 shared Pool 来源、context cancellation/deadline 错误链、goroutine/WaitGroup 退出、表锁回滚、CAS 与 `SKIP LOCKED` 并发结果；未发现 P0/P1/P2。
- SQL Review：通过。测试 SQL 均为固定语句；唯一的表名选择来自本地 audit/receipt 常量集。Workspace/owner predicate、参数绑定、append-only SQLSTATE、事务隔离、keyset/array、DB time 和有界批次均有实库断言；未新增动态 SQL、迁移或 N+1。
- Trellis Check：通过。PRD/Design/Implement 与实际范围一致，task validation 通过（仅有超大 spec context 注入截断警告），生产 Composition/legacy 删除仍明确留给 Final child。

## Staged Boundary

TODO 9 环境和 Memory staged GORM 回归已完成，但没有修改 `cmd/**`、生产 Composition、Schema 或 legacy 实现。child 可按本轮委派归档；Final child 继续拥有实际切换、legacy 删除与 TODO 3 收口。
