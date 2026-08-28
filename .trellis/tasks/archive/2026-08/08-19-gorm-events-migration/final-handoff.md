# Events Final Composition Handoff

## Staged Constructor

Final 从既有 `*platformpostgres.Pool` 构造 `postgres.NewGORMStore(platform)`，
与 legacy `NewStore(pool.DB())` 共用同一物理 Pool；不得引入裸 DSN、第二
连接池、selector、双写或 fallback。

## Current Boundary

生产 Composition 仍构造 legacy Store。本 child 只通过共享 Testcontainers
fixture 验证 staged GORM Store，未改生产选择、migration 或 legacy 构造。

## 实库证据（2026-08-28）

- 功能、事务、锁、失败与 SSE 实库门禁此前已在共享 Pool 上通过（见
  implement.md 与 research/static-validation.md）。
- 新增 `TestEventReplayPlansAtVolumeUseDefaultPlanner`：102,000 行目标
  数据量、冷 Workspace 布局、VACUUM ANALYZE 后默认 planner 下
  watermark/earliest/replay 均走 idx_ops_server_event_workspace_seq，
  无 Seq Scan，replay 单页 ≤ MaxReplayPageSize(100)。
- 修复 `eventAppendRequest` 硬编码 2026-08-27 occurred_at 的定时炸弹
  （24h 保留窗口由 CURRENT_TIMESTAMP 判定，硬编码日期随墙钟过期）。
- 全量 `-race -tags "integration testcontainers"` 套件通过（adapter 73s，
  http 13s，domain 1.4s）。

## 验证盲区

commit-time connection loss/response loss 继承 Foundation 盲区记录，模块
内不可稳定注入，未声明实测。

## Final Work

- Final composition child 将 `cmd/api`/`cmd/worker` 的 Events Store 构造切换
  为共享 Pool 派生的 GORM Store。
- 28 个模块 child 与 TODO 3 全部通过 Final 门禁后删除 legacy pgx Store 与
  直接 pgx 依赖。
- 保留现有 integration fixture 作为生产路径回归套件。
