---
status: accepted
---

# 锁定 River v0.40.0 与 Goose v3.27.0 作为 Workflow Runtime 基础

## Context

项目需要在同一 PostgreSQL 事务中创建 Workflow 领域事实并投递可运行 Node，同时用正式迁移历史替换早期 `awk + psql` 的 Up-only 入口。当前 Go/Docker 工具链固定为 Go 1.25.4；Goose v3.27.2 要求 Go 1.25.7，不能在本任务中隐式升级工具链。历史 `00001`–`00010` 包含未标注 `StatementBegin/StatementEnd` 的 PL/pgSQL dollar-quoted body，直接交给 Goose v3.27.0 会在 `00002` 失败，但已应用迁移不得重写。

## Decision

- 精确锁定 River、riverpgxv5 `v0.40.0` 和 Goose `v3.27.0`，不使用 `latest`、master 或伪版本。
- 项目迁移由 `cmd/migrate` 执行，固定顺序为：项目 Goose Up → River Up → River Validate。River Migrator 与 Client 均显式使用 `workflow` Schema。
- `migrations/embed.go` 是项目 SQL 的唯一嵌入来源。兼容层仅在内存中为 `00001`–`00010` 的 dollar-quoted direction 注入 Goose statement boundary，不修改仓库 SQL；`00011` 起必须在源迁移中原生写 annotation。
- 对没有 Goose history、但完整 `core.schema_meta` 事实证明已执行 `00001`–`00010` 的旧数据库，一次性建立 Goose baseline；任何缺失或不一致事实都以 `WORKFLOW_LEGACY_MIGRATION_MISMATCH` 拒绝，不能猜测接管。
- 单一 PostgreSQL advisory lock 覆盖项目迁移、River 迁移和 Validate 全过程；锁由池外专用 session 持有，避免合法的单连接应用池阻塞 Goose/River。生产入口只执行 Up；Down 仅用于 disposable 数据库升级门禁。
- River 只负责 Job 投递和 typed Worker 获取，PostgreSQL Workflow Run/Node/Outbox 仍是业务事实源。Job Kind/Args 是持久契约，Domain/Application 不依赖 River 或 pgx 类型。

## Version And License

- River `v0.40.0`：MPL-2.0。保留 vendor 中上游 License 和未修改的上游源文件；项目自有领域代码不因此转为 MPL。
- Goose `v3.27.0`：MIT。
- 项目自身 License 尚未确定，这是源码包、镜像和第三方分发前必须关闭的发布风险。

## Upgrade Gate

任何 River/Goose 升级必须先验证：目标 Go/Docker 版本、空库 Up、重复 Up、旧库 baseline、River Validate、one-step Down→Up、事务 InsertTx rollback、Job Kind/Args 兼容、typed Worker smoke 和 vendor license。Goose v3.27.1+ 只有在项目工具链先升级并通过上述矩阵后才能采用。

## Consequences

- 迁移失败会阻止 API/Worker 进入依赖新 Runtime 的路径，不允许自制 pending-node polling 绕过。
- 项目与 River 维护独立 migration history，不宣称跨两套 migration 的全局原子性；失败后依赖各自幂等迁移续跑。
- 若未来退出 River，Workflow 领域模型、Registry 和 PostgreSQL 状态可以保留，但必须提供兼容 Job drain/替换方案。
