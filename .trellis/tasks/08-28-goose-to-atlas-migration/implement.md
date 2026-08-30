# TODO3 执行计划：Goose → Atlas 全量迁移

执行原则：每一步保持工作树可编译、可回滚；Goose 运行时与转换工具在等价性
证据齐备前不删除。实现已完成，以下勾选项同时作为可复核的交付记录；子 agent
派发上下文清单见 implement.jsonl / check.jsonl。

## Step 0 · Spike（技术未知项清零）

- [x] 0.1 选定并锁定 `ariga.io/atlas v1.2.2`（兼容 Go 1.25.4），核验 Apache-2.0 license
      与 vendor 体积；记录候选比较结论供 ADR-0029 引用。
- [x] 0.2 验证 embed.FS → `migrate.MemDir`/只读 Dir 适配 + atlas.sum 完整性校验
      在 `NewExecutor` 路径下生效。
- [x] 0.3 验证 00001–00010 的 dollar-quoted body 无需注解即可被 Atlas scanner
      正确切分；必要时确定 `-- atlas:delimiter` 兜底。
- [x] 0.4 验证 `-- atlas:txmode none` 对 00031/00051 的 CONCURRENTLY 语句生效。
- [x] 0.5 验证 Executor 内部锁与外层 advisory lock 可共存或可关闭，无死锁。
- [x] 0.6 验证 RevisionReadWriter 直写已应用 revision 的 API 形态（版本、描述、
      哈希字段与 atlas.sum 一致）。
- 验证：spike 测试在 testcontainers PG16+pgvector 上通过；PG18 漂移门禁也已通过，
  结论追加到 design.md。

## Step 1 · 迁移目录转换

- [x] 1.1 编写并运行一次性转换工具 `cmd/goose2atlas`：剥离 Goose 注解、删除 Down
      段、NO TRANSACTION 转为 `-- atlas:txmode none`，输出 `atlas/migrations/*.sql`；
      工具在清理阶段删除。
- [x] 1.2 生成 `atlas.sum`（容器化 `atlas migrate hash`）；`atlas migrate validate`
      通过。
- [x] 1.3 新建 `atlas/embed.go`（`package atlas`，嵌入 migrations 目录）；
      更新 `atlas.hcl` migration dir。
- 验证：`go build ./...`；转换产物抽查 00002/00031/00051/00092 一致。

## Step 2 · Atlas 运行时 Runner

- [x] 2.1 `internal/platform/migration` 内实现 Atlas 执行路径：Executor 构造、
      外层 advisory lock 复用、River Up/Validate 顺序不变。
- [x] 2.2 实现 adoption：goose_db_version 集合映射、Eino 冲突指纹校验
      （移植 collision_bridge 纯 SQL 检查）、shell-runner legacy 接管，无法识别时
      以稳定错误码拒绝。
- [x] 2.3 新增 `atlas/migrations/00092_drop_goose_db_version.sql`
      （DROP TABLE IF EXISTS，幂等）并重跑 hash。
- [x] 2.4 `cmd/migrate` 切换到新 runner；以 `NewAtlasRunner` 接收 Atlas MemDir，
      嵌入目录由 `NewAtlasEmbeddedRunner` 构造。
- 验证：`go vet ./internal/platform/migration ./cmd/migrate`；`go run ./cmd/migrate`
      对 PG18 一次性库通过。

## Step 3 · 等价性与接管证据（Goose 删除前的硬门禁）

- [x] 3.1 双跑等价集成测试：空库 Goose 全量 vs Atlas 全量，Atlas CLI
      schema diff 为空；两者对 `atlas/schema.sql` 漂移均为空。
- [x] 3.2 接管矩阵集成测试：四类存量库态接管成功且终态等价；未知库态拒绝；
      重复执行幂等；txmode none 文件失败后续跑成功。
- [x] 3.3 testdb fixture 默认 `MigrationFunc` 切为 Atlas runner（类型签名不变），
      `make testcontainers-integration` 通过。
- 验证：Atlas adoption/spike 集成矩阵与 `make testcontainers-integration` 全绿；
      迁移包其余依赖外部 `ZHIXU_TEST_DATABASE_URL` 的历史业务测试由各自领域门禁负责。

## Step 4 · 切换与清理（同批，避免双轨残留）

- [x] 4.1 删除 `migrations/` 目录、`migrations/embed.go`、`legacyfs.go`、
      Goose Provider 代码、`cmd/goose2atlas`；改写或删除 Goose API 专用测试
      （legacyfs_test、guarded-down、provider 行为测试），保留以终态 Schema 为
      断言的迁移集成测试。
- [x] 4.2 `go.mod` 与 vendor 移除 `pressly/goose`；全仓 `rg -i goose` 仅存历史
      文档与 ADR 表述。
- [x] 4.3 Makefile 与 CI 增加 hash、lint、validate、漂移门禁；`ATLAS_IMAGE` 在
      CI 固定 digest。
- [x] 4.4 文档：atlas/README.md 重写、operations.md、system-design.md、
      ADR-0029 新增与 ADR-0015 supersede 标注。
- 验证：`go build ./...`、短测试集、`make atlas-schema-drift` dry-run 为空、
      CI 迁移步骤绿。

## Step 5 · 收口

- [x] 5.1 Compose smoke：`compose-bootstrap` 迁移门禁与 API/Worker 启动顺序
      不回归（相关 compose 合约目标）。
- [x] 5.2 Review 门禁：go-review 与 SQL 审查（00092 与转换产物）。
- [x] 5.3 Phase 3.3 规范更新：database-guidelines.md 的 Goose 条目改为 Atlas。
- [x] 5.4 roadmap 4.1 标记交付（随任务归档提交）。

## 风险文件与回滚点

- 高风险：`internal/platform/migration/runner.go`、`collision_bridge.go`、
  `internal/platform/testdb/fixture.go`、`.github/workflows/ci.yml`、`atlas.hcl`。
- 回滚点：Step 3 完成前任何一步都可整体放弃（Goose 路径未动）；Step 4 为
  独立提交，回滚即 revert 该提交链；发布后存量库回滚为 fix-forward（无 Down）。
- 并行约束：Step 1–4 期间 `migrations/` 冻结新增；TODO 10 子任务如需新迁移，
  先在 `atlas/migrations/` 追加时间戳版本文件并同步 `atlas/schema.sql`。

## 常用验证命令

- `go build ./...` 与 `go vet ./internal/platform/migration ./cmd/migrate`
- `go test -count=1 ./cmd/migrate ./internal/platform/migration/...`
- `go test -count=1 -timeout=5m -tags='integration testcontainers' ./internal/platform/testdb ./internal/platform/migration`
- `make migrate`
- `make atlas-schema-inspect atlas-schema-drift`（需 ZHIXU_DATABASE_URL 与 ZHIXU_ATLAS_DEV_URL）
- `git diff --check`
