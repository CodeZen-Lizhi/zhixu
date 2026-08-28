# Audit Store GORM 迁移实施清单

## 1. 规划与基线

- [x] 核对父任务顺序：Audit 位于 Wave 1，仅依赖 Foundation，后续 Export/Model Settings/Agent/Tools 等依赖其 scoped Port。
- [x] 读取 Audit Domain/Application/PostgreSQL、生产/测试构造点、跨模块事务调用方与 `00034`/`00081`/`00089` 契约。
- [x] 固定脱敏、advisory lock、精确 replay、重复损坏、append-only、Get/List 与错误矩阵。
- [x] 冻结 staged 边界：不改生产 Composition、不删 pgx、不改 migration；真实门禁只原位修改既有 integration fixture。
- [x] 用户已审批 TODO 10 按模块开发与本 child 的 staged 边界。
- [x] 运行 `task.py start`，确认 child 为 `in_progress` 后再修改 Go 源码。

## 2. Scoped Application Port

- [x] 保留 legacy `Appender.AppendTx(any)`/`Recorder.RecordTx`，新增 `ScopedAppender.AppendScoped(TransactionScope)` 与 `ScopedStore`。
- [x] 新增 `Recorder.RecordScoped`，复用同一脱敏与 persisted binding 校验，不复制业务规则。
- [x] 新增 `ScopedReader.GetScoped` / `Recorder.ReadScoped`，只在 caller scope 读取不可变 Audit 事实，供后续 owner 做 durable closure 验证。
- [x] 静态确认新公开 Port 不包含 GORM、`database/sql`、pgx 或 `any`，旧 Port 明确留给未迁移 owner。

## 3. Staged GORM Store

- [x] 新增 `GORMStore` / `NewGORMStore(*platformpostgres.Pool)`，由同一 Pool 派生 GORM root/UoW，并拒绝 nil/失效依赖与 nil context。
- [x] `Append` 通过 Store 自有共享 UoW；`AppendScoped` 经平台受控 unwrap 使用调用方 scope 的同一事务且不自行提交/回滚。
- [x] `GetScoped` 经同一受控 unwrap 读取 caller scope 中的不可变事实，不走 root 连接或开启嵌套事务。
- [x] 实现 advisory lock、精确 lookup、append returning/重读，保持全局 `NULL` Workspace 并发幂等与重复行 fail-closed。
- [x] 实现 Get/List 的 Workspace 精确隔离、tuple keyset、固定排序、显式 Limit 与 Rows 生命周期。
- [x] List/幂等读取显式传播 `Rows.Close` 错误，并与 `Rows.Err`/扫描错误合并分类。
- [x] 新增 JSONB 单参数 carrier、GORM `?` Raw SQL 与共享 no-row/error helper；不映射 `transaction_id`。
- [x] 确认无 AutoMigrate/Migrator、soft delete、association、Hook、第二 pool、双写/fallback；生产仍构造 `NewStore/NewRepository`。

## 4. 局部验证

- [x] `go test -mod=vendor ./internal/audit/... -count=1 -timeout 60s`
- [x] `go test -race -mod=vendor ./internal/audit/... -count=1 -timeout 60s`
- [x] `go vet -mod=vendor ./internal/audit/... ./internal/platform/postgres`
- [x] `go test -mod=vendor -tags=integration -run '^$' ./internal/audit/adapter/postgres -count=1 -timeout 60s`
- [x] `go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker ./cmd/workspacectl -count=1 -timeout 60s`
- [x] `go list -mod=vendor ./internal/audit/... ./cmd/api ./cmd/worker ./cmd/workspacectl`
- [x] `go mod verify`
- [x] `go mod tidy -diff`；若只显示任务前已有 `go.sum` 漂移，记录而不应用。
- [x] 静态检查 Domain 无数据库实现泄漏，新 Application Port 仅依赖 foundation scope，旧 `any` 调用点未被误接。
- [x] 静态检查无 `AutoMigrate`/`Migrator`，所有 `cmd/**` 及跨模块调用仍使用 legacy Audit Store/Port。
- [x] `python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-audit-migration`
- [x] `git diff --check`

integration `-run '^$'` 只验证测试编译，不作为真实 PostgreSQL 证据。预计超过 60 秒的命令停止扩大范围并记录盲区。

## 5. Review

- [x] 使用 `go-review` 检查 opaque scope、context、typed nil、UoW callback、Rows 生命周期、错误链和接口兼容。
- [x] 使用 `sql-code-review` 检查参数化、advisory lock、`NULL` 唯一语义、JSONB carrier、keyset、trigger、延迟约束和事务原子性。
- [x] 使用 `trellis-check` 检查 PRD/Design、production wiring、依赖 owner、验证证据和 TODO 9 状态。
- [x] 修复范围内明确问题并重跑对应门禁；结果记录到 `research/static-validation.md`。

## 6. TODO 9 真实 PostgreSQL 门禁


- [x] 原位扩展现有 integration test，通过同一 `platformpostgres.Pool` 构造 legacy DB、GORM root 与 Unit of Work；legacy/GORM 分别取得隔离 Testcontainers fixture。
- [x] 在 fixture 中证明 Audit Store 和 caller Unit of Work 来自同一 Pool；当前 Foundation
	  无 Pool identity，不能把 active foreign scope 运行时拒绝登记为已验证能力。
- [x] legacy/GORM 比较全局/Workspace 首写、精确 replay、binding conflict、12 并发单行和重复损坏 fail-closed。
- [x] 比较 Get/List、nullable、同微秒 cursor、JSONB/脱敏/corrupt envelope、no-row 与稳定错误码。
- [x] 验证 UoW commit/rollback、取消/超时、过期 scope、SQLSTATE 和连接释放。
- [x] 验证 Audit 的 `transaction_id` 在 caller UoW 中等于 `pg_current_xact_id()`；Workspace rebind history 和 `00089` refusal 的 owner-fact 同事务闭环仍分别由 Workspace、Tools 与 Final 的跨模块测试拥有，不能在 Audit child 伪造 owner 写入。
- [x] 验证 UPDATE/DELETE 继续被 `55000` 拒绝，List 使用目标索引且结果规模受 `LIMIT` 约束。
- [x] TODO 9 Audit-owned 门禁通过，PRD AC 已勾选并记录 Final handoff；Composition 切换与 legacy 删除仍由 Final 执行。

## 7. 回滚点

- TODO 9 前：删除 staged GORM/scoped Port 文件并还原共享 helper；生产行为和 Schema 不变。
- TODO 9 后、Final 前：继续保留 legacy 构造即可回退 staged 就绪状态。
- Final 切换失败：由 Final child 统一回退 Audit Composition；禁止双写、fallback 或另开事务掩盖差异。

证据见 [`research/static-validation.md`](research/static-validation.md)，Final 构造与回滚约束见 [`final-handoff.md`](final-handoff.md)。
