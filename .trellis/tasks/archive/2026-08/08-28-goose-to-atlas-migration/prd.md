# TODO3 全量从 Goose 迁移到 Atlas

## Goal

把知序的 Schema 定义、版本迁移与漂移检查统一到 Atlas，消除 Goose/Atlas 双轨，
使 Atlas 成为唯一 Schema 事实源；应用、Docker/CI、测试与运维不再依赖 Goose，
为 TODO 10（GORM 数据访问迁移）最终切换解除前置。

路线图定义见 `docs/roadmap.md` 4.1（P0，初估 5–8 人天）。

## 启动时 Confirmed Facts（仓库证据）

- 当前迁移运行时：`cmd/migrate` 加载配置后调用 `internal/platform/migration.Runner.Up`，
  顺序为 项目 Goose Up → River Up → River Validate，全程由池外专用 session 持有
  单一 advisory lock（`zhixu:migrate`）。依据 ADR-0015 与 `runner.go`。
- 迁移源：`migrations/` 下 91 个 Goose SQL 文件，经 `migrations/embed.go` 的
  `embed.FS` 嵌入；history 表为 `public.goose_db_version`。
- 兼容层 1：`legacyfs.go` 在内存中为 `00001`–`00010` 的 dollar-quoted body 注入
  Goose StatementBegin/End，不改写仓库 SQL。
- 兼容层 2：`collision_bridge.go` 以 schema 指纹接管"旧 Eino 78/79 已应用、
  80–82 缺失、83/84 被收养"的冲突历史（约 505 行）。
- 兼容层 3：`adoptLegacyProjectHistory` 对无 Goose history 但 `core.schema_meta`
  完整匹配的旧 shell-runner 数据库一次性 baseline 接管 00001–00010，不匹配则以
  `WORKFLOW_LEGACY_MIGRATION_MISMATCH` 拒绝。
- 部署拓扑：`deploy/Dockerfile` 构建 `zhixu-migrate` 二进制，Compose 中 Migrate
  成功是 API/Worker 的启动门禁；CI（`.github/workflows/ci.yml:91`）执行
  `go run ./cmd/migrate`；Makefile `migrate` 目标同一入口。
- 测试侧：`internal/platform/testdb/fixture.go` 已把迁移抽象为 `MigrationFunc`
  回调，默认实现是 Goose runner，注释明确"until Atlas becomes the sole migration
  callback"；`internal/platform/migration/` 有约 60 个迁移相关集成测试。
- Atlas 侧已存在：`atlas/schema.sql`（从 Goose 迁移完成的空 PG16 库派生的声明式
  基线）、`atlas.hcl`（env local，migration dir 指向尚不存在的
  `file://atlas/migrations`）、Makefile `atlas-schema-inspect`/`atlas-schema-drift`
  （dry-run，官方容器镜像，CI 需固定 digest）。
- `atlas/README.md` 明确：TODO3 负责提供 runtime migration contract，之后 TODO10
  在不改 fixture API 的前提下切换测试库到 Atlas 迁移文件。
- `go.mod` 当前无任何 `ariga.io/atlas` 依赖；Goose 锁定 v3.27.0（ADR-0015）。
- Atlas Go 模块（`ariga.io/atlas/sql/migrate`）提供 in-process `Executor`
  （`NewExecutor`/`Pending`/`ExecuteN`），可直接从 `embed.FS` 形式的迁移目录执行，
  支持 baseline revision，无需 CLI 常驻。
- River 迁移由 River 自带 migrator 管理，与 Goose 无关；TODO3 不改变 River 职责。
- TODO 10 父任务 PRD 约束：TODO 3 完成前不执行最终全仓切换；最终态禁止 GORM
  `AutoMigrate`/`Migrator` 修改 Schema。

## Requirements

- R1：Atlas 版本化迁移目录成为项目 Schema 变更的唯一写入路径；现有 91 个 Goose
  迁移的结果在新空库上与 Atlas 转换结果逐对象一致，并追加 Atlas 专属 00092 清理
  遗留 history。
- R2：已有数据库（含 Goose history 正常库、旧 shell-runner 库、Eino 冲突历史库）
  能被安全接管：不重放已应用变更、不破坏数据，无法识别时明确拒绝而非猜测。
- R3：迁移入口拓扑保持"Migrate 成功才启动 API/Worker"的门禁语义；River
  Up/Validate 顺序与单一 advisory lock 语义不回归。
- R4：CI/Compose/Makefile/运维文档统一切换到 Atlas 入口，并提供漂移检查门禁。
- R5：完成后从 `go.mod`、vendor、运行时代码与测试默认路径移除 Goose 依赖。
- R6：新迁移编写流程（开发者如何新增迁移）有明确文档与校验工具。

## Acceptance Criteria

- [x] AC1：空库经 Atlas 入口完成全量迁移后，与声明式 `atlas/schema.sql`
      漂移检查为空；`make atlas-schema-drift` dry-run 通过。
- [x] AC2：模拟存量库（Goose history 正常/部分、shell-runner 旧库、Eino 冲突
      历史库）接管成功且终态 Schema 一致；无法识别的库以稳定错误码拒绝。
- [x] AC3：重复执行迁移幂等；迁移失败后可恢复续跑；Compose `up --wait` 门禁
      与 Worker readiness 语义不回归。
- [x] AC4：River Up/Validate 仍在项目迁移之后、同一锁范围内执行。
- [x] AC5：CI 中迁移 lint/漂移/哈希完整性检查通过；`go.mod`/vendor 中不再有
      `pressly/goose`。
- [x] AC6：testdb fixture 默认迁移回调切换为 Atlas，`MigrationFunc` API 不变，
      `internal/platform/migration` 相关集成测试改写后通过。

## Out of Scope

- TODO 10 各业务模块的 GORM 迁移本体（由 `08-18-gorm-data-access-migration`
  子任务负责）。
- River 自身迁移机制改造。
- Schema 内容本身的重构或优化（本任务只换迁移机制，不改 Schema 语义）。

## Key Decisions（2026-08-28 用户确认）

- D1（运行时拓扑）：Go 二进制内 in-process 执行 Atlas 迁移
  （`ariga.io/atlas/sql/migrate.Executor` + embed.FS 迁移目录），保留
  cmd/migrate/zhixu-migrate、单一 advisory lock、River Up/Validate 顺序、
  compose 启动门禁、testdb `MigrationFunc` 回调；Atlas CLI 仅用于开发期
  diff/lint/hash 与 CI 校验。否决方案：Atlas CLI 容器作为运行时执行者
  （破坏 fixture 回调与 compose 门禁拓扑，共锁语义难保留）。
- D2（Down 处置）：放弃文件式 Down。91 个 Down 段与 guarded-down 守卫
  （00059/00060，有业务数据时以 55000 拒绝降级）随 Goose 一并移除；恢复路径
  为每文件事务边界 + 幂等续跑 + fix-forward + 备份恢复演练；取舍由新
  ADR-0029 记录。否决方案：CLI 计算的 Down 做 disposable 门禁（守卫语义丢失，
  且需多维护一条 CI 链路）。

无剩余阻塞问题；技术未知项（Atlas scanner 对 dollar-quote 的解析、Executor 锁
交互、revision 直写 API、版本与 license）已列入 implement.md Step 0 spike。
