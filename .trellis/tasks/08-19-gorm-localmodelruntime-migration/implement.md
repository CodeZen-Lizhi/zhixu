# Local Model Runtime GORM 迁移实施清单

## 1. 规划与基线

- [x] 读取 PRD、父设计、backend database/error/quality 与 cross-layer/code-reuse 指南。
- [x] 盘点生命周期 public surface、Model Settings `pgx.Tx` 调用者、独立 runtime command、credential-init 和现有 integration fixture。
- [x] 核对 `00080_managed_ollama_runtime.sql` 表、trigger、SECURITY DEFINER 函数、role grants 与 Down guard。
- [x] 冻结 legacy TxLifecycle 兼容边界：本 child 新增 scoped port，不直接改签或跨事务桥接。
- [x] 完成 Go/SQL/规划审查并把结论写入 research 文件。
- [x] 用户审批复杂设计后运行 `task.py start`；start 前不得改 Go 源码。

## 2. Scoped Port

- [x] 在 `lifecycle_store.go` 新增 `ScopedTxLifecycle`/`WithScope`，签名只包含 context、foundation scope、领域命令/结果。
- [x] 保留 `TxLifecycle`/`WithTx(pgx.Tx)` 并标注 legacy owner/退出条件；不让新 GORM 类型实现 pgx 方法。
- [x] `WithScope` 和每个绑定方法拒绝 nil/typed-nil/stale scope，且不 commit/rollback/fallback；Foundation 当前没有 Pool identity，foreign-pool scope 限制已记录为同池 Composition/TODO9 不变量。

## 3. Staged GORM Store

- [x] 新增 `GORMStore`/`NewGORMStore(*platformpostgres.Pool)`，复用 Pool GORM/UoW，不创建第二池。
- [x] 迁移 manager/demand/runtime/hold/operation/test preparation/probe/recovery 全部 root 能力，保留固定函数调用、Raw SQL、scanner 与业务校验。
- [x] 保留 `ReadDemand` RepeatableRead+ReadOnly 单快照及所有 owned UoW 原子边界。
- [x] 迁移 scoped activation preparation 四个方法，使用 caller-owned scope 内的同一 GORM transaction。
- [x] 抽取 JSONB/time/nullable/no-row/rows-close/context/error helpers；不得重复业务规则或引入 AutoMigrate。
- [x] 生产 `cmd/**`、Model Settings legacy wiring、migration、credential-init/test fixture 保持未改。

## 4. 局部验证

- [x] `go test -mod=vendor ./internal/localmodelruntime/... -count=1 -timeout 60s`
- [x] `go test -race -mod=vendor ./internal/localmodelruntime/... -count=1 -timeout 60s`
- [x] `go vet -mod=vendor ./internal/localmodelruntime/... ./internal/platform/postgres`
- [x] `go test -mod=vendor -tags=integration -run '^$' ./internal/platform/migration ./internal/localmodelruntime/... -count=1 -timeout 60s`
- [x] `go test -mod=vendor -run '^$' ./cmd/local-model-runtime ./cmd/local-model-runtime-credential-init ./internal/modelsettings/adapter/postgres ./internal/modelsettings/runtime -count=1 -timeout 60s`
- [x] `go list -mod=vendor ./internal/localmodelruntime/... ./internal/platform/migration ./cmd/local-model-runtime ./internal/modelsettings/...`
- [x] `go mod verify`；`go mod tidy -diff` 仅记录已有 `go.sum` 漂移，不应用。
- [x] 静态扫描 Domain/Application 不导入 GORM/database/sql/pgx；runtime role SQL 路径仍为固定函数调用；无 AutoMigrate/Migrator/第二 pool/credential-init 改动。
- [x] `python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-localmodelruntime-migration`
- [x] gofmt 与 `git diff --check`

## 5. Review

- [x] 使用 `go-review` 审查 scope 生命周期、context cause、typed nil、UoW、rows/commit/rollback、错误链与安全边界。
- [x] 使用 `sql-code-review` 审查 SECURITY DEFINER 函数调用、参数化、DB time、CAS、锁/快照、JSONB、权限和执行计划。
- [x] 使用 `trellis-check` 核对 PRD/Design、legacy consumer、生产接线和 TODO 9 状态；结果写入 `research/static-validation.md`。

## 6. TODO 9

- [ ] 原位参数化 legacy/GORM integration factory，fixture 使用同一完整 platform Pool。
- [ ] 实测生命周期、lease/clock、CAS、idempotency、probe/hold/operation 回滚、ReadDemand snapshot、scoped commit/rollback、role privileges、SQLSTATE 与 EXPLAIN。
- [ ] TODO 9 全部通过后才勾选 PRD AC；Model Settings/Final 仍负责生产切换和 legacy 删除。

### 6.1 2026-08-27 筛选结果

- [ ] 不满足“只差 fixture 接入”的可执行条件：现有 fixture 位于 `internal/platform/migration` 包，而 shared `internal/platform/testdb` 依赖该包的 Goose runner，直接接入产生 import cycle。
- [ ] 未修改 integration 测试或生产实现；child 保持 `in_progress`，等待后续以外部测试包/公共 migration runner 解决边界后再执行真实 legacy/GORM 门禁。

## 7. 回滚点

- TODO 9 前仅删除 staged GORM/scoped 文件；legacy pgx、Schema、命令和凭据路径不变。
- TODO 9 后保留 legacy 构造作为 Final 回退点；禁止双写或 fallback 掩盖差异。
