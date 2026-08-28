# Review Interview GORM 静态验证

日期：2026-08-20

## 范围与状态

- 新增 `internal/review/interview/adapter/postgres/gorm_{core,model,helpers,read,commands,completion}.go`。
- `GORMRepository` 只从一个 `*platformpostgres.Pool` 取得 GORM root 与 `UnitOfWork`；所有多语句写入只使用 callback 内的 scoped GORM transaction。
- legacy pgx `Repository`、现有 integration fixture、migration、Application/Domain Port 与 `cmd/**` 未由本 child 修改。
- API 与 Worker 仍分别构造 `interviewpostgres.NewRepository(pool/db)`；没有 selector、双写、fallback 或生产 GORM wiring。
- `task.json.status` 保持 `in_progress`，PRD AC 与 TODO 9 全部未勾选。

## 实现核对

- Read：Question Select 保持单个 set-based 查询、`pq.Array` 单参数、claim ID/evidence 稳定顺序；Get/List/GetPath 保持 Workspace、Interview compatibility view、keyset limit+1、完整 scan/validation、Rows Close/Err。
- Commands：Start、Submit、Path Step/Status 保持 advisory lock、receipt-first replay、row lock/CAS、append-only receipt 与 root response-loss recovery。
- Completion：Begin 只冻结 snapshot；Prepare 冻结单一 digest 并收敛 hold；Complete 在一个 UoW 内核验并释放恰好两个 hold，写 Report/Path/Steps、shell/session CAS、receipt 和 terminal reservation。
- Maintenance：保留一个使用数据库时钟、稳定批次和 `FOR UPDATE SKIP LOCKED` 的 CTE；不新增 Session lock。
- Persistence：JSONB carrier 返回 validated string；UUID/text array 使用 `pq.Array`；无 AutoMigrate/Migrator、`gorm.Model`、Save/Preload/Association、独立 `gorm.Open`、root Transaction 或敏感日志。

## Review 结果

- Go Review 发现并修复 1 个 P1：`List` 的行扫描错误原先直接透出 driver error，现经 `gormInterviewClassify(..., PERSISTENCE_INVALID)` 对齐 legacy 错误契约。
- SQL Review 未发现剩余 P0/P1/P2；逐项确认参数顺序/cast、Workspace predicate、compatibility view、锁序、CAS、receipt recovery、两条 hold 与 maintenance CTE。
- Trellis Review 发现并修复 1 个 P2：GORM-only no-row helper 曾为不可能由 `database/sql` 路径返回的 `pgx.ErrNoRows` 保留直接 pgx import；现只识别 `sql.ErrNoRows` / `gorm.ErrRecordNotFound`。文件布局描述也已同步为实际的 owner-colocated fixed SQL + shared helpers。

## 已通过门禁

```text
go test -mod=vendor ./internal/review/interview/... -count=1 -timeout 60s
go test -race -mod=vendor ./internal/review/interview/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/review/interview/...
go test -mod=vendor -tags=integration -run '^$' ./internal/review/interview/adapter/postgres -count=1 -timeout 60s
go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker -count=1 -timeout 60s
go list -mod=vendor ./internal/review/interview/... ./cmd/api ./cmd/worker
go mod verify
python3 ./.trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-review-interview-migration
gofmt -d internal/review/interview/adapter/postgres/gorm_*.go
git diff --check
```

Trellis validate 只有既有大规格文件超过 context injection 限制的截断 warning，两个 manifest 均验证通过。静态扫描确认无被禁 ORM API、独立连接、动态用户 SQL或日志调用。

## 只读依赖检查

`go mod tidy -diff` 返回 1，仅展示当前共享工作树开始前已经存在的大范围 `go.sum` 规范化差异，并建议补充 sqlite checksum；未应用、未修改或回滚 `go.mod` / `go.sum` / vendor。`go mod verify` 与 vendor 模式编译均通过。

Review Core、Interview、Learning Path 三个 child 的联合静态门禁也通过：

```text
go test -mod=vendor ./internal/review/... -count=1 -timeout 60s
go test -race -mod=vendor ./internal/review/... -count=1 -timeout 60s
go vet -mod=vendor ./internal/review/...
go test -mod=vendor -tags=integration -run '^$' ./internal/review/adapter/postgres ./internal/review/interview/adapter/postgres ./internal/review/learningpath/adapter/postgres -count=1 -timeout 60s
```

该结果只证明编译、现有测试与静态契约兼容，不替代三个 owner 的真实 PostgreSQL 共享 Schema/trigger/并发门禁。

## TODO 9 阻断

当前 `ZHIXU_TEST_DATABASE_URL` 未配置，以下不能由 compile/unit 证据替代：

- GORM `?::uuid`、JSONB string Valuer、`pq.Array` 与 updatable Interview Path view/trigger 的真实执行；
- advisory/row lock、并发 exact replay、CAS、commit response-loss、rollback 与连接释放；
- Begin/Prepare/bridge/Complete 的两个 hold、digest、ABANDONED/ORPHANED 和 late completion；
- SQLSTATE/constraint、cancel/deadline/custom cause、坏数据 fail-closed；
- Select/List/Completion/maintenance 的真实执行计划、statement count 与索引使用。

因此本 child 只能视为 staged 静态实现，不可切生产、删除 legacy、勾选 AC、标记完成或归档。
