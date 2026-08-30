---
status: accepted
---

# Atlas 成为唯一 Schema 迁移事实源并移除 Goose

## Context

- roadmap TODO 3（4.1）要求应用、Docker/CI、测试与运维不再依赖 Goose，为 TODO 10（GORM Persistence Model 切换）解除前置；GORM `AutoMigrate`/`Migrator` 在任何入口均被禁止，Persistence Model 只能映射 Atlas 已批准结构。
- ADR-0015 锁定的 Goose v3.27.0 需要内存注解兼容层（为 `00001`–`00010` 的 dollar-quoted body 注入 StatementBegin/End）和 shell-runner 旧库 baseline 接管，长期维护成本高；Goose 也不提供声明式期望态与 drift 检测能力，`atlas/schema.sql` 基线与 Goose 执行器长期是双事实源。
- Go/Docker 工具链锁定 Go 1.25.4（`deploy/Dockerfile` 使用 `golang:1.25.4-bookworm`）。`ariga.io/atlas` 自 v1.3.0 起要求 go 1.26.4，当前不可采用，升级必须先升工具链。

## Decision

- 精确锁定 `ariga.io/atlas v1.2.2`（Apache-2.0，vendor 保留上游 LICENSE）。Atlas CLI 只用于开发期与 CI 的 hash/lint/validate/drift 门禁（容器化 `ATLAS_IMAGE`，CI 固定不可变 digest），不进入应用运行时。
- 运行时拓扑（方案 A）：`cmd/migrate`（镜像内 `zhixu-migrate`）以 in-process 方式使用 `ariga.io/atlas/sql/migrate.Executor` + `atlas.MigrationDir()`（embed.FS）执行项目迁移，随后固定执行 River Up → River Validate。单一 PostgreSQL advisory lock（`zhixu:migrate`）覆盖全过程，Compose migrate 门禁与 testdb `MigrationFunc` 回调语义不变；对外行为与 Goose 时代一致。
- `atlas/migrations/` 是唯一迁移事实源：91 个 Goose 迁移 1:1 转换为 Atlas 版本化文件（去除 `-- +goose` 注解，`NO TRANSACTION` 转为 `-- atlas:txmode none`），并追加 `00092_drop_goose_db_version.sql`（幂等 `DROP TABLE IF EXISTS`）清理遗留 history 表。`atlas.sum` 强制校验目录完整性。
- `atlas/schema.sql` 继续作为声明式期望态基线，由真实迁移终态导出；它包含项目 schema、Atlas revision 表、River schema 与 `00080` 建立的数据库级 ACL，不包含已清理的 Goose history。`make atlas-schema-drift` 对目标库只读，但会删除并重建同一 PostgreSQL 实例上显式指定的 disposable dev 数据库中的非系统 schema；集群角色的 LOGIN 状态仍由独立凭据流程管理，漂移重放不得修改。
- Atlas Community v1.2.2 的 `migrate lint` 需要 Pro 登录，且 Community schema parser 不能完整表示当前 PostgreSQL extension/function 集合。因此硬门禁由仓库契约 lint（文件名、唯一/有序版本、无 Goose/Down、`txmode none`）、Atlas `migrate hash/validate`、目标库与基线重放库的数据库间 Atlas diff，以及覆盖对象定义、owner、schema/table/column/function ACL 的 PostgreSQL catalog fingerprint 共同组成；可用 `ATLAS_TOKEN` 时另跑可选的 `atlas-migrate-lint-pro`，但不能以此替代硬门禁。
- Atlas CLI 与 PostgreSQL client 镜像在 Makefile/CI 固定为批准的不可变 digest；默认分别对应 Atlas v1.2.2 与带 pgvector 的 `psql` client。目标库/声明基线同实例门禁已在 PostgreSQL 16 与生产/CI 使用的 PostgreSQL 18 上验证；fingerprint 对 PG18 新增但语义重复的 `contype='n'` 使用列级 `attnotnull` 归一化比较。
- 移除文件式 Down：91 个 Down 段与 guarded-down 守卫一并删除。恢复路径为每文件事务 + 幂等续跑 + fix-forward + 备份恢复；不再维护生产 Down SQL，依赖 Down 的测试随 Goose 一并删除，幂等性由重复 Up 覆盖。
- 旧库接管（fail closed）：存在 `goose_db_version` history 时按版本集合直写 Atlas revision（哈希来自 `atlas.sum`），随后由 `00092` 清理；shell-runner 旧库沿用 `core.schema_meta` 九项事实校验；Eino `00078`/`00079` 与 `00083`/`00084` 的历史冲突沿用 schema 指纹校验，未版本化的等价应用直接执行语句接管；空 history 表直接 DROP；Atlas revision 与 Goose history 并存时仅接受带 adoption lineage 且版本集合一致的可恢复中间态，其余双历史状态以 `MIGRATION_ADOPTION_CONFLICTING_HISTORY` 拒绝；任何未知版本以 `MIGRATION_ADOPTION_UNKNOWN_VERSION` 拒绝，不猜测接管。
- revision 表使用 Atlas 兼容布局 `atlas_schema_revisions.atlas_schema_revisions`，与 Atlas CLI 互通，便于开发期 `migrate status` 观察同一事实。

## Alternatives

- 保留 Goose 运行时 + 叠加 Atlas CLI 检查：迁移执行与声明式基线仍是双事实源，违反收敛目标，驳回。
- golang-migrate：无声明式 schema/drift 能力，维护信号弱于 Atlas，驳回。
- GORM `AutoMigrate` 作为迁移源：roadmap 明令禁止，Persistence Model 只能映射已批准结构，驳回。
- Atlas CLI/容器在运行时执行迁移：引入外部进程与镜像分发耦合，破坏单一 Go 二进制分发与现有 Compose 门禁形态，驳回。

## Upgrade Gate

任何 Atlas 升级必须先验证：目标 Go/Docker 工具链兼容（go directive 与 golang 镜像 tag）、Community/Pro CLI 能力变化、空库 Up、重复 Up 幂等、Goose history 接管矩阵（全量/部分/shell-runner legacy/Eino 冲突/空表/未知版本拒绝/幂等重跑）、新旧执行器双跑 realm diff 为空、`atlas.sum` 校验、`txmode none` 语句集合、数据库间 diff + catalog fingerprint、Compose migrate 门禁、testdb fixture 生命周期，以及 vendor license。

## Consequences

- 历史 Down 不可再执行；数据库回滚一律 Expand → Backfill → Contract 与 fix-forward，升级前备份成为唯一向后恢复手段。
- 新迁移只追加到 `atlas/migrations/`，同步更新 `atlas/schema.sql` 并重跑 hash；`migrations/` 目录与 `internal/platform/migration` 的 Goose 兼容层（`legacyfs.go`、guarded-down 守卫、转换工具）全部删除。
- ADR-0015 的 River 部分继续有效；其 Goose 相关决策由本 ADR 取代。
