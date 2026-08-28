# Export GORM 迁移执行清单

- [x] 读取父任务、Export contract、数据库/错误/质量规范及 Foundation/Workflow/Events/Audit GORM 约定。
- [x] 新增显式 staged GORM Repository 与事务/side-fact helpers，不修改 legacy/Composition。
- [x] 运行 `gofmt`、`go test ./internal/export/...`、`go vet ./internal/export/...`、受影响集成 compile-only、`git diff --check`。
- [x] 真实 PostgreSQL DSN 不可用时，集成门禁仅完成 compile-only；TODO 9 生产切换与真实 side-fact 原子性仍待 Final。
- [x] 运行 GORM/pgx allowlist、AutoMigrate/Migrator 静态检查；记录真实 PostgreSQL DSN/TODO 9 门禁缺失。
- [x] 完成 Go/SQL 维度自检并回填 child 验证记录；保留 TODO 9、TODO 3、Final Composition 和 legacy 删除边界。

## 验证记录

- `go test -race -count=1 -timeout=60s ./internal/export/...`：通过。
- `go test -tags=integration -run '^$' ./internal/export/adapter/postgres`：仅编译，通过；未设置 `ZHIXU_TEST_DATABASE_URL`，真实 PostgreSQL 测试未执行。
- `go vet ./internal/export/...`、`gofmt`、`git diff --check`：通过。
- `rg 'AutoMigrate|\.Migrator\(' internal/export cmd --glob '*.go'`：无命中。
- Export Adapter 的 pgx 生产残留仍位于 legacy 文件和 GORM bridge；Final 前不得删除，River dispatcher/生产 Composition 不在本 child。
- Go/SQL review：检查 UnitOfWork bridge 的取消/deadline/`sql.ErrTxDone` 错误链、rows/连接释放、参数化 SQL、锁/CAS、分页和 side-fact 同事务；补齐 Begin 未进入 callback 时的 ready 失败信号。

## 回滚点

删除本 child 新增的 `gorm_*.go` 即可恢复 staged 状态；生产仍引用 legacy `Repository`，无需数据库回滚。
