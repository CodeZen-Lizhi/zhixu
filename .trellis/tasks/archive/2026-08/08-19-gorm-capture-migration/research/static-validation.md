# Capture GORM staged 验证

验证日期：2026-08-31。Capture Core 与 Profile GORM Adapter 已完成并通过 legacy/GORM 双实现真实 PostgreSQL 门禁；生产 Composition 仍保持 legacy。父任务明确规定模块 child 在 TODO 9 后归档，而 Worker/API 最终组合、发布链、生产组合 deadlock/serialization 与 legacy 删除由 Final 独占，因此这些 Final 门禁不再误阻断本 child。

## 实施范围

- `GORMRepository` 从唯一 `platformpostgres.Pool` 获取 GORM root/UoW；Create 与 URL materialization 通过 Workspace `ScopedSourceWriter` 加入同一 `foundation.TransactionScope`。
- Create/Get/List/Retry、Outbox lease 和全部 Processing checkpoint 保留 Workspace scope、tuple keyset、DB time、锁序、CAS、terminal replay 与 strict receipt。
- `GORMProfileRepository` 使用 Agent `ScopedModelRunFinalizer`；Profile Revision/Evidence、Profile/Attempt 与 Agent Model Run 在同一 scope 内原子终结。
- Profile 读取使用三次有界 set query 与 `pq.Array`；source loader 在 SQL 阶段执行 500 Chunk/128 KiB fail-closed。
- legacy Repository/ProfileRepository 与 `cmd/**` 生产构造均保留；没有 selector、双写、fallback、second pool 或 Schema 自动管理。

## 真实 PostgreSQL 证据

现有 integration factory 已原位参数化为 `legacy`/`gorm` 两个子用例。每个子用例由 `testdb.Require` 创建独立已迁移数据库和唯一平台 Pool；legacy 使用该 Pool 的 `DB()`，GORM 使用同一 Pool 的 Workspace、Capture、Agent 与 Profile scoped Repository。

已通过的 Capture Core 场景：

- 8 worker 并发 Create、单份 Source/Artifact/Version/Capture/Outbox/receipt、exact replay 与冲突。
- Retry exact replay、跨 Workflow attempt number、FailAttempt terminal 状态。
- Outbox 双 worker claim/reclaim、过期 lease、stale owner/version 拒绝、reschedule、poison、manual recovery 与 terminal 不再领取。
- URL materialization 在 Workspace 写入后制造 PK 冲突，验证 Artifact/Capture/Attempt 全回滚；成功路径验证绑定、CAS 与 Refresh stage。
- cancel/custom cause/deadline、损坏 Capture 行整页 fail-closed、连接归还，以及 Capture keyset/Outbox candidate 目标索引计划。

已通过的 Profile 场景：

- capability unavailable、prepare/bind/complete/fail/retry 和 exact replay。
- READY/STALE rebuild、旧 Revision 保留、Evidence 闭包、Agent Model Run 原子终结与批量 `GetProfiles`。
- 过大 Chunk 在 SQL 阶段 fail-closed，Index activation/Profile completion 交错后得到 STALE，未发生锁序反转。
- cancel/custom cause/deadline、连接归还，以及 Profile、Revision、active Attempt 目标索引计划。
- Composite Source Version FK 跨 Workspace 写入返回真实 SQLSTATE `23503`；Foundation UoW 门禁覆盖 `23505`、rollback、stale scope、cancel cause 与 commit acknowledgement loss 后 durable exact replay。

## 已通过命令

- `go test -mod=vendor ./internal/capture/... -count=1 -timeout 60s`
- `go test -race -mod=vendor ./internal/capture/... -count=1 -timeout 60s`
- `go vet -mod=vendor ./internal/capture/... ./internal/workspace/application ./internal/agent/application ./internal/platform/postgres`
- `go test -mod=vendor -tags=integration -run '^$' ./internal/platform/migration ./cmd/worker -count=1 -timeout 60s`
- `go test -mod=vendor -run '^$' ./cmd/api ./cmd/worker ./internal/workspace/... ./internal/agent/... -count=1 -timeout 60s`
- Capture Core/Profile 上述场景的普通模式与 `-race` 模式均逐项通过；多个 Testcontainers 聚合命令仅因初始化总时长超过 60 秒而拆分，拆分后无断言失败。
- `go list -mod=vendor ./internal/capture/... ./internal/workspace/... ./internal/agent/... ./internal/platform/postgres ./cmd/api ./cmd/worker`
- `go mod verify`
- `python3 .trellis/scripts/task.py validate .trellis/tasks/08-19-gorm-capture-migration`
- 受影响 Go 文件 `gofmt -d` 与 `git diff --check`

`go mod tidy -diff` 仅审查且未应用；输出仍是本任务前已有的 `go.sum` 规范化漂移。

## Review 结果

- Go Review：未发现遗留 P0/P1/P2。SQLSTATE/未知错误保留底层 `errors.Is/As` 链，nil/typed-nil/context、Raw Row/Rows、scanner 与资源关闭均符合约束。
- SQL Review：外部值全部参数化；唯一动态 SQL 是 `FailAttempt` 从封闭枚举选择固定列片段。数组使用 `pq.Array`，JSONB 使用显式 `driver.Valuer`/cast；Workspace predicate、锁序、CAS、DB time、`SKIP LOCKED` 与目标索引符合 legacy/Schema。
- Trellis Check：Profile 前置已满足，staged 实现和 TODO 9 子模块门禁均有证据；生产仍为 legacy，未越过 Final Composition 边界。
- 静态扫描无 AutoMigrate/Migrator、Preload/Association、hook、`gorm.Model`、soft delete、自动时间、`gorm.Open`、`sql.Open` 或 `context.Background`。

## Final 交接

- Final 切换时按 `final-handoff.md` 参数化/复跑 Worker Capture Outbox -> Workflow/River、API/Worker Composition 和发布链门禁。
- 在最终生产组合上复跑 deadlock/serialization 门禁；该复跑用于生产切换，不再阻断已完成的模块级 TODO 9 与 child 归档。
- 回滚保持单点：Final 恢复 legacy Composition；禁止双写或 fallback。staged 回滚可删除新增 `gorm_*.go`，不会改变现有 Schema 或生产行为。
