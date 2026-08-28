# TODO3 设计：Goose → Atlas 全量迁移

## 1. 架构与边界

运行时拓扑（Q1 决策，方案 A）：迁移仍在 `zhixu-migrate` Go 二进制内 in-process
执行，引擎从 Goose Provider 换成 Atlas `sql/migrate.Executor`。保留不变量：

- `cmd/migrate` 入口、`config.LoadMigration` 配置路径、退出码语义不变。
- 单一 advisory lock（`zhixu:migrate`，池外专用 session，`pg_try_advisory_lock`
  轮询）覆盖 项目迁移 → River Up → River Validate 全序列，顺序不变。
- Compose `migrate` 服务（`deploy/compose.bootstrap.yml`）与 Dockerfile 三二进制
  拓扑不变；CI `go run ./cmd/migrate` 不变。
- testdb fixture `MigrationFunc` 类型签名不变，默认实现换为 Atlas。
- River 自带 migrator 与其 `workflow` Schema history 不变。

Atlas CLI 只出现在开发期与 CI：`migrate diff`（生成新迁移）、`migrate hash`、
`migrate lint`、`migrate validate`、schema 漂移 dry-run。CLI 走官方容器镜像
（`ATLAS_IMAGE`，CI 固定 digest，沿用现有 Makefile 模式）。

Atlas Go 依赖：`ariga.io/atlas`（仅 `sql/migrate`、`sql/postgres` 等执行所需
子包），go.mod 精确版本锁定 + vendor，license 在引入时核验并写入新 ADR
（遵循 ADR-0019；Atlas 为路线图既定选型，本 ADR 记录候选比较与执行方式取舍）。

## 2. 迁移目录与文件契约

- 新目录 `atlas/migrations/`：92 个迁移 1:1 转换，保留原文件名
  （`00001_extensions.sql` … `00092_*.sql`）。Atlas version 取文件名首个 `_`
  前缀，零填充数字按字典序排序正确；后续新迁移用 Atlas 默认时间戳版本
  （字典序天然排在 `00092` 之后），不强制定制格式。
- `atlas/migrations/atlas.sum`：由 `atlas migrate hash` 生成的哈希链完整性文件，
  纳入嵌入与 CI 校验。
- 转换规则（对 92 个文件机械执行，可用一次性工具完成）：
  - 删除 `-- +goose Up`、`-- +goose StatementBegin/End` 行；Atlas 的 PostgreSQL
    scanner 原生识别 dollar-quoted body，`legacyfs.go` 的注入层随之废除。
  - `-- +goose NO TRANSACTION`（00031、00051）转换为文件级
    `-- atlas:txmode none` 指令。
  - Down 段整体删除（Q2 决策 a）；文件内容即前向语句序列。
- 嵌入：`atlas/embed.go` 新建 `package atlas`，`//go:embed migrations` 暴露
  `embed.FS`；运行时启动时载入 `migrate.MemDir`（或等价只读 Dir 适配），
  atlas.sum 一并载入用于完整性校验。旧 `migrations/` 目录在清理阶段删除。
- `atlas/schema.sql` 保留为声明式期望态：开发期 `migrate diff` 的 src 与
  CI 漂移门禁基准；`atlas.hcl` env local 的 migration dir 指向新目录。

## 3. 运行时执行流程（新 Runner.Up）

1. 打开池外锁连接并获取 advisory lock（现有代码原样保留）。
2. **状态识别与接管（adoption）**：
   - `atlas_schema_revisions` 已存在 → 跳过接管。
   - 否则 `public.goose_db_version` 存在且有已应用记录 → 读取已应用 version 集合
     V；对 Eino 冲突形态（78/79 legacy 指纹 + 83/84 已应用 + 80–82 缺失）先执行
     现有指纹校验（collision_bridge 的纯 SQL 检查全部保留），通过后 V 视为
     1..79 ∪ 83..92；用 Atlas RevisionReadWriter 将 V 中每个版本写为已应用
     revision（哈希来自迁移文件本身，与 atlas.sum 一致）；executor 随后按序补跑
     pending（含 80–82 与任何更新文件），天然替代 Goose `AllowOutOfOrder`。
   - 否则 `core.schema_meta` 完整匹配旧 shell-runner 事实 → 写入版本 1..10 的
     已应用 revision；不匹配维持 `WORKFLOW_LEGACY_MIGRATION_MISMATCH` 拒绝语义。
   - 否则（全新库）→ 直接执行。
3. `Executor.Execute`（或 `ExecuteN(ctx, 0)`）应用全部 pending 文件；默认每文件
   单事务，`txmode none` 文件除外。
