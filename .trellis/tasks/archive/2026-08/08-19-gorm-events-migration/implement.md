# Events Store GORM 迁移实施清单

## 1. 规划与基线

- [x] 核对 Events Store/Application/HTTP、Schema/trigger/index、生产/测试构造点和 17 个事务追加调用点。
- [x] 固定 Workspace 水位、逻辑 retention、全局 seq gap、SSE cursor、exact replay、SQLSTATE 与 fail-closed scanner 契约。
- [x] 明确仓库无生产物理 cleanup，ORM child 不新增 DELETE 或 high-water 事实。
- [x] 冻结 TODO 9 前边界：不改生产 Composition、不删 pgx、不改 migration/trigger/测试、不完成或归档。
- [x] 完成 PRD、Design、SQL/Schema 与 Go/调用链研究并通过 Trellis task validate。
- [x] 用户审批本最终方案。
- [x] 运行 `task.py start`，确认 child 为 `in_progress` 后再修改 Go 源码。

## 2. Scoped Application Port

- [x] 保留 legacy `Appender.AppendTx(any)`，新增 `ScopedAppender.AppendScoped(TransactionScope)` 与 `ScopedStore`。
- [x] 静态确认新公开 Port 不包含 GORM、`database/sql`、pgx 或 `any`，legacy Port 继续服务未迁移 owner。
- [x] 确认 GORM Store 不实现 legacy `Appender`，防止 opaque scope 再经 `any` 绕开类型边界。

## 3. Staged GORM Store

- [x] 新增 `GORMStore` / `NewGORMStore(*platformpostgres.Pool)`，只使用共享 Pool 的 GORM root，并拒绝 nil/失效依赖与 nil context。
- [x] 实现 Workspace-scoped watermark、nullable earliest 和有界 ListAfter Raw SQL，保留数据库时间、全局 seq gap、升序和 fail-closed scanner。
- [x] 实现 `AppendScoped`，只 unwrap caller transaction，不另开/提交/回滚/fallback；保留 UTC 微秒、JSONB、24 小时 expiry 与完整 exact replay。
- [x] 抽取共享 SQL/scanner/binding/error helper，避免 legacy/GORM 行为漂移；Rows 必须 Close/Err，no-row 同时覆盖 pgx/database/sql/GORM。
- [x] 保留 SQLSTATE、cancel/deadline/`sql.ErrTxDone` 和安全错误；所有 SQL 参数化且错误/日志不含敏感 payload/DSN/路径。
- [x] 确认无 AutoMigrate/Migrator、Hook、association、soft delete、第二 pool、物理 cleanup、双写/fallback；生产仍构造 `NewStore`。

## 4. 局部验证

- [x] `go test -mod=vendor ./internal/events/... -count=1 -timeout 60s`
- [x] `go test -race -mod=vendor ./internal/events/... -count=1 -timeout 60s`
- [x] `go vet -mod=vendor ./internal/events/... ./internal/platform/postgres`
- [x] `go test -mod=vendor -tags=integration -run '^$' ./internal/events/... -count=1 -timeout 60s`
- [x] `go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker -count=1 -timeout 60s`
- [x] `go list -mod=vendor ./internal/events/... ./cmd/api ./cmd/worker`
- [x] `go mod verify`
- [x] `go mod tidy -diff`；若只显示任务前已有 `go.sum` 漂移，记录而不应用。
- [x] 静态检查 Domain 无数据库实现泄漏，新 Application Port 仅依赖 foundation scope，17 个生产 `AppendTx` 与 7 个 `NewStore` 构造点未被误接。
- [x] 静态检查无 AutoMigrate/Migrator、物理 cleanup 和独立连接池，所有生产入口仍使用 legacy Store/Port。
- [x] `python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-events-migration`
- [x] `gofmt -d`（受影响 Go 文件）与 `git diff --check`

integration `-run '^$'` 只验证测试编译，不作为真实 PostgreSQL 证据。预计超过 60 秒的命令停止扩大范围并记录盲区。

## 5. Review

- [x] 使用 `go-review` 检查 opaque scope、context、typed nil、Raw Row/Rows、资源生命周期、错误链和接口兼容。
- [x] 使用 `sql-code-review` 检查参数化、JSONB、nullable、全局 seq/Workspace scope、retention、exact replay、SQLSTATE、trigger 与 caller-owned transaction。
- [x] 使用 `trellis-check` 检查 PRD/Design、production wiring、owner 依赖、验证证据和 TODO 9 状态。
- [x] 修复范围内明确问题并重跑对应门禁；结果记录到 `research/static-validation.md`。

## 6. TODO 9 真实 PostgreSQL 门禁

- [x] 原位扩展 `store_integration_test.go` 与 `handler_postgres_integration_test.go`，通过同一 `platformpostgres.Pool` 构造 legacy DB、GORM root 与 Unit of Work。
- [x] legacy/GORM 比较 Workspace 交错 seq、watermark、earliest、retained List、空结果、gap、分页和 corrupt row 无 partial。
- [x] 验证 owner 行与 Event 同事务 commit/rollback，nil/非平台实现/stale scope、cancel/deadline、response-loss replay 和连接释放；生产 Composition 仍保持 legacy，后续 owner/Final 从同一平台 Pool 组装。
- [x] 比较 JSONB/optional UUID、UTC 微秒、24 小时 expiry、exact replay、binding conflict 与两个独立事务的并发 claim。
- [x] 验证真实 FK/CHECK/SQLSTATE、trigger producer 回放和 SSE Handler cursor/retention 竞态；Serializable retry 的平台事务行为由 Foundation 实库门禁拥有。
- [x] 新增 `TestEventReplayPlansAtVolumeUseDefaultPlanner`：目标 2,000 事件（低 seq 冷布局）+ 50 噪声 Workspace 各 2,000 事件（共 102,000 行），VACUUM ANALYZE 后默认 planner 下 watermark/earliest/replay 均使用 idx_ops_server_event_workspace_seq（无 Seq Scan），replay 单页结果以 MaxReplayPageSize=100 有界。小数据量强制关顺扫的旧断言保留为索引存在性检查。
- [x] TODO 9 功能、事务、锁、失败与默认 planner 性能门禁全部通过；另修复 `eventAppendRequest` 硬编码 2026-08-27 occurred_at 导致 24h 保留窗口随墙钟过期的定时炸弹。PRD AC 已勾选。Composition 切换、owner 调用点迁移与 legacy 删除仍由对应 child/Final 执行。

## 7. 回滚点

- TODO 9 前：删除 staged GORM/scoped Port 文件并还原本 child 的共享 helper；生产行为和 Schema 不变。
- TODO 9 后、Final 前：继续保留 legacy 构造即可回退 staged 就绪状态。
- Final 切换失败：由 Final child 统一回退 Events Composition；禁止双写、fallback、独立事务或物理 cleanup 掩盖差异。
