# Export GORM 迁移执行清单

- [x] 读取父任务、Export contract、数据库/错误/质量规范及 Foundation/Workflow/Events/Audit GORM 约定。
- [x] 抽取单一显式 SQL 核心，以私有 driver-neutral 接口承载 legacy pgx 与 GORM/UoW 两条适配路径。
- [x] 新增强类型 staged `GORMRepository`，接入 scoped Event/Audit，并保持 Event 可选、Download Audit 缺失时 fail-closed 的既有语义。
- [x] 新增 staged `GORMDispatcher`，通过 Workflow 官方 scoped `database/sql` inserter 将围栏与 River insert 放在同一 UoW。
- [x] 使用真实 PostgreSQL/Testcontainers 完成 legacy/GORM 双实现与 pgx Worker 定向回归，补齐 post-commit、取消、坏数据和真实生产 SQL EXPLAIN 证据。
- [x] 完成 Go、SQL、并发、性能和质量审查；独立复审未发现 P0-P2，四项初审意见均已关闭。
- [x] 保持 `cmd/**`、生产 Composition、Schema、依赖和 legacy 删除不变；最终切换继续由 Final child 负责。

## 验证记录

- `go test -mod=vendor -race -count=1 -timeout=60s ./internal/export/... ./internal/events/... ./internal/audit/...`：通过。
- `go test -mod=vendor ./internal/workflow/adapter/river ./internal/platform/postgres`、`go vet` 受影响包：通过。
- `go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker` 与 Export integration compile-only：通过。
- 12 组定向真实 PostgreSQL 用例按单项拆分在 60 秒门禁内通过；覆盖项和命令见 `research/static-validation.md`。
- 生命周期双实现和 GORM optional Event 的 `-race -count=3` 已通过；附件能力在当前 Testcontainers fixture 上完成 `-race -count=1`。
- `go mod tidy -diff` 无输出，`go mod verify` 返回 `all modules verified`；未改动 `go.mod`、`go.sum` 或 vendor。
- GORM/pgx allowlist、`AutoMigrate`/Migrator、公共 `any`、生产 wiring 与 `git diff --check` 静态门禁通过。
- 未执行单条全量 integration 命令：每个变体独立创建并迁移 Atlas 临时库，合并运行会超过项目规定的 60 秒后端测试预算；已按规范拆分为定向用例。

## 回滚点

回滚本 child 的 GORM Adapter、共享私有适配层和对应测试即可；生产仍引用 legacy 实现，没有数据库、依赖或部署回滚步骤。