4. River Up → River Validate（现有代码不变）。
5. 新增迁移 `00092_drop_goose_db_version.sql`：
   `DROP TABLE IF EXISTS public.goose_db_version;`。存量库经接管后执行它收敛；
   全新库执行为 no-op；保证新旧环境终态 Schema 一致（R2）。

启动期只做 revision/哈希完整性校验（O(文件数)）；完整 schema 漂移 diff 作为
CI/运维门禁而非启动门禁（避免每次启动全量 introspection 的时延与权限要求）。

## 4. 失败与回滚模型（Q2 决策 a）

- 不提供文件式 Down。每文件事务边界 + 失败 revision 记录 + 幂等续跑为第一
  恢复机制；`txmode none` 文件沿用现有"先 DROP IF EXISTS 再重建"的可重入写法。
- 生产回滚路径 = fix-forward（新增前向迁移）+ 备份恢复演练（路线图收口项）。
- 删除 guarded-down 集成测试与全部 Down 段；新 ADR-0029 明确记录该取舍，
  ADR-0015 标注相关段落被 supersede。

## 5. 等价性验证设计（Goose 移除前必须完成）

- 双跑等价测试（integration + testcontainers）：同一空库镜像分别经
  旧 Goose runner 与新 Atlas runner 全量迁移，随后用 Atlas CLI
  `schema diff`（或 `migrate diff` dry-run 对 `atlas/schema.sql`）断言差异为空。
  该测试在 Goose 删除后改写为"Atlas 全新库 vs `atlas/schema.sql` 漂移为空"。
- 接管矩阵测试：用旧 runner 构造四类库态（全量 Goose 92、部分 Goose 历史、
  shell-runner legacy、Eino 冲突形态），Atlas runner 接管后断言终态与全新库
  等价、`goose_db_version` 已删除、重复执行幂等。
- 现有约 60 个迁移集成测试以"终态 Schema 事实"为主，引擎替换后应大体原样
  通过；Goose API 专用测试（legacyfs、guarded-down、provider 行为）删除或改写。

## 6. CI / 构建 / 文档接入

- Makefile：`migrate` 目标不变；新增 `atlas-migrate-diff`（开发期生成）、
  `atlas-migrate-hash`、`atlas-migrate-lint`；保留 `atlas-schema-inspect`/
  `atlas-schema-drift`。
- CI：新增哈希完整性（hash 后工作树无 diff）、`migrate lint`（dev-url 指向
  pgvector/pgvector:pg16）、`migrate validate` 与空库漂移门禁。
- 文档：`atlas/README.md` 重写为正式 runtime contract + 新迁移编写流程（R6）；
  `docs/operations.md` 迁移段落与 295 行附近的序列描述更新；
  `docs/architecture/system-design.md` 的 Goose 表述更新；新增 ADR-0029，
  ADR-0015 标注重叠决策被取代。
- 规范同步（Phase 3.3）：`.trellis/spec/backend/database-guidelines.md` 的
  Goose 条目改写为 Atlas 契约。

## 7. 关键取舍

- 接管用"Goose 已应用集合 → Atlas revision 直写"而非重放：避免对存量库重放
  DDL 的风险；代价是接管代码需精确映射版本与哈希（用 Atlas 官方 rrw 写入，
  不手写 SQL）。
- 不在启动期做全量漂移 diff：启动时延与权限最小化；漂移门禁放 CI/运维。
- 保留 `00001`–`00092` 原文件名：版本映射透明、接管逻辑是集合映射而非换算；
  新文件用 Atlas 默认时间戳版本，与历史文件排序兼容。
- 转换工具为一次性脚手架（`cmd/goose2atlas`），等价性证据齐备后与 Goose
  代码同批删除，避免留下双轨。

## 8. 风险与缓解

- Atlas scanner 对 00001–00010 dollar-quoted 复杂 body 的解析：spike 首批验证；
  兜底为逐文件 `-- atlas:delimiter` 指令。
- Executor 自带锁与外层 advisory lock 的交互：spike 验证可关闭/共存，
  确保不死锁（外层锁已是唯一事实锁）。
- CONCURRENTLY 文件在 `txmode none` 下的部分失败：沿用 DROP IF EXISTS 可重入
  写法，并在接管矩阵中注入失败续跑用例。
- Atlas 版本与 Go 1.25.4 兼容性、license：引入时核验并记录 ADR-0029；
  版本精确锁定，不走 latest。
- 与 TODO 10 并行窗口：本任务期间新增 Schema 迁移需先告知本任务 owner，
  转换与等价证明以切替时点重新生成为准（防漂移）。
